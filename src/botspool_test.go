package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// useRemoteLasso makes every host answer the channel probe with path, as a
// host with a lasso that knows --spool would.
func useRemoteLasso(t *testing.T, path string) {
	t.Helper()
	prev := botRemoteLasso
	botRemoteLasso = func(string) (string, error) { return path, nil }
	botRemoteChannelCache = sync.Map{}
	t.Cleanup(func() { botRemoteLasso = prev; botRemoteChannelCache = sync.Map{} })
}

// testRemoteBot is a claude bot on host "exa" whose folder is a temp dir. The
// test backend's file ops are the local filesystem, standing in for SFTP.
func testRemoteBot(t *testing.T) (Backend, *botRecord, string) {
	t.Helper()
	b := useBotTestEnv(t)
	useRemoteLasso(t, "/home/dev/.local/bin/lasso")
	dir := t.TempDir()
	r := &botRecord{Name: "exa", Host: "exa", Dir: dir}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		t.Fatal(err)
	}
	rec, err := getBot("exa")
	if err != nil {
		t.Fatal(err)
	}
	return b, rec, dir
}

func eventStatus(t *testing.T, id int64) (status, claimedAt string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, claimed_at FROM bot_events WHERE id = ?`, id).Scan(&status, &claimedAt); err != nil {
		t.Fatal(err)
	}
	return
}

func inboxNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(filepath.Join(botSpoolDir(dir), "inbox"))
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func TestBotRemoteChannelOffer(t *testing.T) {
	useRemoteLasso(t, "/home/dev/.local/bin/lasso")
	r := &botRecord{Name: "exa", Host: "exa", channelToken: "tok"}
	if ok, why := botChannelOffered(r); !ok {
		t.Fatalf("not offered: %s", why)
	}
	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	_ = json.Unmarshal(botMCPJSON(r, "/home/dev/bots/exa", nil), &got)
	c := got.MCPServers[botChannelName]
	args, _ := json.Marshal(c["args"])
	if c["command"] != "/home/dev/.local/bin/lasso" || string(args) != `["channel","--bot","exa","--spool","/home/dev/bots/exa/.lasso/channel"]` {
		t.Fatalf("lasso-channel = %v", c)
	}
	// It holds no credential and names no address: nothing on the host dials lasso.
	if c["env"] != nil {
		t.Errorf("remote channel env = %v", c["env"])
	}
	if got.MCPServers["lasso"] != nil {
		t.Error("the lasso server was offered to a bot on another host")
	}
	if !strings.Contains(strings.Join(botClaudeArgv(r, "/home/dev/bots/exa"), " "), "server:"+botChannelName) {
		t.Error("the channel is not granted")
	}

	// An omp bot's jobs are bridge prompts on any host, no spool; a host
	// whose lasso cannot spool says why.
	if ok, _ := botChannelOffered(&botRecord{Name: "o", Host: "exa", Runtime: botRuntimeOmp}); !ok {
		t.Error("not offered to an omp bot on another host")
	}
	botRemoteLasso = func(host string) (string, error) {
		return "", &botTestErr{"lasso on " + host + " is too old to deliver jobs"}
	}
	botRemoteChannelCache = sync.Map{}
	if ok, why := botChannelOffered(r); ok || !strings.Contains(why, "too old") {
		t.Errorf("old lasso: offered=%v why=%q", ok, why)
	}
}

type botTestErr struct{ s string }

func (e *botTestErr) Error() string { return e.s }

func TestBotSpoolDelivery(t *testing.T) {
	b, rec, dir := testRemoteBot(t)
	j, err := createBotJob(rec, botJobInput{Name: ptr("sweep"), Message: ptr("do it")})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, _, err := enqueueBotEvent(j, "run", j.Message, "", now); err != nil {
		t.Fatal(err)
	}
	if err := botSpoolSync(b, rec, now); err != nil {
		t.Fatal(err)
	}
	names := inboxNames(t, dir)
	if len(names) != 1 {
		t.Fatalf("inbox = %v", names)
	}
	id, _ := botSpoolID(names[0])
	if st, _ := eventStatus(t, id); st != "claimed" {
		t.Fatalf("status = %s", st)
	}
	var e botEvent
	raw, _ := os.ReadFile(filepath.Join(botSpoolDir(dir), "inbox", names[0]))
	if err := json.Unmarshal(raw, &e); err != nil || e.ID != id || e.Content != "do it" || e.Job != "sweep" {
		t.Fatalf("event file = %s", raw)
	}

	// Waiting in the inbox long past the reclaim: still claimed, never written
	// twice, and its claim kept fresh.
	later := now.Add(10 * time.Minute)
	if err := botSpoolSync(b, rec, later); err != nil {
		t.Fatal(err)
	}
	if got := inboxNames(t, dir); len(got) != 1 {
		t.Fatalf("inbox after reclaim time = %v", got)
	}
	if st, at := eventStatus(t, id); st != "claimed" || at != nowStamp(later) {
		t.Fatalf("claim = %s %s", st, at)
	}

	// The channel moves it to done: lasso acks and clears it.
	done := filepath.Join(botSpoolDir(dir), "done")
	_ = os.MkdirAll(done, 0o700)
	if err := os.Rename(filepath.Join(botSpoolDir(dir), "inbox", names[0]), filepath.Join(done, names[0])); err != nil {
		t.Fatal(err)
	}
	if err := botSpoolSync(b, rec, later); err != nil {
		t.Fatal(err)
	}
	if st, _ := eventStatus(t, id); st != "delivered" {
		t.Fatalf("after done = %s", st)
	}
	if ents, _ := os.ReadDir(done); len(ents) != 0 {
		t.Errorf("done not cleared: %v", ents)
	}
}

func TestBotSpoolReclaimAndWithdraw(t *testing.T) {
	b, rec, dir := testRemoteBot(t)
	j, _ := createBotJob(rec, botJobInput{Name: ptr("sweep"), Message: ptr("do it")})
	now := time.Now()
	id, _, _ := enqueueBotEvent(j, "run", j.Message, "", now)
	if err := botSpoolSync(b, rec, now); err != nil {
		t.Fatal(err)
	}
	// The file is lost (a write that failed, a human tidying up). Within the
	// reclaim it is left alone; after it, written again.
	_ = os.Remove(filepath.Join(botSpoolDir(dir), "inbox", botSpoolFile(id)))
	if err := botSpoolSync(b, rec, now.Add(20*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := inboxNames(t, dir); len(got) != 0 {
		t.Fatalf("rewritten inside the reclaim: %v", got)
	}
	if err := botSpoolSync(b, rec, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := inboxNames(t, dir); len(got) != 1 || got[0] != botSpoolFile(id) {
		t.Fatalf("not rewritten after the reclaim: %v", got)
	}
	// Dropped while waiting (a day unpicked): its file is withdrawn, never
	// delivered late.
	if _, err := db.Exec(`UPDATE bot_events SET status = 'dropped' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := botSpoolSync(b, rec, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := inboxNames(t, dir); len(got) != 0 {
		t.Fatalf("withdrawn file still there: %v", got)
	}
}

func TestBotSpoolHeartbeat(t *testing.T) {
	b, rec, dir := testRemoteBot(t)
	seen := filepath.Join(botSpoolDir(dir), "seen")
	_ = os.MkdirAll(botSpoolDir(dir), 0o700)
	_ = os.WriteFile(seen, []byte("one"), 0o600)
	botSpoolBeats = sync.Map{}
	botChannelSeenAt = sync.Map{}
	if err := botSpoolSync(b, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The first reading is a baseline: a stale file says nothing.
	if st := botChannelState(rec, time.Now()); st["connected"] == true {
		t.Fatalf("connected on a baseline: %v", st)
	}
	_ = os.WriteFile(seen, []byte("two"), 0o600)
	if err := botSpoolSync(b, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	if st := botChannelState(rec, time.Now()); st["connected"] != true || st["available"] != true {
		t.Fatalf("channel state = %v", st)
	}
}

func TestBotChannelSpoolMode(t *testing.T) {
	prev := spoolPoll
	spoolPoll = 20 * time.Millisecond
	t.Cleanup(func() { spoolPoll = prev })
	spool := filepath.Join(t.TempDir(), "channel")
	inbox := filepath.Join(spool, "inbox")
	_ = os.MkdirAll(inbox, 0o700)
	ev, _ := json.Marshal(botEvent{ID: 7, Job: "sweep", Kind: "schedule", Content: "do it", Count: 2, FiredAt: "2026-10-12T19:00:00Z"})
	_ = os.WriteFile(filepath.Join(inbox, "7.json"), ev, 0o600)
	_ = os.WriteFile(filepath.Join(inbox, ".8.json.tmp"), []byte("half"), 0o600)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ch := &botChannelServer{bot: "exa", spool: spool, out: outW}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.serve(ctx, inR)
	lines := bufio.NewScanner(outR)
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n")
	if !lines.Scan() {
		t.Fatal("no initialize answer")
	}
	if strings.Contains(lines.Text(), "create_bot_job") || !strings.Contains(lines.Text(), "Bots view") {
		t.Errorf("remote instructions = %s", lines.Text())
	}
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	if !lines.Scan() {
		t.Fatal("no notification")
	}
	var note struct {
		Method string `json:"method"`
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(lines.Bytes(), &note)
	if note.Method != "notifications/claude/channel" || note.Params.Content != "do it" || note.Params.Meta["event_id"] != "7" || note.Params.Meta["count"] != "2" {
		t.Fatalf("notification = %s", lines.Bytes())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, errDone := os.Stat(filepath.Join(spool, "done", "7.json"))
		_, errSeen := os.Stat(filepath.Join(spool, "seen"))
		if errDone == nil && errSeen == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not moved to done (%v) or no heartbeat (%v)", errDone, errSeen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The half-written temp file is not an event.
	if _, err := os.Stat(filepath.Join(inbox, ".8.json.tmp")); err != nil {
		t.Errorf("temp file touched: %v", err)
	}
}

func TestBotWatchRunsOnTheBotsHost(t *testing.T) {
	_, rec, dir := testRemoteBot(t)
	type call struct {
		command, dir, bot, job string
		timeout                time.Duration
	}
	var got []call
	prev := botExecRemote
	botExecRemote = func(b Backend, command, dir, bot, job string, timeout time.Duration) botExecResult {
		got = append(got, call{command, dir, bot, job, timeout})
		return botExecResult{Stdout: "changed\n"}
	}
	t.Cleanup(func() { botExecRemote = prev })
	cmd, timeout := "./check", 30
	j, err := createBotJob(rec, botJobInput{Name: ptr("watch"), Message: ptr("Judge it."), Command: &cmd, Timeout: &timeout, Cron: ptr("*/10 * * * *")})
	if err != nil {
		t.Fatal(err)
	}
	o, err := runBotJobCommand(j, "schedule", time.Now())
	if err != nil || o.Result != "output" {
		t.Fatalf("outcome = %+v %v", o, err)
	}
	if len(got) != 1 || got[0].dir != dir || got[0].command != "./check" || got[0].bot != "exa" || got[0].job != "watch" || got[0].timeout != 30*time.Second {
		t.Fatalf("remote exec = %+v", got)
	}
}

// The host-side script, run here as the host would run it: in the folder,
// with a clean environment of the host's own values and the run's names.
func TestBotRemoteCommandScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("UI_AUTH", "user:secret")
	t.Setenv("LASSO_MCP_TOKEN", "tok")
	script := botRemoteCommandScript(`pwd; echo "$LASSO_BOT/$LASSO_JOB"; env | grep -c -e UI_AUTH -e LASSO_MCP_TOKEN; echo "$PATH"`, dir, "exa", "it's", 10*time.Second)
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 { // grep -c finding 0 exits 1
			t.Fatalf("script: %v %s", err, out)
		}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	real, _ := filepath.EvalSymlinks(dir)
	if len(lines) != 4 || (lines[0] != dir && lines[0] != real) || lines[1] != "exa/it's" || lines[2] != "0" || !strings.Contains(lines[3], "/.local/bin:") {
		t.Fatalf("script output = %q", lines)
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		return
	}
	start := time.Now()
	err = exec.Command("/bin/sh", "-c", botRemoteCommandScript("sleep 30", dir, "exa", "j", time.Second)).Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 124 || time.Since(start) > 10*time.Second {
		t.Fatalf("timeout run = %v after %s", err, time.Since(start))
	}
}

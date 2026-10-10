package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testWatch makes a watch for the test bot whose command is cmd, run in a
// temp folder that stands in for the bot's.
func testWatch(t *testing.T, cmd string, timeout int) (*botRecord, *botJob, string) {
	t.Helper()
	rec := testJobBot(t)
	dir := t.TempDir()
	if _, err := db.Exec(`UPDATE bots SET dir = ? WHERE name = ?`, dir, rec.Name); err != nil {
		t.Fatal(err)
	}
	j, err := createBotJob(rec, botJobInput{Name: ptr("watch"), Message: ptr("Judge the change."), Command: &cmd, Timeout: &timeout, Cron: ptr("*/10 * * * *")})
	if err != nil {
		t.Fatal(err)
	}
	return rec, j, dir
}

func fire(t *testing.T, j *botJob) botCmdOutcome {
	t.Helper()
	o, err := runBotJobCommand(j, "schedule", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestBotWatchSilenceAndOutput(t *testing.T) {
	_, j, dir := testWatch(t, `cat news 2>/dev/null; rm -f news`, 0)
	// Nothing to say: nothing delivered, nothing in the history, but the run
	// is on the job.
	if o := fire(t, j); o.Result != "quiet" || o.EventID != 0 {
		t.Fatalf("quiet run = %+v", o)
	}
	if ev, _ := listBotJobEvents(j.ID, 10); len(ev) != 0 {
		t.Fatalf("a quiet run made history: %+v", ev)
	}
	got, _ := getBotJob("jess", "watch")
	if got.LastRunResult != "quiet" || got.LastRunAt == "" {
		t.Fatalf("run not recorded: %+v", got)
	}
	// It runs in the bot's folder: output there is delivered after the message.
	_ = os.WriteFile(filepath.Join(dir, "news"), []byte("cover changed on evt-1\n"), 0o644)
	o := fire(t, j)
	if o.Result != "output" || o.EventID == 0 {
		t.Fatalf("output run = %+v", o)
	}
	// A second change before the bot takes the first is kept beside it, each
	// run under its own header, not merged away.
	_ = os.WriteFile(filepath.Join(dir, "news"), []byte("cover changed on evt-2\n"), 0o644)
	if o2 := fire(t, j); o2.EventID != o.EventID {
		t.Fatalf("second output made a new event: %d vs %d", o2.EventID, o.EventID)
	}
	ev, err := claimBotEvents("jess", time.Now())
	if err != nil || len(ev) != 1 {
		t.Fatalf("claim = %+v %v", ev, err)
	}
	e := ev[0]
	if !e.Watch || e.Count != 2 || e.RunStatus != "" {
		t.Fatalf("event = %+v", e)
	}
	for _, want := range []string{"Judge the change.\n", "[run 1 of 2, ", "cover changed on evt-1", "[run 2 of 2, ", "cover changed on evt-2"} {
		if !strings.Contains(e.Content, want) {
			t.Errorf("content lacks %q:\n%s", want, e.Content)
		}
	}
	if strings.Index(e.Content, "evt-1") > strings.Index(e.Content, "evt-2") {
		t.Error("runs out of order")
	}
	// A plain Run now of the same job does not merge into a watch event.
	_ = os.WriteFile(filepath.Join(dir, "news"), []byte("evt-3\n"), 0o644)
	o3 := fire(t, j)
	if o3.EventID == e.ID {
		t.Fatal("merged into a claimed event")
	}
}

func TestBotWatchFailureDamping(t *testing.T) {
	_, j, dir := testWatch(t, `if [ -e ok ]; then echo fine-now; else echo boom >&2; exit 3; fi`, 0)
	var reported []int
	for i := 1; i <= 9; i++ {
		o := fire(t, j)
		if o.Result != "error" || o.Exit != 3 || o.Detail != "boom" {
			t.Fatalf("failure %d = %+v", i, o)
		}
		if o.Reported {
			reported = append(reported, i)
		}
	}
	if fmt.Sprint(reported) != "[1 2 4 8]" {
		t.Fatalf("reported failures %v, want 1,2,4,8", reported)
	}
	got, _ := getBotJob("jess", "watch")
	if got.FailStreak != 9 || got.LastRunResult != "error" || got.LastRunNote != "boom" {
		t.Fatalf("job state = %+v", got)
	}
	// The reports accumulated into one undelivered event, marked error, with
	// no preamble (there is no output to judge).
	ev, _ := claimBotEvents("jess", time.Now())
	if len(ev) != 1 || ev[0].RunStatus != "error" || ev[0].Count != 4 {
		t.Fatalf("failure event = %+v", ev)
	}
	if strings.Contains(ev[0].Content, "Judge the change.") || !strings.Contains(ev[0].Content, "failed (exit 3)") ||
		!strings.Contains(ev[0].Content, "boom") || !strings.Contains(ev[0].Content, "next comes at failure 16") {
		t.Fatalf("failure content:\n%s", ev[0].Content)
	}
	// It works again: one recovery notice, carried with that run's output.
	_ = os.WriteFile(filepath.Join(dir, "ok"), nil, 0o644)
	o := fire(t, j)
	if o.Result != "output" || o.EventID == 0 {
		t.Fatalf("recovery run = %+v", o)
	}
	ev, _ = claimBotEvents("jess", time.Now())
	if len(ev) != 1 || ev[0].RunStatus != "" || !strings.Contains(ev[0].Content, "works again after 9 consecutive failures") || !strings.Contains(ev[0].Content, "fine-now") {
		t.Fatalf("recovery event = %+v", ev)
	}
	// Healthy again: the next run says nothing about recovery.
	fire(t, j)
	ev, _ = claimBotEvents("jess", time.Now())
	if len(ev) != 1 || strings.Contains(ev[0].Content, "works again") {
		t.Fatalf("recovery repeated: %+v", ev)
	}
}

func TestBotWatchTimeout(t *testing.T) {
	// The sleep is in a pipeline: the timeout must kill the whole group, or
	// the pipe stays open and the run never ends.
	_, j, _ := testWatch(t, `sleep 30 | cat`, 1)
	start := time.Now()
	o := fire(t, j)
	if o.Result != "timeout" || !o.Reported {
		t.Fatalf("timeout run = %+v", o)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("timeout took %s", took)
	}
	ev, _ := claimBotEvents("jess", time.Now())
	if len(ev) != 1 || ev[0].RunStatus != "timeout" || !strings.Contains(ev[0].Content, "timed out after 1s") {
		t.Fatalf("timeout event = %+v", ev)
	}
}

func TestBotWatchEnv(t *testing.T) {
	t.Setenv("UI_AUTH", "u:p")
	t.Setenv("MCP_OAUTH", "id:secret")
	t.Setenv("LASSO_MCP_TOKEN", "tok")
	t.Setenv("OP_VAULT", "titan")
	_, j, dir := testWatch(t, `env > env.txt`, 0)
	fire(t, j)
	b, err := os.ReadFile(filepath.Join(dir, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(b)
	for _, bad := range []string{"UI_AUTH=", "MCP_OAUTH=", "LASSO_MCP_TOKEN="} {
		if strings.Contains(env, bad) {
			t.Errorf("leaked %s", bad)
		}
	}
	home, _ := os.UserHomeDir()
	for _, want := range []string{"LASSO_BOT=jess\n", "LASSO_JOB=watch\n", "OP_VAULT=titan\n", "PATH=" + filepath.Join(home, ".local", "bin") + ":"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q:\n%s", want, env)
		}
	}
}

func TestBotWatchStoppedAndRunNow(t *testing.T) {
	rec, j, dir := testWatch(t, `echo ran >> runs.log; echo hello`, 0)
	// A stopped bot's watch does not run: a consumed change would be lost.
	_ = setBotStopped(rec.Name, true)
	if o := fire(t, j); o.Result != "dropped" {
		t.Fatalf("stopped = %+v", o)
	}
	if _, err := os.Stat(filepath.Join(dir, "runs.log")); err == nil {
		t.Fatal("the command ran for a stopped bot")
	}
	_ = setBotStopped(rec.Name, false)
	res, err := runBotJob(j)
	if err != nil || res.Status != "pending" || res.EventID == 0 {
		t.Fatalf("run now = %+v %v", res, err)
	}
	_ = saveBotJob(j, botJobInput{Command: ptr(`true`)})
	if res, _ := runBotJob(j); res.Status != "quiet" || res.EventID != 0 {
		t.Fatalf("quiet run now = %+v", res)
	}
	_ = saveBotJob(j, botJobInput{Command: ptr(`echo nope >&2; exit 2`)})
	if res, _ := runBotJob(j); res.Status != "failed" || res.Exit != 2 || res.Detail != "nope" || res.EventID == 0 {
		t.Fatalf("failed run now = %+v", res)
	}
	// Still running after the wait: say so; a second Run now is busy.
	prev := botRunNowWait
	botRunNowWait = 50 * time.Millisecond
	t.Cleanup(func() { botRunNowWait = prev })
	_ = saveBotJob(j, botJobInput{Command: ptr(`sleep 1`)})
	if res, _ := runBotJob(j); res.Status != "running" {
		t.Fatalf("slow run now = %+v", res)
	}
	if res, _ := runBotJob(j); res.Status != "busy" {
		t.Fatalf("second run now = %+v", res)
	}
	deadline := time.Now().Add(5 * time.Second)
	for botJobCommandRunning(j.ID) {
		if time.Now().After(deadline) {
			t.Fatal("run never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBotWatchTick(t *testing.T) {
	_, j, dir := testWatch(t, `echo tick >> runs.log`, 0)
	due, _ := parseStamp(j.NextAt)
	botJobsTick(due.Add(time.Second))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(filepath.Join(dir, "runs.log")); string(b) == "tick\n" && !botJobCommandRunning(j.ID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tick never ran the command")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The command printed only to its log: nothing delivered, next_at moved on.
	if ev, _ := listBotJobEvents(j.ID, 10); len(ev) != 0 {
		t.Fatalf("events = %+v", ev)
	}
	got, _ := getBotJob("jess", "watch")
	if next, _ := parseStamp(got.NextAt); !next.After(due) || got.LastRunResult != "quiet" {
		t.Fatalf("job = %+v", got)
	}
}

func TestBotWatchBounded(t *testing.T) {
	rs := watchRuns{}
	for i := 0; i < botWatchMaxRuns+5; i++ {
		rs.Runs = append(rs.Runs, watchRun{At: "t", Text: "x"})
		rs.bound()
	}
	if len(rs.Runs) != botWatchMaxRuns || rs.Dropped != 5 {
		t.Fatalf("runs %d dropped %d", len(rs.Runs), rs.Dropped)
	}
	if !strings.Contains(rs.render("m"), "[5 earlier run(s) dropped") {
		t.Fatal("drop not stated")
	}
	h := &headBuffer{max: 4}
	_, _ = h.Write([]byte("abcdef"))
	if got := h.String(); !strings.HasPrefix(got, "abcd\n[output truncated, 2 bytes dropped]") {
		t.Fatalf("head = %q", got)
	}
}

func TestBotJobOnce(t *testing.T) {
	rec := testJobBot(t)
	la := mustLoc(t, "America/Los_Angeles")
	at := time.Now().In(la).Add(48 * time.Hour).Truncate(time.Minute)
	j, err := createBotJob(rec, botJobInput{Name: ptr("once"), Message: ptr("refresh the token"), OnceAt: ptr(at.Format("2006-01-02T15:04")), Timezone: ptr("America/Los_Angeles")})
	if err != nil {
		t.Fatal(err)
	}
	if j.OnceAt != nowStamp(at) || j.NextAt != j.OnceAt {
		t.Fatalf("once = %q next %q, want %s", j.OnceAt, j.NextAt, nowStamp(at))
	}
	botJobsTick(at.Add(-time.Minute))
	if ev, _ := listBotJobEvents(j.ID, 10); len(ev) != 0 {
		t.Fatal("fired early")
	}
	// Lasso was down at the time: it fires once, late, and never again.
	botJobsTick(at.Add(time.Hour))
	botJobsTick(at.Add(2 * time.Hour))
	if ev, _ := listBotJobEvents(j.ID, 10); len(ev) != 1 {
		t.Fatalf("events = %+v", ev)
	}
	j, _ = getBotJob(rec.Name, "once")
	if j.NextAt != "" {
		t.Fatalf("next after firing = %q", j.NextAt)
	}
	// A save that leaves the passed time alone is fine; a new past time is not.
	if err := saveBotJob(j, botJobInput{Message: ptr("x")}); err != nil {
		t.Fatalf("save: %v", err)
	}
	for name, in := range map[string]botJobInput{
		"past":    {Name: ptr("p"), Message: ptr("x"), OnceAt: ptr("2020-01-01T00:00:00Z")},
		"both":    {Name: ptr("b"), Message: ptr("x"), OnceAt: ptr(at.Format(time.RFC3339)), Cron: ptr("0 * * * *")},
		"garbage": {Name: ptr("g"), Message: ptr("x"), OnceAt: ptr("next tuesday")},
	} {
		if _, err := createBotJob(rec, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if p := botOncePreview("2020-01-01T00:00", "UTC", time.Now()); p["error"] == nil {
		t.Errorf("past preview = %v", p)
	}
}

func TestBotWatchTildeFolder(t *testing.T) {
	rec := testJobBot(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, "bot"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE bots SET dir = '~/bot' WHERE name = ?`, rec.Name); err != nil {
		t.Fatal(err)
	}
	j, err := createBotJob(rec, botJobInput{Name: ptr("w"), Command: ptr(`pwd`)})
	if err != nil {
		t.Fatal(err)
	}
	if o := fire(t, j); o.Result != "output" {
		t.Fatalf("~ folder run = %+v", o)
	}
	ev, _ := claimBotEvents("jess", time.Now())
	if len(ev) != 1 || !strings.Contains(ev[0].Content, filepath.Join(home, "bot")) {
		t.Fatalf("ran elsewhere: %+v", ev)
	}
	// A folder that is gone is said plainly, not as a missing /bin/sh.
	_, _ = db.Exec(`UPDATE bots SET dir = '~/gone' WHERE name = ?`, rec.Name)
	if o := fire(t, j); o.Result != "error" || !strings.Contains(o.Detail, "does not exist") {
		t.Fatalf("missing folder = %+v", o)
	}
}

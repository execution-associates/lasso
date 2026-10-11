package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBridge stands in for omp with lasso's bridge loaded
// (ompbridge/lasso-bridge.ts): it serves the bot's socket, says host_hello to
// every client, broadcasts omp's events and answers the commands lasso sends.
// It logs each launch ("argv [-c]") and each command ("in <line>") to logPath
// and appends turns to a session file in the bot's session dir, as omp does.
type fakeBridge struct {
	name, dir, logPath string
	proto              int

	mu        sync.Mutex
	ln        net.Listener
	clients   map[net.Conn]bool
	streaming bool
	pending   []json.RawMessage
	sessionID string
	session   string
	sessions  int
	provider  string
	model     string
	thinking  string
	answers   chan string
	done      chan struct{}
	// onExit runs when omp would exit: restart is true when the restart
	// marker made it, which the launch task answers by relaunching.
	onExit func(restart bool)
}

func startFakeBridge(t *testing.T, name, dir, logPath string, proto int, fresh bool, onExit func(bool)) *fakeBridge {
	t.Helper()
	f := &fakeBridge{name: name, dir: dir, logPath: logPath, proto: proto, clients: map[net.Conn]bool{},
		sessionID: "sess-1", sessions: 1, provider: "fakeco", model: "m1", thinking: "low",
		answers: make(chan string, 4), done: make(chan struct{}), onExit: onExit}
	argv := "argv"
	if !fresh {
		argv += " -c"
	}
	f.log(argv)
	f.openSession(f.sessionID)
	sock := botSocketPath(name)
	_ = os.MkdirAll(filepath.Dir(sock), 0o700)
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("fake bridge: %v", err)
	}
	_ = os.Chmod(sock, 0o600)
	f.ln = ln
	go f.accept()
	go f.watchMarker()
	return f
}

func (f *fakeBridge) log(s string) {
	if fh, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(fh, s)
		fh.Close()
	}
}

func (f *fakeBridge) openSession(id string) {
	dir := botOmpSessionDir(f.dir)
	_ = os.MkdirAll(dir, 0o755)
	f.session = filepath.Join(dir, "2026-10-10T00-00-00-000Z_"+id+".jsonl")
	if _, err := os.Stat(f.session); err != nil {
		_ = os.WriteFile(f.session, []byte(`{"type":"session","version":3,"id":"`+id+`","timestamp":"2026-10-10T00:00:00Z","cwd":"/x"}`+"\n"), 0o644)
	}
}

func (f *fakeBridge) record(role, text string) {
	f.mu.Lock()
	path := f.session
	f.mu.Unlock()
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	now := time.Now()
	b, _ := json.Marshal(map[string]any{
		"type": "message", "id": fmt.Sprintf("%x", now.UnixNano()), "parentId": nil, "timestamp": now.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": role, "content": []map[string]any{{"type": "text", "text": text}}, "timestamp": now.UnixMilli(), "stopReason": "stop"},
	})
	fh.Write(append(b, '\n'))
}

func (f *fakeBridge) emit(v any) {
	b, _ := json.Marshal(v)
	b = append(b, '\n')
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.clients {
		c.Write(b)
	}
}

func (f *fakeBridge) accept() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.clients[c] = true
		hello, _ := json.Marshal(map[string]any{"type": "host_hello", "protocol": f.proto, "version": "test",
			"ready": true, "streaming": f.streaming, "pending": f.pending})
		c.Write(append(hello, '\n'))
		f.mu.Unlock()
		go f.read(c)
	}
}

func (f *fakeBridge) read(c net.Conn) {
	in := bufio.NewScanner(c)
	in.Buffer(make([]byte, 64*1024), 4<<20)
	for in.Scan() {
		f.handle(in.Text())
	}
	f.mu.Lock()
	delete(f.clients, c)
	f.mu.Unlock()
}

func (f *fakeBridge) respond(id, typ string, data any) {
	f.emit(map[string]any{"type": "response", "id": id, "command": typ, "success": true, "data": data})
}

func (f *fakeBridge) handle(line string) {
	f.log("in " + line)
	var c struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Message     string `json:"message"`
		Value       string `json:"value"`
		Provider    string `json:"provider"`
		ModelID     string `json:"modelId"`
		Level       string `json:"level"`
		SessionPath string `json:"sessionPath"`
	}
	if json.Unmarshal([]byte(line), &c) != nil {
		return
	}
	switch c.Type {
	case "get_state":
		f.mu.Lock()
		state := map[string]any{
			"sessionId": f.sessionID, "isStreaming": f.streaming, "thinkingLevel": f.thinking,
			"model":        map[string]any{"id": f.model, "name": "Fake " + f.model, "provider": f.provider, "contextWindow": 200000, "thinking": map[string]any{"efforts": []string{"low", "high"}}},
			"contextUsage": map[string]any{"tokens": 5000, "contextWindow": 200000, "percent": 2.5},
		}
		f.mu.Unlock()
		f.respond(c.ID, c.Type, state)
	case "set_model":
		f.mu.Lock()
		f.provider, f.model = c.Provider, c.ModelID
		f.mu.Unlock()
		f.emit(map[string]any{"type": "model_changed"})
		f.respond(c.ID, c.Type, nil)
	case "set_thinking_level":
		f.mu.Lock()
		f.thinking = c.Level
		f.mu.Unlock()
		f.emit(map[string]any{"type": "thinking_level_changed"})
		f.respond(c.ID, c.Type, nil)
	case "new_session":
		f.mu.Lock()
		f.sessions++
		f.sessionID = fmt.Sprintf("sess-%d", f.sessions)
		f.openSession(f.sessionID)
		f.mu.Unlock()
		f.respond(c.ID, c.Type, map[string]any{"cancelled": false})
	case "switch_session":
		base := filepath.Base(c.SessionPath)
		f.mu.Lock()
		f.sessionID = strings.TrimSuffix(base[strings.LastIndex(base, "_")+1:], ".jsonl")
		f.openSession(f.sessionID)
		f.mu.Unlock()
		f.respond(c.ID, c.Type, map[string]any{"cancelled": false})
	case "abort":
		f.respond(c.ID, c.Type, nil)
	case "extension_ui_response":
		f.mu.Lock()
		f.pending = nil
		f.mu.Unlock()
		f.emit(map[string]any{"type": "host_ui_answered", "id": c.ID})
		f.answers <- c.Value
	case "host_stop":
		f.exit(false)
	case "prompt":
		f.respond(c.ID, c.Type, map[string]any{"agentInvoked": true})
		go f.turn(c.Message)
	default:
		f.emit(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": false, "error": "unknown"})
	}
}

func (f *fakeBridge) turn(msg string) {
	f.mu.Lock()
	f.streaming = true
	f.mu.Unlock()
	f.emit(map[string]any{"type": "agent_start"})
	f.record("user", msg)
	f.emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": msg}})
	reply := "done: " + strings.SplitN(msg, "\n", 2)[0]
	if strings.Contains(msg, "ASK") {
		q := map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Pick one", "options": []string{"A", "B"}}
		raw, _ := json.Marshal(q)
		f.mu.Lock()
		f.pending = []json.RawMessage{raw}
		f.mu.Unlock()
		f.emit(q)
		reply = "picked " + <-f.answers
	}
	f.record("assistant", reply)
	f.emit(map[string]any{"type": "message_end", "message": map[string]any{
		"role": "assistant", "content": []map[string]any{{"type": "text", "text": reply}}}})
	f.mu.Lock()
	f.streaming = false
	f.mu.Unlock()
	f.emit(map[string]any{"type": "agent_end"})
}

// watchMarker is the bridge's self-restart: once idle with the marker there,
// omp exits and the task relaunches.
func (f *fakeBridge) watchMarker() {
	marker := botRestartMarker(f.dir)
	for {
		select {
		case <-f.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
		f.mu.Lock()
		idle := !f.streaming && len(f.pending) == 0
		f.mu.Unlock()
		if _, err := os.Stat(marker); err == nil && idle {
			f.emit(map[string]any{"type": "host_restarting"})
			f.exit(true)
			return
		}
	}
}

// exit is omp ending: the socket goes, and onExit says why.
func (f *fakeBridge) exit(restart bool) {
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		return
	default:
	}
	close(f.done)
	f.ln.Close()
	for c := range f.clients {
		c.Close()
	}
	f.mu.Unlock()
	_ = os.Remove(botSocketPath(f.name))
	if f.onExit != nil {
		f.onExit(restart)
	}
}

// noBotLauncher is every test's launcher unless it installs another.
type noBotLauncher struct{}

func (noBotLauncher) start(Backend, *botRecord, string, bool) error {
	return fmt.Errorf("tests open no herdr panes")
}
func (noBotLauncher) pane(Backend, *botRecord) (string, bool) { return "", false }
func (noBotLauncher) close(Backend, *botRecord)               {}

// fakePaneLauncher is the bot's herdr pane: start runs a fake bridge in it,
// which stays until the pane is closed. omp ending leaves the pane (a shell),
// unless the restart marker made it end: then the task relaunches omp with -c
// in the same pane.
type fakePaneLauncher struct {
	t       *testing.T
	logPath string
	mu      sync.Mutex
	panes   map[string]*fakeBridge
	starts  int
	// proto is the protocol the next bridge started speaks (0: this lasso's).
	proto int
}

func (l *fakePaneLauncher) start(b Backend, r *botRecord, dir string, fresh bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.panes[r.Name]; ok {
		return errBotRunning
	}
	l.run(r.Name, dir, fresh)
	return nil
}

// run starts a bridge in the bot's pane. Called with l.mu held.
func (l *fakePaneLauncher) run(name, dir string, fresh bool) {
	proto := botBridgeProtocol
	if l.proto != 0 {
		proto, l.proto = l.proto, 0
	}
	l.starts++
	var f *fakeBridge
	f = startFakeBridge(l.t, name, dir, l.logPath, proto, fresh, func(restart bool) {
		if !restart {
			return
		}
		_ = os.Remove(botRestartMarker(dir))
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.panes[name] == f {
			l.run(name, dir, false)
		}
	})
	l.panes[name] = f
}

func (l *fakePaneLauncher) pane(_ Backend, r *botRecord) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.panes[r.Name]
	return "fake:" + r.Name, ok
}

func (l *fakePaneLauncher) close(_ Backend, r *botRecord) {
	l.mu.Lock()
	f := l.panes[r.Name]
	delete(l.panes, r.Name)
	l.mu.Unlock()
	if f != nil {
		f.exit(false)
	}
}

func (l *fakePaneLauncher) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

func readLog(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func ompWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func useOmpBotEnv(t *testing.T, name string) (*botRecord, string) {
	r, logPath, _ := useOmpBotEnvL(t, name)
	return r, logPath
}

func useOmpBotEnvL(t *testing.T, name string) (*botRecord, string, *fakePaneLauncher) {
	t.Helper()
	useBotTestEnv(t)
	logPath := filepath.Join(t.TempDir(), "omp.log")
	l := &fakePaneLauncher{t: t, logPath: logPath, panes: map[string]*fakeBridge{}}
	prev := botOmpLaunch
	botOmpLaunch = l
	t.Cleanup(func() {
		l.mu.Lock()
		names := []string{}
		for n := range l.panes {
			names = append(names, n)
		}
		l.mu.Unlock()
		for _, n := range names {
			l.close(nil, &botRecord{Name: n})
		}
		botOmpLaunch = prev
		ompBots.Lock()
		delete(ompBots.m, name)
		ompBots.Unlock()
		bots.mu.Lock()
		delete(bots.starting, name)
		delete(bots.misses, name)
		delete(bots.lines, name)
		bots.mu.Unlock()
	})
	r := &botRecord{Name: name, Runtime: botRuntimeOmp, Dir: filepath.Join(t.TempDir(), name), KeepRunning: true}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		t.Fatal(err)
	}
	return r, logPath, l
}

func TestOmpBotLifecycleChatAndJobs(t *testing.T) {
	r, logPath, l := useOmpBotEnvL(t, "ompy")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	if err := startBot(b, r, false); err != errBotRunning {
		t.Errorf("second start = %v", err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	dir := expandTildeOn(b, r.Dir)
	for _, f := range []string{"AGENTS.md", ".lasso/omp-bridge.ts", ".mise/tasks/bot", ".mise/tasks/restart"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("no %s", f)
		}
	}
	if v := botStatus(b, r, nil); v.PaneID != "fake:ompy" || v.HostOutdated {
		t.Errorf("view = pane %q outdated %v", v.PaneID, v.HostOutdated)
	}

	// The human's message: a prompt, and the answer shows as the chat's rows.
	if err := botOmpSend(r.Name, "hello there", true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the answer in the chat", func() bool {
		v := botStatus(b, r, nil)
		return v.State == "idle" && v.LastKind == "agent" && v.LastText == "done: hello there"
	})
	chat := botOmpChat(b, r, "")
	if len(chat.Items) != 2 || chat.Items[0].Kind != "user" || chat.Items[1].Text != "done: hello there" {
		t.Fatalf("chat = %+v", chat.Items)
	}

	// A question blocks the bot until it is answered from the chat.
	if err := botOmpSend(r.Name, "ASK", true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "blocked", func() bool { return botStatus(b, r, nil).State == "blocked" })
	if v := botStatus(b, r, nil); v.WaitingFor != "Pick one" {
		t.Errorf("waiting_for = %q", v.WaitingFor)
	}
	val := "A"
	if err := botOmpAnswer(r.Name, "q1", &val, nil, false); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the picked answer", func() bool { return botStatus(b, r, nil).LastText == "picked A" })
	if err := botOmpAnswer(r.Name, "q1", &val, nil, false); err == nil {
		t.Error("answered a closed question")
	}

	// A job event arrives as a prompt and is acked.
	msg := "check the news"
	j, err := createBotJob(r, botJobInput{Name: ptr("news"), Message: &msg})
	if err != nil {
		t.Fatal(err)
	}
	id, st, err := enqueueBotEvent(j, "run", j.Message, "", time.Now())
	if err != nil || st != "pending" {
		t.Fatalf("enqueue: %d %s %v", id, st, err)
	}
	botOmpTick(b, r, time.Now())
	ompWaitFor(t, "the job prompt", func() bool { return strings.Contains(readLog(logPath), `[lasso job \"news\" trigger=run`) })
	ompWaitFor(t, "the event acked", func() bool {
		var status string
		db.QueryRow(`SELECT status FROM bot_events WHERE id = ?`, id).Scan(&status)
		return status == "delivered"
	})

	// Stop asks the bridge to end omp, closes the pane, and keeps it down.
	if err := stopBot(b, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readLog(logPath), `"host_stop"`) {
		t.Error("stop did not ask the bridge to end omp")
	}
	ompWaitFor(t, "stopped", func() bool { return !botOmpRunning(b, r) })
	got, _ := getBot(r.Name)
	if !got.Stopped || botStatus(b, got, nil).State != "stopped" {
		t.Errorf("after stop: %+v", botStatus(b, got, nil))
	}
	for i := 0; i < botMissesBeforeRelaunch+1; i++ {
		botOmpTick(b, got, time.Now())
	}
	if l.startCount() != 1 {
		t.Error("keep-running relaunched a stopped bot")
	}
	// A stopped bot's firings are dropped.
	if _, st, _ := enqueueBotEvent(j, "run", j.Message, "", time.Now()); st != "dropped" {
		t.Errorf("stopped bot's event = %s", st)
	}
}

func TestOmpBotKeepRunningWaitsForHerdr(t *testing.T) {
	r, _ := useOmpBotEnv(t, "phoenix")
	b, _ := botBackend("local")
	var launches int
	prev := botRelaunch
	botRelaunch = func(Backend, *botRecord) error { launches++; return nil }
	t.Cleanup(func() { botRelaunch = prev })
	now := time.Now()
	for i := 1; i < botMissesBeforeRelaunch; i++ {
		botOmpTick(b, r, now)
	}
	if launches != 0 {
		t.Fatalf("relaunched after %d misses; herdr's restore gets %d", launches, botMissesBeforeRelaunch)
	}
	botOmpTick(b, r, now)
	if launches != 1 {
		t.Errorf("launches = %d after %d misses", launches, botMissesBeforeRelaunch)
	}
	// Within the start grace a missing pane is not a miss.
	bots.mu.Lock()
	bots.starting[r.Name] = now
	bots.mu.Unlock()
	for i := 0; i < botMissesBeforeRelaunch*2; i++ {
		botOmpTick(b, r, now.Add(time.Second))
	}
	if launches != 1 {
		t.Errorf("relaunched during the start grace (launches = %d)", launches)
	}
}

func TestOmpBotSelfRestart(t *testing.T) {
	r, logPath, l := useOmpBotEnvL(t, "again")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	// What `mise run restart` does: the marker, which the bridge acts on once
	// idle, and the task relaunches omp on the same conversation.
	dir := expandTildeOn(b, r.Dir)
	if err := os.WriteFile(botRestartMarker(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the relaunch", func() bool { return l.startCount() == 2 && botStatus(b, r, nil).State == "idle" })
	if !strings.Contains(readLog(logPath), "argv -c") {
		t.Errorf("relaunch did not continue the conversation:\n%s", readLog(logPath))
	}
	if got, _ := getBot(r.Name); got.Stopped {
		t.Error("a self-restart marked the bot stopped")
	}
}

func TestOmpBotBridgeUpgrade(t *testing.T) {
	r, logPath, l := useOmpBotEnvL(t, "elder")
	b, _ := botBackend("local")
	l.proto = botBridgeProtocol + 1
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	if v := botStatus(b, r, nil); !v.HostOutdated || v.HostVersion == "" {
		t.Fatalf("other bridge not reported: %+v", v)
	}
	// Not while a question is open: the upgrade waits for an idle bot.
	if err := botOmpSend(r.Name, "ASK", true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "blocked", func() bool { return botStatus(b, r, nil).State == "blocked" })
	botOmpTick(b, r, time.Now())
	time.Sleep(300 * time.Millisecond)
	if l.startCount() != 1 {
		t.Fatalf("upgraded a blocked bot (starts = %d)", l.startCount())
	}
	val := "A"
	if err := botOmpAnswer(r.Name, "q1", &val, nil, false); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle again", func() bool { return botStatus(b, r, nil).State == "idle" })
	botOmpTick(b, r, time.Now())
	ompWaitFor(t, "the bridge on this protocol, in the same pane", func() bool {
		v := botStatus(b, r, nil)
		return l.startCount() == 2 && v.State == "idle" && !v.HostOutdated
	})
	if got, _ := getBot(r.Name); got.Stopped {
		t.Error("an upgrade marked the bot stopped")
	}
	if !strings.Contains(readLog(logPath), "argv -c") {
		t.Errorf("the restart did not continue the conversation:\n%s", readLog(logPath))
	}
}

func TestOmpBotLegacyHostMigrates(t *testing.T) {
	r, logPath, l := useOmpBotEnvL(t, "relic")
	b, _ := botBackend("local")
	// A bot an older lasso started under its bot host: the socket answers with
	// protocol 3 and no pane of lasso's own.
	var unitUp sync.Mutex
	unit := true
	prevActive, prevStop := botLegacyUnitActive, botLegacyUnitStop
	botLegacyUnitActive = func(string) bool { unitUp.Lock(); defer unitUp.Unlock(); return unit }
	botLegacyUnitStop = func(string) error { unitUp.Lock(); defer unitUp.Unlock(); unit = false; return nil }
	t.Cleanup(func() { botLegacyUnitActive, botLegacyUnitStop = prevActive, prevStop })
	// The unit ends when its host does.
	host := startFakeBridge(t, r.Name, expandTildeOn(b, r.Dir), logPath, 3, false, func(bool) {
		unitUp.Lock()
		unit = false
		unitUp.Unlock()
	})
	t.Cleanup(func() { host.exit(false) })
	ompWaitFor(t, "idle on the old host", func() bool {
		v := botStatus(b, r, nil)
		return v.State == "idle" && v.HostOutdated
	})
	botOmpTick(b, r, time.Now())
	ompWaitFor(t, "the bot in a pane of its own", func() bool {
		v := botStatus(b, r, nil)
		return l.startCount() == 1 && v.State == "idle" && !v.HostOutdated && v.PaneID == "fake:relic"
	})
	if !strings.Contains(readLog(logPath), `"host_stop"`) {
		t.Error("the old host was not asked to stop")
	}
	unitUp.Lock()
	defer unitUp.Unlock()
	if unit {
		t.Error("the old host's unit was left running")
	}
}

func TestBotNormalizeRuntime(t *testing.T) {
	for _, c := range []struct {
		r  botRecord
		ok bool
	}{
		{botRecord{Name: "a"}, true},
		{botRecord{Name: "a", Runtime: "omp", Effort: "auto", PermissionMode: "write"}, true},
		{botRecord{Name: "a", Runtime: "omp", PermissionMode: "bypassPermissions"}, false},
		{botRecord{Name: "a", Runtime: "claude", Effort: "auto"}, false},
		{botRecord{Name: "a", Runtime: "omp", Host: "citadel"}, false},
		{botRecord{Name: "a", Runtime: "codex"}, false},
		{botRecord{Name: "a", Runtime: "omp", ExtraArgs: []string{"--advisor"}}, true},
		{botRecord{Name: "a", Runtime: "omp", ExtraArgs: []string{"--mode=json"}}, false},
		{botRecord{Name: "a", Runtime: "omp", ExtraArgs: []string{"-c"}}, false},
		{botRecord{Name: "a", Runtime: "claude", ExtraArgs: []string{"-c"}}, true},
	} {
		r := c.r
		err := r.normalize()
		if (err == nil) != c.ok {
			t.Errorf("%+v: err = %v", c.r, err)
		}
		if err == nil && r.Runtime == "" {
			t.Errorf("%+v: runtime left empty", c.r)
		}
	}
}

func TestBotOmpFiles(t *testing.T) {
	t.Setenv("LASSO_DIR", "/l")
	r := &botRecord{Name: "a", Runtime: "omp", Model: "anthropic/claude-opus-5", Effort: "high", PermissionMode: "always-ask",
		LaunchTask: "deep", ExtraArgs: []string{"--append-system-prompt", "be brief", "--advisor"},
		MCP: []botMCPServer{{Name: "mail", Type: "http", URL: "https://m.example/mcp", OAuth: true, Channel: true}}}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	s := botOmpTaskScript(r, "/b/a", []string{"TOKEN"})
	for _, want := range []string{
		"#MISE secrets=[\"TOKEN\"]", "#MISE interactive=true",
		"export LASSO_BOT_NAME='a' LASSO_BOT_SOCKET='/l/bots/a.sock'",
		`LASSO_BOT_RESTART='/b/a/.lasso/restart' LASSO_BOT_RESUME='["mise","run","deep","--","-c"]'`,
		`export LASSO_HERDR_SOCKET="${HERDR_SOCKET_PATH:-}" LASSO_HERDR_PANE="${HERDR_PANE_ID:-}"`,
		"unset HERDR_ENV\n",
		"omp '-e' '/b/a/.lasso/omp-bridge.ts' '--session-dir' '/b/a/.lasso/sessions'",
		"'--approval-mode' 'always-ask'", "'--model' 'anthropic/claude-opus-5'", "'--thinking' 'high'", "be brief",
		"'--advisor' \"$@\"\nstatus=$?\n",
		"rm -f \"$marker\"\n  exec mise run deep -- -c\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("task script lacks %q:\n%s", want, s)
		}
	}
	for _, gone := range []string{"rpc", "LASSO_BOT_RPC", "exec omp", "dangerously"} {
		if strings.Contains(s, gone) {
			t.Errorf("task script has %q:\n%s", gone, s)
		}
	}
	if err := validateResumeArgv(botOmpResumeArgv(r.task())); err != nil {
		t.Errorf("herdr would refuse the resume argv: %v", err)
	}
	if got := botOmpLaunchCommand("deep", false); got != "mise run deep -- -c" {
		t.Errorf("launch = %q", got)
	}
	if got := botOmpLaunchCommand("bot", true); got != "mise run bot" {
		t.Errorf("fresh launch = %q", got)
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(botOmpMCPJSON(r, "/b/a", map[string]bool{"mail": true}), &cfg); err != nil {
		t.Fatal(err)
	}
	h, _ := cfg.MCPServers["mail"]["headers"].(map[string]any)
	if auth, _ := h["Authorization"].(string); !strings.HasPrefix(auth, "!printf 'Bearer %s'") || !strings.Contains(auth, "LASSO_OAUTH_MAIL_ACCESS") {
		t.Errorf("mail headers = %v", h)
	}
	if _, ok := cfg.MCPServers[botChannelName]; ok {
		t.Error("an omp bot got lasso's claude channel")
	}
	e := botEvent{ID: 7, Job: "sweep", Kind: "webhook", Count: 2, FiredAt: "t", Content: "body", Watch: true, RunStatus: "error"}
	if got := botOmpEventMessage(e); got != "[lasso job \"sweep\" trigger=webhook event=7 count=2 fired_at=t watch=true status=error]\nbody" {
		t.Errorf("event message = %q", got)
	}
}

// The bridge and lasso must agree on the protocol, and every bot gets the
// embedded bridge; a folder an older lasso wrote loses its viewer task.
func TestBotOmpBridgeFile(t *testing.T) {
	m := regexp.MustCompile(`(?m)^const PROTOCOL = (\d+);`).FindSubmatch(botOmpBridgeSource)
	if m == nil {
		t.Fatal("no PROTOCOL in ompbridge/lasso-bridge.ts")
	}
	if n, _ := strconv.Atoi(string(m[1])); n != botBridgeProtocol {
		t.Errorf("bridge PROTOCOL = %d, botBridgeProtocol = %d", n, botBridgeProtocol)
	}
	r, _ := useOmpBotEnv(t, "filed")
	b, _ := botBackend("local")
	dir := expandTildeOn(b, r.Dir)
	attach := filepath.Join(dir, ".mise", "tasks", "attach")
	os.MkdirAll(filepath.Dir(attach), 0o755)
	os.WriteFile(attach, []byte("#!/bin/sh\n"), 0o755)
	if err := botMaterialize(b, r); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(botOmpBridgePath(dir)); string(got) != string(botOmpBridgeSource) {
		t.Error("the bridge file is not the embedded bridge")
	}
	if st, err := os.Stat(filepath.Dir(botSocketPath(r.Name))); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the bridge's socket dir: %v %v", st, err)
	}
	if _, err := os.Stat(attach); !os.IsNotExist(err) {
		t.Error("the old viewer task was left behind")
	}
}

func TestBotCreateSeedsInstructions(t *testing.T) {
	useBotTestEnv(t)
	root := t.TempDir()
	do := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveBots(w, httptest.NewRequest("POST", "/api/bots", strings.NewReader(body)))
		return w
	}
	ompDir := filepath.Join(root, "o")
	if w := do(`{"name":"o","dir":"` + ompDir + `","instructions":"You watch the news."}`); w.Code != 200 {
		t.Fatalf("create omp: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(ompDir, "AGENTS.md")); string(b) != "You watch the news.\n" {
		t.Errorf("AGENTS.md = %q", b)
	}
	claudeDir := filepath.Join(root, "c")
	if w := do(`{"name":"c","runtime":"claude","dir":"` + claudeDir + `","instructions":"Answer mail."}`); w.Code != 200 {
		t.Fatalf("create claude: %d %s", w.Code, w.Body)
	}
	if b, _ := os.ReadFile(filepath.Join(claudeDir, "CLAUDE.md")); string(b) != "Answer mail.\n" {
		t.Errorf("CLAUDE.md = %q", b)
	}
	// An existing file is kept, and the answer says so.
	keepDir := filepath.Join(root, "k")
	os.MkdirAll(keepDir, 0o755)
	os.WriteFile(filepath.Join(keepDir, "AGENTS.md"), []byte("mine"), 0o644)
	w := do(`{"name":"k","dir":"` + keepDir + `","instructions":"new"}`)
	if b, _ := os.ReadFile(filepath.Join(keepDir, "AGENTS.md")); string(b) != "mine" || !strings.Contains(w.Body.String(), "kept") {
		t.Errorf("existing AGENTS.md: %q, answer %s", b, w.Body)
	}
}

func TestBotDeleteWithFolder(t *testing.T) {
	r, _ := useOmpBotEnv(t, "goner")
	b, _ := botBackend("local")
	if err := botMaterialize(b, r); err != nil {
		t.Fatal(err)
	}
	dir := expandTildeOn(b, r.Dir)
	del := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveBots(w, httptest.NewRequest("DELETE", "/api/bots/goner"+q, nil))
		return w
	}
	if w := del("?folder=1"); w.Code != 200 || !strings.Contains(w.Body.String(), `"folder_deleted":true`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("folder still there: %v", err)
	}
	if _, err := getBot("goner"); err != errBotNotFound {
		t.Errorf("bot still there: %v", err)
	}
	// The guards, checked before anything changes.
	home, _ := os.UserHomeDir()
	for _, d := range []string{"/", "/tmp", home, filepath.Dir(home), lassoDir()} {
		if err := botFolderRemovable(b, d); err == nil {
			t.Errorf("%s would be deleted", d)
		}
	}
	if err := botFolderRemovable(b, t.TempDir()); err != nil {
		t.Errorf("a plain folder refused: %v", err)
	}
}

func TestOmpBotModelPickers(t *testing.T) {
	r, _ := useOmpBotEnv(t, "picky")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the live model", func() bool {
		v := botStatus(b, r, nil)
		return v.State == "idle" && v.LiveModel != nil && v.LiveModel.ID == "m1"
	})
	v := botStatus(b, r, nil)
	if v.Thinking != "low" || v.Context == nil || v.Context.Tokens != 5000 || v.Context.Window != 200000 ||
		strings.Join(v.ThinkingLevels, ",") != "off,low,high,auto" {
		t.Fatalf("view = thinking %q levels %v context %+v", v.Thinking, v.ThinkingLevels, v.Context)
	}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveBots(w, httptest.NewRequest("POST", "/api/bots/picky/model", strings.NewReader(body)))
		return w
	}
	if w := post(`{"model":"bare-name"}`); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "provider/model") {
		t.Errorf("a model without provider: %d %s", w.Code, w.Body)
	}
	w := post(`{"model":"otherco/m2","thinking":"high"}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), `"restart_needed":true`) {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	ompWaitFor(t, "the switch", func() bool {
		v := botStatus(b, r, nil)
		return v.LiveModel != nil && v.LiveModel.ID == "m2" && v.Thinking == "high"
	})
	got, _ := getBot("picky")
	if got.Model != "otherco/m2" || got.Effort != "high" {
		t.Errorf("saved = %q / %q", got.Model, got.Effort)
	}
	task, _ := os.ReadFile(botTaskPath(expandTildeOn(b, r.Dir)))
	if !strings.Contains(string(task), "'--model' 'otherco/m2'") || !strings.Contains(string(task), "'--thinking' 'high'") {
		t.Errorf("task not rewritten:\n%s", task)
	}
	// A change made elsewhere (the terminal, the bot itself) is kept too.
	if err := botOmpSetModel("picky", "thirdco/m3", "low"); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the outside change saved", func() bool {
		got, _ := getBot("picky")
		return got.Model == "thirdco/m3" && got.Effort == "low"
	})
}

func TestOmpBotSessions(t *testing.T) {
	r, _ := useOmpBotEnv(t, "convo")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	if err := botOmpSend(r.Name, "first chat", true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the answer", func() bool { return botStatus(b, r, nil).LastText == "done: first chat" })
	req := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveBots(w, httptest.NewRequest(method, "/api/bots/convo/"+path, strings.NewReader(body)))
		return w
	}
	if w := req("POST", "sessions/new", ""); w.Code != 200 {
		t.Fatalf("new: %d %s", w.Code, w.Body)
	}
	ompWaitFor(t, "the new conversation", func() bool { return ompBotFor("convo").snapshot().sessionID == "sess-2" })
	if err := botOmpSend(r.Name, "second chat", true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "the second answer", func() bool { return botStatus(b, r, nil).LastText == "done: second chat" })
	var list struct {
		Sessions []botSessionInfo `json:"sessions"`
	}
	json.Unmarshal(req("GET", "sessions", "").Body.Bytes(), &list)
	if len(list.Sessions) != 2 || !list.Sessions[0].Current || list.Sessions[1].ID != "sess-1" ||
		list.Sessions[1].Preview != "first chat" || list.Sessions[1].Messages != 2 {
		t.Fatalf("sessions = %+v", list.Sessions)
	}
	if w := req("POST", "sessions/resume", `{"id":"sess-1"}`); w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body)
	}
	ompWaitFor(t, "back on the first", func() bool {
		return ompBotFor("convo").snapshot().sessionID == "sess-1" && botStatus(b, r, nil).LastText == "done: first chat"
	})
	if w := req("POST", "sessions/resume", `{"id":"nope"}`); w.Code != http.StatusBadGateway {
		t.Errorf("unknown session: %d", w.Code)
	}
}

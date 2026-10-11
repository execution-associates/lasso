package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOmpEnv, when set, makes the test binary a stand-in for `omp --mode
// rpc-ui` (see TestMain). It logs every line it receives and its argv to
// $LASSO_TEST_FAKE_OMP_LOG and appends turns to a session file in
// $LASSO_TEST_FAKE_OMP_SESSIONS.
const fakeOmpEnv = "LASSO_TEST_FAKE_OMP"

func runFakeOmp() int {
	logPath := os.Getenv("LASSO_TEST_FAKE_OMP_LOG")
	logf := func(s string) {
		if logPath == "" {
			return
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(f, s)
			f.Close()
		}
	}
	logf("argv " + strings.Join(os.Args[1:], " "))
	if os.Getenv("HERDR_PANE_ID") != "" {
		logf("herdr-env-leaked")
	}
	var session string
	if dir := os.Getenv("LASSO_TEST_FAKE_OMP_SESSIONS"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		session = filepath.Join(dir, "2026-10-10T00-00-00-000Z_sess-1.jsonl")
		if _, err := os.Stat(session); err != nil {
			_ = os.WriteFile(session, []byte(`{"type":"session","version":3,"id":"sess-1","timestamp":"2026-10-10T00:00:00Z","cwd":"/x"}`+"\n"), 0o644)
		}
	}
	var mu sync.Mutex
	// Under the bot host the RPC is on fds 3 and 4, as the task script moves
	// them for a real omp.
	stdin, stdout := os.Stdin, os.Stdout
	if os.Getenv("LASSO_BOT_RPC") == "fd" {
		stdin, stdout = os.NewFile(3, "rpc-in"), os.NewFile(4, "rpc-out")
	}
	out := bufio.NewWriter(stdout)
	emit := func(v any) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	record := func(role, text string) {
		if session == "" {
			return
		}
		f, err := os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		defer f.Close()
		now := time.Now()
		b, _ := json.Marshal(map[string]any{
			"type": "message", "id": fmt.Sprintf("%x", now.UnixNano()), "parentId": nil, "timestamp": now.UTC().Format(time.RFC3339Nano),
			"message": map[string]any{"role": role, "content": []map[string]any{{"type": "text", "text": text}}, "timestamp": now.UnixMilli(), "stopReason": "stop"},
		})
		f.Write(append(b, '\n'))
	}
	emit(map[string]any{"type": "ready", "protocolVersion": 1})
	answers := make(chan string, 4)
	in := bufio.NewScanner(stdin)
	in.Buffer(make([]byte, 64*1024), 4<<20)
	for in.Scan() {
		line := in.Text()
		logf("in " + line)
		var c struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Message string `json:"message"`
			Value   string `json:"value"`
		}
		if json.Unmarshal([]byte(line), &c) != nil {
			continue
		}
		switch c.Type {
		case "get_state":
			emit(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true, "data": map[string]any{"sessionId": "sess-1", "isStreaming": false}})
		case "abort":
			emit(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true})
		case "extension_ui_response":
			answers <- c.Value
		case "prompt":
			emit(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": true})
			msg := c.Message
			go func() {
				emit(map[string]any{"type": "agent_start"})
				record("user", msg)
				emit(map[string]any{"type": "message_end", "message": map[string]any{"role": "user", "content": msg}})
				reply := "done: " + strings.SplitN(msg, "\n", 2)[0]
				if strings.Contains(msg, "ASK") {
					emit(map[string]any{"type": "extension_ui_request", "id": "q1", "method": "select", "title": "Pick one", "options": []string{"A", "B"}})
					reply = "picked " + <-answers
				}
				record("assistant", reply)
				emit(map[string]any{"type": "message_end", "message": map[string]any{
					"role": "assistant", "content": []map[string]any{{"type": "text", "text": reply}}}})
				emit(map[string]any{"type": "agent_end"})
			}()
		default:
			emit(map[string]any{"type": "response", "id": c.ID, "command": c.Type, "success": false, "error": "unknown"})
		}
	}
	return 0
}

// fakeOmpHost points botHostCommand at the fake omp for the test.
func fakeOmpHost(t *testing.T) (logPath string) {
	t.Helper()
	logPath = filepath.Join(t.TempDir(), "omp.log")
	exe, _ := os.Executable()
	prev := botHostCommand
	botHostCommand = func(dir, task string, fresh bool) *exec.Cmd {
		args := []string{}
		if !fresh {
			args = append(args, "-c")
		}
		cmd := exec.Command(exe, args...)
		cmd.Dir = dir
		cmd.Env = append(botHostChildEnv(os.Environ()), fakeOmpEnv+"=1", "LASSO_TEST_FAKE_OMP_LOG="+logPath,
			"LASSO_TEST_FAKE_OMP_SESSIONS="+botOmpSessionDir(dir))
		return cmd
	}
	t.Cleanup(func() { botHostCommand = prev })
	return logPath
}

type hostConn struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialHost(t *testing.T, sock string) *hostConn {
	t.Helper()
	var conn net.Conn
	var err error
	for i := 0; i < 100; i++ {
		if conn, err = net.Dial("unix", sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	return &hostConn{t: t, conn: conn, r: bufio.NewReader(conn)}
}

func (c *hostConn) send(v any) {
	b, _ := json.Marshal(v)
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
}

// until reads frames until one of type typ arrives.
func (c *hostConn) until(typ string) map[string]any {
	c.t.Helper()
	_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			c.t.Fatalf("waiting for %s: %v", typ, err)
		}
		var f map[string]any
		if json.Unmarshal(line, &f) == nil && f["type"] == typ {
			return f
		}
	}
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

func TestBotHostPassthroughHelloAndStop(t *testing.T) {
	logPath := fakeOmpHost(t)
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	dir := t.TempDir()
	sock := filepath.Join(t.TempDir(), "bots", "b.sock")
	h := newBotHost("b", dir, "bot", sock)
	code := make(chan int, 1)
	go func() { code <- h.run(true) }()

	c := dialHost(t, sock)
	c.until("host_hello")
	c.send(map[string]any{"type": "prompt", "id": "p1", "message": "ASK me"})
	if r := c.until("response"); r["id"] != "p1" || r["success"] != true {
		t.Fatalf("prompt response = %v", r)
	}
	c.until("extension_ui_request")
	if st, _ := os.Stat(sock); st.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v", st.Mode().Perm())
	}
	// A client that connects while the question is open gets it in hello.
	c.conn.Close()
	c = dialHost(t, sock)
	hello := c.until("host_hello")
	pending, _ := hello["pending"].([]any)
	if len(pending) != 1 || !strings.Contains(fmt.Sprint(pending[0]), "q1") || hello["streaming"] != true {
		t.Fatalf("hello = %v", hello)
	}
	c.send(map[string]any{"type": "extension_ui_response", "id": "q1", "value": "B"})
	c.until("host_ui_answered")
	c.until("agent_end")
	h.mu.Lock()
	left := len(h.pending)
	h.mu.Unlock()
	if left != 0 {
		t.Errorf("%d questions still pending after the answer", left)
	}
	c.send(map[string]any{"type": "host_stop"})
	select {
	case got := <-code:
		if got != 0 {
			t.Errorf("exit = %d", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("host did not stop")
	}
	log := readLog(logPath)
	if strings.Contains(log, "herdr-env-leaked") {
		t.Error("omp saw HERDR_* from the host's environment")
	}
	if !strings.Contains(log, `"abort"`) {
		t.Error("stop did not abort the turn first")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket left behind")
	}
}

func TestBotHostRestartMarker(t *testing.T) {
	logPath := fakeOmpHost(t)
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".lasso"), 0o755)
	sock := filepath.Join(t.TempDir(), "bots", "b.sock")
	h := newBotHost("b", dir, "bot", sock)
	code := make(chan int, 1)
	go func() { code <- h.run(true) }()
	c := dialHost(t, sock)
	if hello := c.until("host_hello"); hello["ready"] != true {
		c.until("ready")
	}
	os.WriteFile(botRestartMarker(dir), nil, 0o644)
	c.until("host_restarting")
	c.until("ready")
	ompWaitFor(t, "a relaunch with -c", func() bool { return strings.Count(readLog(logPath), "argv") == 2 })
	lines := strings.Split(readLog(logPath), "\n")
	var argvs []string
	for _, l := range lines {
		if strings.HasPrefix(l, "argv") {
			argvs = append(argvs, l)
		}
	}
	if len(argvs) != 2 || strings.Contains(argvs[0], "-c") || !strings.Contains(argvs[1], "-c") {
		t.Fatalf("launches = %q", argvs)
	}
	if _, err := os.Stat(botRestartMarker(dir)); !os.IsNotExist(err) {
		t.Error("marker left behind")
	}
	h.stop()
	<-code
}

// noBotLauncher is every test's launcher unless it installs another.
type noBotLauncher struct{}

func (noBotLauncher) start(*botRecord, string, bool) error {
	return fmt.Errorf("tests start no systemd units")
}
func (noBotLauncher) active(string) bool { return false }
func (noBotLauncher) stop(string) error  { return nil }

// inProcessLauncher runs the bot host as a goroutine in place of a systemd unit.
type inProcessLauncher struct {
	mu     sync.Mutex
	hosts  map[string]*botHost
	starts int
	// nextProto, when set, is the protocol the next host started reports.
	nextProto *int
}

func (l *inProcessLauncher) start(r *botRecord, dir string, fresh bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.hosts[r.Name]; ok {
		return errBotRunning
	}
	h := newBotHost(r.Name, dir, r.task(), botSocketPath(r.Name))
	if l.nextProto != nil {
		h.protocol, l.nextProto = *l.nextProto, nil
	}
	l.starts++
	l.hosts[r.Name] = h
	go func() {
		h.run(fresh)
		l.mu.Lock()
		delete(l.hosts, r.Name)
		l.mu.Unlock()
	}()
	return nil
}

func (l *inProcessLauncher) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

func (l *inProcessLauncher) active(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.hosts[name]
	return ok
}

func (l *inProcessLauncher) stop(name string) error {
	l.mu.Lock()
	h := l.hosts[name]
	l.mu.Unlock()
	if h != nil {
		h.stop()
	}
	return nil
}

func useOmpBotEnv(t *testing.T, name string) (*botRecord, string) {
	r, logPath, _ := useOmpBotEnvL(t, name)
	return r, logPath
}

func useOmpBotEnvL(t *testing.T, name string) (*botRecord, string, *inProcessLauncher) {
	t.Helper()
	useBotTestEnv(t)
	logPath := fakeOmpHost(t)
	l := &inProcessLauncher{hosts: map[string]*botHost{}}
	prev := botOmpLaunch
	botOmpLaunch = l
	prevOpen, prevClose, prevPane := botOmpOpenViewer, botOmpCloseViewer, botOmpViewerPane
	botOmpOpenViewer = func(Backend, *botRecord, string) error { return nil }
	botOmpCloseViewer = func(Backend, *botRecord) {}
	botOmpViewerPane = func(Backend, *botRecord) (string, bool) { return "", false }
	t.Cleanup(func() { botOmpOpenViewer, botOmpCloseViewer, botOmpViewerPane = prevOpen, prevClose, prevPane })
	t.Cleanup(func() {
		for n := range l.hosts {
			l.stop(n)
		}
		botOmpLaunch = prev
		ompBots.Lock()
		delete(ompBots.m, name)
		ompBots.Unlock()
		botOmpRelaunches.Lock()
		delete(botOmpRelaunches.m, name)
		botOmpRelaunches.Unlock()
		bots.mu.Lock()
		delete(bots.starting, name)
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
	r, logPath := useOmpBotEnv(t, "ompy")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	if err := startBot(b, r, false); err != errBotRunning {
		t.Errorf("second start = %v", err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	if _, err := os.Stat(filepath.Join(expandTildeOn(b, r.Dir), "AGENTS.md")); err != nil {
		t.Error("no AGENTS.md stub")
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

	// Stop ends it and keeps it down.
	if err := stopBot(b, r); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "stopped", func() bool { return !botOmpRunning(r) })
	got, _ := getBot(r.Name)
	if !got.Stopped || botStatus(b, got, nil).State != "stopped" {
		t.Errorf("after stop: %+v", botStatus(b, got, nil))
	}
	botOmpTick(b, got, time.Now())
	if botOmpLaunch.active(r.Name) {
		t.Error("keep-running relaunched a stopped bot")
	}
	// A stopped bot's firings are dropped.
	if _, st, _ := enqueueBotEvent(j, "run", j.Message, "", time.Now()); st != "dropped" {
		t.Errorf("stopped bot's event = %s", st)
	}
}

func TestOmpBotKeepRunningBacksOff(t *testing.T) {
	r, _ := useOmpBotEnv(t, "phoenix")
	b, _ := botBackend("local")
	var launches int
	prev := botRelaunch
	botRelaunch = func(Backend, *botRecord) error { launches++; return nil }
	t.Cleanup(func() { botRelaunch = prev })
	now := time.Now()
	botOmpTick(b, r, now)
	botOmpTick(b, r, now.Add(5*time.Second))
	botOmpTick(b, r, now.Add(11*time.Second))
	botOmpTick(b, r, now.Add(20*time.Second))
	botOmpTick(b, r, now.Add(32*time.Second))
	if launches != 3 {
		t.Errorf("launches = %d, want 3 (0s, then 10s, then 20s later)", launches)
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
	r := &botRecord{Name: "a", Runtime: "omp", Model: "anthropic/claude-opus-5", Effort: "high", PermissionMode: "always-ask",
		ExtraArgs: []string{"--append-system-prompt", "be brief", "--advisor"},
		MCP:       []botMCPServer{{Name: "mail", Type: "http", URL: "https://m.example/mcp", OAuth: true, Channel: true}}}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	s := botOmpTaskScript(r, "/b/a", []string{"TOKEN"})
	for _, want := range []string{"#MISE secrets=[\"TOKEN\"]", "#MISE interactive=true", "exec omp '--mode' 'rpc-ui' '--session-dir' '/b/a/.lasso/sessions'",
		"'--approval-mode' 'always-ask'", "'--model' 'anthropic/claude-opus-5'", "'--thinking' 'high'", "be brief", "'--advisor' \"$@\""} {
		if !strings.Contains(s, want) {
			t.Errorf("task script lacks %q:\n%s", want, s)
		}
	}
	if !strings.Contains(s, `if [ "${LASSO_BOT_RPC:-}" = fd ]; then exec 0<&3 1>&4 3<&- 4>&-; fi`) {
		t.Errorf("task script does not move the RPC fds:\n%s", s)
	}
	if strings.Contains(s, "herdr") || strings.Contains(s, "dangerously") {
		t.Errorf("task script has claude-pane parts:\n%s", s)
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

func TestOmpBotHostProtocolUpgrade(t *testing.T) {
	r, logPath, l := useOmpBotEnvL(t, "elder")
	b, _ := botBackend("local")
	old := 0
	l.nextProto = &old
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	if v := botStatus(b, r, nil); !v.HostOutdated || v.HostVersion == "" {
		t.Fatalf("old host not reported: %+v", v)
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
	ompWaitFor(t, "the host on this protocol", func() bool {
		v := botStatus(b, r, nil)
		return l.startCount() == 2 && v.State == "idle" && !v.HostOutdated
	})
	if got, _ := getBot(r.Name); got.Stopped {
		t.Error("an upgrade marked the bot stopped")
	}
	launches := strings.Count(readLog(logPath), "argv")
	if launches != 2 || !strings.Contains(readLog(logPath), "argv -c") {
		t.Errorf("launches = %d; log:\n%s", launches, readLog(logPath))
	}
}

func TestAttachDecoder(t *testing.T) {
	var d attachDecoder
	kinds := func(keys []attachKey) string {
		var out []string
		for _, k := range keys {
			if k.kind == "text" {
				out = append(out, "text:"+k.text)
			} else {
				out = append(out, k.kind)
			}
		}
		return strings.Join(out, ",")
	}
	if got := kinds(d.feed([]byte("hé\r"))); got != "text:h,text:é,enter" {
		t.Errorf("typed = %s", got)
	}
	// A paste is one text key with its newlines, even split across reads.
	got := kinds(d.feed([]byte("\x1b[200~line one\r\nline")))
	got += kinds(d.feed([]byte(" two\x1b[201~\r")))
	if got != "text:line one\nline two,enter" {
		t.Errorf("paste = %q", got)
	}
	if got := kinds(d.feed([]byte("\x1b[A\x1b"))); got != "esc" {
		t.Errorf("arrow then esc = %s", got)
	}
	if got := kinds(d.feed([]byte("a\x7f\x15\x03"))); got != "text:a,backspace,clear,interrupt" {
		t.Errorf("controls = %s", got)
	}
}

func TestAttachAnswer(t *testing.T) {
	approve := ompUIRequest{Method: "select", Options: []string{"Approve", "Deny"}}
	if a, ok := attachAnswer(approve, "1"); !ok || a["value"] != "Approve" {
		t.Errorf("1 = %v %v", a, ok)
	}
	if a, ok := attachAnswer(approve, "deny"); !ok || a["value"] != "Deny" {
		t.Errorf("deny = %v %v", a, ok)
	}
	if _, ok := attachAnswer(approve, "maybe"); ok {
		t.Error("free text answered a closed select")
	}
	own := ompUIRequest{Method: "select", Options: []string{"Red", "Other (type your own)"}}
	if a, ok := attachAnswer(own, "teal"); !ok || a["value"] != "teal" {
		t.Errorf("own = %v %v", a, ok)
	}
	if a, ok := attachAnswer(ompUIRequest{Method: "confirm"}, "n"); !ok || a["confirmed"] != false {
		t.Errorf("confirm n = %v %v", a, ok)
	}
}

type fakeAttachHerdr struct {
	mu     sync.Mutex
	states []botAttachState
}

func (f *fakeAttachHerdr) report(st botAttachState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := len(f.states); n == 0 || f.states[n-1] != st {
		f.states = append(f.states, st)
	}
}
func (f *fakeAttachHerdr) release() {}
func (f *fakeAttachHerdr) last() botAttachState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.states) == 0 {
		return botAttachState{}
	}
	return f.states[len(f.states)-1]
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestBotAttachDrivesTheBot(t *testing.T) {
	r, _ := useOmpBotEnv(t, "viewme")
	b, _ := botBackend("local")
	h := &fakeAttachHerdr{}
	out := &syncBuf{}
	a := newBotAttach(r.Name, out)
	a.herdr = h
	// Not running yet: herdr hears unknown, and the viewer waits for it.
	go a.connectLoop()
	ompWaitFor(t, "unknown", func() bool { return h.last().state == "unknown" })
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle with its session", func() bool {
		st := h.last()
		return st.state == "idle" && st.sessionID == "sess-1" && strings.HasSuffix(st.sessionPath, "_sess-1.jsonl")
	})
	// A typed message is a prompt; its answer prints in the pane.
	for _, k := range (&attachDecoder{}).feed([]byte("hello pane\r")) {
		a.key(k)
	}
	ompWaitFor(t, "the answer on screen", func() bool { return strings.Contains(out.String(), "done: hello pane") })
	ompWaitFor(t, "idle again", func() bool { return h.last().state == "idle" })
	// A question blocks the pane until it is answered by number.
	for _, k := range (&attachDecoder{}).feed([]byte("ASK\r")) {
		a.key(k)
	}
	ompWaitFor(t, "blocked", func() bool { return h.last().state == "blocked" && h.last().message == "Pick one" })
	if !strings.Contains(out.String(), "1) A") {
		t.Errorf("question not shown:\n%s", out.String())
	}
	for _, k := range (&attachDecoder{}).feed([]byte("2\r")) {
		a.key(k)
	}
	ompWaitFor(t, "picked B", func() bool { return strings.Contains(out.String(), "picked B") })
	ompWaitFor(t, "idle after the answer", func() bool { return h.last().state == "idle" })
	// The chat sees what the pane did.
	if v := botStatus(b, r, nil); v.LastText != "picked B" {
		t.Errorf("bot's last line = %q", v.LastText)
	}
	a.mu.Lock()
	a.closed = true
	conn := a.conn
	a.mu.Unlock()
	if conn != nil {
		conn.Close()
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

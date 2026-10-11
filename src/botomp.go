package main

// omp bots, lasso's side (docs/design/bots.md, "omp bots"). The bot runs under
// `lasso bot-host` (bothost.go) as the systemd user unit lasso-bot-<name>;
// lasso talks to it over <lassoDir>/bots/<name>.sock in omp's RPC protocol.
// Either lasso sharing lasso.db may connect, watch and send. Only the bot
// loop's runner (bots.runner.lock) delivers job events and relaunches.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func botSocketPath(name string) string { return filepath.Join(lassoDir(), "bots", name+".sock") }
func botUnitName(name string) string   { return "lasso-bot-" + name }

// --- launching the host -------------------------------------------------------

// botOmpLauncher starts and stops a bot's host. The systemd one is the real
// thing; tests run the host in-process.
type botOmpLauncher interface {
	start(r *botRecord, dir string, fresh bool) error
	// active reports whether the host is up or coming up.
	active(name string) bool
	stop(name string) error
}

var botOmpLaunch botOmpLauncher = systemdBotLauncher{}

type systemdBotLauncher struct{}

// botHasUserManager: a systemd user manager to start the unit under.
func botHasUserManager() error {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return errors.New("an omp bot runs as a systemd user unit, and lasso has no user manager to ask (XDG_RUNTIME_DIR is unset)")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return errors.New("an omp bot runs as a systemd user unit, and systemd-run is not installed")
	}
	return nil
}

func (systemdBotLauncher) start(r *botRecord, dir string, fresh bool) error {
	if err := botHasUserManager(); err != nil {
		return err
	}
	args := []string{"--user", "--unit", botUnitName(r.Name), "--collect", "--quiet",
		"--description", "lasso bot " + r.Name,
		"--property", "KillMode=control-group", "--property", "TimeoutStopSec=15",
		"--working-directory", dir}
	// A systemd user unit's environment, as a watch command gets
	// (botCommandEnv): never lasso's credentials.
	for _, kv := range botCommandEnv(os.Environ(), r.Name, "") {
		if strings.HasPrefix(kv, "LASSO_JOB=") {
			continue
		}
		args = append(args, "--setenv", kv)
	}
	args = append(args, "--", botLassoExe(), "bot-host", "--bot", r.Name, "--dir", dir,
		"--socket", botSocketPath(r.Name), "--task", r.task())
	if fresh {
		args = append(args, "--fresh")
	}
	out, err := exec.Command("systemd-run", args...).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "already loaded") {
			return errBotRunning
		}
		return fmt.Errorf("systemd-run: %s", firstNonEmpty(msg, err.Error()))
	}
	return nil
}

func (systemdBotLauncher) active(name string) bool {
	out, _ := exec.Command("systemctl", "--user", "is-active", botUnitName(name)).Output()
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading", "deactivating":
		return true
	}
	return false
}

func (systemdBotLauncher) stop(name string) error {
	out, err := exec.Command("systemctl", "--user", "stop", botUnitName(name)).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "not loaded") {
		return fmt.Errorf("systemctl stop: %s", firstNonEmpty(strings.TrimSpace(string(out)), err.Error()))
	}
	return nil
}

// --- the connection ----------------------------------------------------------

// ompUIRequest is a question omp is waiting on: the ask tool, or an approval.
type ompUIRequest struct {
	ID          string   `json:"id"`
	Method      string   `json:"method"` // select | confirm | input | editor
	Title       string   `json:"title"`
	Message     string   `json:"message,omitempty"`
	Options     []string `json:"options,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
}

type ompModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	ContextWindow int64  `json:"context_window,omitempty"`
}

// selector is the model as a --model value: provider/id.
func (m ompModelInfo) selector() string {
	if m.ID == "" {
		return ""
	}
	if m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

type ompContextUsage struct {
	Tokens  int64   `json:"tokens"`
	Window  int64   `json:"window,omitempty"`
	Percent float64 `json:"percent,omitempty"`
}

type ompResponse struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

type ompBot struct {
	name string

	mu        sync.Mutex
	conn      net.Conn
	gen       int // bumped per connection
	ready     bool
	streaming bool
	pending   []ompUIRequest
	sessionID string
	exit      string // the host's last exit, when omp ended on its own
	upSince   time.Time
	waiters   map[string]chan ompResponse
	delivery  bool // the job delivery loop runs (runner only)
	// What the host said in host_hello: its protocol (0 for a host from
	// before there was one) and the lasso version it runs.
	hello       bool
	hostProto   int
	hostVersion string
	upgrading   bool
	// What omp says it runs (get_state): the model, its thinking levels, the
	// current level and how full the context is. modelKnown is set by the
	// first reading on a connection, the baseline a change is measured from.
	model      ompModelInfo
	thinking   string
	efforts    []string
	context    ompContextUsage
	modelKnown bool
	// opMu keeps a job delivery and a host upgrade from interleaving, so an
	// upgrade never stops omp just after it accepted a prompt.
	opMu     sync.Mutex
	dialMu   sync.Mutex
	lastDial time.Time
}

var ompBots = struct {
	sync.Mutex
	m map[string]*ompBot
}{m: map[string]*ompBot{}}

var ompSeq atomic.Uint64

func ompBotFor(name string) *ompBot {
	ompBots.Lock()
	defer ompBots.Unlock()
	ob, ok := ompBots.m[name]
	if !ok {
		ob = &ompBot{name: name, waiters: map[string]chan ompResponse{}}
		ompBots.m[name] = ob
	}
	return ob
}

func (ob *ompBot) connected() bool {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.conn != nil
}

// ensure connects when not connected. Cheap to call often: a failed dial is
// not retried within a second.
func (ob *ompBot) ensure() bool {
	if ob.connected() {
		return true
	}
	ob.dialMu.Lock()
	defer ob.dialMu.Unlock()
	if ob.connected() {
		return true
	}
	if time.Since(ob.lastDial) < time.Second {
		return false
	}
	ob.lastDial = time.Now()
	conn, err := net.DialTimeout("unix", botSocketPath(ob.name), time.Second)
	if err != nil {
		return false
	}
	ob.mu.Lock()
	ob.conn = conn
	ob.gen++
	gen := ob.gen
	ob.mu.Unlock()
	go ob.read(conn, gen)
	return true
}

func (ob *ompBot) read(conn net.Conn, gen int) {
	r := bufio.NewReaderSize(conn, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			ob.observe(line)
		}
		if err != nil {
			break
		}
	}
	conn.Close()
	ob.mu.Lock()
	if ob.gen == gen {
		ob.conn = nil
		ob.hello = false
		ob.modelKnown = false
		ob.ready, ob.streaming = false, false
		ob.pending = nil
		for id, w := range ob.waiters {
			close(w)
			delete(ob.waiters, id)
		}
	}
	ob.mu.Unlock()
	ompChatNudge(ob.name)
}

type ompFrame struct {
	Type       string            `json:"type"`
	ID         string            `json:"id"`
	Method     string            `json:"method"`
	Title      string            `json:"title"`
	Message    string            `json:"message"`
	Options    []string          `json:"options"`
	Holder     string            `json:"placeholder"`
	TargetID   string            `json:"targetId"`
	IsTerminal *bool             `json:"isTerminal"`
	Success    bool              `json:"success"`
	Error      string            `json:"error"`
	Data       json.RawMessage   `json:"data"`
	Ready      bool              `json:"ready"`
	Streaming  bool              `json:"streaming"`
	Pending    []json.RawMessage `json:"pending"`
	Protocol   int               `json:"protocol"`
	Version    string            `json:"version"`
	Code       *int              `json:"code"`
	Stderr     []string          `json:"stderr"`
}

func (f ompFrame) uiRequest() (ompUIRequest, bool) {
	switch f.Method {
	case "select", "confirm", "input", "editor":
	default:
		return ompUIRequest{}, false
	}
	return ompUIRequest{ID: f.ID, Method: f.Method, Title: f.Title, Message: f.Message, Options: f.Options, Placeholder: f.Holder}, f.ID != ""
}

func (ob *ompBot) observe(line []byte) {
	var f ompFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	nudge, askState, turnEnd := false, false, false
	ob.mu.Lock()
	switch f.Type {
	case "host_hello":
		ob.hello, ob.hostProto, ob.hostVersion = true, f.Protocol, f.Version
		ob.ready, ob.streaming, ob.pending = f.Ready, f.Streaming, nil
		for _, raw := range f.Pending {
			var p ompFrame
			if json.Unmarshal(raw, &p) == nil {
				if q, ok := p.uiRequest(); ok {
					ob.pending = append(ob.pending, q)
				}
			}
		}
		ob.exit = ""
		if ob.upSince.IsZero() {
			ob.upSince = time.Now()
		}
		askState, nudge = f.Ready, true
	case "ready":
		ob.ready, ob.exit = true, ""
		ob.upSince = time.Now()
		askState, nudge = true, true
	case "agent_start":
		ob.streaming, nudge = true, true
	case "agent_end":
		if f.IsTerminal == nil || *f.IsTerminal {
			ob.streaming = false
			turnEnd, askState, nudge = true, true, true
		}
	case "message_end", "tool_execution_end":
		nudge = true
	case "model_changed", "thinking_level_changed":
		// Whoever changed it (lasso, the bot's herdr pane, the bot itself):
		// read it back and keep the bot's settings in step.
		askState, nudge = true, true
	case "extension_ui_request":
		if q, ok := f.uiRequest(); ok {
			ob.dropPending(q.ID)
			ob.pending = append(ob.pending, q)
			nudge = true
		} else if f.Method == "cancel" {
			ob.dropPending(f.TargetID)
			ob.dropPending(f.ID)
			nudge = true
		}
	case "host_ui_answered":
		ob.dropPending(f.ID)
		nudge = true
	case "host_restarting":
		ob.ready, ob.streaming, ob.pending = false, false, nil
		nudge = true
	case "host_exit":
		ob.ready, ob.streaming, ob.pending = false, false, nil
		if f.Code != nil && *f.Code != 0 {
			ob.exit = fmt.Sprintf("omp exited with status %d", *f.Code)
			if n := len(f.Stderr); n > 0 {
				ob.exit += ": " + f.Stderr[n-1]
			}
		}
		nudge = true
	case "response":
		if w, ok := ob.waiters[f.ID]; ok {
			delete(ob.waiters, f.ID)
			w <- ompResponse{Success: f.Success, Error: f.Error, Data: f.Data}
			close(w)
		}
	}
	ob.mu.Unlock()
	if askState {
		go ob.refreshState()
	}
	if turnEnd {
		botOmpTurnEnded(ob.name)
	}
	if nudge {
		ompChatNudge(ob.name)
	}
}

func (ob *ompBot) dropPending(id string) {
	if id == "" {
		return
	}
	for i, q := range ob.pending {
		if q.ID == id {
			ob.pending = append(ob.pending[:i:i], ob.pending[i+1:]...)
			return
		}
	}
}

// refreshState asks omp for its session id (it changes with a new session).
func (ob *ompBot) refreshState() {
	resp, err := ob.call(map[string]any{"type": "get_state"}, 10*time.Second)
	if err != nil || !resp.Success {
		return
	}
	var st struct {
		SessionID     string `json:"sessionId"`
		IsStreaming   bool   `json:"isStreaming"`
		ThinkingLevel string `json:"thinkingLevel"`
		Model         *struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			Provider      string `json:"provider"`
			ContextWindow int64  `json:"contextWindow"`
			Thinking      struct {
				Efforts []string `json:"efforts"`
			} `json:"thinking"`
		} `json:"model"`
		ContextUsage *struct {
			Tokens        int64   `json:"tokens"`
			ContextWindow int64   `json:"contextWindow"`
			Percent       float64 `json:"percent"`
		} `json:"contextUsage"`
	}
	if json.Unmarshal(resp.Data, &st) != nil {
		return
	}
	ob.mu.Lock()
	ob.sessionID = st.SessionID
	ob.streaming = st.IsStreaming
	prevModel, prevThinking, known := ob.model.selector(), ob.thinking, ob.modelKnown
	if st.Model != nil {
		ob.model = ompModelInfo{ID: st.Model.ID, Name: st.Model.Name, Provider: st.Model.Provider, ContextWindow: st.Model.ContextWindow}
		ob.efforts = st.Model.Thinking.Efforts
	}
	ob.thinking = st.ThinkingLevel
	if st.ContextUsage != nil {
		ob.context = ompContextUsage{Tokens: st.ContextUsage.Tokens, Window: st.ContextUsage.ContextWindow, Percent: st.ContextUsage.Percent}
	} else {
		ob.context = ompContextUsage{Window: ob.model.ContextWindow}
	}
	ob.modelKnown = true
	model, thinking := ob.model.selector(), ob.thinking
	ob.mu.Unlock()
	// A change after the first reading is the bot's settings' too, so a
	// restart keeps it. The first reading is only the baseline: at launch omp
	// resolves the bot's own --model, which may be an alias or the default.
	if known && (model != prevModel || thinking != prevThinking) {
		go botOmpPersistModel(ob.name, model, thinking, model != prevModel, thinking != prevThinking)
	}
	ompChatNudge(ob.name)
}

// botOmpPersistModel writes a model or thinking change omp reported into the
// bot's row and launch task.
// botOmpPersistMu: a model change and a thinking change arrive as two events
// and two readings; saved concurrently, the second would start from the row
// as it was before the first and put the old model back.
var botOmpPersistMu sync.Mutex

func botOmpPersistModel(name, model, thinking string, modelChanged, thinkingChanged bool) {
	botOmpPersistMu.Lock()
	defer botOmpPersistMu.Unlock()
	r, err := getBot(name)
	if err != nil || !r.isOmp() {
		return
	}
	next := *r
	if modelChanged && model != "" {
		next.Model = model
	}
	if thinkingChanged && thinking != "" {
		if e := normalizeEffort("omp", thinking); e != "" {
			next.Effort = e
		}
	}
	if next.Model == r.Model && next.Effort == r.Effort {
		return
	}
	if err := next.normalize(); err != nil {
		log.Printf("bots:     %s: keep model %q / thinking %q: %v", name, model, thinking, err)
		return
	}
	if err := updateBot(&next); err != nil {
		return
	}
	if b, err := botBackend(next.Host); err == nil {
		_ = botWriteTask(b, &next, expandTildeOn(b, next.Dir))
	}
	log.Printf("bots:     %s: now model %s, thinking %s", name, next.Model, next.Effort)
}

// botOmpSetModel changes a running omp bot's model and thinking level now,
// over RPC. "" leaves that one alone.
func botOmpSetModel(name, model, thinking string) error {
	ob := ompBotFor(name)
	if model != "" {
		provider, id, ok := strings.Cut(model, "/")
		if !ok || provider == "" || id == "" {
			return fmt.Errorf("an omp model is provider/model, not %q", model)
		}
		resp, err := ob.call(map[string]any{"type": "set_model", "provider": provider, "modelId": id}, 15*time.Second)
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("omp refused the model: %s", resp.Error)
		}
	}
	if thinking != "" {
		resp, err := ob.call(map[string]any{"type": "set_thinking_level", "level": thinking}, 15*time.Second)
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("omp refused the thinking level: %s", resp.Error)
		}
	}
	return nil
}

var errOmpNotConnected = errors.New("the bot is not running")

// send writes one command without waiting for its response.
func (ob *ompBot) send(cmd map[string]any) error {
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	ob.mu.Lock()
	conn := ob.conn
	ob.mu.Unlock()
	if conn == nil {
		return errOmpNotConnected
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(append(b, '\n'))
	return err
}

// call sends a command with a fresh id and waits for omp's response to it.
func (ob *ompBot) call(cmd map[string]any, timeout time.Duration) (ompResponse, error) {
	if !ob.ensure() {
		return ompResponse{}, errOmpNotConnected
	}
	id := fmt.Sprintf("lasso-%d-%d", os.Getpid(), ompSeq.Add(1))
	cmd["id"] = id
	w := make(chan ompResponse, 1)
	ob.mu.Lock()
	ob.waiters[id] = w
	ob.mu.Unlock()
	if err := ob.send(cmd); err != nil {
		ob.mu.Lock()
		delete(ob.waiters, id)
		ob.mu.Unlock()
		return ompResponse{}, err
	}
	select {
	case resp, ok := <-w:
		if !ok {
			return ompResponse{}, errOmpNotConnected
		}
		return resp, nil
	case <-time.After(timeout):
		ob.mu.Lock()
		delete(ob.waiters, id)
		ob.mu.Unlock()
		return ompResponse{}, fmt.Errorf("omp did not answer %v within %s", cmd["type"], timeout)
	}
}

type ompSnapshot struct {
	connected, ready, streaming bool
	model                       ompModelInfo
	thinking                    string
	efforts                     []string
	context                     ompContextUsage
	hello                       bool
	hostProto                   int
	hostVersion                 string
	pending                     []ompUIRequest
	sessionID, exit             string
	upSince                     time.Time
}

func (ob *ompBot) snapshot() ompSnapshot {
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ompSnapshot{
		connected: ob.conn != nil, ready: ob.ready, streaming: ob.streaming,
		hello: ob.hello, hostProto: ob.hostProto, hostVersion: ob.hostVersion,
		model: ob.model, thinking: ob.thinking, efforts: append([]string(nil), ob.efforts...), context: ob.context,
		pending: append([]ompUIRequest(nil), ob.pending...), sessionID: ob.sessionID,
		exit: ob.exit, upSince: ob.upSince,
	}
}

// --- the actions a human or the loop takes ------------------------------------

// botOmpSend gives the bot a message: a prompt, or while it works, a steer
// (the human's words land mid-run, as typing into a terminal agent would) or a
// follow-up (a job, which waits for the run to end).
func botOmpSend(name, text string, steer bool) error {
	ob := ompBotFor(name)
	cmd := map[string]any{"type": "prompt", "message": text}
	if ob.snapshot().streaming {
		if steer {
			cmd["streamingBehavior"] = "steer"
		} else {
			cmd["streamingBehavior"] = "followUp"
		}
	}
	resp, err := ob.call(cmd, 30*time.Second)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("omp refused the message: %s", resp.Error)
	}
	// Working from the moment omp accepts a turn, not from its agent_start a
	// beat later, so a poll in between does not read idle. A local slash
	// command runs no turn (agentInvoked false) and gets no agent_end.
	var ack struct {
		AgentInvoked *bool `json:"agentInvoked"`
	}
	_ = json.Unmarshal(resp.Data, &ack)
	if ack.AgentInvoked == nil || *ack.AgentInvoked {
		ob.mu.Lock()
		ob.streaming = true
		ob.mu.Unlock()
	}
	return nil
}

func botOmpAbort(name string) error {
	resp, err := ompBotFor(name).call(map[string]any{"type": "abort"}, 15*time.Second)
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("omp refused to stop: %s", resp.Error)
	}
	return nil
}

// botOmpAnswer answers a question omp is waiting on. Exactly one of value,
// confirmed or cancel is meant; an approval is a select answered by option
// label (Approve/Deny).
func botOmpAnswer(name, id string, value *string, confirmed *bool, cancel bool) error {
	ob := ompBotFor(name)
	if !ob.ensure() {
		return errOmpNotConnected
	}
	found := false
	for _, q := range ob.snapshot().pending {
		if q.ID == id {
			found = true
		}
	}
	if !found {
		return errors.New("that question is no longer open")
	}
	msg := map[string]any{"type": "extension_ui_response", "id": id}
	switch {
	case cancel:
		msg["cancelled"] = true
	case confirmed != nil:
		msg["confirmed"] = *confirmed
	case value != nil:
		msg["value"] = *value
	default:
		return errors.New("answer needs a value, confirmed or cancelled")
	}
	return ob.send(msg)
}

// --- lifecycle ------------------------------------------------------------------

func botOmpRunning(r *botRecord) bool {
	return ompBotFor(r.Name).ensure() || botOmpLaunch.active(r.Name)
}

func botOmpStart(b Backend, r *botRecord, fresh bool) error {
	if botOmpRunning(r) {
		return errBotRunning
	}
	if err := botMaterialize(b, r); err != nil {
		return err
	}
	dir := expandTildeOn(b, r.Dir)
	bots.mu.Lock()
	bots.starting[r.Name] = time.Now()
	bots.mu.Unlock()
	_ = setBotStopped(r.Name, false)
	if err := botOmpLaunch.start(r, dir, fresh); err != nil {
		bots.mu.Lock()
		delete(bots.starting, r.Name)
		bots.mu.Unlock()
		return err
	}
	// The bot in herdr. A failure costs only the pane: the bot runs, and
	// its chat works, without one.
	if err := botOmpOpenViewer(b, r, dir); err != nil {
		log.Printf("bots:     %s: open its herdr pane: %v", r.Name, err)
	}
	return nil
}

// botOmpOpenViewer opens the bot's herdr pane, `lasso bot-attach <name>` in
// a tab of its workspace, when it has none. Typed with this binary's own path
// (the lasso on PATH may be an older one); herdr restores it with the plain
// `lasso bot-attach <name>` the viewer reports. A seam for tests.
var botOmpOpenViewer = func(b Backend, r *botRecord, dir string) error {
	if _, ok := findBotPane(b, r); ok {
		return nil
	}
	paneID, err := openBotTab(b, r, dir)
	if err != nil {
		return err
	}
	invalidatePaneList(b.Name())
	waitPaneReady(b, paneID)
	return paneRun(b, paneID, shellQuote(botLassoExe())+" bot-attach "+r.Name)
}

// botOmpCloseViewer closes the bot's herdr pane. A seam for tests.
var botOmpCloseViewer = func(b Backend, r *botRecord) {
	if p, ok := findBotPane(b, r); ok {
		if _, err := b.HerdrCall("pane.close", map[string]any{"pane_id": p.PaneID}); err == nil {
			invalidatePaneList(b.Name())
		}
	}
}

func botOmpStop(r *botRecord) error {
	if b, err := botBackend(r.Host); err == nil {
		defer botOmpCloseViewer(b, r)
	}
	if err := setBotStopped(r.Name, true); err != nil {
		return err
	}
	ob := ompBotFor(r.Name)
	if ob.ensure() {
		_ = ob.send(map[string]any{"type": "host_stop"})
	}
	bots.mu.Lock()
	delete(bots.starting, r.Name)
	bots.mu.Unlock()
	// The host stops omp itself; the unit stop is the backstop for a host
	// that is wedged or never got its socket up.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && botOmpLaunch.active(r.Name) {
		time.Sleep(200 * time.Millisecond)
	}
	if botOmpLaunch.active(r.Name) {
		return botOmpLaunch.stop(r.Name)
	}
	return nil
}

func botOmpRestart(b Backend, r *botRecord, fresh bool) error {
	if err := botOmpStop(r); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && botOmpRunning(r) {
		time.Sleep(300 * time.Millisecond)
	}
	return botOmpStart(b, r, fresh)
}

// --- status -------------------------------------------------------------------

func botOmpStatus(b Backend, r *botRecord) botView {
	v := botView{botRecord: r, State: "stopped", DirPath: expandTildeOn(b, r.Dir)}
	ob := ompBotFor(r.Name)
	ob.ensure()
	s := ob.snapshot()
	v.SessionID = s.sessionID
	if p, ok := botOmpViewerPane(b, r); ok {
		v.PaneID = p
	}
	if s.connected && s.model.ID != "" {
		m := s.model
		v.LiveModel = &m
		v.Thinking = s.thinking
		// omp's own selectors are off and auto, beside the model's rungs.
		v.ThinkingLevels = append(append([]string{"off"}, s.efforts...), "auto")
		c := s.context
		v.Context = &c
	}
	if s.connected && s.hello {
		v.HostVersion = s.hostVersion
		v.HostOutdated = s.hostProto != botHostProtocol
	}
	switch {
	case !s.connected:
		bots.mu.Lock()
		t, starting := bots.starting[r.Name]
		bots.mu.Unlock()
		if (starting && time.Since(t) < botStartGrace) || botOmpLaunch.active(r.Name) {
			v.State = "starting"
		} else if s.exit != "" && !r.Stopped {
			v.Error = s.exit
		}
	case len(s.pending) > 0:
		v.State = "blocked"
		v.WaitingFor = ompFirstLine(firstNonEmpty(s.pending[0].Title, s.pending[0].Message, "a question"))
	case !s.ready:
		v.State = "starting"
	case s.streaming:
		v.State = "working"
	default:
		v.State = "idle"
	}
	v.LastText, v.LastKind, v.LastAt = botOmpLastLine(b, r)
	return v
}

func ompFirstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return previewText(s)
}

// botOmpSessionFile is the bot's current session: omp names each file
// <time>_<session id>.jsonl in the session dir. Without an id (the bot is not
// connected) it is the newest file, which is the one -c continues.
func botOmpSessionFile(b Backend, r *botRecord) string {
	dir := botOmpSessionDir(expandTildeOn(b, r.Dir))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	sid := ompBotFor(r.Name).snapshot().sessionID
	type cand struct {
		name string
		mod  time.Time
	}
	var all []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if sid != "" && strings.HasSuffix(e.Name(), "_"+sid+".jsonl") {
			return filepath.Join(dir, e.Name())
		}
		if info, err := e.Info(); err == nil {
			all = append(all, cand{e.Name(), info.ModTime()})
		}
	}
	if len(all) == 0 {
		return ""
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mod.After(all[j].mod) })
	return filepath.Join(dir, all[0].name)
}

// botOmpChat is the bot's chat payload, read from its session file.
func botOmpChat(b Backend, r *botRecord, before string) chatPayload {
	// PaneID names the bot, not a pane: the chat view keys its rows and echoes
	// by it, and every action on them goes to /api/bots/<name>/chat.
	out := chatPayload{PaneID: "bot:" + r.Name, Agent: "omp", Host: r.Host, Title: r.Name, Cwd: expandTildeOn(b, r.Dir)}
	s := ompBotFor(r.Name).snapshot()
	path := botOmpSessionFile(b, r)
	if path == "" {
		out.Starting = s.connected
		out.Note = "No conversation yet."
		return out
	}
	info, err := b.Stat(path)
	if err != nil {
		out.Note = "The session file could not be read."
		return out
	}
	run, _ := fillChatLog(&out, b, path, "omp", info.Size(), before, false)
	out.Running = run || s.streaming
	return out
}

// botOmpLastLine is the newest prose row of the bot's chat, cached per
// session file and mtime.
func botOmpLastLine(b Backend, r *botRecord) (string, string, string) {
	path := botOmpSessionFile(b, r)
	if path == "" {
		return botCachedLine(r.Name)
	}
	info, err := b.Stat(path)
	if err != nil {
		return botCachedLine(r.Name)
	}
	key := fmt.Sprintf("omp\x00%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
	bots.mu.Lock()
	if l, ok := bots.lines[r.Name]; ok && l.key == key {
		bots.mu.Unlock()
		return l.text, l.kind, l.at
	}
	bots.mu.Unlock()
	var payload chatPayload
	fillChatLog(&payload, b, path, "omp", info.Size(), "", false)
	line := botLastLine{key: key}
	for i := len(payload.Items) - 1; i >= 0; i-- {
		it := payload.Items[i]
		if it.Text == "" || it.Thinking || (it.Kind != "agent" && it.Kind != "user") {
			continue
		}
		line.text, line.kind, line.at = previewText(it.Text), it.Kind, it.At
		break
	}
	bots.mu.Lock()
	bots.lines[r.Name] = line
	bots.mu.Unlock()
	return line.text, line.kind, line.at
}

// ompChatNudge tells open chats of this bot to refetch now rather than on
// their next poll.
func ompChatNudge(name string) {
	if srvHub != nil {
		srvHub.broadcast("bot-chat", map[string]string{"bot": name})
	}
}

// botOmpTurnEnded runs when a turn ends: the runner pushes the bot's answer.
func botOmpTurnEnded(name string) {
	bots.mu.Lock()
	runner := bots.runLock != nil
	bots.mu.Unlock()
	if !runner {
		return
	}
	go func() {
		r, err := getBot(name)
		if err != nil || !r.Notify {
			return
		}
		b, err := botBackend(r.Host)
		if err != nil {
			return
		}
		// The session file holds the answer once omp has written it.
		time.Sleep(300 * time.Millisecond)
		botNotifyCheck(b, r, nil)
	}()
}

// --- the loop (runner only) -----------------------------------------------------

const (
	botOmpBackoffMin = 10 * time.Second
	botOmpBackoffMax = 5 * time.Minute
	// botOmpHealthy: a bot up this long has its relaunch backoff reset.
	botOmpHealthy = 10 * time.Minute
)

type botOmpRelaunch struct {
	next  time.Time
	delay time.Duration
}

var botOmpRelaunches = struct {
	sync.Mutex
	m map[string]*botOmpRelaunch
}{m: map[string]*botOmpRelaunch{}}

// botOmpTick is one loop pass over an omp bot: keep-running with backoff,
// the notification check and the job delivery loop.
func botOmpTick(b Backend, r *botRecord, now time.Time) {
	ob := ompBotFor(r.Name)
	if ob.ensure() {
		s := ob.snapshot()
		if s.ready {
			bots.mu.Lock()
			delete(bots.starting, r.Name)
			bots.mu.Unlock()
		}
		if !s.upSince.IsZero() && now.Sub(s.upSince) > botOmpHealthy {
			botOmpRelaunches.Lock()
			delete(botOmpRelaunches.m, r.Name)
			botOmpRelaunches.Unlock()
		}
		ob.startDelivery()
		if r.Notify {
			botNotifyCheck(b, r, nil)
		}
		if s.hello && s.hostProto != botHostProtocol {
			go botOmpUpgrade(b, r)
		}
		botOmpKeepViewer(b, r)
		return
	}
	if r.Stopped || !r.KeepRunning || botOmpLaunch.active(r.Name) {
		return
	}
	bots.mu.Lock()
	started, isStarting := bots.starting[r.Name]
	bots.mu.Unlock()
	if isStarting && now.Sub(started) < botStartGrace {
		return
	}
	botOmpRelaunches.Lock()
	rl := botOmpRelaunches.m[r.Name]
	if rl == nil {
		rl = &botOmpRelaunch{}
		botOmpRelaunches.m[r.Name] = rl
	}
	due := now.After(rl.next) || now.Equal(rl.next)
	if due {
		rl.delay = min(max(rl.delay*2, botOmpBackoffMin), botOmpBackoffMax)
		rl.next = now.Add(rl.delay)
	}
	botOmpRelaunches.Unlock()
	if !due {
		return
	}
	log.Printf("bots:     %s is not running; relaunching (keep running)", r.Name)
	if err := botRelaunch(b, r); err != nil && !errors.Is(err, errBotRunning) {
		log.Printf("bots:     relaunch %s: %v", r.Name, err)
	}
}

// startDelivery runs the bot's job delivery loop once per connection life.
func (ob *ompBot) startDelivery() {
	ob.mu.Lock()
	if ob.delivery {
		ob.mu.Unlock()
		return
	}
	ob.delivery = true
	ob.mu.Unlock()
	go ob.deliverLoop()
}

// deliverLoop hands the bot its job events as prompts: claim, prompt, ack.
// A prompt omp did not accept leaves the events claimed, and claimBotEvents
// hands them out again after its timeout: at least once, like the channel.
// It ends when the bot disconnects; the next tick starts it again.
func (ob *ompBot) deliverLoop() {
	defer func() {
		ob.mu.Lock()
		ob.delivery = false
		ob.mu.Unlock()
	}()
	for {
		s := ob.snapshot()
		if !s.connected {
			return
		}
		if s.ready {
			now := time.Now()
			botChannelSeen(ob.name, now)
			botOmpDeliver(ob.name, now)
		}
		select {
		case <-botEventWaiter(ob.name):
		case <-time.After(2 * time.Second):
		}
	}
}

func botOmpDeliver(name string, now time.Time) {
	ob := ompBotFor(name)
	ob.opMu.Lock()
	defer ob.opMu.Unlock()
	events, err := claimBotEvents(name, now)
	if err != nil || len(events) == 0 {
		return
	}
	var ids []int64
	for _, e := range events {
		if err := botOmpSend(name, botOmpEventMessage(e), false); err != nil {
			log.Printf("bots:     %s: deliver event %d: %v", name, e.ID, err)
			break
		}
		ids = append(ids, e.ID)
	}
	if len(ids) > 0 {
		if err := ackBotEvents(name, ids, time.Now()); err != nil {
			log.Printf("bots:     %s: ack events: %v", name, err)
		}
	}
}

// botOmpUpgrade restarts an idle bot whose host speaks another protocol, so
// the host is this lasso's binary again. Only when idle: nothing is waiting
// on the human and no turn is running, and the session continues (-c). It is
// not a Stop: the bot is never marked stopped, so a lasso that dies midway
// leaves keep-running to bring it back.
func botOmpUpgrade(b Backend, r *botRecord) {
	ob := ompBotFor(r.Name)
	ob.mu.Lock()
	if ob.upgrading {
		ob.mu.Unlock()
		return
	}
	ob.upgrading = true
	ob.mu.Unlock()
	defer func() {
		ob.mu.Lock()
		ob.upgrading = false
		ob.mu.Unlock()
	}()
	ob.opMu.Lock()
	defer ob.opMu.Unlock()
	s := ob.snapshot()
	if !s.connected || !s.ready || s.streaming || len(s.pending) > 0 || s.hostProto == botHostProtocol {
		return
	}
	log.Printf("bots:     %s: its host speaks protocol %d (lasso %s), this lasso %d; restarting it on this binary", r.Name, s.hostProto, s.hostVersion, botHostProtocol)
	_ = ob.send(map[string]any{"type": "host_stop"})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (botOmpLaunch.active(r.Name) || ob.connected()) {
		time.Sleep(200 * time.Millisecond)
	}
	if botOmpLaunch.active(r.Name) {
		if err := botOmpLaunch.stop(r.Name); err != nil {
			log.Printf("bots:     %s: upgrade: %v", r.Name, err)
			return
		}
	}
	if err := botOmpStart(b, r, false); err != nil && !errors.Is(err, errBotRunning) {
		log.Printf("bots:     %s: upgrade: start: %v", r.Name, err)
	}
}

// botOmpViewerPane is the bot's herdr pane, if it has one. A seam for tests.
var botOmpViewerPane = func(b Backend, r *botRecord) (string, bool) {
	p, ok := findBotPane(b, r)
	return p.PaneID, ok
}

// botOmpKeepViewer reopens a keep-running bot's herdr pane once it has been
// missing for as long as a claude bot's pane may be before a relaunch, which
// gives herdr's own restore (resume_argv) the first chance.
func botOmpKeepViewer(b Backend, r *botRecord) {
	if !r.KeepRunning {
		return
	}
	if _, ok := botOmpViewerPane(b, r); ok {
		bots.mu.Lock()
		bots.misses[r.Name] = 0
		bots.mu.Unlock()
		return
	}
	bots.mu.Lock()
	bots.misses[r.Name]++
	due := bots.misses[r.Name] >= botMissesBeforeRelaunch
	if due {
		bots.misses[r.Name] = 0
	}
	bots.mu.Unlock()
	if !due {
		return
	}
	if err := botOmpOpenViewer(b, r, expandTildeOn(b, r.Dir)); err != nil {
		log.Printf("bots:     %s: reopen its herdr pane: %v", r.Name, err)
	}
}

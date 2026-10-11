package main

// `lasso bot-attach <name>`: an omp bot in a herdr pane (docs/design/bots.md,
// "omp bots in herdr"). The bot itself runs headless under its bot host; this
// is a viewer that makes it a herdr agent like any other:
//
//   - It prints the conversation as plain lines on the normal screen, so
//     `herdr agent read` (and a human looking at the pane) sees it.
//   - Typed or pasted input becomes a prompt (a steer while the bot works), so
//     `herdr agent prompt` reaches it; Esc aborts the turn; a question or an
//     approval is answered by number, so `herdr agent send-keys 1 Enter` works.
//   - It reports the bot's state, session and resume command to herdr
//     (pane.report_agent, source lasso:bot), so the sidebar, `agent wait` and
//     herdr's restore all work. herdr restores the pane by running this again;
//     the bot host, a separate systemd unit, never stopped.
//
// Ctrl-C detaches the viewer; the bot keeps running.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	botAttachSource = "lasso:bot"
	// botAttachAgent is the agent herdr is told runs in the pane: omp, which
	// is what it is. herdr prompts only an agent it knows, whose process is the
	// pane's foreground, so the viewer runs with HERDR_AGENT=omp (herdr's hint
	// for a wrapper). herdr's own omp resume never runs here: the resume_argv
	// we report with every state wins, and it reruns this viewer, not omp.
	botAttachAgent = "omp"
)

func cliBotAttach(args []string) {
	name := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--bot" && i+1 < len(args):
			name = args[i+1]
			i++
		case strings.HasPrefix(a, "--bot="):
			name = strings.TrimPrefix(a, "--bot=")
		case !strings.HasPrefix(a, "-") && name == "":
			name = a
		}
	}
	if !botNameRE.MatchString(name) {
		fmt.Fprintln(os.Stderr, "usage: lasso bot-attach <bot name>")
		os.Exit(2)
	}
	// herdr reads HERDR_AGENT from the foreground process's own environment,
	// so it is set by exec'ing again rather than with Setenv. In place: the
	// pid herdr sees in the pane stays this one.
	if os.Getenv("HERDR_PANE_ID") != "" && os.Getenv("HERDR_AGENT") != botAttachAgent {
		if exe, err := os.Executable(); err == nil {
			_ = syscall.Exec(exe, os.Args, append(os.Environ(), "HERDR_AGENT="+botAttachAgent))
		}
	}
	a := newBotAttach(name, os.Stdout)
	a.herdr = newBotAttachHerdr(name)
	a.width = attachTermWidth
	os.Exit(a.run(os.Stdin))
}

// --- herdr ---------------------------------------------------------------------

// botAttachHerdr is what the viewer tells herdr. A seam for tests.
type botAttachReporter interface {
	report(st botAttachState)
	release()
}

type botAttachHerdr struct {
	name, pane, sock string
	mu               sync.Mutex
	last             botAttachState
	reported         bool
	renamed          bool
	renaming         bool // a retry loop is claiming the name
	holds            bool // our source holds the pane (a report was accepted)
}

func newBotAttachHerdr(name string) botAttachReporter {
	pane := os.Getenv("HERDR_PANE_ID")
	if pane == "" {
		return nil
	}
	return &botAttachHerdr{name: name, pane: pane, sock: defaultSock()}
}

// botAttachState is the bot as herdr should see it.
type botAttachState struct {
	state       string // idle | working | blocked | unknown
	message     string
	sessionID   string
	sessionPath string
}

func (h *botAttachHerdr) report(st botAttachState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reported && st == h.last {
		return
	}
	params := map[string]any{
		"pane_id": h.pane, "source": botAttachSource, "agent": botAttachAgent,
		"state": st.state, "seq": time.Now().UnixNano(),
		"resume_argv": []string{"lasso", "bot-attach", h.name},
	}
	if st.message != "" {
		params["message"] = st.message
	}
	if st.sessionID != "" {
		params["agent_session_id"] = st.sessionID
	}
	if st.sessionPath != "" {
		params["agent_session_path"] = st.sessionPath
	}
	// herdr takes a resume_argv only from a reporter that already holds the
	// pane, and in a pane its own detection got to first (a view reopened
	// with `mise run attach`) we do not yet. So the first report claims the
	// pane with the state alone; the full one follows at once.
	if !h.holds {
		claim := map[string]any{"pane_id": h.pane, "source": botAttachSource, "agent": botAttachAgent,
			"state": st.state, "seq": time.Now().UnixNano()}
		if _, err := herdrCallSock(h.sock, "pane.report_agent", claim); err != nil {
			return
		}
		h.holds = true
		params["seq"] = time.Now().UnixNano()
	}
	if _, err := herdrCallSock(h.sock, "pane.report_agent", params); err != nil {
		if !strings.Contains(err.Error(), "resume_not_accepted") {
			return
		}
		// herdr takes a restore command only from the pane's foreground
		// process, which this is not when something started it as a child
		// (`mise run attach` does not; see botOmpAttachScript). Report the
		// state alone, without the session either: the pane stays named and
		// promptable, and herdr is never handed a session it could resume as
		// a second omp.
		delete(params, "resume_argv")
		delete(params, "agent_session_id")
		delete(params, "agent_session_path")
		params["seq"] = time.Now().UnixNano()
		if _, err := herdrCallSock(h.sock, "pane.report_agent", params); err != nil {
			return
		}
	}
	h.last, h.reported = st, true
	// The pane goes by the bot's name, which is how herdr's agent commands
	// and lasso's own lookup (findBotPane) address it. herdr can refuse it
	// for a moment after a report (a view reopened in a pane whose last view
	// just released it), so it is retried until it lands.
	if !h.renamed && !h.renaming {
		h.renaming = true
		go h.claimName()
	}
}

func (h *botAttachHerdr) claimName() {
	for i := 0; i < 30; i++ {
		if _, err := herdrCallSock(h.sock, "agent.rename", map[string]any{"target": h.pane, "name": h.name}); err == nil {
			h.mu.Lock()
			h.renamed, h.renaming = true, false
			h.mu.Unlock()
			return
		}
		time.Sleep(time.Second)
	}
	h.mu.Lock()
	h.renaming = false
	h.mu.Unlock()
}

func (h *botAttachHerdr) release() {
	_, _ = herdrCallSock(h.sock, "pane.release_agent", map[string]any{
		"pane_id": h.pane, "source": botAttachSource, "agent": botAttachAgent, "seq": time.Now().UnixNano(),
	})
}

// --- the viewer ------------------------------------------------------------------

type botAttach struct {
	name    string
	out     io.Writer
	herdr   botAttachReporter
	outMu   sync.Mutex // one writer on the terminal
	mu      sync.Mutex
	conn    net.Conn
	dir     string
	ready   bool
	busy    bool
	sid     string
	asks    []ompUIRequest
	line    []rune // what the human is typing
	closed  bool
	restore func() // the terminal as it was
	seq     int
	tools   map[string]string // running tool calls, by id, for their result line
	// What omp runs (get_state), for the status line and the commands.
	model    ompModelInfo
	thinking string
	efforts  []string
	ctx      ompContextUsage
	// models is the last /model listing, so `/model 2` picks from it;
	// sessions the last /sessions one, for `/resume 2`.
	models   []string
	sessions []botSessionInfo
	// divider is printed, with the conversation's tail, once omp reports the
	// session /new or /resume asked for.
	divider string
	// footerRows is how many rows the status line and the input take now,
	// so the next print can clear them before writing above them.
	footerRows int
	width      func() int
}

func newBotAttach(name string, out io.Writer) *botAttach {
	return &botAttach{name: name, out: out, tools: map[string]string{}, width: func() int { return 100 }}
}

// attachTermWidth is the terminal's width, 100 when it cannot be read.
func attachTermWidth() int {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col < 20 {
		return 100
	}
	return int(ws.Col)
}

func (a *botAttach) run(in *os.File) int {
	a.restore = attachRawTerminal(in)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
		case <-done:
		}
		a.shutdown()
	}()
	a.say(fmt.Sprintf("omp bot %s, through lasso. Type a message and press Enter; Esc stops a turn; Ctrl-C closes this view (the bot keeps running).", a.name))
	go a.connectLoop()
	a.readInput(bufio.NewReader(in))
	close(done)
	a.shutdown()
	return 0
}

func (a *botAttach) shutdown() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	conn := a.conn
	a.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if a.herdr != nil {
		a.herdr.release()
	}
	if a.restore != nil {
		a.restore()
	}
	_, _ = io.WriteString(a.out, "\r\n")
	os.Exit(0)
}

// attachRawTerminal puts a terminal stdin in raw mode with bracketed paste on,
// so a paste (herdr's agent.prompt sends one) is one message and Esc reaches
// us. Not a terminal: nothing to do.
func attachRawTerminal(in *os.File) func() {
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, attachGetTermios)
	if err != nil {
		return func() {}
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Iflag &^= unix.IXON | unix.ICRNL | unix.INLCR
	raw.Cc[unix.VMIN], raw.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, attachSetTermios, &raw); err != nil {
		return func() {}
	}
	fmt.Fprint(os.Stdout, "\x1b[?2004h")
	return func() {
		fmt.Fprint(os.Stdout, "\x1b[?2004l")
		_ = unix.IoctlSetTermios(fd, attachSetTermios, old)
	}
}

// --- output ----------------------------------------------------------------------

const attachPrompt = "› "

// say prints lines above the footer (the status line and the input) and
// draws the footer again under them.
func (a *botAttach) say(text string) {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	var b strings.Builder
	b.WriteString(a.clearFooter())
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		b.WriteString(l)
		b.WriteString("\r\n")
	}
	b.WriteString(a.footer())
	_, _ = io.WriteString(a.out, b.String())
}

// redraw draws the footer again in place: the input changed, or the state the
// status line shows did.
func (a *botAttach) redraw() {
	a.outMu.Lock()
	defer a.outMu.Unlock()
	_, _ = io.WriteString(a.out, a.clearFooter()+a.footer())
}

// clearFooter moves to the footer's first row, clearing every row of it.
// Called with outMu held.
func (a *botAttach) clearFooter() string {
	out := "\r\x1b[K"
	for i := 1; i < a.footerRows; i++ {
		out += "\x1b[1A\r\x1b[K"
	}
	return out
}

// footer is the status line and the input line, and records the rows they
// take (a long input wraps). Called with outMu held.
func (a *botAttach) footer() string {
	w := max(a.width(), 20)
	a.mu.Lock()
	status := a.statusLine(w - 1)
	input := attachPrompt + string(a.line)
	a.mu.Unlock()
	n := utf8.RuneCountInString(input)
	a.footerRows = 2 + (n-1)/w
	return "\x1b[2m" + status + "\x1b[0m\r\n" + input
}

// statusLine is the model, thinking, context and state, cut to width. Called
// with a.mu held.
func (a *botAttach) statusLine(width int) string {
	var parts []string
	if a.model.Name != "" {
		parts = append(parts, a.model.Name)
	} else if a.model.ID != "" {
		parts = append(parts, a.model.ID)
	}
	if a.thinking != "" {
		parts = append(parts, "thinking "+a.thinking)
	}
	if c := a.ctx; c.Tokens > 0 {
		if c.Window > 0 {
			pct := c.Percent
			if pct == 0 {
				pct = float64(c.Tokens) / float64(c.Window) * 100
			}
			parts = append(parts, fmt.Sprintf("ctx %s/%s %d%%", formatTokens(c.Tokens), formatTokens(c.Window), int(pct+0.5)))
		} else {
			parts = append(parts, "ctx "+formatTokens(c.Tokens))
		}
	}
	switch {
	case a.conn == nil:
		parts = append(parts, "not running")
	case len(a.asks) > 0:
		parts = append(parts, "waiting on you")
	case !a.ready:
		parts = append(parts, "starting")
	case a.busy:
		parts = append(parts, "working")
	default:
		parts = append(parts, "idle")
	}
	line := "─ " + strings.Join(parts, " · ") + " "
	if n := utf8.RuneCountInString(line); n < width {
		line += strings.Repeat("─", width-n)
	} else if n > width {
		line = string([]rune(line)[:width])
	}
	return line
}

// formatTokens is a token count as 41k or 1M.
func formatTokens(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatInt((n+500)/1000, 10) + "k"
	default:
		s := strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0") + "M"
	}
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// --- the connection --------------------------------------------------------------

func (a *botAttach) connectLoop() {
	backoff := 500 * time.Millisecond
	waiting := false
	for {
		a.mu.Lock()
		closed := a.closed
		a.mu.Unlock()
		if closed {
			return
		}
		conn, err := net.DialTimeout("unix", botSocketPath(a.name), 2*time.Second)
		if err != nil {
			if !waiting {
				a.say("— the bot is not running; waiting for it —")
				a.report()
				waiting = true
			}
			time.Sleep(backoff)
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff, waiting = 500*time.Millisecond, false
		a.mu.Lock()
		a.conn = conn
		a.mu.Unlock()
		a.read(conn)
		a.mu.Lock()
		a.conn, a.ready, a.busy, a.asks = nil, false, false, nil
		a.mu.Unlock()
		a.report()
	}
}

func (a *botAttach) read(conn net.Conn) {
	r := bufio.NewReaderSize(conn, 256*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			a.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (a *botAttach) send(v map[string]any) error {
	a.mu.Lock()
	conn := a.conn
	if _, ok := v["id"]; !ok {
		a.seq++
		v["id"] = fmt.Sprintf("attach-%d-%d", os.Getpid(), a.seq)
	}
	a.mu.Unlock()
	if conn == nil {
		return errOmpNotConnected
	}
	b, _ := json.Marshal(v)
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := conn.Write(append(b, '\n'))
	return err
}

type attachFrame struct {
	ompFrame
	Dir        string          `json:"dir"`
	Message    json.RawMessage `json:"message"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	IsError    bool            `json:"isError"`
	Command    string          `json:"command"`
	Text       string          `json:"text"`
}

func (a *botAttach) handle(line []byte) {
	var f attachFrame
	if json.Unmarshal(line, &f) != nil {
		return
	}
	// ompFrame's Message is a string field, shadowed here by the raw one:
	// read the UI request's own text from the line again.
	var ui ompFrame
	_ = json.Unmarshal(line, &ui)
	switch f.Type {
	case "host_hello":
		a.mu.Lock()
		a.dir, a.ready, a.busy, a.asks = f.Dir, f.Ready, f.Streaming, nil
		for _, raw := range f.Pending {
			var p ompFrame
			if json.Unmarshal(raw, &p) == nil {
				if q, ok := p.uiRequest(); ok {
					a.asks = append(a.asks, q)
				}
			}
		}
		asks := append([]ompUIRequest(nil), a.asks...)
		a.mu.Unlock()
		if f.Ready {
			a.fetchState(true)
		}
		for _, q := range asks {
			a.showAsk(q)
		}
	case "ready":
		a.mu.Lock()
		a.ready = true
		a.mu.Unlock()
		a.fetchState(false)
	case "agent_start":
		a.setBusy(true)
	case "agent_end":
		if f.IsTerminal == nil || *f.IsTerminal {
			a.setBusy(false)
			a.fetchState(false)
		}
	case "message_end":
		a.showMessage(f.Message)
	case "tool_execution_start":
		title := attachToolTitle(f.ToolName, f.Args)
		a.mu.Lock()
		a.tools[f.ToolCallID] = title
		a.mu.Unlock()
		a.say("  · " + title)
	case "tool_execution_end":
		a.mu.Lock()
		title := a.tools[f.ToolCallID]
		delete(a.tools, f.ToolCallID)
		a.mu.Unlock()
		if f.IsError {
			a.say("  ✗ " + firstNonEmpty(title, f.ToolName) + " failed")
		}
	case "extension_ui_request":
		if q, ok := ui.uiRequest(); ok {
			a.mu.Lock()
			a.asks = append(a.asks, q)
			a.mu.Unlock()
			a.showAsk(q)
			a.report()
		} else if f.Method == "cancel" {
			a.dropAsk(f.TargetID)
			a.dropAsk(f.ID)
		}
	case "host_ui_answered":
		a.dropAsk(f.ID)
	case "command_output":
		if f.Text != "" {
			a.say(indent(f.Text, "  "))
		}
	case "host_restarting":
		a.mu.Lock()
		a.ready, a.busy, a.asks = false, false, nil
		a.mu.Unlock()
		a.say("— restarting —")
		a.report()
	case "host_exit":
		a.say("— the bot stopped —")
	case "model_changed", "thinking_level_changed":
		_ = a.send(map[string]any{"type": "get_state"})
	case "response":
		if !f.Success && f.Error != "" {
			a.say("  (" + f.Error + ")")
		}
		// A session change took: read where it landed.
		if f.Success && (f.Command == "new_session" || f.Command == "switch_session") && !ompCancelled(f.ompFrame.Data) {
			_ = a.send(map[string]any{"type": "get_state"})
		} else if f.Command == "new_session" || f.Command == "switch_session" {
			a.mu.Lock()
			a.divider = ""
			a.mu.Unlock()
			a.say("  (the conversation did not change)")
		}
		// get_state's answer: the only response that carries a session id.
		if st, ok := decodeOmpState(f.ompFrame.Data); ok && st.SessionID != "" {
			a.mu.Lock()
			switched := a.sid != "" && a.sid != st.SessionID
			divider, dir := a.divider, a.dir
			if switched {
				a.divider = ""
			}
			a.sid = st.SessionID
			if st.Model != nil {
				a.model, a.efforts = *st.Model, st.Efforts
			}
			a.thinking = st.Thinking
			if st.Context != nil {
				a.ctx = *st.Context
			}
			a.mu.Unlock()
			if switched {
				a.say("── " + firstNonEmpty(divider, "another conversation") + " ──")
				if dir != "" {
					a.showHistory(dir)
				}
			}
			a.report()
		}
	}
}

func (a *botAttach) setBusy(b bool) {
	a.mu.Lock()
	a.busy = b
	a.mu.Unlock()
	a.report()
}

func (a *botAttach) dropAsk(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	for i, q := range a.asks {
		if q.ID == id {
			a.asks = append(a.asks[:i:i], a.asks[i+1:]...)
			break
		}
	}
	a.mu.Unlock()
	a.report()
}

// fetchState asks omp for its session id; with history, it first prints the
// end of the conversation so far.
func (a *botAttach) fetchState(history bool) {
	if history {
		a.mu.Lock()
		dir := a.dir
		a.mu.Unlock()
		if dir != "" {
			a.showHistory(dir)
		}
	}
	_ = a.send(map[string]any{"type": "get_state"})
	a.report()
}

// showHistory prints the newest rows of the session the host continues.
func (a *botAttach) showHistory(dir string) {
	r := &botRecord{Name: a.name, Dir: dir, Runtime: botRuntimeOmp}
	path := botOmpSessionFile(&localBackend{}, r)
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	parsed := readLogPage(&localBackend{}, path, "omp", info.Size(), info.Size(), false)
	items := parsed.items
	if len(items) > 30 {
		items = items[len(items)-30:]
	}
	var b strings.Builder
	for _, it := range items {
		switch {
		case it.Thinking:
		case it.Kind == "user":
			b.WriteString(attachPrompt + it.Text + "\n")
		case it.Kind == "agent" && it.Text != "":
			b.WriteString(it.Text + "\n")
		case it.Kind == "tool" && it.Tool != nil:
			b.WriteString("  · " + firstNonEmpty(it.Tool.Title, it.Tool.Name) + "\n")
		case it.Kind == "marker":
			b.WriteString("  (" + firstNonEmpty(it.Marker, "interrupted") + ")\n")
		}
	}
	if b.Len() > 0 {
		a.say(strings.TrimRight(b.String(), "\n") + "\n— now —")
	}
}

// showMessage prints a finished message: the human's, or the bot's prose.
func (a *botAttach) showMessage(raw json.RawMessage) {
	var m struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	text := ompMessageText(m.Content)
	switch m.Role {
	case "user":
		if text != "" {
			a.say(attachPrompt + text)
		}
	case "assistant":
		if text != "" {
			a.say(text)
		}
	}
}

// ompMessageText is a message's text blocks, joined; thinking and tool calls
// are left out (a tool call shows as its own line).
func ompMessageText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n\n")
}

// attachToolTitle is one line for a tool call: its name and its most telling
// argument.
func attachToolTitle(name string, args json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(args, &m)
	for _, k := range []string{"command", "path", "file_path", "url", "query", "pattern", "intent"} {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			v = strings.Join(strings.Fields(v), " ")
			if utf8.RuneCountInString(v) > 100 {
				v = string([]rune(v)[:99]) + "…"
			}
			return name + " " + v
		}
	}
	return name
}

func (a *botAttach) showAsk(q ompUIRequest) {
	var b strings.Builder
	b.WriteString("? " + q.Title + "\n")
	if q.Message != "" {
		b.WriteString(indent(q.Message, "  ") + "\n")
	}
	switch q.Method {
	case "select":
		for i, o := range q.Options {
			fmt.Fprintf(&b, "  %d) %s\n", i+1, o)
		}
		b.WriteString("  answer with its number, or type your own where it offers one")
	case "confirm":
		b.WriteString("  1) yes  2) no")
	default:
		b.WriteString("  type the answer and press Enter")
	}
	a.say(b.String())
}

// --- herdr state -------------------------------------------------------------------

func (a *botAttach) state() botAttachState {
	a.mu.Lock()
	defer a.mu.Unlock()
	st := botAttachState{sessionID: a.sid}
	if a.dir != "" && a.sid != "" {
		st.sessionPath = botOmpSessionFile(&localBackend{}, &botRecord{Name: a.name, Dir: a.dir})
	}
	switch {
	case a.conn == nil:
		st.state, st.message = "unknown", "the bot is not running"
	case len(a.asks) > 0:
		st.state, st.message = "blocked", ompFirstLine(a.asks[0].Title)
	case !a.ready:
		st.state = "unknown"
	case a.busy:
		st.state = "working"
	default:
		st.state = "idle"
	}
	return st
}

func (a *botAttach) report() {
	if a.herdr != nil {
		a.herdr.report(a.state())
	}
	// Every state change is the status line's too.
	a.redraw()
}

// --- input -------------------------------------------------------------------------

// attachKey is one decoded input event.
type attachKey struct {
	kind string // text | enter | backspace | esc | interrupt | eof | clear
	text string
}

// attachDecoder turns terminal input bytes into keys. A bracketed paste is
// one text key, its newlines kept, so a multi-line message is one message.
type attachDecoder struct {
	buf     []byte
	pasting bool
	paste   []byte
}

func (d *attachDecoder) feed(p []byte) []attachKey {
	d.buf = append(d.buf, p...)
	var out []attachKey
	for len(d.buf) > 0 {
		if d.pasting {
			end := strings.Index(string(d.buf), "\x1b[201~")
			if end < 0 {
				d.paste = append(d.paste, d.buf...)
				d.buf = d.buf[:0]
				break
			}
			d.paste = append(d.paste, d.buf[:end]...)
			d.buf = d.buf[end+6:]
			d.pasting = false
			text := strings.ReplaceAll(strings.ReplaceAll(string(d.paste), "\r\n", "\n"), "\r", "\n")
			d.paste = nil
			out = append(out, attachKey{kind: "text", text: text})
			continue
		}
		c := d.buf[0]
		switch {
		case c == 0x1b:
			if strings.HasPrefix(string(d.buf), "\x1b[200~") {
				d.pasting = true
				d.buf = d.buf[6:]
				continue
			}
			if len(d.buf) == 1 {
				out = append(out, attachKey{kind: "esc"})
				d.buf = d.buf[1:]
				continue
			}
			if d.buf[1] == '[' || d.buf[1] == 'O' {
				// A CSI or SS3 sequence (an arrow key, say): skipped whole.
				i := 2
				for i < len(d.buf) && (d.buf[i] < 0x40 || d.buf[i] > 0x7e) {
					i++
				}
				if i >= len(d.buf) {
					return out // incomplete: wait for the rest
				}
				d.buf = d.buf[i+1:]
				continue
			}
			out = append(out, attachKey{kind: "esc"})
			d.buf = d.buf[1:]
		case c == '\r' || c == '\n':
			out = append(out, attachKey{kind: "enter"})
			d.buf = d.buf[1:]
		case c == 0x7f || c == 0x08:
			out = append(out, attachKey{kind: "backspace"})
			d.buf = d.buf[1:]
		case c == 0x03:
			out = append(out, attachKey{kind: "interrupt"})
			d.buf = d.buf[1:]
		case c == 0x04:
			out = append(out, attachKey{kind: "eof"})
			d.buf = d.buf[1:]
		case c == 0x15:
			out = append(out, attachKey{kind: "clear"})
			d.buf = d.buf[1:]
		case c < 0x20 || c == 0x7f:
			d.buf = d.buf[1:]
		default:
			r, size := utf8.DecodeRune(d.buf)
			if r == utf8.RuneError && !utf8.FullRune(d.buf) {
				return out
			}
			out = append(out, attachKey{kind: "text", text: string(r)})
			d.buf = d.buf[size:]
		}
	}
	return out
}

func (a *botAttach) readInput(r *bufio.Reader) {
	var d attachDecoder
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			for _, k := range d.feed(buf[:n]) {
				if !a.key(k) {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// key handles one key; false ends the viewer.
func (a *botAttach) key(k attachKey) bool {
	switch k.kind {
	case "text":
		a.mu.Lock()
		a.line = append(a.line, []rune(k.text)...)
		a.mu.Unlock()
		a.redraw()
	case "backspace":
		a.mu.Lock()
		if len(a.line) > 0 {
			a.line = a.line[:len(a.line)-1]
		}
		a.mu.Unlock()
		a.redraw()
	case "clear":
		a.mu.Lock()
		a.line = nil
		a.mu.Unlock()
		a.redraw()
	case "esc":
		a.mu.Lock()
		busy := a.busy
		a.mu.Unlock()
		if busy {
			_ = a.send(map[string]any{"type": "abort"})
			a.say("  (stopping)")
		}
	case "interrupt":
		return false
	case "eof":
		a.mu.Lock()
		empty := len(a.line) == 0
		a.mu.Unlock()
		if empty {
			return false
		}
	case "enter":
		a.mu.Lock()
		text := strings.TrimSpace(string(a.line))
		a.line = nil
		a.mu.Unlock()
		a.redraw()
		if text != "" {
			a.submit(text)
		}
	}
	return true
}

// submit sends what the human typed: an answer when a question is open, else
// a message.
func (a *botAttach) submit(text string) {
	a.mu.Lock()
	var q *ompUIRequest
	if len(a.asks) > 0 {
		qq := a.asks[0]
		q = &qq
	}
	busy := a.busy
	a.mu.Unlock()
	if q != nil {
		ans, ok := attachAnswer(*q, text)
		if !ok {
			a.say("  (answer with one of the numbers above)")
			return
		}
		ans["type"], ans["id"] = "extension_ui_response", q.ID
		if err := a.send(ans); err != nil {
			a.say("  (not sent: " + err.Error() + ")")
		}
		return
	}
	if a.command(text) {
		return
	}
	cmd := map[string]any{"type": "prompt", "message": text}
	if busy {
		cmd["streamingBehavior"] = "steer"
	}
	if err := a.send(cmd); err != nil {
		a.say("  (not sent: " + err.Error() + ")")
		return
	}
	// Working from the moment it is sent: herdr's `agent prompt --wait` gives
	// up if it does not see working within seconds of the submit.
	a.setBusy(true)
}

// command runs /model and /thinking here, where they change the bot over RPC
// (lasso keeps the change in the bot's settings when omp reports it), rather
// than sending them to omp as a message. Anything else is not a command.
func (a *botAttach) command(text string) bool {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return false
	}
	arg := strings.Join(fields[1:], " ")
	switch fields[0] {
	case "/model":
		a.mu.Lock()
		current := a.model.selector()
		a.mu.Unlock()
		if arg == "" {
			models := attachModels(current)
			a.mu.Lock()
			a.models = models
			a.mu.Unlock()
			var b strings.Builder
			for i, m := range models {
				mark := ""
				if m == current {
					mark = "  ✓"
				}
				fmt.Fprintf(&b, "  %d) %s%s\n", i+1, m, mark)
			}
			b.WriteString("  pick a number, or /model <provider/model>")
			a.say(b.String())
			return true
		}
		sel := arg
		a.mu.Lock()
		if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(a.models) {
			sel = a.models[n-1]
		}
		a.mu.Unlock()
		provider, id, ok := strings.Cut(sel, "/")
		if !ok || provider == "" || id == "" {
			a.say("  (a model is provider/model, or a number from /model)")
			return true
		}
		if err := a.send(map[string]any{"type": "set_model", "provider": provider, "modelId": id}); err != nil {
			a.say("  (not sent: " + err.Error() + ")")
			return true
		}
		a.say("  model → " + sel)
		return true
	case "/new", "/clear":
		if a.isBusy() {
			a.say("  (the bot is mid-turn: Esc stops it first)")
			return true
		}
		a.mu.Lock()
		a.divider = "new conversation (the last one is in /sessions)"
		a.mu.Unlock()
		if err := a.send(map[string]any{"type": "new_session"}); err != nil {
			a.say("  (not sent: " + err.Error() + ")")
		}
		return true
	case "/sessions":
		a.mu.Lock()
		dir, sid := a.dir, a.sid
		a.mu.Unlock()
		list := ompSessionList(botOmpSessionDir(dir), sid)
		if len(list) > 20 {
			list = list[:20]
		}
		a.mu.Lock()
		a.sessions = list
		a.mu.Unlock()
		if len(list) == 0 {
			a.say("  (no conversations yet)")
			return true
		}
		var b strings.Builder
		for i, ss := range list {
			mark := ""
			if ss.Current {
				mark = "  ✓"
			}
			fmt.Fprintf(&b, "  %d) %-40s %-10s %4d msgs%s\n", i+1, attachSessionName(ss, 40), attachAgo(ss.mod), ss.Messages, mark)
		}
		b.WriteString("  /resume <n> to switch")
		a.say(b.String())
		return true
	case "/resume":
		if a.isBusy() {
			a.say("  (the bot is mid-turn: Esc stops it first)")
			return true
		}
		a.mu.Lock()
		list, dir, sid := a.sessions, a.dir, a.sid
		a.mu.Unlock()
		if len(list) == 0 {
			list = ompSessionList(botOmpSessionDir(dir), sid)
		}
		var target *botSessionInfo
		if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(list) {
			target = &list[n-1]
		} else {
			for i := range list {
				if arg != "" && strings.HasPrefix(list[i].ID, arg) {
					target = &list[i]
					break
				}
			}
		}
		if target == nil {
			a.say("  (pick one from /sessions: /resume <n>)")
			return true
		}
		if target.Current {
			a.say("  (that is the current conversation)")
			return true
		}
		// Newest file is what the host's -c continues: it stays this one
		// across a restart.
		now := time.Now()
		_ = os.Chtimes(target.path, now, now)
		a.mu.Lock()
		a.divider = "resumed: " + attachSessionName(*target, 60)
		a.mu.Unlock()
		if err := a.send(map[string]any{"type": "switch_session", "sessionPath": target.path}); err != nil {
			a.say("  (not sent: " + err.Error() + ")")
		}
		return true
	case "/thinking", "/effort":
		a.mu.Lock()
		levels := append(append([]string{"off"}, a.efforts...), "auto")
		current := a.thinking
		a.mu.Unlock()
		if arg == "" {
			var b strings.Builder
			for _, l := range levels {
				mark := ""
				if l == current {
					mark = "  ✓"
				}
				fmt.Fprintf(&b, "  %s%s\n", l, mark)
			}
			b.WriteString("  /thinking <level>")
			a.say(b.String())
			return true
		}
		if !slices.Contains(levels, arg) {
			a.say("  (one of: " + strings.Join(levels, ", ") + ")")
			return true
		}
		if err := a.send(map[string]any{"type": "set_thinking_level", "level": arg}); err != nil {
			a.say("  (not sent: " + err.Error() + ")")
			return true
		}
		a.say("  thinking → " + arg)
		return true
	}
	return false
}

func (a *botAttach) isBusy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

// attachSessionName is a conversation's title, or its first message quoted.
func attachSessionName(s botSessionInfo, width int) string {
	name := s.Title
	if name == "" && s.Preview != "" {
		name = `"` + s.Preview + `"`
	}
	if name == "" {
		name = "(empty)"
	}
	if utf8.RuneCountInString(name) > width {
		name = string([]rune(name)[:width-1]) + "…"
	}
	return name
}

// attachAgo is how long ago, in a word.
func attachAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return t.Format("Jan 2")
	}
}

// attachModels is what /model offers: the current model, then the models the
// host's omp configuration names for its roles.
func attachModels(current string) []string {
	var out []string
	if current != "" {
		out = append(out, current)
	}
	if home, err := os.UserHomeDir(); err == nil {
		for _, m := range ompConfiguredModels(&localBackend{}, home) {
			if !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
	}
	return out
}

// attachAnswer reads an answer to a question: an option by number (or its
// label), yes/no for a confirm, free text where the question takes it.
func attachAnswer(q ompUIRequest, text string) (map[string]any, bool) {
	switch q.Method {
	case "confirm":
		switch strings.ToLower(text) {
		case "1", "y", "yes":
			return map[string]any{"confirmed": true}, true
		case "2", "n", "no":
			return map[string]any{"confirmed": false}, true
		}
		return nil, false
	case "select":
		if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(q.Options) {
			return map[string]any{"value": q.Options[n-1]}, true
		}
		for _, o := range q.Options {
			if strings.EqualFold(o, text) {
				return map[string]any{"value": o}, true
			}
		}
		for _, o := range q.Options {
			if strings.Contains(strings.ToLower(o), "type your own") {
				return map[string]any{"value": text}, true
			}
		}
		return nil, false
	default:
		return map[string]any{"value": text}, true
	}
}

package main

// A bot's life in herdr: finding its pane, starting, stopping, and the loop
// that keeps herdr able to bring it back (resume_argv) and relaunches a
// keep-running bot whose pane is gone. See bots.go for the folder it runs in.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	botLoopEvery = 10 * time.Second
	// botMissesBeforeRelaunch: a keep-running bot is relaunched only after its
	// pane has been missing this many consecutive ticks (one minute). herdr
	// restores a bot itself after a restart, and lasso must not race that
	// restore with a second copy.
	botMissesBeforeRelaunch = 6
	// botStartGrace covers the window between typing `mise run bot` and herdr
	// detecting claude, when the bot has a pane but no agent yet.
	botStartGrace = 90 * time.Second
)

var errBotRunning = errors.New("the bot is already running")
var errBotNotRunning = errors.New("the bot is not running")

type botRuntime struct {
	mu       sync.Mutex
	starting map[string]time.Time // name -> when lasso typed the launch
	misses   map[string]int
	reported map[string]string // name -> pane+session last reported to herdr
	lines    map[string]botLastLine
	runLock  *os.File // held while this lasso runs the loop (botsOwnLoop)
}

type botLastLine struct {
	key  string
	text string
	at   string
	kind string
}

var bots = &botRuntime{
	starting: map[string]time.Time{},
	misses:   map[string]int{},
	reported: map[string]string{},
	lines:    map[string]botLastLine{},
}

// botBackend resolves the host a bot lives on. A seam for tests.
var botBackend = func(host string) (Backend, error) { return namedHostBackend(host) }

// findBotPane finds the bot's pane: by herdr agent name first (the task script
// claims it), then the claude pane running in the bot's folder — a pane herdr
// restored comes back unnamed until the script's rename lands.
func findBotPane(b Backend, r *botRecord) (pane, bool) {
	if p, err := pluginAgentPane(b, r.Name); err == nil {
		return p, true
	}
	panes, err := panesRaw(b)
	if err != nil {
		return pane{}, false
	}
	dir := filepath.Clean(expandTildeOn(b, r.Dir))
	for _, p := range panes {
		kind, _ := paneAgentPresence(p)
		if kind != "claude" {
			continue
		}
		if filepath.Clean(p.Cwd) == dir || filepath.Clean(p.ForegroundCwd) == dir {
			return p, true
		}
	}
	return pane{}, false
}

// startBot writes the bot's folder and launches it in a new tab of its
// workspace. It resumes the last session unless fresh, or unless that
// session's transcript is gone (claude would exit on an unknown id).
func startBot(b Backend, r *botRecord, fresh bool) error {
	if _, ok := findBotPane(b, r); ok {
		return errBotRunning
	}
	if err := botMaterialize(b, r); err != nil {
		return err
	}
	dir := expandTildeOn(b, r.Dir)
	session := ""
	if !fresh && safeSessionID(r.LastSessionID) != "" && findClaudeTranscriptIn(b, r.LastSessionID, false, dir) != "" {
		session = r.LastSessionID
	}
	paneID, err := openBotTab(b, r, dir)
	if err != nil {
		return err
	}
	bots.mu.Lock()
	bots.starting[r.Name] = time.Now()
	bots.misses[r.Name] = 0
	bots.mu.Unlock()
	_ = setBotStopped(r.Name, false)
	invalidatePaneList(b.Name())
	waitPaneReady(b, paneID)
	if err := paneRun(b, paneID, botLaunchCommand(session)); err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	return nil
}

// openBotTab opens a tab named after the bot in its workspace (creating the
// workspace when there is none by that label) and returns the tab's pane.
func openBotTab(b Backend, r *botRecord, dir string) (string, error) {
	if ws := terminalWorkspaceIDByLabel(b, r.Workspace); ws != "" {
		res, err := b.HerdrCall("tab.create", map[string]any{
			"workspace_id": ws, "cwd": dir, "label": r.Name, "focus": false,
		})
		if err == nil {
			if _, _, p := parseTerminalCreateResult(res); p != "" {
				return p, nil
			}
		} else if !strings.Contains(err.Error(), "workspace_not_found") {
			return "", fmt.Errorf("tab.create: %w", err)
		}
	}
	res, err := b.HerdrCall("workspace.create", map[string]any{
		"cwd": dir, "label": r.Workspace, "focus": false,
	})
	if err != nil {
		return "", fmt.Errorf("workspace.create: %w", err)
	}
	_, tab, p := parseTerminalCreateResult(res)
	if tab != "" {
		_, _ = b.HerdrCall("tab.rename", map[string]any{"tab_id": tab, "label": r.Name})
	}
	if p == "" {
		return "", fmt.Errorf("workspace.create returned no pane")
	}
	return p, nil
}

// stopBot closes the bot's pane and marks it stopped, so keep-running leaves
// it be. Closing the pane ends the claude process; the session stays on disk
// and the next Start resumes it.
func stopBot(b Backend, r *botRecord) error {
	if err := setBotStopped(r.Name, true); err != nil {
		return err
	}
	p, ok := findBotPane(b, r)
	if !ok {
		return nil
	}
	if _, err := b.HerdrCall("pane.close", map[string]any{"pane_id": p.PaneID}); err != nil {
		return fmt.Errorf("pane.close: %w", err)
	}
	invalidatePaneList(b.Name())
	bots.mu.Lock()
	delete(bots.reported, r.Name)
	delete(bots.starting, r.Name)
	bots.mu.Unlock()
	return nil
}

// restartBot stops the bot, waits for its pane to go, and starts it again.
func restartBot(b Backend, r *botRecord, fresh bool) error {
	if err := stopBot(b, r); err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		invalidatePaneList(b.Name())
		if _, ok := findBotPane(b, r); !ok {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	return startBot(b, r, fresh)
}

// --- status ------------------------------------------------------------------

// botView is a bot as the Bots view shows it.
type botView struct {
	*botRecord
	State      string `json:"state"` // stopped | starting | idle | working | blocked
	PaneID     string `json:"pane_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	WaitingFor string `json:"waiting_for,omitempty"`
	LastText   string `json:"last_text,omitempty"`
	LastKind   string `json:"last_kind,omitempty"`
	LastAt     string `json:"last_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

func botStatus(b Backend, r *botRecord, sessions []claudeSessionEntry) botView {
	v := botView{botRecord: r, State: "stopped"}
	p, ok := findBotPane(b, r)
	if !ok {
		bots.mu.Lock()
		if t, ok := bots.starting[r.Name]; ok && time.Since(t) < botStartGrace {
			v.State = "starting"
		}
		bots.mu.Unlock()
		v.LastText, v.LastKind, v.LastAt = botCachedLine(r.Name)
		return v
	}
	v.PaneID = p.PaneID
	_, paneStatus := paneAgentPresence(p)
	s, found := findBotSession(sessions, r.Name, expandTildeOn(b, r.Dir))
	switch {
	case found && s.agentStatus() != "":
		v.State, v.SessionID, v.WaitingFor = s.agentStatus(), s.SessionID, s.WaitingFor
	case paneStatus == "working" || paneStatus == "blocked":
		v.State = paneStatus
	default:
		v.State = "idle"
	}
	if paneStatus == "" && !found {
		v.State = "starting"
	}
	v.LastText, v.LastKind, v.LastAt = botLastLineFor(b, r.Name, p)
	return v
}

// botLastLineFor is the newest prose row of the bot's chat, for the list's
// preview. Reading the transcript is not free, so it is cached per pane and
// transcript mtime.
func botLastLineFor(b Backend, name string, p pane) (string, string, string) {
	key := fmt.Sprintf("%s\x00%s\x00%d", b.Name(), p.PaneID, paneTranscriptAt(b, b.Name(), p))
	bots.mu.Lock()
	if l, ok := bots.lines[name]; ok && l.key == key {
		bots.mu.Unlock()
		return l.text, l.kind, l.at
	}
	bots.mu.Unlock()
	payload := buildChatPayload(b, p, "", true)
	var line botLastLine
	line.key = key
	for i := len(payload.Items) - 1; i >= 0; i-- {
		it := payload.Items[i]
		if it.Text == "" || it.Thinking || (it.Kind != "agent" && it.Kind != "user" && it.Kind != "incoming") {
			continue
		}
		line.text, line.kind, line.at = previewText(it.Text), it.Kind, it.At
		break
	}
	bots.mu.Lock()
	bots.lines[name] = line
	bots.mu.Unlock()
	return line.text, line.kind, line.at
}

func botCachedLine(name string) (string, string, string) {
	bots.mu.Lock()
	defer bots.mu.Unlock()
	l := bots.lines[name]
	return l.text, l.kind, l.at
}

// previewMarkup is the markdown a one-line preview shows as noise.
var previewMarkup = strings.NewReplacer("**", "", "__", "", "`", "", "~~", "")

// previewText flattens prose to one line of at most 160 characters, without
// its markdown emphasis or heading marks.
func previewText(s string) string {
	fields := strings.Fields(previewMarkup.Replace(s))
	for len(fields) > 0 && strings.Trim(fields[0], "#>-*") == "" {
		fields = fields[1:]
	}
	s = strings.Join(fields, " ")
	if utf8.RuneCountInString(s) > 160 {
		r := []rune(s)
		s = string(r[:159]) + "…"
	}
	return s
}

// botStatuses answers GET /api/bots: every bot with its live state, reading
// each host's session registry once.
func botStatuses(list []*botRecord) []botView {
	sessions := map[string][]claudeSessionEntry{}
	out := make([]botView, 0, len(list))
	for _, r := range list {
		b, err := botBackend(r.Host)
		if err != nil {
			out = append(out, botView{botRecord: r, State: "stopped", Error: err.Error()})
			continue
		}
		if _, ok := sessions[r.Host]; !ok {
			sessions[r.Host] = readClaudeSessions(b)
		}
		out = append(out, botStatus(b, r, sessions[r.Host]))
	}
	return out
}

// --- the loop ----------------------------------------------------------------

func startBotLoop(ctx context.Context) {
	t := time.NewTicker(botLoopEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		botTick()
	}
}

// botRelaunch is how botTick brings a keep-running bot back. A seam for tests.
var botRelaunch = func(b Backend, r *botRecord) error { return startBot(b, r, false) }

// botsOwnLoop reports whether this lasso runs the bot loop, taking the lock
// when it is free. Two lassos sharing a lasso.db (titan's dev and production
// both read ~/.lasso) would otherwise both relaunch the same bot and race each
// other's resume reports. The kernel drops the flock when its holder exits, so
// the survivor takes over on its next tick. Interactive Start/Stop work from
// either lasso; only the unattended half is single-runner.
func botsOwnLoop() bool {
	bots.mu.Lock()
	defer bots.mu.Unlock()
	if bots.runLock != nil {
		return true
	}
	path := filepath.Join(lassoDir(), "bots.runner.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return false
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d, %s\n", os.Getpid(), listenNote())), 0)
	bots.runLock = f
	return true
}

func botTick() {
	if !botsOwnLoop() {
		return
	}
	list, err := listBots()
	if err != nil || len(list) == 0 {
		return
	}
	sessions := map[string][]claudeSessionEntry{}
	for _, r := range list {
		b, err := botBackend(r.Host)
		if err != nil {
			continue
		}
		botOAuthTick(b, r)
		p, ok := findBotPane(b, r)
		if ok {
			bots.mu.Lock()
			bots.misses[r.Name] = 0
			bots.mu.Unlock()
			if _, ok := sessions[r.Host]; !ok {
				sessions[r.Host] = readClaudeSessions(b)
			}
			botReportSession(b, r, p, sessions[r.Host])
			continue
		}
		if r.Stopped || !r.KeepRunning {
			continue
		}
		bots.mu.Lock()
		started, isStarting := bots.starting[r.Name]
		if isStarting && time.Since(started) < botStartGrace {
			bots.mu.Unlock()
			continue
		}
		bots.misses[r.Name]++
		due := bots.misses[r.Name] >= botMissesBeforeRelaunch
		if due {
			bots.misses[r.Name] = 0
		}
		bots.mu.Unlock()
		if due {
			log.Printf("bots:     %s has no pane; relaunching (keep running)", r.Name)
			if err := botRelaunch(b, r); err != nil && !errors.Is(err, errBotRunning) {
				log.Printf("bots:     relaunch %s: %v", r.Name, err)
			}
		}
	}
}

// botReportSession tells herdr how to bring the bot back: `mise run bot --
// --resume <id>` in the pane's folder. Sent once per pane and session; herdr
// refuses it until it has detected claude in the pane, so a refusal is simply
// retried on the next tick.
func botReportSession(b Backend, r *botRecord, p pane, sessions []claudeSessionEntry) {
	sid := ""
	if s, ok := findBotSession(sessions, r.Name, expandTildeOn(b, r.Dir)); ok {
		sid = s.SessionID
	} else if p.AgentSession != nil && p.AgentSession.Kind == "id" && p.AgentSession.Agent == "claude" {
		sid = safeSessionID(p.AgentSession.Value)
	}
	if sid == "" {
		return
	}
	if sid != r.LastSessionID {
		_ = setBotSession(r.Name, sid)
		r.LastSessionID = sid
	}
	key := p.PaneID + "\x00" + sid
	bots.mu.Lock()
	done := bots.reported[r.Name] == key
	bots.mu.Unlock()
	if done {
		return
	}
	argv := botResumeArgv(sid)
	if err := validateResumeArgv(argv); err != nil {
		log.Printf("bots:     %s: %v", r.Name, err)
		return
	}
	// seq must rise across every report from this source, lasso restarts
	// included: herdr refuses a report without one once it holds an earlier
	// one, which would strand the second session after a /clear.
	_, err := b.HerdrCall("pane.report_agent_session", map[string]any{
		"pane_id":          p.PaneID,
		"source":           "lasso",
		"agent":            "claude",
		"seq":              time.Now().UnixNano(),
		"agent_session_id": sid,
		"resume_argv":      argv,
	})
	if err != nil {
		// resume_not_accepted until herdr has detected claude: retried next tick.
		if !strings.Contains(err.Error(), "resume_not_accepted") {
			log.Printf("bots:     %s: report session to herdr: %v", r.Name, err)
		}
		return
	}
	log.Printf("bots:     %s: herdr will resume session %s in %s", r.Name, sid, p.PaneID)
	bots.mu.Lock()
	bots.reported[r.Name] = key
	delete(bots.starting, r.Name)
	bots.mu.Unlock()
}

package main

// A bot's conversations: listing them, starting a new one and resuming an
// earlier one (docs/design/bots.md, "Conversations"). An omp bot's are its own
// session files (.lasso/sessions), switched over RPC with no restart; a claude
// bot's are Claude Code's for its folder, reached by a restart.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// botSessionsMax bounds a listing: the newest conversations are the ones
// worth going back to.
const botSessionsMax = 50

type botSessionInfo struct {
	ID string `json:"id"`
	// Title is the conversation's own (omp names it); Preview its first
	// message, for one that has none.
	Title     string `json:"title,omitempty"`
	Preview   string `json:"preview,omitempty"`
	UpdatedAt string `json:"updated_at"`
	Messages  int    `json:"messages,omitempty"`
	Current   bool   `json:"current,omitempty"`
	path      string
	mod       time.Time
}

var errBotBusy = errors.New("the bot is in the middle of a turn: stop it first")

// botSessions lists the bot's conversations, newest first.
func botSessions(b Backend, r *botRecord) []botSessionInfo {
	dir := expandTildeOn(b, r.Dir)
	var out []botSessionInfo
	if r.isOmp() {
		out = ompSessionList(b, botOmpSessionDir(dir), ompBotFor(r.Name).snapshot().sessionID)
	} else {
		out = claudeSessionList(b, dir, r.LastSessionID)
	}
	if len(out) > botSessionsMax {
		out = out[:botSessionsMax]
	}
	return out
}

func ompSessionList(b Backend, dir, current string) []botSessionInfo {
	ents, err := b.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []botSessionInfo
	for _, e := range ents {
		if e.Dir || !strings.HasSuffix(e.Name, ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name)
		info, err := b.Stat(path)
		if err != nil {
			continue
		}
		s := ompSessionSummary(b, path)
		if s.ID == "" {
			continue
		}
		s.mod, s.UpdatedAt = info.ModTime(), info.ModTime().UTC().Format(time.RFC3339)
		s.Current = s.ID == current
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	return out
}

// ompSessionSummary reads one omp session file: its id and title, its first
// user message, and how many messages it holds. Lines are read whole (an
// inlined image makes one huge) but only these few fields are decoded.
func ompSessionSummary(b Backend, path string) botSessionInfo {
	f, err := b.Open(path)
	if err != nil {
		return botSessionInfo{}
	}
	defer f.Close()
	s := botSessionInfo{path: path}
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			ompSummaryLine(&s, line)
		}
		if err != nil {
			break
		}
	}
	return s
}

func ompSummaryLine(s *botSessionInfo, line []byte) {
	var head struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Title   string `json:"title"`
		Message struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	// The cheap test first: most lines are messages, and only a user one
	// before the preview is found needs decoding. Matched anywhere in the
	// line: nothing promises the entry's own type is its first key.
	text := string(line)
	switch {
	case strings.Contains(text, `"type":"message"`):
		s.Messages++
		if s.Preview != "" || !strings.Contains(text, `"role":"user"`) {
			return
		}
	case strings.Contains(text, `"type":"session"`) || strings.Contains(text, `"type":"title"`) ||
		strings.Contains(text, `"type":"title_change"`):
	default:
		return
	}
	if json.Unmarshal(line, &head) != nil {
		return
	}
	switch head.Type {
	case "title", "title_change":
		if strings.TrimSpace(head.Title) != "" {
			s.Title = strings.TrimSpace(head.Title)
		}
	case "session":
		s.ID = head.ID
		if s.Title == "" && strings.TrimSpace(head.Title) != "" {
			s.Title = strings.TrimSpace(head.Title)
		}
	case "message":
		if head.Message.Role == "user" {
			s.Preview = previewText(ompMessageText(head.Message.Content))
		}
	}
}

// claudeSessionList is Claude Code's conversations for the bot's folder:
// ~/.claude/projects/<slug>/<id>.jsonl on its host, previewed by their first
// user message.
func claudeSessionList(b Backend, dir, current string) []botSessionInfo {
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return nil
	}
	root := filepath.Join(claudeDir(home), "projects", claudeProjectSlug(dir))
	ents, err := b.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []botSessionInfo
	for _, e := range ents {
		if e.Dir || !strings.HasSuffix(e.Name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name, ".jsonl")
		if safeSessionID(id) == "" {
			continue
		}
		path := filepath.Join(root, e.Name)
		st, err := b.Stat(path)
		if err != nil {
			continue
		}
		out = append(out, botSessionInfo{
			ID: id, path: path, mod: st.ModTime(), UpdatedAt: st.ModTime().UTC().Format(time.RFC3339),
			Current: id == current,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mod.After(out[j].mod) })
	if len(out) > botSessionsMax {
		out = out[:botSessionsMax]
	}
	for i := range out {
		out[i].Preview = claudeFirstPrompt(b, out[i].path)
	}
	return out
}

// claudeFirstPrompt is a Claude Code transcript's first real user message,
// read from the head of the file.
func claudeFirstPrompt(b Backend, path string) string {
	f, err := b.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(io.LimitReader(f, 512*1024), 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && strings.Contains(string(line[:min(len(line), 200)]), `"type":"user"`) {
			var rec struct {
				IsMeta  bool `json:"isMeta"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &rec) == nil && !rec.IsMeta {
				if t := previewText(ompMessageText(rec.Message.Content)); t != "" && !strings.HasPrefix(t, "<") {
					return t
				}
			}
		}
		if err != nil {
			return ""
		}
	}
}

// --- new and resume --------------------------------------------------------------

// botNewSession starts the bot on an empty conversation. A running omp bot
// switches over RPC; anything else (re)starts fresh.
func botNewSession(b Backend, r *botRecord) error {
	if r.isOmp() && ompBotFor(r.Name).ensure() && ompBotFor(r.Name).snapshot().ready {
		ob := ompBotFor(r.Name)
		if ob.snapshot().streaming {
			return errBotBusy
		}
		resp, err := ob.call(map[string]any{"type": "new_session"}, 15*time.Second)
		if err != nil {
			return err
		}
		if !resp.Success || ompCancelled(resp.Data) {
			return fmt.Errorf("omp did not start a new conversation: %s", firstNonEmpty(resp.Error, "it was cancelled"))
		}
		ob.refreshState()
		return nil
	}
	if botRunning(b, r) {
		return restartBot(b, r, true)
	}
	return startBot(b, r, true)
}

// botResumeSession switches the bot to one of its earlier conversations.
func botResumeSession(b Backend, r *botRecord, id string) error {
	var target *botSessionInfo
	for _, s := range botSessions(b, r) {
		if s.ID == id {
			s := s
			target = &s
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no conversation %q for this bot", id)
	}
	if !r.isOmp() {
		// Claude Code resumes by id at launch: the next start resumes the
		// bot's last session, so that is what this one becomes.
		if err := setBotSession(r.Name, id); err != nil {
			return err
		}
		r.LastSessionID = id
		if botRunning(b, r) {
			return restartBot(b, r, false)
		}
		return startBot(b, r, false)
	}
	// The launch task's `-c` (a restart, herdr's restore) continues the newest
	// file: making this one newest is what keeps it across a restart, even
	// before anything is said in it.
	_ = botTouch(b, target.path, time.Now())
	ob := ompBotFor(r.Name)
	if !(ob.ensure() && ob.snapshot().ready) {
		if botRunning(b, r) {
			return errors.New("the bot is still starting; try again in a moment")
		}
		return startBot(b, r, false)
	}
	if ob.snapshot().streaming {
		return errBotBusy
	}
	resp, err := ob.call(map[string]any{"type": "switch_session", "sessionPath": target.path}, 20*time.Second)
	if err != nil {
		return err
	}
	if !resp.Success || ompCancelled(resp.Data) {
		return fmt.Errorf("omp did not switch: %s", firstNonEmpty(resp.Error, "it was cancelled"))
	}
	ob.refreshState()
	return nil
}

// ompCancelled reads omp's {"cancelled": true}, its answer to a session change
// it declined (a session from another folder, say).
func ompCancelled(data json.RawMessage) bool {
	var d struct {
		Cancelled bool `json:"cancelled"`
	}
	return json.Unmarshal(data, &d) == nil && d.Cancelled
}

// ompMessageText is a message's text blocks, joined; thinking and tool calls
// are left out.
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

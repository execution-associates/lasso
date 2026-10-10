package main

// Claude Code keeps a registry of its live sessions, one file per process:
// ~/.claude/sessions/<pid>.json with the session id, cwd, the --name, and a
// status it updates itself (busy / idle / waiting, plus waitingFor saying why).
// Bots read it for three things herdr's pane state cannot give as directly:
// which session a bot is on (the resume_argv report), whether it is waiting on
// its human and for what, and when a turn has ended.
//
// Only the JSON is read. The same directory holds <pid>.*.key files, the bearer
// tokens for each session's cc-socks inbox; they are never opened.

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type claudeSessionEntry struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// agentStatus maps Claude's own words onto herdr's vocabulary.
func (e claudeSessionEntry) agentStatus() string {
	switch e.Status {
	case "busy", "shell":
		return "working"
	case "waiting":
		return "blocked"
	case "idle":
		return "idle"
	}
	return ""
}

// claudeSessionAlive is a seam: the local check is kill(pid, 0); a remote host
// is asked for /proc/<pid> (a host without /proc is assumed alive, and its
// stale entries are then told apart by recency).
var claudeSessionAlive = func(b Backend, pid int) bool {
	if pid <= 0 {
		return false
	}
	if _, ok := b.(*localBackend); ok {
		err := syscall.Kill(pid, 0)
		return err == nil || err == syscall.EPERM
	}
	if _, err := b.Stat("/proc/" + strconv.Itoa(pid)); err == nil {
		return true
	}
	if _, err := b.Stat("/proc/self"); err != nil {
		return true
	}
	return false
}

// readClaudeSessions lists the live interactive sessions on b's host.
func readClaudeSessions(b Backend) []claudeSessionEntry {
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return nil
	}
	dir := filepath.Join(claudeDir(home), "sessions")
	ents, err := b.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []claudeSessionEntry
	for _, e := range ents {
		if e.Dir || !strings.HasSuffix(e.Name, ".json") {
			continue
		}
		raw, err := b.ReadFile(filepath.Join(dir, e.Name))
		if err != nil {
			continue
		}
		var s claudeSessionEntry
		if json.Unmarshal(raw, &s) != nil || s.Kind != "interactive" || safeSessionID(s.SessionID) == "" {
			continue
		}
		if !claudeSessionAlive(b, s.PID) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// findBotSession picks the bot's live session: its folder and its --name, the
// most recently updated when a stale entry lingers. A session renamed by hand
// (/rename) still matches on the folder when it is the only one there.
func findBotSession(entries []claudeSessionEntry, name, dir string) (claudeSessionEntry, bool) {
	dir = filepath.Clean(dir)
	var best claudeSessionEntry
	found, named := false, false
	for _, s := range entries {
		if filepath.Clean(s.Cwd) != dir {
			continue
		}
		isNamed := s.Name == name
		switch {
		case !found, isNamed && !named, isNamed == named && s.UpdatedAt > best.UpdatedAt:
			best, found, named = s, true, isNamed
		}
	}
	return best, found
}

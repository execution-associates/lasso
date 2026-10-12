package main

import (
	"testing"
	"time"
)

func TestTerminalTabName(t *testing.T) {
	for _, tc := range []struct {
		command, name string
		generate      bool
	}{
		{"", "", false},
		{"btm", "btm", false},
		{"git status", "git status", false},
		{"ssh  citadel", "ssh citadel", false},
		{"npm run dev --port 3000", "npm", true},
		{"tail -f log | grep err", "tail", true},
		{"FOO=1 sudo /usr/bin/htop -d 5", "htop", true},
		{"echo a\necho b", "echo", true},
		// A credential never leaves the box for the titler.
		{"export GITHUB_TOKEN=abc && gh pr list", "export", false},
		{"curl -H 'Authorization: Bearer x' https://example.com", "curl", false},
	} {
		name, gen := terminalTabName(tc.command)
		if name != tc.name || gen != tc.generate {
			t.Errorf("terminalTabName(%q) = %q, %v; want %q, %v", tc.command, name, gen, tc.name, tc.generate)
		}
	}
}

func TestAutoTitleTerminalRenamesTab(t *testing.T) {
	titlerRunner = func(titler, string) (string, error) { return "Lasso dev server with a long tail of words\n", nil }
	t.Cleanup(func() { titlerRunner = runTitler })
	b := &terminalCreateBackend{memBackend: newMemBackend()}
	autoTitleTerminal(b, "ws:t2", "", "cd ~/projects/lasso && mise run dev")
	if b.renameParams["tab_id"] != "ws:t2" {
		t.Fatalf("rename params = %#v", b.renameParams)
	}
	got, _ := b.renameParams["label"].(string)
	if got != "Lasso dev server with a long" {
		t.Fatalf("label = %q, want the name cut at a word within 30 chars", got)
	}
}

// A terminal whose command starts an agent CLI gets the same herdr label an
// auto-titled agent does: the name's first two words, as an agent name.
func TestAutoTitleTerminalLabelsAnAgent(t *testing.T) {
	titlerRunner = func(titler, string) (string, error) { return "Claude fixing SSO loop\n", nil }
	t.Cleanup(func() { titlerRunner = runTitler })
	labelPoll = time.Millisecond
	t.Cleanup(func() { labelPoll = 2 * time.Second })

	b := newRenameFake()
	b.notFound = 2 // herdr detects the agent a moment after the tab is named
	autoTitleTerminal(b, "t1", "p1", `claude "fix the SSO loop"`)
	if got := b.renamed["t1"]; got != "Claude fixing SSO loop" {
		t.Errorf("tab = %q, want the generated name", got)
	}
	if got := b.labels["p1"]; got != "claude-fixing" {
		t.Errorf("herdr label = %q, want the name's first two words", got)
	}
}

// Most terminals run no agent at all; the label attempt must end quietly and
// leave the tab name in place.
func TestAutoTitleTerminalWithoutAnAgentGivesUp(t *testing.T) {
	titlerRunner = func(titler, string) (string, error) { return "Lasso dev server\n", nil }
	t.Cleanup(func() { titlerRunner = runTitler })
	labelPoll, terminalLabelWait = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { labelPoll, terminalLabelWait = 2*time.Second, 60*time.Second })

	b := newRenameFake()
	b.notFound = 1 << 30
	autoTitleTerminal(b, "t1", "p1", "cd ~/projects/lasso && mise run dev")
	if got := b.renamed["t1"]; got != "Lasso dev server" {
		t.Errorf("tab = %q, want the generated name", got)
	}
	if got := b.labels["p1"]; got != "" {
		t.Errorf("herdr label = %q, want none", got)
	}
}

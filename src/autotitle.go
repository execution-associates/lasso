package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Auto-titling: an agent's title is the first line of its prompt (see
// promptTitle), which is what the branch, the work dir, the herdr workspace
// label and therefore the agents sidebar all get named after. That first line
// is written for the agent, not for a sidebar — a pasted paragraph gives every
// entry the same opening words, and anything long is simply clipped mid-word.
//
// So after a create (from the UI or the MCP create_agent tool) lasso asks a
// local agent CLI to distill the whole prompt into two names: a 3-6 word title
// for the workspace and the record, and a one- or two-word name for the places
// that are narrower still: a scratch agent's tab and herdr's custom agent label
// (agent.rename, https://herdr.dev/docs/agents/#custom-agent-labels). Gated by
// the auto_title_agents setting (default on); the Settings tab exposes it as
// "Auto-title new agents".
//
// Three things are deliberate here:
//
//   - It runs on the LASSO host, never the host the agent was created on. The
//     titler is a one-shot local process (an API round-trip, no repo access),
//     so making it follow the agent to a remote box would mean shipping the
//     prompt over SSH and requiring a logged-in CLI on every host in the fleet
//     for a cosmetic rename. Only the resulting rename travels to the agent's
//     host, over the herdr RPC that host is already driven by.
//
//   - Only the DISPLAY name changes. The branch, work dir and prompt file were
//     all named from the original title before the CLI could answer, and
//     renaming a live worktree's branch out from under a booting agent is a far
//     bigger blast radius than a nicer label is worth.
//
//   - An explicit title is never overwritten. The web creator has no title
//     field (it always derives from the prompt), but an MCP caller that passed
//     one meant it.

// autoTitleKey is the settings-table key for the toggle. Unset means on.
const autoTitleKey = "auto_title_agents"

// autoTitleEnabled reports whether auto-titling is on (the default). A nil db
// (tests, shutdown) reads as on — the caller still has to be past createAgent
// for that to matter.
func autoTitleEnabled() bool {
	if db == nil {
		return true
	}
	v, err := getSetting(autoTitleKey)
	if err != nil || v == "" {
		return true
	}
	on, err := strconv.ParseBool(v)
	return err != nil || on
}

// ---------------------------------------------------------------------------
// GET/POST /api/auto-title — the Settings tab's toggle
// ---------------------------------------------------------------------------

func serveAutoTitle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"enabled": autoTitleEnabled()})
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Enabled == nil {
			http.Error(w, "enabled required", http.StatusBadRequest)
			return
		}
		if err := setSetting(autoTitleKey, strconv.FormatBool(*req.Enabled)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"enabled": autoTitleEnabled()})
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// the rename itself
// ---------------------------------------------------------------------------

// autoTitleAgent generates names for rec from its prompt and applies them: the
// title to the agent's herdr workspace (what the agents sidebar shows) and to
// the persisted record (the name list_agents / get_agent surface), the short
// name to a scratch agent's tab and to herdr's agent label. Runs in its own
// goroutine off createAgent, alongside the boot: nothing downstream waits on
// the names, and the CLI takes seconds.
//
// Every failure past "the feature is off" is user-visible — a toast, not just a
// log line — because the whole feature is a rename the user is watching the
// sidebar for. b is the agent's own host backend (the rename travels there);
// the titler CLI runs locally regardless.
func autoTitleAgent(b Backend, host string, rec AgentRecord) {
	names, err := generateAgentTitle(rec.Description)
	if err != nil {
		log.Printf("agent %s on %s: auto-title failed: %v", rec.ID, host, err)
		notifyUI(notice{
			Level:  "error",
			Title:  fmt.Sprintf("Couldn't auto-title %q", rec.Title),
			Detail: "No local agent CLI could name it — " + err.Error(),
		})
		return
	}
	if names.Title != rec.Title {
		if err := renameAgentHome(b, rec, names); err != nil {
			log.Printf("agent %s on %s: auto-title rename failed: %v", rec.ID, host, err)
			notifyUI(notice{
				Level:  "error",
				Title:  fmt.Sprintf("Couldn't rename %q to %q", rec.Title, names.Title),
				Detail: err.Error(),
			})
			return
		}
		// Only the workspace (or a scratch agent's own tab, above). A worktree
		// agent's herdr TAB is deliberately left alone: it's the user's own
		// organization of the terminal, shared with whatever else they put in it,
		// and retitling it is a rename they never asked for in a place that isn't
		// the agent's.
		if err := updateAgentTitle(rec.ID, host, names.Title); err != nil {
			log.Printf("agent %s on %s: auto-title record update failed: %v", rec.ID, host, err)
		}
		// The cross-host pane listing is served from a short-lived cache; drop it
		// so the next poll shows the new name instead of the old one.
		invalidatePanesCache()
		log.Printf("agent %s on %s: auto-titled %q -> %q (%s)", rec.ID, host, rec.Title, names.Title, names.Name)
	}
	if rec.RootPane == "" {
		return
	}
	label, err := labelAgent(b, host, rec.RootPane, names.Name, labelWait)
	switch {
	case errors.Is(err, errAgentNeverAppeared):
		// The boot never brought an agent up in the pane; bootAgent already
		// records that as a failed agent, so a second toast would only repeat it.
		log.Printf("agent %s on %s: no herdr label: %v", rec.ID, host, err)
	case err != nil:
		log.Printf("agent %s on %s: herdr label failed: %v", rec.ID, host, err)
		notifyUI(notice{
			Level:  "error",
			Title:  fmt.Sprintf("Couldn't label %q in herdr", names.Title),
			Detail: err.Error(),
		})
	case label != "":
		invalidatePaneList(host)
		invalidatePanesCache()
		log.Printf("agent %s on %s: herdr label %q", rec.ID, host, label)
	}
}

// renameAgentHome renames whatever names the agent in the sidebar: its own
// workspace, with the title, or — for a scratch agent living as a tab in the
// shared Scratch workspace — that tab, with the short name, since renaming the
// workspace would rename every other scratch agent's home with it and a tab
// strip has room for a word or two, not a sentence.
func renameAgentHome(b Backend, rec AgentRecord, names agentTitles) error {
	if rec.Type == "scratch" && workspaceLabelRaw(b, rec.WorkspaceID) == scratchWorkspaceLabel {
		p, ok := paneGet(b, rec.RootPane)
		if !ok || p.TabID == "" {
			return fmt.Errorf("pane %s: tab not found", rec.RootPane)
		}
		_, err := b.HerdrCall("tab.rename", map[string]any{"tab_id": p.TabID, "label": names.Name})
		return err
	}
	_, err := b.HerdrCall("workspace.rename", map[string]any{
		"workspace_id": rec.WorkspaceID,
		"label":        names.Title,
	})
	return err
}

// errAgentNeverAppeared is labelAgent giving up because herdr never saw an
// agent in the pane.
var errAgentNeverAppeared = errors.New("herdr never detected an agent in the pane")

// labelWait bounds how long autoTitleAgent waits for herdr to detect the agent,
// and labelPoll is how often labelAgent asks. The titler usually answers before
// the boot has launched the CLI (a worktree's setup script alone can take
// minutes), and herdr refuses to label a pane it has not yet seen an agent in.
// Vars so tests can shrink them.
var (
	labelWait = 5 * time.Minute
	labelPoll = 2 * time.Second
)

// labelMaxSuffix bounds the -2, -3, … suffixes tried when the name is taken.
const labelMaxSuffix = 9

// labelAgent gives the agent in paneID herdr's custom agent label for name
// (agentName's spelling: "SSO redirect" is "sso-redirect"), and returns the
// label it set, "" when it set none. herdr names are unique among live agents,
// so a taken name gets the first free suffix; a bot's name counts as taken even
// while the bot is stopped, because lasso finds a bot by that name and the bot
// could not claim it back. A label already on the pane (a human's rename, or a
// harness that names itself) is left alone. wait bounds how long it waits for
// herdr to detect an agent in the pane at all.
func labelAgent(b Backend, host, paneID, name string, wait time.Duration) (string, error) {
	base := agentName(name)
	if base == "" {
		return "", nil
	}
	deadline := time.Now().Add(wait)
	n := 1
	for {
		if current := agentLabelOf(b, paneID); current != "" {
			return "", nil
		}
		label := labelCandidate(base, n)
		if botNamed(host, label) != "" {
			if n++; n > labelMaxSuffix {
				return "", fmt.Errorf("%s and %d variants are all taken", base, labelMaxSuffix-1)
			}
			continue
		}
		_, err := b.HerdrCall("agent.rename", map[string]any{"target": paneID, "name": label})
		switch {
		case err == nil:
			return label, nil
		case strings.Contains(err.Error(), "agent_name_taken"):
			if n++; n > labelMaxSuffix {
				return "", fmt.Errorf("%s and %d variants are all taken", base, labelMaxSuffix-1)
			}
		case strings.Contains(err.Error(), "agent_not_found"):
			if _, ok := paneGet(b, paneID); !ok {
				return "", errAgentNeverAppeared // the pane itself is gone
			}
			if time.Now().After(deadline) {
				return "", errAgentNeverAppeared
			}
			time.Sleep(labelPoll)
		default:
			return "", err
		}
	}
}

// labelCandidate is the nth label tried for base: base itself, then base-2,
// base-3, … with base cut so the result stays within herdr's 32 characters.
func labelCandidate(base string, n int) string {
	if n <= 1 {
		return base
	}
	suffix := "-" + strconv.Itoa(n)
	if len(base)+len(suffix) > 32 {
		base = strings.TrimRight(base[:32-len(suffix)], "_-")
	}
	return base + suffix
}

// ---------------------------------------------------------------------------
// asking a local agent CLI for the title
// ---------------------------------------------------------------------------

// titler is one CLI lasso will ask for a title, in the order listed: claude
// first, then codex, opencode, omp, pi. Each runs one non-interactive turn with the
// instruction as its last argument (argv, so no quoting is involved) and prints
// the answer — and only the answer — on stdout; their progress chatter,
// sandbox warnings and session banners all go to stderr.
type titler struct {
	id string
	// args precede the instruction on the command line.
	args []string
}

var titlers = []titler{
	{id: "claude", args: []string{"-p"}},
	// --skip-git-repo-check: the titler runs from the home dir, which usually
	// isn't a repo, and codex exec otherwise refuses to start there.
	{id: "codex", args: []string{"exec", "--skip-git-repo-check"}},
	{id: "opencode", args: []string{"run"}},
	// -p is print mode for both: one turn, answer on stdout, exit. Neither
	// needs a repo (they run from the home dir), and pi skips its project-trust
	// prompt entirely in non-interactive modes.
	{id: "omp", args: []string{"-p"}},
	{id: "pi", args: []string{"-p"}},
}

// titlerTimeout caps one CLI's turn. Generous — a first launch pays for config
// loading and MCP handshakes — but bounded, so a hung or rate-limited CLI falls
// through to the next one instead of pinning the goroutine forever. A var so
// tests can shrink it.
var titlerTimeout = 90 * time.Second

// titlerRunner runs one titler and returns its stdout. A package var so tests
// can substitute a fake instead of shelling out to a real agent CLI.
var titlerRunner = runTitler

// generateAgentTitle asks each titler in turn for a title, returning the first
// clean answer. The error, when every one fails, names what each did — the
// toast's whole job is telling the user WHICH part of the chain is missing
// (a CLI that isn't installed reads very differently from one that isn't
// logged in).
func generateAgentTitle(prompt string) (agentTitles, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return agentTitles{}, errors.New("no prompt to summarize")
	}
	return askTitlers(titleInstruction(prompt), func(out string) (agentTitles, bool) {
		t := parseAgentTitles(out)
		return t, t.Title != ""
	})
}

// generateTitle asks each titler in turn for a title answering instruction,
// returning the first usable one.
func generateTitle(instruction string) (string, error) {
	return askTitlers(instruction, func(out string) (string, bool) {
		t := cleanTitle(out)
		return t, t != ""
	})
}

// askTitlers asks each titler in turn, returning the first answer parse
// accepts.
func askTitlers[T any](instruction string, parse func(string) (T, bool)) (T, error) {
	var fails []string
	for _, t := range titlers {
		out, err := titlerRunner(t, instruction)
		if err != nil {
			fails = append(fails, t.id+": "+err.Error())
			continue
		}
		if v, ok := parse(out); ok {
			return v, nil
		}
		fails = append(fails, t.id+": answered with no usable title")
	}
	var zero T
	return zero, errors.New(strings.Join(fails, "; "))
}

// titlePromptLimit caps how much of the prompt rides into the instruction. A
// title comes out of the opening paragraphs; the rest is context the titler
// would pay tokens (and latency) to read past.
const titlePromptLimit = 4000

// titleInstruction wraps the agent's prompt in the ask. The rules exist because
// the titlers are CODING agents: without "do not attempt the task" a capable
// one will happily start editing files instead of naming the work.
func titleInstruction(prompt string) string {
	if len(prompt) > titlePromptLimit {
		prompt = strings.ToValidUTF8(prompt[:titlePromptLimit], "")
	}
	return `Name the coding task below, for display in a list of running agents.

Reply with exactly two lines, in this form:
name: <one or two words>
title: <three to six words>

Rules:
- name is a handle of one or two words, at most ` + strconv.Itoa(agentShortNameMaxLen) + ` characters, for a narrow tab.
- title is 3 to 6 words, at most ` + strconv.Itoa(autoTitleMaxLen) + ` characters.
- Both say what the task IS, specifically enough to tell it apart from similar work.
- No trailing punctuation, no quotes, no markdown.
- Reply with the two lines alone — no preamble, no explanation, no alternatives.
- Do NOT attempt the task, read any files, or use any tools.

Task:
` + prompt
}

// runTitler shells out to one titler CLI and returns its stdout.
func runTitler(t titler, instruction string) (string, error) {
	bin, ok := titlerBin(t.id)
	if !ok {
		return "", errors.New("not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), titlerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, append(append([]string{}, t.args...), instruction)...)
	// Run from the home dir: it's the one directory these CLIs can be expected
	// to already trust, and the titler has no business in any repo.
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.Env = titlerEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("timed out after %s", titlerTimeout)
	}
	if err != nil {
		if detail := lastLine(stderr.String()); detail != "" {
			return "", fmt.Errorf("%v (%s)", err, detail)
		}
		return "", err
	}
	return stdout.String(), nil
}

// titlerEnv is the environment the titler CLIs run with: lasso's own, minus the
// three Claude Code session markers a lasso launched from inside a Claude Code
// session leaks. Claude Code reads their presence as "this is a child session"
// and changes its behavior accordingly (see claudeCommand, which scrubs the
// same three for the agents it launches).
func titlerEnv() []string {
	drop := map[string]bool{
		"CLAUDE_CODE_CHILD_SESSION": true,
		"CLAUDECODE":                true,
		"CLAUDE_CODE_SESSION_ID":    true,
	}
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// titlerInstallDirs are the home-relative directories these CLIs install into,
// searched when PATH doesn't have them. lasso commonly runs as a systemd user
// unit with a hand-written PATH (see the unit's Environment=PATH), which is how
// an opencode in ~/.opencode/bin ends up invisible to a lasso that can see
// claude in ~/.local/bin. Mirrors tailscaleBin's fallback list.
var titlerInstallDirs = []string{
	".local/bin",
	".local/share/mise/shims",
	".bun/bin",
	".opencode/bin",
	".cargo/bin",
	"go/bin",
}

// titlerBin resolves a titler's executable, preferring PATH.
func titlerBin(name string) (string, bool) {
	if p, err := exec.LookPath(name); err == nil {
		return p, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	for _, dir := range titlerInstallDirs {
		p := filepath.Join(home, dir, name)
		if _, err := exec.LookPath(p); err == nil {
			return p, true
		}
	}
	for _, p := range []string{"/usr/local/bin/" + name, "/opt/homebrew/bin/" + name} {
		if _, err := exec.LookPath(p); err == nil {
			return p, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------------------
// cleaning up what the CLI said
// ---------------------------------------------------------------------------

// autoTitleMaxLen caps a generated title. It's a display name in a narrow
// sidebar column — past this it's clipped anyway, which is the problem the
// feature exists to fix.
const autoTitleMaxLen = 60

// agentShortNameMaxLen caps the one- or two-word name: a scratch agent's tab
// label and the source of its herdr agent label (32 characters at most).
const agentShortNameMaxLen = 24

// agentTitles is what the titler names an agent: Title for the workspace and the
// record, Name (one or two words) for the tab and herdr's agent label.
type agentTitles struct {
	Name  string
	Title string
}

// parseAgentTitles reads the titler's "name: …" and "title: …" lines. A model
// that ignored the format but still answered with a title has answered: the
// title is then its first plain line, and the name falls back to the title's
// first two words that carry meaning.
func parseAgentTitles(raw string) agentTitles {
	var t agentTitles
	var plain []string
	for _, ln := range strings.Split(raw, "\n") {
		// Only the leading bullet or markup: the value's own quotes and backticks
		// are trimmed from both its ends by cleanTitle, after the label is cut.
		line := strings.TrimLeft(strings.TrimSpace(ln), "-•*_# ")
		pre, rest, ok := strings.Cut(line, ":")
		switch label := strings.ToLower(strings.TrimSpace(trimTitleDecoration(pre))); {
		case ok && label == "name" && t.Name == "":
			t.Name = shortName(rest)
		case ok && label == "title" && t.Title == "":
			t.Title = cleanTitle(rest)
		default:
			plain = append(plain, ln)
		}
	}
	if t.Title == "" {
		t.Title = cleanTitle(strings.Join(plain, "\n"))
	}
	if t.Name == "" {
		t.Name = shortName(withoutFillerWords(t.Title))
	}
	return t
}

// shortName cleans s like a title and keeps its first two words, within
// agentShortNameMaxLen.
func shortName(s string) string {
	words := strings.Fields(cleanTitle(s))
	if len(words) > 2 {
		words = words[:2]
	}
	return truncateTitleTo(strings.TrimRight(strings.Join(words, " "), ".!,;:"), agentShortNameMaxLen)
}

// fillerWords are dropped before a title's first two words become a fallback
// name, so "Fix the SSO loop" is "Fix SSO" rather than "Fix the".
var fillerWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "of": true,
	"to": true, "for": true, "in": true, "on": true, "with": true, "from": true,
}

func withoutFillerWords(s string) string {
	var kept []string
	for _, w := range strings.Fields(s) {
		if !fillerWords[strings.ToLower(w)] {
			kept = append(kept, w)
		}
	}
	return strings.Join(kept, " ")
}

// cleanTitle turns a titler's raw stdout into a title, or "" if there's nothing
// usable in it. The instruction asks for a bare title and the CLIs generally
// comply, but "comply" is not a guarantee: a model that prefixes a lead-in,
// wraps the answer in quotes or dresses it in markdown has still answered, so
// take the answer rather than throwing the turn away.
func cleanTitle(raw string) string {
	line := titleLine(raw)
	line = trimTitleDecoration(line)
	// "Title: Fix the login flow" — drop the label, keep the title. Only an
	// exact "title" prefix, so a real colon ("Login: fix the redirect") stays.
	if pre, rest, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(pre), "title") {
		line = trimTitleDecoration(rest)
	}
	line = strings.Join(strings.Fields(line), " ")
	line = strings.TrimRight(line, ".!,;")
	return truncateTitle(line)
}

// titleLine picks the answer out of the CLI's output: the first non-empty line
// that isn't a lead-in ("Here's a short title:").
func titleLine(raw string) string {
	for _, ln := range strings.Split(raw, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasSuffix(t, ":") {
			continue
		}
		return t
	}
	return ""
}

// trimTitleDecoration strips the quoting and markdown a model may wrap its
// answer in.
func trimTitleDecoration(s string) string {
	return strings.Trim(s, " \t`*_#\"'“”‘’")
}

// truncateTitle caps a title at autoTitleMaxLen, cutting back to the last space
// so it ends on a whole word (mirroring titleSlug's word-boundary cut).
func truncateTitle(s string) string { return truncateTitleTo(s, autoTitleMaxLen) }

// truncateTitleTo cuts s to at most n bytes, at a word boundary when it can.
func truncateTitleTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = strings.ToValidUTF8(s[:n], "")
	if i := strings.LastIndex(s, " "); i > 0 {
		s = s[:i]
	}
	return strings.TrimRight(strings.TrimSpace(s), ".,;:!-")
}

// lastLine is the final non-empty line of s, trimmed — the useful part of a
// failed CLI's stderr (the error, past whatever banner preceded it).
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

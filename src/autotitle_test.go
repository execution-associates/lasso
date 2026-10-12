package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The titlers are coding-agent CLIs answering in prose habits, not an API with
// a schema: the instruction asks for a bare title and they mostly comply, but a
// lead-in, a pair of quotes or a markdown wrapper is still an answer. Throwing
// those turns away would fall through to the next CLI (or to a toast) over
// formatting we can strip.
func TestCleanTitle(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"bare", "Fix the login redirect\n", "Fix the login redirect"},
		{"quoted", `"Fix the login redirect"`, "Fix the login redirect"},
		{"smart quotes", "“Fix the login redirect”", "Fix the login redirect"},
		{"markdown", "**Fix the login redirect**", "Fix the login redirect"},
		{"backticks", "`Fix the login redirect`", "Fix the login redirect"},
		{"lead-in line", "Here's a short title:\n\nFix the login redirect\n", "Fix the login redirect"},
		{"title label", "Title: Fix the login redirect", "Fix the login redirect"},
		{"trailing period", "Fix the login redirect.", "Fix the login redirect"},
		{"trailing prose", "Fix the login redirect\n\nThis names the task.", "Fix the login redirect"},
		{"inner colon kept", "Login: fix the redirect", "Login: fix the redirect"},
		{"collapsed whitespace", "Fix   the  login\tredirect", "Fix the login redirect"},
		{"empty", "\n\n", ""},
		{"only a lead-in", "Here is the title:\n", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cleanTitle(c.raw); got != c.want {
				t.Errorf("cleanTitle(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// A title is a display name in a narrow sidebar column — past autoTitleMaxLen
// it's clipped anyway, which is the very problem auto-titling exists to fix. A
// model that ignores the word limit must not reintroduce it.
func TestCleanTitleCapsLengthAtAWordBoundary(t *testing.T) {
	const long = "Rename agent titles and panes based on the content of the original prompt when creating an agent"
	got := cleanTitle(long)
	if len(got) > autoTitleMaxLen {
		t.Errorf("title %q is %d chars, want <= %d", got, len(got), autoTitleMaxLen)
	}
	// Cut on a word boundary: the kept text is a whole-word prefix of the
	// original, not "…the origi".
	if !strings.HasPrefix(long, got) || !strings.HasPrefix(long[len(got):], " ") {
		t.Errorf("title = %q, want a whole-word prefix of %q", got, long)
	}
}

// The fallback chain is the feature's whole resilience story: claude, then
// codex, then opencode. A CLI that isn't installed (or isn't logged in) must
// hand off rather than end the attempt.
func TestGenerateAgentTitleFallsThroughToTheNextCLI(t *testing.T) {
	var tried []string
	titlerRunner = func(tl titler, _ string) (string, error) {
		tried = append(tried, tl.id)
		switch tl.id {
		case "claude":
			return "", errors.New("not installed")
		case "codex":
			return "\n\n", nil // ran, but said nothing usable
		default:
			return "Fix the login redirect\n", nil
		}
	}
	t.Cleanup(func() { titlerRunner = runTitler })

	got, err := generateAgentTitle("the login page redirects in a loop after SSO")
	if err != nil {
		t.Fatalf("generateAgentTitle: %v", err)
	}
	if got.Title != "Fix the login redirect" {
		t.Errorf("title = %q, want the third CLI's answer", got.Title)
	}
	if want := []string{"claude", "codex", "opencode"}; strings.Join(tried, ",") != strings.Join(want, ",") {
		t.Errorf("tried %v, want %v (in order)", tried, want)
	}
}

// When every CLI fails the error is what reaches the user's toast, so it has to
// say which part of the chain is missing — "claude isn't installed" and "claude
// isn't logged in" call for very different fixes.
func TestGenerateAgentTitleReportsEveryFailure(t *testing.T) {
	titlerRunner = func(tl titler, _ string) (string, error) {
		return "", errors.New("not installed")
	}
	t.Cleanup(func() { titlerRunner = runTitler })

	_, err := generateAgentTitle("do a thing")
	if err == nil {
		t.Fatal("generateAgentTitle succeeded with every CLI failing")
	}
	for _, tl := range titlers {
		if !strings.Contains(err.Error(), tl.id) {
			t.Errorf("error %q does not name %s", err, tl.id)
		}
	}
}

// The titlers are coding agents pointed at a prompt that tells one to do work.
// Without the guard rails in the instruction a capable CLI starts the task
// instead of naming it — and the prompt has to actually be in there.
func TestTitleInstructionCarriesThePromptAndItsGuardRails(t *testing.T) {
	in := titleInstruction("wire up the settings toggle")
	if !strings.Contains(in, "wire up the settings toggle") {
		t.Error("instruction does not carry the prompt")
	}
	for _, want := range []string{"Do NOT attempt the task", "two lines alone", "name:", "title:"} {
		if !strings.Contains(in, want) {
			t.Errorf("instruction is missing its %q guard rail:\n%s", want, in)
		}
	}
	long := strings.Repeat("x", titlePromptLimit*2)
	if got := len(titleInstruction(long)) - len(titleInstruction("")); got > titlePromptLimit {
		t.Errorf("prompt contributed %d chars, want it capped at %d", got, titlePromptLimit)
	}
}

// The titler answers "name: …" and "title: …", but it is a coding agent in
// prose habits: bullets, bold labels and a lead-in are still an answer, and one
// that ignored the format and gave a bare title has still named the task.
func TestParseAgentTitles(t *testing.T) {
	cases := []struct {
		name, raw, wantName, wantTitle string
	}{
		{"format", "name: SSO loop\ntitle: Fix the SSO login loop\n", "SSO loop", "Fix the SSO login loop"},
		{"title first", "title: Fix the SSO login loop\nname: SSO loop", "SSO loop", "Fix the SSO login loop"},
		{"markdown", "**Name:** `SSO loop`\n**Title:** \"Fix the SSO login loop\"", "SSO loop", "Fix the SSO login loop"},
		{"bullets and lead-in", "Here you go:\n- name: SSO loop.\n- title: Fix the SSO login loop.", "SSO loop", "Fix the SSO login loop"},
		{"name too long", "name: Fix the SSO login loop\ntitle: Fix the SSO login loop", "Fix the", "Fix the SSO login loop"},
		{"bare title", "Fix the SSO login loop\n", "Fix SSO", "Fix the SSO login loop"},
		{"empty", "\n", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseAgentTitles(c.raw)
			if got.Name != c.wantName || got.Title != c.wantTitle {
				t.Errorf("parseAgentTitles(%q) = %+v, want name %q title %q", c.raw, got, c.wantName, c.wantTitle)
			}
		})
	}
}

// renameFake is a herdr that records renames and holds agent labels the way
// agent.rename does: unique among live agents, and only for a pane herdr has
// seen an agent in.
type renameFake struct {
	*memBackend
	wsLabel  string
	renamed  map[string]string // workspace or tab id -> label
	labels   map[string]string // pane id -> agent label
	taken    map[string]bool
	notFound int // agent.rename answers agent_not_found this many times first
	calls    []string
}

func newRenameFake() *renameFake {
	return &renameFake{
		memBackend: newMemBackend(),
		renamed:    map[string]string{},
		labels:     map[string]string{"p1": ""},
		taken:      map[string]bool{},
	}
}

func (b *renameFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	b.calls = append(b.calls, method)
	p, _ := params.(map[string]any)
	switch method {
	case "workspace.get":
		return json.Marshal(map[string]any{"workspace": map[string]any{"label": b.wsLabel}})
	case "pane.get":
		return json.RawMessage(`{"pane":{"pane_id":"p1","tab_id":"t1"}}`), nil
	case "workspace.rename":
		b.renamed[p["workspace_id"].(string)] = p["label"].(string)
	case "tab.rename":
		b.renamed[p["tab_id"].(string)] = p["label"].(string)
	case "agent.list":
		var agents []map[string]any
		for pane, name := range b.labels {
			agents = append(agents, map[string]any{"pane_id": pane, "name": name})
		}
		return json.Marshal(map[string]any{"agents": agents})
	case "agent.rename":
		if b.notFound > 0 {
			b.notFound--
			return nil, errors.New("agent_not_found: agent target p1 not found")
		}
		name := p["name"].(string)
		if b.taken[name] {
			return nil, errors.New("agent_name_taken: agent name " + name + " is already used")
		}
		b.labels[p["target"].(string)] = name
	}
	return json.RawMessage(`{}`), nil
}

func openTitleTestDB(t *testing.T, answer string) {
	t.Helper()
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)
	titlerRunner = func(titler, string) (string, error) { return answer, nil }
	t.Cleanup(func() { titlerRunner = runTitler })
}

// autoTitleAgent's job is a rename that reaches every place an agent's name
// lives: the herdr workspace (what the agents sidebar shows), the lasso record
// (the name list_agents/get_agent hand out) and herdr's agent label. Renaming
// one and not the others is how an agent ends up unreachable by the name on
// screen.
//
// A worktree agent's herdr TAB is not one of those places — it's the user's own
// organization of the terminal.
func TestAutoTitleAgentRenamesWorkspaceRecordAndLabelButNotTheTab(t *testing.T) {
	openTitleTestDB(t, "name: SSO loop\ntitle: Fix the SSO loop\n")
	rec := AgentRecord{
		ID: "a1", Host: "local", Title: "the login page redirects in a loop after SSO and",
		Type: "worktree", Agent: "claude", Description: "the login page redirects in a loop after SSO and nobody can sign in",
		WorkspaceID: "ws1", RootPane: "p1",
	}
	if err := appendAgent("local", rec); err != nil {
		t.Fatalf("appendAgent: %v", err)
	}

	b := newRenameFake()
	autoTitleAgent(b, "local", rec)

	if got := b.renamed["ws1"]; got != "Fix the SSO loop" {
		t.Errorf("workspace label = %q, want the generated title", got)
	}
	if _, ok := b.renamed["t1"]; ok {
		t.Error("auto-titling renamed a worktree agent's herdr tab — the tab is the user's, not the agent's")
	}
	if got := b.labels["p1"]; got != "sso-loop" {
		t.Errorf("herdr agent label = %q, want the short name as an agent name", got)
	}
	got, err := findAgentRecord("local", "a1")
	if err != nil {
		t.Fatalf("findAgentRecord: %v", err)
	}
	if got.Title != "Fix the SSO loop" {
		t.Errorf("record title = %q, want the generated title — list_agents would hand out a name the sidebar no longer shows", got.Title)
	}
}

// A scratch agent's home is a tab in the shared Scratch workspace, and a tab
// strip has room for a word or two: the tab gets the short name, the record
// keeps the title.
func TestAutoTitleScratchAgentNamesItsTabShort(t *testing.T) {
	openTitleTestDB(t, "name: SSO loop\ntitle: Fix the SSO loop\n")
	rec := AgentRecord{
		ID: "a1", Host: "local", Title: "the login page redirects", Type: "scratch", Agent: "claude",
		Description: "the login page redirects in a loop after SSO", WorkspaceID: "ws1", RootPane: "p1",
	}
	if err := appendAgent("local", rec); err != nil {
		t.Fatalf("appendAgent: %v", err)
	}
	b := newRenameFake()
	b.wsLabel = scratchWorkspaceLabel
	autoTitleAgent(b, "local", rec)

	if got := b.renamed["t1"]; got != "SSO loop" {
		t.Errorf("scratch tab = %q, want the short name", got)
	}
	if _, ok := b.renamed["ws1"]; ok {
		t.Error("renamed the shared Scratch workspace")
	}
	if got, _ := findAgentRecord("local", "a1"); got.Title != "Fix the SSO loop" {
		t.Errorf("record title = %q, want the long title", got.Title)
	}
}

// herdr refuses a label until it has detected the agent, which is usually
// after the titler has answered (the boot is still launching the CLI), and
// refuses a name another live agent holds.
func TestLabelAgentWaitsForTheAgentAndSkipsTakenNames(t *testing.T) {
	openTitleTestDB(t, "")
	labelPoll = time.Millisecond
	t.Cleanup(func() { labelPoll = 2 * time.Second })

	b := newRenameFake()
	b.notFound = 3
	b.taken["sso-loop"] = true
	got, err := labelAgent(b, "local", "p1", "SSO loop", labelWait)
	if err != nil {
		t.Fatalf("labelAgent: %v", err)
	}
	if got != "sso-loop-2" || b.labels["p1"] != "sso-loop-2" {
		t.Errorf("label = %q (herdr has %q), want sso-loop-2", got, b.labels["p1"])
	}
}

// A label already on the pane is someone's choice (a human's rename, a harness
// naming itself), not a default to improve on.
func TestLabelAgentKeepsAnExistingLabel(t *testing.T) {
	openTitleTestDB(t, "")
	b := newRenameFake()
	b.labels["p1"] = "mine"
	got, err := labelAgent(b, "local", "p1", "SSO loop", labelWait)
	if err != nil || got != "" {
		t.Fatalf("labelAgent = %q, %v; want no label set", got, err)
	}
	if b.labels["p1"] != "mine" {
		t.Errorf("label = %q, want the existing one kept", b.labels["p1"])
	}
}

// Giving up is bounded: a boot that never brings an agent up must not pin the
// goroutine forever.
func TestLabelAgentGivesUpWhenNoAgentAppears(t *testing.T) {
	openTitleTestDB(t, "")
	labelPoll = time.Millisecond
	t.Cleanup(func() { labelPoll = 2 * time.Second })

	b := newRenameFake()
	b.notFound = 1 << 30
	if _, err := labelAgent(b, "local", "p1", "SSO loop", 20*time.Millisecond); !errors.Is(err, errAgentNeverAppeared) {
		t.Errorf("err = %v, want errAgentNeverAppeared", err)
	}
}

func TestLabelCandidateStaysWithinHerdrsLimit(t *testing.T) {
	base := strings.Repeat("a", 32)
	if got := labelCandidate(base, 1); got != base {
		t.Errorf("first candidate = %q, want the base", got)
	}
	if got := labelCandidate(base, 2); len(got) > 32 || !strings.HasSuffix(got, "-2") {
		t.Errorf("candidate = %q, want <= 32 chars ending -2", got)
	}
}

// The toggle has to default ON with nothing stored — the setting is written
// only when someone opens Settings and flips it, so every existing install
// reads an unset key and must still get the feature.
func TestAutoTitleToggleDefaultsOnAndRoundTrips(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)

	get := func() bool {
		rec := httptest.NewRecorder()
		serveAutoTitle(rec, httptest.NewRequest(http.MethodGet, "/api/auto-title", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Enabled
	}

	if !get() || !autoTitleEnabled() {
		t.Error("auto-titling is off with the setting unset, want on by default")
	}
	rec := httptest.NewRecorder()
	serveAutoTitle(rec, httptest.NewRequest(http.MethodPost, "/api/auto-title",
		strings.NewReader(`{"enabled":false}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if get() || autoTitleEnabled() {
		t.Error("auto-titling still on after being switched off")
	}
}

// createAgent has to actually reach auto-titling — the whole feature hangs off
// one conditional at the end of a long function, and a create whose title came
// from the prompt is the case the UI always produces.
func TestCreateAgentAutoTitlesAPromptDerivedTitle(t *testing.T) {
	called := newTitlerSpy(t, "Generated title")

	b := &createAgentBackend{memBackend: newMemBackend()}
	prev := defaultBackend()
	setDefaultBackend(b)
	t.Cleanup(func() { setDefaultBackend(prev) })

	if _, err := createAgent(b, createAgentReq{
		Type: "scratch", Prompt: "the login page redirects in a loop after SSO", NoFocus: true,
	}); err != nil {
		t.Fatalf("createAgent: %v", err)
	}
	select {
	case <-called:
	case <-time.After(10 * time.Second):
		t.Fatal("no titler CLI ran for an agent titled from its prompt")
	}
}

// An explicit title is the caller's choice, not a default to improve on: the
// MCP tool exposes one precisely for a machine-written prompt whose first line
// is useless, so overwriting it would discard the better name.
func TestCreateAgentLeavesAnExplicitTitleAlone(t *testing.T) {
	called := newTitlerSpy(t, "Generated title")

	b := &createAgentBackend{memBackend: newMemBackend()}
	prev := defaultBackend()
	setDefaultBackend(b)
	t.Cleanup(func() { setDefaultBackend(prev) })

	rec, err := createAgent(b, createAgentReq{
		Type: "scratch", Title: "Chosen name", Prompt: "a long machine-written prompt", NoFocus: true,
	})
	if err != nil {
		t.Fatalf("createAgent: %v", err)
	}
	if rec.Title != "Chosen name" {
		t.Errorf("title = %q, want the caller's", rec.Title)
	}
	select {
	case <-called:
		t.Error("a titler CLI ran for an agent whose title the caller chose")
	case <-time.After(250 * time.Millisecond):
	}
}

// newTitlerSpy opens a scratch state db and swaps in a titler that reports it
// ran on the returned channel — the shared setup of the two createAgent gate
// tests, which differ only in whether that channel should ever fire.
func newTitlerSpy(t *testing.T, title string) <-chan struct{} {
	t.Helper()
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)
	called := make(chan struct{}, len(titlers))
	titlerRunner = func(titler, string) (string, error) {
		called <- struct{}{}
		return title, nil
	}
	t.Cleanup(func() { titlerRunner = runTitler })
	return called
}

// A background failure's only route to the user is the SSE stream's "notice"
// frame — get the event name or the shape wrong and the toast silently never
// fires, which is indistinguishable from the failure never happening.
func TestHubNoticeReachesTheEventStream(t *testing.T) {
	// The stream is per host now, so it needs a host to resolve to. Its poll
	// against a herdr that isn't there just marks the feed down; the notice
	// fan-out under test is global and unaffected.
	//
	// The hub's context dies with the test (newTestHub, not newHub): that poll
	// FAILS every *pollEvery for as long as it runs, and it is filed under the
	// default host — which is never idle-stopped — in the process-global
	// pane.list cache. Left running, this one test poisoned "local" for the rest
	// of the binary: any later test whose feed refreshed inside the 400ms the
	// error stayed fresh was served it instead of polling its own backend, which
	// is what made TestFeedsAreIndependentPerHost intermittent.
	setDefaultBackend(&localBackend{})
	t.Cleanup(func() { setDefaultBackend(nil) })
	h := newTestHub(t)
	srv := httptest.NewServer(http.HandlerFunc(h.serveSSE))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	// Wait for the priming "active" frame — proof the client is registered, so
	// the notice can't race the subscription.
	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read priming frame: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}

	before := time.Now().UnixMilli()
	h.notify(notice{Level: "error", Title: "Couldn't auto-title", Detail: "claude: not installed"})
	h.notify(notice{Level: "info", Title: "Second"})
	after := time.Now().UnixMilli()

	// The host feed pushes its own "active" frames on the same stream, so skip
	// past any that arrive before the notice rather than reading the first frame
	// and asserting on it.
	next := func() notice {
		var event, data string
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("read notice frame: %v", err)
			}
			switch {
			case strings.HasPrefix(line, "event: "):
				event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				data = ""
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			}
			if event == "notice" && data != "" {
				break
			}
		}
		var got notice
		if err := json.Unmarshal([]byte(data), &got); err != nil {
			t.Fatalf("decode notice: %v (%s)", err, data)
		}
		return got
	}
	got := next()
	if got.Level != "error" || got.Title != "Couldn't auto-title" || got.Detail != "claude: not installed" {
		t.Errorf("notice = %+v, want the one sent", got)
	}
	// Each notice is stamped with an identity a client can keep it under, and
	// when it was raised.
	second := next()
	if got.ID == "" || second.ID == "" || got.ID == second.ID {
		t.Errorf("ids = %q, %q, want two distinct ones", got.ID, second.ID)
	}
	for _, n := range []notice{got, second} {
		if n.At < before || n.At > after {
			t.Errorf("%q at = %d, want within [%d, %d]", n.Title, n.At, before, after)
		}
	}
}

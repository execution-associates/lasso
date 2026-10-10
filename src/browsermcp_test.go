package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeBrowserMCPEnv, when set, makes the test binary a chrome-devtools-mcp
// stand-in (see TestMain): "ok" serves four tools over stdio, "crash" dies on
// startup the way a broken install does.
const fakeBrowserMCPEnv = "LASSO_TEST_FAKE_BROWSER_MCP"

// fakeJPEG is what the fake's snap tool returns: the bytes must survive the
// child → bridge → client round trip exactly (base64 twice over).
var fakeJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xff, 0xd9}

func runFakeBrowserMCP(mode string) int {
	if mode == "crash" {
		fmt.Fprintln(os.Stderr, "Error: boom: fake chrome-devtools-mcp failure")
		return 3
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-chrome-devtools-mcp", Version: "0"}, nil)
	obj := map[string]any{"type": "object", "properties": map[string]any{
		"x":      map[string]any{"type": "number", "description": "a number; list_pages has none"},
		"pageId": map[string]any{"type": "number", "description": "The page."},
	}}
	srv.AddTool(&mcp.Tool{Name: "echo", Description: "echo the arguments and argv (click through list_pages first)", InputSchema: obj,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, _ := json.Marshal(map[string]any{"args": req.Params.Arguments, "argv": os.Args[1:]})
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "snap", Description: "a screenshot", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "took a screenshot"},
				&mcp.ImageContent{MIMEType: "image/jpeg", Data: fakeJPEG},
			}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "env", Description: "the environment", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(os.Environ(), "\n")}}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "list_pages", Description: "list the pages", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "[]"}}}, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil && !errors.Is(err, io.EOF) {
		return 1
	}
	return 0
}

// fakeToolCount is how many tools the fake child serves.
const fakeToolCount = 4

// testBrowserMCP is a bridge whose chrome-devtools-mcp is this test binary.
// max <= 0 is no limit, the production default.
func testBrowserMCP(t *testing.T, mode string, max int) *browserMCPBridge {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b := newBrowserMCPBridge(browserMCPConfig{Binary: exe, Max: max, ExtraEnv: []string{fakeBrowserMCPEnv + "=" + mode}})
	b.setListenAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1})
	t.Cleanup(func() { b.closeAll("test over") })
	return b
}

// mcpServerWith is lasso's /mcp exactly as main wires it — withMCPAuth around
// withBrowserToolGate around the real server — with b as the browser bridge.
func mcpServerWith(t *testing.T, b *browserMCPBridge, user, pass string, hasAuth bool) *httptest.Server {
	t.Helper()
	prev := browserMCP
	browserMCP = b
	t.Cleanup(func() { browserMCP = prev })
	h := withMCPAuth(withBrowserToolGate(withRequestBase(newMCPHandler()), user, pass, hasAuth), user, pass, hasAuth)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// hdrRT sends fixed headers (and basic credentials) on every request: an
// agent's MCP client configured with them.
type hdrRT struct {
	h          http.Header
	user, pass string
}

func (h hdrRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h.h {
		r.Header[k] = v
	}
	if h.user != "" {
		r.SetBasicAuth(h.user, h.pass)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mcpConnect(t *testing.T, endpoint string, rt http.RoundTripper) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, DisableStandaloneSSE: true}
	if rt != nil {
		tr.HTTPClient = &http.Client{Transport: rt}
	}
	sess, err := c.Connect(ctx, tr, nil)
	if err == nil {
		t.Cleanup(func() { _ = sess.Close() })
	}
	return sess, err
}

func mustConnect(t *testing.T, endpoint string, rt http.RoundTripper) *mcp.ClientSession {
	t.Helper()
	sess, err := mcpConnect(t, endpoint, rt)
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// childPIDs is every live child's pid, by browser, across all sessions.
func (b *browserMCPBridge) childPIDs() map[string][]int {
	out := map[string][]int{}
	for _, s := range b.snapshot() {
		s.mu.Lock()
		for p, sl := range s.children {
			if sl.child != nil {
				out[p] = append(out[p], sl.child.pid)
			}
		}
		s.mu.Unlock()
	}
	return out
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("child pid %d is still running", pid)
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitChildren(t *testing.T, b *browserMCPBridge, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b.liveChildren() == n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("live children = %d, want %d", b.liveChildren(), n)
}

// fakeEcho is what the fake's echo tool answers.
type fakeEcho struct {
	Args map[string]any `json:"args"`
	Argv []string       `json:"argv"`
}

// wsEndpoint is the --wsEndpoint the child was started with: which browser's
// /cdp it dials.
func (e fakeEcho) wsEndpoint() string {
	for i, a := range e.Argv {
		if a == "--wsEndpoint" && i+1 < len(e.Argv) {
			return e.Argv[i+1]
		}
	}
	return ""
}

// callEcho calls browser_echo. A tool error comes back as its text in refusal,
// with a zero fakeEcho.
func callEcho(t *testing.T, sess *mcp.ClientSession, args map[string]any) (out fakeEcho, refusal string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "browser_echo", Arguments: args})
	if err != nil {
		t.Fatalf("browser_echo %v: %v", args, err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		return fakeEcho{}, text
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("echo answer %q: %v", text, err)
	}
	return out, ""
}

func listTools(t *testing.T, sess *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	byName := map[string]*mcp.Tool{}
	for tl, err := range sess.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		byName[tl.Name] = tl
	}
	return byName
}

func browserToolNames(tools map[string]*mcp.Tool) []string {
	var out []string
	for n := range tools {
		if strings.HasPrefix(n, browserToolPrefix) {
			out = append(out, n)
		}
	}
	return out
}

func TestBrowserToolsMirrorAndForward(t *testing.T) {
	t.Setenv("UI_AUTH", "u:secret")
	t.Setenv("MCP_OAUTH", "cid:csecret")
	openTestDB(t) // resolving a named browser reads the stored list
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)
	sess := mustConnect(t, srv.URL, nil)
	init := sess.InitializeResult()
	if init.ServerInfo.Name != "lasso" || !strings.Contains(init.Instructions, "browser_new_page") || !strings.Contains(init.Instructions, `"browser"`) {
		t.Errorf("initialize = %+v / %q", init.ServerInfo, init.Instructions)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// tools/list carries lasso's own tools AND the child's, renamed browser_*,
	// with descriptions, schemas and annotations as the child gave them — plus
	// the optional `browser`.
	byName := listTools(t, sess)
	if byName["list_agents"] == nil || byName["list_browsers"] == nil || byName["shared_browser"] == nil {
		t.Errorf("lasso's own tools are missing: %v", byName)
	}
	if got := browserToolNames(byName); len(got) != fakeToolCount {
		t.Fatalf("browser tools = %v", got)
	}
	for _, raw := range []string{"echo", "snap", "env", "list_pages"} {
		if byName[raw] != nil {
			t.Errorf("%s is on /mcp under its own name", raw)
		}
	}
	e := byName["browser_echo"]
	if e == nil || e.Annotations == nil || !e.Annotations.ReadOnlyHint {
		t.Fatalf("browser_echo = %+v", e)
	}
	// Sibling tool names in descriptions follow the rename; English words don't.
	if e.Description != "echo the arguments and argv (click through browser_list_pages first)" {
		t.Errorf("description = %q", e.Description)
	}
	es, _ := json.Marshal(e.InputSchema)
	if !strings.Contains(string(es), "a number; browser_list_pages has none") || !strings.Contains(string(es), `"x"`) {
		t.Errorf("schema descriptions not renamed: %s", es)
	}
	if !strings.Contains(string(es), "pass the same `browser` as the browser_list_pages/browser_new_page call") {
		t.Errorf("pageId does not say it belongs to one browser: %s", es)
	}
	for _, name := range []string{"browser_echo", "browser_snap", "browser_env", "browser_list_pages"} {
		s, _ := json.Marshal(byName[name].InputSchema)
		if !strings.Contains(string(s), `"browser":{"description":"Which browser to run this in`) {
			t.Errorf("%s schema lacks browser: %s", name, s)
		}
		if strings.Contains(string(s), `"required":["browser"`) {
			t.Errorf("%s: browser must be optional: %s", name, s)
		}
	}

	// tools/call forwards the arguments as given, minus `browser`, under the
	// child's own tool name, and the child ran with lasso's flags: its own
	// /cdp, the internal token, the screenshot bounds.
	echoed, msg := callEcho(t, sess, map[string]any{"x": 7, "browser": "default"})
	if msg != "" {
		t.Fatal(msg)
	}
	if echoed.Args["x"] != float64(7) || echoed.Args["browser"] != nil || len(echoed.Args) != 1 {
		t.Errorf("args = %v", echoed.Args)
	}
	argv := strings.Join(echoed.Argv, " ")
	for _, want := range []string{
		"--wsEndpoint ws://127.0.0.1:1/cdp",
		`--wsHeaders {"X-Lasso-Internal":"` + internalCDPToken + `"}`,
		"--no-usage-statistics", "--no-performance-crux",
		"--screenshotFormat jpeg", "--screenshotMaxWidth 1280", "--screenshotMaxHeight 1280",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv %q lacks %q", argv, want)
		}
	}

	// Image content comes back byte for byte.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "browser_snap"})
	if err != nil || len(res.Content) != 2 {
		t.Fatalf("snap: %v %+v", err, res)
	}
	img, ok := res.Content[1].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/jpeg" || !bytes.Equal(img.Data, fakeJPEG) {
		t.Errorf("image = %#v", res.Content[1])
	}

	// lasso's credentials never reach the child.
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "browser_env"})
	if err != nil {
		t.Fatal(err)
	}
	env := res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(env, "UI_AUTH") || strings.Contains(env, "MCP_OAUTH") || strings.Contains(env, "secret") {
		t.Errorf("child env leaks credentials:\n%s", env)
	}
	if !strings.Contains(env, "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS=1") {
		t.Errorf("child env lacks the telemetry opt-out:\n%s", env)
	}
	if strings.Contains(env, browserGateToken) {
		t.Errorf("child env carries the gate token")
	}

	// Three calls to the default browser: one child, reused.
	pids := b.childPIDs()
	if len(pids) != 1 || len(pids[defaultBrowserProfile]) != 1 || pids[defaultBrowserProfile][0] <= 1 {
		t.Fatalf("live pids = %v", pids)
	}
	if b.sessions() != 1 || b.liveChildren() != 1 {
		t.Errorf("sessions=%d children=%d", b.sessions(), b.liveChildren())
	}
	// Closing the session (DELETE) kills the child.
	_ = sess.Close()
	waitChildren(t, b, 0)
	waitGone(t, pids[defaultBrowserProfile][0])
}

// initialize and tools/list spawn nothing: the tool list comes from a cache,
// learned by ONE short probe per lasso process (per binary), not per session.
func TestBrowserToolsLazySpawn(t *testing.T) {
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)

	var sessions []*mcp.ClientSession
	for i := 0; i < 5; i++ {
		sess := mustConnect(t, srv.URL, nil)
		if got := browserToolNames(listTools(t, sess)); len(got) != fakeToolCount {
			t.Fatalf("session %d browser tools: %v", i, got)
		}
		sessions = append(sessions, sess)
	}
	if n := b.probes.Load(); n != 1 {
		t.Errorf("probes = %d, want exactly 1 for 5 sessions", n)
	}
	if b.liveChildren() != 0 || b.sessions() != 0 || len(b.snapshot()) != 0 {
		t.Errorf("after initialize + tools/list: children=%d sessions=%d", b.liveChildren(), len(b.snapshot()))
	}

	// The first tool call is what spawns.
	if _, msg := callEcho(t, sessions[2], nil); msg != "" {
		t.Fatal(msg)
	}
	if b.liveChildren() != 1 || b.sessions() != 1 {
		t.Errorf("after one call: children=%d sessions=%d", b.liveChildren(), b.sessions())
	}

	// A binary that changed under lasso (an upgrade) is probed again by the
	// next initialize.
	b.tools.Store(&browserMCPToolCache{key: "some older binary", tools: b.tools.Load().tools})
	sess := mustConnect(t, srv.URL, nil)
	if got := browserToolNames(listTools(t, sess)); len(got) != fakeToolCount {
		t.Fatalf("after re-probe: %v", got)
	}
	if n := b.probes.Load(); n != 2 {
		t.Errorf("probes after a binary change = %d, want 2", n)
	}
}

// Calls route to the browser they name, each browser getting its own child in
// the session, and an unknown browser is an error naming the real ones.
func TestBrowserToolsRouteByBrowser(t *testing.T) {
	f := testFleet(t)
	if _, err := f.create("Work", "", ""); err != nil {
		t.Fatal(err)
	}
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{nil, "ws://127.0.0.1:1/cdp"},
		{map[string]any{"browser": "work"}, "ws://127.0.0.1:1/cdp/p/work"},
		{map[string]any{"browser": "WORK"}, "ws://127.0.0.1:1/cdp/p/work"}, // the display name, any case
		{map[string]any{"browser": ""}, "ws://127.0.0.1:1/cdp"},
		{map[string]any{"browser": "default", "x": 1}, "ws://127.0.0.1:1/cdp"},
	} {
		got, msg := callEcho(t, sess, c.args)
		if msg != "" {
			t.Fatalf("%v: %s", c.args, msg)
		}
		if got.wsEndpoint() != c.want {
			t.Errorf("%v dialed %q, want %q", c.args, got.wsEndpoint(), c.want)
		}
		if _, has := got.Args["browser"]; has {
			t.Errorf("%v: browser reached the child: %v", c.args, got.Args)
		}
	}
	pids := b.childPIDs()
	if len(pids[defaultBrowserProfile]) != 1 || len(pids["work"]) != 1 || b.liveChildren() != 2 {
		t.Errorf("children = %v (%d)", pids, b.liveChildren())
	}

	// Unknown: a tool error listing the browsers, nothing spawned, and no
	// fallback to another browser.
	_, msg := callEcho(t, sess, map[string]any{"browser": "personal", "x": 1})
	if !strings.Contains(msg, `no browser "personal"`) || !strings.Contains(msg, `work ("Work")`) || !strings.Contains(msg, "default") {
		t.Errorf("unknown browser: %q", msg)
	}
	if _, msg := callEcho(t, sess, map[string]any{"browser": 3}); !strings.Contains(msg, "must be a string") {
		t.Errorf("non-string browser: %q", msg)
	}
	if b.liveChildren() != 2 {
		t.Errorf("a refused call spawned: %d children", b.liveChildren())
	}
}

// Browsers resolve at call time: one created after the session started works
// without reconnecting, and one deleted since is refused.
func TestBrowserToolsBrowserCreatedAfterSessionStart(t *testing.T) {
	f := testFleet(t)
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
	if _, msg := callEcho(t, sess, map[string]any{"browser": "late"}); !strings.Contains(msg, `no browser "late"`) {
		t.Fatalf("before it exists: %q", msg)
	}
	if _, err := f.create("Late", "late", ""); err != nil {
		t.Fatal(err)
	}
	got, msg := callEcho(t, sess, map[string]any{"browser": "Late"})
	if msg != "" || got.wsEndpoint() != "ws://127.0.0.1:1/cdp/p/late" {
		t.Fatalf("after create: %q %q", msg, got.wsEndpoint())
	}
	if err := f.remove(context.Background(), "late"); err != nil {
		t.Fatal(err)
	}
	if _, msg := callEcho(t, sess, map[string]any{"browser": "late"}); !strings.Contains(msg, `no browser "late"`) {
		t.Errorf("after delete: %q", msg)
	}
}

// One browser stopping closes only that browser's child in each session; the
// session and its other children carry on, and the next call to the stopped
// browser spawns a fresh child.
func TestBrowserToolsStopClosesOnlyThatChild(t *testing.T) {
	f := testFleet(t)
	if _, err := f.create("Work", "work", ""); err != nil {
		t.Fatal(err)
	}
	b := testBrowserMCP(t, "ok", 0)
	f.onStop = b.browserStoppedFor
	sharedBrowser.onStop = b.browserStopped
	work := runProfileOn(t, f, "work", newFakeChromium(t))
	srv := mcpServerWith(t, b, "", "", false)

	a := mustConnect(t, srv.URL, nil)
	other := mustConnect(t, srv.URL, nil)
	for _, s := range []*mcp.ClientSession{a, other} {
		for _, p := range []string{"default", "work"} {
			if _, msg := callEcho(t, s, map[string]any{"browser": p}); msg != "" {
				t.Fatal(msg)
			}
		}
	}
	before := b.childPIDs()
	if len(before["work"]) != 2 || len(before[defaultBrowserProfile]) != 2 {
		t.Fatalf("children = %v", before)
	}

	if err := work.stop(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	for _, pid := range before["work"] {
		waitGone(t, pid)
	}
	after := b.childPIDs()
	if len(after["work"]) != 0 || len(after[defaultBrowserProfile]) != 2 {
		t.Errorf("after the work stop: %v", after)
	}
	for _, pid := range before[defaultBrowserProfile] {
		if !alive(pid) {
			t.Errorf("default child %d died with the work browser", pid)
		}
	}
	if len(b.snapshot()) != 2 {
		t.Errorf("sessions = %d, want both still open", len(b.snapshot()))
	}

	got, msg := callEcho(t, a, nil)
	if msg != "" || got.wsEndpoint() != "ws://127.0.0.1:1/cdp" {
		t.Fatalf("default after the stop: %q %q", msg, got.wsEndpoint())
	}
	if got, msg := callEcho(t, a, map[string]any{"browser": "work"}); msg != "" || got.wsEndpoint() != "ws://127.0.0.1:1/cdp/p/work" {
		t.Fatalf("work after the stop: %q %q", msg, got.wsEndpoint())
	}
	now := b.childPIDs()
	if len(now["work"]) != 1 || now["work"][0] == before["work"][0] || now["work"][0] == before["work"][1] {
		t.Errorf("work children after respawn = %v (before %v)", now["work"], before["work"])
	}
}

// A child that dies on its own is dropped; the session survives and the next
// call to that browser respawns.
func TestBrowserToolsChildDeathRespawns(t *testing.T) {
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Fatal(msg)
	}
	pid := b.childPIDs()[defaultBrowserProfile][0]
	_ = syscall.Kill(pid, syscall.SIGKILL)
	waitChildren(t, b, 0)
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Fatalf("after the child died: %s", msg)
	}
	if p := b.childPIDs()[defaultBrowserProfile]; len(p) != 1 || p[0] == pid {
		t.Errorf("respawned = %v (old %d)", p, pid)
	}
}

// A session that makes no browser call for browserMCPIdle has its children
// stopped (/mcp sessions have no timeout of their own); it stays a session, and
// its next call spawns afresh.
func TestBrowserToolsIdleSessionLosesItsChildren(t *testing.T) {
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Fatal(msg)
	}
	pid := b.childPIDs()[defaultBrowserProfile][0]
	b.reapIdle(time.Now().Add(browserMCPIdle - time.Minute))
	if !alive(pid) || b.liveChildren() != 1 {
		t.Fatalf("reaped before the idle time")
	}
	b.reapIdle(time.Now().Add(browserMCPIdle + time.Minute))
	waitGone(t, pid)
	if b.liveChildren() != 0 || len(b.snapshot()) != 1 {
		t.Errorf("after the reap: children=%d sessions=%d", b.liveChildren(), len(b.snapshot()))
	}
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Fatalf("after the reap: %s", msg)
	}
	if p := b.childPIDs()[defaultBrowserProfile]; len(p) != 1 || p[0] == pid {
		t.Errorf("respawned = %v (old %d)", p, pid)
	}
}

// No limit by default: many sessions and browsers each get their child.
func TestBrowserToolsNoCapByDefault(t *testing.T) {
	if b := newBrowserMCPBridge(browserMCPConfig{}); b.cfg.Max != 0 {
		t.Errorf("default Max = %d, want 0 (no limit)", b.cfg.Max)
	}
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)
	for i := 0; i < 10; i++ {
		sess := mustConnect(t, srv.URL, nil)
		if _, msg := callEcho(t, sess, nil); msg != "" {
			t.Fatalf("session %d: %s", i, msg)
		}
	}
	if b.liveChildren() != 10 {
		t.Errorf("children = %d, want 10", b.liveChildren())
	}
}

// The opt-in limit counts children, not sessions: a session over it still
// opens, and its first browser call is refused naming the knob.
func TestBrowserToolsOptInCap(t *testing.T) {
	testFleet(t)
	if _, err := sharedBrowsers.create("Work", "work", ""); err != nil {
		t.Fatal(err)
	}
	b := testBrowserMCP(t, "ok", 1)
	srv := mcpServerWith(t, b, "", "", false)
	first := mustConnect(t, srv.URL, nil)
	if _, msg := callEcho(t, first, nil); msg != "" {
		t.Fatal(msg)
	}
	second := mustConnect(t, srv.URL, nil)
	if _, msg := callEcho(t, second, nil); !strings.Contains(msg, "limit of 1") || !strings.Contains(msg, "LASSO_BROWSER_MCP_MAX") {
		t.Errorf("second session's call over the cap: %q", msg)
	}
	if _, msg := callEcho(t, first, map[string]any{"browser": "work"}); !strings.Contains(msg, "limit of 1") {
		t.Errorf("a second browser in the same session over the cap: %q", msg)
	}
	b.mu.Lock()
	starting := b.starting
	b.mu.Unlock()
	if b.liveChildren() != 1 || starting != 0 {
		t.Errorf("after refusals: children=%d starting=%d", b.liveChildren(), starting)
	}
	_ = first.Close()
	waitChildren(t, b, 0)
	if _, msg := callEcho(t, second, nil); msg != "" {
		t.Fatalf("a slot freed by a close is reusable: %s", msg)
	}
}

// With no usable chrome-devtools-mcp, /mcp works as ever and simply has no
// browser_* tools; shared_browser says why. Tools registered while it was
// there are withdrawn by the next initialize once it is gone.
func TestBrowserToolsUnavailable(t *testing.T) {
	openTestDB(t)
	for _, c := range []struct {
		cfg  browserMCPConfig
		look func(string) (string, error)
		want []string
	}{
		{browserMCPConfig{Binary: "/nonexistent/chrome-devtools-mcp"}, nil, []string{"does not exist", "npm i -g chrome-devtools-mcp"}},
		{browserMCPConfig{Binary: "off"}, nil, []string{"disabled"}},
		{browserMCPConfig{}, func(string) (string, error) { return "", errors.New("not found") }, []string{"not installed", "mise use -g npm:chrome-devtools-mcp"}},
	} {
		b := newBrowserMCPBridge(c.cfg)
		if c.look != nil {
			b.lookPath = c.look
		}
		sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
		tools := listTools(t, sess)
		if tools["list_agents"] == nil || len(browserToolNames(tools)) != 0 {
			t.Errorf("%+v: tools = %v", c.cfg, tools)
		}
		var out sharedBrowserOut
		if msg := callTool(t, sess, "shared_browser", map[string]any{"start": false}, &out); msg != "" {
			t.Fatal(msg)
		}
		for _, w := range c.want {
			if out.BrowserTools || !strings.Contains(out.ToolsReason, w) {
				t.Errorf("%+v: shared_browser = %v %q, want %q", c.cfg, out.BrowserTools, out.ToolsReason, w)
			}
		}
	}
	var nilBridge *browserMCPBridge
	if _, reason, ok := nilBridge.resolve(); ok || !strings.Contains(reason, "not configured") {
		t.Errorf("unconfigured: %v %q", ok, reason)
	}

	// Registered, then switched off: the next initialize withdraws them.
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)
	sess := mustConnect(t, srv.URL, nil)
	if got := browserToolNames(listTools(t, sess)); len(got) != fakeToolCount {
		t.Fatalf("browser tools = %v", got)
	}
	b.cfg.Binary = "off"
	sess = mustConnect(t, srv.URL, nil)
	if got := browserToolNames(listTools(t, sess)); len(got) != 0 {
		t.Errorf("after off: browser tools = %v", got)
	}
}

// A chrome-devtools-mcp that cannot start is never /mcp's problem: initialize
// succeeds without browser tools, and the broken binary is not re-probed on
// every initialize.
func TestBrowserToolsProbeFailure(t *testing.T) {
	b := testBrowserMCP(t, "crash", 4)
	srv := mcpServerWith(t, b, "", "", false)
	for i := 0; i < 3; i++ {
		sess, err := mcpConnect(t, srv.URL, nil)
		if err != nil {
			t.Fatalf("a broken chrome-devtools-mcp broke /mcp: %v", err)
		}
		if got := browserToolNames(listTools(t, sess)); len(got) != 0 {
			t.Errorf("browser tools from a crashing child: %v", got)
		}
	}
	if n := b.probes.Load(); n != 1 {
		t.Errorf("probes = %d, want 1 (a failure is remembered)", n)
	}
	if len(b.snapshot()) != 0 || b.liveChildren() != 0 || b.starting != 0 {
		t.Errorf("after a failed probe: sessions=%d children=%d starting=%d", len(b.snapshot()), b.liveChildren(), b.starting)
	}
}

// With the tool list already learned, a child that fails to start is the tool
// call's error (isError, with the reason), not a broken session.
func TestBrowserToolsSpawnFailureIsAToolError(t *testing.T) {
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
	b.cfg.ExtraEnv = []string{fakeBrowserMCPEnv + "=crash"}
	_, msg := callEcho(t, sess, nil)
	if !strings.Contains(msg, "exited during startup") || !strings.Contains(msg, "exit status 3") || !strings.Contains(msg, "boom") {
		t.Errorf("spawn failure: %q", msg)
	}
	b.cfg.ExtraEnv = []string{fakeBrowserMCPEnv + "=ok"}
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Errorf("a failed spawn is not remembered; the next call retries: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// the gate: /cdp's standard on every browser tool call
// ---------------------------------------------------------------------------

// Under UI_AUTH alone /mcp is open, but the browser tools are not: a call
// without the UI_AUTH credentials (or with wrong ones, or with a forged gate
// header) is a tool error and spawns nothing, while lasso's other tools work.
func TestBrowserToolsRefusedUnderUIAuthWithoutCredentials(t *testing.T) {
	prev := oauthCfg
	oauthCfg = oauthConf{}
	t.Cleanup(func() { oauthCfg = prev })
	testFleet(t)
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "u", "p", true)

	forged := http.Header{browserGateHeader: {"ok"}, browserRefusalHeader: {""}}
	for name, rt := range map[string]http.RoundTripper{
		"none":   nil,
		"wrong":  hdrRT{user: "u", pass: "wrong"},
		"forged": hdrRT{h: forged},
	} {
		sess, err := mcpConnect(t, srv.URL, rt)
		if err != nil {
			t.Fatalf("%s: /mcp itself is open under UI_AUTH alone: %v", name, err)
		}
		tools := listTools(t, sess)
		if len(browserToolNames(tools)) != fakeToolCount {
			t.Errorf("%s: browser tools not listed: %v", name, tools)
		}
		if _, msg := callEcho(t, sess, nil); !strings.Contains(msg, "UI_AUTH") {
			t.Errorf("%s: browser call = %q, want a UI_AUTH refusal", name, msg)
		}
		var browsers listBrowsersOut
		if msg := callTool(t, sess, "list_browsers", nil, &browsers); msg != "" || len(browsers.Browsers) == 0 {
			t.Errorf("%s: lasso's other tools should not need UI_AUTH: %s", name, msg)
		}
	}
	if b.liveChildren() != 0 || len(b.snapshot()) != 0 {
		t.Errorf("refused calls spawned: children=%d sessions=%d", b.liveChildren(), len(b.snapshot()))
	}
	sess := mustConnect(t, srv.URL, hdrRT{user: "u", pass: "p"})
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Errorf("with the credentials: %s", msg)
	}
}

// A foreign Origin is refused whatever the credentials — a web page must not
// drive the browser through a loopback lasso — while no Origin (an agent's MCP
// client) and lasso's own origin pass.
func TestBrowserToolsRefuseForeignOrigin(t *testing.T) {
	prev := oauthCfg
	oauthCfg = oauthConf{}
	t.Cleanup(func() { oauthCfg = prev })
	openTestDB(t)
	b := testBrowserMCP(t, "ok", 0)

	open := mcpServerWith(t, b, "", "", false)
	for _, origin := range []string{"https://evil.example", "null", "http://127.0.0.1:1"} {
		sess := mustConnect(t, open.URL, hdrRT{h: http.Header{"Origin": {origin}}})
		if _, msg := callEcho(t, sess, nil); !strings.Contains(msg, "cross-origin") {
			t.Errorf("open, Origin %s: %q", origin, msg)
		}
	}
	if b.liveChildren() != 0 {
		t.Errorf("a cross-origin call spawned")
	}
	sess := mustConnect(t, open.URL, hdrRT{h: http.Header{"Origin": {open.URL}}})
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Errorf("lasso's own origin: %s", msg)
	}

	authed := mcpServerWith(t, b, "u", "p", true)
	sess = mustConnect(t, authed.URL, hdrRT{h: http.Header{"Origin": {"https://evil.example"}}, user: "u", pass: "p"})
	if _, msg := callEcho(t, sess, nil); !strings.Contains(msg, "cross-origin") {
		t.Errorf("UI_AUTH, foreign Origin with credentials: %q", msg)
	}
}

// Under MCP_OAUTH /mcp needs a credential, and the browser tools also need its
// reach to include lasso's own machine, where the browsers run.
func TestBrowserToolsRefuseNonLocalCallerUnderOAuth(t *testing.T) {
	openTestDB(t)
	enableOAuth(t, "")
	stubSSHHosts(t, "gigachad")
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)

	if _, err := mcpConnect(t, srv.URL, nil); err == nil {
		t.Errorf("no token: /mcp answered")
	}
	remote := mustConnect(t, srv.URL, bearerRT{token: hostClientToken(t, "gigachad", scopeSelf)})
	if _, msg := callEcho(t, remote, nil); !strings.Contains(msg, "outside this credential's reach") {
		t.Errorf("self-scoped remote token: %q", msg)
	}
	if b.liveChildren() != 0 {
		t.Errorf("a refused call spawned")
	}
	local := hostClientToken(t, "local", scopeSelf)
	sess := mustConnect(t, srv.URL, bearerRT{token: local})
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Errorf("lasso-host token: %s", msg)
	}
	evil := mustConnect(t, srv.URL, hdrRT{h: http.Header{"Origin": {"https://evil.example"}, "Authorization": {"Bearer " + local}}})
	if _, msg := callEcho(t, evil, nil); !strings.Contains(msg, "cross-origin") {
		t.Errorf("foreign Origin with a good token: %q", msg)
	}
}

// A call that never passed withBrowserToolGate (no HTTP request, or a header a
// client made up) is refused: the verdict is a per-process secret, not a flag.
func TestBrowserToolRefusalWithoutTheGate(t *testing.T) {
	for _, req := range []*mcp.CallToolRequest{
		nil,
		{},
		{Extra: &mcp.RequestExtra{Header: http.Header{}}},
		{Extra: &mcp.RequestExtra{Header: http.Header{browserGateHeader: {"ok"}}}},
		{Extra: &mcp.RequestExtra{Header: http.Header{browserGateHeader: {browserGateToken[:63] + "x"}}}},
	} {
		if browserToolRefusal(req) == "" {
			t.Errorf("%+v passed", req)
		}
	}
	ok := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{browserGateHeader: {browserGateToken}}}}
	if why := browserToolRefusal(ok); why != "" {
		t.Errorf("the real token: %s", why)
	}

	// The wrapper overwrites whatever a client sent.
	var seen http.Header
	h := withBrowserToolGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Clone() }), "u", "p", true)
	r := httptest.NewRequest("POST", "http://lasso.lan:8190/mcp", nil)
	r.Header.Set(browserGateHeader, browserGateToken) // even the right value, guessed
	r.Header.Set(browserRefusalHeader, "")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if seen.Get(browserGateHeader) != "" || !strings.Contains(seen.Get(browserRefusalHeader), "UI_AUTH") {
		t.Errorf("headers after the gate: %v", seen)
	}
}

func TestBrowserToolRename(t *testing.T) {
	in := &mcp.Tool{Name: "take_snapshot", Description: "Take a snapshot. Prefer it to take_screenshot; click and fill use its uids. See list_pages.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"filePath": map[string]any{"type": "string", "description": "Like take_screenshot's."},
			"browser":  nil,
		}}}
	out, own := browserTool(in, []string{"take_snapshot", "take_screenshot", "click", "fill", "list_pages"})
	if out.Name != "browser_take_snapshot" || !own {
		t.Errorf("name %q own %v", out.Name, own)
	}
	if out.Description != "Take a snapshot. Prefer it to browser_take_screenshot; click and fill use its uids. See browser_list_pages." {
		t.Errorf("description = %q", out.Description)
	}
	s, _ := json.Marshal(out.InputSchema)
	if !strings.Contains(string(s), "Like browser_take_screenshot's.") {
		t.Errorf("schema = %s", s)
	}
	if in.Name != "take_snapshot" || strings.Contains(in.Description, "browser_") {
		t.Errorf("the cached tool was modified: %+v", in)
	}
}

func TestInternalCDPToken(t *testing.T) {
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) })
	cdp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(299) })
	h := withInternalCDP(outer, cdp)
	do := func(path, tok string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8190"+path, nil)
		if tok != "" {
			r.Header.Set(internalCDPHeader, tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if len(internalCDPToken) != 64 {
		t.Fatalf("token length %d", len(internalCDPToken))
	}
	if internalCDPToken == browserGateToken {
		t.Fatalf("the internal CDP token and the browser gate token are the same value")
	}
	for _, p := range []string{"/cdp", "/cdp/", "/cdp/json/list"} {
		if c := do(p, internalCDPToken); c != 299 {
			t.Errorf("right token on %s: %d", p, c)
		}
	}
	if c := do("/cdp", internalCDPToken[:63]+"x"); c != 403 {
		t.Errorf("wrong token: %d", c)
	}
	if c := do("/cdp", ""); c != 403 {
		t.Errorf("no token: %d", c)
	}
	// The token opens /cdp and nothing else.
	for _, p := range []string{"/api/browser", "/mcp", "/cdpx"} {
		if c := do(p, internalCDPToken); c != 403 {
			t.Errorf("right token on %s: %d", p, c)
		}
	}

	// And Chromium never sees it.
	f := newFakeChromium(t)
	m := testBrowserManager(t, f)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8190/cdp/json/version", nil)
	r.Header.Set(internalCDPHeader, internalCDPToken)
	w := httptest.NewRecorder()
	withInternalCDP(outer, http.HandlerFunc(m.serveCDP)).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("proxied: %d %s", w.Code, w.Body)
	}
	if got := f.last().Header.Get(internalCDPHeader); got != "" {
		t.Errorf("internal token forwarded to Chromium")
	}
}

func TestBrowserMCPEnvAllowlist(t *testing.T) {
	got := browserMCPEnv([]string{
		"PATH=/usr/bin", "HOME=/home/u", "UI_AUTH=u:p", "MCP_OAUTH=a:b", "LASSO_MCP_TOKEN=t",
		"MISE_DATA_DIR=/m", "MISE_GITHUB_TOKEN=ghp_x", "AWS_SECRET_ACCESS_KEY=k", "XDG_CONFIG_HOME=/c",
	})
	j := strings.Join(got, " ")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/u", "MISE_DATA_DIR=/m", "XDG_CONFIG_HOME=/c", "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS=1"} {
		if !strings.Contains(j, want) {
			t.Errorf("env lacks %s: %v", want, got)
		}
	}
	for _, bad := range []string{"UI_AUTH", "MCP_OAUTH", "LASSO_MCP_TOKEN", "MISE_GITHUB_TOKEN", "AWS_SECRET"} {
		if strings.Contains(j, bad) {
			t.Errorf("env keeps %s: %v", bad, got)
		}
	}
	args := browserMCPArgs("ws://127.0.0.1:8190/cdp", "tok", "--slim  --viewport 800x600")
	if args[0] != "--wsEndpoint" || args[len(args)-1] != "800x600" || args[len(args)-3] != "--slim" {
		t.Errorf("args = %v", args)
	}
}

func TestBrowserMCPListenAddr(t *testing.T) {
	b := newBrowserMCPBridge(browserMCPConfig{})
	for _, c := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8190}, "ws://127.0.0.1:8190/cdp"},
		{&net.TCPAddr{IP: net.IPv4(100, 86, 22, 100), Port: 8190}, "ws://100.86.22.100:8190/cdp"},
		{&net.TCPAddr{IP: net.IPv4zero, Port: 8190}, "ws://127.0.0.1:8190/cdp"},
		{&net.TCPAddr{IP: net.IPv6unspecified, Port: 8190}, "ws://127.0.0.1:8190/cdp"},
		{&net.TCPAddr{IP: net.IPv6loopback, Port: 8190}, "ws://[::1]:8190/cdp"},
	} {
		b.setListenAddr(c.addr)
		if got := b.cdpEndpoint.Load().(string); got != c.want {
			t.Errorf("%v → %q, want %q", c.addr, got, c.want)
		}
	}
}

// The shared_browser tool and /api/browser both report whether the browser
// tools can run, and shared_browser hands back the CDP listing's URL.
func TestSharedBrowserReportsBrowserTools(t *testing.T) {
	openTestDB(t)
	f := newFakeChromium(t)
	prevB := sharedBrowser
	sharedBrowser = testBrowserManager(t, f)
	t.Cleanup(func() { sharedBrowser = prevB })
	b := testBrowserMCP(t, "ok", 4)
	srv := mcpServerWith(t, b, "", "", false)

	st := sharedBrowser.status()
	if !st.ToolsAvailable || st.ToolsBinary == "" || st.ToolsReason != "" || st.ToolsSessions != 0 {
		t.Errorf("status = %+v", st)
	}

	sess := mustConnect(t, srv.URL, nil)
	var out sharedBrowserOut
	if msg := callTool(t, sess, "shared_browser", map[string]any{"start": false}, &out); msg != "" {
		t.Fatal(msg)
	}
	su, _ := url.Parse(srv.URL)
	if !out.BrowserTools || out.ToolsReason != "" || out.BrowsersURL != "http://"+su.Host+"/cdp/browsers" || out.Browser != "default" {
		t.Errorf("out = %+v", out)
	}
	if _, msg := callEcho(t, sess, nil); msg != "" {
		t.Fatal(msg)
	}
	if st := sharedBrowser.status(); st.ToolsSessions != 1 {
		t.Errorf("tools_sessions = %d after a call", st.ToolsSessions)
	}

	b.cfg.Binary = "off"
	out = sharedBrowserOut{}
	if msg := callTool(t, sess, "shared_browser", map[string]any{"start": false}, &out); msg != "" {
		t.Fatal(msg)
	}
	if out.BrowserTools || !strings.Contains(out.ToolsReason, "disabled") {
		t.Errorf("off: %+v", out)
	}
}

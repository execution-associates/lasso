package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakePluginMCPEnv, when set, makes the test binary a plugin's stdio MCP
// server (see TestMain). The mode names the tool set, so two fake plugins can
// be told apart; "die" in any mode exits the process, for the restart path.
const fakePluginMCPEnv = "LASSO_TEST_FAKE_PLUGIN_MCP"

func runFakePluginMCP(mode string) int {
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake-plugin-" + mode, Version: "0"}, nil)
	obj := map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}
	srv.AddTool(&mcp.Tool{Name: mode + "_greet", Description: "greet someone", InputSchema: obj},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &in)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: mode + " says hello, " + in.Name + " " + os.Getenv("GREETING_SUFFIX")}}}, nil
		})
	srv.AddTool(&mcp.Tool{Name: "die", Description: "exit the server", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			os.Exit(7)
			return nil, nil
		})
	srv.AddTool(&mcp.Tool{Name: "env", Description: "the environment", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(os.Environ(), "\n")}}}, nil
		})
	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil && !errors.Is(err, io.EOF) {
		return 1
	}
	return 0
}

// writePlugin creates <dir>/<name>/plugin.json (plus any extra files).
func writePlugin(t *testing.T, dir, name string, manifest any, files map[string]string) string {
	t.Helper()
	pd := filepath.Join(dir, name)
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	var b []byte
	switch m := manifest.(type) {
	case string:
		b = []byte(m)
	default:
		b, _ = json.Marshal(m)
	}
	if err := os.WriteFile(filepath.Join(pd, pluginManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		p := filepath.Join(pd, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return pd
}

func TestPluginManifestValidation(t *testing.T) {
	tab := map[string]any{"id": "main", "label": "Main", "entry": "ui/index.html"}
	mcpOK := map[string]any{"image": "python:3.12-slim", "command": []string{"python3", "server.py"}}
	cases := []struct {
		name    string
		dir     string
		m       map[string]any
		wantErr string
	}{
		{"ok", "hello", map[string]any{"name": "hello", "tabs": []any{tab}, "mcp": mcpOK}, ""},
		{"tabs only", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "d", "label": "Docs", "url": "https://example.com/x"}}}, ""},
		{"name mismatch", "other", map[string]any{"name": "hello"}, "does not match its directory"},
		{"bad name", "Hello", map[string]any{"name": "Hello"}, "must match"},
		{"bad tab id", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "Main!", "label": "x", "entry": "a.html"}}}, "id"},
		{"dup tab", "hello", map[string]any{"name": "hello", "tabs": []any{tab, tab}}, "duplicate id"},
		{"both entry and url", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A", "entry": "a.html", "url": "https://x.y"}}}, "both entry and url"},
		{"neither", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A"}}}, "needs an entry"},
		{"traversal entry", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A", "entry": "../../etc/passwd"}}}, ".."},
		{"absolute entry", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A", "entry": "/etc/passwd"}}}, "relative"},
		{"hidden entry", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A", "entry": ".env"}}}, "hidden"},
		{"javascript url", "hello", map[string]any{"name": "hello", "tabs": []any{map[string]any{"id": "a", "label": "A", "url": "javascript:alert(1)"}}}, "http(s)"},
		{"no image", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"command": []string{"x"}}}, "mcp.image"},
		{"no command", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine"}}, "mcp.command"},
		{"bad network", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "network": []string{"api.example.com:99999"}}}, "port"},
		{"bad env key", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "env": map[string]string{"A-B": "1"}}}, "environment variable"},
		{"secret no hosts", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "secrets": []any{map[string]any{"name": "TOK"}}}}, "hosts is required"},
		{"ip network", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "network": []string{"1.2.3.4:443"}}}, "IP address"},
		{"ip network no port", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "network": []string{"10.0.0.1"}}}, "IP address"},
		{"single-label wildcard", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "network": []string{"*.com"}}}, "single label"},
		{"two-label wildcard ok", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "network": []string{"*.example.com:8443"}}}, ""},
		{"secret ip host", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "secrets": []any{map[string]any{"name": "TOK", "hosts": []string{"8.8.8.8"}}}}}, "IP address"},
		{"secret wildcard host", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "secrets": []any{map[string]any{"name": "TOK", "hosts": []string{"*.io"}}}}}, "single label"},
		{"secret case dupe", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "secrets": []any{map[string]any{"name": "TOK", "hosts": []string{"a.b"}}, map[string]any{"name": "tok", "hosts": []string{"a.b"}}}}}, "duplicate secret"},
		{"vm image ok", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "vm_image": "images:debian/13/cloud", "command": []string{"x"}}}, ""},
		{"bad vm image", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "vm_image": "-x y", "command": []string{"x"}}}, "mcp.vm_image"},
		{"secret env clash", "hello", map[string]any{"name": "hello", "mcp": map[string]any{"image": "alpine", "command": []string{"x"}, "env": map[string]string{"TOK": "x"}, "secrets": []any{map[string]any{"name": "TOK", "hosts": []string{"a.b"}}}}}, "also in mcp.env"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			pd := writePlugin(t, dir, c.dir, c.m, nil)
			_, err := loadPluginManifest(pd, c.dir)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}

	// Malformed JSON and a missing manifest are errors, never panics.
	dir := t.TempDir()
	pd := writePlugin(t, dir, "bad", "{not json", nil)
	if _, err := loadPluginManifest(pd, "bad"); err == nil {
		t.Error("malformed manifest accepted")
	}
	_ = os.MkdirAll(filepath.Join(dir, "empty"), 0o755)
	if _, err := loadPluginManifest(filepath.Join(dir, "empty"), "empty"); err == nil || !strings.Contains(err.Error(), "no plugin.json") {
		t.Errorf("missing manifest: %v", err)
	}
}

func TestPluginFingerprint(t *testing.T) {
	base := func() *pluginManifest {
		return &pluginManifest{
			Name: "hello", Version: "0.1.0", Description: "d",
			Tabs: []pluginTabSpec{
				{ID: "main", Label: "Hello", Icon: "sparkles", Entry: "ui/index.html"},
				{ID: "docs", Label: "Docs", URL: "https://example.com"},
			},
			MCP: &pluginMCPSpec{
				Image: "python:3.12-slim", Command: []string{"python3", "-u", "server.py"},
				Network: []string{"api.example.com", "b.example.com:8443"},
				Env:     map[string]string{"LOG_LEVEL": "info"},
				Secrets: []pluginSecretSpec{{Name: "TOK", Hosts: []string{"api.example.com", "b.example.com"}}},
			},
		}
	}
	fp := base().fingerprint()
	same := map[string]func(m *pluginManifest){
		"version":        func(m *pluginManifest) { m.Version = "9.9.9" },
		"description":    func(m *pluginManifest) { m.Description = "other" },
		"label and icon": func(m *pluginManifest) { m.Tabs[0].Label, m.Tabs[0].Icon = "Hi", "book" },
		"tab order":      func(m *pluginManifest) { m.Tabs[0], m.Tabs[1] = m.Tabs[1], m.Tabs[0] },
		"network order":  func(m *pluginManifest) { m.MCP.Network = []string{"b.example.com:8443", "api.example.com:443"} },
		"env value":      func(m *pluginManifest) { m.MCP.Env["LOG_LEVEL"] = "debug" },
		"secret hosts order": func(m *pluginManifest) {
			m.MCP.Secrets[0].Hosts = []string{"b.example.com", "api.example.com"}
		},
		"entry spelling": func(m *pluginManifest) { m.Tabs[0].Entry = "./ui/index.html" },
	}
	for n, f := range same {
		m := base()
		f(m)
		if got := m.fingerprint(); got != fp {
			t.Errorf("%s changed the fingerprint; cosmetic edits must not need re-approval", n)
		}
	}
	differ := map[string]func(m *pluginManifest){
		"entry":        func(m *pluginManifest) { m.Tabs[0].Entry = "ui/other.html" },
		"url":          func(m *pluginManifest) { m.Tabs[1].URL = "https://evil.example" },
		"new tab":      func(m *pluginManifest) { m.Tabs = append(m.Tabs, pluginTabSpec{ID: "x", Label: "X", Entry: "x.html"}) },
		"image":        func(m *pluginManifest) { m.MCP.Image = "python:3.13" },
		"command":      func(m *pluginManifest) { m.MCP.Command = []string{"python3", "server.py", "-u"} },
		"network":      func(m *pluginManifest) { m.MCP.Network = append(m.MCP.Network, "evil.example") },
		"network port": func(m *pluginManifest) { m.MCP.Network[0] = "api.example.com:80" },
		"env key":      func(m *pluginManifest) { m.MCP.Env["LD_PRELOAD"] = "x" },
		"secret host":  func(m *pluginManifest) { m.MCP.Secrets[0].Hosts = append(m.MCP.Secrets[0].Hosts, "evil.example") },
		"secret": func(m *pluginManifest) {
			m.MCP.Secrets = append(m.MCP.Secrets, pluginSecretSpec{Name: "X", Hosts: []string{"a.b"}})
		},
		"mcp removed": func(m *pluginManifest) { m.MCP = nil },
		"vm image":    func(m *pluginManifest) { m.MCP.VMImage = "images:debian/13/cloud" },
	}
	for n, f := range differ {
		m := base()
		f(m)
		if got := m.fingerprint(); got == fp {
			t.Errorf("%s did not change the fingerprint; it must need re-approval", n)
		}
	}
	// A manifest without vm_image canonicalizes exactly as before the field
	// existed, so every approval made before it still matches.
	b, _ := json.Marshal(base().perms().MCP)
	if strings.Contains(string(b), "vm_image") {
		t.Errorf("an absent vm_image is in the canonical permissions: %s", b)
	}
	m := base()
	m.MCP.VMImage = "images:debian/13/cloud"
	if b, _ := json.Marshal(m.perms().MCP); !strings.Contains(string(b), `"vm_image":"images:debian/13/cloud"`) {
		t.Errorf("vm_image missing from the permissions: %s", b)
	}
}

func testPluginManager(t *testing.T) (*pluginManager, string) {
	t.Helper()
	openTestDB(t)
	dir := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(dir, 0o755)
	m := newPluginManager(dir, nil)
	t.Cleanup(m.stopAll)
	// The theme registry is process-global; a test's plugin themes must not
	// outlive it.
	t.Cleanup(resetPluginThemes)
	return m, dir
}

func pluginByName(t *testing.T, l pluginsPayload, name string) pluginPayload {
	t.Helper()
	p := findPluginPayload(l, name)
	if p == nil {
		t.Fatalf("plugin %q not listed: %+v", name, l.Plugins)
	}
	return *p
}

func TestPluginStatesAndApproval(t *testing.T) {
	m, dir := testPluginManager(t)
	man := map[string]any{"name": "hello", "version": "0.1.0",
		"tabs": []any{map[string]any{"id": "main", "label": "Hello", "icon": "sparkles", "entry": "ui/index.html"}}}
	writePlugin(t, dir, "hello", man, map[string]string{"ui/index.html": "<p>hi</p>"})
	writePlugin(t, dir, "broken", `{"name": "nope"}`, nil)
	bumps := 0
	m.onChange = func() { bumps++ }

	m.rescan()
	l := m.listing()
	if p := pluginByName(t, l, "hello"); p.State != pluginStateDisabled || len(p.Tabs) != 0 || len(p.Permissions.Tabs) != 1 {
		t.Fatalf("fresh plugin = %+v; want disabled, no tabs served, permissions listed", p)
	}
	if p := pluginByName(t, l, "broken"); p.State != pluginStateInvalid || !strings.Contains(p.Error, "does not match") {
		t.Fatalf("broken plugin = %+v", p)
	}
	if bumps == 0 {
		t.Error("the first scan did not announce the listing")
	}

	// A stale fingerprint is refused, the current one approves.
	if err := m.enable("hello", "0000"); !errors.Is(err, errPluginChanged) {
		t.Fatalf("enable with a stale fingerprint = %v", err)
	}
	fp := pluginByName(t, l, "hello").Fingerprint
	if err := m.enable("hello", fp); err != nil {
		t.Fatal(err)
	}
	p := pluginByName(t, m.listing(), "hello")
	if p.State != pluginStateEnabled || len(p.Tabs) != 1 {
		t.Fatalf("enabled plugin = %+v", p)
	}
	if tab := p.Tabs[0]; tab.GlobalID != "plugin:hello:main" || tab.Src != "/plugins/hello/ui/index.html" || tab.Icon != "sparkles" {
		t.Errorf("tab = %+v", tab)
	}
	if err := m.enable("broken", ""); err == nil {
		t.Error("an invalid plugin was enabled")
	}

	// A cosmetic edit keeps it enabled; a permission edit needs approval again.
	man["version"] = "0.2.0"
	man["tabs"] = []any{map[string]any{"id": "main", "label": "Renamed", "entry": "ui/index.html"}}
	writePlugin(t, dir, "hello", man, nil)
	m.rescan()
	if p := pluginByName(t, m.listing(), "hello"); p.State != pluginStateEnabled || p.Version != "0.2.0" {
		t.Fatalf("after a cosmetic edit = %+v", p)
	}
	man["tabs"] = []any{map[string]any{"id": "main", "label": "Hello", "entry": "ui/other.html"}}
	writePlugin(t, dir, "hello", man, nil)
	m.rescan()
	if p := pluginByName(t, m.listing(), "hello"); p.State != pluginStateNeedsApproval || len(p.Tabs) != 0 {
		t.Fatalf("after a permission edit = %+v; want needs_approval with no tabs", p)
	}

	// Trust is stored beside the approval and survives a disable.
	if err := m.setTrusted("hello", true); err != nil {
		t.Fatal(err)
	}
	if err := m.disable("hello"); err != nil {
		t.Fatal(err)
	}
	if p := pluginByName(t, m.listing(), "hello"); p.State != pluginStateDisabled || !p.Trusted {
		t.Fatalf("after disable = %+v", p)
	}
	if err := m.disable("ghost"); !errors.Is(err, errPluginNotFound) {
		t.Errorf("disable ghost = %v", err)
	}
}

func TestPluginFileServing(t *testing.T) {
	m, dir := testPluginManager(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outside, []byte("TOP SECRET"), 0o644)
	pd := writePlugin(t, dir, "hello", map[string]any{"name": "hello",
		"tabs": []any{map[string]any{"id": "main", "label": "Hello", "entry": "ui/index.html"}}},
		map[string]string{"ui/index.html": "<p>hi</p>", "ui/app.js": "console.log(1)", ".env": "TOKEN=x"})
	if err := os.Symlink(outside, filepath.Join(pd, "ui", "leak.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(pd, "escape")); err != nil {
		t.Fatal(err)
	}
	get := func(p string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = p // bypass the request parser's own cleaning, as a raw client could
		w := httptest.NewRecorder()
		m.serveFiles(w, r)
		if csp := w.Header().Get("Content-Security-Policy"); csp != pluginDocCSP {
			t.Errorf("%s: CSP = %q", p, csp)
		}
		if strings.Contains(w.Header().Get("Content-Security-Policy"), "allow-same-origin") {
			t.Errorf("%s: CSP allows same-origin", p)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: headers = %v", p, w.Header())
		}
		return w
	}

	m.rescan()
	if w := get("/plugins/hello/ui/index.html"); w.Code != http.StatusNotFound {
		t.Fatalf("a disabled plugin's file = %d", w.Code)
	}
	if err := m.enable("hello", ""); err != nil {
		t.Fatal(err)
	}
	w := get("/plugins/hello/ui/index.html")
	if w.Code != http.StatusOK || w.Body.String() != "<p>hi</p>" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index = %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w := get("/plugins/hello/ui/"); w.Code != http.StatusOK || w.Body.String() != "<p>hi</p>" {
		t.Errorf("directory index = %d", w.Code)
	}
	if w := get("/plugins/hello/ui/app.js"); w.Code != http.StatusOK {
		t.Errorf("app.js = %d", w.Code)
	}
	for _, p := range []string{
		"/plugins/hello/../../../etc/passwd",
		"/plugins/hello/ui/../../secret.txt",
		"/plugins/hello/ui/leak.txt",       // symlink to a file outside
		"/plugins/hello/escape/secret.txt", // symlinked directory outside
		"/plugins/hello/.env",
		"/plugins/hello/ui/missing.html",
		"/plugins/nope/ui/index.html",
		"/plugins/../lasso.db",
	} {
		if w := get(p); w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "TOKEN") {
			t.Errorf("%s = %d %q; want 404", p, w.Code, w.Body.String())
		}
	}
}

func TestSidebarTabsUIState(t *testing.T) {
	openTestDB(t)
	us, _ := getUIState()
	if us.SidebarTabs == nil || len(us.SidebarTabs) != 0 {
		t.Fatalf("default sidebar_tabs = %#v; want []", us.SidebarTabs)
	}
	resp := postUIState(t, `{"sidebar_tabs":[{"id":"files","hidden":false},{"id":"plugin:hello:main","hidden":true},{"id":"settings","hidden":true},{"id":"usage","hidden":true}]}`)
	want := []sidebarTab{{"files", false}, {"plugin:hello:main", true}, {"settings", false}, {"usage", true}}
	if !slices.Equal(resp.SidebarTabs, want) {
		t.Fatalf("sidebar_tabs = %+v, want %+v (settings may never be hidden)", resp.SidebarTabs, want)
	}
	// A patch that does not name it leaves it alone.
	resp = postUIState(t, `{"usage_compact":true}`)
	if !slices.Equal(resp.SidebarTabs, want) {
		t.Fatalf("sidebar_tabs after an unrelated patch = %+v", resp.SidebarTabs)
	}
	// Replace-on-write, including to empty.
	resp = postUIState(t, `{"sidebar_tabs":[{"id":"browser"}]}`)
	if !slices.Equal(resp.SidebarTabs, []sidebarTab{{"browser", false}}) {
		t.Fatalf("replace = %+v", resp.SidebarTabs)
	}
	resp = postUIState(t, `{"sidebar_tabs":[]}`)
	if len(resp.SidebarTabs) != 0 {
		t.Fatalf("clear = %+v", resp.SidebarTabs)
	}

	many := make([]string, 65)
	for i := range many {
		many[i] = `{"id":"t` + strings.Repeat("x", i) + `"}`
	}
	for name, body := range map[string]string{
		"dupes":    `{"sidebar_tabs":[{"id":"files"},{"id":"files"}]}`,
		"empty id": `{"sidebar_tabs":[{"id":""}]}`,
		"long id":  `{"sidebar_tabs":[{"id":"` + strings.Repeat("a", 101) + `"}]}`,
		"too many": `{"sidebar_tabs":[` + strings.Join(many, ",") + `]}`,
		"garbage":  `{"sidebar_tabs":"files"}`,
	} {
		if w := postUIStateRaw(t, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s; want 400", name, w.Code, w.Body.String())
		}
	}
	if us, _ := getUIState(); len(us.SidebarTabs) != 0 {
		t.Errorf("a refused patch was stored: %+v", us.SidebarTabs)
	}
}

// fakePluginManifest is a trusted-runnable manifest whose MCP server is this
// test binary in the given fake mode.
func fakePluginManifest(t *testing.T, name, mode string) map[string]any {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"name": name, "version": "0.1.0",
		"mcp": map[string]any{"image": "unused:latest", "command": []string{exe},
			"env": map[string]string{fakePluginMCPEnv: mode, "GREETING_SUFFIX": "!"}}}
}

func waitPlugin(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func mcpStatusOf(m *pluginManager, name string) pluginMCPPayload {
	p := findPluginPayload(m.listing(), name)
	if p == nil || p.MCP == nil {
		return pluginMCPPayload{}
	}
	return *p.MCP
}

func TestPluginMCPMirroringScopeAndRestart(t *testing.T) {
	oldMin := pluginBackoffMin
	pluginBackoffMin = 50 * time.Millisecond
	t.Cleanup(func() { pluginBackoffMin = oldMin })
	t.Setenv("UI_AUTH", "u:secret")
	t.Setenv("LASSO_MCP_TOKEN", "tok")

	openTestDB(t)
	dir := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(dir, 0o755)
	srv := mcp.NewServer(&mcp.Implementation{Name: "lasso-test", Version: "0"}, nil)
	m := newPluginManager(dir, func() *mcp.Server { return srv })
	t.Cleanup(m.stopAll)
	writePlugin(t, dir, "alpha", fakePluginManifest(t, "alpha", "a"), nil)
	writePlugin(t, dir, "beta", fakePluginManifest(t, "beta", "b"), nil)
	m.rescan()
	for _, n := range []string{"alpha", "beta"} {
		if err := m.setTrusted(n, true); err != nil { // host runner: no isb in unit tests
			t.Fatal(err)
		}
		if err := m.enable(n, ""); err != nil {
			t.Fatal(err)
		}
	}
	waitPlugin(t, "both plugins running", func() bool {
		return mcpStatusOf(m, "alpha").Status == pluginMCPRunning && mcpStatusOf(m, "beta").Status == pluginMCPRunning
	})
	if st := mcpStatusOf(m, "alpha"); st.Sandboxed || !slices.Equal(st.Tools, []string{"alpha__a_greet", "alpha__die", "alpha__env"}) {
		t.Fatalf("alpha mcp = %+v", st)
	}

	// Through the shared server: mirrored names, prefixed descriptions, calls
	// forwarded with their arguments.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	lt, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var greet *mcp.Tool
	for _, tl := range lt.Tools {
		if tl.Name == "alpha__a_greet" {
			greet = tl
		}
	}
	if greet == nil || greet.Description != "[plugin alpha] greet someone" {
		t.Fatalf("mirrored tool = %+v (all: %d)", greet, len(lt.Tools))
	}
	res, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha__a_greet", Arguments: map[string]any{"name": "Stephan"}})
	if err != nil || res.IsError || res.Content[0].(*mcp.TextContent).Text != "a says hello, Stephan !" {
		t.Fatalf("call = %v %+v", err, res)
	}

	// A trusted child still gets a minimal environment: no lasso credentials.
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha__env"})
	if err != nil {
		t.Fatal(err)
	}
	if env := res.Content[0].(*mcp.TextContent).Text; strings.Contains(env, "UI_AUTH") || strings.Contains(env, "LASSO_MCP_TOKEN") || !strings.Contains(env, "GREETING_SUFFIX=!") {
		t.Errorf("child env leaks lasso's credentials or lacks the manifest env:\n%s", env)
	}

	// A caller confined to another host is refused as a tool error.
	s := m.serverFor("alpha")
	confined := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "alpha__a_greet"},
		Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "c1", Extra: map[string]any{
			tokenHostKey: "norm", tokenScopeKey: scopeSelf}}},
	}
	res, err = s.forward("a_greet")(ctx, confined)
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "outside this credential's reach") {
		t.Fatalf("confined caller = %v %+v", err, res)
	}
	local := &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "alpha__a_greet", Arguments: json.RawMessage(`{"name":"x"}`)},
		Extra: &mcp.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "c2", Extra: map[string]any{
			tokenHostKey: "local", tokenScopeKey: scopeSelf}}},
	}
	if res, err = s.forward("a_greet")(ctx, local); err != nil || res.IsError {
		t.Fatalf("local-scoped caller = %v %+v", err, res)
	}

	// /api/plugins/<name>/call: own tools by either name, never another's.
	call := func(plugin, tool string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]any{"tool": tool, "arguments": map[string]any{"name": "tab"}})
		r := httptest.NewRequest(http.MethodPost, "/api/plugins/"+plugin+"/call", bytes.NewReader(b))
		w := httptest.NewRecorder()
		m.serveAPI(w, r)
		return w
	}
	for _, tool := range []string{"a_greet", "alpha__a_greet"} {
		w := call("alpha", tool)
		var out mcp.CallToolResult
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.IsError {
			t.Errorf("alpha calling %s = %d %s", tool, w.Code, w.Body.String())
		}
	}
	for _, tool := range []string{"b_greet", "beta__b_greet", "alpha__b_greet", "create_agent"} {
		if w := call("alpha", tool); w.Code != http.StatusForbidden {
			t.Errorf("alpha calling %s = %d %s; want 403", tool, w.Code, w.Body.String())
		}
	}
	if w := call("ghost", "x"); w.Code != http.StatusNotFound {
		t.Errorf("ghost call = %d", w.Code)
	}

	// The child dies: its tools go, it is restarted, they come back.
	_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha__die"})
	waitPlugin(t, "alpha restarted", func() bool {
		st := mcpStatusOf(m, "alpha")
		s.mu.Lock()
		n := s.launches
		s.mu.Unlock()
		return n >= 2 && st.Status == pluginMCPRunning && len(st.Tools) == 3
	})
	res, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha__a_greet", Arguments: map[string]any{"name": "again"}})
	if err != nil || res.IsError {
		t.Fatalf("after restart = %v %+v", err, res)
	}

	// Disabling removes the tools from /mcp.
	if err := m.disable("beta"); err != nil {
		t.Fatal(err)
	}
	lt, _ = client.ListTools(ctx, nil)
	for _, tl := range lt.Tools {
		if strings.HasPrefix(tl.Name, "beta__") {
			t.Errorf("disabled plugin's tool %s still listed", tl.Name)
		}
	}
	if st := mcpStatusOf(m, "beta"); st.Status != pluginMCPStopped || len(st.Tools) != 0 {
		t.Errorf("disabled beta mcp = %+v", st)
	}
}

func TestPluginSandboxUnavailableWithoutISB(t *testing.T) {
	t.Setenv("LASSO_ISB", "/nonexistent/isb")
	m, dir := testPluginManager(t)
	m.server = func() *mcp.Server { return nil }
	writePlugin(t, dir, "hello", map[string]any{"name": "hello",
		"mcp": map[string]any{"image": "alpine", "command": []string{"true"}}}, nil)
	m.rescan()
	if err := m.enable("hello", ""); err != nil {
		t.Fatal(err)
	}
	waitPlugin(t, "unavailable", func() bool { return mcpStatusOf(m, "hello").Status == pluginMCPUnavailable })
	st := mcpStatusOf(m, "hello")
	if !st.Sandboxed || !strings.Contains(st.Detail, "not found") {
		t.Fatalf("mcp = %+v", st)
	}
	l := m.listing()
	if l.Sandbox.Available || l.Sandbox.Kind != "isb" || !strings.Contains(l.Sandbox.Reason, "not found") {
		t.Errorf("sandbox = %+v", l.Sandbox)
	}
	if p := pluginByName(t, l, "hello"); p.Isolation != pluginIsolationContainer || p.VM || p.Trusted {
		t.Errorf("default isolation = %q (vm %v, trusted %v)", p.Isolation, p.VM, p.Trusted)
	}
}

// fakeISB writes a shell script that answers --version like isb does.
func fakeISB(t *testing.T, dir, version string) string {
	t.Helper()
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "isb")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho isb "+version+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestISBResolution(t *testing.T) {
	oldCheck, oldMise := isbServeCheck, isbMiseInstalls
	t.Cleanup(func() { isbServeCheck, isbMiseInstalls = oldCheck, oldMise })
	serveErr := errors.New("dial unix serve.sock: connect: no such file or directory")
	isbServeCheck = func() error { return serveErr }
	tmp := t.TempDir()

	// Too old: unavailable, naming the version found.
	t.Setenv("LASSO_ISB", fakeISB(t, filepath.Join(tmp, "old"), "0.7.0"))
	st := currentSandboxStatus()
	if st.Available || !strings.Contains(st.Reason, "isb 1.0 or later is required (found 0.7.0") {
		t.Fatalf("old isb = %+v", st)
	}
	// New enough, but isb serve is down: unavailable for that reason.
	newBin := fakeISB(t, filepath.Join(tmp, "new"), "1.0.0")
	t.Setenv("LASSO_ISB", newBin)
	st = currentSandboxStatus()
	if st.Available || st.ServeRunning || st.Path != newBin || st.Version != "1.0.0" || !strings.Contains(st.Reason, "isb serve is not running") {
		t.Fatalf("serve down = %+v", st)
	}
	isbServeCheck = func() error { return nil }
	if st = currentSandboxStatus(); !st.Available || !st.ServeRunning || st.Reason != "" {
		t.Fatalf("all good = %+v", st)
	}
	t.Setenv("LASSO_ISB", "off")
	if st = currentSandboxStatus(); st.Available || !strings.Contains(st.Reason, "LASSO_ISB=off") {
		t.Fatalf("off = %+v", st)
	}

	// Unset: an old isb on PATH loses to the newest mise install, and mise's
	// "1.0"/"latest" aliases are not versions.
	t.Setenv("LASSO_ISB", "")
	pathDir := filepath.Join(tmp, "path")
	fakeISB(t, pathDir, "0.7.0")
	t.Setenv("PATH", pathDir)
	mise := filepath.Join(tmp, "mise")
	fakeISB(t, filepath.Join(mise, "0.5.0"), "0.5.0")
	want := fakeISB(t, filepath.Join(mise, "1.0.0"), "1.0.0")
	fakeISB(t, filepath.Join(mise, "latest"), "9.9.9")
	isbMiseInstalls = func() string { return mise }
	if bin, v, reason, ok := resolveISB(); !ok || bin != want || v != "1.0.0" {
		t.Fatalf("resolve = %q %q %q %v; want the mise 1.0.0", bin, v, reason, ok)
	}
	// Nothing anywhere: says how to get it.
	t.Setenv("PATH", t.TempDir())
	isbMiseInstalls = func() string { return filepath.Join(tmp, "nope") }
	if _, _, reason, ok := resolveISB(); ok || !strings.Contains(reason, "isb not found") {
		t.Fatalf("missing isb = %q %v", reason, ok)
	}
}

// The isolation flag: stored beside trust, a flip restarts the server in the
// new isolation, and trusted wins over vm.
func TestPluginIsolationFlag(t *testing.T) {
	m, dir := testPluginManager(t)
	var mu sync.Mutex
	var started []string
	m.runner = func(iso string) pluginRunner {
		mu.Lock()
		started = append(started, iso)
		mu.Unlock()
		return hostPluginRunner{} // the fake child, whatever the isolation
	}
	writePlugin(t, dir, "hello", fakePluginManifest(t, "hello", "a"), nil)
	m.rescan()
	if err := m.enable("hello", ""); err != nil {
		t.Fatal(err)
	}
	waitPlugin(t, "running", func() bool { return mcpStatusOf(m, "hello").Status == pluginMCPRunning })
	first := m.serverFor("hello")

	if code := apiCall(t, m, "POST", "/api/plugins/hello/isolation", map[string]any{"vm": true}, nil); code != 200 {
		t.Fatalf("isolation vm = %d", code)
	}
	if code := apiCall(t, m, "POST", "/api/plugins/hello/isolation", map[string]any{}, nil); code != http.StatusBadRequest {
		t.Fatalf("isolation without vm = %d", code)
	}
	p := pluginByName(t, m.listing(), "hello")
	if !p.VM || p.Isolation != pluginIsolationVM {
		t.Fatalf("after vm on = vm %v isolation %q", p.VM, p.Isolation)
	}
	vmServer := m.serverFor("hello")
	if vmServer == first || vmServer.isolation != pluginIsolationVM {
		t.Fatal("a vm flip did not restart the server in a vm")
	}
	waitPlugin(t, "running in a vm", func() bool { return mcpStatusOf(m, "hello").Status == pluginMCPRunning })

	// Trusted wins: the server moves to the host, and a vm flip while trusted
	// changes the stored choice without a restart.
	if err := m.setTrusted("hello", true); err != nil {
		t.Fatal(err)
	}
	host := m.serverFor("hello")
	if host == vmServer || host.isolation != pluginIsolationHost {
		t.Fatal("trust did not move the server to the host")
	}
	if p := pluginByName(t, m.listing(), "hello"); p.Isolation != pluginIsolationHost || !p.VM || !p.Trusted {
		t.Fatalf("trusted+vm listing = %+v", p)
	}
	if err := m.setVM("hello", false); err != nil {
		t.Fatal(err)
	}
	if m.serverFor("hello") != host {
		t.Error("a vm flip while trusted restarted the server")
	}
	if err := m.setTrusted("hello", false); err != nil {
		t.Fatal(err)
	}
	if s := m.serverFor("hello"); s == host || s.isolation != pluginIsolationContainer {
		t.Fatal("untrusting did not move the server back into a container")
	}
	mu.Lock()
	got := slices.Clone(started)
	mu.Unlock()
	if !slices.Equal(got, []string{"container", "vm", "host", "container"}) {
		t.Errorf("runners started = %v", got)
	}
	if err := m.setVM("ghost", true); !errors.Is(err, errPluginNotFound) {
		t.Errorf("setVM ghost = %v", err)
	}
}

func TestPluginSecretUnresolvedIsUnavailable(t *testing.T) {
	old := pluginSecretLookup
	pluginSecretLookup = func(context.Context, string) (string, error) { return "", errors.New("nope") }
	t.Cleanup(func() { pluginSecretLookup = old })
	m, dir := testPluginManager(t)
	man := fakePluginManifest(t, "hello", "a")
	man["mcp"].(map[string]any)["secrets"] = []any{map[string]any{"name": "EXAMPLE_TOKEN", "hosts": []string{"api.example.com"}}}
	writePlugin(t, dir, "hello", man, nil)
	m.rescan()
	_ = m.setTrusted("hello", true)
	if err := m.enable("hello", ""); err != nil {
		t.Fatal(err)
	}
	waitPlugin(t, "unavailable", func() bool { return mcpStatusOf(m, "hello").Status == pluginMCPUnavailable })
	if st := mcpStatusOf(m, "hello"); !strings.Contains(st.Detail, "EXAMPLE_TOKEN") {
		t.Fatalf("detail = %q; want it to name the secret", st.Detail)
	}
}

func TestISBCreateArgs(t *testing.T) {
	spec := &pluginMCPSpec{
		Image: "python:3.12-slim", Command: []string{"python3", "-u", "server.py"},
		Network: []string{"api.example.com", "b.example.com:8443"},
		Env:     map[string]string{"LOG_LEVEL": "info", "LASSO_PLUGIN_DATA": "/etc"},
		Secrets: []pluginSecretSpec{{Name: "EXAMPLE_TOKEN", Hosts: []string{"API.example.com", "c.example.com"}}},
	}
	argv := isbCreateArgs("hello", "/p/hello", "/d/hello", spec, false)
	args := strings.Join(argv, " ")
	for _, want := range []string{
		"create -i docker:python:3.12-slim --idmap auto",
		"-l owner=lasso -l lasso.plugin=hello",
		"-c oci.entrypoint=" + pluginKeepAlive,
		"-v /p/hello:/plugin:ro",
		"-v /d/hello:/data -e",
		"-e LOG_LEVEL=info",
		"--egress api.example.com:443 --egress b.example.com:8443 --egress c.example.com:443",
		"--secret EXAMPLE_TOKEN=lasso-plugin-hello.example_token@api.example.com,c.example.com",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("create args lack %q:\n%s", want, args)
		}
	}
	if argv[len(argv)-1] != "lasso-plugin-hello" {
		t.Errorf("the sandbox name is not last: %v", argv)
	}
	if strings.Contains(args, "--vm") || strings.Contains(args, "--egress none") {
		t.Errorf("container args = %s", args)
	}
	// LASSO_PLUGIN_DATA=/data comes after the manifest's own attempt, so it wins.
	if strings.LastIndex(args, "-e LASSO_PLUGIN_DATA=/data") < strings.Index(args, "-e LASSO_PLUGIN_DATA=/etc") {
		t.Errorf("LASSO_PLUGIN_DATA is not last: %s", args)
	}
	// api.example.com is named once, though both a network entry and a host.
	if strings.Count(args, "--egress api.example.com:443") != 1 {
		t.Errorf("duplicate egress entry: %s", args)
	}

	// No network, no secrets: no egress at all; no data dir: no /data.
	bare := strings.Join(isbCreateArgs("n", "/p", "", &pluginMCPSpec{Image: "ghcr:org/srv:1", Command: []string{"x"}}, false), " ")
	if !strings.Contains(bare, "-i ghcr:org/srv:1 ") || !strings.Contains(bare, "--egress none") || strings.Contains(bare, "/data") || strings.Contains(bare, "--secret") {
		t.Errorf("bare args = %s", bare)
	}
	// images: is a system container: no OCI keep-alive.
	sys := strings.Join(isbCreateArgs("n", "/p", "", &pluginMCPSpec{Image: "images:alpine/3.20", Command: []string{"x"}}, false), " ")
	if !strings.Contains(sys, "-i images:alpine/3.20 ") || strings.Contains(sys, "oci.entrypoint") {
		t.Errorf("system image args = %s", sys)
	}

	// VM: the default VM image (the OCI one cannot boot), --vm, no idmap, no
	// keep-alive; mounts, env, egress and secrets as in a container.
	vm := strings.Join(isbCreateArgs("hello", "/p/hello", "/d/hello", spec, true), " ")
	for _, want := range []string{"create -i " + pluginDefaultVMImage + " --vm", "-v /p/hello:/plugin:ro", "--secret EXAMPLE_TOKEN=", "--egress b.example.com:8443"} {
		if !strings.Contains(vm, want) {
			t.Errorf("vm args lack %q:\n%s", want, vm)
		}
	}
	if strings.Contains(vm, "--idmap") || strings.Contains(vm, "oci.entrypoint") || strings.Contains(vm, "docker:") {
		t.Errorf("vm args = %s", vm)
	}
	spec.VMImage = "images:debian/13/cloud"
	if vm := strings.Join(isbCreateArgs("hello", "/p", "", spec, true), " "); !strings.Contains(vm, "-i images:debian/13/cloud --vm") {
		t.Errorf("vm_image not used: %s", vm)
	}
}

func TestISBExecArgs(t *testing.T) {
	spec := &pluginMCPSpec{Image: "x", Command: []string{"python3", "-u", "server.py"}}
	got := isbExecArgs("hello", spec)
	want := []string{"exec", "-i", "-T", "-w", "/plugin", "-u", pluginGuestUser, "lasso-plugin-hello", "--", "python3", "-u", "server.py"}
	if !slices.Equal(got, want) {
		t.Errorf("exec = %v\nwant %v", got, want)
	}
}

func TestISBImage(t *testing.T) {
	for in, want := range map[string]struct {
		ref string
		oci bool
	}{
		"python:3.12-slim":       {"docker:python:3.12-slim", true},
		"ubuntu:24.04":           {"docker:ubuntu:24.04", true}, // Docker Hub's, not isb's ubuntu: remote
		"alpine":                 {"docker:alpine", true},
		"docker:node:22":         {"docker:node:22", true},
		"ghcr:org/app:v2":        {"ghcr:org/app:v2", true},
		"oci:reg.example.com/x":  {"oci:reg.example.com/x", true},
		"images:debian/12":       {"images:debian/12", false},
		"ghcr.io/org/app:latest": {"docker:ghcr.io/org/app:latest", true},
	} {
		ref, oci := isbImage(in)
		if ref != want.ref || oci != want.oci {
			t.Errorf("isbImage(%q) = %q %v; want %q %v", in, ref, oci, want.ref, want.oci)
		}
	}
	if got := pluginSecretStore("a-b", "My_Token"); got != "lasso-plugin-a-b.my_token" {
		t.Errorf("store name = %q", got)
	}
	if strings.HasPrefix(pluginSecretStore("a-b", "x"), pluginSecretStorePrefix("a")) {
		t.Error("plugin a's prefix matches plugin a-b's secrets")
	}
}

// Plugin tool names survive the CLI's kebab spelling, and an exact name wins
// over a normalized one.
func TestFindMCPToolPluginNames(t *testing.T) {
	tools := []*mcp.Tool{{Name: "hello__get-x"}, {Name: "hello__get_x"}, {Name: "list_hosts"}}
	if got := findMCPTool(tools, "hello__get_x"); got == nil || got.Name != "hello__get_x" {
		t.Errorf("exact = %v", got)
	}
	if got := findMCPTool(tools, "hello__get-x"); got == nil || got.Name != "hello__get-x" {
		t.Errorf("exact kebab = %v", got)
	}
	if got := findMCPTool(tools, cliToolName("hello__greet")); got != nil {
		t.Errorf("unknown tool resolved to %v", got)
	}
	greet := []*mcp.Tool{{Name: "hello__greet"}}
	if got := findMCPTool(greet, cliToolName("hello__greet")); got == nil {
		t.Errorf("%q did not resolve", cliToolName("hello__greet"))
	}
}

func TestOnboardingDoneUIState(t *testing.T) {
	openTestDB(t)
	// A fresh install has not seen the tour.
	if us, _ := getUIState(); us.OnboardingDone {
		t.Fatal("fresh install: onboarding_done = true; want false")
	}
	// An unrelated write (a sidebar persist on first load) must not mark it
	// seen: the blob now exists, but it carries the field as false.
	postUIState(t, `{"usage_compact":true}`)
	if us, _ := getUIState(); us.OnboardingDone {
		t.Fatal("after an unrelated patch: onboarding_done = true; want false")
	}
	if resp := postUIState(t, `{"onboarding_done":true}`); !resp.OnboardingDone {
		t.Fatal("patch did not set onboarding_done")
	}
	// A blob from before the field existed is an install already in use.
	if err := setSetting("ui_state", `{"usage_compact":true}`); err != nil {
		t.Fatal(err)
	}
	if us, _ := getUIState(); !us.OnboardingDone {
		t.Fatal("legacy blob: onboarding_done = false; want true")
	}
}

// Two lassos sharing a plugins directory (titan's dev and production builds
// both read ~/.lasso) must not both run its servers: each launch starts with
// `isb rm -f lasso-plugin-<name>`, so they would delete each other's sandboxes.
// The runner lock gives the servers to one; the other lists them as
// unavailable, naming the holder, and takes over once the holder lets go.
// flock is per open file description, so two managers in one process contend
// exactly like two processes.
func TestPluginRunnerLockOneLassoPerDirectory(t *testing.T) {
	openTestDB(t)
	dir := filepath.Join(t.TempDir(), "plugins")
	_ = os.MkdirAll(dir, 0o755)
	srvA := mcp.NewServer(&mcp.Implementation{Name: "a", Version: "0"}, nil)
	srvB := mcp.NewServer(&mcp.Implementation{Name: "b", Version: "0"}, nil)
	a := newPluginManager(dir, func() *mcp.Server { return srvA })
	b := newPluginManager(dir, func() *mcp.Server { return srvB })
	t.Cleanup(b.stopAll)
	t.Cleanup(a.stopAll)
	writePlugin(t, dir, "alpha", fakePluginManifest(t, "alpha", "a"), nil)

	a.rescan() // a takes the lock first
	if err := a.setTrusted("alpha", true); err != nil {
		t.Fatal(err)
	}
	if err := a.enable("alpha", ""); err != nil {
		t.Fatal(err)
	}
	waitPlugin(t, "alpha running in a", func() bool { return mcpStatusOf(a, "alpha").Status == pluginMCPRunning })

	b.rescan() // shares the db, so alpha is enabled for b too
	st := mcpStatusOf(b, "alpha")
	if st.Status != pluginMCPUnavailable || !strings.Contains(st.Detail, "other lasso") || !strings.Contains(st.Detail, "pid "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("b's view of alpha = %+v, want unavailable naming the holder", st)
	}
	b.mu.Lock()
	n := len(b.servers)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("b started %d servers without the lock", n)
	}

	a.stopAll() // releases the lock
	b.rescan()
	waitPlugin(t, "alpha running in b after a let go", func() bool { return mcpStatusOf(b, "alpha").Status == pluginMCPRunning })
}

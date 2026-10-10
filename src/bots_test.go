package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBotNormalize(t *testing.T) {
	r := botRecord{Name: "news-bot"}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if r.Dir != "~/bots/news-bot" || r.Workspace != botDefaultWorkspace || r.Host != "local" {
		t.Errorf("defaults = %q %q %q", r.Dir, r.Workspace, r.Host)
	}
	for name, bad := range map[string]botRecord{
		"uppercase name":   {Name: "News"},
		"quote in folder":  {Name: "a", Dir: "~/it's"},
		"relative folder":  {Name: "a", Dir: "bots/a"},
		"unknown effort":   {Name: "a", Effort: "ludicrous"},
		"unknown perm":     {Name: "a", PermissionMode: "yolo"},
		"stdio no command": {Name: "a", MCP: []botMCPServer{{Name: "x"}}},
		"http bad url":     {Name: "a", MCP: []botMCPServer{{Name: "x", Type: "http", URL: "ftp://x"}}},
		"duplicate server": {Name: "a", MCP: []botMCPServer{{Name: "x", Command: "c"}, {Name: "x", Command: "c"}}},
		"newline in arg":   {Name: "a", ExtraArgs: []string{"--x\nrm"}},
		"reserved name":    {Name: "manage"},
	} {
		if err := bad.normalize(); err == nil {
			t.Errorf("%s: normalize accepted %+v", name, bad)
		}
	}
}

func testBot() *botRecord {
	r := &botRecord{
		Name:           "news-bot",
		Model:          "opus",
		Effort:         "high",
		PermissionMode: "bypassPermissions",
		StrictMCP:      true,
		ExtraArgs:      []string{"--append-system-prompt", "it's $HOME"},
		MCP: []botMCPServer{
			{Name: "gmail-channel", Command: "bun", Args: []string{"run", "shim.ts"}, Env: map[string]string{"TOKEN": "${GMAIL_TOKEN}"}, Channel: true},
			{Name: "docs", Type: "http", URL: "https://example.com/mcp"},
		},
	}
	if err := r.normalize(); err != nil {
		panic(err)
	}
	return r
}

func TestBotTaskScript(t *testing.T) {
	r := testBot()
	s := botTaskScript(r, "/home/u/bots/news-bot", []string{"GMAIL_TOKEN", "PLAIN"})
	for _, want := range []string{
		"exec claude '--mcp-config' '/home/u/bots/news-bot/.lasso/mcp.json'",
		"'--strict-mcp-config'",
		"'--dangerously-load-development-channels' 'server:gmail-channel'",
		"'--model' 'opus' '--effort' 'high' '--permission-mode' 'bypassPermissions' '--name' 'news-bot'",
		`'it'\''s $HOME' "$@"`,
		"I am using this for local development",
		"❯ Yes, I trust this folder",
		`herdr agent rename "$HERDR_PANE_ID" "$NAME"`,
		"#MISE secrets=[\"GMAIL_TOKEN\",\"PLAIN\"]\n#MISE interactive=true\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "server:docs") {
		t.Error("a non-channel server got a channel grant")
	}
	// The script must at least parse.
	f := filepath.Join(t.TempDir(), "bot")
	if err := os.WriteFile(f, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}

	r.MCP[0].Channel = false
	if s := botTaskScript(r, "/d", nil); strings.Contains(s, "local development") || strings.Contains(s, "#MISE secrets") {
		t.Error("a bot with no channels or env got a dev-channel answer or a secrets grant")
	}
}

func TestBotMCPJSON(t *testing.T) {
	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(botMCPJSON(testBot(), "/d", nil), &got); err != nil {
		t.Fatal(err)
	}
	g := got.MCPServers["gmail-channel"]
	if g["type"] != "stdio" || g["command"] != "bun" || g["env"].(map[string]any)["TOKEN"] != "${GMAIL_TOKEN}" {
		t.Errorf("stdio entry = %v", g)
	}
	if _, ok := g["channel"]; ok {
		t.Error("lasso's channel flag leaked into mcp.json")
	}
	if d := got.MCPServers["docs"]; d["type"] != "http" || d["url"] != "https://example.com/mcp" {
		t.Errorf("http entry = %v", d)
	}
}

func TestValidateResumeArgv(t *testing.T) {
	if err := validateResumeArgv(botResumeArgv("4a8eb1d3-f2fb-4755-aa35-8fc5efac2e1e")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{
		nil,
		{"/usr/bin/mise", "run"},
		{"-x"},
		{"mise", "it's"},
		make([]string, 65),
	} {
		if err := validateResumeArgv(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if l := len(botLaunchCommand("4a8eb1d3-f2fb-4755-aa35-8fc5efac2e1e")); l > maxTypedLaunch {
		t.Errorf("launch line %d bytes", l)
	}
}

// fnoxStore is the fake fnox's secrets, by key.
var fnoxStore map[string]string

type toolCall struct {
	dir   string
	tool  string
	args  []string
	stdin string
}

// recordTools stands in for mise and fnox: versions new enough, `fnox list`
// answering listOut, everything else succeeding silently.
func recordTools(t *testing.T, listOut string) *[]toolCall {
	t.Helper()
	var mu sync.Mutex
	calls := &[]toolCall{}
	fnoxStore = map[string]string{}
	prev := botRun
	botRun = func(b Backend, dir, tool string, args []string, stdin []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, toolCall{dir, tool, args, string(stdin)})
		switch {
		case len(args) == 1 && args[0] == "--version" && tool == "mise":
			return []byte("2026.10.7 linux-x64 (2026-10-09)"), nil
		case len(args) == 1 && args[0] == "--version":
			return []byte("fnox 1.39.0"), nil
		case tool == "fnox" && args[0] == "list":
			return []byte(listOut), nil
		case tool == "fnox" && args[0] == "set":
			fnoxStore[args[1]] = string(stdin)
		case tool == "fnox" && args[0] == "get":
			if v, ok := fnoxStore[args[1]]; ok {
				return []byte(v + "\n"), nil
			}
			return nil, fmt.Errorf("no such secret")
		case tool == "fnox" && args[0] == "remove":
			delete(fnoxStore, args[1])
		}
		return nil, nil
	}
	botToolsOK = sync.Map{}
	t.Cleanup(func() { botRun = prev; botToolsOK = sync.Map{} })
	return calls
}

func TestBotEnvValuesTravelOnStdin(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	calls := recordTools(t, "")
	b := &localBackend{}
	dir := t.TempDir()
	if err := botEnvSet(b, dir, "API_KEY", "hunter2", true); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[len(*calls)-1]
	if c.tool != "fnox" || c.stdin != "hunter2" || strings.Join(c.args, " ") != "set API_KEY" {
		t.Fatalf("secret call = %+v", c)
	}
	if err := botEnvSet(b, dir, "PLAIN", "v", false); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[len(*calls)-1]; strings.Join(c.args, " ") != "set PLAIN --provider plain" || c.stdin != "v" {
		t.Errorf("plain call = %+v", c)
	}
	for _, c := range *calls {
		if strings.Contains(strings.Join(c.args, " "), "hunter2") {
			t.Errorf("a value reached argv: %+v", c)
		}
	}
	if err := botEnvSet(b, dir, "1BAD", "v", false); err == nil {
		t.Error("accepted a bad key")
	}

	// The first write created fnox.toml with lasso's own age key, 0600.
	fnox, err := os.ReadFile(botFnoxFile(dir))
	if err != nil || !bytes.Contains(fnox, []byte(`default_provider = "lasso"`)) || !bytes.Contains(fnox, []byte(botAgeKeyPath(b))) {
		t.Fatalf("fnox.toml: %v\n%s", err, fnox)
	}
	key, err := os.ReadFile(botAgeKeyPath(b))
	if err != nil || !bytes.Contains(fnox, []byte(ageRecipientFromFile(string(key)))) {
		t.Fatalf("age key: %v", err)
	}
	if fi, _ := os.Stat(botAgeKeyPath(b)); fi.Mode().Perm() != 0o600 {
		t.Errorf("age key mode %v", fi.Mode().Perm())
	}
	// A second bot reuses the key; an edited fnox.toml is never rewritten.
	os.WriteFile(botFnoxFile(dir), []byte("mine"), 0o644)
	if err := botEnvInit(b, dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(botFnoxFile(dir)); string(got) != "mine" {
		t.Error("botEnvInit overwrote fnox.toml")
	}
	if again, _ := os.ReadFile(botAgeKeyPath(b)); !bytes.Equal(again, key) {
		t.Error("the age key changed")
	}
}

func TestBotToolsTooOld(t *testing.T) {
	prev := botRun
	botRun = func(b Backend, dir, tool string, args []string, stdin []byte) ([]byte, error) {
		if tool == "mise" {
			return []byte("2026.7.11 linux-x64"), nil
		}
		return []byte("fnox 1.39.0"), nil
	}
	botToolsOK = sync.Map{}
	t.Cleanup(func() { botRun = prev; botToolsOK = sync.Map{} })
	if err := botCheckTools(&localBackend{}); err == nil || !strings.Contains(err.Error(), "mise self-update") {
		t.Errorf("err = %v", err)
	}
	for _, c := range []struct {
		a, b string
		less bool
	}{{"2026.7.11", "2026.10.4", true}, {"2026.10.4", "2026.10.4", false}, {"1.40.0", "1.39.0", false}, {"", "1.0", true}} {
		if versionLess(c.a, c.b) != c.less {
			t.Errorf("versionLess(%q, %q) != %v", c.a, c.b, c.less)
		}
	}
}

func TestParseFnoxList(t *testing.T) {
	file := "/home/u/bots/a/fnox.toml"
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	row := func(k, ty, src, pk string) string {
		return " " + pad(k, 11) + pad(ty, 18) + pad(src, 40) + pad(pk, 20) + "\n"
	}
	out := row("Key", "Type", "Source File", "Provider Key") + " Description\n"
	out = strings.Replace(out, "Provider Key        \n", "Provider Key        Description\n", 1)
	out += row("API_TOKEN", "provider (lasso)", file, "YWdlLWVu...")
	out += row("GREETING", "provider (plain)", file, "hello there")
	out += row("GLOBAL", "provider (plain)", "/home/u/.config/fnox/config.toml", "x")
	got := parseFnoxList(out, file)
	if len(got) != 2 || got[0] != (botEnvVar{Key: "API_TOKEN", Secret: true, Provider: "lasso"}) ||
		got[1] != (botEnvVar{Key: "GREETING", Value: "hello there", Provider: "plain"}) {
		t.Errorf("got %+v\n%s", got, out)
	}
}

func TestFindBotSession(t *testing.T) {
	entries := []claudeSessionEntry{
		{SessionID: "old", Cwd: "/b/x", Name: "x", UpdatedAt: 1},
		{SessionID: "new", Cwd: "/b/x/", Name: "x", UpdatedAt: 5},
		{SessionID: "renamed", Cwd: "/b/x", Name: "other", UpdatedAt: 9},
		{SessionID: "elsewhere", Cwd: "/b/y", Name: "x", UpdatedAt: 9},
	}
	if s, ok := findBotSession(entries, "x", "/b/x"); !ok || s.SessionID != "new" {
		t.Errorf("got %+v", s)
	}
	if s, ok := findBotSession(entries[2:3], "x", "/b/x"); !ok || s.SessionID != "renamed" {
		t.Errorf("a lone renamed session in the folder: %+v %v", s, ok)
	}
	if (claudeSessionEntry{Status: "waiting"}).agentStatus() != "blocked" {
		t.Error("waiting must read as blocked")
	}
}

// botHerdrFake is a host with no panes at all: every bot reads as stopped.
type botHerdrFake struct{ *localBackend }

func (f *botHerdrFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	switch method {
	case "pane.list":
		return json.RawMessage(`{"panes":[]}`), nil
	}
	return nil, &herdrError{Code: "not_found", Message: method}
}

func useBotTestEnv(t *testing.T) Backend {
	t.Helper()
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	b := &botHerdrFake{&localBackend{}}
	prev := botBackend
	botBackend = func(string) (Backend, error) { return b, nil }
	t.Cleanup(func() { botBackend = prev })
	invalidatePaneList("local")
	recordTools(t, "")
	return b
}

func TestBotsAPI(t *testing.T) {
	useBotTestEnv(t)
	dir := filepath.Join(t.TempDir(), "news")
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		serveBots(w, req)
		return w
	}
	w := do("POST", "/api/bots", `{"name":"news","dir":"`+dir+`","model":"haiku","mcp":[{"name":"c","command":"x","channel":true}]}`)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	task, err := os.ReadFile(botTaskPath(dir))
	if err != nil || !bytes.Contains(task, []byte("'--model' 'haiku'")) || !bytes.Contains(task, []byte("server:c")) {
		t.Fatalf("task script: %v\n%s", err, task)
	}
	if fi, _ := os.Stat(botTaskPath(dir)); fi.Mode().Perm()&0o100 == 0 {
		t.Error("task script is not executable")
	}
	if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Error("no CLAUDE.md stub")
	}
	if w := do("POST", "/api/bots", `{"name":"news"}`); w.Code != http.StatusConflict {
		t.Errorf("duplicate name: %d", w.Code)
	}

	w = do("GET", "/api/bots", "")
	var list struct {
		Bots []struct {
			State   string `json:"state"`
			Stopped bool   `json:"stopped"`
		} `json:"bots"`
	}
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Bots) != 1 || list.Bots[0].State != "stopped" || !list.Bots[0].Stopped {
		t.Fatalf("list = %s", w.Body)
	}

	// CLAUDE.md is the human's: a save never overwrites it.
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("mine"), 0o644)
	if w := do("PUT", "/api/bots/news", `{"name":"renamed","dir":"`+dir+`","model":"sonnet"}`); w.Code != 200 {
		t.Fatalf("update: %d %s", w.Code, w.Body)
	}
	got, _ := getBot("news")
	if got == nil || got.Model != "sonnet" {
		t.Fatalf("after update: %+v", got)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md")); string(md) != "mine" {
		t.Error("save overwrote CLAUDE.md")
	}
	if task, _ := os.ReadFile(botTaskPath(dir)); !bytes.Contains(task, []byte("'sonnet'")) || bytes.Contains(task, []byte("server:c")) {
		t.Errorf("task not regenerated:\n%s", task)
	}

	if w := do("DELETE", "/api/bots/news", ""); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := getBot("news"); err != errBotNotFound {
		t.Errorf("still there: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("delete removed the folder")
	}
}

func TestPreviewText(t *testing.T) {
	if got := previewText("## **→ Done.** Ran `ls`\n\nnext"); got != "→ Done. Ran ls next" {
		t.Errorf("got %q", got)
	}
}

func TestReorderBots(t *testing.T) {
	useBotTestEnv(t)
	for _, n := range []string{"a", "b", "c"} {
		r := &botRecord{Name: n}
		if err := r.normalize(); err != nil {
			t.Fatal(err)
		}
		if err := insertBot(r); err != nil {
			t.Fatal(err)
		}
	}
	order := func() string {
		list, _ := listBots()
		var names []string
		for _, r := range list {
			names = append(names, r.Name)
		}
		return strings.Join(names, ",")
	}
	if got := order(); got != "a,b,c" {
		t.Fatalf("creation order = %s", got)
	}
	// A partial list (a bot created meanwhile) keeps the unnamed ones after.
	req := httptest.NewRequest("PUT", "/api/bots/order", strings.NewReader(`{"names":["c","a"]}`))
	w := httptest.NewRecorder()
	serveBots(w, req)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := order(); got != "c,a,b" {
		t.Errorf("after reorder = %s", got)
	}
	r := &botRecord{Name: "d"}
	r.normalize()
	insertBot(r)
	if got := order(); got != "c,a,b,d" {
		t.Errorf("a new bot is not last: %s", got)
	}
}

func TestBotTickRelaunchesAfterGrace(t *testing.T) {
	useBotTestEnv(t)
	for _, r := range []*botRecord{
		{Name: "keep", Dir: "/tmp/keep", KeepRunning: true},
		{Name: "stopped", Dir: "/tmp/stopped", KeepRunning: true, Stopped: true},
		{Name: "once", Dir: "/tmp/once"},
	} {
		if err := r.normalize(); err != nil {
			t.Fatal(err)
		}
		if err := insertBot(r); err != nil {
			t.Fatal(err)
		}
	}
	var relaunched []string
	prev := botRelaunch
	botRelaunch = func(b Backend, r *botRecord) error {
		relaunched = append(relaunched, r.Name)
		return nil
	}
	t.Cleanup(func() { botRelaunch = prev })
	bots.mu.Lock()
	bots.misses = map[string]int{}
	bots.starting = map[string]time.Time{}
	bots.mu.Unlock()

	for i := 0; i < botMissesBeforeRelaunch-1; i++ {
		botTick()
	}
	if len(relaunched) != 0 {
		t.Fatalf("relaunched inside the grace: %v", relaunched)
	}
	botTick()
	if len(relaunched) != 1 || relaunched[0] != "keep" {
		t.Fatalf("relaunched = %v, want only keep", relaunched)
	}
}

// fakeOAuthMCP is an MCP server that wants OAuth, with its own authorization
// server: the 401 names the resource metadata, which names the issuer.
func fakeOAuthMCP(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	seen := &sync.Map{}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+srv.URL+`/.well-known/oauth-protected-resource/mcp", scope="read write"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"resource": srv.URL + "/mcp", "authorization_servers": []string{srv.URL}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer": srv.URL, "authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint": srv.URL + "/token", "registration_endpoint": srv.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		seen.Store("redirect", in["redirect_uris"].([]any)[0])
		json.NewEncoder(w).Encode(map[string]any{"client_id": "cid", "token_endpoint_auth_method": "none"})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			ch, _ := seen.Load("challenge")
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != ch || r.Form.Get("client_id") != "cid" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "refresh_token": "rt1", "expires_in": 60})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "rt1" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "at2", "refresh_token": "rt2", "expires_in": 3600})
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestBotOAuthSignInAndRefresh(t *testing.T) {
	b := useBotTestEnv(t)
	srv, seen := fakeOAuthMCP(t)
	dir := t.TempDir()
	rec := &botRecord{Name: "oauthy", Dir: dir, MCP: []botMCPServer{{Name: "docs", Type: "http", URL: srv.URL + "/mcp", OAuth: true}}}
	if err := rec.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(rec); err != nil {
		t.Fatal(err)
	}

	start, err := startBotOAuth(rec, "docs", "https://lasso.example")
	if err != nil {
		t.Fatal(err)
	}
	if start.Localhost || start.Redirect != "https://lasso.example/api/bots/oauth/callback" {
		t.Errorf("redirect = %+v", start)
	}
	if r, _ := seen.Load("redirect"); r != start.Redirect {
		t.Errorf("registered redirect = %v", r)
	}
	au, _ := url.Parse(start.AuthorizeURL)
	q := au.Query()
	if q.Get("client_id") != "cid" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != srv.URL+"/mcp" || q.Get("scope") != "read write" {
		t.Errorf("authorize query = %v", q)
	}
	seen.Store("challenge", q.Get("code_challenge"))

	// The human lands on the redirect; a pasted address works the same way.
	req := httptest.NewRequest("POST", "/api/bots/oauth/finish",
		strings.NewReader(`{"url":"https://lasso.example/api/bots/oauth/callback?code=the-code&state=`+q.Get("state")+`"}`))
	w := httptest.NewRecorder()
	serveBots(w, req)
	if w.Code != 200 {
		t.Fatalf("finish: %d %s", w.Code, w.Body)
	}
	if fnoxStore[botOAuthKey("docs", "ACCESS")] != "at1" || fnoxStore[botOAuthKey("docs", "REFRESH")] != "rt1" {
		t.Fatalf("stored = %v", fnoxStore)
	}
	mcp, _ := os.ReadFile(botMCPPath(dir))
	if !bytes.Contains(mcp, []byte(`"headersHelper"`)) || !bytes.Contains(mcp, []byte("LASSO_OAUTH_DOCS_ACCESS")) {
		t.Errorf("mcp.json has no headersHelper:\n%s", mcp)
	}
	// A state is single-use.
	w = httptest.NewRecorder()
	serveBots(w, httptest.NewRequest("POST", "/api/bots/oauth/finish",
		strings.NewReader(`{"url":"https://x/?code=the-code&state=`+q.Get("state")+`"}`)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("replayed state: %d", w.Code)
	}

	// Expiring within five minutes: the tick refreshes it.
	botOAuthTick(b, rec)
	if fnoxStore[botOAuthKey("docs", "ACCESS")] != "at2" || fnoxStore[botOAuthKey("docs", "REFRESH")] != "rt2" {
		t.Fatalf("after refresh = %v", fnoxStore)
	}
	row, _ := getBotOAuth("oauthy", "docs")
	if row.Status != "connected" || time.Until(time.Unix(row.ExpiresAt, 0)) < 50*time.Minute {
		t.Errorf("row = %+v", row)
	}

	// The helper's command prints the header JSON.
	helper := botOAuthHelper(dir, "docs")
	if !strings.Contains(helper, "fnox -c '"+botFnoxFile(dir)+"' get LASSO_OAUTH_DOCS_ACCESS") {
		t.Errorf("helper = %s", helper)
	}

	if err := signOutBotOAuth(b, rec, "docs"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fnoxStore[botOAuthKey("docs", "ACCESS")]; ok {
		t.Error("sign out left the token")
	}
	if mcp, _ := os.ReadFile(botMCPPath(dir)); bytes.Contains(mcp, []byte("headersHelper")) {
		t.Error("sign out left the headersHelper")
	}
}

func TestBotOAuthKeysStayOutOfTheTask(t *testing.T) {
	dir := t.TempDir()
	file := botFnoxFile(dir)
	os.WriteFile(file, nil, 0o644)
	pad := func(s string, n int) string { return s + strings.Repeat(" ", n-len(s)) }
	row := func(k, ty, src, pk string) string {
		return " " + pad(k, 30) + pad(ty, 18) + pad(src, len(file)+2) + pad(pk, 20) + "\n"
	}
	out := strings.TrimSuffix(row("Key", "Type", "Source File", "Provider Key"), "\n") + "Description\n"
	out += row("API_KEY", "provider (lasso)", file, "x")
	out += row("LASSO_OAUTH_DOCS_ACCESS", "provider (lasso)", file, "y")
	recordTools(t, out)
	if got := botEnvKeys(&localBackend{}, dir); len(got) != 1 || got[0] != "API_KEY" {
		t.Errorf("task keys = %v", got)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	if err := json.Unmarshal(botMCPJSON(testBot()), &got); err != nil {
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

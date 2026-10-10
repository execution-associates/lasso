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
	s := botTaskScript(r, "/home/u/bots/news-bot")
	for _, want := range []string{
		"exec claude '--mcp-config' '/home/u/bots/news-bot/.lasso/mcp.json'",
		"'--strict-mcp-config'",
		"'--dangerously-load-development-channels' 'server:gmail-channel'",
		"'--model' 'opus' '--effort' 'high' '--permission-mode' 'bypassPermissions' '--name' 'news-bot'",
		`'it'\''s $HOME' "$@"`,
		"I am using this for local development",
		"❯ Yes, I trust this folder",
		`herdr agent rename "$HERDR_PANE_ID" "$NAME"`,
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
	if s := botTaskScript(r, "/d"); strings.Contains(s, "local development") {
		t.Error("the dev-channel answer is written for a bot with no channels")
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

type miseCall struct {
	dir   string
	args  []string
	stdin string
}

func recordMise(t *testing.T, out string) *[]miseCall {
	t.Helper()
	var mu sync.Mutex
	calls := &[]miseCall{}
	prev := botRun
	botRun = func(b Backend, dir string, args []string, stdin []byte) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		*calls = append(*calls, miseCall{dir, args, string(stdin)})
		return []byte(out), nil
	}
	t.Cleanup(func() { botRun = prev })
	return calls
}

func TestBotEnvSecretTravelsOnStdin(t *testing.T) {
	calls := recordMise(t, "")
	b := &localBackend{}
	dir := t.TempDir()
	if err := botEnvSet(b, dir, "API_KEY", "hunter2", true); err != errBotNoAgeKey {
		t.Fatalf("without an age key: %v", err)
	}
	home, _ := b.HomeDir()
	key := filepath.Join(home, ".config", "mise", "age.txt")
	os.MkdirAll(filepath.Dir(key), 0o700)
	os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600)
	t.Cleanup(func() { os.Remove(key) })

	if err := botEnvSet(b, dir, "API_KEY", "hunter2", true); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[len(*calls)-1]
	if c.stdin != "hunter2" || strings.Contains(strings.Join(c.args, " "), "hunter2") {
		t.Fatalf("secret call = %+v", c)
	}
	if strings.Join(c.args, " ") != "set --file "+filepath.Join(dir, "mise.toml")+" --age-encrypt --stdin API_KEY" {
		t.Errorf("args = %q", c.args)
	}
	if err := botEnvSet(b, dir, "PLAIN", "v", false); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[len(*calls)-1]; c.args[len(c.args)-1] != "PLAIN=v" {
		t.Errorf("plain args = %q", c.args)
	}
	if err := botEnvSet(b, dir, "1BAD", "v", false); err == nil {
		t.Error("accepted a bad key")
	}
}

func TestParseMiseSet(t *testing.T) {
	file := "/home/u/bots/a/mise.toml"
	out := "FOO    bar baz   " + file + "\nTOKEN  [redacted] " + file + "\nGLOBAL x /home/u/.config/mise/config.toml\n"
	got := parseMiseSet(out, file)
	if len(got) != 2 || got[0] != (botEnvVar{Key: "FOO", Value: "bar baz"}) || got[1] != (botEnvVar{Key: "TOKEN", Secret: true}) {
		t.Errorf("got %+v", got)
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
	recordMise(t, "")
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

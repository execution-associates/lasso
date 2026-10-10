package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPluginFingerprintStableWithoutAgents(t *testing.T) {
	m := &pluginManifest{
		Name:  "hello",
		Tabs:  []pluginTabSpec{{ID: "main", Label: "Hello", Entry: "ui/index.html"}},
		Views: []pluginTabSpec{{ID: "v", Label: "V", Entry: "ui/v.html"}},
	}
	// The canonical form as it was before agents: no agents key at all.
	old := []byte(`{"tabs":[{"entry":"ui/index.html"}],"views":[{"entry":"ui/v.html"}]}`)
	sum := sha256.Sum256(append([]byte("lasso-plugin-perms/1\n"), old...))
	if got, want := m.fingerprint(), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("fingerprint = %s, want the pre-agents %s", got, want)
	}
}

func TestPluginAgentsFingerprint(t *testing.T) {
	base := func(agents ...pluginAgentSpec) *pluginManifest {
		return &pluginManifest{
			Name:   "openbot",
			Views:  []pluginTabSpec{{ID: "chat", Label: "Chat", Entry: "ui/index.html"}},
			Agents: agents,
		}
	}
	none := base().fingerprint()
	one := base(pluginAgentSpec{Name: "jessica"}).fingerprint()
	if one == none {
		t.Error("granting an agent did not change the fingerprint")
	}
	if got := base(pluginAgentSpec{Name: "jessica", Host: "local"}).fingerprint(); got != one {
		t.Error(`{name} and {name, host: "local"} should approve the same thing`)
	}
	ab := base(pluginAgentSpec{Name: "a"}, pluginAgentSpec{Name: "b"}).fingerprint()
	if got := base(pluginAgentSpec{Name: "b"}, pluginAgentSpec{Name: "a"}, pluginAgentSpec{Name: "a"}).fingerprint(); got != ab {
		t.Error("reordering or repeating agents changed the fingerprint")
	}
	if got := base(pluginAgentSpec{Name: "jessica", Host: "citadel"}).fingerprint(); got == one {
		t.Error("moving the grant to another host did not change the fingerprint")
	}
}

func TestPluginAgentsValidation(t *testing.T) {
	bad := [][]pluginAgentSpec{
		{{Name: ""}},
		{{Name: "-x"}},
		{{Name: "a b"}},
		{{Name: "ok", Host: "../x"}},
		{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}, {Name: "e"}, {Name: "f"}, {Name: "g"}, {Name: "h"}, {Name: "i"}},
	}
	for _, a := range bad {
		if validatePluginAgents(a) == nil {
			t.Errorf("validatePluginAgents(%+v) accepted it", a)
		}
	}
	if err := validatePluginAgents([]pluginAgentSpec{{Name: "jessica"}, {Name: "clem", Host: "citadel"}}); err != nil {
		t.Errorf("valid agents refused: %v", err)
	}
}

// pluginChatFake answers the few herdr RPCs a plugin chat call makes and
// records what was typed or pressed.
type pluginChatFake struct {
	Backend
	status string
	mu     sync.Mutex
	keys   []any
	gets   []string
}

func (f *pluginChatFake) Name() string { return "local" }

func (f *pluginChatFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch method {
	case "agent.get":
		target, _ := params.(map[string]any)["target"].(string)
		f.gets = append(f.gets, target)
		if target != "jessica" && target != "w1:p1" {
			return nil, &herdrError{Code: "not_found", Message: target}
		}
		// herdr answers a pane id target with the agent in it: the name check
		// is what keeps a grant from being addressed by pane.
		return json.RawMessage(`{"agent":{"name":"jessica","pane_id":"w1:p1"}}`), nil
	case "pane.list":
		return json.RawMessage(`{"panes":[{"pane_id":"w1:p1","workspace_id":"w1","agent":"claude","agent_status":"` + f.status + `","terminal_title":"✳ Jessica"}]}`), nil
	case "pane.send_keys":
		f.keys = append(f.keys, params)
		return json.RawMessage(`{}`), nil
	}
	return nil, &herdrError{Code: "method_not_found", Message: method}
}

func setupPluginChat(t *testing.T, agents []map[string]any) (*pluginManager, *pluginChatFake) {
	t.Helper()
	m, dir := testPluginManager(t)
	man := map[string]any{"name": "openbot", "version": "0.1.0",
		"views":  []any{map[string]any{"id": "chat", "label": "Chat", "entry": "ui/index.html"}},
		"agents": agents}
	writePlugin(t, dir, "openbot", man, map[string]string{"ui/index.html": "<p>hi</p>"})
	m.rescan()
	if err := m.enable("openbot", pluginByName(t, m.listing(), "openbot").Fingerprint); err != nil {
		t.Fatal(err)
	}
	// pane.list is cached per host name; another test's snapshot must not
	// answer for this fake.
	invalidatePaneList("local")
	t.Cleanup(func() { invalidatePaneList("local") })
	fb := &pluginChatFake{status: "working"}
	prev := pluginChatBackend
	pluginChatBackend = func(host string) (Backend, error) { return fb, nil }
	t.Cleanup(func() { pluginChatBackend = prev })
	return m, fb
}

func pluginChatReq(m *pluginManager, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	m.serveAPI(w, r)
	return w
}

func TestPluginChatGrant(t *testing.T) {
	m, fb := setupPluginChat(t, []map[string]any{{"name": "jessica"}})

	if w := pluginChatReq(m, "POST", "/api/plugins/openbot/chat/stop", `{"agent":"jessica"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"sent"`) {
		t.Fatalf("granted stop = %d %s", w.Code, w.Body)
	}
	if len(fb.keys) != 1 || !strings.Contains(asJSON(fb.keys[0]), `"Escape"`) || !strings.Contains(asJSON(fb.keys[0]), `"w1:p1"`) {
		t.Fatalf("keys sent = %v; want one Escape to w1:p1", fb.keys)
	}

	cases := []struct {
		name, path, body string
		code             int
	}{
		{"other agent", "/api/plugins/openbot/chat/stop", `{"agent":"clem"}`, http.StatusForbidden},
		{"other host", "/api/plugins/openbot/chat/stop", `{"agent":"jessica","host":"citadel"}`, http.StatusForbidden},
		{"pane id as agent", "/api/plugins/openbot/chat/stop", `{"agent":"w1:p1"}`, http.StatusForbidden},
		{"other plugin", "/api/plugins/hello/chat/stop", `{"agent":"jessica"}`, http.StatusNotFound},
		{"empty text", "/api/plugins/openbot/chat/send", `{"agent":"jessica","text":"  "}`, http.StatusBadRequest},
		{"bad answer", "/api/plugins/openbot/chat/answer", `{"agent":"jessica","answers":[{"selected":[-1]}]}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if w := pluginChatReq(m, "POST", c.path, c.body); w.Code != c.code {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, strings.TrimSpace(w.Body.String()), c.code)
		}
	}
	if len(fb.keys) != 1 {
		t.Errorf("a refused call still pressed keys: %v", fb.keys)
	}
	if w := pluginChatReq(m, "GET", "/api/plugins/openbot/chat/stop", ""); w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET stop = %d", w.Code)
	}

	// Disabled: the grant is no longer in force.
	if err := m.disable("openbot"); err != nil {
		t.Fatal(err)
	}
	if w := pluginChatReq(m, "POST", "/api/plugins/openbot/chat/stop", `{"agent":"jessica"}`); w.Code != http.StatusNotFound {
		t.Errorf("disabled stop = %d", w.Code)
	}
}

func TestPluginChatAgentNameMustMatch(t *testing.T) {
	// A grant for the pane's id is still checked against the agent's NAME.
	m, _ := setupPluginChat(t, []map[string]any{{"name": "w1"}})
	if w := pluginChatReq(m, "POST", "/api/plugins/openbot/chat/stop", `{"agent":"w1"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown agent name = %d %s; want 404", w.Code, w.Body)
	}
}

func TestPluginChatStopRefusedWhenIdle(t *testing.T) {
	m, fb := setupPluginChat(t, []map[string]any{{"name": "jessica"}})
	fb.status = "idle"
	invalidatePaneList("local")
	w := pluginChatReq(m, "POST", "/api/plugins/openbot/chat/stop", `{"agent":"jessica"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refused"`) {
		t.Fatalf("idle stop = %d %s; want refused", w.Code, w.Body)
	}
	if len(fb.keys) != 0 {
		t.Errorf("Escape sent to an idle agent: %v", fb.keys)
	}
}

func TestPluginChatRead(t *testing.T) {
	m, _ := setupPluginChat(t, []map[string]any{{"name": "jessica"}})
	w := pluginChatReq(m, "GET", "/api/plugins/openbot/chat?agent=jessica", "")
	if w.Code != 200 {
		t.Fatalf("read = %d %s", w.Code, w.Body)
	}
	var out chatPayload
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PaneID != "w1:p1" || out.Items == nil {
		t.Errorf("payload = %+v; want pane w1:p1 with a (possibly empty) items list", out)
	}
	if w := pluginChatReq(m, "GET", "/api/plugins/openbot/chat?agent=clem", ""); w.Code != http.StatusForbidden {
		t.Errorf("un-granted read = %d", w.Code)
	}
}

func asJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestClaudeIncomingChannelRows(t *testing.T) {
	log := chatLog(
		`{"type":"user","uuid":"c1","timestamp":"2026-10-10T01:00:00Z","isMeta":true,"origin":{"kind":"channel","server":"gmail-channel"},"message":{"role":"user","content":"<channel source=\"gmail-channel\" subject=\"Hi\">hello</channel>"}}`,
		`{"type":"user","uuid":"m1","timestamp":"2026-10-10T01:00:01Z","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>not a channel</local-command-caveat>"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-10-10T01:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"On it."}]}}`,
	)
	plain := parseTranscriptLines("claude", splitLogLines(log, 0), false)
	if len(plain.items) != 1 || plain.items[0].Kind != "agent" {
		t.Fatalf("lasso's chat = %+v; want only the agent row", plain.items)
	}
	with := parseTranscriptLines("claude", splitLogLines(log, 0), true)
	if len(with.items) != 2 {
		t.Fatalf("with incoming = %+v; want the channel row and the agent row", with.items)
	}
	in := with.items[0]
	if in.Kind != "incoming" || in.Source != "gmail-channel" || !strings.Contains(in.Text, "hello") {
		t.Errorf("incoming row = %+v", in)
	}
}

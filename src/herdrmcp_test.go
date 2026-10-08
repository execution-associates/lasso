package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// herdrFixtureSchema is a trimmed `herdr api schema --json`: a no-arg method,
// one whose source parameter is an enum behind a $ref, one with a nested
// object, a client-local method, an alias-bearing method, and two that the
// deny list must drop (a streaming subscription and an agent's self-report).
const herdrFixtureSchema = `{
  "protocol": 22,
  "schema_version": 1,
  "schemas": {
    "request": {
      "oneOf": [
        {"type": "object", "properties": {"method": {"const": "ping", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/EmptyParams"}}},
        {"type": "object", "properties": {"method": {"const": "pane.list", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/EmptyParams"}}},
        {"type": "object", "properties": {"method": {"const": "pane.read", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/PaneReadParams"}}},
        {"type": "object", "properties": {"method": {"const": "pane.close", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/PaneTarget"}}},
        {"type": "object", "properties": {"method": {"const": "agent.prompt", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/AgentPromptParams"}}},
        {"type": "object", "properties": {"method": {"const": "popup.close", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/EmptyParams"}}},
        {"type": "object", "properties": {"method": {"const": "events.subscribe", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/EmptyParams"}}},
        {"type": "object", "properties": {"method": {"const": "pane.report_agent", "type": "string"}, "params": {"$ref": "#/schemas/request/$defs/EmptyParams"}}}
      ],
      "$defs": {
        "EmptyParams": {"type": "object", "properties": {}},
        "PaneTarget": {"type": "object", "properties": {"pane_id": {"type": "string"}}, "required": ["pane_id"]},
        "PaneReadParams": {
          "type": "object",
          "properties": {"pane_id": {"type": "string"}, "source": {"$ref": "#/schemas/request/$defs/ReadSource"}},
          "required": ["pane_id", "source"]
        },
        "ReadSource": {"type": "string", "enum": ["visible", "recent", "recent_unwrapped"]},
        "AgentPromptParams": {
          "type": "object",
          "properties": {"target": {"type": "string"}, "text": {"type": "string"}, "wait": {"$ref": "#/schemas/request/$defs/AgentPromptWaitOptions"}},
          "required": ["target", "text"]
        },
        "AgentPromptWaitOptions": {"type": "object", "properties": {"timeout_ms": {"type": "integer"}}},
        "Unused": {"type": "string"}
      }
    }
  }
}`

// herdrFixtureReplacing returns the fixture with one method renamed, which is
// the shape of a herdr update that moves the method set WITHOUT moving the
// protocol number (0.9.1 adding pane.link.resolve inside protocol 22).
func herdrFixtureReplacing(oldMethod, newMethod string) []byte {
	return []byte(strings.Replace(herdrFixtureSchema, `"const": "`+oldMethod+`"`, `"const": "`+newMethod+`"`, 1))
}

func herdrToolsByName(t *testing.T, sess *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	tools, err := listMCPTools(context.Background(), sess)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	m := map[string]*mcp.Tool{}
	for _, tool := range tools {
		m[tool.Name] = tool
	}
	return m
}

// newHerdrMCPForTest builds the server from the fixture, never touching a
// herdr binary.
func newHerdrMCPForTest(t *testing.T) *herdrMCPServer {
	t.Helper()
	h := newHerdrMCPServer()
	h.loadSchema = func(context.Context) ([]byte, error) { return []byte(herdrFixtureSchema), nil }
	if err := h.refresh(context.Background()); err != nil {
		t.Fatalf("load fixture schema: %v", err)
	}
	return h
}

// herdrMCPTestSession serves h behind the real gate (withMCPAuth) over httptest
// and connects an SDK client presenting token ("" = none), so auth, transport
// and tool dispatch are all the production path.
func herdrMCPTestSession(t *testing.T, h *herdrMCPServer, token string) *mcp.ClientSession {
	t.Helper()
	sess, err := herdrMCPDial(t, h, token)
	if err != nil {
		t.Fatalf("connect to /herdr-mcp: %v", err)
	}
	return sess
}

func herdrMCPDial(t *testing.T, h *herdrMCPServer, token string) (*mcp.ClientSession, error) {
	t.Helper()
	srv := httptest.NewServer(withMCPAuth(h.handler(), "", "", false))
	t.Cleanup(srv.Close)
	hc := http.DefaultClient
	if token != "" {
		hc = &http.Client{Transport: bearerRT{token: token}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	sess, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: srv.URL, HTTPClient: hc, DisableStandaloneSSE: true}, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess, nil
}

// herdrFakeHost is one host's herdr: it records every call and answers with
// where the call landed, or fails wholesale while down (herdr restarting, the
// socket gone).
type herdrFakeHost struct {
	*memBackend
	name string
	down bool

	mu    sync.Mutex
	calls []herdrFakeCall
}

type herdrFakeCall struct {
	Method string
	Params map[string]any
}

func (b *herdrFakeHost) Name() string { return b.name }

func (b *herdrFakeHost) HerdrCall(method string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	b.mu.Lock()
	b.calls = append(b.calls, herdrFakeCall{method, p})
	b.mu.Unlock()
	if b.down {
		return nil, fmt.Errorf("dial unix /run/herdr.sock: connect: connection refused")
	}
	return json.Marshal(map[string]any{"host": b.name, "method": method})
}

func (b *herdrFakeHost) lastCall(t *testing.T) herdrFakeCall {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		t.Fatalf("host %s received no herdr call", b.name)
	}
	return b.calls[len(b.calls)-1]
}

// stubHerdrHosts points the MCP tools' backend resolution at fakes, keyed by
// host name, and declares the non-local ones as ssh aliases so host scope
// admits them. Anything else is "not available", as hostBackend says.
func stubHerdrHosts(t *testing.T, hosts ...*herdrFakeHost) {
	t.Helper()
	byName := map[string]Backend{}
	var aliases []string
	for _, h := range hosts {
		byName[h.name] = h
		if h.name != "local" {
			aliases = append(aliases, h.name)
		}
	}
	stubSSHHosts(t, aliases...)
	prev := resolveBackend
	resolveBackend = func(host string) (Backend, error) {
		if host == "" {
			host = "local"
		}
		if b, ok := byName[host]; ok {
			return b, nil
		}
		return nil, fmt.Errorf("host %s not available", host)
	}
	t.Cleanup(func() { resolveBackend = prev })
}

func newHerdrFakeHost(name string) *herdrFakeHost {
	return &herdrFakeHost{memBackend: newMemBackend(), name: name}
}

// callHerdrTool invokes one tool and returns its result payload and error text
// (empty on success).
func callHerdrTool(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport error: %v", name, err)
	}
	if res.IsError {
		return nil, toolErrorText(res)
	}
	out, _ := res.StructuredContent.(map[string]any)
	return out, ""
}

func TestHerdrToolsFromFixtureSchema(t *testing.T) {
	sess := herdrMCPTestSession(t, newHerdrMCPForTest(t), "")
	tools := herdrToolsByName(t, sess)

	var names []string
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	// herdr-mcp's naming, the deny list applied, and machine_list alongside.
	want := []string{"agent_prompt", "machine_list", "pane_close", "pane_list", "pane_read", "ping", "popup_close"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	read := tools["pane_read"]
	schema, _ := json.Marshal(read.InputSchema)
	var in struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
		Defs       map[string]any            `json:"$defs"`
	}
	if err := json.Unmarshal(schema, &in); err != nil {
		t.Fatal(err)
	}
	if in.Properties["source"]["enum"] == nil || in.Properties["source"]["$ref"] != nil {
		t.Errorf("source enum was not inlined: %v", in.Properties["source"])
	}
	if len(in.Defs) != 0 {
		t.Errorf("unneeded $defs retained: %v", in.Defs)
	}
	if _, ok := in.Properties["host"]; !ok {
		t.Error("pane_read takes no host argument")
	}
	if _, ok := in.Properties["machine"]; !ok {
		t.Error("pane_read takes no machine alias")
	}
	if strings.Join(in.Required, ",") != "pane_id,source" {
		t.Errorf("required = %v — host must stay optional", in.Required)
	}
	if read.Title != "Pane Read" || !strings.HasPrefix(read.Description, "Read a terminal pane's output") {
		t.Errorf("title/description = %q / %q", read.Title, read.Description)
	}
	if !read.Annotations.ReadOnlyHint {
		t.Error("pane_read is not annotated read-only")
	}

	// A nested object behind a $ref keeps its definition, rewritten local.
	prompt, _ := json.Marshal(tools["agent_prompt"].InputSchema)
	if !strings.Contains(string(prompt), `"#/$defs/AgentPromptWaitOptions"`) || strings.Contains(string(prompt), "#/schemas/") {
		t.Errorf("agent_prompt schema refs not rewritten: %s", prompt)
	}

	if a := tools["pane_close"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("pane_close annotations = %+v, want destructive", a)
	}

	// The attached-client methods cannot be routed, so they do not offer to be.
	popup, _ := json.Marshal(tools["popup_close"].InputSchema)
	if strings.Contains(string(popup), `"host"`) {
		t.Errorf("popup_close offers a host argument: %s", popup)
	}
}

func TestHerdrToolsRouteByHost(t *testing.T) {
	local, gig, sleepy := newHerdrFakeHost("local"), newHerdrFakeHost("gigachad"), newHerdrFakeHost("sleepy")
	sleepy.down = true
	stubHerdrHosts(t, local, gig, sleepy)
	sess := herdrMCPTestSession(t, newHerdrMCPForTest(t), "")

	// No host: an unidentified caller's own host is the box lasso runs on.
	out, errMsg := callHerdrTool(t, sess, "pane_list", nil)
	if errMsg != "" || out["host"] != "local" {
		t.Fatalf("pane_list with no host = %v %q, want local", out, errMsg)
	}

	// host routes, and is stripped before herdr sees it (it rejects unknown params).
	out, errMsg = callHerdrTool(t, sess, "pane_read", map[string]any{"host": "gigachad", "pane_id": "w1:p1", "source": "recent"})
	if errMsg != "" || out["host"] != "gigachad" || out["method"] != "pane.read" {
		t.Fatalf("pane_read on gigachad = %v %q", out, errMsg)
	}
	if p := gig.lastCall(t).Params; p["host"] != nil || p["machine"] != nil || p["pane_id"] != "w1:p1" {
		t.Errorf("params herdr received = %v", p)
	}

	// machine is herdr-mcp's spelling of the same thing.
	if out, errMsg = callHerdrTool(t, sess, "pane_list", map[string]any{"machine": "gigachad"}); errMsg != "" || out["host"] != "gigachad" {
		t.Fatalf("pane_list machine=gigachad = %v %q", out, errMsg)
	}
	if _, errMsg = callHerdrTool(t, sess, "pane_list", map[string]any{"host": "gigachad", "machine": "local"}); !strings.Contains(errMsg, "disagree") {
		t.Errorf("conflicting host/machine = %q, want a refusal", errMsg)
	}

	// A host lasso cannot address is a tool error naming the rule, never a call.
	if _, errMsg = callHerdrTool(t, sess, "pane_list", map[string]any{"host": "nowhere"}); !strings.Contains(errMsg, "not addressable") {
		t.Errorf("unknown host = %q, want the host-scope refusal", errMsg)
	}

	// A host whose herdr is down fails that call only; the others keep answering.
	if _, errMsg = callHerdrTool(t, sess, "pane_list", map[string]any{"host": "sleepy"}); !strings.Contains(errMsg, "connection refused") || !strings.Contains(errMsg, `"host":"sleepy"`) {
		t.Errorf("down host = %q, want herdr's error naming the host", errMsg)
	}
	if out, errMsg = callHerdrTool(t, sess, "pane_list", map[string]any{"host": "gigachad"}); errMsg != "" || out["host"] != "gigachad" {
		t.Errorf("gigachad after sleepy failed = %v %q", out, errMsg)
	}

	// Client-local methods run on lasso's box and refuse to be sent elsewhere.
	if out, errMsg = callHerdrTool(t, sess, "popup_close", nil); errMsg != "" || out["host"] != "local" {
		t.Errorf("popup_close = %v %q, want local", out, errMsg)
	}
	if _, errMsg = callHerdrTool(t, sess, "popup_close", map[string]any{"host": "gigachad"}); errMsg == "" {
		t.Error("popup_close was routed to gigachad")
	}
}

func TestHerdrToolsValidateAndAliasArguments(t *testing.T) {
	local := newHerdrFakeHost("local")
	stubHerdrHosts(t, local)
	sess := herdrMCPTestSession(t, newHerdrMCPForTest(t), "")

	_, errMsg := callHerdrTool(t, sess, "pane_read", map[string]any{"pane_id": "w1:p1", "sorce": "recent"})
	if !strings.Contains(errMsg, `did you mean \"source\"`) {
		t.Errorf("typo = %q, want a suggestion", errMsg)
	}
	_, errMsg = callHerdrTool(t, sess, "pane_read", map[string]any{"pane_id": "w1:p1", "source": "everything"})
	if !strings.Contains(errMsg, "must be one of") {
		t.Errorf("bad enum = %q, want the permitted values", errMsg)
	}
	if n := len(local.calls); n != 0 {
		t.Errorf("%d invalid calls reached herdr", n)
	}

	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: "agent_prompt",
		Arguments: map[string]any{"target": "reviewer", "message": "go"}})
	if err != nil || res.IsError {
		t.Fatalf("agent_prompt with the message alias: %v %s", err, toolErrorText(res))
	}
	if p := local.lastCall(t).Params; p["text"] != "go" || p["message"] != nil {
		t.Errorf("alias not mapped: %v", p)
	}
	noted := false
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok && strings.Contains(tc.Text, `Mapped argument "message" to "text"`) {
			noted = true
		}
	}
	if !noted {
		t.Errorf("no note saying the alias was mapped: %v", res.Content)
	}
}

// Over the real endpoint with MCP_OAUTH on: no credential is refused at the
// door, and a host-scoped credential reaches exactly what it reaches on /mcp.
func TestHerdrMCPAuthAndReach(t *testing.T) {
	openTestDB(t)
	enableOAuth(t, "")
	local, gig := newHerdrFakeHost("local"), newHerdrFakeHost("gigachad")
	stubHerdrHosts(t, local, gig)
	stubHostProbes(t)
	h := newHerdrMCPForTest(t)

	if _, err := herdrMCPDial(t, h, ""); err == nil {
		t.Fatal("an anonymous client connected to /herdr-mcp with MCP_OAUTH set")
	}
	if _, err := herdrMCPDial(t, h, "not-a-token"); err == nil {
		t.Fatal("a bogus bearer token connected to /herdr-mcp")
	}

	sess := herdrMCPTestSession(t, h, hostClientToken(t, "gigachad", scopeSelf))
	// No host lands on the credential's own host, not lasso's.
	if out, errMsg := callHerdrTool(t, sess, "pane_list", nil); errMsg != "" || out["host"] != "gigachad" {
		t.Fatalf("self-scoped pane_list = %v %q, want gigachad", out, errMsg)
	}
	for _, args := range []map[string]any{{"host": "local"}, {"machine": "local"}} {
		_, errMsg := callHerdrTool(t, sess, "pane_list", args)
		if !strings.Contains(errMsg, "may not address host") {
			t.Errorf("self-scoped %v = %q, want the credential refusal", args, errMsg)
		}
	}
	// The attached-client methods live on lasso's box, which this caller may not touch.
	if _, errMsg := callHerdrTool(t, sess, "popup_close", nil); !strings.Contains(errMsg, "may not address host") {
		t.Errorf("self-scoped popup_close = %q, want refused", errMsg)
	}
	if n := len(local.calls); n != 0 {
		t.Errorf("a contained caller reached lasso's herdr %d times", n)
	}
	out, errMsg := callHerdrTool(t, sess, "machine_list", nil)
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	ms, _ := out["machines"].([]any)
	if len(ms) != 1 || ms[0].(map[string]any)["host"] != "gigachad" {
		t.Errorf("self-scoped machine_list = %v, want only gigachad", ms)
	}

	fleet := herdrMCPTestSession(t, h, hostClientToken(t, "local", scopeFleet))
	if out, errMsg := callHerdrTool(t, fleet, "pane_list", map[string]any{"host": "gigachad"}); errMsg != "" || out["host"] != "gigachad" {
		t.Errorf("fleet caller on gigachad = %v %q", out, errMsg)
	}
	if out, errMsg := callHerdrTool(t, fleet, "pane_list", nil); errMsg != "" || out["host"] != "local" {
		t.Errorf("fleet caller with no host = %v %q, want its own host", out, errMsg)
	}
}

// stubHostProbes answers host discovery instantly, so machine_list (list_hosts
// underneath) never dials a real ssh alias.
func stubHostProbes(t *testing.T) {
	t.Helper()
	resetHostStore(t)
	prev := probeHostFn
	probeHostFn = func(_ context.Context, alias string, _ int) HostInfo {
		return HostInfo{Alias: alias, Reachable: true, Running: true, Compatible: true}
	}
	t.Cleanup(func() {
		drainSweep(t)
		probeHostFn = prev
		resetHostStore(t)
	})
}

// Reload follows herdr's schema without a restart: a method that moved
// appears, the one it replaced stops being advertised to a session that was
// already connected, and the same document again changes nothing.
func TestHerdrMCPReload(t *testing.T) {
	stubHerdrHosts(t, newHerdrFakeHost("local"))
	h := newHerdrMCPForTest(t)
	sess := herdrMCPTestSession(t, h, "")
	if _, ok := herdrToolsByName(t, sess)["pane_list"]; !ok {
		t.Fatal("pane_list missing before the reload")
	}

	next := herdrFixtureReplacing("pane.list", "pane.link.resolve")
	h.loadSchema = func(context.Context) ([]byte, error) { return next, nil }
	if err := h.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools := herdrToolsByName(t, sess)
	if _, ok := tools["pane_list"]; ok {
		t.Error("a removed method is still advertised")
	}
	if _, ok := tools["pane_link_resolve"]; !ok {
		t.Error("the added method is not advertised")
	}
	if _, ok := tools["machine_list"]; !ok {
		t.Error("machine_list was dropped by the reload")
	}
	if _, errMsg := callHerdrTool(t, sess, "pane_link_resolve", nil); errMsg != "" {
		t.Errorf("the new tool does not dispatch: %s", errMsg)
	}

	added, removed, changed, err := h.reload(next)
	if err != nil || changed || added != nil || removed != nil {
		t.Errorf("same schema again = %v %v %v %v, want a no-op", added, removed, changed, err)
	}

	// A schema that will not load keeps the tools that were working.
	h.loadSchema = func(context.Context) ([]byte, error) { return nil, fmt.Errorf("herdr: not found") }
	if err := h.refresh(context.Background()); err == nil {
		t.Error("a failed load reported success")
	}
	h.loadSchema = func(context.Context) ([]byte, error) { return []byte(`{"protocol":22}`), nil }
	if err := h.refresh(context.Background()); err == nil {
		t.Error("a malformed schema reported success")
	}
	if n := h.toolCount(); n != 6 {
		t.Errorf("tools after failed reloads = %d, want the 6 that were registered", n)
	}
}

func TestHerdrMCPTimeout(t *testing.T) {
	cases := []struct {
		method string
		args   map[string]any
		want   time.Duration
	}{
		{"pane.list", nil, herdrMCPCallTimeout},
		{"worktree.create", nil, 120 * time.Second},
		{"agent.wait", nil, herdrLongPollTimeout},
		{"agent.wait", map[string]any{"timeout_ms": float64(5000)}, 5*time.Second + herdrCallSlack},
		{"agent.prompt", map[string]any{"wait": map[string]any{"timeout_ms": float64(1_200_000)}}, 20*time.Minute + herdrCallSlack},
		{"agent.start", map[string]any{"timeout_ms": float64(90_000)}, 90*time.Second + herdrCallSlack},
		{"agent.start", map[string]any{"timeout_ms": float64(1000)}, herdrMCPCallTimeout},
	}
	for _, c := range cases {
		if got := herdrMCPTimeout(c.method, c.args); got != c.want {
			t.Errorf("herdrMCPTimeout(%s, %v) = %v, want %v", c.method, c.args, got, c.want)
		}
	}
}

// A forwarded call gives up when its caller does, rather than holding the
// socket for the rest of a long-poll window.
func TestHerdrCallSockWithinHonorsContext(t *testing.T) {
	sock := herdrEchoSock(t, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := herdrCallSockWithin(ctx, sock, "agent.wait", map[string]any{}, time.Minute)
	if err == nil {
		t.Fatal("a cancelled call succeeded")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("call took %v after its context ended", elapsed)
	}
}

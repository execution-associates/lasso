package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// /herdr-mcp — herdr's own socket API as MCP tools, served by lasso.
//
// This is the tool surface of the standalone herdr-mcp bridge
// (github.com/Orange-County-AI/herdr-mcp), rebuilt on lasso's machinery so it
// needs no second process: one tool per herdr socket method, generated from
// the schema the installed herdr binary prints (`herdr api schema --json`), the
// tool name being the method with "." and "-" turned into "_" (pane.list ->
// pane_list). Same names, descriptions, annotations, argument aliases and agent
// readiness handling as the bridge, so a client written against herdr-mcp works
// unchanged.
//
// What differs is where calls go. herdr-mcp routes by herdr's saved SSH
// machines; here a call goes wherever lasso's other MCP tools go — the `host`
// argument, resolved through resolveBackend and gated by the caller's
// credential exactly like /mcp (callerscope.go). `machine` is accepted as an
// alias of `host` so a herdr-mcp caller's arguments still route.
//
// It is its own endpoint rather than more tools on /mcp because ninety-odd raw
// socket methods are a different job from orchestrating agents, and an agent
// registered for one should not be handed the other's tool list.
//
// Nothing here queues. herdr-mcp parks calls through a herdr outage because it
// holds one socket client per machine; lasso dials a fresh connection per call
// (herdrCallSock) and keeps one SSH master per remote host in hostBackend's
// pool, which liveness-checks and redials it. A herdr that is down therefore
// costs exactly the call that hit it — a tool error naming the host — and
// every other host keeps answering.

// herdrMCPDenyMethods are omitted from the tool surface: the streaming
// subscription (an MCP tool returns once), the harness-internal lifecycle
// reporting agents use to describe THEMSELVES, the graphics plumbing, and the
// connection-scoped ssh-agent registration. herdr-mcp's default deny list,
// verbatim.
var herdrMCPDenyMethods = []string{
	"events.subscribe",
	"pane.report_agent",
	"pane.report_agent_session",
	"pane.report_metadata",
	"workspace.report_metadata",
	"pane.clear_agent_authority",
	"pane.release_agent",
	"pane.graphics.*",
	"server.ssh_agent.register",
}

// herdrSchemaRefresh is how often the schema is re-read once one has loaded,
// and herdrSchemaRetry how often until then (lasso booting while herdr is
// mid-upgrade must not leave the endpoint empty for five minutes).
const (
	herdrSchemaRefresh = 5 * time.Minute
	herdrSchemaRetry   = 30 * time.Second
)

// herdrMCPCallTimeout is the floor for one forwarded call. herdrTimeoutFor's 3s
// default is tuned for the UI's cheap reads; an MCP caller reaching for an
// arbitrary method (plugin.install clones a repo) needs headroom, and herdr's
// own error is the better answer than lasso's read deadline.
const herdrMCPCallTimeout = 60 * time.Second

// herdrLongPollMethods block inside herdr until something happens, for up to
// fifteen minutes (herdr-mcp's queue gives them their own lane for the same
// reason). Without a caller-supplied timeout_ms they get the whole window.
var herdrLongPollMethods = map[string]bool{
	"agent.wait":           true,
	"agent.prompt":         true,
	"events.wait":          true,
	"pane.wait_for_output": true,
}

const herdrLongPollTimeout = 15 * time.Minute

// herdrCallSlack pads a caller's timeout_ms so herdr's own timeout answer —
// the useful one — arrives before lasso's read deadline does.
const herdrCallSlack = 15 * time.Second

// herdrClientLocalMethods act on the herdr CLIENT attached to a session (a
// window title, a popup, an announcement, a live handoff), not on a server's
// shared state. There is no attached client at the far end of an SSH-forwarded
// socket, so routing one to another host would silently do nothing: they take
// no host argument and run on the box lasso runs on.
var herdrClientLocalMethods = map[string]bool{
	"client.window_title.set":      true,
	"client.window_title.clear":    true,
	"client_shell.surface.set":     true,
	"popup.close":                  true,
	"product_announcement.dismiss": true,
	"release_notes.dismiss":        true,
	"server.live_handoff":          true,
}

const herdrMCPInstructions = `This server exposes herdr's socket API, served by lasso for every host lasso can drive.
Each MCP tool maps directly to one herdr method: underscores in the tool name correspond to dots in the socket method name (agent_read calls agent.read).

Common agent workflow: worktree_create or workspace_create creates a root_pane.pane_id; pane_split creates another pane_id; agent_start requires that existing pane_id and does not create panes. agent_start waits for interactive readiness when possible. Then call agent_prompt with target (agent name or pane_id) and text; use agent_wait to wait for status and agent_read with source=recent_unwrapped to read output.

Read source values are visible (rendered viewport), recent (scrollback with soft wraps), recent_unwrapped (scrollback with wraps joined; best for logs), and detection (agent detector buffer).
Use session_snapshot or the list methods to discover stable workspace, tab, pane, and agent identifiers before mutating state.
Prefer agent_prompt, agent_wait, and agent_read for agent conversations. pane_send_text and pane_send_keys are lower-level terminal input and can interleave with an agent's active turn.
Close, remove, unlink, uninstall, release, and server-stop methods are destructive. Only call them when the user explicitly intends that state change.
events_subscribe and harness-internal lifecycle reporting are intentionally omitted from this client-facing tool surface.

Hosts: most tools take an optional host argument — "local" (the box lasso runs on) or an ssh alias; machine_list shows the ones your credential may address. machine is accepted as an alias of host. Omit both for your own host. Each host is an independent herdr server, so workspace, tab, pane and agent IDs are scoped to it: two hosts can both have w1:p1 or an agent named reviewer. Discover IDs on the host you intend to drive, never reuse another host's there. A failed remote call never falls back to another host, and a connection error does not prove a mutation was not applied — inspect state before retrying. Tools that act on the attached herdr client (window title, popup, announcements, live handoff) take no host and run on lasso's own box.`

// ---------------------------------------------------------------------------
// schema -> tool definitions
// ---------------------------------------------------------------------------

const herdrRequestDefPrefix = "#/schemas/request/$defs/"

// herdrSchema is the document `herdr api schema --json` prints.
type herdrSchema struct {
	Protocol      int                        `json:"protocol"`
	SchemaVersion int                        `json:"schema_version"`
	Schemas       map[string]json.RawMessage `json:"schemas"`

	// Digest is the sha256 of the raw document. The protocol number is not a
	// staleness signal: herdr 0.9.1 added pane.link.resolve INSIDE protocol 22,
	// so a surface compared by protocol kept serving one tool fewer than the
	// running herdr had. Comparing digests catches a method set that moved
	// without the number moving.
	Digest string `json:"-"`
}

// herdrMethod is one socket method as an MCP tool.
type herdrMethod struct {
	Method      string
	ToolName    string
	InputSchema map[string]any
}

func herdrToolName(method string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(method)
}

func parseHerdrSchema(data []byte) (*herdrSchema, error) {
	var s herdrSchema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("decode herdr API schema: %w", err)
	}
	s.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if s.Protocol <= 0 {
		return nil, fmt.Errorf("herdr API schema has invalid protocol %d", s.Protocol)
	}
	if s.SchemaVersion <= 0 {
		return nil, fmt.Errorf("herdr API schema has invalid schema_version %d", s.SchemaVersion)
	}
	if len(s.Schemas["request"]) == 0 {
		return nil, fmt.Errorf("herdr API schema does not contain schemas.request")
	}
	return &s, nil
}

// methods turns the request schema's oneOf variants into standalone MCP input
// schemas, each carrying only the $defs it transitively uses (a client renders
// every tool's schema, and the full request $defs is hundreds of definitions).
func (s *herdrSchema) methods(deny []string) ([]herdrMethod, error) {
	var request struct {
		OneOf []map[string]any `json:"oneOf"`
		Defs  map[string]any   `json:"$defs"`
	}
	if err := json.Unmarshal(s.Schemas["request"], &request); err != nil {
		return nil, fmt.Errorf("decode schemas.request: %w", err)
	}
	if len(request.OneOf) == 0 || len(request.Defs) == 0 {
		return nil, fmt.Errorf("schemas.request is missing oneOf variants or $defs")
	}
	out := make([]herdrMethod, 0, len(request.OneOf))
	seen := map[string]bool{}
	for _, variant := range request.OneOf {
		method, ref, err := herdrMethodVariant(variant)
		if err != nil {
			return nil, err
		}
		if herdrMatchesAny(method, deny) {
			continue
		}
		if seen[method] {
			return nil, fmt.Errorf("schemas.request contains duplicate method %q", method)
		}
		seen[method] = true

		rootName := strings.TrimPrefix(ref, herdrRequestDefPrefix)
		root, ok := request.Defs[rootName]
		if !ok {
			return nil, fmt.Errorf("method %q references missing definition %q", method, rootName)
		}
		input, ok := herdrCloneJSON(root).(map[string]any)
		if !ok || input["type"] != "object" {
			return nil, fmt.Errorf("method %q input schema must be an object", method)
		}
		closure := map[string]any{}
		for _, r := range herdrRequestRefs(input) {
			if err := herdrCollectDefs(strings.TrimPrefix(r, herdrRequestDefPrefix), request.Defs, closure); err != nil {
				return nil, fmt.Errorf("method %q input schema: %w", method, err)
			}
		}
		defs := make(map[string]any, len(closure))
		for name, def := range closure {
			c := herdrCloneJSON(def)
			herdrRewriteRefs(c)
			defs[name] = c
		}
		herdrRewriteRefs(input)
		herdrInlineEnums(input, defs)
		defs = herdrReferencedDefs(input, defs)
		if len(defs) > 0 {
			input["$defs"] = defs
		}
		out = append(out, herdrMethod{Method: method, ToolName: herdrToolName(method), InputSchema: input})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the herdr API schema exposes no methods after filtering")
	}
	return out, nil
}

func herdrMethodVariant(variant map[string]any) (method, ref string, err error) {
	props, ok := variant["properties"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("schemas.request variant is missing properties")
	}
	ms, ok := props["method"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("schemas.request variant is missing method schema")
	}
	method, ok = ms["const"].(string)
	if !ok || method == "" {
		return "", "", fmt.Errorf("schemas.request variant has no method const")
	}
	ps, ok := props["params"].(map[string]any)
	if !ok {
		return "", "", fmt.Errorf("method %q is missing params schema", method)
	}
	ref, ok = ps["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, herdrRequestDefPrefix) {
		return "", "", fmt.Errorf("method %q has unsupported params schema reference %q", method, ref)
	}
	return method, ref, nil
}

func herdrCollectDefs(name string, defs, collected map[string]any) error {
	if _, ok := collected[name]; ok {
		return nil
	}
	def, ok := defs[name]
	if !ok {
		return fmt.Errorf("missing referenced definition %q", name)
	}
	collected[name] = def
	for _, r := range herdrRequestRefs(def) {
		if err := herdrCollectDefs(strings.TrimPrefix(r, herdrRequestDefPrefix), defs, collected); err != nil {
			return err
		}
	}
	return nil
}

func herdrRequestRefs(v any) []string {
	var refs []string
	var visit func(any)
	visit = func(cur any) {
		switch t := cur.(type) {
		case map[string]any:
			for k, child := range t {
				if r, ok := child.(string); ok && k == "$ref" && strings.HasPrefix(r, herdrRequestDefPrefix) {
					refs = append(refs, r)
				}
				visit(child)
			}
		case []any:
			for _, child := range t {
				visit(child)
			}
		}
	}
	visit(v)
	return refs
}

// herdrRewriteRefs points request-document refs at the tool schema's own $defs.
func herdrRewriteRefs(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if r, ok := child.(string); ok && k == "$ref" && strings.HasPrefix(r, herdrRequestDefPrefix) {
				t[k] = "#/$defs/" + strings.TrimPrefix(r, herdrRequestDefPrefix)
				continue
			}
			herdrRewriteRefs(child)
		}
	case []any:
		for _, child := range t {
			herdrRewriteRefs(child)
		}
	}
}

// herdrInlineEnums replaces a bare $ref to an enum definition with the enum
// itself, so every permitted value sits at the parameter that takes it.
func herdrInlineEnums(v any, defs map[string]any) {
	switch t := v.(type) {
	case map[string]any:
		if r, ok := t["$ref"].(string); ok && len(t) == 1 && strings.HasPrefix(r, "#/$defs/") {
			if def, ok := defs[strings.TrimPrefix(r, "#/$defs/")].(map[string]any); ok && def["enum"] != nil {
				delete(t, "$ref")
				for k, child := range def {
					t[k] = child
				}
				return
			}
		}
		for _, child := range t {
			herdrInlineEnums(child, defs)
		}
	case []any:
		for _, child := range t {
			herdrInlineEnums(child, defs)
		}
	}
}

// herdrReferencedDefs keeps only the definitions still reachable once leaf
// enums have been inlined.
func herdrReferencedDefs(input, defs map[string]any) map[string]any {
	used := map[string]bool{}
	var add func(any)
	add = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if r, ok := t["$ref"].(string); ok && strings.HasPrefix(r, "#/$defs/") {
				name := strings.TrimPrefix(r, "#/$defs/")
				if !used[name] {
					used[name] = true
					add(defs[name])
				}
			}
			for _, child := range t {
				add(child)
			}
		case []any:
			for _, child := range t {
				add(child)
			}
		}
	}
	add(input)
	out := make(map[string]any, len(used))
	for name := range used {
		out[name] = defs[name]
	}
	return out
}

// herdrCloneJSON deep-copies a decoded JSON value, so the per-tool rewrites
// never edit the shared request $defs.
func herdrCloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		c := make(map[string]any, len(t))
		for k, child := range t {
			c[k] = herdrCloneJSON(child)
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, child := range t {
			c[i] = herdrCloneJSON(child)
		}
		return c
	default:
		return v
	}
}

func herdrMatchesAny(method string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, method); ok {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// the server and its reload
// ---------------------------------------------------------------------------

const (
	herdrHostArg     = "host"
	herdrMachineArg  = "machine"
	herdrHostArgDesc = `Host to run this on: "local" (the box lasso runs on) or an ssh alias, as machine_list (or /mcp's list_hosts) shows them. Omit it for your own host. Workspace, tab, pane and agent IDs are scoped to one host, so an id discovered on one never addresses a pane on another; list on the host you intend to drive.`
	herdrMachineDesc = `Alias of host, for callers written against the standalone herdr-mcp bridge. A host name, not a herdr saved-machine profile id.`
)

// herdrMCPServer is the MCP server behind /herdr-mcp and the tool set it was
// last registered from.
type herdrMCPServer struct {
	srv *mcp.Server

	// loadSchema reads the raw schema document. A field so a test can hand in
	// a fixture without a herdr binary.
	loadSchema func(ctx context.Context) ([]byte, error)

	mu       sync.Mutex
	digest   string
	protocol int
	tools    map[string]bool
}

// herdrMCP is the one /herdr-mcp server, built in main.
var herdrMCP *herdrMCPServer

func newHerdrMCPServer() *herdrMCPServer {
	h := &herdrMCPServer{
		srv: mcp.NewServer(&mcp.Implementation{
			Name:    "lasso-herdr",
			Title:   "Herdr socket API (via lasso)",
			Version: lassoSemver,
		}, &mcp.ServerOptions{Instructions: herdrMCPInstructions}),
		loadSchema: loadHerdrSchemaBytes,
		tools:      map[string]bool{},
	}
	h.registerMachineList()
	return h
}

// loadHerdrSchemaBytes asks the installed herdr binary for the schema it was
// built with — the same lookup doctor.go uses. It needs the binary, not a
// running server.
func loadHerdrSchemaBytes(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "herdr", "api", "schema", "--json")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("herdr api schema --json: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("herdr api schema --json: %w", err)
	}
	return out, nil
}

func (h *herdrMCPServer) handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return h.srv }, &mcp.StreamableHTTPOptions{
		// Same reason as newMCPHandler: lasso is loopback-bound and reached
		// through a tunnel under a public hostname, which the SDK's DNS-rebinding
		// guard would 403. The gate is withMCPAuth plus Access.
		DisableLocalhostProtection: true,
	})
}

// toolCount reports how many herdr methods are registered right now
// (machine_list not included).
func (h *herdrMCPServer) toolCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.tools)
}

// reload swaps the tool surface to the one data describes, returning what
// changed. An unchanged digest is a no-op. It is both the cold start and the
// hot reload, so the two cannot drift. Removed methods are unregistered —
// otherwise the endpoint keeps advertising a method the running herdr no
// longer answers — and the SDK's tools/list_changed tells connected clients to
// look again.
func (h *herdrMCPServer) reload(data []byte) (added, removed []string, changed bool, err error) {
	schema, err := parseHerdrSchema(data)
	if err != nil {
		return nil, nil, false, err
	}
	methods, err := schema.methods(herdrMCPDenyMethods)
	if err != nil {
		return nil, nil, false, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if schema.Digest == h.digest {
		return nil, nil, false, nil
	}
	fresh := make(map[string]bool, len(methods))
	for _, m := range methods {
		def := m
		if !herdrClientLocalMethods[def.Method] {
			def.InputSchema = herdrWithHostArguments(def.InputSchema)
		}
		fresh[def.ToolName] = true
		if !h.tools[def.ToolName] {
			added = append(added, def.ToolName)
		}
		h.srv.AddTool(&mcp.Tool{
			Name:        def.ToolName,
			Title:       herdrToolTitle(def.Method),
			Description: herdrToolDescription(def.Method, def.InputSchema),
			InputSchema: def.InputSchema,
			Annotations: herdrAnnotations(def.Method),
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return h.call(ctx, req, def.Method, def.InputSchema), nil
		})
	}
	for name := range h.tools {
		if !fresh[name] {
			removed = append(removed, name)
		}
	}
	if len(removed) > 0 {
		h.srv.RemoveTools(removed...)
	}
	h.tools = fresh
	h.digest = schema.Digest
	h.protocol = schema.Protocol
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, true, nil
}

// refresh re-reads the schema and reloads when it moved. A schema that fails
// to load or register leaves the previous tools in place: a herdr binary
// missing mid-upgrade is no reason to empty the endpoint.
func (h *herdrMCPServer) refresh(ctx context.Context) error {
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	data, err := h.loadSchema(lctx)
	if err != nil {
		return err
	}
	added, removed, changed, err := h.reload(data)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	h.mu.Lock()
	n, proto := len(h.tools), h.protocol
	h.mu.Unlock()
	if len(added) == n {
		log.Printf("herdr-mcp: /herdr-mcp serving %d herdr methods (protocol %d)", n, proto)
	} else {
		log.Printf("herdr-mcp: herdr's schema changed: %d tools at protocol %d, added %v, removed %v", n, proto, added, removed)
	}
	// The binary and the running server can disagree after `herdr update`
	// until the server restarts. Calls still go through — herdr answers an
	// unknown method itself — but say why some may fail.
	if _, live, perr := herdrPing(*herdrSock); perr == nil && live != proto {
		log.Printf("herdr-mcp: WARNING: the herdr binary's schema is protocol %d but the running local server speaks %d — tools follow the binary; restart herdr to match", proto, live)
	}
	return nil
}

// run loads the schema now and keeps it current until ctx ends.
func (h *herdrMCPServer) run(ctx context.Context) {
	loaded := false
	for {
		if err := h.refresh(ctx); err != nil {
			log.Printf("herdr-mcp: schema not (re)loaded, keeping %d tools: %v", h.toolCount(), err)
		} else {
			loaded = true
		}
		every := herdrSchemaRefresh
		if !loaded {
			every = herdrSchemaRetry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// herdrWithHostArguments returns a copy of input that also accepts host and
// its machine alias. Injected into the schemas rather than minted as per-host
// tools: ninety methods times a fleet is not a tool list anyone can use.
func herdrWithHostArguments(input map[string]any) map[string]any {
	c := make(map[string]any, len(input)+1)
	for k, v := range input {
		c[k] = v
	}
	props, _ := c["properties"].(map[string]any)
	merged := make(map[string]any, len(props)+2)
	for k, v := range props {
		merged[k] = v
	}
	merged[herdrHostArg] = map[string]any{"type": "string", "description": herdrHostArgDesc}
	merged[herdrMachineArg] = map[string]any{"type": "string", "description": herdrMachineDesc}
	c["properties"] = merged
	return c
}

// ---------------------------------------------------------------------------
// a call
// ---------------------------------------------------------------------------

func (h *herdrMCPServer) call(ctx context.Context, req *mcp.CallToolRequest, method string, input map[string]any) (res *mcp.CallToolResult) {
	started := time.Now()
	host := ""
	// A tool error, never a crashed request: a backend that is half torn down
	// (an SSH master dying under the call) must cost this call only.
	defer func() {
		if p := recover(); p != nil {
			res = herdrErrorResult(method, host, fmt.Errorf("internal error: %v", p))
		}
		if res.IsError {
			log.Printf("herdr-mcp: %s on %q failed after %s: %s", herdrToolName(method), host,
				time.Since(started).Round(time.Millisecond), herdrResultText(res))
		}
	}()

	var raw json.RawMessage
	if req != nil && req.Params != nil {
		raw = req.Params.Arguments
	}
	args, notes, err := herdrNormalizeArguments(method, input, raw)
	if err != nil {
		return herdrErrorResult(method, "", err, notes...)
	}
	selector, err := herdrTakeHost(args)
	if err != nil {
		return herdrErrorResult(method, "", err, notes...)
	}
	cs := callerFrom(req)
	if herdrClientLocalMethods[method] {
		if selector != "" && !isLocalHost(selector) {
			return herdrErrorResult(method, selector, fmt.Errorf("%s acts on the herdr client attached to lasso's own session and cannot be routed to host %q", herdrToolName(method), selector), notes...)
		}
		host = "local"
	} else {
		host = cs.hostOr(selector)
	}
	if err := cs.requireHost(host); err != nil {
		return herdrErrorResult(method, host, err, notes...)
	}
	b, err := resolveBackend(host)
	if err != nil {
		return herdrErrorResult(method, host, err, notes...)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return herdrErrorResult(method, host, fmt.Errorf("encode arguments: %w", err), notes...)
	}
	// RawMessage, not []byte: the backends marshal params into the request
	// envelope, and a plain []byte would go to herdr as a base64 string.
	params := json.RawMessage(encoded)

	if method == "agent.wait" {
		if err := herdrWaitThroughLaunch(ctx, b, params); err != nil {
			return herdrErrorResult(method, host, err, notes...)
		}
	}
	result, err := herdrCallWithin(ctx, b, method, params, herdrMCPTimeout(method, args))
	if err != nil {
		if strings.HasPrefix(method, "agent.") {
			err = herdrEnrichAgentError(ctx, b, params, err)
		}
		return herdrErrorResult(method, host, err, notes...)
	}
	if method == "agent.start" {
		var note string
		result, note = herdrWaitForStartedAgent(ctx, b, result, params)
		if note != "" {
			notes = append(notes, note)
		}
	}
	var structured map[string]any
	if err := json.Unmarshal(result, &structured); err != nil {
		return herdrErrorResult(method, host, fmt.Errorf("herdr returned a non-object result: %w", err), notes...)
	}
	content := []mcp.Content{&mcp.TextContent{Text: string(result)}}
	for _, n := range notes {
		content = append(content, &mcp.TextContent{Text: n})
	}
	return &mcp.CallToolResult{Content: content, StructuredContent: structured}
}

// herdrTakeHost removes host/machine from the arguments (herdr's socket rejects
// params it does not know) and returns the host they named. Both at once is
// fine when they agree.
func herdrTakeHost(args map[string]any) (string, error) {
	pick := func(name string) (string, error) {
		v, ok := args[name]
		if !ok {
			return "", nil
		}
		delete(args, name)
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%q must be a string naming a host", name)
		}
		return strings.TrimSpace(s), nil
	}
	host, err := pick(herdrHostArg)
	if err != nil {
		return "", err
	}
	machine, err := pick(herdrMachineArg)
	if err != nil {
		return "", err
	}
	if host != "" && machine != "" && host != machine {
		return "", fmt.Errorf("host %q and machine %q disagree; machine is an alias of host, pass one", host, machine)
	}
	if host == "" {
		host = machine
	}
	return host, nil
}

// herdrMCPTimeout is the read deadline for one forwarded call: at least
// herdrMCPCallTimeout (or the UI's longer budget for a slow mutation), the
// whole long-poll window for a blocking method, and always past a timeout_ms
// the caller set — top level, or agent.prompt's nested wait.timeout_ms.
func herdrMCPTimeout(method string, args map[string]any) time.Duration {
	d := max(herdrTimeoutFor(method), herdrMCPCallTimeout)
	if herdrLongPollMethods[method] {
		d = herdrLongPollTimeout
	}
	ms, _ := args["timeout_ms"].(float64)
	if w, ok := args["wait"].(map[string]any); ok {
		if n, _ := w["timeout_ms"].(float64); n > ms {
			ms = n
		}
	}
	if ms > 0 {
		if want := time.Duration(ms)*time.Millisecond + herdrCallSlack; herdrLongPollMethods[method] || want > d {
			d = want
		}
	}
	return d
}

func herdrErrorResult(method, host string, err error, notes ...string) *mcp.CallToolResult {
	body := map[string]any{"method": method, "error": err.Error()}
	if host != "" {
		body["host"] = host
	}
	payload, _ := json.Marshal(body)
	content := []mcp.Content{&mcp.TextContent{Text: string(payload)}}
	for _, n := range notes {
		content = append(content, &mcp.TextContent{Text: n})
	}
	return &mcp.CallToolResult{Content: content, IsError: true}
}

func herdrResultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return "(no content)"
}

// ---------------------------------------------------------------------------
// machine_list
// ---------------------------------------------------------------------------

const herdrMachineListTool = "machine_list"

// registerMachineList adds the one tool that is not a herdr socket method:
// without it a caller has no way to learn what it may pass as host. The
// answer is list_hosts', narrowed by the same credential, so the two can never
// disagree about where a caller may go.
func (h *herdrMCPServer) registerMachineList() {
	closedWorld := false
	h.srv.AddTool(&mcp.Tool{
		Name:        herdrMachineListTool,
		Title:       "Machine List",
		Description: `List the hosts this endpoint can drive for you — the box lasso runs on ("local") plus its reachable ssh aliases, narrowed to your credential's scope; the same answer as /mcp's list_hosts. Pass a returned host as the "host" (or "machine") argument on any other tool. An entry in state "probing" or "timeout" is not down; call again shortly. Inputs: none.`,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_, out, err := listHostsTool(ctx, req, listHostsIn{})
		if err != nil {
			return herdrErrorResult(herdrMachineListTool, "", err), nil
		}
		payload := map[string]any{
			"machines": out.Hosts,
			"probing":  out.Probing,
			"note":     `Pass a host as the "host" argument on any tool; omit it for your own host. Workspace, tab, pane and agent IDs are scoped per host.`,
		}
		b, err := json.Marshal(payload)
		if err != nil {
			return herdrErrorResult(herdrMachineListTool, "", err), nil
		}
		var structured map[string]any
		_ = json.Unmarshal(b, &structured)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}, StructuredContent: structured}, nil
	})
}

// ---------------------------------------------------------------------------
// titles, annotations, descriptions (herdr-mcp's, so clients see the same)
// ---------------------------------------------------------------------------

func herdrToolTitle(method string) string {
	parts := strings.FieldsFunc(method, func(r rune) bool { return r == '.' || r == '_' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

func herdrAnnotations(method string) *mcp.ToolAnnotations {
	closedWorld := false
	readOnly := herdrIsReadOnly(method)
	a := &mcp.ToolAnnotations{ReadOnlyHint: readOnly, IdempotentHint: readOnly, OpenWorldHint: &closedWorld}
	if !readOnly {
		destructive := herdrIsDestructive(method)
		a.DestructiveHint = &destructive
	}
	return a
}

func herdrIsReadOnly(method string) bool {
	if method == "ping" || method == "session.snapshot" {
		return true
	}
	for _, suffix := range []string{
		".list", ".get", ".read", ".explain", ".info", ".current", ".snapshot",
		".export", ".wait", ".wait_for_output", ".neighbor", ".edges", ".process_info",
		".agent_manifests", ".action.list", ".log.list",
	} {
		if strings.HasSuffix(method, suffix) {
			return true
		}
	}
	return false
}

func herdrIsDestructive(method string) bool {
	if method == "server.stop" || method == "server.live_handoff" {
		return true
	}
	for _, suffix := range []string{
		".close", ".remove", ".unlink", ".uninstall", ".disable",
		".clear_agent_authority", ".release_agent",
	} {
		if strings.HasSuffix(method, suffix) {
			return true
		}
	}
	return false
}

// herdrToolDescription uses caller vocabulary rather than the socket
// protocol's; the fallback keeps a newly added herdr method distinguishable
// until it gets a curated line.
func herdrToolDescription(method string, input map[string]any) string {
	if d, ok := herdrCuratedDescriptions[method]; ok {
		return d + herdrInputSummary(input)
	}
	parts := strings.Split(method, ".")
	resource := strings.Join(parts[:len(parts)-1], " ")
	var action string
	switch parts[len(parts)-1] {
	case "list":
		action = "List " + resource + " records."
	case "get", "current", "info", "layout", "edges", "neighbor", "process_info", "explain":
		action = "Inspect " + resource + " state."
	case "create", "open", "install", "link", "enable", "focus", "move", "rename", "set", "apply", "swap", "split", "resize", "zoom":
		action = "Change " + resource + " state."
	case "read":
		action = "Read " + resource + " output or state."
	case "wait", "wait_for_output":
		action = "Wait for " + resource + " state or output."
	case "close", "remove", "unlink", "uninstall", "disable", "clear_agent_authority", "release_agent":
		action = "Remove or stop " + resource + " state."
	default:
		action = fmt.Sprintf("Operate Herdr %s.", strings.ReplaceAll(method, ".", " "))
	}
	return action + herdrInputSummary(input)
}

func herdrInputSummary(input map[string]any) string {
	props, _ := input["properties"].(map[string]any)
	if len(props) == 0 {
		return " Inputs: none."
	}
	required := map[string]bool{}
	if vs, ok := input["required"].([]any); ok {
		for _, v := range vs {
			if s, ok := v.(string); ok {
				required[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		if required[name] {
			names = append(names, name+" (required)")
		} else {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return " Inputs: " + strings.Join(names, ", ") + "."
}

var herdrCuratedDescriptions = map[string]string{
	"ping":                          "Check whether the active Herdr session socket is reachable and protocol-compatible.",
	"session.snapshot":              "Get one snapshot of all workspaces, tabs, panes, and recognized agents. Use this to discover stable IDs before changing terminal state.",
	"workspace.create":              "Create a terminal workspace with a root pane. The result contains workspace_id, tab_id, and root_pane.pane_id for follow-up calls.",
	"worktree.create":               "Create and open a Git worktree as a workspace. Set cwd to the source repository path; the result contains root_pane.pane_id for agent_start. repo and repo_path are accepted aliases for cwd.",
	"worktree.open":                 "Open an existing Git worktree as a workspace. Set cwd or path to locate it; the result contains root_pane.pane_id.",
	"agent.list":                    "List running coding agents and their live status (idle, working, blocked, done, unknown), name, kind, pane_id, workspace, and cwd. Use agent_read for output or agent_prompt to send work.",
	"agent.get":                     "Inspect one running agent by unique name or pane_id, including launch_pending and interactive_ready during startup.",
	"agent.read":                    "Read an agent's output or scrollback by agent name or pane_id. source must be visible, recent, recent_unwrapped, or detection; prefer recent_unwrapped for transcripts and logs.",
	"agent.explain":                 "Explain why Herdr did or did not recognize a terminal as an agent, including detection state and manifest details.",
	"agent.send_keys":               "Send logical key presses to a running agent by name or pane_id. Use for explicit interactive controls such as Escape or Ctrl+C, not normal task prompts.",
	"agent.start":                   "Start a supported coding-agent harness in an existing available shell pane. This does not create or split a pane: obtain pane_id from worktree_create, workspace_create, tab_create, or pane_split first. kind is the harness (for example claude, codex, omp); name is the unique agent name. agent and harness are accepted aliases for kind.",
	"agent.prompt":                  "Send a task prompt to a running, interactive agent by name or pane_id. text is the prompt body; prompt, message, and body are accepted aliases. Wait for agent_start to return interactive_ready before prompting.",
	"agent.wait":                    "Wait for a named running agent to reach idle, done, blocked, or another requested status. If the agent is still launching, waits until it becomes addressable before waiting for the requested status.",
	"pane.list":                     "List terminal panes with stable pane IDs, workspace, tab, working directory, and recognized agent state.",
	"pane.current":                  "Get the current terminal pane and its stable pane_id. Prefer explicit pane IDs rather than relying on UI focus for mutations.",
	"pane.split":                    "Split a terminal pane to create a new shell pane. The result contains the new pane_id; use it as agent_start.pane_id to launch an agent.",
	"pane.read":                     "Read a terminal pane's output or scrollback. source must be visible, recent, recent_unwrapped, or detection; prefer recent_unwrapped for logs and transcripts.",
	"pane.send_text":                "Send literal text to a terminal pane without pressing Enter. Use pane_send_keys or pane_send_input when terminal execution is intended.",
	"pane.send_keys":                "Send logical key presses to a terminal pane, including Enter, Escape, and Ctrl+C. Use agent_prompt instead for normal coding-agent tasks.",
	"pane.send_input":               "Send text and/or logical keys to a terminal pane in one call. This is raw terminal control and can interleave with an active agent turn.",
	"pane.wait_for_output":          "Wait until a terminal pane's selected output snapshot matches text or a regex. Use pane_read to inspect the matching output.",
	"events.wait":                   "Wait for one supported Herdr event, currently pane agent-status changes. Use it when coordinating state transitions across panes.",
	"notification.show":             "Show a desktop notification in the active Herdr session.",
	"server.agent_manifests":        "List installed agent-detection manifests and the harness kinds that agent_start can launch.",
	"server.reload_agent_manifests": "Reload agent-detection manifests after changing their configuration.",
	"pane.link.resolve":             "Resolve a link at a position in a terminal pane without opening it, reporting the target it would activate.",
	"pane.link.activate":            "Open the link at a position in a terminal pane, as a click would.",
	"workspace.list":                "List workspaces with stable workspace IDs, labels, and tab and pane counts. IDs are scoped to one host.",
	"tab.list":                      "List tabs with stable tab IDs and their workspace. IDs are scoped to one host.",
	"worktree.list":                 "List Git worktree workspaces with their branch, path, and workspace ID.",
	"command.invoke":                "Invoke a Herdr command by name, the same action a keybinding would trigger.",
	"plugin.list":                   "List installed Herdr plugins, their ids, and whether each is enabled.",
	"plugin.action.list":            "List the actions installed plugins expose, by plugin id.",
	"plugin.action.invoke":          "Invoke one plugin action by its fully qualified action id.",
}

// ---------------------------------------------------------------------------
// arguments: aliases and validation (herdr-mcp's arguments.go)
// ---------------------------------------------------------------------------

var herdrArgumentAliases = map[string]map[string]string{
	"worktree.create": {"repo": "cwd", "repo_path": "cwd"},
	"agent.prompt":    {"prompt": "text", "message": "text", "body": "text"},
	"agent.start":     {"agent": "kind", "harness": "kind"},
}

// herdrNormalizeArguments maps argument aliases onto herdr's names and checks
// the call against the tool's schema before it costs a round trip, naming the
// accepted parameters (and the closest one) when a caller guesses wrong —
// herdr's own "unknown field" leaves an agent guessing again.
func herdrNormalizeArguments(method string, input map[string]any, raw json.RawMessage) (map[string]any, []string, error) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	var notes []string
	for alias, canonical := range herdrArgumentAliases[method] {
		v, ok := args[alias]
		if !ok {
			continue
		}
		if _, set := args[canonical]; set {
			return nil, nil, fmt.Errorf("arguments %q and its alias %q cannot both be set", canonical, alias)
		}
		args[canonical] = v
		delete(args, alias)
		notes = append(notes, fmt.Sprintf("Mapped argument %q to %q.", alias, canonical))
	}
	sort.Strings(notes)
	if err := herdrValidateArguments(input, args); err != nil {
		return nil, nil, fmt.Errorf("invalid arguments for %s: %w", herdrToolName(method), err)
	}
	return args, notes, nil
}

func herdrValidateArguments(schema map[string]any, args map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	accepted := make([]string, 0, len(props))
	for name := range props {
		accepted = append(accepted, name)
	}
	sort.Strings(accepted)
	for name := range args {
		if _, ok := props[name]; !ok {
			msg := fmt.Sprintf("%q is not accepted (accepted: %s)", name, strings.Join(accepted, ", "))
			if s := herdrClosest(name, accepted); s != "" {
				msg += fmt.Sprintf("; did you mean %q?", s)
			}
			return fmt.Errorf("%s", msg)
		}
	}
	if req, ok := schema["required"].([]any); ok {
		for _, v := range req {
			name, _ := v.(string)
			if _, ok := args[name]; !ok {
				return fmt.Errorf("missing required %q (accepted: %s)", name, strings.Join(accepted, ", "))
			}
		}
	}
	defs, _ := schema["$defs"].(map[string]any)
	for name, v := range args {
		prop, _ := props[name].(map[string]any)
		if r, ok := prop["$ref"].(string); ok {
			prop, _ = defs[strings.TrimPrefix(r, "#/$defs/")].(map[string]any)
		}
		enum, ok := prop["enum"].([]any)
		if !ok {
			continue
		}
		allowed := false
		vals := make([]string, 0, len(enum))
		for _, e := range enum {
			allowed = allowed || e == v
			vals = append(vals, fmt.Sprintf("%q", e))
		}
		if !allowed {
			return fmt.Errorf("%q must be one of %s", name, strings.Join(vals, ", "))
		}
	}
	return nil
}

// herdrClosest is the accepted name nearest to a mistyped one, if any is near.
func herdrClosest(in string, candidates []string) string {
	best, bestD := "", int(^uint(0)>>1)
	for _, c := range candidates {
		if d := herdrLevenshtein(in, c); d < bestD {
			best, bestD = c, d
		}
	}
	if bestD > max(2, len(in)/2) {
		return ""
	}
	return best
}

func herdrLevenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for i := range prev {
		prev[i] = i
	}
	for i, ra := range a {
		cur[0] = i + 1
		for j, rb := range b {
			cost := 1
			if ra == rb {
				cost = 0
			}
			cur[j+1] = min(cur[j]+1, prev[j+1]+1, prev[j]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// ---------------------------------------------------------------------------
// agent readiness (herdr-mcp's agent.go)
// ---------------------------------------------------------------------------

const herdrAgentStartupTimeout = 30 * time.Second

// herdrWaitForStartedAgent holds agent_start's answer until the agent is
// interactive, so the caller's next agent_prompt is not typed into a harness
// still drawing its first screen. A start that stays launching still succeeds,
// with a note saying what to wait on.
func herdrWaitForStartedAgent(ctx context.Context, b Backend, startResult, params json.RawMessage) (json.RawMessage, string) {
	target := herdrStringArg(params, "name")
	if target == "" {
		return startResult, ""
	}
	agent, err := herdrWaitForAgentReady(ctx, b, target, herdrArgTimeout(params, herdrAgentStartupTimeout))
	if err != nil {
		return startResult, fmt.Sprintf("Agent %q started but is still launching. Use agent_wait with target %q before agent_prompt; its pane is available for raw terminal input.", target, target)
	}
	var result map[string]any
	if json.Unmarshal(startResult, &result) != nil {
		return startResult, ""
	}
	result["agent"] = agent
	updated, err := json.Marshal(result)
	if err != nil {
		return startResult, ""
	}
	return updated, ""
}

// herdrWaitThroughLaunch lets agent_wait on a just-started agent wait for it
// to become addressable rather than fail on a name herdr does not know yet.
func herdrWaitThroughLaunch(ctx context.Context, b Backend, params json.RawMessage) error {
	target := herdrStringArg(params, "target")
	if target == "" {
		return nil
	}
	launching, _, err := herdrLaunchingAgent(ctx, b, target)
	if err != nil || !launching {
		return nil
	}
	if _, err := herdrWaitForAgentReady(ctx, b, target, herdrArgTimeout(params, herdrAgentStartupTimeout)); err != nil {
		return fmt.Errorf("agent %q is still launching; %w", target, err)
	}
	return nil
}

// herdrEnrichAgentError explains an agent.* failure that is really "still
// launching", which herdr reports as a bare not-found.
func herdrEnrichAgentError(ctx context.Context, b Backend, params json.RawMessage, err error) error {
	target := herdrStringArg(params, "target")
	if target == "" {
		return err
	}
	launching, paneID, lerr := herdrLaunchingAgent(ctx, b, target)
	if lerr != nil || !launching {
		return err
	}
	return fmt.Errorf("%w; agent %q is still launching (launch_pending=true, pane_id=%q). Wait with agent_wait before prompting, or use pane_send_keys with that pane_id for raw terminal input", err, target, paneID)
}

func herdrWaitForAgentReady(ctx context.Context, b Backend, target string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		res, err := herdrCallWithin(ctx, b, "agent.get", map[string]any{"target": target}, herdrReadTimeout)
		if err == nil {
			var resp struct {
				Agent map[string]any `json:"agent"`
			}
			if json.Unmarshal(res, &resp) == nil && resp.Agent != nil {
				if ready, _ := resp.Agent["interactive_ready"].(bool); ready {
					return resp.Agent, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for interactive readiness after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func herdrLaunchingAgent(ctx context.Context, b Backend, target string) (bool, string, error) {
	res, err := herdrCallWithin(ctx, b, "agent.list", map[string]any{}, herdrReadTimeout)
	if err != nil {
		return false, "", err
	}
	var resp struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(res, &resp); err != nil {
		return false, "", err
	}
	for _, a := range resp.Agents {
		name, _ := a["name"].(string)
		paneID, _ := a["pane_id"].(string)
		if target != name && target != paneID {
			continue
		}
		launching, _ := a["launch_pending"].(bool)
		return launching, paneID, nil
	}
	return false, "", nil
}

func herdrStringArg(params json.RawMessage, name string) string {
	var m map[string]any
	if json.Unmarshal(params, &m) != nil {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

func herdrArgTimeout(params json.RawMessage, fallback time.Duration) time.Duration {
	var m map[string]any
	if json.Unmarshal(params, &m) != nil {
		return fallback
	}
	ms, _ := m["timeout_ms"].(float64)
	if ms <= 0 {
		return fallback
	}
	return time.Duration(ms) * time.Millisecond
}

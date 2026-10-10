package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The browser tools — lasso's shared browsers driven through Google's
// chrome-devtools-mcp, served as tools on lasso's own /mcp beside list_agents
// and the rest (see browser.go for the browsers themselves). An agent that has
// lasso's MCP server has the browser: nothing to install on its machine, no
// second server to add, lasso's own auth in front.
//
// chrome-devtools-mcp only speaks stdio, so lasso is a BRIDGE. It learns
// chrome-devtools-mcp's tool list once per process (a short probe child,
// refreshed by every real spawn and re-probed when the binary changes) and
// registers each tool on the shared /mcp server as browser_<name>, with an
// optional `browser` argument added. A call strips `browser` and forwards to
// the calling session's child for that browser, spawning it on the first call
// that needs it. So one MCP session holds up to one chrome-devtools-mcp child
// per browser it has used, and a session that never calls a browser tool holds
// none. Children are per session (not shared) because chrome-devtools-mcp
// keeps per-client state — the "selected page", console/network buffers,
// emulation — and per browser because one child can only dial one browser.
//
// The shared server is one *mcp.Server for every session, but every tool call
// carries its *mcp.ServerSession, which is what children are keyed by; the
// session's end is ServerSession.Wait returning.
//
// A child connects to lasso's OWN /cdp (/cdp/p/<id> for a browser other than
// the default), never to Chromium's loopback port: that address survives
// relaunches, launches the browser lazily, and its in-flight counter is what
// keeps the idle stop from firing under an agent that is still connected. It
// authenticates with an internal token (see internalCDPToken in cdpproxy.go)
// because an agent's credential never reaches lasso's own child.
//
// The browser tools are gated to /cdp's standard, which is stricter than the
// rest of /mcp (withBrowserToolGate, browserToolRefusal): a foreign Origin is
// refused, under UI_AUTH alone the call needs the UI_AUTH credentials, and
// under MCP_OAUTH the caller's reach must include lasso's own machine. A call
// that does not meet it is a tool error.
//
// Lifetime: a browser stopping (stop, relaunch, idle stop, crash) closes that
// browser's child in every session and nothing else — its CDP connection is
// dead, and the next call to that browser spawns a fresh one. A child that dies
// on its own is dropped the same way. The session ending (DELETE, shutdown)
// kills every child it holds, and so does browserMCPIdle without a browser
// call: /mcp sessions have no timeout of their own, so a client that went away
// without closing its session would otherwise keep its children forever.

// browserMCPIdle stops the children of a session that has made no browser
// call for this long (the session itself stays: it is /mcp's). The next call
// spawns a fresh child, as after a browser restart.
const browserMCPIdle = 30 * time.Minute

// browserMCPStartTimeout bounds spawning a child through its tools/list. node's
// cold start is a second or two; anything past this is a child that is stuck.
const browserMCPStartTimeout = 45 * time.Second

// browserMCPListWait is how long an /mcp initialize waits for a tool-list probe
// in flight before answering without the browser tools; the probe's result is
// announced with tools/list_changed when it lands.
const browserMCPListWait = 10 * time.Second

// browserMCPProbeRetry keeps a binary whose probe failed from being probed on
// every initialize.
const browserMCPProbeRetry = time.Minute

// browserMCPTerminate is how long the SDK's stdio close waits after closing the
// child's stdin (and again after SIGTERM) before escalating.
const browserMCPTerminate = 2 * time.Second

// browserMCPInstallHint is what every "not installed" answer tells the operator.
// There is deliberately no `npx chrome-devtools-mcp@latest` fallback: fetching an
// unpinned package from the registry at runtime, on the machine holding the
// browser's logged-in profile, is a supply-chain hole with lasso's name on it.
const browserMCPInstallHint = "install it on lasso's machine (`npm i -g chrome-devtools-mcp` or `mise use -g npm:chrome-devtools-mcp`), or set LASSO_BROWSER_MCP to its path"

// browserToolPrefix names every mirrored chrome-devtools-mcp tool on /mcp:
// click is browser_click, navigate_page is browser_navigate_page. lasso's own
// browser tools (list_browsers, open_browser_tab, …) do not start with it, so
// the prefix is the whole of what marks a tool as chrome-devtools-mcp's.
const browserToolPrefix = "browser_"

// browserToolParam is the argument every browser_* tool gains, and loses again
// before the call reaches a child.
const browserToolParam = "browser"

// browserToolParamDesc is that argument's description.
const browserToolParamDesc = "Which browser to run this in: its id or display name (list_browsers shows them). Omit for the default browser. Each browser is a separate Chromium (or a remote browser lasso dials) with its own cookies, logins and pages, so a pageId from one browser means nothing in another."

type browserMCPConfig struct {
	Binary    string // -browser-mcp / LASSO_BROWSER_MCP: a path or PATH name; "off" disables; "" = chrome-devtools-mcp
	ExtraArgs string // LASSO_BROWSER_MCP_ARGS, whitespace-split after lasso's own
	// Max is the most chrome-devtools-mcp children alive at once across every
	// session and browser (-browser-mcp-max / LASSO_BROWSER_MCP_MAX); <= 0 is
	// no limit, the default. The tool-list probe is not counted: it lives for a
	// second.
	Max int
	// ExtraEnv is appended to the child's minimal environment. A test seam (the
	// fake child is this test binary, told what to be by an env var); nothing in
	// production sets it.
	ExtraEnv []string
}

// browserMCPBridge serves the browser_* tools on lasso's /mcp.
type browserMCPBridge struct {
	cfg      browserMCPConfig
	lookPath func(string) (string, error)

	// cdpEndpoint is ws://<the address lasso bound>/cdp, set once the listener
	// is up (the route table is built before the bind).
	cdpEndpoint atomic.Value // string

	mu       sync.Mutex
	live     map[*mcp.ServerSession]*browserMCPSession // sessions that have called a browser tool
	children int                                       // counted children alive, across every session
	starting int                                       // slots reserved by spawns still in flight

	// tools is chrome-devtools-mcp's tool list, keyed by the binary it came
	// from; probeMu makes a burst of first initializes share one probe.
	tools   atomic.Pointer[browserMCPToolCache]
	probeMu sync.Mutex
	probes  atomic.Int64 // probe children spawned, for tests
	failKey string       // under probeMu: the binary whose last probe failed
	failAt  time.Time

	// regMu guards what is registered on srv: the names, and the list they
	// came from (so an unchanged list is not re-added, which would send every
	// session a tools/list_changed for nothing).
	regMu    sync.Mutex
	srv      *mcp.Server
	regNames map[string]bool
	regList  []*mcp.Tool
}

// browserMCPToolCache is one binary's tool list, already filtered to what
// Server.AddTool accepts. Registration makes copies; nothing mutates it.
type browserMCPToolCache struct {
	key   string
	tools []*mcp.Tool
}

// browserMCP is the process-wide bridge main wires up; nil reads as a feature
// that is not configured.
var browserMCP *browserMCPBridge

func newBrowserMCPBridge(cfg browserMCPConfig) *browserMCPBridge {
	b := &browserMCPBridge{cfg: cfg, lookPath: exec.LookPath, live: map[*mcp.ServerSession]*browserMCPSession{}}
	b.cdpEndpoint.Store("")
	return b
}

// setListenAddr records the address the main listener actually bound, which is
// where a child dials /cdp. An unspecified bind (0.0.0.0 / ::, which lasso
// refuses without auth and should never use) is dialed on loopback instead.
func (b *browserMCPBridge) setListenAddr(addr net.Addr) {
	if b == nil || addr == nil {
		return
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	b.cdpEndpoint.Store("ws://" + net.JoinHostPort(host, port) + "/cdp")
}

// resolve finds the chrome-devtools-mcp to run. ok=false carries a reason the
// Settings pane, /api/browser and shared_browser all show as-is.
func (b *browserMCPBridge) resolve() (bin, reason string, ok bool) {
	if b == nil {
		return "", "the browser tools are not configured on this lasso", false
	}
	e := strings.TrimSpace(b.cfg.Binary)
	if strings.EqualFold(e, "off") {
		return "", "the browser tools are disabled (LASSO_BROWSER_MCP=off / -browser-mcp off)", false
	}
	if e == "" {
		e = "chrome-devtools-mcp"
	}
	if strings.ContainsRune(e, '/') {
		if st, err := os.Stat(e); err != nil || st.IsDir() {
			return "", fmt.Sprintf("the configured chrome-devtools-mcp %q does not exist: %s", e, browserMCPInstallHint), false
		}
		return e, "", true
	}
	p, err := b.lookPath(e)
	if err != nil {
		if e == "chrome-devtools-mcp" {
			return "", "chrome-devtools-mcp is not installed on lasso's machine: " + browserMCPInstallHint, false
		}
		return "", fmt.Sprintf("the configured chrome-devtools-mcp %q is not in PATH: %s", e, browserMCPInstallHint), false
	}
	return p, "", true
}

// sessions is the number of sessions holding at least one child: the agents
// actually using a browser, not every client connected to /mcp.
func (b *browserMCPBridge) sessions() int {
	n := 0
	for _, s := range b.snapshot() {
		s.mu.Lock()
		for _, sl := range s.children {
			if sl.child != nil {
				n++
				break
			}
		}
		s.mu.Unlock()
	}
	return n
}

// liveChildren is the number of counted children alive.
func (b *browserMCPBridge) liveChildren() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.children
}

// ---------------------------------------------------------------------------
// registration on /mcp
// ---------------------------------------------------------------------------

// attach makes srv (lasso's shared /mcp server) the one the browser tools are
// registered on, and has every initialize there make sure they are current: a
// cheap check when the cached list is this binary's, a probe when it is not
// (chrome-devtools-mcp installed or upgraded since), and a removal when
// chrome-devtools-mcp is gone or switched off. A probe that fails is logged,
// never the initialize's error: /mcp's own tools do not depend on it.
func (b *browserMCPBridge) attach(srv *mcp.Server) {
	if b == nil || srv == nil {
		return
	}
	b.regMu.Lock()
	b.srv, b.regNames, b.regList = srv, map[string]bool{}, nil
	b.regMu.Unlock()
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				b.ensureTools(browserMCPListWait)
			}
			return next(ctx, method, req)
		}
	})
	// A list learned before this server existed (another attach, in tests).
	if c := b.tools.Load(); c != nil {
		b.register(c.tools)
	}
}

// ensureTools brings the registered browser tools in line with the binary
// that would run now, waiting up to wait for a probe it has to start. The
// probe itself runs detached, so a slow one still lands (and is announced with
// tools/list_changed) after the caller has moved on.
func (b *browserMCPBridge) ensureTools(wait time.Duration) {
	if b == nil {
		return
	}
	bin, _, ok := b.resolve()
	if !ok {
		b.register(nil)
		return
	}
	key := browserMCPBinKey(bin)
	if c := b.tools.Load(); c != nil && c.key == key {
		b.register(c.tools)
		return
	}
	b.probeMu.Lock()
	recent := b.failKey == key && time.Since(b.failAt) < browserMCPProbeRetry
	b.probeMu.Unlock()
	if recent {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), browserMCPStartTimeout)
		defer cancel()
		tools, err := b.toolList(ctx)
		if err != nil {
			log.Printf("browser-mcp: no browser tools on /mcp: %v", err)
			return
		}
		b.register(tools)
	}()
	select {
	case <-done:
	case <-time.After(wait):
		log.Printf("browser-mcp: chrome-devtools-mcp's tool list is taking longer than %s; the browser tools will be announced when it arrives", wait)
	}
}

// register makes the browser tools on /mcp exactly tools (nil = none): each
// added (or replaced) as browser_<name>, and any registered before that is no
// longer in the list removed. An unchanged list touches nothing.
func (b *browserMCPBridge) register(tools []*mcp.Tool) {
	b.regMu.Lock()
	defer b.regMu.Unlock()
	srv := b.srv
	if srv == nil {
		return
	}
	if sameToolSlice(tools, b.regList) && (len(tools) > 0 || len(b.regNames) == 0) {
		return
	}
	names := map[string]bool{}
	var raw []string
	for _, t := range tools {
		raw = append(raw, t.Name)
	}
	for _, t := range tools {
		bt, own := browserTool(t, raw)
		if own {
			log.Printf("browser-mcp: tool %q has a %q argument of its own; it is passed through and runs in the default browser", t.Name, browserToolParam)
		}
		srv.AddTool(bt, b.forward(t.Name, own))
		names[bt.Name] = true
	}
	var gone []string
	for n := range b.regNames {
		if !names[n] {
			gone = append(gone, n)
		}
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		srv.RemoveTools(gone...)
	}
	if len(tools) > 0 || len(gone) > 0 {
		log.Printf("browser-mcp: %d browser tool(s) on /mcp (%d removed)", len(names), len(gone))
	}
	b.regNames, b.regList = names, tools
}

// sameToolSlice is identity, not equality: learnTools hands back the cached
// slice itself when a list did not change.
func sameToolSlice(a, b []*mcp.Tool) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// ---------------------------------------------------------------------------
// the tool list: learned once, answered from memory
// ---------------------------------------------------------------------------

// browserMCPBinKey identifies the binary a tool list came from: its resolved
// path (a mise install dir names the version), size and mtime, so an upgrade
// in place reads as a different binary and is probed again. A shim whose
// target moves without the shim changing is caught by the refresh every real
// spawn does instead (learnTools).
func browserMCPBinKey(bin string) string {
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	st, err := os.Stat(real)
	if err != nil {
		return real
	}
	return fmt.Sprintf("%s|%d|%d", real, st.Size(), st.ModTime().UnixNano())
}

// toolList is chrome-devtools-mcp's tool list, from the cache when it was
// learned from this binary, else from a probe child that lists its tools and
// exits. chrome-devtools-mcp dials the browser only when a tool runs, so the
// probe starts no Chromium (measured on 1.10.1 with an unreachable
// --wsEndpoint: tools/list answers all 30 tools).
func (b *browserMCPBridge) toolList(ctx context.Context) ([]*mcp.Tool, error) {
	bin, reason, ok := b.resolve()
	if !ok {
		return nil, errors.New(reason)
	}
	key := browserMCPBinKey(bin)
	if c := b.tools.Load(); c != nil && c.key == key {
		return c.tools, nil
	}
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	if c := b.tools.Load(); c != nil && c.key == key { // a concurrent caller probed first
		return c.tools, nil
	}
	b.probes.Add(1)
	ch, tools, err := b.spawn(ctx, bin, defaultBrowserProfile)
	if err != nil {
		b.failKey, b.failAt = key, time.Now()
		return nil, err
	}
	ch.stop("tool-list probe done")
	b.failKey = ""
	return b.learnTools(key, tools), nil
}

// learnTools stores a tool list (filtered to what AddTool accepts) as the
// cache for a binary and returns it. A real spawn calls it too, so an upgrade
// the key could not see is picked up — and registered on /mcp, which tells
// every session with tools/list_changed.
func (b *browserMCPBridge) learnTools(key string, tools []*mcp.Tool) []*mcp.Tool {
	var ok []*mcp.Tool
	for _, t := range tools {
		if !mcpObjectSchema(t.InputSchema) {
			// Server.AddTool panics on a non-object input schema. A child that
			// ships one has a broken tool, not a broken bridge: skip it.
			log.Printf("browser-mcp: skipping tool %q: its input schema is not an object", t.Name)
			continue
		}
		c := *t
		if c.OutputSchema != nil && !mcpObjectSchema(c.OutputSchema) {
			c.OutputSchema = nil
		}
		ok = append(ok, &c)
	}
	prev := b.tools.Load()
	if prev != nil && sameTools(prev.tools, ok) {
		if prev.key != key {
			b.tools.Store(&browserMCPToolCache{key: key, tools: prev.tools})
		}
		return prev.tools
	}
	if prev != nil {
		log.Printf("browser-mcp: chrome-devtools-mcp's tool list changed (%d → %d tools)", len(prev.tools), len(ok))
	}
	b.tools.Store(&browserMCPToolCache{key: key, tools: ok})
	return ok
}

func sameTools(a, b []*mcp.Tool) bool {
	if len(a) != len(b) {
		return false
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// browserTool is the /mcp copy of one of chrome-devtools-mcp's tools: named
// browser_<name>, its descriptions' mentions of its sibling tools renamed to
// match (only names with an underscore, like list_pages or take_snapshot: a
// bare "click" or "fill" is an English word as often as a tool), and the
// `browser` argument added. own reports a tool that already has a `browser`
// argument of its own, which is then passed through untouched and never
// routed. The schema is cloned, so AddTool and the SDK get a value nobody else
// holds.
func browserTool(t *mcp.Tool, siblings []string) (tool *mcp.Tool, own bool) {
	c := *t
	c.Name = browserToolPrefix + t.Name
	re := browserToolNameRE(siblings)
	c.Description = renameBrowserTools(t.Description, re)
	if c.Title != "" {
		c.Title = "Browser: " + c.Title
	}
	var schema map[string]any
	raw, _ := json.Marshal(t.InputSchema)
	_ = json.Unmarshal(raw, &schema)
	renameInDescriptions(schema, re)
	props, _ := schema["properties"].(map[string]any)
	if _, has := props[browserToolParam]; has {
		own = true
	}
	if !own {
		if props == nil {
			props = map[string]any{}
			schema["properties"] = props
		}
		props[browserToolParam] = map[string]any{"type": "string", "description": browserToolParamDesc}
		if pid, ok := props["pageId"].(map[string]any); ok {
			d, _ := pid["description"].(string)
			pid["description"] = strings.TrimSpace(d + " A pageId belongs to ONE browser: pass the same `browser` as the browser_list_pages/browser_new_page call it came from.")
		}
	}
	c.InputSchema = schema
	return &c, own
}

// browserToolNameRE matches the sibling tool names worth renaming in text.
func browserToolNameRE(siblings []string) *regexp.Regexp {
	var alts []string
	for _, n := range siblings {
		if strings.Contains(n, "_") {
			alts = append(alts, regexp.QuoteMeta(n))
		}
	}
	if len(alts) == 0 {
		return nil
	}
	// Longest first, so take_snapshot is never matched as a prefix of a longer
	// name. \b on both sides: "browser_list_pages" has no boundary before
	// list_pages ('_' is a word character), so nothing is prefixed twice.
	sort.Slice(alts, func(i, j int) bool { return len(alts[i]) > len(alts[j]) })
	return regexp.MustCompile(`\b(` + strings.Join(alts, "|") + `)\b`)
}

func renameBrowserTools(s string, re *regexp.Regexp) string {
	if re == nil || s == "" {
		return s
	}
	return re.ReplaceAllString(s, browserToolPrefix+"$1")
}

// renameInDescriptions renames sibling tools in every "description" string of
// a JSON schema, at any depth.
func renameInDescriptions(v any, re *regexp.Regexp) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "description" {
				x[k] = renameBrowserTools(s, re)
				continue
			}
			renameInDescriptions(e, re)
		}
	case []any:
		for _, e := range x {
			renameInDescriptions(e, re)
		}
	}
}

// ---------------------------------------------------------------------------
// the gate: /cdp's standard, per call
// ---------------------------------------------------------------------------

// The browser tools front /cdp — the child a call reaches dials it on lasso's
// internal token — so a call must meet /cdp's standard, which is stricter than
// /mcp's: /mcp is open under UI_AUTH alone and takes requests with any Origin.
// The verdict needs the HTTP request (its Origin, its Host, its basic
// credentials), which a tool handler does not get, so withBrowserToolGate
// reaches it on every /mcp request and stamps it into a header the handler
// does get (the SDK copies the request's headers onto every call's Extra).

// browserGateHeader carries browserGateToken when the request meets the
// standard; browserRefusalHeader carries why it does not.
const (
	browserGateHeader    = "X-Lasso-Browser-Gate"
	browserRefusalHeader = "X-Lasso-Browser-Refusal"
)

// browserGateToken is the per-process value that says "this request passed
// withBrowserToolGate". Both headers are set or deleted on every request, so a
// client cannot supply its own, and the value is random so even a path that
// skipped the wrapper could not be talked through by a forged one.
var browserGateToken = newInternalCDPToken()

// withBrowserToolGate stamps the browser-tool verdict onto every /mcp request.
// It sits inside withMCPAuth, so under MCP_OAUTH an unauthenticated request
// never reaches it.
func withBrowserToolGate(next http.Handler, user, pass string, hasAuth bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(browserGateHeader)
		r.Header.Del(browserRefusalHeader)
		if why := browserRequestRefusal(r, user, pass, hasAuth); why != "" {
			r.Header.Set(browserRefusalHeader, why)
		} else {
			r.Header.Set(browserGateHeader, browserGateToken)
		}
		next.ServeHTTP(w, r)
	})
}

// browserRequestRefusal is /cdp's rule applied to an /mcp request:
//
//   - the Origin guard first: no Origin (a CLI or agent's MCP client) or
//     lasso's own origin only (cdpOriginAllowed), so no web page a user visits
//     can drive the browser through a loopback lasso's /mcp.
//   - MCP_OAUTH set: what /mcp accepts (withMCPAuth has already required a
//     lasso bearer token or the UI_AUTH basic credentials); the token's reach
//     is checked per call (requireLocalBrowser), since TokenInfo is a call's.
//   - only UI_AUTH set: its basic credentials, which /mcp itself does not ask
//     for in this configuration and /cdp does.
//   - neither: open, /mcp's and /cdp's trust model.
//
// There is no same-origin-page allowance like /cdp's: no page of lasso's speaks
// MCP. Cloudflare Access (gate.wrap) still fronts all of it.
func browserRequestRefusal(r *http.Request, user, pass string, hasAuth bool) string {
	if !cdpOriginAllowed(r) {
		return "the browser tools refuse a cross-origin request: a web page cannot drive lasso's browsers"
	}
	if oauthCfg.Enabled || !hasAuth {
		return ""
	}
	u, p, ok := r.BasicAuth()
	if ok && subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1 &&
		subtle.ConstantTimeCompare([]byte(p), []byte(pass)) == 1 {
		return ""
	}
	return "the browser tools need lasso's UI_AUTH credentials, which /mcp's other tools do not: send them as HTTP basic auth (an Authorization: Basic header) on this MCP connection"
}

// browserToolRefusal is a browser tool call's verdict: the request's, as
// withBrowserToolGate stamped it, then the caller's reach (requireLocalBrowser:
// the browsers run on lasso's own machine). "" = allowed.
func browserToolRefusal(req *mcp.CallToolRequest) string {
	var h http.Header
	if req != nil && req.Extra != nil {
		h = req.Extra.Header
	}
	if h == nil || subtle.ConstantTimeCompare([]byte(h.Get(browserGateHeader)), []byte(browserGateToken)) != 1 {
		if h != nil && h.Get(browserRefusalHeader) != "" {
			return h.Get(browserRefusalHeader)
		}
		return "the browser tools are served only over lasso's /mcp HTTP endpoint"
	}
	if err := requireLocalBrowser(req); err != nil {
		return err.Error()
	}
	return ""
}

// ---------------------------------------------------------------------------
// sessions and their children
// ---------------------------------------------------------------------------

// browserMCPChild is one chrome-devtools-mcp process, dialing one browser.
type browserMCPChild struct {
	b       *browserMCPBridge
	profile string
	client  *mcp.ClientSession
	cmd     *exec.Cmd
	pid     int
	counted bool // holds one of the bridge's children slots
	once    sync.Once
}

// stop ends the child, once: the SDK's stdio close (stdin, SIGTERM, SIGKILL,
// browserMCPTerminate apart), then a SIGKILL to its process group and a reap.
func (c *browserMCPChild) stop(why string) {
	c.once.Do(func() {
		_ = c.client.Close()
		reapBrowserMCPChild(c.cmd)
		if c.counted {
			c.b.mu.Lock()
			c.b.children--
			c.b.mu.Unlock()
		}
		log.Printf("browser-mcp: child pid %d for browser %q stopped (%s)", c.pid, c.profile, why)
	})
}

// browserMCPSlot is a session's child for one browser: spawning until ready
// closes, then either child or err.
type browserMCPSlot struct {
	ready chan struct{}
	child *browserMCPChild
	err   error
}

// browserMCPSession is one /mcp session's browser state: its children.
type browserMCPSession struct {
	b  *browserMCPBridge
	ss *mcp.ServerSession

	mu       sync.Mutex
	closed   bool
	inflight int                        // browser calls running now
	lastUsed time.Time                  // the last browser call's start or end
	children map[string]*browserMCPSlot // by browser id
}

// sessionFor is ss's browser state, made on its first browser call. The
// session ending (DELETE, shutdown) stops its children.
func (b *browserMCPBridge) sessionFor(ss *mcp.ServerSession) (*browserMCPSession, error) {
	if ss == nil {
		return nil, errors.New("the browser tools need an MCP session")
	}
	b.mu.Lock()
	s := b.live[ss]
	if s != nil {
		b.mu.Unlock()
		return s, nil
	}
	s = &browserMCPSession{b: b, ss: ss, lastUsed: time.Now(), children: map[string]*browserMCPSlot{}}
	b.live[ss] = s
	n := len(b.live)
	b.mu.Unlock()
	log.Printf("browser-mcp: session %s made its first browser call (%d sessions)", ss.ID(), n)
	go func() { _ = ss.Wait(); s.end("session ended") }()
	return s, nil
}

// use brackets a browser call, for the idle reaper.
func (s *browserMCPSession) use(delta int) {
	s.mu.Lock()
	s.inflight += delta
	s.lastUsed = time.Now()
	s.mu.Unlock()
}

// forward is a browser tool's handler: it checks the gate, picks the browser,
// strips the `browser` argument, and hands the call to this session's child
// for that browser as-is (name and raw arguments), returning its result as-is
// — Content (images too), StructuredContent, IsError and _meta — so the bridge
// adds nothing a client could tell apart from chrome-devtools-mcp's own answer.
func (b *browserMCPBridge) forward(name string, own bool) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if why := browserToolRefusal(req); why != "" {
			return browserMCPToolError(why), nil
		}
		browser, args, refusal := browserRoute(req.Params.Arguments, own)
		if refusal != "" {
			return browserMCPToolError(refusal), nil
		}
		s, err := b.sessionFor(req.Session)
		if err != nil {
			return browserMCPToolError(err.Error()), nil
		}
		s.use(1)
		defer s.use(-1)
		child, err := s.child(ctx, browser)
		if err != nil {
			return browserMCPToolError(err.Error()), nil
		}
		p := &mcp.CallToolParams{Name: name, Meta: req.Params.Meta}
		if len(args) > 0 {
			p.Arguments = args
		}
		return child.client.CallTool(ctx, p)
	}
}

func browserMCPToolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// browserRoute decides which browser a call runs in and strips `browser` from
// its arguments. Browsers are resolved NOW, so one created after the session
// started works and one deleted since is refused. An unknown browser is a
// refusal naming the current ones — never a quiet fallback to another, which
// would act in the wrong browser with the wrong logins.
func browserRoute(raw json.RawMessage, own bool) (browser string, args json.RawMessage, refusal string) {
	if own || len(raw) == 0 {
		return defaultBrowserProfile, raw, ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return defaultBrowserProfile, raw, "" // not an object: the child's to refuse
	}
	v, has := m[browserToolParam]
	if !has {
		return defaultBrowserProfile, raw, ""
	}
	delete(m, browserToolParam)
	args, _ = json.Marshal(m)
	var name string
	if string(v) != "null" {
		if err := json.Unmarshal(v, &name); err != nil {
			return "", nil, "`browser` must be a string: a browser's id or display name"
		}
	}
	if strings.TrimSpace(name) == "" {
		return defaultBrowserProfile, args, ""
	}
	id, err := resolveProfile(name)
	if err != nil {
		return "", nil, err.Error()
	}
	return id, args, ""
}

// child is this session's child for a browser, spawning it on first use.
// Concurrent calls for one browser share one spawn. The spawn runs detached
// from the caller, so a call that gives up still leaves the child ready for
// the next; a failed spawn is forgotten, so the next call tries again.
func (s *browserMCPSession) child(ctx context.Context, profile string) (*browserMCPChild, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("this MCP session has ended; reconnect to start a new one")
	}
	sl := s.children[profile]
	if sl == nil {
		sl = &browserMCPSlot{ready: make(chan struct{})}
		s.children[profile] = sl
		go s.spawnInto(sl, profile)
	}
	s.mu.Unlock()
	select {
	case <-sl.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if sl.err != nil {
		return nil, sl.err
	}
	return sl.child, nil
}

func (s *browserMCPSession) spawnInto(sl *browserMCPSlot, profile string) {
	b := s.b
	fail := func(err error) {
		s.mu.Lock()
		if s.children[profile] == sl {
			delete(s.children, profile)
		}
		sl.err = err
		s.mu.Unlock()
		close(sl.ready)
	}
	if profile != defaultBrowserProfile {
		// The default browser always exists; another may have been deleted
		// between the call's resolve and here.
		if _, err := browserFor(profile); err != nil {
			fail(err)
			return
		}
	}
	bin, reason, ok := b.resolve()
	if !ok {
		fail(errors.New(reason))
		return
	}
	if err := b.reserve(); err != nil {
		fail(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserMCPStartTimeout)
	defer cancel()
	ch, tools, err := b.spawn(ctx, bin, profile)
	if err != nil {
		b.unreserve()
		fail(err)
		return
	}
	b.mu.Lock()
	b.starting--
	b.children++
	n := b.children
	b.mu.Unlock()
	ch.counted = true
	b.register(b.learnTools(browserMCPBinKey(bin), tools))

	s.mu.Lock()
	if s.closed || s.children[profile] != sl {
		s.mu.Unlock()
		ch.stop("session ended while it was starting")
		fail(errors.New("this MCP session has ended; reconnect to start a new one"))
		return
	}
	sl.child = ch
	s.mu.Unlock()
	close(sl.ready)
	limit := "no limit"
	if b.cfg.Max > 0 {
		limit = fmt.Sprintf("limit %d", b.cfg.Max)
	}
	log.Printf("browser-mcp: child pid %d started for browser %q (%d live, %s)", ch.pid, profile, n, limit)

	// A child that dies on its own (a crash, an OOM kill) is dropped; the next
	// call to its browser spawns a fresh one. The session is untouched.
	go func() {
		_ = ch.client.Wait()
		s.drop(profile, sl, "child exited")
	}()
}

// drop forgets a browser's child (if it is still that slot's) and stops it.
func (s *browserMCPSession) drop(profile string, sl *browserMCPSlot, why string) {
	s.mu.Lock()
	if s.children[profile] == sl {
		delete(s.children, profile)
	}
	s.mu.Unlock()
	if sl.child != nil {
		sl.child.stop(why)
	}
}

// end closes the session's browser side once: every child stopped, the
// session unregistered. A spawn still in flight sees closed and stops its own.
func (s *browserMCPSession) end(why string) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.closed = true
	kids := s.takeChildrenLocked()
	s.mu.Unlock()

	s.b.mu.Lock()
	if s.b.live[s.ss] == s {
		delete(s.b.live, s.ss)
	}
	s.b.mu.Unlock()
	stopChildren(kids, why)
	return true
}

func (s *browserMCPSession) takeChildrenLocked() []*browserMCPChild {
	var kids []*browserMCPChild
	for _, sl := range s.children {
		if sl.child != nil {
			kids = append(kids, sl.child)
		}
	}
	s.children = map[string]*browserMCPSlot{}
	return kids
}

func stopChildren(kids []*browserMCPChild, why string) {
	var wg sync.WaitGroup
	for _, c := range kids {
		wg.Add(1)
		go func() { defer wg.Done(); c.stop(why) }()
	}
	wg.Wait()
}

// run stops idle sessions' children until ctx ends.
func (b *browserMCPBridge) run(ctx context.Context) {
	if b == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			b.reapIdle(now)
		}
	}
}

// reapIdle stops the children of every session with no browser call running
// and none for browserMCPIdle. The session's entry stays (it is still an /mcp
// session); its next browser call spawns afresh.
func (b *browserMCPBridge) reapIdle(now time.Time) {
	for _, s := range b.snapshot() {
		s.mu.Lock()
		var kids []*browserMCPChild
		if s.inflight == 0 && now.Sub(s.lastUsed) >= browserMCPIdle {
			kids = s.takeChildrenLocked()
		}
		s.mu.Unlock()
		if len(kids) > 0 {
			log.Printf("browser-mcp: session %s made no browser call for %s: stopping %d child(ren)", s.ss.ID(), browserMCPIdle, len(kids))
			stopChildren(kids, "session idle")
		}
	}
}

// spawn starts a chrome-devtools-mcp dialing a browser's /cdp and lists its
// tools. The caller owns the child (and, for a counted one, the slot).
func (b *browserMCPBridge) spawn(ctx context.Context, bin, profile string) (*browserMCPChild, []*mcp.Tool, error) {
	endpoint, _ := b.cdpEndpoint.Load().(string)
	if endpoint == "" {
		return nil, nil, errors.New("lasso is not listening yet; retry in a moment")
	}
	if profile != defaultBrowserProfile {
		// cdpEndpoint is ws://<addr>/cdp; the browser's own is one level down.
		endpoint += strings.TrimPrefix(cdpPathFor(profile), "/cdp")
	}

	cmd := exec.Command(bin, browserMCPArgs(endpoint, internalCDPToken, b.cfg.ExtraArgs)...)
	cmd.Env = append(browserMCPEnv(os.Environ()), b.cfg.ExtraEnv...)
	cmd.Dir = os.TempDir()
	tail := &tailBuffer{max: 8 << 10}
	logw := &browserMCPStderr{tail: tail}
	cmd.Stderr = logw
	// Its own process group, so the stop takes anything it spawned with it, and
	// (linux) Pdeathsig so a kill -9 of lasso does not leave node running.
	cmd.SysProcAttr = browserSysProcAttr()

	client := mcp.NewClient(&mcp.Implementation{Name: "lasso", Version: lassoSemver}, nil)
	cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: browserMCPTerminate}, nil)
	if err != nil {
		reapBrowserMCPChild(cmd)
		return nil, nil, browserMCPStartFailure(bin, cmd, err, tail)
	}
	pid := cmd.Process.Pid
	logw.setPID(pid)
	ch := &browserMCPChild{b: b, profile: profile, client: cs, cmd: cmd, pid: pid}

	var tools []*mcp.Tool
	for t, err := range cs.Tools(ctx, nil) { // follows nextCursor pagination
		if err != nil {
			ch.stop("tools/list failed")
			return nil, nil, fmt.Errorf("chrome-devtools-mcp (%s) started but tools/list failed: %v%s", bin, err, browserMCPTail(tail))
		}
		tools = append(tools, t)
	}
	return ch, tools, nil
}

// closeAll stops every session's children and waits. Called at shutdown, so
// lasso never exits ahead of a child; the /mcp sessions themselves are left to
// the server's shutdown.
func (b *browserMCPBridge) closeAll(why string) {
	sessions := b.snapshot()
	if len(sessions) == 0 {
		return
	}
	log.Printf("browser-mcp: stopping the children of %d session(s): %s", len(sessions), why)
	var wg sync.WaitGroup
	for _, s := range sessions {
		wg.Add(1)
		go func() { defer wg.Done(); s.end(why) }()
	}
	wg.Wait()
}

func (b *browserMCPBridge) snapshot() []*browserMCPSession {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*browserMCPSession, 0, len(b.live))
	for _, s := range b.live {
		out = append(out, s)
	}
	return out
}

// browserStopped is the default browser's stop hook (browserManager.onStop).
func (b *browserMCPBridge) browserStopped(why string) {
	b.browserStoppedFor(defaultBrowserProfile, why)
}

// browserStoppedFor stops every session's child for one browser, which just
// went away: their CDP connections belong to the process that ended. The
// sessions stay, and so do their children for other browsers; the next call to
// this browser spawns a child that dials the next one. The set is taken NOW,
// synchronously — a child that connects after this moment is on the next
// browser and must survive — and stopped in the background, since the hook
// runs with the browser's launch lock held. A spawn still in flight is left
// alone: chrome-devtools-mcp dials CDP only when a tool runs, which is after
// this stop, so it reaches the next browser.
func (b *browserMCPBridge) browserStoppedFor(profile, why string) {
	var kids []*browserMCPChild
	for _, s := range b.snapshot() {
		s.mu.Lock()
		if sl := s.children[profile]; sl != nil && sl.child != nil {
			delete(s.children, profile)
			kids = append(kids, sl.child)
		}
		s.mu.Unlock()
	}
	if len(kids) > 0 {
		log.Printf("browser-mcp: browser %q stopped (%s): stopping %d child(ren)", profile, why, len(kids))
		go stopChildren(kids, "browser "+profile+" stopped ("+why+")")
	}
}

// reserve takes a child slot under the opt-in limit. With no limit it still
// counts, so the log and status can say how many are alive.
func (b *browserMCPBridge) reserve() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.Max > 0 && b.children+b.starting >= b.cfg.Max {
		return fmt.Errorf("lasso's browser tools are at their limit of %d chrome-devtools-mcp processes (LASSO_BROWSER_MCP_MAX / -browser-mcp-max; 0 = no limit): an agent's MCP session holds one per browser it has used until the session ends or goes %s without a browser call — close an idle agent's session, or raise or remove the limit", b.cfg.Max, browserMCPIdle)
	}
	b.starting++
	return nil
}

func (b *browserMCPBridge) unreserve() {
	b.mu.Lock()
	b.starting--
	b.mu.Unlock()
}

// browserMCPArgs is the child's command line. lasso's own flags come first so
// LASSO_BROWSER_MCP_ARGS can add to them (a later duplicate wins in yargs).
//
//   - --wsEndpoint is lasso's own /cdp (see the file comment), --wsHeaders the
//     internal token that lets it through. The token rides argv, which any
//     process on lasso's machine can read from /proc/<pid>/cmdline:
//     chrome-devtools-mcp takes websocket headers from nowhere else (no env
//     var, no file). Measured, the window is short — it sets process.title,
//     after which its cmdline reads just "chrome-devtools-mcp" — but that is
//     its behavior, not a guarantee, so treat the token as readable by other
//     users of lasso's machine, who could equally reach a loopback lasso's
//     open /cdp. It is never logged and dies with lasso.
//   - usage statistics and CrUX off: the URLs an agent visits are the human's
//     business, not a telemetry endpoint's.
//   - screenshots as JPEG q80 bounded to 1280px: the browser renders at
//     -browser-scale (2 by default), so an unbounded PNG screenshot is ~4x the
//     pixels — and image tokens scale with pixels — for no gain to a model.
func browserMCPArgs(endpoint, token, extra string) []string {
	hdr, _ := json.Marshal(map[string]string{internalCDPHeader: token})
	args := []string{
		"--wsEndpoint", endpoint,
		"--wsHeaders", string(hdr),
		"--no-usage-statistics",
		"--no-performance-crux",
		"--screenshotFormat", "jpeg",
		"--screenshotQuality", "80",
		"--screenshotMaxWidth", "1280",
		"--screenshotMaxHeight", "1280",
	}
	return append(args, strings.Fields(extra)...)
}

// browserMCPEnv is the child's environment: an allowlist, not lasso's minus a
// denylist. The child needs to find node (PATH, and mise's shims need HOME and
// its own dirs) and nothing else; lasso's environment carries UI_AUTH,
// MCP_OAUTH, LASSO_MCP_TOKEN and whatever the operator's shell exported, none
// of which a process driving arbitrary web pages should hold. MISE_* is limited
// to the directory settings, since MISE_GITHUB_TOKEN is a MISE_ variable too.
func browserMCPEnv(env []string) []string {
	// Belt and braces: its own opt-out, in case a future version reads only the
	// environment. CI=1 would do it too, but also changes other behavior.
	return append(minimalChildEnv(env), "CHROME_DEVTOOLS_MCP_NO_USAGE_STATISTICS=1")
}

// minimalChildEnv is the allowlist every child lasso runs on the host starts
// from — chrome-devtools-mcp above, and a trusted plugin's MCP server
// (pluginsandbox.go), which gets its manifest env and secrets on top.
func minimalChildEnv(env []string) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true, "TMPDIR": true,
		"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true,
		"MISE_DATA_DIR": true, "MISE_CONFIG_DIR": true, "MISE_CACHE_DIR": true, "MISE_STATE_DIR": true,
	}
	out := []string{}
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if keep[k] {
			out = append(out, kv)
		}
	}
	return out
}

// reapBrowserMCPChild makes sure nothing of a child is left: SIGKILL to its
// process group (anything it spawned), and a Wait for the leader when the SDK's
// close did not get to reap it — Client.Connect returns some failures (an
// unsupported protocol version) without closing its transport, and that would
// otherwise be a zombie.
func reapBrowserMCPChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if pid := cmd.Process.Pid; pid > 1 { // never kill(-0)/kill(-1): see browserManager.kill
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if cmd.ProcessState == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// browserMCPStartFailure names why a child did not come up: the binary could
// not be executed, or it exited (with its status and the tail of its stderr,
// which is where node prints the actual problem), or it did not answer.
func browserMCPStartFailure(bin string, cmd *exec.Cmd, err error, tail *tailBuffer) error {
	var why string
	switch {
	case cmd.Process == nil:
		why = fmt.Sprintf("could not be started: %v", err)
	case cmd.ProcessState != nil:
		why = fmt.Sprintf("exited during startup (%s): %v", cmd.ProcessState, err)
	default:
		why = fmt.Sprintf("did not complete the MCP handshake: %v", err)
	}
	return fmt.Errorf("chrome-devtools-mcp (%s) %s%s", bin, why, browserMCPTail(tail))
}

func browserMCPTail(t *tailBuffer) string {
	s := t.String()
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return "; stderr: " + clipLine(strings.Join(lines, " | "), 800)
}

// mcpObjectSchema reports whether a schema (as the client decoded it — a map,
// or anything that marshals to one) is a JSON object schema.
func mcpObjectSchema(s any) bool {
	if s == nil {
		return false
	}
	b, err := json.Marshal(s)
	if err != nil {
		return false
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	return m["type"] == "object"
}

// browserMCPStderr sends a child's stderr to lasso's log, a line at a time and
// at a bounded rate — chrome-devtools-mcp prints a disclaimer banner on every
// start and can be chatty on errors, eight of them at once more so — while
// keeping the tail for a startup failure's explanation.
type browserMCPStderr struct {
	tail *tailBuffer
	// label prefixes each logged line ("browser-mcp" when empty). Plugin
	// servers (pluginmcp.go) reuse this writer under their own name.
	label string

	mu      sync.Mutex
	pid     int
	partial []byte
	window  time.Time
	lines   int
	dropped int
}

const browserMCPLogPerMinute = 30

func (w *browserMCPStderr) name() string {
	if w.label != "" {
		return w.label
	}
	return "browser-mcp"
}

func (w *browserMCPStderr) setPID(pid int) {
	w.mu.Lock()
	w.pid = pid
	w.mu.Unlock()
}

func (w *browserMCPStderr) Write(p []byte) (int, error) {
	_, _ = w.tail.Write(p)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.partial = append(w.partial, p...)
	for {
		i := strings.IndexByte(string(w.partial), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(w.partial[:i]))
		w.partial = w.partial[i+1:]
		if line == "" {
			continue
		}
		now := time.Now()
		if now.Sub(w.window) >= time.Minute {
			if w.dropped > 0 {
				log.Printf("%s[%d]: (%d more stderr line(s) not logged)", w.name(), w.pid, w.dropped)
			}
			w.window, w.lines, w.dropped = now, 0, 0
		}
		if w.lines >= browserMCPLogPerMinute {
			w.dropped++
			continue
		}
		w.lines++
		who := "starting" // the pid is only known once Connect has returned
		if w.pid > 0 {
			who = fmt.Sprint(w.pid)
		}
		log.Printf("%s[%s]: %s", w.name(), who, clipLine(line, 400))
	}
	if len(w.partial) > 4096 { // a line that never ends is not worth buffering
		w.partial = w.partial[:0]
	}
	return len(p), nil
}

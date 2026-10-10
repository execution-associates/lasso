package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A plugin's MCP server, mirrored onto lasso's /mcp.
//
// Each enabled plugin with an mcp section gets ONE long-lived child — not one
// per session like the browser tools' chrome-devtools-mcp, because a plugin's
// tools are stateless as far as lasso knows: they sit on the shared server beside create_agent, every session sees
// the same list, and the SDK announces additions and removals with
// tools/list_changed. lasso connects an MCP client to the child, lists its
// tools, and registers each as `<plugin>__<tool>` with a handler that forwards
// the raw arguments and hands the child's CallToolResult back untouched — the
// browsermcp.go pattern, for the same reason: lasso adds nothing a client could
// tell apart from talking to the plugin directly.
//
// How the child runs is a pluginRunner: in an isb sandbox (pluginsandbox.go:
// a container by default, a VM when the operator asks) or on the host (a
// plugin the operator marked trusted). Either way this file sees a stream to
// speak MCP over, a channel that closes when the child dies, and a stop.
//
// Lifecycle: a child that dies has its tools removed and is restarted with
// backoff (pluginBackoffMin doubling to pluginBackoffMax), its tools re-added
// once it answers again. A server that cannot start for a reason retrying will
// not fix — no usable isb on this machine, isb serve down, a secret that did
// not resolve — goes
// `unavailable` and waits for the operator (Restart, or a reload).

var (
	pluginBackoffMin = time.Second
	pluginBackoffMax = 60 * time.Second
	// pluginStartTimeout bounds a launch through its tools/list. A container
	// is up in seconds; the case this is sized for is a first start that
	// downloads its image, or a VM that boots a kernel and its agent (isb
	// gives a VM 300s to get ready).
	pluginStartTimeout = 10 * time.Minute
	// pluginConnectAttempt bounds one MCP initialize. A stdio child gets one
	// attempt; a failed initialize is a failed launch.
	pluginConnectAttempt = 60 * time.Second
)

// No liveness ping: a dead server is seen without one. A trusted child is
// lasso's own process, and a sandboxed one is reached through `isb exec`,
// which exits within milliseconds of the server inside dying (measured on
// isb 1.0.0, in a container and in a VM), closing the session and proc.done().

// pluginLaunch is everything a runner needs to start one plugin's server.
type pluginLaunch struct {
	Name string
	Dir  string
	// DataDir is the plugin's one writable place on the host
	// (<lassoDir>/plugin-data/<name>), mounted at /data in a sandbox and named
	// by LASSO_PLUGIN_DATA either way. "" = none (tests).
	DataDir string
	MCP     *pluginMCPSpec
	Secrets map[string]string // resolved values; never logged
}

type pluginRunner interface {
	sandboxed() bool
	// start launches the child. An error wrapping errPluginUnavailable is not
	// retried until the operator acts.
	start(ctx context.Context, l pluginLaunch, logw io.Writer) (pluginProc, error)
}

// pluginStream is one MCP connection to a child.
type pluginStream struct {
	r io.ReadCloser
	w io.WriteCloser
}

type pluginProc interface {
	// connect returns a stream to the child's MCP server. May be called again
	// after a failed initialize; a runner that cannot redial says so.
	connect(ctx context.Context) (pluginStream, error)
	// done closes when the child process has exited.
	done() <-chan struct{}
	// stop ends the child and everything it spawned, synchronously.
	stop()
}

var (
	errPluginUnavailable = errors.New("unavailable")
	errNotPluginTool     = errors.New("not this plugin's tool")
	errPluginNotRunning  = errors.New("plugin MCP server not running")
)

// unavailable wraps a reason as a terminal launch failure.
func unavailable(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errPluginUnavailable, fmt.Sprintf(format, a...))
}

// pluginSecretLookup resolves one approved secret's value: lasso's own
// environment first, then the operator's `secret` CLI. A var so tests can
// stand in for the CLI.
var pluginSecretLookup = func(ctx context.Context, name string) (string, error) {
	if v := os.Getenv(name); v != "" {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "secret", name).Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimRight(string(out), "\r\n")
	if v == "" {
		return "", errors.New("empty value")
	}
	return v, nil
}

// pluginServer supervises one plugin's child and its mirrored tools.
type pluginServer struct {
	name string
	dir  string
	spec *pluginMCPSpec
	fp   string
	// isolation is where it runs (host / container / vm); a change is a
	// restart, since a server cannot move between them in place.
	isolation string
	runner    pluginRunner
	server    func() *mcp.Server
	notify    func()
	// dataDir is created (0700) before each launch; ring keeps the child's
	// recent stderr (the server's, through isb exec when sandboxed) for GET
	// /api/plugins/<name>/log. Both are set by
	// the manager after construction and may be empty/nil.
	dataDir string
	ring    *lineRing

	cancel context.CancelFunc
	done   chan struct{}

	mu       sync.Mutex
	status   string
	detail   string
	client   *mcp.ClientSession
	own      map[string]bool   // the child's own tool names, as it listed them
	mirrored map[string]string // mirrored name -> the child's name
	launches int               // successful launches, restarts included
}

type pluginServerStatus struct {
	Status string
	Detail string
	Tools  []string
}

func newPluginServer(e *pluginEntry, isolation string, server func() *mcp.Server, runner pluginRunner, notify func()) *pluginServer {
	return &pluginServer{
		name: e.Name, dir: e.Dir, spec: e.Man.MCP, fp: e.FP, isolation: isolation,
		runner: runner, server: server, notify: notify,
		status: pluginMCPStopped,
	}
}

func (s *pluginServer) setStatus(status, detail string) {
	s.mu.Lock()
	changed := s.status != status || s.detail != detail
	s.status, s.detail = status, detail
	s.mu.Unlock()
	if changed && s.notify != nil {
		s.notify()
	}
}

func (s *pluginServer) snapshot() pluginServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	tools := make([]string, 0, len(s.mirrored))
	for n := range s.mirrored {
		tools = append(tools, n)
	}
	sort.Strings(tools)
	return pluginServerStatus{Status: s.status, Detail: s.detail, Tools: tools}
}

func (s *pluginServer) start(parent context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.setStatus(pluginMCPStarting, "")
	go func() {
		defer close(s.done)
		s.run(ctx)
	}()
}

// stop ends the supervision loop and waits for the child to be gone.
func (s *pluginServer) stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
}

// run is the restart loop.
func (s *pluginServer) run(ctx context.Context) {
	backoff := pluginBackoffMin
	for ctx.Err() == nil {
		s.setStatus(pluginMCPStarting, "")
		began := time.Now()
		proc, err := s.launch(ctx)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if errors.Is(err, errPluginUnavailable) {
				detail := strings.TrimPrefix(err.Error(), errPluginUnavailable.Error()+": ")
				log.Printf("plugin[%s]: MCP unavailable: %s", s.name, detail)
				s.setStatus(pluginMCPUnavailable, detail)
				<-ctx.Done()
				break
			}
			log.Printf("plugin[%s]: MCP server failed to start: %v (retrying in %s)", s.name, err, backoff)
			s.setStatus(pluginMCPError, err.Error())
		} else {
			why := s.serve(ctx, proc)
			if ctx.Err() != nil {
				break
			}
			// A child that ran for a good while and then died is a fresh
			// failure, not the next step of a crash loop.
			if time.Since(began) > pluginBackoffMax {
				backoff = pluginBackoffMin
			}
			log.Printf("plugin[%s]: MCP server %s (restarting in %s)", s.name, why, backoff)
			s.setStatus(pluginMCPError, why+"; restarting")
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, pluginBackoffMax)
	}
	s.setStatus(pluginMCPStopped, "")
}

// launch starts the child, completes the MCP handshake and lists its tools. On
// success the tools are registered and the server is running.
func (s *pluginServer) launch(ctx context.Context) (pluginProc, error) {
	secrets := map[string]string{}
	for _, sec := range s.spec.Secrets {
		v, err := pluginSecretLookup(ctx, sec.Name)
		if err != nil {
			// Named, never shown: the value (or its absence) stays off the log.
			return nil, unavailable("secret %s could not be resolved (not in lasso's environment, and `secret %s` failed)", sec.Name, sec.Name)
		}
		secrets[sec.Name] = v
	}
	if s.dataDir != "" {
		if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
			return nil, unavailable("cannot create the plugin's data directory %s: %v", s.dataDir, err)
		}
	}
	logw := &browserMCPStderr{tail: &tailBuffer{max: 8 << 10}, label: "plugin:" + s.name}
	var out io.Writer = logw
	if s.ring != nil {
		out = io.MultiWriter(logw, s.ring)
	}
	lctx, cancel := context.WithTimeout(ctx, pluginStartTimeout)
	defer cancel()
	proc, err := s.runner.start(lctx, pluginLaunch{Name: s.name, Dir: s.dir, DataDir: s.dataDir, MCP: s.spec, Secrets: secrets}, out)
	if err != nil {
		return nil, err
	}
	client, err := s.handshake(lctx, proc)
	if err != nil {
		tail := browserMCPTail(logw.tail)
		proc.stop()
		return nil, fmt.Errorf("%v%s", err, tail)
	}
	var tools []*mcp.Tool
	for t, err := range client.Tools(lctx, nil) { // follows pagination
		if err != nil {
			_ = client.Close()
			proc.stop()
			return nil, fmt.Errorf("tools/list failed: %v%s", err, browserMCPTail(logw.tail))
		}
		tools = append(tools, t)
	}
	s.register(client, tools)
	return proc, nil
}

// handshake dials the child and initializes, redialing while the child is
// alive and the deadline allows.
func (s *pluginServer) handshake(ctx context.Context, proc pluginProc) (*mcp.ClientSession, error) {
	var last error
	for {
		stream, err := proc.connect(ctx)
		if err != nil {
			if last != nil {
				return nil, fmt.Errorf("%v (last handshake error: %v)", err, last)
			}
			return nil, err
		}
		actx, cancel := context.WithTimeout(ctx, pluginConnectAttempt)
		c := mcp.NewClient(&mcp.Implementation{Name: "lasso", Version: lassoSemver}, nil)
		cs, err := c.Connect(actx, &mcp.IOTransport{Reader: stream.r, Writer: stream.w}, nil)
		cancel()
		if err == nil {
			return cs, nil
		}
		_ = stream.r.Close()
		_ = stream.w.Close()
		last = err
		select {
		case <-proc.done():
			return nil, fmt.Errorf("the MCP server exited during startup: %v", err)
		case <-ctx.Done():
			return nil, fmt.Errorf("no MCP handshake before the deadline: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// serve holds the connection until the child dies or the loop is cancelled,
// then unregisters the tools and stops the child. It returns why it ended.
func (s *pluginServer) serve(ctx context.Context, proc pluginProc) string {
	s.mu.Lock()
	client := s.client
	n := len(s.mirrored)
	s.mu.Unlock()
	where := "in an isb " + s.isolation
	if !s.runner.sandboxed() {
		where = "TRUSTED, on the host"
	}
	log.Printf("plugin[%s]: MCP server running (%d tools, %s)", s.name, n, where)
	s.setStatus(pluginMCPRunning, "")
	sessDone := make(chan struct{})
	go func() { _ = client.Wait(); close(sessDone) }()
	why := "stopped"
	select {
	case <-ctx.Done():
	case <-proc.done():
		why = "exited"
	case <-sessDone:
		why = "closed its connection"
	}
	s.unregister()
	_ = client.Close()
	if why != "stopped" {
		if s.ring != nil {
			if last := s.ring.last(8); len(last) > 0 {
				why += "; output: " + clipLine(strings.Join(last, " | "), 800)
			}
		}
		// Said now, not after proc.stop: removing a sandbox takes seconds,
		// and the listing should not read "running" meanwhile.
		s.setStatus(pluginMCPError, why+"; restarting")
	}
	proc.stop()
	return why
}

// register mirrors the child's tools onto lasso's shared server.
func (s *pluginServer) register(client *mcp.ClientSession, tools []*mcp.Tool) {
	own := map[string]bool{}
	mirrored := map[string]string{}
	var srv *mcp.Server
	if s.server != nil {
		srv = s.server()
	}
	for _, t := range tools {
		own[t.Name] = true
		name := s.name + pluginToolSep + t.Name
		if !mcpToolNameRE.MatchString(name) {
			log.Printf("plugin[%s]: skipping tool %q: %q is not a valid tool name (%s)", s.name, t.Name, name, mcpToolNameRE)
			continue
		}
		if !mcpObjectSchema(t.InputSchema) {
			// Server.AddTool panics on one; a broken tool is not a broken plugin.
			log.Printf("plugin[%s]: skipping tool %q: its input schema is not an object", s.name, t.Name)
			continue
		}
		mt := *t
		mt.Name = name
		mt.Description = "[plugin " + s.name + "] " + t.Description
		if mt.OutputSchema != nil && !mcpObjectSchema(mt.OutputSchema) {
			mt.OutputSchema = nil
		}
		mirrored[name] = t.Name
		if srv != nil {
			srv.AddTool(&mt, s.forward(t.Name))
		}
	}
	s.mu.Lock()
	s.client, s.own, s.mirrored = client, own, mirrored
	s.launches++
	s.mu.Unlock()
}

func (s *pluginServer) unregister() {
	s.mu.Lock()
	names := make([]string, 0, len(s.mirrored))
	for n := range s.mirrored {
		names = append(names, n)
	}
	s.client, s.own, s.mirrored = nil, nil, nil
	s.mu.Unlock()
	if len(names) > 0 && s.server != nil {
		if srv := s.server(); srv != nil {
			srv.RemoveTools(names...)
		}
	}
}

// forward is a mirrored tool's /mcp handler. A plugin runs on lasso's machine
// (in a sandbox or, trusted, directly), so a caller whose credential does not
// reach lasso's own machine may not use it — the shared_browser rule, since a
// plugin tool reaches whatever lasso's machine lets it.
func (s *pluginServer) forward(tool string) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cs := callerFrom(req)
		if !cs.allows("local") {
			return toolErrorResult(fmt.Sprintf("plugin %s runs on lasso's own machine, which is outside this credential's reach (%s)", s.name, cs.reachSummary())), nil
		}
		var args any
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			args = req.Params.Arguments
		}
		var meta mcp.Meta
		if req != nil && req.Params != nil {
			meta = req.Params.Meta
		}
		res, err := s.call(ctx, tool, args, meta)
		if err != nil {
			return toolErrorResult(err.Error()), nil
		}
		return res, nil
	}
}

func (s *pluginServer) call(ctx context.Context, tool string, args any, meta mcp.Meta) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	client, status := s.client, s.status
	s.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("%w: plugin %s's MCP server is %s", errPluginNotRunning, s.name, status)
	}
	return client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args, Meta: meta})
}

// callOwn is a plugin tab calling one of its own tools, by the child's name or
// the mirrored one. Membership in THIS child's list is the check; a name that
// only another plugin's child serves is refused.
func (s *pluginServer) callOwn(ctx context.Context, tool string, args any) (*mcp.CallToolResult, error) {
	s.mu.Lock()
	own, mirrored, status := s.own, s.mirrored, s.status
	s.mu.Unlock()
	if own == nil {
		return nil, fmt.Errorf("%w: plugin %s's MCP server is %s", errPluginNotRunning, s.name, status)
	}
	name := tool
	if orig, ok := mirrored[tool]; ok {
		name = orig
	} else if !own[tool] {
		return nil, errNotPluginTool
	}
	return s.call(ctx, name, args, nil)
}

func toolErrorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

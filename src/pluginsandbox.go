package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Where a plugin's MCP server runs.
//
// By default in an isb sandbox (https://github.com/execution-associates/isb):
// an unprivileged incus container built from the manifest's OCI image, with
// the plugin directory mounted read-only at /plugin, its data directory
// read-write at /data, a network that reaches only the hosts the operator
// approved (isb's egress proxy), and secrets that never enter the guest at all
// (isb's proxy swaps a placeholder for the value on the wire, and only toward
// the hosts each secret is approved for). The operator may move one plugin
// into a VM (its own kernel) with the per-plugin isolation flag, or mark it
// TRUSTED to run it on the host as lasso's user with a minimal environment.
//
// Transport: the sandbox's own process is a long sleep, and the MCP server is
// `isb exec -i -T lasso-plugin-<name> -- <command>`, a child of lasso whose
// stdin/stdout ARE the server's. isb 1.0 forwards stdin only with a tty, -i
// or -T, so both flags are load-bearing: without them the server reads EOF at
// once and exits. Measured on isb 1.0.0: when the server inside dies, the exec
// exits within milliseconds (container and VM alike), so a dead server is
// seen through proc.done() and the session closing — no ping is needed.

const (
	pluginGuestDir    = "/plugin"
	pluginGuestData   = "/data"
	pluginSandboxPref = "lasso-plugin-"
	// pluginDefaultVMImage is the VM image a plugin without mcp.vm_image
	// boots in VM mode. It has python3; a node/bun plugin needs a vm_image of
	// its own (an OCI image cannot boot as a VM).
	pluginDefaultVMImage = "images:ubuntu/24.04/cloud"
	// pluginGuestUser is who the server runs as, never root. In a container
	// isb's `--idmap auto` maps guest 1000 to host 1000, so what the server
	// writes to /data lands owned by lasso's user (when lasso runs as uid
	// 1000, isb's convention). In a VM it matters more: incus shares /data
	// over virtiofs with no uid translation (measured on incus with isb
	// 1.0.0), so a file guest ROOT creates there is owned by host root, mode
	// bits included — setuid too. The server therefore runs as 1000 in the
	// VM as well, where the default image has no such user and no sudo.
	pluginGuestUser = "1000:1000"
	// pluginKeepAlive is an OCI container's init. The image's default CMD
	// (python's REPL, say) would exit at once without a tty and take the
	// instance with it; the server runs separately, through exec.
	pluginKeepAlive = `sh -c "sleep infinity || exec tail -f /dev/null"`
	// isbMinVersion: 1.0 is the release whose `isb exec` forwards stdin only
	// when asked, which is what the exec argv is written for, and whose
	// egress proxy this sandbox relies on.
	isbMinVersion = "1.0.0"
	// isbCallTimeout bounds each housekeeping call (rm, secret set/rm). They
	// take a second or two; a wedged daemon must not wedge lasso's shutdown.
	isbCallTimeout = 30 * time.Second
)

// Isolation levels, as the listing reports them.
const (
	pluginIsolationHost      = "host"
	pluginIsolationContainer = "container"
	pluginIsolationVM        = "vm"
)

// pluginIsolation is where a grant puts a plugin's MCP server. Trusted wins
// over vm: the host is outside every sandbox.
func pluginIsolation(g pluginGrant) string {
	switch {
	case g.Trusted:
		return pluginIsolationHost
	case g.VM:
		return pluginIsolationVM
	default:
		return pluginIsolationContainer
	}
}

// defaultPluginRunner is the manager's runner choice for an isolation level.
func defaultPluginRunner(isolation string) pluginRunner {
	switch isolation {
	case pluginIsolationHost:
		return hostPluginRunner{}
	case pluginIsolationVM:
		return isbPluginRunner{vm: true}
	default:
		return isbPluginRunner{}
	}
}

// ---------------------------------------------------------------------------
// finding isb
// ---------------------------------------------------------------------------

// sandboxStatus is the listing's `sandbox` object.
type sandboxStatus struct {
	Kind         string `json:"kind"`
	Available    bool   `json:"available"`
	Path         string `json:"path,omitempty"`
	Version      string `json:"version,omitempty"`
	ServeRunning bool   `json:"serve_running"`
	Reason       string `json:"reason,omitempty"`
}

// isbMiseInstalls is where mise keeps its isb installs. A var so tests can
// point it at a temp dir.
var isbMiseInstalls = func() string {
	data := os.Getenv("MISE_DATA_DIR")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		data = filepath.Join(home, ".local", "share", "mise")
	}
	return filepath.Join(data, "installs", "github-execution-associates-isb")
}

// newestMiseISB is the highest X.Y.Z install with an isb binary in it.
func newestMiseISB() string {
	root := isbMiseInstalls()
	if root == "" {
		return ""
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	best, bestV := "", [3]int{-1, -1, -1}
	for _, e := range ents {
		v, ok := parseSemver(e.Name())
		if !ok || strings.Count(e.Name(), ".") != 2 {
			continue // mise's "1", "1.0" and "latest" are links to a real one
		}
		p := filepath.Join(root, e.Name(), "isb")
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			continue
		}
		if semverLess(bestV, v) {
			best, bestV = p, v
		}
	}
	return best
}

type isbVersionCache struct {
	mod     time.Time
	size    int64
	version string
	err     error
}

var (
	isbVersionsMu sync.Mutex
	isbVersions   = map[string]isbVersionCache{}
)

// isbVersion runs `<bin> --version` ("isb 1.0.0"), cached per binary until it
// changes on disk: the listing asks on every GET.
func isbVersion(bin string) (string, error) {
	st, err := os.Stat(bin)
	if err != nil {
		return "", err
	}
	isbVersionsMu.Lock()
	c, ok := isbVersions[bin]
	isbVersionsMu.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.version, c.err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--version")
	cmd.Stdin = nil
	out, err := cmd.Output()
	var v string
	if err == nil {
		f := strings.Fields(string(out))
		if len(f) == 0 {
			err = errors.New("no version in its --version output")
		} else {
			v = strings.TrimPrefix(f[len(f)-1], "v")
		}
	}
	isbVersionsMu.Lock()
	isbVersions[bin] = isbVersionCache{mod: st.ModTime(), size: st.Size(), version: v, err: err}
	isbVersionsMu.Unlock()
	return v, err
}

// isbNewEnough reports whether version is at least isbMinVersion.
func isbNewEnough(version string) bool {
	have, ok := parseSemver(version)
	if !ok {
		return false
	}
	want, _ := parseSemver(isbMinVersion)
	return !semverLess(have, want)
}

// resolveISB finds an isb CLI of at least isbMinVersion: LASSO_ISB (a path or
// PATH name, "off" disables sandboxed plugins) and nothing else when it is
// set; otherwise isb on PATH, then the newest mise install — lasso often runs
// as a systemd unit whose PATH lacks the operator's shell additions, and the
// PATH one may be an older pin. The first new enough candidate wins.
func resolveISB() (bin, version, reason string, ok bool) {
	e := strings.TrimSpace(os.Getenv("LASSO_ISB"))
	if strings.EqualFold(e, "off") {
		return "", "", "sandboxed plugins are disabled (LASSO_ISB=off)", false
	}
	var cands []string
	if e != "" {
		if strings.ContainsRune(e, '/') {
			if st, err := os.Stat(e); err != nil || st.IsDir() {
				return "", "", fmt.Sprintf("isb not found at LASSO_ISB=%q", e), false
			}
			cands = []string{e}
		} else {
			p, err := exec.LookPath(e)
			if err != nil {
				return "", "", fmt.Sprintf("isb not found: LASSO_ISB=%q is not on PATH", e), false
			}
			cands = []string{p}
		}
	} else {
		if p, err := exec.LookPath("isb"); err == nil {
			cands = append(cands, p)
		}
		if p := newestMiseISB(); p != "" {
			cands = append(cands, p)
		}
	}
	if len(cands) == 0 {
		return "", "", "isb not found: install it (mise use -g github:execution-associates/isb) or set LASSO_ISB", false
	}
	var found []string
	for _, c := range cands {
		v, err := isbVersion(c)
		if err != nil {
			found = append(found, fmt.Sprintf("%s: %v", c, err))
			continue
		}
		if isbNewEnough(v) {
			return c, v, "", true
		}
		found = append(found, v+" at "+c)
	}
	return cands[0], "", fmt.Sprintf("isb %s or later is required (found %s)", strings.TrimSuffix(isbMinVersion, ".0"), strings.Join(found, "; ")), false
}

// isbServeSocket is the daemon's unix socket, found the way the isb CLI finds
// it: ISB_SERVE_SOCKET, else $XDG_RUNTIME_DIR/isb/serve.sock, else
// <tmp>/isb-<uid>/serve.sock.
func isbServeSocket() string {
	if s := os.Getenv("ISB_SERVE_SOCKET"); s != "" {
		return s
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "isb", "serve.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("isb-%d", os.Getuid()), "serve.sock")
}

// isbServeCheck asks the daemon's /healthz over its unix socket. `isb serve`
// runs the egress proxy, so a sandbox created while it is down has a network
// that goes nowhere; lasso refuses to start one rather than run a plugin
// whose every request fails. A var so tests can stand in for the daemon.
var isbServeCheck = func() error {
	sock := isbServeSocket()
	c := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}},
	}
	resp, err := c.Get("http://isb/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var h struct {
		OK bool `json:"ok"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&h) != nil || !h.OK {
		return fmt.Errorf("/healthz answered %s", resp.Status)
	}
	return nil
}

func currentSandboxStatus() sandboxStatus {
	st := sandboxStatus{Kind: "isb"}
	bin, v, reason, ok := resolveISB()
	st.Path, st.Version = bin, v
	if !ok {
		st.Reason = reason
		return st
	}
	if err := isbServeCheck(); err != nil {
		st.Reason = "isb serve is not running (it runs the egress proxy plugin sandboxes need): " + err.Error()
		return st
	}
	st.ServeRunning, st.Available = true, true
	return st
}

// isbEnv is the isb CLI's environment: lasso's minus its own credentials (as
// for Chromium), and minus what would send a sandbox or a secret somewhere
// else — an org (ISB_ORG, INCUS_PROJECT) or a remote daemon (ISB_URL,
// ISB_TOKEN). XDG_STATE_HOME stays: the CLI and the daemon share each
// sandbox's egress CA through $XDG_STATE_HOME/isb.
func isbEnv() []string {
	out := []string{}
	for _, kv := range browserEnv(os.Environ()) {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ISB_ORG", "INCUS_PROJECT", "ISB_URL", "ISB_TOKEN":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// isbQuiet runs one isb housekeeping command, returning its combined output.
// Stdin is left nil (/dev/null), so nothing can wait on it.
func isbQuiet(isb string, stdin io.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), isbCallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, isb, args...)
	cmd.Env = isbEnv()
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---------------------------------------------------------------------------
// the sandbox's shape (pure, so the security-relevant flags are testable)
// ---------------------------------------------------------------------------

func pluginSandboxName(plugin string) string { return pluginSandboxPref + plugin }

// pluginSecretStore is the isb store name one approved secret is written
// under for the life of a sandbox. The '.' cannot occur in a plugin name, so
// one plugin's prefix (lasso-plugin-a.) never matches another's (a-b).
func pluginSecretStore(plugin, secret string) string {
	return pluginSandboxPref + plugin + "." + strings.ToLower(secret)
}

func pluginSecretStorePrefix(plugin string) string { return pluginSandboxPref + plugin + "." }

// isbOCIPrefixes are isb's OCI registries; an image naming one is used as is.
var isbOCIPrefixes = []string{"docker:", "ghcr:", "quay:", "oci:"}

// isbImage maps a manifest's mcp.image to isb's image syntax: an explicit
// registry prefix (or isb's own images: remote) is kept, and anything else is
// a Docker Hub reference ("python:3.12-slim" → "docker:python:3.12-slim").
// oci says whether it boots as an application container, which needs a
// keep-alive init.
func isbImage(image string) (ref string, oci bool) {
	for _, p := range isbOCIPrefixes {
		if strings.HasPrefix(image, p) {
			return image, true
		}
	}
	if strings.HasPrefix(image, "images:") {
		return image, false
	}
	return "docker:" + image, true
}

// pluginEgressHosts is the sandbox's allowlist: the approved network entries
// plus each secret's hosts (on 443, where a secret is sent). isb allows a
// secret's hosts by itself; naming them keeps the list explicit.
func pluginEgressHosts(spec *pluginMCPSpec) []string {
	seen := map[string]bool{}
	var out []string
	add := func(h string) {
		if !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	for _, n := range spec.Network {
		e, _ := parsePluginNet(n) // validated with the manifest
		add(fmt.Sprintf("%s:%d", e.Host, e.Port))
	}
	for _, s := range spec.Secrets {
		for _, h := range s.Hosts {
			add(strings.ToLower(h) + ":443")
		}
	}
	return out
}

// isbCreateArgs is `isb create`'s argv for one plugin. Secrets appear by NAME
// only (`--secret NAME=<store>@hosts`): the value is in isb's store, written
// there from stdin, and the guest gets a placeholder. The plain env values do
// ride argv — they are non-secret by definition. LASSO_PLUGIN_DATA comes after
// the manifest's env so a manifest cannot point it elsewhere.
func isbCreateArgs(plugin, dir, dataDir string, spec *pluginMCPSpec, vm bool) []string {
	name := pluginSandboxName(plugin)
	var image string
	oci := false
	if vm {
		image = pluginDefaultVMImage
		if spec.VMImage != "" {
			image = spec.VMImage
		}
	} else {
		image, oci = isbImage(spec.Image)
	}
	args := []string{"create", "-i", image}
	if vm {
		args = append(args, "--vm")
	} else {
		args = append(args, "--idmap", "auto")
	}
	args = append(args, "-l", "owner=lasso", "-l", "lasso.plugin="+plugin)
	if oci {
		args = append(args, "-c", "oci.entrypoint="+pluginKeepAlive)
	}
	args = append(args, "-v", dir+":"+pluginGuestDir+":ro")
	if dataDir != "" {
		args = append(args, "-v", dataDir+":"+pluginGuestData)
	}
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-e", k+"="+spec.Env[k])
	}
	if dataDir != "" {
		args = append(args, "-e", "LASSO_PLUGIN_DATA="+pluginGuestData)
	}
	hosts := pluginEgressHosts(spec)
	if len(hosts) == 0 {
		args = append(args, "--egress", "none")
	}
	for _, h := range hosts {
		args = append(args, "--egress", h)
	}
	for _, s := range spec.Secrets {
		hs := make([]string, 0, len(s.Hosts))
		for _, h := range s.Hosts {
			hs = append(hs, strings.ToLower(h))
		}
		args = append(args, "--secret", s.Name+"="+pluginSecretStore(plugin, s.Name)+"@"+strings.Join(hs, ","))
	}
	return append(args, name)
}

// isbExecArgs is the MCP child's argv. -i AND -T: isb 1.0 forwards stdin only
// with a tty, -i or -T, and -T also keeps a pty from mangling the JSON-RPC
// stream. The server runs as pluginGuestUser in a container and a VM alike.
func isbExecArgs(plugin string, spec *pluginMCPSpec) []string {
	args := []string{"exec", "-i", "-T", "-w", pluginGuestDir, "-u", pluginGuestUser, pluginSandboxName(plugin), "--"}
	return append(args, spec.Command...)
}

// ---------------------------------------------------------------------------
// the isb runner
// ---------------------------------------------------------------------------

type isbPluginRunner struct{ vm bool }

func (isbPluginRunner) sandboxed() bool { return true }

func (r isbPluginRunner) start(ctx context.Context, l pluginLaunch, logw io.Writer) (pluginProc, error) {
	st := currentSandboxStatus()
	if !st.Available {
		return nil, unavailable("%s", st.Reason)
	}
	isb := st.Path
	// isb's -v shorthand is SRC:GUEST[:OPTS]; a ':' in the source cannot be
	// said in it.
	for _, p := range []string{l.Dir, l.DataDir} {
		if strings.Contains(p, ":") {
			return nil, unavailable("the path %s contains ':', which isb's mount syntax cannot express", p)
		}
	}
	name := pluginSandboxName(l.Name)
	// A previous lasso that died without cleaning up leaves one behind, and
	// the name is fixed.
	removePluginSandbox(isb, name)

	var stores []string
	cleanup := func() {
		removePluginSandbox(isb, name)
		deletePluginSecrets(isb, stores)
	}
	for _, s := range l.MCP.Secrets {
		store := pluginSecretStore(l.Name, s.Name)
		// The value goes in on stdin, never argv (argv is world-readable in
		// /proc). Overwritten on every start, so the store holds what lasso
		// resolved this time.
		if out, err := isbQuiet(isb, strings.NewReader(l.Secrets[s.Name]), "secret", "set", store, "-"); err != nil {
			cleanup()
			return nil, fmt.Errorf("store secret %s in isb: %v: %s", s.Name, err, clipLine(out, 300))
		}
		stores = append(stores, store)
	}

	create := exec.CommandContext(ctx, isb, isbCreateArgs(l.Name, l.Dir, l.DataDir, l.MCP, r.vm)...)
	create.Env = isbEnv()
	create.Stdin = nil
	tail := &tailBuffer{max: 4 << 10}
	create.Stdout = io.MultiWriter(logw, tail)
	create.Stderr = io.MultiWriter(logw, tail)
	create.SysProcAttr = browserSysProcAttr()
	create.Cancel = func() error { return killGroup(create) }
	if err := create.Run(); err != nil {
		cleanup()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("isb create: %v%s", err, browserMCPTail(tail))
	}

	cmd := exec.Command(isb, isbExecArgs(l.Name, l.MCP)...)
	cmd.Env = isbEnv()
	cmd.Stderr = logw
	// Own process group + Pdeathsig, like Chromium: a kill -9 of lasso must
	// not leave the exec (and so the server) running.
	cmd.SysProcAttr = browserSysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, fmt.Errorf("start isb exec: %v", err)
	}
	p := &stdioProc{cmd: cmd, stdin: stdin, stdout: stdout, exited: make(chan struct{}), cleanup: cleanup}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	return p, nil
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil || cmd.Process.Pid <= 1 {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// removePluginSandbox deletes a plugin's sandbox, running or not. "Not found"
// is the usual answer and not worth a line.
func removePluginSandbox(isb, name string) {
	out, err := isbQuiet(isb, nil, "rm", "-f", name)
	if err != nil && !strings.Contains(out, "not found") {
		log.Printf("plugins:  isb rm -f %s: %v: %s", name, err, clipLine(out, 300))
	}
}

// deletePluginSecrets removes store entries lasso wrote for a sandbox.
func deletePluginSecrets(isb string, stores []string) {
	if len(stores) == 0 {
		return
	}
	if out, err := isbQuiet(isb, nil, append([]string{"secret", "rm"}, stores...)...); err != nil && !strings.Contains(out, "not found") {
		log.Printf("plugins:  isb secret rm %s: %v: %s", strings.Join(stores, " "), err, clipLine(out, 300))
	}
}

// purgePluginSecrets removes every store entry under a plugin's prefix — the
// ones a running sandbox's stop would have, plus any a crashed lasso left
// behind. Run on uninstall and unlink. Best effort: no isb, no purge.
func purgePluginSecrets(plugin string) {
	isb, _, _, ok := resolveISB()
	if !ok {
		return
	}
	out, err := isbQuiet(isb, nil, "secret", "ls", "--json")
	if err != nil {
		return
	}
	var list []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal([]byte(out), &list) != nil {
		return
	}
	var mine []string
	for _, s := range list {
		if strings.HasPrefix(s.Name, pluginSecretStorePrefix(plugin)) {
			mine = append(mine, s.Name)
		}
	}
	deletePluginSecrets(isb, mine)
}

// sweepLegacyMSBSandboxes stops and removes any `lasso-plugin-*` microsandbox
// microVMs a lasso before 5.0 left running: nothing else will ever stop them.
// Best effort and quiet when msb is absent; stdin stays nil (an inherited one
// hangs msb), and every call is bounded.
func sweepLegacyMSBSandboxes() {
	msb, err := exec.LookPath("msb")
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return
		}
		msb = filepath.Join(home, ".microsandbox", "bin", "msb")
		if st, serr := os.Stat(msb); serr != nil || st.IsDir() {
			return
		}
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, msb, args...)
		cmd.Stdin = nil
		out, err := cmd.Output()
		return string(out), err
	}
	out, err := run("ls", "-q")
	if err != nil {
		return
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(name, pluginSandboxPref) {
			continue
		}
		_, _ = run("stop", name)
		if _, err := run("rm", name); err == nil {
			log.Printf("plugins:  removed legacy microsandbox sandbox %s", name)
		} else {
			log.Printf("plugins:  could not remove legacy microsandbox sandbox %s: %v", name, err)
		}
	}
}

// ---------------------------------------------------------------------------
// the stdio child (both runners)
// ---------------------------------------------------------------------------

// stdioProc is a child whose stdin/stdout are an MCP server's: the server
// itself (trusted) or `isb exec` (sandboxed). cleanup, when set, runs after
// the child is gone — removing the sandbox and its store secrets.
type stdioProc struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	exited  chan struct{}
	cleanup func()

	mu   sync.Mutex
	used bool
	once sync.Once
}

func (p *stdioProc) done() <-chan struct{} { return p.exited }

// connect hands out the child's stdio exactly once: a stdio server has one
// conversation, and a failed initialize on it means a failed launch.
func (p *stdioProc) connect(context.Context) (pluginStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used {
		return pluginStream{}, errors.New("the MCP server did not complete its handshake over stdio")
	}
	p.used = true
	return pluginStream{r: p.stdout, w: p.stdin}, nil
}

func (p *stdioProc) stop() {
	p.once.Do(func() {
		_ = p.stdin.Close() // a well-behaved stdio server exits on EOF
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			stopProcessGroup(p.cmd, p.exited, 2*time.Second)
		}
		if p.cleanup != nil {
			p.cleanup()
		}
	})
}

// stopProcessGroup SIGTERMs a child's process group, escalating to SIGKILL
// after grace, and waits for the leader.
func stopProcessGroup(cmd *exec.Cmd, exited <-chan struct{}, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 1 { // never kill(-0)/kill(-1)
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(grace):
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		log.Printf("plugins:  pid %d did not exit after SIGKILL", pid)
	}
}

// ---------------------------------------------------------------------------
// the host runner (trusted plugins)
// ---------------------------------------------------------------------------

type hostPluginRunner struct{}

func (hostPluginRunner) sandboxed() bool { return false }

// start runs the command in the plugin directory as lasso's user. The
// environment is the same allowlist chrome-devtools-mcp gets — never lasso's
// UI_AUTH/MCP_OAUTH/LASSO_MCP_TOKEN, nor whatever else the operator's shell
// exported — plus the manifest's env and the resolved secrets as plain vars.
// Trusting a plugin means trusting its code, not handing it lasso's keys.
func (hostPluginRunner) start(ctx context.Context, l pluginLaunch, logw io.Writer) (pluginProc, error) {
	cmd := exec.Command(l.MCP.Command[0], l.MCP.Command[1:]...)
	cmd.Dir = l.Dir
	env := minimalChildEnv(os.Environ())
	for k, v := range l.MCP.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range l.Secrets {
		env = append(env, k+"="+v)
	}
	if l.DataDir != "" { // last, so the manifest's env cannot redirect it
		env = append(env, "LASSO_PLUGIN_DATA="+l.DataDir)
	}
	cmd.Env = env
	cmd.Stderr = logw
	cmd.SysProcAttr = browserSysProcAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %v", l.MCP.Command[0], err)
	}
	p := &stdioProc{cmd: cmd, stdin: stdin, stdout: stdout, exited: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	return p, nil
}

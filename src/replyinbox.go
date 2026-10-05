package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The reply inbox is how an agent answers a send_agent message when the sender
// is not a terminal it can type into: a phone talking to claude.ai, a script, an
// agent on another machine. Lasso runs one `tailcat serve <port>` child, which
// forwards that tailcat port to a loopback listener in this process. The
// message an agent receives carries the tailcat address and a per-message token,
// and the agent pipes its reply to `tailcat <addr> <port>`. tailcat punches
// through NAT over DERP, so a sandbox or a box that cannot route to lasso (no
// tailnet, no ssh, no public URL) can still answer.
//
// Only that one port is served: `tailcat serve` proxies the ports it is given
// to the same port on localhost, so naming anything wider (`all`) would hand
// every loopback service on this box to whoever holds the address.
//
// The address embeds a WireGuard pre-shared key and anyone holding it can
// connect, so a connection proves nothing by itself. What it may do is bounded
// by the framing: the first line must carry the token of a message lasso sent
// (stored hashed, see agentmsg.go), the body is capped, and what lands is stored
// as untrusted data for the sender to read, never acted on.

const (
	replyInboxMaxBody   = 1 << 20 // one reply; a longer one is cut and marked
	replyInboxConnLimit = 60 * time.Second
	replyHeader         = "lasso-reply"
)

// replyInboxT owns the listener and the tailcat child. Started lazily by the
// first send_agent that wants a reply (and at boot when replies are still
// expected), restarted by the next ensure after the child dies.
type replyInboxT struct {
	mu   sync.Mutex
	ln   net.Listener
	port int
	addr string
	cmd  *exec.Cmd
	dead chan struct{} // closed when cmd exits
	// lastErr is the most recent start failure, so a burst of sends does not
	// spawn tailcat once per call when it cannot start at all.
	lastErr   error
	lastErrAt time.Time
}

var replyInbox = &replyInboxT{}

// tailcatBin finds the tailcat binary. lasso.service's PATH is narrower than a
// login shell's (no linuxbrew), so the usual install dirs are tried after it.
func tailcatBin() (string, error) {
	if v := strings.TrimSpace(os.Getenv("LASSO_TAILCAT")); v != "" {
		if v == "off" {
			return "", errors.New("the reply inbox is disabled (LASSO_TAILCAT=off)")
		}
		return exec.LookPath(v)
	}
	if p, err := exec.LookPath("tailcat"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local/share/mise/shims/tailcat"),
		filepath.Join(home, ".local/bin/tailcat"),
		"/home/linuxbrew/.linuxbrew/bin/tailcat",
		"/opt/mise/shims/tailcat",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("tailcat is not installed on lasso's host (mise use -g github:tailscale/tailcat), so agents cannot reply over tailcat; set LASSO_TAILCAT to its path")
}

// inboxName keys the tailcat identity and the loopback port on lasso's own
// listen port: a dev lasso and the production one share LASSO_DIR and
// lasso.db, and must not serve the same tailcat address from two processes.
func inboxName() string {
	_, port, err := net.SplitHostPort(*listenAddr)
	if err != nil || port == "" {
		port = "default"
	}
	return "inbox-" + port
}

// tailcatConfigDir keeps the inbox's key under lasso's own directory rather
// than the user's ~/.config/tailcat, which their own tailcat sessions use.
func tailcatConfigDir() string { return filepath.Join(lassoDir(), "tailcat") }

func (r *replyInboxT) alive() bool {
	if r.cmd == nil || r.addr == "" {
		return false
	}
	select {
	case <-r.dead:
		return false
	default:
		return true
	}
}

// ensure returns the inbox's tailcat address and port, starting it if needed.
func (r *replyInboxT) ensure(ctx context.Context) (string, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alive() {
		return r.addr, r.port, nil
	}
	if r.lastErr != nil && time.Since(r.lastErrAt) < 30*time.Second {
		return "", 0, r.lastErr
	}
	addr, port, err := r.start(ctx)
	if err != nil {
		r.lastErr, r.lastErrAt = err, time.Now()
		return "", 0, err
	}
	r.lastErr = nil
	return addr, port, nil
}

// start runs with r.mu held.
func (r *replyInboxT) start(ctx context.Context) (string, int, error) {
	bin, err := tailcatBin()
	if err != nil {
		return "", 0, err
	}
	name := inboxName()
	if r.ln == nil {
		ln, err := listenInbox(name)
		if err != nil {
			return "", 0, fmt.Errorf("reply inbox listener: %w", err)
		}
		r.ln = ln
		r.port = ln.Addr().(*net.TCPAddr).Port
		go r.accept(ln)
	}
	cfg := tailcatConfigDir()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		return "", 0, err
	}
	env := append(os.Environ(), "XDG_CONFIG_HOME="+cfg)
	key := filepath.Join(cfg, "tailcat", "keys", name+".private.json")
	if _, err := os.Stat(key); err != nil {
		// A saved key with its region baked in keeps the address the same across
		// restarts and self-updates, so a reply to a message sent before one
		// still lands. --fixed-region: an auto region would change the address.
		gctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		gen := exec.CommandContext(gctx, bin, "genkey", "--key="+name, "--fixed-region")
		gen.Env = env
		out, gerr := gen.CombinedOutput()
		cancel()
		if gerr != nil {
			return "", 0, fmt.Errorf("tailcat genkey: %v: %s", gerr, strings.TrimSpace(string(out)))
		}
	}
	addrFile := filepath.Join(cfg, name+".addr")
	_ = os.Remove(addrFile)
	cmd := exec.Command(bin, "serve", "--key="+key, strconv.Itoa(r.port))
	cmd.Env = append(env, "TAILCAT_ADDR_FILE="+addrFile)
	cmd.Dir = cfg
	tail := &tailBuffer{max: 4 << 10}
	cmd.Stdout = io.Discard
	cmd.Stderr = tail
	cmd.SysProcAttr = browserSysProcAttr() // own group + Pdeathsig, like every child
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start tailcat: %w", err)
	}
	dead := make(chan struct{})
	go func() {
		err := cmd.Wait()
		log.Printf("reply inbox: tailcat exited: %v %s", err, strings.TrimSpace(tail.String()))
		close(dead)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if b, err := os.ReadFile(addrFile); err == nil && strings.TrimSpace(string(b)) != "" {
			r.cmd, r.dead, r.addr = cmd, dead, strings.TrimSpace(string(b))
			log.Printf("reply inbox: tailcat serving loopback port %d", r.port)
			return r.addr, r.port, nil
		}
		select {
		case <-dead:
			return "", 0, fmt.Errorf("tailcat exited before it was listening: %s", strings.TrimSpace(tail.String()))
		case <-ctx.Done():
			killProcessGroup(cmd)
			return "", 0, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			killProcessGroup(cmd)
			return "", 0, fmt.Errorf("tailcat did not report an address within 30s: %s", strings.TrimSpace(tail.String()))
		}
	}
}

// listenInbox binds loopback, on the port this inbox used last when it is free:
// the port is part of the reply command a pending message carries, so keeping it
// keeps those working across a restart.
func listenInbox(name string) (net.Listener, error) {
	key := "reply_inbox_port:" + name
	if v, _ := getSetting(key); v != "" {
		if ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", v)); err == nil {
			return ln, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	_ = setSetting(key, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	return ln, nil
}

func (r *replyInboxT) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go handleReplyConn(c)
	}
}

// stop tears the child and listener down (shutdown path).
func (r *replyInboxT) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil {
		killProcessGroup(r.cmd)
		r.cmd = nil
	}
	if r.ln != nil {
		r.ln.Close()
		r.ln = nil
	}
	r.addr = ""
}

// handleReplyConn reads one framed reply and answers with one line the sending
// agent sees on its terminal, so it knows whether the reply landed.
func handleReplyConn(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(replyInboxConnLimit))
	token, body, truncated, err := readFramedReply(c)
	if err != nil {
		fmt.Fprintf(c, "lasso: reply NOT delivered: %v\n", err)
		return
	}
	id, err := recordReply(token, body, "tailcat", truncated)
	if err != nil {
		fmt.Fprintf(c, "lasso: reply NOT delivered: %v\n", err)
		return
	}
	note := ""
	if truncated {
		note = fmt.Sprintf(" (cut to %d bytes)", replyInboxMaxBody)
	}
	fmt.Fprintf(c, "lasso: reply to %s delivered%s\n", id, note)
}

// readFramedReply parses `lasso-reply <token>\n<body>`. The header is
// required: without it any holder of the address could drop text in front of
// whoever reads replies next.
func readFramedReply(rd io.Reader) (token, body string, truncated bool, err error) {
	br := bufio.NewReaderSize(rd, 4096)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", "", false, errors.New("empty connection")
	}
	f := strings.Fields(line)
	if len(f) != 2 || !strings.EqualFold(f[0], replyHeader) {
		return "", "", false, fmt.Errorf("the first line must be %q", replyHeader+" <token>")
	}
	b, rerr := io.ReadAll(io.LimitReader(br, replyInboxMaxBody+1))
	if rerr != nil && len(b) == 0 && !errors.Is(rerr, io.EOF) {
		return "", "", false, rerr
	}
	if len(b) > replyInboxMaxBody {
		b, truncated = b[:replyInboxMaxBody], true
	}
	return f[1], string(b), truncated, nil
}

// resumeReplyInbox starts the inbox at boot when a message sent recently may
// still be answered, so a restart between a send and its reply does not leave
// the reply with nowhere to land.
func resumeReplyInbox(ctx context.Context) {
	if !repliesExpected() {
		return
	}
	if _, _, err := replyInbox.ensure(ctx); err != nil {
		log.Printf("reply inbox: %v", err)
	}
}

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBotOmpSocketOn(t *testing.T) {
	t.Setenv("LASSO_DIR", "/l")
	if p, err := botOmpSocketOn(&localBackend{}, "a"); err != nil || p != "/l/bots/a.sock" {
		t.Errorf("local = %q, %v", p, err)
	}
	rb := &remoteBackend{alias: "exa-bot", home: "/home/dev"}
	if p, err := botOmpSocketOn(rb, "exa"); err != nil || p != "/home/dev/.lasso/bots/exa.sock" {
		t.Errorf("remote = %q, %v", p, err)
	}
	if _, err := botOmpSocketOn(&remoteBackend{alias: "x"}, "exa"); err == nil {
		t.Error("a host whose home is unknown has a socket path")
	}
	// The task script hands the bridge the host's path.
	r := &botRecord{Name: "exa", Runtime: botRuntimeOmp}
	if s := botOmpTaskScript(r, "/home/dev/bots/exa", "/home/dev/.lasso/bots/exa.sock", nil); !strings.Contains(s, "LASSO_BOT_SOCKET='/home/dev/.lasso/bots/exa.sock'") {
		t.Errorf("task script does not name the host's socket:\n%s", s)
	}
}

func TestBotOmpForwardSockFits(t *testing.T) {
	long := strings.Repeat("h", 120)
	p := botOmpForwardSock(long, strings.Repeat("n", 64))
	// sun_path is 104 bytes on macOS, 108 on Linux.
	if len(p) >= 104 && os.TempDir() == "/tmp" {
		t.Errorf("forward socket %q is %d bytes", p, len(p))
	}
	if botOmpForwardSock("a", "b") == botOmpForwardSock("a", "c") {
		t.Error("two bots share a forward socket")
	}
}

// fakeSSHForward stands in for ssh's -L of a unix socket: it listens on the
// local end and, per connection, dials the remote end and pipes the two, or
// closes the connection when nothing listens there, as ssh does.
type fakeSSHForward struct {
	mu       sync.Mutex
	adds     int
	cancels  int
	listener map[string]net.Listener
	conns    []net.Conn
}

func (f *fakeSSHForward) add(_ *remoteBackend, local, remote string) error {
	ln, err := net.Listen("unix", local)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.adds++
	f.listener[local] = ln
	f.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go func() {
				up, err := net.Dial("unix", remote)
				if err != nil {
					c.Close()
					return
				}
				go func() { io.Copy(up, c); up.Close() }()
				io.Copy(c, up)
				c.Close()
			}()
		}
	}()
	return nil
}

func (f *fakeSSHForward) cancel(_ *remoteBackend, local, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
	if ln, ok := f.listener[local]; ok {
		ln.Close()
		delete(f.listener, local)
	}
	return nil
}

// masterGone closes every forward the way a dying ssh master does: the
// listeners and their connections go, and the socket files stay.
func (f *fakeSSHForward) masterGone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
	for local, ln := range f.listener {
		if ul, ok := ln.(*net.UnixListener); ok {
			ul.SetUnlinkOnClose(false)
		}
		ln.Close()
		delete(f.listener, local)
	}
}

func (f *fakeSSHForward) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.adds, f.cancels
}

func TestOmpRemoteBotDialsThroughForward(t *testing.T) {
	useBotTestEnv(t)
	home := t.TempDir()
	rb := &remoteBackend{alias: "farhost", home: home}
	prevB := botBackend
	botBackend = func(string) (Backend, error) { return rb, nil }
	fwd := &fakeSSHForward{listener: map[string]net.Listener{}}
	prevAdd, prevCancel := sshForwardUnix, sshCancelForwardUnix
	sshForwardUnix, sshCancelForwardUnix = fwd.add, fwd.cancel
	t.Cleanup(func() {
		botOmpDropForward("far")
		botBackend = prevB
		sshForwardUnix, sshCancelForwardUnix = prevAdd, prevCancel
		ompBots.Lock()
		delete(ompBots.m, "far")
		ompBots.Unlock()
	})
	r := &botRecord{Name: "far", Runtime: botRuntimeOmp, Host: "farhost", Dir: "~/bots/far"}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		t.Fatal(err)
	}

	// Nothing serves the socket on the host: the forward accepts and ssh
	// closes the connection, which is not a running bot.
	if _, _, err := botOmpDial("far"); !errors.Is(err, errOmpNoBridge) {
		t.Fatalf("dial with no bridge = %v", err)
	}
	if _, _, err := botOmpDial("far"); !errors.Is(err, errOmpNoBridge) {
		t.Fatalf("second dial with no bridge = %v", err)
	}
	if adds, _ := fwd.counts(); adds != 1 {
		t.Errorf("forwards added = %d, want 1 (reused while the bot is down)", adds)
	}

	// The bridge listens on the host and says hello first.
	remote := filepath.Join(home, ".lasso", "bots", "far.sock")
	_ = os.MkdirAll(filepath.Dir(remote), 0o700)
	ln, err := net.Listen("unix", remote)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(`{"type":"host_hello","protocol":4,"version":"test","ready":true}` + "\n"))
			go io.Copy(io.Discard, c)
		}
	}()
	conn, br, err := botOmpDial("far")
	if err != nil {
		t.Fatalf("dial = %v", err)
	}
	line, _ := br.ReadString('\n')
	conn.Close()
	if !strings.Contains(line, "host_hello") {
		t.Errorf("first frame = %q", line)
	}

	// The connection the bot loop keeps sees the hello.
	ob := ompBotFor("far")
	if !ob.ensure() {
		t.Fatal("ensure did not connect")
	}
	ompWaitFor(t, "hello", func() bool { return ob.snapshot().hello })

	// The master that held the forward went away: the next dial adds it on
	// the current one.
	fwd.masterGone()
	ompWaitFor(t, "disconnect", func() bool { return !ob.connected() })
	time.Sleep(1100 * time.Millisecond) // ensure's one dial a second
	if !ob.ensure() {
		t.Fatal("ensure did not reconnect after the master went")
	}
	if adds, _ := fwd.counts(); adds != 2 {
		t.Errorf("forwards added = %d, want 2", adds)
	}

	// Stop cancels it.
	botOmpDropForward("far")
	if _, cancels := fwd.counts(); cancels < 1 {
		t.Error("the forward was not cancelled")
	}
	if _, err := os.Stat(botOmpForwardSock("farhost", "far")); err == nil {
		t.Error("the forward's local socket is left behind")
	}
}

func TestOmpRemoteSessionsOverBackend(t *testing.T) {
	// The session list reads through the backend: a fake host whose files are
	// a temp dir stands in for SFTP.
	dir := t.TempDir()
	sess := filepath.Join(dir, "2026-10-12T20-00-00-000Z_abc.jsonl")
	if err := os.WriteFile(sess, []byte(`{"type":"session","id":"abc","title":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	list := ompSessionList(&localBackend{}, dir, "abc")
	if len(list) != 1 || list[0].ID != "abc" || !list[0].Current {
		t.Fatalf("sessions = %+v", list)
	}
	old := time.Now().Add(-time.Hour)
	if err := botTouch(&localBackend{}, sess, old); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(sess); st.ModTime().Sub(old).Abs() > time.Second {
		t.Errorf("mtime = %v, want %v", st.ModTime(), old)
	}
}

package main

// An omp bot on another host (docs/design/bots.md, "omp bots on another
// host"). Its bridge serves the socket on the bot's own host, under that
// host's ~/.lasso/bots, and lasso reaches it through a unix-socket forward it
// adds to the host's ssh master (`ssh -O forward -L local:remote`): the same
// way herdr's socket is reached. Nothing on the bot's host connects to lasso.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// botOmpSocketOn is where the bridge of the bot named name serves its socket
// on b's host: lasso's own bots dir locally, ~/.lasso/bots on another host.
func botOmpSocketOn(b Backend, name string) (string, error) {
	if b == nil || b.Name() == "local" {
		return botSocketPath(name), nil
	}
	home, err := b.HomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("the home directory on %s is not known", b.Name())
	}
	return path.Join(home, ".lasso", "bots", name+".sock"), nil
}

// botOmpForwardSock is the local end of a remote bot's forward. Hashed, so a
// long host alias or bot name stays inside sun_path; the pid keeps a dev and a
// production lasso on one machine apart.
func botOmpForwardSock(alias, name string) string {
	sum := sha256.Sum256([]byte(alias + "\x00" + name))
	return filepath.Join(os.TempDir(), fmt.Sprintf("lasso-omp-%d-%s.sock", os.Getpid(), hex.EncodeToString(sum[:6])))
}

// ompForward is one bot's forward on one ssh master. A master that is
// replaced (the host pool redials) takes its forwards with it, so a forward
// is only reused on the backend that made it.
type ompForward struct {
	rb     *remoteBackend
	local  string
	remote string
}

var ompForwards = struct {
	sync.Mutex
	m map[string]*ompForward // bot name ->
}{m: map[string]*ompForward{}}

// Seams for tests: adding and cancelling a forward on a master.
var (
	sshForwardUnix = func(rb *remoteBackend, local, remote string) error {
		return rb.controlForward("forward", local, remote)
	}
	sshCancelForwardUnix = func(rb *remoteBackend, local, remote string) error {
		return rb.controlForward("cancel", local, remote)
	}
)

// controlForward adds (op "forward") or removes (op "cancel") a unix-socket
// forward on the host's control master.
func (b *remoteBackend) controlForward(op, local, remote string) error {
	ctx, cancel := context.WithTimeout(context.Background(), sshOpTimeout)
	defer cancel()
	out, err := sshCmd(ctx, "-o", "ControlPath="+b.ctlPath, "-O", op, "-L", local+":"+remote, b.alias).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh %s -O %s: %s", b.alias, op, firstNonEmpty(strings.TrimSpace(string(out)), err.Error()))
	}
	return nil
}

// botOmpForward returns the local end of the bot's forward on rb, adding it
// when there is none on this master.
func botOmpForward(rb *remoteBackend, name, remote string) (string, error) {
	ompForwards.Lock()
	defer ompForwards.Unlock()
	if f, ok := ompForwards.m[name]; ok {
		if f.rb == rb && f.remote == remote {
			if _, err := os.Stat(f.local); err == nil {
				return f.local, nil
			}
		}
		botOmpDropForwardLocked(name)
	}
	local := botOmpForwardSock(rb.alias, name)
	// A socket left by a forward this master no longer holds would make ssh's
	// bind fail.
	_ = os.Remove(local)
	if err := sshForwardUnix(rb, local, remote); err != nil {
		return "", err
	}
	ompForwards.m[name] = &ompForward{rb: rb, local: local, remote: remote}
	return local, nil
}

// botOmpDropForward cancels the bot's forward, if it has one: when the bot
// stops, is deleted, or its forward stopped answering.
func botOmpDropForward(name string) {
	ompForwards.Lock()
	defer ompForwards.Unlock()
	botOmpDropForwardLocked(name)
}

func botOmpDropForwardLocked(name string) {
	f, ok := ompForwards.m[name]
	if !ok {
		return
	}
	delete(ompForwards.m, name)
	_ = sshCancelForwardUnix(f.rb, f.local, f.remote)
	_ = os.Remove(f.local)
}

// errOmpNoBridge: the forward is up but nothing serves the socket on the bot's
// host (the bot is not running). The forward is kept for when it is.
var errOmpNoBridge = errors.New("no bridge answers on the bot's host")

// botOmpHelloWait bounds how long a remote dial waits for the bridge's
// host_hello, which it sends first on every connection.
const botOmpHelloWait = 3 * time.Second

// botOmpDial connects to the bot's bridge. A bot on lasso's machine is a
// plain unix dial. A bot on another host is dialed through its forward, and
// only counts as connected once the bridge's first frame arrives: ssh accepts
// on the local end before it learns whether the remote socket exists, and
// closes the connection when it does not.
var botOmpDial = func(name string) (net.Conn, *bufio.Reader, error) {
	r, err := getBot(name)
	if err != nil || r.Host == "local" {
		return ompDialLocal(botSocketPath(name))
	}
	b, err := botBackend(r.Host)
	if err != nil {
		return nil, nil, err
	}
	rb, ok := b.(*remoteBackend)
	if !ok {
		return ompDialLocal(botSocketPath(name))
	}
	remote, err := botOmpSocketOn(b, name)
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; ; attempt++ {
		local, err := botOmpForward(rb, name, remote)
		if err != nil {
			return nil, nil, err
		}
		conn, err := net.DialTimeout("unix", local, time.Second)
		if err != nil {
			// The local end refuses: the master that held the forward went
			// away. Drop it and add it again on the current master, once.
			botOmpDropForward(name)
			if attempt == 0 {
				continue
			}
			return nil, nil, err
		}
		br := bufio.NewReaderSize(conn, 256*1024)
		_ = conn.SetReadDeadline(time.Now().Add(botOmpHelloWait))
		_, err = br.Peek(1)
		_ = conn.SetReadDeadline(time.Time{})
		if err != nil {
			conn.Close()
			return nil, nil, errOmpNoBridge
		}
		return conn, br, nil
	}
}

func ompDialLocal(sock string) (net.Conn, *bufio.Reader, error) {
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		return nil, nil, err
	}
	return conn, bufio.NewReaderSize(conn, 256*1024), nil
}

// botTouch sets a file's modification time to t on b's host.
func botTouch(b Backend, p string, t time.Time) error {
	if rb, ok := b.(*remoteBackend); ok {
		// ISO 8601 with Z: the form GNU and BSD touch both read.
		_, err := rb.runOut("touch -d " + shellQuote(t.UTC().Format("2006-01-02T15:04:05Z")) + " -- " + shellQuote(p))
		return err
	}
	return os.Chtimes(p, t, t)
}

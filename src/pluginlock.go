package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// One lasso per plugins directory runs the plugin MCP servers.
//
// Two lassos can share a LASSO_DIR — on titan the dev build (8190) and the
// production one (8090) both read ~/.lasso — and with it the same approvals.
// Each would then reconcile the same enabled plugins onto the same fixed
// sandbox name (lasso-plugin-<name>) and the same isb store entries, and every
// launch begins with `isb rm -f`: the two would delete each other's sandboxes
// forever. So the servers belong to whoever holds an flock on
// <plugins>/.runner.lock (hidden: the scanner and /plugins/ never see it).
// The other lasso still serves tabs, themes and fonts — those are files — and
// lists each server as unavailable, naming the holder. The kernel drops the
// lock when its holder exits (SIGKILL included), and the survivor takes over
// on its next rescan tick.

const pluginRunnerLockName = ".runner.lock"

// ownsRunner reports whether this process may run plugin servers, taking the
// lock if it is free. Called from reconcile, i.e. at boot and every rescan, so
// a lasso that lost the race at boot picks the servers up once the holder goes.
func (m *pluginManager) ownsRunner() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runLock != nil {
		return true
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		m.runnerElsewhere = fmt.Sprintf("cannot open the plugins directory: %v", err)
		return false
	}
	path := filepath.Join(m.dir, pluginRunnerLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		m.runnerElsewhere = fmt.Sprintf("cannot open %s: %v", path, err)
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := ""
		if b, rerr := os.ReadFile(path); rerr == nil {
			holder = strings.TrimSpace(string(b))
		}
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			if holder == "" {
				holder = "another process"
			}
			m.runnerElsewhere = "plugin servers run in the other lasso using this plugins directory (" + holder + ")"
		} else {
			m.runnerElsewhere = fmt.Sprintf("cannot lock %s: %v", path, err)
		}
		return false
	}
	// Who holds it, for the other lasso's listing. Written after the lock, so a
	// reader sees either the previous holder's note or this one.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d, %s\n", os.Getpid(), listenNote())), 0)
	m.runLock = f
	m.runnerElsewhere = ""
	return true
}

// releaseRunner drops the lock (shutdown, after every server has stopped), so
// a sibling lasso can take the servers over without waiting for this process
// to be reaped.
func (m *pluginManager) releaseRunner() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runLock != nil {
		_ = m.runLock.Close()
		m.runLock = nil
	}
}

// listenNote names this lasso for a human reading the other one's listing.
func listenNote() string {
	if a := strings.TrimSpace(os.Getenv("LASSO_LISTEN")); a != "" {
		return "listening on " + a
	}
	for i, a := range os.Args {
		if (a == "-listen" || a == "--listen") && i+1 < len(os.Args) {
			return "listening on " + os.Args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "-listen="); ok {
			return "listening on " + v
		}
	}
	return "lasso"
}

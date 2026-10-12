package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOmpRemoteSmoke runs a real omp bot on a real host lasso drives, through
// the real ssh master, SFTP, herdr pane and bridge forward, with a throwaway
// lasso.db: no HTTP server, hub or theme sync, so nothing else on the fleet is
// touched. Opt in:
//
//	LASSO_SMOKE_OMP_HOST=<ssh alias>            the host
//	LASSO_SMOKE_OMP_SOCK=<herdr socket there>   its herdr socket
//	LASSO_SMOKE_OMP_MODEL=<provider/model>      optional
//	LASSO_SMOKE_OMP_ENV=<KEY>                   optional: a key from this
//	                                            process's env, set as a secret
//	                                            in the bot's fnox
//	LASSO_SMOKE_OMP_BLANK=<KEY>[,<KEY>]         optional: variables the host's
//	                                            panes inherit that the bot must
//	                                            see empty (its own mise.toml)
//
// It creates the bot ~/bots/omp-smoke there, prompts it, then stops it and
// removes the folder.
func TestOmpRemoteSmoke(t *testing.T) {
	host, sock := os.Getenv("LASSO_SMOKE_OMP_HOST"), os.Getenv("LASSO_SMOKE_OMP_SOCK")
	if host == "" || sock == "" {
		t.Skip("set LASSO_SMOKE_OMP_HOST and LASSO_SMOKE_OMP_SOCK to run against a real host")
	}
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeTestDB)
	ctx, cancel := context.WithCancel(context.Background())
	_, proto := localProtocol()
	rb, err := newRemoteBackend(ctx, host, sock, proto)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { rb.Close(); cancel() })
	prev, prevLaunch := botBackend, botOmpLaunch
	botBackend = func(string) (Backend, error) { return rb, nil }
	// The real launcher: this test opens a real herdr pane on the host.
	botOmpLaunch = herdrBotLauncher{}
	t.Cleanup(func() { botBackend, botOmpLaunch = prev, prevLaunch })

	const name = "omp-smoke"
	r := &botRecord{Name: name, Runtime: botRuntimeOmp, Host: host, Dir: "~/bots/" + name, Workspace: "Bots",
		Model: os.Getenv("LASSO_SMOKE_OMP_MODEL")}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		t.Fatal(err)
	}
	dir := expandTildeOn(rb, r.Dir)
	t.Cleanup(func() {
		_ = stopBot(rb, r)
		botOmpDropForward(name)
		if err := botFolderRemovable(rb, dir); err == nil {
			_ = rb.RemoveAll(dir)
		}
		if _, err := rb.Stat(dir); err == nil {
			t.Errorf("the folder %s is left on %s", dir, host)
		}
		_ = deleteBot(name)
	})
	if err := botMaterialize(rb, r); err != nil {
		t.Fatal(err)
	}
	if blank := os.Getenv("LASSO_SMOKE_OMP_BLANK"); blank != "" {
		var toml strings.Builder
		toml.WriteString("[env]\n")
		for _, k := range strings.Split(blank, ",") {
			toml.WriteString(strings.TrimSpace(k) + " = \"\"\n")
		}
		if err := rb.WriteFile(dir+"/mise.toml", []byte(toml.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := botRun(rb, dir, "mise", []string{"trust", dir + "/mise.toml"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if key := os.Getenv("LASSO_SMOKE_OMP_ENV"); key != "" {
		if err := botEnvSet(rb, dir, key, os.Getenv(key), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := startBot(rb, r, true); err != nil {
		t.Fatal(err)
	}
	waitUntil := func(what string, d time.Duration, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s (state %q)", what, botStatus(rb, r, nil).State)
	}
	waitUntil("idle", 90*time.Second, func() bool { return botStatus(rb, r, nil).State == "idle" })
	v := botStatus(rb, r, nil)
	t.Logf("status: state=%s pane=%s model=%+v host_version=%s outdated=%v", v.State, v.PaneID, v.LiveModel, v.HostVersion, v.HostOutdated)
	if v.PaneID == "" || v.HostOutdated {
		t.Errorf("view: pane %q outdated %v", v.PaneID, v.HostOutdated)
	}

	if err := botOmpSend(name, "Reply with exactly the word SMOKEOK and nothing else.", false); err != nil {
		t.Fatal(err)
	}
	waitUntil("an answer", 180*time.Second, func() bool {
		for _, it := range botOmpChat(rb, r, "").Items {
			if it.Kind == "agent" && strings.Contains(it.Text, "SMOKEOK") {
				return true
			}
		}
		return false
	})
	waitUntil("idle after the turn", 60*time.Second, func() bool { return botStatus(rb, r, nil).State == "idle" })
	if s := botSessions(rb, r); len(s) == 0 || !s[0].Current {
		t.Errorf("sessions = %+v", s)
	}

	if err := stopBot(rb, r); err != nil {
		t.Fatal(err)
	}
	waitUntil("the pane to close", 30*time.Second, func() bool {
		invalidatePaneList(rb.Name())
		_, ok := botOmpLaunch.pane(rb, r)
		return !ok
	})
	if st := botStatus(rb, r, nil).State; st != "stopped" {
		t.Errorf("after stop: %s", st)
	}
	remote, _ := botOmpSocketOn(rb, name)
	if _, err := rb.Stat(remote); err == nil {
		t.Errorf("the bridge's socket %s is left on %s", remote, host)
	}
}

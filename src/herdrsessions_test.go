package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What herdr 0.9.3 prints, trimmed to three sessions.
const herdrSessionListFixture = `{"sessions":[` +
	`{"default":true,"name":"default","running":true,"session_dir":"/h/.config/herdr","socket_path":"/h/.config/herdr/herdr.sock"},` +
	`{"default":false,"name":"work","running":true,"session_dir":"/h/.config/herdr/sessions/work","socket_path":"/h/.config/herdr/sessions/work/herdr.sock"},` +
	`{"default":false,"name":"bench","running":false,"session_dir":"/h/.config/herdr/sessions/bench","socket_path":"/h/.config/herdr/sessions/bench/herdr.sock"}]}`

func TestDecodeHerdrSessions(t *testing.T) {
	got, err := decodeHerdrSessions([]byte(herdrSessionListFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Name != "bench" || got[1].Name != "default" || got[2].Name != "work" {
		t.Fatalf("sessions = %+v, want sorted by name", got)
	}
	if !got[1].Default || !got[1].Running || got[0].Running || got[2].Socket != "/h/.config/herdr/sessions/work/herdr.sock" {
		t.Errorf("fields lost in decoding: %+v", got)
	}

	for _, bad := range []string{`[]`, `{}`, `{"sessions":[{"name":""}]}`, `{"sessions":[{"name":"w"},{"name":"w"}]}`} {
		if _, err := decodeHerdrSessions([]byte(bad)); err == nil {
			t.Errorf("roster %s accepted", bad)
		}
	}
	// An older herdr answers in prose; say what it said, not a JSON error.
	_, err = decodeHerdrSessions([]byte("error: unrecognized subcommand 'session'\n\nUsage: herdr"))
	if err == nil || err.Error() != "error: unrecognized subcommand 'session'" {
		t.Errorf("older herdr error = %v", err)
	}
}

func TestMarkCurrentSession(t *testing.T) {
	sessions, _ := decodeHerdrSessions([]byte(herdrSessionListFixture))
	markCurrentSession(sessions, "/h/.config/herdr/sessions/work/herdr.sock", nil)
	for _, s := range sessions {
		if s.Current != (s.Name == "work") {
			t.Errorf("%s current = %v", s.Name, s.Current)
		}
	}

	// Locally a symlinked config dir still names the same socket.
	dir := t.TempDir()
	real := filepath.Join(dir, "herdr.sock")
	if err := os.WriteFile(real, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sock")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	local := []HerdrSession{{Name: "default", Socket: link}, {Name: "other", Socket: filepath.Join(dir, "missing.sock")}}
	markCurrentSession(local, real, sameFile)
	if !local[0].Current || local[1].Current {
		t.Errorf("symlinked socket: %+v", local)
	}
}

// fakeSSHFleet puts an ssh on PATH that runs the remote command line locally,
// and a $HOME whose ~/.local/bin is where remoteHerdrShell looks first, so a
// test drives probeHost's real command through both shells. It returns the
// file the fake herdr is written to and a counter of ssh invocations.
func fakeSSHFleet(t *testing.T, sshExit string) (herdrPath string, sshCalls func() int) {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(root, "ssh-calls")
	body := "#!/bin/sh\necho x >> " + shellQuote(count) + "\n"
	if sshExit != "" {
		body += "echo 'ssh: connect to host x port 22: Connection refused' >&2\nexit " + sshExit + "\n"
	} else {
		body += "for last; do :; done\nexec /bin/sh -c \"$last\"\n"
	}
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/sh")
	return filepath.Join(home, ".local", "bin", "herdr"), func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
}

func writeFakeHerdr(t *testing.T, path, sessionList string) {
	t.Helper()
	script := `#!/bin/sh
case "$1 $2" in
"status server") echo '{"running":true,"version":"0.9.3","protocol":22,"socket":"/h/.config/herdr/sessions/work/herdr.sock"}' ;;
"session list") ` + sessionList + ` ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The roster rides the status probe's ssh: one handshake per host per sweep,
// as before sessions were listed.
func TestProbeHostListsSessionsInTheSameRoundTrip(t *testing.T) {
	herdr, calls := fakeSSHFleet(t, "")
	writeFakeHerdr(t, herdr, "echo "+shellQuote(herdrSessionListFixture))

	hi := probeHost(context.Background(), "box", 22)
	if !hi.Reachable || !hi.Running || !hi.Compatible || hi.Err != "" {
		t.Fatalf("probe = %+v, want a usable host", hi)
	}
	if n := calls(); n != 1 {
		t.Errorf("probe ran ssh %d times, want 1", n)
	}
	if len(hi.Sessions) != 3 || hi.SessionsErr != "" {
		t.Fatalf("sessions = %+v (%q)", hi.Sessions, hi.SessionsErr)
	}
	for _, s := range hi.Sessions {
		if s.Current != (s.Name == "work") {
			t.Errorf("%s current = %v; the status probe's socket is work's", s.Name, s.Current)
		}
	}
}

// A herdr too old to list sessions is still a usable host; the roster just
// says why it is missing.
func TestProbeHostWithoutSessionList(t *testing.T) {
	herdr, _ := fakeSSHFleet(t, "")
	writeFakeHerdr(t, herdr, `echo "error: unrecognized subcommand 'session'" >&2; exit 2`)

	hi := probeHost(context.Background(), "box", 22)
	if !hi.Reachable || !hi.Running || !hi.Compatible || hi.Err != "" {
		t.Fatalf("probe = %+v, want a usable host", hi)
	}
	if hi.Sessions != nil || !strings.Contains(hi.SessionsErr, "unrecognized subcommand") {
		t.Errorf("sessions = %+v, err %q", hi.Sessions, hi.SessionsErr)
	}
}

// The roster command's exit code is the one ssh reports now, so the verdicts
// the status probe used to read from it must still come out right.
func TestProbeHostVerdictsSurviveTheRoster(t *testing.T) {
	t.Run("herdr missing", func(t *testing.T) {
		fakeSSHFleet(t, "") // no herdr written
		hi := probeHost(context.Background(), "box", 22)
		if !hi.Reachable || hi.Running || !strings.Contains(hi.Err, "not found") {
			t.Errorf("probe = %+v, want reachable without herdr", hi)
		}
		if hi.Sessions != nil {
			t.Errorf("sessions = %+v on a host without herdr", hi.Sessions)
		}
	})
	t.Run("ssh refused", func(t *testing.T) {
		fakeSSHFleet(t, "255")
		hi := probeHost(context.Background(), "box", 22)
		if hi.Reachable || !strings.Contains(hi.Err, "Connection refused") {
			t.Errorf("probe = %+v, want unreachable", hi)
		}
	})
	t.Run("protocol mismatch", func(t *testing.T) {
		herdr, _ := fakeSSHFleet(t, "")
		writeFakeHerdr(t, herdr, "echo "+shellQuote(herdrSessionListFixture))
		hi := probeHost(context.Background(), "box", 23)
		if !hi.Reachable || !hi.Running || hi.Compatible || hi.Err != "protocol 22 ≠ 23" {
			t.Errorf("probe = %+v, want running but incompatible", hi)
		}
	})
	t.Run("server stopped", func(t *testing.T) {
		herdr, _ := fakeSSHFleet(t, "")
		script := `#!/bin/sh
case "$1 $2" in
"status server") echo '{"running":false,"socket":"/h/.config/herdr/herdr.sock"}'; exit 1 ;;
"session list") echo '{"sessions":[{"name":"default","default":true,"running":false,"socket_path":"/h/.config/herdr/herdr.sock"}]}' ;;
esac
`
		if err := os.WriteFile(herdr, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		hi := probeHost(context.Background(), "box", 22)
		if !hi.Reachable || hi.Running || hi.Err != "herdr not running" {
			t.Errorf("probe = %+v, want reachable, herdr not running", hi)
		}
		if len(hi.Sessions) != 1 || hi.Sessions[0].Running {
			t.Errorf("sessions = %+v", hi.Sessions)
		}
	})
}

// list_hosts (and machine_list over it) carries each host's roster, lasso's
// own box included, straight from the sweep's store.
func TestListHostsShowsSessions(t *testing.T) {
	resetHostStore(t)
	prevProbe, prevHosts, prevLocal := probeHostFn, sshConfigHostsFn, listLocalSessionsFn
	sshConfigHostsFn = func() []string { return []string{"box"} }
	probeHostFn = func(_ context.Context, alias string, _ int) HostInfo {
		return HostInfo{Alias: alias, Reachable: true, Running: true, Compatible: true,
			Sessions: []HerdrSession{{Name: "default", Running: true, Current: true}, {Name: "work", Running: true}}}
	}
	listLocalSessionsFn = func(context.Context) ([]HerdrSession, error) {
		return []HerdrSession{{Name: "default", Default: true, Running: true, Current: true}}, nil
	}
	t.Cleanup(func() {
		drainSweep(t)
		probeHostFn, sshConfigHostsFn, listLocalSessionsFn = prevProbe, prevHosts, prevLocal
		resetHostStore(t)
	})

	_, out, err := listHostsTool(context.Background(), nil, listHostsIn{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	byHost := map[string]hostEntry{}
	for _, h := range out.Hosts {
		byHost[h.Host] = h
	}
	if s := byHost[testHostname].Sessions; len(s) != 1 || !s[0].Current {
		t.Errorf("lasso's own sessions = %+v", s)
	}
	if s := byHost["box"].Sessions; len(s) != 2 || !s[0].Current || s[1].Name != "work" {
		t.Errorf("box sessions = %+v", s)
	}
}

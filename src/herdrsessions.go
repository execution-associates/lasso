package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// A herdr host can run several named sessions, each its own server on its own
// socket (`herdr --session <name>`). lasso drives one per host: the socket
// -herdr-sock names locally, and the one `herdr status server` reports on a
// remote. The roster below lists the others so a caller can see them. The
// design follows herdr-mcp's session routing (Orange-County-AI/herdr-mcp#4, by
// @salmonumbrella).

// HerdrSession is one entry of `herdr session list --json`.
type HerdrSession struct {
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
	Running bool   `json:"running"`
	Socket  string `json:"socket,omitempty"`
	// Current marks the session lasso drives on this host when a call names no
	// session: the one whose socket is lasso's.
	Current bool `json:"current,omitempty"`
}

// herdrSessionsMarker separates the two commands' output in the probe's one ssh
// round trip. A bare word, so it needs no quoting in sh, bash, zsh or fish.
const herdrSessionsMarker = "lasso:herdr-session-list"

// herdrProbeCmd is what probeHost runs remotely: the server status, then the
// session roster. One ssh carries both, so listing sessions costs no extra
// handshake per sweep (each one is a ProxyCommand, a key exchange, and under
// OpenSSH's PerSourcePenalties a killed one counts against this host). The
// roster's stderr goes to stdout so a herdr without `session list` reports why.
const herdrProbeCmd = "herdr status server --json; echo " + herdrSessionsMarker + "; herdr session list --json 2>&1"

// localSessionsTimeout bounds the local `herdr session list`. It runs no ssh,
// so it answers in milliseconds; the bound is for a wedged binary.
const localSessionsTimeout = 5 * time.Second

// decodeHerdrSessions reads `herdr session list --json`. A roster without a
// sessions array, or with an empty or repeated name, is refused: a session is
// addressed by name, so two of one name would make it ambiguous.
func decodeHerdrSessions(out []byte) ([]HerdrSession, error) {
	var roster struct {
		Sessions []struct {
			Name       string `json:"name"`
			Default    bool   `json:"default"`
			Running    bool   `json:"running"`
			SocketPath string `json:"socket_path"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(out, &roster); err != nil {
		if line := firstLine(strings.TrimSpace(string(out))); line != "" && !strings.HasPrefix(line, "{") {
			return nil, errors.New(line) // herdr's own complaint, e.g. an older herdr
		}
		return nil, fmt.Errorf("decode herdr session list: %w", err)
	}
	if roster.Sessions == nil {
		return nil, errors.New("herdr session list has no sessions array; this herdr is too old to list sessions")
	}
	seen := map[string]bool{}
	sessions := make([]HerdrSession, 0, len(roster.Sessions))
	for _, s := range roster.Sessions {
		if s.Name == "" || seen[s.Name] {
			return nil, fmt.Errorf("herdr session list has an empty or repeated session name %q", s.Name)
		}
		seen[s.Name] = true
		sessions = append(sessions, HerdrSession{Name: s.Name, Default: s.Default, Running: s.Running, Socket: s.SocketPath})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions, nil
}

// markCurrentSession flags the session whose socket is the one lasso drives.
// same compares two paths when string equality is not enough (a symlinked
// config dir locally); nil means compare the strings only, as for a remote
// path this process cannot stat.
func markCurrentSession(sessions []HerdrSession, socket string, same func(a, b string) bool) {
	if socket == "" {
		return
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].Socket == socket ||
			(same != nil && sessions[i].Socket != "" && same(sessions[i].Socket, socket))
	}
}

// splitProbeOutput cuts herdrProbeCmd's stdout at the marker. marked reports
// whether the marker was there at all, which proves the remote shell ran.
func splitProbeOutput(out []byte) (status, sessions []byte, marked bool) {
	status, sessions, marked = bytes.Cut(out, []byte(herdrSessionsMarker))
	return bytes.TrimSpace(status), bytes.TrimSpace(sessions), marked
}

// probeSessions fills a probed host's roster from the second half of the probe.
func probeSessions(hi HostInfo, out []byte) HostInfo {
	sessions, err := decodeHerdrSessions(out)
	if err != nil {
		hi.SessionsErr = err.Error()
		return hi
	}
	markCurrentSession(sessions, hi.Socket, nil)
	hi.Sessions = sessions
	return hi
}

// listLocalSessions runs `herdr session list --json` on lasso's own box.
func listLocalSessions(ctx context.Context) ([]HerdrSession, error) {
	ctx, cancel := context.WithTimeout(ctx, localSessionsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, herdrBinary(), "session", "list", "--json")
	cmd.WaitDelay = probeWaitDelay
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("timed out listing local herdr sessions")
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if line := firstLine(strings.TrimSpace(string(ee.Stderr))); line != "" {
				return nil, errors.New(line)
			}
		}
		return nil, fmt.Errorf("herdr session list: %w", err)
	}
	sessions, err := decodeHerdrSessions(out)
	if err != nil {
		return nil, err
	}
	markCurrentSession(sessions, *herdrSock, sameFile)
	return sessions, nil
}

// sameFile reports whether two local paths name one file.
func sameFile(a, b string) bool {
	ai, err := os.Stat(a)
	if err != nil {
		return false
	}
	bi, err := os.Stat(b)
	return err == nil && os.SameFile(ai, bi)
}

// listLocalSessionsFn is the seam tests replace, so a sweep never runs the
// real herdr on whatever box the tests happen to run on.
var listLocalSessionsFn = listLocalSessions

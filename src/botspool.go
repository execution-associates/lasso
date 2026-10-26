package main

// Jobs for a claude bot on another host (docs/design/bots.md, "Jobs on
// another host"). A machine lasso drives is never a client of lasso, so the
// bot's channel cannot long-poll /bot-channel/ from there. Every byte moves
// lasso -> host instead, over the host's Backend (SFTP and the ssh master):
//
//	<dir>/.lasso/channel/inbox/<id>.json   an event, written by lasso (tmp, then rename)
//	<dir>/.lasso/channel/done/<id>.json    the same file, moved there by the channel once delivered
//	<dir>/.lasso/channel/seen              the channel's heartbeat, rewritten every botSpoolBeat
//
// The bot's claude runs the host's own lasso as `lasso channel --bot <name>
// --spool <dir>/.lasso/channel`. It polls inbox, pushes each event into the
// session exactly as the HTTP channel does, and moves the file to done. The
// runner-lock holder's spool loop (botSpoolTick) acks what it finds in done,
// writes newly claimed events into inbox, keeps an event claimed for as long
// as its file waits there, and writes a claimed event again only when its
// file is in neither place after botEventReclaim (a write that failed, a
// file someone removed). Delivery stays at least once.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// The spool loop's period: a remote bot's event reaches its inbox within
	// this, and the channel polls the inbox every second.
	botSpoolEvery = 3 * time.Second
	// How often the channel rewrites its heartbeat; well inside botChannelFresh.
	botSpoolBeat = 10 * time.Second
	// The remote lasso's channel probe is remembered this long.
	botRemoteChannelOK   = 10 * time.Minute
	botRemoteChannelFail = time.Minute
)

func botSpoolDir(dir string) string { return filepath.Join(dir, ".lasso", "channel") }

// botSpoolID reads an event id out of an inbox or done file name. Anything
// else (the channel's own temp files, a stray file) is not an event.
func botSpoolID(name string) (int64, bool) {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok || base == "" || base[0] == '.' {
		return 0, false
	}
	id, err := strconv.ParseInt(base, 10, 64)
	return id, err == nil && id > 0
}

func botSpoolFile(id int64) string { return strconv.FormatInt(id, 10) + ".json" }

// botSpoolBeatTalk reads the heartbeat's capability: a channel with Talk's
// reply tool writes "talk" after its pid. An older one's beat has no such word.
func botSpoolBeatTalk(beat string) bool {
	return slices.Contains(strings.Fields(beat), "talk")
}

// --- the host's lasso -----------------------------------------------------------

type botRemoteChannelProbe struct {
	path string
	err  error
	at   time.Time
}

var botRemoteChannelCache sync.Map // host -> botRemoteChannelProbe

// botRemoteLasso finds the lasso a bot's channel runs on host and checks that
// it knows --spool, answering its path. A var so tests need no ssh.
var botRemoteLasso = func(host string) (string, error) {
	b, err := botBackend(host)
	if err != nil {
		return "", fmt.Errorf("%s is not reachable: %v", host, err)
	}
	rb, ok := b.(*remoteBackend)
	if !ok {
		return "", fmt.Errorf("%s is not a host lasso reaches over ssh", host)
	}
	// A login shell, so lasso is found where the human's shell finds it. The
	// path is marked because a profile may print a line of its own.
	script := `p=$(command -v lasso) || exit 3; printf 'LASSO_PATH=%s\n' "$p"; "$p" channel -h 2>&1; true`
	out, err := rb.runStdin(`${SHELL:-sh} -lc `+shellQuote(script), nil)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return "", fmt.Errorf("jobs need lasso on %s, and it is not installed there", host)
		}
		return "", fmt.Errorf("could not check lasso on %s: %v", host, err)
	}
	path := ""
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "LASSO_PATH="); ok {
			path = p
		}
	}
	if path == "" {
		return "", fmt.Errorf("could not find lasso on %s", host)
	}
	if !strings.Contains(string(out), "-spool") {
		return "", fmt.Errorf("lasso on %s (%s) is too old to deliver jobs: update it there (lasso update)", host, path)
	}
	return path, nil
}

// botRemoteChannel is botRemoteLasso remembered per host, so the Jobs tab and
// every file write do not each cost an ssh round trip.
func botRemoteChannel(host string) (string, error) {
	if v, ok := botRemoteChannelCache.Load(host); ok {
		p := v.(botRemoteChannelProbe)
		ttl := botRemoteChannelOK
		if p.err != nil {
			ttl = botRemoteChannelFail
		}
		if time.Since(p.at) < ttl {
			return p.path, p.err
		}
	}
	path, err := botRemoteLasso(host)
	botRemoteChannelCache.Store(host, botRemoteChannelProbe{path: path, err: err, at: time.Now()})
	return path, err
}

// --- lasso's half: the spool loop ------------------------------------------------

func startBotSpoolLoop(ctx context.Context) {
	t := time.NewTicker(botSpoolEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		botSpoolTick(time.Now())
	}
}

var (
	botSpoolBusy    sync.Map // bot -> struct{}: a pass is running
	botSpoolLastErr sync.Map // bot -> string: the last error logged
	botSpoolBeats   sync.Map // bot -> string: the heartbeat last read
)

// botSpoolTick runs one pass for every running claude bot on another host,
// each in its own goroutine so a slow host holds up only its own bots.
func botSpoolTick(now time.Time) {
	if !botsOwnLoop() {
		return
	}
	list, err := listBots()
	if err != nil {
		return
	}
	for _, r := range list {
		if r.Host == "local" || r.isOmp() || r.Stopped {
			continue
		}
		if _, busy := botSpoolBusy.LoadOrStore(r.Name, struct{}{}); busy {
			continue
		}
		go func(r *botRecord) {
			defer botSpoolBusy.Delete(r.Name)
			if ok, _ := botChannelOffered(r); !ok {
				return
			}
			b, err := botBackend(r.Host)
			if err == nil {
				err = botSpoolSync(b, r, now)
			}
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if prev, _ := botSpoolLastErr.Swap(r.Name, msg); prev != msg && msg != "" {
				log.Printf("bots:     %s's job spool on %s: %v", r.Name, r.Host, err)
			}
		}(r)
	}
}

// botSpoolSync is one pass over a remote bot's spool: acks from done, the
// heartbeat, then inbox reconciled against the bot's claimed events.
func botSpoolSync(b Backend, r *botRecord, now time.Time) error {
	dir := expandTildeOn(b, r.Dir)
	if st, err := b.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("the bot's folder %s does not exist", dir)
	}
	spool := botSpoolDir(dir)
	inbox, done := filepath.Join(spool, "inbox"), filepath.Join(spool, "done")

	// Delivered: ack, then clear. The ack goes first, so a lasso that dies
	// between the two finds the file again and acks a delivered event, which
	// changes nothing.
	if ents, err := b.ReadDir(done); err == nil {
		for _, e := range ents {
			id, ok := botSpoolID(e.Name)
			if !ok {
				continue
			}
			if err := ackBotEvents(r.Name, []int64{id}, now); err != nil {
				return err
			}
			if err := b.RemoveAll(filepath.Join(done, e.Name)); err != nil {
				return err
			}
		}
	}

	// Talk replies the channel left: recorded, then removed.
	if err := botSpoolCollectReplies(b, r.Name, spool, now); err != nil {
		return err
	}

	if beat, err := b.ReadFile(filepath.Join(spool, "seen")); err == nil {
		// Changed since the last read means the channel is alive now. Its
		// own clock never counts: the host's may be off.
		if prev, known := botSpoolBeats.Swap(r.Name, string(beat)); known && prev.(string) != string(beat) {
			botChannelSeen(r.Name, now, botSpoolBeatTalk(string(beat)))
		}
	}

	ents, err := b.ReadDir(inbox)
	if err != nil {
		if mkErr := b.MkdirAll(inbox, 0o700); mkErr != nil {
			return fmt.Errorf("spool: %v", err)
		}
		if ents, err = b.ReadDir(inbox); err != nil {
			return fmt.Errorf("spool: %v", err)
		}
	}
	waiting := map[int64]string{}
	for _, e := range ents {
		if id, ok := botSpoolID(e.Name); ok {
			waiting[id] = e.Name
		}
	}
	claimed, err := listClaimedBotEvents(r.Name)
	if err != nil {
		return err
	}
	var still, again []int64
	for _, c := range claimed {
		if _, ok := waiting[c.ID]; ok {
			still = append(still, c.ID)
			delete(waiting, c.ID)
			continue
		}
		if at, ok := parseStamp(c.ClaimedAt); !ok || now.Sub(at) >= botEventReclaim {
			again = append(again, c.ID)
		}
	}
	// A file whose event is no longer claimed (dropped after a day, say)
	// must not be delivered late.
	for _, name := range waiting {
		_ = b.RemoveAll(filepath.Join(inbox, name))
	}
	// Waiting in the inbox is in flight, however long it takes.
	if err := touchBotEventClaims(still, now); err != nil {
		return err
	}
	var out []botEvent
	if len(again) > 0 {
		evs, err := reclaimBotEvents(r.Name, again, now)
		if err != nil {
			return err
		}
		out = append(out, evs...)
	}
	evs, err := claimPendingBotEvents(r.Name, now)
	if err != nil {
		return err
	}
	out = append(out, evs...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for _, e := range out {
		body, err := json.Marshal(e)
		if err != nil {
			return err
		}
		name := botSpoolFile(e.ID)
		tmp := filepath.Join(inbox, "."+name+".tmp")
		if err := b.WriteFile(tmp, body, 0o600); err != nil {
			return err
		}
		if err := b.Rename(tmp, filepath.Join(inbox, name)); err != nil {
			_ = b.RemoveAll(tmp)
			return err
		}
	}
	return nil
}

// --- the queue's spool side ------------------------------------------------------

type botClaim struct {
	ID        int64
	ClaimedAt string
}

func listClaimedBotEvents(bot string) ([]botClaim, error) {
	rows, err := db.Query(`SELECT id, claimed_at FROM bot_events WHERE bot = ? AND status = 'claimed' ORDER BY id`, bot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []botClaim
	for rows.Next() {
		var c botClaim
		if err := rows.Scan(&c.ID, &c.ClaimedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func touchBotEventClaims(ids []int64, now time.Time) error {
	for _, id := range ids {
		if _, err := db.Exec(`UPDATE bot_events SET claimed_at = ? WHERE id = ? AND status = 'claimed'`, nowStamp(now), id); err != nil {
			return err
		}
	}
	return nil
}

// claimPendingBotEvents is claimBotEvents without the reclaim: on another host
// a claim stays in flight while its file waits in the inbox, and the spool
// loop decides itself when one needs writing again.
func claimPendingBotEvents(bot string, now time.Time) ([]botEvent, error) {
	return claimBotEventsWhere(bot, now, `status = 'pending'`)
}

// reclaimBotEvents re-reads claimed events for writing again.
func reclaimBotEvents(bot string, ids []int64, now time.Time) ([]botEvent, error) {
	list := make([]string, len(ids))
	for i, id := range ids {
		list[i] = strconv.FormatInt(id, 10)
	}
	return claimBotEventsWhere(bot, now, `status = 'claimed' AND id IN (`+strings.Join(list, ",")+`)`)
}

// --- watches on another host ------------------------------------------------------

// botExecRemote runs a watch's command on the bot's host. A var so tests need
// no ssh.
var botExecRemote = execBotCommandRemote

// execBotCommandRemote runs command in dir on b's host over the ssh master,
// with the environment botCommandEnv gives a local watch, built from the
// host's own values (never lasso's). The host's timeout(1), when it has one,
// bounds the run there and kills its process group; the ssh end is bounded
// too, so a host without it still cannot hold lasso's side.
func execBotCommandRemote(b Backend, command, dir, bot, job string, timeout time.Duration) botExecResult {
	start := time.Now()
	rb, ok := b.(*remoteBackend)
	if !ok {
		return botExecResult{Err: fmt.Errorf("%s is not a host lasso reaches over ssh", b.Name())}
	}
	if st, err := b.Stat(dir); err != nil || !st.IsDir() {
		return botExecResult{Err: fmt.Errorf("the bot's folder %s does not exist on %s", dir, b.Name())}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+15*time.Second)
	defer cancel()
	stdout, stderr := &headBuffer{max: botRunBytes}, &headBuffer{max: botRunBytes}
	err := rb.runStreams(ctx, "/bin/sh -c "+shellQuote(botRemoteCommandScript(command, dir, bot, job, timeout)), stdout, stderr)
	res := botExecResult{Stdout: stdout.String(), Stderr: stderr.String(), Took: time.Since(start)}
	if ctx.Err() != nil {
		res.TimedOut = true
		return res
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			res.Err = err
			return res
		}
		res.Exit = ee.ExitCode()
		switch {
		case res.Exit == 124 && res.Took >= timeout:
			res.TimedOut = true
		case res.Exit == 255:
			// ssh's own failure: the command did not run.
			res.Err = fmt.Errorf("ssh %s: %s", b.Name(), lastLines(strings.TrimSpace(res.Stderr), 3))
		}
	}
	return res
}

// botRemoteCommandScript is the host-side sh script for one watch run: into
// the folder, then env -i with a systemd-user-like environment, under
// timeout(1) when the host has it.
func botRemoteCommandScript(command, dir, bot, job string, timeout time.Duration) string {
	secs := max(1, int(timeout/time.Second))
	return strings.Join([]string{
		`cd ` + shellQuote(dir) + ` || exit 125`,
		`set --`,
		`for k in SHELL LANG LC_ALL LC_CTYPE TZ TMPDIR XDG_CONFIG_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_STATE_HOME MISE_DATA_DIR MISE_CONFIG_DIR MISE_CACHE_DIR MISE_STATE_DIR DBUS_SESSION_BUS_ADDRESS SSH_AUTH_SOCK OP_VAULT; do eval "v=\${$k-}"; [ -n "$v" ] && set -- "$@" "$k=$v"; done`,
		`rt=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}; [ -d "$rt" ] && set -- "$@" "XDG_RUNTIME_DIR=$rt"`,
		`u=${USER:-${LOGNAME:-$(id -un)}}`,
		`md=${MISE_DATA_DIR:-$HOME/.local/share/mise}`,
		`p="$HOME/.local/bin:$md/shims:${PATH:-/usr/bin:/bin}:/usr/local/bin:/usr/bin:/bin"`,
		`t=; command -v timeout >/dev/null 2>&1 && t="timeout -k 5 ` + strconv.Itoa(secs) + `"`,
		`exec env -i HOME="$HOME" USER="$u" LOGNAME="$u" PATH="$p" LASSO_BOT=` + shellQuote(bot) + ` LASSO_JOB=` + shellQuote(job) + ` "$@" $t /bin/sh -c ` + shellQuote(command),
	}, "\n")
}

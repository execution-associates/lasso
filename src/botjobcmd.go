package main

// Watches: a bot job with a command (docs/design/bots.md, "Watches"). Each
// firing runs the command in the bot's folder, and only what it prints
// becomes an event:
//
//   - exit 0, nothing on stdout: nothing is delivered. The run is recorded on
//     the job (last_run_*) so the Jobs tab can say "checked 3m ago, quiet",
//     but it adds nothing to the history. Silence is the feature.
//   - exit 0 with output: an event whose body is the job's message (a
//     standing preamble) followed by the output.
//   - a non-zero exit, a timeout, or a command that could not start: a
//     failure report, damped to the 1st, 2nd, 4th, 8th... consecutive
//     failure, and one recovery notice when it works again. The event carries
//     run_status (error or timeout) so the bot reads it as a diagnostic.
//
// Firings that pile up while the bot is busy are different events, not
// repeats: a run with output appends to the job's undelivered watch event
// under its own header instead of replacing it (bounded, oldest first out,
// the loss stated in the body). These are everloop's --command semantics.
//
// The command runs as lasso's user with a small environment, like a systemd
// user unit's (botCommandEnv): never lasso's own credentials. Scripts fetch
// their own secrets.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	botCommandMax            = 4000
	botCommandTimeoutDefault = 60
	botCommandTimeoutMax     = 3600
	// Output bounds: one run's stdout and stderr each, and one accumulated
	// event. A chatty command meeting a long outage must not grow the queue
	// without limit.
	botRunBytes      = 16 << 10
	botWatchMaxBytes = 64 << 10
	botWatchMaxRuns  = 20
)

// botRunNowWait is how long Run now waits for a watch's command before
// answering "running"; a test shortens it.
var botRunNowWait = 20 * time.Second

// watchRun is one firing worth delivering. Status is "" for output, error or
// timeout for a reported failure, recovered for the one notice that a broken
// watch works again.
type watchRun struct {
	At     string `json:"at"`
	Text   string `json:"text"`
	Status string `json:"status,omitempty"`
	Exit   int    `json:"exit,omitempty"`
}

// watchRuns is what bot_events.runs holds for a watch event: the runs behind
// its rendered content, so a later firing can append without re-parsing it.
type watchRuns struct {
	Runs    []watchRun `json:"runs"`
	Dropped int        `json:"dropped,omitempty"`
}

// status is the event's run_status: the newest run that changed the watch's
// health, so an event that holds failures and then a recovery reads healthy.
func (r watchRuns) status() string {
	last := ""
	for _, run := range r.Runs {
		if run.Status != "" {
			last = run.Status
		}
	}
	if last == "recovered" {
		return ""
	}
	return last
}

// bound drops the oldest runs past the caps, counting them.
func (r *watchRuns) bound() {
	total := 0
	for _, run := range r.Runs {
		total += len(run.Text)
	}
	for len(r.Runs) > 1 && (len(r.Runs) > botWatchMaxRuns || total > botWatchMaxBytes) {
		total -= len(r.Runs[0].Text)
		r.Runs = r.Runs[1:]
		r.Dropped++
	}
}

// render is the delivered body: the job's message as a preamble (left off an
// event that is only failure reports, where "judge this" would apply to a
// stack trace), then each run in firing order, under a header once there is
// more than one.
func (r watchRuns) render(preamble string) string {
	hasOutput := false
	for _, run := range r.Runs {
		if run.Status == "" {
			hasOutput = true
		}
	}
	var b strings.Builder
	if preamble != "" && hasOutput {
		b.WriteString(preamble + "\n")
	}
	if r.Dropped > 0 {
		fmt.Fprintf(&b, "\n[%d earlier run(s) dropped to keep this event bounded]\n", r.Dropped)
	}
	for i, run := range r.Runs {
		switch {
		case len(r.Runs) > 1:
			fmt.Fprintf(&b, "\n[run %d of %d, %s]\n", i+1, len(r.Runs), run.At)
		case b.Len() > 0:
			b.WriteString("\n")
		}
		b.WriteString(run.Text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- running the command -------------------------------------------------------

type botExecResult struct {
	Stdout, Stderr string
	Exit           int
	TimedOut       bool
	Err            error // could not run at all (no folder, no sh), not a command failure
	Took           time.Duration
}

func (r botExecResult) failed() bool { return r.Err != nil || r.TimedOut || r.Exit != 0 }

func (r botExecResult) status() string {
	if r.TimedOut {
		return "timeout"
	}
	return "error"
}

// headBuffer keeps the first max bytes written and counts the rest, so a
// command that dumps a megabyte cannot become the event (or lasso's memory).
type headBuffer struct {
	max     int
	buf     bytes.Buffer
	dropped int
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - h.buf.Len(); room > 0 {
		h.buf.Write(p[:min(room, len(p))])
		h.dropped += max(0, len(p)-room)
	} else {
		h.dropped += len(p)
	}
	return len(p), nil
}

func (h *headBuffer) String() string {
	s := strings.ToValidUTF8(h.buf.String(), "")
	if h.dropped > 0 {
		s += fmt.Sprintf("\n[output truncated, %d bytes dropped]", h.dropped)
	}
	return s
}

// execBotCommand runs command under sh -c in dir with a bounded lifetime. The
// command gets its own process group and a timeout kills the group, not just
// the shell: a hung curl inside a pipeline would otherwise keep the pipe open
// and the run alive. WaitDelay is the backstop for a grandchild that holds
// the pipe after the group is gone.
func execBotCommand(command, dir string, env []string, timeout time.Duration) botExecResult {
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = browserSysProcAttr() // own group + Pdeathsig, like every child
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout, stderr := &headBuffer{max: botRunBytes}, &headBuffer{max: botRunBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	res := botExecResult{Stdout: stdout.String(), Stderr: stderr.String(), Took: time.Since(start)}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		return res
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.Exit = ee.ExitCode()
		} else {
			res.Err = err
		}
	}
	return res
}

// botCommandEnv is a watch's environment: roughly what a systemd user unit
// gets (HOME, USER, PATH, XDG_RUNTIME_DIR, the session bus, the ssh agent,
// OP_VAULT naming the 1Password vault `secret` reads) and nothing of lasso's
// own (UI_AUTH, MCP_OAUTH, LASSO_MCP_TOKEN are never on the list). PATH leads
// with ~/.local/bin and mise's shims, so a script finds the tools a login
// shell would without sourcing a profile. LASSO_BOT and LASSO_JOB name the
// run.
func botCommandEnv(env []string, bot, job string) []string {
	out := minimalChildEnv(env)
	extra := map[string]bool{"XDG_RUNTIME_DIR": true, "DBUS_SESSION_BUS_ADDRESS": true, "SSH_AUTH_SOCK": true, "OP_VAULT": true}
	vals := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if extra[k] {
			out = append(out, kv)
		}
		vals[k] = v
	}
	set := func(k, v string) {
		for i, kv := range out {
			if strings.HasPrefix(kv, k+"=") {
				out[i] = k + "=" + v
				return
			}
		}
		out = append(out, k+"="+v)
	}
	home := vals["HOME"]
	if home == "" {
		home, _ = os.UserHomeDir()
		set("HOME", home)
	}
	if vals["USER"] == "" {
		if u := vals["LOGNAME"]; u != "" {
			set("USER", u)
		} else if home != "" {
			set("USER", filepath.Base(home))
		}
	}
	if vals["XDG_RUNTIME_DIR"] == "" {
		if d := "/run/user/" + strconv.Itoa(os.Getuid()); dirExists(d) {
			set("XDG_RUNTIME_DIR", d)
		}
	}
	miseData := vals["MISE_DATA_DIR"]
	if miseData == "" && home != "" {
		miseData = filepath.Join(home, ".local", "share", "mise")
	}
	var path []string
	seen := map[string]bool{}
	add := func(dirs ...string) {
		for _, d := range dirs {
			if d != "" && !seen[d] {
				seen[d] = true
				path = append(path, d)
			}
		}
	}
	if home != "" {
		add(filepath.Join(home, ".local", "bin"))
	}
	if miseData != "" {
		add(filepath.Join(miseData, "shims"))
	}
	add(filepath.SplitList(vals["PATH"])...)
	add("/usr/local/bin", "/usr/bin", "/bin")
	set("PATH", strings.Join(path, string(os.PathListSeparator)))
	set("LASSO_BOT", bot)
	set("LASSO_JOB", job)
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (j *botJob) timeout() time.Duration {
	if j.Timeout <= 0 {
		return botCommandTimeoutDefault * time.Second
	}
	return time.Duration(j.Timeout) * time.Second
}

// --- one firing ------------------------------------------------------------------

// botCmdOutcome is what one firing of a watch came to.
type botCmdOutcome struct {
	Result  string // quiet | output | error | timeout | dropped
	EventID int64  // the event it queued or merged into, 0 for none
	Exit    int
	// Reported: a failure that reached the bot (not damped).
	Reported bool
	Detail   string
}

// botJobRunning holds the ids of the jobs whose command this lasso is running.
var botJobRunning sync.Map // int64 -> chan struct{} (closed when done)

func botJobCommandRunning(id int64) bool {
	_, ok := botJobRunning.Load(id)
	return ok
}

// startBotJobCommand runs j's command in the background unless a run of it is
// already going, answering a channel that gets the outcome (nil when busy).
func startBotJobCommand(j *botJob, kind string) <-chan botCmdOutcome {
	done := make(chan struct{})
	if _, busy := botJobRunning.LoadOrStore(j.ID, done); busy {
		return nil
	}
	out := make(chan botCmdOutcome, 1)
	go func() {
		defer func() {
			botJobRunning.Delete(j.ID)
			close(done)
		}()
		o, err := runBotJobCommand(j, kind, time.Now())
		if err != nil {
			log.Printf("bots:     job %s/%s: %v", j.Bot, j.Name, err)
		}
		out <- o
	}()
	return out
}

// runBotJobCommand is one firing of a watch, start to finish: run the command
// (unless the bot is stopped), update the job's run state and damping, and
// queue whatever is worth delivering.
func runBotJobCommand(j *botJob, kind string, now time.Time) (botCmdOutcome, error) {
	rec, err := getBot(j.Bot)
	if err != nil {
		return botCmdOutcome{}, err
	}
	if rec.Stopped {
		// Not run at all: a watch that consumed a change (most keep state)
		// while nobody was listening would lose it.
		id, _, err := enqueueBotEvent(j, kind, j.Message, "", now)
		return botCmdOutcome{Result: "dropped", EventID: id}, err
	}
	// The stored folder may be "~/…": expand it against the bot's host, as
	// every other bot path does. exec reports a missing Dir as a missing
	// /bin/sh, so say what is actually wrong.
	dir := rec.Dir
	if b, err := botBackend(rec.Host); err == nil {
		dir = expandTildeOn(b, dir)
	}
	var res botExecResult
	if dirExists(dir) {
		res = execBotCommand(j.Command, dir, botCommandEnv(os.Environ(), j.Bot, j.Name), j.timeout())
	} else {
		res = botExecResult{Err: fmt.Errorf("the bot's folder %s does not exist", dir)}
	}
	end := now.Add(res.Took)
	stamp := nowStamp(end)

	// The streak is read fresh: j may be a snapshot from before an earlier run.
	var streak, reported int
	_ = db.QueryRow(`SELECT fail_streak, fail_reported FROM bot_jobs WHERE id = ?`, j.ID).Scan(&streak, &reported)
	var runs []watchRun
	o := botCmdOutcome{Exit: res.Exit}
	if res.failed() {
		streak++
		o.Result = res.status()
		o.Detail = botFailureDetail(res)
		if botShouldReport(streak) {
			reported = streak
			o.Reported = true
			runs = append(runs, watchRun{At: stamp, Status: res.status(), Exit: res.Exit, Text: botFailureText(j, res, streak)})
		}
	} else {
		if reported > 0 {
			// One event: the bot was told the watch was broken and would
			// otherwise never learn that it is not.
			runs = append(runs, watchRun{At: stamp, Status: "recovered",
				Text: fmt.Sprintf("Job %q: the command works again after %d consecutive failures.", j.Name, streak)})
		}
		streak, reported = 0, 0
		o.Result = "quiet"
		if out := strings.TrimRight(res.Stdout, "\n"); strings.TrimSpace(out) != "" {
			o.Result = "output"
			runs = append(runs, watchRun{At: stamp, Text: out})
		}
	}
	note := ""
	if res.failed() {
		note = o.Detail
	}
	if _, err := db.Exec(`UPDATE bot_jobs SET fail_streak = ?, fail_reported = ?, last_run_at = ?, last_run_result = ?, last_run_exit = ?, last_run_ms = ?, last_run_note = ?
		WHERE id = ?`, streak, reported, stamp, o.Result, res.Exit, res.Took.Milliseconds(), note, j.ID); err != nil {
		return o, err
	}
	if len(runs) == 0 {
		return o, nil
	}
	id, status, err := enqueueBotRuns(j, kind, runs, end)
	o.EventID = id
	if status == "dropped" {
		o.Result = "dropped"
	}
	return o, err
}

// botShouldReport damps a broken command to the 1st, 2nd, 4th, 8th...
// consecutive failure: prompt when a break is new, a trickle when it lasts.
func botShouldReport(n int) bool { return n > 0 && n&(n-1) == 0 }

func botNextReportAt(n int) int {
	next := 1
	for next <= n {
		next <<= 1
	}
	return next
}

// botFailureText renders one reported failure. stderr is in it because that
// is where a broken environment says so ("uv: not found").
func botFailureText(j *botJob, res botExecResult, streak int) string {
	var b strings.Builder
	switch {
	case res.Err != nil:
		fmt.Fprintf(&b, "Job %q: the command could not run: %v", j.Name, res.Err)
	case res.TimedOut:
		fmt.Fprintf(&b, "Job %q: the command timed out after %s", j.Name, j.timeout())
	default:
		fmt.Fprintf(&b, "Job %q: the command failed (exit %d)", j.Name, res.Exit)
	}
	fmt.Fprintf(&b, ". Consecutive failure %d; reports are damped, the next comes at failure %d.\n", streak, botNextReportAt(streak))
	fmt.Fprintf(&b, "$ %s\n", j.Command)
	if s := strings.TrimSpace(res.Stderr); s != "" {
		b.WriteString(s + "\n")
	}
	if s := strings.TrimSpace(res.Stdout); s != "" {
		b.WriteString(s + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// botFailureDetail is the short line the Jobs tab and Run now show for a
// failure: the error, else the last line of stderr.
func botFailureDetail(res botExecResult) string {
	var s string
	switch {
	case res.Err != nil:
		s = res.Err.Error()
	case res.TimedOut:
		s = "timed out"
	default:
		lines := strings.Split(strings.TrimSpace(res.Stderr), "\n")
		s = strings.TrimSpace(lines[len(lines)-1])
	}
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "…"
	}
	return s
}

// enqueueBotRuns queues a watch's runs: appended to the job's undelivered
// watch event when there is one (each run stays its own section), else a new
// event. A bot stopped while the command ran gets the firing dropped.
func enqueueBotRuns(j *botJob, kind string, runs []watchRun, now time.Time) (int64, string, error) {
	stamp := nowStamp(now)
	if rec, err := getBot(j.Bot); err != nil {
		return 0, "", err
	} else if rec.Stopped {
		rs := watchRuns{Runs: runs}
		res, err := db.Exec(`INSERT INTO bot_events (bot, job_id, job, kind, content, count, status, reason, created_at, fired_at, done_at, runs, run_status)
			VALUES (?, ?, ?, ?, ?, ?, 'dropped', 'the bot was stopped', ?, ?, ?, ?, ?)`,
			j.Bot, j.ID, j.Name, kind, rs.render(j.Message), len(runs), stamp, stamp, stamp, string(mustJSON(rs)), rs.status())
		if err != nil {
			return 0, "", err
		}
		id, _ := res.LastInsertId()
		return id, "dropped", nil
	}
	var id int64
	var raw string
	err := db.QueryRow(`SELECT id, runs FROM bot_events WHERE job_id = ? AND status = 'pending' AND kind IN ('schedule', 'run') AND runs != '' ORDER BY id LIMIT 1`, j.ID).Scan(&id, &raw)
	if err == nil {
		var rs watchRuns
		_ = json.Unmarshal([]byte(raw), &rs)
		rs.Runs = append(rs.Runs, runs...)
		rs.bound()
		if _, err := db.Exec(`UPDATE bot_events SET count = ?, fired_at = ?, content = ?, runs = ?, run_status = ? WHERE id = ?`,
			len(rs.Runs), stamp, rs.render(j.Message), string(mustJSON(rs)), rs.status(), id); err != nil {
			return 0, "", err
		}
		botEventNotify(j.Bot)
		return id, "pending", nil
	}
	rs := watchRuns{Runs: runs}
	rs.bound()
	res, err := db.Exec(`INSERT INTO bot_events (bot, job_id, job, kind, content, count, status, created_at, fired_at, runs, run_status)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?)`,
		j.Bot, j.ID, j.Name, kind, rs.render(j.Message), len(rs.Runs), stamp, stamp, string(mustJSON(rs)), rs.status())
	if err != nil {
		return 0, "", err
	}
	id, _ = res.LastInsertId()
	botEventQueued(j.Bot, stamp)
	return id, "pending", nil
}

// --- Run now -----------------------------------------------------------------------

// botRunNowResult answers Run now. Status is pending (queued for the bot) or
// dropped (the bot is stopped) for any job; a watch adds quiet (the command
// printed nothing, so nothing was delivered), failed (Exit and Detail say
// how; EventID is set when the failure was reported rather than damped),
// running (still going after botRunNowWait; it delivers only if it prints)
// and busy (a run was already going).
type botRunNowResult struct {
	EventID int64  `json:"event_id"`
	Status  string `json:"status"`
	Exit    int    `json:"exit,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func runBotJobCommandNow(j *botJob) botRunNowResult {
	ch := startBotJobCommand(j, "run")
	if ch == nil {
		return botRunNowResult{Status: "busy"}
	}
	select {
	case o := <-ch:
		r := botRunNowResult{EventID: o.EventID, Exit: o.Exit, Detail: o.Detail}
		switch o.Result {
		case "output":
			r.Status = "pending"
		case "error", "timeout":
			r.Status = "failed"
		default:
			r.Status = o.Result // quiet | dropped
		}
		return r
	case <-time.After(botRunNowWait):
		return botRunNowResult{Status: "running"}
	}
}

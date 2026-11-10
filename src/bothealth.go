package main

// Bot liveness: whether each bot can actually do its job, decided on the bot
// runner's tick from what lasso already sees (its pane, Claude Code's session
// registry, the channel's polls, the transcript, an omp bridge's answers), and
// alerts on the transitions. See docs/design/bot-alerts.md.
//
// Health states:
//
//	ok        up and hearing its channel
//	starting  inside the start grace lasso gave a launch
//	stopped   the human's Stop: never alerts
//	gone      no pane (claude) / no pane and no bridge (omp)
//	shell     a pane with no agent in it: herdr restored a plain shell
//	deaf      up, but cannot hear its channel (reasons below)
//	unknown   the bot's host did not answer: down only after 3 × down_after
//
// A hold (hold_until) is a deliberate down: gone and shell page nobody and
// keep-running does not relaunch, but a bot that is up and deaf still alerts.
// Only the runner-lock holder evaluates and alerts; every lasso on the db reads
// the stored result.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const botAlertSchema = `
CREATE TABLE IF NOT EXISTS bot_alert_state (
	bot             TEXT PRIMARY KEY,
	state           TEXT NOT NULL DEFAULT '',
	reason          TEXT NOT NULL DEFAULT '',
	since           TEXT NOT NULL DEFAULT '',
	down            INTEGER NOT NULL DEFAULT 0,
	down_since      TEXT NOT NULL DEFAULT '',
	down_reason     TEXT NOT NULL DEFAULT '',
	last_sent_at    TEXT NOT NULL DEFAULT '',
	reminders       INTEGER NOT NULL DEFAULT 0,
	bad_since       TEXT NOT NULL DEFAULT '',
	good_since      TEXT NOT NULL DEFAULT '',
	updated_at      TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS alert_channels (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	name         TEXT NOT NULL,
	kind         TEXT NOT NULL,
	config       TEXT NOT NULL DEFAULT '{}',
	secret       TEXT NOT NULL DEFAULT '',
	enabled      INTEGER NOT NULL DEFAULT 1,
	last_test_at TEXT NOT NULL DEFAULT '',
	last_error   TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS alert_deliveries (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	at           TEXT NOT NULL,
	bot          TEXT NOT NULL DEFAULT '',
	channel_id   INTEGER NOT NULL DEFAULT 0,
	channel_name TEXT NOT NULL DEFAULT '',
	channel_kind TEXT NOT NULL DEFAULT '',
	event        TEXT NOT NULL,
	status       TEXT NOT NULL,
	error        TEXT NOT NULL DEFAULT '',
	attempts     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS alert_deliveries_at ON alert_deliveries(id DESC);
`

// --- settings ------------------------------------------------------------------

// alertSettings is the global half, the `alerts` settings key. Durations are
// seconds.
type alertSettings struct {
	Enabled      bool    `json:"enabled"`
	Channels     []int64 `json:"channels"`
	DownAfter    int     `json:"down_after"`
	UpAfter      int     `json:"up_after"`
	RemindEvery  int     `json:"remind_every"`
	RemindMax    int     `json:"remind_max"`
	HandleWithin int     `json:"handle_within"`
	// LinkBase makes an alert's link to the bot absolute, e.g.
	// https://lasso.example.com; "" sends no link.
	LinkBase string `json:"link_base"`
}

var alertDefaults = alertSettings{
	Enabled:      true,
	Channels:     []int64{},
	DownAfter:    120,
	UpAfter:      60,
	RemindEvery:  0,
	RemindMax:    6,
	HandleWithin: 300,
}

const alertSettingsKey = "alerts"

func loadAlertSettings() alertSettings {
	s := alertDefaults
	s.Channels = []int64{}
	if raw, _ := getSetting(alertSettingsKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &s)
	}
	if s.Channels == nil {
		s.Channels = []int64{}
	}
	return s
}

func (s *alertSettings) validate() error {
	if err := checkAlertDur("down_after", s.DownAfter, 0, 86400); err != nil {
		return err
	}
	if err := checkAlertDur("up_after", s.UpAfter, 0, 86400); err != nil {
		return err
	}
	if s.RemindEvery != 0 {
		if err := checkAlertDur("remind_every", s.RemindEvery, 300, 7*86400); err != nil {
			return err
		}
	}
	if s.RemindMax < 0 || s.RemindMax > 100 {
		return errors.New("remind_max must be 0-100")
	}
	if err := checkAlertDur("handle_within", s.HandleWithin, 60, 86400); err != nil {
		return err
	}
	if len(s.Channels) > 32 {
		return errors.New("at most 32 default channels")
	}
	s.LinkBase = strings.TrimRight(strings.TrimSpace(s.LinkBase), "/")
	if s.LinkBase != "" {
		if err := checkAlertURL(s.LinkBase); err != nil {
			return fmt.Errorf("link_base: %w", err)
		}
	}
	if s.Channels == nil {
		s.Channels = []int64{}
	}
	return nil
}

func checkAlertDur(name string, v, lo, hi int) error {
	if v < lo || v > hi {
		return fmt.Errorf("%s must be %d-%d seconds", name, lo, hi)
	}
	return nil
}

// botAlerts is a bot's own half, the bots.alerts column. Mode "" inherits the
// global channels, "off" sends nothing (a heartbeat bound to the bot still
// tells the truth), "custom" sends to Channels instead. A nil threshold
// inherits the global one.
type botAlerts struct {
	Mode         string           `json:"mode,omitempty"`
	Channels     []int64          `json:"channels,omitempty"`
	DownAfter    *int             `json:"down_after,omitempty"`
	UpAfter      *int             `json:"up_after,omitempty"`
	RemindEvery  *int             `json:"remind_every,omitempty"`
	HandleWithin *int             `json:"handle_within,omitempty"`
	HealthChecks []botHealthCheck `json:"health_checks,omitempty"`
}

// botHealthCheck is an HTTP endpoint the bot depends on, such as a channel
// bridge's /health: down on anything but a 2xx, or, with JSONField, unless that
// top-level field of the JSON answer is true (e.g. polling_ok).
type botHealthCheck struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	JSONField string `json:"json_field,omitempty"`
}

var botHealthCheckNameRE = regexp.MustCompile(`^[A-Za-z0-9 _.-]{1,40}$`)
var jsonFieldRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func (a *botAlerts) normalize() error {
	switch a.Mode {
	case "", "inherit":
		a.Mode = ""
	case "off", "custom":
	default:
		return errors.New("alerts mode must be inherit, off or custom")
	}
	if len(a.Channels) > 32 {
		return errors.New("at most 32 alert channels")
	}
	if a.Mode != "custom" {
		a.Channels = nil
	}
	if a.DownAfter != nil {
		if err := checkAlertDur("down_after", *a.DownAfter, 0, 86400); err != nil {
			return err
		}
	}
	if a.UpAfter != nil {
		if err := checkAlertDur("up_after", *a.UpAfter, 0, 86400); err != nil {
			return err
		}
	}
	if a.RemindEvery != nil && *a.RemindEvery != 0 {
		if err := checkAlertDur("remind_every", *a.RemindEvery, 300, 7*86400); err != nil {
			return err
		}
	}
	if a.HandleWithin != nil {
		if err := checkAlertDur("handle_within", *a.HandleWithin, 60, 86400); err != nil {
			return err
		}
	}
	if len(a.HealthChecks) > 8 {
		return errors.New("at most 8 health checks")
	}
	for i := range a.HealthChecks {
		c := &a.HealthChecks[i]
		c.Name, c.URL, c.JSONField = strings.TrimSpace(c.Name), strings.TrimSpace(c.URL), strings.TrimSpace(c.JSONField)
		if c.Name == "" {
			c.Name = fmt.Sprintf("check %d", i+1)
		}
		if !botHealthCheckNameRE.MatchString(c.Name) {
			return fmt.Errorf("health check name %q must be 1-40 letters, digits, spaces, _ . or -", c.Name)
		}
		if err := checkAlertURL(c.URL); err != nil {
			return fmt.Errorf("health check %q: %w", c.Name, err)
		}
		if c.JSONField != "" && !jsonFieldRE.MatchString(c.JSONField) {
			return fmt.Errorf("health check %q: json_field must be a plain field name", c.Name)
		}
	}
	return nil
}

func (a *botAlerts) empty() bool {
	return a.Mode == "" && len(a.Channels) == 0 && a.DownAfter == nil && a.UpAfter == nil &&
		a.RemindEvery == nil && a.HandleWithin == nil && len(a.HealthChecks) == 0
}

func botAlertsJSON(a *botAlerts) string {
	if a == nil || a.empty() {
		return ""
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// checkAlertURL accepts an http(s) URL with a host and no control characters.
func checkAlertURL(raw string) error {
	if raw == "" {
		return errors.New("a URL is required")
	}
	if len(raw) > 2048 || hasControl(raw) {
		return errors.New("the URL is not usable")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the URL must be http:// or https://")
	}
	return nil
}

// botAlertConfig is the effective configuration for one bot.
type botAlertConfig struct {
	Enabled      bool // global switch: routed channels and in-app notifications
	Mode         string
	Channels     []int64
	DownAfter    time.Duration
	UpAfter      time.Duration
	RemindEvery  time.Duration
	RemindMax    int
	HandleWithin time.Duration
	HealthChecks []botHealthCheck
}

func effectiveAlertConfig(g alertSettings, r *botRecord) botAlertConfig {
	c := botAlertConfig{
		Enabled:      g.Enabled,
		Channels:     g.Channels,
		DownAfter:    time.Duration(g.DownAfter) * time.Second,
		UpAfter:      time.Duration(g.UpAfter) * time.Second,
		RemindEvery:  time.Duration(g.RemindEvery) * time.Second,
		RemindMax:    g.RemindMax,
		HandleWithin: time.Duration(g.HandleWithin) * time.Second,
	}
	a := r.Alerts
	if a == nil {
		return c
	}
	c.Mode = a.Mode
	switch a.Mode {
	case "off":
		c.Channels = nil
	case "custom":
		c.Channels = a.Channels
	}
	if a.DownAfter != nil {
		c.DownAfter = time.Duration(*a.DownAfter) * time.Second
	}
	if a.UpAfter != nil {
		c.UpAfter = time.Duration(*a.UpAfter) * time.Second
	}
	if a.RemindEvery != nil {
		c.RemindEvery = time.Duration(*a.RemindEvery) * time.Second
	}
	if a.HandleWithin != nil {
		c.HandleWithin = time.Duration(*a.HandleWithin) * time.Second
	}
	c.HealthChecks = a.HealthChecks
	return c
}

// --- hold ------------------------------------------------------------------------

// botHeld reports whether the bot is on a deliberate hold right now.
func botHeld(r *botRecord, now time.Time) bool {
	return r.HoldUntil == -1 || (r.HoldUntil > 0 && r.HoldUntil > now.Unix())
}

// setBotHold holds the bot until `until` (-1 = until released) or releases
// it (0).
func setBotHold(name string, until int64, reason string) error {
	if until == 0 {
		reason = ""
	}
	_, err := db.Exec(`UPDATE bots SET hold_until = ?, hold_reason = ? WHERE name = ?`, until, reason, name)
	return err
}

// botHoldInput is a hold request: minutes from now, an RFC 3339 time, or
// neither for "until released".
type botHoldInput struct {
	Minutes int    `json:"minutes,omitempty"`
	Until   string `json:"until,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func (in botHoldInput) resolve(now time.Time) (int64, string, error) {
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) > 200 || hasControl(reason) {
		return 0, "", errors.New("reason must be at most 200 characters on one line")
	}
	switch {
	case in.Minutes < 0 || in.Minutes > 60*24*90:
		return 0, "", errors.New("minutes must be 1-129600")
	case in.Minutes > 0 && in.Until != "":
		return 0, "", errors.New("give minutes or until, not both")
	case in.Minutes > 0:
		return now.Add(time.Duration(in.Minutes) * time.Minute).Unix(), reason, nil
	case in.Until != "":
		t, err := time.Parse(time.RFC3339, in.Until)
		if err != nil {
			return 0, "", errors.New("until must be an RFC 3339 time")
		}
		if !t.After(now) {
			return 0, "", errors.New("until must be in the future")
		}
		return t.Unix(), reason, nil
	}
	return -1, reason, nil
}

// serveBotHold is POST (hold) and DELETE (release) /api/bots/<name>/hold.
func serveBotHold(w http.ResponseWriter, r *http.Request, rec *botRecord) {
	switch r.Method {
	case http.MethodPost:
		var in botHoldInput
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		until, reason, err := in.resolve(time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := setBotHold(rec.Name, until, reason); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rec.HoldUntil, rec.HoldReason = until, reason
	case http.MethodDelete:
		if err := setBotHold(rec.Name, 0, ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		rec.HoldUntil, rec.HoldReason = 0, ""
	default:
		http.Error(w, "POST or DELETE", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "hold": botHoldView(rec, time.Now())})
}

// botHoldInfo is a hold as clients read it.
type botHoldInfo struct {
	Held   bool   `json:"held"`
	Until  string `json:"until,omitempty"` // "" while held = until released
	Reason string `json:"reason,omitempty"`
}

func botHoldView(r *botRecord, now time.Time) botHoldInfo {
	if !botHeld(r, now) {
		return botHoldInfo{}
	}
	h := botHoldInfo{Held: true, Reason: r.HoldReason}
	if r.HoldUntil > 0 {
		h.Until = time.Unix(r.HoldUntil, 0).UTC().Format(time.RFC3339)
	}
	return h
}

// --- signals and evaluation --------------------------------------------------------

// botHealthSignals is everything evalBotHealth decides from. Gathering is
// botHealthSignalsFor's; deciding is pure, so tests feed signals directly.
type botHealthSignals struct {
	Omp      bool
	Stopped  bool
	Starting bool // inside the start grace lasso gave a launch
	HostErr  string
	Pane     bool // a pane (claude) / a pane or a live bridge (omp)
	AgentUp  bool // the agent runs in it: claude detected or its session live; omp's bridge connected
	// Channel: lasso's channel is in the bot's launch; ChannelAge is how long
	// since it last polled (-1: never).
	Channel    bool
	ChannelAge time.Duration
	// MissingGrants: channels the running claude process was launched without.
	MissingGrants []string
	// Unhandled: a channel event acked longer than handle_within ago that the
	// transcript never shows arriving. Queued: an event waiting undelivered
	// for longer than handle_within.
	Unhandled int64
	Queued    int64
	// ProbeFailed: an omp bridge that is connected stopped answering get_state.
	ProbeFailed string
	// Checks: the bot's own health checks that failed, with why.
	Checks []string
}

type botHealth struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	Since  string `json:"since,omitempty"`
}

func evalBotHealth(s botHealthSignals) botHealth {
	switch {
	case s.Stopped:
		return botHealth{State: "stopped"}
	case s.HostErr != "":
		return botHealth{State: "unknown", Reason: "its host did not answer: " + s.HostErr}
	case !s.Pane && s.Starting:
		return botHealth{State: "starting"}
	case !s.Pane:
		return botHealth{State: "gone", Reason: "no pane: the bot is not running"}
	case !s.AgentUp && s.Starting:
		return botHealth{State: "starting"}
	case !s.AgentUp && s.Omp:
		return botHealth{State: "shell", Reason: "the pane is there but omp's bridge does not answer"}
	case !s.AgentUp:
		return botHealth{State: "shell", Reason: "the pane is a plain shell: claude is not running in it"}
	}
	var deaf []string
	if len(s.MissingGrants) > 0 {
		deaf = append(deaf, "claude was launched without its channel grant for "+strings.Join(s.MissingGrants, ", "))
	}
	if s.ProbeFailed != "" {
		deaf = append(deaf, "omp stopped answering: "+s.ProbeFailed)
	}
	if s.Channel && !s.Starting && (s.ChannelAge < 0 || s.ChannelAge > botChannelFresh) {
		if s.ChannelAge < 0 {
			deaf = append(deaf, "lasso's channel has never polled")
		} else {
			deaf = append(deaf, fmt.Sprintf("lasso's channel last polled %s ago", s.ChannelAge.Round(time.Second)))
		}
	}
	if s.Unhandled > 0 {
		deaf = append(deaf, fmt.Sprintf("event %d was delivered but never reached the conversation", s.Unhandled))
	}
	if s.Queued > 0 {
		deaf = append(deaf, fmt.Sprintf("event %d is still waiting to be delivered", s.Queued))
	}
	deaf = append(deaf, s.Checks...)
	if len(deaf) > 0 {
		return botHealth{State: "deaf", Reason: strings.Join(deaf, "; ")}
	}
	if s.Starting {
		return botHealth{State: "starting"}
	}
	return botHealth{State: "ok"}
}

// botProcArgv reads a process's argv on b's host. A seam for tests.
var botProcArgv = func(b Backend, pid int) ([]string, error) {
	raw, err := b.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00"), nil
}

// A process's argv never changes, so it is read once per host and pid.
var botArgvCache = struct {
	sync.Mutex
	m map[string][]string
}{m: map[string][]string{}}

func botCachedArgv(b Backend, pid int) ([]string, error) {
	key := b.Name() + "\x00" + strconv.Itoa(pid)
	botArgvCache.Lock()
	argv, ok := botArgvCache.m[key]
	botArgvCache.Unlock()
	if ok {
		return argv, nil
	}
	argv, err := botProcArgv(b, pid)
	if err != nil {
		return nil, err
	}
	botArgvCache.Lock()
	if len(botArgvCache.m) > 256 {
		botArgvCache.m = map[string][]string{}
	}
	botArgvCache.m[key] = argv
	botArgvCache.Unlock()
	return argv, nil
}

// botMissingGrants lists the channels grants names that argv was not launched
// with (one --dangerously-load-development-channels server:<name> each).
func botMissingGrants(argv []string, grants []string) []string {
	have := map[string]bool{}
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--dangerously-load-development-channels" {
			have[strings.TrimPrefix(argv[i+1], "server:")] = true
		}
	}
	var out []string
	for _, g := range grants {
		if !have[g] {
			out = append(out, g)
		}
	}
	return out
}

// botStoredChannelToken reads the bot's channel token without making one:
// a bot with none was launched without lasso's channel.
func botStoredChannelToken(name string) string {
	var tok string
	_ = db.QueryRow(`SELECT channel_token FROM bots WHERE name = ?`, name).Scan(&tok)
	return tok
}

var eventIDRE = regexp.MustCompile(`event_id="(\d+)"`)

// botIncomingEvents reads a chat page for the newest lasso-channel event id
// it shows arriving, and the time of its oldest row.
func botIncomingEvents(items []chatItem) (int64, string) {
	var max int64
	oldest := ""
	for _, it := range items {
		if oldest == "" && it.At != "" {
			oldest = it.At
		}
		if it.Kind != "incoming" || (it.Source != "" && it.Source != botChannelName) {
			continue
		}
		for _, m := range eventIDRE.FindAllStringSubmatch(it.Text, -1) {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > max {
				max = id
			}
		}
	}
	return max, oldest
}

// botUnhandledEvent is the newest delivered event when it was acked longer
// than within ago and the transcript does not show it arriving. A page that
// starts after the ack cannot tell, and says nothing.
func botUnhandledEvent(name string, now time.Time, within time.Duration) int64 {
	var id int64
	var done string
	if err := db.QueryRow(`SELECT id, done_at FROM bot_events WHERE bot = ? AND status = 'delivered' ORDER BY id DESC LIMIT 1`, name).Scan(&id, &done); err != nil {
		return 0
	}
	doneAt, ok := parseStamp(done)
	if !ok || now.Sub(doneAt) < within {
		return 0
	}
	seen, oldest := botCachedIncoming(name)
	if seen >= id {
		return 0
	}
	// The page must reach back past the ack: a transcript that starts after it
	// (a fresh session, or a long turn since) cannot say the event is missing.
	t, err := time.Parse(time.RFC3339Nano, oldest)
	if err != nil || t.After(doneAt) {
		return 0
	}
	return id
}

// botQueuedEvent is the oldest event still pending or claimed that fired
// longer than within ago.
func botQueuedEvent(name string, now time.Time, within time.Duration) int64 {
	var id int64
	var created, fired string
	if err := db.QueryRow(`SELECT id, created_at, fired_at FROM bot_events WHERE bot = ? AND status IN ('pending', 'claimed') ORDER BY id LIMIT 1`, name).Scan(&id, &created, &fired); err != nil {
		return 0
	}
	t, ok := parseStamp(fired)
	if !ok {
		if t, ok = parseStamp(created); !ok {
			return 0
		}
	}
	if now.Sub(t) < within {
		return 0
	}
	return id
}

func botStartingAt(name string, now time.Time) bool {
	bots.mu.Lock()
	defer bots.mu.Unlock()
	t, ok := bots.starting[name]
	return ok && now.Sub(t) < botStartGrace
}

// botHealthSignalsFor gathers one bot's signals. sessions caches each host's
// claude session registry for the pass.
func botHealthSignalsFor(r *botRecord, cfg botAlertConfig, sessions map[string][]claudeSessionEntry, now time.Time) botHealthSignals {
	s := botHealthSignals{Omp: r.isOmp(), Stopped: r.Stopped, Starting: botStartingAt(r.Name, now)}
	if s.Stopped {
		return s
	}
	b, err := botBackend(r.Host)
	if err != nil {
		s.HostErr = err.Error()
		return s
	}
	if err := botHostPing(b); err != nil {
		s.HostErr = err.Error()
		return s
	}
	s.Checks = botHealthCheckFailures(r.Name, cfg.HealthChecks, now)
	if s.Omp {
		ob := ompBotFor(r.Name)
		connected := ob.ensure()
		_, pane := botOmpLaunch.pane(b, r)
		s.Pane, s.AgentUp = connected || pane, connected
		if connected {
			s.ProbeFailed = botOmpProbe(ob, now)
			s.Queued = botQueuedEvent(r.Name, now, cfg.HandleWithin)
		}
		return s
	}
	p, ok := findBotPane(b, r)
	if !ok {
		return s
	}
	s.Pane = true
	if _, ok := sessions[r.Host]; !ok {
		sessions[r.Host] = readClaudeSessions(b)
	}
	kind, _ := paneAgentPresence(p)
	sess, found := findBotSession(sessions[r.Host], r.Name, expandTildeOn(b, r.Dir))
	s.AgentUp = kind != "" || found
	if !s.AgentUp {
		return s
	}
	rec := *r
	rec.channelToken = botStoredChannelToken(r.Name)
	grants := rec.channelGrants()
	s.Channel = slices.Contains(grants, botChannelName)
	if s.Channel {
		s.ChannelAge = -1
		if t, ok := parseStamp(botChannelSeenStamp(r.Name)); ok {
			s.ChannelAge = now.Sub(t)
		}
		_, _, _ = botLastLineFor(b, r.Name, p)
		s.Unhandled = botUnhandledEvent(r.Name, now, cfg.HandleWithin)
		s.Queued = botQueuedEvent(r.Name, now, cfg.HandleWithin)
	}
	if found && len(grants) > 0 {
		if argv, err := botCachedArgv(b, sess.PID); err == nil && len(argv) > 0 {
			s.MissingGrants = botMissingGrants(argv, grants)
		}
	}
	return s
}

// botHostPing checks the host answers before anything is read from it, so an
// unreachable host reads unknown rather than every bot on it gone. Local is
// always there. A seam for tests.
var botHostPing = func(b Backend) error {
	if _, ok := b.(*localBackend); ok {
		return nil
	}
	_, err := b.HomeDir()
	return err
}

// --- health checks and omp probes (asynchronous, cached) ---------------------------

// The tick never waits on a network call: each check runs in the background
// at most every healthCheckEvery, and the tick reads the newest result.
const healthCheckEvery = 30 * time.Second
const ompProbeEvery = 60 * time.Second

type cachedCheck struct {
	at      time.Time
	running bool
	failure string // "" = passed
}

var healthChecks = struct {
	sync.Mutex
	m map[string]*cachedCheck // bot \x00 name \x00 url
}{m: map[string]*cachedCheck{}}

var healthCheckHTTP = &http.Client{
	Timeout:       5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func botHealthCheckFailures(bot string, checks []botHealthCheck, now time.Time) []string {
	var out []string
	for _, c := range checks {
		key := bot + "\x00" + c.Name + "\x00" + c.URL + "\x00" + c.JSONField
		healthChecks.Lock()
		cc := healthChecks.m[key]
		if cc == nil {
			cc = &cachedCheck{}
			healthChecks.m[key] = cc
		}
		if !cc.running && now.Sub(cc.at) >= healthCheckEvery {
			cc.running = true
			go func(c botHealthCheck, cc *cachedCheck) {
				f := runHealthCheck(c)
				healthChecks.Lock()
				cc.failure, cc.at, cc.running = f, time.Now(), false
				healthChecks.Unlock()
			}(c, cc)
		}
		failure := cc.failure
		healthChecks.Unlock()
		if failure != "" {
			out = append(out, c.Name+": "+failure)
		}
	}
	return out
}

// runHealthCheck GETs the check and says what is wrong, "" when it passed.
func runHealthCheck(c botHealthCheck) string {
	resp, err := healthCheckHTTP.Get(c.URL)
	if err != nil {
		return scrubAlertErr(err, nil)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Sprintf("answered HTTP %d", resp.StatusCode)
	}
	if c.JSONField == "" {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return "did not answer JSON"
	}
	if v, ok := m[c.JSONField].(bool); !ok || !v {
		return fmt.Sprintf("%s is %v", c.JSONField, m[c.JSONField])
	}
	return ""
}

type ompProbeState struct {
	at      time.Time
	running bool
	failure string
}

var ompProbes = struct {
	sync.Mutex
	m map[string]*ompProbeState
}{m: map[string]*ompProbeState{}}

// botOmpProbeTimeout is how long a connected bridge has to answer get_state.
var botOmpProbeTimeout = 10 * time.Second

// botOmpProbe returns the newest probe's failure ("" = answered) and starts
// another when one is due.
func botOmpProbe(ob *ompBot, now time.Time) string {
	ompProbes.Lock()
	defer ompProbes.Unlock()
	p := ompProbes.m[ob.name]
	if p == nil {
		p = &ompProbeState{}
		ompProbes.m[ob.name] = p
	}
	if !p.running && now.Sub(p.at) >= ompProbeEvery {
		p.running = true
		go func() {
			_, err := ob.call(map[string]any{"type": "get_state"}, botOmpProbeTimeout)
			f := ""
			// A bridge that went away is gone, not deaf: the next tick sees it.
			if err != nil && !errors.Is(err, errOmpNotConnected) {
				f = err.Error()
			}
			ompProbes.Lock()
			p.failure, p.at, p.running = f, time.Now(), false
			ompProbes.Unlock()
		}()
	}
	return p.failure
}

// --- the alert state machine -------------------------------------------------------

type botAlertState struct {
	Bot        string `json:"bot"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	Since      string `json:"since,omitempty"`
	Down       bool   `json:"down"`
	DownSince  string `json:"down_since,omitempty"`
	DownReason string `json:"down_reason,omitempty"`
	LastSentAt string `json:"last_sent_at,omitempty"`
	Reminders  int    `json:"reminders"`
	BadSince   string `json:"-"`
	GoodSince  string `json:"-"`
	known      bool
}

func loadBotAlertState(bot string) botAlertState {
	st := botAlertState{Bot: bot}
	var down int
	err := db.QueryRow(`SELECT state, reason, since, down, down_since, down_reason, last_sent_at, reminders, bad_since, good_since FROM bot_alert_state WHERE bot = ?`, bot).
		Scan(&st.State, &st.Reason, &st.Since, &down, &st.DownSince, &st.DownReason, &st.LastSentAt, &st.Reminders, &st.BadSince, &st.GoodSince)
	st.known = err == nil
	st.Down = down != 0
	return st
}

func saveBotAlertState(st botAlertState, now time.Time) {
	_, err := db.Exec(`INSERT INTO bot_alert_state (bot, state, reason, since, down, down_since, down_reason, last_sent_at, reminders, bad_since, good_since, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bot) DO UPDATE SET state=excluded.state, reason=excluded.reason, since=excluded.since, down=excluded.down,
			down_since=excluded.down_since, down_reason=excluded.down_reason, last_sent_at=excluded.last_sent_at,
			reminders=excluded.reminders, bad_since=excluded.bad_since, good_since=excluded.good_since, updated_at=excluded.updated_at`,
		st.Bot, st.State, st.Reason, st.Since, boolInt(st.Down), st.DownSince, st.DownReason, st.LastSentAt, st.Reminders, st.BadSince, st.GoodSince, nowStamp(now))
	if err != nil {
		log.Printf("alerts:   save %s: %v", st.Bot, err)
	}
}

func deleteBotAlertState(bot string) {
	_, _ = db.Exec(`DELETE FROM bot_alert_state WHERE bot = ?`, bot)
}

// botHealthOf is the stored health the runner last saw, for any lasso's views.
func botHealthOf(bot string) *botHealth {
	st := loadBotAlertState(bot)
	if !st.known || st.State == "" {
		return nil
	}
	return &botHealth{State: st.State, Reason: st.Reason, Since: st.Since}
}

// alertEvent types.
const (
	alertDown      = "down"
	alertRecovered = "recovered"
	alertReminder  = "reminder"
	alertTest      = "test"
)

// stepBotAlert moves one bot's alert state on by one evaluation and returns
// the event to send, if any. held suppresses gone/shell (but not deaf).
//
// A bad state must hold for down_after before it is "down"; a down bot must be
// good for up_after before it is "recovered". starting is neutral: it neither
// starts nor clears either window, so a crash loop (gone, starting, gone…)
// still pages. A Stop or a hold resolves a gone/shell outage at once.
func stepBotAlert(st botAlertState, h botHealth, held bool, cfg botAlertConfig, now time.Time) (botAlertState, string) {
	stamp := nowStamp(now)
	if st.State != h.State {
		st.Since = stamp
	}
	st.State, st.Reason = h.State, h.Reason
	parse := func(s string) time.Time {
		t, _ := parseStamp(s)
		return t
	}
	bad := false
	switch h.State {
	case "gone", "shell":
		bad = !held
	case "deaf":
		bad = true
	case "unknown":
		// An unreachable host is a reason to wait, not to page about every bot
		// on it, until it has been out for three windows.
		bad = st.BadSince != "" && now.Sub(parse(st.BadSince)) >= 3*cfg.DownAfter
		if st.BadSince == "" {
			st.BadSince = stamp
		}
	}
	neutral := h.State == "starting" || (h.State == "unknown" && !bad)
	resolved := h.State == "stopped" || (held && (h.State == "gone" || h.State == "shell"))

	if !st.Down {
		switch {
		case bad:
			if st.BadSince == "" {
				st.BadSince = stamp
			}
			if now.Sub(parse(st.BadSince)) >= cfg.DownAfter {
				st.Down, st.DownSince, st.DownReason = true, st.BadSince, h.Reason
				st.LastSentAt, st.Reminders, st.GoodSince = stamp, 0, ""
				return st, alertDown
			}
		case neutral:
		default:
			st.BadSince = ""
		}
		return st, ""
	}
	// Down.
	switch {
	case resolved:
		st.Down, st.BadSince, st.GoodSince = false, "", ""
		st.LastSentAt = stamp
		return st, alertRecovered
	case bad:
		st.GoodSince = ""
		st.DownReason = h.Reason
		if cfg.RemindEvery > 0 && st.Reminders < cfg.RemindMax && now.Sub(parse(st.LastSentAt)) >= cfg.RemindEvery {
			st.Reminders++
			st.LastSentAt = stamp
			return st, alertReminder
		}
	case neutral:
	default:
		if st.GoodSince == "" {
			st.GoodSince = stamp
		}
		if now.Sub(parse(st.GoodSince)) >= cfg.UpAfter {
			st.Down, st.BadSince, st.GoodSince = false, "", ""
			st.LastSentAt = stamp
			return st, alertRecovered
		}
	}
	return st, ""
}

// --- the tick ------------------------------------------------------------------------

// botHealthTick evaluates every bot, steps its alert state, sends what the
// transitions call for, and pings each heartbeat whose bot is not down. Only
// botTick calls it, so only the runner-lock holder ever alerts.
func botHealthTick(list []*botRecord, sessions map[string][]claudeSessionEntry, now time.Time) {
	if sessions == nil {
		sessions = map[string][]claudeSessionEntry{}
	}
	g := loadAlertSettings()
	channels, err := listAlertChannels(true)
	if err != nil {
		log.Printf("alerts:   %v", err)
	}
	for _, r := range list {
		cfg := effectiveAlertConfig(g, r)
		h := evalBotHealth(botHealthSignalsFor(r, cfg, sessions, now))
		held := botHeld(r, now)
		prev := loadBotAlertState(r.Name)
		var next botAlertState
		var event string
		if !prev.known {
			// The first sighting is a baseline: a bot already down starts its
			// window now, and nothing is sent for the past.
			prev.State = h.State
			prev.Since = nowStamp(now)
		}
		next, event = stepBotAlert(prev, h, held, cfg, now)
		if !prev.known || next != prev {
			saveBotAlertState(next, now)
		}
		if event != "" {
			log.Printf("alerts:   %s %s (%s): %s", r.Name, event, h.State, h.Reason)
			ev := alertEventFor(r, next, event, now)
			if cfg.Enabled && cfg.Mode != "off" {
				publishNotification(botAlertNotification(r, ev))
			}
			dispatchAlert(ev, alertTargets(channels, cfg, r.Name))
		}
		botHeartbeats(channels, r, next, now)
	}
}

// alertTargets is the enabled channels a bot's transition goes to: its routed
// channels (none when alerts are off or disabled), plus every heartbeat bound
// to it, which carries the reason to its monitor.
func alertTargets(all []*alertChannel, cfg botAlertConfig, bot string) []*alertChannel {
	var out []*alertChannel
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		if c.Kind == "heartbeat" {
			if c.heartbeatBot() == bot {
				out = append(out, c)
			}
			continue
		}
		if cfg.Enabled && slices.Contains(cfg.Channels, c.ID) {
			out = append(out, c)
		}
	}
	return out
}

func alertEventFor(r *botRecord, st botAlertState, event string, now time.Time) alertEvent {
	ev := alertEvent{
		Type:   event,
		Bot:    r.Name,
		Host:   r.Host,
		State:  st.State,
		Reason: st.DownReason,
		Since:  st.DownSince,
		At:     nowStamp(now),
		Path:   "/bots/" + url.PathEscape(r.Name),
	}
	if event == alertRecovered {
		ev.Reason = st.Reason
		if t, ok := parseStamp(st.DownSince); ok {
			ev.DowntimeSec = int64(now.Sub(t).Seconds())
		}
		if st.State == "stopped" {
			ev.Reason = "stopped by hand"
		} else if r.HoldUntil != 0 && (st.State == "gone" || st.State == "shell") {
			ev.Reason = "put on hold"
		}
	}
	return ev
}

// botAlertNotification is the transition as one of lasso's own notifications
// (the Inbox, Web Push, the live channel).
func botAlertNotification(r *botRecord, ev alertEvent) notification {
	n := notification{
		Bot:  r.Name,
		Tag:  "bot-alert:" + r.Name,
		Host: r.Host,
		URL:  ev.Path,
		Icon: botIconURL(r),
	}
	switch ev.Type {
	case alertRecovered:
		n.Kind, n.Level = notifBotRecovered, "info"
		n.Title = r.Name + " recovered"
		n.Body = "Back after " + humanDuration(time.Duration(ev.DowntimeSec)*time.Second) + "."
		if ev.Reason == "stopped by hand" || ev.Reason == "put on hold" {
			n.Body = "No longer alerting: " + ev.Reason + "."
		}
	default:
		n.Kind, n.Level = notifBotDown, "error"
		n.Title = r.Name + " is " + ev.State
		if ev.Type == alertReminder {
			n.Title = r.Name + " is still " + ev.State
		}
		n.Body = ev.Reason
	}
	return n
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

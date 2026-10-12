package main

// Anonymous usage analytics (docs/reference/analytics.md is the user-facing
// statement of exactly what is sent; keep the two in step).
//
// lasso sends a small, closed vocabulary of events to PostHog's capture API:
// its own (install_active once a day, agent_created when an agent comes in
// over MCP) and the ones its web and phone clients post to /api/telemetry,
// which are checked against the same allowlist and forwarded in batches. The
// clients never talk to PostHog themselves, so a browser on lasso's origin
// carries no third-party script and no cookie.
//
// The rules this file exists to keep:
//   - Anonymous. The only identity is a random install id in lasso.db. Every
//     property is an enum value or a count bucket from telemetrySpec; nothing
//     a user typed, named or browsed can reach a payload.
//   - Off means nothing at all: the setting, LASSO_TELEMETRY=0, DO_NOT_TRACK
//     and an empty ingestion key each stop every request, including any
//     "opted out" signal. Events queued before a switch-off are dropped.
//   - Never in the way. Tracking is a non-blocking append to a capped queue
//     (oldest dropped); one goroutine sends a batch at a time with a 5 s
//     deadline, never retries, swallows every failure and logs only under
//     LASSO_TELEMETRY_DEBUG. Shutdown does not wait for it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// The PostHog project lasso reports to. The key is the project's public,
// write-only ingestion key (phc_…), safe to ship in a binary. An empty key
// means this build sends nothing. Both can be overridden from the environment
// for testing (LASSO_TELEMETRY_HOST, LASSO_TELEMETRY_KEY).
const (
	telemetryDefaultHost = "https://posthog.execution.associates"
	telemetryDefaultKey  = ""
	telemetryPrivacyURL  = "https://www.executionassociates.com/privacy/"
)

const (
	telemetrySettingKey   = "telemetry"
	telemetryActiveDayKey = "telemetry_active_day"

	telemetryQueueCap   = 500
	telemetryBatchMax   = 50
	telemetryFlushEvery = 30 * time.Second
	// install_active is checked hourly and sent at most once per UTC day; the
	// first check waits a little so a boot is never the moment lasso reaches out.
	telemetryActiveFirst = 2 * time.Minute
	telemetryActiveEvery = time.Hour
)

// telemetryTimeout bounds every request to PostHog, connect to last byte. A
// var only so the tests can hold a hanging server to a shorter deadline.
var telemetryTimeout = 5 * time.Second

// ---------------------------------------------------------------------------
// the vocabulary
// ---------------------------------------------------------------------------

var (
	telemetryCountBuckets = []string{"0", "1-5", "6-20", "21+"}
	telemetryHostBuckets  = []string{"1", "2-3", "4+"}
	telemetryViews        = []string{
		"terminal", "chat", "grid", "bots", "browser", "files", "scratch", "settings",
		"simple_inbox", "simple_agents", "inbox_card", "agents_column", "bots_column",
		"agent_window", "bot_window", "terminal_window", "browser_window", "new_form",
	}
	telemetryHarnesses = []string{"claude", "codex", "opencode", "omp", "pi", "other"}
	telemetryFeatures  = []string{
		"shortcut_used", "paste_upload", "terminal_zoom", "browser_new_tab", "plugin_enabled",
		"theme_changed", "voice_input", "file_saved", "bot_created", "agent_pinned",
	}
	telemetryInboxActions = []string{"acknowledge", "kill", "reply", "answer", "stop", "rename"}
)

// telemetrySpec is every event lasso can send and, for each, the exact set of
// properties it carries with the values each may take. An event must carry
// every listed property and nothing else.
var telemetrySpec = map[string]map[string][]string{
	"install_active": {
		"hosts_count":    telemetryHostBuckets,
		"agents_running": telemetryCountBuckets,
		"bots_count":     telemetryCountBuckets,
	},
	"view_opened":          {"view": telemetryViews},
	"agent_created":        {"harness": telemetryHarnesses, "kind": {"git", "scratch"}, "from": {"web", "phone", "mac", "mcp"}},
	"inbox_action":         {"action": telemetryInboxActions, "from": {"web", "phone", "mac"}},
	"feature_used":         {"feature": telemetryFeatures},
	"onboarding_completed": {},
}

// telemetryCommonSpec is what every event carries besides its own properties.
// app_version is lasso's version (validated by shape, not by list). The two
// $-flags are PostHog processing directives that make it store less: no GeoIP
// lookup and no person profile for the install id.
var telemetryCommonSpec = map[string][]string{
	"app":  {"server", "web", "phone", "mac"},
	"os":   {"linux", "darwin"},
	"arch": {"amd64", "arm64", "x86_64"},
}

// Which events a web or phone client may post, and as which apps.
var (
	telemetryClientEvents = []string{"view_opened", "agent_created", "inbox_action", "feature_used", "onboarding_completed"}
	telemetryClientApps   = []string{"web", "phone"}
)

// telemetryValid reports whether event+props is exactly an allowed shape.
func telemetryValid(event string, props map[string]string) bool {
	spec, ok := telemetrySpec[event]
	if !ok || len(props) != len(spec) {
		return false
	}
	for k, allowed := range spec {
		v, ok := props[k]
		if !ok || !slices.Contains(allowed, v) {
			return false
		}
	}
	return true
}

// telemetryBucket maps a count onto the 0 / 1-5 / 6-20 / 21+ buckets.
func telemetryBucket(n int) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 5:
		return "1-5"
	case n <= 20:
		return "6-20"
	}
	return "21+"
}

func telemetryHostBucket(n int) string {
	switch {
	case n <= 1:
		return "1"
	case n <= 3:
		return "2-3"
	}
	return "4+"
}

// telemetryHarness names an agent CLI by the closed list; anything else is
// "other", so a custom command line never leaves the machine.
func telemetryHarness(agent string) string {
	a := strings.ToLower(strings.TrimSpace(agent))
	if slices.Contains(telemetryHarnesses, a) {
		return a
	}
	return "other"
}

func telemetryKind(typ string) string {
	if typ == "scratch" {
		return "scratch"
	}
	return "git"
}

// ---------------------------------------------------------------------------
// the switch
// ---------------------------------------------------------------------------

type telemetrySetting struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	InstallID string `json:"install_id,omitempty"`
}

func telemetryHost() string {
	if h := strings.TrimSpace(os.Getenv("LASSO_TELEMETRY_HOST")); h != "" {
		return strings.TrimRight(h, "/")
	}
	return telemetryDefaultHost
}

func telemetryKey() string {
	if k, ok := os.LookupEnv("LASSO_TELEMETRY_KEY"); ok {
		return strings.TrimSpace(k)
	}
	return telemetryDefaultKey
}

func envFalse(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}

func envTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// telemetryEnvOff names the environment variable that turns analytics off in
// this process, or "" when the environment leaves it to the setting.
func telemetryEnvOff() string {
	if envTrue(os.Getenv("DO_NOT_TRACK")) {
		return "DO_NOT_TRACK"
	}
	if envFalse(os.Getenv("LASSO_TELEMETRY")) {
		return "LASSO_TELEMETRY"
	}
	return ""
}

// The stored setting is read through a short cache: a view_opened per click
// should not be a db read, and `lasso telemetry off` from a shell (another
// process) still takes effect within telemetrySettingTTL.
const telemetrySettingTTL = 30 * time.Second

var telemetryCache struct {
	mu  sync.Mutex
	at  time.Time
	val telemetrySetting
}

func readTelemetrySetting() telemetrySetting {
	var s telemetrySetting
	if db == nil {
		return s
	}
	v, err := getSetting(telemetrySettingKey)
	if err != nil || v == "" {
		return s
	}
	_ = json.Unmarshal([]byte(v), &s)
	return s
}

func cachedTelemetrySetting() telemetrySetting {
	telemetryCache.mu.Lock()
	defer telemetryCache.mu.Unlock()
	if telemetryCache.at.IsZero() || time.Since(telemetryCache.at) > telemetrySettingTTL {
		telemetryCache.val = readTelemetrySetting()
		telemetryCache.at = time.Now()
	}
	return telemetryCache.val
}

func forgetTelemetryCache() {
	telemetryCache.mu.Lock()
	telemetryCache.at = time.Time{}
	telemetryCache.mu.Unlock()
}

// telemetrySettingOn is the user's choice alone: on unless switched off.
func telemetrySettingOn() bool {
	s := cachedTelemetrySetting()
	return s.Enabled == nil || *s.Enabled
}

// telemetryActive is whether anything may be sent right now.
func telemetryActive() bool {
	return telemetryEnvOff() == "" && telemetryKey() != "" && telemetrySettingOn()
}

// setTelemetryEnabled stores the user's choice, keeping the install id.
func setTelemetryEnabled(on bool) error {
	s := readTelemetrySetting()
	s.Enabled = &on
	b, _ := json.Marshal(s)
	if err := setSetting(telemetrySettingKey, string(b)); err != nil {
		return err
	}
	forgetTelemetryCache()
	if !on {
		telemetryQ.drop()
	}
	return nil
}

// telemetryInstallID is this lasso's anonymous id, minted on first use.
func telemetryInstallID() string {
	s := readTelemetrySetting()
	if s.InstallID != "" {
		return s.InstallID
	}
	s.InstallID = newUUIDv4()
	b, _ := json.Marshal(s)
	if err := setSetting(telemetrySettingKey, string(b)); err != nil {
		return ""
	}
	forgetTelemetryCache()
	return s.InstallID
}

func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func telemetryDebugf(format string, args ...any) {
	if envTrue(os.Getenv("LASSO_TELEMETRY_DEBUG")) {
		log.Printf("telemetry: "+format, args...)
	}
}

// ---------------------------------------------------------------------------
// the queue and the sender
// ---------------------------------------------------------------------------

type telemetryEvent struct {
	App   string
	Event string
	Props map[string]string
	At    time.Time
}

type telemetryQueue struct {
	mu      sync.Mutex
	events  []telemetryEvent
	dropped int
	kick    chan struct{}
}

var telemetryQ = &telemetryQueue{kick: make(chan struct{}, 1)}

func (q *telemetryQueue) push(e telemetryEvent) {
	q.mu.Lock()
	q.events = append(q.events, e)
	if over := len(q.events) - telemetryQueueCap; over > 0 {
		q.events = slices.Delete(q.events, 0, over)
		q.dropped += over
	}
	full := len(q.events) >= telemetryBatchMax
	q.mu.Unlock()
	if full {
		select {
		case q.kick <- struct{}{}:
		default:
		}
	}
}

func (q *telemetryQueue) take(n int) []telemetryEvent {
	q.mu.Lock()
	defer q.mu.Unlock()
	n = min(n, len(q.events))
	out := slices.Clone(q.events[:n])
	q.events = slices.Delete(q.events, 0, n)
	return out
}

func (q *telemetryQueue) drop() {
	q.mu.Lock()
	q.events = nil
	q.mu.Unlock()
}

func (q *telemetryQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.events)
}

// telemetryTrack queues one event. It never blocks and never fails: an event
// outside the vocabulary, or any event while analytics is off, is dropped.
func telemetryTrack(app, event string, props map[string]string) {
	if !telemetryActive() {
		return
	}
	if props == nil {
		props = map[string]string{}
	}
	if !slices.Contains(telemetryCommonSpec["app"], app) || !telemetryValid(event, props) {
		telemetryDebugf("dropped %s/%s: not in the vocabulary", app, event)
		return
	}
	telemetryQ.push(telemetryEvent{App: app, Event: event, Props: props, At: time.Now().UTC()})
}

// telemetryDoer is the HTTP seam: the sender's client in production, a fake
// in tests.
type telemetryDoer interface {
	Do(*http.Request) (*http.Response, error)
}

var telemetryHTTP telemetryDoer = &http.Client{
	Timeout: telemetryTimeout,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: telemetryTimeout,
		MaxIdleConns:          1,
		IdleConnTimeout:       90 * time.Second,
	},
	// PostHog's capture API answers directly; a redirect is not followed.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// telemetryProps is an event's full property set: its own plus the common ones.
func telemetryProps(e telemetryEvent) map[string]any {
	p := map[string]any{
		"app":                     e.App,
		"app_version":             lassoSemver,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
		"$lib":                    "lasso",
		"$geoip_disable":          true,
		"$process_person_profile": false,
	}
	for k, v := range e.Props {
		p[k] = v
	}
	return p
}

// telemetryFlush sends up to one batch. It is the only thing that talks to
// PostHog, and only the sender goroutine calls it, so at most one request is
// ever in flight. Whatever happens to the request, the batch is gone.
func telemetryFlush(ctx context.Context) {
	if !telemetryActive() {
		telemetryQ.drop()
		return
	}
	batch := telemetryQ.take(telemetryBatchMax)
	if len(batch) == 0 {
		return
	}
	id := telemetryInstallID()
	if id == "" {
		return
	}
	type capture struct {
		Event      string         `json:"event"`
		DistinctID string         `json:"distinct_id"`
		Timestamp  string         `json:"timestamp"`
		Properties map[string]any `json:"properties"`
	}
	body := struct {
		APIKey string    `json:"api_key"`
		Batch  []capture `json:"batch"`
	}{APIKey: telemetryKey()}
	for _, e := range batch {
		body.Batch = append(body.Batch, capture{
			Event: e.Event, DistinctID: id, Timestamp: e.At.Format(time.RFC3339Nano), Properties: telemetryProps(e),
		})
	}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, telemetryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, telemetryHost()+"/batch/", bytes.NewReader(b))
	if err != nil {
		telemetryDebugf("bad host: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lasso/"+lassoSemver)
	resp, err := telemetryHTTP.Do(req)
	if err != nil {
		telemetryDebugf("send %d events: %v", len(batch), err)
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		telemetryDebugf("send %d events: HTTP %d", len(batch), resp.StatusCode)
	}
}

// startTelemetry runs the sender and the daily install_active check until ctx
// ends. It is started once from runServer and nothing waits for it to stop.
func startTelemetry(ctx context.Context) {
	tick := time.NewTicker(telemetryFlushEvery)
	defer tick.Stop()
	active := time.NewTimer(telemetryActiveFirst)
	defer active.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-telemetryQ.kick:
		case <-active.C:
			telemetryDaily(time.Now())
			active.Reset(telemetryActiveEvery)
		}
		telemetryFlush(ctx)
	}
}

// ---------------------------------------------------------------------------
// lasso's own events
// ---------------------------------------------------------------------------

// telemetryCounts is what install_active reports, as counts; seams for tests.
var telemetryCounts = func() (hosts, agents, bots int) {
	hosts = 1 // lasso's own machine
	hs, _ := hostSnapshot()
	for _, h := range hs {
		if h.Reachable && h.Running {
			hosts++
		}
	}
	if recs, err := listAllAgents(); err == nil {
		agents = len(recs)
	}
	if bs, err := listBots(); err == nil {
		bots = len(bs)
	}
	return
}

func telemetryActiveProps() map[string]string {
	h, a, b := telemetryCounts()
	return map[string]string{
		"hosts_count":    telemetryHostBucket(h),
		"agents_running": telemetryBucket(a),
		"bots_count":     telemetryBucket(b),
	}
}

// telemetryDaily sends install_active if it has not gone out today (UTC). The
// day is stored, so a restart does not send a second one.
func telemetryDaily(now time.Time) {
	if !telemetryActive() {
		return
	}
	day := now.UTC().Format("2006-01-02")
	if last, _ := getSetting(telemetryActiveDayKey); last == day {
		return
	}
	if err := setSetting(telemetryActiveDayKey, day); err != nil {
		return
	}
	telemetryTrack("server", "install_active", telemetryActiveProps())
}

func telemetryAgentCreatedProps(agent, typ, from string) map[string]string {
	return map[string]string{"harness": telemetryHarness(agent), "kind": telemetryKind(typ), "from": from}
}

// ---------------------------------------------------------------------------
// HTTP: /api/telemetry (client events) and /api/telemetry/config (the toggle)
// ---------------------------------------------------------------------------

const (
	telemetryBodyMax      = 16 << 10
	telemetryEventsPerReq = 20
	// Each client may post this many events per minute; the rest are dropped.
	telemetryClientPerMin = 60
	telemetryLimiterKeys  = 1024
)

type telemetryLimiter struct {
	mu      sync.Mutex
	window  time.Time
	counts  map[string]int
	nowFunc func() time.Time
}

var telemetryLimit = &telemetryLimiter{counts: map[string]int{}, nowFunc: time.Now}

// allow takes up to n events from key's budget for the current minute and
// returns how many it may forward.
func (l *telemetryLimiter) allow(key string, n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFunc()
	if now.Sub(l.window) >= time.Minute || len(l.counts) > telemetryLimiterKeys {
		l.window = now
		clear(l.counts)
	}
	left := telemetryClientPerMin - l.counts[key]
	n = max(0, min(n, left))
	l.counts[key] += n
	return n
}

// telemetryClientKey identifies a client for the rate limit: its tab id when
// it sends one, else its address.
func telemetryClientKey(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Lasso-Client")); id != "" && len(id) <= 64 {
		return "c:" + id
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "a:" + host
}

type telemetryPost struct {
	App    string `json:"app"`
	Events []struct {
		Event string         `json:"event"`
		Props map[string]any `json:"props"`
	} `json:"events"`
}

// serveTelemetry takes a web or phone client's events. It answers 204 to
// anything well-formed, forwarded or not, so a client never learns or shows
// anything about analytics from it; what it forwards is only what passes the
// allowlist, with `from` forced to the posting app.
func serveTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !telemetryActive() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, telemetryBodyMax)
	var in telemetryPost
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !slices.Contains(telemetryClientApps, in.App) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	events := in.Events
	if len(events) > telemetryEventsPerReq {
		events = events[:telemetryEventsPerReq]
	}
	n := telemetryLimit.allow(telemetryClientKey(r), len(events))
	for _, e := range events[:n] {
		if !slices.Contains(telemetryClientEvents, e.Event) {
			continue
		}
		spec := telemetrySpec[e.Event]
		props := map[string]string{}
		for k := range spec {
			if s, ok := e.Props[k].(string); ok {
				props[k] = s
			}
		}
		if _, ok := spec["from"]; ok {
			props["from"] = in.App
		}
		telemetryTrack(in.App, e.Event, props)
	}
	w.WriteHeader(http.StatusNoContent)
}

// telemetryConfig is the Settings toggle's view: the stored choice, and
// whether anything is actually sent (the environment or a key-less build can
// hold it off whatever the choice).
func telemetryConfig() map[string]any {
	return map[string]any{
		"enabled":     telemetrySettingOn(),
		"active":      telemetryActive(),
		"env_off":     telemetryEnvOff(),
		"has_key":     telemetryKey() != "",
		"privacy_url": telemetryPrivacyURL,
	}
}

func serveTelemetryConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, telemetryConfig())
	case http.MethodPost:
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Enabled == nil {
			http.Error(w, "enabled required", http.StatusBadRequest)
			return
		}
		if err := setTelemetryEnabled(*req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, telemetryConfig())
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}

// ---------------------------------------------------------------------------
// CLI: lasso telemetry [on|off|status]
// ---------------------------------------------------------------------------

func cliTelemetry(args []string) {
	if err := openDB(); err != nil {
		fatal("telemetry: %v", err)
	}
	defer db.Close()
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "on", "off":
		if err := setTelemetryEnabled(cmd == "on"); err != nil {
			fatal("telemetry: %v", err)
		}
	case "status":
	default:
		fatal("telemetry: usage: lasso telemetry [on|off|status]")
	}
	state := "on"
	if !telemetrySettingOn() {
		state = "off"
	}
	fmt.Printf("anonymous usage analytics: %s\n", state)
	if why := telemetryEnvOff(); why != "" {
		fmt.Printf("  (held off in this shell by %s)\n", why)
	} else if telemetryKey() == "" {
		fmt.Println("  (this build has no analytics key, so nothing is sent)")
	}
	fmt.Println("  what is collected: https://github.com/execution-associates/lasso/blob/main/docs/reference/analytics.md")
}

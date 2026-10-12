package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The spec's allowlist, restated here rather than read from telemetrySpec, so
// a property added to the code without a spec change fails these tests.
var specEventProps = map[string]map[string][]string{
	"install_active": {
		"hosts_count":    {"1", "2-3", "4+"},
		"agents_running": {"0", "1-5", "6-20", "21+"},
		"bots_count":     {"0", "1-5", "6-20", "21+"},
	},
	"view_opened": {"view": {
		"terminal", "chat", "grid", "bots", "browser", "files", "scratch", "settings",
		"simple_inbox", "simple_agents", "inbox_card", "agents_column", "bots_column",
		"agent_window", "bot_window", "terminal_window", "browser_window", "new_form",
	}},
	"agent_created": {
		"harness": {"claude", "codex", "opencode", "omp", "pi", "other"},
		"kind":    {"git", "scratch"},
		"from":    {"web", "phone", "mac", "mcp"},
	},
	"inbox_action": {
		"action": {"acknowledge", "kill", "reply", "answer", "stop", "rename"},
		"from":   {"web", "phone", "mac"},
	},
	"feature_used": {"feature": {
		"shortcut_used", "paste_upload", "terminal_zoom", "browser_new_tab", "plugin_enabled",
		"theme_changed", "voice_input", "file_saved", "bot_created", "agent_pinned",
	}},
	"onboarding_completed": {},
}

// fakePostHog records every request the sender makes; respond decides the
// answer (nil = 200).
type fakePostHog struct {
	mu       sync.Mutex
	reqs     []capturedBatch
	urls     []string
	inFlight atomic.Int32
	maxIn    atomic.Int32
	respond  func(*http.Request) (*http.Response, error)
}

type capturedBatch struct {
	APIKey string `json:"api_key"`
	Batch  []struct {
		Event      string         `json:"event"`
		DistinctID string         `json:"distinct_id"`
		Timestamp  string         `json:"timestamp"`
		Properties map[string]any `json:"properties"`
	} `json:"batch"`
}

func (f *fakePostHog) Do(r *http.Request) (*http.Response, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxIn.Load()
		if n <= m || f.maxIn.CompareAndSwap(m, n) {
			break
		}
	}
	b, _ := io.ReadAll(r.Body)
	var cb capturedBatch
	_ = json.Unmarshal(b, &cb)
	f.mu.Lock()
	f.reqs = append(f.reqs, cb)
	f.urls = append(f.urls, r.URL.String())
	f.mu.Unlock()
	if f.respond != nil {
		return f.respond(r)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"status":1}`))}, nil
}

func (f *fakePostHog) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakePostHog) events() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range f.reqs {
		for _, e := range r.Batch {
			p := map[string]any{"__event": e.Event}
			for k, v := range e.Properties {
				p[k] = v
			}
			out = append(out, p)
		}
	}
	return out
}

// useTelemetry switches analytics on against a fake PostHog and a fresh db,
// undoing everything when the test ends.
func useTelemetry(t *testing.T) *fakePostHog {
	t.Helper()
	openTestDB(t)
	t.Setenv("LASSO_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("LASSO_TELEMETRY_KEY", "phc_test")
	t.Setenv("LASSO_TELEMETRY_HOST", "https://posthog.test")
	f := &fakePostHog{}
	prev := telemetryHTTP
	telemetryHTTP = f
	forgetTelemetryCache()
	telemetryQ.drop()
	prevLimit := telemetryLimit
	telemetryLimit = &telemetryLimiter{counts: map[string]int{}, nowFunc: time.Now}
	t.Cleanup(func() {
		telemetryHTTP = prev
		telemetryLimit = prevLimit
		telemetryQ.drop()
		forgetTelemetryCache()
	})
	return f
}

func postTelemetry(t *testing.T, client string, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/telemetry", strings.NewReader(body))
	if client != "" {
		r.Header.Set("X-Lasso-Client", client)
	}
	rec := httptest.NewRecorder()
	serveTelemetry(rec, r)
	return rec
}

// emitEverything drives every path lasso can produce an event on: its own
// install_active and MCP agent_created, and every client event through
// /api/telemetry, including ones that must be refused.
func emitEverything(t *testing.T) {
	t.Helper()
	for _, c := range [][3]int{{1, 0, 0}, {2, 3, 1}, {3, 6, 6}, {9, 25, 40}} {
		telemetryCounts = func() (int, int, int) { return c[0], c[1], c[2] }
		telemetryTrack("server", "install_active", telemetryActiveProps())
	}
	for _, a := range []string{"claude", "Codex", "opencode", "omp", "pi", "my-wrapper --secret=x", ""} {
		for _, typ := range []string{"git", "scratch", ""} {
			telemetryTrack("server", "agent_created", telemetryAgentCreatedProps(a, typ, "mcp"))
		}
	}
	var evs []string
	for _, v := range specEventProps["view_opened"]["view"] {
		evs = append(evs, fmt.Sprintf(`{"event":"view_opened","props":{"view":%q}}`, v))
	}
	for _, a := range specEventProps["inbox_action"]["action"] {
		evs = append(evs, fmt.Sprintf(`{"event":"inbox_action","props":{"action":%q,"from":"mac"}}`, a))
	}
	for _, f := range specEventProps["feature_used"]["feature"] {
		evs = append(evs, fmt.Sprintf(`{"event":"feature_used","props":{"feature":%q}}`, f))
	}
	evs = append(evs,
		`{"event":"agent_created","props":{"harness":"claude","kind":"git"}}`,
		`{"event":"onboarding_completed","props":{}}`,
		// Refused: not a client event, unknown event, bad values, extra keys.
		`{"event":"install_active","props":{"hosts_count":"1","agents_running":"0","bots_count":"0"}}`,
		`{"event":"page_viewed","props":{"url":"https://secret"}}`,
		`{"event":"view_opened","props":{"view":"/home/me/project"}}`,
		`{"event":"feature_used","props":{"feature":"shortcut_used","key":"⌘K"}}`,
		`{"event":"view_opened","props":{"view":"terminal","path":"/etc/passwd"}}`,
	)
	for i := 0; i < len(evs); i += telemetryEventsPerReq {
		chunk := evs[i:min(i+telemetryEventsPerReq, len(evs))]
		for _, app := range []string{"web", "phone"} {
			body := fmt.Sprintf(`{"app":%q,"events":[%s]}`, app, strings.Join(chunk, ","))
			if rec := postTelemetry(t, app+fmt.Sprint(i), body); rec.Code != http.StatusNoContent {
				t.Fatalf("POST /api/telemetry = %d %s", rec.Code, rec.Body.String())
			}
		}
	}
	// An app the endpoint does not speak for.
	postTelemetry(t, "x", `{"app":"mac","events":[{"event":"onboarding_completed","props":{}}]}`)
}

func flushAll(t *testing.T) {
	t.Helper()
	for range 100 {
		if telemetryQ.len() == 0 {
			return
		}
		telemetryFlush(context.Background())
	}
	t.Fatal("queue never drained")
}

// Off, by every route, means no request at all — not even an "opted out" one.
func TestTelemetryOffSendsNothing(t *testing.T) {
	cases := []struct {
		name string
		set  func(t *testing.T)
	}{
		{"setting off", func(t *testing.T) {
			if err := setTelemetryEnabled(false); err != nil {
				t.Fatal(err)
			}
		}},
		{"DO_NOT_TRACK", func(t *testing.T) { t.Setenv("DO_NOT_TRACK", "1") }},
		{"LASSO_TELEMETRY=0", func(t *testing.T) { t.Setenv("LASSO_TELEMETRY", "0") }},
		{"empty key", func(t *testing.T) { t.Setenv("LASSO_TELEMETRY_KEY", "") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := useTelemetry(t)
			c.set(t)
			emitEverything(t)
			telemetryDaily(time.Now())
			for range 3 {
				telemetryFlush(context.Background())
			}
			if n := f.count(); n != 0 {
				t.Fatalf("%d requests while off, want 0", n)
			}
			if telemetryActive() {
				t.Fatal("telemetryActive() = true while off")
			}
			if telemetryQ.len() != 0 {
				t.Fatalf("%d events queued while off", telemetryQ.len())
			}
		})
	}
}

// Switching off drops what was already queued: nothing recorded before the
// switch leaves afterwards.
func TestTelemetrySwitchOffDropsQueue(t *testing.T) {
	f := useTelemetry(t)
	telemetryTrack("server", "onboarding_completed", nil)
	if telemetryQ.len() != 1 {
		t.Fatalf("queued %d, want 1", telemetryQ.len())
	}
	rec := httptest.NewRecorder()
	serveTelemetryConfig(rec, httptest.NewRequest(http.MethodPost, "/api/telemetry/config", strings.NewReader(`{"enabled":false}`)))
	if rec.Code != 200 {
		t.Fatalf("POST config = %d", rec.Code)
	}
	telemetryFlush(context.Background())
	if f.count() != 0 || telemetryQ.len() != 0 {
		t.Fatalf("requests=%d queued=%d after switching off", f.count(), telemetryQ.len())
	}
}

// Every event that reaches PostHog carries only allowlisted keys with allowed
// values, whatever the clients posted.
func TestTelemetryOnlyAllowlistedDataLeaves(t *testing.T) {
	f := useTelemetry(t)
	prevCounts := telemetryCounts
	t.Cleanup(func() { telemetryCounts = prevCounts })
	emitEverything(t)
	flushAll(t)

	evs := f.events()
	if len(evs) == 0 {
		t.Fatal("nothing sent")
	}
	seen := map[string]bool{}
	for _, e := range evs {
		name := e["__event"].(string)
		seen[name] = true
		spec, ok := specEventProps[name]
		if !ok {
			t.Fatalf("event %q is not in the spec", name)
		}
		for k, v := range e {
			switch k {
			case "__event":
			case "app":
				if !slices.Contains([]string{"server", "web", "phone"}, v.(string)) {
					t.Errorf("%s: app=%v", name, v)
				}
			case "app_version":
				if v != lassoSemver {
					t.Errorf("%s: app_version=%v", name, v)
				}
			case "os":
				if !slices.Contains([]string{"linux", "darwin"}, v.(string)) {
					t.Errorf("%s: os=%v", name, v)
				}
			case "arch":
				if v != runtime.GOARCH {
					t.Errorf("%s: arch=%v", name, v)
				}
			case "$lib":
				if v != "lasso" {
					t.Errorf("%s: $lib=%v", name, v)
				}
			case "$geoip_disable":
				if v != true {
					t.Errorf("%s: $geoip_disable=%v", name, v)
				}
			case "$process_person_profile":
				if v != false {
					t.Errorf("%s: $process_person_profile=%v", name, v)
				}
			default:
				allowed, ok := spec[k]
				if !ok {
					t.Errorf("%s carries %q, not in the spec", name, k)
					continue
				}
				s, _ := v.(string)
				if !slices.Contains(allowed, s) {
					t.Errorf("%s: %s=%v, not an allowed value", name, k, v)
				}
			}
		}
		for k := range spec {
			if _, ok := e[k]; !ok {
				t.Errorf("%s lacks %q", name, k)
			}
		}
		// A client's `from` is the app it posted as, never what it claimed.
		if from, ok := e["from"]; ok && e["app"] != "server" && from != e["app"] {
			t.Errorf("%s: from=%v posted as app=%v", name, from, e["app"])
		}
	}
	for name := range specEventProps {
		if !seen[name] {
			t.Errorf("no %s was sent", name)
		}
	}
	// All of it under one random id and the configured key, to /batch/.
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.reqs[0].Batch[0].DistinctID
	if len(id) != 36 || id[14] != '4' {
		t.Errorf("distinct_id %q is not a UUIDv4", id)
	}
	for i, r := range f.reqs {
		if r.APIKey != "phc_test" || f.urls[i] != "https://posthog.test/batch/" {
			t.Errorf("request %d: key=%q url=%q", i, r.APIKey, f.urls[i])
		}
		if len(r.Batch) > telemetryBatchMax {
			t.Errorf("batch of %d, cap %d", len(r.Batch), telemetryBatchMax)
		}
		for _, e := range r.Batch {
			if e.DistinctID != id {
				t.Errorf("distinct_id changed: %q vs %q", e.DistinctID, id)
			}
		}
	}
}

// PostHog down, slow, erroring or unresolvable: nothing surfaces, nothing is
// logged above debug, the request is cut at the deadline, nothing is retried
// and nothing piles up.
func TestTelemetryFailuresStayInvisible(t *testing.T) {
	failures := map[string]func(*http.Request) (*http.Response, error){
		"dns": func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial tcp: lookup posthog.test: no such host")
		},
		"5xx": func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("down"))}, nil
		},
		"hang": func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		},
	}
	prevTimeout := telemetryTimeout
	telemetryTimeout = 200 * time.Millisecond
	t.Cleanup(func() { telemetryTimeout = prevTimeout })
	var logs bytes.Buffer
	prevLog := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prevLog) })

	for name, respond := range failures {
		t.Run(name, func(t *testing.T) {
			f := useTelemetry(t)
			f.respond = respond
			before := runtime.NumGoroutine()
			for range 3 * telemetryBatchMax {
				telemetryTrack("web", "view_opened", map[string]string{"view": "terminal"})
			}
			start := time.Now()
			flushAll(t)
			if d := time.Since(start); d > 3*telemetryTimeout+time.Second {
				t.Errorf("three failing flushes took %v", d)
			}
			// One request per batch: no retries.
			if n := f.count(); n != 3 {
				t.Errorf("%d requests for 3 batches, want 3 (no retries)", n)
			}
			if m := f.maxIn.Load(); m > 1 {
				t.Errorf("%d requests in flight at once", m)
			}
			if g := runtime.NumGoroutine(); g > before+2 {
				t.Errorf("goroutines %d -> %d", before, g)
			}
		})
	}
	// Other tests' background goroutines may log; only telemetry's lines count.
	if strings.Contains(logs.String(), "telemetry") {
		t.Errorf("failures logged without LASSO_TELEMETRY_DEBUG: %q", logs.String())
	}
}

// The queue is capped and drops its oldest events.
func TestTelemetryQueueIsBounded(t *testing.T) {
	useTelemetry(t)
	for i := range 2 * telemetryQueueCap {
		v := "terminal"
		if i >= 2*telemetryQueueCap-1 {
			v = "chat"
		}
		telemetryTrack("web", "view_opened", map[string]string{"view": v})
	}
	if n := telemetryQ.len(); n != telemetryQueueCap {
		t.Fatalf("queue holds %d, cap %d", n, telemetryQueueCap)
	}
	all := telemetryQ.take(telemetryQueueCap)
	if all[len(all)-1].Props["view"] != "chat" {
		t.Error("the newest event was dropped instead of the oldest")
	}
}

// The sender stops with its context even while a request hangs, so shutdown
// never waits on PostHog.
func TestTelemetrySenderStopsWithContext(t *testing.T) {
	f := useTelemetry(t)
	started := make(chan struct{}, 1)
	f.respond = func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { startTelemetry(ctx); close(done) }()
	for range telemetryBatchMax {
		telemetryTrack("web", "view_opened", map[string]string{"view": "grid"})
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("a full batch did not kick the sender")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sender still running a second after its context ended")
	}
}

func TestTelemetryEndpointLimits(t *testing.T) {
	f := useTelemetry(t)
	if rec := httptest.NewRecorder(); true {
		serveTelemetry(rec, httptest.NewRequest(http.MethodGet, "/api/telemetry", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET = %d", rec.Code)
		}
	}
	big := `{"app":"web","events":[` + strings.Repeat(`{"event":"view_opened","props":{"view":"terminal"}},`, 400) + `{}]}`
	if rec := postTelemetry(t, "a", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d", rec.Code)
	}
	if rec := postTelemetry(t, "a", `{"app":`); rec.Code != http.StatusBadRequest {
		t.Errorf("broken json = %d", rec.Code)
	}
	// Rate limit: one client gets telemetryClientPerMin a minute, another its own.
	one := `{"event":"view_opened","props":{"view":"files"}}`
	batch := `{"app":"web","events":[` + strings.TrimSuffix(strings.Repeat(one+",", telemetryEventsPerReq), ",") + `]}`
	for range 10 {
		postTelemetry(t, "busy", batch)
	}
	postTelemetry(t, "quiet", `{"app":"web","events":[`+one+`]}`)
	flushAll(t)
	if got, want := len(f.events()), telemetryClientPerMin+1; got != want {
		t.Errorf("forwarded %d events, want %d (limit %d per client)", got, want, telemetryClientPerMin)
	}
}

func TestTelemetryDailyOncePerDay(t *testing.T) {
	useTelemetry(t)
	prevCounts := telemetryCounts
	telemetryCounts = func() (int, int, int) { return 1, 2, 0 }
	t.Cleanup(func() { telemetryCounts = prevCounts })
	day := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	telemetryDaily(day)
	telemetryDaily(day.Add(5 * time.Hour))
	if n := telemetryQ.len(); n != 1 {
		t.Fatalf("%d install_active on one day, want 1", n)
	}
	telemetryDaily(day.Add(24 * time.Hour))
	if n := telemetryQ.len(); n != 2 {
		t.Fatalf("%d after the next day, want 2", n)
	}
}

func TestTelemetryConfigDefaultsOnAndRoundTrips(t *testing.T) {
	useTelemetry(t)
	get := func() map[string]any {
		rec := httptest.NewRecorder()
		serveTelemetryConfig(rec, httptest.NewRequest(http.MethodGet, "/api/telemetry/config", nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return m
	}
	if m := get(); m["enabled"] != true || m["active"] != true {
		t.Fatalf("fresh config = %v, want enabled and active", m)
	}
	if err := setTelemetryEnabled(false); err != nil {
		t.Fatal(err)
	}
	if m := get(); m["enabled"] != false || m["active"] != false {
		t.Fatalf("after off = %v", m)
	}
	id := telemetryInstallID()
	if err := setTelemetryEnabled(true); err != nil {
		t.Fatal(err)
	}
	if telemetryInstallID() != id {
		t.Error("toggling changed the install id")
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if m := get(); m["enabled"] != true || m["active"] != false || m["env_off"] != "DO_NOT_TRACK" {
		t.Fatalf("under DO_NOT_TRACK = %v", m)
	}
}

func TestTelemetryHarnessNeverLeaksACommand(t *testing.T) {
	for in, want := range map[string]string{
		"claude": "claude", "CODEX": "codex", " omp ": "omp", "aider --model x": "other", "": "other",
	} {
		if got := telemetryHarness(in); got != want {
			t.Errorf("telemetryHarness(%q) = %q, want %q", in, got, want)
		}
	}
}

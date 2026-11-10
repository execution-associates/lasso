package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestBotUpdateDueDay(t *testing.T) {
	la := mustZone(t, "America/Los_Angeles")
	c := botUpdateConfig{Enabled: true, Time: "04:00", Timezone: "America/Los_Angeles"}
	for _, tc := range []struct {
		now     time.Time
		day     string
		due     bool
		comment string
	}{
		{time.Date(2026, 11, 9, 3, 59, 0, 0, la), "", false, "before the time"},
		{time.Date(2026, 11, 9, 4, 0, 0, 0, la), "2026-11-09", true, "at the time"},
		{time.Date(2026, 11, 9, 6, 59, 0, 0, la), "2026-11-09", true, "inside the window"},
		{time.Date(2026, 11, 9, 7, 0, 0, 0, la), "", false, "the window closed"},
		// The same instant seen from another zone is the same answer.
		{time.Date(2026, 11, 9, 13, 30, 0, 0, time.UTC), "2026-11-09", true, "05:30 in LA"},
	} {
		day, due := c.dueDay(tc.now)
		if day != tc.day || due != tc.due {
			t.Errorf("%s: dueDay = %q %v, want %q %v", tc.comment, day, due, tc.day, tc.due)
		}
	}
	// A window that crosses midnight belongs to the day it opened on.
	late := botUpdateConfig{Enabled: true, Time: "23:30", Timezone: "UTC"}
	if day, due := late.dueDay(time.Date(2026, 11, 10, 1, 0, 0, 0, time.UTC)); !due || day != "2026-11-09" {
		t.Errorf("past midnight: %q %v", day, due)
	}
	// Off is never due.
	c.Enabled = false
	if _, due := c.dueDay(time.Date(2026, 11, 9, 4, 30, 0, 0, la)); due {
		t.Error("a disabled setting is due")
	}
}

// 02:30 does not exist in Los Angeles on 2026-03-08; time.Date moves it to
// 03:30 rather than skipping the day.
func TestBotUpdateDSTGap(t *testing.T) {
	la := mustZone(t, "America/Los_Angeles")
	c := botUpdateConfig{Enabled: true, Time: "02:30", Timezone: "America/Los_Angeles"}
	day, due := c.dueDay(time.Date(2026, 3, 8, 4, 0, 0, 0, la))
	if !due || day != "2026-03-08" {
		t.Errorf("spring forward: %q %v", day, due)
	}
}

func TestBotUpdateNextRun(t *testing.T) {
	c := botUpdateConfig{Enabled: true, Time: "04:00", Timezone: "Europe/Berlin"}
	berlin := mustZone(t, "Europe/Berlin")
	next, ok := c.nextRun(time.Date(2026, 11, 9, 1, 0, 0, 0, berlin))
	if !ok || !next.Equal(time.Date(2026, 11, 9, 4, 0, 0, 0, berlin)) {
		t.Errorf("before today's time: %v", next)
	}
	next, _ = c.nextRun(time.Date(2026, 11, 9, 4, 0, 0, 0, berlin))
	if !next.Equal(time.Date(2026, 11, 10, 4, 0, 0, 0, berlin)) {
		t.Errorf("at today's time: %v", next)
	}
}

func TestBotUpdateValidate(t *testing.T) {
	for _, c := range []botUpdateConfig{
		{Time: "4:00", Timezone: "UTC"},
		{Time: "24:00", Timezone: "UTC"},
		{Time: "04:60", Timezone: "UTC"},
		{Time: "04:00", Timezone: "Local"},
		{Time: "04:00", Timezone: ""},
		{Time: "04:00", Timezone: "Mars/Olympus"},
	} {
		if c.validate() == nil {
			t.Errorf("%+v passed", c)
		}
	}
	if err := (botUpdateConfig{Time: "23:59", Timezone: "Asia/Kolkata"}).validate(); err != nil {
		t.Error(err)
	}
}

func TestBotUpdateOutdated(t *testing.T) {
	for _, tc := range []struct {
		running, installed string
		want               bool
	}{
		{"2.1.319", "2.1.321", true},
		{"2.1.321", "2.1.321", false},
		{"2.1.321", "2.1.319", false}, // an older claude on PATH never restarts it
		{"2.1.99", "2.1.100", true},
		{"", "2.1.321", false},
		{"2.1.319", "", false},
	} {
		if got := botUpdateOutdated(tc.running, tc.installed); got != tc.want {
			t.Errorf("%q -> %q = %v", tc.running, tc.installed, got)
		}
	}
}

func TestBotUpdatesAPI(t *testing.T) {
	useBotTestEnv(t)
	do := func(method, body string) (*httptest.ResponseRecorder, botUpdateView) {
		req := httptest.NewRequest(method, "/api/bot-updates?bots=0", strings.NewReader(body))
		w := httptest.NewRecorder()
		serveBotUpdates(w, req)
		var v botUpdateView
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return w, v
	}
	_, v := do("GET", "")
	if v.Enabled || v.Time != "04:00" || v.Timezone == "" || v.NextRun != "" {
		t.Fatalf("defaults = %+v", v)
	}
	w, v := do("POST", `{"enabled":true,"time":"05:15","timezone":"Europe/Berlin"}`)
	if w.Code != 200 || !v.Enabled || v.Time != "05:15" || v.Timezone != "Europe/Berlin" || v.NextRun == "" {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// A patch keeps what it does not name.
	_, v = do("POST", `{"time":"06:00"}`)
	if !v.Enabled || v.Timezone != "Europe/Berlin" || v.Time != "06:00" {
		t.Fatalf("patch = %+v", v)
	}
	if w, _ := do("POST", `{"timezone":"Nowhere/Land"}`); w.Code != 400 {
		t.Fatalf("bad zone: %d", w.Code)
	}
	if _, v := do("GET", ""); v.Time != "06:00" || v.Timezone != "Europe/Berlin" {
		t.Fatalf("a refused patch changed the setting: %+v", v)
	}
}

// updateHarness is a running claude bot with a faked pane and a real Claude
// Code registry entry, on a host whose installed claude the test picks.
type updateHarness struct {
	t         *testing.T
	rec       *botRecord
	regPath   string
	installed string
	restarted []string
	now       time.Time
}

func newUpdateHarness(t *testing.T) *updateHarness {
	t.Helper()
	useBotTestEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	h := &updateHarness{t: t, installed: "2.1.321"}
	dir := filepath.Join(t.TempDir(), "jess")
	h.rec = &botRecord{Name: "jess", Dir: dir, Runtime: botRuntimeClaude}
	if err := h.rec.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(h.rec); err != nil {
		t.Fatal(err)
	}
	sessions := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	h.regPath = filepath.Join(sessions, fmt.Sprintf("%d.json", os.Getpid()))
	h.session("2.1.319", "idle")

	prevPane, prevVersion, prevRestart := pluginAgentPane, botToolVersion, botUpdateRestart
	pluginAgentPane = func(_ Backend, agent string) (pane, error) {
		if agent == "jess" {
			return pane{PaneID: "w:p1", Cwd: dir, Agent: "claude"}, nil
		}
		return pane{}, errPluginAgentNotRunning
	}
	botToolVersion = func(Backend, string) (string, error) { return h.installed, nil }
	botUpdateRestart = func(_ Backend, r *botRecord) error {
		h.restarted = append(h.restarted, r.Name)
		h.session(h.installed, "idle")
		return nil
	}
	t.Cleanup(func() { pluginAgentPane, botToolVersion, botUpdateRestart = prevPane, prevVersion, prevRestart })
	botUpdates.Lock()
	botUpdates.tried, botUpdates.installed = map[string]string{}, map[string]botInstalledVersion{}
	botUpdates.Unlock()

	b, _ := json.Marshal(botUpdateConfig{Enabled: true, Time: "04:00", Timezone: "UTC"})
	if err := setSetting(botUpdateSettingKey, string(b)); err != nil {
		t.Fatal(err)
	}
	h.now = time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)
	return h
}

// session rewrites the bot's registry entry.
func (h *updateHarness) session(version, status string) {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{
		"pid": os.Getpid(), "sessionId": "7993a7f6-743d-4293-a660-90c96266fa07", "cwd": filepath.Clean(h.rec.Dir),
		"kind": "interactive", "name": "jess", "status": status, "version": version, "updatedAt": 1,
	})
	if err := os.WriteFile(h.regPath, b, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *updateHarness) tick(d time.Duration) {
	h.t.Helper()
	for end := h.now.Add(d); h.now.Before(end); {
		h.now = h.now.Add(10 * time.Second)
		list, err := listBots()
		if err != nil {
			h.t.Fatal(err)
		}
		botUpdateTick(list, map[string][]claudeSessionEntry{}, h.now)
	}
}

func TestBotUpdateTickRestartsAnOutdatedIdleBot(t *testing.T) {
	h := newUpdateHarness(t)
	h.tick(55 * time.Minute) // 03:55: not yet
	if len(h.restarted) != 0 {
		t.Fatalf("restarted before the time: %v", h.restarted)
	}
	h.tick(10 * time.Minute) // through 04:05
	if strings.Join(h.restarted, ",") != "jess" {
		t.Fatalf("restarted = %v, want jess once", h.restarted)
	}
}

func TestBotUpdateTickWaitsForTheTurn(t *testing.T) {
	h := newUpdateHarness(t)
	h.session("2.1.319", "busy")
	h.tick(2 * time.Hour) // 05:00, busy throughout
	if len(h.restarted) != 0 {
		t.Fatalf("restarted mid-turn: %v", h.restarted)
	}
	h.session("2.1.319", "idle")
	h.tick(time.Minute)
	if len(h.restarted) != 1 {
		t.Fatalf("not restarted once idle: %v", h.restarted)
	}
}

func TestBotUpdateTickSkips(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		h := newUpdateHarness(t)
		h.session("2.1.321", "idle")
		h.tick(2 * time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted = %v", h.restarted)
		}
	})
	t.Run("busy past the window", func(t *testing.T) {
		h := newUpdateHarness(t)
		h.session("2.1.319", "busy")
		h.tick(4 * time.Hour) // 07:00: the window closed
		h.session("2.1.319", "idle")
		h.tick(time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted outside the window: %v", h.restarted)
		}
	})
	t.Run("off", func(t *testing.T) {
		h := newUpdateHarness(t)
		b, _ := json.Marshal(botUpdateConfig{Enabled: false, Time: "04:00", Timezone: "UTC"})
		_ = setSetting(botUpdateSettingKey, string(b))
		h.tick(2 * time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted while off: %v", h.restarted)
		}
	})
	t.Run("held", func(t *testing.T) {
		h := newUpdateHarness(t)
		if err := setBotHold(h.rec.Name, -1, "testing"); err != nil {
			t.Fatal(err)
		}
		h.tick(2 * time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted while held: %v", h.restarted)
		}
	})
}

// A restart that did not take (the bot still on the old version) is not
// retried every tick: once per day.
func TestBotUpdateTickOncePerDay(t *testing.T) {
	h := newUpdateHarness(t)
	botUpdateRestart = func(_ Backend, r *botRecord) error {
		h.restarted = append(h.restarted, r.Name)
		return nil
	}
	h.tick(2 * time.Hour)
	if len(h.restarted) != 1 {
		t.Fatalf("day one: %v", h.restarted)
	}
	h.tick(24 * time.Hour)
	if len(h.restarted) != 2 {
		t.Fatalf("day two: %v", h.restarted)
	}
}

func TestPrintedVersion(t *testing.T) {
	for in, want := range map[string]string{
		"2.1.321 (Claude Code)\n": "2.1.321",
		"omp/18.18.6\n":           "18.18.6",
		"omp v18.18.6":            "",
	} {
		if got := printedVersion(in); got != want {
			t.Errorf("printedVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// ompUpdateBot is a connected omp bot whose bridge said hello with the omp it
// runs, on the claude harness's setting and clock.
func ompUpdateBot(h *updateHarness, version string, streaming bool) *botRecord {
	h.t.Helper()
	r := &botRecord{Name: "luna", Dir: filepath.Join(h.t.TempDir(), "luna"), Runtime: botRuntimeOmp}
	if err := r.normalize(); err != nil {
		h.t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		h.t.Fatal(err)
	}
	ob := ompBotFor(r.Name)
	a, b := net.Pipe()
	h.t.Cleanup(func() {
		a.Close()
		b.Close()
		ompBots.Lock()
		delete(ompBots.m, r.Name)
		ompBots.Unlock()
	})
	ob.mu.Lock()
	ob.conn = a
	ob.mu.Unlock()
	hello, _ := json.Marshal(map[string]any{"type": "host_hello", "protocol": botBridgeProtocol, "version": "test",
		"omp_version": version, "ready": true, "streaming": streaming})
	ob.observe(hello)
	return r
}

func TestBotUpdateTickRestartsAnOutdatedOmpBot(t *testing.T) {
	h := newUpdateHarness(t)
	h.session("2.1.321", "idle") // the claude bot is current
	botToolVersion = func(_ Backend, tool string) (string, error) {
		if tool == "omp" {
			return "18.18.6", nil
		}
		return h.installed, nil
	}
	ompUpdateBot(h, "18.18.5", true)
	h.tick(2 * time.Hour) // 05:00, mid-turn throughout
	if len(h.restarted) != 0 {
		t.Fatalf("restarted mid-turn: %v", h.restarted)
	}
	ob := ompBotFor("luna")
	ob.mu.Lock()
	ob.streaming = false
	ob.mu.Unlock()
	h.tick(time.Minute)
	if strings.Join(h.restarted, ",") != "luna" {
		t.Fatalf("restarted = %v, want luna", h.restarted)
	}
	v := botUpdateBots(h.now)
	var got []string
	for _, b := range v {
		got = append(got, b.Name+":"+b.Runtime+":"+b.Running+":"+b.Installed)
	}
	if want := "jess:claude:2.1.321:2.1.321,luna:omp:18.18.5:18.18.6"; strings.Join(got, ",") != want {
		t.Fatalf("bots = %v, want %s", got, want)
	}
}

func TestBotUpdateOmpSkips(t *testing.T) {
	t.Run("up to date", func(t *testing.T) {
		h := newUpdateHarness(t)
		h.session("2.1.321", "idle")
		botToolVersion = func(_ Backend, tool string) (string, error) {
			if tool == "omp" {
				return "18.18.6", nil
			}
			return h.installed, nil
		}
		ompUpdateBot(h, "18.18.6", false)
		h.tick(2 * time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted = %v", h.restarted)
		}
	})
	t.Run("an older bridge says no version", func(t *testing.T) {
		h := newUpdateHarness(t)
		h.session("2.1.321", "idle")
		botToolVersion = func(_ Backend, tool string) (string, error) {
			if tool == "omp" {
				return "18.18.6", nil
			}
			return h.installed, nil
		}
		ompUpdateBot(h, "", false)
		h.tick(2 * time.Hour)
		if len(h.restarted) != 0 {
			t.Fatalf("restarted = %v", h.restarted)
		}
	})
}

// An omp bot is restarted the way `mise run restart` does it: the marker,
// which its bridge acts on once idle.
func TestBotUpdateRestartOmpLeavesTheMarker(t *testing.T) {
	useBotTestEnv(t)
	dir := t.TempDir()
	r := &botRecord{Name: "luna", Dir: dir, Runtime: botRuntimeOmp}
	b, err := botBackend("local")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(botRestartMarker(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := botUpdateRestart(b, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(botRestartMarker(dir)); err != nil {
		t.Fatalf("no restart marker: %v", err)
	}
}

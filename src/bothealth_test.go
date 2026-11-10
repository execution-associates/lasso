package main

import (
	"strings"
	"testing"
	"time"
)

func TestEvalBotHealth(t *testing.T) {
	up := botHealthSignals{Pane: true, AgentUp: true}
	for _, tc := range []struct {
		name  string
		s     botHealthSignals
		state string
		want  string // a fragment of the reason
	}{
		{"stopped wins", botHealthSignals{Stopped: true}, "stopped", ""},
		{"host down", botHealthSignals{HostErr: "ssh: timeout"}, "unknown", "did not answer"},
		{"no pane", botHealthSignals{}, "gone", "no pane"},
		{"no pane, starting", botHealthSignals{Starting: true}, "starting", ""},
		{"plain shell", botHealthSignals{Pane: true}, "shell", "plain shell"},
		{"omp pane, no bridge", botHealthSignals{Omp: true, Pane: true}, "shell", "bridge"},
		{"shell inside grace", botHealthSignals{Pane: true, Starting: true}, "starting", ""},
		{"up", up, "ok", ""},
		{"no grant", botHealthSignals{Pane: true, AgentUp: true, MissingGrants: []string{"lasso-channel"}}, "deaf", "without its channel grant for lasso-channel"},
		{"channel stale", botHealthSignals{Pane: true, AgentUp: true, Channel: true, ChannelAge: 5 * time.Minute}, "deaf", "last polled 5m0s ago"},
		{"channel never", botHealthSignals{Pane: true, AgentUp: true, Channel: true, ChannelAge: -1}, "deaf", "never polled"},
		{"channel fresh", botHealthSignals{Pane: true, AgentUp: true, Channel: true, ChannelAge: 10 * time.Second}, "ok", ""},
		{"stale channel while starting", botHealthSignals{Pane: true, AgentUp: true, Channel: true, ChannelAge: -1, Starting: true}, "starting", ""},
		{"acked, unhandled", botHealthSignals{Pane: true, AgentUp: true, Unhandled: 42}, "deaf", "event 42 was delivered"},
		{"queued", botHealthSignals{Pane: true, AgentUp: true, Queued: 7}, "deaf", "event 7 is still waiting"},
		{"omp probe", botHealthSignals{Omp: true, Pane: true, AgentUp: true, ProbeFailed: "timeout"}, "deaf", "omp stopped answering"},
		{"bridge check", botHealthSignals{Pane: true, AgentUp: true, Checks: []string{"telegram: polling_ok is false"}}, "deaf", "polling_ok is false"},
	} {
		h := evalBotHealth(tc.s)
		if h.State != tc.state || !strings.Contains(h.Reason, tc.want) {
			t.Errorf("%s: got %s %q, want %s ~%q", tc.name, h.State, h.Reason, tc.state, tc.want)
		}
	}
}

func TestBotMissingGrants(t *testing.T) {
	argv := []string{"claude", "--mcp-config", "/x", "--dangerously-load-development-channels", "server:telegram", "--name", "jess"}
	got := botMissingGrants(argv, []string{"telegram", "lasso-channel"})
	if len(got) != 1 || got[0] != "lasso-channel" {
		t.Errorf("missing = %v", got)
	}
	if got := botMissingGrants(argv, []string{"telegram"}); len(got) != 0 {
		t.Errorf("missing = %v", got)
	}
}

func TestBotIncomingEvents(t *testing.T) {
	items := []chatItem{
		{Kind: "user", At: "2026-11-09T10:00:00Z", Text: "hi"},
		{Kind: "incoming", Source: botChannelName, Text: `<channel source="lasso-channel" job="j" event_id="12" count="1">x</channel>`},
		{Kind: "incoming", Source: "telegram", Text: `event_id="99"`},
		{Kind: "incoming", Source: botChannelName, Text: `<channel event_id="15">y</channel>`},
	}
	max, oldest := botIncomingEvents(items)
	if max != 15 || oldest != "2026-11-09T10:00:00Z" {
		t.Errorf("max %d oldest %q", max, oldest)
	}
}

func TestBotHoldInput(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	if u, _, err := (botHoldInput{}).resolve(now); err != nil || u != -1 {
		t.Errorf("no time: %d %v", u, err)
	}
	if u, _, err := (botHoldInput{Minutes: 60}).resolve(now); err != nil || u != now.Unix()+3600 {
		t.Errorf("minutes: %d %v", u, err)
	}
	if _, _, err := (botHoldInput{Until: now.Add(-time.Hour).Format(time.RFC3339)}).resolve(now); err == nil {
		t.Error("a past until was accepted")
	}
	if _, _, err := (botHoldInput{Minutes: 5, Until: now.Add(time.Hour).Format(time.RFC3339)}).resolve(now); err == nil {
		t.Error("minutes and until together were accepted")
	}
	r := &botRecord{HoldUntil: now.Unix() + 10}
	if !botHeld(r, now) || botHeld(r, now.Add(11*time.Second)) {
		t.Error("a timed hold does not lapse on time")
	}
	if !botHeld(&botRecord{HoldUntil: -1}, now) || botHeld(&botRecord{}, now) {
		t.Error("indefinite / no hold")
	}
}

// stepper drives stepBotAlert through a timeline of health states.
type stepper struct {
	t   *testing.T
	st  botAlertState
	cfg botAlertConfig
	now time.Time
}

func newStepper(t *testing.T) *stepper {
	return &stepper{t: t, st: botAlertState{Bot: "b", State: "ok"}, now: time.Unix(1_800_000_000, 0),
		cfg: botAlertConfig{DownAfter: 2 * time.Minute, UpAfter: time.Minute, RemindMax: 2}}
}

// run steps every 10s for d in state, returning the events sent.
func (s *stepper) run(state string, held bool, d time.Duration) []string {
	var out []string
	for end := s.now.Add(d); s.now.Before(end); s.now = s.now.Add(10 * time.Second) {
		var ev string
		s.st, ev = stepBotAlert(s.st, botHealth{State: state, Reason: state + " reason"}, held, s.cfg, s.now)
		if ev != "" {
			out = append(out, ev)
		}
	}
	return out
}

func TestStepBotAlertWindows(t *testing.T) {
	s := newStepper(t)
	if ev := s.run("gone", false, 110*time.Second); len(ev) != 0 {
		t.Fatalf("paged inside down_after: %v", ev)
	}
	if ev := s.run("gone", false, 20*time.Second); len(ev) != 1 || ev[0] != alertDown {
		t.Fatalf("down: %v", ev)
	}
	if ev := s.run("gone", false, time.Hour); len(ev) != 0 {
		t.Fatalf("no reminders configured, got %v", ev)
	}
	// ok for less than up_after, then bad again: no recovery, no second down.
	if ev := s.run("ok", false, 50*time.Second); len(ev) != 0 {
		t.Fatalf("recovered inside up_after: %v", ev)
	}
	if ev := s.run("shell", false, 30*time.Second); len(ev) != 0 {
		t.Fatalf("flapped: %v", ev)
	}
	if s.st.DownReason != "shell reason" {
		t.Errorf("reason not updated while down: %q", s.st.DownReason)
	}
	if ev := s.run("ok", false, 70*time.Second); len(ev) != 1 || ev[0] != alertRecovered {
		t.Fatalf("recovered: %v", ev)
	}
}

func TestStepBotAlertRelaunchDoesNotPage(t *testing.T) {
	s := newStepper(t)
	// keep-running: gone for 60s, starting for 20s, then up.
	ev := append(s.run("gone", false, 60*time.Second), s.run("starting", false, 20*time.Second)...)
	ev = append(ev, s.run("ok", false, 5*time.Minute)...)
	if len(ev) != 0 {
		t.Fatalf("a relaunch paged: %v", ev)
	}
}

func TestStepBotAlertCrashLoopPages(t *testing.T) {
	s := newStepper(t)
	var ev []string
	for i := 0; i < 4; i++ {
		ev = append(ev, s.run("gone", false, 30*time.Second)...)
		ev = append(ev, s.run("starting", false, 40*time.Second)...)
	}
	if len(ev) != 1 || ev[0] != alertDown {
		t.Fatalf("a crash loop: %v", ev)
	}
}

func TestStepBotAlertReminders(t *testing.T) {
	s := newStepper(t)
	s.cfg.RemindEvery = 10 * time.Minute
	ev := s.run("deaf", false, 2*time.Hour)
	if strings.Join(ev, ",") != "down,reminder,reminder" {
		t.Fatalf("events = %v (remind_max 2)", ev)
	}
}

func TestStepBotAlertHold(t *testing.T) {
	s := newStepper(t)
	if ev := s.run("gone", true, time.Hour); len(ev) != 0 {
		t.Fatalf("a held bot paged for being gone: %v", ev)
	}
	if ev := s.run("deaf", true, 3*time.Minute); len(ev) != 1 || ev[0] != alertDown {
		t.Fatalf("a held, deaf bot did not page: %v", ev)
	}
	// Held and gone resolves the outage at once.
	if ev := s.run("gone", true, 10*time.Second); len(ev) != 1 || ev[0] != alertRecovered {
		t.Fatalf("hold did not resolve: %v", ev)
	}
	// A human Stop resolves at once too.
	s.run("gone", false, 3*time.Minute)
	if !s.st.Down {
		t.Fatal("not down after release")
	}
	if ev := s.run("stopped", false, 10*time.Second); len(ev) != 1 || ev[0] != alertRecovered {
		t.Fatalf("stop did not resolve: %v", ev)
	}
}

func TestStepBotAlertUnknownWaitsLonger(t *testing.T) {
	s := newStepper(t)
	if ev := s.run("unknown", false, 5*time.Minute); len(ev) != 0 {
		t.Fatalf("an unreachable host paged within 3 windows: %v", ev)
	}
	if ev := s.run("unknown", false, 2*time.Minute); len(ev) != 1 {
		t.Fatalf("an unreachable host never paged: %v", ev)
	}
}

func TestAlertSettingsValidation(t *testing.T) {
	s := alertDefaults
	if err := s.validate(); err != nil {
		t.Fatal(err)
	}
	s.RemindEvery = 60
	if err := s.validate(); err == nil {
		t.Error("remind_every under 5 minutes accepted")
	}
	a := botAlerts{Mode: "loud"}
	if err := a.normalize(); err == nil {
		t.Error("bad mode accepted")
	}
	a = botAlerts{Mode: "inherit", Channels: []int64{1}}
	if err := a.normalize(); err != nil || a.Mode != "" || a.Channels != nil || !a.empty() {
		t.Errorf("inherit keeps channels: %+v %v", a, err)
	}
	a = botAlerts{HealthChecks: []botHealthCheck{{URL: "file:///etc/passwd"}}}
	if err := a.normalize(); err == nil {
		t.Error("a file: health check accepted")
	}
}

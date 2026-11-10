package main

// Bot liveness end to end: a real bot row, a faked herdr pane, real alert
// channels pointing at httptest receivers, and the runner's health tick driven
// on a fake clock.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type liveHarness struct {
	t          *testing.T
	rec        *botRecord
	pane       *pane // nil: the bot has no pane
	hook, beat *recorder
	now        time.Time
	relaunched int
}

func newLiveHarness(t *testing.T, name string) *liveHarness {
	t.Helper()
	useAlertTestEnv(t)
	t.Setenv("HOME", t.TempDir()) // no real Claude Code session registry
	h := &liveHarness{t: t, hook: newRecorder(t), beat: newRecorder(t), now: time.Now().Add(time.Hour).Truncate(time.Second)}
	dir := filepath.Join(t.TempDir(), name)
	h.rec = &botRecord{Name: name, Dir: dir, Runtime: botRuntimeClaude}
	if err := h.rec.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(h.rec); err != nil {
		t.Fatal(err)
	}
	prevPane := pluginAgentPane
	pluginAgentPane = func(_ Backend, agent string) (pane, error) {
		if agent == name && h.pane != nil {
			return *h.pane, nil
		}
		return pane{}, errPluginAgentNotRunning
	}
	prevRelaunch := botRelaunch
	botRelaunch = func(Backend, *botRecord) error { h.relaunched++; return nil }
	t.Cleanup(func() { pluginAgentPane, botRelaunch = prevPane, prevRelaunch })
	bots.mu.Lock()
	bots.misses, bots.starting, bots.lines = map[string]int{}, map[string]time.Time{}, map[string]botLastLine{}
	bots.mu.Unlock()
	heartbeatState.Lock()
	heartbeatState.last, heartbeatState.running, heartbeatState.failing = map[int64]time.Time{}, map[int64]bool{}, map[int64]string{}
	heartbeatState.Unlock()

	hook, err := createAlertChannel(alertChannelInput{Kind: "webhook", Secrets: map[string]string{"url": h.hook.srv.URL + "/alert", "signing_secret": "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := createAlertChannel(alertChannelInput{Kind: "heartbeat", Config: map[string]string{"bot": name, "every": "30"}, Secrets: map[string]string{"url": h.beat.srv.URL + "/api/ping/tok", "ping_secret": "mcs_test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyAlertSettingsPatch(alertSettingsPatch{Channels: &[]int64{hook.ID}}); err != nil {
		t.Fatal(err)
	}
	h.up()
	return h
}

func (h *liveHarness) up()   { h.pane = &pane{PaneID: "w:p1", Cwd: h.rec.Dir, Agent: "claude"} }
func (h *liveHarness) gone() { h.pane = nil }

// tick runs the runner's health pass every 10s for d.
func (h *liveHarness) tick(d time.Duration) {
	h.t.Helper()
	for end := h.now.Add(d); h.now.Before(end); {
		h.now = h.now.Add(10 * time.Second)
		list, err := listBots()
		if err != nil {
			h.t.Fatal(err)
		}
		botHealthTick(list, nil, h.now)
		alertWG.Wait()
	}
}

// events is the webhook's alert types, in order, and clears them.
func (h *liveHarness) events() []string {
	var out []string
	for _, r := range h.hook.all() {
		var ev alertEvent
		_ = json.Unmarshal(r.body, &ev)
		out = append(out, ev.Type+":"+ev.State)
	}
	h.hook.reset()
	return out
}

// beats counts the heartbeat's requests by kind (ping, report, pause,
// resume), and clears them. Every one must carry the ping secret.
func (h *liveHarness) beats() map[string]int {
	out := map[string]int{}
	for _, r := range h.beat.all() {
		if r.header.Get("Authorization") != "Bearer mcs_test" {
			h.t.Errorf("%s without the ping secret", r.path)
		}
		kind := "ping"
		for _, k := range []string{"report", "pause", "resume"} {
			if strings.HasSuffix(r.path, "/"+k) {
				kind = k
			}
		}
		out[kind]++
	}
	h.beat.reset()
	return out
}

func (h *liveHarness) reload() {
	r, err := getBot(h.rec.Name)
	if err != nil {
		h.t.Fatal(err)
	}
	h.rec = r
}

func TestBotAlertsDownThenRecovered(t *testing.T) {
	h := newLiveHarness(t, "jess")
	h.tick(90 * time.Second)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("a healthy bot alerted: %v", ev)
	}
	if b := h.beats(); b["ping"] < 2 || b["resume"] != 1 || b["pause"] != 0 {
		// The first pass re-asserts "not paused" once (ping, resume, ping).
		t.Fatalf("heartbeat while up = %v", b)
	}
	if hl := botHealthOf("jess"); hl == nil || hl.State != "ok" {
		t.Fatalf("health = %+v", hl)
	}

	h.gone()
	h.tick(110 * time.Second)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("alerted inside down_after: %v", ev)
	}
	if b := h.beats(); len(b) != 0 {
		t.Fatalf("heartbeat while the bot was gone: %v", b)
	}
	h.tick(20 * time.Second)
	if ev := h.events(); len(ev) != 1 || ev[0] != "down:gone" {
		t.Fatalf("down = %v", ev)
	}
	if b := h.beats(); b["report"] != 1 || len(b) != 1 {
		t.Fatalf("the heartbeat's report: %v", b)
	}
	// In lasso's own Inbox too.
	found := false
	for _, n := range inboxNotices.items {
		if n.Kind == string(notifBotDown) && n.Bot == "jess" {
			found = true
		}
	}
	if !found {
		t.Error("no bot_down notice in the Inbox")
	}
	h.tick(10 * time.Minute)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("repeated while down (no reminders set): %v", ev)
	}

	h.up()
	h.tick(50 * time.Second)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("recovered inside up_after: %v", ev)
	}
	h.tick(20 * time.Second)
	if ev := h.events(); len(ev) != 1 || ev[0] != "recovered:ok" {
		t.Fatalf("recovered = %v", ev)
	}
	h.tick(time.Minute)
	if b := h.beats(); b["ping"] < 2 {
		t.Fatalf("pings after recovery = %v", b)
	}
	if d := listAlertDeliveries("jess", 10); len(d) < 2 {
		t.Errorf("deliveries = %+v", d)
	}
}

func TestBotAlertsQuickRelaunchDoesNotPage(t *testing.T) {
	h := newLiveHarness(t, "clem")
	h.tick(30 * time.Second)
	h.gone()
	h.tick(60 * time.Second)
	bots.mu.Lock()
	bots.starting["clem"] = h.now
	bots.mu.Unlock()
	h.tick(20 * time.Second)
	h.up()
	h.tick(5 * time.Minute)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("a relaunch inside down_after paged: %v", ev)
	}
}

func TestBotAlertsHold(t *testing.T) {
	h := newLiveHarness(t, "fitty")
	if _, err := db.Exec(`UPDATE bots SET keep_running = 1`); err != nil {
		t.Fatal(err)
	}
	h.tick(30 * time.Second)
	h.beats()

	// Held, then gone: no alert, no relaunch, and the monitor stays green.
	w := alertHTTPRecorderFor(t, "POST", "/api/bots/fitty/hold", `{"reason":"upgrading"}`)
	if w.Code != 200 {
		t.Fatalf("hold: %d %s", w.Code, w.Body)
	}
	h.reload()
	h.gone()
	h.tick(10 * time.Minute)
	for i := 0; i < botMissesBeforeRelaunch+2; i++ {
		botTick()
	}
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("a held, gone bot paged: %v", ev)
	}
	if h.relaunched != 0 {
		t.Fatalf("keep-running relaunched a held bot %d times", h.relaunched)
	}
	for _, r := range h.beat.all() {
		if strings.HasSuffix(r.path, "/pause") && !strings.Contains(string(r.body), "held: upgrading") {
			t.Errorf("the pause does not say why: %s", r.body)
		}
	}
	if b := h.beats(); b["pause"] != 1 || b["ping"] != 0 || b["report"] != 0 {
		// A deliberate outage pauses the monitor once and pings nothing.
		t.Fatalf("heartbeat while held = %v", b)
	}
	v := botStatus(&botHerdrFake{&localBackend{}}, h.rec, nil)
	if v.Hold == nil || v.Hold.Reason != "upgrading" || v.Health == nil || v.Health.State != "gone" {
		t.Errorf("view = hold %+v health %+v", v.Hold, v.Health)
	}

	// Held but up and deaf: still pages.
	h.up()
	if _, err := db.Exec(`UPDATE bots SET channel_token = 'tok', channel_seen_at = ? WHERE name = 'fitty'`, nowStamp(h.now.Add(-10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	h.tick(3 * time.Minute)
	if ev := h.events(); len(ev) != 1 || ev[0] != "down:deaf" {
		t.Fatalf("held and deaf = %v", ev)
	}
	// Alerted down while held: the monitor is resumed (no ping, the bot is
	// not healthy) so it goes down with the bot, and gets the reason.
	if b := h.beats(); b["resume"] != 1 || b["ping"] != 0 || b["report"] != 1 {
		t.Fatalf("heartbeat for a held, deaf bot = %v", b)
	}
	if hl := botHealthOf("fitty"); hl == nil || !strings.Contains(hl.Reason, "last polled") {
		t.Errorf("health = %+v", hl)
	}

	// Released while gone: it pages, and keep-running relaunches it.
	if w := alertHTTPRecorderFor(t, "DELETE", "/api/bots/fitty/hold", ""); w.Code != 200 {
		t.Fatalf("release: %d", w.Code)
	}
	h.reload()
	h.gone()
	h.tick(3 * time.Minute)
	if ev := h.events(); len(ev) != 1 || ev[0] != "reminder:gone" && ev[0] != "down:gone" {
		// Still down from the deaf alert: the reason moves on, nothing new is sent.
		if len(ev) != 0 {
			t.Fatalf("after release = %v", ev)
		}
	}
	for i := 0; i < botMissesBeforeRelaunch; i++ {
		botTick()
	}
	if h.relaunched != 1 {
		t.Fatalf("relaunches after release = %d", h.relaunched)
	}
}

func alertHTTPRecorderFor(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	serveBots(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	return w
}

func flockNB(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }

func TestBotAlertsShellRestore(t *testing.T) {
	h := newLiveHarness(t, "olivia")
	h.tick(30 * time.Second)
	// herdr restored the pane, but into a plain shell.
	h.pane = &pane{PaneID: "w:p1", Cwd: h.rec.Dir}
	h.tick(130 * time.Second)
	if ev := h.events(); len(ev) != 1 || ev[0] != "down:shell" {
		t.Fatalf("shell = %v", ev)
	}
}

func TestBotAlertsAckedButNeverHandled(t *testing.T) {
	h := newLiveHarness(t, "stub")
	// The bot's transcript: one turn before any delivery, and no delivery since.
	transcript := filepath.Join(t.TempDir(), "s1.jsonl")
	start := h.now.Add(-time.Hour).UTC().Format(time.RFC3339)
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":%q,"message":{"role":"user","content":"hello"}}`, start),
		fmt.Sprintf(`{"type":"user","uuid":"u2","timestamp":%q,"isMeta":true,"origin":{"kind":"channel","server":"lasso-channel"},"message":{"role":"user","content":"<channel source=\"lasso-channel\" event_id=\"1\">old</channel>"}}`, start),
	}
	if err := os.WriteFile(transcript, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.pane = &pane{PaneID: "w:p1", Cwd: h.rec.Dir, Agent: "claude", AgentSession: &agentSession{Kind: "path", Agent: "claude", Value: transcript}}
	// lasso's channel is in its launch and polling.
	seen := func() {
		_, _ = db.Exec(`UPDATE bots SET channel_token = 'tok', channel_seen_at = ? WHERE name = 'stub'`, nowStamp(h.now))
	}
	seen()
	acked := h.now.Add(-time.Minute)
	if _, err := db.Exec(`INSERT INTO bot_events (id, bot, job_id, job, kind, content, count, status, created_at, fired_at, claimed_at, done_at)
		VALUES (5, 'stub', 0, 'j', 'schedule', 'x', 1, 'delivered', ?, ?, ?, ?)`, nowStamp(acked), nowStamp(acked), nowStamp(acked), nowStamp(acked)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ { // 500s: past handle_within (300s) and down_after
		seen()
		h.tick(10 * time.Second)
	}
	ev := h.events()
	if len(ev) != 1 || ev[0] != "down:deaf" {
		t.Fatalf("acked, unhandled = %v", ev)
	}
	if hl := botHealthOf("stub"); hl == nil || !strings.Contains(hl.Reason, "never reached the conversation") {
		t.Errorf("health = %+v", hl)
	}
	// The delivery shows up in the conversation: the bot recovers.
	arrived := fmt.Sprintf(`{"type":"user","uuid":"u3","timestamp":%q,"isMeta":true,"origin":{"kind":"channel","server":"lasso-channel"},"message":{"role":"user","content":"<channel source=\"lasso-channel\" event_id=\"5\">x</channel>"}}`, acked.UTC().Format(time.RFC3339))
	f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(arrived + "\n")
	f.Close()
	_ = os.Chtimes(transcript, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	for i := 0; i < 8; i++ {
		seen()
		h.tick(10 * time.Second)
	}
	if ev := h.events(); len(ev) != 1 || ev[0] != "recovered:ok" {
		t.Fatalf("after the event arrived = %v (health %+v)", ev, botHealthOf("stub"))
	}
}

func TestBotAlertsOmpProbe(t *testing.T) {
	r, _, l := useOmpBotEnvL(t, "ompy")
	prev := alertRetryDelays
	alertRetryDelays = []time.Duration{0}
	t.Cleanup(func() { alertRetryDelays = prev })
	prevT := botOmpProbeTimeout
	botOmpProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { botOmpProbeTimeout = prevT })
	ompProbes.Lock()
	delete(ompProbes.m, "ompy")
	ompProbes.Unlock()
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })
	botOmpTick(b, r, time.Now()) // the runner's pass clears the start grace once ready
	cfg := effectiveAlertConfig(alertDefaults, r)
	sig := func(now time.Time) botHealthSignals {
		return botHealthSignalsFor(r, cfg, map[string][]claudeSessionEntry{}, now)
	}
	now := time.Now()
	if h := evalBotHealth(sig(now)); h.State != "ok" {
		t.Fatalf("an answering bridge = %+v", h)
	}
	l.mu.Lock()
	f := l.panes["ompy"]
	l.mu.Unlock()
	f.mu.Lock()
	f.silent = true
	f.mu.Unlock()
	ompWaitFor(t, "a failed probe", func() bool {
		now = now.Add(2 * ompProbeEvery)
		return evalBotHealth(sig(now)).State == "deaf"
	})
	f.mu.Lock()
	f.silent = false
	f.mu.Unlock()
	l.close(nil, r)
	ompWaitFor(t, "gone", func() bool { return evalBotHealth(sig(time.Now())).State == "gone" })
}

func TestBotAlertsOnlyTheRunnerSends(t *testing.T) {
	h := newLiveHarness(t, "solo")
	// Another lasso holds the runner lock.
	bots.mu.Lock()
	held := bots.runLock
	bots.runLock = nil
	bots.mu.Unlock()
	t.Cleanup(func() {
		bots.mu.Lock()
		if bots.runLock != nil {
			bots.runLock.Close()
		}
		bots.runLock = held
		bots.mu.Unlock()
	})
	other, err := os.OpenFile(filepath.Join(lassoDir(), "bots.runner.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := flockNB(other); err != nil {
		t.Fatal(err)
	}
	h.gone()
	for i := 0; i < 30; i++ {
		botTick()
	}
	alertWG.Wait()
	if st := loadBotAlertState("solo"); st.State == "gone" {
		t.Fatal("a lasso without the runner lock evaluated the bot")
	}
}

func TestBotAlertsOverMCP(t *testing.T) {
	h := newLiveHarness(t, "mcpbot")
	h.tick(20 * time.Second)
	sess := mcpTestSession(t, "")
	var ch alertChannelOut
	if msg := callTool(t, sess, "create_alert_channel", map[string]any{"kind": "slack", "secrets": map[string]any{"webhook_url": h.hook.srv.URL + "/slack/SECRET"}}, &ch); msg != "" {
		t.Fatal(msg)
	}
	if ch.Channel == nil || ch.Channel.Kind != "slack" || len(ch.Channel.SecretsSet) != 1 {
		t.Fatalf("created = %+v", ch.Channel)
	}
	var list alertChannelsOut
	callTool(t, sess, "list_alert_channels", map[string]any{}, &list)
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), "SECRET") || len(list.Channels) != 3 || len(list.Kinds) != 6 {
		t.Fatalf("list = %s", raw)
	}
	var tr testAlertOut
	if msg := callTool(t, sess, "test_alert_channel", map[string]any{"id": ch.Channel.ID}, &tr); msg != "" || !tr.OK {
		t.Fatalf("test = %v %+v", msg, tr)
	}
	var hold holdBotOut
	if msg := callTool(t, sess, "hold_bot", map[string]any{"name": "mcpbot", "minutes": 30, "reason": "maintenance"}, &hold); msg != "" || !hold.Hold.Held || hold.Hold.Until == "" {
		t.Fatalf("hold = %v %+v", msg, hold)
	}
	var change botChangeOut
	if msg := callTool(t, sess, "update_bot", map[string]any{"name": "mcpbot", "alerts": map[string]any{"mode": "custom", "channels": []int64{ch.Channel.ID}, "down_after": 60}}, &change); msg != "" || change.RestartNeeded {
		t.Fatalf("update_bot alerts = %v %+v", msg, change)
	}
	if msg := callTool(t, sess, "update_bot", map[string]any{"name": "mcpbot", "alerts": map[string]any{"mode": "custom", "channels": []int64{999}}}, &change); msg == "" {
		t.Error("an unknown channel id was accepted")
	}
	var got getBotOut
	if msg := callTool(t, sess, "get_bot", map[string]any{"name": "mcpbot"}, &got); msg != "" {
		t.Fatal(msg)
	}
	if got.Bot.Hold == nil || got.Bot.Health == nil || got.Config.Alerts == nil || got.Config.Alerts.Mode != "custom" {
		t.Fatalf("get_bot = hold %+v health %+v alerts %+v", got.Bot.Hold, got.Bot.Health, got.Config.Alerts)
	}
	var settings map[string]any
	if msg := callTool(t, sess, "update_settings", map[string]any{"alerts": map[string]any{"remind_every": 1800}}, &settings); msg != "" {
		t.Fatal(msg)
	}
	if loadAlertSettings().RemindEvery != 1800 {
		t.Error("update_settings alerts did not save")
	}
	var overview map[string]any
	if msg := callTool(t, sess, "list_bot_alerts", map[string]any{"bot": "mcpbot"}, &overview); msg != "" || len(overview["bots"].([]any)) != 1 {
		t.Fatalf("list_bot_alerts = %v %v", msg, overview)
	}
	var rel holdBotOut
	if msg := callTool(t, sess, "release_bot", map[string]any{"name": "mcpbot"}, &rel); msg != "" {
		t.Fatal(msg)
	}
	if r, _ := getBot("mcpbot"); r.HoldUntil != 0 {
		t.Error("release_bot left the hold")
	}
}

// A bot stopped by hand pauses its monitor, so Monicron shows it paused, not
// green; starting it again resumes the monitor and pings.
func TestBotAlertsStoppedPausesHeartbeat(t *testing.T) {
	h := newLiveHarness(t, "stopme")
	h.tick(30 * time.Second)
	h.beats()
	if err := setBotStopped("stopme", true); err != nil {
		t.Fatal(err)
	}
	h.gone()
	h.tick(10 * time.Minute)
	if ev := h.events(); len(ev) != 0 {
		t.Fatalf("a stopped bot paged: %v", ev)
	}
	if b := h.beats(); b["pause"] != 1 || len(b) != 1 {
		t.Fatalf("heartbeat while stopped = %v", b)
	}
	if err := setBotStopped("stopme", false); err != nil {
		t.Fatal(err)
	}
	h.up()
	h.tick(time.Minute)
	if b := h.beats(); b["resume"] != 1 || b["ping"] < 2 || b["pause"] != 0 {
		t.Fatalf("heartbeat after start = %v", b)
	}
}

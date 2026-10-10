package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestCronNext(t *testing.T) {
	la := mustLoc(t, "America/Los_Angeles")
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, la)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		cron, after string
		want        []string
	}{
		{"0 6,12,18 * * *", "2026-10-10 11:00", []string{"2026-10-10 12:00", "2026-10-10 18:00", "2026-10-11 06:00"}},
		{"30 9 * * 1-5", "2026-10-09 10:00", []string{"2026-10-12 09:30", "2026-10-13 09:30"}}, // Fri → Mon
		{"*/15 * * * *", "2026-10-10 11:07", []string{"2026-10-10 11:15", "2026-10-10 11:30"}},
		{"0 10 1 * *", "2026-10-10 00:00", []string{"2026-11-01 10:00", "2026-12-01 10:00"}},
		{"47 7 * * *; 15 9 * * *", "2026-10-10 08:00", []string{"2026-10-10 09:15", "2026-10-11 07:47"}},
		// Both day fields restricted: either matches (the 13th, or any Friday).
		{"0 0 13 * fri", "2026-10-10 00:00", []string{"2026-10-13 00:00", "2026-10-16 00:00"}},
		{"0 9 * * 7", "2026-10-10 00:00", []string{"2026-10-11 09:00"}}, // 7 is Sunday
		{"@daily", "2026-10-10 11:00", []string{"2026-10-11 00:00"}},
		// Spring forward (2027-03-14, 2:00→3:00): 2:30 does not exist that day.
		{"30 2 * * *", "2027-03-13 12:00", []string{"2027-03-15 02:30"}},
	} {
		s, err := parseCronSchedule(c.cron)
		if err != nil {
			t.Fatalf("%q: %v", c.cron, err)
		}
		got := s.nextN(at(c.after), la, len(c.want))
		for i, w := range c.want {
			if i >= len(got) || !got[i].Equal(at(w)) {
				t.Errorf("%q after %s: fire %d = %v, want %s", c.cron, c.after, i, got, w)
				break
			}
		}
	}
	// Fall back (2026-11-01, 1:00-2:00 runs twice): 1:30 fires once.
	s, _ := parseCronSchedule("30 1 * * *")
	got := s.nextN(at("2026-10-31 12:00"), la, 2)
	if len(got) != 2 || got[1].Sub(got[0]) < 23*time.Hour {
		t.Errorf("fall-back fires = %v", got)
	}
	for _, bad := range []string{"", "* * * *", "60 * * * *", "0 24 * * *", "0 0 0 * *", "0 0 * 13 *", "0 0 * * 8", "5-1 * * * *", "*/0 * * * *", "a * * * *", "1,,2 * * * *"} {
		if _, err := parseCronSchedule(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func testJobBot(t *testing.T) *botRecord {
	t.Helper()
	useBotTestEnv(t)
	r := &botRecord{Name: "jess", Host: "local"}
	if err := r.normalize(); err != nil {
		t.Fatal(err)
	}
	if err := insertBot(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestBotJobQueue(t *testing.T) {
	rec := testJobBot(t)
	msg, cron, tz := "sweep", "0 6 * * *", "America/Los_Angeles"
	j, err := createBotJob(rec, botJobInput{Name: ptr("sweep"), Message: &msg, Cron: &cron, Timezone: &tz})
	if err != nil {
		t.Fatal(err)
	}
	if j.NextAt == "" || !j.Enabled {
		t.Fatalf("job = %+v", j)
	}
	now := time.Now()
	// Two firings before the channel takes either merge into one event.
	id1, st, _ := enqueueBotEvent(j, "schedule", j.Message, "", now)
	id2, _, _ := enqueueBotEvent(j, "run", j.Message, "", now)
	if st != "pending" || id1 != id2 {
		t.Fatalf("not merged: %d %d %s", id1, id2, st)
	}
	// A webhook is its own event.
	hook, _, _ := enqueueBotEvent(j, "webhook", "payload", "1.2.3.4", now)
	if hook == id1 {
		t.Fatal("webhook merged into the schedule's event")
	}
	ev, err := claimBotEvents(rec.Name, now)
	if err != nil || len(ev) != 2 || ev[0].Count != 2 {
		t.Fatalf("claim = %+v %v", ev, err)
	}
	// In flight: not handed out again until the reclaim window passes.
	if again, _ := claimBotEvents(rec.Name, now.Add(time.Second)); len(again) != 0 {
		t.Fatalf("claimed twice: %+v", again)
	}
	// A firing now starts a new event rather than merging into the claimed one.
	id3, _, _ := enqueueBotEvent(j, "schedule", j.Message, "", now)
	if id3 == id1 {
		t.Fatal("merged into an in-flight event")
	}
	if err := ackBotEvents(rec.Name, []int64{ev[0].ID}, now); err != nil {
		t.Fatal(err)
	}
	// The unacked webhook comes back after the reclaim window, with id3.
	again, _ := claimBotEvents(rec.Name, now.Add(botEventReclaim+time.Second))
	if len(again) != 2 || again[0].ID != hook || again[1].ID != id3 {
		t.Fatalf("reclaim = %+v", again)
	}
	// A stopped bot's firing is dropped and logged.
	_ = setBotStopped(rec.Name, true)
	_, st, _ = enqueueBotEvent(j, "schedule", j.Message, "", now)
	if st != "dropped" {
		t.Fatalf("stopped bot: %s", st)
	}
	hist, _ := listBotJobEvents(j.ID, 10)
	if hist[0].Status != "dropped" || hist[0].Reason == "" {
		t.Fatalf("history = %+v", hist[0])
	}
}

func ptr[T any](v T) *T { return &v }

func TestBotJobsTick(t *testing.T) {
	rec := testJobBot(t)
	msg, cron := "hourly", "0 * * * *"
	j, err := createBotJob(rec, botJobInput{Name: ptr("hourly"), Message: &msg, Cron: &cron})
	if err != nil {
		t.Fatal(err)
	}
	due, _ := parseStamp(j.NextAt)
	botJobsTick(due.Add(-time.Second))
	if ev, _ := listBotJobEvents(j.ID, 10); len(ev) != 0 {
		t.Fatalf("fired early: %+v", ev)
	}
	// Three hours late (lasso was down): it fires once, then carries on.
	late := due.Add(3 * time.Hour)
	botJobsTick(late)
	botJobsTick(late.Add(time.Second))
	ev, _ := listBotJobEvents(j.ID, 10)
	if len(ev) != 1 || ev[0].Kind != "schedule" || ev[0].Count != 1 {
		t.Fatalf("events = %+v", ev)
	}
	j, _ = getBotJob(rec.Name, "hourly")
	if next, _ := parseStamp(j.NextAt); !next.After(late) {
		t.Fatalf("next_at %s not after %s", j.NextAt, late)
	}
	// Paused: no schedule, no next.
	if err := saveBotJob(j, botJobInput{Enabled: ptr(false)}); err != nil || j.NextAt != "" {
		t.Fatalf("paused: %v %q", err, j.NextAt)
	}
}

func TestBotJobValidation(t *testing.T) {
	rec := testJobBot(t)
	for name, in := range map[string]botJobInput{
		"no message, no webhook": {Name: ptr("a")},
		"bad name":               {Name: ptr("A b"), Message: ptr("x")},
		"bad cron":               {Name: ptr("a"), Message: ptr("x"), Cron: ptr("99 * * * *")},
		"bad zone":               {Name: ptr("a"), Message: ptr("x"), Timezone: ptr("Mars/Olympus")},
		"reserved":               {Name: ptr("preview"), Message: ptr("x")},
	} {
		if _, err := createBotJob(rec, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	remote := &botRecord{Name: "far", Host: "citadel"}
	if _, err := createBotJob(remote, botJobInput{Name: ptr("a"), Message: ptr("x")}); err == nil {
		t.Error("created a job for a bot on another host")
	}
	j, err := createBotJob(rec, botJobInput{Name: ptr("hook"), Webhook: ptr(true)})
	if err != nil || len(j.WebhookKey) < 20 {
		t.Fatalf("webhook job = %+v %v", j, err)
	}
	if _, err := createBotJob(rec, botJobInput{Name: ptr("hook"), Webhook: ptr(true)}); err == nil {
		t.Error("duplicate name accepted")
	}
}

func TestBotHook(t *testing.T) {
	rec := testJobBot(t)
	j, err := createBotJob(rec, botJobInput{Name: ptr("deploy"), Message: ptr("A deploy finished."), Webhook: ptr(true)})
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, body string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		serveBotHook(w, req)
		return w
	}
	base := "/hooks/bots/jess/deploy"
	for _, p := range []string{base + "?key=wrong", base, "/hooks/bots/jess/nope?key=" + j.WebhookKey, "/hooks/bots/other/deploy?key=" + j.WebhookKey} {
		if w := post(p, "x"); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	w := post(base+"?key="+j.WebhookKey, "v1.2.3 is live")
	if w.Code != http.StatusAccepted {
		t.Fatalf("hook = %d %s", w.Code, w.Body)
	}
	if w := post(base, "again", "Authorization", "Bearer "+j.WebhookKey); w.Code != http.StatusAccepted {
		t.Fatalf("bearer hook = %d", w.Code)
	}
	ev, _ := claimBotEvents("jess", time.Now())
	if len(ev) != 2 || ev[0].Kind != "webhook" || !strings.HasPrefix(ev[0].Content, "A deploy finished.\n\nWebhook payload") || !strings.HasSuffix(ev[0].Content, "v1.2.3 is live") {
		t.Fatalf("events = %+v", ev)
	}
	if w := post(base+"?key="+j.WebhookKey, strings.Repeat("x", botHookBodyMax+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize = %d", w.Code)
	}
	if w := post(base+"?key="+j.WebhookKey, "\xff\xfe"); w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("binary = %d", w.Code)
	}
	// Rotating the key retires the old one.
	old := j.WebhookKey
	if err := rotateBotJobKey(j); err != nil || j.WebhookKey == old {
		t.Fatal("rotate", err)
	}
	if w := post(base+"?key="+old, "x"); w.Code != http.StatusNotFound {
		t.Errorf("old key still works: %d", w.Code)
	}
	// Paused: accepted, logged as dropped.
	_ = saveBotJob(j, botJobInput{Enabled: ptr(false)})
	w = post(base+"?key="+j.WebhookKey, "x")
	var out struct{ Status string }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Status != "dropped" {
		t.Errorf("paused hook = %s", w.Body)
	}
}

func TestBotChannelEndpoint(t *testing.T) {
	rec := testJobBot(t)
	tok, err := ensureBotChannelToken(rec.Name)
	if err != nil || tok == "" {
		t.Fatal(err)
	}
	if again, _ := ensureBotChannelToken(rec.Name); again != tok {
		t.Fatal("token changed")
	}
	j, _ := createBotJob(rec, botJobInput{Name: ptr("a"), Message: ptr("do it")})
	_, _ = runBotJob(j)
	srv := httptest.NewServer(http.HandlerFunc(serveBotChannel))
	defer srv.Close()
	get := func(token string) (*http.Response, error) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/bot-channel/jess/next?wait=0", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return http.DefaultClient.Do(req)
	}
	if resp, _ := get("nope"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token = %d", resp.StatusCode)
	}

	// The channel process against it: initialize, initialized, then one event
	// arrives as a channel notification and is acked.
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ch := &botChannelServer{bot: "jess", base: srv.URL, token: tok, out: outW, client: srv.Client(), waitSeconds: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.serve(ctx, inR)
	lines := bufio.NewScanner(outR)
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`+"\n")
	if !lines.Scan() {
		t.Fatal("no initialize answer")
	}
	var init struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	_ = json.Unmarshal(lines.Bytes(), &init)
	if _, ok := init.Result.Capabilities.Experimental["claude/channel"]; !ok || init.Result.Instructions == "" {
		t.Fatalf("initialize = %s", lines.Bytes())
	}
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
	if !lines.Scan() {
		t.Fatal("no notification")
	}
	var note struct {
		Method string `json:"method"`
		Params struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(lines.Bytes(), &note)
	if note.Method != "notifications/claude/channel" || note.Params.Content != "do it" || note.Params.Meta["job"] != "a" || note.Params.Meta["trigger"] != "run" {
		t.Fatalf("notification = %s", lines.Bytes())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ev, _ := listBotJobEvents(j.ID, 1)
		if len(ev) == 1 && ev[0].Status == "delivered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never acked: %+v", ev)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := botChannelState(rec, time.Now()); st["connected"] != true {
		t.Errorf("channel state = %v", st)
	}
}

func TestBotChannelInMCPJSON(t *testing.T) {
	r := &botRecord{Name: "jess", Host: "local", channelToken: "tok"}
	var got struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	_ = json.Unmarshal(botMCPJSON(r, "/d", nil), &got)
	c := got.MCPServers[botChannelName]
	env, _ := c["env"].(map[string]any)
	if c["type"] != "stdio" || env["LASSO_CHANNEL_TOKEN"] != "tok" || !strings.HasPrefix(env["LASSO_URL"].(string), "http://") {
		t.Fatalf("lasso-channel = %v", c)
	}
	argv := strings.Join(botClaudeArgv(r, "/d"), " ")
	if !strings.Contains(argv, "--dangerously-load-development-channels server:"+botChannelName) {
		t.Errorf("no grant: %s", argv)
	}
	if !strings.Contains(botTaskScript(r, "/d", nil), "I am using this for local development") {
		t.Error("the task does not answer the development-channel menu")
	}
	// Not for a bot on another host, nor over a server of the bot's own name.
	for _, other := range []*botRecord{
		{Name: "jess", Host: "citadel", channelToken: "tok"},
		{Name: "jess", Host: "local", channelToken: "tok", MCP: []botMCPServer{{Name: botChannelName, Type: "stdio", Command: "mine"}}},
	} {
		got.MCPServers = nil
		_ = json.Unmarshal(botMCPJSON(other, "/d", nil), &got)
		if c := got.MCPServers[botChannelName]; c != nil && c["command"] != "mine" {
			t.Errorf("host %s: lasso-channel = %v", other.Host, c)
		}
		if strings.Count(strings.Join(botClaudeArgv(other, "/d"), " "), "server:"+botChannelName) > 1 {
			t.Error("granted twice")
		}
	}
}

package main

// Review access (clientreview.go, docs/design/client-api.md "Review access"):
// the scripted responder, the demo person's hard rules, and the guarantees the
// long-lived review link must keep.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// --- helpers ------------------------------------------------------------------------

// reviewCall drives /api/client-review the way the owner's Settings call does.
func reviewCall(t *testing.T, method, body string) (int, map[string]any, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/client-review", strings.NewReader(body))
	w := httptest.NewRecorder()
	serveClientReview(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.Bytes()
}

// enableReview turns review access on and answers its pairing code.
func (c *clientTest) enableReview(t *testing.T, days int) string {
	t.Helper()
	code, out, raw := reviewCall(t, "PUT", `{"enabled":true,"days":`+itoa(days)+`,"base_url":"https://lasso.example.com"}`)
	if code != 200 {
		t.Fatalf("enable review access: %d %s", code, raw)
	}
	s, _ := out["code"].(string)
	if !strings.HasPrefix(s, clientPairPrefix) {
		t.Fatalf("no code in the enable answer: %s", raw)
	}
	if link, _ := out["link"].(string); !strings.HasPrefix(link, "https://lasso.example.com/client/pair#code=lpc_") {
		t.Fatalf("link = %q", link)
	}
	return s
}

// pairReview redeems a review code and answers the device token.
func (c *clientTest) pairReview(t *testing.T, code, label string) string {
	t.Helper()
	resp, body := c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"`+label+`","platform":"ios"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("review pair: %d %s", resp.StatusCode, body)
	}
	var out struct{ Token string }
	_ = json.Unmarshal(body, &out)
	return out.Token
}

// awaitBotMessages waits for n bot messages in a thread and answers the
// history.
func (c *clientTest) awaitBotMessages(token, tid string, n int, within time.Duration) []clientMessageView {
	c.t.Helper()
	deadline := time.Now().Add(within)
	for {
		msgs := c.history(token, tid)
		got := 0
		for _, m := range msgs {
			if m.Role == "bot" {
				got++
			}
		}
		if got >= n {
			return msgs
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("no %d bot messages in %s within %s: %+v", n, tid, within, msgs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fastDemoReplies makes the demo bot answer almost at once for the test.
func fastDemoReplies(t *testing.T) {
	t.Helper()
	prev := clientDemoReplyDelay
	clientDemoReplyDelay = 5 * time.Millisecond
	t.Cleanup(func() { clientDemoReplyDelay = prev })
}

// --- the scripted responder ----------------------------------------------------------

// demoSentences counts sentence ends, ignoring anything inside quotes (a file
// name such as "a.pdf" is not the end of a sentence).
func demoSentences(s string) int {
	inQuote, n := false, 0
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == '.' || r == '!' || r == '?'):
			n++
		}
	}
	return n
}

func TestDemoBotResponder(t *testing.T) {
	cases := []struct {
		in   demoReplyIn
		want []string
	}{
		{demoReplyIn{Text: "hello"}, []string{"Hi", "Lasso"}},
		{demoReplyIn{Text: "Hi there!"}, []string{"Hi", "demo bot"}},
		{demoReplyIn{Text: "help"}, []string{"thread", "files"}},
		{demoReplyIn{Text: "what can you do?"}, []string{"threads", "calls"}},
		{demoReplyIn{Text: "how does this work"}, []string{"thread"}},
		{demoReplyIn{Text: "tell me about threads"}, []string{"Threads", "one-to-one"}},
		{demoReplyIn{Text: "can I call you?"}, []string{"call", "hands-free"}},
		{demoReplyIn{Text: "does voice work"}, []string{"speech"}},
		{demoReplyIn{Text: "can I send a photo?"}, []string{"file", "attach"}},
		{demoReplyIn{Text: "who are you?"}, []string{"demo bot", "Lasso"}},
		{demoReplyIn{Text: "what is Lasso"}, []string{"phone"}},
		{demoReplyIn{Text: "thanks"}, []string{"welcome"}},
		{demoReplyIn{Text: "thank you!"}, []string{"welcome"}},
		// An attachment is acknowledged by name.
		{demoReplyIn{Attachments: []string{"garden.png"}}, []string{"garden.png", "files"}},
		{demoReplyIn{Attachments: []string{"a.pdf", "b.jpg"}}, []string{"a.pdf", "b.jpg"}},
		{demoReplyIn{Attachments: []string{"a", "b", "c", "d"}}, []string{"and 2 more"}},
		// The fallback quotes the message, bounded, and says it is scripted.
		{demoReplyIn{Text: strings.Repeat("x", 200)}, []string{"scripted", strings.Repeat("x", clientReviewQuoteMax) + "…"}},
		{demoReplyIn{Text: "line one\nline two", Mode: "voice"}, []string{"I heard", `"line one line two"`}},
	}
	for _, tc := range cases {
		got := demoBotReply(tc.in)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("demoBotReply(%+v) = %q, missing %q", tc.in, got, w)
			}
		}
		if n := demoSentences(got); n > 3 {
			t.Errorf("demoBotReply(%+v) is %d sentences: %q", tc.in, n, got)
		}
		if strings.ContainsAny(got, "\r\n") || !utf8.ValidString(got) {
			t.Errorf("demoBotReply(%+v) is not one plain line: %q", tc.in, got)
		}
	}
	// A quote is one line, has no control characters, and is bounded.
	if got := demoQuote("a\nb\tc\x00d"); got != "a b cd" {
		t.Fatalf("demoQuote = %q", got)
	}
	if got := demoQuote(strings.Repeat("é", 100)); len([]rune(got)) != clientReviewQuoteMax+1 {
		t.Fatalf("demoQuote is %d runes: %q", len([]rune(got)), got)
	}
}

// --- the demo person and the demo bot ------------------------------------------------

func TestClientReviewOnlyTheDemoBot(t *testing.T) {
	c := newClientTest(t)
	code := c.enableReview(t, 30)
	tok := c.pairReview(t, code, "Review phone")

	var me struct {
		User    struct{ ID, Name string }
		Bots    []clientBotView
		Storage map[string]any
	}
	if resp := c.get("/client/v1/me", tok, &me); resp.StatusCode != 200 {
		t.Fatalf("/me: %d", resp.StatusCode)
	}
	if me.User.Name != clientReviewUser {
		t.Fatalf("the review person is %q", me.User.Name)
	}
	if len(me.Bots) != 1 || me.Bots[0].Name != clientDemoBot {
		t.Fatalf("bots = %+v", me.Bots)
	}
	b := me.Bots[0]
	if b.Description != clientDemoBotDescription || b.State != "available" || b.IsOwner || b.ConversationID == "" || !b.Capabilities.Chat || !b.Capabilities.Voice {
		t.Fatalf("demo bot view = %+v", b)
	}
	if me.Storage["quota_bytes"].(float64) != float64(clientReviewQuotaMB<<20) {
		t.Fatalf("storage = %+v", me.Storage)
	}

	// The state view never holds the code.
	status, view, raw := reviewCall(t, "GET", "")
	if status != 200 || strings.Contains(string(raw), "lpc_") || strings.Contains(string(raw), `"code"`) {
		t.Fatalf("GET /api/client-review = %d %s", status, raw)
	}
	rv, _ := view["review"].(map[string]any)
	if rv["enabled"] != true || rv["devices"].(float64) != 1 || rv["max_devices"].(float64) != clientReviewDevicesMax || rv["expires_at"] == nil {
		t.Fatalf("review view = %+v", rv)
	}

	// A real bot is not there, by name or id.
	if resp, _ := c.do("GET", "/client/v1/bots/jess", tok, ""); resp.StatusCode != 404 {
		t.Fatalf("a real bot's view: %d", resp.StatusCode)
	}
	if resp, _ := c.do("GET", "/client/v1/bots/Demo%20Bot", tok, ""); resp.StatusCode != 200 {
		t.Fatalf("the demo bot's view: %d", resp.StatusCode)
	}
}

func TestClientReviewGrants(t *testing.T) {
	c := newClientTest(t)
	c.addBot("mark")
	code := c.enableReview(t, 30)
	c.pairReview(t, code, "Review phone")
	reviewID := findUserID(t, clientReviewUser)
	annID, ann := c.person("Ann", false, "jess")

	// A real bot can never be granted to the review person, and the demo bot
	// never to a real one.
	if _, err := setAppGrant(reviewID, "jess", false, time.Now()); err == nil {
		t.Fatal("a real bot was granted to the review person")
	}
	if _, err := setAppGrant(reviewID, "mark", false, time.Now()); err == nil {
		t.Fatal("a real bot was granted to the review person")
	}
	if _, err := setAppGrant(annID, clientDemoBot, false, time.Now()); err == nil {
		t.Fatal("the demo bot was granted to a real person")
	}
	if _, err := setAppGrant(reviewID, clientDemoBot, true, time.Now()); err == nil {
		t.Fatal("the demo bot was made an owner's")
	}
	// The owner's /api refuses them too.
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		serveAppUsers(w, req)
		return w
	}
	if w := do("PUT", "/api/app-users/"+reviewID+"/grants/jess", `{}`); w.Code != 400 {
		t.Fatalf("grant route let a real bot in: %d %s", w.Code, w.Body)
	}
	if w := do("PUT", "/api/app-users/"+annID+"/grants/"+urlPathEscape(clientDemoBot), `{}`); w.Code != 400 {
		t.Fatalf("grant route let the demo bot out: %d %s", w.Code, w.Body)
	}
	if w := do("DELETE", "/api/app-users/"+reviewID+"/grants/"+urlPathEscape(clientDemoBot), ""); w.Code != 400 {
		t.Fatalf("the review grant is removable: %d %s", w.Code, w.Body)
	}
	// Its person is not disabled, quota-edited, deleted or paired another way.
	if _, err := updateAppUser(reviewID, appUserPatch{Disabled: ptr(true)}, time.Now()); err == nil {
		t.Fatal("the review person was disabled")
	}
	quota := 1000
	if _, err := updateAppUser(reviewID, appUserPatch{QuotaMB: optInt{Set: true, Val: &quota}}, time.Now()); err == nil {
		t.Fatal("the review person's quota was raised")
	}
	if _, err := deleteAppUser(reviewID); err == nil {
		t.Fatal("the review person was deleted")
	}
	if _, _, err := mintAppPairing(reviewID, "local", time.Now()); err == nil {
		t.Fatal("a one-time pairing was minted for the review person")
	}
	// A stray real-bot grant row is filtered at read time anyway.
	if _, err := db.Exec(`INSERT INTO app_grants (user_id, bot, owner, created_at) VALUES (?, 'jess', 1, ?)`, reviewID, nowStamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	gs, _ := listAppGrants(reviewID)
	if len(gs) != 1 || gs[0].Bot != clientDemoBot || gs[0].Owner {
		t.Fatalf("read-time filter let a stray grant through: %+v", gs)
	}
	if _, err := getAppGrant(reviewID, "jess"); err == nil {
		t.Fatal("getAppGrant read a stray real-bot grant")
	}
	_, _ = db.Exec(`DELETE FROM app_grants WHERE user_id = ? AND bot = 'jess'`, reviewID)
	// The other direction too: a stray demo-bot grant on a real person reads
	// as no grant at all.
	if _, err := db.Exec(`INSERT INTO app_grants (user_id, bot, owner, created_at) VALUES (?, ?, 1, ?)`, annID, clientDemoBot, nowStamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := getAppGrant(annID, clientDemoBot); err == nil {
		t.Fatal("getAppGrant read a stray demo-bot grant")
	}
	gs, _ = listAppGrants(annID)
	for _, g := range gs {
		if g.Bot == clientDemoBot {
			t.Fatalf("listAppGrants read a stray demo-bot grant: %+v", gs)
		}
	}
	if resp, _ := c.do("GET", "/client/v1/bots/"+urlPathEscape(clientDemoBot), ann, ""); resp.StatusCode != 404 {
		t.Fatalf("a real person reached the demo bot: %d", resp.StatusCode)
	}
	_, _ = db.Exec(`DELETE FROM app_grants WHERE user_id = ? AND bot = ?`, annID, clientDemoBot)
}

func urlPathEscape(s string) string { return strings.ReplaceAll(s, " ", "%20") }

// --- the demo bot is not a real bot --------------------------------------------------

func TestClientReviewDemoBotIsNotARealBot(t *testing.T) {
	c := newClientTest(t)
	_ = c.enableReview(t, 30)

	// No row, and no reader will make one.
	if _, err := getBot(clientDemoBot); err == nil {
		t.Fatal("getBot found the demo bot")
	}
	// Its name is not a legal bot name, so create_bot cannot make one either.
	rec := &botRecord{Name: clientDemoBot, Host: "local", Dir: t.TempDir()}
	if err := rec.normalize(); err == nil {
		t.Fatal("the demo bot's name is a legal bot name")
	}

	bot := mcpAnonSession(t)
	// The owner's own listing has real bots in it, and never the demo bot.
	if msg := callTool(t, bot, "list_bots", map[string]any{}, &struct{}{}); msg != "" {
		t.Fatalf("list_bots: %s", msg)
	}
	var listed struct {
		Bots []struct{ Name string } `json:"bots"`
	}
	callTool(t, bot, "list_bots", map[string]any{}, &listed)
	if len(listed.Bots) == 0 {
		t.Fatal("list_bots shows nothing at all: the check below proves nothing")
	}
	for _, b := range listed.Bots {
		if b.Name == clientDemoBot {
			t.Fatal("list_bots lists the demo bot")
		}
	}
	for tool, args := range map[string]map[string]any{
		"get_bot":     {"name": clientDemoBot},
		"start_bot":   {"name": clientDemoBot},
		"stop_bot":    {"name": clientDemoBot},
		"restart_bot": {"name": clientDemoBot},
		"send_bot":    {"name": clientDemoBot, "text": "hello"},
		"update_bot":  {"name": clientDemoBot, "model": "x"},
		"delete_bot":  {"name": clientDemoBot},
	} {
		if msg := callTool(t, bot, tool, args, &struct{}{}); msg == "" {
			t.Errorf("%s operated on the demo bot", tool)
		}
	}
	// Even a row written into the table by hand is not a bot any path can
	// reach: getBot refuses the name before it reads.
	if _, err := db.Exec(`INSERT INTO bots (host, name, dir, created_at, updated_at) VALUES ('local', ?, ?, ?, ?)`,
		clientDemoBot, t.TempDir(), nowStamp(time.Now()), nowStamp(time.Now())); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM bots WHERE name = ?`, clientDemoBot)
	if _, err := getBot(clientDemoBot); err == nil {
		t.Fatal("a hand-written row made the demo bot a real bot")
	}
	if msg := callTool(t, bot, "start_bot", map[string]any{"name": clientDemoBot}, &struct{}{}); msg == "" {
		t.Fatal("start_bot started a hand-written demo bot row")
	}
	// Nor is such a row listed.
	callTool(t, bot, "list_bots", map[string]any{}, &listed)
	for _, b := range listed.Bots {
		if b.Name == clientDemoBot {
			t.Fatal("list_bots lists a hand-written demo bot row")
		}
	}
	if bs, err := listBots(); err != nil {
		t.Fatal(err)
	} else {
		for _, b := range bs {
			if b.Name == clientDemoBot {
				t.Fatal("listBots returns a hand-written demo bot row")
			}
		}
	}
}

// --- the review code's limits --------------------------------------------------------

func TestClientReviewPairing(t *testing.T) {
	c := newClientTest(t)
	fastDemoReplies(t)
	code := c.enableReview(t, 30)

	// Multi-use: every use pairs one more device.
	var tokens []string
	for i := 0; i < clientReviewDevicesMax; i++ {
		tokens = append(tokens, c.pairReview(t, code, "phone "+itoa(i)))
	}
	if got := reviewDeviceCount(findUserID(t, clientReviewUser)); got != clientReviewDevicesMax {
		t.Fatalf("paired %d devices", got)
	}
	// The 26th is refused, and the oldest is not evicted.
	resp, body := c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"one too many","platform":"ios"}`)
	if resp.StatusCode != 403 || errCode(body) != "device_limit" {
		t.Fatalf("the 26th device: %d %s", resp.StatusCode, body)
	}
	if resp := c.get("/client/v1/me", tokens[0], nil); resp.StatusCode != 200 {
		t.Fatalf("the oldest device was signed out: %d", resp.StatusCode)
	}
	// Revoking one frees a place.
	devs, _ := listAppDevices(findUserID(t, clientReviewUser))
	if err := revokeAppDevice(devs[0].UserID, devs[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	c.pairReview(t, code, "a replacement")

	// An expired code pairs nothing.
	_, _ = db.Exec(`UPDATE app_review SET expires_at = ? WHERE id = 'review'`, nowStamp(time.Now().Add(-time.Minute)))
	resp, body = c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"late","platform":"ios"}`)
	if resp.StatusCode != 401 || errCode(body) != "pairing_failed" {
		t.Fatalf("an expired review code: %d %s", resp.StatusCode, body)
	}
	// Re-minting makes a fresh link and retires the old one.
	newCode := c.enableReview(t, 30)
	if newCode == code {
		t.Fatal("re-minting reused the old code")
	}
	resp, body = c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"old","platform":"ios"}`)
	if resp.StatusCode != 401 {
		t.Fatalf("the retired code still pairs: %d %s", resp.StatusCode, body)
	}
	devs, _ = listAppDevices(findUserID(t, clientReviewUser))
	revoked := ""
	for _, d := range devs {
		if d.RevokedAt == "" {
			revoked = d.ID
			break
		}
	}
	if err := revokeAppDevice(findUserID(t, clientReviewUser), revoked, time.Now()); err != nil {
		t.Fatal(err)
	}
	live := c.pairReview(t, newCode, "new phone")
	if resp := c.get("/client/v1/me", live, nil); resp.StatusCode != 200 {
		t.Fatalf("the re-minted link does not pair: %d", resp.StatusCode)
	}
	reviewID := findUserID(t, clientReviewUser)

	// Turning it off refuses pairing, signs every device out and removes the
	// data.
	convID := c.conv(live, clientDemoBot)
	c.send(live, convID, `{"text":"hello"}`)
	_, up := c.upload(live, convID, "photo.png", pngBytes())
	a, _ := appAttachmentByID(up.ID)
	b, _ := botBackend("local")
	filePath := filepath.Join(expandTildeOn(b, clientDemoRoot()), a.Path)
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("the upload was not stored: %v", err)
	}
	if status, _, raw := reviewCall(t, "PUT", `{"enabled":false}`); status != 200 || strings.Contains(string(raw), `"enabled":true`) {
		t.Fatalf("disable: %d %s", status, raw)
	}
	if resp, body := c.do("POST", "/client/v1/pair", "", `{"code":"`+newCode+`","label":"x","platform":"ios"}`); resp.StatusCode != 401 {
		t.Fatalf("pairing after disable: %d %s", resp.StatusCode, body)
	}
	for _, tok := range append(tokens, live) {
		if resp := c.get("/client/v1/me", tok, nil); resp.StatusCode != 401 {
			t.Fatalf("a review device still works after disable: %d", resp.StatusCode)
		}
	}
	var users, threads, msgs, atts, devices int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_users WHERE review = 1`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_threads WHERE user_id = ?`, reviewID).Scan(&threads)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ?`, convID).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_attachments WHERE id = ?`, up.ID).Scan(&atts)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_devices`).Scan(&devices)
	rv := loadClientReview()
	if users != 0 || threads != 0 || msgs != 0 || atts != 0 || devices != 0 || rv.Enabled || rv.CodeHash != "" || rv.UserID != "" {
		t.Fatalf("disable left users=%d threads=%d msgs=%d atts=%d devices=%d review=%+v", users, threads, msgs, atts, devices, rv)
	}
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("the upload survived disable: %v", err)
	}
	if status, _, raw := reviewCall(t, "GET", ""); status != 200 || strings.Contains(string(raw), `"enabled":true`) {
		t.Fatalf("GET after disable: %d %s", status, raw)
	}
}

// Normal pairing is untouched: one use, ten minutes.
func TestClientReviewNormalPairingUnchanged(t *testing.T) {
	c := newClientTest(t)
	c.enableReview(t, 30)
	u, _ := createAppUser("Ann", "", time.Now())
	if _, err := setAppGrant(u.ID, "jess", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, _, err := mintAppPairing(u.ID, "local", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	c.pairReview(t, code, "Ann's phone")
	resp, body := c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"again","platform":"ios"}`)
	if resp.StatusCode != 401 || errCode(body) != "pairing_failed" {
		t.Fatalf("a one-time code was used twice: %d %s", resp.StatusCode, body)
	}
	if n := reviewDeviceCount(findUserID(t, clientReviewUser)); n != 0 {
		t.Fatalf("a normal pairing touched the review person: %d devices", n)
	}
}

// --- the person's messages and replies -----------------------------------------------

func TestClientReviewReplies(t *testing.T) {
	c := newClientTest(t)
	fastDemoReplies(t)
	code := c.enableReview(t, 30)
	tok := c.pairReview(t, code, "Review phone")
	cid := c.conv(tok, clientDemoBot)

	// The stream sees the delivery and the scripted answer.
	ch, stop, _ := c.openStream(tok, cid, "")
	defer stop()
	nextEvent(t, ch, "hello")
	resp, sent := c.send(tok, cid, `{"text":"hello","client_id":"first"}`)
	if resp.StatusCode != 202 || sent.State != "delivered" || len(sent.Deliveries) != 1 || sent.Deliveries[0].State != "delivered" {
		t.Fatalf("send = %d %+v", resp.StatusCode, sent)
	}
	state := nextEvent(t, ch, "message_state")
	if !strings.Contains(state.Data, `"state":"delivered"`) {
		t.Fatalf("message_state = %s", state.Data)
	}
	reply := nextEvent(t, ch, "message")
	var rv clientMessageView
	_ = json.Unmarshal([]byte(reply.Data), &rv)
	if rv.Role != "bot" || rv.Bot != clientDemoBot || rv.InReplyTo != sent.ID || !rv.Done || !strings.Contains(rv.Text, "Hi") {
		t.Fatalf("reply = %+v", rv)
	}
	// It is a stored message, like any bot's.
	msgs := c.history(tok, cid)
	if len(msgs) != 2 || msgs[1].ID != rv.ID {
		t.Fatalf("history = %+v", msgs)
	}

	// A file is acknowledged by name.
	_, up := c.upload(tok, cid, "garden.png", pngBytes())
	if resp, _ := c.send(tok, cid, `{"text":"here is the garden","attachments":["`+up.ID+`"]}`); resp.StatusCode != 202 {
		t.Fatalf("send with a file: %d", resp.StatusCode)
	}
	msgs = c.awaitBotMessages(tok, cid, 2, 5*time.Second)
	if last := msgs[len(msgs)-1]; !strings.Contains(last.Text, "garden.png") {
		t.Fatalf("the file was not acknowledged: %+v", last)
	}

	// A thread the person starts works the same way.
	_, threadBody := c.do("POST", "/client/v1/threads", tok, `{"title":"Garden plans","bots":["`+clientDemoBot+`"],"message":{"text":"what can you do?"}}`)
	var created struct {
		Thread  clientThreadView
		Message clientMessageView
	}
	_ = json.Unmarshal(threadBody, &created)
	if created.Thread.ID == "" || created.Message.ID == "" {
		t.Fatalf("thread create = %s", threadBody)
	}
	msgs = c.awaitBotMessages(tok, created.Thread.ID, 1, 5*time.Second)
	if last := msgs[len(msgs)-1]; last.Role != "bot" || last.InReplyTo != created.Message.ID || !strings.Contains(last.Text, "threads") {
		t.Fatalf("thread reply = %+v", last)
	}

	// A voice call works end to end: the spoken turn is answered on the call.
	resp, callBody := c.do("POST", "/client/v1/conversations/"+cid+"/calls", tok, "")
	var call struct{ Call appCall }
	_ = json.Unmarshal(callBody, &call)
	if resp.StatusCode != 201 || call.Call.State != "open" {
		t.Fatalf("call start: %d %s", resp.StatusCode, callBody)
	}
	resp, vm := c.send(tok, cid, `{"text":"hello","mode":"voice","call_id":"`+call.Call.ID+`"}`)
	if resp.StatusCode != 202 || vm.CallID != call.Call.ID {
		t.Fatalf("voice send: %d %+v", resp.StatusCode, vm)
	}
	msgs = c.awaitBotMessages(tok, cid, 3, 5*time.Second)
	last := msgs[len(msgs)-1]
	if last.Role != "bot" || last.CallID != call.Call.ID || last.Mode != "voice" || last.InReplyTo != vm.ID {
		t.Fatalf("voice reply = %+v", last)
	}
	// Hanging up ends the call and queues nothing for a bot that has no queue.
	if resp, _ := c.do("POST", "/client/v1/calls/"+call.Call.ID+"/end", tok, ""); resp.StatusCode != 200 {
		t.Fatalf("hang up: %d", resp.StatusCode)
	}
	var queued int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bot_events WHERE bot = ?`, clientDemoBot).Scan(&queued)
	if queued != 0 {
		t.Fatalf("%d bot events were queued for the demo bot", queued)
	}
}

// --- the review device reaches nothing else -------------------------------------------

func TestClientReviewCannotReachOtherPeople(t *testing.T) {
	c := newClientTest(t)
	fastDemoReplies(t)
	code := c.enableReview(t, 30)
	rev := c.pairReview(t, code, "Review phone")

	_, ann := c.person("Ann", false, "jess")
	annConv := c.conv(ann, "jess")
	c.send(ann, annConv, `{"text":"my secret launch plan"}`)
	_, annUp := c.upload(ann, annConv, "private.png", append(pngBytes(), []byte("ann's")...))
	c.send(ann, annConv, `{"text":"see attached","attachments":["`+annUp.ID+`"]}`)
	annThread := c.newThread(ann, `{"title":"Ann's thread","bots":["jess"]}`)
	c.sendIn(ann, annThread, `{"text":"thread secret"}`)
	resp, callBody := c.do("POST", "/client/v1/conversations/"+annConv+"/calls", ann, "")
	var call struct{ Call appCall }
	_ = json.Unmarshal(callBody, &call)
	if resp.StatusCode != 201 {
		t.Fatalf("ann's call: %d", resp.StatusCode)
	}

	// Every id of Ann's is one 404 to the review device, and byte for byte the
	// same as an id that does not exist.
	paths := []struct{ method, path, body string }{
		{"GET", "/client/v1/conversations/" + annConv + "/messages", ""},
		{"POST", "/client/v1/conversations/" + annConv + "/messages", `{"text":"hi"}`},
		{"GET", "/client/v1/conversations/" + annConv + "/events", ""},
		{"POST", "/client/v1/conversations/" + annConv + "/attachments?name=x.png", ""},
		{"POST", "/client/v1/conversations/" + annConv + "/calls", ""},
		{"GET", "/client/v1/threads/" + annConv, ""},
		{"PATCH", "/client/v1/threads/" + annConv, `{"title":"mine now"}`},
		{"DELETE", "/client/v1/threads/" + annConv, ""},
		{"GET", "/client/v1/threads/" + annThread + "/messages", ""},
		{"POST", "/client/v1/threads/" + annThread + "/messages", `{"text":"hi"}`},
		{"GET", "/client/v1/threads/" + annThread + "/events", ""},
		{"POST", "/client/v1/threads/" + annThread + "/bots", `{"bot":"jess"}`},
		{"GET", "/client/v1/attachments/" + annUp.ID, ""},
		{"DELETE", "/client/v1/attachments/" + annUp.ID, ""},
		{"GET", "/client/v1/calls/" + call.Call.ID, ""},
		{"POST", "/client/v1/calls/" + call.Call.ID + "/end", ""},
		{"GET", "/client/v1/bots/jess", ""},
		{"GET", "/client/v1/bots/jess/avatar", ""},
	}
	var missing []byte
	for _, p := range paths {
		resp, body := c.do(p.method, p.path, rev, p.body)
		if resp.StatusCode != 404 {
			t.Errorf("%s %s: %d %s", p.method, p.path, resp.StatusCode, body)
		}
		if missing == nil {
			missing = body
		} else if string(body) != string(missing) {
			t.Errorf("%s %s answers a different 404: %s", p.method, p.path, body)
		}
	}
	// Search finds none of Ann's words.
	resp, body := c.do("GET", "/client/v1/search?q=secret", rev, "")
	if resp.StatusCode != 200 || strings.Contains(string(body), "secret") {
		t.Fatalf("review search saw another person's words: %d %s", resp.StatusCode, body)
	}
	// Proposals are not the review person's either.
	var props struct{ Proposals []any }
	c.get("/client/v1/proposals?state=all", rev, &props)
	if len(props.Proposals) != 0 {
		t.Fatalf("review proposals = %+v", props.Proposals)
	}

	// The person stream carries only the review person's own threads.
	ch, stop := c.openPersonStream(rev, "", "")
	defer stop()
	hello := nextEvent(t, ch, "hello")
	if strings.Contains(hello.Data, annThread) || strings.Contains(hello.Data, annConv) || !strings.Contains(hello.Data, clientDemoBot) {
		t.Fatalf("review hello = %s", hello.Data)
	}
	c.send(ann, annConv, `{"text":"another secret"}`)
	select {
	case ev, ok := <-ch:
		if ok && (strings.Contains(ev.Data, annThread) || strings.Contains(ev.Data, annConv)) {
			t.Fatalf("another person's event reached the review stream: %s", ev.Data)
		}
	case <-time.After(400 * time.Millisecond):
	}

	// A review device token is not a lasso credential: on a network listener
	// every /api route refuses it, as it does for any device.
	cfg := &edgeConfig{}
	mux := http.NewServeMux()
	mux.HandleFunc("/client/", serveClient)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("api")) })
	h := withCaller(withLocalGuard(mux, newHostGuard("lasso.example.com")), cfg)
	for _, role := range []listenerRole{roleAccess, roleTailnet, roleServe} {
		for _, p := range []string{"/api/client-review", "/api/app-users", "/api/bots"} {
			r := httptest.NewRequest("GET", p, nil)
			r.Host = "lasso.example.com"
			r.Header.Set("Authorization", "Bearer "+rev)
			if w := serveAs(t, h, role, r); w.Code != 403 {
				t.Fatalf("%s on %s with a review device token: %d", p, role, w.Code)
			}
		}
		r := httptest.NewRequest("GET", "/client/v1/me", nil)
		r.Host = "lasso.example.com"
		r.Header.Set("Authorization", "Bearer "+rev)
		if w := serveAs(t, h, role, r); w.Code != 200 {
			t.Fatalf("/client on %s: %d", role, w.Code)
		}
	}
}

// The owner's control surface follows the push settings' pattern: the state
// read never has the code, and an anonymous /mcp caller cannot turn it on.
func TestClientReviewSettingsGating(t *testing.T) {
	newClientTest(t)
	bot := mcpAnonSession(t)
	msg := callTool(t, bot, "update_settings", map[string]any{"client_review": map[string]any{"enabled": true}}, &struct{}{})
	if msg == "" {
		t.Fatal("an anonymous caller turned review access on")
	}
	if loadClientReview().Enabled {
		t.Fatal("review access was enabled by an anonymous caller")
	}
	enableOAuth(t, "")
	fleet := mcpTestSession(t, hostClientToken(t, "local", scopeFleet))
	var out map[string]any
	if msg := callTool(t, fleet, "update_settings", map[string]any{"client_review": map[string]any{"enabled": true, "days": 7}}, &out); msg != "" {
		t.Fatalf("a fleet credential was refused: %s", msg)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"code":"lpc_`) {
		t.Fatalf("the enabling call did not return the link once: %s", raw)
	}
	var state map[string]any
	if msg := callTool(t, fleet, "get_settings", map[string]any{"section": "client_review"}, &state); msg != "" {
		t.Fatalf("get_settings: %s", msg)
	}
	raw, _ = json.Marshal(state["client_review"])
	if !strings.Contains(string(raw), `"enabled":true`) || strings.Contains(string(raw), "lpc_") {
		t.Fatalf("get_settings client_review = %s", raw)
	}
}

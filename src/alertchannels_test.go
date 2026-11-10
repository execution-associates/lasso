package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// useAlertTestEnv is a fresh lasso.db with retries off.
func useAlertTestEnv(t *testing.T) {
	t.Helper()
	useBotTestEnv(t)
	prev := alertRetryDelays
	alertRetryDelays = []time.Duration{0}
	t.Cleanup(func() { alertRetryDelays = prev })
	alertKeyCache.Lock()
	alertKeyCache.path, alertKeyCache.key = "", nil
	alertKeyCache.Unlock()
}

func alertAPI(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	serveAlerts(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil {
		out = map[string]any{"raw": w.Body.String()}
	}
	return w.Code, out
}

// recorder is an HTTP receiver that keeps every request.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
	code int
	body string
	srv  *httptest.Server
}

type recorded struct {
	path   string
	header http.Header
	body   []byte
}

func newRecorder(t *testing.T) *recorder {
	r := &recorder{code: 200}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, recorded{path: req.URL.RequestURI(), header: req.Header.Clone(), body: b})
		code, body := r.code, r.body
		r.mu.Unlock()
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

func (r *recorder) reset() {
	r.mu.Lock()
	r.reqs = nil
	r.mu.Unlock()
}

func TestAlertChannelSecretsAreWriteOnly(t *testing.T) {
	useAlertTestEnv(t)
	rec := newRecorder(t)
	secretURL := rec.srv.URL + "/hooks/T0KEN-in-the-path"
	code, out := alertAPI(t, "POST", "/api/alerts/channels",
		`{"name":"ops","kind":"webhook","secrets":{"url":"`+secretURL+`","signing_secret":"shh-signing"}}`)
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	ch := out["channel"].(map[string]any)
	id := int64(ch["id"].(float64))
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "T0KEN") || strings.Contains(string(raw), "shh-signing") {
		t.Fatalf("the create answer carries a secret: %s", raw)
	}
	if set := ch["secrets_set"].([]any); len(set) != 2 {
		t.Errorf("secrets_set = %v", set)
	}
	// Never in plain text at rest either.
	var stored string
	_ = db.QueryRow(`SELECT secret FROM alert_channels WHERE id = ?`, id).Scan(&stored)
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, "T0KEN") {
		t.Fatalf("stored secret = %q", stored)
	}
	dbBytes, _ := os.ReadFile(filepath.Join(os.Getenv("LASSO_DIR"), "lasso.db"))
	if strings.Contains(string(dbBytes), "shh-signing") {
		t.Fatal("the signing secret is in lasso.db in plain text")
	}
	// A listing never shows them.
	_, list := alertAPI(t, "GET", "/api/alerts/channels", "")
	raw, _ = json.Marshal(list)
	if strings.Contains(string(raw), "T0KEN") || strings.Contains(string(raw), "shh-signing") {
		t.Fatalf("the listing carries a secret: %s", raw)
	}
	// A save that leaves secrets out keeps them; "" clears one.
	code, out = alertAPI(t, "PUT", "/api/alerts/channels/"+chanID(id), `{"name":"ops2","secrets":{"signing_secret":""}}`)
	if code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}
	c, _ := getAlertChannel(id)
	if c.Name != "ops2" || c.secrets["url"] != secretURL || c.secrets["signing_secret"] != "" {
		t.Errorf("after save: %q %v", c.Name, c.secrets)
	}
	// The kind cannot change, and an unknown field is refused.
	if code, _ := alertAPI(t, "PUT", "/api/alerts/channels/"+chanID(id), `{"kind":"slack"}`); code != 400 {
		t.Errorf("kind change: %d", code)
	}
	if code, _ := alertAPI(t, "PUT", "/api/alerts/channels/"+chanID(id), `{"config":{"url":"x"}}`); code != 400 {
		t.Errorf("a secret as config: %d", code)
	}
	// Test: a failing endpoint's error never carries the URL.
	rec.mu.Lock()
	rec.code, rec.body = 500, "boom"
	rec.mu.Unlock()
	_, res := alertAPI(t, "POST", "/api/alerts/channels/"+chanID(id)+"/test", "")
	if res["ok"] != false || strings.Contains(res["error"].(string), "T0KEN") || !strings.Contains(res["error"].(string), "500") {
		t.Errorf("test result = %v", res)
	}
	c, _ = getAlertChannel(id)
	if c.LastTestAt == "" || c.LastError == "" {
		t.Errorf("test not recorded: %+v", c)
	}
	// A draft test of the saved channel uses its stored secrets.
	rec.mu.Lock()
	rec.code = 200
	rec.mu.Unlock()
	_, res = alertAPI(t, "POST", "/api/alerts/channels/test?id="+chanID(id), `{"name":"draft"}`)
	if res["ok"] != true {
		t.Errorf("draft test = %v", res)
	}
	// Deleting it drops it from the defaults.
	if code, out := alertAPI(t, "PUT", "/api/alerts/settings", `{"channels":[`+chanID(id)+`]}`); code != 200 {
		t.Fatalf("settings: %d %v", code, out)
	}
	alertAPI(t, "DELETE", "/api/alerts/channels/"+chanID(id), "")
	if s := loadAlertSettings(); len(s.Channels) != 0 {
		t.Errorf("defaults still list the deleted channel: %v", s.Channels)
	}
}

func chanID(n int64) string { return strconv.FormatInt(n, 10) }

func TestAlertSettingsAPI(t *testing.T) {
	useAlertTestEnv(t)
	if code, _ := alertAPI(t, "PUT", "/api/alerts/settings", `{"channels":[99]}`); code != 400 {
		t.Errorf("an unknown channel id was accepted: %d", code)
	}
	if code, _ := alertAPI(t, "PUT", "/api/alerts/settings", `{"down_after":-1}`); code != 400 {
		t.Errorf("a negative down_after was accepted: %d", code)
	}
	code, out := alertAPI(t, "PUT", "/api/alerts/settings", `{"down_after":300,"link_base":"https://lasso.example.com/"}`)
	if code != 200 || out["down_after"].(float64) != 300 || out["link_base"] != "https://lasso.example.com" {
		t.Errorf("settings = %d %v", code, out)
	}
	// A heartbeat is never a routed channel.
	_, hb := alertAPI(t, "POST", "/api/alerts/channels", `{"kind":"heartbeat","config":{"bot":"jess"},"secrets":{"url":"https://hc.example/ping/x","ping_secret":"mcs_x"}}`)
	id := int64(hb["channel"].(map[string]any)["id"].(float64))
	if code, _ := alertAPI(t, "PUT", "/api/alerts/settings", `{"channels":[`+chanID(id)+`]}`); code != 400 {
		t.Errorf("a heartbeat was routed: %d", code)
	}
}

func TestWebhookIsSigned(t *testing.T) {
	useAlertTestEnv(t)
	rec := newRecorder(t)
	c, err := createAlertChannel(alertChannelInput{Kind: "webhook", Secrets: map[string]string{"url": rec.srv.URL + "/in", "signing_secret": "k3y", "authorization": "Bearer abc"}})
	if err != nil {
		t.Fatal(err)
	}
	dispatchAlert(alertEvent{Type: alertDown, Bot: "jess", State: "gone", Reason: "no pane", At: nowStamp(time.Now()), Path: "/bots/jess"}, []*alertChannel{c})
	alertWG.Wait()
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	ts := r.header.Get("X-Lasso-Timestamp")
	m := hmac.New(sha256.New, []byte("k3y"))
	m.Write([]byte(ts + "." + string(r.body)))
	want := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if r.header.Get("X-Lasso-Signature") != want {
		t.Errorf("signature %q, want %q", r.header.Get("X-Lasso-Signature"), want)
	}
	if r.header.Get("Authorization") != "Bearer abc" || r.header.Get("X-Lasso-Event") != "down" || r.header.Get("X-Lasso-Delivery") == "" {
		t.Errorf("headers = %v", r.header)
	}
	var ev alertEvent
	if json.Unmarshal(r.body, &ev) != nil || ev.Bot != "jess" || ev.Reason != "no pane" || ev.ID == "" {
		t.Errorf("body = %s", r.body)
	}
	if d := listAlertDeliveries("jess", 5); len(d) != 1 || d[0].Status != "sent" {
		t.Errorf("deliveries = %+v", d)
	}
}

func TestChatChannelPayloads(t *testing.T) {
	useAlertTestEnv(t)
	rec := newRecorder(t)
	prev := telegramAPIBase
	telegramAPIBase = rec.srv.URL
	t.Cleanup(func() { telegramAPIBase = prev })
	ev := alertEvent{Type: alertRecovered, Bot: "fitty", State: "ok", DowntimeSec: 600, At: nowStamp(time.Now()), URL: "https://l.example/bots/fitty"}

	slack, _ := createAlertChannel(alertChannelInput{Kind: "slack", Secrets: map[string]string{"webhook_url": rec.srv.URL + "/slack"}})
	discord, _ := createAlertChannel(alertChannelInput{Kind: "discord", Secrets: map[string]string{"webhook_url": rec.srv.URL + "/discord?thread_id=4"}})
	tg, err := createAlertChannel(alertChannelInput{Kind: "telegram", Config: map[string]string{"chat_id": "-100123", "thread_id": "7"}, Secrets: map[string]string{"bot_token": "123:SECRETTOKEN"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*alertChannel{slack, discord, tg} {
		if _, err := deliverAlert(c, ev); err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
	}
	reqs := rec.all()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d", len(reqs))
	}
	var s map[string]any
	_ = json.Unmarshal(reqs[0].body, &s)
	if !strings.Contains(s["text"].(string), "fitty recovered") || !strings.Contains(string(reqs[0].body), "Down for 10m") {
		t.Errorf("slack = %s", reqs[0].body)
	}
	if !strings.Contains(reqs[1].path, "wait=true") || !strings.Contains(reqs[1].path, "thread_id=4") || !strings.Contains(string(reqs[1].body), `"embeds"`) {
		t.Errorf("discord = %s %s", reqs[1].path, reqs[1].body)
	}
	var m map[string]any
	_ = json.Unmarshal(reqs[2].body, &m)
	if reqs[2].path != "/bot123:SECRETTOKEN/sendMessage" || m["chat_id"] != "-100123" || m["message_thread_id"].(float64) != 7 || m["parse_mode"] != "HTML" {
		t.Errorf("telegram = %s %s", reqs[2].path, reqs[2].body)
	}

	// Telegram's refusal is explained, and never names the token.
	rec.mu.Lock()
	rec.code, rec.body = 401, `{"ok":false,"description":"Unauthorized"}`
	rec.mu.Unlock()
	_, err = deliverAlert(tg, ev)
	msg := scrubAlertErr(err, tg.secrets)
	if !strings.Contains(msg, "Unauthorized") || strings.Contains(msg, "SECRETTOKEN") {
		t.Errorf("telegram error = %q", msg)
	}
	if err := validateTelegram(map[string]string{"chat_id": "abc", "bot_token": "1:x"}); err == nil {
		t.Error("a non-numeric chat id was accepted")
	}
}

func TestScrubAlertErrDropsURLs(t *testing.T) {
	// A dial error's *url.Error embeds the URL, which is the credential.
	_, err := alertHTTP.Get("http://127.0.0.1:1/services/T000/B000/XXXXSECRET")
	msg := scrubAlertErr(err, map[string]string{"webhook_url": "http://127.0.0.1:1/services/T000/B000/XXXXSECRET"})
	if strings.Contains(msg, "XXXXSECRET") || msg == "" {
		t.Errorf("scrubbed = %q", msg)
	}
}

// fakeSMTP is just enough of an SMTP server: EHLO (AUTH PLAIN, optional
// STARTTLS), AUTH, MAIL, RCPT, DATA, QUIT.
type fakeSMTP struct {
	ln       net.Listener
	starttls bool
	mu       sync.Mutex
	auth     []string
	from     string
	to       []string
	data     string
}

func startFakeSMTP(t *testing.T, starttls bool) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, starttls: starttls}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		f.mu.Lock()
		switch cmd {
		case "EHLO", "HELO":
			if f.starttls {
				say("250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN")
			} else {
				say("250-fake\r\n250 AUTH PLAIN")
			}
		case "AUTH":
			parts := strings.Fields(line)
			if len(parts) == 3 {
				dec, _ := base64.StdEncoding.DecodeString(parts[2])
				f.auth = append(f.auth, string(dec))
			}
			say("235 ok")
		case "MAIL":
			f.from = line
			say("250 ok")
		case "RCPT":
			f.to = append(f.to, line)
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					f.mu.Unlock()
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.data = b.String()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			f.mu.Unlock()
			return
		default:
			say("502 no")
		}
		f.mu.Unlock()
	}
}

func TestEmailChannel(t *testing.T) {
	useAlertTestEnv(t)
	f := startFakeSMTP(t, false)
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	c, err := createAlertChannel(alertChannelInput{Kind: "email",
		Config:  map[string]string{"host": "127.0.0.1", "port": port, "security": "none", "username": "lasso", "from": "Lasso <lasso@example.com>", "to": "a@example.com, b@example.com"},
		Secrets: map[string]string{"password": "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	ev := alertEvent{Type: alertDown, Bot: "olivia", State: "deaf", Reason: "lasso's channel has never polled", Since: "2026-11-09T10:00:00Z", At: nowStamp(time.Now())}
	if _, err := deliverAlert(c, ev); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.auth) != 1 || f.auth[0] != "\x00lasso\x00pw" {
		t.Errorf("auth = %q", f.auth)
	}
	if !strings.Contains(f.from, "<lasso@example.com>") || len(f.to) != 2 {
		t.Errorf("envelope = %q %q", f.from, f.to)
	}
	for _, want := range []string{"Subject: [lasso] olivia is deaf", "To: <a@example.com>, <b@example.com>", "Content-Type: text/plain; charset=utf-8", "lasso's channel has never polled"} {
		if !strings.Contains(f.data, want) {
			t.Errorf("message lacks %q:\n%s", want, f.data)
		}
	}
}

func TestEmailChannelRefuses(t *testing.T) {
	useAlertTestEnv(t)
	// STARTTLS required but not offered: refused before any password is sent.
	f := startFakeSMTP(t, false)
	_, port, _ := net.SplitHostPort(f.ln.Addr().String())
	c, err := createAlertChannel(alertChannelInput{Kind: "email",
		Config:  map[string]string{"host": "127.0.0.1", "port": port, "username": "u", "from": "x@example.com", "to": "y@example.com"},
		Secrets: map[string]string{"password": "pw"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deliverAlert(c, alertEvent{Type: alertTest, At: nowStamp(time.Now())}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("err = %v", err)
	}
	f.mu.Lock()
	if len(f.auth) != 0 {
		t.Error("a password went over plaintext")
	}
	f.mu.Unlock()
	// Header injection through any field is refused at save.
	for _, cfg := range []map[string]string{
		{"host": "h", "from": "x@example.com\r\nBcc: z@example.com", "to": "y@example.com"},
		{"host": "h", "from": "x@example.com", "to": "y@example.com\nBcc: z@example.com"},
	} {
		if _, err := createAlertChannel(alertChannelInput{Kind: "email", Config: cfg}); err == nil {
			t.Errorf("accepted %q", cfg)
		}
	}
}

func TestHeartbeatChannel(t *testing.T) {
	useAlertTestEnv(t)
	rec := newRecorder(t)
	if _, err := createAlertChannel(alertChannelInput{Kind: "heartbeat", Config: map[string]string{"bot": "stub"}, Secrets: map[string]string{"url": rec.srv.URL + "/api/ping/tok"}}); err == nil {
		t.Error("a pausing heartbeat without its ping secret was accepted")
	}
	if _, err := createAlertChannel(alertChannelInput{Kind: "heartbeat", Config: map[string]string{"bot": "stub", "on_hold": "silent"}, Secrets: map[string]string{"url": rec.srv.URL + "/api/ping/tok"}}); err != nil {
		t.Errorf("a silent heartbeat needs no secret: %v", err)
	}
	if _, err := createAlertChannel(alertChannelInput{Kind: "heartbeat", Config: map[string]string{"bot": "stub", "every": "5", "on_hold": "silent"}, Secrets: map[string]string{"url": rec.srv.URL}}); err == nil {
		t.Error("a 5s heartbeat was accepted")
	}
	c, err := createAlertChannel(alertChannelInput{Kind: "heartbeat", Config: map[string]string{"bot": "stub", "every": "30"}, Secrets: map[string]string{"url": rec.srv.URL + "/api/ping/tok", "ping_secret": "mcs_s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	down := alertEvent{Type: alertDown, Bot: "stub", State: "gone", Reason: "no pane", At: nowStamp(time.Now())}
	if _, err := deliverAlert(c, down); err != nil {
		t.Fatal(err)
	}
	if _, err := deliverAlert(c, alertEvent{Type: alertRecovered, Bot: "stub", At: nowStamp(time.Now())}); err != nil {
		t.Fatal(err)
	}
	reqs := rec.all()
	if len(reqs) != 2 || reqs[0].path != "/api/ping/tok/report" || !strings.Contains(string(reqs[0].body), "no pane") || reqs[1].path != "/api/ping/tok" {
		t.Fatalf("heartbeat requests = %+v", reqs)
	}
	for _, r := range reqs {
		if r.header.Get("Authorization") != "Bearer mcs_s3cret" {
			t.Errorf("%s without the ping secret: %q", r.path, r.header.Get("Authorization"))
		}
	}
	// The secret is write-only like any other.
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "mcs_s3cret") {
		t.Error("the ping secret is in the channel's JSON")
	}
}

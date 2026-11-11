package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// pushCall is one request a fake provider received.
type pushCall struct {
	Host   string
	Path   string
	Header http.Header
	Body   map[string]any
	Proto  string
}

// pushRig stands in for APNs (both environments), FCM and Google's token
// endpoint, all TLS servers speaking HTTP/2.
type pushRig struct {
	t        *testing.T
	calls    chan pushCall
	mu       sync.Mutex
	status   int    // what APNs/FCM answer next (0: 200)
	reason   string // the reason code with it
	only     string // when set, only this host answers status
	onCall   func() // run as a push arrives, before it is answered
	apnsKey  *ecdsa.PrivateKey
	tokens   int // token exchanges
	logs     *bytes.Buffer
	prodHost string
}

func (p *pushRig) answer(status int, reason string) {
	p.mu.Lock()
	p.status, p.reason = status, reason
	p.mu.Unlock()
}

func newPushRig(t *testing.T) *pushRig {
	t.Helper()
	// A push an earlier test decided must be out before this test's fake
	// providers stand in: the worker reads the providers' addresses as it sends.
	if !pushWaitIdle(10 * time.Second) {
		t.Fatal("pushes from an earlier test are still in flight")
	}
	rig := &pushRig{t: t, calls: make(chan pushCall, 64), logs: &bytes.Buffer{}}
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	rig.apnsKey = ek
	der, _ := x509.MarshalPKCS8PrivateKey(ek)
	p8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rder, _ := x509.MarshalPKCS8PrivateKey(rk)
	sa, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "lasso-test", "client_email": "push@lasso-test.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rder}))})
	mk := func(name string) *httptest.Server {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.URL.Path == "/token" {
				rig.mu.Lock()
				rig.tokens++
				rig.mu.Unlock()
				_ = r.ParseForm()
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"access_token":"ya29.fake","expires_in":3600}`)
				return
			}
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			select {
			case rig.calls <- pushCall{Host: name, Path: r.URL.Path, Header: r.Header.Clone(), Body: m, Proto: r.Proto}:
			default:
			}
			rig.mu.Lock()
			st, reason := rig.status, rig.reason
			if rig.only != "" && rig.only != name {
				st = 200
			}
			hook := rig.onCall
			rig.mu.Unlock()
			if hook != nil {
				hook()
			}
			if st == 0 || st == 200 {
				w.WriteHeader(200)
				return
			}
			w.WriteHeader(st)
			if name == "fcm" {
				fmt.Fprintf(w, `{"error":{"code":%d,"status":"%s","message":"Requested entity was not found.","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError","errorCode":"%s"}]}}`, st, reason, reason)
				return
			}
			fmt.Fprintf(w, `{"reason":%q}`, reason)
		}))
		s.EnableHTTP2 = true
		s.StartTLS()
		t.Cleanup(s.Close)
		return s
	}
	prod, sand, fcm := mk("production"), mk("sandbox"), mk("fcm")
	rig.prodHost = prod.URL
	prevHosts, prevBase, prevTok, prevClient, prevSecret, prevRetries := apnsHosts, fcmSendBase, googleTokenURL, pushHTTPClient, clientPushSecret, clientPushRetries
	apnsHosts = map[string]string{"production": prod.URL, "sandbox": sand.URL}
	fcmSendBase, googleTokenURL = fcm.URL, fcm.URL+"/token"
	pool := prod.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	for _, s := range []*httptest.Server{sand, fcm} {
		pool.AddCert(s.Certificate())
	}
	tr := prod.Client().Transport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	pushHTTPClient = &http.Client{Transport: tr, Timeout: 5 * time.Second}
	clientPushRetries = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	clientPushSecret = func(_ context.Context, ref string) (string, error) {
		switch ref {
		case "APNS_P8":
			return p8, nil
		case "FCM_SA":
			return string(sa), nil
		case "op://Lasso/garbage/field":
			return "not a key", nil
		case "APNS_P8_ONE_LINE":
			return strings.ReplaceAll(strings.TrimSpace(p8), "\n", `\n`), nil
		}
		return "", fmt.Errorf("%s is not set in lasso's environment", ref)
	}
	resetPushSecrets()
	pushFailingNow.Delete("apns")
	pushFailingNow.Delete("fcm")
	pushLastSent.Lock()
	pushLastSent.thread, pushLastSent.badge = map[string]time.Time{}, map[string]time.Time{}
	pushLastSent.Unlock()
	log.SetOutput(rig.logs)
	t.Cleanup(func() {
		// Every push this test decided goes out to its own fakes, before
		// they close (a cut connection would be retried at the next test's).
		pushWaitIdle(10 * time.Second)
		log.SetOutput(io.Discard)
		apnsHosts, fcmSendBase, googleTokenURL, pushHTTPClient, clientPushSecret, clientPushRetries = prevHosts, prevBase, prevTok, prevClient, prevSecret, prevRetries
		resetPushSecrets()
	})
	return rig
}

// next is the push the last action sent. Pushes are decided synchronously and
// counted until sent, so once the queue is idle every one has reached the
// fake provider: no waiting on time.
func (p *pushRig) next() pushCall {
	p.t.Helper()
	if !pushWaitIdle(5 * time.Second) {
		p.t.Fatal("a push is still in flight")
	}
	select {
	case c := <-p.calls:
		return c
	default:
		p.t.Fatal("no push arrived")
	}
	return pushCall{}
}

func (p *pushRig) none() {
	p.t.Helper()
	if !pushWaitIdle(5 * time.Second) {
		p.t.Fatal("a push is still in flight")
	}
	select {
	case c := <-p.calls:
		p.t.Fatalf("an unexpected push: %+v", c)
	default:
	}
}

func setPushConfig(t *testing.T, body string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	serveClientPushConfig(w, httptest.NewRequest("PUT", "/api/client-push", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out
}

func (c *clientTest) pairOn(userID, platform string) string {
	c.t.Helper()
	code, _, _ := mintAppPairing(userID, "local", time.Now())
	_, body := c.do("POST", "/client/v1/pair", "", `{"code":"`+code+`","label":"phone","platform":"`+platform+`"}`)
	var out struct{ Token string }
	_ = json.Unmarshal(body, &out)
	return out.Token
}

func verifyAPNsJWT(t *testing.T, auth string, key *ecdsa.PrivateKey) map[string]any {
	t.Helper()
	tok := strings.TrimPrefix(auth, "bearer ")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", auth)
	}
	hb, _ := b64.DecodeString(parts[0])
	cb, _ := b64.DecodeString(parts[1])
	sig, _ := b64.DecodeString(parts[2])
	var hdr, claims map[string]any
	_ = json.Unmarshal(hb, &hdr)
	_ = json.Unmarshal(cb, &claims)
	if hdr["alg"] != "ES256" || hdr["kid"] != "ABCDE12345" || claims["iss"] != "JF4UNR89NW" || claims["iat"] == nil {
		t.Fatalf("jwt header %v claims %v", hdr, claims)
	}
	d := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, d[:], r, s) {
		t.Fatal("the provider token does not verify")
	}
	return claims
}

func TestClientPushUnconfigured(t *testing.T) {
	c := newClientTest(t)
	newPushRig(t)
	_, ann := c.person("Ann", false, "jess")
	var me map[string]any
	c.get("/client/v1/me", ann, &me)
	if p := me["push"].(map[string]any); p["available"] != false || p["reason"] != "not_configured" || p["provider"] != "apns" || p["registered"] != false {
		t.Fatalf("push = %v", p)
	}
	// Registering works without credentials; the token is never returned.
	tok := strings.Repeat("ab", 32)
	code, out, raw := c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+strings.ToUpper(tok)+`","environment":"production","app_id":"com.orangecountyai.Lasso.ios"}`)
	if code != 200 || out["push"].(map[string]any)["registered"] != true || strings.Contains(strings.ToLower(string(raw)), tok) {
		t.Fatalf("register: %d %s", code, raw)
	}
	var stored string
	_ = db.QueryRow(`SELECT token FROM app_push_tokens`).Scan(&stored)
	if stored != tok {
		t.Fatalf("stored %q", stored)
	}
	for _, bad := range []string{`{"provider":"fcm","token":"x"}`, `{"provider":"apns","token":"zz","environment":"production"}`,
		`{"provider":"apns","token":"` + tok + `"}`} {
		if code, _, _ := c.jsonDo("PUT", "/client/v1/device/push", ann, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// Unreadable credentials.
	setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"MISSING_VAR"}}`)
	c.get("/client/v1/me", ann, &me)
	if p := me["push"].(map[string]any); p["available"] != false || p["reason"] != "unreadable" || p["environment"] != "production" {
		t.Fatalf("unreadable = %v", p)
	}
	setPushConfig(t, `{"apns":{"key_ref":"op://Lasso/garbage/field"}}`)
	if st, _ := clientPushState("apns"); st != pushUnreadable {
		t.Fatalf("garbage key = %s", st)
	}
	// Validation and references only.
	w := httptest.NewRecorder()
	serveClientPushConfig(w, httptest.NewRequest("PUT", "/api/client-push", strings.NewReader(`{"apns":{"key_ref":"-----BEGIN PRIVATE KEY-----"}}`)))
	if w.Code != 400 {
		t.Fatalf("a key in the setting: %d", w.Code)
	}
	// Unsupported platform; delete; revoking deletes the token.
	other := c.pairOn(findUserID(t, "Ann"), "other")
	c.get("/client/v1/me", other, &me)
	if p := me["push"].(map[string]any); p["provider"] != "none" || p["reason"] != "unsupported_platform" {
		t.Fatalf("other = %v", p)
	}
	if code, out, _ := c.jsonDo("DELETE", "/client/v1/device/push", ann, ""); code != 200 || out["push"].(map[string]any)["registered"] != false {
		t.Fatal("unregister")
	}
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+tok+`","environment":"sandbox"}`)
	c.do("DELETE", "/client/v1/device", ann, "")
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_push_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("signing out kept the push token")
	}
}

func TestClientPushAPNs(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	cfg := setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"APNS_P8"}}`)
	if cfg["apns"].(map[string]any)["state"] != pushReady {
		t.Fatalf("state = %v", cfg)
	}
	annID, ann := c.person("Ann", false, "jess")
	tok := strings.Repeat("cd", 32)
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+tok+`","environment":"production"}`)
	var me map[string]any
	c.get("/client/v1/me", ann, &me)
	if p := me["push"].(map[string]any); p["available"] != true || p["environment"] != "production" {
		t.Fatalf("push = %v", p)
	}
	tid := c.newThread(ann, `{"bots":["jess"],"title":"Kitchen remodel quotes"}`)
	c.sendIn(ann, tid, `{"text":"quotes?"}`)
	rt := clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	// An interim note is not pushed; the answer is.
	botClientReply("jess", tid, rt, "looking", false, nil, false, time.Now())
	botClientReply("jess", tid, rt, "Permits in Irvine need a plan check\nand more", true, nil, false, time.Now())
	call := rig.next()
	if call.Host != "production" || call.Path != "/3/device/"+tok || call.Proto != "HTTP/2.0" {
		t.Fatalf("call = %+v", call)
	}
	h := call.Header
	if h.Get("apns-topic") != clientPushDefaultTopic || h.Get("apns-push-type") != "alert" || h.Get("apns-priority") != "10" || h.Get("apns-collapse-id") != tid || h.Get("apns-expiration") == "" {
		t.Fatalf("headers = %v", h)
	}
	verifyAPNsJWT(t, h.Get("authorization"), rig.apnsKey)
	aps := call.Body["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	lasso := call.Body["lasso"].(map[string]any)
	if alert["title"] != "jess" || alert["subtitle"] != "Kitchen remodel quotes" || alert["body"] != "Permits in Irvine need a plan check and more" ||
		aps["sound"] != "default" || aps["thread-id"] != tid || aps["category"] != "LASSO_MESSAGE" || aps["badge"].(float64) != 1 ||
		lasso["kind"] != "message" || lasso["thread_id"] != tid || lasso["bot"] != "jess" || lasso["v"].(float64) != 1 {
		t.Fatalf("payload = %v", call.Body)
	}
	rig.none() // the interim note
	// Within 30 s: quiet.
	c.sendIn(ann, tid, `{"text":"more"}`)
	rt = clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	botClientReply("jess", tid, rt, "second", true, nil, false, time.Now())
	call = rig.next()
	if call.Header.Get("apns-priority") != "5" || call.Body["aps"].(map[string]any)["sound"] != nil {
		t.Fatalf("quiet push = %v / %v", call.Header, call.Body)
	}
	// Previews off: nothing written leaves lasso; a main thread has no subtitle.
	c.jsonDo("PUT", "/client/v1/me/settings", ann, `{"push_previews":false}`)
	cid := c.conv(ann, "jess")
	_, sm := c.send(ann, cid, `{"text":"main"}`)
	botClientReply("jess", cid, replyTokens(t)[sm.ID], "secret words", true, nil, false, time.Now())
	call = rig.next()
	alert = call.Body["aps"].(map[string]any)["alert"].(map[string]any)
	if alert["body"] != "New message" || alert["subtitle"] != nil || strings.Contains(fmt.Sprint(call.Body), "secret") {
		t.Fatalf("previews off = %v", call.Body)
	}
	c.jsonDo("PUT", "/client/v1/me/settings", ann, `{"push_previews":true}`)
	// Reading on another device: a badge-only push.
	other := c.pairOn(annID, "ios")
	var page struct{ Messages []clientMessageView }
	c.get("/client/v1/threads/"+tid+"/messages", other, &page)
	last := page.Messages[len(page.Messages)-1].Cursor
	c.jsonDo("POST", "/client/v1/threads/"+tid+"/read", other, `{"cursor":"`+last+`"}`)
	call = rig.next()
	if call.Body["aps"].(map[string]any)["alert"] != nil || call.Body["lasso"].(map[string]any)["kind"] != "badge" || call.Header.Get("apns-collapse-id") != "" {
		t.Fatalf("badge push = %v %v", call.Header, call.Body)
	}
	// A muted thread: nothing.
	c.jsonDo("PATCH", "/client/v1/threads/"+tid, ann, `{"muted":true}`)
	time.Sleep(50 * time.Millisecond)
	for len(rig.calls) > 0 {
		<-rig.calls
	}
	c.sendIn(ann, tid, `{"text":"muted?"}`)
	rt = clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	botClientReply("jess", tid, rt, "muted answer", true, nil, false, time.Now())
	rig.none()
	c.jsonDo("PATCH", "/client/v1/threads/"+tid, ann, `{"muted":false}`)
	// A proposal pushes.
	c.addBot("mark")
	_, _ = setAppGrant(annID, "mark", false, time.Now())
	c.sendIn(ann, tid, `{"text":"anyone else?"}`)
	rt = clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	a, _ := botActorFromToken("jess", rt, false, time.Now())
	if _, err := botThreadProposeBot(a, tid, "mark", "Mark knows permits.", time.Now()); err != nil {
		t.Fatal(err)
	}
	call = rig.next()
	if call.Body["aps"].(map[string]any)["category"] != "LASSO_PROPOSAL" || call.Body["lasso"].(map[string]any)["proposal_id"] == nil ||
		call.Body["aps"].(map[string]any)["alert"].(map[string]any)["body"] != "Suggests adding mark" {
		t.Fatalf("proposal push = %v", call.Body)
	}
	// Sandbox goes to the sandbox host.
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+tok+`","environment":"sandbox"}`)
	time.Sleep(50 * time.Millisecond)
	for len(rig.calls) > 0 {
		<-rig.calls
	}
	pushLastSent.Lock()
	pushLastSent.thread = map[string]time.Time{}
	pushLastSent.Unlock()
	botClientReply("jess", tid, rt, "to sandbox", true, nil, false, time.Now())
	if call = rig.next(); call.Host != "sandbox" {
		t.Fatalf("host = %s", call.Host)
	}
	// Nothing of a token or a message is logged.
	if s := rig.logs.String(); strings.Contains(s, tok) || strings.Contains(s, "Permits") || strings.Contains(s, "secret") {
		t.Fatalf("logs leak: %s", s)
	}
}

func TestClientPushForeground(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"APNS_P8"}}`)
	annID, ann := c.person("Ann", false, "jess")
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+strings.Repeat("ef", 32)+`","environment":"production"}`)
	tid := c.newThread(ann, `{"bots":["jess"],"title":"T"}`)
	reply := func(text string) {
		c.sendIn(ann, tid, `{"text":"q"}`)
		rt := clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
		pushLastSent.Lock()
		pushLastSent.thread = map[string]time.Time{}
		pushLastSent.Unlock()
		botClientReply("jess", tid, rt, text, true, nil, false, time.Now())
	}
	// The stream is open in the foreground: no push.
	ch, stop := c.openPersonStream(ann, "", "")
	nextEvent(t, ch, "hello")
	reply("while open")
	rig.none()
	// The app says background: pushed at once, stream or not.
	c.jsonDo("POST", "/client/v1/device/presence", ann, `{"state":"background"}`)
	reply("backgrounded")
	rig.next()
	c.jsonDo("POST", "/client/v1/device/presence", ann, `{"state":"foreground"}`)
	reply("foreground again")
	rig.none()
	stop()
	// A heartbeat older than 25 s (a killed app): pushed. Whether or not the
	// closed stream has cleared it yet, the device is not in the foreground.
	_, _ = db.Exec(`UPDATE app_devices SET stream_seen_at = ?, presence = 'foreground' WHERE user_id = ?`, nowStamp(time.Now().Add(-30*time.Second)), annID)
	reply("stale heartbeat")
	rig.next()
	if code, _, _ := c.jsonDo("POST", "/client/v1/device/presence", ann, `{"state":"asleep"}`); code != 400 {
		t.Fatal("bad presence accepted")
	}
}

func TestClientPushFCM(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	setPushConfig(t, `{"fcm":{"credentials_ref":"FCM_SA"}}`)
	annID, _ := c.person("Ann", false, "jess")
	droid := c.pairOn(annID, "android")
	c.jsonDo("PUT", "/client/v1/device/push", droid, `{"provider":"fcm","token":"fXy:APA91b-test_token","app_id":"com.orangecountyai.lasso"}`)
	var me map[string]any
	c.get("/client/v1/me", droid, &me)
	if p := me["push"].(map[string]any); p["available"] != true || p["provider"] != "fcm" {
		t.Fatalf("push = %v", p)
	}
	tid := c.newThread(droid, `{"bots":["jess"],"title":"Kitchen"}`)
	c.sendIn(droid, tid, `{"text":"q"}`)
	rt := clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	botClientReply("jess", tid, rt, "answer", true, nil, false, time.Now())
	call := rig.next()
	if call.Path != "/v1/projects/lasso-test/messages:send" || call.Header.Get("Authorization") != "Bearer ya29.fake" {
		t.Fatalf("fcm call = %+v", call)
	}
	msg := call.Body["message"].(map[string]any)
	data := msg["data"].(map[string]any)
	android := msg["android"].(map[string]any)
	if msg["token"] != "fXy:APA91b-test_token" || data["kind"] != "message" || data["title"] != "jess" || data["subtitle"] != "Kitchen" ||
		data["body"] != "answer" || data["badge"] != "1" || data["quiet"] != "0" || data["url"] != "lasso://thread/"+tid ||
		android["priority"] != "high" || android["collapse_key"] != tid || android["ttl"] != "86400s" || msg["notification"] != nil {
		t.Fatalf("fcm message = %v", call.Body)
	}
	for k, v := range data {
		if _, ok := v.(string); !ok {
			t.Errorf("data %s is not a string: %v", k, v)
		}
	}
	// A dead token is deleted.
	rig.answer(404, "UNREGISTERED")
	c.sendIn(droid, tid, `{"text":"q2"}`)
	rt = clientEventsOf(claim(t, "jess"), "message")[0].Meta["reply_token"]
	botClientReply("jess", tid, rt, "answer 2", true, nil, false, time.Now())
	rig.next()
	pushWaitFor(t, func() bool {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM app_push_tokens`).Scan(&n)
		return n == 0
	})
	if rig.tokens != 1 {
		t.Fatalf("token exchanges = %d (the access token is cached)", rig.tokens)
	}
}

func pushWaitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// Every row of the dead-token table, through the owner's test push.
func TestClientPushResults(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"APNS_P8"},"fcm":{"credentials_ref":"FCM_SA"}}`)
	annID, ann := c.person("Ann", false, "jess")
	droid := c.pairOn(annID, "android")
	var me struct{ Device struct{ ID string } }
	c.get("/client/v1/me", ann, &me)
	iosDev := me.Device.ID
	c.get("/client/v1/me", droid, &me)
	droidDev := me.Device.ID
	register := func() {
		c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+strings.Repeat("aa", 32)+`","environment":"production"}`)
		c.jsonDo("PUT", "/client/v1/device/push", droid, `{"provider":"fcm","token":"fcm-token"}`)
	}
	test := func(dev string) map[string]any {
		w := httptest.NewRecorder()
		serveClientPushConfig(w, httptest.NewRequest("POST", "/api/client-push/test", strings.NewReader(`{"user_id":"`+annID+`","device_id":"`+dev+`"}`)))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		for len(rig.calls) > 0 {
			<-rig.calls
		}
		return out
	}
	has := func(dev string) bool { _, ok := pushTokenOfDevice(dev); return ok }
	cases := []struct {
		dev     string
		status  int
		reason  string
		deleted bool
		failing bool
	}{
		{iosDev, 410, "Unregistered", true, false},
		{iosDev, 400, "BadDeviceToken", true, false},
		{iosDev, 400, "DeviceTokenNotForTopic", true, false},
		{iosDev, 403, "ExpiredProviderToken", false, true},
		{iosDev, 403, "Forbidden", false, true},
		{iosDev, 400, "TopicDisallowed", false, true},
		{iosDev, 429, "TooManyRequests", false, false},
		{iosDev, 503, "ServiceUnavailable", false, false},
		{droidDev, 404, "UNREGISTERED", true, false},
		{droidDev, 403, "SENDER_ID_MISMATCH", true, false},
		{droidDev, 403, "PERMISSION_DENIED", false, true},
		{droidDev, 500, "INTERNAL", false, false},
	}
	for _, tc := range cases {
		register()
		pushFailingNow.Delete("apns")
		pushFailingNow.Delete("fcm")
		rig.answer(tc.status, tc.reason)
		out := test(tc.dev)
		if out["ok"] != false || out["reason"] != tc.reason {
			t.Errorf("%d %s: %v", tc.status, tc.reason, out)
		}
		if has(tc.dev) == tc.deleted {
			t.Errorf("%d %s: token deleted = %v, want %v", tc.status, tc.reason, !has(tc.dev), tc.deleted)
		}
		prov := "apns"
		if tc.dev == droidDev {
			prov = "fcm"
		}
		if st, _ := clientPushState(prov); (st == pushFailing) != tc.failing {
			t.Errorf("%d %s: state %s", tc.status, tc.reason, st)
		}
		if !tc.deleted {
			tk, _ := pushTokenOfDevice(tc.dev)
			if tk.LastError != tc.reason {
				t.Errorf("%d %s: last_error %q", tc.status, tc.reason, tk.LastError)
			}
		}
	}
	// Success clears failing and records last_ok_at; the owner sees push on the device, never the token.
	register()
	rig.answer(200, "")
	if out := test(iosDev); out["ok"] != true {
		t.Fatalf("ok push = %v", out)
	}
	if st, _ := clientPushState("apns"); st != pushReady {
		t.Fatalf("state after success = %s", st)
	}
	w := httptest.NewRecorder()
	serveAppUsers(w, httptest.NewRequest("GET", "/api/app-users/"+annID, nil))
	if !strings.Contains(w.Body.String(), `"push":{`) || strings.Contains(w.Body.String(), strings.Repeat("aa", 32)) || strings.Contains(w.Body.String(), "fcm-token") {
		t.Fatalf("owner view = %s", w.Body)
	}
	// The setting holds references only; GET answers them and the states.
	w = httptest.NewRecorder()
	serveClientPushConfig(w, httptest.NewRequest("GET", "/api/client-push", nil))
	if !strings.Contains(w.Body.String(), `"key_ref":"APNS_P8"`) || strings.Contains(w.Body.String(), "PRIVATE KEY") || !strings.Contains(w.Body.String(), `"project_id":"lasso-test"`) {
		t.Fatalf("config view = %s", w.Body)
	}
	raw, _ := getSetting(clientPushSettingKey)
	if strings.Contains(raw, "PRIVATE") {
		t.Fatal("a key reached lasso.db")
	}
	_ = url.Values{}
}

// A token registered with the wrong APNs environment is tried on the other
// before it is dropped, and moves there when it works; a one-line PEM reads.
func TestClientPushAPNsEnvironmentFallback(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"APNS_P8_ONE_LINE"}}`)
	if st, why := clientPushState("apns"); st != pushReady {
		t.Fatalf("one-line PEM: %s %s", st, why)
	}
	_, ann := c.person("Ann", false, "jess")
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+strings.Repeat("ab", 32)+`","environment":"sandbox"}`)
	rig.mu.Lock()
	rig.status, rig.reason, rig.only = 400, "BadDeviceToken", "sandbox"
	rig.mu.Unlock()
	var me struct{ Device struct{ ID string } }
	c.get("/client/v1/me", ann, &me)
	tok, _ := pushTokenOfDevice(me.Device.ID)
	res := sendAPNs(pushJob{tok: tok, note: pushNote{Kind: "test", Title: "t", Body: "b"}})
	if !res.OK {
		t.Fatalf("fallback = %+v", res)
	}
	if a, b := rig.next(), rig.next(); a.Host != "sandbox" || b.Host != "production" {
		t.Fatalf("hosts %s, %s", a.Host, b.Host)
	}
	if tok, ok := pushTokenOfDevice(me.Device.ID); !ok || tok.Environment != "production" {
		t.Fatalf("token after fallback = %+v", tok)
	}
}

// A push decided while the app was in the background is not sent once the
// app has come to the foreground: not from the queue, not on a retry.
func TestClientPushRecheckedBeforeSending(t *testing.T) {
	c := newClientTest(t)
	rig := newPushRig(t)
	setPushConfig(t, `{"apns":{"team_id":"JF4UNR89NW","key_id":"ABCDE12345","key_ref":"APNS_P8"}}`)
	annID, ann := c.person("Ann", false, "jess")
	c.jsonDo("PUT", "/client/v1/device/push", ann, `{"provider":"apns","token":"`+strings.Repeat("12", 32)+`","environment":"production"}`)
	var me struct{ Device struct{ ID string } }
	c.get("/client/v1/me", ann, &me)
	tok, _ := pushTokenOfDevice(me.Device.ID)
	job := pushJob{tok: tok, note: pushNote{Kind: "message", ThreadID: "t_x", Title: "jess", Body: "hi"}}
	foreground := func() {
		_, _ = db.Exec(`UPDATE app_devices SET presence = 'foreground', stream_seen_at = ? WHERE user_id = ?`, nowStamp(time.Now()), annID)
	}
	if !pushStillWanted(job, time.Now()) {
		t.Fatal("a background device's push is not wanted")
	}
	// Queued, then the app came to the front before the worker took it.
	foreground()
	enqueuePush(job)
	rig.none()
	// A retry after a transient failure: the app comes to the front while
	// the provider answers the first attempt.
	_, _ = db.Exec(`UPDATE app_devices SET presence = 'background' WHERE user_id = ?`, annID)
	clientPushRetries = []time.Duration{0, 0}
	rig.answer(503, "ServiceUnavailable")
	rig.mu.Lock()
	rig.onCall = foreground
	rig.mu.Unlock()
	res := sendPushWithRetry(job)
	if !res.Dropped {
		t.Fatalf("retry of a push to a foreground device = %+v", res)
	}
	if n := len(rig.calls); n != 1 {
		t.Fatalf("%d attempts reached the provider", n)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// --- a stand-in for Inworld ----------------------------------------------------

// inworldRig speaks Inworld's documented protocol: the streaming WebSocket
// (create, send_text with flush_context, close_context, cancel_context in;
// contextCreated, audioChunk, flushCompleted, contextClosed or a status out) and
// the voices list. It records what lasso sent.
type inworldRig struct {
	t   *testing.T
	srv *httptest.Server
	key string

	mu         sync.Mutex
	conns      int
	auths      []string
	creates    []map[string]any
	texts      []string
	cancels    []string
	closes     []string
	cancelled  map[string]bool
	chunks     int
	chunkBytes int
	chunkDelay time.Duration
	status     int // handshake status (0: accept)
	errCode    int // answer send_text with this status code
	errMsg     string
	stall      bool // never answer a send_text
	dropAfter  int  // close the connection after this many audio chunks (0: never)
	pcm        func(n int) []byte
	hsDelay    time.Duration // before the handshake is answered
	voices     []map[string]any

	chunkCount atomic.Int64
	sentBytes  atomic.Int64
}

func newInworldRig(t *testing.T, key string) *inworldRig {
	t.Helper()
	r := &inworldRig{t: t, key: key, chunks: 3, chunkBytes: 4800, cancelled: map[string]bool{},
		voices: []map[string]any{
			{"voiceId": "Ashley", "displayName": "Ashley", "languageCode": "en-US", "gender": "female", "description": "A warm, natural female voice", "source": "SYSTEM"},
			{"voiceId": "Dennis", "displayName": "Dennis", "languageCode": "en-US", "gender": "male", "description": "Calm", "source": "SYSTEM"},
			{"voiceId": "Odd", "displayName": "Odd", "languageCode": "es-MX", "gender": "", "description": "", "source": "SYSTEM"},
		}}
	mux := http.NewServeMux()
	mux.HandleFunc("/tts/v1/voice:streamBidirectional", r.handleWS)
	mux.HandleFunc("/voices/v1/voices", r.handleVoices)
	r.srv = httptest.NewServer(mux)
	prevWS, prevV := inworldWSURL, inworldVoicesURL
	inworldWSURL = "ws" + strings.TrimPrefix(r.srv.URL, "http") + "/tts/v1/voice:streamBidirectional"
	inworldVoicesURL = r.srv.URL + "/voices/v1/voices"
	t.Cleanup(func() { r.srv.CloseClientConnections(); r.srv.Close(); inworldWSURL, inworldVoicesURL = prevWS, prevV })
	return r
}

func (r *inworldRig) handleVoices(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Authorization") != "Basic "+r.key {
		http.Error(w, `{"code":16,"message":"bad key"}`, 401)
		return
	}
	r.mu.Lock()
	vs := r.voices
	r.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"voices": vs, "totalSize": len(vs), "nextPageToken": ""})
}

func (r *inworldRig) set(f func()) { r.mu.Lock(); f(); r.mu.Unlock() }

func (r *inworldRig) handleWS(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	st := r.status
	hd := r.hsDelay
	r.auths = append(r.auths, req.Header.Get("Authorization"))
	r.mu.Unlock()
	if hd > 0 {
		time.Sleep(hd)
	}
	if st != 0 {
		http.Error(w, "refused", st)
		return
	}
	if req.Header.Get("Authorization") != "Basic "+r.key {
		http.Error(w, "bad key", 401)
		return
	}
	c, err := websocket.Accept(w, req, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	r.mu.Lock()
	r.conns++
	r.mu.Unlock()
	ctx := req.Context()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		var cid string
		_ = json.Unmarshal(m["contextId"], &cid)
		switch {
		case m["create"] != nil:
			var cr map[string]any
			_ = json.Unmarshal(m["create"], &cr)
			cr["contextId"] = cid
			r.mu.Lock()
			r.creates = append(r.creates, cr)
			r.mu.Unlock()
			r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "contextCreated": map[string]any{}, "status": map[string]any{"code": 0}}})
		case m["send_text"] != nil:
			var st struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(m["send_text"], &st)
			r.mu.Lock()
			r.texts = append(r.texts, st.Text)
			r.mu.Unlock()
			go r.synth(ctx, c, cid)
		case m["cancel_context"] != nil:
			r.mu.Lock()
			r.cancels = append(r.cancels, cid)
			r.cancelled[cid] = true
			r.mu.Unlock()
			r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "contextClosed": map[string]any{}, "status": map[string]any{"code": 0}}})
		case m["close_context"] != nil:
			r.mu.Lock()
			r.closes = append(r.closes, cid)
			r.mu.Unlock()
			r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "contextClosed": map[string]any{}, "status": map[string]any{"code": 0}}})
		}
	}
}

func (r *inworldRig) reply(ctx context.Context, c *websocket.Conn, v any) bool {
	b, _ := json.Marshal(v)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if c.Write(wctx, websocket.MessageText, b) != nil {
		return false
	}
	r.sentBytes.Add(int64(len(b)))
	return true
}

func (r *inworldRig) isCancelled(cid string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled[cid]
}

func (r *inworldRig) synth(ctx context.Context, c *websocket.Conn, cid string) {
	r.mu.Lock()
	chunks, size, delay, stall, ec, em, drop := r.chunks, r.chunkBytes, r.chunkDelay, r.stall, r.errCode, r.errMsg, r.dropAfter
	r.mu.Unlock()
	if stall {
		return
	}
	if ec != 0 {
		r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "status": map[string]any{"code": ec, "message": em}}})
		return
	}
	pcm := bytes.Repeat([]byte{1, 0}, size/2)
	if r.pcm != nil {
		pcm = r.pcm(size)
	}
	b64 := base64.StdEncoding.EncodeToString(pcm)
	for i := 0; i < chunks; i++ {
		if r.isCancelled(cid) {
			return
		}
		if drop > 0 && int(r.chunkCount.Add(1)) > drop {
			c.CloseNow()
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		if !r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "audioChunk": map[string]any{"audioContent": b64,
			"usage": map[string]any{"processedCharactersCount": 0, "modelId": "m"}}, "status": map[string]any{"code": 0}}}) {
			return
		}
	}
	if r.isCancelled(cid) {
		return
	}
	r.reply(ctx, c, map[string]any{"result": map[string]any{"contextId": cid, "flushCompleted": map[string]any{}, "status": map[string]any{"code": 0}}})
}

func (r *inworldRig) cancelCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cancels)
}

// waitCancels waits for upstream to have been told of n cancelled contexts.
func (r *inworldRig) waitCancels(n int) int {
	deadline := time.Now().Add(3 * time.Second)
	for r.cancelCount() < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	return r.cancelCount()
}

func (r *inworldRig) snapshotTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

// --- the app side of a test ----------------------------------------------------

const ttsTestKey = "dGVzdC1rZXk6c2VjcmV0LXZhbHVl"

type ttsRig struct {
	*clientTest
	up    *inworldRig
	user  string
	token string
}

// newTTSTest sets the relay up against a stand-in with a configured engine.
func newTTSTest(t *testing.T, configure bool) *ttsRig {
	t.Helper()
	c := newClientTest(t)
	up := newInworldRig(t, ttsTestKey)
	prevSecret := clientTTSSecret
	clientTTSSecret = func(_ context.Context, ref string) (string, error) {
		if ref == "INWORLD_TEST_KEY" {
			return ttsTestKey + "\n", nil
		}
		return "", fmt.Errorf("%s is not set in lasso's environment", ref)
	}
	prevTune := tune()
	t.Cleanup(func() {
		clientTTSSecret = prevSecret
		setTune(func(u *ttsTuning) { *u = prevTune })
		resetTTSSecrets()
		ttsMarkWorking()
	})
	setTune(func(u *ttsTuning) { u.AuthEvery = 50 * time.Millisecond })
	ttsChars = &ttsCharWindow{hits: map[string][]ttsCharHit{}}
	resetTTSSecrets()
	ttsMarkWorking()
	if configure {
		setTTSConfig(t, `{"inworld":{"key_ref":"INWORLD_TEST_KEY","default_voice":"Ashley"}}`)
	}
	uid, tok := c.person("Ann", false, "jess")
	return &ttsRig{clientTest: c, up: up, user: uid, token: tok}
}

func setTTSConfig(t *testing.T, raw string) {
	t.Helper()
	if err := setSetting(clientTTSSettingKey, raw); err != nil {
		t.Fatal(err)
	}
	resetTTSSecrets()
}

type ttsConn struct {
	t   *testing.T
	ws  *websocket.Conn
	ctx context.Context
	ch  chan ttsRead
}

type ttsEvent struct {
	M   map[string]any
	PCM []byte
}

type ttsRead struct {
	ev  ttsEvent
	err error
}

func (r *ttsRig) dial(token string) (*ttsConn, *http.Response, error) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	r.t.Cleanup(cancel)
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.srv.URL, "http")+"/client/v1/tts/stream", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil, resp, err
	}
	ws.SetReadLimit(8 << 20)
	r.t.Cleanup(func() { ws.CloseNow() })
	c := &ttsConn{t: r.t, ws: ws, ctx: ctx, ch: make(chan ttsRead)}
	go c.pump()
	return c, resp, nil
}

// pump reads frames for ever (a read that times out would close the
// connection), pairing an audio header with the binary frame after it. The
// channel is unbuffered, so a test that is not receiving is a client that is
// not reading.
func (c *ttsConn) pump() {
	for {
		typ, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.ch <- ttsRead{err: err}
			return
		}
		if typ != websocket.MessageText {
			c.ch <- ttsRead{err: fmt.Errorf("a binary frame with no audio header before it")}
			return
		}
		ev := ttsEvent{}
		if err := json.Unmarshal(data, &ev.M); err != nil {
			c.ch <- ttsRead{err: fmt.Errorf("frame is not JSON: %s", data)}
			return
		}
		if ev.M["type"] == "audio" {
			typ, bin, err := c.ws.Read(context.Background())
			if err != nil || typ != websocket.MessageBinary {
				c.ch <- ttsRead{err: fmt.Errorf("audio header not followed by a binary frame: %v %v", typ, err)}
				return
			}
			if int(ev.M["bytes"].(float64)) != len(bin) {
				c.ch <- ttsRead{err: fmt.Errorf("audio header says %v bytes, frame has %d", ev.M["bytes"], len(bin))}
				return
			}
			ev.PCM = bin
		}
		select {
		case c.ch <- ttsRead{ev: ev}:
		case <-c.ctx.Done():
			return
		}
	}
}

func (r *ttsRig) open(extra string) *ttsConn {
	r.t.Helper()
	c, _, err := r.dial(r.token)
	if err != nil {
		r.t.Fatalf("dial: %v", err)
	}
	c.send(`{"type":"start","engine":"inworld","model":"tts-2-realtime","voice":"Ashley","sample_rate":24000` + extra + `}`)
	ev := c.recv(5 * time.Second)
	if ev.M["type"] != "ready" {
		r.t.Fatalf("expected ready, got %v", ev.M)
	}
	return c
}

func (c *ttsConn) send(s string) {
	c.t.Helper()
	if err := c.ws.Write(c.ctx, websocket.MessageText, []byte(s)); err != nil {
		c.t.Fatalf("send: %v", err)
	}
}

// recvErr reads the next event; the error is the connection's end (use
// websocket.CloseStatus) or a timeout.
func (c *ttsConn) recvErr(d time.Duration) (ttsEvent, error) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case rd := <-c.ch:
		if rd.err != nil {
			if websocket.CloseStatus(rd.err) == -1 && !strings.Contains(rd.err.Error(), "EOF") && !strings.Contains(rd.err.Error(), "closed") {
				c.t.Fatalf("%v", rd.err)
			}
			// the pump has gone: later receives see the same end
			go func() {
				for {
					c.ch <- rd
				}
			}()
		}
		return rd.ev, rd.err
	case <-t.C:
		return ttsEvent{}, context.DeadlineExceeded
	}
}

func (c *ttsConn) recv(d time.Duration) ttsEvent {
	c.t.Helper()
	ev, err := c.recvErr(d)
	if err != nil {
		c.t.Fatalf("recv: %v", err)
	}
	return ev
}

// until reads events until one satisfies f and answers all of them.
func (c *ttsConn) until(d time.Duration, f func(ttsEvent) bool) []ttsEvent {
	c.t.Helper()
	var out []ttsEvent
	deadline := time.Now().Add(d)
	for {
		ev := c.recv(time.Until(deadline))
		out = append(out, ev)
		if f(ev) {
			return out
		}
	}
}

func isType(typ, id string) func(ttsEvent) bool {
	return func(e ttsEvent) bool { return e.M["type"] == typ && (id == "" || e.M["id"] == id) }
}

func (c *ttsConn) closeStatus(d time.Duration) (websocket.StatusCode, string) {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for {
		_, err := c.recvErr(time.Until(deadline))
		if err != nil {
			return websocket.CloseStatus(err), err.Error()
		}
	}
}

// --- tests ---------------------------------------------------------------------

func TestTTSNotConfigured(t *testing.T) {
	r := newTTSTest(t, false)
	var out struct {
		Engines []map[string]any `json:"engines"`
	}
	if resp := r.get("/client/v1/tts", r.token, &out); resp.StatusCode != 200 || len(out.Engines) != 1 {
		t.Fatalf("GET /tts: %d %+v", resp.StatusCode, out)
	}
	e := out.Engines[0]
	if e["id"] != "inworld" || e["available"] != false || e["reason"] != "not_configured" {
		t.Fatalf("engine = %v", e)
	}
	if len(e["models"].([]any)) != 2 || len(e["voices"].([]any)) != 0 {
		t.Fatalf("engine = %v", e)
	}
	var me struct {
		Features []string `json:"features"`
	}
	r.get("/client/v1/me", r.token, &me)
	found := false
	for _, f := range me.Features {
		found = found || f == "tts"
	}
	if !found {
		t.Fatalf("features = %v", me.Features)
	}
	// The upgrade itself is refused, as plain HTTP with the client API's error.
	_, resp, err := r.dial(r.token)
	if err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("the stream with nothing configured: %v %v", resp, err)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `"code":"not_configured"`) {
		t.Fatalf("body = %s", b)
	}
	if r.up.conns != 0 || len(r.up.auths) != 0 {
		t.Fatal("lasso contacted the upstream with nothing configured")
	}
	// A reference nobody can read is not configured either.
	setTTSConfig(t, `{"inworld":{"key_ref":"MISSING_KEY_VAR"}}`)
	r.get("/client/v1/tts", r.token, &out)
	if out.Engines[0]["reason"] != "unreadable" || out.Engines[0]["available"] != false {
		t.Fatalf("engine = %v", out.Engines[0])
	}
	setTTSConfig(t, `{"inworld":{"key_ref":"INWORLD_TEST_KEY","enabled":false}}`)
	if _, resp, err := r.dial(r.token); err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("a disabled relay's stream: %v %v", resp, err)
	}
	r.get("/client/v1/tts", r.token, &out)
	if out.Engines[0]["reason"] != "not_configured" {
		t.Fatalf("a disabled engine reads %v", out.Engines[0]["reason"])
	}
}

func TestTTSCatalogShape(t *testing.T) {
	r := newTTSTest(t, true)
	resp, body := r.do("GET", "/client/v1/tts", r.token, "")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out struct {
		Engines []struct {
			ID           string
			Name         string
			Available    bool
			Reason       *string
			Models       []map[string]any
			Voices       []map[string]any
			DefaultVoice string `json:"default_voice"`
			SampleRate   int    `json:"sample_rate"`
			Format       string
			Usage        map[string]int
		}
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	e := out.Engines[0]
	if !e.Available || e.Reason != nil || e.Name != "Inworld" || e.DefaultVoice != "Ashley" || e.SampleRate != 24000 || e.Format != "pcm_s16le" {
		t.Fatalf("engine = %+v", e)
	}
	if e.Models[0]["id"] != "tts-2-realtime" || e.Models[0]["default"] != true || e.Models[1]["id"] != "tts-2-flash" || e.Models[1]["name"] != "TTS-2 Flash" {
		t.Fatalf("models = %v", e.Models)
	}
	if len(e.Voices) != 3 || e.Voices[0]["id"] != "Ashley" || e.Voices[0]["gender"] != "female" || e.Voices[0]["language"] != "en" ||
		e.Voices[0]["description"] != "A warm, natural female voice" || e.Voices[2]["gender"] != nil || e.Voices[2]["language"] != "es" {
		t.Fatalf("voices = %v", e.Voices)
	}
	if e.Usage["characters_this_month"] != 0 || e.Usage["limit_characters"] != ttsMonthlyPerPerson {
		t.Fatalf("usage = %v", e.Usage)
	}
	if strings.Contains(string(body), ttsTestKey) {
		t.Fatal("the key is in the answer")
	}
}

func TestTTSAuthAndRevocation(t *testing.T) {
	r := newTTSTest(t, true)
	if resp, _ := r.do("GET", "/client/v1/tts", "", ""); resp.StatusCode != 401 {
		t.Fatalf("no token: %d", resp.StatusCode)
	}
	if _, resp, err := r.dial("clt_nope"); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("a bad token reached the stream: %v %v", resp, err)
	}
	if _, resp, err := r.dial(""); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("no token reached the stream: %v %v", resp, err)
	}
	// The token travels in the Authorization header only: never the URL.
	resp, _ := r.do("GET", "/client/v1/tts/stream?token="+r.token, "", "")
	if resp.StatusCode != 401 {
		t.Fatalf("a token in the URL authenticated: %d", resp.StatusCode)
	}
	if resp, _ := r.do("GET", "/client/v1/tts/stream", r.token, ""); resp.StatusCode != 426 {
		t.Fatalf("a plain GET: %d", resp.StatusCode)
	}
	if resp, _ := r.do("POST", "/client/v1/tts", r.token, ""); resp.StatusCode != 405 {
		t.Fatalf("POST /tts: %d", resp.StatusCode)
	}
	// An open stream dies when its device is revoked.
	c := r.open("")
	ds, _ := listAppDevices(r.user)
	if len(ds) != 1 {
		t.Fatalf("devices = %d", len(ds))
	}
	if err := revokeAppDevice(r.user, ds[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, _ := c.closeStatus(5 * time.Second)
	if code != websocket.StatusPolicyViolation {
		t.Fatalf("close code after revocation = %d", code)
	}
	if resp, _ := r.do("GET", "/client/v1/tts", r.token, ""); resp.StatusCode != 401 {
		t.Fatalf("a revoked device: %d", resp.StatusCode)
	}
}

func TestTTSSentencesInOrder(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.chunks = 3 })
	c := r.open("")
	c.send(`{"type":"text","id":"s1","text":"One sentence."}`)
	c.send(`{"type":"text","id":"s2","text":"Two sentences, now."}`)
	c.send(`{"type":"text","id":"s3","text":"Three."}`)
	c.send(`{"type":"end"}`)
	var evs []ttsEvent
	for {
		ev, err := c.recvErr(5 * time.Second)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("closed with %v", err)
			}
			break
		}
		evs = append(evs, ev)
	}
	var order []string
	seqs := map[string]int{}
	for _, e := range evs {
		id, _ := e.M["id"].(string)
		switch e.M["type"] {
		case "audio":
			if int(e.M["seq"].(float64)) != seqs[id] {
				t.Fatalf("seq for %s = %v, want %d", id, e.M["seq"], seqs[id])
			}
			seqs[id]++
			if len(e.PCM) != 4800 || len(e.PCM)%2 != 0 {
				t.Fatalf("pcm = %d bytes", len(e.PCM))
			}
			order = append(order, id+":a")
		case "done":
			order = append(order, id+":done")
			if int(e.M["audio_ms"].(float64)) != 3*4800/2*1000/24000 {
				t.Fatalf("done = %v", e.M)
			}
		default:
			t.Fatalf("unexpected frame %v", e.M)
		}
	}
	want := "s1:a s1:a s1:a s1:done s2:a s2:a s2:a s2:done s3:a s3:a s3:a s3:done"
	if strings.Join(order, " ") != want {
		t.Fatalf("order = %s", strings.Join(order, " "))
	}
	for _, e := range evs {
		if e.M["type"] == "done" && e.M["id"] == "s2" && int(e.M["characters"].(float64)) != len("Two sentences, now.") {
			t.Fatalf("done = %v", e.M)
		}
	}
	// What lasso asked upstream for.
	cr := r.up.creates[0]
	ac := cr["audioConfig"].(map[string]any)
	if cr["voiceId"] != "Ashley" || cr["modelId"] != "inworld-tts-2" || ac["audioEncoding"] != "PCM" || ac["sampleRateHertz"].(float64) != 24000 || cr["autoMode"] != false {
		t.Fatalf("create = %v", cr)
	}
	if got := r.up.snapshotTexts(); strings.Join(got, "|") != "One sentence.|Two sentences, now.|Three." {
		t.Fatalf("texts = %v", got)
	}
	if r.up.conns != 1 {
		t.Fatalf("upstream connections = %d", r.up.conns)
	}
	if r.up.auths[0] != "Basic "+ttsTestKey {
		t.Fatal("the upstream was not given the key the reference named, trimmed")
	}
	if used, _ := ttsUsed(r.user); used != len("One sentence.")+len("Two sentences, now.")+len("Three.") {
		t.Fatalf("usage = %d", used)
	}
}

func TestTTSReadyFrameAndFlash(t *testing.T) {
	r := newTTSTest(t, true)
	c, _, err := r.dial(r.token)
	if err != nil {
		t.Fatal(err)
	}
	c.send(`{"type":"start","engine":"inworld","model":"tts-2-flash","voice":"Dennis","sample_rate":48000}`)
	ev := c.recv(5 * time.Second)
	want := map[string]any{"type": "ready", "sample_rate": 48000.0, "format": "pcm_s16le", "channels": 1.0, "model": "tts-2-flash", "voice": "Dennis"}
	for k, v := range want {
		if ev.M[k] != v {
			t.Fatalf("ready = %v", ev.M)
		}
	}
	c.send(`{"type":"text","id":"a","text":"Hi there."}`)
	c.until(5*time.Second, isType("done", "a"))
	if m := r.up.creates[0]["modelId"]; m != "inworld-tts-2-flash" {
		t.Fatalf("model = %v", m)
	}
}

func TestTTSStartIsValidated(t *testing.T) {
	r := newTTSTest(t, true)
	for name, start := range map[string]string{
		"engine":      `{"type":"start","engine":"elevenlabs","voice":"Ashley"}`,
		"model":       `{"type":"start","engine":"inworld","model":"gpt","voice":"Ashley"}`,
		"voice":       `{"type":"start","engine":"inworld","voice":"NotOffered"}`,
		"voice abuse": `{"type":"start","engine":"inworld","voice":"../../x"}`,
		"rate":        `{"type":"start","engine":"inworld","voice":"Ashley","sample_rate":9}`,
		"not start":   `{"type":"text","id":"a","text":"hi"}`,
		"junk":        `nope`,
	} {
		c, _, err := r.dial(r.token)
		if err != nil {
			t.Fatal(err)
		}
		c.send(start)
		ev := c.recv(5 * time.Second)
		if ev.M["type"] != "error" || ev.M["code"] != "invalid_request" || ev.M["fatal"] != true {
			t.Fatalf("%s: %v", name, ev.M)
		}
		c.closeStatus(3 * time.Second)
	}
	if r.up.conns != 0 {
		t.Fatalf("a bad start opened %d upstream connections", r.up.conns)
	}
	// Defaults: model and voice from the settings.
	c, _, _ := r.dial(r.token)
	c.send(`{"type":"start","engine":"inworld"}`)
	ev := c.recv(5 * time.Second)
	if ev.M["model"] != "tts-2-realtime" || ev.M["voice"] != "Ashley" || ev.M["sample_rate"] != 24000.0 {
		t.Fatalf("defaults = %v", ev.M)
	}
}

func TestTTSTextValidation(t *testing.T) {
	r := newTTSTest(t, true)
	c := r.open("")
	expect := func(frame, code, id string) {
		t.Helper()
		c.send(frame)
		ev := c.recv(5 * time.Second)
		if ev.M["type"] != "error" || ev.M["code"] != code || ev.M["fatal"] != false || (id == "" && ev.M["id"] != nil) || (id != "" && ev.M["id"] != id) {
			t.Fatalf("%s -> %v", frame, ev.M)
		}
	}
	expect(`{"type":"text","id":"big","text":"`+strings.Repeat("a", ttsMaxTextChars+1)+`"}`, "text_too_long", "big")
	expect(`{"type":"text","id":"e","text":"  \n\t "}`, "invalid_request", "e")
	expect(`{"type":"text","id":"bad id","text":"hi"}`, "invalid_request", "")
	expect(`{"type":"text","text":"hi"}`, "invalid_request", "")
	expect(`{"type":"nope"}`, "invalid_request", "")
	// Not UTF-8 on the wire.
	if err := c.ws.Write(c.ctx, websocket.MessageText, []byte("{\"type\":\"text\",\"id\":\"u\",\"text\":\"a\xffb\"}")); err != nil {
		t.Fatal(err)
	}
	if ev := c.recv(5 * time.Second); ev.M["code"] != "invalid_request" {
		t.Fatalf("invalid utf-8: %v", ev.M)
	}
	// Control characters and invisible formatting are stripped before forwarding.
	c.send(`{"type":"text","id":"ok","text":"  Hel\u0000lo‮,​\n\nwor\u0007ld.\u0000 "}`)
	c.until(5*time.Second, isType("done", "ok"))
	if got := r.up.snapshotTexts(); len(got) != 1 || got[0] != "Hello, world." {
		t.Fatalf("forwarded %q", got)
	}
	// An id is used once.
	expect(`{"type":"text","id":"ok","text":"again"}`, "invalid_request", "ok")
	// A frame that is not text.
	if err := c.ws.Write(c.ctx, websocket.MessageBinary, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	c.until(3*time.Second, isType("error", ""))
}

func TestTTSCleanText(t *testing.T) {
	for in, want := range map[string]string{
		"plain":               "plain",
		"a\r\nb":              "a b",
		"  \t x  y  ":         "x y",
		"zero​width":          "zerowidth",
		"bidi‮flip":           "bidiflip",
		"nul\x00byte":         "nulbyte",
		"tag [whisper] stays": "tag [whisper] stays",
		"emoji \U0001F600 ok": "emoji \U0001F600 ok",
		"nbsp here":           "nbsp here",
		"line sep":            "line sep",
		"\ufeffbom":           "bom",
	} {
		if got, ok := ttsCleanText(in); !ok || got != want {
			t.Errorf("ttsCleanText(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " \n ", "\x00​", "a\xffb", "\xc3"} {
		if _, ok := ttsCleanText(in); ok {
			t.Errorf("ttsCleanText(%q) accepted", in)
		}
	}
	if ttsCountChars("ab\U0001F600") != 4 {
		t.Fatal("a character outside the BMP counts two")
	}
}

func TestTTSCancelOneAndAll(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.chunks = 40; r.up.chunkDelay = 20 * time.Millisecond })
	c := r.open("")
	c.send(`{"type":"text","id":"s1","text":"First long one."}`)
	c.send(`{"type":"text","id":"s2","text":"Second."}`)
	c.send(`{"type":"text","id":"s3","text":"Third."}`)
	c.until(5*time.Second, isType("audio", "s1"))
	c.send(`{"type":"cancel","id":"s1"}`)
	evs := c.until(10*time.Second, isType("audio", "s2"))
	// Frames for s1 may precede the cancel; none follow the first s2 frame, and
	// s1 never completes.
	for _, e := range evs {
		if e.M["id"] == "s1" && e.M["type"] != "audio" {
			t.Fatalf("a %v for a cancelled sentence", e.M["type"])
		}
	}
	if r.up.waitCancels(1) < 1 {
		t.Fatal("the cancel did not reach upstream")
	}
	// Cancel everything: s2 is in flight, s3 queued upstream or not.
	c.send(`{"type":"cancel"}`)
	c.send(`{"type":"text","id":"s4","text":"Fourth."}`)
	r.up.set(func() { r.up.chunks = 2; r.up.chunkDelay = 0 })
	evs = c.until(10*time.Second, isType("done", "s4"))
	seen4 := false
	for _, e := range evs {
		id, _ := e.M["id"].(string)
		if id == "s4" {
			seen4 = true
			continue
		}
		if seen4 {
			t.Fatalf("a frame for %q after the new sentence began", id)
		}
		if id == "s3" || (id == "s2" && e.M["type"] != "audio") {
			t.Fatalf("a %v for a cancelled sentence %s", e.M["type"], id)
		}
	}
	// And nothing late arrives afterwards.
	if ev, err := c.recvErr(400 * time.Millisecond); err == nil {
		t.Fatalf("late frame %v", ev.M)
	}
	if n := r.up.waitCancels(2); n < 2 {
		t.Fatalf("upstream cancels = %d", n)
	}
	// Cancelling an id that is gone, or never was, is not an error.
	c.send(`{"type":"cancel","id":"s1"}`)
	c.send(`{"type":"cancel","id":"nothing"}`)
	c.send(`{"type":"text","id":"s5","text":"Fifth."}`)
	c.until(5*time.Second, isType("done", "s5"))
}

func TestTTSEndFinishesQueuedThenCloses(t *testing.T) {
	r := newTTSTest(t, true)
	c := r.open("")
	c.send(`{"type":"text","id":"s1","text":"One."}`)
	c.send(`{"type":"end"}`)
	c.send(`{"type":"text","id":"s2","text":"Too late."}`)
	var done1, errs int
	for {
		ev, err := c.recvErr(5 * time.Second)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatal(err)
			}
			break
		}
		if ev.M["type"] == "done" && ev.M["id"] == "s1" {
			done1++
		}
		if ev.M["type"] == "error" && ev.M["id"] == "s2" {
			errs++
		}
	}
	if done1 != 1 || errs != 1 {
		t.Fatalf("done=%d errors for the late sentence=%d", done1, errs)
	}
	if len(r.up.snapshotTexts()) != 1 {
		t.Fatalf("texts = %v", r.up.snapshotTexts())
	}
}

func TestTTSUpstreamErrorsAndRecovery(t *testing.T) {
	r := newTTSTest(t, true)
	c := r.open("")
	for code, want := range map[int]string{8: "rate_limited", 3: "invalid_request", 4: "upstream_timeout", 13: "upstream_unavailable"} {
		r.up.set(func() { r.up.errCode = code; r.up.errMsg = "something with Secret text" })
		id := fmt.Sprintf("e%d", code)
		c.send(`{"type":"text","id":"` + id + `","text":"Fails."}`)
		ev := c.recv(5 * time.Second)
		if ev.M["type"] != "error" || ev.M["code"] != want || ev.M["id"] != id || ev.M["fatal"] != false {
			t.Fatalf("status %d -> %v", code, ev.M)
		}
		if strings.Contains(fmt.Sprint(ev.M["message"]), "Secret") {
			t.Fatal("the upstream's message reached the app")
		}
	}
	// A quota message is told apart from a busy service.
	r.up.set(func() { r.up.errCode = 8; r.up.errMsg = "insufficient credits" })
	c.send(`{"type":"text","id":"q","text":"Fails."}`)
	ev := c.recv(5 * time.Second)
	if ev.M["code"] != "quota_exceeded" || ev.M["fatal"] != true {
		t.Fatalf("quota -> %v", ev.M)
	}
	c.closeStatus(3 * time.Second)
	// A new stream works once the upstream does.
	r.up.set(func() { r.up.errCode = 0 })
	c = r.open("")
	c.send(`{"type":"text","id":"ok","text":"Works."}`)
	c.until(5*time.Second, isType("done", "ok"))
}

func TestTTSRefusedKeyIsFailing(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.status = 401 })
	c, _, _ := r.dial(r.token)
	c.send(`{"type":"start","engine":"inworld"}`)
	ev := c.recv(5 * time.Second)
	if ev.M["code"] != "upstream_unavailable" || ev.M["fatal"] != true {
		t.Fatalf("refused key -> %v", ev.M)
	}
	var out struct {
		Engines []map[string]any
	}
	r.get("/client/v1/tts", r.token, &out)
	if out.Engines[0]["available"] != false || out.Engines[0]["reason"] != "failing" {
		t.Fatalf("engine = %v", out.Engines[0])
	}
	// While it is failing a new stream is turned away without a call upstream.
	n := len(r.up.auths)
	c, _, _ = r.dial(r.token)
	if ev := c.recv(3 * time.Second); ev.M["code"] != "upstream_unavailable" {
		t.Fatalf("%v", ev.M)
	}
	if len(r.up.auths) != n {
		t.Fatal("a failing engine kept calling upstream")
	}
	// Failing wears off, and a working call clears it.
	r.up.set(func() { r.up.status = 0 })
	ttsMarkWorking()
	r.get("/client/v1/tts", r.token, &out)
	if out.Engines[0]["available"] != true {
		t.Fatalf("engine = %v", out.Engines[0])
	}
}

func TestTTSTimeoutThenRecovery(t *testing.T) {
	r := newTTSTest(t, true)
	setTune(func(u *ttsTuning) { u.FirstAudio = 300 * time.Millisecond })
	c := r.open("")
	r.up.set(func() { r.up.stall = true })
	c.send(`{"type":"text","id":"slow","text":"Never answered."}`)
	ev := c.recv(5 * time.Second)
	if ev.M["code"] != "upstream_timeout" || ev.M["id"] != "slow" || ev.M["fatal"] != false {
		t.Fatalf("timeout -> %v", ev.M)
	}
	if n := r.up.waitCancels(1); n != 1 {
		t.Fatalf("the timed out sentence was not cancelled upstream: %d", n)
	}
	r.up.set(func() { r.up.stall = false })
	c.send(`{"type":"text","id":"next","text":"Fine now."}`)
	c.until(5*time.Second, isType("done", "next"))
	// A stall after audio began is a timeout too.
	setTune(func(u *ttsTuning) { u.ChunkStall = 300 * time.Millisecond })
	r.up.set(func() { r.up.chunks = 5; r.up.chunkDelay = 0; r.up.dropAfter = 0 })
	r.up.set(func() { r.up.chunkDelay = 700 * time.Millisecond })
	c.send(`{"type":"text","id":"mid","text":"Stalls part way."}`)
	evs := c.until(5*time.Second, isType("error", "mid"))
	if evs[len(evs)-1].M["code"] != "upstream_timeout" {
		t.Fatalf("%v", evs[len(evs)-1].M)
	}
}

func TestTTSUpstreamDropsMidStream(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.chunks = 5; r.up.dropAfter = 2 })
	c := r.open("")
	c.send(`{"type":"text","id":"cut","text":"Cut off."}`)
	evs := c.until(5*time.Second, isType("error", "cut"))
	last := evs[len(evs)-1].M
	if last["code"] != "upstream_unavailable" || last["fatal"] != false {
		t.Fatalf("drop -> %v", last)
	}
	// The next sentence reconnects.
	r.up.set(func() { r.up.dropAfter = 0 })
	c.send(`{"type":"text","id":"again","text":"Back."}`)
	c.until(5*time.Second, isType("done", "again"))
	if r.up.conns != 2 {
		t.Fatalf("upstream connections = %d, want a reconnect", r.up.conns)
	}
}

func TestTTSBackPressure(t *testing.T) {
	r := newTTSTest(t, true)
	setTune(func(u *ttsTuning) { u.QueueBytes = 256 << 10 })
	const chunk, n = 32 << 10, 2000 // 62 MiB
	r.up.set(func() { r.up.chunks = n; r.up.chunkBytes = chunk })
	c := r.open("")
	c.send(`{"type":"text","id":"big","text":"A very long answer."}`)
	// The app reads nothing for a while: upstream is held back, not buffered.
	time.Sleep(1500 * time.Millisecond)
	if sent := r.up.sentBytes.Load(); sent > 48<<20 {
		t.Fatalf("lasso let %d MiB through while the app read nothing", sent>>20)
	} else if sent == 0 {
		t.Fatal("nothing was sent upstream")
	}
	got := 0
	for _, e := range c.until(30*time.Second, isType("done", "big")) {
		got += len(e.PCM)
	}
	if got != n*chunk {
		t.Fatalf("the app got %d bytes, want %d", got, n*chunk)
	}
}

func TestTTSMonthlyQuotaAndRollover(t *testing.T) {
	r := newTTSTest(t, true)
	setTTSConfig(t, `{"inworld":{"key_ref":"INWORLD_TEST_KEY","default_voice":"Ashley","monthly_character_limit":26}}`)
	var clk struct {
		sync.Mutex
		t time.Time
	}
	clk.t = time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC)
	setTune(func(u *ttsTuning) { u.Now = func() time.Time { clk.Lock(); defer clk.Unlock(); return clk.t } })
	c := r.open("")
	c.send(`{"type":"text","id":"a","text":"Thirteen chars"}`) // 14
	c.until(5*time.Second, isType("done", "a"))
	c.send(`{"type":"text","id":"b","text":"Twelve chars"}`)
	c.until(5*time.Second, isType("done", "b"))
	used, _ := ttsUsed(r.user)
	if used != 26 {
		t.Fatalf("used = %d", used)
	}
	c.send(`{"type":"text","id":"c","text":"x"}`)
	ev := c.recv(5 * time.Second)
	if ev.M["code"] != "quota_exceeded" || ev.M["id"] != "c" || ev.M["fatal"] != true {
		t.Fatalf("over quota -> %v", ev.M)
	}
	c.closeStatus(3 * time.Second)
	if n := len(r.up.snapshotTexts()); n != 2 {
		t.Fatalf("the over-quota sentence was sent upstream (%d texts)", n)
	}
	var out struct {
		Engines []struct {
			Available bool
			Reason    *string
			Usage     map[string]int
		}
	}
	r.get("/client/v1/tts", r.token, &out)
	e := out.Engines[0]
	if e.Available || e.Reason == nil || *e.Reason != "quota_exceeded" || e.Usage["characters_this_month"] != 26 || e.Usage["limit_characters"] != 26 {
		t.Fatalf("engine = %+v", e)
	}
	// A new stream is refused up front.
	c2, _, _ := r.dial(r.token)
	if ev := c2.recv(3 * time.Second); ev.M["code"] != "quota_exceeded" {
		t.Fatalf("%v", ev.M)
	}
	// The next month starts at zero.
	clk.Lock()
	clk.t = time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	clk.Unlock()
	r.get("/client/v1/tts", r.token, &out)
	e = out.Engines[0]
	if !e.Available || e.Usage["characters_this_month"] != 0 {
		t.Fatalf("after rollover: %+v", e)
	}
	c = r.open("")
	c.send(`{"type":"text","id":"d","text":"A new month."}`)
	c.until(5*time.Second, isType("done", "d"))
	if used, _ := ttsUsed(r.user); used != len("A new month.") {
		t.Fatalf("used = %d", used)
	}
	var rows int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_tts_usage WHERE user_id = ?`, r.user).Scan(&rows)
	if rows != 2 {
		t.Fatalf("usage rows = %d, want one per month", rows)
	}
}

func TestTTSTotalCapAcrossPeople(t *testing.T) {
	r := newTTSTest(t, true)
	setTTSConfig(t, `{"inworld":{"key_ref":"INWORLD_TEST_KEY","default_voice":"Ashley","monthly_character_limit":1000,"total_monthly_character_limit":20}}`)
	_, bobTok := r.person("Bob", false, "jess")
	c := r.open("")
	c.send(`{"type":"text","id":"a","text":"Twelve chars"}`)
	c.until(5*time.Second, isType("done", "a"))
	b, _, _ := r.dial(bobTok)
	b.send(`{"type":"start","engine":"inworld"}`)
	if ev := b.recv(5 * time.Second); ev.M["type"] != "ready" {
		t.Fatalf("%v", ev.M)
	}
	b.send(`{"type":"text","id":"b","text":"Twelve chars"}`)
	ev := b.recv(5 * time.Second)
	if ev.M["code"] != "quota_exceeded" {
		t.Fatalf("the total did not cap the second person: %v", ev.M)
	}
	// Characters actually sent are what counts, and nothing is lost on a refusal.
	var total int
	_ = db.QueryRow(`SELECT SUM(characters) FROM app_tts_usage`).Scan(&total)
	if total != 12 {
		t.Fatalf("total = %d", total)
	}
}

func TestTTSRateLimits(t *testing.T) {
	r := newTTSTest(t, true)
	setTune(func(u *ttsTuning) { u.CharsPerMin = 20 })
	c := r.open("")
	c.send(`{"type":"text","id":"a","text":"Twelve chars"}`)
	c.until(5*time.Second, isType("done", "a"))
	c.send(`{"type":"text","id":"b","text":"Twelve chars"}`)
	ev := c.recv(5 * time.Second)
	if ev.M["code"] != "rate_limited" || ev.M["id"] != "b" || ev.M["fatal"] != false {
		t.Fatalf("chars/min -> %v", ev.M)
	}
	if n := len(r.up.snapshotTexts()); n != 1 {
		t.Fatalf("a rate limited sentence went upstream (%d)", n)
	}
	setTune(func(u *ttsTuning) { u.CharsPerMin = 12000 })
	setTune(func(u *ttsTuning) { u.SentencesPerMin = 2 })
	clientLimits = newClientWindow()
	c.send(`{"type":"text","id":"c","text":"One"}`)
	c.send(`{"type":"text","id":"d","text":"Two"}`)
	c.send(`{"type":"text","id":"e","text":"Three"}`)
	evs := c.until(5*time.Second, isType("error", "e"))
	if evs[len(evs)-1].M["code"] != "rate_limited" {
		t.Fatalf("%v", evs[len(evs)-1].M)
	}
}

func TestTTSStreamConcurrency(t *testing.T) {
	r := newTTSTest(t, true)
	setTune(func(u *ttsTuning) { u.PerDevice = 2 })
	a, b := r.open(""), r.open("")
	_, resp, err := r.dial(r.token)
	if err == nil || resp == nil || resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("third stream: %v %v", resp, err)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `"code":"rate_limited"`) {
		t.Fatalf("body = %s", b)
	}
	a.ws.Close(websocket.StatusNormalClosure, "")
	b.ws.Close(websocket.StatusNormalClosure, "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		ttsStreams.Lock()
		n := ttsStreams.total
		ttsStreams.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d streams still counted after close", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTTSIdleStreamCloses(t *testing.T) {
	r := newTTSTest(t, true)
	setTune(func(u *ttsTuning) { u.Idle = 300 * time.Millisecond })
	c := r.open("")
	code, _ := c.closeStatus(5 * time.Second)
	if code != websocket.StatusNormalClosure {
		t.Fatalf("idle close = %d", code)
	}
}

func TestTTSNoSecretOrTextInLogs(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return buf.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	r := newTTSTest(t, true)
	c := r.open("")
	c.send(`{"type":"text","id":"s1","text":"The password is hunter2 zebra."}`)
	c.until(5*time.Second, isType("done", "s1"))
	r.up.set(func() { r.up.errCode = 13; r.up.errMsg = "echo hunter2" })
	c.send(`{"type":"text","id":"s2","text":"Another zebra secret."}`)
	c.until(5*time.Second, isType("error", "s2"))
	c.send(`{"type":"end"}`)
	c.closeStatus(5 * time.Second)
	// the owner's views
	for _, p := range []string{"/client/v1/tts"} {
		_, body := r.do("GET", p, r.token, "")
		if strings.Contains(string(body), ttsTestKey) {
			t.Fatal("the key is in " + p)
		}
	}
	rec := httptest.NewRecorder()
	serveClientTTSConfig(rec, httptest.NewRequest("GET", "/api/client-tts", nil))
	if strings.Contains(rec.Body.String(), ttsTestKey) || !strings.Contains(rec.Body.String(), "INWORLD_TEST_KEY") {
		t.Fatalf("owner view: %s", rec.Body.String())
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	logs := buf.String()
	mu.Unlock()
	for _, bad := range []string{"hunter2", "zebra", ttsTestKey, "password"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("%q in the logs:\n%s", bad, logs)
		}
	}
	if !strings.Contains(logs, "tts:") {
		t.Fatal("nothing was logged at all")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestTTSSettingsAreOwnerOnly(t *testing.T) {
	r := newTTSTest(t, false)
	bot := mcpAnonSession(t)
	msg := callTool(t, bot, "update_settings", map[string]any{"client_tts": map[string]any{"inworld": map[string]any{"key_ref": "SOME_VAR"}}}, &struct{}{})
	if !strings.Contains(msg, "real MCP credential") {
		t.Fatalf("an anonymous caller: %q", msg)
	}
	if c := loadClientTTSConfig(); c.Inworld.KeyRef != "" {
		t.Fatalf("stored: %+v", c)
	}
	// A bot may read the section, never a person's usage.
	_, _ = r.up, r.token
	var got struct {
		ClientTTS map[string]any `json:"client_tts"`
	}
	if msg := callTool(t, bot, "get_settings", map[string]any{"section": "client_tts"}, &got); msg != "" {
		t.Fatalf("get_settings: %s", msg)
	}
	if got.ClientTTS == nil || strings.Contains(fmt.Sprint(got.ClientTTS), "people") {
		t.Fatalf("section = %v", got.ClientTTS)
	}
	enableOAuth(t, "")
	fleet := mcpTestSession(t, hostClientToken(t, "local", scopeFleet))
	if msg := callTool(t, fleet, "update_settings", map[string]any{"client_tts": map[string]any{"inworld": map[string]any{"key_ref": "SOME_VAR", "monthly_character_limit": 1234}}}, &struct{}{}); msg != "" {
		t.Fatalf("a fleet credential was refused: %s", msg)
	}
	if c := loadClientTTSConfig(); c.Inworld.KeyRef != "SOME_VAR" || c.Inworld.MonthlyCharacterLimit != 1234 {
		t.Fatalf("stored: %+v", c)
	}
}

func TestTTSOwnerAPI(t *testing.T) {
	r := newTTSTest(t, false)
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		serveClientTTSConfig(rec, httptest.NewRequest("PUT", "/api/client-tts", strings.NewReader(body)))
		return rec
	}
	for _, bad := range []string{
		`{"inworld":{"key_ref":"has space"}}`,
		`{"inworld":{"key_ref":"op://only/two"}}`,
		`{"inworld":{"default_model":"gpt-4"}}`,
		`{"inworld":{"default_voice":"../x"}}`,
		`{"inworld":{"monthly_character_limit":-1}}`,
		`{"inworld":{"monthly_character_limit":"many"}}`,
		`{"inworld":{"base_url":"https://evil.example"}}`,
		`{"inworld":{"ws_url":"wss://evil.example"}}`,
		`{"inworld":{"enabled":"yes"}}`,
	} {
		if rec := put(bad); rec.Code != 400 {
			t.Errorf("%s -> %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if rec := put(`{"inworld":{"key_ref":"INWORLD_TEST_KEY","default_voice":"Dennis","default_model":"tts-2-flash","monthly_character_limit":1000}}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	// merge-patch: another field keeps the rest; null clears.
	if rec := put(`{"inworld":{"enabled":false}}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	c := loadClientTTSConfig().Inworld
	if c.KeyRef != "INWORLD_TEST_KEY" || c.DefaultVoice != "Dennis" || c.enabled() || c.MonthlyCharacterLimit != 1000 {
		t.Fatalf("config = %+v", c)
	}
	put(`{"inworld":{"enabled":null,"default_voice":null,"monthly_character_limit":null}}`)
	c = loadClientTTSConfig().Inworld
	if !c.enabled() || c.DefaultVoice != "" || c.personLimit() != ttsMonthlyPerPerson {
		t.Fatalf("config = %+v", c)
	}
	// The test route speaks one fixed phrase and counts it.
	rec := httptest.NewRecorder()
	serveClientTTSConfig(rec, httptest.NewRequest("POST", "/api/client-tts/test", strings.NewReader(`{"voice":"Ashley"}`)))
	var res map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res["ok"] != true || res["characters"] != float64(len(ttsTestPhrase)) || res["first_audio_ms"] == nil || res["audio_bytes"] != 3*4800.0 {
		t.Fatalf("test route: %d %s", rec.Code, rec.Body.String())
	}
	if texts := r.up.snapshotTexts(); len(texts) != 1 || texts[0] != ttsTestPhrase {
		t.Fatalf("texts = %v", texts)
	}
	// The owner's view lists usage per person.
	rec = httptest.NewRecorder()
	serveClientTTSConfig(rec, httptest.NewRequest("GET", "/api/client-tts", nil))
	if !strings.Contains(rec.Body.String(), ttsOwnerTestUser) || !strings.Contains(rec.Body.String(), `"people"`) {
		t.Fatalf("view: %s", rec.Body.String())
	}
	// A refused key reads as a failure, not a panic.
	r.up.set(func() { r.up.status = 401 })
	rec = httptest.NewRecorder()
	serveClientTTSConfig(rec, httptest.NewRequest("POST", "/api/client-tts/test", nil))
	res = nil
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res["ok"] != false || res["code"] != "upstream_unavailable" {
		t.Fatalf("refused key: %s", rec.Body.String())
	}
}

func TestTTSSchemaIsIdempotentAndUsageSurvivesDelete(t *testing.T) {
	r := newTTSTest(t, true)
	if _, err := db.Exec(clientTTSSchema); err != nil {
		t.Fatal(err)
	}
	cfg := loadClientTTSConfig().Inworld
	if err := ttsReserve(r.user, 40, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(clientTTSSchema); err != nil {
		t.Fatal(err)
	}
	if u, _ := ttsUsed(r.user); u != 40 {
		t.Fatalf("used = %d", u)
	}
	// Reopening the database runs the migration again over existing data.
	dir := t.TempDir()
	t.Setenv("LASSO_DIR", dir)
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO app_tts_usage (user_id, month, engine, characters) VALUES ('u', '2026-11', 'inworld', 5)`); err != nil {
		t.Fatal(err)
	}
	closeTestDB()
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = db.QueryRow(`SELECT characters FROM app_tts_usage WHERE user_id = 'u'`).Scan(&n)
	if n != 5 {
		t.Fatalf("the row did not survive a reopen: %d", n)
	}
	closeTestDB()
}

func TestTTSDeletedPersonKeepsTheBillCounted(t *testing.T) {
	r := newTTSTest(t, true)
	cfg := loadClientTTSConfig().Inworld
	if err := ttsReserve(r.user, 40, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteAppUser(r.user); err != nil {
		t.Fatal(err)
	}
	var mine, folded, total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_tts_usage WHERE user_id = ?`, r.user).Scan(&mine)
	_, total = ttsUsed("")
	_ = db.QueryRow(`SELECT characters FROM app_tts_usage WHERE user_id = ?`, ttsDeletedUser).Scan(&folded)
	if mine != 0 || folded != 40 || total != 40 {
		t.Fatalf("rows=%d folded=%d total=%d", mine, folded, total)
	}
}

func TestTTSNoGoroutinesWhenUnconfigured(t *testing.T) {
	r := newTTSTest(t, false)
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		var out map[string]any
		r.get("/client/v1/tts", r.token, &out)
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("goroutines %d -> %d with nothing configured", before, after)
	}
}

// scripts/tts-live-check.py is what the coordinator runs against the real
// service; here it runs against the stand-in (with a tone for audio), to prove
// the script itself works. It needs uv and its packages, so it is opt-in:
// LASSO_TTS_SCRIPT=1.
func TestTTSLiveCheckScript(t *testing.T) {
	if os.Getenv("LASSO_TTS_SCRIPT") == "" {
		t.Skip("set LASSO_TTS_SCRIPT=1 to run scripts/tts-live-check.py against the stand-in")
	}
	r := newTTSTest(t, true)
	r.up.set(func() {
		r.up.chunks = 40
		r.up.pcm = func(n int) []byte {
			b := make([]byte, n)
			for i := 0; i < n/2; i++ {
				v := int16(3000 * math.Sin(float64(i)/20))
				b[2*i], b[2*i+1] = byte(v), byte(v>>8)
			}
			return b
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/app-users", serveAppUsers)
	mux.HandleFunc("/api/app-users/", serveAppUsers)
	mux.HandleFunc("/api/client-tts", serveClientTTSConfig)
	mux.HandleFunc("/client/", serveClient)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "out.wav")
	cmd := exec.Command("uv", "run", "--script", "../scripts/tts-live-check.py", "--url", srv.URL, "--out", out)
	b, err := cmd.CombinedOutput()
	t.Logf("%s", b)
	if err != nil {
		t.Fatalf("the script failed: %v", err)
	}
	if strings.Contains(string(b), ttsTestKey) {
		t.Fatal("the key is in the script's output")
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
		t.Fatalf("wav: %v", err)
	}
}

// The app waits 1.8 s for first audio and drops a socket quiet for 2.5 s: while
// a sentence is outstanding and nothing else is sent, a progress frame goes at
// least every second, and upstream silence ends in an upstream_timeout frame
// before the app's own deadline.
func TestTTSKeepAliveAndEarlyTimeout(t *testing.T) {
	r := newTTSTest(t, true)
	if tune().FirstAudio >= 1800*time.Millisecond {
		t.Fatalf("first-audio timeout %s is not shorter than the app's 1.8 s", tune().FirstAudio)
	}
	setTune(func(u *ttsTuning) { u.FirstAudio = 2600 * time.Millisecond })
	c := r.open("")
	r.up.set(func() { r.up.stall = true })
	c.send(`{"type":"text","id":"slow","text":"Never answered."}`)
	var gaps []time.Duration
	last := time.Now()
	for {
		ev := c.recv(5 * time.Second)
		gaps = append(gaps, time.Since(last))
		last = time.Now()
		if ev.M["type"] == "error" {
			if ev.M["code"] != "upstream_timeout" {
				t.Fatalf("%v", ev.M)
			}
			break
		}
		if ev.M["type"] != "progress" || ev.M["id"] != "slow" {
			t.Fatalf("unexpected %v", ev.M)
		}
	}
	if len(gaps) < 3 {
		t.Fatalf("only %d frames in 2.6 s", len(gaps))
	}
	for _, g := range gaps {
		if g > 1100*time.Millisecond {
			t.Fatalf("the app heard nothing for %s", g)
		}
	}
	// Nothing outstanding, nothing sent.
	if ev, err := c.recvErr(1500 * time.Millisecond); err == nil {
		t.Fatalf("a frame with nothing outstanding: %v", ev.M)
	}
}

// Text that arrives before ready (the app may not wait) is queued and spoken.
func TestTTSTextBeforeReady(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.hsDelay = 300 * time.Millisecond })
	c, _, err := r.dial(r.token)
	if err != nil {
		t.Fatal(err)
	}
	c.send(`{"type":"start","engine":"inworld"}`)
	c.send(`{"type":"text","id":"e1","text":"Early one."}`)
	c.send(`{"type":"text","id":"e2","text":"Early two."}`)
	if ev := c.recv(5 * time.Second); ev.M["type"] != "ready" {
		t.Fatalf("first frame = %v", ev.M)
	}
	c.until(5*time.Second, isType("done", "e1"))
	c.until(5*time.Second, isType("done", "e2"))
}

// done carries the characters counted and the audio's true length, and usage is
// current as soon as a sentence has been sent.
func TestTTSDoneIsAccurate(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.chunks = 5; r.up.chunkBytes = 9600 })
	c2, _, _ := r.dial(r.token)
	c2.send(`{"type":"start","engine":"inworld","sample_rate":48000}`)
	c2.recv(5 * time.Second)
	c2.send("{\"type\":\"text\",\"id\":\"a\",\"text\":\"Hi \U0001F600 there\"}")
	evs := c2.until(5*time.Second, isType("done", "a"))
	bytes := 0
	for _, e := range evs {
		bytes += len(e.PCM)
	}
	d := evs[len(evs)-1].M
	if int(d["characters"].(float64)) != ttsCountChars("Hi \U0001F600 there") || int(d["characters"].(float64)) != 11 {
		t.Fatalf("done = %v", d)
	}
	if want := bytes / 2 * 1000 / 48000; int(d["audio_ms"].(float64)) != want || want != 500 {
		t.Fatalf("audio_ms = %v, bytes = %d", d["audio_ms"], bytes)
	}
	var out struct {
		Engines []struct{ Usage map[string]int }
	}
	r.get("/client/v1/tts", r.token, &out)
	if out.Engines[0].Usage["characters_this_month"] != 11 {
		t.Fatalf("usage = %v", out.Engines[0].Usage)
	}
}

// A sentence cancelled before it went upstream is neither counted nor answered.
func TestTTSCancelledBeforeSendIsNotCountedOrAnswered(t *testing.T) {
	r := newTTSTest(t, true)
	r.up.set(func() { r.up.chunks = 10; r.up.chunkDelay = 30 * time.Millisecond })
	c := r.open("")
	c.send(`{"type":"text","id":"s1","text":"Plays."}`)
	c.send(`{"type":"text","id":"s2","text":"Never sent upstream."}`)
	c.send(`{"type":"text","id":"s3","text":"Neither."}`)
	c.until(5*time.Second, isType("audio", "s1"))
	c.send(`{"type":"cancel","id":"s3"}`)
	c.send(`{"type":"cancel","id":"s2"}`)
	evs := c.until(10*time.Second, isType("done", "s1"))
	for _, e := range evs {
		if e.M["id"] == "s2" || e.M["id"] == "s3" {
			t.Fatalf("a frame for a cancelled sentence: %v", e.M)
		}
	}
	if ev, err := c.recvErr(500 * time.Millisecond); err == nil {
		t.Fatalf("late frame %v", ev.M)
	}
	if got := r.up.snapshotTexts(); len(got) > 2 {
		t.Fatalf("texts = %v", got)
	}
	used, _ := ttsUsed(r.user)
	if used != len("Plays.") && used != len("Plays.")+len("Never sent upstream.") {
		t.Fatalf("usage = %d", used)
	}
}

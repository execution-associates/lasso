package main

// The Inworld side of the text-to-speech relay, and the only file that knows
// their protocol. Everything here follows Inworld's documentation (see
// docs/design/client-tts.md, "Inworld", for the pages and what is verified);
// no key existed when it was written, so it was built against a stand-in
// server (clienttts_test.go) and each place the real service may differ is
// marked UNVERIFIED. To correct it, change this file: the stream
// (clienttts_stream.go) sees only ttsUp* values.
//
// One upstream WebSocket serves one app stream. Each sentence is one Inworld
// context: create, send_text with flush_context, then audioChunk messages until
// flushCompleted, then close_context. A cancelled sentence is cancel_context.
// The host is fixed in this file; neither a setting nor a client can name one.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// The fixed upstream. Tests replace them with a stand-in's address; nothing at
// run time does.
var (
	inworldWSURL     = "wss://api.inworld.ai/tts/v1/voice:streamBidirectional"
	inworldVoicesURL = "https://api.inworld.ai/voices/v1/voices"
)

const (
	inworldDialTimeout   = 1200 * time.Millisecond
	inworldReadLimit     = 4 << 20
	inworldVoicesPage    = 200
	inworldVoicesPages   = 3
	inworldVoicesMaxBody = 4 << 20
)

// inworldAuth is the Authorization header: Inworld's key is already the Base64
// credential the portal shows, sent as-is after "Basic" (never re-encoded).
func inworldAuth(key string) string { return "Basic " + key }

// ttsErr is a failure with the relay's error code, as the app sees it.
type ttsErr struct {
	Code string // upstream_unavailable | upstream_timeout | quota_exceeded | rate_limited | invalid_request
	Msg  string
	Auth bool // the key was refused
}

func (e *ttsErr) Error() string { return e.Code + ": " + e.Msg }

// --- events ------------------------------------------------------------------

const (
	upAudio = iota
	upFlushed
	upError
)

type upEvent struct {
	kind  int
	audio []byte
	err   *ttsErr
}

// upQueue holds one context's events between the connection's reader and the
// stream. Audio is bounded: past tune().QueueBytes push waits, which stops
// the reader and so, through TCP, upstream; a slow phone cannot make lasso
// buffer more than that per sentence.
type upQueue struct {
	mu       sync.Mutex
	items    []upEvent
	bytes    int
	closed   bool
	notEmpty chan struct{}
	space    chan struct{}
}

func newUpQueue() *upQueue {
	return &upQueue{notEmpty: make(chan struct{}, 1), space: make(chan struct{}, 1)}
}

func (q *upQueue) push(ctx context.Context, ev upEvent) bool {
	for {
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			return false
		}
		if len(q.items) == 0 || q.bytes+len(ev.audio) <= tune().QueueBytes {
			q.items = append(q.items, ev)
			q.bytes += len(ev.audio)
			q.mu.Unlock()
			select {
			case q.notEmpty <- struct{}{}:
			default:
			}
			return true
		}
		q.mu.Unlock()
		select {
		case <-q.space:
		case <-ctx.Done():
			return false
		}
	}
}

var errUpQueueClosed = errors.New("closed")

// pop answers the next event, waiting until the deadline passes (context
// deadline exceeded), ctx ends, or the queue is closed and empty.
func (q *upQueue) pop(ctx context.Context, deadline time.Time) (upEvent, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			ev := q.items[0]
			q.items[0] = upEvent{}
			q.items = q.items[1:]
			q.bytes -= len(ev.audio)
			q.mu.Unlock()
			select {
			case q.space <- struct{}{}:
			default:
			}
			return ev, nil
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return upEvent{}, errUpQueueClosed
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			return upEvent{}, context.DeadlineExceeded
		}
		t := time.NewTimer(wait)
		select {
		case <-q.notEmpty:
			t.Stop()
		case <-t.C:
			return upEvent{}, context.DeadlineExceeded
		case <-ctx.Done():
			t.Stop()
			return upEvent{}, ctx.Err()
		}
	}
}

func (q *upQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.items, q.bytes = nil, 0
	q.mu.Unlock()
	select {
	case q.notEmpty <- struct{}{}:
	default:
	}
	select {
	case q.space <- struct{}{}:
	default:
	}
}

// upContext is one sentence's context on the connection.
type upContext struct {
	id     string
	q      *upQueue
	upDone chan struct{} // closed when upstream has finished with it: flushed, failed or lost
	once   sync.Once
}

func (c *upContext) finish() { c.once.Do(func() { close(c.upDone) }) }

// --- the connection ----------------------------------------------------------

type inworldConn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	wmu    sync.Mutex
	mu     sync.Mutex
	ctxs   map[string]*upContext
	dead   chan struct{}
	seq    int
}

// dialInworld opens the upstream WebSocket. A refused key is ttsErr.Auth.
func dialInworld(parent context.Context, key string) (*inworldConn, error) {
	ctx, cancel := context.WithCancel(parent)
	dctx, dcancel := context.WithTimeout(ctx, inworldDialTimeout)
	defer dcancel()
	ws, resp, err := websocket.Dial(dctx, inworldWSURL, &websocket.DialOptions{
		HTTPHeader:      http.Header{"Authorization": {inworldAuth(key)}},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		cancel()
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return nil, &ttsErr{Code: "upstream_unavailable", Msg: "Inworld refused lasso's key.", Auth: true}
			case http.StatusTooManyRequests:
				return nil, &ttsErr{Code: "rate_limited", Msg: "Inworld is rate limiting lasso."}
			}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &ttsErr{Code: "upstream_timeout", Msg: "Inworld did not answer in time."}
		}
		return nil, &ttsErr{Code: "upstream_unavailable", Msg: "Inworld could not be reached."}
	}
	ws.SetReadLimit(inworldReadLimit)
	c := &inworldConn{ws: ws, ctx: ctx, cancel: cancel, ctxs: map[string]*upContext{}, dead: make(chan struct{})}
	go c.readLoop()
	return c, nil
}

func (c *inworldConn) isDead() bool {
	select {
	case <-c.dead:
		return true
	default:
		return false
	}
}

func (c *inworldConn) close() {
	c.cancel()
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
}

// inworldMessage is a server message. UNVERIFIED against a live service: the
// shapes are the documented ones (result.contextId, result.audioChunk.
// audioContent as Base64, result.flushCompleted, result.contextClosed,
// result.status.{code,message}); a top-level {"error":{…}} is also accepted.
type inworldMessage struct {
	Result *struct {
		ContextID  string `json:"contextId"`
		AudioChunk *struct {
			AudioContent string `json:"audioContent"`
		} `json:"audioChunk"`
		FlushCompleted *json.RawMessage `json:"flushCompleted"`
		ContextClosed  *json.RawMessage `json:"contextClosed"`
		Status         *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"status"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *inworldConn) readLoop() {
	defer func() {
		close(c.dead)
		c.mu.Lock()
		ctxs := c.ctxs
		c.ctxs = map[string]*upContext{}
		c.mu.Unlock()
		pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer pcancel()
		for _, uc := range ctxs {
			uc.q.push(pctx, upEvent{kind: upError, err: &ttsErr{Code: "upstream_unavailable", Msg: "The connection to Inworld was lost."}})
			uc.finish()
		}
		c.cancel()
	}()
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var m inworldMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		c.dispatch(&m)
	}
}

func (c *inworldConn) dispatch(m *inworldMessage) {
	if m.Error != nil && m.Result == nil {
		// Not tied to a context: every open one fails.
		e := inworldStatusErr(m.Error.Code, m.Error.Message)
		c.mu.Lock()
		all := make([]*upContext, 0, len(c.ctxs))
		for _, uc := range c.ctxs {
			all = append(all, uc)
		}
		c.mu.Unlock()
		for _, uc := range all {
			c.deliver(uc, upEvent{kind: upError, err: e}, true)
		}
		return
	}
	if m.Result == nil {
		return
	}
	r := m.Result
	c.mu.Lock()
	uc := c.ctxs[r.ContextID]
	c.mu.Unlock()
	if uc == nil {
		return // a cancelled or finished context: anything still arriving is dropped
	}
	switch {
	case r.Status != nil && r.Status.Code != 0:
		c.deliver(uc, upEvent{kind: upError, err: inworldStatusErr(r.Status.Code, r.Status.Message)}, true)
	case r.AudioChunk != nil && r.AudioChunk.AudioContent != "":
		pcm, err := base64.StdEncoding.DecodeString(r.AudioChunk.AudioContent)
		if err != nil || len(pcm) == 0 {
			return
		}
		c.deliver(uc, upEvent{kind: upAudio, audio: pcm}, false)
	case r.FlushCompleted != nil:
		c.deliver(uc, upEvent{kind: upFlushed}, true)
	case r.ContextClosed != nil:
		c.forget(uc.id)
		uc.finish()
	}
}

func (c *inworldConn) deliver(uc *upContext, ev upEvent, terminal bool) {
	uc.q.push(c.ctx, ev)
	if terminal {
		uc.finish()
	}
}

func (c *inworldConn) forget(id string) {
	c.mu.Lock()
	delete(c.ctxs, id)
	c.mu.Unlock()
}

// inworldStatusErr maps a gRPC status code from a message to the relay's.
// UNVERIFIED: the documented facts are code 8 (RESOURCE_EXHAUSTED) for the
// concurrency limit and INVALID_ARGUMENT for a bad request; the quota wording
// is a guess, so anything else is upstream_unavailable.
func inworldStatusErr(code int, msg string) *ttsErr {
	switch code {
	case 8:
		l := strings.ToLower(msg)
		if strings.Contains(l, "credit") || strings.Contains(l, "quota") || strings.Contains(l, "balance") || strings.Contains(l, "billing") {
			return &ttsErr{Code: "quota_exceeded", Msg: "Inworld's quota is used up."}
		}
		return &ttsErr{Code: "rate_limited", Msg: "Inworld is busy; try again."}
	case 3, 11:
		return &ttsErr{Code: "invalid_request", Msg: "Inworld refused the request."}
	case 4:
		return &ttsErr{Code: "upstream_timeout", Msg: "Inworld did not answer in time."}
	case 7, 16:
		return &ttsErr{Code: "upstream_unavailable", Msg: "Inworld refused lasso's key.", Auth: true}
	}
	return &ttsErr{Code: "upstream_unavailable", Msg: "Inworld failed to synthesize."}
}

func (c *inworldConn) send(v any) error {
	b, _ := json.Marshal(v)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, b)
}

// synth starts one sentence: it opens a context, sends the text and flushes it.
// The characters are counted by the caller just before this is called.
func (c *inworldConn) synth(model, voice string, rate int, text string) (*upContext, error) {
	if c.isDead() {
		return nil, &ttsErr{Code: "upstream_unavailable", Msg: "The connection to Inworld was lost."}
	}
	c.mu.Lock()
	c.seq++
	id := fmt.Sprintf("c%d", c.seq)
	uc := &upContext{id: id, q: newUpQueue(), upDone: make(chan struct{})}
	c.ctxs[id] = uc
	c.mu.Unlock()
	// autoMode is off: the text is flushed explicitly, so nothing waits on
	// Inworld's buffering. PCM has no header (UNVERIFIED: s16le, mono).
	create := map[string]any{"create": map[string]any{
		"voiceId": voice, "modelId": model, "autoMode": false,
		"audioConfig": map[string]any{"audioEncoding": "PCM", "sampleRateHertz": rate},
	}, "contextId": id}
	if err := c.send(create); err != nil {
		c.forget(id)
		return nil, &ttsErr{Code: "upstream_unavailable", Msg: "The connection to Inworld was lost."}
	}
	if err := c.send(map[string]any{"send_text": map[string]any{"text": text, "flush_context": map[string]any{}}, "contextId": id}); err != nil {
		c.forget(id)
		return nil, &ttsErr{Code: "upstream_unavailable", Msg: "The connection to Inworld was lost."}
	}
	return uc, nil
}

// end closes a finished context (Inworld frees its slot once it is closed).
func (c *inworldConn) end(uc *upContext) {
	_ = c.send(map[string]any{"close_context": map[string]any{}, "contextId": uc.id})
	c.forget(uc.id)
}

// cancel drops a context: whatever Inworld has not synthesized is discarded
// (and, by Inworld's billing page, not billed) and audio still arriving for it
// is dropped here.
func (c *inworldConn) cancelCtx(uc *upContext) {
	c.forget(uc.id)
	uc.q.close()
	uc.finish()
	if !c.isDead() {
		_ = c.send(map[string]any{"cancel_context": map[string]any{}, "contextId": uc.id})
	}
}

// --- voices ------------------------------------------------------------------

// inworldListVoices reads the voice catalog over HTTPS with the same key.
// UNVERIFIED: GET /voices/v1/voices with pageSize/pageToken and a voices array
// of {voiceId, displayName, languageCode, gender, description} is the documented
// shape.
func inworldListVoices(ctx context.Context, key string) ([]ttsVoice, error) {
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var out []ttsVoice
	seen := map[string]bool{}
	token := ""
	for page := 0; page < inworldVoicesPages; page++ {
		q := url.Values{"pageSize": {fmt.Sprint(inworldVoicesPage)}}
		if token != "" {
			q.Set("pageToken", token)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, inworldVoicesURL+"?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", inworldAuth(key))
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("voices: not reachable")
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, inworldVoicesMaxBody))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			ttsMarkFailing()
		}
		if rerr != nil || resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("voices: status %d", resp.StatusCode)
		}
		var r struct {
			Voices []struct {
				VoiceID      string `json:"voiceId"`
				DisplayName  string `json:"displayName"`
				LanguageCode string `json:"languageCode"`
				LangCode     string `json:"langCode"`
				Gender       string `json:"gender"`
				Description  string `json:"description"`
			} `json:"voices"`
			NextPageToken string `json:"nextPageToken"`
		}
		if json.Unmarshal(body, &r) != nil {
			return nil, errors.New("voices: unreadable answer")
		}
		for _, v := range r.Voices {
			if !ttsVoiceIDRE(v.VoiceID) || seen[v.VoiceID] {
				continue
			}
			seen[v.VoiceID] = true
			tv := ttsVoice{ID: v.VoiceID, Name: clientMetaClean(v.DisplayName, 80), Description: clientMetaClean(v.Description, 240)}
			if tv.Name == "" {
				tv.Name = v.VoiceID
			}
			if g := strings.ToLower(v.Gender); g == "male" || g == "female" {
				tv.Gender = &g
			}
			lang := v.LanguageCode
			if lang == "" {
				lang = v.LangCode
			}
			lang = strings.ToLower(strings.NewReplacer("_", "-").Replace(lang))
			if i := strings.IndexByte(lang, '-'); i > 0 {
				lang = lang[:i]
			}
			tv.Language = lang
			out = append(out, tv)
		}
		token = r.NextPageToken
		if token == "" {
			break
		}
	}
	return out, nil
}

// --- the owner's test --------------------------------------------------------

// ttsSelfTest synthesizes ttsTestPhrase once and reports how it went. It
// reports the code, never a message that could hold more than lasso wrote.
func ttsSelfTest(ctx context.Context, key string, cfg clientTTSInworld, model ttsModel, voice string) map[string]any {
	out := map[string]any{"model": model.ID, "voice": voice}
	fail := func(code, msg string) map[string]any {
		out["ok"] = false
		out["code"] = code
		out["message"] = msg
		return out
	}
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(ctx, tune().Sentence)
	defer cancel()
	conn, err := dialInworld(ctx, key)
	if err != nil {
		var te *ttsErr
		errors.As(err, &te)
		if te.Auth {
			ttsMarkFailing()
		}
		return fail(te.Code, te.Msg)
	}
	defer conn.close()
	out["connect_ms"] = time.Since(t0).Milliseconds()
	chars := ttsCountChars(ttsTestPhrase)
	if err := ttsReserve(ttsOwnerTestUser, chars, cfg); err != nil {
		return fail("quota_exceeded", "This month's character limit is reached.")
	}
	month := ttsMonth(tune().Now())
	t1 := time.Now()
	uc, err := conn.synth(model.Upstream, voice, ttsDefaultSampleRate, ttsTestPhrase)
	if err != nil {
		ttsRefund(ttsOwnerTestUser, chars, month)
		var te *ttsErr
		errors.As(err, &te)
		return fail(te.Code, te.Msg)
	}
	out["characters"] = chars
	total := 0
	first := time.Duration(0)
	for {
		ev, err := uc.q.pop(ctx, t1.Add(tune().Sentence))
		if err != nil {
			conn.cancelCtx(uc)
			return fail("upstream_timeout", "Inworld did not finish in time.")
		}
		switch ev.kind {
		case upAudio:
			if first == 0 {
				first = time.Since(t1)
				out["first_audio_ms"] = first.Milliseconds()
			}
			total += len(ev.audio)
			continue
		case upError:
			if ev.err.Auth {
				ttsMarkFailing()
			}
			return fail(ev.err.Code, ev.err.Msg)
		}
		break
	}
	conn.end(uc)
	ttsMarkWorking()
	ms := total / 2 * 1000 / ttsDefaultSampleRate
	ttsAddAudio(ttsOwnerTestUser, ms)
	out["ok"] = true
	out["total_ms"] = time.Since(t1).Milliseconds()
	out["audio_ms"] = ms
	out["audio_bytes"] = total
	return out
}

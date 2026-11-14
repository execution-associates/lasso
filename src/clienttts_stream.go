package main

// GET /client/v1/tts/stream: the app's side of the text-to-speech relay
// (docs/design/client-tts.md has the frames). One WebSocket per spoken reply.
// The app sends sentences; lasso synthesizes them in order through one upstream
// connection (clienttts_inworld.go) and answers each with an `audio` text frame
// followed by one binary frame of PCM s16le, then `done`.
//
// Three goroutines do the work besides the reader of the app's frames:
//   - the feeder takes sentences in order and sends each upstream, one at a time
//     (a sentence is sent only when the one before it has been fully received);
//   - the player takes them in the same order and writes their audio to the app;
//   - housekeeping pings, checks the device is still authorized and ends idle and
//     old streams.
//
// The hand-off from feeder to player holds one sentence, so the next sentence is
// already being synthesized while the current one plays, and never more: audio
// held is at most two sentences' worth, each bounded (tune().QueueBytes), so a
// slow phone slows upstream rather than growing memory. Logs carry ids, sizes and
// timings only, never text or audio.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

var ttsIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// --- concurrency -------------------------------------------------------------

var ttsStreams = struct {
	sync.Mutex
	device, person map[string]int
	total          int
}{device: map[string]int{}, person: map[string]int{}}

func ttsStreamAcquire(deviceID, userID string) bool {
	ttsStreams.Lock()
	defer ttsStreams.Unlock()
	if ttsStreams.device[deviceID] >= tune().PerDevice || ttsStreams.person[userID] >= tune().PerPerson || ttsStreams.total >= tune().Total {
		return false
	}
	ttsStreams.device[deviceID]++
	ttsStreams.person[userID]++
	ttsStreams.total++
	return true
}

func ttsStreamRelease(deviceID, userID string) {
	ttsStreams.Lock()
	defer ttsStreams.Unlock()
	if ttsStreams.device[deviceID]--; ttsStreams.device[deviceID] <= 0 {
		delete(ttsStreams.device, deviceID)
	}
	if ttsStreams.person[userID]--; ttsStreams.person[userID] <= 0 {
		delete(ttsStreams.person, userID)
	}
	ttsStreams.total--
}

// ttsDeviceAuthorized is the check every client route does: the device
// unrevoked and its person enabled.
func ttsDeviceAuthorized(id *clientIdentity) bool {
	var n int
	return db.QueryRow(`SELECT COUNT(*) FROM app_devices d JOIN app_users u ON u.id = d.user_id
		WHERE d.id = ? AND d.user_id = ? AND d.revoked_at = '' AND u.disabled = 0`, id.Device.ID, id.User.ID).Scan(&n) == nil && n == 1
}

// --- the stream --------------------------------------------------------------

type ttsSentence struct {
	id    string
	text  string
	chars int
	n     int64 // order of arrival

	cancelled  atomic.Bool
	cancelCh   chan struct{}
	cancelOnce sync.Once
	ready      chan struct{} // closed once the feeder has tried to send it upstream

	// set by the feeder, under the stream's mu
	uc       *upContext
	conn     *inworldConn
	startErr *ttsErr
	issuedAt time.Time
}

type ttsStream struct {
	ws     *websocket.Conn
	id     *clientIdentity
	ctx    context.Context
	cancel context.CancelFunc
	wmu    sync.Mutex

	key   string
	cfg   clientTTSInworld
	model ttsModel
	voice string
	rate  int

	mu     sync.Mutex
	live   map[string]*ttsSentence
	ids    map[string]bool
	up     *inworldConn
	ending bool
	last   time.Time // the last frame from the app
	nextN  int64
	wrote  atomic.Int64 // unix nanos of the last frame sent to the app

	inCh   chan *ttsSentence
	playCh chan *ttsSentence

	done      chan struct{}
	closeOnce sync.Once
	code      websocket.StatusCode
	reason    string

	sentences, chars, audioMS atomic.Int64
	bad                       int
}

// finish ends the stream with a WebSocket close code and a short reason.
func (st *ttsStream) finish(code websocket.StatusCode, reason string) {
	st.closeOnce.Do(func() {
		st.code, st.reason = code, reason
		close(st.done)
	})
}

type ttsFrame struct {
	Type       string `json:"type"`
	Engine     string `json:"engine"`
	Model      string `json:"model"`
	Voice      string `json:"voice"`
	SampleRate int    `json:"sample_rate"`
	ID         string `json:"id"`
	Text       string `json:"text"`
}

// write sends one frame, or two that must stay together; a phone that takes
// nothing for tune().Write loses the stream.
func (st *ttsStream) write(frames ...ttsOut) error {
	for _, f := range frames {
		// Not st.ctx: a cancelled context closes a WebSocket without a goodbye.
		ctx, cancel := context.WithTimeout(context.Background(), tune().Write)
		err := st.ws.Write(ctx, f.typ, f.data)
		cancel()
		st.wrote.Store(time.Now().UnixNano())
		if err != nil {
			st.finish(websocket.StatusGoingAway, "write failed")
			return err
		}
	}
	return nil
}

type ttsOut struct {
	typ  websocket.MessageType
	data []byte
}

func jsonFrame(v any) ttsOut {
	b, _ := json.Marshal(v)
	return ttsOut{websocket.MessageText, b}
}

// sendFor writes frames that belong to a sentence, unless it was cancelled: the
// check is under the write lock, so once a cancel has returned nothing more
// goes out for that sentence.
func (st *ttsStream) sendFor(s *ttsSentence, frames ...ttsOut) error {
	st.wmu.Lock()
	defer st.wmu.Unlock()
	if s != nil && s.cancelled.Load() {
		return errTTSCancelled
	}
	return st.write(frames...)
}

var errTTSCancelled = errors.New("cancelled")

func (st *ttsStream) sendError(s *ttsSentence, code, msg string, fatal bool) {
	var id any
	if s != nil {
		id = s.id
	}
	_ = st.sendFor(s, jsonFrame(map[string]any{"type": "error", "id": id, "code": code, "message": msg, "fatal": fatal}))
	if fatal {
		st.finish(websocket.StatusNormalClosure, code)
	}
}

func (st *ttsStream) release(s *ttsSentence) {
	st.mu.Lock()
	delete(st.live, s.id)
	st.mu.Unlock()
}

// cancelSentence drops one sentence: nothing more is sent to the app for it and
// the upstream context, if there is one, is cancelled.
func (st *ttsStream) cancelSentence(s *ttsSentence) {
	s.cancelOnce.Do(func() {
		s.cancelled.Store(true)
		close(s.cancelCh)
		st.mu.Lock()
		uc, conn := s.uc, s.conn
		delete(st.live, s.id)
		st.mu.Unlock()
		if uc != nil && conn != nil {
			conn.cancelCtx(uc)
		}
	})
	// The barrier: a frame for it that was being written is out by now.
	st.wmu.Lock()
	st.wmu.Unlock() //nolint:staticcheck
}

func (st *ttsStream) cancelAll() {
	st.mu.Lock()
	all := make([]*ttsSentence, 0, len(st.live))
	for _, s := range st.live {
		all = append(all, s)
	}
	st.mu.Unlock()
	for _, s := range all {
		st.cancelSentence(s)
	}
}

// serveClientTTSStream upgrades the request and runs the relay.
func serveClientTTSStream(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	if r.Method != http.MethodGet {
		clientMethod(w, "GET")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		clientError(w, http.StatusUpgradeRequired, "upgrade_required", "This route is a WebSocket.")
		return
	}
	if !clientLimits.allow("ttsopen:"+id.Device.ID, ttsStreamOpensPerMin, time.Minute, time.Now()) {
		w.Header().Set("Retry-After", "10")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many streams opened.")
		return
	}
	// Refusals that can be known before the upgrade are plain HTTP with the
	// client API's error JSON, so an app can tell them apart: 404 not_configured
	// (nothing set up, switched off, key unreadable), 429 rate_limited (too many
	// streams open, or opened too fast). Quota and a failing upstream are in-band
	// frames, since they change while the app is connected.
	if _, reason, _ := clientTTSState(); reason == "not_configured" || reason == "unreadable" {
		clientError(w, http.StatusNotFound, "not_configured", "Text to speech is not set up on this lasso.")
		return
	}
	if !ttsStreamAcquire(id.Device.ID, id.User.ID) {
		w.Header().Set("Retry-After", "5")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many speech streams are open.")
		return
	}
	defer ttsStreamRelease(id.Device.ID, id.User.ID)
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return // Accept has answered
	}
	ws.SetReadLimit(16 << 10)
	ctx, cancel := context.WithTimeout(context.Background(), tune().Lifetime)
	st := &ttsStream{ws: ws, id: id, ctx: ctx, cancel: cancel, live: map[string]*ttsSentence{}, ids: map[string]bool{},
		inCh: make(chan *ttsSentence, ttsMaxQueued), playCh: make(chan *ttsSentence, 1), done: make(chan struct{}), last: time.Now(),
		code: websocket.StatusNormalClosure}
	t0 := time.Now()
	defer func() {
		// The goodbye first, while the connection is still open; then everything
		// that was running for it stops.
		reason := st.reason
		if len(reason) > 100 {
			reason = reason[:100]
		}
		_ = ws.Close(st.code, reason)
		cancel()
		st.mu.Lock()
		up := st.up
		st.mu.Unlock()
		if up != nil {
			up.close()
		}
		log.Printf("tts:      stream closed device=%s sentences=%d chars=%d audio_ms=%d after=%s (%s)", id.Device.ID, st.sentences.Load(), st.chars.Load(), st.audioMS.Load(), time.Since(t0).Round(time.Millisecond), st.reason)
	}()
	if !st.begin() {
		return
	}
	var wg sync.WaitGroup
	for _, f := range []func(){st.readLoop, st.feeder, st.player, st.housekeeping} {
		wg.Add(1)
		go func(f func()) { defer wg.Done(); f() }(f)
	}
	select {
	case <-st.done:
	case <-ctx.Done():
		st.finish(websocket.StatusGoingAway, "lifetime")
	}
	st.cancelAll()
	_ = ws.Close(st.code, st.reason[:min(len(st.reason), 100)])
	cancel()
	wg.Wait()
}

// begin checks the engine, reads `start`, connects upstream and answers `ready`.
// It reports false when the stream is over.
func (st *ttsStream) begin() bool {
	key, reason, cfg := clientTTSState()
	switch reason {
	case "not_configured", "unreadable":
		st.sendError(nil, "not_configured", "Text to speech is not set up on this lasso.", true)
		return false
	case "failing":
		st.sendError(nil, "upstream_unavailable", "The speech service is not answering; try again shortly.", true)
		return false
	}
	person, total := ttsUsed(st.id.User.ID)
	if person >= cfg.personLimit() || total >= cfg.totalLimit() {
		st.sendError(nil, "quota_exceeded", "This month's speech allowance is used up.", true)
		return false
	}
	st.key, st.cfg = key, cfg
	// Waiting for start is bounded by a timer, not a context: a cancelled read
	// would drop the connection without a goodbye.
	timer := time.AfterFunc(tune().Start, func() {
		st.sendError(nil, "invalid_request", "Send {\"type\":\"start\"} first.", true)
		_ = st.ws.Close(websocket.StatusNormalClosure, "start timeout")
	})
	defer timer.Stop()
	typ, data, err := st.ws.Read(context.Background())
	if err != nil {
		st.sendError(nil, "invalid_request", "Send {\"type\":\"start\"} first.", true)
		return false
	}
	var f ttsFrame
	if typ != websocket.MessageText || !utf8.Valid(data) || json.Unmarshal(data, &f) != nil || f.Type != "start" {
		st.sendError(nil, "invalid_request", "The first frame must be a start.", true)
		return false
	}
	if f.Engine != ttsEngineInworld {
		st.sendError(nil, "invalid_request", "Unknown engine.", true)
		return false
	}
	st.model = cfg.model()
	if f.Model != "" {
		m, ok := ttsModelByID(f.Model)
		if !ok {
			st.sendError(nil, "invalid_request", "Unknown model.", true)
			return false
		}
		st.model = m
	}
	st.rate = ttsDefaultSampleRate
	if f.SampleRate != 0 {
		if !ttsSampleRates[f.SampleRate] {
			st.sendError(nil, "invalid_request", "sample_rate must be 16000, 24000 or 48000.", true)
			return false
		}
		st.rate = f.SampleRate
	}
	vs := ttsVoiceList(key, cfg, time.Second)
	st.voice = f.Voice
	if st.voice == "" {
		st.voice = ttsDefaultVoice(cfg, vs)
	}
	if st.voice == "" || !ttsHasVoice(vs, st.voice) {
		st.sendError(nil, "invalid_request", "That voice is not offered.", true)
		return false
	}
	up, derr := st.dial()
	if derr != nil {
		st.sendError(nil, derr.Code, derr.Msg, true)
		return false
	}
	st.mu.Lock()
	st.up = up
	st.mu.Unlock()
	log.Printf("tts:      stream open device=%s model=%s rate=%d", st.id.Device.ID, st.model.ID, st.rate)
	return st.write(jsonFrame(map[string]any{"type": "ready", "sample_rate": st.rate, "format": "pcm_s16le", "channels": 1,
		"model": st.model.ID, "voice": st.voice})) == nil
}

// dial connects upstream, a few times with a short back-off; a refused key
// marks the engine failing.
func (st *ttsStream) dial() (*inworldConn, *ttsErr) {
	var last *ttsErr
	for _, wait := range []time.Duration{0, 200 * time.Millisecond} {
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-st.ctx.Done():
				return nil, &ttsErr{Code: "upstream_unavailable", Msg: "Stopped."}
			}
		}
		c, err := dialInworld(st.ctx, st.key)
		if err == nil {
			ttsMarkWorking()
			return c, nil
		}
		var te *ttsErr
		if !errors.As(err, &te) {
			te = &ttsErr{Code: "upstream_unavailable", Msg: "Inworld could not be reached."}
		}
		last = te
		if te.Auth || te.Code == "rate_limited" {
			break // another try would not change the answer
		}
	}
	if last.Code != "rate_limited" {
		ttsMarkFailing()
	}
	return nil, last
}

// --- the app's frames --------------------------------------------------------

func (st *ttsStream) readLoop() {
	for {
		typ, data, err := st.ws.Read(context.Background())
		if err != nil {
			st.finish(websocket.StatusNormalClosure, "client gone")
			return
		}
		st.mu.Lock()
		st.last = time.Now()
		st.mu.Unlock()
		if typ != websocket.MessageText {
			st.sendError(nil, "invalid_request", "Frames are JSON text.", true)
			st.finish(websocket.StatusUnsupportedData, "binary frame")
			return
		}
		if !utf8.Valid(data) {
			st.badFrame("The frame is not UTF-8.")
			continue
		}
		var f ttsFrame
		if json.Unmarshal(data, &f) != nil {
			st.badFrame("The frame is not JSON.")
			continue
		}
		switch f.Type {
		case "text":
			st.onText(f)
		case "cancel":
			if f.ID == "" {
				st.cancelAll()
				continue
			}
			st.mu.Lock()
			s := st.live[f.ID]
			st.mu.Unlock()
			if s != nil {
				st.cancelSentence(s)
			}
		case "end":
			st.mu.Lock()
			if !st.ending {
				st.ending = true
				close(st.inCh)
			}
			st.mu.Unlock()
		default:
			st.badFrame("Unknown frame type.")
		}
		if st.bad >= ttsBadFramesMax {
			st.sendError(nil, "invalid_request", "Too many bad frames.", true)
			return
		}
	}
}

func (st *ttsStream) badFrame(msg string) {
	st.bad++
	st.sendError(nil, "invalid_request", msg, false)
}

func (st *ttsStream) onText(f ttsFrame) {
	if !ttsIDRE.MatchString(f.ID) {
		st.badFrame("id must be 1-64 of A-Za-z0-9._:-")
		return
	}
	rej := func(code, msg string) {
		st.sendError(&ttsSentence{id: f.ID}, code, msg, false)
	}
	st.mu.Lock()
	ending, dup, n, ids := st.ending, st.ids[f.ID], len(st.live), len(st.ids)
	st.mu.Unlock()
	switch {
	case ending:
		rej("invalid_request", "The stream is ending.")
		return
	case dup:
		rej("invalid_request", "That id was already used on this stream.")
		return
	case ids >= ttsMaxIDsPerStream:
		rej("invalid_request", "Too many sentences on one stream; open a new one.")
		return
	}
	text, ok := ttsCleanText(f.Text)
	chars := ttsCountChars(text)
	switch {
	case !ok:
		rej("invalid_request", "The text is empty or not valid text.")
		return
	case len([]rune(text)) > ttsMaxTextChars:
		rej("text_too_long", "A sentence may be at most 1000 characters.")
		return
	case n >= ttsMaxQueued:
		rej("rate_limited", "Too many sentences are waiting.")
		return
	}
	now := time.Now()
	if !clientLimits.allow("tts:"+st.id.User.ID, tune().SentencesPerMin, time.Minute, now) || !ttsChars.allow(st.id.User.ID, chars, tune().CharsPerMin, now) {
		rej("rate_limited", "Slow down.")
		return
	}
	st.mu.Lock()
	st.nextN++
	seqn := st.nextN
	st.mu.Unlock()
	s := &ttsSentence{id: f.ID, n: seqn, text: text, chars: chars, cancelCh: make(chan struct{}), ready: make(chan struct{})}
	st.mu.Lock()
	st.ids[f.ID] = true
	st.live[f.ID] = s
	st.mu.Unlock()
	st.inCh <- s // cap ttsMaxQueued, and live is capped at the same number
}

// --- upstream side -----------------------------------------------------------

func (st *ttsStream) feeder() {
	defer close(st.playCh)
	for {
		var s *ttsSentence
		select {
		case <-st.ctx.Done():
			return
		case v, ok := <-st.inCh:
			if !ok {
				return
			}
			s = v
		}
		select {
		case st.playCh <- s:
		case <-st.ctx.Done():
			return
		}
		if s.cancelled.Load() {
			close(s.ready)
			continue
		}
		st.issue(s)
		close(s.ready)
		st.mu.Lock()
		uc, conn := s.uc, s.conn
		st.mu.Unlock()
		if uc == nil {
			continue
		}
		select {
		case <-uc.upDone:
			if !s.cancelled.Load() {
				conn.end(uc)
			}
		case <-s.cancelCh:
		case <-st.ctx.Done():
			return
		}
	}
}

// issue sends one sentence upstream, counting its characters first.
func (st *ttsStream) issue(s *ttsSentence) {
	st.mu.Lock()
	up := st.up
	st.mu.Unlock()
	if up == nil || up.isDead() {
		nu, err := st.dial()
		if err != nil {
			s.startErr = err
			return
		}
		st.mu.Lock()
		st.up = nu
		st.mu.Unlock()
		up = nu
	}
	if s.cancelled.Load() {
		return
	}
	if err := ttsReserve(st.id.User.ID, s.chars, st.cfg); err != nil {
		if errors.Is(err, errTTSQuota) {
			s.startErr = &ttsErr{Code: "quota_exceeded", Msg: "This month's speech allowance is used up."}
		} else {
			s.startErr = &ttsErr{Code: "upstream_unavailable", Msg: "lasso could not record the usage."}
		}
		return
	}
	month := ttsMonth(tune().Now())
	uc, err := up.synth(st.model.Upstream, st.voice, st.rate, s.text)
	if err != nil {
		ttsRefund(st.id.User.ID, s.chars, month)
		var te *ttsErr
		if !errors.As(err, &te) {
			te = &ttsErr{Code: "upstream_unavailable", Msg: "Inworld could not be reached."}
		}
		s.startErr = te
		return
	}
	st.chars.Add(int64(s.chars))
	st.sentences.Add(1)
	st.mu.Lock()
	s.uc, s.conn, s.issuedAt = uc, up, time.Now()
	cancelled := s.cancelled.Load()
	st.mu.Unlock()
	if cancelled {
		up.cancelCtx(uc)
		return
	}
	log.Printf("tts:      sentence sent device=%s id=%s chars=%d", st.id.Device.ID, s.id, s.chars)
}

// --- the app's audio ---------------------------------------------------------

func (st *ttsStream) player() {
	for {
		var s *ttsSentence
		select {
		case <-st.ctx.Done():
			return
		case v, ok := <-st.playCh:
			if !ok {
				st.finish(websocket.StatusNormalClosure, "")
				return
			}
			s = v
		}
		select {
		case <-s.ready:
		case <-s.cancelCh:
			continue
		case <-st.ctx.Done():
			return
		}
		if s.cancelled.Load() {
			continue
		}
		st.mu.Lock()
		serr, uc := s.startErr, s.uc
		st.mu.Unlock()
		if serr != nil {
			st.fail(s, serr)
			continue
		}
		if uc == nil {
			continue
		}
		st.play(s, uc)
	}
}

// fail reports a sentence's failure; running out of allowance ends the stream.
func (st *ttsStream) fail(s *ttsSentence, e *ttsErr) {
	if e.Auth {
		ttsMarkFailing()
	}
	st.release(s)
	st.sendError(s, e.Code, e.Msg, e.Code == "quota_exceeded")
	log.Printf("tts:      sentence failed device=%s id=%s code=%s", st.id.Device.ID, s.id, e.Code)
}

func (st *ttsStream) play(s *ttsSentence, uc *upContext) {
	defer st.release(s)
	overall := s.issuedAt.Add(tune().Sentence)
	first := s.issuedAt.Add(tune().FirstAudio)
	seq, total := 0, 0
	var carry []byte
	gotAudio := false
	for {
		dl := time.Now().Add(tune().ChunkStall)
		if !gotAudio {
			dl = first
		}
		if dl.After(overall) {
			dl = overall
		}
		ev, err := uc.q.pop(st.ctx, dl)
		if s.cancelled.Load() || st.ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			if s.conn != nil {
				s.conn.cancelCtx(uc)
			}
			st.fail(s, &ttsErr{Code: "upstream_timeout", Msg: "The speech service took too long."})
			return
		case err != nil:
			st.fail(s, &ttsErr{Code: "upstream_unavailable", Msg: "The speech service stopped."})
			return
		}
		switch ev.kind {
		case upError:
			if ev.err.Code == "upstream_unavailable" || ev.err.Code == "upstream_timeout" {
				// The connection is suspect; the next sentence reconnects if it is gone.
			}
			st.fail(s, ev.err)
			return
		case upFlushed:
			ms := int(int64(total/2)*1000/int64(st.rate) + 0)
			if err := st.sendFor(s, jsonFrame(map[string]any{"type": "done", "id": s.id, "characters": s.chars, "audio_ms": ms})); err != nil {
				return
			}
			st.audioMS.Add(int64(ms))
			ttsAddAudio(st.id.User.ID, ms)
			ttsMarkWorking()
			return
		}
		pcm := ev.audio
		if !gotAudio && len(pcm) >= 44 && string(pcm[:4]) == "RIFF" && string(pcm[8:12]) == "WAVE" {
			pcm = pcm[44:] // a header the encoding should not have sent
		}
		gotAudio = true
		if len(carry) > 0 {
			pcm = append(carry, pcm...)
			carry = nil
		}
		if len(pcm)%2 == 1 {
			carry = []byte{pcm[len(pcm)-1]}
			pcm = pcm[:len(pcm)-1]
		}
		for len(pcm) > 0 {
			n := len(pcm)
			if n > ttsFrameMaxBytes {
				n = ttsFrameMaxBytes
			}
			part := pcm[:n]
			pcm = pcm[n:]
			if err := st.sendFor(s, jsonFrame(map[string]any{"type": "audio", "id": s.id, "seq": seq, "bytes": len(part)}),
				ttsOut{websocket.MessageBinary, part}); err != nil {
				return
			}
			seq++
			total += len(part)
		}
	}
}

// --- housekeeping ------------------------------------------------------------

func (st *ttsStream) housekeeping() {
	ping := time.NewTicker(tune().PingEvery)
	prog := time.NewTicker(300 * time.Millisecond)
	defer prog.Stop()
	auth := time.NewTicker(tune().AuthEvery)
	defer ping.Stop()
	defer auth.Stop()
	for {
		select {
		case <-st.ctx.Done():
			return
		case <-st.done:
			return
		case <-auth.C:
			if !ttsDeviceAuthorized(st.id) {
				st.finish(websocket.StatusPolicyViolation, "access_revoked")
				return
			}
			st.mu.Lock()
			idle := len(st.live) == 0 && time.Since(st.last) >= tune().Idle
			st.mu.Unlock()
			if idle {
				st.finish(websocket.StatusNormalClosure, "idle")
				return
			}
		case <-prog.C:
			st.progress()
		case <-ping.C:
			ctx, cancel := context.WithTimeout(st.ctx, 10*time.Second)
			err := st.ws.Ping(ctx)
			cancel()
			if err != nil {
				st.finish(websocket.StatusGoingAway, "ping failed")
				return
			}
		}
	}
}

// ttsProgressEvery is how long the app may hear nothing while a sentence is
// outstanding: past it a {"type":"progress"} frame says lasso is still working
// (an app that gives up after a few quiet seconds is kept waiting only while the
// upstream is). The frame names the oldest sentence still outstanding.
const ttsProgressEvery = 800 * time.Millisecond

func (st *ttsStream) progress() {
	if time.Since(time.Unix(0, st.wrote.Load())) < ttsProgressEvery {
		return
	}
	st.mu.Lock()
	var oldest *ttsSentence
	for _, s := range st.live {
		if oldest == nil || s.n < oldest.n {
			oldest = s
		}
	}
	st.mu.Unlock()
	if oldest == nil {
		return
	}
	_ = st.sendFor(oldest, jsonFrame(map[string]any{"type": "progress", "id": oldest.id}))
}

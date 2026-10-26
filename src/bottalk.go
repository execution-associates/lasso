package main

// Talk: an OpenAI-compatible door into a claude bot (docs/design/bots.md,
// "Talk"), so a chat app that speaks Chat Completions (Conduit, on a client's
// phone) can talk to the bot on its own URL and key.
//
//	GET  /talk/<bot>/v1/models             one model, the bot
//	POST /talk/<bot>/v1/chat/completions   a turn, streamed or not
//
// The message goes in through lasso's channel (botchannel.go) as a bot_events
// row of kind "talk" carrying a chat_id, and the bot answers with the
// channel's reply tool, which lands in bot_talk_replies (over HTTP from a
// bot on lasso's machine, through the spool's outbox from one on another
// host). The reply lives in lasso.db rather than in memory because two
// lassos share one db: the channel may post to the one the phone is not
// waiting on.
//
// /talk/ is exempt from UI_AUTH and passes the network listeners unproven
// (selfCredentialed): the bot's Talk key is its only credential, as a job's
// webhook key is. At the edge it sits behind an Access Bypass policy.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const botTalkSchema = `
CREATE TABLE IF NOT EXISTS bot_talks (
	chat_id    TEXT PRIMARY KEY,
	bot        TEXT NOT NULL,
	event_id   INTEGER NOT NULL DEFAULT 0,
	state      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	ended_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS bot_talks_bot ON bot_talks(bot, created_at);
CREATE TABLE IF NOT EXISTS bot_talk_replies (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	bot        TEXT NOT NULL,
	chat_id    TEXT NOT NULL,
	text       TEXT NOT NULL,
	done       INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	taken      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS bot_talk_replies_chat ON bot_talk_replies(chat_id, id);
`

const (
	// Conduit gives up after 5 minutes without a byte and 30 minutes in all;
	// Cloudflare cuts a response that is silent for 100 s. A keepalive every
	// 15 s keeps both quiet, and the wait ends before Conduit's own cap.
	botTalkKeepalive = 15 * time.Second
	botTalkMaxWait   = 25 * time.Minute
	// The request body, images included (base64 inflates them by a third).
	botTalkBodyMax = 25 << 20
	// One reply's text, as the channel sends it.
	botTalkReplyMax = 64 << 10
	botTalkImageMax = 10 << 20
	botTalkImages   = 4
	// Images a client sent stay in the bot's folder this long.
	botTalkImageTTL = 7 * 24 * time.Hour
	// A reply may answer its message, or be carried into the next request
	// as a late one, for this long.
	botTalkReplyTTL = 24 * time.Hour
	// Activity: plain names only the kind of each step, detailed adds its
	// subject.
	botTalkActivityPlain    = "plain"
	botTalkActivityDetailed = "detailed"
)

// botTalkLookLocal/Remote are how often a waiting request reads the bot's
// state and transcript: each read is a herdr call plus a transcript page,
// over SFTP for a bot on another host. A test shortens these.
var (
	botTalkLookLocal  = 2 * time.Second
	botTalkLookRemote = 4 * time.Second
	botTalkPoll       = time.Second
	// How long the bot must read idle before lasso stops waiting for a reply
	// and answers with what the bot wrote in its terminal, or ends a turn the
	// bot already replied to with done=false.
	botTalkIdleGrace = 10 * time.Second
	// How long the bot must read blocked before the phone is told so.
	botTalkBlockedGrace = 30 * time.Second
)

var errTalkUnknownChat = errors.New("no message with that chat_id is waiting for a reply (it may be over a day old)")

// --- storage -----------------------------------------------------------------

// botTalkSettings reads the bot's Talk key ("" = off) and activity mode.
func botTalkSettings(name string) (key, activity string, err error) {
	err = db.QueryRow(`SELECT talk_key, talk_activity FROM bots WHERE name = ?`, name).Scan(&key, &activity)
	if activity != botTalkActivityDetailed {
		activity = botTalkActivityPlain
	}
	return key, activity, err
}

func setBotTalkKey(name, key string) error {
	_, err := db.Exec(`UPDATE bots SET talk_key = ? WHERE name = ?`, key, name)
	return err
}

func setBotTalkActivity(name, activity string) error {
	if activity != botTalkActivityPlain && activity != botTalkActivityDetailed {
		return fmt.Errorf("activity must be %q or %q", botTalkActivityPlain, botTalkActivityDetailed)
	}
	_, err := db.Exec(`UPDATE bots SET talk_activity = ? WHERE name = ?`, activity, name)
	return err
}

func newBotTalkKey() string { return "lt-" + botRandomKey(32) }

// deleteBotTalksOf forgets a deleted bot's conversations.
func deleteBotTalksOf(bot string) {
	_, _ = db.Exec(`DELETE FROM bot_talk_replies WHERE bot = ?`, bot)
	_, _ = db.Exec(`DELETE FROM bot_talks WHERE bot = ?`, bot)
}

// botTalksMaintain drops conversations, replies and delivered talk events
// past the reply window (botJobsMaintain).
func botTalksMaintain(now time.Time) {
	cut := nowStamp(now.Add(-2 * botTalkReplyTTL))
	_, _ = db.Exec(`DELETE FROM bot_talk_replies WHERE created_at < ?`, cut)
	_, _ = db.Exec(`DELETE FROM bot_talks WHERE created_at < ?`, cut)
	_, _ = db.Exec(`DELETE FROM bot_events WHERE kind = 'talk' AND status IN ('delivered', 'dropped') AND created_at < ?`, cut)
}

// enqueueBotTalk queues the client's turn for the bot's channel and opens the
// conversation the reply will be matched to.
func enqueueBotTalk(bot, chatID, content, source string, now time.Time) (int64, error) {
	stamp := nowStamp(now)
	res, err := db.Exec(`INSERT INTO bot_events (bot, job_id, job, kind, content, status, source, created_at, fired_at, chat_id)
		VALUES (?, 0, '', 'talk', ?, 'pending', ?, ?, ?, ?)`, bot, content, source, stamp, stamp, chatID)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO bot_talks (chat_id, bot, event_id, state, created_at) VALUES (?, ?, ?, 'open', ?)`,
		chatID, bot, id, stamp); err != nil {
		return 0, err
	}
	botEventQueued(bot, stamp)
	return id, nil
}

// botTalkReply records one reply the bot sent through its channel and wakes
// whoever waits on that conversation. A reply after the conversation ended
// (the phone left, or the bot already said done) is kept as a late one, for
// the client's next request to carry.
func botTalkReply(bot, chatID, text string, done bool, now time.Time) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("text is empty")
	}
	if len(text) > botTalkReplyMax {
		return fmt.Errorf("text is over %d KB", botTalkReplyMax>>10)
	}
	if !utf8.ValidString(text) {
		return errors.New("text must be UTF-8")
	}
	var created string
	if err := db.QueryRow(`SELECT created_at FROM bot_talks WHERE chat_id = ? AND bot = ?`, chatID, bot).Scan(&created); err != nil {
		return errTalkUnknownChat
	}
	if t, ok := parseStamp(created); !ok || now.Sub(t) > botTalkReplyTTL {
		return errTalkUnknownChat
	}
	if _, err := db.Exec(`INSERT INTO bot_talk_replies (bot, chat_id, text, done, created_at) VALUES (?, ?, ?, ?, ?)`,
		bot, chatID, text, done, nowStamp(now)); err != nil {
		return err
	}
	botTalkWake.notify(chatID)
	return nil
}

// botTalkWake wakes a waiting request as soon as its reply lands. Another
// lasso on the same db records replies without waking this one; the wait's
// own poll catches those.
var botTalkWake = newWakeSet()

type wakeSet struct {
	sync.Mutex
	ch map[string]chan struct{}
}

func newWakeSet() *wakeSet { return &wakeSet{ch: map[string]chan struct{}{}} }

func (s *wakeSet) waiter(key string) <-chan struct{} {
	s.Lock()
	defer s.Unlock()
	c, ok := s.ch[key]
	if !ok {
		c = make(chan struct{})
		s.ch[key] = c
	}
	return c
}

func (s *wakeSet) notify(key string) {
	s.Lock()
	defer s.Unlock()
	if c, ok := s.ch[key]; ok {
		close(c)
		delete(s.ch, key)
	}
}

// botTalkState says whether a request would reach the bot now:
//
//	ready        the channel is listening and has the reply tool
//	waiting      the bot runs but its channel has not connected yet (still
//	             starting): a message waits for it
//	restart      a channel from before Talk is listening: refused until the
//	             bot restarts onto the current one
//	unavailable  refused (an omp bot, no channel, stopped), with the reason
func botTalkState(rec *botRecord, now time.Time) (state, reason string) {
	if rec.isOmp() {
		return "unavailable", "Talk works with claude bots; this one runs omp."
	}
	if offered, why := botChannelOffered(rec); !offered {
		return "unavailable", "The bot gets no lasso channel: " + why + "."
	}
	if rec.Stopped {
		return "unavailable", "The bot is stopped. Start it to take messages."
	}
	var seen string
	var talk bool
	_ = db.QueryRow(`SELECT channel_seen_at, channel_talk FROM bots WHERE name = ?`, rec.Name).Scan(&seen, &talk)
	t, ok := parseStamp(seen)
	connected := ok && now.Sub(t) < botChannelFresh
	if connected && !talk {
		if rec.Host != "local" {
			// The channel there is that host's lasso: a restart picks up a
			// new one only once it is installed.
			return "restart", "The bot's channel is from before Talk. Update lasso on " + mcpHostName(rec.Host) + " if it is older, then restart the bot."
		}
		return "restart", "The bot's channel is from before Talk. Restart the bot to turn it on."
	}
	if !connected {
		return "waiting", "The bot's channel is not connected yet; messages wait until it is."
	}
	return "ready", ""
}

// --- the request ---------------------------------------------------------------

type talkMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type talkRequest struct {
	Model    string        `json:"model"`
	Messages []talkMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// botTalkTurn is what the bot has not seen yet: the run of user messages after
// the last assistant one. A chat app resends the whole history every time,
// and the bot already holds it. System and developer messages are skipped:
// the bot's own instructions are the ones that count.
func botTalkTurn(msgs []talkMessage) []talkMessage {
	var out []talkMessage
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msgs[i].Role {
		case "user":
			out = append(out, msgs[i])
		case "system", "developer":
			continue
		default:
			i = -1
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

type talkImage struct {
	ext  string
	data []byte
}

// botTalkParts reads one message's content: a string, or an array of parts.
// Text parts are the text; an image part with a data: URL is an image (raster
// only, told by its bytes, as a bot's picture is); an http(s) image link is
// passed on as a line of text, never fetched; anything else is a note that it
// was left out.
func botTalkParts(raw json.RawMessage) (text string, images []talkImage, err error) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil, nil
	}
	var parts []struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		ImageURL json.RawMessage `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, errors.New("a message's content must be a string or an array of parts")
	}
	var lines []string
	for _, p := range parts {
		switch p.Type {
		case "text", "input_text":
			if p.Text != "" {
				lines = append(lines, p.Text)
			}
		case "image_url", "input_image":
			url := ""
			var obj struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(p.ImageURL, &obj) == nil && obj.URL != "" {
				url = obj.URL
			} else {
				_ = json.Unmarshal(p.ImageURL, &url)
			}
			switch {
			case strings.HasPrefix(url, "data:"):
				img, err := botTalkDecodeImage(url)
				if err != nil {
					return "", nil, err
				}
				if len(images) >= botTalkImages {
					return "", nil, fmt.Errorf("at most %d images per message", botTalkImages)
				}
				images = append(images, img)
			case strings.HasPrefix(url, "https://"), strings.HasPrefix(url, "http://"):
				lines = append(lines, "Image link (not opened by lasso): "+url)
			}
		default:
			lines = append(lines, "[An attachment of a kind Talk does not pass on was left out.]")
		}
	}
	return strings.Join(lines, "\n\n"), images, nil
}

func botTalkDecodeImage(url string) (talkImage, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return talkImage{}, errors.New("an image must be a base64 data: URL")
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > botTalkImageMax+3 {
		return talkImage{}, fmt.Errorf("an image is over %d MB", botTalkImageMax>>20)
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		if data, err = base64.RawStdEncoding.DecodeString(payload); err != nil {
			return talkImage{}, errors.New("an image's base64 does not decode")
		}
	}
	if len(data) > botTalkImageMax {
		return talkImage{}, fmt.Errorf("an image is over %d MB", botTalkImageMax>>20)
	}
	ext, ok := botAvatarTypes[http.DetectContentType(data)]
	if !ok {
		return talkImage{}, errors.New("an image must be PNG, JPEG, WebP or GIF")
	}
	return talkImage{ext: ext, data: data}, nil
}

func botTalkDir(dir string) string { return filepath.Join(dir, ".lasso", "talk") }

// botTalkSaveImages writes the turn's images into the bot's folder on its
// host, where the bot can Read them, and clears the ones past their week.
// Each name starts with its unix time, so the sweep needs no stat per file.
func botTalkSaveImages(b Backend, dir, chatID string, images []talkImage, now time.Time) ([]string, error) {
	if len(images) == 0 {
		return nil, nil
	}
	td := botTalkDir(dir)
	if err := b.MkdirAll(td, 0o700); err != nil {
		return nil, err
	}
	if ents, err := b.ReadDir(td); err == nil {
		for _, e := range ents {
			sec, _, _ := strings.Cut(e.Name, "-")
			if n, err := strconv.ParseInt(sec, 10, 64); err == nil && now.Sub(time.Unix(n, 0)) > botTalkImageTTL {
				_ = b.RemoveAll(filepath.Join(td, e.Name))
			}
		}
	}
	var paths []string
	for i, img := range images {
		p := filepath.Join(td, fmt.Sprintf("%d-%s-%d.%s", now.Unix(), chatID, i+1, img.ext))
		if err := b.WriteFile(p, img.data, 0o600); err != nil {
			return nil, err
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// botTalkContent is the event body: the client's words, then a line per
// image saved for the bot to read.
func botTalkContent(text string, paths []string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(text))
	for _, p := range paths {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString("Attached image: " + p)
	}
	return sb.String()
}

// --- the handler ---------------------------------------------------------------

func talkError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"message": msg, "type": "invalid_request_error", "code": code,
	}})
}

// serveBotTalk is /talk/<bot>/v1/…. A wrong key, an unknown bot and a bot
// with Talk off all read the same, so names cannot be probed.
func serveBotTalk(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/talk/")
	name, tail, _ := strings.Cut(rest, "/")
	key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	stored, activity, err := botTalkSettings(name)
	if err != nil || stored == "" || !tokenEqual(strings.TrimSpace(key), stored) {
		talkError(w, http.StatusUnauthorized, "invalid_api_key", "Incorrect API key, or this bot does not take Talk messages.")
		return
	}
	switch strings.TrimSuffix(tail, "/") {
	case "v1/models":
		if r.Method != http.MethodGet {
			talkError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET")
			return
		}
		writeJSON(w, map[string]any{"object": "list", "data": []map[string]any{botTalkModel(name)}})
	case "v1/models/" + name:
		writeJSON(w, botTalkModel(name))
	case "v1/chat/completions":
		if r.Method != http.MethodPost {
			talkError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST")
			return
		}
		serveBotTalkCompletion(w, r, name, activity)
	default:
		talkError(w, http.StatusNotFound, "not_found", "Talk serves v1/models and v1/chat/completions.")
	}
}

func botTalkModel(name string) map[string]any {
	return map[string]any{"id": name, "object": "model", "created": 0, "owned_by": "lasso"}
}

func serveBotTalkCompletion(w http.ResponseWriter, r *http.Request, name, activity string) {
	var in talkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, botTalkBodyMax)).Decode(&in); err != nil {
		talkError(w, http.StatusBadRequest, "invalid_body", "The body must be a chat completion request (JSON, at most 25 MB).")
		return
	}
	turn := botTalkTurn(in.Messages)
	if len(turn) == 0 {
		talkError(w, http.StatusBadRequest, "no_user_message", "The request has no user message after the last assistant one.")
		return
	}
	var texts []string
	var images []talkImage
	for _, m := range turn {
		text, imgs, err := botTalkParts(m.Content)
		if err != nil {
			talkError(w, http.StatusBadRequest, "invalid_content", err.Error())
			return
		}
		if text != "" {
			texts = append(texts, text)
		}
		images = append(images, imgs...)
	}
	if len(images) > botTalkImages {
		talkError(w, http.StatusBadRequest, "invalid_content", fmt.Sprintf("at most %d images per turn", botTalkImages))
		return
	}
	text := strings.Join(texts, "\n\n")
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		talkError(w, http.StatusBadRequest, "no_user_message", "The message is empty.")
		return
	}
	rec, err := getBot(name)
	if err != nil {
		talkError(w, http.StatusServiceUnavailable, "bot_unavailable", "The bot is gone.")
		return
	}
	now := time.Now()
	if state, reason := botTalkState(rec, now); state == "unavailable" || state == "restart" {
		talkError(w, http.StatusServiceUnavailable, "bot_unavailable", reason)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		talkError(w, http.StatusServiceUnavailable, "bot_unavailable", "lasso cannot reach the bot's host: "+err.Error())
		return
	}
	chatID := randHex(4)
	paths, err := botTalkSaveImages(b, expandTildeOn(b, rec.Dir), chatID, images, now)
	if err != nil {
		talkError(w, http.StatusServiceUnavailable, "bot_unavailable", "lasso could not save the image for the bot: "+err.Error())
		return
	}
	eventID, err := enqueueBotTalk(name, chatID, botTalkContent(text, paths), hookSource(r), now)
	if err != nil {
		talkError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	var out talkOut
	if in.Stream {
		sse, err := newTalkSSE(w, chatID, name, now)
		if err != nil {
			talkError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		out = sse
	} else {
		out = &talkJSON{w: w, id: chatID, model: name, created: now.Unix()}
	}
	c := &botTalkCall{
		rec: rec, b: b, chatID: chatID, eventID: eventID, out: out,
		stream: in.Stream, detailed: activity == botTalkActivityDetailed,
		look: botTalkLookLocal, steps: map[string]bool{},
	}
	if rec.Host != "local" {
		c.look = botTalkLookRemote
	}
	c.run(r.Context(), now)
}

// --- the wait --------------------------------------------------------------------

// talkOut is where a request's answer goes: an SSE stream of chunks, or one
// JSON body written at the end.
type talkOut interface {
	keepalive() error
	content(s string) error
	reasoning(s string) error
	finish() error
}

type talkSSE struct {
	w       http.ResponseWriter
	fl      http.Flusher
	id      string
	model   string
	created int64
	wrote   bool
}

func newTalkSSE(w http.ResponseWriter, id, model string, now time.Time) (*talkSSE, error) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &talkSSE{w: w, fl: fl, id: "chatcmpl-" + id, model: model, created: now.Unix()}
	return s, s.chunk(map[string]any{"role": "assistant", "content": ""}, nil)
}

func (s *talkSSE) chunk(delta map[string]any, finish any) error {
	b, _ := json.Marshal(map[string]any{
		"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
	})
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

// keepalive is an SSE comment: a chat client's parser drops it, and it is
// bytes on the wire for every idle timer between here and the phone.
func (s *talkSSE) keepalive() error {
	if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

func (s *talkSSE) content(text string) error {
	if s.wrote {
		text = "\n\n" + text
	}
	s.wrote = true
	return s.chunk(map[string]any{"content": text}, nil)
}

func (s *talkSSE) reasoning(text string) error {
	return s.chunk(map[string]any{"reasoning_content": text}, nil)
}

func (s *talkSSE) finish() error {
	if !s.wrote {
		// Conduit treats a stream with no content as a failure.
		if err := s.content("(No answer.)"); err != nil {
			return err
		}
	}
	if err := s.chunk(map[string]any{}, "stop"); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

// talkJSON answers stream:false. While it waits it writes spaces: leading
// whitespace is valid JSON, and it keeps the same idle timers quiet.
type talkJSON struct {
	w       http.ResponseWriter
	id      string
	model   string
	created int64
	started bool
	text    []string
}

func (j *talkJSON) start() {
	if !j.started {
		j.started = true
		j.w.Header().Set("Content-Type", "application/json")
		j.w.WriteHeader(http.StatusOK)
	}
}

func (j *talkJSON) keepalive() error {
	j.start()
	if _, err := io.WriteString(j.w, " "); err != nil {
		return err
	}
	if fl, ok := j.w.(http.Flusher); ok {
		fl.Flush()
	}
	return nil
}

func (j *talkJSON) content(text string) error { j.text = append(j.text, text); return nil }
func (j *talkJSON) reasoning(string) error    { return nil }

func (j *talkJSON) finish() error {
	j.start()
	text := strings.Join(j.text, "\n\n")
	if text == "" {
		text = "(No answer.)"
	}
	return json.NewEncoder(j.w).Encode(map[string]any{
		"id": "chatcmpl-" + j.id, "object": "chat.completion", "created": j.created, "model": j.model,
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

// botTalkCall is one request waiting on the bot.
type botTalkCall struct {
	rec      *botRecord
	b        Backend
	chatID   string
	eventID  int64
	out      talkOut
	stream   bool
	detailed bool
	look     time.Duration

	lastReply    int64
	replied      bool
	taken        []int64
	steps        map[string]bool
	lastLabel    string
	idleSince    time.Time
	blockedSince time.Time
	blockedSaid  bool
}

// run waits for the bot's answer until it is done, the client leaves, or the
// wait runs out, and records how the conversation ended.
func (c *botTalkCall) run(ctx context.Context, now time.Time) {
	deadline := now.Add(botTalkMaxWait)
	nextKeep := now.Add(botTalkKeepalive)
	nextLook := now.Add(c.look)
	if err := c.late(now); err != nil {
		c.end("left", true)
		return
	}
	for {
		wake := botTalkWake.waiter(c.chatID)
		done, err := c.pull()
		if err != nil {
			c.end("left", true)
			return
		}
		if done {
			c.end("answered", false)
			return
		}
		now = time.Now()
		if !now.Before(deadline) {
			_ = c.out.content("I'm still working on this. Ask again in a few minutes and I'll pick it up.")
			c.end("left", false)
			return
		}
		if !now.Before(nextLook) {
			nextLook = now.Add(c.look)
			if fin, err := c.watch(now); err != nil {
				c.end("left", true)
				return
			} else if fin {
				c.end("answered", false)
				return
			}
		}
		if !now.Before(nextKeep) {
			nextKeep = now.Add(botTalkKeepalive)
			if err := c.out.keepalive(); err != nil {
				c.end("left", true)
				return
			}
		}
		select {
		case <-ctx.Done():
			c.end("left", true)
			return
		case <-wake:
		case <-time.After(botTalkPoll):
		}
	}
}

// end closes the answer and the conversation. gone means the client cannot
// be written to any more. Replies the client got are marked taken (a stream
// marks each as it goes); a conversation that ended without the bot's last
// word ("left") keeps its later replies for the next request to carry.
func (c *botTalkCall) end(state string, gone bool) {
	if !gone && c.out.finish() == nil {
		c.markTaken()
	}
	_, _ = db.Exec(`UPDATE bot_talks SET state = ?, ended_at = ? WHERE chat_id = ? AND state = 'open'`, state, nowStamp(time.Now()), c.chatID)
}

func (c *botTalkCall) markTaken() {
	for _, id := range c.taken {
		_, _ = db.Exec(`UPDATE bot_talk_replies SET taken = 1 WHERE id = ?`, id)
	}
	c.taken = nil
}

// late carries replies that missed their request (the client left, or the
// wait ran out) into this one, ahead of this turn's answer.
func (c *botTalkCall) late(now time.Time) error {
	rows, err := db.Query(`SELECT r.id, r.text FROM bot_talk_replies r JOIN bot_talks t ON t.chat_id = r.chat_id
		WHERE r.bot = ? AND r.taken = 0 AND r.chat_id != ? AND r.created_at > ? AND (t.state != 'open' OR t.created_at < ?)
		ORDER BY r.id`, c.rec.Name, c.chatID, nowStamp(now.Add(-botTalkReplyTTL)), nowStamp(now.Add(-botTalkMaxWait-time.Minute)))
	if err != nil {
		return nil
	}
	var texts []string
	for rows.Next() {
		var id int64
		var text string
		if rows.Scan(&id, &text) == nil {
			c.taken = append(c.taken, id)
			texts = append(texts, text)
		}
	}
	rows.Close()
	if len(texts) == 0 {
		return nil
	}
	return c.out.content("(An answer to an earlier message, which did not reach you then:)\n\n" + strings.Join(texts, "\n\n") + "\n\n---")
}

// pull hands the client every reply that arrived since the last pull, and
// says whether one of them was the bot's last word.
func (c *botTalkCall) pull() (bool, error) {
	rows, err := db.Query(`SELECT id, text, done FROM bot_talk_replies WHERE chat_id = ? AND id > ? ORDER BY id`, c.chatID, c.lastReply)
	if err != nil {
		return false, nil
	}
	type reply struct {
		id   int64
		text string
		done bool
	}
	var got []reply
	for rows.Next() {
		var rp reply
		if rows.Scan(&rp.id, &rp.text, &rp.done) == nil {
			got = append(got, rp)
		}
	}
	rows.Close()
	done := false
	for _, rp := range got {
		c.lastReply = rp.id
		c.replied = true
		c.taken = append(c.taken, rp.id)
		if err := c.out.content(rp.text); err != nil {
			return false, err
		}
		if c.stream {
			c.markTaken()
		}
		if rp.done {
			done = true
		}
	}
	return done, nil
}

// talkLook is what a waiting request reads about the bot: its state, and its
// transcript's rows when asked for them.
type talkLook struct {
	state string
	items []chatItem
}

// botTalkLookFn reads the bot's state (Claude Code's session registry, else
// herdr) and, when transcript is set, its chat rows with channel deliveries.
// A test replaces it.
var botTalkLookFn = func(b Backend, rec *botRecord, transcript bool) talkLook {
	p, ok := findBotPane(b, rec)
	if !ok {
		return talkLook{state: "stopped"}
	}
	_, paneStatus := paneAgentPresence(p)
	lk := talkLook{state: "idle"}
	if s, found := findBotSession(readClaudeSessions(b), rec.Name, expandTildeOn(b, rec.Dir)); found && s.agentStatus() != "" {
		lk.state = s.agentStatus()
	} else if paneStatus == "working" || paneStatus == "blocked" {
		lk.state = paneStatus
	}
	if transcript {
		lk.items = buildChatPayload(b, p, "", true).Items
	}
	return lk
}

// watch is the slower half of the wait: the event's fate, the bot's activity
// and state, and the answer when the bot wrote it in its terminal instead of
// replying. It says whether the request is answered.
func (c *botTalkCall) watch(now time.Time) (bool, error) {
	var status, reason string
	if err := db.QueryRow(`SELECT status, reason FROM bot_events WHERE id = ?`, c.eventID).Scan(&status, &reason); err != nil {
		status = "dropped"
		reason = "it was removed"
	}
	if status == "dropped" {
		return true, c.out.content("(lasso could not get this message to the bot: " + reason + ")")
	}
	if status == "pending" {
		// The channel has not taken it yet: nothing in the session to read.
		return false, nil
	}
	delivered := status == "delivered"
	lk := botTalkLookFn(c.b, c.rec, c.stream || !c.replied)
	steps, prose, _ := botTalkSteps(lk.items, c.chatID, c.detailed)
	if c.stream {
		for _, s := range steps {
			if c.steps[s.key] {
				continue
			}
			c.steps[s.key] = true
			if s.text == c.lastLabel {
				continue
			}
			c.lastLabel = s.text
			if err := c.out.reasoning(s.text + "\n"); err != nil {
				return false, err
			}
		}
	}
	if lk.state == "blocked" {
		if c.blockedSince.IsZero() {
			c.blockedSince = now
		}
		if !c.blockedSaid && now.Sub(c.blockedSince) >= botTalkBlockedGrace {
			c.blockedSaid = true
			if err := c.out.reasoning("Waiting on something on my end; this may take a while.\n"); err != nil {
				return false, err
			}
		}
	} else {
		c.blockedSince = time.Time{}
	}
	if !delivered || lk.state != "idle" {
		c.idleSince = time.Time{}
		return false, nil
	}
	if c.idleSince.IsZero() {
		c.idleSince = now
	}
	if now.Sub(c.idleSince) < botTalkIdleGrace {
		return false, nil
	}
	if c.replied {
		// It said "on it" and then finished without a last word.
		return true, nil
	}
	if prose != "" {
		return true, c.out.content(prose)
	}
	return false, nil
}

// talkStep is one line of activity: key dedupes it across reads.
type talkStep struct {
	key, text string
}

// botTalkPlainSteps names a step by its kind alone: never its subject, a
// path, a command or a server's name.
var botTalkPlainSteps = map[string]string{
	"shell":  "Running a command",
	"read":   "Reading a file",
	"edit":   "Editing a file",
	"write":  "Editing a file",
	"search": "Searching files",
	"web":    "Looking something up online",
	"eval":   "Running code",
	"todo":   "Planning",
	"task":   "Handing part of this to a helper",
	"image":  "Working with an image",
}

// botTalkSteps reads the bot's answer to chatID out of its transcript: the
// rows after the channel delivery carrying it, up to the next message of any
// kind (another delivery, or the human typing in the terminal). One session
// serves every caller, so nothing past that point is this client's to see.
// It returns the activity lines, the bot's prose (the fallback answer when it
// never calls reply), and whether the delivery was found.
func botTalkSteps(items []chatItem, chatID string, detailed bool) ([]talkStep, string, bool) {
	marker := `chat_id="` + chatID + `"`
	start := -1
	for i, it := range items {
		if it.Kind == "incoming" && strings.Contains(it.Text, marker) {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, "", false
	}
	var steps []talkStep
	var prose []string
	for _, it := range items[start+1:] {
		if it.Kind == "incoming" || it.Kind == "user" {
			break
		}
		switch {
		case it.Kind == "tool" && it.Tool != nil:
			t := it.Tool
			if strings.Contains(t.Name, botChannelName) || t.Family == "ask" || t.Family == "plan" {
				continue
			}
			key := "tool:" + t.CallID
			if t.CallID == "" {
				key = "tool:" + it.ID
			}
			text := botTalkPlainSteps[t.Family]
			if text == "" {
				text = "Using a tool"
			}
			if detailed {
				text = t.Title
				if t.Subject != "" {
					text += " · " + t.Subject
				}
				text = truncRunes(text, 120)
			}
			steps = append(steps, talkStep{key: key, text: text})
		case it.Kind == "agent" && !it.Thinking && strings.TrimSpace(it.Text) != "":
			prose = append(prose, strings.TrimSpace(it.Text))
			if detailed {
				steps = append(steps, talkStep{key: "say:" + it.ID, text: truncRunes(previewText(it.Text), 200)})
			}
		}
	}
	return steps, strings.Join(prose, "\n\n"), true
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// --- the channel's replies -----------------------------------------------------

// botTalkOutboxEntry is one reply a spool channel left in its outbox for the
// spool loop to collect (botspool.go).
type botTalkOutboxEntry struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	Done   bool   `json:"done"`
}

func botSpoolOutbox(spool string) string { return filepath.Join(spool, "outbox") }

// botSpoolCollectReplies reads a remote bot's outbox: each file is recorded,
// then removed. One that is not a reply, or answers no open conversation, is
// logged and removed: the channel already told the bot "sent".
func botSpoolCollectReplies(b Backend, bot, spool string, now time.Time) error {
	out := botSpoolOutbox(spool)
	ents, err := b.ReadDir(out)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if !e.Dir && strings.HasSuffix(e.Name, ".json") && !strings.HasPrefix(e.Name, ".") {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(out, n)
		raw, err := b.ReadFile(p)
		if err != nil {
			continue
		}
		var e botTalkOutboxEntry
		if err := json.Unmarshal(bytes.TrimSpace(raw), &e); err != nil {
			log.Printf("bots:     %s: outbox %s is not a reply; removing it", bot, n)
		} else if err := botTalkReply(bot, e.ChatID, e.Text, e.Done, now); err != nil {
			log.Printf("bots:     %s: a reply for %s: %v", bot, e.ChatID, err)
		}
		if err := b.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}

// --- /api/bots/<name>/talk -------------------------------------------------------

type botTalkView struct {
	Enabled  bool   `json:"enabled"`
	Path     string `json:"path"`
	Key      string `json:"key,omitempty"`
	Model    string `json:"model"`
	Activity string `json:"activity"`
	// State is botTalkState's: ready, waiting, restart or unavailable.
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	LastAt string `json:"last_at,omitempty"`
}

func botTalkViewOf(rec *botRecord, now time.Time) (botTalkView, error) {
	key, activity, err := botTalkSettings(rec.Name)
	if err != nil {
		return botTalkView{}, err
	}
	v := botTalkView{Enabled: key != "", Path: "/talk/" + rec.Name + "/v1", Key: key, Model: rec.Name, Activity: activity}
	v.State, v.Reason = botTalkState(rec, now)
	var last *string
	_ = db.QueryRow(`SELECT MAX(created_at) FROM bot_talks WHERE bot = ?`, rec.Name).Scan(&last)
	if last != nil {
		v.LastAt = *last
	}
	return v, nil
}

// botTalkEnable turns Talk on with a new key; already on, it changes nothing.
func botTalkEnable(rec *botRecord) error {
	if rec.isOmp() {
		return errors.New("Talk works with claude bots; this one runs omp")
	}
	key, _, err := botTalkSettings(rec.Name)
	if err != nil || key != "" {
		return err
	}
	return setBotTalkKey(rec.Name, newBotTalkKey())
}

// botTalkRotate gives a bot with Talk on a new key: the old one stops working
// at once.
func botTalkRotate(rec *botRecord) error {
	key, _, err := botTalkSettings(rec.Name)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("Talk is off for this bot")
	}
	return setBotTalkKey(rec.Name, newBotTalkKey())
}

// serveBotTalkSettings is the Talk tab:
//
//	GET  talk            the view
//	POST talk {activity} plain or detailed
//	POST talk/enable     on, with a new key
//	POST talk/rotate     a new key
//	POST talk/disable    off; the key is forgotten
func serveBotTalkSettings(w http.ResponseWriter, r *http.Request, rec *botRecord, rest string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodGet && rest != "" {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var err error
	if r.Method == http.MethodPost {
		switch rest {
		case "":
			var in struct {
				Activity string `json:"activity"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			err = setBotTalkActivity(rec.Name, in.Activity)
		case "enable":
			err = botTalkEnable(rec)
		case "rotate":
			err = botTalkRotate(rec)
		case "disable":
			err = setBotTalkKey(rec.Name, "")
		default:
			http.NotFound(w, r)
			return
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v, err := botTalkViewOf(rec, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

package main

// The client API: /client/v1, the surface the phone apps use to talk to the
// bots a person was granted (docs/design/client-api.md is the contract).
//
// It is self-credentialed like /talk and /hooks/bots: exempt from UI_AUTH,
// unproven on the network listeners, cross-site exempt and bypassed at Access.
// The device token in the Authorization header is the only credential and the
// only identity: nothing in a body, a query or a message can name a user, an
// owner or a verification. It reaches nothing of /api.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	clientAPIVersion   = 1
	clientStreamMax    = 55 * time.Minute
	clientKeepalive    = 15 * time.Second
	clientStatePoll    = 3 * time.Second
	clientStreamsPerDv = 3
)

// --- rate limits -----------------------------------------------------------------------

// clientWindow counts events per key over a sliding window, in memory.
type clientWindow struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newClientWindow() *clientWindow { return &clientWindow{hits: map[string][]time.Time{}} }

func (w *clientWindow) prune(key string, per time.Duration, now time.Time) []time.Time {
	hs := w.hits[key]
	i := 0
	for i < len(hs) && now.Sub(hs[i]) >= per {
		i++
	}
	hs = hs[i:]
	if len(hs) == 0 {
		delete(w.hits, key)
	} else {
		w.hits[key] = hs
	}
	return hs
}

// count says how many hits the key has in the window.
func (w *clientWindow) count(key string, per time.Duration, now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.prune(key, per, now))
}

// hit records one; allow records one only when under limit, and says so.
func (w *clientWindow) hit(key string, per time.Duration, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.prune(key, per, now)
	w.hits[key] = append(w.hits[key], now)
}

func (w *clientWindow) allow(key string, limit int, per time.Duration, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.prune(key, per, now)) >= limit {
		return false
	}
	w.hits[key] = append(w.hits[key], now)
	return true
}

var (
	clientPairFails = newClientWindow()
	clientLimits    = newClientWindow()
	clientStreamsMu sync.Mutex
	clientStreamCnt = map[string]int{}
)

const (
	clientPairPer        = 10 * time.Minute
	clientPairPerSource  = 10
	clientPairGlobal     = 60
	clientMessagesPerMin = 30
	clientUploadsPerMin  = 20
)

// --- responses ---------------------------------------------------------------------------

func clientHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

func clientJSON(w http.ResponseWriter, status int, v any) {
	clientHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func clientError(w http.ResponseWriter, status int, code, msg string) {
	clientJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func clientFail(w http.ResponseWriter, err error) {
	var ce *clientErr
	if errors.As(err, &ce) {
		clientError(w, ce.Status, ce.Code, ce.Msg)
		return
	}
	clientError(w, http.StatusInternalServerError, "internal", "Something went wrong on lasso's side.")
}

func clientNotFound(w http.ResponseWriter) {
	clientError(w, http.StatusNotFound, "not_found", "Not found.")
}

func clientUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="lasso client"`)
	clientError(w, http.StatusUnauthorized, "unauthorized", "This device is not signed in. Pair it again.")
}

func clientMethod(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	clientError(w, http.StatusMethodNotAllowed, "method_not_allowed", allow+" only.")
}

func clientDecode(w http.ResponseWriter, r *http.Request, max int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			clientError(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is too large.")
		} else {
			clientError(w, http.StatusBadRequest, "invalid_request", "The body must be JSON.")
		}
		return false
	}
	return true
}

// --- the router --------------------------------------------------------------------------

// serveClient is /client/…: the pairing page and the versioned API.
func serveClient(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/client"), "/")
	switch {
	case rest == "pair":
		serveClientPairPage(w, r)
		return
	case rest == "v1/pair":
		serveClientPair(w, r)
		return
	case !strings.HasPrefix(rest, "v1/"):
		clientNotFound(w)
		return
	}
	// Everything else needs a device token. Auth comes before routing, so an
	// unauthenticated caller learns nothing about which routes exist.
	tok, _ := bearerToken(r)
	id, ok := authenticateDeviceToken(tok, time.Now())
	if !ok {
		clientUnauthorized(w)
		return
	}
	segs := strings.Split(strings.TrimPrefix(rest, "v1/"), "/")
	switch {
	case len(segs) == 1 && segs[0] == "me":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		serveClientMe(w, id)
	case len(segs) == 1 && segs[0] == "bots":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		clientJSON(w, http.StatusOK, map[string]any{"bots": clientBotsOf(id, time.Now())})
	case len(segs) == 2 && segs[0] == "bots":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		serveClientBot(w, id, segs[1])
	case len(segs) == 3 && segs[0] == "bots" && segs[2] == "avatar":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		serveClientAvatar(w, r, id, segs[1])
	case len(segs) == 3 && segs[0] == "conversations":
		serveClientConversation(w, r, id, segs[1], segs[2])
	case len(segs) == 2 && segs[0] == "attachments":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			clientMethod(w, "GET")
			return
		}
		serveClientAttachment(w, r, id, segs[1])
	case len(segs) == 2 && segs[0] == "calls":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		serveClientCall(w, id, segs[1])
	case len(segs) == 3 && segs[0] == "calls" && segs[2] == "end":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return
		}
		serveClientCallEnd(w, id, segs[1])
	case len(segs) == 1 && segs[0] == "device":
		if r.Method != http.MethodDelete {
			clientMethod(w, "DELETE")
			return
		}
		_ = revokeAppDevice(id.User.ID, id.Device.ID, time.Now())
		clientJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		clientNotFound(w)
	}
}

// clientGrantedBot answers the bot's record when the token's user holds a grant
// on it; unknown and not-granted read the same.
func clientGrantedBot(id *clientIdentity, name string) (*botRecord, *appGrant, bool) {
	g, err := getAppGrant(id.User.ID, name)
	if err != nil {
		return nil, nil, false
	}
	rec, err := getBot(name)
	if err != nil {
		return nil, nil, false
	}
	return rec, g, true
}

// clientOwnConv answers the conversation when it is the token's user's and the
// grant still stands; anything else reads as not found.
func clientOwnConv(id *clientIdentity, cid string) (*appConv, bool) {
	c, err := convByID(cid)
	if err != nil || c.UserID != id.User.ID {
		return nil, false
	}
	if _, err := getAppGrant(id.User.ID, c.Bot); err != nil {
		return nil, false
	}
	return c, true
}

// --- pairing -------------------------------------------------------------------------------

func serveClientPair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		clientMethod(w, "POST")
		return
	}
	now := time.Now()
	src := hookSource(r)
	// Failures are counted per source and overall; past either limit nothing
	// is even tried. A real code is 128 random bits, so this is hygiene, not
	// the defence.
	if clientPairFails.count("src:"+src, clientPairPer, now) >= clientPairPerSource ||
		clientPairFails.count("all", clientPairPer, now) >= clientPairGlobal {
		w.Header().Set("Retry-After", "600")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many pairing attempts. Try again in a few minutes.")
		return
	}
	var in struct {
		Code     string `json:"code"`
		Label    string `json:"label"`
		Platform string `json:"platform"`
	}
	if !clientDecode(w, r, 4<<10, &in) {
		return
	}
	token, dev, user, err := redeemAppPairing(in.Code, in.Label, in.Platform, now)
	if err != nil {
		if errors.Is(err, errPairingRefused) {
			clientPairFails.hit("src:"+src, clientPairPer, now)
			clientPairFails.hit("all", clientPairPer, now)
			clientError(w, http.StatusUnauthorized, "pairing_failed", "This pairing did not work. Ask for a new link.")
			return
		}
		clientFail(w, err)
		return
	}
	clientJSON(w, http.StatusOK, map[string]any{
		"api":    clientAPIVersion,
		"token":  token,
		"device": map[string]any{"id": dev.ID, "label": dev.Label, "platform": dev.Platform},
		"user":   map[string]any{"id": user.ID, "name": user.Name},
	})
}

// clientPairPage is the page a pairing link opens. It is static: it reads the
// code from the URL fragment (which a browser never sends to a server, so no
// log holds it) and the server's address from its own location, and offers the
// app link lasso://pair?code=…&server=…. Nothing is reflected from the request.
const clientPairPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Pair with lasso</title>
<style nonce="%[1]s">
body{font:16px/1.5 system-ui,sans-serif;margin:0;padding:2rem 1rem;background:#111;color:#eee;display:flex;justify-content:center}
main{max-width:30rem;width:100%%}h1{font-size:1.4rem}a.btn{display:block;text-align:center;background:#e8b04a;color:#111;padding:.9rem 1rem;border-radius:.6rem;font-weight:600;text-decoration:none;margin:1.2rem 0}
code{display:block;word-break:break-all;background:#222;padding:.7rem;border-radius:.5rem;margin:.5rem 0;font-size:.85rem;user-select:all}
.muted{color:#aaa;font-size:.9rem}[hidden]{display:none}
</style></head>
<body><main>
<h1>Pair this phone with lasso</h1>
<p id="ok" hidden>Open the link in the lasso app on this phone. It works once and expires ten minutes after it was made.</p>
<a id="open" class="btn" href="#" hidden>Open in the lasso app</a>
<p id="manual" class="muted" hidden>If the app does not open, copy this into its pairing screen:</p>
<code id="code" hidden></code>
<p id="bad" hidden>This link has no pairing code. Ask for a new one.</p>
<p class="muted">Only open this on the phone you want to pair. The code in this address is a one-time key to your account.</p>
<script nonce="%[1]s">
(function(){
var p=new URLSearchParams(location.hash.slice(1)),c=p.get("code");
if(!c||!/^lpc_[A-Za-z0-9_-]{16,64}$/.test(c)){document.getElementById("bad").hidden=false;return}
var link="lasso://pair?code="+encodeURIComponent(c)+"&server="+encodeURIComponent(location.origin);
var a=document.getElementById("open");a.href=link;a.hidden=false;
document.getElementById("ok").hidden=false;
document.getElementById("manual").hidden=false;
var k=document.getElementById("code");k.textContent=c;k.hidden=false;
history.replaceState(null,"",location.pathname);
setTimeout(function(){location.href=link},300);
})();
</script></main></body></html>`

func serveClientPairPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		clientMethod(w, "GET")
		return
	}
	nonce := randHex(12)
	clientHeaders(w)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Frame-Options", "DENY")
	_, _ = fmt.Fprintf(w, clientPairPage, nonce)
}

// --- me, bots ------------------------------------------------------------------------------

type clientBotView struct {
	Name           string     `json:"name"`
	AvatarURL      string     `json:"avatar_url,omitempty"`
	State          string     `json:"state"`
	StateReason    string     `json:"state_reason,omitempty"`
	Capabilities   clientCaps `json:"capabilities"`
	IsOwner        bool       `json:"is_owner"`
	ConversationID string     `json:"conversation_id"`
}

func clientBotsOf(id *clientIdentity, now time.Time) []clientBotView {
	out := []clientBotView{}
	gs, err := listAppGrants(id.User.ID)
	if err != nil {
		return out
	}
	for _, g := range gs {
		if v, ok := clientBotViewOf(id, g.Bot, now); ok {
			out = append(out, v)
		}
	}
	return out
}

func clientBotViewOf(id *clientIdentity, name string, now time.Time) (clientBotView, bool) {
	rec, g, ok := clientGrantedBot(id, name)
	if !ok {
		return clientBotView{}, false
	}
	conv, err := convFor(id.User.ID, name, now)
	if err != nil {
		return clientBotView{}, false
	}
	state, caps, reason := botClientState(rec, now)
	v := clientBotView{Name: name, State: state, StateReason: reason, Capabilities: caps, IsOwner: g.Owner, ConversationID: conv.ID}
	if rec.AvatarImage != "" {
		_, rev, _ := strings.Cut(rec.AvatarImage, "?")
		v.AvatarURL = "/client/v1/bots/" + url.PathEscape(name) + "/avatar?" + rev
	}
	return v, true
}

func serveClientMe(w http.ResponseWriter, id *clientIdentity) {
	clientJSON(w, http.StatusOK, map[string]any{
		"api":  clientAPIVersion,
		"user": map[string]any{"id": id.User.ID, "name": id.User.Name},
		"device": map[string]any{"id": id.Device.ID, "label": id.Device.Label, "platform": id.Device.Platform,
			"created_at": id.Device.CreatedAt},
		"bots": clientBotsOf(id, time.Now()),
	})
}

func serveClientBot(w http.ResponseWriter, id *clientIdentity, name string) {
	v, ok := clientBotViewOf(id, name, time.Now())
	if !ok {
		clientNotFound(w)
		return
	}
	clientJSON(w, http.StatusOK, map[string]any{"bot": v})
}

func serveClientAvatar(w http.ResponseWriter, r *http.Request, id *clientIdentity, name string) {
	rec, _, ok := clientGrantedBot(id, name)
	if !ok || rec.AvatarImage == "" {
		clientNotFound(w)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		clientNotFound(w)
		return
	}
	serveBotAvatar(w, r, b, rec)
}

// --- a conversation ------------------------------------------------------------------------

func serveClientConversation(w http.ResponseWriter, r *http.Request, id *clientIdentity, cid, action string) {
	conv, ok := clientOwnConv(id, cid)
	if !ok {
		clientNotFound(w)
		return
	}
	switch action {
	case "messages":
		switch r.Method {
		case http.MethodGet:
			serveClientMessages(w, r, conv)
		case http.MethodPost:
			serveClientSend(w, r, id, conv)
		default:
			clientMethod(w, "GET, POST")
		}
	case "events":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		serveClientEvents(w, r, id, conv)
	case "attachments":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return
		}
		serveClientUpload(w, r, id, conv)
	case "calls":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return
		}
		serveClientCallStart(w, id, conv)
	default:
		clientNotFound(w)
	}
}

func serveClientMessages(w http.ResponseWriter, r *http.Request, conv *appConv) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			clientError(w, http.StatusBadRequest, "invalid_request", "limit must be 1-100.")
			return
		}
		limit = n
	}
	parse := func(k string) (int64, bool) {
		v := q.Get(k)
		if v == "" {
			return 0, true
		}
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil && n > 0
	}
	before, ok1 := parse("before")
	after, ok2 := parse("after")
	if !ok1 || !ok2 || (before > 0 && after > 0) {
		clientError(w, http.StatusBadRequest, "invalid_request", "before and after are cursors from earlier pages; use one.")
		return
	}
	// The events cursor is read first: a stream opened with it as
	// Last-Event-ID misses nothing that landed while this page was read.
	cursor := newestEventSeq(conv.ID)
	clientReconcile(conv.ID, time.Now())
	msgs, err := pageMessages(conv.ID, before, after, limit)
	if err != nil {
		clientFail(w, err)
		return
	}
	var older any
	if after == 0 && len(msgs) > 0 {
		first, _ := strconv.ParseInt(msgs[0].Cursor, 10, 64)
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq < ?`, conv.ID, first).Scan(&n)
		if n > 0 {
			older = msgs[0].Cursor
		}
	}
	out := map[string]any{"conversation_id": conv.ID, "messages": msgs, "events_cursor": strconv.FormatInt(cursor, 10), "older_cursor": older}
	clientJSON(w, http.StatusOK, out)
}

func serveClientSend(w http.ResponseWriter, r *http.Request, id *clientIdentity, conv *appConv) {
	now := time.Now()
	if !clientLimits.allow("msg:"+id.User.ID, clientMessagesPerMin, time.Minute, now) {
		w.Header().Set("Retry-After", "30")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Slow down: too many messages.")
		return
	}
	var in clientSendIn
	if !clientDecode(w, r, 256<<10, &in) {
		return
	}
	m, replay, err := clientSend(id, conv, in, now)
	if err != nil {
		clientFail(w, err)
		return
	}
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	clientJSON(w, status, map[string]any{"message": m, "replayed": replay})
}

// --- attachments ---------------------------------------------------------------------------

func serveClientUpload(w http.ResponseWriter, r *http.Request, id *clientIdentity, conv *appConv) {
	now := time.Now()
	if !clientLimits.allow("up:"+id.User.ID, clientUploadsPerMin, time.Minute, now) {
		w.Header().Set("Retry-After", "30")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Slow down: too many uploads.")
		return
	}
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_attachments WHERE user_id = ? AND direction = 'in' AND message_id = ''`, id.User.ID).Scan(&pending)
	if pending >= clientPendingUploads {
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many uploads waiting to be sent in a message.")
		return
	}
	if r.ContentLength > clientAttachMax {
		clientError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("A file may be at most %d MB.", clientAttachMax>>20))
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, clientAttachMax+1))
	if err != nil {
		clientError(w, http.StatusBadRequest, "invalid_request", "The upload could not be read.")
		return
	}
	if len(data) > clientAttachMax {
		clientError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("A file may be at most %d MB.", clientAttachMax>>20))
		return
	}
	if len(data) == 0 {
		clientError(w, http.StatusBadRequest, "invalid_request", "The file is empty.")
		return
	}
	rec, err := getBot(conv.Bot)
	if err != nil {
		clientNotFound(w)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		clientError(w, http.StatusBadGateway, "bot_unavailable", "lasso cannot reach the bot's host.")
		return
	}
	a, err := clientStoreFile(b, rec, expandTildeOn(b, rec.Dir), conv, "in", r.URL.Query().Get("name"), data)
	if err != nil {
		clientFail(w, err)
		return
	}
	clientJSON(w, http.StatusCreated, map[string]any{"attachment": a.view()})
}

func serveClientAttachment(w http.ResponseWriter, r *http.Request, id *clientIdentity, aid string) {
	a, err := appAttachmentByID(aid)
	if err != nil || a.UserID != id.User.ID {
		clientNotFound(w)
		return
	}
	if _, ok := clientOwnConv(id, a.ConvID); !ok {
		clientNotFound(w)
		return
	}
	rec, err := getBot(a.Bot)
	if err != nil {
		clientNotFound(w)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		clientNotFound(w)
		return
	}
	data, err := clientConfinedRead(b, rec.Host == "local", expandTildeOn(b, rec.Dir), a.Path, clientAttachMax)
	if err != nil {
		clientNotFound(w)
		return
	}
	// Served as what the bytes are, not what the row or anyone's header says.
	if mt, _, ok := clientSniff(data); !ok || mt != a.Mime {
		clientNotFound(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.Mime)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("Referrer-Policy", "no-referrer")
	disp := "attachment"
	if strings.HasPrefix(a.Mime, "image/") || strings.HasPrefix(a.Mime, "audio/") || strings.HasPrefix(a.Mime, "video/") || a.Mime == "application/pdf" {
		disp = "inline"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": a.Name}))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// --- calls ---------------------------------------------------------------------------------

func serveClientCallStart(w http.ResponseWriter, id *clientIdentity, conv *appConv) {
	rec, err := getBot(conv.Bot)
	if err != nil {
		clientNotFound(w)
		return
	}
	now := time.Now()
	if state, _, reason := botClientState(rec, now); state == "stopped" || state == "unavailable" {
		clientError(w, http.StatusConflict, "bot_unavailable", reason)
		return
	}
	call, err := clientStartCall(conv, now)
	if err != nil {
		clientFail(w, err)
		return
	}
	clientJSON(w, http.StatusCreated, map[string]any{"call": call})
}

func clientOwnCall(id *clientIdentity, callID string) (*appCall, bool) {
	c, err := appCallByID(callID)
	if err != nil || c.UserID != id.User.ID {
		return nil, false
	}
	if _, ok := clientOwnConv(id, c.ConvID); !ok {
		return nil, false
	}
	return c, true
}

func serveClientCall(w http.ResponseWriter, id *clientIdentity, callID string) {
	c, ok := clientOwnCall(id, callID)
	if !ok {
		clientNotFound(w)
		return
	}
	clientJSON(w, http.StatusOK, map[string]any{"call": c})
}

func serveClientCallEnd(w http.ResponseWriter, id *clientIdentity, callID string) {
	c, ok := clientOwnCall(id, callID)
	if !ok {
		clientNotFound(w)
		return
	}
	clientEndCall(c, "user", "", time.Now(), true)
	c, _ = appCallByID(callID)
	clientJSON(w, http.StatusOK, map[string]any{"call": c})
}

// --- the stream ----------------------------------------------------------------------------

func clientStreamAcquire(deviceID string) bool {
	clientStreamsMu.Lock()
	defer clientStreamsMu.Unlock()
	if clientStreamCnt[deviceID] >= clientStreamsPerDv {
		return false
	}
	clientStreamCnt[deviceID]++
	return true
}

func clientStreamRelease(deviceID string) {
	clientStreamsMu.Lock()
	defer clientStreamsMu.Unlock()
	if clientStreamCnt[deviceID]--; clientStreamCnt[deviceID] <= 0 {
		delete(clientStreamCnt, deviceID)
	}
}

type clientSSE struct {
	w  http.ResponseWriter
	fl http.Flusher
}

func (s *clientSSE) event(id int64, typ string, data any) error {
	b, _ := json.Marshal(data)
	return s.raw(id, typ, string(b))
}

func (s *clientSSE) raw(id int64, typ, data string) error {
	var sb strings.Builder
	if id > 0 {
		sb.WriteString("id: " + strconv.FormatInt(id, 10) + "\n")
	}
	sb.WriteString("event: " + typ + "\ndata: " + data + "\n\n")
	if _, err := io.WriteString(s.w, sb.String()); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

func (s *clientSSE) keepalive() error {
	if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	s.fl.Flush()
	return nil
}

// clientOutstanding is the newest delivered person's message that the bot has
// not closed with a done message since: what the activity lines are about.
func clientOutstanding(convID string) (string, bool) {
	var mid string
	var seq int64
	if db.QueryRow(`SELECT id, seq FROM app_messages WHERE conv_id = ? AND role = 'user' AND state = 'delivered' ORDER BY seq DESC LIMIT 1`, convID).Scan(&mid, &seq) != nil {
		return "", false
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role = 'bot' AND done = 1 AND seq > ?`, convID, seq).Scan(&n)
	return mid, n == 0
}

func serveClientEvents(w http.ResponseWriter, r *http.Request, id *clientIdentity, conv *appConv) {
	fl, ok := w.(http.Flusher)
	if !ok {
		clientError(w, http.StatusInternalServerError, "internal", "Streaming is not supported here.")
		return
	}
	if !clientLimits.allow("stream:"+id.Device.ID, 20, time.Minute, time.Now()) || !clientStreamAcquire(id.Device.ID) {
		w.Header().Set("Retry-After", "10")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many open streams for this device.")
		return
	}
	defer clientStreamRelease(id.Device.ID)

	last, haveLast := int64(0), false
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		last, haveLast = parseCursor(v)
	} else if v := r.URL.Query().Get("last_event_id"); v != "" {
		last, haveLast = parseCursor(v)
	}
	rec, err := getBot(conv.Bot)
	if err != nil {
		clientNotFound(w)
		return
	}
	b, _ := botBackend(rec.Host)
	clientHeaders(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &clientSSE{w: w, fl: fl}

	now := time.Now()
	resync := false
	if !haveLast {
		last = newestEventSeq(conv.ID)
	} else if oldest := oldestEventSeq(conv.ID); oldest > 0 && last < oldest-1 {
		// Events between Last-Event-ID and now were pruned: the client must
		// re-read the conversation.
		resync = true
	}
	state, _, reason := botClientState(rec, now)
	hello := map[string]any{"conversation_id": conv.ID, "bot_state": state, "events_cursor": strconv.FormatInt(last, 10), "call": nil}
	if reason != "" {
		hello["bot_state_reason"] = reason
	}
	if c, err := openCallOf(conv.ID); err == nil {
		hello["call"] = c
	}
	if s.event(0, "hello", hello) != nil {
		return
	}
	if resync {
		if s.event(0, "resync", map[string]any{"reason": "events_pruned"}) != nil {
			return
		}
		last = newestEventSeq(conv.ID)
	}

	deadline := now.Add(clientStreamMax)
	nextKeep := now.Add(clientKeepalive)
	nextState := now.Add(clientStatePoll)
	look := botTalkLookLocal
	if rec.Host != "local" {
		look = botTalkLookRemote
	}
	nextLook := now.Add(look)
	lastState := state
	seenSteps := map[string]bool{}
	lastLabel := ""
	for {
		wake := clientWake.waiter(conv.ID)
		now = time.Now()
		if !stillAuthorized(id.Device.ID, id.User.ID, conv.Bot) {
			_ = s.event(0, "closed", map[string]any{"reason": "access_revoked"})
			return
		}
		if !now.Before(deadline) {
			_ = s.event(0, "closed", map[string]any{"reason": "reconnect"})
			return
		}
		clientReconcile(conv.ID, now)
		evs, err := eventsAfter(conv.ID, last, 100)
		if err != nil {
			return
		}
		for _, e := range evs {
			last = e.Seq
			if s.raw(e.Seq, e.Type, e.Data) != nil {
				return
			}
		}
		if !now.Before(nextState) {
			nextState = now.Add(clientStatePoll)
			if st, _, why := botClientState(rec, now); st != lastState {
				lastState = st
				d := map[string]any{"state": st}
				if why != "" {
					d["reason"] = why
				}
				if s.event(0, "bot_state", d) != nil {
					return
				}
			}
		}
		if !now.Before(nextLook) && !rec.isOmp() && b != nil {
			nextLook = now.Add(look)
			if mid, outstanding := clientOutstanding(conv.ID); outstanding {
				lk := botTalkLookFn(b, rec, true)
				steps, _, _ := botTalkStepsMarker(lk.items, `message_id="`+mid+`"`, false)
				for _, st := range steps {
					if seenSteps[st.key] {
						continue
					}
					seenSteps[st.key] = true
					if st.text == lastLabel {
						continue
					}
					lastLabel = st.text
					if s.event(0, "activity", map[string]any{"text": st.text, "message_id": mid}) != nil {
						return
					}
				}
			}
		}
		if !now.Before(nextKeep) {
			nextKeep = now.Add(clientKeepalive)
			if s.keepalive() != nil {
				return
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-wake:
		case <-time.After(time.Second):
		}
	}
}

func parseCursor(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	return n, err == nil && n >= 0
}

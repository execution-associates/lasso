package main

// The bot side of the client API (docs/design/client-api.md): what a bot may
// do for the people using the phone app, and how their messages reach it.
//
//   - A person's message is a bot_events row of kind "client" whose meta (set
//     by lasso alone, from the token's user and the grant) says who it is from.
//     The claude channel pushes it as channel attributes; an omp bot gets a
//     prompt with a header line (botOmpClientMessage).
//   - A bot answers with the channel's client_reply and end_call tools (or the
//     same two tools on /mcp, for an omp bot): both name a conversation or a
//     call explicitly, so a reply can only land where it is addressed.
//   - Files the bot sends are read from its folder once, at send time,
//     confined to it, and kept as a copy lasso addresses by id.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	clientTextMax        = 16000
	clientBotTextMax     = 64 << 10
	clientAttachMax      = 20 << 20
	clientAttachPerMsg   = 4
	clientPendingUploads = 20
	clientNameShown      = 80
)

// clientErr is an error with the HTTP shape the API answers it in.
type clientErr struct {
	Status int
	Code   string
	Msg    string
}

func (e *clientErr) Error() string { return e.Msg }

func cerr(status int, code, msg string) *clientErr { return &clientErr{status, code, msg} }

// --- what a bot can do for a client ------------------------------------------------

type clientCaps struct {
	Chat           bool `json:"chat"`
	Voice          bool `json:"voice"`
	AttachmentsIn  bool `json:"attachments_in"`
	AttachmentsOut bool `json:"attachments_out"`
	EndCall        bool `json:"end_call"`
	StreamingText  bool `json:"streaming_text"`
}

var botClientStateCache sync.Map // bot -> clientStateSighting

type clientStateSighting struct {
	state string
	at    time.Time
}

// botClientToolsOnMCP reports whether an omp bot has lasso's MCP server (and so
// the client_reply and client_end_call tools): a bot on lasso's own machine,
// while /mcp is not gated.
func botClientToolsOnMCP(r *botRecord) bool {
	_, ok := botLassoMCP(r)
	return ok
}

// botClientState is the coarse view a person sees of a bot, whether a message
// would reach it, and what the bot can do for them. States: available, busy,
// starting (running, not yet listening: a message waits), stopped, unavailable
// (cannot take app messages now; reason says why).
func botClientState(rec *botRecord, now time.Time) (state string, caps clientCaps, reason string) {
	if rec.Stopped {
		return "stopped", clientCaps{}, "The bot is stopped."
	}
	if rec.isOmp() {
		ob := ompBotFor(rec.Name)
		ob.ensure()
		s := ob.snapshot()
		tools := botClientToolsOnMCP(rec)
		caps = clientCaps{Chat: true, Voice: true, AttachmentsIn: true, AttachmentsOut: tools, EndCall: tools}
		switch {
		case !s.connected || !s.ready:
			return "starting", caps, ""
		case s.streaming || len(s.pending) > 0:
			return "busy", caps, ""
		}
		return "available", caps, ""
	}
	if offered, why := botChannelOffered(rec); !offered {
		return "unavailable", clientCaps{}, "The bot gets no lasso channel: " + why + "."
	}
	var seen string
	var client bool
	_ = db.QueryRow(`SELECT channel_seen_at, channel_client FROM bots WHERE name = ?`, rec.Name).Scan(&seen, &client)
	t, ok := parseStamp(seen)
	connected := ok && now.Sub(t) < botChannelFresh
	if connected && !client {
		return "unavailable", clientCaps{}, "The bot must restart to take messages from the app."
	}
	caps = clientCaps{Chat: true, Voice: true, AttachmentsIn: true, AttachmentsOut: true, EndCall: true}
	if !connected {
		return "starting", caps, ""
	}
	if v, ok := botClientStateCache.Load(rec.Name); ok {
		if s := v.(clientStateSighting); now.Sub(s.at) < 5*time.Second {
			return s.state, caps, ""
		}
	}
	state = "available"
	if b, err := botBackend(rec.Host); err == nil {
		if lk := botTalkLookFn(b, rec, false); lk.state == "working" || lk.state == "blocked" {
			state = "busy"
		}
	}
	botClientStateCache.Store(rec.Name, clientStateSighting{state, now})
	return state, caps, ""
}

// --- files --------------------------------------------------------------------------

// clientAttachTypes are the content types an attachment may be, told by the
// bytes (http.DetectContentType), never by name or the client's header. HEIC is
// not among them: the app converts to JPEG before it uploads.
var clientAttachTypes = map[string]string{
	"image/png":       "png",
	"image/jpeg":      "jpg",
	"image/webp":      "webp",
	"image/gif":       "gif",
	"application/pdf": "pdf",
	"text/plain":      "txt",
	"audio/mpeg":      "mp3",
	"audio/wave":      "wav",
	"audio/aiff":      "aiff",
	"audio/ogg":       "ogg",
	"application/ogg": "ogg",
	"video/mp4":       "mp4",
	"video/webm":      "webm",
	"application/zip": "zip",
}

// clientSniff answers the content type and extension of an attachment, or
// false when its type is not allowed.
func clientSniff(data []byte) (mime, ext string, ok bool) {
	if len(data) == 0 {
		return "", "", false
	}
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	mime, _, _ = strings.Cut(httpDetect(head), ";")
	ext, ok = clientAttachTypes[mime]
	if !ok {
		return "", "", false
	}
	if mime == "text/plain" {
		// The sniff reads the first 512 bytes: the rest must be text too.
		if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
			return "", "", false
		}
	}
	return mime, ext, true
}

func httpDetect(b []byte) string { return http.DetectContentType(b) }

// clientDisplayName makes a file name safe to show: a base name, no control
// characters, bounded.
func clientDisplayName(name, fallbackExt string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = name[strings.LastIndex(name, "/")+1:]
	var sb strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '<' || r == '>' || r == '"' {
			continue
		}
		sb.WriteRune(r)
	}
	n := strings.TrimSpace(sb.String())
	n = strings.Trim(n, ". ")
	if r := []rune(n); len(r) > clientNameShown {
		n = string(r[:clientNameShown])
	}
	if n == "" {
		n = "file." + fallbackExt
	}
	return n
}

func clientUserDir(userID string) string { return filepath.Join(".lasso", "client", userID) }

// clientNoSymlinks refuses a path with a symlink in any component below root,
// and anything that is not a regular file at the end.
func clientNoSymlinks(b Backend, root, rel string) error {
	cur := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		fi, err := b.Lstat(cur)
		if err != nil {
			return errors.New("the file is not there")
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not followed")
		}
		if i == len(parts)-1 && !fi.Mode().IsRegular() {
			return errors.New("not a regular file")
		}
		if i < len(parts)-1 && !fi.IsDir() {
			return errors.New("not a directory")
		}
	}
	return nil
}

// clientConfinedRead reads the file at rel (relative to the bot's folder root)
// without leaving it: no symlink in the path, and on lasso's own machine the
// open itself is confined with os.Root.
func clientConfinedRead(b Backend, local bool, root, rel string, max int64) ([]byte, error) {
	if !filepath.IsLocal(rel) {
		return nil, errors.New("outside the bot's folder")
	}
	if err := clientNoSymlinks(b, root, rel); err != nil {
		return nil, err
	}
	var data []byte
	if local {
		r, err := os.OpenRoot(root)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		f, err := r.Open(rel)
		if err != nil {
			return nil, errors.New("outside the bot's folder")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		if st.Size() > max {
			return nil, fmt.Errorf("the file is over %d MB", max>>20)
		}
		data = make([]byte, 0, st.Size())
		buf := make([]byte, 64<<10)
		for {
			n, err := f.Read(buf)
			data = append(data, buf[:n]...)
			if int64(len(data)) > max {
				return nil, fmt.Errorf("the file is over %d MB", max>>20)
			}
			if err != nil {
				break
			}
		}
		return data, nil
	}
	fi, err := b.Stat(filepath.Join(root, rel))
	if err != nil {
		return nil, errors.New("the file is not there")
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("the file is over %d MB", max>>20)
	}
	data, err = b.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("the file is over %d MB", max>>20)
	}
	return data, nil
}

// clientDeniedRel is what a bot may not send to a person even from its own
// folder: lasso's and the runtimes' own files (they hold the channel token and
// secrets) and the usual places credentials live.
func clientDeniedRel(rel string) bool {
	for _, p := range strings.Split(rel, string(filepath.Separator)) {
		switch strings.ToLower(p) {
		case ".lasso", ".mise", ".omp", ".claude", ".git", ".ssh", ".gnupg", ".aws", "fnox.toml", "age.txt", "id_rsa", "id_ed25519":
			return true
		}
		if strings.HasPrefix(strings.ToLower(p), ".env") {
			return true
		}
	}
	return false
}

// clientBotFileRel turns the path a bot named into one relative to its folder,
// or says why it is not one a bot may send.
func clientBotFileRel(root string, local bool, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) {
		return "", errors.New("a path is required")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	p = filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if (err != nil || !filepath.IsLocal(rel)) && local {
		// The bot may have named its folder by its real path.
		if realRoot, e := filepath.EvalSymlinks(root); e == nil && realRoot != root {
			rel, err = filepath.Rel(realRoot, p)
		}
	}
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("the file is outside the bot's folder")
	}
	if clientDeniedRel(rel) {
		return "", errors.New("that file is not one a bot may send (lasso's own files and credentials)")
	}
	return rel, nil
}

// clientStoreFile writes a copy under the user's directory in the bot's folder
// and records it. direction is "in" for the person's uploads, "out" for files
// the bot sent.
func clientStoreFile(b Backend, rec *botRecord, root string, conv *appConv, direction, name string, data []byte) (appAttachment, error) {
	mime, ext, ok := clientSniff(data)
	if !ok {
		return appAttachment{}, cerr(http.StatusUnsupportedMediaType, "unsupported_type", "That file type is not allowed. Images, PDF, text, audio, video and zip files are.")
	}
	id := clientID("a_", 12)
	sub := clientUserDir(conv.UserID)
	if direction == "out" {
		sub = filepath.Join(sub, "out")
	}
	rel := filepath.Join(sub, id+"."+ext)
	if err := b.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
		return appAttachment{}, cerr(http.StatusBadGateway, "bot_unavailable", "lasso could not reach the bot's folder.")
	}
	if err := b.WriteFile(filepath.Join(root, rel), data, 0o600); err != nil {
		return appAttachment{}, cerr(http.StatusBadGateway, "bot_unavailable", "lasso could not save the file for the bot.")
	}
	a := appAttachment{ID: id, UserID: conv.UserID, Bot: conv.Bot, ConvID: conv.ID, Direction: direction,
		Name: clientDisplayName(name, ext), Mime: mime, Size: int64(len(data)), Path: rel, CreatedAt: nowStamp(time.Now())}
	if err := insertAppAttachment(a); err != nil {
		_ = b.RemoveAll(filepath.Join(root, rel))
		return appAttachment{}, err
	}
	return a, nil
}

// clientRegisterBotFiles reads the files a bot named, each confined to its
// folder, and registers a copy of each for the person. An error names the
// first file that could not be sent; nothing is registered then.
func clientRegisterBotFiles(b Backend, rec *botRecord, conv *appConv, paths []string) ([]appAttachment, error) {
	if len(paths) > clientAttachPerMsg {
		return nil, fmt.Errorf("at most %d attachments per message", clientAttachPerMsg)
	}
	root := expandTildeOn(b, rec.Dir)
	local := rec.Host == "local"
	type loaded struct {
		name string
		data []byte
	}
	var files []loaded
	for _, p := range paths {
		rel, err := clientBotFileRel(root, local, expandTildeOn(b, p))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		data, err := clientConfinedRead(b, local, root, rel, clientAttachMax)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p, err)
		}
		if _, _, ok := clientSniff(data); !ok {
			return nil, fmt.Errorf("%s: that file type cannot be sent (images, PDF, text, audio, video and zip can)", p)
		}
		files = append(files, loaded{filepath.Base(rel), data})
	}
	var out []appAttachment
	for _, f := range files {
		a, err := clientStoreFile(b, rec, root, conv, "out", f.name, f.data)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f.name, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// --- the person's message -------------------------------------------------------------

type clientSendIn struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
	Mode        string   `json:"mode"`
	CallID      string   `json:"call_id"`
	ClientID    string   `json:"client_id"`
}

var clientClientIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// clientSend records a person's message and queues it for the bot. replay is
// true when client_id matched a message already sent (nothing is queued).
func clientSend(id *clientIdentity, conv *appConv, in clientSendIn, now time.Time) (m clientMessageView, replay bool, err error) {
	text := strings.TrimSpace(in.Text)
	if !utf8.ValidString(text) || len(text) > clientTextMax*4 || utf8.RuneCountInString(text) > clientTextMax {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", fmt.Sprintf("text must be UTF-8, at most %d characters.", clientTextMax))
	}
	if len(in.Attachments) > clientAttachPerMsg {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", fmt.Sprintf("at most %d attachments per message.", clientAttachPerMsg))
	}
	if text == "" && len(in.Attachments) == 0 {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", "A message needs text or an attachment.")
	}
	mode := in.Mode
	switch mode {
	case "":
		mode = "text"
	case "text", "voice":
	default:
		return m, false, cerr(http.StatusBadRequest, "invalid_request", `mode must be "text" or "voice".`)
	}
	if in.ClientID != "" && !clientClientIDRE.MatchString(in.ClientID) {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", "client_id must be 1-64 letters, digits, . _ or -.")
	}
	if in.ClientID != "" {
		var mid string
		if db.QueryRow(`SELECT id FROM app_messages WHERE conv_id = ? AND client_id = ?`, conv.ID, in.ClientID).Scan(&mid) == nil {
			m, err = messageByID(conv.ID, mid)
			return m, true, err
		}
	}
	callID := ""
	if mode == "voice" {
		call, e := appCallByID(in.CallID)
		if e != nil || call.ConvID != conv.ID || call.UserID != conv.UserID || call.State != "open" {
			return m, false, cerr(http.StatusBadRequest, "invalid_request", "A voice message needs the call_id of an open call in this conversation.")
		}
		callID = call.ID
	} else if in.CallID != "" {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", `call_id goes with mode "voice".`)
	}
	rec, e := getBot(conv.Bot)
	if e != nil {
		return m, false, cerr(http.StatusNotFound, "not_found", "Not found.")
	}
	state, _, reason := botClientState(rec, now)
	if state == "stopped" || state == "unavailable" {
		return m, false, cerr(http.StatusConflict, "bot_unavailable", reason)
	}
	// The attachments must be this person's own uploads to this conversation,
	// not yet used.
	var atts []appAttachment
	seen := map[string]bool{}
	for _, aid := range in.Attachments {
		a, e := appAttachmentByID(aid)
		if e != nil || seen[aid] || a.UserID != conv.UserID || a.ConvID != conv.ID || a.Direction != "in" || a.MessageID != "" {
			return m, false, cerr(http.StatusBadRequest, "invalid_request", "An attachment id is unknown or already used.")
		}
		seen[aid] = true
		atts = append(atts, a)
	}
	grant, e := getAppGrant(conv.UserID, conv.Bot)
	if e != nil {
		return m, false, cerr(http.StatusNotFound, "not_found", "Not found.")
	}
	b, e := botBackend(rec.Host)
	if e != nil {
		return m, false, cerr(http.StatusBadGateway, "bot_unavailable", "lasso cannot reach the bot's host.")
	}
	root := expandTildeOn(b, rec.Dir)

	views := make([]clientAttachmentView, 0, len(atts))
	files := make([]map[string]any, 0, len(atts))
	var footer []string
	for _, a := range atts {
		views = append(views, a.view())
		abs := filepath.Join(root, a.Path)
		files = append(files, map[string]any{"path": abs, "name": a.Name, "type": a.Mime, "size": a.Size})
		kind := "file"
		if strings.HasPrefix(a.Mime, "image/") {
			kind = "image"
		}
		footer = append(footer, fmt.Sprintf("Attached %s: %s", kind, abs))
	}
	ab, _ := json.Marshal(views)
	msgID := clientID("m_", 12)
	if _, err := db.Exec(`INSERT INTO app_messages (id, conv_id, role, text, done, mode, call_id, attachments, state, client_id, created_at)
		VALUES (?, ?, 'user', ?, 1, ?, ?, ?, 'queued', ?, ?)`, msgID, conv.ID, text, mode, callID, string(ab), in.ClientID, nowStamp(now)); err != nil {
		// The same client_id raced in from another request: that message wins.
		var mid string
		if in.ClientID != "" && db.QueryRow(`SELECT id FROM app_messages WHERE conv_id = ? AND client_id = ?`, conv.ID, in.ClientID).Scan(&mid) == nil {
			m, err = messageByID(conv.ID, mid)
			return m, true, err
		}
		return m, false, err
	}
	for _, a := range atts {
		_, _ = db.Exec(`UPDATE app_attachments SET message_id = ? WHERE id = ?`, msgID, a.ID)
	}
	meta := clientEventMeta(id.User, id.Device, grant, conv, msgID, mode, callID, files)
	content := text
	if len(footer) > 0 {
		if content != "" {
			content += "\n\n"
		}
		content += strings.Join(footer, "\n")
	}
	eventID, e := enqueueBotClientEvent(conv.Bot, content, meta, now)
	if e != nil {
		_, _ = db.Exec(`UPDATE app_messages SET state = 'failed', failed_reason = 'lasso could not queue it' WHERE id = ?`, msgID)
		return m, false, e
	}
	_, _ = db.Exec(`UPDATE app_messages SET event_id = ? WHERE id = ?`, eventID, msgID)
	if callID != "" {
		_, _ = db.Exec(`UPDATE app_calls SET touched_at = ? WHERE id = ?`, nowStamp(now), callID)
	}
	m, e = messageByID(conv.ID, msgID)
	if e != nil {
		return m, false, e
	}
	appendClientEvent(conv.ID, "message", m, now)
	clientReconcile(conv.ID, now)
	return m, false, nil
}

// clientMetaClean makes a value safe as a channel attribute: one line, no
// control characters, bounded.
func clientMetaClean(s string, max int) string {
	var sb strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			r = ' '
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if r := []rune(out); len(r) > max {
		out = string(r[:max])
	}
	return out
}

// clientEventMeta is the facts a bot gets with a person's message. Lasso sets
// every one of them from its own records: the token's user, the grant, the
// pairing that made the device. Nothing the client sends reaches this map but
// the message text itself (the event's content), the conversation's own ids
// and a client-chosen mode that was checked against an open call.
func clientEventMeta(u *appUser, d appDevice, g *appGrant, conv *appConv, msgID, mode, callID string, files []map[string]any) map[string]string {
	meta := map[string]string{
		"event":           "message",
		"user_id":         u.ID,
		"user_name":       clientMetaClean(u.Name, clientNameMax),
		"verified":        "true",
		"verified_via":    "paired",
		"paired_by":       clientMetaClean(d.PairedBy, 64),
		"is_owner":        fmt.Sprint(g.Owner),
		"conversation_id": conv.ID,
		"message_id":      msgID,
		"mode":            mode,
		"nonce":           randHex(8),
	}
	if callID != "" {
		meta["call_id"] = callID
	}
	if len(files) > 0 {
		b, _ := json.Marshal(files)
		meta["attachments"] = string(b)
	}
	return meta
}

// enqueueBotClientEvent queues an event of kind "client" for the bot's channel
// (or an omp bot's delivery loop).
func enqueueBotClientEvent(bot, content string, meta map[string]string, now time.Time) (int64, error) {
	mb, _ := json.Marshal(meta)
	stamp := nowStamp(now)
	res, err := db.Exec(`INSERT INTO bot_events (bot, job_id, job, kind, content, status, source, created_at, fired_at, meta)
		VALUES (?, 0, '', 'client', ?, 'pending', 'client', ?, ?, ?)`, bot, content, stamp, stamp, string(mb))
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	botEventQueued(bot, stamp)
	return id, nil
}

// clientReconcile moves queued messages whose event the bot's channel took
// (delivered) or lasso gave up on (dropped) to their state, and announces it.
// convID "" covers every conversation.
func clientReconcile(convID string, now time.Time) {
	q := `SELECT m.id, m.conv_id, e.status, e.reason FROM app_messages m JOIN bot_events e ON e.id = m.event_id
		WHERE m.state = 'queued' AND m.role = 'user' AND m.event_id > 0 AND e.status IN ('delivered', 'dropped')`
	var args []any
	if convID != "" {
		q += ` AND m.conv_id = ?`
		args = append(args, convID)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return
	}
	type change struct{ id, conv, status, reason string }
	var changes []change
	for rows.Next() {
		var c change
		if rows.Scan(&c.id, &c.conv, &c.status, &c.reason) == nil {
			changes = append(changes, c)
		}
	}
	rows.Close()
	for _, c := range changes {
		state, reason := "delivered", ""
		if c.status == "dropped" {
			state, reason = "failed", clientFailReason(c.reason)
		}
		res, err := db.Exec(`UPDATE app_messages SET state = ?, failed_reason = ? WHERE id = ? AND state = 'queued'`, state, reason, c.id)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			data := map[string]any{"message_id": c.id, "state": state}
			if reason != "" {
				data["reason"] = reason
			}
			appendClientEvent(c.conv, "message_state", data, now)
		}
	}
}

// clientFailReason is what a person is told when a message did not reach the
// bot: no internal detail.
func clientFailReason(reason string) string {
	switch {
	case strings.Contains(reason, "stopped"):
		return "The bot was stopped."
	case strings.Contains(reason, "within a day"):
		return "The bot did not pick the message up."
	}
	return "The message could not be delivered."
}

// --- calls --------------------------------------------------------------------------

// clientStartCall opens a call in the conversation, ending one still open.
func clientStartCall(conv *appConv, now time.Time) (*appCall, error) {
	if old, err := openCallOf(conv.ID); err == nil {
		clientEndCall(old, "replaced", "", now, false)
	}
	c := &appCall{ID: clientID("call_", 8), ConvID: conv.ID, UserID: conv.UserID, Bot: conv.Bot, State: "open", StartedAt: nowStamp(now)}
	if _, err := db.Exec(`INSERT INTO app_calls (id, conv_id, user_id, bot, state, started_at, touched_at) VALUES (?, ?, ?, ?, 'open', ?, ?)`,
		c.ID, c.ConvID, c.UserID, c.Bot, c.StartedAt, c.StartedAt); err != nil {
		return nil, err
	}
	appendClientEvent(conv.ID, "call_started", map[string]any{"call_id": c.ID, "started_at": c.StartedAt}, now)
	return c, nil
}

// clientEndCall ends an open call (once: false when it was already over). The
// bot is told when the person hung up or the call timed out, so it does not
// answer into silence.
func clientEndCall(c *appCall, reason, lastWords string, now time.Time, tellBot bool) bool {
	res, err := db.Exec(`UPDATE app_calls SET state = 'ended', end_reason = ?, ended_at = ? WHERE id = ? AND state = 'open'`, reason, nowStamp(now), c.ID)
	if err != nil {
		return false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false
	}
	data := map[string]any{"call_id": c.ID, "reason": reason, "ended_at": nowStamp(now)}
	if lastWords != "" {
		data["last_words"] = lastWords
	}
	appendClientEvent(c.ConvID, "call_ended", data, now)
	if tellBot {
		clientTellBotHangup(c, reason, now)
	}
	return true
}

func clientTellBotHangup(c *appCall, reason string, now time.Time) {
	conv, err := convByID(c.ConvID)
	if err != nil {
		return
	}
	u, err := getAppUser(conv.UserID)
	if err != nil {
		return
	}
	g, err := getAppGrant(conv.UserID, conv.Bot)
	if err != nil {
		return
	}
	meta := map[string]string{
		"event": "call_ended", "user_id": u.ID, "user_name": clientMetaClean(u.Name, clientNameMax),
		"verified": "true", "verified_via": "paired", "is_owner": fmt.Sprint(g.Owner),
		"conversation_id": conv.ID, "call_id": c.ID, "mode": "voice", "end_reason": reason, "nonce": randHex(8),
	}
	why := "The person hung up."
	if reason == "timeout" {
		why = "The call timed out after a long silence."
	}
	_, _ = enqueueBotClientEvent(conv.Bot, why+" No reply is needed.", meta, now)
}

// --- the bot's tools ------------------------------------------------------------------

// botClientReply is client_reply: the bot's message to the person in one
// conversation, with files from its folder. lenient (the spool, where nobody
// can be told of an error) delivers the text even when an attachment cannot be
// sent, and says so in the message.
func botClientReply(bot, convID, text string, done bool, paths []string, lenient bool, now time.Time) (clientMessageView, error) {
	var zero clientMessageView
	conv, err := convByID(strings.TrimSpace(convID))
	if err != nil || conv.Bot != bot {
		return zero, errors.New("no conversation with that id for this bot")
	}
	if u, err := getAppUser(conv.UserID); err != nil || u.Disabled {
		return zero, errors.New("that conversation is closed")
	}
	if _, err := getAppGrant(conv.UserID, conv.Bot); err != nil {
		return zero, errors.New("that conversation is closed")
	}
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) {
		return zero, errors.New("text must be UTF-8")
	}
	if len(text) > clientBotTextMax {
		return zero, fmt.Errorf("text is over %d KB; send it in parts with done=false on all but the last", clientBotTextMax>>10)
	}
	if text == "" && len(paths) == 0 {
		return zero, errors.New("text or attachments are required")
	}
	var views []clientAttachmentView
	if len(paths) > 0 {
		rec, err := getBot(bot)
		if err != nil {
			return zero, err
		}
		b, err := botBackend(rec.Host)
		if err != nil {
			return zero, err
		}
		atts, err := clientRegisterBotFiles(b, rec, conv, paths)
		if err != nil {
			if !lenient {
				return zero, fmt.Errorf("an attachment cannot be sent: %v", err)
			}
			if text != "" {
				text += "\n\n"
			}
			text += "[An attachment could not be sent: " + clientFailReasonFile(err) + "]"
		}
		for _, a := range atts {
			views = append(views, a.view())
		}
	}
	callID := ""
	if c, err := openCallOf(conv.ID); err == nil {
		callID = c.ID
	}
	return addBotMessage(conv, text, done, callID, views, now)
}

func clientFailReasonFile(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// botClientEndCall is end_call: the bot hangs up, optionally after last words
// the app speaks first.
func botClientEndCall(bot, callID, lastWords string, now time.Time) error {
	call, err := appCallByID(strings.TrimSpace(callID))
	if err != nil || call.Bot != bot {
		return errors.New("no call with that id for this bot")
	}
	if call.State != "open" {
		return errors.New("that call is already over")
	}
	lastWords = strings.TrimSpace(lastWords)
	if len(lastWords) > clientBotTextMax || !utf8.ValidString(lastWords) {
		return errors.New("last_words must be UTF-8, under 64 KB")
	}
	conv, err := convByID(call.ConvID)
	if err != nil {
		return errors.New("that call is over")
	}
	if lastWords != "" {
		if _, err := addBotMessage(conv, lastWords, true, call.ID, nil, now); err != nil {
			return err
		}
	}
	if !clientEndCall(call, "bot", lastWords, now, false) {
		return errors.New("that call is already over")
	}
	return nil
}

// --- omp: which conversation a turn belongs to ----------------------------------------

// clientTurn is a client message an omp bot was handed: the header's nonce
// (random, known only to lasso and the bot's prompt) maps back to it, so the
// bot's answer is routed by what lasso delivered, never by what text claims.
type clientTurn struct {
	Bot, ConvID, MsgID string
	At                 time.Time
}

var ompClientTurns sync.Map // nonce -> clientTurn

var ompHeaderNonceRE = regexp.MustCompile(`\bnonce=([0-9a-f]{16})\b`)

const ompClientMessagePrefix = "[lasso client message "

// ompHeaderNonce reads the nonce out of a user message that starts with
// lasso's client header; "" for any other message.
func ompHeaderNonce(text string) string {
	if !strings.HasPrefix(text, ompClientMessagePrefix) {
		return ""
	}
	first, _, _ := strings.Cut(text, "\n")
	if m := ompHeaderNonceRE.FindStringSubmatch(first); m != nil {
		return m[1]
	}
	return ""
}

// ompMessageParts reads an omp message: its role, its text parts joined, and
// whether it calls tools (so it is an interim note, not the turn's answer).
func ompMessageParts(raw json.RawMessage) (role, text string, hasTool bool, stop string) {
	var m struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stopReason"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "", "", false, ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return m.Role, s, false, m.StopReason
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return m.Role, "", false, m.StopReason
	}
	var texts []string
	for _, p := range parts {
		switch p.Type {
		case "text":
			texts = append(texts, p.Text)
		case "toolCall":
			hasTool = true
		}
	}
	return m.Role, strings.Join(texts, "\n"), hasTool, m.StopReason
}

// observeMessageEnd routes a finished omp message: a user message starting
// with a registered client header opens that person's turn; assistant text
// during it is their reply; any other user message (the human at the terminal,
// a job) closes it. With no matching header nothing is forwarded.
func (ob *ompBot) observeMessageEnd(raw json.RawMessage) {
	role, text, hasTool, stop := ompMessageParts(raw)
	var post func()
	ob.mu.Lock()
	switch role {
	case "user":
		prev, prevDone := ob.clientCur, ob.clientDone
		var next *clientTurn
		if nonce := ompHeaderNonce(text); nonce != "" {
			if v, ok := ompClientTurns.LoadAndDelete(nonce); ok {
				if t := v.(clientTurn); t.Bot == ob.name {
					next = &t
				}
			}
		}
		ob.clientCur, ob.clientDone = next, false
		if prev != nil && !prevDone {
			t := *prev
			post = func() { clientTurnEnd(t) }
		}
	case "assistant":
		if cur := ob.clientCur; cur != nil && strings.TrimSpace(text) != "" {
			done := !hasTool && stop != "toolUse"
			if done {
				ob.clientDone = true
			}
			t := *cur
			post = func() { clientBotTurnMessage(t, text, done) }
		}
	}
	ob.mu.Unlock()
	if post != nil {
		post()
	}
}

// observeAgentEnd closes the open client turn when omp's run ends.
func (ob *ompBot) observeAgentEnd() {
	ob.mu.Lock()
	cur, done := ob.clientCur, ob.clientDone
	ob.clientCur, ob.clientDone = nil, false
	ob.mu.Unlock()
	if cur != nil && !done {
		clientTurnEnd(*cur)
	}
}

// clientBotTurnMessage forwards an omp bot's text to the conversation its turn
// belongs to.
func clientBotTurnMessage(t clientTurn, text string, done bool) {
	conv, err := convByID(t.ConvID)
	if err != nil || conv.Bot != t.Bot {
		return
	}
	if _, err := getAppGrant(conv.UserID, conv.Bot); err != nil {
		return
	}
	text = strings.TrimSpace(text)
	if len(text) > clientBotTextMax {
		text = text[:clientBotTextMax]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	callID := ""
	if c, err := openCallOf(conv.ID); err == nil {
		callID = c.ID
	}
	_, _ = addBotMessage(conv, text, done, callID, nil, time.Now())
}

// clientTurnEnd says a turn is over without a closing message.
func clientTurnEnd(t clientTurn) {
	appendClientEvent(t.ConvID, "turn_end", map[string]any{"message_id": t.MsgID}, time.Now())
}

// --- maintenance (the runner's) ---------------------------------------------------------

func clientMaintain(now time.Time) {
	clientReconcile("", now)
	cut := nowStamp(now.Add(-clientEventKeep))
	_, _ = db.Exec(`DELETE FROM app_events WHERE created_at < ?`, cut)
	_, _ = db.Exec(`DELETE FROM bot_events WHERE kind = 'client' AND status IN ('delivered', 'dropped') AND created_at < ?`, cut)
	_, _ = db.Exec(`DELETE FROM app_pairings WHERE created_at < ?`, nowStamp(now.Add(-24*time.Hour)))
	_, _ = db.Exec(`DELETE FROM app_calls WHERE state = 'ended' AND ended_at < ?`, nowStamp(now.Add(-30*24*time.Hour)))
	// Calls nobody ended: a phone that lost its network.
	rows, err := db.Query(`SELECT `+appCallCols+` FROM app_calls WHERE state = 'open' AND (touched_at < ? OR started_at < ?)`,
		nowStamp(now.Add(-clientCallIdle)), nowStamp(now.Add(-clientCallMaxAge)))
	if err == nil {
		var stale []*appCall
		for rows.Next() {
			if c, err := scanAppCall(rows); err == nil {
				stale = append(stale, c)
			}
		}
		rows.Close()
		for _, c := range stale {
			clientEndCall(c, "timeout", "", now, true)
		}
	}
	// Uploads never attached to a message.
	orphans, err := db.Query(`SELECT `+appAttachmentCols+` FROM app_attachments WHERE direction = 'in' AND message_id = '' AND created_at < ?`, nowStamp(now.Add(-clientOrphanUpload)))
	if err == nil {
		var gone []appAttachment
		for orphans.Next() {
			if a, err := scanAppAttachment(orphans); err == nil {
				gone = append(gone, a)
			}
		}
		orphans.Close()
		for _, a := range gone {
			_, _ = db.Exec(`DELETE FROM app_attachments WHERE id = ?`, a.ID)
		}
		clientRemoveFiles(gone)
	}
	ompClientTurns.Range(func(k, v any) bool {
		if now.Sub(v.(clientTurn).At) > time.Hour {
			ompClientTurns.Delete(k)
		}
		return true
	})
}

// clientRemoveFiles deletes attachment files from their bots' folders, best
// effort and off the caller's path (a bot on another host is an ssh round trip
// per file). The bots are resolved first, so the goroutine reads no database.
func clientRemoveFiles(atts []appAttachment) {
	type victim struct {
		b    Backend
		path string
	}
	var victims []victim
	for _, a := range atts {
		rec, err := getBot(a.Bot)
		if err != nil || !filepath.IsLocal(a.Path) {
			continue
		}
		b, err := botBackend(rec.Host)
		if err != nil {
			continue
		}
		victims = append(victims, victim{b, filepath.Join(expandTildeOn(b, rec.Dir), a.Path)})
	}
	if len(victims) == 0 {
		return
	}
	go func() {
		for _, v := range victims {
			_ = v.b.RemoveAll(v.path)
		}
	}()
}

// ompAnswering reports whether an omp bot is answering the person in this
// conversation right now: its open client turn. The tools an omp bot has on
// /mcp (lasso's /mcp is not authenticated per bot) are limited to it, so one
// bot, or anything else that can reach /mcp, cannot post into a conversation
// the bot is not in the middle of answering.
func ompAnswering(bot, convID string) bool {
	ob := ompBotFor(bot)
	ob.mu.Lock()
	defer ob.mu.Unlock()
	return ob.clientCur != nil && ob.clientCur.ConvID == convID
}

// mcpClientReply is client_reply on /mcp: botClientReply, for the conversation
// the omp bot is answering.
func mcpClientReply(bot, convID, text string, done bool, paths []string) error {
	if !ompAnswering(bot, strings.TrimSpace(convID)) {
		return errors.New("you can send into the conversation you are answering right now, the one in the header of the message you are handling")
	}
	_, err := botClientReply(bot, convID, text, done, paths, false, time.Now())
	return err
}

// mcpClientEndCall is client_end_call on /mcp: for a call of the conversation
// the omp bot is answering.
func mcpClientEndCall(bot, callID, lastWords string) error {
	call, err := appCallByID(strings.TrimSpace(callID))
	if err != nil || call.Bot != bot || !ompAnswering(bot, call.ConvID) {
		return errors.New("you can end the call of the conversation you are answering right now")
	}
	return botClientEndCall(bot, callID, lastWords, time.Now())
}

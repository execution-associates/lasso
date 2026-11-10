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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
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
	// Threads: the bot can take thread messages. ThreadTools: full, write
	// (a claude bot on another host) or none (an omp bot without lasso's MCP
	// server).
	Threads     bool   `json:"threads"`
	ThreadTools string `json:"thread_tools"`
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
		return "stopped", clientCaps{ThreadTools: "none"}, "The bot is stopped."
	}
	if rec.isOmp() {
		ob := ompBotFor(rec.Name)
		ob.ensure()
		s := ob.snapshot()
		tools := botClientToolsOnMCP(rec)
		caps = clientCaps{Chat: true, Voice: true, AttachmentsIn: true, AttachmentsOut: tools, EndCall: tools, Threads: true, ThreadTools: "none"}
		if tools {
			caps.ThreadTools = "full"
		}
		switch {
		case !s.connected || !s.ready:
			return "starting", caps, ""
		case s.streaming || len(s.pending) > 0:
			return "busy", caps, ""
		}
		return "available", caps, ""
	}
	if offered, why := botChannelOffered(rec); !offered {
		return "unavailable", clientCaps{ThreadTools: "none"}, "The bot gets no lasso channel: " + why + "."
	}
	var seen string
	var client, threads bool
	_ = db.QueryRow(`SELECT channel_seen_at, channel_client, channel_threads FROM bots WHERE name = ?`, rec.Name).Scan(&seen, &client, &threads)
	t, ok := parseStamp(seen)
	connected := ok && now.Sub(t) < botChannelFresh
	if connected && (!client || !threads) {
		return "unavailable", clientCaps{ThreadTools: "none"}, "The bot must restart to take messages from the app."
	}
	caps = clientCaps{Chat: true, Voice: true, AttachmentsIn: true, AttachmentsOut: true, EndCall: true, Threads: true, ThreadTools: "full"}
	if rec.Host != "local" {
		caps.ThreadTools = "write"
	}
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
		if clientHostileRune(r) {
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
// without leaving it.
//
// On lasso's own machine the file is opened once, under os.Root, with
// O_NOFOLLOW (a final component that is a symbolic link is refused by the
// kernel) and O_NONBLOCK (a FIFO cannot hold lasso), and everything after that
// is asked of the descriptor: Fstat must say a regular file within the limit,
// and the descriptor's own path (where the system reports it) must be the path
// that was asked for, so a directory swapped for a link between the walk and
// the open is caught. There is no check-then-open gap on the file itself.
//
// On another host the folder is read over SFTP. The walk (clientNoSymlinks)
// and the open are two operations there and sftp offers no no-follow open, so
// a path swapped for a link in between is a race lasso cannot close from here;
// the handle is still checked (regular file, size) before anything is read.
func clientConfinedRead(b Backend, local bool, root, rel string, max int64) ([]byte, error) {
	if !filepath.IsLocal(rel) {
		return nil, errors.New("outside the bot's folder")
	}
	if err := clientNoSymlinks(b, root, rel); err != nil {
		return nil, err
	}
	if local {
		return clientReadLocal(root, rel, max)
	}
	f, err := b.Open(filepath.Join(root, rel))
	if err != nil {
		return nil, errors.New("the file is not there")
	}
	defer f.Close()
	if st, ok := f.(interface{ Stat() (os.FileInfo, error) }); ok {
		fi, err := st.Stat()
		if err != nil || !fi.Mode().IsRegular() {
			return nil, errors.New("not a regular file")
		}
		if fi.Size() > max {
			return nil, fmt.Errorf("the file is over %d MB", max>>20)
		}
	}
	return readAtMost(f, max)
}

func readAtMost(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("the file is over %d MB", max>>20)
	}
	return data, nil
}

func clientReadLocal(root, rel string, max int64) ([]byte, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("outside the bot's folder, or not a plain file")
	}
	defer f.Close()
	st, err := f.Stat() // the descriptor's, not the path's
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > max {
		return nil, fmt.Errorf("the file is over %d MB", max>>20)
	}
	// Where the system can name what the descriptor is open on (Linux), it
	// must be the file that was asked for, with no link on the way.
	if got, err := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", f.Fd())); err == nil {
		if realRoot, e := filepath.EvalSymlinks(root); e == nil && got != filepath.Join(realRoot, rel) {
			return nil, errors.New("the path changed while it was being read")
		}
	}
	return readAtMost(f, max)
}

// clientOutboxRel is the one place in a bot's folder it may send files from.
// A bot copies or writes what it wants a person to have there first; lasso
// reads from nowhere else, so a steered bot cannot send its configuration, its
// secrets or the rest of its folder just by naming them.
var clientOutboxRel = filepath.Join(".lasso", "client", "outbox")

// clientBotFileRel turns the path a bot named into one relative to its folder
// that lies in the outbox, or says why it is not one a bot may send.
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
	if !strings.HasPrefix(rel, clientOutboxRel+string(filepath.Separator)) {
		return "", fmt.Errorf("a bot sends only files from its outbox, %s/ in its folder: copy the file there first", filepath.ToSlash(clientOutboxRel))
	}
	return rel, nil
}

// clientQuotaCheck refuses a stored file that would take a person (for their
// uploads) or a conversation and a bot (for the copies of what a bot sends)
// past its limit. Called with clientQuotaMu held.
func clientQuotaCheck(conv *appConv, direction string, size int64) error {
	if direction == "in" {
		u, _ := getAppUser(conv.UserID)
		bytes, files := userStoredUploads(conv.UserID)
		if files+1 > clientQuotaFilesFor(u) || bytes+size > clientQuotaBytesFor(u) {
			return cerr(http.StatusForbidden, clientQuotaCode, clientQuotaMsg)
		}
		return nil
	}
	var cb, cn, bb int64
	_ = db.QueryRow(`SELECT COALESCE(SUM(size), 0), COUNT(*) FROM app_attachments WHERE conv_id = ? AND direction = 'out' AND expired_at = ''`, conv.ID).Scan(&cb, &cn)
	_ = db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM app_attachments WHERE bot = ? AND direction = 'out' AND expired_at = ''`, conv.Bot).Scan(&bb)
	if cn+1 > clientOutFilesPerConv || cb+size > clientOutBytesPerConv || bb+size > clientOutBytesPerBot {
		return cerr(http.StatusForbidden, clientQuotaCode, "Too many files have been sent in this conversation or by this bot; they are freed when the person deletes them or their retention removes them.")
	}
	return nil
}

// clientStoreFile writes a copy under the user's directory in the bot's folder
// and records it. direction is "in" for the person's uploads, "out" for files
// the bot sent.
func clientStoreFile(b Backend, rec *botRecord, root string, conv *appConv, direction, name string, data []byte) (appAttachment, error) {
	mime, ext, ok := clientSniff(data)
	if !ok {
		return appAttachment{}, cerr(http.StatusUnsupportedMediaType, "unsupported_type", "That file type is not allowed. Images, PDF, text, audio, video and zip files are.")
	}
	clientQuotaMu.Lock()
	defer clientQuotaMu.Unlock()
	if err := clientQuotaCheck(conv, direction, int64(len(data))); err != nil {
		return appAttachment{}, err
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
	a.Width, a.Height = clientImageSize(mime, data)
	if err := insertAppAttachment(a); err != nil {
		_ = b.RemoveAll(filepath.Join(root, rel))
		return appAttachment{}, err
	}
	return a, nil
}

// clientRegisterBotFiles reads the files a bot named, each from its outbox and
// confined to its folder, and registers a copy of each for the person. An error
// names the first file that could not be sent; nothing is registered then. It
// answers the sent copies and the outbox paths they came from, which the caller
// clears once the message is out.
func clientRegisterBotFiles(b Backend, rec *botRecord, conv *appConv, paths []string) ([]appAttachment, []string, error) {
	if len(paths) > clientAttachPerMsg {
		return nil, nil, fmt.Errorf("at most %d attachments per message", clientAttachPerMsg)
	}
	root := expandTildeOn(b, rec.Dir)
	local := rec.Host == "local"
	type loaded struct {
		name string
		data []byte
	}
	var files []loaded
	var rels []string
	for _, p := range paths {
		rel, err := clientBotFileRel(root, local, expandTildeOn(b, p))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", p, err)
		}
		data, err := clientConfinedRead(b, local, root, rel, clientAttachMax)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %v", p, err)
		}
		if _, _, ok := clientSniff(data); !ok {
			return nil, nil, fmt.Errorf("%s: that file type cannot be sent (images, PDF, text, audio, video and zip can)", p)
		}
		files = append(files, loaded{filepath.Base(rel), data})
		rels = append(rels, rel)
	}
	var out []appAttachment
	for _, f := range files {
		a, err := clientStoreFile(b, rec, root, conv, "out", f.name, f.data)
		if err != nil {
			for _, done := range out {
				_, _ = db.Exec(`DELETE FROM app_attachments WHERE id = ?`, done.ID)
			}
			clientRemoveFiles(out)
			return nil, nil, fmt.Errorf("%s: %v", f.name, err)
		}
		out = append(out, a)
	}
	return out, rels, nil
}

// --- the person's message -------------------------------------------------------------

type clientSendIn struct {
	Text        string   `json:"text"`
	Attachments []string `json:"attachments"`
	Mode        string   `json:"mode"`
	CallID      string   `json:"call_id"`
	ClientID    string   `json:"client_id"`
	// Mentions are the bots the person picked in the app's @ picker: the
	// message goes to them (else to the lead). The text is display only.
	Mentions []string `json:"mentions"`
}

var clientClientIDRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// clientSend is clientSendThread for a conversation: its main thread.
func clientSend(id *clientIdentity, conv *appConv, in clientSendIn, now time.Time) (clientMessageView, bool, error) {
	t, err := threadByID(conv.ID)
	if err != nil {
		return clientMessageView{}, false, cerr(http.StatusNotFound, "not_found", "Not found.")
	}
	return clientSendThread(id, t, in, now)
}

// clientMetaClean makes a value safe as a channel attribute or a header field:
// one line, bounded, with none of the characters that could close or reopen a
// tag or an attribute (< > " and backslash), no control or invisible formatting
// character.
func clientMetaClean(s string, max int) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r == '<' || r == '>' || r == '"' || r == '\\' || r == '`':
			continue
		case clientHostileRune(r):
			r = ' '
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

var clientTagRE = regexp.MustCompile(`(?i)<(\s*/?\s*)(lasso-)?channel`)

// clientNeutralize makes a person's words unable to pass for lasso's own
// framing, without removing any of their text: a string that reads as a
// <channel> tag (opening or closing), as the omp header or event lines, or as
// the omp quote markers is changed to a visibly different one. The text the bot
// sees still reads the way it was written.
func clientNeutralize(s string) string {
	s = clientTagRE.ReplaceAllString(s, "\u2039${1}${2}channel")
	s = strings.ReplaceAll(s, ompClientMessagePrefix, "(lasso client message ")
	s = strings.ReplaceAll(s, "[lasso client event", "(lasso client event")
	s = strings.ReplaceAll(s, "[lasso job", "(lasso job")
	s = strings.ReplaceAll(s, "----- the person's words", "\u2010\u2010\u2010\u2010\u2010 the person's words")
	s = strings.ReplaceAll(s, "----- the bot's words", "\u2010\u2010\u2010\u2010\u2010 the bot's words")
	s = strings.ReplaceAll(s, clientCtxMarkerPrefix, "\u2010\u2010\u2010\u2010\u2010 lasso thread context")
	s = strings.ReplaceAll(s, ompBotMessagePrefix, "(lasso bot message ")
	return s
}

// clientAttachmentsAttr writes the files a person sent as the one attribute
// they appear in: entries "path|type|size|name" joined by "; ". It holds no
// quote, angle bracket or control character (paths are lasso's, names are
// cleaned, and the separators are removed from names), so it cannot reshape a
// tag.
func clientAttachmentsAttr(files []map[string]any) string {
	var parts []string
	for _, f := range files {
		name := strings.NewReplacer("|", "_", ";", "_").Replace(clientMetaClean(fmt.Sprint(f["name"]), clientNameShown))
		parts = append(parts, fmt.Sprintf("%s|%s|%v|%s", clientMetaClean(fmt.Sprint(f["path"]), 1024), f["type"], f["size"], name))
	}
	return strings.Join(parts, "; ")
}

var clientOutboxMade sync.Map // host + folder -> struct{}: made already this run

// clientMakeOutbox makes the folder a bot sends files from (best effort, once
// per folder per run: a bot on another host costs a round trip each time).
func clientMakeOutbox(b Backend, root string) {
	key := b.Name() + "\x00" + root
	if _, done := clientOutboxMade.Load(key); done {
		return
	}
	if b.MkdirAll(filepath.Join(root, clientOutboxRel), 0o700) == nil {
		clientOutboxMade.Store(key, struct{}{})
	}
}

// clientEventMeta is the facts a bot gets with a person's message. Lasso sets
// every one of them from its own records: the token's user, the grant, the
// pairing that made the device. Nothing the client sends reaches this map but
// the message text itself (the event's content), the conversation's own ids
// and a client-chosen mode that was checked against an open call.
//
// device_id is lasso's own bookkeeping (the delivery step drops the message if
// the device was revoked meanwhile) and never reaches a bot.
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
		"device_id":       d.ID,
	}
	if callID != "" {
		meta["call_id"] = callID
	}
	if len(files) > 0 {
		meta["attachments"] = clientAttachmentsAttr(files)
	}
	return meta
}

// clientInternalMeta are the attributes a delivery strips before a bot sees
// them.
var clientInternalMeta = []string{"nonce", "device_id", "context_block"}

// clientAccessNow re-reads, at the moment a queued event is claimed, whether
// the person may still reach the bot: the user exists and is enabled, the grant
// stands and (for a message) the sending device is not revoked. It answers the
// current owner flag and name, which replace what was stamped at send time. q
// is the transaction the claim runs in.
func clientAccessNow(q interface {
	QueryRow(string, ...any) *sql.Row
}, bot string, meta map[string]string) (owner bool, name string, ok bool) {
	uid := meta["user_id"]
	if uid == "" {
		return false, "", false
	}
	var ownerInt int
	if q.QueryRow(`SELECT u.name, g.owner FROM app_users u JOIN app_grants g ON g.user_id = u.id AND g.bot = ?
		WHERE u.id = ? AND u.disabled = 0`, bot, uid).Scan(&name, &ownerInt) != nil {
		return false, "", false
	}
	if dev := meta["device_id"]; dev != "" {
		var n int
		if q.QueryRow(`SELECT COUNT(*) FROM app_devices WHERE id = ? AND user_id = ? AND revoked_at = ''`, dev, uid).Scan(&n) != nil || n != 1 {
			return false, "", false
		}
	}
	return ownerInt != 0, name, true
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

// clientFailReason is what a person is told when a message did not reach the
// bot: no internal detail.
func clientFailReason(reason string) string {
	switch {
	case strings.Contains(reason, "stopped"):
		return "The bot was stopped."
	case strings.Contains(reason, "within a day"):
		return "The bot did not pick the message up."
	case strings.Contains(reason, "access revoked"):
		return "Your access to this bot changed before it took the message."
	case strings.Contains(reason, "left the thread"):
		return "The bot left the thread before it took the message."
	case strings.Contains(reason, "thread was deleted"):
		return "The thread was deleted before the bot took the message."
	}
	return "The message could not be delivered."
}

// --- calls --------------------------------------------------------------------------

// clientStartCall opens a call in the conversation, ending one still open.
func clientStartCall(conv *appConv, now time.Time) (*appCall, error) {
	if old, err := openCallOf(conv.ID); err == nil {
		clientEndCall(old, "replaced", "", now, false)
	}
	c := &appCall{ID: clientID("call_", 8), ConvID: conv.ID, ThreadID: conv.ID, UserID: conv.UserID, Bot: conv.Bot, State: "open", StartedAt: nowStamp(now)}
	if _, err := db.Exec(`INSERT INTO app_calls (id, conv_id, user_id, bot, state, started_at, touched_at) VALUES (?, ?, ?, ?, 'open', ?, ?)`,
		c.ID, c.ConvID, c.UserID, c.Bot, c.StartedAt, c.StartedAt); err != nil {
		return nil, err
	}
	appendClientEvent(conv.ID, "call_started", map[string]any{"thread_id": conv.ID, "call_id": c.ID, "bot": c.Bot, "started_at": c.StartedAt}, now)
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
	data := map[string]any{"thread_id": c.ConvID, "call_id": c.ID, "bot": c.Bot, "reason": reason, "ended_at": nowStamp(now)}
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
	t, err := threadByID(c.ConvID)
	if err != nil {
		return
	}
	conv := t.conv(c.Bot)
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
		"conversation_id": conv.ID, "thread_id": conv.ID, "call_id": c.ID, "mode": "voice", "end_reason": reason, "nonce": randHex(8),
	}
	why := "The person hung up."
	if reason == "timeout" {
		why = "The call timed out after a long silence."
	}
	_, _ = enqueueBotClientEvent(conv.Bot, why+" No reply is needed.", meta, now)
}

// --- the bot's tools ------------------------------------------------------------------

// botClientReply is client_reply: the bot's message to the person in one
// thread, with files from its outbox. token is the reply_token of the message
// being answered: it must be this bot's and open (not closed by a final reply,
// not expired). Into the message's own thread a final reply (done=true)
// closes it and interim notes leave it usable; into another thread of the
// same person the bot is in, the post is a final message that does not close
// it (at most clientCrossPostsMax per token). lenient (the spool, where nobody
// can be told of an error) delivers the text even when an attachment cannot
// be sent, and says so in the message.
func botClientReply(bot, threadID, token, text string, done bool, paths []string, lenient bool, now time.Time) (clientMessageView, error) {
	return botClientReplyM(bot, threadID, token, text, done, paths, nil, lenient, now)
}

// botClientReplyM is botClientReply with the thread's bots the message
// addresses.
func botClientReplyM(bot, threadID, token, text string, done bool, paths, mentions []string, lenient bool, now time.Time) (clientMessageView, error) {
	var zero clientMessageView
	a, err := botActorFromToken(bot, token, false, now)
	if err != nil {
		return zero, err
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return zero, errors.New("thread_id is required: the thread_id of the message you are answering")
	}
	return botReplyInThread(a, threadID, text, done, paths, mentions, lenient, now)
}

// botClientSend is the part of client_reply after the bot has shown it may
// write here.
func botClientSend(conv *appConv, bot, token, text string, done bool, paths []string, lenient bool, replyTo, callID string, now time.Time) (clientMessageView, error) {
	return botClientSendM(conv, bot, token, text, done, paths, nil, lenient, replyTo, callID, now)
}

// botClientSendM is botClientSend naming the bots the message addresses.
// replyTo is the id of the message being answered and callID the call that
// message was sent in; both may be empty.
func botClientSendM(conv *appConv, bot, token, text string, done bool, paths, mentions []string, lenient bool, replyTo, callID string, now time.Time) (clientMessageView, error) {
	var zero clientMessageView
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
	var sentRels []string
	var b Backend
	var root string
	if len(paths) > 0 {
		rec, err := getBot(bot)
		if err != nil {
			return zero, err
		}
		b, err = botBackend(rec.Host)
		if err != nil {
			return zero, err
		}
		root = expandTildeOn(b, rec.Dir)
		atts, rels, err := clientRegisterBotFiles(b, rec, conv, paths)
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
		sentRels = rels
	}
	// A final reply consumes the message's token, once: a second final reply
	// racing this one finds it closed.
	if done && token != "" && !closeReplyToken(token, now) {
		return zero, errReplyToken
	}
	m, err := addBotReplyMentions(conv, text, done, callID, replyTo, views, mentions, now)
	if err != nil {
		if done && token != "" {
			reopenReplyToken(token)
		}
		return zero, err
	}
	// What was sent leaves the outbox: it is a hand-off, not a store.
	for _, rel := range sentRels {
		_ = b.RemoveAll(filepath.Join(root, rel))
	}
	return m, nil
}

func clientFailReasonFile(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return s
}

// botClientEndCall is end_call: the bot hangs up, optionally after last words
// the app speaks first. token is a reply_token of the call's conversation (the
// message being answered); it may already be closed by the final reply, since a
// bot says its answer and then hangs up, but it must not have expired.
func botClientEndCall(bot, callID, token, lastWords string, now time.Time) error {
	call, err := appCallByID(strings.TrimSpace(callID))
	if err != nil || call.Bot != bot {
		return errors.New("no call with that id for this bot")
	}
	if err := checkReplyToken(token, bot, call.ConvID, true, now); err != nil {
		return errors.New("end_call needs the reply_token of a message from this call's conversation, as it arrived (it stays valid for six hours after it reached you)")
	}
	return endCallAs(call, lastWords, now)
}

// endCallAs is the hang-up after the caller has shown it may.
func endCallAs(call *appCall, lastWords string, now time.Time) error {
	if call.State != "open" {
		return errors.New("that call is already over")
	}
	lastWords = strings.TrimSpace(lastWords)
	if len(lastWords) > clientBotTextMax || !utf8.ValidString(lastWords) {
		return errors.New("last_words must be UTF-8, under 64 KB")
	}
	t, err := threadByID(call.ConvID)
	if err != nil {
		return errors.New("that call is over")
	}
	conv := t.conv(call.Bot)
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
	// UserID is the person the turn acts for; Kind is "message" (theirs) or
	// "bot_message" (another bot's in a thread).
	UserID, Kind string
	At           time.Time
	// crossPosts counts posts into the person's other threads (at most
	// clientCrossPostsMax).
	crossPosts int
}

var ompClientTurns sync.Map // nonce -> clientTurn

var ompHeaderNonceRE = regexp.MustCompile(`\bnonce=([0-9a-f]{16})\b`)

const ompClientMessagePrefix = "[lasso client message "

// ompBotMessagePrefix starts the header of another bot's message in a thread,
// as an omp bot receives it.
const ompBotMessagePrefix = "[lasso bot message "

// ompHeaderNonce reads the nonce out of a user message that starts with
// lasso's client header (a person's message, or another bot's in a thread);
// "" for any other message.
func ompHeaderNonce(text string) string {
	if !strings.HasPrefix(text, ompClientMessagePrefix) && !strings.HasPrefix(text, ompBotMessagePrefix) {
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
		if prev != nil && !prevDone && prev.Kind != "bot_message" {
			t := *prev
			post = func() { clientTurnEnd(t) }
		}
	case "assistant":
		if cur := ob.clientCur; cur != nil && strings.TrimSpace(text) != "" {
			done := !hasTool && stop != "toolUse"
			if done {
				ob.clientDone = true
			}
			// NO_REPLY: the bot has nothing to say (another bot's message that
			// needed nothing from it). Nothing is posted.
			if isNoReply(text) {
				break
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
	if cur != nil && !done && cur.Kind != "bot_message" {
		clientTurnEnd(*cur)
	}
}

// clientBotTurnMessage forwards an omp bot's text to the conversation its turn
// belongs to.
func clientBotTurnMessage(t clientTurn, text string, done bool) {
	th, err := threadByID(t.ConvID)
	if err != nil || !isActiveParticipant(th.ID, t.Bot) {
		return
	}
	conv := th.conv(t.Bot)
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
	_, _ = addBotReply(conv, text, done, callOfMessage(conv.ID, t.MsgID), t.MsgID, nil, time.Now())
}

// clientTurnEnd says a turn is over without a closing message.
func clientTurnEnd(t clientTurn) {
	appendClientEvent(t.ConvID, "turn_end", map[string]any{"thread_id": t.ConvID, "message_id": t.MsgID, "bot": t.Bot}, time.Now())
}

// --- maintenance (the runner's) ---------------------------------------------------------

func clientMaintain(now time.Time) {
	clientReconcile("", now)
	clientExpireProposals(now)
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
	// Retention: a file, the person's or the bot's sent to them, is deleted once
	// it is older than the retention in force for the person now (their choice,
	// within the owner's maximum; none keeps it until they delete it). The
	// message keeps a marker for it.
	if users, err := listAppUsers(); err == nil {
		for _, u := range users {
			if days := clientRetentionDaysOf(u); days > 0 {
				removeAttachments(userAttachmentsOlderThan(u.ID, now.Add(-time.Duration(days)*24*time.Hour), true), clientRemovedRetention, now)
			}
		}
	}
	_, _ = db.Exec(`DELETE FROM app_reply_tokens WHERE expires_at < ?`, nowStamp(now.Add(-48*time.Hour)))
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
		// The primary copy and every bot's copy of it (attachments the person
		// sent into a thread with several bots).
		holders := attachmentHolders(a)
		if a.Path == "" && a.Bot != "" {
			// Removed already: its copies may still be there.
			delete(holders, a.Bot)
		}
		for bot, p := range holders {
			rec, err := getBot(bot)
			if err != nil || !filepath.IsLocal(p) {
				continue
			}
			b, err := botBackend(rec.Host)
			if err != nil {
				continue
			}
			victims = append(victims, victim{b, filepath.Join(expandTildeOn(b, rec.Dir), p)})
		}
		_, _ = db.Exec(`DELETE FROM app_attachment_copies WHERE attachment_id = ?`, a.ID)
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
	_, ok := ompAnsweringTurn(bot, convID)
	return ok
}

// ompAnsweringTurn is the open client turn when it is in this conversation.
func ompAnsweringTurn(bot, convID string) (clientTurn, bool) {
	ob := ompBotFor(bot)
	ob.mu.Lock()
	defer ob.mu.Unlock()
	if ob.clientCur == nil || ob.clientCur.ConvID != convID {
		return clientTurn{}, false
	}
	return *ob.clientCur, true
}

// mcpClientReply is client_reply on /mcp: for the person the omp bot is
// answering, in the thread of that message or another thread of theirs it is
// in. /mcp does not know which bot is calling, so what bounds it is the open
// client turn lasso opened itself from a nonce it issued (observeMessageEnd):
// there is no reply_token to present, because an omp bot is handed none, and a
// turn that is not open cannot be written into.
func mcpClientReply(bot, threadID, text string, done bool, paths []string) error {
	return mcpClientReplyM(bot, threadID, text, done, paths, nil)
}

// mcpClientReplyM is mcpClientReply naming the bots the message addresses.
func mcpClientReplyM(bot, threadID, text string, done bool, paths, mentions []string) error {
	a, err := botActorFromTurn(bot)
	if err != nil {
		return errors.New("you can send only while you are answering a person's message from lasso's app, into their threads")
	}
	if strings.TrimSpace(threadID) == "" {
		threadID = a.Thread
	}
	_, err = botReplyInThread(a, threadID, text, done, paths, mentions, false, time.Now())
	return err
}

// mcpClientEndCall is client_end_call on /mcp: for a call of the conversation
// the omp bot is answering.
func mcpClientEndCall(bot, callID, lastWords string) error {
	call, err := appCallByID(strings.TrimSpace(callID))
	if err != nil || call.Bot != bot || !ompAnswering(bot, call.ConvID) {
		return errors.New("you can end the call of the conversation you are answering right now")
	}
	return endCallAs(call, lastWords, time.Now())
}

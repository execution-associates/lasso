package main

// A person's message in a thread (docs/design/client-threads.md, "Messages in
// threads" and "Participants and routing"): routed by the send body's
// mentions, never by the text; one delivery (a bot_events row, a reply token)
// per bot it reaches; files copied into each recipient's folder before its
// delivery is queued.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clientMentionsMax     = 10
	clientParticipantsMax = 10
)

// clientRecipient is one bot a person's message goes to.
type clientRecipient struct {
	rec    *botRecord
	grant  *appGrant
	routed string // main | lead | mention | call
}

// clientRoute decides who a person's message goes to: a voice message to the
// call's bot, else the bots in mentions (each once), else the lead. Every
// recipient must be an active participant the person is granted, and able to
// take app messages now.
func clientRoute(t *appThread, userID string, mentions []string, call *appCall, now time.Time) ([]clientRecipient, error) {
	active := activeBots(t.ID)
	if len(active) == 0 {
		return nil, cerr(http.StatusConflict, "no_participants", "Nobody is in this thread any more: add a bot first.")
	}
	isActive := map[string]bool{}
	for _, b := range active {
		isActive[b.Bot] = true
	}
	var names []string
	routed := "lead"
	switch {
	case call != nil:
		names, routed = []string{call.Bot}, "call"
	case len(mentions) > 0:
		seen := map[string]bool{}
		for _, n := range mentions {
			n = strings.TrimSpace(n)
			if seen[n] {
				continue
			}
			seen[n] = true
			names = append(names, n)
		}
		routed = "mention"
	default:
		names = []string{active[0].Bot}
	}
	if t.isMain() {
		routed = "main"
	}
	var out []clientRecipient
	for _, n := range names {
		if !isActive[n] {
			return nil, &clientErr{Status: http.StatusConflict, Code: "not_participant", Msg: n + " is not in this thread: add it first."}
		}
		rec, g, ok := grantedBotOf(userID, n)
		if !ok {
			return nil, cerr(http.StatusNotFound, "not_found", "Not found.")
		}
		if state, _, reason := botClientState(rec, now); state == "stopped" || state == "unavailable" {
			if reason == "" {
				reason = "It cannot take messages now."
			}
			return nil, cerr(http.StatusConflict, "bot_unavailable", n+": "+reason)
		}
		out = append(out, clientRecipient{rec: rec, grant: g, routed: routed})
	}
	return out, nil
}

func grantedBotOf(userID, bot string) (*botRecord, *appGrant, bool) {
	g, err := getAppGrant(userID, bot)
	if err != nil {
		return nil, nil, false
	}
	rec, err := getBot(bot)
	if err != nil {
		return nil, nil, false
	}
	return rec, g, true
}

// clientCheckMentions validates a send's mentions before anything is stored.
func clientCheckMentions(t *appThread, mentions []string) error {
	if len(mentions) > clientMentionsMax {
		return cerr(http.StatusBadRequest, "invalid_request", fmt.Sprintf("at most %d mentions per message.", clientMentionsMax))
	}
	for _, n := range mentions {
		if t.isMain() && n != t.MainBot {
			return &clientErr{Status: http.StatusConflict, Code: "not_participant", Msg: "A main thread has only its own bot."}
		}
	}
	return nil
}

// clientSendThread records a person's message in a thread and queues it for
// each bot it is routed to. replay is true when client_id matched a message
// already sent (nothing is queued).
func clientSendThread(id *clientIdentity, t *appThread, in clientSendIn, now time.Time) (m clientMessageView, replay bool, err error) {
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
		if db.QueryRow(`SELECT id FROM app_messages WHERE conv_id = ? AND client_id = ?`, t.ID, in.ClientID).Scan(&mid) == nil {
			m, err = messageByID(t.ID, mid)
			return m, true, err
		}
	}
	var call *appCall
	callID := ""
	mentions := in.Mentions
	if mode == "voice" {
		c, e := appCallByID(in.CallID)
		if e != nil || c.ConvID != t.ID || c.UserID != t.UserID || c.State != "open" {
			return m, false, cerr(http.StatusBadRequest, "invalid_request", "A voice message needs the call_id of an open call in this conversation.")
		}
		call, callID, mentions = c, c.ID, nil
	} else if in.CallID != "" {
		return m, false, cerr(http.StatusBadRequest, "invalid_request", `call_id goes with mode "voice".`)
	}
	if err := clientCheckMentions(t, mentions); err != nil {
		return m, false, err
	}
	recips, err := clientRoute(t, id.User.ID, mentions, call, now)
	if err != nil {
		return m, false, err
	}
	// The attachments must be this person's own uploads to this thread, not
	// yet used.
	var atts []appAttachment
	seen := map[string]bool{}
	for _, aid := range in.Attachments {
		a, e := appAttachmentByID(aid)
		if e != nil || seen[aid] || a.UserID != t.UserID || a.ConvID != t.ID || a.Direction != "in" || a.MessageID != "" || a.ExpiredAt != "" {
			return m, false, cerr(http.StatusBadRequest, "invalid_request", "An attachment id is unknown or already used.")
		}
		seen[aid] = true
		atts = append(atts, a)
	}
	type target struct {
		clientRecipient
		b    Backend
		root string
	}
	var targets []target
	for _, rc := range recips {
		b, e := botBackend(rc.rec.Host)
		if e != nil {
			return m, false, cerr(http.StatusBadGateway, "bot_unavailable", "lasso cannot reach "+rc.rec.Name+"'s host.")
		}
		targets = append(targets, target{rc, b, expandTildeOn(b, rc.rec.Dir)})
	}
	// Each recipient reads its own copy of the files: made before anything is
	// queued, so a copy that cannot be written sends nothing.
	for _, tg := range targets {
		for _, a := range atts {
			if err := clientCopyAttachment(a, tg.rec, tg.b, tg.root, now); err != nil {
				return m, false, err
			}
		}
	}
	views := make([]clientAttachmentView, 0, len(atts))
	for _, a := range atts {
		views = append(views, a.view())
	}
	ab, _ := json.Marshal(views)
	var mentionsJSON string
	if len(mentions) > 0 && mode != "voice" {
		var names []string
		for _, rc := range recips {
			names = append(names, rc.rec.Name)
		}
		mb, _ := json.Marshal(names)
		mentionsJSON = string(mb)
	}
	msgID := clientID("m_", 12)
	res, e := db.Exec(`INSERT INTO app_messages (id, conv_id, role, text, done, mode, call_id, attachments, state, client_id, mentions, created_at)
		VALUES (?, ?, 'user', ?, 1, ?, ?, ?, 'queued', ?, ?, ?)`, msgID, t.ID, text, mode, callID, string(ab), in.ClientID, mentionsJSON, nowStamp(now))
	if e != nil {
		// The same client_id raced in from another request: that message wins.
		var mid string
		if in.ClientID != "" && db.QueryRow(`SELECT id FROM app_messages WHERE conv_id = ? AND client_id = ?`, t.ID, in.ClientID).Scan(&mid) == nil {
			m, err = messageByID(t.ID, mid)
			return m, true, err
		}
		return m, false, e
	}
	seq, _ := res.LastInsertId()
	// Before anything is queued, so every bot is handed the refined title.
	refined := refineThreadTitle(t, text, seq, now)
	for _, a := range atts {
		_, _ = db.Exec(`UPDATE app_attachments SET message_id = ? WHERE id = ?`, msgID, a.ID)
	}
	var firstEvent int64
	for _, tg := range targets {
		files := make([]map[string]any, 0, len(atts))
		var footer []string
		for _, a := range atts {
			abs := filepath.Join(tg.root, a.Path)
			files = append(files, map[string]any{"path": abs, "name": a.Name, "type": a.Mime, "size": a.Size})
			kind := "file"
			if strings.HasPrefix(a.Mime, "image/") {
				kind = "image"
			}
			footer = append(footer, fmt.Sprintf("Attached %s: %s", kind, abs))
		}
		conv := t.conv(tg.rec.Name)
		meta := clientEventMeta(id.User, id.Device, tg.grant, conv, msgID, mode, callID, files)
		clientThreadMeta(meta, t, tg.routed)
		meta["reply_token"] = issueReplyToken(conv, msgID, callID, now)
		clientMakeOutbox(tg.b, tg.root)
		content := clientNeutralize(text)
		if len(footer) > 0 {
			if content != "" {
				content += "\n\n"
			}
			content += strings.Join(footer, "\n")
		}
		eventID, e := enqueueBotClientEvent(tg.rec.Name, content, meta, now)
		state, reason := "queued", ""
		if e != nil {
			state, reason = "failed", "The message could not be delivered."
		}
		_, _ = db.Exec(`INSERT OR REPLACE INTO app_message_deliveries (message_id, thread_id, bot, event_id, state, failed_reason, routed) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			msgID, t.ID, tg.rec.Name, eventID, state, reason, tg.routed)
		if firstEvent == 0 {
			firstEvent = eventID
		}
	}
	_, _ = db.Exec(`UPDATE app_messages SET event_id = ? WHERE id = ?`, firstEvent, msgID)
	clientRefreshAggregate(msgID)
	if callID != "" {
		_, _ = db.Exec(`UPDATE app_calls SET touched_at = ? WHERE id = ?`, nowStamp(now), callID)
	}
	reopened := touchThread(t.ID, seq, "user", now)
	// Sending marks the thread read up to the new message.
	_, _ = db.Exec(`UPDATE app_threads SET last_read_seq = MAX(last_read_seq, ?) WHERE id = ?`, seq, t.ID)
	m, e = messageByID(t.ID, msgID)
	if e != nil {
		return m, false, e
	}
	appendClientEvent(t.ID, "message", m, now)
	var changes []string
	if reopened {
		changes = append(changes, "state")
	}
	if refined {
		changes = append(changes, "title")
	}
	if len(changes) > 0 {
		threadUpdated(t.ID, changes, now) // its stub too
	} else {
		clientStubChanged(t.ID, now)
	}
	clientReconcile(t.ID, now)
	return m, false, nil
}

// clientThreadMeta adds a thread's facts to a delivery's attributes, from
// lasso's records.
func clientThreadMeta(meta map[string]string, t *appThread, routed string) {
	meta["thread_id"] = t.ID
	meta["thread_kind"] = t.Kind
	if !t.isMain() && t.Title != "" {
		meta["thread_title"] = clientMetaClean(t.Title, 80)
	} else {
		delete(meta, "thread_title")
	}
	names := activeBotNames(t.ID)
	meta["participants"] = strings.Join(names, ",")
	if len(names) > 0 {
		meta["lead"] = names[0]
	}
	if routed != "" {
		meta["routed"] = routed
	}
}

// clientCopyAttachment puts a person's upload in another bot's folder at the
// same relative path, unless that bot holds the primary copy or one already.
func clientCopyAttachment(a appAttachment, to *botRecord, b Backend, root string, now time.Time) error {
	if a.Bot == to.Name {
		return nil
	}
	var have string
	if db.QueryRow(`SELECT path FROM app_attachment_copies WHERE attachment_id = ? AND bot = ?`, a.ID, to.Name).Scan(&have) == nil {
		return nil
	}
	data, err := clientAttachmentBytes(a)
	if err != nil {
		return cerr(http.StatusBadGateway, "bot_unavailable", "lasso could not read a file to hand to "+to.Name+".")
	}
	if err := b.MkdirAll(filepath.Join(root, filepath.Dir(a.Path)), 0o700); err != nil {
		return cerr(http.StatusBadGateway, "bot_unavailable", "lasso could not reach "+to.Name+"'s folder.")
	}
	if err := b.WriteFile(filepath.Join(root, a.Path), data, 0o600); err != nil {
		return cerr(http.StatusBadGateway, "bot_unavailable", "lasso could not save the file for "+to.Name+".")
	}
	_, _ = db.Exec(`INSERT OR REPLACE INTO app_attachment_copies (attachment_id, bot, path, created_at) VALUES (?, ?, ?, ?)`, a.ID, to.Name, a.Path, nowStamp(now))
	return nil
}

// clientAttachmentBytes reads a stored file: the primary copy, else any other.
func clientAttachmentBytes(a appAttachment) ([]byte, error) {
	holders := []struct{ bot, path string }{{a.Bot, a.Path}}
	rows, err := db.Query(`SELECT bot, path FROM app_attachment_copies WHERE attachment_id = ? ORDER BY created_at`, a.ID)
	if err == nil {
		for rows.Next() {
			var h struct{ bot, path string }
			if rows.Scan(&h.bot, &h.path) == nil {
				holders = append(holders, h)
			}
		}
		rows.Close()
	}
	var lastErr error = errClientNotFound
	for _, h := range holders {
		if h.path == "" {
			continue
		}
		rec, err := getBot(h.bot)
		if err != nil {
			continue
		}
		b, err := botBackend(rec.Host)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := clientConfinedRead(b, rec.Host == "local", expandTildeOn(b, rec.Dir), h.path, clientAttachMax)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// attachmentHolders are the bots whose folders hold a copy of a file, with
// the copy's path: the primary first.
func attachmentHolders(a appAttachment) map[string]string {
	out := map[string]string{}
	if a.Path != "" {
		out[a.Bot] = a.Path
	}
	rows, err := db.Query(`SELECT bot, path FROM app_attachment_copies WHERE attachment_id = ?`, a.ID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var bot, p string
		if rows.Scan(&bot, &p) == nil {
			out[bot] = p
		}
	}
	return out
}

// --- delivery states ----------------------------------------------------------

// clientRefreshAggregate writes a person's message's state from its
// deliveries, so an older lasso (and the old field) read the aggregate.
func clientRefreshAggregate(msgID string) (state, reason string) {
	ds := deliveriesOf([]string{msgID})[msgID]
	state, reason = aggregateState(ds)
	if state != "" {
		_, _ = db.Exec(`UPDATE app_messages SET state = ?, failed_reason = ? WHERE id = ?`, state, reason, msgID)
	}
	return state, reason
}

// clientReconcile moves queued deliveries whose event the bot's channel took
// (delivered) or lasso gave up on (dropped) to their state, and announces each
// (message_state, with the message's aggregate). A message an older lasso
// queued with no delivery row is reconciled by its own event. threadID ""
// covers every thread.
func clientReconcile(threadID string, now time.Time) {
	q := `SELECT d.message_id, d.thread_id, d.bot, e.status, e.reason FROM app_message_deliveries d JOIN bot_events e ON e.id = d.event_id
		WHERE d.state = 'queued' AND d.event_id > 0 AND e.status IN ('delivered', 'dropped')`
	var args []any
	if threadID != "" {
		q += ` AND d.thread_id = ?`
		args = append(args, threadID)
	}
	type change struct{ msg, thread, bot, status, reason string }
	var changes []change
	if rows, err := db.Query(q, args...); err == nil {
		for rows.Next() {
			var c change
			if rows.Scan(&c.msg, &c.thread, &c.bot, &c.status, &c.reason) == nil {
				changes = append(changes, c)
			}
		}
		rows.Close()
	}
	for _, c := range changes {
		state, reason := "delivered", ""
		if c.status == "dropped" {
			state, reason = "failed", clientFailReason(c.reason)
		}
		res, err := db.Exec(`UPDATE app_message_deliveries SET state = ?, failed_reason = ? WHERE message_id = ? AND bot = ? AND state = 'queued'`, state, reason, c.msg, c.bot)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			agg, _ := clientRefreshAggregate(c.msg)
			data := map[string]any{"thread_id": c.thread, "message_id": c.msg, "bot": c.bot, "state": state, "aggregate_state": agg}
			if reason != "" {
				data["reason"] = reason
			}
			appendClientEvent(c.thread, "message_state", data, now)
		}
	}
	// An older lasso's message: no delivery row, its own event.
	q = `SELECT m.id, m.conv_id, e.status, e.reason FROM app_messages m JOIN bot_events e ON e.id = m.event_id
		WHERE m.state = 'queued' AND m.role = 'user' AND m.event_id > 0 AND e.status IN ('delivered', 'dropped')
		AND NOT EXISTS (SELECT 1 FROM app_message_deliveries d WHERE d.message_id = m.id)`
	args = nil
	if threadID != "" {
		q += ` AND m.conv_id = ?`
		args = append(args, threadID)
	}
	var legacy []change
	if rows, err := db.Query(q, args...); err == nil {
		for rows.Next() {
			var c change
			if rows.Scan(&c.msg, &c.thread, &c.status, &c.reason) == nil {
				legacy = append(legacy, c)
			}
		}
		rows.Close()
	}
	for _, c := range legacy {
		state, reason := "delivered", ""
		if c.status == "dropped" {
			state, reason = "failed", clientFailReason(c.reason)
		}
		res, err := db.Exec(`UPDATE app_messages SET state = ?, failed_reason = ? WHERE id = ? AND state = 'queued'`, state, reason, c.msg)
		if err != nil {
			continue
		}
		if n, _ := res.RowsAffected(); n == 1 {
			data := map[string]any{"thread_id": c.thread, "message_id": c.msg, "state": state, "aggregate_state": state}
			if reason != "" {
				data["reason"] = reason
			}
			appendClientEvent(c.thread, "message_state", data, now)
		}
	}
}

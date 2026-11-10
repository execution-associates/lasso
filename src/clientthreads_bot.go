package main

// Client threads, the bots' side (docs/design/client-threads.md, "Delivery to
// the bot", "Context hand-off", "Bots talking in a thread"):
//
//   - When a bot takes a message, lasso decides from what it has shown that bot
//     of the thread before (app_thread_bots.ctx_*) and the bot's context epoch
//     (bots.client_epoch) whether it needs the thread's context: none, the
//     messages it missed (delta) or the summary and recent messages (full).
//     The decision and the bookkeeping run in the claim's transaction.
//   - In a thread with several bots, a bot's finished message is delivered to
//     the other participants (addressed when it mentions them, informational
//     otherwise), capped per run of bot messages since the person last wrote.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clientCtxFullMessages  = 15
	clientCtxChars         = 8000
	clientCtxMessageChars  = 1200
	clientCtxStaleAfter    = 12 * time.Hour
	clientCtxStaleMessages = 25
	clientSummaryStaleNew  = 6
	clientSummaryStaleMore = 10

	// clientBotRunMax is how many bot messages in a row, since the person last
	// wrote in a thread, are handed to the thread's other bots. Past it the
	// bots' messages still reach the person, and the bots hear each other
	// again once the person writes.
	clientBotRunMax = 8
	// clientBotFanPerWindow bounds bot-to-bot deliveries in one thread over
	// clientBotFanWindow, whatever the person does.
	clientBotFanPerWindow = 40
	clientBotFanWindow    = 10 * time.Minute
)

// clientCtxMarkerPrefix is the start of the context block's marker lines.
const clientCtxMarkerPrefix = "----- lasso thread context"

// --- the context epoch ----------------------------------------------------------

// bumpClientEpoch notes that the bot's model context no longer holds what lasso
// showed it: every thread's context is handed over again on its next message.
func bumpClientEpoch(bot string) {
	_, _ = db.Exec(`UPDATE bots SET client_epoch = client_epoch + 1 WHERE name = ?`, bot)
}

// clientSessionReport records what a claude bot's channel says of its session:
// "<session id>:<source>:<stamp>", the SessionStart hook's file. A new report
// whose source is startup, clear or compact, or whose session id differs from
// the last one, bumps the epoch; resume of the same session leaves it.
func clientSessionReport(bot, report string) {
	report = strings.TrimSpace(report)
	if report == "" || len(report) > 256 {
		return
	}
	var prev string
	if db.QueryRow(`SELECT client_session FROM bots WHERE name = ?`, bot).Scan(&prev) != nil || prev == report {
		return
	}
	parts := strings.SplitN(report, ":", 3)
	if len(parts) < 2 {
		return
	}
	id, source := parts[0], parts[1]
	prevID, _, _ := strings.Cut(prev, ":")
	bump := false
	switch source {
	case "startup", "clear", "compact":
		bump = true
	default:
		bump = id != prevID
	}
	_, _ = db.Exec(`UPDATE bots SET client_session = ? WHERE name = ?`, report, bot)
	if bump {
		bumpClientEpoch(bot)
	}
}

// clientOmpSession records the session id an omp bot reported (get_state);
// a different one than last time bumps the epoch.
func clientOmpSession(bot, sessionID string) {
	if sessionID == "" {
		return
	}
	var prev string
	if db.QueryRow(`SELECT client_session FROM bots WHERE name = ?`, bot).Scan(&prev) != nil || prev == sessionID {
		return
	}
	_, _ = db.Exec(`UPDATE bots SET client_session = ? WHERE name = ?`, sessionID, bot)
	bumpClientEpoch(bot)
}

// --- the claim ------------------------------------------------------------------

// clientClaimThread is a client event's thread side, when a bot takes it, in
// the claim's transaction: the thread must still exist with the bot active in
// it (else drop says why), and a message gets the thread's current facts and
// the context hand-off. A reclaim keeps what the first claim decided.
func clientClaimThread(tx *sql.Tx, bot string, e *botEvent, now time.Time) (drop string, err error) {
	ev := e.Meta["event"]
	if ev != "message" && ev != "bot_message" {
		return "", nil
	}
	tid := e.Meta["thread_id"]
	if tid == "" {
		tid = e.Meta["conversation_id"]
	}
	t, terr := scanAppThread(tx.QueryRow(`SELECT `+appThreadCols+` FROM app_threads WHERE id = ?`, tid))
	if terr != nil {
		// A conversation an older lasso made without a thread: its message is
		// delivered as before.
		var n int
		if strings.HasPrefix(tid, "c_") && tx.QueryRow(`SELECT COUNT(*) FROM app_convs WHERE id = ? AND bot = ?`, tid, bot).Scan(&n) == nil && n == 1 {
			return "", nil
		}
		return "the thread was deleted", nil
	}
	var removed string
	if tx.QueryRow(`SELECT removed_at FROM app_thread_bots WHERE thread_id = ? AND bot = ?`, t.ID, bot).Scan(&removed) != nil || removed != "" {
		return "the bot left the thread", nil
	}
	if e.Meta["context"] != "" {
		return "", nil
	}
	parts := activeBotsQ(tx, t.ID)
	names := make([]string, 0, len(parts))
	var self threadBot
	for _, p := range parts {
		names = append(names, p.Bot)
		if p.Bot == bot {
			self = p
		}
	}
	e.Meta["thread_id"] = t.ID
	e.Meta["thread_kind"] = t.Kind
	if !t.isMain() {
		e.Meta["thread_title"] = clientMetaClean(t.Title, clientThreadTitleMax)
	}
	e.Meta["participants"] = strings.Join(names, ",")
	if len(names) > 0 {
		e.Meta["lead"] = names[0]
	}
	var epoch, deliveries int64
	if err := tx.QueryRow(`SELECT client_epoch, client_deliveries FROM bots WHERE name = ?`, bot).Scan(&epoch, &deliveries); err != nil {
		return "", err
	}
	msgSeq := int64(0)
	_ = tx.QueryRow(`SELECT seq FROM app_messages WHERE id = ? AND conv_id = ?`, e.Meta["message_id"], t.ID).Scan(&msgSeq)
	if msgSeq == 0 {
		msgSeq = t.LastSeq + 1
	}
	kind, block := clientContextFor(tx, t, self, bot, e.Meta["user_name"], names, msgSeq, epoch, deliveries, now)
	e.Meta["context"] = kind
	if kind != "none" {
		marker := randHex(8)
		e.Meta["context_marker"] = marker
		e.Meta["context_block"] = block
	}
	if !t.isMain() && summaryStale(tx, t) {
		e.Meta["summary_stale"] = "true"
	}
	if _, err := tx.Exec(`UPDATE bots SET client_deliveries = client_deliveries + 1 WHERE name = ?`, bot); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`UPDATE app_thread_bots SET ctx_epoch = ?, ctx_seq = MAX(ctx_seq, ?), ctx_at = ?, ctx_deliveries = ? WHERE thread_id = ? AND bot = ?`,
		epoch, msgSeq, nowStamp(now), deliveries+1, t.ID, bot); err != nil {
		return "", err
	}
	mb, _ := json.Marshal(e.Meta)
	if _, err := tx.Exec(`UPDATE bot_events SET meta = ? WHERE id = ?`, string(mb), e.ID); err != nil {
		return "", err
	}
	return "", nil
}

// summaryStale: a thread with no summary and at least 6 messages, or one whose
// summary is 10 messages behind.
func summaryStale(q rowQuerier, t *appThread) bool {
	var n int
	if t.Summary == "" {
		_ = q.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role IN ('user', 'bot')`, t.ID).Scan(&n)
		return n >= clientSummaryStaleNew
	}
	_ = q.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role IN ('user', 'bot') AND seq > ?`, t.ID, t.SummarySeq).Scan(&n)
	return n >= clientSummaryStaleMore
}

// ctxMessage is one message of a thread as the context block shows it.
type ctxMessage struct {
	Seq       int64
	Role, Bot string
	Text      string
	Atts      []clientAttachmentView
	CreatedAt string
	Done      bool
}

func ctxMessagesQ(q rowQuerier, threadID, where string, args ...any) []ctxMessage {
	rows, err := q.Query(`SELECT seq, role, bot, text, attachments, created_at, done FROM app_messages WHERE conv_id = ? AND `+where, append([]any{threadID}, args...)...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []ctxMessage
	for rows.Next() {
		var m ctxMessage
		var atts string
		if rows.Scan(&m.Seq, &m.Role, &m.Bot, &m.Text, &atts, &m.CreatedAt, &m.Done) == nil {
			_ = json.Unmarshal([]byte(atts), &m.Atts)
			out = append(out, m)
		}
	}
	return out
}

// clientContextFor decides what bot B needs of thread T to take message M
// (at msgSeq), and builds the block.
func clientContextFor(q rowQuerier, t *appThread, self threadBot, bot, person string, names []string, msgSeq, epoch, deliveries int64, now time.Time) (string, string) {
	stale := self.CtxEpoch != epoch || deliveries-self.CtxDeliveries >= clientCtxStaleMessages
	if at, ok := parseStamp(self.CtxAt); ok && now.Sub(at) > clientCtxStaleAfter {
		stale = true
	}
	if self.CtxSeq == 0 {
		var earlier int
		_ = q.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq < ?`, t.ID, msgSeq).Scan(&earlier)
		if earlier > 0 || t.AnchorMessageID != "" {
			stale = true
		}
	}
	if !stale {
		missed := ctxMessagesQ(q, t.ID, `seq > ? AND seq < ? AND NOT (role = 'bot' AND bot = ?) ORDER BY seq`, self.CtxSeq, msgSeq, bot)
		if len(missed) == 0 {
			return "none", ""
		}
		lines := make([]string, 0, len(missed))
		total := 0
		for _, m := range missed {
			l := ctxLine(m, person, 0)
			lines = append(lines, l)
			total += utf8.RuneCountInString(l)
		}
		if len(missed) <= clientCtxFullMessages && total <= clientCtxChars {
			return "delta", ctxHeader(t, bot, person, names) + "\nSince you last saw this thread:\n" + strings.Join(lines, "\n")
		}
	}
	return "full", clientFullContext(q, t, bot, person, names, msgSeq)
}

// clientFullContext is the full block: the header, the anchor, the summary and
// the last messages before msgSeq within the budget.
func clientFullContext(q rowQuerier, t *appThread, bot, person string, names []string, msgSeq int64) string {
	var sb strings.Builder
	sb.WriteString(ctxHeader(t, bot, person, names))
	if t.AnchorMessageID != "" {
		who := person
		if t.AnchorRole == "bot" {
			who = t.AnchorBot
		}
		fmt.Fprintf(&sb, "\nIt hangs off this message (%s, %s): %s", clientNeutralize(who), ctxStamp(t.AnchorAt), clientNeutralize(clientPreview(t.AnchorText, clientCtxMessageChars)))
	}
	if t.Summary != "" {
		fmt.Fprintf(&sb, "\nSummary (by %s, %s):\n%s", t.SummaryBy, ctxStamp(t.SummaryAt), clientNeutralize(t.Summary))
	}
	recent := ctxMessagesQ(q, t.ID, `seq < ? ORDER BY seq DESC LIMIT ?`, msgSeq, clientCtxFullMessages)
	var total int
	_ = q.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq < ?`, t.ID, msgSeq).Scan(&total)
	var lines []string
	used := 0
	for _, m := range recent { // newest first
		l := ctxLine(m, person, clientCtxMessageChars)
		n := utf8.RuneCountInString(l)
		if used+n > clientCtxChars {
			break
		}
		used += n
		lines = append(lines, l)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	if len(lines) > 0 {
		left := total - len(lines)
		if left > 0 {
			fmt.Fprintf(&sb, "\nRecent messages, oldest first (%d earlier ones not shown; thread_read has them):", left)
		} else {
			sb.WriteString("\nRecent messages, oldest first:")
		}
		sb.WriteString("\n" + strings.Join(lines, "\n"))
	}
	return sb.String()
}

func ctxStamp(s string) string {
	if t, ok := parseStamp(s); ok {
		return t.UTC().Format("2006-01-02 15:04")
	}
	return s
}

func ctxHeader(t *appThread, bot, person string, names []string) string {
	started := ctxStamp(t.CreatedAt)
	if len(started) >= 10 {
		started = started[:10]
	}
	if t.isMain() {
		return fmt.Sprintf("Your main chat with %s (%s), started %s. You are %s.", clientNeutralize(person), t.ID, started, bot)
	}
	where := ""
	if t.AnchorMessageID != "" {
		where = " off a message in your main chat with them"
		if t.AnchorBot != "" && t.AnchorBot != bot {
			where = " off a message in their main chat with " + t.AnchorBot
		}
	}
	var ps []string
	for i, p := range names {
		if i == 0 {
			p += " (lead)"
		}
		ps = append(ps, p)
	}
	return fmt.Sprintf("Thread %q (%s): a thread of %s's, started %s%s. Participants: %s. You are %s.",
		clientNeutralize(t.Title), t.ID, clientNeutralize(person), started, where, strings.Join(ps, ", "), bot)
}

// ctxLine is one message as a block line: [<UTC date> <HH:MM> <who>] <text>.
func ctxLine(m ctxMessage, person string, clip int) string {
	who := person
	switch m.Role {
	case "bot":
		who = m.Bot
	case "system":
		who = "lasso"
	}
	text := strings.TrimSpace(m.Text)
	if clip > 0 && utf8.RuneCountInString(text) > clip {
		text = string([]rune(text)[:clip-1]) + "…"
	}
	text = strings.ReplaceAll(clientNeutralize(text), "\n", "\n  ")
	for _, a := range m.Atts {
		kind := "file"
		if strings.HasPrefix(a.Type, "image/") {
			kind = "image"
		}
		if text != "" {
			text += " "
		}
		text += fmt.Sprintf("[%s: %s, %s]", kind, clientMetaClean(a.Name, clientNameShown), a.Type)
	}
	if m.Role == "bot" && !m.Done {
		text = "(interim) " + text
	}
	return fmt.Sprintf("[%s %s] %s", ctxStamp(m.CreatedAt), clientNeutralize(who), text)
}

// clientContextWrap is the block between its markers.
func clientContextWrap(block, marker string) string {
	return fmt.Sprintf("%[1]s begin %[2]s -----\n%[3]s\n%[1]s end %[2]s -----", clientCtxMarkerPrefix, marker, block)
}

// --- bots talking in a thread -----------------------------------------------------

// clientBotRun is how many finished bot messages a thread has had since the
// person last wrote, and whether the pause notice for this run is out.
func clientBotRun(threadID string) (run int, noticed bool) {
	var lastUser int64
	_ = db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM app_messages WHERE conv_id = ? AND role = 'user'`, threadID).Scan(&lastUser)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role = 'bot' AND done = 1 AND seq > ?`, threadID, lastUser).Scan(&run)
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role = 'system' AND seq > ? AND json_extract(system, '$.type') = 'bots_paused'`, threadID, lastUser).Scan(&n)
	return run, n > 0
}

// clientBotsPaused posts the one notice of a run that the bots stopped hearing
// each other.
func clientBotsPaused(t *appThread, now time.Time) {
	if _, noticed := clientBotRun(t.ID); noticed {
		return
	}
	_, _ = addSystemMessage(t, clientSystemView{Type: "bots_paused", By: "lasso", Limit: clientBotRunMax},
		fmt.Sprintf("The bots paused passing messages to each other after %d in a row. They pick up again when you write.", clientBotRunMax), now)
}

// clientFanOut hands a bot's finished message in a thread to the thread's
// other active participants: addressed to the ones it mentions, informational
// to the rest. A bot never receives its own message; a bot that left, lost
// its grant or cannot take messages now receives nothing.
func clientFanOut(t *appThread, m clientMessageView, now time.Time) {
	if t.isMain() || m.Role != "bot" || !m.Done {
		return
	}
	others := []threadBot{}
	for _, b := range activeBots(t.ID) {
		if b.Bot != m.Bot {
			others = append(others, b)
		}
	}
	if len(others) == 0 {
		return
	}
	if run, _ := clientBotRun(t.ID); run > clientBotRunMax {
		clientBotsPaused(t, now)
		return
	}
	if !clientLimits.allow("fan:"+t.ID, clientBotFanPerWindow, clientBotFanWindow, now) {
		clientBotsPaused(t, now)
		return
	}
	u, err := getAppUser(t.UserID)
	if err != nil || u.Disabled {
		return
	}
	mentioned := map[string]bool{}
	for _, n := range m.Mentions {
		mentioned[n] = true
	}
	for _, b := range others {
		rec, g, ok := grantedBotOf(t.UserID, b.Bot)
		if !ok {
			continue
		}
		if state, _, _ := botClientState(rec, now); state == "stopped" || state == "unavailable" {
			continue
		}
		conv := t.conv(b.Bot)
		meta := map[string]string{
			"event": "bot_message", "user_id": u.ID, "user_name": clientMetaClean(u.Name, clientNameMax),
			"verified": "true", "verified_via": "paired", "is_owner": fmt.Sprint(g.Owner),
			"conversation_id": t.ID, "message_id": m.ID, "mode": "text", "nonce": randHex(8),
			"from_bot": m.Bot, "addressed": fmt.Sprint(mentioned[b.Bot]),
		}
		routed := "bot"
		if mentioned[b.Bot] {
			routed = "bot_mention"
		}
		clientThreadMeta(meta, t, routed)
		meta["reply_token"] = issueReplyToken(conv, m.ID, "", now)
		content := clientNeutralize(m.Text)
		for _, a := range m.Attachments {
			if content != "" {
				content += "\n"
			}
			content += fmt.Sprintf("[%s sent a file: %s, %s]", m.Bot, clientMetaClean(a.Name, clientNameShown), a.Type)
		}
		_, _ = enqueueBotClientEvent(b.Bot, content, meta, now)
	}
}

// clientTokenFromBot says whether a reply token was handed to the bot with
// another bot's message (not with the person's own).
func clientTokenFromBot(tok replyToken) bool {
	var role string
	_ = db.QueryRow(`SELECT role FROM app_messages WHERE id = ?`, tok.MessageID).Scan(&role)
	return role == "bot"
}

// clientNoReply is what an omp bot answers when a message needs nothing from
// it (another bot's informational message): lasso posts nothing.
const clientNoReply = "NO_REPLY"

func isNoReply(text string) bool {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(text), "`*_.")) == clientNoReply
}

// cursorString is a seq as the API's cursor.
func cursorString(seq int64) string { return strconv.FormatInt(seq, 10) }

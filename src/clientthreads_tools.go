package main

// The bots' thread tools (docs/design/client-threads.md, "Bot tools"), shared
// by both runtimes: a claude bot calls them on its channel with a reply token,
// an omp bot on /mcp inside its open client turn. Either way the call acts for
// one person only (the token's, or the turn's), and only in threads of that
// person in which the bot is an active participant; anything else reads as
// "no such thread".

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clientCrossPostsMax      = 5
	clientBotThreadsPerDay   = 10
	clientSummariesPerDay    = 30
	clientProposalsPerDay    = 10
	clientBotReadMax         = 50
	clientBotReadBytes       = 64 << 10
	clientBotListSummaryChar = 500
)

var errNoThreadForBot = errors.New("no such thread for you: name a thread of the person you are answering that you are in (thread_list shows them)")

// botActor is who a thread tool acts as and for: the bot, the person, and the
// message that authorised it.
type botActor struct {
	Bot, UserID string
	Thread      string // the thread the authorising message is in
	MsgID       string
	CallID      string
	Token       string      // claude: the reply token presented
	Open        bool        // the token (or turn) is still open
	FromBot     bool        // handed over with another bot's message
	turn        *clientTurn // omp: the open turn
}

// botActorFromToken checks a claude bot's reply token: this bot's, unexpired,
// and open unless allowClosed; the person enabled and still granted the bot.
func botActorFromToken(bot, tok string, allowClosed bool, now time.Time) (*botActor, error) {
	t, ok := lookupReplyToken(strings.TrimSpace(tok))
	if !ok || t.Bot != bot || !now.Before(t.Expires) || (t.Closed && !allowClosed) {
		return nil, errReplyToken
	}
	uid := threadOwner(t.ConvID)
	if !clientPersonReaches(uid, bot) {
		return nil, errors.New("that person's conversation is closed")
	}
	return &botActor{Bot: bot, UserID: uid, Thread: t.ConvID, MsgID: t.MessageID, CallID: t.CallID, Token: strings.TrimSpace(tok),
		Open: !t.Closed, FromBot: clientTokenFromBot(t)}, nil
}

// botActorFromTurn is an omp bot's open client turn.
func botActorFromTurn(bot string) (*botActor, error) {
	ob := ompBotFor(bot)
	ob.mu.Lock()
	cur := ob.clientCur
	ob.mu.Unlock()
	if cur == nil {
		return nil, errors.New("these tools work only while you are answering a person's message from lasso's app")
	}
	uid := cur.UserID
	if uid == "" {
		uid = threadOwner(cur.ConvID)
	}
	if !clientPersonReaches(uid, bot) {
		return nil, errors.New("that person's conversation is closed")
	}
	return &botActor{Bot: bot, UserID: uid, Thread: cur.ConvID, MsgID: cur.MsgID, CallID: callOfMessage(cur.ConvID, cur.MsgID),
		Open: true, FromBot: cur.Kind == "bot_message", turn: cur}, nil
}

// clientPersonReaches: the person exists, is enabled and holds the grant.
func clientPersonReaches(userID, bot string) bool {
	var n int
	return userID != "" && db.QueryRow(`SELECT COUNT(*) FROM app_users u JOIN app_grants g ON g.user_id = u.id AND g.bot = ?
		WHERE u.id = ? AND u.disabled = 0`, bot, userID).Scan(&n) == nil && n == 1
}

// threadFor is a thread of the actor's person in which the bot is active.
func (a *botActor) threadFor(tid string) (*appThread, error) {
	t, err := threadByID(strings.TrimSpace(tid))
	if err != nil || t.UserID != a.UserID || !isActiveParticipant(t.ID, a.Bot) {
		return nil, errNoThreadForBot
	}
	return t, nil
}

// writesOnlyHere: a bot answering another bot's message acts only in that
// thread.
func (a *botActor) mayWrite(tid string) error {
	if a.FromBot && tid != a.Thread {
		return errors.New("answering another bot's message, you can act only in the thread it came from")
	}
	return nil
}

// --- client_reply --------------------------------------------------------------

// botReplyInThread is client_reply after the actor is known: into the
// message's own thread as a reply (done closes the token), or into another
// thread of the same person as a final message that does not close it.
func botReplyInThread(a *botActor, tid, text string, done bool, paths, mentions []string, lenient bool, now time.Time) (clientMessageView, error) {
	var zero clientMessageView
	if tid == "" {
		tid = a.Thread
	}
	t, err := a.threadFor(tid)
	if err != nil {
		return zero, err
	}
	if err := a.mayWrite(t.ID); err != nil {
		return zero, err
	}
	var ms []string
	for _, n := range mentions {
		n = strings.TrimSpace(n)
		if n == "" || n == a.Bot {
			continue
		}
		if !isActiveParticipant(t.ID, n) {
			if lenient {
				continue
			}
			return zero, fmt.Errorf("%s is not in this thread: mention only its participants", n)
		}
		ms = append(ms, n)
	}
	if len(ms) > clientMentionsMax {
		return zero, fmt.Errorf("at most %d mentions", clientMentionsMax)
	}
	if t.ID == a.Thread {
		token := a.Token
		if a.turn != nil {
			token = ""
		}
		return botClientSendM(t.conv(a.Bot), a.Bot, token, text, done, paths, ms, lenient, a.MsgID, a.CallID, now)
	}
	if !a.Open {
		return zero, errReplyToken
	}
	key := "xpost:" + a.Token
	if a.turn != nil {
		key = "xpost:turn:" + a.turn.MsgID
	}
	if clientLimits.count(key, clientReplyWindow, now) >= clientCrossPostsMax {
		return zero, fmt.Errorf("at most %d posts into other threads per message you answer", clientCrossPostsMax)
	}
	m, err := botClientSendM(t.conv(a.Bot), a.Bot, "", text, true, paths, ms, lenient, "", "", now)
	if err == nil {
		clientLimits.hit(key, clientReplyWindow, now)
	}
	return m, err
}

// --- thread_list ------------------------------------------------------------------

type botThreadListItem struct {
	ID               string   `json:"id"`
	Kind             string   `json:"kind"`
	Title            string   `json:"title,omitempty"`
	Participants     []string `json:"participants"`
	Lead             string   `json:"lead"`
	State            string   `json:"state"`
	Summary          string   `json:"summary,omitempty"`
	SummaryUpdatedAt string   `json:"summary_updated_at,omitempty"`
	MessageCount     int      `json:"message_count"`
	LastActivityAt   string   `json:"last_activity_at"`
	AnchorText       string   `json:"anchor_text,omitempty"`
}

func botThreadList(a *botActor, state string, limit int, cursor string) (map[string]any, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	offset, ok := decodeCursor(cursor)
	if !ok {
		return nil, errors.New("cursor must come from next_cursor")
	}
	where := `t.user_id = ? AND EXISTS (SELECT 1 FROM app_thread_bots b WHERE b.thread_id = t.id AND b.bot = ? AND b.removed_at = '')`
	switch state {
	case "", "open":
		where += ` AND t.state = 'open'`
	case "archived":
		where += ` AND t.state = 'archived'`
	case "all":
	default:
		return nil, errors.New("state must be open, archived or all")
	}
	rows, err := db.Query(`SELECT `+prefixCols("t.", appThreadCols)+` FROM app_threads t WHERE `+where+` ORDER BY t.last_activity_at DESC, t.id LIMIT ? OFFSET ?`,
		a.UserID, a.Bot, limit+1, offset)
	if err != nil {
		return nil, err
	}
	var ts []*appThread
	for rows.Next() {
		if t, err := scanAppThread(rows); err == nil {
			ts = append(ts, t)
		}
	}
	rows.Close()
	var next any
	if len(ts) > limit {
		ts = ts[:limit]
		next = encodeCursor(offset + limit)
	}
	items := []botThreadListItem{}
	for _, t := range ts {
		names := activeBotNames(t.ID)
		it := botThreadListItem{ID: t.ID, Kind: t.Kind, Participants: names, State: t.State, MessageCount: threadReplyCount(t.ID),
			LastActivityAt: t.LastActivityAt, AnchorText: clientPreview(t.AnchorText, 200)}
		if len(names) > 0 {
			it.Lead = names[0]
		}
		if !t.isMain() {
			it.Title = t.Title
		} else {
			it.State = "open"
		}
		if t.Summary != "" {
			it.Summary = t.Summary
			if utf8.RuneCountInString(it.Summary) > clientBotListSummaryChar {
				it.Summary = string([]rune(it.Summary)[:clientBotListSummaryChar-1]) + "…"
			}
			it.SummaryUpdatedAt = t.SummaryAt
		}
		items = append(items, it)
	}
	u, _ := getAppUser(a.UserID)
	name := ""
	if u != nil {
		name = u.Name
	}
	return map[string]any{"person": map[string]any{"user_id": a.UserID, "name": name}, "current_thread_id": a.Thread,
		"threads": items, "next_cursor": next}, nil
}

// --- thread_read -------------------------------------------------------------------

func botThreadRead(a *botActor, tid, before string, limit int, now time.Time) (map[string]any, error) {
	t, err := a.threadFor(tid)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > clientBotReadMax {
		limit = clientBotReadMax
	}
	var b int64
	if before != "" {
		var ok bool
		if b, ok = parseCursor(before); !ok || b <= 0 {
			return nil, errors.New("before must be a cursor from an earlier page")
		}
	}
	msgs, err := pageMessages(t.ID, b, 0, limit)
	if err != nil {
		return nil, err
	}
	person := ""
	if u, err := getAppUser(t.UserID); err == nil {
		person = u.Name
	}
	_ = person
	// The page holds at most 64 KB of text: the oldest go first.
	total := 0
	start := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		n := len(msgs[i].Text)
		if total+n > clientBotReadBytes && start < len(msgs) {
			break
		}
		total += n
		start = i
	}
	msgs = msgs[start:]
	out := []map[string]any{}
	for _, m := range msgs {
		role := m.Role
		if role == "user" {
			role = "person"
		}
		item := map[string]any{"cursor": m.Cursor, "id": m.ID, "role": role, "text": m.Text, "created_at": m.CreatedAt}
		if m.Role == "bot" {
			item["bot"] = m.Bot
			if !m.Done {
				item["interim"] = true
			}
		}
		atts := []map[string]any{}
		for _, av := range m.Attachments {
			e := map[string]any{"name": av.Name, "type": av.Type, "size": av.Size}
			if p := botFilePath(a.Bot, av.ID); p != "" && !av.Expired {
				e["path"] = p
			}
			atts = append(atts, e)
		}
		item["attachments"] = atts
		out = append(out, item)
	}
	var older any
	if len(msgs) > 0 {
		first, _ := strconv.ParseInt(msgs[0].Cursor, 10, 64)
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq < ?`, t.ID, first).Scan(&n)
		if n > 0 {
			older = msgs[0].Cursor
		}
		if b == 0 {
			last, _ := strconv.ParseInt(msgs[len(msgs)-1].Cursor, 10, 64)
			_, _ = db.Exec(`UPDATE app_thread_bots SET ctx_seq = MAX(ctx_seq, ?) WHERE thread_id = ? AND bot = ?`, last, t.ID, a.Bot)
		}
	}
	th := map[string]any{"id": t.ID, "kind": t.Kind, "participants": activeBotNames(t.ID), "lead": threadLead(t.ID), "state": t.State}
	if !t.isMain() {
		th["title"] = t.Title
	} else {
		th["state"] = "open"
	}
	if t.AnchorMessageID != "" {
		an := map[string]any{"role": t.AnchorRole, "text": t.AnchorText, "created_at": t.AnchorAt}
		if t.AnchorBot != "" {
			an["bot"] = t.AnchorBot
		}
		th["anchor"] = an
	}
	if t.Summary != "" {
		th["summary"] = map[string]any{"text": t.Summary, "by": t.SummaryBy, "updated_at": t.SummaryAt}
	}
	return map[string]any{"thread": th, "messages": out, "older_cursor": older}, nil
}

// botFilePath is the path of a file in the bot's own folder: its own copy of
// an upload, or one it sent (the stored copy), else "".
func botFilePath(bot, attID string) string {
	a, err := appAttachmentByID(attID)
	if err != nil || a.ExpiredAt != "" {
		return ""
	}
	rel := attachmentHolders(a)[bot]
	if rel == "" {
		return ""
	}
	rec, err := getBot(bot)
	if err != nil {
		return ""
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		return ""
	}
	return expandTildeOn(b, rec.Dir) + "/" + rel
}

// --- thread_create ---------------------------------------------------------------------

func botThreadCreate(a *botActor, title, text, anchorID, summary string, now time.Time) (map[string]any, error) {
	if a.FromBot {
		return nil, errors.New("answering another bot's message, you cannot start threads")
	}
	if !a.Open {
		return nil, errReplyToken
	}
	ttl, ok := cleanThreadTitle(title)
	if !ok {
		return nil, fmt.Errorf("title must be 1-%d characters, one line, without < > \" \\ or `", clientThreadTitleMax)
	}
	text = strings.TrimSpace(text)
	if text == "" || !utf8.ValidString(text) || len(text) > clientBotTextMax {
		return nil, errors.New("text is required: your first message in the thread (UTF-8, under 64 KB)")
	}
	summary, err := cleanSummary(summary)
	if err != nil {
		return nil, err
	}
	var anchor *clientMessageView
	var anchorTID string
	if anchorID = strings.TrimSpace(anchorID); anchorID != "" {
		mainConv, err := convFor(a.UserID, a.Bot, now)
		if err != nil {
			return nil, err
		}
		m, err := messageByID(mainConv.ID, anchorID)
		if err != nil || m.Role == "system" {
			return nil, errors.New("anchor_message_id must be a message in your main thread with this person; nothing was made")
		}
		var other string
		if db.QueryRow(`SELECT id FROM app_threads WHERE anchor_message_id = ?`, m.ID).Scan(&other) == nil {
			return nil, fmt.Errorf("that message already has a thread (%s); nothing was made", other)
		}
		anchor, anchorTID = &m, mainConv.ID
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_threads WHERE user_id = ? AND kind = 'thread'`, a.UserID).Scan(&n)
	if n >= clientThreadsMax {
		return nil, errors.New("this person has too many threads; nothing was made")
	}
	if !clientLimits.allow("botthread:"+a.Bot+":"+a.UserID, clientBotThreadsPerDay, 24*time.Hour, now) {
		return nil, fmt.Errorf("at most %d new threads a day for one person", clientBotThreadsPerDay)
	}
	t, err := createThread(threadCreateSpec{UserID: a.UserID, Title: ttl, Bots: []string{a.Bot}, Anchor: anchor, AnchorTID: anchorTID,
		CreatedBy: "bot:" + a.Bot, AddedBy: "bot:" + a.Bot, Summary: summary, SummaryBy: a.Bot}, now)
	if err != nil {
		var te *errThreadExists
		if errors.As(err, &te) {
			return nil, fmt.Errorf("that message already has a thread (%s); nothing was made", te.ThreadID)
		}
		return nil, err
	}
	m, err := addBotReply(t.conv(a.Bot), text, true, "", "", nil, now)
	if err != nil {
		return nil, err
	}
	// Its context is current from the start.
	epoch := int64(0)
	_ = db.QueryRow(`SELECT client_epoch FROM bots WHERE name = ?`, a.Bot).Scan(&epoch)
	var deliveries int64
	_ = db.QueryRow(`SELECT client_deliveries FROM bots WHERE name = ?`, a.Bot).Scan(&deliveries)
	seq, _ := strconv.ParseInt(m.Cursor, 10, 64)
	_, _ = db.Exec(`UPDATE app_thread_bots SET ctx_epoch = ?, ctx_seq = ?, ctx_at = ?, ctx_deliveries = ? WHERE thread_id = ? AND bot = ?`,
		epoch, seq, nowStamp(now), deliveries, t.ID, a.Bot)
	if summary != "" {
		_, _ = db.Exec(`UPDATE app_threads SET summary_seq = ? WHERE id = ?`, seq, t.ID)
	}
	return map[string]any{"thread_id": t.ID, "message_id": m.ID}, nil
}

// cleanSummary keeps a summary plain: UTF-8, line breaks, no other controls,
// 4000 characters.
func cleanSummary(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errors.New("summary must be UTF-8")
	}
	var sb strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' {
			sb.WriteRune(r)
			continue
		}
		if r == '\r' || clientHostileRune(r) && r != '"' && r != '<' && r != '>' && r != '\\' && r != '`' {
			continue
		}
		sb.WriteRune(r)
	}
	out := sb.String()
	if utf8.RuneCountInString(out) > clientThreadSummaryMax {
		return "", fmt.Errorf("summary is over %d characters", clientThreadSummaryMax)
	}
	return out, nil
}

// --- thread_update ---------------------------------------------------------------------

func botThreadUpdate(a *botActor, tid, title, state string, now time.Time) (map[string]any, error) {
	if !a.Open {
		return nil, errReplyToken
	}
	t, err := a.threadFor(tid)
	if err != nil {
		return nil, err
	}
	if err := a.mayWrite(t.ID); err != nil {
		return nil, err
	}
	if t.isMain() {
		return nil, errors.New("the main thread cannot be renamed or archived")
	}
	var changes []string
	if title != "" {
		ttl, ok := cleanThreadTitle(title)
		if !ok {
			return nil, fmt.Errorf("title must be 1-%d characters, one line, without < > \" \\ or `", clientThreadTitleMax)
		}
		changed, err := nameThread(t, ttl, "bot:"+a.Bot, now)
		if err != nil {
			return nil, err
		}
		if changed {
			changes = append(changes, "title")
		}
	}
	if state != "" {
		if state != "open" && state != "archived" {
			return nil, errors.New("state must be open or archived")
		}
		if state != t.State {
			_, _ = db.Exec(`UPDATE app_threads SET state = ?, updated_at = ? WHERE id = ?`, state, nowStamp(now), t.ID)
			changes = append(changes, "state")
		}
	}
	if len(changes) > 0 {
		threadUpdated(t.ID, changes, now)
	}
	t, _ = threadByID(t.ID)
	return map[string]any{"thread_id": t.ID, "title": t.Title, "state": t.State}, nil
}

// --- thread_summary ----------------------------------------------------------------------

func botThreadSummary(a *botActor, tid, summary string, now time.Time) (map[string]any, error) {
	t, err := a.threadFor(tid)
	if err != nil {
		return nil, err
	}
	if err := a.mayWrite(t.ID); err != nil {
		return nil, err
	}
	s, err := cleanSummary(summary)
	if err != nil {
		return nil, err
	}
	if !clientLimits.allow("summary:"+t.ID, clientSummariesPerDay, 24*time.Hour, now) {
		return nil, fmt.Errorf("at most %d summaries a day for one thread", clientSummariesPerDay)
	}
	by, at := a.Bot, nowStamp(now)
	if s == "" {
		by, at = "", ""
	}
	_, _ = db.Exec(`UPDATE app_threads SET summary = ?, summary_by = ?, summary_at = ?, summary_seq = ?, updated_at = ? WHERE id = ?`,
		s, by, at, newestSeq(t.ID), nowStamp(now), t.ID)
	threadUpdated(t.ID, []string{"summary"}, now)
	return map[string]any{"thread_id": t.ID, "updated_at": nowStamp(now)}, nil
}

// --- thread_propose_bot ----------------------------------------------------------------------

func botThreadProposeBot(a *botActor, tid, bot, reason string, now time.Time) (map[string]any, error) {
	if !a.Open {
		return nil, errReplyToken
	}
	t, err := a.threadFor(tid)
	if err != nil {
		return nil, err
	}
	if err := a.mayWrite(t.ID); err != nil {
		return nil, err
	}
	if t.isMain() {
		return nil, errors.New("a main thread has only its own bot: propose bots in a thread (thread_create)")
	}
	bot = strings.TrimSpace(bot)
	if _, _, ok := grantedBotOf(a.UserID, bot); !ok {
		return nil, errors.New("that bot is not available to this person")
	}
	if isActiveParticipant(t.ID, bot) {
		return nil, errors.New("that bot is already in this thread")
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" || !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > clientProposalReasonMax {
		return nil, fmt.Errorf("reason is required: one sentence the person sees, at most %d characters", clientProposalReasonMax)
	}
	var cleaned strings.Builder
	for _, r := range reason {
		if !clientHostileRune(r) || r == '"' {
			cleaned.WriteRune(r)
		}
	}
	reason = cleaned.String()
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_proposals WHERE thread_id = ? AND state = 'pending'`, t.ID).Scan(&pending)
	if pending >= clientProposalsPending {
		return nil, fmt.Errorf("this thread already has %d suggestions waiting for the person", clientProposalsPending)
	}
	var dup int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_proposals WHERE thread_id = ? AND bot = ? AND state = 'pending'`, t.ID, bot).Scan(&dup)
	if dup > 0 {
		return nil, errors.New("adding that bot is already suggested; the person has not answered yet")
	}
	if !clientLimits.allow("propose:"+a.Bot+":"+a.UserID, clientProposalsPerDay, 24*time.Hour, now) {
		return nil, fmt.Errorf("at most %d suggestions a day for one person", clientProposalsPerDay)
	}
	id := clientID("bp_", 8)
	if _, err := db.Exec(`INSERT INTO app_proposals (id, thread_id, user_id, by_bot, bot, reason, state, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		id, t.ID, a.UserID, a.Bot, bot, reason, nowStamp(now), nowStamp(now.Add(clientProposalTTL))); err != nil {
		return nil, err
	}
	p, _ := proposalByID(id)
	appendClientEvent(t.ID, "proposal_created", map[string]any{"proposal": p}, now)
	threadUpdated(t.ID, []string{"proposals"}, now)
	return map[string]any{"proposal_id": id, "state": "pending"}, nil
}

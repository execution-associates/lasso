package main

// Client threads, the storage half (docs/design/client-threads.md): every
// conversation is a person's main thread with one bot, and other threads have a
// title, belong to that one person and may hold several of their bots.
//
// The schema is additive: app_convs stays as the (person, bot) -> main-thread
// index, and the columns that held a conversation id (app_messages.conv_id and
// friends) now hold a thread id. A main thread's id is its conversation's, so
// an older lasso on the same db keeps working on main threads.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// clientThreadsSchema is created after the column migrations, since its indexes
// and triggers read columns those add.
const clientThreadsSchema = `
CREATE TABLE IF NOT EXISTS app_threads (
	id                TEXT PRIMARY KEY,
	user_id           TEXT NOT NULL,
	kind              TEXT NOT NULL,
	main_bot          TEXT NOT NULL DEFAULT '',
	title             TEXT NOT NULL DEFAULT '',
	anchor_thread_id  TEXT NOT NULL DEFAULT '',
	anchor_message_id TEXT NOT NULL DEFAULT '',
	anchor_role       TEXT NOT NULL DEFAULT '',
	anchor_bot        TEXT NOT NULL DEFAULT '',
	anchor_text       TEXT NOT NULL DEFAULT '',
	anchor_at         TEXT NOT NULL DEFAULT '',
	state             TEXT NOT NULL DEFAULT 'open',
	pinned_at         TEXT NOT NULL DEFAULT '',
	muted             INTEGER NOT NULL DEFAULT 0,
	summary           TEXT NOT NULL DEFAULT '',
	summary_by        TEXT NOT NULL DEFAULT '',
	summary_at        TEXT NOT NULL DEFAULT '',
	summary_seq       INTEGER NOT NULL DEFAULT 0,
	last_read_seq     INTEGER NOT NULL DEFAULT 0,
	last_seq          INTEGER NOT NULL DEFAULT 0,
	last_activity_at  TEXT NOT NULL,
	created_by        TEXT NOT NULL,
	client_id         TEXT NOT NULL DEFAULT '',
	created_at        TEXT NOT NULL,
	updated_at        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS app_threads_user ON app_threads(user_id, state, last_activity_at);
CREATE UNIQUE INDEX IF NOT EXISTS app_threads_anchor ON app_threads(anchor_message_id) WHERE anchor_message_id != '';
CREATE UNIQUE INDEX IF NOT EXISTS app_threads_client_id ON app_threads(user_id, client_id) WHERE client_id != '';

CREATE TABLE IF NOT EXISTS app_thread_bots (
	thread_id       TEXT NOT NULL,
	bot             TEXT NOT NULL,
	position        INTEGER NOT NULL,
	joined_at       TEXT NOT NULL,
	added_by        TEXT NOT NULL,
	removed_at      TEXT NOT NULL DEFAULT '',
	removed_reason  TEXT NOT NULL DEFAULT '',
	ctx_epoch       INTEGER NOT NULL DEFAULT -1,
	ctx_seq         INTEGER NOT NULL DEFAULT 0,
	ctx_at          TEXT NOT NULL DEFAULT '',
	ctx_deliveries  INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (thread_id, bot)
);
CREATE INDEX IF NOT EXISTS app_thread_bots_bot ON app_thread_bots(bot);

CREATE TABLE IF NOT EXISTS app_message_deliveries (
	message_id    TEXT NOT NULL,
	thread_id     TEXT NOT NULL,
	bot           TEXT NOT NULL,
	event_id      INTEGER NOT NULL DEFAULT 0,
	state         TEXT NOT NULL DEFAULT 'queued',
	failed_reason TEXT NOT NULL DEFAULT '',
	routed        TEXT NOT NULL DEFAULT 'lead',
	PRIMARY KEY (message_id, bot)
);
CREATE INDEX IF NOT EXISTS app_message_deliveries_thread ON app_message_deliveries(thread_id);
CREATE INDEX IF NOT EXISTS app_message_deliveries_event ON app_message_deliveries(event_id);

CREATE TABLE IF NOT EXISTS app_attachment_copies (
	attachment_id TEXT NOT NULL,
	bot           TEXT NOT NULL,
	path          TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	PRIMARY KEY (attachment_id, bot)
);

CREATE TABLE IF NOT EXISTS app_proposals (
	id          TEXT PRIMARY KEY,
	thread_id   TEXT NOT NULL,
	user_id     TEXT NOT NULL,
	by_bot      TEXT NOT NULL,
	bot         TEXT NOT NULL,
	reason      TEXT NOT NULL DEFAULT '',
	state       TEXT NOT NULL DEFAULT 'pending',
	created_at  TEXT NOT NULL,
	expires_at  TEXT NOT NULL,
	decided_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS app_proposals_thread ON app_proposals(thread_id, state);
CREATE UNIQUE INDEX IF NOT EXISTS app_proposals_pending ON app_proposals(thread_id, bot) WHERE state = 'pending';

CREATE TABLE IF NOT EXISTS app_push_tokens (
	id            TEXT PRIMARY KEY,
	device_id     TEXT NOT NULL UNIQUE,
	user_id       TEXT NOT NULL,
	provider      TEXT NOT NULL,
	token         TEXT NOT NULL UNIQUE,
	environment   TEXT NOT NULL DEFAULT '',
	app_id        TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	last_ok_at    TEXT NOT NULL DEFAULT '',
	last_error    TEXT NOT NULL DEFAULT '',
	last_error_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS app_events_user ON app_events(user_id, seq);

CREATE VIRTUAL TABLE IF NOT EXISTS app_search USING fts5(
	body, kind UNINDEXED, user_id UNINDEXED, thread_id UNINDEXED, ref_id UNINDEXED,
	tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS app_search_msg_ins AFTER INSERT ON app_messages
WHEN NEW.role IN ('user', 'bot') AND NEW.text != '' BEGIN
	INSERT INTO app_search(rowid, body, kind, user_id, thread_id, ref_id) VALUES (NEW.seq*4, NEW.text, 'message',
		COALESCE((SELECT user_id FROM app_threads WHERE id = NEW.conv_id), (SELECT user_id FROM app_convs WHERE id = NEW.conv_id), ''),
		NEW.conv_id, NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS app_search_msg_del AFTER DELETE ON app_messages BEGIN
	DELETE FROM app_search WHERE rowid = OLD.seq*4;
END;
CREATE TRIGGER IF NOT EXISTS app_search_att_sent AFTER UPDATE OF message_id ON app_attachments
WHEN OLD.message_id = '' AND NEW.message_id != '' BEGIN
	INSERT OR REPLACE INTO app_search(rowid, body, kind, user_id, thread_id, ref_id)
		VALUES (NEW.rowid*4+1, NEW.name, 'file', NEW.user_id, NEW.conv_id, NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS app_search_att_ins AFTER INSERT ON app_attachments
WHEN NEW.message_id != '' BEGIN
	INSERT OR REPLACE INTO app_search(rowid, body, kind, user_id, thread_id, ref_id)
		VALUES (NEW.rowid*4+1, NEW.name, 'file', NEW.user_id, NEW.conv_id, NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS app_search_att_del AFTER DELETE ON app_attachments BEGIN
	DELETE FROM app_search WHERE rowid = OLD.rowid*4+1;
END;
CREATE TRIGGER IF NOT EXISTS app_search_title AFTER UPDATE OF title ON app_threads WHEN NEW.kind = 'thread' BEGIN
	DELETE FROM app_search WHERE rowid = NEW.rowid*4+2;
	INSERT INTO app_search(rowid, body, kind, user_id, thread_id, ref_id) VALUES (NEW.rowid*4+2, NEW.title, 'thread', NEW.user_id, NEW.id, NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS app_search_title_ins AFTER INSERT ON app_threads WHEN NEW.kind = 'thread' BEGIN
	INSERT INTO app_search(rowid, body, kind, user_id, thread_id, ref_id) VALUES (NEW.rowid*4+2, NEW.title, 'thread', NEW.user_id, NEW.id, NEW.id);
END;
CREATE TRIGGER IF NOT EXISTS app_search_title_del AFTER DELETE ON app_threads BEGIN
	DELETE FROM app_search WHERE rowid = OLD.rowid*4+2;
END;
`

// clientThreadsColumns are the additive columns client threads need on the
// client API's (and the bots') existing tables.
var clientThreadsColumns = []string{
	`ALTER TABLE app_messages ADD COLUMN bot TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_messages ADD COLUMN mentions TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_messages ADD COLUMN system TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_events ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_users ADD COLUMN push_previews INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE app_devices ADD COLUMN presence TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_devices ADD COLUMN presence_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE app_devices ADD COLUMN stream_seen_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE bots ADD COLUMN client_epoch INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE bots ADD COLUMN client_session TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE bots ADD COLUMN client_deliveries INTEGER NOT NULL DEFAULT 0`,
	// The channel announced threads1: it hands the bot thread attributes and
	// the context block, and has the thread tools (botchannel.go).
	`ALTER TABLE bots ADD COLUMN channel_threads INTEGER NOT NULL DEFAULT 0`,
}

const clientThreadsMigratedKey = "client_threads_migrated"

// migrateClientThreads makes every conversation a main thread, fills in what
// existing messages and events need to know, and (once) marks what was already
// seen as read and indexes what exists. It runs at every start, in one
// transaction, and changes nothing the second time.
func migrateClientThreads(h *sql.DB, now time.Time) error {
	tx, err := h.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		// 1. every conversation is a main thread (an older lasso may have made
		// new ones since the last start)
		`INSERT OR IGNORE INTO app_threads (id, user_id, kind, main_bot, last_seq, last_activity_at, created_by, created_at, updated_at)
			SELECT c.id, c.user_id, 'main', c.bot,
				COALESCE((SELECT MAX(seq) FROM app_messages WHERE conv_id = c.id), 0),
				COALESCE((SELECT MAX(created_at) FROM app_messages WHERE conv_id = c.id), c.created_at),
				'main', c.created_at, c.created_at
			FROM app_convs c`,
		`INSERT OR IGNORE INTO app_thread_bots (thread_id, bot, position, joined_at, added_by)
			SELECT id, bot, 0, created_at, 'main' FROM app_convs`,
		// 2. authors and deliveries of existing messages
		`UPDATE app_messages SET bot = (SELECT bot FROM app_convs WHERE id = app_messages.conv_id)
			WHERE role = 'bot' AND bot = '' AND conv_id IN (SELECT id FROM app_convs)`,
		`INSERT OR IGNORE INTO app_message_deliveries (message_id, thread_id, bot, event_id, state, failed_reason, routed)
			SELECT m.id, m.conv_id, c.bot, m.event_id, m.state, m.failed_reason, 'main'
			FROM app_messages m JOIN app_convs c ON c.id = m.conv_id WHERE m.role = 'user'`,
		// 3. events learn their person
		`UPDATE app_events SET user_id = (SELECT user_id FROM app_threads WHERE id = app_events.conv_id)
			WHERE user_id = '' AND conv_id IN (SELECT id FROM app_threads)`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	var done string
	_ = tx.QueryRow(`SELECT value FROM settings WHERE key = ?`, clientThreadsMigratedKey).Scan(&done)
	if done == "" {
		for _, q := range []string{
			// 4. what the person has already seen is read: no flood of unread
			// after the upgrade
			`UPDATE app_threads SET last_read_seq = last_seq WHERE kind = 'main'`,
			// 5. index what exists
			`INSERT OR REPLACE INTO app_search(rowid, body, kind, user_id, thread_id, ref_id)
				SELECT m.seq*4, m.text, 'message', t.user_id, m.conv_id, m.id
				FROM app_messages m JOIN app_threads t ON t.id = m.conv_id WHERE m.role IN ('user', 'bot') AND m.text != ''`,
			`INSERT OR REPLACE INTO app_search(rowid, body, kind, user_id, thread_id, ref_id)
				SELECT a.rowid*4+1, a.name, 'file', a.user_id, a.conv_id, a.id FROM app_attachments a WHERE a.message_id != ''`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			clientThreadsMigratedKey, nowStamp(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// backfillMainThread makes the thread of one conversation an older lasso made
// since the last start (migrateClientThreads step 1, for that row only).
func backfillMainThread(convID string) {
	_, _ = db.Exec(`INSERT OR IGNORE INTO app_threads (id, user_id, kind, main_bot, last_seq, last_activity_at, created_by, created_at, updated_at)
		SELECT c.id, c.user_id, 'main', c.bot,
			COALESCE((SELECT MAX(seq) FROM app_messages WHERE conv_id = c.id), 0),
			COALESCE((SELECT MAX(created_at) FROM app_messages WHERE conv_id = c.id), c.created_at),
			'main', c.created_at, c.created_at
		FROM app_convs c WHERE c.id = ?`, convID)
	_, _ = db.Exec(`INSERT OR IGNORE INTO app_thread_bots (thread_id, bot, position, joined_at, added_by)
		SELECT id, bot, 0, created_at, 'main' FROM app_convs WHERE id = ?`, convID)
	_, _ = db.Exec(`UPDATE app_threads SET last_read_seq = last_seq WHERE id = ? AND last_read_seq = 0`, convID)
}

// --- threads ----------------------------------------------------------------

type appThread struct {
	ID, UserID, Kind, MainBot, Title                                             string
	AnchorThreadID, AnchorMessageID, AnchorRole, AnchorBot, AnchorText, AnchorAt string
	State, PinnedAt                                                              string
	Muted                                                                        bool
	Summary, SummaryBy, SummaryAt                                                string
	SummarySeq, LastReadSeq, LastSeq                                             int64
	LastActivityAt, CreatedBy, ClientID, CreatedAt, UpdatedAt                    string
}

const appThreadCols = `id, user_id, kind, main_bot, title, anchor_thread_id, anchor_message_id, anchor_role, anchor_bot, anchor_text, anchor_at,
	state, pinned_at, muted, summary, summary_by, summary_at, summary_seq, last_read_seq, last_seq, last_activity_at, created_by, client_id, created_at, updated_at`

func scanAppThread(row interface{ Scan(...any) error }) (*appThread, error) {
	var t appThread
	err := row.Scan(&t.ID, &t.UserID, &t.Kind, &t.MainBot, &t.Title, &t.AnchorThreadID, &t.AnchorMessageID, &t.AnchorRole, &t.AnchorBot, &t.AnchorText, &t.AnchorAt,
		&t.State, &t.PinnedAt, &t.Muted, &t.Summary, &t.SummaryBy, &t.SummaryAt, &t.SummarySeq, &t.LastReadSeq, &t.LastSeq, &t.LastActivityAt, &t.CreatedBy, &t.ClientID, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	return &t, err
}

func (t *appThread) isMain() bool { return t.Kind == "main" }

// conv is the thread as the conversation-shaped handle the message, file and
// call code works with, for one bot: a main thread's conversation, or this
// thread with the bot a write is for.
func (t *appThread) conv(bot string) *appConv {
	return &appConv{ID: t.ID, UserID: t.UserID, Bot: bot, CreatedAt: t.CreatedAt}
}

// threadByID reads a thread. A conversation an older lasso made since the last
// start has no thread yet: it gets one now.
func threadByID(id string) (*appThread, error) {
	t, err := scanAppThread(db.QueryRow(`SELECT `+appThreadCols+` FROM app_threads WHERE id = ?`, id))
	if errors.Is(err, errClientNotFound) && strings.HasPrefix(id, "c_") {
		if _, cerr := convByID(id); cerr == nil {
			backfillMainThread(id)
			return scanAppThread(db.QueryRow(`SELECT `+appThreadCols+` FROM app_threads WHERE id = ?`, id))
		}
	}
	return t, err
}

// threadVisible says whether a person sees a thread: theirs, and for a main
// thread only while they hold the grant on its bot.
func threadVisible(t *appThread, userID string) bool {
	if t == nil || t.UserID != userID {
		return false
	}
	if t.isMain() {
		_, err := getAppGrant(userID, t.MainBot)
		return err == nil
	}
	return true
}

type threadBot struct {
	Bot           string
	Position      int
	JoinedAt      string
	AddedBy       string
	RemovedAt     string
	RemovedReason string
	CtxEpoch      int64
	CtxSeq        int64
	CtxAt         string
	CtxDeliveries int64
}

const threadBotCols = `bot, position, joined_at, added_by, removed_at, removed_reason, ctx_epoch, ctx_seq, ctx_at, ctx_deliveries`

func scanThreadBot(row interface{ Scan(...any) error }) (threadBot, error) {
	var b threadBot
	err := row.Scan(&b.Bot, &b.Position, &b.JoinedAt, &b.AddedBy, &b.RemovedAt, &b.RemovedReason, &b.CtxEpoch, &b.CtxSeq, &b.CtxAt, &b.CtxDeliveries)
	return b, err
}

type rowQuerier interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

// activeBotsQ are a thread's active participants in join order: the first leads.
func activeBotsQ(q rowQuerier, threadID string) []threadBot {
	rows, err := q.Query(`SELECT `+threadBotCols+` FROM app_thread_bots WHERE thread_id = ? AND removed_at = '' ORDER BY position, joined_at, bot`, threadID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []threadBot
	for rows.Next() {
		if b, err := scanThreadBot(rows); err == nil {
			out = append(out, b)
		}
	}
	return out
}

func activeBots(threadID string) []threadBot { return activeBotsQ(db, threadID) }

func activeBotNames(threadID string) []string {
	out := []string{}
	for _, b := range activeBots(threadID) {
		out = append(out, b.Bot)
	}
	return out
}

func threadLead(threadID string) string {
	if bs := activeBots(threadID); len(bs) > 0 {
		return bs[0].Bot
	}
	return ""
}

// threadParticipant reads one bot's row in a thread (active or removed).
func threadParticipant(threadID, bot string) (threadBot, bool) {
	b, err := scanThreadBot(db.QueryRow(`SELECT `+threadBotCols+` FROM app_thread_bots WHERE thread_id = ? AND bot = ?`, threadID, bot))
	return b, err == nil
}

func isActiveParticipant(threadID, bot string) bool {
	b, ok := threadParticipant(threadID, bot)
	return ok && b.RemovedAt == ""
}

// touchThread notes a new message in a thread: its newest seq and activity,
// and a new message from the person or a bot moves an archived thread back to
// open (it answers whether it did).
func touchThread(threadID string, seq int64, role string, now time.Time) (reopened bool) {
	stamp := nowStamp(now)
	_, _ = db.Exec(`UPDATE app_threads SET last_seq = MAX(last_seq, ?), last_activity_at = ?, updated_at = ? WHERE id = ?`, seq, stamp, stamp, threadID)
	if role == "user" || role == "bot" {
		res, err := db.Exec(`UPDATE app_threads SET state = 'open' WHERE id = ? AND state = 'archived'`, threadID)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 1 {
				return true
			}
		}
	}
	return false
}

// messageSeq is a message's cursor, or 0.
func messageSeq(id string) int64 {
	var seq int64
	_ = db.QueryRow(`SELECT seq FROM app_messages WHERE id = ?`, id).Scan(&seq)
	return seq
}

// newestSeq is the newest message of a thread, or 0.
func newestSeq(threadID string) int64 {
	var s sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(seq) FROM app_messages WHERE conv_id = ?`, threadID).Scan(&s)
	return s.Int64
}

// --- deliveries ---------------------------------------------------------------

type clientDeliveryView struct {
	Bot          string `json:"bot"`
	State        string `json:"state"`
	FailedReason string `json:"failed_reason,omitempty"`
	Routed       string `json:"routed"`
}

// deliveriesOf are the deliveries of messages, by message id, in the order
// they were made.
func deliveriesOf(ids []string) map[string][]clientDeliveryView {
	out := map[string][]clientDeliveryView{}
	if len(ids) == 0 {
		return out
	}
	ph := strings.Repeat("?,", len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT message_id, bot, state, failed_reason, routed FROM app_message_deliveries WHERE message_id IN (`+ph[:len(ph)-1]+`) ORDER BY rowid`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var d clientDeliveryView
		if rows.Scan(&mid, &d.Bot, &d.State, &d.FailedReason, &d.Routed) == nil {
			if d.State != "failed" {
				d.FailedReason = ""
			}
			out[mid] = append(out[mid], d)
		}
	}
	return out
}

// aggregateState is a person's message's state from its deliveries: queued
// while any is, else delivered if any was, else failed with the first reason.
func aggregateState(ds []clientDeliveryView) (state, reason string) {
	if len(ds) == 0 {
		return "", ""
	}
	delivered := false
	for _, d := range ds {
		switch d.State {
		case "queued":
			return "queued", ""
		case "delivered":
			delivered = true
		}
	}
	if delivered {
		return "delivered", ""
	}
	for _, d := range ds {
		if d.State == "failed" {
			return "failed", d.FailedReason
		}
	}
	return "failed", ""
}

// --- the stub under an anchor -------------------------------------------------

type clientThreadStub struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Bots           []string `json:"bots"`
	ReplyCount     int      `json:"reply_count"`
	Unread         int      `json:"unread"`
	LastActivityAt string   `json:"last_activity_at"`
}

// threadStubsOf are the threads hanging off messages of a main thread, by the
// anchor message's id.
func threadStubsOf(mainID string) map[string]*clientThreadStub {
	out := map[string]*clientThreadStub{}
	rows, err := db.Query(`SELECT `+appThreadCols+` FROM app_threads WHERE anchor_thread_id = ? AND anchor_message_id != '' AND kind = 'thread'`, mainID)
	if err != nil {
		return out
	}
	var ts []*appThread
	for rows.Next() {
		if t, err := scanAppThread(rows); err == nil {
			ts = append(ts, t)
		}
	}
	rows.Close()
	for _, t := range ts {
		out[t.AnchorMessageID] = &clientThreadStub{ID: t.ID, Title: t.Title, Bots: activeBotNames(t.ID),
			ReplyCount: threadReplyCount(t.ID), Unread: threadUnread(t), LastActivityAt: t.LastActivityAt}
	}
	return out
}

func threadReplyCount(threadID string) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role IN ('user', 'bot')`, threadID).Scan(&n)
	return n
}

// threadUnread counts the bot's finished messages after the read marker.
func threadUnread(t *appThread) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND role = 'bot' AND done = 1 AND seq > ?`, t.ID, t.LastReadSeq).Scan(&n)
	return n
}

// clientStubChanged tells the person's streams that the stub under a
// thread's anchor changed (a new message, a read, a rename, its bots): a
// thread_stub event on the main thread the anchor is in. It is not a message
// event, so it is never pushed or counted as unread.
func clientStubChanged(threadID string, now time.Time) {
	t, err := threadByID(threadID)
	if err != nil || t.isMain() || t.AnchorMessageID == "" || t.AnchorThreadID == "" {
		return
	}
	stub := threadStubsOf(t.AnchorThreadID)[t.AnchorMessageID]
	if stub == nil {
		return
	}
	appendClientEventFor(t.AnchorThreadID, t.UserID, "thread_stub",
		map[string]any{"thread_id": t.AnchorThreadID, "message_id": t.AnchorMessageID, "thread": stub}, now)
}

// --- system messages ----------------------------------------------------------

type clientSystemView struct {
	Type  string `json:"type"`
	Bot   string `json:"bot,omitempty"`
	By    string `json:"by"`
	Title string `json:"title,omitempty"`
	// Limit is the run of bot messages that paused them (bots_paused).
	Limit int `json:"limit,omitempty"`
}

// addSystemMessage records lasso noting a change in a thread that is not main
// (a bot added or removed, a rename) and announces it.
func addSystemMessage(t *appThread, sys clientSystemView, text string, now time.Time) (clientMessageView, error) {
	if t.isMain() {
		return clientMessageView{}, errors.New("a main thread has no system messages")
	}
	sb, _ := json.Marshal(sys)
	id := clientID("m_", 12)
	res, err := db.Exec(`INSERT INTO app_messages (id, conv_id, role, text, done, mode, attachments, state, system, created_at)
		VALUES (?, ?, 'system', ?, 1, 'text', '[]', '', ?, ?)`, id, t.ID, text, string(sb), nowStamp(now))
	if err != nil {
		return clientMessageView{}, err
	}
	seq, _ := res.LastInsertId()
	touchThread(t.ID, seq, "system", now)
	m, err := messageByID(t.ID, id)
	if err != nil {
		return m, err
	}
	appendClientEvent(t.ID, "message", m, now)
	clientStubChanged(t.ID, now)
	return m, nil
}

// --- small text helpers ---------------------------------------------------------

// clientPreview is text shortened for a list: line breaks as spaces, at most n
// characters.
func clientPreview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		r := []rune(s)
		return string(r[:n])
	}
	return s
}

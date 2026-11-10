package main

// Client threads, the person's side (docs/design/client-threads.md): list,
// start, read, rename, pin, mute, archive and delete threads, add and remove
// bots, move the read marker, answer proposals, and the person stream that
// carries every thread's events on one connection.
//
// Every route resolves its ids against the token's user: an unknown id,
// someone else's, and an ungranted main thread's all answer the same 404.

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	clientThreadsMax        = 1000
	clientThreadsPerHour    = 30
	clientThreadTitleMax    = 80
	clientThreadAutoTitle   = 60
	clientThreadSummaryMax  = 4000
	clientAnchorTextMax     = 500
	clientProposalsPending  = 3
	clientProposalTTL       = 7 * 24 * time.Hour
	clientProposalReasonMax = 280
	clientPreviewMax        = 140
)

// --- views --------------------------------------------------------------------

type clientThreadBotView struct {
	Name     string `json:"name"`
	Lead     bool   `json:"lead"`
	JoinedAt string `json:"joined_at"`
	AddedBy  string `json:"added_by,omitempty"`
}

type clientAnchorView struct {
	ThreadID  string `json:"thread_id"`
	MessageID string `json:"message_id"`
	// Cursor is the anchor message's cursor in its main thread, for
	// messages?around=; absent once the message is gone.
	Cursor    string `json:"cursor,omitempty"`
	Role      string `json:"role"`
	Bot       string `json:"bot,omitempty"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
	Available bool   `json:"available"`
}

type clientSummaryView struct {
	Text      string `json:"text"`
	By        string `json:"by"`
	UpdatedAt string `json:"updated_at"`
}

type clientLastMessageView struct {
	ID        string `json:"id"`
	Cursor    string `json:"cursor"`
	Role      string `json:"role"`
	Bot       string `json:"bot,omitempty"`
	Preview   string `json:"preview"`
	CreatedAt string `json:"created_at"`
}

type clientThreadView struct {
	ID             string                 `json:"id"`
	Kind           string                 `json:"kind"`
	Bot            string                 `json:"bot,omitempty"`
	Title          string                 `json:"title,omitempty"`
	Bots           []clientThreadBotView  `json:"bots"`
	Lead           string                 `json:"lead,omitempty"`
	Anchor         *clientAnchorView      `json:"anchor,omitempty"`
	State          string                 `json:"state"`
	Pinned         bool                   `json:"pinned"`
	Muted          bool                   `json:"muted"`
	Summary        *clientSummaryView     `json:"summary,omitempty"`
	ReplyCount     int                    `json:"reply_count"`
	Unread         int                    `json:"unread"`
	ReadCursor     string                 `json:"read_cursor"`
	LastMessage    *clientLastMessageView `json:"last_message,omitempty"`
	LastActivityAt string                 `json:"last_activity_at"`
	Proposals      []clientProposalView   `json:"proposals"`
	Call           *appCall               `json:"call"`
	CreatedBy      string                 `json:"created_by"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
}

func addedByView(s string) string {
	switch {
	case strings.HasPrefix(s, "proposal:"):
		return "proposal"
	case strings.HasPrefix(s, "bot:"):
		return "bot"
	}
	return ""
}

func createdByView(s string) string {
	if strings.HasPrefix(s, "bot:") {
		return "bot"
	}
	return s
}

// attachmentsPreview is what a list shows for a message with only files.
func attachmentsPreview(raw []clientAttachmentView) string {
	switch {
	case len(raw) == 1 && strings.HasPrefix(raw[0].Type, "image/"):
		return "Photo"
	case len(raw) == 1:
		return "File"
	case len(raw) > 1:
		return fmt.Sprintf("%d files", len(raw))
	}
	return ""
}

func threadViewOf(t *appThread) clientThreadView { return threadViewSnap(t, nil) }

// threadViewSnap is threadViewOf with its unread count taken at a stream
// position (clientSnapshot), so it agrees with the events after it.
func threadViewSnap(t *appThread, snap *clientSnapshot) clientThreadView {
	v := clientThreadView{ID: t.ID, Kind: t.Kind, State: t.State, Pinned: t.PinnedAt != "", Muted: t.Muted,
		ReadCursor: strconv.FormatInt(t.LastReadSeq, 10), LastActivityAt: t.LastActivityAt,
		CreatedBy: createdByView(t.CreatedBy), CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Bots: []clientThreadBotView{}, Proposals: []clientProposalView{}}
	for i, b := range activeBots(t.ID) {
		bv := clientThreadBotView{Name: b.Bot, Lead: i == 0, JoinedAt: b.JoinedAt}
		if !t.isMain() {
			bv.AddedBy = addedByView(b.AddedBy)
		}
		v.Bots = append(v.Bots, bv)
		if i == 0 {
			v.Lead = b.Bot
		}
	}
	if t.isMain() {
		v.Bot = t.MainBot
		v.State = "open"
	} else {
		v.Title = t.Title
		if t.AnchorMessageID != "" {
			var seq int64
			_ = db.QueryRow(`SELECT seq FROM app_messages WHERE id = ? AND conv_id = ?`, t.AnchorMessageID, t.AnchorThreadID).Scan(&seq)
			v.Anchor = &clientAnchorView{ThreadID: t.AnchorThreadID, MessageID: t.AnchorMessageID, Role: t.AnchorRole, Bot: t.AnchorBot,
				Text: t.AnchorText, CreatedAt: t.AnchorAt, Available: seq > 0}
			if seq > 0 {
				v.Anchor.Cursor = cursorString(seq)
			}
		}
	}
	if t.Summary != "" {
		v.Summary = &clientSummaryView{Text: t.Summary, By: t.SummaryBy, UpdatedAt: t.SummaryAt}
	}
	v.ReplyCount = threadReplyCount(t.ID)
	v.Unread = threadUnreadSnap(t, snap)
	if m, err := scanAppMessage(db.QueryRow(`SELECT `+appMessageCols+` FROM app_messages WHERE conv_id = ? ORDER BY seq DESC LIMIT 1`, t.ID)); err == nil {
		lm := &clientLastMessageView{ID: m.ID, Cursor: m.Cursor, Role: m.Role, Bot: m.Bot, CreatedAt: m.CreatedAt, Preview: clientPreview(m.Text, clientPreviewMax)}
		if lm.Preview == "" {
			lm.Preview = attachmentsPreview(m.Attachments)
		}
		v.LastMessage = lm
	}
	v.Proposals = pendingProposalsOf(t.ID)
	if c, err := openCallOf(t.ID); err == nil {
		v.Call = c
	}
	return v
}

// --- counts ------------------------------------------------------------------

// visibleThreadSQL is the condition, over app_threads t, that a thread is the
// person's (the first argument) and visible to them.
const visibleThreadSQL = `t.user_id = ? AND (t.kind = 'thread' OR EXISTS (SELECT 1 FROM app_grants g WHERE g.user_id = t.user_id AND g.bot = t.main_bot))`

// clientUnreadBadge is the person's unread total over every visible thread
// and the badge: unread in threads that are not muted, plus pending proposals
// in those.
func clientUnreadBadge(userID string) (unread, badge int) { return clientUnreadBadgeSnap(userID, nil) }

// clientSnapshot is a stream position and what it covers: messages up to
// MaxSeq (the newest when the position was read) whose message event is at
// or before Events. Counts taken against it leave out exactly what the stream
// replays from Events, so an app adding one per replayed message never counts
// one twice.
type clientSnapshot struct {
	UserID string
	Events int64
	MaxSeq int64
}

// takeClientSnapshot reads the person stream's position and the newest
// message together.
func takeClientSnapshot(userID string) *clientSnapshot {
	s := &clientSnapshot{UserID: userID}
	tx, err := db.Begin()
	if err != nil {
		return s
	}
	defer tx.Rollback()
	var m, e sql.NullInt64
	_ = tx.QueryRow(`SELECT MAX(seq) FROM app_messages`).Scan(&m)
	_ = tx.QueryRow(`SELECT MAX(seq) FROM app_events WHERE user_id = ?`, userID).Scan(&e)
	s.MaxSeq, s.Events = m.Int64, e.Int64
	return s
}

// snapSQL bounds a count over app_messages m to a snapshot.
func (s *clientSnapshot) sql() (string, []any) {
	if s == nil {
		return "", nil
	}
	return ` AND m.seq <= ? AND NOT EXISTS (SELECT 1 FROM app_events e WHERE e.user_id = ? AND e.seq > ? AND e.type = 'message' AND json_extract(e.data, '$.id') = m.id)`,
		[]any{s.MaxSeq, s.UserID, s.Events}
}

func threadUnreadSnap(t *appThread, snap *clientSnapshot) int {
	if snap == nil {
		return threadUnread(t)
	}
	extra, args := snap.sql()
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages m WHERE m.conv_id = ? AND m.role = 'bot' AND m.done = 1 AND m.seq > ?`+extra,
		append([]any{t.ID, t.LastReadSeq}, args...)...).Scan(&n)
	return n
}

func clientUnreadBadgeSnap(userID string, snap *clientSnapshot) (unread, badge int) {
	extra, args := snap.sql()
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages m JOIN app_threads t ON t.id = m.conv_id
		WHERE `+visibleThreadSQL+` AND m.role = 'bot' AND m.done = 1 AND m.seq > t.last_read_seq`+extra, append([]any{userID}, args...)...).Scan(&unread)
	var quiet, props int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages m JOIN app_threads t ON t.id = m.conv_id
		WHERE `+visibleThreadSQL+` AND t.muted = 1 AND m.role = 'bot' AND m.done = 1 AND m.seq > t.last_read_seq`+extra, append([]any{userID}, args...)...).Scan(&quiet)
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_proposals p JOIN app_threads t ON t.id = p.thread_id
		WHERE `+visibleThreadSQL+` AND t.muted = 0 AND p.state = 'pending'`, userID).Scan(&props)
	return unread, unread - quiet + props
}

// ensureMainThreads makes the main thread of every bot the person is granted.
func ensureMainThreads(userID string, now time.Time) {
	gs, err := listAppGrants(userID)
	if err != nil {
		return
	}
	for _, g := range gs {
		if _, err := getBot(g.Bot); err == nil {
			_, _ = convFor(userID, g.Bot, now)
		}
	}
}

// newestUserEventSeq is the person stream's position.
func newestUserEventSeq(userID string) int64 {
	var s sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(seq) FROM app_events WHERE user_id = ?`, userID).Scan(&s)
	return s.Int64
}

// --- the router ----------------------------------------------------------------

// serveClientThreadRoutes answers the thread routes that are not a thread's
// messages, events, attachments or calls; false when the path is none of them.
func serveClientThreadRoutes(w http.ResponseWriter, r *http.Request, id *clientIdentity, segs []string) bool {
	switch {
	case len(segs) == 1 && segs[0] == "threads":
		switch r.Method {
		case http.MethodGet:
			serveClientThreadList(w, r, id)
		case http.MethodPost:
			serveClientThreadCreate(w, r, id)
		default:
			clientMethod(w, "GET, POST")
		}
	case len(segs) == 2 && segs[0] == "threads":
		t, ok := clientOwnThread(id, segs[1])
		if !ok {
			clientNotFound(w)
			return true
		}
		switch r.Method {
		case http.MethodGet:
			clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t)})
		case http.MethodPatch:
			serveClientThreadPatch(w, r, id, t)
		case http.MethodDelete:
			serveClientThreadDelete(w, id, t)
		default:
			clientMethod(w, "GET, PATCH, DELETE")
		}
	case len(segs) == 4 && segs[0] == "threads" && segs[2] == "bots":
		t, ok := clientOwnThread(id, segs[1])
		if !ok {
			clientNotFound(w)
			return true
		}
		if r.Method != http.MethodDelete {
			clientMethod(w, "DELETE")
			return true
		}
		serveClientThreadRemoveBot(w, id, t, segs[3])
	case len(segs) == 1 && segs[0] == "events":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		serveClientPersonEvents(w, r, id)
	case len(segs) == 1 && segs[0] == "proposals":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		serveClientProposals(w, r, id)
	case len(segs) == 3 && segs[0] == "proposals" && (segs[2] == "accept" || segs[2] == "decline"):
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return true
		}
		serveClientProposalDecide(w, id, segs[1], segs[2] == "accept")
	default:
		return serveClientExtraRoutes(w, r, id, segs)
	}
	return true
}

// serveClientThreadSubroute is a thread's routes beside messages, events,
// attachments and calls: bots and read.
func serveClientThreadSubroute(w http.ResponseWriter, r *http.Request, id *clientIdentity, t *appThread, action string) bool {
	switch action {
	case "bots":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return true
		}
		serveClientThreadAddBot(w, r, id, t)
	case "read":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return true
		}
		serveClientThreadRead(w, r, id, t)
	default:
		return false
	}
	return true
}

// threadStreamAuthorized re-checks a per-thread stream: the device and the
// user, and the thread still visible.
func threadStreamAuthorized(id *clientIdentity, t *appThread) bool {
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM app_devices d JOIN app_users u ON u.id = d.user_id
		WHERE d.id = ? AND d.user_id = ? AND d.revoked_at = '' AND u.disabled = 0`, id.Device.ID, id.User.ID).Scan(&n) != nil || n != 1 {
		return false
	}
	cur, err := threadByID(t.ID)
	return err == nil && threadVisible(cur, id.User.ID)
}

// --- GET /threads ----------------------------------------------------------------

type clientListCursor struct {
	O int `json:"o"`
}

func encodeCursor(o int) string {
	b, _ := json.Marshal(clientListCursor{O: o})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, false
	}
	var c clientListCursor
	if json.Unmarshal(raw, &c) != nil || c.O < 0 {
		return 0, false
	}
	return c.O, true
}

func serveClientThreadList(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	q := r.URL.Query()
	now := time.Now()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			clientError(w, http.StatusBadRequest, "invalid_request", "limit must be 1-100.")
			return
		}
		limit = n
	}
	offset, ok := decodeCursor(q.Get("cursor"))
	if !ok {
		clientError(w, http.StatusBadRequest, "invalid_request", "cursor must come from next_cursor.")
		return
	}
	where := visibleThreadSQL
	args := []any{id.User.ID}
	switch q.Get("state") {
	case "", "open":
		where += ` AND t.state = 'open'`
	case "archived":
		where += ` AND t.state = 'archived'`
	case "all":
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be open, archived or all.")
		return
	}
	switch q.Get("kind") {
	case "", "all":
	case "main", "thread":
		where += ` AND t.kind = ?`
		args = append(args, q.Get("kind"))
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "kind must be all, main or thread.")
		return
	}
	if bot := q.Get("bot"); bot != "" {
		where += ` AND EXISTS (SELECT 1 FROM app_thread_bots b WHERE b.thread_id = t.id AND b.bot = ? AND b.removed_at = '')
			AND EXISTS (SELECT 1 FROM app_grants g2 WHERE g2.user_id = t.user_id AND g2.bot = ?)`
		args = append(args, bot, bot)
	}
	switch q.Get("pinned") {
	case "":
	case "true":
		where += ` AND t.pinned_at != ''`
	case "false":
		where += ` AND t.pinned_at = ''`
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "pinned must be true or false.")
		return
	}
	// The stream's position and the counts are one snapshot: nothing that
	// lands while the list is read is missed, and nothing the stream replays
	// from the position is already counted.
	ensureMainThreads(id.User.ID, now)
	snap := takeClientSnapshot(id.User.ID)
	cursor := snap.Events
	rows, err := db.Query(`SELECT `+prefixCols("t.", appThreadCols)+` FROM app_threads t WHERE `+where+`
		ORDER BY (t.pinned_at != '') DESC, t.pinned_at DESC, t.last_activity_at DESC, t.id LIMIT ? OFFSET ?`, append(args, limit+1, offset)...)
	if err != nil {
		clientFail(w, err)
		return
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
	views := make([]clientThreadView, 0, len(ts))
	for _, t := range ts {
		views = append(views, threadViewSnap(t, snap))
	}
	unread, badge := clientUnreadBadgeSnap(id.User.ID, snap)
	clientJSON(w, http.StatusOK, map[string]any{"threads": views, "next_cursor": next,
		"events_cursor": strconv.FormatInt(cursor, 10), "unread_total": unread, "badge": badge})
}

func prefixCols(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = p + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}

// --- POST /threads -----------------------------------------------------------------

type clientThreadCreateIn struct {
	Title           *string       `json:"title"`
	Bots            []string      `json:"bots"`
	AnchorMessageID string        `json:"anchor_message_id"`
	Message         *clientSendIn `json:"message"`
	ClientID        string        `json:"client_id"`
}

// cleanThreadTitle checks a title the person or a bot gave: 1-80 characters,
// one line, nothing that could reshape the markup it is shown in.
func cleanThreadTitle(s string) (string, bool) { return cleanLine(s, clientThreadTitleMax) }

// autoThreadTitle makes a title from text: its first line, cleaned, 60
// characters.
func autoThreadTitle(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	var sb strings.Builder
	for _, r := range line {
		if clientHostileRune(r) {
			continue
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if utf8.RuneCountInString(out) > clientThreadAutoTitle {
		out = strings.TrimSpace(string([]rune(out)[:clientThreadAutoTitle]))
	}
	return out
}

// errThreadExists carries the thread an anchor already has.
type errThreadExists struct{ ThreadID string }

func (e *errThreadExists) Error() string { return "That message already has a thread." }

// threadCreateSpec is a new thread, from the person or from a bot.
type threadCreateSpec struct {
	UserID    string
	Title     string
	Bots      []string
	Anchor    *clientMessageView // a message of a main thread
	AnchorTID string
	CreatedBy string // user | bot:<name>
	AddedBy   string // user | bot:<name>
	ClientID  string
	Summary   string
	SummaryBy string
}

// createThread stores a thread and its participants and announces it.
func createThread(s threadCreateSpec, now time.Time) (*appThread, error) {
	stamp := nowStamp(now)
	tid := clientID("t_", 12)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var aRole, aBot, aText, aAt, aMsg string
	if s.Anchor != nil {
		aMsg, aRole, aBot, aAt = s.Anchor.ID, s.Anchor.Role, s.Anchor.Bot, s.Anchor.CreatedAt
		aText = s.Anchor.Text
		if utf8.RuneCountInString(aText) > clientAnchorTextMax {
			aText = string([]rune(aText)[:clientAnchorTextMax])
		}
	}
	var sumAt, sumBy string
	if s.Summary != "" {
		sumAt, sumBy = stamp, s.SummaryBy
	}
	if _, err := tx.Exec(`INSERT INTO app_threads (id, user_id, kind, title, anchor_thread_id, anchor_message_id, anchor_role, anchor_bot, anchor_text, anchor_at,
		summary, summary_by, summary_at, last_activity_at, created_by, client_id, created_at, updated_at)
		VALUES (?, ?, 'thread', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tid, s.UserID, s.Title, s.AnchorTID, aMsg, aRole, aBot, aText, aAt, s.Summary, sumBy, sumAt, stamp, s.CreatedBy, s.ClientID, stamp, stamp); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			if aMsg != "" {
				var other string
				if db.QueryRow(`SELECT id FROM app_threads WHERE anchor_message_id = ?`, aMsg).Scan(&other) == nil {
					return nil, &errThreadExists{ThreadID: other}
				}
			}
		}
		return nil, err
	}
	for i, b := range s.Bots {
		if _, err := tx.Exec(`INSERT INTO app_thread_bots (thread_id, bot, position, joined_at, added_by) VALUES (?, ?, ?, ?, ?)`, tid, b, i, stamp, s.AddedBy); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t, err := threadByID(tid)
	if err != nil {
		return nil, err
	}
	appendClientEvent(t.ID, "thread_created", map[string]any{"thread": threadViewOf(t)}, now)
	return t, nil
}

func serveClientThreadCreate(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	now := time.Now()
	var in clientThreadCreateIn
	if !clientDecode(w, r, 256<<10, &in) {
		return
	}
	if in.ClientID != "" && !clientClientIDRE.MatchString(in.ClientID) {
		clientError(w, http.StatusBadRequest, "invalid_request", "client_id must be 1-64 letters, digits, . _ or -.")
		return
	}
	if in.ClientID != "" {
		var tid string
		if db.QueryRow(`SELECT id FROM app_threads WHERE user_id = ? AND client_id = ?`, id.User.ID, in.ClientID).Scan(&tid) == nil {
			if t, err := threadByID(tid); err == nil {
				clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t), "replayed": true})
				return
			}
		}
	}
	var anchor *clientMessageView
	var anchorTID string
	if in.AnchorMessageID != "" {
		var conv string
		if db.QueryRow(`SELECT conv_id FROM app_messages WHERE id = ?`, in.AnchorMessageID).Scan(&conv) != nil {
			clientNotFound(w)
			return
		}
		mt, ok := clientOwnThread(id, conv)
		if !ok || !mt.isMain() {
			clientNotFound(w)
			return
		}
		m, err := messageByID(conv, in.AnchorMessageID)
		if err != nil || m.Role == "system" {
			clientNotFound(w)
			return
		}
		var other string
		if db.QueryRow(`SELECT id FROM app_threads WHERE anchor_message_id = ?`, m.ID).Scan(&other) == nil {
			clientJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "thread_exists", "message": "That message already has a thread.", "thread_id": other}})
			return
		}
		anchor, anchorTID = &m, conv
		if len(in.Bots) == 0 {
			in.Bots = []string{mt.MainBot}
		}
	}
	if len(in.Bots) == 0 || len(in.Bots) > clientParticipantsMax {
		clientError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("bots: 1-%d bot names.", clientParticipantsMax))
		return
	}
	var bots []string
	seen := map[string]bool{}
	for _, b := range in.Bots {
		if seen[b] {
			continue
		}
		seen[b] = true
		if _, _, ok := grantedBotOf(id.User.ID, b); !ok {
			clientNotFound(w)
			return
		}
		bots = append(bots, b)
	}
	title := ""
	if in.Title != nil {
		var ok bool
		if title, ok = cleanThreadTitle(*in.Title); !ok {
			clientError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("title must be 1-%d characters, one line.", clientThreadTitleMax))
			return
		}
	}
	if title == "" && in.Message != nil {
		title = autoThreadTitle(in.Message.Text)
	}
	if title == "" && anchor != nil {
		title = autoThreadTitle(anchor.Text)
	}
	if title == "" {
		title = "New thread"
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_threads WHERE user_id = ? AND kind = 'thread'`, id.User.ID).Scan(&n)
	if n >= clientThreadsMax {
		clientError(w, http.StatusConflict, "limit_reached", fmt.Sprintf("You have %d threads: delete some first.", clientThreadsMax))
		return
	}
	// The first message's recipients must be able to take it, or no thread is
	// made.
	if in.Message != nil {
		if in.Message.Mode == "voice" || len(in.Message.Attachments) > 0 {
			clientError(w, http.StatusBadRequest, "invalid_request", "A thread's first message is text: upload files into the thread once it exists, then send.")
			return
		}
		names := in.Message.Mentions
		if len(names) == 0 {
			names = bots[:1]
		}
		if len(names) > clientMentionsMax {
			clientError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("at most %d mentions per message.", clientMentionsMax))
			return
		}
		for _, nm := range names {
			if !seen[nm] {
				clientError(w, http.StatusConflict, "not_participant", nm+" is not in this thread: add it first.")
				return
			}
			rec, _, _ := grantedBotOf(id.User.ID, nm)
			if state, _, reason := botClientState(rec, now); state == "stopped" || state == "unavailable" {
				clientError(w, http.StatusConflict, "bot_unavailable", nm+": "+reason)
				return
			}
		}
	}
	if !clientLimits.allow("thread:"+id.User.ID, clientThreadsPerHour, time.Hour, now) {
		w.Header().Set("Retry-After", "600")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many threads started. Try again later.")
		return
	}
	t, err := createThread(threadCreateSpec{UserID: id.User.ID, Title: title, Bots: bots, Anchor: anchor, AnchorTID: anchorTID,
		CreatedBy: "user", AddedBy: "user", ClientID: in.ClientID}, now)
	if err != nil {
		var te *errThreadExists
		if errors.As(err, &te) {
			clientJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "thread_exists", "message": te.Error(), "thread_id": te.ThreadID}})
			return
		}
		if in.ClientID != "" && strings.Contains(err.Error(), "UNIQUE") {
			var tid string
			if db.QueryRow(`SELECT id FROM app_threads WHERE user_id = ? AND client_id = ?`, id.User.ID, in.ClientID).Scan(&tid) == nil {
				if t, err := threadByID(tid); err == nil {
					clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t), "replayed": true})
					return
				}
			}
		}
		clientFail(w, err)
		return
	}
	out := map[string]any{"replayed": false}
	if in.Message != nil {
		m, _, err := clientSendThread(id, t, *in.Message, now)
		if err != nil {
			deleteThread(t, now, false)
			clientFail(w, err)
			return
		}
		out["message"] = m
	}
	t, _ = threadByID(t.ID)
	out["thread"] = threadViewOf(t)
	clientJSON(w, http.StatusCreated, out)
}

// --- PATCH /threads/{id} -------------------------------------------------------------

type clientThreadPatch struct {
	Title  *string `json:"title"`
	Pinned *bool   `json:"pinned"`
	Muted  *bool   `json:"muted"`
	State  *string `json:"state"`
	Lead   *string `json:"lead"`
}

func mainThreadErr() error {
	return cerr(http.StatusConflict, "main_thread", "A bot's main chat cannot be changed that way.")
}

func serveClientThreadPatch(w http.ResponseWriter, r *http.Request, id *clientIdentity, t *appThread) {
	now := time.Now()
	var p clientThreadPatch
	if !clientDecode(w, r, 16<<10, &p) {
		return
	}
	if p.Title == nil && p.Pinned == nil && p.Muted == nil && p.State == nil && p.Lead == nil {
		clientError(w, http.StatusBadRequest, "invalid_request", "Nothing to change: pass title, pinned, muted, state or lead.")
		return
	}
	if t.isMain() && (p.Title != nil || p.State != nil || p.Lead != nil) {
		clientFail(w, mainThreadErr())
		return
	}
	var title string
	if p.Title != nil {
		var ok bool
		if title, ok = cleanThreadTitle(*p.Title); !ok {
			clientError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("title must be 1-%d characters, one line.", clientThreadTitleMax))
			return
		}
	}
	if p.State != nil && *p.State != "open" && *p.State != "archived" {
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be open or archived.")
		return
	}
	if p.Lead != nil && !isActiveParticipant(t.ID, *p.Lead) {
		clientError(w, http.StatusConflict, "not_participant", *p.Lead+" is not in this thread.")
		return
	}
	badgeBefore := 0
	if p.Muted != nil {
		_, badgeBefore = clientUnreadBadge(t.UserID)
	}
	var changes []string
	stamp := nowStamp(now)
	if p.Title != nil && title != t.Title {
		if err := renameThread(t, title, "user", now); err != nil {
			clientFail(w, err)
			return
		}
		changes = append(changes, "title")
	}
	if p.Pinned != nil && *p.Pinned != (t.PinnedAt != "") {
		v := ""
		if *p.Pinned {
			v = stamp
		}
		_, _ = db.Exec(`UPDATE app_threads SET pinned_at = ?, updated_at = ? WHERE id = ?`, v, stamp, t.ID)
		changes = append(changes, "pinned")
	}
	if p.Muted != nil && *p.Muted != t.Muted {
		_, _ = db.Exec(`UPDATE app_threads SET muted = ?, updated_at = ? WHERE id = ?`, boolInt(*p.Muted), stamp, t.ID)
		changes = append(changes, "muted")
	}
	if p.State != nil && *p.State != t.State {
		_, _ = db.Exec(`UPDATE app_threads SET state = ?, updated_at = ? WHERE id = ?`, *p.State, stamp, t.ID)
		changes = append(changes, "state")
	}
	if p.Lead != nil && *p.Lead != threadLead(t.ID) {
		setThreadLead(t, *p.Lead, "user", now)
		changes = append(changes, "lead", "bots")
	}
	if len(changes) > 0 {
		threadUpdated(t.ID, changes, now)
	}
	if p.Muted != nil {
		clientBadgeMaybeDown(t.UserID, badgeBefore, now)
	}
	t, _ = threadByID(t.ID)
	clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t)})
}

// renameThread changes a thread's title and records it (system renamed).
func renameThread(t *appThread, title, by string, now time.Time) error {
	stamp := nowStamp(now)
	if _, err := db.Exec(`UPDATE app_threads SET title = ?, updated_at = ? WHERE id = ?`, title, stamp, t.ID); err != nil {
		return err
	}
	t.Title = title
	_, err := addSystemMessage(t, clientSystemView{Type: "renamed", By: by, Title: title}, "The thread was renamed to "+title+".", now)
	return err
}

// setThreadLead moves a participant to the front of the join order.
func setThreadLead(t *appThread, bot, by string, now time.Time) {
	var minPos int
	_ = db.QueryRow(`SELECT COALESCE(MIN(position), 0) FROM app_thread_bots WHERE thread_id = ?`, t.ID).Scan(&minPos)
	_, _ = db.Exec(`UPDATE app_thread_bots SET position = ? WHERE thread_id = ? AND bot = ?`, minPos-1, t.ID, bot)
	_, _ = db.Exec(`UPDATE app_threads SET updated_at = ? WHERE id = ?`, nowStamp(now), t.ID)
	_, _ = addSystemMessage(t, clientSystemView{Type: "lead_changed", Bot: bot, By: by}, bot+" leads the thread now.", now)
}

// threadUpdated announces a change to a thread (thread_updated, with its view
// as it now reads).
func threadUpdated(threadID string, changes []string, now time.Time) {
	t, err := threadByID(threadID)
	if err != nil {
		return
	}
	appendClientEvent(t.ID, "thread_updated", map[string]any{"thread": threadViewOf(t), "changes": changes}, now)
	clientStubChanged(t.ID, now)
}

// --- DELETE /threads/{id} -------------------------------------------------------------

func serveClientThreadDelete(w http.ResponseWriter, id *clientIdentity, t *appThread) {
	if t.isMain() {
		clientFail(w, mainThreadErr())
		return
	}
	now := time.Now()
	_, before := clientUnreadBadge(t.UserID)
	n := deleteThread(t, now, true)
	clientBadgeMaybeDown(t.UserID, before, now)
	clientJSON(w, http.StatusOK, map[string]any{"deleted": true, "removed_files": n})
}

// deleteThread removes a thread for good: an open call ended (the bot told),
// deliveries not yet taken dropped, its messages, events, proposals, files
// (the primary and every copy) and rows. announce sends thread_deleted.
func deleteThread(t *appThread, now time.Time, announce bool) int {
	if c, err := openCallOf(t.ID); err == nil {
		clientEndCall(c, "user", "", now, true)
	}
	dropClientEvents(`json_extract(meta, '$.thread_id') = ? AND json_extract(meta, '$.event') != 'call_ended'`, t.ID)
	_, _ = db.Exec(`UPDATE bot_events SET reason = 'the thread was deleted' WHERE kind = 'client' AND status = 'dropped' AND reason = 'access revoked' AND meta = '' AND id IN
		(SELECT event_id FROM app_message_deliveries WHERE thread_id = ?)`, t.ID)
	var atts []appAttachment
	if rows, err := db.Query(`SELECT `+appAttachmentCols+` FROM app_attachments WHERE conv_id = ?`, t.ID); err == nil {
		for rows.Next() {
			if a, err := scanAppAttachment(rows); err == nil {
				atts = append(atts, a)
			}
		}
		rows.Close()
	}
	files := 0
	for _, a := range atts {
		if a.ExpiredAt == "" {
			files++
		}
	}
	clientRemoveFiles(atts)
	for _, q := range []string{
		`DELETE FROM app_attachment_copies WHERE attachment_id IN (SELECT id FROM app_attachments WHERE conv_id = ?)`,
		`DELETE FROM app_attachments WHERE conv_id = ?`,
		`DELETE FROM app_reply_tokens WHERE conv_id = ?`,
		`DELETE FROM app_message_deliveries WHERE thread_id = ?`,
		`DELETE FROM app_messages WHERE conv_id = ?`,
		`DELETE FROM app_events WHERE conv_id = ?`,
		`DELETE FROM app_calls WHERE conv_id = ?`,
		`DELETE FROM app_proposals WHERE thread_id = ?`,
		`DELETE FROM app_thread_bots WHERE thread_id = ?`,
		`DELETE FROM app_threads WHERE id = ?`,
	} {
		_, _ = db.Exec(q, t.ID)
	}
	if announce {
		appendClientEventFor(t.ID, t.UserID, "thread_deleted", map[string]any{"thread_id": t.ID}, now)
	}
	return files
}

// --- participants ---------------------------------------------------------------

func serveClientThreadAddBot(w http.ResponseWriter, r *http.Request, id *clientIdentity, t *appThread) {
	var in struct {
		Bot string `json:"bot"`
	}
	if !clientDecode(w, r, 4<<10, &in) {
		return
	}
	if t.isMain() {
		clientFail(w, mainThreadErr())
		return
	}
	if _, _, ok := grantedBotOf(id.User.ID, in.Bot); !ok {
		clientNotFound(w)
		return
	}
	if err := addThreadBot(t, in.Bot, "user", "user", time.Now()); err != nil {
		clientFail(w, err)
		return
	}
	t, _ = threadByID(t.ID)
	clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t)})
}

// addThreadBot makes a bot an active participant (idempotent): a bot removed
// earlier comes back at the end of the join order. addedBy is stored, by is
// the system message's.
func addThreadBot(t *appThread, bot, addedBy, by string, now time.Time) error {
	if isActiveParticipant(t.ID, bot) {
		return nil
	}
	if len(activeBots(t.ID)) >= clientParticipantsMax {
		return cerr(http.StatusConflict, "limit_reached", fmt.Sprintf("A thread holds at most %d bots.", clientParticipantsMax))
	}
	var maxPos sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(position) FROM app_thread_bots WHERE thread_id = ?`, t.ID).Scan(&maxPos)
	stamp := nowStamp(now)
	if _, err := db.Exec(`INSERT INTO app_thread_bots (thread_id, bot, position, joined_at, added_by) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(thread_id, bot) DO UPDATE SET position = excluded.position, joined_at = excluded.joined_at, added_by = excluded.added_by,
			removed_at = '', removed_reason = '', ctx_epoch = -1, ctx_seq = 0, ctx_at = '', ctx_deliveries = 0`,
		t.ID, bot, maxPos.Int64+1, stamp, addedBy); err != nil {
		return err
	}
	_, _ = addSystemMessage(t, clientSystemView{Type: "bot_added", Bot: bot, By: by}, bot+" joined the thread.", now)
	threadUpdated(t.ID, []string{"bots"}, now)
	return nil
}

func serveClientThreadRemoveBot(w http.ResponseWriter, id *clientIdentity, t *appThread, bot string) {
	if t.isMain() {
		clientFail(w, mainThreadErr())
		return
	}
	if !isActiveParticipant(t.ID, bot) {
		clientNotFound(w)
		return
	}
	if len(activeBots(t.ID)) <= 1 {
		clientError(w, http.StatusConflict, "last_participant", "A thread needs at least one bot.")
		return
	}
	removeThreadBot(t, bot, "user", time.Now())
	t, _ = threadByID(t.ID)
	clientJSON(w, http.StatusOK, map[string]any{"thread": threadViewOf(t)})
}

// removeThreadBot takes a bot out of a thread: what is queued for it there is
// dropped, its pending proposals are withdrawn, and a system message records
// it. reason is user, access or bot_deleted.
func removeThreadBot(t *appThread, bot, reason string, now time.Time) {
	stamp := nowStamp(now)
	res, err := db.Exec(`UPDATE app_thread_bots SET removed_at = ?, removed_reason = ? WHERE thread_id = ? AND bot = ? AND removed_at = ''`, stamp, reason, t.ID, bot)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	_, _ = db.Exec(`UPDATE bot_events SET status = 'dropped', reason = 'the bot left the thread', content = '', meta = '', done_at = ?
		WHERE kind = 'client' AND bot = ? AND status IN ('pending', 'claimed') AND json_extract(meta, '$.thread_id') = ?`, stamp, bot, t.ID)
	for _, p := range listProposals(`thread_id = ? AND by_bot = ? AND state = 'pending'`, t.ID, bot) {
		resolveProposal(p, "withdrawn", now)
	}
	_, _ = addSystemMessage(t, clientSystemView{Type: "bot_removed", Bot: bot, By: reason}, bot+" left the thread.", now)
	threadUpdated(t.ID, []string{"bots"}, now)
	clientReconcile(t.ID, now)
}

// clientRevokeThreads runs the grant-revoked steps for a person and bot: the
// bot leaves every non-main thread of theirs and proposals naming it are
// withdrawn.
func clientRevokeThreads(userID, bot string, now time.Time) {
	rows, err := db.Query(`SELECT `+prefixCols("t.", appThreadCols)+` FROM app_threads t JOIN app_thread_bots b ON b.thread_id = t.id
		WHERE t.user_id = ? AND t.kind = 'thread' AND b.bot = ? AND b.removed_at = ''`, userID, bot)
	if err == nil {
		var ts []*appThread
		for rows.Next() {
			if t, err := scanAppThread(rows); err == nil {
				ts = append(ts, t)
			}
		}
		rows.Close()
		for _, t := range ts {
			removeThreadBot(t, bot, "access", now)
		}
	}
	for _, p := range listProposals(`user_id = ? AND (bot = ? OR by_bot = ?) AND state = 'pending'`, userID, bot, bot) {
		resolveProposal(p, "withdrawn", now)
	}
}

// clientForgetBotThreads runs the bot-deleted steps in non-main threads: the
// bot is removed, its messages stay, and its files are removed unless another
// bot holds a copy, which becomes the primary.
func clientForgetBotThreads(bot string, now time.Time) {
	rows, err := db.Query(`SELECT `+prefixCols("t.", appThreadCols)+` FROM app_threads t JOIN app_thread_bots b ON b.thread_id = t.id
		WHERE t.kind = 'thread' AND b.bot = ? AND b.removed_at = ''`, bot)
	if err == nil {
		var ts []*appThread
		for rows.Next() {
			if t, err := scanAppThread(rows); err == nil {
				ts = append(ts, t)
			}
		}
		rows.Close()
		for _, t := range ts {
			removeThreadBot(t, bot, "bot_deleted", now)
		}
	}
	for _, p := range listProposals(`(bot = ? OR by_bot = ?) AND state = 'pending'`, bot, bot) {
		resolveProposal(p, "withdrawn", now)
	}
	// Files in non-main threads whose primary copy is in the bot's folder.
	arows, err := db.Query(`SELECT `+prefixCols("a.", appAttachmentCols)+` FROM app_attachments a JOIN app_threads t ON t.id = a.conv_id
		WHERE t.kind = 'thread' AND a.bot = ?`, bot)
	if err != nil {
		return
	}
	var atts []appAttachment
	for arows.Next() {
		if a, err := scanAppAttachment(arows); err == nil {
			atts = append(atts, a)
		}
	}
	arows.Close()
	for _, a := range atts {
		var other, path string
		if db.QueryRow(`SELECT bot, path FROM app_attachment_copies WHERE attachment_id = ? AND bot != ? ORDER BY created_at LIMIT 1`, a.ID, bot).Scan(&other, &path) == nil {
			_, _ = db.Exec(`UPDATE app_attachments SET bot = ?, path = ? WHERE id = ?`, other, path, a.ID)
			_, _ = db.Exec(`DELETE FROM app_attachment_copies WHERE attachment_id = ? AND bot = ?`, a.ID, other)
			continue
		}
		if a.ExpiredAt == "" {
			removeAttachment(a, "bot", now)
		}
	}
	_, _ = db.Exec(`DELETE FROM app_attachment_copies WHERE bot = ?`, bot)
}

// --- read markers -------------------------------------------------------------------

func serveClientThreadRead(w http.ResponseWriter, r *http.Request, id *clientIdentity, t *appThread) {
	var in struct {
		Cursor string `json:"cursor"`
	}
	if !clientDecode(w, r, 4<<10, &in) {
		return
	}
	cur, ok := parseCursor(in.Cursor)
	if !ok || cur <= 0 {
		clientError(w, http.StatusBadRequest, "invalid_request", "cursor must be a message's cursor.")
		return
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq = ?`, t.ID, cur).Scan(&n)
	if n != 1 {
		clientError(w, http.StatusBadRequest, "invalid_request", "cursor is not a message of this thread.")
		return
	}
	now := time.Now()
	_, before := clientUnreadBadge(t.UserID)
	_, _ = db.Exec(`UPDATE app_threads SET last_read_seq = ? WHERE id = ? AND last_read_seq < ?`, cur, t.ID, cur)
	t, _ = threadByID(t.ID)
	unread, badge := clientUnreadBadge(t.UserID)
	out := map[string]any{"thread_id": t.ID, "read_cursor": strconv.FormatInt(t.LastReadSeq, 10), "unread": threadUnread(t), "unread_total": unread, "badge": badge}
	appendClientEvent(t.ID, "thread_read", out, now)
	clientStubChanged(t.ID, now)
	clientBadgeMaybeDown(t.UserID, before, now)
	clientJSON(w, http.StatusOK, out)
}

// --- messages around a cursor --------------------------------------------------------

func serveClientMessagesAround(w http.ResponseWriter, t *appThread, around int64, limit int) {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq = ?`, t.ID, around).Scan(&n)
	if n != 1 {
		clientError(w, http.StatusBadRequest, "invalid_request", "around is not a message of this thread.")
		return
	}
	cursor := newestEventSeq(t.ID)
	clientReconcile(t.ID, time.Now())
	older := (limit - 1) / 2
	before, err := pageMessages(t.ID, around, 0, older)
	if err != nil {
		clientFail(w, err)
		return
	}
	after, err := pageMessages(t.ID, 0, around-1, limit-len(before))
	if err != nil {
		clientFail(w, err)
		return
	}
	msgs := append(before, after...)
	var olderCursor, newerCursor any
	if len(msgs) > 0 {
		first, _ := strconv.ParseInt(msgs[0].Cursor, 10, 64)
		last, _ := strconv.ParseInt(msgs[len(msgs)-1].Cursor, 10, 64)
		var c int
		_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq < ?`, t.ID, first).Scan(&c)
		if c > 0 {
			olderCursor = msgs[0].Cursor
		}
		_ = db.QueryRow(`SELECT COUNT(*) FROM app_messages WHERE conv_id = ? AND seq > ?`, t.ID, last).Scan(&c)
		if c > 0 {
			newerCursor = msgs[len(msgs)-1].Cursor
		}
	}
	clientJSON(w, http.StatusOK, map[string]any{"conversation_id": t.ID, "thread_id": t.ID, "messages": msgs,
		"events_cursor": strconv.FormatInt(cursor, 10), "older_cursor": olderCursor, "newer_cursor": newerCursor})
}

// --- proposals ------------------------------------------------------------------------

type clientProposalView struct {
	ID        string `json:"id"`
	ThreadID  string `json:"thread_id"`
	By        string `json:"by"`
	Bot       string `json:"bot"`
	Reason    string `json:"reason"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	DecidedAt string `json:"decided_at,omitempty"`
	userID    string
}

const proposalCols = `id, thread_id, user_id, by_bot, bot, reason, state, created_at, expires_at, decided_at`

func listProposals(where string, args ...any) []clientProposalView {
	rows, err := db.Query(`SELECT `+proposalCols+` FROM app_proposals WHERE `+where+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []clientProposalView{}
	for rows.Next() {
		var p clientProposalView
		if rows.Scan(&p.ID, &p.ThreadID, &p.userID, &p.By, &p.Bot, &p.Reason, &p.State, &p.CreatedAt, &p.ExpiresAt, &p.DecidedAt) == nil {
			out = append(out, p)
		}
	}
	return out
}

func pendingProposalsOf(threadID string) []clientProposalView {
	ps := listProposals(`thread_id = ? AND state = 'pending'`, threadID)
	if ps == nil {
		ps = []clientProposalView{}
	}
	return ps
}

func proposalByID(id string) (clientProposalView, bool) {
	ps := listProposals(`id = ?`, id)
	if len(ps) != 1 {
		return clientProposalView{}, false
	}
	return ps[0], true
}

// resolveProposal closes a pending proposal (accepted, declined, expired or
// withdrawn): the person's streams hear proposal_resolved, the thread's view
// changes, and the proposing bot is told by an event of its own.
func resolveProposal(p clientProposalView, state string, now time.Time) (clientProposalView, bool) {
	res, err := db.Exec(`UPDATE app_proposals SET state = ?, decided_at = ? WHERE id = ? AND state = 'pending'`, state, nowStamp(now), p.ID)
	if err != nil {
		return p, false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return p, false
	}
	p, _ = proposalByID(p.ID)
	_, before := clientUnreadBadge(p.userID)
	appendClientEvent(p.ThreadID, "proposal_resolved", map[string]any{"proposal": p}, now)
	threadUpdated(p.ThreadID, []string{"proposals"}, now)
	clientTellProposer(p, now)
	clientBadgeMaybeDown(p.userID, before, now)
	return p, true
}

func serveClientProposals(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	q := `p.user_id = ? AND EXISTS (SELECT 1 FROM app_threads t WHERE t.id = p.thread_id AND ` + visibleThreadSQL + `)`
	switch r.URL.Query().Get("state") {
	case "", "pending":
		q += ` AND p.state = 'pending'`
	case "all":
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be pending or all.")
		return
	}
	rows, err := db.Query(`SELECT `+prefixCols("p.", proposalCols)+` FROM app_proposals p WHERE `+q+` ORDER BY p.created_at DESC, p.id LIMIT 100`, id.User.ID, id.User.ID)
	if err != nil {
		clientFail(w, err)
		return
	}
	defer rows.Close()
	out := []clientProposalView{}
	for rows.Next() {
		var p clientProposalView
		if rows.Scan(&p.ID, &p.ThreadID, &p.userID, &p.By, &p.Bot, &p.Reason, &p.State, &p.CreatedAt, &p.ExpiresAt, &p.DecidedAt) == nil {
			out = append(out, p)
		}
	}
	clientJSON(w, http.StatusOK, map[string]any{"proposals": out})
}

func serveClientProposalDecide(w http.ResponseWriter, id *clientIdentity, pid string, accept bool) {
	p, ok := proposalByID(pid)
	if !ok || p.userID != id.User.ID {
		clientNotFound(w)
		return
	}
	t, ok := clientOwnThread(id, p.ThreadID)
	if !ok {
		clientNotFound(w)
		return
	}
	now := time.Now()
	want := "declined"
	if accept {
		want = "accepted"
	}
	closed := func() {
		clientError(w, http.StatusConflict, "proposal_closed", "This suggestion is no longer open.")
	}
	if p.State != "pending" {
		if p.State != want {
			closed()
			return
		}
		out := map[string]any{"proposal": p}
		if accept {
			out["thread"] = threadViewOf(t)
		}
		clientJSON(w, http.StatusOK, out)
		return
	}
	if !accept {
		np, ok := resolveProposal(p, "declined", now)
		if !ok {
			closed()
			return
		}
		_, _ = addSystemMessage(t, clientSystemView{Type: "proposal_declined", Bot: p.Bot, By: "user"}, "You chose not to add "+p.Bot+".", now)
		clientJSON(w, http.StatusOK, map[string]any{"proposal": np})
		return
	}
	_, _, granted := grantedBotOf(id.User.ID, p.Bot)
	full := len(activeBots(t.ID)) >= clientParticipantsMax && !isActiveParticipant(t.ID, p.Bot)
	if !granted || full {
		resolveProposal(p, "withdrawn", now)
		closed()
		return
	}
	np, ok := resolveProposal(p, "accepted", now)
	if !ok {
		closed()
		return
	}
	if err := addThreadBot(t, p.Bot, "proposal:"+p.ID, "proposal", now); err != nil {
		clientFail(w, err)
		return
	}
	t, _ = threadByID(t.ID)
	clientJSON(w, http.StatusOK, map[string]any{"proposal": np, "thread": threadViewOf(t)})
}

// clientTellProposer tells the bot that proposed it how a proposal ended: a
// client event of its own, needing no reply and carrying no reply token.
func clientTellProposer(p clientProposalView, now time.Time) {
	u, err := getAppUser(p.userID)
	if err != nil {
		return
	}
	g, err := getAppGrant(p.userID, p.By)
	if err != nil {
		return
	}
	verb := map[string]string{"accepted": "accepted adding", "declined": "declined adding", "expired": "did not answer adding", "withdrawn": "can no longer add"}[p.State]
	what := "The person " + verb + " " + p.Bot + " to this thread."
	if p.State == "expired" {
		what = "The proposal to add " + p.Bot + " to this thread expired."
	}
	meta := map[string]string{
		"event": "proposal_resolved", "user_id": u.ID, "user_name": clientMetaClean(u.Name, clientNameMax),
		"verified": "true", "verified_via": "paired", "is_owner": fmt.Sprint(g.Owner),
		"conversation_id": p.ThreadID, "thread_id": p.ThreadID, "proposal_id": p.ID, "bot": p.Bot, "state": p.State,
		"mode": "text", "nonce": randHex(8),
	}
	_, _ = enqueueBotClientEvent(p.By, what+" No reply is needed.", meta, now)
}

// clientExpireProposals is the maintenance pass's: pending proposals past
// their time become expired.
func clientExpireProposals(now time.Time) {
	for _, p := range listProposals(`state = 'pending' AND expires_at < ?`, nowStamp(now)) {
		resolveProposal(p, "expired", now)
	}
}

// --- the person stream ----------------------------------------------------------------

const clientForegroundWindow = 25 * time.Second

var (
	clientPersonStreamsMu sync.Mutex
	clientPersonStreams   = map[string]int{} // device -> open person streams
)

// clientStreamSeen writes the device's heartbeat (and presence at open).
func clientStreamSeen(deviceID, presence string, now time.Time) {
	if presence != "" {
		_, _ = db.Exec(`UPDATE app_devices SET stream_seen_at = ?, presence = ?, presence_at = ? WHERE id = ?`, nowStamp(now), presence, nowStamp(now), deviceID)
		return
	}
	_, _ = db.Exec(`UPDATE app_devices SET stream_seen_at = ? WHERE id = ?`, nowStamp(now), deviceID)
}

func serveClientPersonEvents(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	fl, ok := w.(http.Flusher)
	if !ok {
		clientError(w, http.StatusInternalServerError, "internal", "Streaming is not supported here.")
		return
	}
	presence := r.URL.Query().Get("presence")
	switch presence {
	case "":
		presence = "foreground"
	case "foreground", "background":
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "presence must be foreground or background.")
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
	clientHeaders(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &clientSSE{w: w, fl: fl}

	now := time.Now()
	clientPersonStreamsMu.Lock()
	clientPersonStreams[id.Device.ID]++
	clientPersonStreamsMu.Unlock()
	clientStreamSeen(id.Device.ID, presence, now)
	defer func() {
		clientPersonStreamsMu.Lock()
		clientPersonStreams[id.Device.ID]--
		left := clientPersonStreams[id.Device.ID]
		if left <= 0 {
			delete(clientPersonStreams, id.Device.ID)
		}
		clientPersonStreamsMu.Unlock()
		if left <= 0 {
			_, _ = db.Exec(`UPDATE app_devices SET stream_seen_at = '' WHERE id = ?`, id.Device.ID)
		}
	}()

	resync := false
	ensureMainThreads(id.User.ID, now)
	var snap *clientSnapshot
	if !haveLast {
		// A fresh start: the position and the counts in hello are one
		// snapshot, as in GET /threads.
		snap = takeClientSnapshot(id.User.ID)
		last = snap.Events
	} else {
		var oldest sql.NullInt64
		_ = db.QueryRow(`SELECT MIN(seq) FROM app_events WHERE user_id = ?`, id.User.ID).Scan(&oldest)
		if oldest.Int64 > 0 && last < oldest.Int64-1 {
			resync = true
		}
	}
	unread, badge := clientUnreadBadgeSnap(id.User.ID, snap)
	bots := clientPersonBotStates(id, now)
	calls := []*appCall{}
	if rows, err := db.Query(`SELECT `+prefixCols("c.", appCallCols)+` FROM app_calls c JOIN app_threads t ON t.id = c.conv_id
		WHERE c.state = 'open' AND `+visibleThreadSQL+` ORDER BY c.started_at`, id.User.ID); err == nil {
		for rows.Next() {
			if c, err := scanAppCall(rows); err == nil {
				calls = append(calls, c)
			}
		}
		rows.Close()
	}
	botList := []map[string]any{}
	for _, b := range bots {
		botList = append(botList, b)
	}
	hello := map[string]any{"user_id": id.User.ID, "events_cursor": strconv.FormatInt(last, 10), "unread_total": unread, "badge": badge,
		"bots": botList, "calls": calls}
	if s.event(0, "hello", hello) != nil {
		return
	}
	if resync {
		if s.event(0, "resync", map[string]any{"reason": "events_pruned"}) != nil {
			return
		}
		last = newestUserEventSeq(id.User.ID)
	}
	deadline := now.Add(clientStreamMax)
	nextKeep := now.Add(clientKeepalive)
	nextState := now.Add(clientStatePoll)
	nextBeat := now.Add(clientKeepalive)
	nextLook := now.Add(botTalkLookLocal)
	lastStates := map[string]string{}
	for _, b := range bots {
		lastStates[b["bot"].(string)] = b["state"].(string)
	}
	seenSteps := map[string]bool{}
	lastLabel := map[string]string{}
	for {
		wake := clientWake.waiter(clientPersonKey(id.User.ID))
		now = time.Now()
		var n int
		if db.QueryRow(`SELECT COUNT(*) FROM app_devices d JOIN app_users u ON u.id = d.user_id
			WHERE d.id = ? AND d.user_id = ? AND d.revoked_at = '' AND u.disabled = 0`, id.Device.ID, id.User.ID).Scan(&n) != nil || n != 1 {
			_ = s.event(0, "closed", map[string]any{"reason": "access_revoked"})
			return
		}
		if !now.Before(deadline) {
			_ = s.event(0, "closed", map[string]any{"reason": "reconnect"})
			return
		}
		clientReconcileUser(id.User.ID, now)
		evs, err := personEventsAfter(id.User.ID, last, 100)
		if err != nil {
			return
		}
		granted := grantedSet(id.User.ID)
		kinds := map[string]*appThread{}
		for _, e := range evs {
			last = e.Seq
			t, known := kinds[e.Thread]
			if !known {
				t, _ = threadByID(e.Thread)
				kinds[e.Thread] = t
			}
			// A main thread's events reach the person only while the grant
			// holds; a deleted thread's only as its own deletion.
			if t == nil && e.Type != "thread_deleted" {
				continue
			}
			if t != nil && t.isMain() && !granted[t.MainBot] {
				continue
			}
			if s.raw(e.Seq, e.Type, e.Data) != nil {
				return
			}
		}
		if !now.Before(nextState) {
			nextState = now.Add(clientStatePoll)
			for _, b := range clientPersonBotStates(id, now) {
				name, st := b["bot"].(string), b["state"].(string)
				if lastStates[name] != st {
					lastStates[name] = st
					if s.event(0, "bot_state", b) != nil {
						return
					}
				}
			}
		}
		if !now.Before(nextLook) {
			nextLook = now.Add(botTalkLookLocal)
			for _, a := range clientPersonActivity(id.User.ID, seenSteps, lastLabel) {
				if s.event(0, "activity", a) != nil {
					return
				}
			}
		}
		if !now.Before(nextBeat) {
			nextBeat = now.Add(clientKeepalive)
			clientStreamSeen(id.Device.ID, "", now)
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

func grantedSet(userID string) map[string]bool {
	out := map[string]bool{}
	gs, _ := listAppGrants(userID)
	for _, g := range gs {
		out[g.Bot] = true
	}
	return out
}

func clientPersonBotStates(id *clientIdentity, now time.Time) []map[string]any {
	out := []map[string]any{}
	gs, _ := listAppGrants(id.User.ID)
	for _, g := range gs {
		rec, err := getBot(g.Bot)
		if err != nil {
			continue
		}
		st, _, why := botClientState(rec, now)
		// "bot" and "name" both: name is what /me's bot objects use.
		m := map[string]any{"bot": g.Bot, "name": g.Bot, "state": st}
		if why != "" && st == "unavailable" {
			m["reason"] = why
		}
		out = append(out, m)
	}
	return out
}

type personEventRow struct {
	Seq          int64
	Thread, Type string
	Data         string
}

func personEventsAfter(userID string, after int64, limit int) ([]personEventRow, error) {
	rows, err := db.Query(`SELECT seq, conv_id, type, data FROM app_events WHERE user_id = ? AND seq > ? ORDER BY seq LIMIT ?`, userID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []personEventRow
	for rows.Next() {
		var e personEventRow
		if err := rows.Scan(&e.Seq, &e.Thread, &e.Type, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// clientReconcileUser reconciles the deliveries of every thread of a person.
func clientReconcileUser(userID string, now time.Time) {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_message_deliveries d JOIN app_threads t ON t.id = d.thread_id WHERE t.user_id = ? AND d.state = 'queued'`, userID).Scan(&n)
	if n == 0 {
		return
	}
	rows, err := db.Query(`SELECT DISTINCT d.thread_id FROM app_message_deliveries d JOIN app_threads t ON t.id = d.thread_id WHERE t.user_id = ? AND d.state = 'queued'`, userID)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	rows.Close()
	for _, t := range ids {
		clientReconcile(t, now)
	}
}

// clientPersonActivity is what claude bots are doing on the person's
// outstanding messages: plain step kinds only.
func clientPersonActivity(userID string, seen map[string]bool, lastLabel map[string]string) []map[string]any {
	var out []map[string]any
	rows, err := db.Query(`SELECT d.thread_id, d.bot, d.message_id FROM app_message_deliveries d JOIN app_threads t ON t.id = d.thread_id
		JOIN app_messages m ON m.id = d.message_id
		WHERE t.user_id = ? AND d.state = 'delivered' AND m.created_at > ?
		AND NOT EXISTS (SELECT 1 FROM app_messages b WHERE b.conv_id = d.thread_id AND b.role = 'bot' AND b.bot = d.bot AND b.done = 1 AND b.seq > m.seq)
		ORDER BY m.seq DESC LIMIT 5`, userID, nowStamp(time.Now().Add(-time.Hour)))
	if err != nil {
		return nil
	}
	type o struct{ thread, bot, msg string }
	var os []o
	for rows.Next() {
		var x o
		if rows.Scan(&x.thread, &x.bot, &x.msg) == nil {
			os = append(os, x)
		}
	}
	rows.Close()
	done := map[string]bool{}
	for _, x := range os {
		if done[x.bot] {
			continue
		}
		done[x.bot] = true
		rec, err := getBot(x.bot)
		if err != nil || rec.isOmp() {
			continue
		}
		b, err := botBackend(rec.Host)
		if err != nil {
			continue
		}
		lk := botTalkLookFn(b, rec, true)
		steps, _, _ := botTalkStepsMarker(lk.items, `message_id="`+x.msg+`"`, false)
		for _, st := range steps {
			key := x.bot + "\x00" + st.key
			if seen[key] {
				continue
			}
			seen[key] = true
			if st.text == lastLabel[x.bot] {
				continue
			}
			lastLabel[x.bot] = st.text
			out = append(out, map[string]any{"thread_id": x.thread, "bot": x.bot, "message_id": x.msg, "text": st.text})
		}
	}
	return out
}

// readJSONBody reads an optional small JSON body.
func readJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		clientError(w, http.StatusRequestEntityTooLarge, "too_large", "The request body is too large.")
		return false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if json.Unmarshal(body, v) != nil {
		clientError(w, http.StatusBadRequest, "invalid_request", "The body must be JSON.")
		return false
	}
	return true
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The chat's "Up next" queue: messages typed while an agent is working, held by
// lasso and typed into the pane once it is idle, one at a time, oldest first.
//
// It used to be React state in the browser tab that typed it, so it went out
// only while that tab stayed open — and a phone suspends a tab the moment it is
// locked or switched away from, which left messages queued on a phone that
// never went anywhere. Here the queue is lasso's: rows in lasso.db (it survives
// a restart), shown identically on every device (chat_queue_rev over SSE, and
// the `queue` field of every /api/chat poll), and delivered by a loop that runs
// whether or not anything is watching.
//
// A queue is keyed by HOST + PANE ID, the address the chat's own composer
// sends to: the payload's host (the machine the pane lives on, after any hop
// chatScreenPane followed) and that host's pane id. A pane id is never carried
// to another host. Two guards stand in for a stable agent identity, since pane
// ids are unique only while the pane lives: a queue whose pane is gone from a
// non-empty listing chatQueueMissLimit times running is dropped, and a queue is
// bound to the conversation it was typed against (the transcript `path` on
// screen), so a pane that is now in a different conversation — a /clear, or an
// id reused by a new pane — pauses rather than receiving someone else's
// follow-up.
//
// Delivery is chatSubmit, exactly a manual send, and keeps its three outcomes:
//
//   - confirmed: the row becomes "sent" and stays, as the conversation's echo,
//     until the transcript records a new user turn (the client's own echo rule,
//     newestUserID); only then may the next one go. A confirmed send reads as
//     running only once the transcript has the turn, so an agent that merely
//     looks idle in that gap must not be handed the next message.
//   - refused: nothing was typed (the pane holds a human's draft). The message
//     stays queued and the queue pauses with the reason.
//   - uncertain: it may have landed. The row is marked failed, the queue pauses,
//     and nothing is resent by itself — "Send queued messages" is a human's call.
//
// Stop (/api/chat/stop) pauses a non-empty queue too: the next message after an
// interrupt is a decision, not something that fires the moment the agent goes
// quiet. A lasso that dies mid-send leaves a "sending" row; whoever next takes
// the runner lock marks it failed and pauses, since the paste may have landed.
//
// Only one lasso per lasso.db delivers (chatqueue.runner.lock, the bots' and
// plugins' flock pattern): titan's dev and production lassos share ~/.lasso,
// and two runners would type every message twice. Either lasso serves the API.

const chatQueueSchema = `
CREATE TABLE IF NOT EXISTS chat_queues (
  host    TEXT NOT NULL,
  pane_id TEXT NOT NULL,
  paused  INTEGER NOT NULL DEFAULT 0,
  -- note/tone: why the queue is paused, when something other than a human
  -- paused it ("bad" = nothing was typed, "warn" = something may have been).
  note    TEXT NOT NULL DEFAULT '',
  tone    TEXT NOT NULL DEFAULT '',
  -- session: the transcript path the queue is bound to ('' = unbound).
  session TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (host, pane_id)
);
CREATE TABLE IF NOT EXISTS chat_queue (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  host       TEXT NOT NULL,
  pane_id    TEXT NOT NULL,
  position   INTEGER NOT NULL,
  text       TEXT NOT NULL,
  created_at INTEGER NOT NULL,          -- unix ms
  created_by TEXT NOT NULL DEFAULT '',  -- the client (tab) that queued it
  state      TEXT NOT NULL DEFAULT 'queued', -- queued | sending | sent | failed
  sent_at    INTEGER NOT NULL DEFAULT 0,
  after_user TEXT NOT NULL DEFAULT '',  -- the transcript's newest user turn when it was sent
  detail     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS chat_queue_target ON chat_queue(host, pane_id, position);
`

// Item states.
const (
	cqQueued  = "queued"
	cqSending = "sending"
	cqSent    = "sent"
	cqFailed  = "failed"
)

const (
	// chatQueueMaxItems bounds one pane's queue; chatQueueMaxText one message.
	chatQueueMaxItems = 50
	chatQueueMaxText  = 64 << 10
	// chatQueueMissLimit is how many non-empty listings running must lack the
	// pane before its queue is dropped: one listing can miss a pane mid-reload.
	chatQueueMissLimit = 3
)

var (
	// chatQueueEvery is the delivery loop's tick.
	chatQueueEvery = 1500 * time.Millisecond
	// chatQueueIdleFor is how long the agent must have read idle on the loop's
	// own passes before a message is typed (see process).
	chatQueueIdleFor = 2 * time.Second
	// chatQueueEchoWait is how long a sent message waits for its user turn to
	// appear in the transcript before the queue moves on anyway, provided the
	// agent is idle: a slash command, say, records no turn at all.
	chatQueueEchoWait = 90 * time.Second
	// chatQueueBackend resolves a queue's host. A seam for tests.
	chatQueueBackend = func(host string) (Backend, error) { return namedHostBackend(host) }
	// chatQueueNow is the clock. A seam for tests.
	chatQueueNow = time.Now
)

// chatQueueItem is one queued message as every client sees it.
type chatQueueItem struct {
	ID        int64  `json:"id"`
	Text      string `json:"text"`
	State     string `json:"state"`
	CreatedAt int64  `json:"created_at"`
	CreatedBy string `json:"created_by,omitempty"`
	SentAt    int64  `json:"sent_at,omitempty"`
	// After is the transcript's newest user turn when a sent message went out.
	// A client hides the echo once its own newest user turn differs, rather
	// than showing the message twice until the loop's next tick retires it.
	After  string `json:"after,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// chatQueueView is one pane's queue: the in-flight message (sending or sent)
// first, then what waits, in order.
type chatQueueView struct {
	Paused bool            `json:"paused"`
	Note   string          `json:"note,omitempty"`
	Tone   string          `json:"tone,omitempty"`
	Items  []chatQueueItem `json:"items"`
}

type chatQueueRow struct {
	paused  bool
	note    string
	tone    string
	session string
}

func chatQueueChanged() {
	if srvHub != nil {
		srvHub.bumpChatQueueRev()
	}
}

func loadChatQueueRow(host, pane string) (chatQueueRow, bool, error) {
	var q chatQueueRow
	var paused int
	err := db.QueryRow(`SELECT paused, note, tone, session FROM chat_queues WHERE host=? AND pane_id=?`, host, pane).
		Scan(&paused, &q.note, &q.tone, &q.session)
	if errors.Is(err, sql.ErrNoRows) {
		return q, false, nil
	}
	q.paused = paused != 0
	return q, err == nil, err
}

// loadChatQueueItems lists a pane's messages: in flight first, then by position.
func loadChatQueueItems(host, pane string) ([]chatQueueItem, error) {
	rows, err := db.Query(`SELECT id, text, state, created_at, created_by, sent_at, after_user, detail
		FROM chat_queue WHERE host=? AND pane_id=?
		ORDER BY CASE state WHEN 'sent' THEN 0 WHEN 'sending' THEN 1 ELSE 2 END, sent_at, position, id`, host, pane)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []chatQueueItem{}
	for rows.Next() {
		var it chatQueueItem
		if err := rows.Scan(&it.ID, &it.Text, &it.State, &it.CreatedAt, &it.CreatedBy, &it.SentAt, &it.After, &it.Detail); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// chatQueueFor is a pane's queue, or nil when it has none.
func chatQueueFor(host, pane string) *chatQueueView {
	if db == nil || pane == "" {
		return nil
	}
	items, err := loadChatQueueItems(host, pane)
	if err != nil || len(items) == 0 {
		return nil
	}
	q, _, _ := loadChatQueueRow(host, pane)
	return &chatQueueView{Paused: q.paused, Note: q.note, Tone: q.tone, Items: items}
}

// tidyChatQueue removes the queue row of a pane with no messages left, so its
// pause (and binding) does not outlive what it was holding: a message queued
// into an empty queue starts fresh.
func tidyChatQueue(host, pane string) {
	_, _ = db.Exec(`DELETE FROM chat_queues WHERE host=? AND pane_id=?
		AND NOT EXISTS (SELECT 1 FROM chat_queue WHERE host=? AND pane_id=?)`, host, pane, host, pane)
}

// enqueueChat appends a message to a pane's queue. session is the transcript the
// client had on screen ("" when it had none), bound only when the queue is new.
func enqueueChat(host, pane, text, session, by string) (int64, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	var maxPos sql.NullInt64
	if err := tx.QueryRow(`SELECT COUNT(*), MAX(position) FROM chat_queue WHERE host=? AND pane_id=?`, host, pane).Scan(&n, &maxPos); err != nil {
		return 0, err
	}
	if n >= chatQueueMaxItems {
		return 0, errChatQueueFull
	}
	if n == 0 {
		// A fresh queue: whatever paused the last one (a Stop, a refusal) was
		// about the messages it held, all of which are gone.
		if _, err := tx.Exec(`INSERT INTO chat_queues(host, pane_id, paused, note, tone, session) VALUES(?,?,0,'','',?)
			ON CONFLICT(host, pane_id) DO UPDATE SET paused=0, note='', tone='', session=excluded.session`, host, pane, session); err != nil {
			return 0, err
		}
	} else if _, err := tx.Exec(`INSERT OR IGNORE INTO chat_queues(host, pane_id, session) VALUES(?,?,?)`, host, pane, session); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`INSERT INTO chat_queue(host, pane_id, position, text, created_at, created_by) VALUES(?,?,?,?,?,?)`,
		host, pane, maxPos.Int64+1, text, chatQueueNow().UnixMilli(), by)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

var (
	errChatQueueFull     = fmt.Errorf("the queue holds at most %d messages", chatQueueMaxItems)
	errChatQueueNoItem   = errors.New("no such queued message")
	errChatQueueInFlight = errors.New("that message is being sent")
)

// editChatQueued replaces a waiting message's text.
func editChatQueued(host, pane string, id int64, text string) error {
	res, err := db.Exec(`UPDATE chat_queue SET text=?, detail='' WHERE id=? AND host=? AND pane_id=? AND state IN ('queued','failed')`, text, id, host, pane)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return chatQueueMissing(host, pane, id)
	}
	return nil
}

// deleteChatQueued removes a message. One being typed right now cannot be;
// a sent one can (it only removes the echo).
func deleteChatQueued(host, pane string, id int64) error {
	res, err := db.Exec(`DELETE FROM chat_queue WHERE id=? AND host=? AND pane_id=? AND state<>'sending'`, id, host, pane)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return chatQueueMissing(host, pane, id)
	}
	tidyChatQueue(host, pane)
	return nil
}

func chatQueueMissing(host, pane string, id int64) error {
	var state string
	if db.QueryRow(`SELECT state FROM chat_queue WHERE id=? AND host=? AND pane_id=?`, id, host, pane).Scan(&state) == nil {
		return errChatQueueInFlight
	}
	return errChatQueueNoItem
}

// reorderChatQueue puts the named waiting messages first, in the given order;
// the rest keep theirs after them.
func reorderChatQueue(host, pane string, order []int64) error {
	items, err := loadChatQueueItems(host, pane)
	if err != nil {
		return err
	}
	waiting := map[int64]bool{}
	for _, it := range items {
		if it.State == cqQueued || it.State == cqFailed {
			waiting[it.ID] = true
		}
	}
	seen := map[int64]bool{}
	var seq []int64
	for _, id := range order {
		if !waiting[id] {
			return errChatQueueNoItem
		}
		if !seen[id] {
			seen[id] = true
			seq = append(seq, id)
		}
	}
	for _, it := range items {
		if waiting[it.ID] && !seen[it.ID] {
			seq = append(seq, it.ID)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range seq {
		if _, err := tx.Exec(`UPDATE chat_queue SET position=? WHERE id=?`, i+1, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pauseChatQueue holds a queue, with the reason when lasso is the one pausing.
func pauseChatQueue(host, pane, note, tone string) {
	_, _ = db.Exec(`UPDATE chat_queues SET paused=1, note=?, tone=? WHERE host=? AND pane_id=?`, note, tone, host, pane)
}

// resumeChatQueue lets a paused queue go again. A failed message goes back to
// waiting (the human has seen why it failed and chose to send), and the
// binding is cleared: resuming after "this pane is in a different
// conversation" is the human saying the new one is right, and the next
// confirmed send binds it.
func resumeChatQueue(host, pane string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE chat_queues SET paused=0, note='', tone='', session='' WHERE host=? AND pane_id=?`, host, pane); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE chat_queue SET state='queued', detail='' WHERE host=? AND pane_id=? AND state='failed'`, host, pane); err != nil {
		return err
	}
	return tx.Commit()
}

// pauseChatQueueForStop is Stop's half of the queue: a non-empty queue holds.
func pauseChatQueueForStop(host, pane string) {
	if db == nil {
		return
	}
	var n int
	if db.QueryRow(`SELECT COUNT(*) FROM chat_queue WHERE host=? AND pane_id=? AND state IN ('queued','failed')`, host, pane).Scan(&n) != nil || n == 0 {
		return
	}
	pauseChatQueue(host, pane, "", "")
	chatQueueChanged()
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

// serveChatQueue is /api/chat/queue (GET list, POST add, PATCH pause/resume/
// reorder) and /api/chat/queue/<id> (PUT edit, DELETE remove). Every call names
// its pane (`pane_id` in a body, `?pane=` otherwise) and is addressed to a host
// the way /api/chat/send is (`?host=`, else the tab's own), behind the same
// auth. Each answers with the pane's queue as it now stands.
func serveChatQueue(w http.ResponseWriter, r *http.Request) {
	be, err := reqHostBackend(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	host := be.Name()
	idPart := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/chat/queue"), "/")
	var body struct {
		PaneID  string  `json:"pane_id"`
		Text    string  `json:"text"`
		Session string  `json:"session"`
		Client  string  `json:"client"`
		Paused  *bool   `json:"paused"`
		Order   []int64 `json:"order"`
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, chatQueueMaxText+16<<10)).Decode(&body); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
	}
	pane := strings.TrimSpace(body.PaneID)
	if pane == "" {
		pane = strings.TrimSpace(r.URL.Query().Get("pane"))
	}
	if pane == "" {
		http.Error(w, "pane_id is required", http.StatusBadRequest)
		return
	}
	checkText := func() bool {
		switch {
		case strings.TrimSpace(body.Text) == "":
			http.Error(w, "text is required", http.StatusBadRequest)
		case len(body.Text) > chatQueueMaxText:
			http.Error(w, fmt.Sprintf("text is longer than %d bytes", chatQueueMaxText), http.StatusRequestEntityTooLarge)
		default:
			return true
		}
		return false
	}
	fail := func(err error) {
		switch {
		case errors.Is(err, errChatQueueNoItem):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, errChatQueueInFlight), errors.Is(err, errChatQueueFull):
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	if idPart != "" {
		id, err := strconv.ParseInt(idPart, 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodPut:
			if !checkText() {
				return
			}
			err = editChatQueued(host, pane, id, body.Text)
		case http.MethodDelete:
			err = deleteChatQueued(host, pane, id)
		default:
			http.Error(w, "PUT or DELETE", http.StatusMethodNotAllowed)
			return
		}
		if err != nil {
			fail(err)
			return
		}
		chatQueueChanged()
		writeChatQueue(w, host, pane)
		return
	}

	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !checkText() {
			return
		}
		// The pane must be one this host has, as for a send: a queue nobody
		// can deliver is a message silently lost.
		if _, err := chatPaneKind(be, pane); errors.Is(err, errChatNoPane) {
			http.Error(w, "pane not found", http.StatusNotFound)
			return
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		by := body.Client
		if len(by) > 100 {
			by = by[:100]
		}
		if _, err := enqueueChat(host, pane, body.Text, body.Session, by); err != nil {
			fail(err)
			return
		}
		chatQueueChanged()
		chatQueues.poke()
	case http.MethodPatch:
		if body.Order != nil {
			if err := reorderChatQueue(host, pane, body.Order); err != nil {
				fail(err)
				return
			}
		}
		if body.Paused != nil {
			if *body.Paused {
				pauseChatQueue(host, pane, "", "")
			} else if err := resumeChatQueue(host, pane); err != nil {
				fail(err)
				return
			}
		}
		chatQueueChanged()
		chatQueues.poke()
	default:
		http.Error(w, "GET, POST or PATCH", http.StatusMethodNotAllowed)
		return
	}
	writeChatQueue(w, host, pane)
}

func writeChatQueue(w http.ResponseWriter, host, pane string) {
	q := chatQueueFor(host, pane)
	if q == nil {
		q = &chatQueueView{Items: []chatQueueItem{}}
	}
	writeJSON(w, q)
}

// ---------------------------------------------------------------------------
// delivery
// ---------------------------------------------------------------------------

type chatQueueRunner struct {
	mu       sync.Mutex
	lock     *os.File
	inflight map[string]bool
	misses   map[string]int
	// idleSince is when each queue's agent was first seen idle since it last
	// read working or blocked.
	idleSince map[string]time.Time
	wake      chan struct{}
	wg        sync.WaitGroup
}

var chatQueues = newChatQueueRunner()

func newChatQueueRunner() *chatQueueRunner {
	return &chatQueueRunner{
		inflight:  map[string]bool{},
		misses:    map[string]int{},
		idleSince: map[string]time.Time{},
		wake:      make(chan struct{}, 1),
	}
}

// poke runs the loop now rather than at its next tick: a message queued for an
// agent that went idle a moment ago need not wait.
func (c *chatQueueRunner) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func startChatQueueLoop(ctx context.Context) {
	t := time.NewTicker(chatQueueEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-chatQueues.wake:
		}
		chatQueues.tick()
	}
}

// ownsLoop reports whether this lasso delivers, taking chatqueue.runner.lock
// when it is free. Taking it is also when a predecessor's interrupted sends are
// settled (sweepInterrupted).
func (c *chatQueueRunner) ownsLoop() bool {
	c.mu.Lock()
	if c.lock != nil {
		c.mu.Unlock()
		return true
	}
	path := filepath.Join(lassoDir(), "chatqueue.runner.lock")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		c.mu.Unlock()
		return false
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		c.mu.Unlock()
		return false
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid %d, %s\n", os.Getpid(), listenNote())), 0)
	c.lock = f
	c.mu.Unlock()
	sweepInterruptedChatSends()
	return true
}

// release drops the runner lock (tests; the kernel does it on exit).
func (c *chatQueueRunner) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lock != nil {
		_ = c.lock.Close()
		c.lock = nil
	}
}

// sweepInterruptedChatSends settles the rows a runner left "sending" when it
// died: the paste may have landed, so they are failed, never retried, and
// their queues pause until a human looks.
func sweepInterruptedChatSends() {
	rows, err := db.Query(`SELECT DISTINCT host, pane_id FROM chat_queue WHERE state='sending'`)
	if err != nil {
		return
	}
	var keys [][2]string
	for rows.Next() {
		var h, p string
		if rows.Scan(&h, &p) == nil {
			keys = append(keys, [2]string{h, p})
		}
	}
	rows.Close()
	if len(keys) == 0 {
		return
	}
	const why = "lasso restarted while sending this — check the terminal before sending it again"
	_, _ = db.Exec(`UPDATE chat_queue SET state='failed', detail=? WHERE state='sending'`, why)
	for _, k := range keys {
		pauseChatQueue(k[0], k[1], why, "warn")
	}
	chatQueueChanged()
}

// tick starts one delivery pass per queue that is not already in one.
func (c *chatQueueRunner) tick() {
	if db == nil || !c.ownsLoop() {
		return
	}
	rows, err := db.Query(`SELECT DISTINCT host, pane_id FROM chat_queue`)
	if err != nil {
		return
	}
	var keys [][2]string
	for rows.Next() {
		var h, p string
		if rows.Scan(&h, &p) == nil {
			keys = append(keys, [2]string{h, p})
		}
	}
	rows.Close()
	for _, k := range keys {
		key := k[0] + "\x00" + k[1]
		c.mu.Lock()
		busy := c.inflight[key]
		if !busy {
			c.inflight[key] = true
		}
		c.mu.Unlock()
		if busy {
			continue
		}
		c.wg.Add(1)
		go func(host, pane, key string) {
			defer c.wg.Done()
			defer func() {
				c.mu.Lock()
				delete(c.inflight, key)
				c.mu.Unlock()
			}()
			c.process(host, pane)
		}(k[0], k[1], key)
	}
}

// newestUserTurn is the id of the transcript's newest user turn (the client's
// newestUserID).
func newestUserTurn(items []chatItem) string {
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == "user" {
			return items[i].ID
		}
	}
	return ""
}

// process advances one pane's queue by at most one step.
func (c *chatQueueRunner) process(host, pane string) {
	key := host + "\x00" + pane
	items, err := loadChatQueueItems(host, pane)
	if err != nil {
		return
	}
	if len(items) == 0 {
		tidyChatQueue(host, pane)
		return
	}
	q, _, err := loadChatQueueRow(host, pane)
	if err != nil {
		return
	}
	be, err := chatQueueBackend(host)
	if err != nil {
		return
	}
	panes, err := panesRaw(be)
	if err != nil {
		return
	}
	p, ok := findChatPane(panes, pane)
	if !ok || pane == "" {
		// Only a listing with panes in it counts: an empty one is as likely a
		// herdr mid-restart as a fleet with nothing left in it.
		if len(panes) == 0 {
			return
		}
		c.mu.Lock()
		c.misses[key]++
		gone := c.misses[key] >= chatQueueMissLimit
		if gone {
			delete(c.misses, key)
		}
		c.mu.Unlock()
		if gone {
			// Nothing can show or deliver a queue whose pane is gone, and a pane
			// id reused later must not inherit it.
			_, _ = db.Exec(`DELETE FROM chat_queue WHERE host=? AND pane_id=?`, host, pane)
			tidyChatQueue(host, pane)
			log.Printf("chat queue: %s %s is gone; dropped %d queued message(s)", host, pane, len(items))
			chatQueueChanged()
		}
		return
	}
	c.mu.Lock()
	delete(c.misses, key)
	c.mu.Unlock()

	head := items[0]
	if head.State == cqSending {
		return
	}
	if q.paused && head.State != cqSent {
		// Not watched while paused, so a resume starts the idle clock over.
		c.mu.Lock()
		delete(c.idleSince, key)
		c.mu.Unlock()
		return
	}
	kind, status := paneAgentPresence(p)
	payload := buildChatPayload(be, p, "", false)
	newest := newestUserTurn(payload.Items)
	running := payload.Running || status == "working"
	// How long the agent has read idle on this loop's own passes. A message
	// sent from a composer a moment ago (a direct send, which this queue
	// knows nothing about) reads idle until herdr or the transcript catches
	// up; chatQueueIdleFor of quiet is what keeps the queue from typing into
	// that turn's start.
	now := chatQueueNow()
	c.mu.Lock()
	if running || status == "blocked" {
		delete(c.idleSince, key)
	} else if _, ok := c.idleSince[key]; !ok {
		c.idleSince[key] = now
	}
	quiet := now.Sub(c.idleSince[key])
	c.mu.Unlock()
	if head.State == cqSent {
		// The previous message's turn has to be in the transcript before the
		// next one goes (see the header). An idle agent past the wait is let go:
		// a slash command records no turn.
		landed := newest != "" && newest != head.After
		stale := !running && chatQueueNow().UnixMilli()-head.SentAt > chatQueueEchoWait.Milliseconds()
		if landed || stale {
			_, _ = db.Exec(`DELETE FROM chat_queue WHERE id=? AND state='sent'`, head.ID)
			tidyChatQueue(host, pane)
			chatQueueChanged()
		}
		return
	}
	if q.paused {
		return
	}
	if kind == "" {
		pauseChatQueue(host, pane, "no agent is running in this pane", "bad")
		chatQueueChanged()
		return
	}
	// A blocked agent's screen is a dialog: typed text would answer it.
	if running || status == "blocked" || quiet < chatQueueIdleFor {
		return
	}
	if q.session != "" && payload.Path != "" && q.session != payload.Path {
		pauseChatQueue(host, pane, "this pane is in a different conversation now", "warn")
		chatQueueChanged()
		return
	}
	var next *chatQueueItem
	for i := range items {
		if items[i].State == cqQueued {
			next = &items[i]
			break
		}
	}
	if next == nil {
		return
	}
	// Claimed with a conditional update, so a message deleted or edited away
	// from another lasso in the meantime is not sent.
	res, err := db.Exec(`UPDATE chat_queue SET state='sending', detail='' WHERE id=? AND state='queued' AND text=?`, next.ID, next.Text)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return
	}
	chatQueueChanged()
	outcome, detail := chatSubmit(be, pane, kind, next.Text)
	switch outcome {
	case chatSent:
		_, _ = db.Exec(`UPDATE chat_queue SET state='sent', sent_at=?, after_user=? WHERE id=?`, chatQueueNow().UnixMilli(), newest, next.ID)
		if payload.Path != "" {
			_, _ = db.Exec(`UPDATE chat_queues SET session=? WHERE host=? AND pane_id=?`, payload.Path, host, pane)
		}
	case chatRefused:
		_, _ = db.Exec(`UPDATE chat_queue SET state='queued' WHERE id=?`, next.ID)
		pauseChatQueue(host, pane, orDefault(detail, "the message was refused"), "bad")
	default:
		why := orDefault(detail, "delivery unconfirmed") + " — check the terminal. Nothing will be resent automatically."
		_, _ = db.Exec(`UPDATE chat_queue SET state='failed', detail=? WHERE id=?`, why, next.ID)
		pauseChatQueue(host, pane, why, "warn")
	}
	chatQueueChanged()
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// queueFake is one omp pane on one host: herdr's listing of it, its composer
// (a paste draws into it, Enter submits it) and its transcript, which records
// each submitted message as a user turn when `record` is set — the harness
// writing the turn the queue waits for before it sends the next.
type queueFake struct {
	Backend
	name string

	mu          sync.Mutex
	paneID      string
	status      string // herdr's agent_status
	path        string // the transcript
	composer    string // what the composer shows
	ignorePaste bool   // bytes accepted, never drawn: an uncertain send
	record      bool
	gone        bool // the pane is not listed; another one is
	writes      []string
	turns       int
}

func (b *queueFake) Name() string { return privateHostName(&b.name, "queuefake") }

func (b *queueFake) HomeDir() (string, error)           { return "", nil }
func (b *queueFake) Stat(p string) (fs.FileInfo, error) { return os.Stat(p) }
func (b *queueFake) Open(p string) (io.ReadSeekCloser, error) {
	return os.Open(p)
}

func (b *queueFake) set(fn func(*queueFake)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(b)
	invalidatePaneList(b.Name())
}

func (b *queueFake) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, w := range b.writes {
		if w != "\r" {
			out = append(out, w)
		}
	}
	return out
}

func (b *queueFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p, _ := params.(map[string]any)
	switch method {
	case "pane.list":
		if b.gone {
			return json.RawMessage(`{"panes":[` + chatPaneStatus("w9:p9", b.path, true, "idle") + `]}`), nil
		}
		return json.RawMessage(`{"panes":[` + chatPaneStatus(b.paneID, b.path, true, b.status) + `]}`), nil
	case "pane.read":
		screen := "some transcript\n╰─  ─╯"
		if b.composer != "" {
			screen = "some transcript\n╰─ " + b.composer + " ─╯"
		}
		return json.RawMessage(fmt.Sprintf(`{"read":{"text":%q}}`, screen)), nil
	case "pane.send_text":
		text, _ := p["text"].(string)
		b.writes = append(b.writes, text)
		if text != "\r" {
			if !b.ignorePaste {
				b.composer = text
			}
			return json.RawMessage(`{}`), nil
		}
		if b.composer != "" && b.record {
			b.turns++
			f, err := os.OpenFile(b.path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				fmt.Fprintf(f, `{"type":"message","id":"q%d","message":{"role":"user","content":[{"type":"text","text":%q}]}}`+"\n", b.turns, b.composer)
				f.Close()
			}
		}
		b.composer = ""
		return json.RawMessage(`{}`), nil
	}
	return nil, fmt.Errorf("unexpected herdr method %q", method)
}

// useQueueEnv stands up a fresh db, one fake pane (w1:p1, idle, recording
// turns) as both the tab's host and every queue's, and fast send timings.
func useQueueEnv(t *testing.T) (*queueFake, *chatQueueRunner) {
	t.Helper()
	openTestDB(t)
	withFastSubmit(t)
	path := filepath.Join(t.TempDir(), "2026-10-12T00-00-00-000Z_q.jsonl")
	if err := os.WriteFile(path, chatLog(recHeader, recUser), 0o644); err != nil {
		t.Fatal(err)
	}
	be := &queueFake{paneID: "w1:p1", status: "idle", path: path, record: true}
	useFakeHost(t, be)
	prev := chatQueueBackend
	chatQueueBackend = func(string) (Backend, error) { return be, nil }
	t.Cleanup(func() { chatQueueBackend = prev })
	prevIdle := chatQueueIdleFor
	chatQueueIdleFor = 0
	t.Cleanup(func() { chatQueueIdleFor = prevIdle })
	r := newChatQueueRunner()
	t.Cleanup(r.release)
	return be, r
}

// step runs one delivery pass and waits for it.
func step(t *testing.T, r *chatQueueRunner, be *queueFake) {
	t.Helper()
	invalidatePaneList(be.Name())
	r.tick()
	r.wg.Wait()
}

func queueOf(t *testing.T, be *queueFake) *chatQueueView {
	t.Helper()
	return chatQueueFor(be.Name(), "w1:p1")
}

func mustEnqueue(t *testing.T, be *queueFake, text string) int64 {
	t.Helper()
	id, err := enqueueChat(be.Name(), "w1:p1", text, "", "tab-a")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func queueReq(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	serveChatQueue(rec, httptest.NewRequest(method, target, rd))
	return rec
}

func decodeQueue(t *testing.T, rec *httptest.ResponseRecorder) chatQueueView {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var v chatQueueView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func texts(v *chatQueueView) []string {
	if v == nil {
		return nil
	}
	var out []string
	for _, it := range v.Items {
		out = append(out, it.Text)
	}
	return out
}

// The API: add, list, edit, reorder, pause, delete — each validated, each
// answering with the queue as it now stands.
func TestChatQueueAPI(t *testing.T) {
	be, _ := useQueueEnv(t)

	for _, c := range []struct {
		method, target, body string
		want                 int
	}{
		{http.MethodPost, "/api/chat/queue", `{"pane_id":"w1:p1","text":"  "}`, http.StatusBadRequest},
		{http.MethodPost, "/api/chat/queue", `{"text":"hi"}`, http.StatusBadRequest},
		{http.MethodPost, "/api/chat/queue", `{"pane_id":"w9:p9","text":"hi"}`, http.StatusNotFound},
		{http.MethodPost, "/api/chat/queue", `not json`, http.StatusBadRequest},
		{http.MethodPost, "/api/chat/queue", `{"pane_id":"w1:p1","text":"` + strings.Repeat("x", chatQueueMaxText+1) + `"}`, http.StatusRequestEntityTooLarge},
		{http.MethodDelete, "/api/chat/queue?pane=w1:p1", "", http.StatusMethodNotAllowed},
		{http.MethodDelete, "/api/chat/queue/abc?pane=w1:p1", "", http.StatusBadRequest},
		{http.MethodDelete, "/api/chat/queue/99?pane=w1:p1", "", http.StatusNotFound},
	} {
		if rec := queueReq(t, c.method, c.target, c.body); rec.Code != c.want {
			t.Errorf("%s %s %.40s: status %d, want %d (%s)", c.method, c.target, c.body, rec.Code, c.want, strings.TrimSpace(rec.Body.String()))
		}
	}

	v := decodeQueue(t, queueReq(t, http.MethodGet, "/api/chat/queue?pane=w1:p1", ""))
	if len(v.Items) != 0 {
		t.Fatalf("empty queue lists %v", v.Items)
	}
	for _, s := range []string{"one", "two", "three"} {
		v = decodeQueue(t, queueReq(t, http.MethodPost, "/api/chat/queue", `{"pane_id":"w1:p1","text":"`+s+`","client":"tab-a"}`))
	}
	if got := texts(&v); strings.Join(got, ",") != "one,two,three" {
		t.Fatalf("after adds: %v", got)
	}
	if v.Items[0].CreatedBy != "tab-a" || v.Items[0].State != cqQueued || v.Items[0].CreatedAt == 0 {
		t.Errorf("first item = %+v", v.Items[0])
	}
	one, two, three := v.Items[0].ID, v.Items[1].ID, v.Items[2].ID

	v = decodeQueue(t, queueReq(t, http.MethodPut, fmt.Sprintf("/api/chat/queue/%d", two), `{"pane_id":"w1:p1","text":"TWO"}`))
	v = decodeQueue(t, queueReq(t, http.MethodPatch, "/api/chat/queue", fmt.Sprintf(`{"pane_id":"w1:p1","order":[%d,%d]}`, three, one)))
	if got := texts(&v); strings.Join(got, ",") != "three,one,TWO" {
		t.Fatalf("after edit + reorder: %v", got)
	}
	if rec := queueReq(t, http.MethodPatch, "/api/chat/queue", `{"pane_id":"w1:p1","order":[12345]}`); rec.Code != http.StatusNotFound {
		t.Errorf("reorder naming a stranger: status %d, want 404", rec.Code)
	}

	v = decodeQueue(t, queueReq(t, http.MethodPatch, "/api/chat/queue", `{"pane_id":"w1:p1","paused":true}`))
	if !v.Paused {
		t.Error("pause did not stick")
	}
	v = decodeQueue(t, queueReq(t, http.MethodPatch, "/api/chat/queue", `{"pane_id":"w1:p1","paused":false}`))
	if v.Paused {
		t.Error("resume did not stick")
	}

	// A message being typed right now is neither edited nor removed.
	if _, err := db.Exec(`UPDATE chat_queue SET state='sending' WHERE id=?`, three); err != nil {
		t.Fatal(err)
	}
	if rec := queueReq(t, http.MethodDelete, fmt.Sprintf("/api/chat/queue/%d?pane=w1:p1", three), ""); rec.Code != http.StatusConflict {
		t.Errorf("delete while sending: status %d, want 409", rec.Code)
	}
	if rec := queueReq(t, http.MethodPut, fmt.Sprintf("/api/chat/queue/%d", three), `{"pane_id":"w1:p1","text":"x"}`); rec.Code != http.StatusConflict {
		t.Errorf("edit while sending: status %d, want 409", rec.Code)
	}
	// Another pane's message is not this pane's to remove.
	if rec := queueReq(t, http.MethodDelete, fmt.Sprintf("/api/chat/queue/%d?pane=w2:p2", one), ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete via another pane: status %d, want 404", rec.Code)
	}
	v = decodeQueue(t, queueReq(t, http.MethodDelete, fmt.Sprintf("/api/chat/queue/%d?pane=w1:p1", one), ""))
	if got := texts(&v); strings.Join(got, ",") != "three,TWO" {
		t.Fatalf("after delete: %v", got)
	}

	// The cap.
	for i := len(v.Items); i < chatQueueMaxItems; i++ {
		mustEnqueue(t, be, fmt.Sprint("m", i))
	}
	if rec := queueReq(t, http.MethodPost, "/api/chat/queue", `{"pane_id":"w1:p1","text":"one too many"}`); rec.Code != http.StatusConflict {
		t.Errorf("past the cap: status %d, want 409", rec.Code)
	}
}

// The chat poll carries the queue, so every device converges on it.
func TestChatPayloadCarriesQueue(t *testing.T) {
	be, _ := useQueueEnv(t)
	be.set(func(b *queueFake) { b.status = "working" })
	mustEnqueue(t, be, "later")
	rec := httptest.NewRecorder()
	serveChat(rec, httptest.NewRequest(http.MethodGet, "/api/chat?pane=w1:p1", nil))
	var out chatPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Queue == nil || len(out.Queue.Items) != 1 || out.Queue.Items[0].Text != "later" {
		t.Fatalf("payload queue = %+v", out.Queue)
	}
}

// Nothing goes while the agent works; once idle, one message at a time, oldest
// first, and the next only after the transcript has recorded the previous.
func TestChatQueueDeliversInOrderOneAtATime(t *testing.T) {
	be, r := useQueueEnv(t)
	be.set(func(b *queueFake) { b.status = "working" })
	mustEnqueue(t, be, "first")
	mustEnqueue(t, be, "second")

	step(t, r, be)
	if got := be.sent(); len(got) != 0 {
		t.Fatalf("sent %v while the agent was working", got)
	}

	be.set(func(b *queueFake) { b.status = "idle"; b.record = false })
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "first" {
		t.Fatalf("after the first idle pass: sent %v, want [first]", got)
	}
	q := queueOf(t, be)
	if q == nil || q.Items[0].State != cqSent || q.Items[0].Text != "first" {
		t.Fatalf("queue = %+v, want first as the sent echo", q)
	}

	// The turn is not in the transcript yet: an idle-looking agent is not
	// handed the next message.
	step(t, r, be)
	step(t, r, be)
	if got := be.sent(); len(got) != 1 {
		t.Fatalf("sent %v before the first message's turn was recorded", got)
	}

	// The harness writes the turn: the echo retires, and the next pass sends.
	appendUserTurn(t, be.path, "u-first", "first")
	step(t, r, be)
	if q := queueOf(t, be); q == nil || strings.Join(texts(q), ",") != "second" {
		t.Fatalf("after the turn landed: queue %v, want [second]", texts(q))
	}
	be.set(func(b *queueFake) { b.record = true })
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "first,second" {
		t.Fatalf("sent %v, want first then second", got)
	}
	step(t, r, be)
	if q := queueOf(t, be); q != nil {
		t.Fatalf("queue not emptied: %+v", q)
	}
	if n := countRows(t, `SELECT COUNT(*) FROM chat_queues`); n != 0 {
		t.Errorf("an empty queue left %d chat_queues row(s)", n)
	}
}

// A sent message whose turn never shows (a slash command) holds the queue only
// as long as chatQueueEchoWait, and only while the agent is idle.
func TestChatQueueEchoWaitLetsGo(t *testing.T) {
	be, r := useQueueEnv(t)
	be.set(func(b *queueFake) { b.record = false })
	mustEnqueue(t, be, "/compact")
	mustEnqueue(t, be, "next")
	step(t, r, be)
	now := time.Now()
	prev := chatQueueNow
	chatQueueNow = func() time.Time { return now.Add(chatQueueEchoWait + time.Second) }
	t.Cleanup(func() { chatQueueNow = prev })
	step(t, r, be)
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "/compact,next" {
		t.Fatalf("sent %v, want both once the wait ran out", got)
	}
}

// Refused (a human's draft in the pane): nothing typed, the message stays,
// the queue pauses with the reason, and it is not retried until resumed.
func TestChatQueueRefusalPausesWithoutRetry(t *testing.T) {
	be, r := useQueueEnv(t)
	be.set(func(b *queueFake) { b.composer = "half a thought" })
	mustEnqueue(t, be, "hello")
	step(t, r, be)
	step(t, r, be)
	if len(be.writes) != 0 {
		t.Fatalf("writes = %q, want none over a draft", be.writes)
	}
	q := queueOf(t, be)
	if q == nil || !q.Paused || q.Tone != "bad" || q.Note == "" || q.Items[0].State != cqQueued {
		t.Fatalf("queue = %+v, want paused (bad) with the message still queued", q)
	}

	be.set(func(b *queueFake) { b.composer = "" })
	step(t, r, be)
	if len(be.writes) != 0 {
		t.Fatal("a paused queue sent by itself")
	}
	if err := resumeChatQueue(be.Name(), "w1:p1"); err != nil {
		t.Fatal(err)
	}
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "hello" {
		t.Fatalf("after resume: sent %v", got)
	}
}

// Uncertain: it may have landed, so the message is failed, the queue pauses,
// and nothing is typed again by itself.
func TestChatQueueUncertainIsNeverResent(t *testing.T) {
	be, r := useQueueEnv(t)
	be.set(func(b *queueFake) { b.ignorePaste = true })
	mustEnqueue(t, be, "maybe")
	mustEnqueue(t, be, "after")
	step(t, r, be)
	step(t, r, be)
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "maybe" {
		t.Fatalf("sent %v, want exactly one attempt", got)
	}
	q := queueOf(t, be)
	if q == nil || !q.Paused || q.Tone != "warn" || q.Items[0].State != cqFailed || q.Items[0].Detail == "" {
		t.Fatalf("queue = %+v, want paused (warn) with the message failed", q)
	}
	// Resuming is the human's go-ahead: the failed message goes back in line.
	if err := resumeChatQueue(be.Name(), "w1:p1"); err != nil {
		t.Fatal(err)
	}
	if q := queueOf(t, be); q.Paused || q.Items[0].State != cqQueued {
		t.Fatalf("after resume: %+v", q)
	}
}

// A runner that died mid-send leaves a "sending" row; whoever takes the lock
// next marks it failed and pauses, rather than typing it again.
func TestChatQueueSweepsInterruptedSends(t *testing.T) {
	be, r := useQueueEnv(t)
	id := mustEnqueue(t, be, "in flight")
	mustEnqueue(t, be, "waiting")
	if _, err := db.Exec(`UPDATE chat_queue SET state='sending' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	step(t, r, be)
	if got := be.sent(); len(got) != 0 {
		t.Fatalf("sent %v after a restart mid-send", got)
	}
	q := queueOf(t, be)
	if q == nil || !q.Paused || q.Items[0].State != cqFailed || !strings.Contains(q.Items[0].Detail, "restarted") {
		t.Fatalf("queue = %+v", q)
	}
}

// The queue is lasso.db's: it outlives the process that held it.
func TestChatQueueSurvivesReopen(t *testing.T) {
	be, _ := useQueueEnv(t)
	mustEnqueue(t, be, "kept")
	closeTestDB()
	if err := openDB(); err != nil {
		t.Fatal(err)
	}
	if q := queueOf(t, be); q == nil || strings.Join(texts(q), ",") != "kept" {
		t.Fatalf("after reopen: %+v", q)
	}
}

// Two lassos on one lasso.db: only the lock holder delivers, and the other
// takes over once it is gone.
func TestChatQueueRunnerLock(t *testing.T) {
	be, r1 := useQueueEnv(t)
	r2 := newChatQueueRunner()
	t.Cleanup(r2.release)
	if !r1.ownsLoop() {
		t.Fatal("first runner did not take the lock")
	}
	if r2.ownsLoop() {
		t.Fatal("second runner took a held lock")
	}
	mustEnqueue(t, be, "once")
	step(t, r2, be)
	if got := be.sent(); len(got) != 0 {
		t.Fatalf("the runner without the lock sent %v", got)
	}
	r1.release()
	step(t, r2, be)
	if got := be.sent(); strings.Join(got, ",") != "once" {
		t.Fatalf("after the holder left: sent %v", got)
	}
}

// A pane gone from a non-empty listing for chatQueueMissLimit passes takes its
// queue with it; an empty listing (herdr mid-restart) proves nothing.
func TestChatQueueDropsAGonePane(t *testing.T) {
	be, r := useQueueEnv(t)
	mustEnqueue(t, be, "orphan")
	be.set(func(b *queueFake) { b.gone = true })
	for i := 0; i < chatQueueMissLimit-1; i++ {
		step(t, r, be)
	}
	if q := queueOf(t, be); q == nil {
		t.Fatal("dropped before the miss limit")
	}
	step(t, r, be)
	if q := queueOf(t, be); q != nil {
		t.Fatalf("queue kept for a gone pane: %+v", q)
	}
	if len(be.writes) != 0 {
		t.Fatalf("writes = %q", be.writes)
	}
}

// A queue bound to one conversation pauses when the pane is in another.
func TestChatQueuePausesOnAnotherConversation(t *testing.T) {
	be, r := useQueueEnv(t)
	if _, err := enqueueChat(be.Name(), "w1:p1", "follow-up", "/elsewhere/old.jsonl", ""); err != nil {
		t.Fatal(err)
	}
	step(t, r, be)
	q := queueOf(t, be)
	if len(be.writes) != 0 || q == nil || !q.Paused || q.Tone != "warn" {
		t.Fatalf("writes %q, queue %+v: want paused, nothing typed", be.writes, q)
	}
	// Resuming is the human accepting the conversation on screen.
	if err := resumeChatQueue(be.Name(), "w1:p1"); err != nil {
		t.Fatal(err)
	}
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "follow-up" {
		t.Fatalf("after resume: sent %v", got)
	}
}

// Stop holds a queue that has something in it, and only then.
func TestChatQueueStopPauses(t *testing.T) {
	be, _ := useQueueEnv(t)
	pauseChatQueueForStop(be.Name(), "w1:p1")
	if n := countRows(t, `SELECT COUNT(*) FROM chat_queues`); n != 0 {
		t.Fatalf("stop on an empty queue made %d row(s)", n)
	}
	mustEnqueue(t, be, "after the stop")
	pauseChatQueueForStop(be.Name(), "w1:p1")
	if q := queueOf(t, be); q == nil || !q.Paused {
		t.Fatalf("queue = %+v, want paused", q)
	}
	// A message queued into an EMPTY queue starts fresh, unpaused.
	if err := deleteChatQueued(be.Name(), "w1:p1", queueOf(t, be).Items[0].ID); err != nil {
		t.Fatal(err)
	}
	mustEnqueue(t, be, "fresh")
	if q := queueOf(t, be); q.Paused {
		t.Fatal("a fresh queue inherited the old pause")
	}
}

// A blocked agent's screen is a dialog, and typed text would answer it.
func TestChatQueueWaitsOutABlockedAgent(t *testing.T) {
	be, r := useQueueEnv(t)
	be.set(func(b *queueFake) { b.status = "blocked" })
	mustEnqueue(t, be, "not into a dialog")
	step(t, r, be)
	if len(be.writes) != 0 {
		t.Fatalf("typed %q into a blocked pane", be.writes)
	}
}

func appendUserTurn(t *testing.T, path, id, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fmt.Fprintf(f, `{"type":"message","id":%q,"message":{"role":"user","content":[{"type":"text","text":%q}]}}`+"\n", id, text)
}

func countRows(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A message is typed only once the agent has read idle for chatQueueIdleFor
// on the loop's own passes: a send from a composer a moment ago reads idle
// until herdr or the transcript catches up, and the queue must not type into
// the start of that turn.
func TestChatQueueWaitsForAQuietAgent(t *testing.T) {
	be, r := useQueueEnv(t)
	chatQueueIdleFor = 2 * time.Second
	now := time.Now()
	prev := chatQueueNow
	chatQueueNow = func() time.Time { return now }
	t.Cleanup(func() { chatQueueNow = prev })

	mustEnqueue(t, be, "patience")
	step(t, r, be)
	if len(be.writes) != 0 {
		t.Fatalf("typed %q on the first idle sighting", be.writes)
	}
	// Working in between restarts the clock.
	now = now.Add(time.Second)
	be.set(func(b *queueFake) { b.status = "working" })
	step(t, r, be)
	be.set(func(b *queueFake) { b.status = "idle" })
	now = now.Add(time.Second)
	step(t, r, be)
	now = now.Add(time.Second)
	step(t, r, be)
	if len(be.writes) != 0 {
		t.Fatalf("typed %q before %v of quiet", be.writes, chatQueueIdleFor)
	}
	now = now.Add(time.Second)
	step(t, r, be)
	if got := be.sent(); strings.Join(got, ",") != "patience" {
		t.Fatalf("sent %v after the quiet spell", got)
	}
}

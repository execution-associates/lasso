package main

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

// The Inbox's notices (docs/design/notifications.md, "The Inbox"): lasso's
// own notices and an agent's deliberate message, kept HERE rather than by
// each client, so the Mac app and the phone's simple app list the same ones.
// A client reads them with GET /api/notices when it connects and on a poll,
// hears new ones live as the `notification` SSE event (same id), and hides
// an acknowledged one by `notice:<id>` in ui_state.dismissed_cards, which is
// shared by every device. Memory only: a notice is about this run of lasso,
// and a restart starts the list empty, as it starts every client's.

// inboxNoticeMax bounds the list; the oldest go first.
const inboxNoticeMax = 50

type inboxNoticeLog struct {
	mu    sync.Mutex
	items []liveNotification
}

var inboxNotices inboxNoticeLog

// inboxKeeps reports whether a kind is listed as a notice. Every other kind
// is already an Inbox entry of its own (a blocked agent, a finished turn, a
// bot's reply), or never listed (a test).
func inboxKeeps(k notifKind) bool { return k == notifLassoNotice || k == notifAgentMessage }

func (l *inboxNoticeLog) add(n liveNotification) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if slices.ContainsFunc(l.items, func(x liveNotification) bool { return x.ID == n.ID }) {
		return
	}
	l.items = append(l.items, n)
	if over := len(l.items) - inboxNoticeMax; over > 0 {
		l.items = slices.Delete(l.items, 0, over)
	}
}

// list is the kept notices, oldest first.
func (l *inboxNoticeLog) list() []liveNotification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]liveNotification{}, l.items...)
}

// stampNotification gives n its id and time, once: the live event, the kept
// notice and the client acknowledging it all name it by that id. A notice
// kind is kept for the Inbox whether or not anything is listening right now,
// since a client that opens later lists it too.
func stampNotification(n notification) notification {
	if n.ID != "" {
		return n
	}
	n.ID = fmt.Sprintf("%s-%d", noticeBoot, noticeSeq.Add(1))
	n.At = time.Now().UnixMilli()
	if inboxKeeps(n.Kind) {
		inboxNotices.add(liveNotificationOf(n))
	}
	return n
}

// noticesPayload is GET /api/notices: the kept notices, oldest first.
type noticesPayload struct {
	Notices []liveNotification `json:"notices"`
}

func serveNotices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, noticesPayload{inboxNotices.list()})
}

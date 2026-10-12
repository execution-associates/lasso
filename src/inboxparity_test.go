package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func getUIStateHTTP(t *testing.T) uiState {
	t.Helper()
	w := httptest.NewRecorder()
	serveUIState(w, httptest.NewRequest(http.MethodGet, "/api/ui-state", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/ui-state: %d %s", w.Code, w.Body)
	}
	var us uiState
	if err := json.Unmarshal(w.Body.Bytes(), &us); err != nil {
		t.Fatal(err)
	}
	return us
}

// The Inbox's baseline is lasso's, not a device's: the first read sets it,
// every later read and write answers the same value, and no body moves it.
func TestInboxSinceIsSharedAndServerOwned(t *testing.T) {
	openTestDB(t)
	uiStateRevHub(t)

	if us, _ := getUIState(); us.InboxSince != 0 {
		t.Fatalf("fresh db already has a baseline: %d", us.InboxSince)
	}
	since := getUIStateHTTP(t).InboxSince
	if since <= 0 {
		t.Fatalf("GET did not seed inbox_since: %d", since)
	}
	if stored, _ := getUIState(); stored.InboxSince != since {
		t.Fatalf("seed not persisted: %d != %d", stored.InboxSince, since)
	}
	if again := getUIStateHTTP(t).InboxSince; again != since {
		t.Fatalf("a second read moved it: %d != %d", again, since)
	}
	got := postUIState(t, `{"inbox_since":1,"files_click_navigates":false,"client_id":"A"}`)
	if got.InboxSince != since {
		t.Fatalf("a body moved it: %d != %d", got.InboxSince, since)
	}
	if stored, _ := getUIState(); stored.InboxSince != since || stored.FilesClickNavigates {
		t.Fatalf("after the patch: since %d, files_click_navigates %v", stored.InboxSince, stored.FilesClickNavigates)
	}
}

// The boot seed, like a GET's, sets it once; a later seed leaves it be.
func TestSeedInboxSinceOnce(t *testing.T) {
	openTestDB(t)
	us, err := seedInboxSince()
	if err != nil || us.InboxSince <= 0 {
		t.Fatalf("seed: %d %v", us.InboxSince, err)
	}
	again, _ := seedInboxSince()
	if again.InboxSince != us.InboxSince || getUIStateHTTP(t).InboxSince != us.InboxSince {
		t.Fatalf("moved: %d then %d", us.InboxSince, again.InboxSince)
	}
}

// Notices are kept by lasso for every client: lasso's own and an agent's
// message are listed, with the same id the live event carried; the other
// kinds are Inbox entries of their own and are not.
func TestInboxNoticesAreKeptOnceWithTheLiveID(t *testing.T) {
	prevLog := inboxNotices.items
	inboxNotices.items = nil
	t.Cleanup(func() { inboxNotices.items = prevLog })
	prevHub := srvHub
	srvHub = newHub()
	t.Cleanup(func() { srvHub = prevHub })

	n := stampNotification(notification{Kind: notifLassoNotice, Level: "error", Title: "theme refused"})
	if n.ID == "" || n.At == 0 {
		t.Fatalf("not stamped: %+v", n)
	}
	// Stamping again (publish hands its stamped copy to deliver) keeps one.
	if again := stampNotification(n); again.ID != n.ID {
		t.Fatalf("restamped: %s != %s", again.ID, n.ID)
	}
	stampNotification(notification{Kind: notifAgentMessage, Title: "green"})
	stampNotification(notification{Kind: notifAgentFinished, Title: "done"})
	stampNotification(notification{Kind: notifBotMessage, Title: "reply"})
	stampNotification(notification{Kind: notifTest, Title: "test"})

	w := httptest.NewRecorder()
	serveNotices(w, httptest.NewRequest(http.MethodGet, "/api/notices", nil))
	var got noticesPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Notices) != 2 || got.Notices[0].ID != n.ID || got.Notices[0].Kind != "lasso_notice" ||
		got.Notices[0].Level != "error" || got.Notices[1].Kind != "agent_message" {
		t.Fatalf("notices = %+v", got.Notices)
	}
	if got.Notices[0].At != n.At {
		t.Fatalf("at %d != %d", got.Notices[0].At, n.At)
	}

	for range inboxNoticeMax + 5 {
		stampNotification(notification{Kind: notifLassoNotice, Title: "x"})
	}
	if l := inboxNotices.list(); len(l) != inboxNoticeMax || l[0].ID == n.ID {
		t.Fatalf("cap: %d kept, oldest %s", len(l), l[0].ID)
	}
}

// The Inbox's shared cases live in mac/Spec/inbox/rules.json, read by the Mac
// app's tests; the phone's tests read a copy inside src/web (its dev
// container mounts only that tree). The copy must not drift:
// LASSO_UPDATE_CONTRACT=1 rewrites it from the original.
func TestInboxRulesCopyInSync(t *testing.T) {
	orig := filepath.Join("..", "mac", "Spec", "inbox", "rules.json")
	cp := filepath.Join("web", "src", "simple", "inbox-rules.json")
	want, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(want, &doc); err != nil || len(doc.Cases) == 0 {
		t.Fatalf("%s: no cases (%v)", orig, err)
	}
	if os.Getenv("LASSO_UPDATE_CONTRACT") == "1" {
		if err := os.WriteFile(cp, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(cp)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%s differs from %s; copy it with LASSO_UPDATE_CONTRACT=1 go test -run TestInboxRulesCopyInSync", cp, orig)
	}
}

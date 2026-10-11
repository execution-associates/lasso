package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifWantsFollowsTheChoiceElseTheDefault(t *testing.T) {
	none := map[string]bool{}
	if !notifWants(none, notifAgentBlocked) {
		t.Error("agent_blocked is on by default")
	}
	if notifWants(none, notifAgentFinished) {
		t.Error("agent_finished is off by default")
	}
	chose := map[string]bool{"agent_blocked": false, "agent_finished": true}
	if notifWants(chose, notifAgentBlocked) || !notifWants(chose, notifAgentFinished) {
		t.Error("an explicit choice must win over the default")
	}
	if !notifWants(map[string]bool{"test": false}, notifTest) {
		t.Error("a test always goes through")
	}
	if notifWants(none, notifKind("made_up")) {
		t.Error("a kind outside the catalog is never wanted")
	}
}

func postNotifyDevice(t *testing.T, body string) (int, notifyDeviceView) {
	t.Helper()
	rec := httptest.NewRecorder()
	serveNotifyDevice(rec, httptest.NewRequest(http.MethodPost, "/api/notify/device", strings.NewReader(body)))
	var v notifyDeviceView
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, v
}

func TestNotifyDeviceMergesPerKindAndNullRestoresTheDefault(t *testing.T) {
	openTestDB(t)
	code, v := postNotifyDevice(t, `{"id":"phone-abcdef12","label":"iPhone","kinds":{"agent_finished":true,"bot_message":false}}`)
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !v.Device.Wants["agent_finished"] || v.Device.Wants["bot_message"] || !v.Device.Wants["agent_blocked"] {
		t.Errorf("wants = %+v", v.Device.Wants)
	}
	// A second write touches only what it names.
	_, v = postNotifyDevice(t, `{"id":"phone-abcdef12","kinds":{"bot_message":null}}`)
	if !v.Device.Wants["agent_finished"] || !v.Device.Wants["bot_message"] {
		t.Errorf("after null: wants = %+v", v.Device.Wants)
	}
	if v.Device.Label != "iPhone" {
		t.Errorf("label = %q, kept from the first write", v.Device.Label)
	}
	if len(v.Kinds) != len(notifKindCatalog) {
		t.Errorf("catalog has %d kinds, view %d", len(notifKindCatalog), len(v.Kinds))
	}
}

func TestNotifyDeviceRefusesUnknownKindsAndBadIDs(t *testing.T) {
	openTestDB(t)
	if code, _ := postNotifyDevice(t, `{"id":"phone-abcdef12","kinds":{"made_up":true}}`); code != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d", code)
	}
	if code, _ := postNotifyDevice(t, `{"id":"x","kinds":{}}`); code != http.StatusBadRequest {
		t.Errorf("short id: status %d", code)
	}
	if d, _ := loadNotifyDevice("phone-abcdef12"); len(d.Kinds) != 0 {
		t.Errorf("a refused write saved %+v", d.Kinds)
	}
}

// A subscription is sent only the kinds its device takes; one registered
// before devices existed gets the defaults.
func TestWebPushSkipsKindsTheDeviceTurnedOff(t *testing.T) {
	openTestDB(t)
	resetVapidCache(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	pushTestSub(t, srv.URL+"/legacy")
	pushTestSub(t, srv.URL+"/phone")
	if _, err := db.Exec(`UPDATE push_subscriptions SET device_id = 'phone-abcdef12' WHERE endpoint LIKE '%/phone'`); err != nil {
		t.Fatal(err)
	}
	if code, _ := postNotifyDevice(t, `{"id":"phone-abcdef12","kinds":{"agent_blocked":false,"agent_finished":true}}`); code != http.StatusOK {
		t.Fatal(code)
	}

	send := func(n notification) int32 {
		hits.Store(0)
		if err := (webPushChannel{}).deliver(context.Background(), n); err != nil {
			t.Fatalf("deliver: %v", err)
		}
		return hits.Load()
	}
	if got := send(notification{Kind: notifAgentBlocked, Title: "x"}); got != 1 {
		t.Errorf("agent_blocked reached %d devices, want 1 (the legacy one)", got)
	}
	if got := send(notification{Kind: notifAgentFinished, Title: "x"}); got != 1 {
		t.Errorf("agent_finished reached %d devices, want 1 (the phone)", got)
	}
	if got := send(notification{Kind: notifLassoNotice, Level: "success", Title: "synced"}); got != 0 {
		t.Errorf("a successful lasso notice was pushed to %d devices", got)
	}
	if got := send(notification{Kind: notifLassoNotice, Level: "error", Title: "refused"}); got != 2 {
		t.Errorf("a lasso error reached %d devices, want 2", got)
	}
}

func TestObserveNotifiesAFinishedTurn(t *testing.T) {
	w := &blockedWatcher{}
	now := time.Now()
	if got := w.observe([]hostPane{blockedTestPane("local", "p1", "done")}, now); len(got) != 0 {
		t.Fatalf("a first sighting says nothing about a turn: %+v", got)
	}
	w.observe([]hostPane{blockedTestPane("local", "p1", "working")}, now.Add(10*time.Second))
	got := w.observe([]hostPane{blockedTestPane("local", "p1", "done")}, now.Add(20*time.Second))
	if len(got) != 1 || got[0].Kind != notifAgentFinished {
		t.Fatalf("want one agent_finished, got %+v", got)
	}
	if got[0].Title != "Fix the push flow" || !strings.HasPrefix(got[0].Tag, "finished:") {
		t.Errorf("notification = %+v", got[0])
	}
	if again := w.observe([]hostPane{blockedTestPane("local", "p1", "idle")}, now.Add(30*time.Second)); len(again) != 0 {
		t.Errorf("done → idle is not another finished turn: %+v", again)
	}
}

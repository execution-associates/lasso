package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"testing"
)

// uiStateRevHub swaps in a fresh hub so a test can watch ui_state_rev move.
func uiStateRevHub(t *testing.T) func() int {
	t.Helper()
	prev := srvHub
	srvHub = newHub()
	t.Cleanup(func() { srvHub = prev })
	return func() int {
		_, rev := srvHub.revs()
		return rev
	}
}

func TestDefaultBot(t *testing.T) {
	openTestDB(t)
	rev := uiStateRevHub(t)

	got := postUIState(t, `{"client_id":"A"}`)
	if got.DefaultBot != "" {
		t.Fatalf("fresh install has a default bot: %q", got.DefaultBot)
	}
	r0 := rev()

	got = postUIState(t, `{"default_bot":"jessica","client_id":"A"}`)
	if got.DefaultBot != "jessica" || rev() != r0+1 {
		t.Fatalf("set: bot %q rev %d (from %d)", got.DefaultBot, rev(), r0)
	}
	// Restating it is a no-op: no save, no rev bump.
	postUIState(t, `{"default_bot":"jessica","client_id":"A"}`)
	if rev() != r0+1 {
		t.Fatalf("no-op bumped the rev: %d", rev())
	}
	// A patch not naming it leaves it alone.
	got = postUIState(t, `{"chat_sidebar":true,"client_id":"A"}`)
	if got.DefaultBot != "jessica" {
		t.Fatalf("unrelated patch changed the default bot: %q", got.DefaultBot)
	}

	// An invalid name refuses the whole patch.
	for _, bad := range []string{`"Jessica"`, `"-x"`, `"a b"`, `"` + fmt.Sprintf("%041d", 0) + `"`} {
		w := postUIStateRaw(t, `{"default_bot":`+bad+`,"chat_sidebar":false,"client_id":"A"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("default_bot %s accepted: %d", bad, w.Code)
		}
	}
	stored, _ := getUIState()
	if stored.DefaultBot != "jessica" || !stored.ChatSidebar {
		t.Fatalf("a refused patch landed: %+v", stored)
	}

	got = postUIState(t, `{"default_bot":"","client_id":"A"}`)
	if got.DefaultBot != "" {
		t.Fatalf("clear: %q", got.DefaultBot)
	}
}

func TestCardDismissOps(t *testing.T) {
	openTestDB(t)
	rev := uiStateRevHub(t)

	got := postUIState(t, `{"client_id":"A"}`)
	if got.DismissedCards == nil || len(got.DismissedCards) != 0 {
		t.Fatalf("fresh install: %#v", got.DismissedCards)
	}
	r0 := rev()

	got = postUIState(t, `{"card_dismiss":{"titan\u0000w1:p1":1000,"minime\u0000w2:p1":2000},"client_id":"A"}`)
	want := map[string]int64{"titan\x00w1:p1": 1000, "minime\x00w2:p1": 2000}
	if !maps.Equal(got.DismissedCards, want) || rev() != r0+1 {
		t.Fatalf("dismiss: %v rev %d", got.DismissedCards, rev())
	}

	// The same ops again change nothing and bump nothing.
	postUIState(t, `{"card_dismiss":{"titan\u0000w1:p1":1000},"client_id":"A"}`)
	if rev() != r0+1 {
		t.Fatalf("no-op dismissal bumped the rev: %d", rev())
	}

	// The map itself in a body is ignored, and an unrelated patch keeps it.
	got = postUIState(t, `{"dismissed_cards":{},"files_click_navigates":false,"client_id":"B"}`)
	if !maps.Equal(got.DismissedCards, want) {
		t.Fatalf("direct write or unrelated patch changed it: %v", got.DismissedCards)
	}
	r1 := rev()
	postUIState(t, `{"dismissed_cards":{"x\u0000y":5},"client_id":"B"}`)
	if rev() != r1 {
		t.Fatalf("an ignored direct write bumped the rev")
	}

	// A later value moves a dismissal; null forgets one.
	got = postUIState(t, `{"card_dismiss":{"titan\u0000w1:p1":3000,"minime\u0000w2:p1":null},"client_id":"A"}`)
	want = map[string]int64{"titan\x00w1:p1": 3000}
	if !maps.Equal(got.DismissedCards, want) {
		t.Fatalf("move/forget: %v", got.DismissedCards)
	}
	stored, _ := getUIState()
	if !maps.Equal(stored.DismissedCards, want) {
		t.Fatalf("not persisted: %v", stored.DismissedCards)
	}

	for _, bad := range []string{`{"":1}`, `{"` + fmt.Sprintf("%0301d", 0) + `":1}`, `{"a\u0000b":-1}`} {
		if w := postUIStateRaw(t, `{"card_dismiss":`+bad+`,"client_id":"A"}`); w.Code != http.StatusBadRequest {
			t.Fatalf("card_dismiss %s accepted: %d", bad, w.Code)
		}
	}
}

func TestMergeDismissedCardsCap(t *testing.T) {
	stored := map[string]int64{}
	for i := range maxDismissedCards {
		stored[fmt.Sprintf("h\x00p%03d", i)] = int64(1000 + i)
	}
	at := int64(5)
	got := mergeDismissedCards(stored, map[string]*int64{"h\x00new": &at})
	if len(got) != maxDismissedCards {
		t.Fatalf("len %d", len(got))
	}
	// The new entry has the smallest transcript_at, so it is the one dropped.
	if _, ok := got["h\x00new"]; ok {
		t.Fatal("cap kept the smallest transcript_at")
	}
	at = 99999
	got = mergeDismissedCards(stored, map[string]*int64{"h\x00new": &at})
	if _, ok := got["h\x00p000"]; ok || got["h\x00new"] != 99999 || len(got) != maxDismissedCards {
		t.Fatalf("cap did not drop the oldest: len %d", len(got))
	}
	// Repair drops what the API could not have written.
	if got := mergeDismissedCards(map[string]int64{"": 1, "a": -1, "b": 2}, nil); !maps.Equal(got, map[string]int64{"b": 2}) {
		t.Fatalf("repair = %v", got)
	}
}

// A desktop client names an agent by its herdr label (`herdr agent rename`,
// agent.list's name) before its tab: one workspace often holds several agents.
func TestEnumerateHostPanesCarriesAgentLabel(t *testing.T) {
	t.Setenv("LASSO_DIR", t.TempDir())
	if err := openDB(); err != nil {
		t.Fatalf("openDB: %v", err)
	}
	t.Cleanup(closeTestDB)
	panes, err := enumerateHostPanes(&labelFake{memBackend: newMemBackend()}, "local", "local")
	if err != nil {
		t.Fatalf("enumerateHostPanes: %v", err)
	}
	got := map[string]string{}
	for _, p := range panes {
		got[p.PaneID] = p.AgentLabel
	}
	if got["p1"] != "stub" || got["p2"] != "" {
		t.Fatalf("agent labels = %v, want p1 stub and p2 none", got)
	}
}

type labelFake struct{ *memBackend }

func (b *labelFake) HerdrCall(method string, params any) (json.RawMessage, error) {
	switch method {
	case "pane.list":
		return json.RawMessage(`{"panes":[
			{"pane_id":"p1","agent":"claude","agent_status":"idle","workspace_id":"w1","tab_id":"t1"},
			{"pane_id":"p2","agent":"claude","agent_status":"idle","workspace_id":"w1","tab_id":"t2"}]}`), nil
	case "agent.list":
		return json.RawMessage(`{"agents":[
			{"pane_id":"p1","agent":"claude","name":"stub"},
			{"pane_id":"p2","agent":"claude"}]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

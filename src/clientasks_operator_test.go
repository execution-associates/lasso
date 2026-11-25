package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The operator's Inbox (clientasks_operator.go): lasso's own UI as one app user.

func operatorDo(t *testing.T, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	w := httptest.NewRecorder()
	serveOperatorAsks(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	raw, _ := io.ReadAll(w.Result().Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return w.Code, out, string(raw)
}

func TestOperatorAsks(t *testing.T) {
	c := newClientTest(t)
	rig := newChannelRig(t, "jess")

	// Nobody owns a bot: an empty Inbox that says why, and nothing to act on.
	code, out, raw := operatorDo(t, "GET", "/api/asks", "")
	if code != 200 || out["operator"] != nil || out["reason"] == "" || len(out["asks"].([]any)) != 0 {
		t.Fatalf("no owner: %d %s", code, raw)
	}
	if code, _, raw := operatorDo(t, "POST", "/api/asks/ask_x/answer", `{"choice":"send"}`); code != http.StatusConflict || errCode([]byte(raw)) != "no_operator" {
		t.Fatalf("answer with no operator: %d %s", code, raw)
	}

	// One owner needs no choosing.
	annID, ann := c.person("Ann", true, "jess")
	askID := mustAsk(t, "jess", ndaAsk)
	code, out, raw = operatorDo(t, "GET", "/api/asks", "")
	if code != 200 || len(out["asks"].([]any)) != 1 {
		t.Fatalf("one owner: %d %s", code, raw)
	}
	_, out, raw = operatorDo(t, "GET", "/api/asks/operator", "")
	if out["operator"].(map[string]any)["id"] != annID || out["chosen"] != false {
		t.Fatalf("operator = %s", raw)
	}

	// A second owner makes it a choice, and the choice holds.
	c.addBot("clem")
	bobID, _ := c.person("Bob", true, "clem")
	code, out, raw = operatorDo(t, "GET", "/api/asks", "")
	if code != 200 || out["operator"] != nil || len(out["candidates"].([]any)) != 2 || len(out["asks"].([]any)) != 0 {
		t.Fatalf("two owners: %d %s", code, raw)
	}
	if code, _, raw := operatorDo(t, "PUT", "/api/asks/operator", `{"user_id":"u_nobody"}`); code != 400 {
		t.Fatalf("an unknown person: %d %s", code, raw)
	}
	if code, out, raw := operatorDo(t, "PUT", "/api/asks/operator", `{"user_id":"`+bobID+`"}`); code != 200 || out["operator"].(map[string]any)["id"] != bobID || out["chosen"] != true {
		t.Fatalf("choose bob: %d %s", code, raw)
	}
	// Bob's Inbox is not Ann's: her ask is unknown there.
	if _, out, raw := operatorDo(t, "GET", "/api/asks", ""); len(out["asks"].([]any)) != 0 {
		t.Fatalf("bob sees ann's ask: %s", raw)
	}
	if code, _, raw := operatorDo(t, "POST", "/api/asks/"+askID+"/answer", `{"choice":"send"}`); code != 404 {
		t.Fatalf("bob answers ann's ask: %d %s", code, raw)
	}

	// As Ann, the answer reaches the bot labelled, with no device named.
	if code, _, raw := operatorDo(t, "PUT", "/api/asks/operator", `{"user_id":"`+annID+`"}`); code != 200 {
		t.Fatalf("choose ann: %d %s", code, raw)
	}
	if code, out, raw := operatorDo(t, "POST", "/api/asks/"+askID+"/answer", `{"choice":"hold"}`); code != 200 || out["ask"].(map[string]any)["state"] != "answered" {
		t.Fatalf("answer: %d %s", code, raw)
	}
	got := clientEventsOf(claim(t, "jess"), "ask_answered")
	if len(got) != 1 {
		t.Fatalf("bot events = %+v", got)
	}
	m := got[0].Meta
	if m["choice"] != "hold" || m["user_id"] != annID || m["is_owner"] != "true" || m["verified_via"] != "operator" || m["device_id"] != "" || m["paired_by"] != "" {
		t.Fatalf("answer event = %+v", m)
	}
	if err := rig.ch.notify(got[0]); err != nil {
		t.Fatal(err)
	}
	ls := rig.lines()
	if last := ls[len(ls)-1]; !strings.Contains(last, `"verified_via":"operator"`) {
		t.Errorf("the bot is not told the answer came from the web UI: %s", last)
	}
	// The phone sees the same closed ask.
	if list, _ := c.asks(ann, "?state=all"); len(list) != 1 || list[0].State != "answered" {
		t.Fatalf("ann's phone = %+v", list)
	}

	// Clearing the choice with two owners is back to nobody.
	if code, out, raw := operatorDo(t, "PUT", "/api/asks/operator", `{}`); code != 200 || out["operator"] != nil {
		t.Fatalf("clear: %d %s", code, raw)
	}
}

// The Bots view's Threads page: the operator's own threads on the person's routes.
func TestOperatorThreads(t *testing.T) {
	c := newClientTest(t)
	do := func(method, path, body string) (int, map[string]any, string) {
		t.Helper()
		w := httptest.NewRecorder()
		serveOperatorThreads(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		raw, _ := io.ReadAll(w.Result().Body)
		out := map[string]any{}
		_ = json.Unmarshal(raw, &out)
		return w.Code, out, string(raw)
	}
	if code, _, raw := do("GET", "/api/operator/threads", ""); code != http.StatusConflict || errCode([]byte(raw)) != "no_operator" {
		t.Fatalf("no operator: %d %s", code, raw)
	}
	_, ann := c.person("Ann", true, "jess")
	c.addBot("clem")
	_, bob := c.person("Bob", false, "clem")
	main := c.conv(ann, "jess")
	bobs := c.conv(bob, "clem")

	code, out, raw := do("GET", "/api/operator/bots", "")
	if code != 200 || len(out["bots"].([]any)) != 1 || out["bots"].([]any)[0].(map[string]any)["main_thread_id"] != main {
		t.Fatalf("bots: %d %s", code, raw)
	}
	if code, out, raw := do("GET", "/api/operator/threads?bot=jess", ""); code != 200 || len(out["threads"].([]any)) != 1 {
		t.Fatalf("threads: %d %s", code, raw)
	}
	code, out, raw = do("POST", "/api/operator/threads/"+main+"/messages", `{"text":"hello from the web","client_id":"w1"}`)
	if code != http.StatusAccepted || out["message"].(map[string]any)["role"] != "user" {
		t.Fatalf("send: %d %s", code, raw)
	}
	// The phone reads what the web wrote: one thread, two clients.
	if h := c.history(ann, main); len(h) != 1 || h[0].Text != "hello from the web" {
		t.Fatalf("ann's phone = %+v", h)
	}
	if code, out, raw := do("GET", "/api/operator/threads/"+main+"/messages", ""); code != 200 || len(out["messages"].([]any)) != 1 {
		t.Fatalf("messages: %d %s", code, raw)
	}
	// Someone else's thread, and the routes that need a device, are not here.
	for _, p := range []string{"/api/operator/threads/" + bobs + "/messages", "/api/operator/threads/" + bobs, "/api/operator/events",
		"/api/operator/threads/" + main + "/events", "/api/operator/me", "/api/operator/device", "/api/operator/asks"} {
		if code, _, raw := do("GET", p, ""); code != 404 {
			t.Errorf("%s: %d %s", p, code, raw)
		}
	}
	if code, _, raw := do("POST", "/api/operator/threads/"+main+"/calls", `{}`); code != 404 {
		t.Errorf("a call from the web: %d %s", code, raw)
	}
}

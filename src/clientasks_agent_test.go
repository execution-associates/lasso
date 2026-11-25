package main

import (
	"net/http"
	"strings"
	"testing"
)

// A coding agent's question in the operator's Inbox (clientasks_agent.go).

const agentHookAsk = `{"hook_event_name":"PreToolUse","session_id":"5f0c9d4e-aaaa-bbbb-cccc-0123456789ab","cwd":"/home/x/projects/lasso","tool_name":"AskUserQuestion",
 "tool_input":{"questions":[
  {"question":"Which database should the service use?","header":"Database","multiSelect":false,"options":[
    {"label":"Postgres","description":"What production already runs"},{"label":"SQLite","description":"One file, no server"}]},
  {"question":"Ship behind a flag?","header":"Rollout","multiSelect":false,"options":[{"label":"Yes","description":""},{"label":"No","description":""}]}]}}`

func TestAgentAsks(t *testing.T) {
	c := newClientTest(t)
	type typed struct {
		pane, kind, expect string
		labels             []string
		picks              []chatAskPick
	}
	var got []typed
	outcome, detail := "sent", ""
	prevA, prevK := agentAnswerFn, agentPaneKindFn
	agentAnswerFn = func(_ Backend, pane, kind, expect string, labels []string, picks []chatAskPick) (string, string) {
		got = append(got, typed{pane, kind, expect, labels, picks})
		return outcome, detail
	}
	agentPaneKindFn = func(_ Backend, pane string) (string, error) {
		if pane == "w1:p9" {
			return "", errChatNoPane
		}
		return "claude", nil
	}
	t.Cleanup(func() { agentAnswerFn, agentPaneKindFn = prevA, prevK })
	hook := func(pane, body string) map[string]any {
		t.Helper()
		code, out, raw := operatorDo(t, "POST", "/api/asks/agent?pane_id="+pane, body)
		if code != 200 {
			t.Fatalf("hook: %d %s", code, raw)
		}
		return out
	}
	done := func(event string) map[string]any {
		return hook("w1:p1", `{"hook_event_name":"`+event+`","session_id":"5f0c9d4e-aaaa-bbbb-cccc-0123456789ab","tool_name":"AskUserQuestion"}`)
	}
	waiting := func() []any {
		t.Helper()
		_, out, _ := operatorDo(t, "GET", "/api/asks", "")
		return out["asks"].([]any)
	}

	// Nobody to ask: the hook is told so and still succeeds.
	if out := hook("w1:p1", agentHookAsk); out["skipped"] == nil || len(out["asks"].([]any)) != 0 {
		t.Fatalf("no operator: %v", out)
	}
	annID, ann := c.person("Ann", true, "jess")

	// What lasso could not type back stays in the terminal.
	for pane, body := range map[string]string{
		"":      agentHookAsk,
		"w1:p9": agentHookAsk,
		"w1:p1": strings.Replace(agentHookAsk, `"multiSelect":false`, `"multiSelect":true`, 1),
	} {
		if out := hook(pane, body); out["skipped"] == nil || len(waiting()) != 0 {
			t.Fatalf("pane %q: %v", pane, out)
		}
	}

	out := hook("w1:p1", agentHookAsk)
	ids := out["asks"].([]any)
	if len(ids) != 2 || out["skipped"] != nil {
		t.Fatalf("filed: %v", out)
	}
	list := waiting()
	first := list[0].(map[string]any)
	if len(list) != 2 || first["bot"] != "" || first["question"] != "Which database should the service use?" || first["subject"] != "Database" || first["area"] != "lasso" ||
		first["agent"].(map[string]any)["pane_id"] != "w1:p1" || first["agent"].(map[string]any)["of"] != float64(2) ||
		first["options"].([]any)[0].(map[string]any)["detail"] != "What production already runs" {
		t.Fatalf("list = %v", list)
	}
	// The phone lists it too, with no bot to hold a grant on.
	if phone, _ := c.asks(ann, ""); len(phone) != 2 || phone[0].Agent == nil {
		t.Fatalf("ann's phone = %+v", phone)
	}

	// One answer of two types nothing yet.
	q1, q2 := ids[0].(string), ids[1].(string)
	code, ans, raw := operatorDo(t, "POST", "/api/asks/"+q1+"/answer", `{"choice":"o2"}`)
	if code != 200 || ans["ask"].(map[string]any)["agent"].(map[string]any)["delivery"] != "waiting" || len(got) != 0 {
		t.Fatalf("first answer: %d %s typed %v", code, raw, got)
	}
	if code, _, raw := operatorDo(t, "POST", "/api/asks/"+q1+"/answer", `{"message_id":"m_x"}`); code == 200 {
		t.Fatalf("a message answers an agent: %s", raw)
	}
	code, ans, raw = operatorDo(t, "POST", "/api/asks/"+q2+"/answer", `{"other":"only for  staff"}`)
	if code != 200 || ans["ask"].(map[string]any)["agent"].(map[string]any)["delivery"] != "sent" || len(got) != 1 {
		t.Fatalf("second answer: %d %s typed %v", code, raw, got)
	}
	g := got[0]
	if g.pane != "w1:p1" || g.kind != "claude" || g.expect != "Which database should the service use?" || len(g.labels) != 2 || len(g.picks) != 2 ||
		len(g.picks[0].Selected) != 1 || g.picks[0].Selected[0] != 1 || g.picks[0].Options != 2 || g.picks[1].Custom != "only for staff" || len(g.picks[1].Selected) != 0 {
		t.Fatalf("typed = %+v", g)
	}
	if a, _ := askByID(q1); a.Agent.Delivery != "sent" || a.UserID != annID {
		t.Fatalf("first ask = %+v", a.Agent)
	}
	// No bot hears of it.
	if evs := clientEventsOf(claim(t, "jess"), "ask_answered"); len(evs) != 0 {
		t.Fatalf("bot events = %+v", evs)
	}

	// The terminal answered first: what is open is withdrawn, once.
	hook("w1:p1", agentHookAsk)
	if len(waiting()) != 2 {
		t.Fatal("a second call was not filed")
	}
	if out := done("PostToolUse"); out["withdrawn"] != float64(2) || len(waiting()) != 0 {
		t.Fatalf("done: %v", out)
	}
	if out := done("Stop"); out["withdrawn"] != float64(0) {
		t.Fatalf("done again: %v", out)
	}

	// A dialog that is gone: the answer stands and says it was not typed.
	one := hook("w1:p1", agentHookAsk)["asks"].([]any)
	outcome, detail = "refused", "that question is no longer on the pane's screen — answer it in the terminal"
	_, _, _ = operatorDo(t, "POST", "/api/asks/"+one[0].(string)+"/answer", `{"choice":"o1"}`)
	code, ans, raw = operatorDo(t, "POST", "/api/asks/"+one[1].(string)+"/answer", `{"choice":"o1"}`)
	if code != http.StatusOK || !strings.Contains(ans["ask"].(map[string]any)["agent"].(map[string]any)["delivery"].(string), "no longer on the pane") {
		t.Fatalf("refused: %d %s", code, raw)
	}
	// A new call replaces a session's older open one; a dropped question
	// keeps the rest out of the pane.
	hook("w1:p1", agentHookAsk)
	next := hook("w1:p1", agentHookAsk)
	if next["withdrawn"] != float64(2) || len(waiting()) != 2 {
		t.Fatalf("replaced: %v", next)
	}
	n := len(got)
	ids = next["asks"].([]any)
	_, _, _ = operatorDo(t, "POST", "/api/asks/"+ids[0].(string)+"/drop", `{}`)
	_, ans, raw = operatorDo(t, "POST", "/api/asks/"+ids[1].(string)+"/answer", `{"choice":"o1"}`)
	if len(got) != n || !strings.Contains(raw, "closed unanswered") {
		t.Fatalf("after a drop: typed %d→%d %s %v", n, len(got), raw, ans)
	}
}

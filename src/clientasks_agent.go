package main

// A coding agent's question in the operator's Inbox (docs/design/client-asks.md,
// "An agent's question"). Claude Code's AskUserQuestion dialog blocks its pane
// until someone answers; the lasso-asks plugin's hooks post the tool call
// here, each question becomes an ask of the operator's, and once every
// question of the call is answered lasso types the answers into the pane with
// the chat view's own code (chatAnswer), which checks the dialog is still
// that question first. The terminal's dialog stays up the whole time: whoever
// answers first wins, and the hooks withdraw what the terminal settled.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// askAgentOpenMax is how many agent questions may wait on one person.
	askAgentOpenMax = 40
	// askAgentMaxAge closes a question nobody settled, in the Inbox or the
	// terminal: the hooks that withdraw one do not run when a session is killed.
	askAgentMaxAge     = 24 * time.Hour
	askOptionDetailMax = 300
	askAgentWhyMax     = 400
)

var askAgentSessionRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// askAgent is where an agent's ask came from and how its answer is typed
// back: stored as JSON in app_asks.agent.
type askAgent struct {
	Host    string `json:"host"`
	PaneID  string `json:"pane_id"`
	Name    string `json:"name,omitempty"`
	Session string `json:"session"`
	// Group is the tool call: its questions are answered in one go. N is this
	// question's place among its Of.
	Group string `json:"group"`
	N     int    `json:"n"`
	Of    int    `json:"of"`
	// Expect and Labels are the call's FIRST question and its options, what
	// the dialog on screen is checked against before anything is typed.
	Expect   string   `json:"expect"`
	Labels   []string `json:"labels"`
	Previews bool     `json:"previews,omitempty"`
	// Delivery is what became of typing the answers: "sent", or why not.
	Delivery string `json:"delivery,omitempty"`
}

type askAgentView struct {
	Name   string `json:"name,omitempty"`
	Host   string `json:"host"`
	PaneID string `json:"pane_id"`
	N      int    `json:"n"`
	Of     int    `json:"of"`
	// Delivery is "sent" once the answers were typed into the pane, "waiting"
	// while other questions of the same call are open, or the reason lasso
	// could not type them (the person answers in the terminal instead).
	Delivery string `json:"delivery,omitempty"`
}

func (g *askAgent) view() *askAgentView {
	return &askAgentView{Name: g.Name, Host: g.Host, PaneID: g.PaneID, N: g.N, Of: g.Of, Delivery: g.Delivery}
}

func askAgentKey(session, group string, n int) string {
	return fmt.Sprintf("agent:%s:%s:%d", session, group, n)
}

// agentHookIn is a Claude Code hook's stdin, as the plugin forwards it.
type agentHookIn struct {
	Event     string `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Questions []agentQuestion `json:"questions"`
	} `json:"tool_input"`
}

type agentQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
		Preview     string `json:"preview"`
	} `json:"options"`
}

// serveAgentAsk is POST /api/asks/agent?pane_id=…: a hook of the lasso-asks
// plugin. The dialog opening (PreToolUse) files its questions; every other
// event means the session moved on, so what is still open is withdrawn. It
// always answers 200 with what it did: a hook must never fail its agent.
func serveAgentAsk(w http.ResponseWriter, r *http.Request, u *appUser) {
	if r.Method != http.MethodPost {
		clientMethod(w, "POST")
		return
	}
	var in agentHookIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in); err != nil {
		clientError(w, http.StatusBadRequest, "invalid_request", "The body is a Claude Code hook's JSON.")
		return
	}
	if !askAgentSessionRE.MatchString(in.SessionID) {
		clientError(w, http.StatusBadRequest, "invalid_request", "session_id is required.")
		return
	}
	now := time.Now()
	withdrawn := agentAsksWithdraw(u.ID, in.SessionID, now)
	if in.Event != "PreToolUse" || in.ToolName != "AskUserQuestion" {
		clientJSON(w, http.StatusOK, map[string]any{"withdrawn": withdrawn})
		return
	}
	ids, skipped := agentAsksFile(u, in, strings.TrimSpace(r.URL.Query().Get("pane_id")), now)
	out := map[string]any{"asks": ids, "withdrawn": withdrawn}
	if skipped != "" {
		out["skipped"] = skipped
	}
	clientJSON(w, http.StatusOK, out)
}

// agentAsksWithdraw closes a session's open asks: the terminal answered, or
// the agent moved on.
func agentAsksWithdraw(userID, session string, now time.Time) int {
	n := 0
	for _, a := range listAsks(`a.user_id = ? AND a.agent != '' AND a.state = 'open' AND a.dedupe_key LIKE ? ORDER BY a.seq`, userID, "agent:"+session+":%") {
		if _, ok := askClose(a, "withdrawn", "terminal", now); ok {
			n++
		}
	}
	return n
}

// agentAsksFile turns one AskUserQuestion call into asks, or says why not.
// All or none: a call lasso could only answer in part stays in the terminal.
func agentAsksFile(u *appUser, in agentHookIn, paneID string, now time.Time) ([]string, string) {
	qs := in.ToolInput.Questions
	if paneID == "" {
		return nil, "the session is not in a herdr pane, so lasso could not type the answer back"
	}
	if len(qs) == 0 || len(qs) > 4 {
		return nil, "no questions"
	}
	be, err := botBackend("local")
	if err != nil {
		return nil, err.Error()
	}
	if _, err := agentPaneKindFn(be, paneID); err != nil {
		return nil, "no such pane on this host"
	}
	for _, q := range qs {
		switch {
		case q.MultiSelect:
			return nil, "a multi-select question is answered in the terminal"
		case len(q.Options) == 0 || len(q.Options) > askOptionsMax:
			return nil, "a question needs 1 to 4 options"
		case askText(q.Question, askQuestionMax, false) == "":
			return nil, "an empty question"
		}
	}
	var open int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_asks WHERE user_id = ? AND agent != '' AND state = 'open'`, u.ID).Scan(&open)
	if open+len(qs) > askAgentOpenMax {
		return nil, "too many agent questions are already waiting"
	}
	name := agentLabelOf(be, paneID)
	group := randHex(4)
	labels := []string{}
	for _, o := range qs[0].Options {
		labels = append(labels, o.Label)
	}
	area := askText(filepath.Base(in.Cwd), askAreaMax, false)
	if area == "." || area == "/" {
		area = ""
	}
	stamp := nowStamp(now)
	ids := []string{}
	for i, q := range qs {
		g := askAgent{Host: "local", PaneID: paneID, Name: askText(name, askLabelMax, false), Session: in.SessionID, Group: group, N: i + 1, Of: len(qs),
			Expect: qs[0].Question, Labels: labels}
		opts := []askOption{}
		for j, o := range q.Options {
			if o.Preview != "" {
				g.Previews = true
			}
			opts = append(opts, askOption{ID: fmt.Sprintf("o%d", j+1), Label: askText(o.Label, askOptionLabelMax, false), Detail: askText(o.Description, askOptionDetailMax, false)})
		}
		// The whole question when the one line had to cut it short.
		why := ""
		if tooLong(q.Question, askQuestionMax) {
			why = askText(q.Question, askAgentWhyMax, false)
		}
		gj, _ := json.Marshal(g)
		oj, _ := json.Marshal(opts)
		id := clientID("ask_", 12)
		// claude's dialog has no free-text row beside previews.
		_, err := db.Exec(`INSERT INTO app_asks (id, user_id, bot, thread_id, dedupe_key, area, subject, blocking, question, why, options, allow_other,
			deadline, on_expire, created_at, updated_at, agent) VALUES (?, ?, '', '', ?, ?, ?, 'none', ?, ?, ?, ?, ?, '', ?, ?, ?)`,
			id, u.ID, askAgentKey(in.SessionID, group, i+1), area, askText(q.Header, askSubjectMax, false), askText(q.Question, askQuestionMax, false), why,
			string(oj), boolInt(!g.Previews), nowStamp(now.Add(askAgentMaxAge)), stamp, stamp, string(gj))
		if err != nil {
			continue
		}
		ids = append(ids, id)
		if a, err := askByID(id); err == nil {
			askAnnounce(a, now)
		}
	}
	return ids, ""
}

// askTellAgent is askTellBot for an agent's ask: once every question of the
// call is answered, the answers are typed into its pane. Dropped and expired
// asks tell nobody: the dialog is still in the terminal.
func askTellAgent(a *appAsk, event string, now time.Time) {
	if event != "ask_answered" {
		return
	}
	g := a.Agent
	group := listAsks(`a.user_id = ? AND a.agent != '' AND a.dedupe_key LIKE ? ORDER BY a.seq`, a.UserID, fmt.Sprintf("agent:%s:%s:%%", g.Session, g.Group))
	note := func(delivery string) {
		for _, x := range group {
			if x.State != "answered" {
				continue
			}
			x.Agent.Delivery = delivery
			b, _ := json.Marshal(x.Agent)
			_, _ = db.Exec(`UPDATE app_asks SET agent = ?, updated_at = ? WHERE id = ?`, string(b), nowStamp(now), x.ID)
			if x.ID != a.ID {
				if cur, err := askByID(x.ID); err == nil {
					askAnnounce(cur, now)
				}
			}
		}
	}
	picks := []chatAskPick{}
	for _, x := range group {
		switch x.State {
		case "open":
			note("waiting")
			return
		case "answered":
		default:
			note("another question of this call was closed unanswered: answer it in the terminal")
			return
		}
		p := chatAskPick{Options: len(x.Options), Previews: x.Agent.Previews}
		if x.Other != "" {
			p.Custom = strings.Join(strings.Fields(x.Other), " ")
		} else {
			for i, o := range x.Options {
				if o.ID == x.Choice {
					p.Selected = []int{i}
				}
			}
		}
		picks = append(picks, p)
	}
	if len(picks) == 0 || len(picks) != g.Of {
		note("part of this call is missing: answer it in the terminal")
		return
	}
	if err := checkAskAnswers(g.Labels, picks); err != nil {
		note(err.Error() + ": answer it in the terminal")
		return
	}
	be, err := botBackend(g.Host)
	if err != nil {
		note("lasso cannot reach the agent's host: answer it in the terminal")
		return
	}
	kind, err := agentPaneKindFn(be, g.PaneID)
	if err != nil {
		note("the agent's pane is gone")
		return
	}
	outcome, detail := agentAnswerFn(be, g.PaneID, kind, g.Expect, g.Labels, picks)
	if outcome != "sent" {
		note(detail)
		return
	}
	note("sent")
}

// agentAnswerFn types the answers and agentPaneKindFn finds the pane's
// harness; seams for tests, which have no pane.
var (
	agentAnswerFn   = chatAnswer
	agentPaneKindFn = chatPaneKind
)

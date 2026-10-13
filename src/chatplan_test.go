package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Escapes as Claude Code 2.1 draws its plan dialog: the number and an unfocused
// placeholder in one muted colour, the focused row in another, a focused empty
// field dim, and typed text plain after a reset.
const (
	planMuted   = "\x1b[0m\x1b[38;2;56;74;91m"
	planFocus   = "\x1b[0m\x1b[38;2;57;75;88m"
	planReset   = "\x1b[0m"
	planDim     = "\x1b[0m\x1b[2m"
	planAsked   = "Claude has written up a plan and is ready to execute. Would you like to proceed?"
	planFeedRow = "Tell Claude what to change"
)

// planScreen draws a plan dialog: rows are the options (the cursor on row
// `cursor`, counting the field as the last row), field is the free-text row
// ("" for none), typed says the field holds the human's text rather than its
// placeholder.
func planScreen(question string, rows []string, field string, typed bool, cursor int) string {
	var b strings.Builder
	b.WriteString("  ⎿  /plan to preview\n  ──────────────────────────\n")
	if question != "" {
		b.WriteString("   " + planMuted + question + planReset + "\n\n")
	}
	all := append([]string(nil), rows...)
	if field != "" {
		all = append(all, field)
	}
	for i, r := range all {
		mark := "     "
		if i == cursor {
			mark = "   " + planFocus + "❯ "
		}
		num := planMuted + itoaTest(i+1) + ". "
		switch {
		case field != "" && i == len(all)-1 && typed:
			b.WriteString(mark + num + planReset + r + "\r\n")
		case field != "" && i == len(all)-1 && i == cursor:
			b.WriteString(mark + num + planDim + r + planReset + "\r\n")
		case field != "" && i == len(all)-1:
			b.WriteString(mark + planMuted + itoaTest(i+1) + ". " + r + planReset + "\r\n")
		default:
			b.WriteString(mark + num + planReset + r + "\r\n")
		}
		if field != "" && i == len(all)-1 {
			b.WriteString("        " + planMuted + "shift+tab to approve with this feedback" + planReset + "\r\n")
		}
	}
	b.WriteString("\n   " + planMuted + "ctrl+g to edit in Hx · ~/.claude/plans/x.md" + planReset)
	return b.String()
}

func itoaTest(n int) string { return string(rune('0' + n)) }

// The dialog exactly as a live Claude Code 2.1 pane drew it (bypass-capable
// session): two options, the feedback field, the cursor on the first row.
func TestParsePlanDialogLive(t *testing.T) {
	raw, err := os.ReadFile("testdata/screens/claude-plan-bypass.ansi")
	if err != nil {
		t.Fatal(err)
	}
	d, ok := parsePlanDialog(string(raw))
	if !ok {
		t.Fatal("the live dialog did not parse")
	}
	if d.Question != planAsked {
		t.Errorf("question = %q", d.Question)
	}
	want := []string{"Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session", "Yes, manually approve edits"}
	if strings.Join(d.Options, "|") != strings.Join(want, "|") {
		t.Errorf("options = %q, want %q", d.Options, want)
	}
	if !d.HasInput || d.Other != planFeedRow || d.Cursor != 0 {
		t.Errorf("field/other/cursor = %v/%q/%d, want the empty feedback field and the cursor on row 1", d.HasInput, d.Other, d.Cursor)
	}
	// The plan's own numbered steps sit above the dialog on the same screen
	// and must not be read as its rows.
	ask := d.ask()
	if len(ask.Questions) != 1 || len(ask.Questions[0].Options) != 2 || ask.Questions[0].Other != planFeedRow {
		t.Errorf("ask = %+v", ask)
	}
}

// The options vary with the session's permission mode and the Claude Code
// version; whatever the dialog shows is what is offered, nothing assumed.
func TestParsePlanDialogVariants(t *testing.T) {
	for _, tc := range []struct {
		name     string
		screen   string
		ok       bool
		options  []string
		other    string
		hasInput bool
		cursor   int
		question string
	}{
		{
			name:    "auto-accept",
			screen:  planScreen(planAsked, []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, planFeedRow, false, 0),
			ok:      true,
			options: []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, other: planFeedRow, hasInput: true, question: planAsked,
		},
		{
			name: "manual, three approvals",
			screen: planScreen(planAsked, []string{"Yes, and use auto mode", "Yes, and auto-accept edits", "Yes, manually approve edits"},
				planFeedRow, false, 1),
			ok:      true,
			options: []string{"Yes, and use auto mode", "Yes, and auto-accept edits", "Yes, manually approve edits"}, other: planFeedRow, hasInput: true, cursor: 1, question: planAsked,
		},
		{
			// Older Claude Code: "keep planning" is an option, not a field.
			name:    "no field",
			screen:  planScreen("Would you like to proceed?", []string{"Yes, and auto-accept edits", "Yes, and manually approve edits", "No, keep planning"}, "", false, 2),
			ok:      true,
			options: []string{"Yes, and auto-accept edits", "Yes, and manually approve edits", "No, keep planning"}, cursor: 2, question: "Would you like to proceed?",
		},
		{
			// The field focused and empty: its placeholder is dim.
			name:    "field focused",
			screen:  planScreen(planAsked, []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, planFeedRow, false, 2),
			ok:      true,
			options: []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, other: planFeedRow, hasInput: true, cursor: 2, question: planAsked,
		},
		{
			// The human has typed into the field: it is a draft, not a place
			// lasso may type into, so no `other` is offered.
			name:    "draft in the field",
			screen:  planScreen(planAsked, []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, "make it say hi", true, 0),
			ok:      true,
			options: []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, hasInput: true, question: planAsked,
		},
		{
			// A pane too short for the question: the field's hint anchors it.
			name:    "short pane",
			screen:  planScreen("", []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, planFeedRow, false, 0),
			ok:      true,
			options: []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, other: planFeedRow, hasInput: true,
		},
		{
			name: "wrapped label",
			screen: "   " + planMuted + planAsked + planReset + "\n\n" +
				"   " + planFocus + "❯ " + planMuted + "1. " + planFocus + "Yes, and switch to BYPASS PERMISSIONS (no further\n" +
				"      " + planFocus + "prompts) for this session" + planReset + "\n" +
				"     " + planMuted + "2. " + planReset + "Yes, manually approve edits\n" +
				"     " + planMuted + "3. Tell Claude what to change" + planReset + "\n" +
				"        " + planMuted + "shift+tab to approve with this feedback" + planReset + "\n",
			ok:      true,
			options: []string{"Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session", "Yes, manually approve edits"}, other: planFeedRow, hasInput: true, question: planAsked,
		},
		{
			// Answered: the dialog is in scrollback and claude has drawn its
			// composer and more conversation underneath.
			name: "scrollback",
			screen: planScreen(planAsked, []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, planFeedRow, false, 0) +
				"\n● Writing hello.txt\n● Done.\n────\n❯ \n────\n  ⏵⏵ accept edits on",
		},
		{
			name:   "prose mentioning the question",
			screen: "● I'll ask: Would you like to proceed?\n\n  1. First thing\n  2. Second thing\n",
		},
		{
			name:   "no cursor",
			screen: strings.ReplaceAll(planScreen(planAsked, []string{"A", "B"}, planFeedRow, false, 0), "❯ ", "  "),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := parsePlanDialog(tc.screen)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v (%+v)", ok, tc.ok, d)
			}
			if !ok {
				return
			}
			if strings.Join(d.Options, "|") != strings.Join(tc.options, "|") {
				t.Errorf("options = %q, want %q", d.Options, tc.options)
			}
			if d.Other != tc.other || d.HasInput != tc.hasInput || d.Cursor != tc.cursor || d.Question != tc.question {
				t.Errorf("other/input/cursor/question = %q/%v/%d/%q, want %q/%v/%d/%q",
					d.Other, d.HasInput, d.Cursor, d.Question, tc.other, tc.hasInput, tc.cursor, tc.question)
			}
		})
	}
}

const (
	recPlanClaude = `{"type":"assistant","uuid":"pa1","timestamp":"2026-10-13T01:00:00.000Z","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_plan","name":"ExitPlanMode","input":{"plan":"# Plan: write hello world to hello.txt\n\n## Steps\n1. Run printf.\n2. Stop.\n","planFilePath":"/home/me/.claude/plans/cheerful-taco.md"}}]}}`
	recPlanApproved = `{"type":"user","uuid":"pu1","timestamp":"2026-10-13T01:01:00.000Z","toolUseResult":{"plan":"# Plan","filePath":"/home/me/.claude/plans/cheerful-taco.md","isAgent":false},` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_plan","content":"User has approved your plan. You can now start coding.\n\n## Approved Plan:\n# Plan: write hello world to hello.txt"}]}}`
	recPlanSentBack = `{"type":"user","uuid":"pu2","timestamp":"2026-10-13T01:01:00.000Z","toolUseResult":"Error: The user doesn't want to proceed with this tool use.",` +
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_plan","is_error":true,"content":"The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file). To tell you how to proceed, the user said:\nWrite \"hi there\" instead of hello world\n\nNote: The user's next message may contain a correction or preference."}]}}`
)

func planToolOf(t *testing.T, parsed chatParse) *chatTool {
	t.Helper()
	for _, it := range parsed.items {
		if it.Tool != nil && it.Tool.Plan != nil {
			return it.Tool
		}
	}
	t.Fatal("no plan card in the parse")
	return nil
}

// The plan is the card: its Markdown, its file and its title, and once the
// dialog was answered a verdict rather than the plan printed a second time.
func TestParseClaudePlan(t *testing.T) {
	tool := planToolOf(t, parseTranscript("claude", chatLog(recPlanClaude), 0))
	if tool.Family != "plan" || tool.Title != "Plan" || tool.State != "running" {
		t.Errorf("family/title/state = %q/%q/%q", tool.Family, tool.Title, tool.State)
	}
	if !strings.HasPrefix(tool.Plan.Text, "# Plan: write hello world") || tool.Plan.Path != "/home/me/.claude/plans/cheerful-taco.md" {
		t.Errorf("plan = %+v", tool.Plan)
	}
	if tool.Subject != "Plan: write hello world to hello.txt" {
		t.Errorf("subject = %q, want the plan's heading", tool.Subject)
	}
	if tool.Ask != nil {
		t.Error("the transcript alone must not invent choices: they come from the screen")
	}

	ok := planToolOf(t, parseTranscript("claude", chatLog(recPlanClaude, recPlanApproved), 0))
	if ok.State != "completed" || ok.ResultLine != "approved" || ok.Output != "" {
		t.Errorf("approved = %q/%q/%q", ok.State, ok.ResultLine, ok.Output)
	}

	back := planToolOf(t, parseTranscript("claude", chatLog(recPlanClaude, recPlanSentBack), 0))
	if back.State != "completed" || back.ResultLine != "changes asked" || back.Plan.Feedback != `Write "hi there" instead of hello world` || back.Error != "" {
		t.Errorf("sent back = %q/%q/%q/%q", back.State, back.ResultLine, back.Plan.Feedback, back.Error)
	}
}

// planBackend is a claude pane showing a screen; every write is recorded and
// leaves the dialog behind, as answering it does.
type planBackend struct {
	chatSendBackend
}

// HerdrCall answers pane.read with the screen JSON-encoded: the embedded
// backend's %q would write its escapes as Go's "\x1b", which is not JSON.
func (b *planBackend) HerdrCall(method string, params any) (json.RawMessage, error) {
	if method == "pane.read" && !b.failRead {
		text, _ := json.Marshal(b.screen)
		return json.RawMessage(`{"read":{"text":` + string(text) + `}}`), nil
	}
	return b.chatSendBackend.HerdrCall(method, params)
}

func newPlanBackend(screen string) *planBackend {
	return &planBackend{chatSendBackend{paneID: "w1:p1", agent: "claude", screen: screen}}
}

func TestAttachPlanAsk(t *testing.T) {
	be := newPlanBackend(planScreen(planAsked, []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}, planFeedRow, false, 0))
	parsed := parseTranscript("claude", chatLog(recPlanClaude), 0)
	attachPlanAsk(be, "w1:p1", parsed.items)
	tool := planToolOf(t, parsed)
	if tool.Ask == nil || len(tool.Ask.Questions) != 1 {
		t.Fatalf("ask = %+v, want the dialog's one question", tool.Ask)
	}
	q := tool.Ask.Questions[0]
	if q.Question != planAsked || len(q.Options) != 2 || q.Options[0].Label != "Yes, and auto-accept edits" || q.Other != planFeedRow {
		t.Errorf("question = %+v", q)
	}

	// No dialog on screen: no choices, only the plan.
	be.screen = "● Thinking…\n────\n❯ \n────"
	parsed = parseTranscript("claude", chatLog(recPlanClaude), 0)
	attachPlanAsk(be, "w1:p1", parsed.items)
	if planToolOf(t, parsed).Ask != nil {
		t.Error("an ask with no dialog on screen")
	}

	// An answered plan never reads the screen for one.
	be.failRead = true
	parsed = parseTranscript("claude", chatLog(recPlanClaude, recPlanApproved), 0)
	attachPlanAsk(be, "w1:p1", parsed.items)
	if planToolOf(t, parsed).Ask != nil {
		t.Error("an ask on an answered plan")
	}
}

func TestServeChatAnswerPlan(t *testing.T) {
	gap := chatAskSegmentGap
	chatAskSegmentGap = 0
	t.Cleanup(func() { chatAskSegmentGap = gap })
	opts := []string{"Yes, and auto-accept edits", "Yes, manually approve edits"}
	be := newPlanBackend("")
	useFakeHost(t, be)
	post := func(body string) map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		serveChatAnswer(rec, httptest.NewRequest(http.MethodPost, "/api/chat/answer", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
		}
		var out map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	labels := `["Yes, and auto-accept edits","Yes, manually approve edits"]`
	body := func(answer string) string {
		return `{"pane_id":"w1:p1","expect":"` + planAsked + `","labels":` + labels + `,"answers":[` + answer + `]}`
	}
	down, up := "\x1b[B", "\x1b[A"

	for _, tc := range []struct {
		name    string
		screen  string
		answer  string
		outcome string
		writes  []string
	}{
		{"pick the first", planScreen(planAsked, opts, planFeedRow, false, 0), `{"selected":[0],"options":2}`, "sent", []string{"\r"}},
		{"pick the second", planScreen(planAsked, opts, planFeedRow, false, 0), `{"selected":[1],"options":2}`, "sent", []string{down + "\r"}},
		// A human left the cursor on the field: the walk goes UP from there,
		// since the dialog wraps and a walk from the top would land elsewhere.
		{"from a moved cursor", planScreen(planAsked, opts, planFeedRow, false, 2), `{"selected":[0],"options":2}`, "sent", []string{up + up + "\r"}},
		{"feedback", planScreen(planAsked, opts, planFeedRow, false, 0), `{"selected":[],"options":2,"custom":"Write hi there","other":"` + planFeedRow + `"}`, "sent",
			[]string{down + down, "Write hi there", "\r"}},
		{"feedback, cursor already on the field", planScreen(planAsked, opts, planFeedRow, false, 2), `{"selected":[],"options":2,"custom":"Write hi there","other":"` + planFeedRow + `"}`, "sent",
			[]string{"Write hi there", "\r"}},
		// The human's own draft is in the field: typing would glue onto it.
		{"draft in the field", planScreen(planAsked, opts, "make it", true, 0), `{"selected":[],"options":2,"custom":"Write hi there","other":"` + planFeedRow + `"}`, "refused", nil},
		{"no field", planScreen(planAsked, opts, "", false, 0), `{"selected":[],"options":2,"custom":"Write hi there"}`, "refused", nil},
		// The options changed under the card (another permission mode, a
		// newer dialog): refused, never mapped by index onto different rows.
		{"different options", planScreen(planAsked, []string{"Yes, and use auto mode", "Yes, manually approve edits"}, planFeedRow, false, 0), `{"selected":[0],"options":2}`, "refused", nil},
		{"an extra option", planScreen(planAsked, append([]string{"Yes, and use auto mode"}, opts...), planFeedRow, false, 0), `{"selected":[0],"options":2}`, "refused", nil},
		// The dialog is gone: the agent moved on, or the human answered it.
		{"dialog gone", "● Writing hello.txt\n────\n❯ \n────", `{"selected":[0],"options":2}`, "refused", nil},
		{"an AskUserQuestion up instead", "  Which fruit?\n  ❯ 1. Yes, and auto-accept edits\n    2. Banana\n  Enter to select · ↑/↓ to navigate · Esc to cancel", `{"selected":[0],"options":2}`, "refused", nil},
		{"two picks", planScreen(planAsked, opts, planFeedRow, false, 0), `{"selected":[0,1],"options":2,"multi":true}`, "refused", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			be.screen, be.writes = tc.screen, nil
			out := post(body(tc.answer))
			if out["outcome"] != tc.outcome {
				t.Fatalf("outcome = %q (%s), want %q", out["outcome"], out["detail"], tc.outcome)
			}
			if strings.Join(be.writes, "|") != strings.Join(tc.writes, "|") {
				t.Errorf("writes = %q, want %q", be.writes, tc.writes)
			}
		})
	}
}

func TestClipPlanKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", chatPlanCap) // two bytes each: the cap lands mid-rune
	got := clipPlan(s)
	if !strings.HasSuffix(got, "\n…") || strings.ContainsRune(got, '�') || len(got) > chatPlanCap+len("\n…") {
		t.Errorf("clipped to %d bytes", len(got))
	}
	for _, r := range strings.TrimSuffix(got, "\n…") {
		if r != 'é' {
			t.Fatalf("split a rune: %q", r)
		}
	}
}

package main

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Claude Code's plan approval, as a chat shows it and answers it.
//
// When a claude agent in plan mode is done planning it calls ExitPlanMode with
// the plan as Markdown, and its terminal stops on a dialog:
//
//	Claude has written up a plan and is ready to execute. Would you like to proceed?
//
//	❯ 1. Yes, and switch to BYPASS PERMISSIONS (no further prompts) for this session
//	  2. Yes, manually approve edits
//	  3. Tell Claude what to change
//	     shift+tab to approve with this feedback
//
// The call's arguments carry the plan (chatPlan), so the card shows what is
// being approved. The CHOICES are not in the transcript at all: they depend on
// the session's permission mode and the Claude Code version, so they are read
// off the pane's screen while the dialog is up (parsePlanDialog) and offered as
// an ordinary ask, answered through the same /api/chat/answer.

// chatPlan is the plan an ExitPlanMode call put up for approval.
type chatPlan struct {
	// Text is the plan as the agent wrote it, Markdown.
	Text string `json:"text"`
	// Path is the plan file Claude Code keeps it in (~/.claude/plans/…), on
	// the pane's host.
	Path string `json:"path,omitempty"`
	// Feedback is what the human asked to change, once the plan was sent back
	// with some ("Tell Claude what to change").
	Feedback string `json:"feedback,omitempty"`
}

// chatPlanCap bounds one plan's text. A plan is the document the human is
// approving, so it gets far more room than a tool's output — but it rides
// every poll while it is on screen.
const chatPlanCap = 48 << 10

func isExitPlanMode(name string) bool { return strings.EqualFold(name, "ExitPlanMode") }

// describePlan fills a card from ExitPlanMode's arguments: the plan itself, its
// file, and its title (the first heading) as the one-line subject.
func describePlan(t *chatTool, text, path string) {
	t.Title = "Plan"
	text = strings.TrimSpace(text)
	if text == "" && path == "" {
		return
	}
	t.Plan = &chatPlan{Text: clipPlan(text), Path: path}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(strings.TrimLeft(line, "#")); line != "" {
			t.Subject = clipLine(line, chatSubjectCap)
			break
		}
	}
	if t.Subject == "" {
		t.Subject = clipLine(shortPath(path), chatSubjectCap)
	}
}

// clipPlan is clipBlock that never splits a rune: a plan is prose, and a cut
// that lands inside a multibyte character turns the last word into U+FFFD.
func clipPlan(s string) string {
	if len(s) <= chatPlanCap {
		return s
	}
	cut := chatPlanCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…"
}

// planFeedbackRE pulls the human's words out of the result Claude Code writes
// when a plan is sent back: "…To tell you how to proceed, the user said:\n<the
// feedback>\n\nNote: The user's next message may contain a correction…".
var planFeedbackRE = regexp.MustCompile(`(?s)the user said:\s*\n(.*?)(?:\n\s*\nNote: |\z)`)

// finishPlan settles a plan card. Approval returns the whole plan again ("User
// has approved your plan … ## Approved Plan: …"), which the card already shows,
// so it becomes a verdict. A plan sent back WITH feedback is the human's
// decision, not a failure, so it reads as completed with the feedback beside
// the plan; a bare rejection (Esc) stays the error it is.
func finishPlan(t *chatTool, body string, isError bool) {
	if !isError {
		t.State = "completed"
		t.ResultLine = "approved"
		return
	}
	if m := planFeedbackRE.FindStringSubmatch(body); m != nil {
		if fb := strings.TrimSpace(m[1]); fb != "" {
			t.State = "completed"
			t.ResultLine = "changes asked"
			t.Plan.Feedback = clipBlock(fb, chatAskQuestionCap)
			return
		}
	}
	t.State = "error"
	t.Error = clipLine(body, 200)
	if t.Error == "" {
		t.Error = "failed"
	}
}

// ---------------------------------------------------------------------------
// the dialog on screen
// ---------------------------------------------------------------------------

// planDialog is the plan-approval dialog as the pane draws it.
type planDialog struct {
	// Question is the dialog's prompt line(s), "" when a short pane has
	// scrolled it off the top.
	Question string
	// Options are the rows Enter answers with, in order, as drawn.
	Options []string
	// HasInput says the row after Options is a free-text field ("Tell Claude
	// what to change"): typing fills it and Enter sends the plan back with it.
	HasInput bool
	// Other is that field's placeholder, set only while the field is EMPTY.
	// Text typed into it replaces the placeholder on screen, and a lasso answer
	// typed after it would be glued onto the human's draft.
	Other string
	// Cursor is the row ❯ is on, counting the input row as len(Options).
	Cursor int
}

// planQuestionMark is what makes a dialog the plan approval. Every Claude Code
// version since plan mode shipped has asked it in these words.
const planQuestionMark = "would you like to proceed?"

// planInputHint is the line Claude Code draws under the plan dialog's free-text
// row, and the plain-text sign that the row is a field rather than an option.
const planInputHint = "to approve with this feedback"

var planRowRE = regexp.MustCompile(`^\s*(❯\s*)?(\d{1,2})\.\s+(.*)$`)

// parsePlanDialog reads the plan dialog off an ANSI screen (pane.read
// format=ansi). ok is false unless the dialog is unmistakably up: its numbered
// rows numbered 1..n with the cursor on one of them, under the plan question or
// above the free-text row's hint, at the BOTTOM of the screen — a dialog in
// scrollback above whatever claude drew since is history, not a question.
//
// The attributes matter for one thing: the free-text row's placeholder is drawn
// styled (dim while the row is focused, in the muted colour of its number while
// it is not), while text the human typed into it is drawn plain — measured on
// Claude Code 2.1 — and plain text cannot tell "Tell Claude what to change"
// from a draft that happens to say so.
func parsePlanDialog(screen string) (planDialog, bool) {
	type cell struct {
		text   string
		styled bool // every printed rune on the line has an attribute set
	}
	raw := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	lines := make([]cell, len(raw))
	for i, l := range raw {
		runes, styled := sgrStyled(l)
		allStyled, printed := true, false
		for j, r := range runes {
			if r == ' ' || r == '\t' {
				continue
			}
			printed = true
			if !styled[j] {
				allStyled = false
			}
		}
		lines[i] = cell{text: strings.TrimRight(string(runes), " \t"), styled: printed && allStyled}
	}

	// The question: its last line on screen, so an older dialog in scrollback
	// never stands in for the one that is up.
	qEnd := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(lines[i].text), planQuestionMark) {
			qEnd = i
			break
		}
	}
	start := -1
	if qEnd >= 0 {
		for i := qEnd + 1; i < len(lines); i++ {
			if m := planRowRE.FindStringSubmatch(lines[i].text); m != nil {
				if m[2] == "1" {
					start = i
				}
				break
			}
		}
	} else {
		// A pane too short for the question: the free-text row's hint
		// anchors the dialog, and its rows are the numbered run above it.
		hint := -1
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(strings.ToLower(lines[i].text), planInputHint) {
				hint = i
				break
			}
		}
		for i := hint - 1; i >= 0 && hint > 0; i-- {
			if m := planRowRE.FindStringSubmatch(lines[i].text); m != nil && m[2] == "1" {
				start = i
				break
			}
		}
	}
	if start < 0 {
		return planDialog{}, false
	}

	var d planDialog
	type row struct {
		label  string
		styled bool
		input  bool
		cursor bool
	}
	var rows []row
	end := start
	for i := start; i < len(lines); i++ {
		t := lines[i].text
		if strings.TrimSpace(t) == "" {
			break
		}
		if m := planRowRE.FindStringSubmatch(t); m != nil {
			if m[2] != strconv.Itoa(len(rows)+1) {
				return planDialog{}, false
			}
			rows = append(rows, row{label: strings.TrimSpace(m[3]), styled: lines[i].styled, cursor: m[1] != ""})
			end = i
			continue
		}
		if len(rows) == 0 {
			return planDialog{}, false
		}
		last := &rows[len(rows)-1]
		if strings.Contains(strings.ToLower(t), planInputHint) {
			last.input = true
		} else {
			// A label the pane's width wrapped.
			last.label += " " + strings.TrimSpace(t)
			last.styled = last.styled && lines[i].styled
		}
		end = i
	}
	// Below the rows there is at most the dialog's own footer ("ctrl+g to edit
	// in …") — never the composer or more conversation, which would mean the
	// dialog is in scrollback.
	tail := 0
	for i := end + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i].text) != "" {
			tail++
		}
	}
	if tail > 2 {
		return planDialog{}, false
	}
	if len(rows) < 2 {
		return planDialog{}, false
	}
	d.Cursor = -1
	for i, r := range rows {
		if r.cursor {
			d.Cursor = i
		}
	}
	if d.Cursor < 0 {
		return planDialog{}, false
	}
	// Only the LAST row can be the field: it is where Claude Code draws it, and
	// a field anywhere else would make every index after it mean something else.
	for i, r := range rows {
		if r.input && i != len(rows)-1 {
			return planDialog{}, false
		}
	}
	opts := rows
	if last := rows[len(rows)-1]; last.input {
		d.HasInput = true
		if last.styled {
			d.Other = last.label
		}
		opts = rows[:len(rows)-1]
	}
	for _, r := range opts {
		d.Options = append(d.Options, collapseSpace(r.label))
	}
	if qEnd >= 0 {
		// The question may wrap: its paragraph is every non-blank line up to
		// the one that ends it.
		q := []string{strings.TrimSpace(lines[qEnd].text)}
		for i := qEnd - 1; i >= 0; i-- {
			t := strings.TrimSpace(lines[i].text)
			if t == "" || strings.Trim(t, "─━-═╌ ") == "" {
				break
			}
			q = append([]string{t}, q...)
		}
		d.Question = collapseSpace(strings.Join(q, " "))
	}
	return d, true
}

// ask is the dialog as the one-question ask a client renders. The field is
// the question's "Other" row under the dialog's own name for it.
func (d planDialog) ask() *chatAsk {
	q := chatAskQuestion{Question: d.Question, Other: d.Other}
	if q.Question == "" {
		q.Question = "Would you like to proceed?"
	}
	for _, o := range d.Options {
		q.Options = append(q.Options, chatAskOption{Label: o})
	}
	return &chatAsk{Questions: []chatAskQuestion{q}}
}

// attachPlanAsk offers a pending plan approval's choices. Only a running
// ExitPlanMode on the TAIL of the conversation gets one, and only while the
// pane's screen shows the dialog: a choice is exactly what that dialog shows,
// never a list lasso assumes.
func attachPlanAsk(be Backend, paneID string, items []chatItem) {
	var t *chatTool
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i].Tool; it != nil && isExitPlanMode(it.Name) && it.State == "running" {
			t = it
			break
		}
	}
	if t == nil {
		return
	}
	screen, ok := paneVisibleANSI(be, paneID)
	if !ok {
		return
	}
	if d, ok := parsePlanDialog(screen); ok {
		t.Ask = d.ask()
	}
}

// planAnswer answers the plan dialog on screen. Stricter than an ask's check,
// because the screen can be read exactly: the question (when it is on screen)
// and EVERY option label must be what the card showed, in order, and a typed
// answer needs the field still empty and named as the card named it. The walk
// starts from the cursor the screen shows, since a human may have moved it and
// the dialog wraps at both ends.
func planAnswer(be Backend, paneID string, d planDialog, expect string, labels []string, answers []chatAskPick) (outcome, detail string) {
	gone := "that plan approval is no longer on the pane's screen — answer it in the terminal"
	if len(answers) != 1 || answers[0].Multi {
		return "refused", "a plan approval takes one answer"
	}
	// The card may have been drawn while a short pane hid the question (it then
	// asks the generic "Would you like to proceed?"), so the screen's question
	// must CONTAIN what the card showed rather than equal it.
	if d.Question != "" && expect != "" && !strings.Contains(d.Question, collapseSpace(expect)) {
		return "refused", gone
	}
	if len(labels) != len(d.Options) {
		return "refused", gone
	}
	for i, l := range labels {
		if collapseSpace(l) != d.Options[i] {
			return "refused", gone
		}
	}
	a := answers[0]
	custom := strings.TrimSpace(a.Custom)
	target := -1
	switch {
	case custom != "":
		if !d.HasInput {
			return "refused", "this plan dialog has no field for changes — answer it in the terminal"
		}
		if d.Other == "" {
			return "refused", "the plan dialog's feedback field already has text in it — finish it in the terminal"
		}
		if a.Other != "" && collapseSpace(a.Other) != d.Other {
			return "refused", gone
		}
		target = len(d.Options)
	case len(a.Selected) == 1 && a.Selected[0] < len(d.Options):
		target = a.Selected[0]
	default:
		return "refused", "pick one of the plan dialog's options"
	}
	var k askKeys
	if n := target - d.Cursor; n > 0 {
		k.keys(askDown(n))
	} else if n < 0 {
		k.keys(strings.Repeat("\x1b[A", -n))
	}
	if custom != "" {
		k.text(custom)
	}
	k.keys("\r")
	return sendAskSegments(be, paneID, k.done())
}

// looksLikePlanQuestion says an answer was picked for a plan approval, which
// must then find one on screen: the ask check's footer and probes would accept
// a screen that merely mentions it.
func looksLikePlanQuestion(expect string) bool {
	return strings.Contains(strings.ToLower(expect), planQuestionMark)
}

// sgrStyled splits an ANSI line into its printed runes and whether each was
// drawn with a colour or dim set — anything but the terminal's plain default.
// Every other escape is dropped.
func sgrStyled(line string) (runes []rune, styled []bool) {
	fg, faint := false, false
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '\x1b' {
			runes = append(runes, rs[i])
			styled = append(styled, fg || faint)
			continue
		}
		if i+1 >= len(rs) || rs[i+1] != '[' {
			i++
			continue
		}
		j := i + 2
		for j < len(rs) && (rs[j] < 0x40 || rs[j] > 0x7e) {
			j++
		}
		if j < len(rs) && rs[j] == 'm' {
			params := strings.Split(string(rs[i+2:j]), ";")
			for p := 0; p < len(params); p++ {
				switch v := params[p]; {
				case v == "" || v == "0":
					fg, faint = false, false
				case v == "2":
					faint = true
				case v == "22":
					faint = false
				case v == "39":
					fg = false
				case v == "38":
					fg = true
					if p+1 < len(params) && params[p+1] == "5" {
						p += 2
					} else if p+1 < len(params) && params[p+1] == "2" {
						p += 4
					}
				case v == "48" || v == "58":
					if p+1 < len(params) && params[p+1] == "5" {
						p += 2
					} else if p+1 < len(params) && params[p+1] == "2" {
						p += 4
					}
				case len(v) == 2 && (v[0] == '3' || v[0] == '9') && v[1] >= '0' && v[1] <= '7':
					fg = true
				}
			}
		}
		i = j
	}
	return runes, styled
}

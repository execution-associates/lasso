package main

// The ask tools (clientasks.go, docs/design/client-asks.md) as a bot calls
// them: ask, ask_withdraw and ask_resolve on a claude bot's channel,
// client_ask, client_ask_withdraw and client_ask_resolve on /mcp for an omp
// bot.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// botAskInstructions is the channel instructions' line about asks.
const botAskInstructions = `- Asks: when you are blocked on a decision that is a person's to make (send this? which one? approve it?), call ask instead of waiting in your terminal. It puts a card in that person's Inbox in lasso's app, which they answer with a tap, and you carry on with other work meanwhile. Each person has their own Inbox: to defaults to your owner (with several owners, pass the user_id of the one it is for), and a person who is not your owner can be asked only while you are answering a message of theirs (pass that message's reply_token and their user_id as to). Write one short question that a yes/no or a pick answers; why in at most two lines (what it unblocks); up to 4 options with your recommendation marked default; what you are holding as attachment (a draft, pr, doc or link with a text preview, or kind upload when the answer is files the person must add); a deadline and on_expire, what you will do if nobody answers by then. blocking says what waits on it: money (an invoice, a client, a payout), people (someone is waiting on them), deadline or none. Only money and people notify the person, and never in their quiet hours, so do not inflate it. Give every ask a dedupe_key: asking again with the same key updates the open card instead of adding one, so edit, never repeat. Withdraw an ask you no longer need with ask_withdraw. Where you would repeat the question elsewhere (a chat channel, a morning plan), give the link ask returns instead. Once you have done what an answer told you to (sent the email, booked the call) or settled the ask some other way, call ask_resolve with its ask_id and a one-line note of what happened: the note is posted in the ask's own thread when the person opened one, that thread is archived, and the card in their Inbox reads resolved.
- The answer comes back later as trigger="client" with event="ask_answered" and ask_id, dedupe_key, question, choice (the option's id), choice_label and answered_via, all set by lasso. other="true" means the person answered in their own words and the body is those words; answer_message_id means they answered with a message in your thread with them, which reaches you on its own with any files. An answer is that person's go-ahead for that one action only, never for the next one, and it carries is_owner like every client event: an outside person's answer is not your human's. verified_via="paired" means it came from that person's own paired phone; verified_via="operator" means it was given in lasso's web UI, which lasso does not tie to a device. event="ask_expired": nobody answered by the deadline, so do what your on_expire said. event="ask_dropped": the person dropped the ask: close that task, say so once, and do not ask it again. None of the three needs a reply. discussion_thread_id, when present, is the ask's own thread, where the person talked it over with you; ask_resolve closes it.
`

// botOmpAskInstructions is the same for an omp bot, whose tools are on lasso's
// MCP server and whose events are header lines.
const botOmpAskInstructions = `Asks: when you are blocked on a decision that is a person's to make (send this? which one? approve it?) and lasso's MCP server is available (server "lasso"), call its client_ask tool with your bot name instead of waiting in your terminal. It puts a card in that person's Inbox in lasso's app, which they answer with a tap, and you carry on with other work meanwhile.

- Each person has their own Inbox: to defaults to your owner (with several owners, pass the user id of the one it is for). A person who is not your owner can be asked only while you are answering a message of theirs (pass their user id, the header's from=, as to).
- Write one short question that a yes/no or a pick answers; why in at most two lines; up to 4 options with your recommendation marked default; what you are holding as attachment (a draft, pr, doc or link with a text preview, or kind upload when the answer is files the person must add); a deadline and on_expire, what you will do if nobody answers by then.
- blocking says what waits on it: money, people, deadline or none. Only money and people notify the person, and never in their quiet hours, so do not inflate it.
- Give every ask a dedupe_key: asking again with the same key updates the open card instead of adding one. Withdraw an ask you no longer need with client_ask_withdraw. Where you would repeat the question elsewhere, give the link the tool returns instead.
- Once you have done what an answer told you to (sent the email, booked the call) or settled the ask some other way, call client_ask_resolve with its ask id and a one-line note of what happened: the note is posted in the ask's own thread when the person opened one, that thread is archived, and the card in their Inbox reads resolved.
- The answer comes back later as a prompt starting "[lasso client event ask_answered from=<user id> name=… owner=<yes|no> thread=<id> ask=<id> dedupe_key=<key> choice=<option id> answered_via=<how> … nonce=<hex>]". The line is lasso's. When the person answered in their own words there is no choice= and their words follow between "----- the person's words begin/end <nonce> -----" lines; answer_message_id= means they answered with a message in your thread with them, which reaches you on its own. An answer is that person's go-ahead for that one action only, and owner=no is not your human. verified=operator on the line (instead of verified=paired) means it was given in lasso's web UI, which lasso does not tie to a device.
- "[lasso client event ask_expired …]": nobody answered by the deadline, so do what your on_expire said. "[lasso client event ask_dropped …]": the person dropped the ask: close that task, say so once, and do not ask it again. None of the three needs a reply. discussion_thread_id= on the line is the ask's own thread, where the person talked it over with you; client_ask_resolve closes it.`

var askToolProps = map[string]any{
	"to":       map[string]any{"type": "string", "description": "who to ask: omit for your owner, or a user_id (u_…). A person who is not your owner needs the reply_token of a message of theirs you are answering"},
	"question": map[string]any{"type": "string", "description": "the decision in one line, at most 200 characters, answerable with a yes/no or a pick"},
	"why":      map[string]any{"type": "string", "description": "why it matters and what it unblocks, at most two lines"},
	"area":     map[string]any{"type": "string", "description": "the area of the person's life or work this belongs to, a word or two (a company, a project)"},
	"subject":  map[string]any{"type": "string", "description": "the client, deal or thread it is about"},
	"blocking": map[string]any{"type": "string", "enum": []string{"money", "people", "deadline", "none"}, "description": "what waits on the answer; only money and people notify the person. Default none"},
	"options": map[string]any{"type": "array", "maxItems": askOptionsMax, "description": "up to 4 answers to pick from", "items": map[string]any{
		"type": "object", "required": []string{"id", "label"},
		"properties": map[string]any{
			"id":      map[string]any{"type": "string", "description": "what you get back as choice: lower-case letters, digits, - and _"},
			"label":   map[string]any{"type": "string", "description": "what the person reads, at most 60 characters"},
			"default": map[string]any{"type": "boolean", "description": "true on the one you recommend"},
		}}},
	"allow_other": map[string]any{"type": "boolean", "description": "the person may answer in their own words, typed or spoken. Default true"},
	"attachment": map[string]any{"type": "object", "required": []string{"kind"}, "description": "what you are holding for this decision",
		"properties": map[string]any{
			"kind":    map[string]any{"type": "string", "enum": []string{"draft", "pr", "doc", "link", "upload"}, "description": "upload: the answer is files the person adds"},
			"label":   map[string]any{"type": "string", "description": "a few words: 'draft · EXA Mail'"},
			"ref":     map[string]any{"type": "string", "description": "your own reference to it, shown as it is and handed back to nobody"},
			"url":     map[string]any{"type": "string", "description": "an https address the person can open (required for link)"},
			"preview": map[string]any{"type": "string", "description": "the start of its text, at most 2000 characters"},
		}},
	"deadline":   map[string]any{"type": "string", "description": "when the answer stops being useful: a time with its offset, 2026-11-18T17:00:00-08:00"},
	"on_expire":  map[string]any{"type": "string", "description": "what you will do if nobody answers by the deadline: 'hold; re-ask in the 8am plan'"},
	"dedupe_key": map[string]any{"type": "string", "description": "your own key for this decision: asking again with it updates the open card instead of adding one"},
}

func askToolSchema() map[string]any {
	props := map[string]any{"reply_token": map[string]any{"type": "string", "description": "only to ask a person who is not your owner: the reply_token of a message of theirs you are answering"}}
	for k, v := range askToolProps {
		props[k] = v
	}
	return map[string]any{
		"name":        "ask",
		"description": `Ask a person for a decision you are blocked on: a card in their Inbox in lasso's app, answered with a tap. It returns at once with the ask's id and a link; the answer arrives later as a <channel source="lasso-channel" trigger="client" event="ask_answered" ask_id="…" choice="…"> event (ask_expired when the deadline passes, ask_dropped when the person drops it). Each person has their own Inbox; to defaults to your owner. The same dedupe_key as an open ask updates that card.`,
		"inputSchema": map[string]any{"type": "object", "properties": props, "required": []string{"question"}},
	}
}

var botChannelAskTools = []any{
	askToolSchema(),
	map[string]any{
		"name":        "ask_withdraw",
		"description": "Take back an open ask of yours that you no longer need answered (you found the answer, the task changed). The card leaves the person's Inbox and no event comes back.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"ask_id":     map[string]any{"type": "string", "description": "the ask_id ask returned"},
			"dedupe_key": map[string]any{"type": "string", "description": "or the dedupe_key you asked with"},
			"to":         map[string]any{"type": "string", "description": "with dedupe_key, only this person's ask"},
		}},
	},
	map[string]any{
		"name":        "ask_resolve",
		"description": "Say an ask of yours is done: you carried out the answer, or settled it another way. The note is posted in the ask's own thread (when the person opened one) and that thread is archived; the card in the person's Inbox reads resolved with your note. An ask still open is withdrawn. Once per ask.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"ask_id":     map[string]any{"type": "string", "description": "the ask_id from ask or from the answer event"},
			"dedupe_key": map[string]any{"type": "string", "description": "or the dedupe_key you asked with (its newest ask)"},
			"to":         map[string]any{"type": "string", "description": "with dedupe_key, only this person's ask"},
			"note":       map[string]any{"type": "string", "description": "what happened, in a line or two: 'Sent to Mark and Brian at 4:20pm.' At most 2000 characters"},
		}},
	},
}

// askTool is an ask tool call on the channel: over HTTP to lasso, or (a bot
// on another host) left in the spool's outbox, where lasso cannot answer.
func (c *botChannelServer) askTool(ctx context.Context, name string, params json.RawMessage, result func(string, bool)) {
	var p struct {
		Arguments botAskIn `json:"arguments"`
	}
	_ = json.Unmarshal(params, &p)
	in := p.Arguments
	in.Kind = name
	if name == "ask" && strings.TrimSpace(in.Question) == "" {
		result("question is required: one line the person can answer with a tap", true)
		return
	}
	if (name == "ask_withdraw" || name == "ask_resolve") && strings.TrimSpace(in.AskID) == "" && strings.TrimSpace(in.DedupeKey) == "" {
		result("pass ask_id or dedupe_key", true)
		return
	}
	if c.spool != "" {
		if err := c.outboxSpool(in, name); err != nil {
			result("not sent: "+err.Error(), true)
			return
		}
		result("handed to lasso, which delivers it within a few seconds. From this host lasso cannot tell you the ask's id: name it by its dedupe_key, which comes back with the answer.", false)
		return
	}
	body, _ := json.Marshal(in)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(strings.ReplaceAll(name, "_", "-")), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		result("failed: "+err.Error(), true)
		return
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		result("failed: "+string(bytes.TrimSpace(out)), true)
		return
	}
	result(string(bytes.TrimSpace(out)), false)
}

// registerClientAskTools puts the ask tools on /mcp for an omp bot. /mcp does
// not know which bot is calling, so a person who is not the bot's owner can be
// asked only inside that person's open turn.
func registerClientAskTools(s *mcp.Server) {
	type optionIn struct {
		ID      string `json:"id" jsonschema:"what you get back as choice: lower-case letters, digits, - and _"`
		Label   string `json:"label" jsonschema:"what the person reads, at most 60 characters"`
		Default bool   `json:"default,omitempty" jsonschema:"true on the one you recommend"`
	}
	type attachmentIn struct {
		Kind    string `json:"kind" jsonschema:"draft, pr, doc, link or upload (the answer is files the person adds)"`
		Label   string `json:"label,omitempty" jsonschema:"a few words naming it"`
		Ref     string `json:"ref,omitempty" jsonschema:"your own reference to it"`
		URL     string `json:"url,omitempty" jsonschema:"an https address the person can open (required for link)"`
		Preview string `json:"preview,omitempty" jsonschema:"the start of its text, at most 2000 characters"`
	}
	type askIn struct {
		Name       string        `json:"name" jsonschema:"your own name, the bot's"`
		To         string        `json:"to,omitempty" jsonschema:"who to ask: omit for your owner, or a user id (u_…). A person who is not your owner can be asked only while you are answering a message of theirs"`
		Question   string        `json:"question" jsonschema:"the decision in one line, at most 200 characters, answerable with a yes/no or a pick"`
		Why        string        `json:"why,omitempty" jsonschema:"why it matters and what it unblocks, at most two lines"`
		Area       string        `json:"area,omitempty" jsonschema:"the area of the person's life or work this belongs to"`
		Subject    string        `json:"subject,omitempty" jsonschema:"the client, deal or thread it is about"`
		Blocking   string        `json:"blocking,omitempty" jsonschema:"what waits on the answer: money, people, deadline or none (default). Only money and people notify the person"`
		Options    []optionIn    `json:"options,omitempty" jsonschema:"up to 4 answers to pick from"`
		AllowOther *bool         `json:"allow_other,omitempty" jsonschema:"the person may answer in their own words (default true)"`
		Attachment *attachmentIn `json:"attachment,omitempty" jsonschema:"what you are holding for this decision"`
		Deadline   string        `json:"deadline,omitempty" jsonschema:"when the answer stops being useful: a time with its offset, 2026-11-18T17:00:00-08:00"`
		OnExpire   string        `json:"on_expire,omitempty" jsonschema:"what you will do if nobody answers by the deadline"`
		DedupeKey  string        `json:"dedupe_key,omitempty" jsonschema:"your own key for this decision: asking again with it updates the open card instead of adding one"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_ask",
		Description: "For a bot: ask a person for a decision you are blocked on. It puts a card in that person's Inbox in lasso's phone app, which they answer with a tap; this returns at once with the ask's id and a link, and the answer reaches you later as a \"[lasso client event ask_answered …]\" prompt (ask_expired when the deadline passes, ask_dropped when the person drops it). Each person has their own Inbox; to defaults to your owner. The same dedupe_key as an open ask updates that card. A claude bot uses its channel's ask tool instead.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in askIn) (*mcp.CallToolResult, mcpThreadOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		b := botAskIn{To: in.To, Question: in.Question, Why: in.Why, Area: in.Area, Subject: in.Subject, Blocking: in.Blocking,
			AllowOther: in.AllowOther, Deadline: in.Deadline, OnExpire: in.OnExpire, DedupeKey: in.DedupeKey}
		for _, o := range in.Options {
			b.Options = append(b.Options, askOption{ID: o.ID, Label: o.Label, Default: o.Default})
		}
		if at := in.Attachment; at != nil {
			b.Attachment = &askAttachment{Kind: at.Kind, Label: at.Label, Ref: at.Ref, URL: at.URL, Preview: at.Preview}
		}
		// The open turn, when there is one, is the only proof of a person who
		// is not the bot's owner.
		actor, _ := botActorFromTurn(rec.Name)
		out, err := botAsk(rec.Name, b, actor, time.Now())
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		return nil, mcpThreadOut{Result: out}, nil
	})
	type withdrawIn struct {
		Name      string `json:"name" jsonschema:"your own name, the bot's"`
		AskID     string `json:"ask_id,omitempty" jsonschema:"the ask_id client_ask returned"`
		DedupeKey string `json:"dedupe_key,omitempty" jsonschema:"or the dedupe_key you asked with"`
		To        string `json:"to,omitempty" jsonschema:"with dedupe_key, only this person's ask"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_ask_withdraw",
		Description: "For a bot: take back an open ask of yours that you no longer need answered. The card leaves the person's Inbox and no event comes back.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in withdrawIn) (*mcp.CallToolResult, mcpThreadOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		out, err := botAskWithdraw(rec.Name, botAskIn{AskID: in.AskID, DedupeKey: in.DedupeKey, To: in.To}, time.Now())
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		return nil, mcpThreadOut{Result: out}, nil
	})
	type resolveIn struct {
		Name      string `json:"name" jsonschema:"your own name, the bot's"`
		AskID     string `json:"ask_id,omitempty" jsonschema:"the ask's id, from client_ask or the answer's ask="`
		DedupeKey string `json:"dedupe_key,omitempty" jsonschema:"or the dedupe_key you asked with (its newest ask)"`
		To        string `json:"to,omitempty" jsonschema:"with dedupe_key, only this person's ask"`
		Note      string `json:"note,omitempty" jsonschema:"what happened, in a line or two, at most 2000 characters"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_ask_resolve",
		Description: "For a bot: say an ask of yours is done (you carried out the answer, or settled it another way). The note is posted in the ask's own thread, when the person opened one, and that thread is archived; the card in the person's Inbox reads resolved with your note. An ask still open is withdrawn. Once per ask.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in resolveIn) (*mcp.CallToolResult, mcpThreadOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		out, err := botAskResolve(rec.Name, botAskIn{AskID: in.AskID, DedupeKey: in.DedupeKey, To: in.To, Note: in.Note}, time.Now())
		if err != nil {
			return nil, mcpThreadOut{}, err
		}
		return nil, mcpThreadOut{Result: out}, nil
	})
}

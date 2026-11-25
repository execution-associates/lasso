package main

// Asks (docs/design/client-asks.md): a bot blocked on a decision puts one
// question in a person's Inbox in lasso's phone app, and the person's answer
// comes back to the bot as a client event. An ask belongs to one person and
// one bot. A bot may ask its owner at any time; a person who is not its owner
// only while it is answering a message of theirs.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const clientAsksSchema = `
CREATE TABLE IF NOT EXISTS app_asks (
	seq            INTEGER PRIMARY KEY AUTOINCREMENT,
	id             TEXT NOT NULL UNIQUE,
	user_id        TEXT NOT NULL,
	bot            TEXT NOT NULL,
	thread_id      TEXT NOT NULL,
	dedupe_key     TEXT NOT NULL DEFAULT '',
	area           TEXT NOT NULL DEFAULT '',
	subject        TEXT NOT NULL DEFAULT '',
	blocking       TEXT NOT NULL DEFAULT 'none',
	question       TEXT NOT NULL,
	why            TEXT NOT NULL DEFAULT '',
	attachment     TEXT NOT NULL DEFAULT '',
	options        TEXT NOT NULL DEFAULT '[]',
	allow_other    INTEGER NOT NULL DEFAULT 1,
	deadline       TEXT NOT NULL DEFAULT '',
	on_expire      TEXT NOT NULL DEFAULT '',
	state          TEXT NOT NULL DEFAULT 'open', -- open | answered | dropped | expired | withdrawn
	seen_at        TEXT NOT NULL DEFAULT '',
	snoozed_until  TEXT NOT NULL DEFAULT '',
	choice         TEXT NOT NULL DEFAULT '',
	other          TEXT NOT NULL DEFAULT '',
	answer_message TEXT NOT NULL DEFAULT '',
	answered_via   TEXT NOT NULL DEFAULT '',
	revision       INTEGER NOT NULL DEFAULT 1,
	pushed_at      TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	closed_at      TEXT NOT NULL DEFAULT '',
	agent          TEXT NOT NULL DEFAULT '',
	untold         INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS app_asks_user ON app_asks(user_id, state);
CREATE INDEX IF NOT EXISTS app_asks_bot ON app_asks(bot, state);
CREATE UNIQUE INDEX IF NOT EXISTS app_asks_key ON app_asks(user_id, bot, dedupe_key) WHERE state = 'open' AND dedupe_key != '';
`

const (
	askQuestionMax    = 200
	askWhyMax         = 400
	askAreaMax        = 32
	askSubjectMax     = 64
	askOptionsMax     = 4
	askOptionLabelMax = 60
	askLabelMax       = 64
	askRefMax         = 200
	askURLMax         = 2000
	askPreviewMax     = 2000
	askOnExpireMax    = 300
	askOtherMax       = 4000
	askKeyMax         = 64
	// askOpenMax is how many open asks one bot may hold with one person.
	askOpenMax = 20
	// askPerHour bounds a bot's new asks, askUpdatesPerHour its re-asks of an
	// open one (the same dedupe_key).
	askPerHour        = 30
	askUpdatesPerHour = 120
	// askMaxAge closes an ask that has no deadline; askKeep is how long a
	// closed ask stays listed before it is deleted.
	askMaxAge    = 30 * 24 * time.Hour
	askKeep      = 30 * 24 * time.Hour
	askSnoozeMax = 30 * 24 * time.Hour
	// askPushLate is how long after an ask was made a push held back by quiet
	// hours is still sent.
	askPushLate = 24 * time.Hour
	// askUndoWindow is how long a dropped ask can be brought back: the bot is
	// told of the drop only once it has passed.
	askUndoWindow = time.Minute
	// askQuietDefault is a person's quiet hours until they choose their own.
	askQuietDefault = "22:00-08:00"
	askBodyMax      = 64 << 10
)

var (
	askOptionIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	askKeyRE      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	askViaRE      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
)

var askBlockingRank = map[string]int{"money": 0, "people": 1, "deadline": 2, "none": 3}

var askAttachmentKinds = map[string]bool{"draft": true, "pr": true, "doc": true, "link": true, "upload": true}

type askOption struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
	// Detail is a line under the label: what picking it means. Only an
	// agent's ask carries one (clientasks_agent.go).
	Detail string `json:"detail,omitempty"`
}

// askAttachment is what the bot is holding for the decision: a draft, a pull
// request, a document or a link, shown as a label and a text preview; or
// "upload", a slot the person fills with files of their own.
type askAttachment struct {
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Ref     string `json:"ref,omitempty"`
	URL     string `json:"url,omitempty"`
	Preview string `json:"preview,omitempty"`
}

type askAnswerView struct {
	Choice    string `json:"choice,omitempty"`
	Label     string `json:"label,omitempty"`
	Other     string `json:"other,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Via       string `json:"via,omitempty"`
	At        string `json:"at"`
}

// clientAskView is an ask as the person's app reads it.
type clientAskView struct {
	ID string `json:"id"`
	// Bot and ThreadID are empty on an agent's ask, which has Agent instead.
	Bot          string         `json:"bot"`
	ThreadID     string         `json:"thread_id"`
	Agent        *askAgentView  `json:"agent,omitempty"`
	Area         string         `json:"area,omitempty"`
	Subject      string         `json:"subject,omitempty"`
	Blocking     string         `json:"blocking"`
	Rank         int            `json:"rank"`
	Question     string         `json:"question"`
	Why          string         `json:"why,omitempty"`
	Attachment   *askAttachment `json:"attachment,omitempty"`
	Options      []askOption    `json:"options"`
	AllowOther   bool           `json:"allow_other"`
	Deadline     string         `json:"deadline,omitempty"`
	OnExpire     string         `json:"on_expire,omitempty"`
	State        string         `json:"state"` // new | seen | snoozed | answered | dropped | expired | withdrawn
	SnoozedUntil string         `json:"snoozed_until,omitempty"`
	Answer       *askAnswerView `json:"answer,omitempty"`
	Revision     int            `json:"revision"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	ClosedAt     string         `json:"closed_at,omitempty"`
	// UndoUntil is set on a dropped ask the bot has not been told about yet:
	// until then POST /asks/{id}/reopen brings it back.
	UndoUntil string `json:"undo_until,omitempty"`
}

type appAsk struct {
	Seq                                       int64
	ID, UserID, Bot, ThreadID, DedupeKey      string
	Area, Subject, Blocking, Question, Why    string
	Attachment                                *askAttachment
	Options                                   []askOption
	AllowOther                                bool
	Deadline, OnExpire                        string
	State, SeenAt, SnoozedUntil               string
	Choice, Other, AnswerMessage, AnsweredVia string
	Revision                                  int
	PushedAt, CreatedAt, UpdatedAt, ClosedAt  string
	// Agent is set on a coding agent's question (clientasks_agent.go): no bot
	// and no thread, and the answer is typed into the agent's pane.
	Agent *askAgent
	// Untold: dropped, and the bot not told yet (the undo window).
	Untold bool
}

const appAskCols = `seq, id, user_id, bot, thread_id, dedupe_key, area, subject, blocking, question, why, attachment, options, allow_other,
	deadline, on_expire, state, seen_at, snoozed_until, choice, other, answer_message, answered_via, revision, pushed_at, created_at, updated_at, closed_at, agent, untold`

func scanAppAsk(row interface{ Scan(...any) error }) (*appAsk, error) {
	var a appAsk
	var att, opts, agent string
	err := row.Scan(&a.Seq, &a.ID, &a.UserID, &a.Bot, &a.ThreadID, &a.DedupeKey, &a.Area, &a.Subject, &a.Blocking, &a.Question, &a.Why, &att, &opts, &a.AllowOther,
		&a.Deadline, &a.OnExpire, &a.State, &a.SeenAt, &a.SnoozedUntil, &a.Choice, &a.Other, &a.AnswerMessage, &a.AnsweredVia, &a.Revision, &a.PushedAt,
		&a.CreatedAt, &a.UpdatedAt, &a.ClosedAt, &agent, &a.Untold)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	if err != nil {
		return nil, err
	}
	if att != "" {
		var v askAttachment
		if json.Unmarshal([]byte(att), &v) == nil {
			a.Attachment = &v
		}
	}
	if agent != "" {
		var v askAgent
		if json.Unmarshal([]byte(agent), &v) == nil {
			a.Agent = &v
		}
	}
	_ = json.Unmarshal([]byte(opts), &a.Options)
	if a.Options == nil {
		a.Options = []askOption{}
	}
	return &a, nil
}

func askByID(id string) (*appAsk, error) {
	return scanAppAsk(db.QueryRow(`SELECT `+appAskCols+` FROM app_asks WHERE id = ?`, id))
}

// listAsks reads every matching ask before it answers, so a caller may query
// again for each (lasso.db is one connection).
func listAsks(where string, args ...any) []*appAsk {
	rows, err := db.Query(`SELECT `+appAskCols+` FROM app_asks a WHERE `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*appAsk
	for rows.Next() {
		if a, err := scanAppAsk(rows); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func (a *appAsk) snoozed(now time.Time) bool {
	return a.State == "open" && a.SnoozedUntil != "" && a.SnoozedUntil > nowStamp(now)
}

// waiting: open and on the person's list now.
func (a *appAsk) waiting(now time.Time) bool { return a.State == "open" && !a.snoozed(now) }

// rank is where the ask sorts: what blocks money first, then people, then
// what has a deadline (named so, or due within a day), then the rest.
func (a *appAsk) rank(now time.Time) int {
	r, ok := askBlockingRank[a.Blocking]
	if !ok {
		r = 3
	}
	if r == 3 && a.Deadline != "" && a.Deadline <= nowStamp(now.Add(24*time.Hour)) {
		r = 2
	}
	return r
}

func (a *appAsk) optionLabel(id string) string {
	for _, o := range a.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return ""
}

func (a *appAsk) view(now time.Time) clientAskView {
	v := clientAskView{ID: a.ID, Bot: a.Bot, ThreadID: a.ThreadID, Area: a.Area, Subject: a.Subject, Blocking: a.Blocking, Rank: a.rank(now),
		Question: a.Question, Why: a.Why, Attachment: a.Attachment, Options: a.Options, AllowOther: a.AllowOther, Deadline: a.Deadline, OnExpire: a.OnExpire,
		State: a.State, Revision: a.Revision, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt, ClosedAt: a.ClosedAt}
	if a.Agent != nil {
		v.Agent = a.Agent.view()
	}
	if a.State == "dropped" && a.Untold {
		if t, err := time.Parse(time.RFC3339, a.ClosedAt); err == nil {
			v.UndoUntil = nowStamp(t.Add(askUndoWindow))
		}
	}
	switch {
	case a.snoozed(now):
		v.State, v.SnoozedUntil = "snoozed", a.SnoozedUntil
	case a.State == "open" && a.SeenAt != "":
		v.State = "seen"
	case a.State == "open":
		v.State = "new"
	case a.State == "answered":
		v.Answer = &askAnswerView{Choice: a.Choice, Label: a.optionLabel(a.Choice), Other: a.Other, MessageID: a.AnswerMessage, Via: a.AnsweredVia, At: a.ClosedAt}
	}
	return v
}

// askVisibleSQL bounds asks (alias a) to those of bots the person still
// holds, and their agents' own.
const askVisibleSQL = `a.user_id = ? AND (a.agent != '' OR EXISTS (SELECT 1 FROM app_grants g WHERE g.user_id = a.user_id AND g.bot = a.bot))`

// clientAsksWaiting is how many asks wait on the person now: open, not
// snoozed, of a bot they still hold. quiet leaves out those whose bot's main
// thread is muted, as the badge does for messages.
func clientAsksWaiting(userID string, now time.Time, quiet bool) int {
	q := `SELECT COUNT(*) FROM app_asks a WHERE ` + askVisibleSQL + ` AND a.state = 'open' AND (a.snoozed_until = '' OR a.snoozed_until <= ?)`
	if quiet {
		q += ` AND NOT EXISTS (SELECT 1 FROM app_threads t WHERE t.id = a.thread_id AND t.muted = 1)`
	}
	var n int
	_ = db.QueryRow(q, userID, nowStamp(now)).Scan(&n)
	return n
}

// --- what a bot sends ----------------------------------------------------------------

// botAskIn is the ask tool's arguments: the body of the channel's HTTP call,
// an /mcp tool's input, or a spool outbox file with its kind set.
type botAskIn struct {
	Kind       string         `json:"kind,omitempty"` // ask | ask_withdraw (spool only)
	To         string         `json:"to,omitempty"`
	ReplyToken string         `json:"reply_token,omitempty"`
	AskID      string         `json:"ask_id,omitempty"`
	Question   string         `json:"question,omitempty"`
	Why        string         `json:"why,omitempty"`
	Area       string         `json:"area,omitempty"`
	Subject    string         `json:"subject,omitempty"`
	Blocking   string         `json:"blocking,omitempty"`
	Options    []askOption    `json:"options,omitempty"`
	AllowOther *bool          `json:"allow_other,omitempty"`
	Attachment *askAttachment `json:"attachment,omitempty"`
	Deadline   string         `json:"deadline,omitempty"`
	OnExpire   string         `json:"on_expire,omitempty"`
	DedupeKey  string         `json:"dedupe_key,omitempty"`
}

// askText cleans a field a person reads: one line when lines is false, no
// control or invisible formatting characters, bounded.
func askText(s string, max int, lines bool) string {
	var sb strings.Builder
	for _, r := range strings.ReplaceAll(s, "\r\n", "\n") {
		switch {
		case r == '\n' && lines:
		case r == '\t' && lines:
			r = ' '
		case clientHostileRune(r):
			r = ' '
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if !lines {
		out = strings.Join(strings.Fields(out), " ")
	}
	if r := []rune(out); len(r) > max {
		out = strings.TrimSpace(string(r[:max]))
	}
	return out
}

func tooLong(s string, max int) bool { return len([]rune(strings.TrimSpace(s))) > max }

// normalize checks an ask and fills a (the fields a bot sets), answering what
// to change in words the bot can act on.
func (in *botAskIn) normalize(a *appAsk, now time.Time) error {
	a.Question = askText(in.Question, askQuestionMax, false)
	if a.Question == "" {
		return errors.New("question is required: one line the person can answer with a tap")
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{{"question", in.Question, askQuestionMax}, {"why", in.Why, askWhyMax}, {"area", in.Area, askAreaMax}, {"subject", in.Subject, askSubjectMax},
		{"on_expire", in.OnExpire, askOnExpireMax}} {
		if tooLong(f.v, f.max) {
			return fmt.Errorf("%s is over %d characters; shorten it", f.name, f.max)
		}
	}
	a.Why = askText(in.Why, askWhyMax, true)
	a.Area = askText(in.Area, askAreaMax, false)
	a.Subject = askText(in.Subject, askSubjectMax, false)
	a.OnExpire = askText(in.OnExpire, askOnExpireMax, false)
	a.Blocking = strings.TrimSpace(in.Blocking)
	if a.Blocking == "" {
		a.Blocking = "none"
	}
	if _, ok := askBlockingRank[a.Blocking]; !ok {
		return errors.New("blocking must be money, people, deadline or none")
	}
	if len(in.Options) > askOptionsMax {
		return fmt.Errorf("at most %d options; the person can always answer in their own words when allow_other is true", askOptionsMax)
	}
	a.Options = []askOption{}
	seen := map[string]bool{}
	defaults := 0
	for _, o := range in.Options {
		o.ID = strings.TrimSpace(o.ID)
		if !askOptionIDRE.MatchString(o.ID) || seen[o.ID] {
			return errors.New("each option needs its own id: lower-case letters, digits, - and _, at most 32 (send, cc, hold)")
		}
		if tooLong(o.Label, askOptionLabelMax) {
			return fmt.Errorf("an option's label is over %d characters", askOptionLabelMax)
		}
		o.Label = askText(o.Label, askOptionLabelMax, false)
		if o.Label == "" {
			return errors.New("each option needs a label")
		}
		o.Detail = ""
		if o.Default {
			defaults++
		}
		seen[o.ID] = true
		a.Options = append(a.Options, o)
	}
	if defaults > 1 {
		return errors.New("mark at most one option default: the one you recommend")
	}
	a.AllowOther = in.AllowOther == nil || *in.AllowOther
	a.Attachment = nil
	if at := in.Attachment; at != nil {
		v := askAttachment{Kind: strings.TrimSpace(at.Kind), Label: askText(at.Label, askLabelMax, false), Ref: askText(at.Ref, askRefMax, false),
			URL: strings.TrimSpace(at.URL)}
		if !askAttachmentKinds[v.Kind] {
			return errors.New("attachment.kind must be draft, pr, doc, link or upload")
		}
		if tooLong(at.Label, askLabelMax) || tooLong(at.Ref, askRefMax) {
			return fmt.Errorf("attachment.label is at most %d characters and attachment.ref %d", askLabelMax, askRefMax)
		}
		if tooLong(at.Preview, askPreviewMax) {
			return fmt.Errorf("attachment.preview is over %d characters; send the start of it", askPreviewMax)
		}
		v.Preview = askText(at.Preview, askPreviewMax, true)
		if v.URL != "" {
			u, err := url.Parse(v.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || len(v.URL) > askURLMax {
				return errors.New("attachment.url must be an https address")
			}
		}
		if v.Kind == "link" && v.URL == "" {
			return errors.New("a link attachment needs attachment.url")
		}
		a.Attachment = &v
	}
	if len(a.Options) == 0 && !a.AllowOther && (a.Attachment == nil || a.Attachment.Kind != "upload") {
		return errors.New("the person has no way to answer: give options, or leave allow_other true")
	}
	a.Deadline = ""
	if d := strings.TrimSpace(in.Deadline); d != "" {
		t, err := time.Parse(time.RFC3339, d)
		if err != nil {
			return errors.New("deadline must be a time with its offset, like 2026-11-18T17:00:00-08:00")
		}
		if !t.After(now) {
			return errors.New("deadline is in the past")
		}
		if t.After(now.Add(askMaxAge)) {
			return fmt.Errorf("deadline is more than %d days away", int(askMaxAge.Hours()/24))
		}
		a.Deadline = nowStamp(t)
	}
	a.DedupeKey = strings.TrimSpace(in.DedupeKey)
	if a.DedupeKey != "" && !askKeyRE.MatchString(a.DedupeKey) {
		return fmt.Errorf("dedupe_key is 1-%d letters, digits, . _ : and -", askKeyMax)
	}
	return nil
}

// askOwners are the people who own the bot in lasso's app.
func askOwners(bot string) []*appUser {
	rows, err := db.Query(`SELECT `+prefixCols("u.", appUserCols)+` FROM app_users u JOIN app_grants g ON g.user_id = u.id
		WHERE g.bot = ? AND g.owner = 1 AND u.disabled = 0 AND u.review = 0 ORDER BY g.created_at, u.id`, bot)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*appUser
	for rows.Next() {
		if u, err := scanAppUser(rows); err == nil {
			out = append(out, u)
		}
	}
	return out
}

// askAddressee is who an ask goes to. "owner" (or nothing) is the bot's
// owner, when it has exactly one. A user id names a person the bot is granted
// to: an owner at any time, anyone else only when actor proves the bot is
// answering a message of theirs (a claude bot's open reply token, an omp
// bot's open turn), so a bot cannot start on a person who has not written.
func askAddressee(bot, to string, actor *botActor) (*appUser, error) {
	to = strings.TrimSpace(to)
	owners := askOwners(bot)
	if to == "" || to == "owner" {
		switch len(owners) {
		case 0:
			return nil, errors.New("you have no owner in lasso's app yet: your human makes themselves your owner in the Bots view's People tab and pairs their phone")
		case 1:
			return owners[0], nil
		}
		var names []string
		for _, o := range owners {
			names = append(names, fmt.Sprintf("%s (%s)", o.ID, clientMetaClean(o.Name, clientNameMax)))
		}
		return nil, errors.New("you have several owners; pass to with the user id of the one this is for: " + strings.Join(names, ", "))
	}
	for _, o := range owners {
		if o.ID == to {
			return o, nil
		}
	}
	if actor != nil && actor.Open && !actor.FromBot && actor.UserID == to && clientPersonReaches(to, bot) {
		if u, err := getAppUser(to); err == nil && !u.Review {
			return u, nil
		}
	}
	return nil, errors.New("you can ask your owner at any time; anyone else only while you are answering a message of theirs (pass that message's reply_token, and their user_id as to)")
}

// botAsk is the ask tool: a new ask for the person, or (the same dedupe_key
// as an open one) that ask brought up to date.
func botAsk(bot string, in botAskIn, actor *botActor, now time.Time) (map[string]any, error) {
	if isClientDemoBot(bot) {
		return nil, errors.New("no such bot")
	}
	a := &appAsk{Bot: bot}
	if err := in.normalize(a, now); err != nil {
		return nil, err
	}
	u, err := askAddressee(bot, in.To, actor)
	if err != nil {
		return nil, err
	}
	conv, err := convFor(u.ID, bot, now)
	if err != nil {
		return nil, err
	}
	a.UserID, a.ThreadID = u.ID, conv.ID
	att, opts := "", string(mustJSON(a.Options))
	if a.Attachment != nil {
		att = string(mustJSON(a.Attachment))
	}
	out := func(a *appAsk, state string) map[string]any {
		return map[string]any{"ask_id": a.ID, "state": state, "to": map[string]any{"user_id": u.ID, "name": clientMetaClean(u.Name, clientNameMax)},
			"link": "lasso://ask/" + a.ID}
	}
	if a.DedupeKey != "" {
		if old, err := scanAppAsk(db.QueryRow(`SELECT `+appAskCols+` FROM app_asks WHERE user_id = ? AND bot = ? AND dedupe_key = ? AND state = 'open'`,
			u.ID, bot, a.DedupeKey)); err == nil {
			if !clientLimits.allow("ask-update:"+bot, askUpdatesPerHour, time.Hour, now) {
				return nil, errors.New("too many updates to open asks this hour; the card already shows your last one")
			}
			// What the person is asked changed: it reads as new again.
			seen := old.SeenAt
			if old.Question != a.Question || string(mustJSON(old.Options)) != opts {
				seen = ""
			}
			if _, err := db.Exec(`UPDATE app_asks SET area = ?, subject = ?, blocking = ?, question = ?, why = ?, attachment = ?, options = ?, allow_other = ?,
				deadline = ?, on_expire = ?, seen_at = ?, revision = revision + 1, updated_at = ? WHERE id = ? AND state = 'open'`,
				a.Area, a.Subject, a.Blocking, a.Question, a.Why, att, opts, boolInt(a.AllowOther), a.Deadline, a.OnExpire, seen, nowStamp(now), old.ID); err != nil {
				return nil, err
			}
			cur, err := askByID(old.ID)
			if err != nil {
				return nil, err
			}
			askAnnounce(cur, now)
			askPushMaybe(cur, now)
			return out(cur, "updated"), nil
		}
	}
	var open int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_asks WHERE user_id = ? AND bot = ? AND state = 'open'`, u.ID, bot).Scan(&open)
	if open >= askOpenMax {
		return nil, fmt.Errorf("you already have %d open asks with this person: withdraw the ones you no longer need (ask_withdraw) before adding another", open)
	}
	if !clientLimits.allow("ask:"+bot, askPerHour, time.Hour, now) {
		return nil, fmt.Errorf("at most %d new asks an hour; batch what is left into one question, or wait", askPerHour)
	}
	a.ID = clientID("ask_", 12)
	stamp := nowStamp(now)
	if _, err := db.Exec(`INSERT INTO app_asks (id, user_id, bot, thread_id, dedupe_key, area, subject, blocking, question, why, attachment, options, allow_other,
		deadline, on_expire, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.Bot, a.ThreadID, a.DedupeKey, a.Area, a.Subject, a.Blocking, a.Question, a.Why, att, opts, boolInt(a.AllowOther),
		a.Deadline, a.OnExpire, stamp, stamp); err != nil {
		// The same key raced in: that ask stands.
		if old, e := scanAppAsk(db.QueryRow(`SELECT `+appAskCols+` FROM app_asks WHERE user_id = ? AND bot = ? AND dedupe_key = ? AND state = 'open'`,
			u.ID, bot, a.DedupeKey)); e == nil && a.DedupeKey != "" {
			return out(old, "updated"), nil
		}
		return nil, err
	}
	cur, err := askByID(a.ID)
	if err != nil {
		return nil, err
	}
	askAnnounce(cur, now)
	askPushMaybe(cur, now)
	return out(cur, "created"), nil
}

// botAskWithdraw takes back the bot's own open asks: the one named by id, or
// those with the dedupe_key (of one person when to names them).
func botAskWithdraw(bot string, in botAskIn, now time.Time) (map[string]any, error) {
	id, key := strings.TrimSpace(in.AskID), strings.TrimSpace(in.DedupeKey)
	var asks []*appAsk
	switch {
	case id != "":
		asks = listAsks(`a.bot = ? AND a.id = ? AND a.state = 'open'`, bot, id)
	case key != "":
		q, args := `a.bot = ? AND a.dedupe_key = ? AND a.state = 'open'`, []any{bot, key}
		if to := strings.TrimSpace(in.To); to != "" && to != "owner" {
			q += ` AND a.user_id = ?`
			args = append(args, to)
		}
		asks = listAsks(q, args...)
	default:
		return nil, errors.New("pass ask_id or dedupe_key")
	}
	n := 0
	for _, a := range asks {
		if _, ok := askClose(a, "withdrawn", "", now); ok {
			n++
		}
	}
	if n == 0 {
		return nil, errors.New("no open ask of yours matches: it was already answered, dropped, expired or withdrawn")
	}
	return map[string]any{"withdrawn": n}, nil
}

// askAnnounce tells the person's streams about an ask as it now is: one
// event, "ask", for a new one and for every change, which a client applies by
// id.
func askAnnounce(a *appAsk, now time.Time) {
	if a.Agent != nil {
		// No thread: the person's stream alone hears it.
		appendClientEventFor("", a.UserID, "ask", map[string]any{"ask": a.view(now)}, now)
		return
	}
	appendClientEvent(a.ThreadID, "ask", map[string]any{"ask": a.view(now)}, now)
}

// askClose ends an open ask (dropped, expired or withdrawn), once even when
// raced: the person's streams hear it and the badge goes down. It answers the
// ask as it now reads.
func askClose(a *appAsk, state, via string, now time.Time) (*appAsk, bool) {
	_, before := clientUnreadBadge(a.UserID)
	res, err := db.Exec(`UPDATE app_asks SET state = ?, answered_via = ?, closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`,
		state, via, nowStamp(now), nowStamp(now), a.ID)
	if err != nil {
		return a, false
	}
	cur, err := askByID(a.ID)
	if err != nil {
		return a, false
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return cur, false
	}
	askAnnounce(cur, now)
	clientBadgeMaybeDown(cur.UserID, before, now)
	return cur, true
}

// --- what the bot is told ------------------------------------------------------------

const (
	askToldChoice  = "This is that person's go-ahead for this one action only. No reply is needed."
	askToldExpired = "Nobody answered before the deadline. Do what your on_expire said, if anything. No reply is needed."
	askToldDropped = "The person dropped this ask: close the task, say so once, and do not ask it again. No reply is needed."
)

// askTellBot queues what became of an ask for the bot that asked: a client
// event carrying lasso's own facts as attributes. The body is the person's
// own words when they answered in them (other="true"), and otherwise one
// sentence of lasso's. dev is the device that answered, so a device revoked
// before the bot takes the event withdraws the answer with it.
func askTellBot(a *appAsk, event string, dev *appDevice, now time.Time) {
	if a.Agent != nil {
		askTellAgent(a, event, now)
		return
	}
	u, err := getAppUser(a.UserID)
	if err != nil {
		return
	}
	g, err := getAppGrant(a.UserID, a.Bot)
	if err != nil {
		return
	}
	meta := map[string]string{
		"event": event, "user_id": u.ID, "user_name": clientMetaClean(u.Name, clientNameMax),
		"verified": "true", "verified_via": "paired", "is_owner": fmt.Sprint(g.Owner),
		"conversation_id": a.ThreadID, "thread_id": a.ThreadID, "mode": "text", "nonce": randHex(8),
		"ask_id": a.ID, "question": clientMetaClean(a.Question, askQuestionMax),
	}
	if a.DedupeKey != "" {
		meta["dedupe_key"] = a.DedupeKey
	}
	body := ""
	switch event {
	case "ask_answered":
		body = askToldChoice
		meta["answered_via"], meta["answered_at"] = a.AnsweredVia, a.ClosedAt
		if a.Choice != "" {
			meta["choice"], meta["choice_label"] = a.Choice, clientMetaClean(a.optionLabel(a.Choice), askOptionLabelMax)
		}
		if a.AnswerMessage != "" {
			meta["answer_message_id"] = a.AnswerMessage
			body = "The person answered with a message in your thread with them (answer_message_id), which reaches you on its own. No reply to this event is needed."
		}
		if a.Other != "" {
			meta["other"] = "true"
			body = a.Other
		}
		switch {
		case dev != nil && dev.ID != "":
			meta["device_id"], meta["paired_by"] = dev.ID, clientMetaClean(dev.PairedBy, 64)
		case dev != nil:
			// Answered in lasso's own UI (clientasks_operator.go): no paired
			// device proved it, and every process on lasso's machine can
			// reach that UI's routes. The bot is told so.
			meta["verified_via"] = "operator"
		}
	case "ask_expired":
		body = askToldExpired
		if a.OnExpire != "" {
			meta["on_expire"] = clientMetaClean(a.OnExpire, askOnExpireMax)
		}
	case "ask_dropped":
		body = askToldDropped
	}
	_, _ = enqueueBotClientEvent(a.Bot, clientNeutralize(body), meta, now)
}

// askEventForOmp is an ask's outcome as the prompt an omp bot receives: lasso's
// line, then lasso's sentence, or the person's own words between nonce markers.
func askEventForOmp(m map[string]string, owner, thread, content string) string {
	var h strings.Builder
	via := "paired"
	if m["verified_via"] == "operator" {
		via = "operator"
	}
	fmt.Fprintf(&h, "[lasso client event %s from=%s name=%q verified=%s owner=%s thread=%s ask=%s", m["event"], m["user_id"], m["user_name"], via, owner, thread, m["ask_id"])
	for _, k := range []string{"dedupe_key", "choice", "answered_via", "answered_at", "answer_message_id"} {
		if m[k] != "" {
			fmt.Fprintf(&h, " %s=%s", k, m[k])
		}
	}
	for _, k := range []string{"choice_label", "question", "on_expire"} {
		if m[k] != "" {
			fmt.Fprintf(&h, " %s=%q", k, m[k])
		}
	}
	fmt.Fprintf(&h, " nonce=%s]\n", m["nonce"])
	if m["other"] == "true" {
		fmt.Fprintf(&h, "----- the person's words begin %[1]s -----\n%[2]s\n----- the person's words end %[1]s -----", m["nonce"], clientNeutralize(content))
		return h.String()
	}
	return h.String() + content
}

// --- the person's routes -------------------------------------------------------------

// clientOwnAsk is an ask of the token's user, of a bot they still hold;
// unknown, someone else's and ungranted read the same.
func clientOwnAsk(id *clientIdentity, askID string) (*appAsk, bool) {
	a, err := askByID(askID)
	if err != nil || a.UserID != id.User.ID {
		return nil, false
	}
	if a.Agent != nil {
		return a, true
	}
	if _, _, ok := grantedBotOf(id.User.ID, a.Bot); !ok {
		return nil, false
	}
	return a, true
}

// serveClientAskRoutes answers /asks and /asks/{id}[/answer|seen|snooze|drop];
// false when the path is none of them.
func serveClientAskRoutes(w http.ResponseWriter, r *http.Request, id *clientIdentity, segs []string) bool {
	if len(segs) == 0 || segs[0] != "asks" || len(segs) > 3 {
		return false
	}
	now := time.Now()
	if len(segs) == 1 {
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		serveClientAskList(w, r, id, now)
		return true
	}
	a, ok := clientOwnAsk(id, segs[1])
	if !ok {
		clientNotFound(w)
		return true
	}
	// A deadline that passed closes the ask here too, so an answer cannot
	// land between the deadline and the runner's next look.
	if a.State == "open" && askDue(a, now) {
		a = askExpire(a, now)
	}
	if len(segs) == 2 {
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
		return true
	}
	if r.Method != http.MethodPost {
		switch segs[2] {
		case "answer", "seen", "snooze", "drop":
			clientMethod(w, "POST")
		default:
			clientNotFound(w)
		}
		return true
	}
	if !clientLimits.allow("ask-act:"+id.User.ID, 120, time.Minute, now) {
		w.Header().Set("Retry-After", "10")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Too many requests. Try again in a moment.")
		return true
	}
	closed := func() {
		clientError(w, http.StatusConflict, "ask_closed", "This ask is no longer open.")
	}
	switch segs[2] {
	case "answer":
		serveClientAskAnswer(w, r, id, a, now)
	case "seen":
		if a.State == "open" && a.SeenAt == "" {
			_, _ = db.Exec(`UPDATE app_asks SET seen_at = ?, updated_at = ? WHERE id = ? AND state = 'open' AND seen_at = ''`, nowStamp(now), nowStamp(now), a.ID)
			if a, _ = askByID(a.ID); a == nil {
				clientNotFound(w)
				return true
			}
			askAnnounce(a, now)
		}
		clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
	case "snooze":
		var in struct {
			Until *string `json:"until"`
		}
		if !clientDecode(w, r, 4<<10, &in) {
			return true
		}
		if a.State != "open" {
			closed()
			return true
		}
		until := ""
		if in.Until != nil {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.Until))
			if err != nil || !t.After(now) || t.After(now.Add(askSnoozeMax)) {
				clientError(w, http.StatusBadRequest, "invalid_request", "until must be a time in the next 30 days, or null to bring the ask back now.")
				return true
			}
			until = nowStamp(t)
		}
		_, before := clientUnreadBadge(id.User.ID)
		seen := a.SeenAt
		if seen == "" {
			seen = nowStamp(now)
		}
		_, _ = db.Exec(`UPDATE app_asks SET snoozed_until = ?, seen_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`, until, seen, nowStamp(now), a.ID)
		if a, _ = askByID(a.ID); a == nil {
			clientNotFound(w)
			return true
		}
		askAnnounce(a, now)
		clientBadgeMaybeDown(id.User.ID, before, now)
		clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
	case "drop":
		if a.State == "dropped" {
			clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
			return true
		}
		// The bot hears of it once the undo window has passed (clientAsksTick).
		_, _ = db.Exec(`UPDATE app_asks SET untold = 1 WHERE id = ? AND state = 'open'`, a.ID)
		cur, ok := askClose(a, "dropped", askVia(id, ""), now)
		if !ok {
			_, _ = db.Exec(`UPDATE app_asks SET untold = 0 WHERE id = ? AND state != 'dropped'`, a.ID)
			if cur.State == "dropped" {
				clientJSON(w, http.StatusOK, map[string]any{"ask": cur.view(now)})
				return true
			}
			closed()
			return true
		}
		clientJSON(w, http.StatusOK, map[string]any{"ask": cur.view(now)})
	case "reopen":
		// Undo a drop the bot has not heard of. One UPDATE decides it against
		// the tick that tells the bot.
		if a.State == "open" {
			clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
			return true
		}
		res, err := db.Exec(`UPDATE app_asks SET state = 'open', untold = 0, closed_at = '', answered_via = '', updated_at = ? WHERE id = ? AND state = 'dropped' AND untold = 1`,
			nowStamp(now), a.ID)
		if n, _ := res.RowsAffected(); err != nil || n != 1 {
			clientError(w, http.StatusConflict, "ask_closed", "It is too late to bring this ask back: the bot has been told.")
			return true
		}
		if a, _ = askByID(a.ID); a == nil {
			clientNotFound(w)
			return true
		}
		askAnnounce(a, now)
		clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
	default:
		clientNotFound(w)
	}
	return true
}

// askVia is how an answer was given, as the bot reads it: the device's
// platform, which lasso knows, and the gesture, which the app names.
func askVia(id *clientIdentity, via string) string {
	p := id.Device.Platform
	if p == "" {
		p = "other"
	}
	if via = strings.TrimSpace(via); askViaRE.MatchString(via) {
		return p + "-" + via
	}
	return p
}

// serveClientAskList is GET /asks?state=waiting|all: the person's asks, what
// is blocked on them first.
func serveClientAskList(w http.ResponseWriter, r *http.Request, id *clientIdentity, now time.Time) {
	state := r.URL.Query().Get("state")
	if state != "" && state != "waiting" && state != "all" {
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be waiting or all.")
		return
	}
	cursor := newestUserEventSeq(id.User.ID)
	asks := listAsks(askVisibleSQL+` ORDER BY a.seq`, id.User.ID)
	for i, a := range asks {
		if a.State == "open" && askDue(a, now) {
			asks[i] = askExpire(a, now)
		}
	}
	waiting, snoozed := 0, 0
	out := []clientAskView{}
	group := func(a *appAsk) int {
		switch {
		case a.waiting(now):
			return 0
		case a.State == "open":
			return 1
		}
		return 2
	}
	sort.SliceStable(asks, func(i, j int) bool {
		a, b := asks[i], asks[j]
		if ga, gb := group(a), group(b); ga != gb {
			return ga < gb
		}
		switch group(a) {
		case 0:
			if ra, rb := a.rank(now), b.rank(now); ra != rb {
				return ra < rb
			}
			return a.Seq < b.Seq // oldest first
		case 1:
			return a.SnoozedUntil < b.SnoozedUntil
		}
		return a.ClosedAt > b.ClosedAt
	})
	for _, a := range asks {
		switch group(a) {
		case 0:
			waiting++
		case 1:
			snoozed++
		}
		if state == "all" || group(a) == 0 {
			if len(out) < 200 {
				out = append(out, a.view(now))
			}
		}
	}
	clientJSON(w, http.StatusOK, map[string]any{"asks": out, "counts": map[string]int{"waiting": waiting, "snoozed": snoozed},
		"events_cursor": fmt.Sprint(cursor)})
}

// serveClientAskAnswer is POST /asks/{id}/answer {choice?, other?,
// message_id?, via?}: one of an option's id, the person's own words, or a
// message they sent in the ask's thread.
func serveClientAskAnswer(w http.ResponseWriter, r *http.Request, id *clientIdentity, a *appAsk, now time.Time) {
	var in struct {
		Choice    string `json:"choice"`
		Other     string `json:"other"`
		MessageID string `json:"message_id"`
		Via       string `json:"via"`
	}
	if !clientDecode(w, r, askBodyMax, &in) {
		return
	}
	bad := func(msg string) { clientError(w, http.StatusBadRequest, "invalid_request", msg) }
	choice, msgID := strings.TrimSpace(in.Choice), strings.TrimSpace(in.MessageID)
	if tooLong(in.Other, askOtherMax) {
		bad(fmt.Sprintf("other is at most %d characters.", askOtherMax))
		return
	}
	other := askText(in.Other, askOtherMax, true)
	given := 0
	for _, v := range []string{choice, other, msgID} {
		if v != "" {
			given++
		}
	}
	if given != 1 {
		bad("Pass exactly one of choice, other and message_id.")
		return
	}
	if a.State != "open" {
		// The same answer again (a retry after a dropped connection) reads as
		// it did the first time.
		if a.State == "answered" && a.Choice == choice && a.Other == other && a.AnswerMessage == msgID {
			clientJSON(w, http.StatusOK, map[string]any{"ask": a.view(now)})
			return
		}
		clientError(w, http.StatusConflict, "ask_closed", "This ask is no longer open.")
		return
	}
	upload := a.Attachment != nil && a.Attachment.Kind == "upload"
	switch {
	case choice != "":
		if a.optionLabel(choice) == "" {
			bad("choice must be the id of one of the ask's options.")
			return
		}
	case other != "":
		if !a.AllowOther {
			bad("This ask takes one of its options only.")
			return
		}
	default:
		if !a.AllowOther && !upload {
			bad("This ask takes one of its options only.")
			return
		}
		// A message of the person's own, in the ask's thread, sent after the
		// ask was made: the bot receives it as any message.
		var role, at string
		if db.QueryRow(`SELECT role, created_at FROM app_messages WHERE id = ? AND conv_id = ?`, msgID, a.ThreadID).Scan(&role, &at) != nil ||
			role != "user" || at < a.CreatedAt {
			bad("message_id must be a message you sent in this ask's thread after it was asked.")
			return
		}
	}
	_, before := clientUnreadBadge(id.User.ID)
	via := askVia(id, in.Via)
	res, err := db.Exec(`UPDATE app_asks SET state = 'answered', choice = ?, other = ?, answer_message = ?, answered_via = ?, seen_at = CASE WHEN seen_at = '' THEN ? ELSE seen_at END,
		closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`, choice, other, msgID, via, nowStamp(now), nowStamp(now), nowStamp(now), a.ID)
	if err != nil {
		clientFail(w, err)
		return
	}
	cur, err := askByID(a.ID)
	if err != nil {
		clientNotFound(w)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// Another device answered first.
		if cur.State == "answered" && cur.Choice == choice && cur.Other == other && cur.AnswerMessage == msgID {
			clientJSON(w, http.StatusOK, map[string]any{"ask": cur.view(now)})
			return
		}
		clientError(w, http.StatusConflict, "ask_closed", "This ask is no longer open.")
		return
	}
	askTellBot(cur, "ask_answered", &id.Device, now)
	if cur.Agent != nil {
		// Typing the answer into the pane records how that went.
		if again, err := askByID(cur.ID); err == nil {
			cur = again
		}
	}
	askAnnounce(cur, now)
	clientBadgeMaybeDown(id.User.ID, before, now)
	clientJSON(w, http.StatusOK, map[string]any{"ask": cur.view(now)})
}

// --- deadlines, pushes, upkeep -------------------------------------------------------

// askDue: the ask's deadline has passed, or it has none and is too old.
func askDue(a *appAsk, now time.Time) bool {
	if a.Deadline != "" {
		return a.Deadline <= nowStamp(now)
	}
	return a.CreatedAt <= nowStamp(now.Add(-askMaxAge))
}

// askExpire closes an ask nobody answered in time and tells its bot, which
// runs its own fallback.
func askExpire(a *appAsk, now time.Time) *appAsk {
	cur, ok := askClose(a, "expired", "", now)
	if ok {
		askTellBot(cur, "ask_expired", nil, now)
	}
	return cur
}

// clientAsksTick is the runner's look at asks: deadlines that passed, pushes
// that quiet hours held back, and asks whose snooze just ended (announced, so
// an open app lists them again).
func clientAsksTick(now time.Time) {
	stamp := nowStamp(now)
	for _, a := range listAsks(`a.state = 'open' AND ((a.deadline != '' AND a.deadline <= ?) OR (a.deadline = '' AND a.created_at <= ?))`,
		stamp, nowStamp(now.Add(-askMaxAge))) {
		askExpire(a, now)
	}
	// A drop past its undo window is final: the bot is told, once.
	for _, a := range listAsks(`a.state = 'dropped' AND a.untold = 1 AND a.closed_at <= ?`, nowStamp(now.Add(-askUndoWindow))) {
		res, err := db.Exec(`UPDATE app_asks SET untold = 0 WHERE id = ? AND state = 'dropped' AND untold = 1`, a.ID)
		if n, _ := res.RowsAffected(); err == nil && n == 1 {
			askTellBot(a, "ask_dropped", nil, now)
		}
	}
	for _, a := range listAsks(`a.state = 'open' AND a.snoozed_until != '' AND a.snoozed_until <= ?`, stamp) {
		_, _ = db.Exec(`UPDATE app_asks SET snoozed_until = '', updated_at = ? WHERE id = ? AND snoozed_until = ?`, stamp, a.ID, a.SnoozedUntil)
		if cur, err := askByID(a.ID); err == nil {
			askAnnounce(cur, now)
			askPushMaybe(cur, now)
		}
	}
	for _, a := range listAsks(`a.state = 'open' AND a.pushed_at = '' AND a.blocking IN ('money', 'people') AND a.created_at > ?`, nowStamp(now.Add(-askPushLate))) {
		askPushMaybe(a, now)
	}
}

// clientAsksMaintain forgets closed asks past their keep.
func clientAsksMaintain(now time.Time) {
	_, _ = db.Exec(`DELETE FROM app_asks WHERE state != 'open' AND closed_at < ?`, nowStamp(now.Add(-askKeep)))
}

// clientWithdrawAsks closes the open asks matching where without telling the
// bot: the grant they were asked under is gone.
func clientWithdrawAsks(where string, args ...any) {
	now := time.Now()
	for _, a := range listAsks(`a.state = 'open' AND `+where, args...) {
		_, _ = db.Exec(`UPDATE app_asks SET state = 'withdrawn', closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`, nowStamp(now), nowStamp(now), a.ID)
	}
}

// askQuietHours reads a person's quiet hours: the minutes of the day they
// start and end, in the person's zone; off when they turned them off.
func askQuietHours(userID string) (start, end int, loc *time.Location, off bool) {
	var tz, quiet string
	_ = db.QueryRow(`SELECT time_zone, ask_quiet FROM app_users WHERE id = ?`, userID).Scan(&tz, &quiet)
	loc = time.Local
	if tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	if quiet == "off" {
		return 0, 0, loc, true
	}
	if quiet == "" {
		quiet = askQuietDefault
	}
	a, b, _ := strings.Cut(quiet, "-")
	start, ok1 := parseClock(a)
	end, ok2 := parseClock(b)
	if !ok1 || !ok2 {
		start, _ = parseClock("22:00")
		end, _ = parseClock("08:00")
	}
	return start, end, loc, false
}

// parseClock reads "HH:MM" as minutes of the day.
func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil || len(strings.TrimSpace(s)) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// askQuietNow: the person's quiet hours cover now, so no ask interrupts them.
func askQuietNow(userID string, now time.Time) bool {
	start, end, loc, off := askQuietHours(userID)
	if off || start == end {
		return false
	}
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// askPushMaybe sends the one push an ask gets: only for what blocks money or
// people, never in the person's quiet hours (the runner sends it when they
// end), never for a snoozed ask or a muted bot.
func askPushMaybe(a *appAsk, now time.Time) {
	if a.State != "open" || a.PushedAt != "" || (a.Blocking != "money" && a.Blocking != "people") || a.snoozed(now) {
		return
	}
	if (!pushConfigured("apns") && !pushConfigured("fcm")) || askQuietNow(a.UserID, now) {
		return
	}
	t, err := threadByID(a.ThreadID)
	if err != nil || t.Muted {
		return
	}
	res, err := db.Exec(`UPDATE app_asks SET pushed_at = ? WHERE id = ? AND pushed_at = ''`, nowStamp(now), a.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return
	}
	n := pushNote{Kind: "ask", ThreadID: t.ID, AskID: a.ID, Bot: a.Bot, Title: a.Bot, Body: clientPreview(a.Question, clientPushPreviewMax)}
	if clientPushPreviews(a.UserID) {
		n.Subtitle = strings.Join(nonEmpty(a.Area, a.Subject), " · ")
	}
	clientPushNote(t, n, "Needs your answer", now)
}

func nonEmpty(vs ...string) []string {
	var out []string
	for _, v := range vs {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// clientAskSettingsView is the person's time zone and quiet hours as
// /me's settings read them: null for "lasso's own zone" and for "off".
func clientAskSettingsView(userID string) (tz any, quiet any) {
	var z, q string
	_ = db.QueryRow(`SELECT time_zone, ask_quiet FROM app_users WHERE id = ?`, userID).Scan(&z, &q)
	if z != "" {
		tz = z
	}
	if q == "off" {
		return tz, nil
	}
	if q == "" {
		q = askQuietDefault
	}
	a, b, _ := strings.Cut(q, "-")
	return tz, map[string]string{"start": a, "end": b}
}

// --- the bot's side over HTTP and the spool ------------------------------------------

// serveBotChannelAsk is POST /bot-channel/<bot>/ask and /ask-withdraw: a
// claude bot's ask tools. The channel token proved the bot; a reply token,
// when one is passed, proves the person it is answering.
func serveBotChannelAsk(w http.ResponseWriter, r *http.Request, bot, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in botAskIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, askBodyMax)).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	in.Kind = strings.ReplaceAll(action, "-", "_")
	out, err := botAskCall(bot, in, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

// botAskCall runs one ask tool for a claude bot.
func botAskCall(bot string, in botAskIn, now time.Time) (map[string]any, error) {
	if in.Kind == "ask_withdraw" {
		return botAskWithdraw(bot, in, now)
	}
	var actor *botActor
	if tok := strings.TrimSpace(in.ReplyToken); tok != "" {
		a, err := botActorFromToken(bot, tok, false, now)
		if err != nil {
			return nil, err
		}
		actor = a
	}
	return botAsk(bot, in, actor, now)
}

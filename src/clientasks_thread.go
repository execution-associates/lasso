package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// An ask's own thread (docs/design/client-asks.md, "Talking an ask over").
// Every bot ask hangs off the person's main thread with its bot, which holds
// none of the ask's words, so "Open thread" there lands on an unrelated
// conversation. The first time the person opens an ask's thread lasso makes a
// standalone thread with the asking bot, titled from the question and started
// by the bot's own message carrying the ask, and remembers it on the ask
// (app_asks.discussion): every later open, from any device, is that thread.

var errAskNoThread = errors.New("an agent's question has no thread")

// askThreadMu serializes the check-then-create, so two devices opening the
// same ask at once get one thread.
var askThreadMu sync.Mutex

// askThread is the ask's own thread, made on first use.
func askThread(a *appAsk, now time.Time) (*appThread, error) {
	if a.Agent != nil || a.Bot == "" {
		return nil, errAskNoThread
	}
	askThreadMu.Lock()
	defer askThreadMu.Unlock()
	if cur, err := askByID(a.ID); err == nil {
		a = cur
	}
	if a.Discussion != "" {
		// A thread the person deleted is made again.
		if t, err := threadByID(a.Discussion); err == nil && t.UserID == a.UserID {
			return t, nil
		}
	}
	title := autoThreadTitle(nonEmptyOr(a.Subject, a.Question))
	if a.Subject != "" {
		title = autoThreadTitle(a.Subject + ": " + a.Question)
	}
	if title == "" {
		title = clientThreadUntitled
	}
	t, err := createThread(threadCreateSpec{
		UserID:    a.UserID,
		Title:     title,
		TitleAuto: true,
		Bots:      []string{a.Bot},
		CreatedBy: "user",
		AddedBy:   "user",
	}, now)
	if err != nil {
		return nil, err
	}
	// The ask is on the thread before its first message is, so that message's
	// own event already carries the ask (askOfMessages).
	if _, err := db.Exec(`UPDATE app_asks SET discussion = ?, discussion_message = '', updated_at = ? WHERE id = ?`, t.ID, nowStamp(now), a.ID); err != nil {
		return nil, err
	}
	m, err := addBotReply(t.conv(a.Bot), askThreadSeed(a), true, "", "", nil, now)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE app_asks SET discussion_message = ? WHERE id = ?`, m.ID, a.ID); err != nil {
		return nil, err
	}
	if cur, err := askByID(a.ID); err == nil {
		askAnnounce(cur, now)
	}
	return threadByID(t.ID)
}

// askThreadSeed is the bot's first message in an ask's thread: the ask as the
// person read it on the card, so the thread starts where the question is.
func askThreadSeed(a *appAsk) string {
	var b strings.Builder
	b.WriteString(a.Question)
	if a.Why != "" {
		b.WriteString("\n\n" + a.Why)
	}
	if at := a.Attachment; at != nil {
		label := nonEmptyOr(at.Label, at.Kind)
		if at.Ref != "" {
			label += " (" + at.Ref + ")"
		}
		b.WriteString("\n\n" + label)
		if at.URL != "" {
			b.WriteString(": " + at.URL)
		}
		if at.Preview != "" {
			b.WriteString("\n\n> " + strings.ReplaceAll(strings.TrimSpace(at.Preview), "\n", "\n> "))
		}
	}
	if len(a.Options) > 0 {
		labels := make([]string, 0, len(a.Options))
		for _, o := range a.Options {
			labels = append(labels, o.Label)
		}
		b.WriteString("\n\nOptions: " + strings.Join(labels, " · "))
	}
	b.WriteString("\n\n(My question from your Inbox. Reply here to talk it over.)")
	return b.String()
}

// nonEmptyOr is s, or def when s is empty.
func nonEmptyOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// askOfMessages maps the messages of an ask's own thread that carry an ask to
// it: the bot's first message there. A thread made before lasso kept that
// message's id names none, so its first bot message stands in.
func askOfMessages(threadID string) map[string]*clientAskView {
	asks := listAsks(`a.discussion = ? AND a.agent = ''`, threadID)
	if len(asks) == 0 {
		return nil
	}
	now := time.Now()
	out := map[string]*clientAskView{}
	for _, a := range asks {
		msg := a.DiscussionMsg
		if msg == "" {
			_ = db.QueryRow(`SELECT id FROM app_messages WHERE conv_id = ? AND role = 'bot' AND bot = ? ORDER BY seq LIMIT 1`, threadID, a.Bot).Scan(&msg)
		}
		if msg != "" {
			v := a.view(now)
			out[msg] = &v
		}
	}
	return out
}

// botAskResolve is a bot saying it has finished what one of its asks was
// about (ask_resolve): the ask records the bot's closing note, which the note
// also is in the ask's own thread, and that thread is archived. An ask still
// open is withdrawn first, since nothing is left to decide. Only the asking
// bot resolves its ask, and only once; the thread is the ask's own, which the
// bot started, so the note reaches nobody the ask did not.
func botAskResolve(bot string, in botAskIn, now time.Time) (map[string]any, error) {
	id, key := strings.TrimSpace(in.AskID), strings.TrimSpace(in.DedupeKey)
	var asks []*appAsk
	switch {
	case id != "":
		asks = listAsks(`a.bot = ? AND a.agent = '' AND a.id = ?`, bot, id)
	case key != "":
		q, args := `a.bot = ? AND a.agent = '' AND a.dedupe_key = ?`, []any{bot, key}
		if to := strings.TrimSpace(in.To); to != "" && to != "owner" {
			q += ` AND a.user_id = ?`
			args = append(args, to)
		}
		asks = listAsks(q+` ORDER BY a.seq DESC LIMIT 1`, args...)
	default:
		return nil, errors.New("pass ask_id or dedupe_key")
	}
	if len(asks) == 0 {
		return nil, errors.New("no ask of yours matches")
	}
	a := asks[0]
	if a.ResolvedAt != "" {
		return nil, errors.New("that ask is already resolved")
	}
	if tooLong(in.Note, askResolutionMax) {
		return nil, fmt.Errorf("note is longer than %d characters: shorten it", askResolutionMax)
	}
	note := askText(in.Note, askResolutionMax, true)
	if _, err := getAppGrant(a.UserID, a.Bot); err != nil {
		return nil, errors.New("that person no longer has access to you")
	}
	if a.State == "open" {
		a, _ = askClose(a, "withdrawn", "", now)
	}
	res, err := db.Exec(`UPDATE app_asks SET resolved_at = ?, resolution = ?, updated_at = ? WHERE id = ? AND resolved_at = ''`,
		nowStamp(now), note, nowStamp(now), a.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errors.New("that ask is already resolved")
	}
	out := map[string]any{"resolved": a.ID, "state": a.State, "thread_archived": false}
	if a.Discussion != "" {
		if t, err := threadByID(a.Discussion); err == nil && t.UserID == a.UserID && isActiveParticipant(t.ID, a.Bot) {
			if note != "" {
				// A message reopens an archived thread, so it goes first.
				if _, err := addBotReply(t.conv(a.Bot), note, true, "", "", nil, now); err != nil {
					return nil, err
				}
			}
			res, err := db.Exec(`UPDATE app_threads SET state = 'archived', updated_at = ? WHERE id = ? AND state = 'open'`, nowStamp(now), t.ID)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 1 {
					threadUpdated(t.ID, []string{"state"}, now)
				}
			}
			out["thread_id"], out["thread_archived"] = t.ID, true
		}
	}
	if cur, err := askByID(a.ID); err == nil {
		askAnnounce(cur, now)
	}
	return out, nil
}

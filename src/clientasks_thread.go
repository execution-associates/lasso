package main

import (
	"errors"
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
	if _, err := addBotReply(t.conv(a.Bot), askThreadSeed(a), true, "", "", nil, now); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`UPDATE app_asks SET discussion = ?, updated_at = ? WHERE id = ?`, t.ID, nowStamp(now), a.ID); err != nil {
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

package main

// Notifications — lasso telling a HUMAN about something that happened. One
// pipeline carries every event, whether it reaches a locked phone or an open
// tab:
//
//   - a KIND says what happened (an agent waiting on you, an agent finishing a
//     turn, a bot replying, one of lasso's own notices). The kinds are a closed
//     catalog (notifKindCatalog) because each device chooses which of them may
//     interrupt it.
//   - a TRANSPORT says how it reaches the human: Web Push (webpush.go) to a
//     subscribed device, and the live channel (notifylive.go) to every open
//     tab and the Mac app over SSE. Each transport honours the receiving
//     device's choices: Web Push filters per subscription on the server, and a
//     live client filters what it is handed against its own device's choices.
//
// Everything above the transport is deliberately ignorant of how delivery
// works: a notification carries text, a collapse tag, and the host the event
// happened on, not endpoints or keys. Transports own their addressing.
//
// The choices decide what INTERRUPTS a device (a push, a toast, a sound),
// never what a client lists: an Inbox shows everything waiting either way.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// notifKind is what happened.
type notifKind string

const (
	// notifAgentBlocked: an agent stopped mid-task and is waiting on a human —
	// a tool-approval dialog, codex's "Action Required", omp's plan gate. The
	// one event that costs real time to miss, since nothing moves until someone
	// answers it.
	notifAgentBlocked notifKind = "agent_blocked"
	// notifAgentFinished: an agent stopped working with its turn done
	// (notifywatch.go). Off by default: a busy fleet finishes turns all day.
	notifAgentFinished notifKind = "agent_finished"
	// notifAgentMessage: an agent chose to say something — "the migration is
	// green", "I need a decision on the schema". Unlike a blocked agent this is
	// not inferred from a status, so it is never collapsed or rate-limited: the
	// agent asked once and means it.
	notifAgentMessage notifKind = "agent_message"
	// notifBotMessage: a bot finished a turn with something new to say
	// (botNotifyTick). Tagged per bot, so a run of answers leaves the newest.
	notifBotMessage notifKind = "bot_message"
	// notifLassoNotice: lasso's own news (notifyUI) — a theme a host refused, an
	// auto-title that failed. Pushed to a phone only at level error.
	notifLassoNotice notifKind = "lasso_notice"
	// notifTest is the Settings tab's "send a test notification" button. It is a
	// kind rather than a special case inside a transport so that the button
	// exercises exactly the path a real notification takes. Not in the catalog:
	// a test always goes through.
	notifTest notifKind = "test"
)

// notifKindInfo is one kind as a device's settings list it.
type notifKindInfo struct {
	ID          notifKind `json:"id"`
	Label       string    `json:"label"`
	Description string    `json:"description"`
	// Default is whether a device that never chose gets this kind.
	Default bool `json:"default"`
}

// notifKindCatalog is every kind a device can choose, in the order settings
// list them. The defaults are what a device got before choices existed, plus
// lasso's notices, which every open tab already showed as toasts.
var notifKindCatalog = []notifKindInfo{
	{notifAgentBlocked, "An agent is waiting on you", "A question, an approval or a plan gate.", true},
	{notifAgentFinished, "An agent finished a turn", "Any agent that stops working with its turn done.", false},
	{notifAgentMessage, "An agent pinged you", "An agent's own message for you (lasso notify).", true},
	{notifBotMessage, "A bot replied", "A bot finished a turn with something new to say.", true},
	{notifLassoNotice, "lasso's own notices", "Errors and results from lasso itself, like a theme a host would not take.", true},
}

// notifDelivers is whether a device with these choices is sent n. Its kind
// must be one the device takes, and a bot's turn is announced once: a bot is
// a herdr agent, so the watcher sees its pane finish the same turn the bot
// loop reports as a reply. A device that takes bot replies hears the reply;
// one that takes only finished turns still hears that the bot finished.
func notifDelivers(choices map[string]bool, n notification) bool {
	if !notifWants(choices, n.Kind) {
		return false
	}
	if n.Kind == notifAgentFinished && n.Bot != "" &&
		notifWants(choices, notifBotMessage) {
		return false
	}
	return true
}

func notifKindKnown(k string) bool {
	for _, c := range notifKindCatalog {
		if string(c.ID) == k {
			return true
		}
	}
	return false
}

// notifWants reports whether a device with these choices takes a kind: its
// own choice, else the kind's default. Only explicit choices are stored, so a
// kind added later reaches every device at its default.
func notifWants(choices map[string]bool, k notifKind) bool {
	if k == notifTest {
		return true
	}
	if v, ok := choices[string(k)]; ok {
		return v
	}
	for _, c := range notifKindCatalog {
		if c.ID == k {
			return c.Default
		}
	}
	return false
}

// notification is one thing worth telling the user about.
type notification struct {
	Kind  notifKind
	Title string
	Body  string
	// Level is a lasso notice's severity ("error", "info", "success"); empty
	// for every other kind.
	Level string
	// Bot names the bot this is about when it happened in a bot's pane (a bot
	// is a herdr agent too), so a device is told about one bot turn once
	// (notifDelivers).
	Bot string
	// Tag collapses re-sends: a notification with a tag REPLACES an earlier one
	// carrying the same tag on the device rather than stacking beside it. Keyed
	// per pane by the watcher, so an agent that blocks, unblocks and blocks
	// again leaves one entry in the notification centre, not three.
	Tag string
	// Host is the host the event happened on ("local" or an ssh alias), so
	// opening the notification can land the tab on the right machine. It names
	// no pane on purpose: herdr's focus is one global per session, shared with
	// the TUI and every other lasso client, so a link that focused a pane would
	// move it for everyone (the same reason lib/url.ts keeps focus out of the
	// URL).
	Host string
	// URL is the lasso page opening the notification lands on ("/bots/<name>"),
	// root-relative; "" opens lasso on Host.
	URL string
	// Icon is a root-relative image shown with it (a bot's picture); "" is
	// lasso's own icon.
	Icon string
}

// notifTransport is one way a notification reaches a human.
type notifTransport interface {
	// name identifies the transport in logs.
	name() string
	// active reports whether this transport has anywhere to deliver TO — no
	// registered devices means the whole notification pipeline is off, and the
	// watcher's fleet poll never runs. Called on a poll interval, so it must be
	// cheap.
	active() bool
	// deliver sends n. It is called off the caller's goroutine and may block up
	// to notifDeliverTimeout.
	deliver(ctx context.Context, n notification) error
}

var notifTransportsMu sync.RWMutex
var notifTransports []notifTransport

// registerNotifTransport adds a transport to the fan-out. Called once per
// transport from runServer; nothing registers a transport at init time, so CLI
// subcommands and tests deliver nowhere unless they opt in.
func registerNotifTransport(t notifTransport) {
	notifTransportsMu.Lock()
	defer notifTransportsMu.Unlock()
	notifTransports = append(notifTransports, t)
}

// notifDeliverTimeout bounds one fan-out. A push service that has gone slow
// costs at most this much of a background goroutine, and never delays the poll
// loop that produced the notification.
const notifDeliverTimeout = 30 * time.Second

// activeNotifTransports returns the transports with somewhere to deliver.
func activeNotifTransports() []notifTransport {
	notifTransportsMu.RLock()
	all := notifTransports
	notifTransportsMu.RUnlock()
	var out []notifTransport
	for _, t := range all {
		if t.active() {
			out = append(out, t)
		}
	}
	return out
}

// notifyEnabled reports whether any transport has a destination. The blocked
// watcher gates its fleet-wide pane.list poll on this: with no device
// subscribed, notifications must cost exactly nothing.
func notifyEnabled() bool { return len(activeNotifTransports()) > 0 }

// deliverNotification sends n through every active transport and reports which
// ones took it, plus whatever the rest failed with.
//
// This is the request-path form. An agent that pings its human has to be told
// whether anything is actually listening — "sent" when nothing is subscribed is
// the one answer that would make the feature worse than not having it — and the
// Settings tab's test button exists precisely to surface the error. Background
// producers use publishNotification instead, which cannot wait on a push
// service.
//
// A partial result is a real one: sent names the transports that accepted it and
// err carries the others' failures, so both can be reported at once.
func deliverNotification(ctx context.Context, n notification) (sent []string, err error) {
	var errs []error
	for _, t := range activeNotifTransports() {
		if e := t.deliver(ctx, n); e != nil {
			errs = append(errs, fmt.Errorf("%s: %w", t.name(), e))
			continue
		}
		sent = append(sent, t.name())
	}
	err = errors.Join(errs...)
	// Log what went out, not just what failed. This interrupts a human on a
	// locked phone, so "why did I get that?" has to be answerable from the log
	// afterwards — otherwise the only way to explain a notification is to
	// re-derive it from a live fleet query, which is exactly what happened the
	// first time one looked surprising. The tag names the pane; the body is left
	// out (it is the agent's text, and it already reached the device).
	switch {
	case len(sent) > 0 && err != nil:
		log.Printf("notify: %s %q via %s [%s] (some failed: %v)", n.Kind, n.Title, strings.Join(sent, ", "), n.Tag, err)
	case len(sent) > 0:
		log.Printf("notify: %s %q via %s [%s]", n.Kind, n.Title, strings.Join(sent, ", "), n.Tag)
	case err != nil:
		log.Printf("notify: %s %q undelivered: %v", n.Kind, n.Title, err)
	}
	return sent, err
}

// notifyNoDestination is what a caller is told when the notification went
// nowhere because nothing is subscribed. It names the fix, because the caller is
// often an agent that will otherwise report "I notified you" to a human who
// received nothing.
const notifyNoDestination = "nothing is subscribed to notifications — a device has to register itself in lasso's Settings tab first (on iOS: add lasso to the Home Screen, then enable it there)"

// notifyResult is the request-path answer: whether the notification reached
// anything, through what, and what went wrong. Shared by the Settings test
// button and the notify MCP tool so "sent" means the same thing in both.
type notifyResult struct {
	Sent       bool
	Transports []string
	Detail     string
}

// notifyNow delivers n and describes the outcome. It never errors: a caller
// asking to reach a human wants to know what happened, and "no device is
// subscribed" is an answer, not a fault.
func notifyNow(ctx context.Context, n notification) notifyResult {
	sent, err := deliverNotification(ctx, n)
	res := notifyResult{Sent: len(sent) > 0, Transports: sent}
	switch {
	case err != nil:
		res.Detail = err.Error()
	case len(sent) == 0:
		res.Detail = notifyNoDestination
	}
	return res
}

// publishNotification fans n out to every active transport, off the caller's
// goroutine. Its callers are poll loops; none may wait on a push service, and a
// delivery failure is theirs to ignore — deliverNotification logs both the
// outcome and any failure, so there is nothing left for this to report.
func publishNotification(n notification) {
	if !notifyEnabled() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifDeliverTimeout)
		defer cancel()
		_, _ = deliverNotification(ctx, n)
	}()
}

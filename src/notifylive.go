package main

import (
	"context"
	"errors"
)

// liveChannel is the notifTransport (notify.go) for clients that are open
// right now: every tab and the Mac app, over their SSE stream, as a
// `notification` event. The server does not know which device a stream
// belongs to, so it hands every client every notification and each one
// filters by its own device's choices (GET /api/notify?device=) with the
// same rule as notifDelivers; a choice governs what interrupts that client,
// never what it lists.
type liveChannel struct{}

// liveNotification is the `notification` SSE event. ID and At let a client
// that keeps notifications (an Inbox) dedupe and order them.
type liveNotification struct {
	ID    string `json:"id"`
	At    int64  `json:"at"`
	Kind  string `json:"kind"`
	Level string `json:"level,omitempty"`
	// Bot names the bot a pane's event is about; a client applies the same
	// once-per-bot-turn rule as Web Push (notifDelivers).
	Bot   string `json:"bot,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Tag   string `json:"tag,omitempty"`
	Host  string `json:"host,omitempty"`
	URL   string `json:"url,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

func (liveChannel) name() string { return "live" }

func (liveChannel) active() bool { return srvHub != nil && srvHub.eventClientCount() > 0 }

func (liveChannel) deliver(_ context.Context, n notification) error {
	if srvHub == nil {
		return errors.New("no hub")
	}
	if srvHub.broadcast("notification", liveNotificationOf(stampNotification(n))) == 0 {
		return errors.New("no open client took it")
	}
	return nil
}

// liveNotificationOf is n as a client receives it; n is already stamped
// (stampNotification), so the event and the Inbox's kept notice share an id.
func liveNotificationOf(n notification) liveNotification {
	return liveNotification{
		ID:    n.ID,
		At:    n.At,
		Kind:  string(n.Kind),
		Level: n.Level,
		Bot:   n.Bot,
		Title: n.Title,
		Body:  n.Body,
		Tag:   n.Tag,
		Host:  n.Host,
		URL:   n.URL,
		Icon:  n.Icon,
	}
}

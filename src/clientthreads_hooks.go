package main

import (
	"net/http"
	"time"
)

// clientEventAppended is where an event of a thread is seen after it is
// stored: the push decisions subscribe here (clientpush.go).
func clientEventAppended(threadID, userID, typ string, data any, now time.Time) {}

// clientBadgeMaybeDown pushes the new badge when it went down.
func clientBadgeMaybeDown(userID string, before int, now time.Time) {}

func serveClientExtraRoutes(w http.ResponseWriter, r *http.Request, id *clientIdentity, segs []string) bool {
	return false
}

func clientPushView(id *clientIdentity) map[string]any {
	return map[string]any{"available": false, "provider": "none", "registered": false, "reason": "not_configured"}
}

func botThreadSearch(a *botActor, query, threadID string, limit int, now time.Time) (map[string]any, error) {
	return map[string]any{"results": []any{}}, nil
}

const clientBotSearchPerMin = 20

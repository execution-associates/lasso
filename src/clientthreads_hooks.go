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
	if len(segs) == 1 && segs[0] == "search" {
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		serveClientSearch(w, r, id)
		return true
	}
	return false
}

func clientPushView(id *clientIdentity) map[string]any {
	return map[string]any{"available": false, "provider": "none", "registered": false, "reason": "not_configured"}
}



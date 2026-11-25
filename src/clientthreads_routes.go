package main

import "net/http"

// serveClientExtraRoutes are the person's routes beside threads: search,
// this device's push token and presence, and their asks (clientasks.go).
func serveClientExtraRoutes(w http.ResponseWriter, r *http.Request, id *clientIdentity, segs []string) bool {
	switch {
	case len(segs) == 1 && segs[0] == "search":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return true
		}
		serveClientSearch(w, r, id)
	case len(segs) == 2 && segs[0] == "device" && segs[1] == "push":
		serveClientDevicePush(w, r, id)
	case len(segs) == 2 && segs[0] == "device" && segs[1] == "presence":
		if r.Method != http.MethodPost {
			clientMethod(w, "POST")
			return true
		}
		serveClientPresence(w, r, id)
	default:
		return serveClientAskRoutes(w, r, id, segs)
	}
	return true
}

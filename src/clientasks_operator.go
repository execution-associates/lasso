package main

// The operator's Inbox (docs/design/client-asks.md, "The operator's Inbox"):
// lasso's own web UI lists and answers asks as ONE app user, the operator's
// own, since whoever reaches /api is the lasso operator and not a person the
// client API knows. Which app user that is is a setting; with exactly one
// person owning bots it needs no choosing.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const askOperatorKey = "asks_operator"

type askOperatorPerson struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// askOperatorCandidates are the people who own at least one bot in the app.
func askOperatorCandidates() []askOperatorPerson {
	rows, err := db.Query(`SELECT DISTINCT u.id, u.name FROM app_users u JOIN app_grants g ON g.user_id = u.id
		WHERE g.owner = 1 AND u.disabled = 0 AND u.review = 0 ORDER BY u.created_at, u.id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []askOperatorPerson{}
	for rows.Next() {
		var p askOperatorPerson
		if rows.Scan(&p.ID, &p.Name) == nil {
			out = append(out, p)
		}
	}
	return out
}

// askOperator is the app user lasso's own UI acts as, or why there is none:
// the one chosen in the setting while they still stand, else the only person
// who owns a bot.
func askOperator() (u *appUser, chosen bool, reason string) {
	if id, _ := getSetting(askOperatorKey); id != "" {
		if u, err := getAppUser(id); err == nil && !u.Disabled && !u.Review {
			return u, true, ""
		}
		return nil, true, "The person chosen for this Inbox no longer exists or is disabled. Choose another."
	}
	switch c := askOperatorCandidates(); len(c) {
	case 0:
		return nil, false, "Nobody owns a bot in the phone app yet. Make yourself a bot's owner in the Bots view's People tab."
	case 1:
		u, err := getAppUser(c[0].ID)
		if err != nil {
			return nil, false, "That person could not be read."
		}
		return u, false, ""
	}
	return nil, false, "Several people own bots. Choose which of them this Inbox is."
}

// serveOperatorAsks is /api/asks: the person's ask routes (clientasks.go) for
// the operator's app user, and /api/asks/operator, which reads and sets who
// that is. Behind UI_AUTH and off machine callers like the rest of /api.
func serveOperatorAsks(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/asks"), "/")
	u, chosen, reason := askOperator()
	view := func() map[string]any {
		out := map[string]any{"operator": nil, "chosen": chosen, "candidates": askOperatorCandidates()}
		if u != nil {
			out["operator"] = askOperatorPerson{ID: u.ID, Name: u.Name}
		} else {
			out["reason"] = reason
		}
		return out
	}
	if rest == "operator" {
		switch r.Method {
		case http.MethodGet:
		case http.MethodPut:
			var in struct {
				UserID *string `json:"user_id"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			id := ""
			if in.UserID != nil {
				id = strings.TrimSpace(*in.UserID)
				if p, err := getAppUser(id); err != nil || p.Disabled || p.Review {
					http.Error(w, "no such person", http.StatusBadRequest)
					return
				}
			}
			if err := setSetting(askOperatorKey, id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			u, chosen, reason = askOperator()
		default:
			http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
			return
		}
		clientJSON(w, http.StatusOK, view())
		return
	}
	if rest == "agent" {
		// A hook of the lasso-asks plugin (clientasks_agent.go). With nobody
		// to ask it says so and still answers 200: a hook never fails its agent.
		if u == nil {
			clientJSON(w, http.StatusOK, map[string]any{"asks": []string{}, "skipped": reason})
			return
		}
		serveAgentAsk(w, r, u)
		return
	}
	if u == nil {
		if rest == "" && r.Method == http.MethodGet {
			out := view()
			out["asks"], out["counts"] = []clientAskView{}, map[string]int{"waiting": 0, "snoozed": 0}
			clientJSON(w, http.StatusOK, out)
			return
		}
		clientError(w, http.StatusConflict, "no_operator", reason)
		return
	}
	segs := []string{"asks"}
	if rest != "" {
		segs = append(segs, strings.Split(rest, "/")...)
	}
	if !serveClientAskRoutes(w, r, operatorIdentity(u), segs) {
		clientNotFound(w)
	}
}

// operatorIdentity is the operator's app user as a caller of the person's
// routes. What they do reads as done on the web: there is no device to name.
func operatorIdentity(u *appUser) *clientIdentity {
	return &clientIdentity{User: u, Device: appDevice{Platform: "web"}}
}

// serveOperatorThreads is /api/operator/…: the operator's own threads with
// their bots, on the person's routes (clientthreads.go, clientapi.go), for the
// Bots view's Threads page. Only what a page without a paired device can use
// is here: threads, their messages, files and read marks, and search. The
// streams, calls and device routes need a device and stay on /client.
func serveOperatorThreads(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/operator"), "/")
	u, _, reason := askOperator()
	if u == nil {
		clientError(w, http.StatusConflict, "no_operator", reason)
		return
	}
	id := operatorIdentity(u)
	segs := strings.Split(rest, "/")
	switch {
	case len(segs) == 1 && segs[0] == "bots":
		if r.Method != http.MethodGet {
			clientMethod(w, "GET")
			return
		}
		clientJSON(w, http.StatusOK, map[string]any{"operator": askOperatorPerson{ID: u.ID, Name: u.Name}, "bots": clientBotsOf(id, time.Now())})
	case len(segs) <= 2 && segs[0] == "threads", len(segs) == 1 && segs[0] == "search":
		if !serveClientThreadRoutes(w, r, id, segs) {
			clientNotFound(w)
		}
	case len(segs) == 3 && segs[0] == "threads" && (segs[2] == "messages" || segs[2] == "attachments" || segs[2] == "read"):
		serveClientThreadAction(w, r, id, segs[1], segs[2])
	case len(segs) == 2 && segs[0] == "attachments" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		serveClientAttachment(w, r, id, segs[1])
	default:
		clientNotFound(w)
	}
}

package main

// Full-text search over a person's threads (docs/design/client-threads.md,
// "Search"): SQLite FTS5 over app_search, kept by triggers. Nothing the person
// types is FTS syntax: each word is a quoted term, the last one a prefix. The
// query filters by the person; every hit is then joined back to its row and
// kept only when its thread is theirs and visible (and, for a bot, one it is
// in), so a snippet only ever comes from a row that passed.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	clientSearchQueryMax  = 200
	clientSearchTerms     = 8
	clientSearchDeep      = 200
	clientSearchSnippet   = 160
	clientSearchPerMin    = 30
	clientBotSearchPerMin = 20
	searchHLStart         = ''
	searchHLEnd           = ''
)

// ftsQuery turns what a person typed into an FTS5 query of quoted terms, the
// last one a prefix: "permit" "irvine"*.
func ftsQuery(q string) (string, bool) {
	var terms []string
	for _, w := range strings.Fields(q) {
		w = strings.NewReplacer(`"`, "", "*", "").Replace(w)
		if w == "" {
			continue
		}
		terms = append(terms, `"`+w+`"`)
		if len(terms) == clientSearchTerms {
			break
		}
	}
	if len(terms) == 0 {
		return "", false
	}
	terms[len(terms)-1] += "*"
	return strings.Join(terms, " "), true
}

// searchSnippet strips the highlight marks from an FTS5 snippet and answers
// the text and the [start, end) highlights in Unicode code points, cut to the
// snippet's length.
func searchSnippet(raw string) (string, [][2]int) {
	var out []rune
	var hl [][2]int
	start := -1
	for _, r := range raw {
		switch r {
		case searchHLStart:
			start = len(out)
		case searchHLEnd:
			if start >= 0 {
				hl = append(hl, [2]int{start, len(out)})
				start = -1
			}
		default:
			out = append(out, r)
		}
	}
	if len(out) > clientSearchSnippet {
		out = append(out[:clientSearchSnippet-1], '…')
		kept := hl[:0]
		for _, h := range hl {
			if h[0] >= clientSearchSnippet-1 {
				continue
			}
			if h[1] > clientSearchSnippet-1 {
				h[1] = clientSearchSnippet - 1
			}
			kept = append(kept, h)
		}
		hl = kept
	}
	if hl == nil {
		hl = [][2]int{}
	}
	return string(out), hl
}

type searchOpts struct {
	UserID, Scope, Bot, ThreadID, State string
	// AsBot limits the hits to threads this bot is an active participant of.
	AsBot string
}

type searchHit struct {
	Kind    string
	Thread  *appThread
	Message *clientMessageView
	Att     *appAttachment
	Snippet string
	HL      [][2]int
}

// clientSearch answers up to 200 hits, best first, every one re-checked.
func clientSearch(q string, o searchOpts) ([]searchHit, error) {
	fq, ok := ftsQuery(q)
	if !ok {
		return nil, errors.New("q needs a word to look for")
	}
	where := `app_search MATCH ? AND user_id = ?`
	args := []any{fq, o.UserID}
	switch o.Scope {
	case "", "all":
	case "threads":
		where += ` AND kind = 'thread'`
	case "messages":
		where += ` AND kind = 'message'`
	case "files":
		where += ` AND kind = 'file'`
	default:
		return nil, errors.New("scope must be all, threads, messages or files")
	}
	if o.ThreadID != "" {
		where += ` AND thread_id = ?`
		args = append(args, o.ThreadID)
	}
	rows, err := db.Query(`SELECT kind, thread_id, ref_id, snippet(app_search, 0, char(57344), char(57345), '…', 20) FROM app_search
		WHERE `+where+` ORDER BY rank LIMIT ?`, append(args, clientSearchDeep)...)
	if err != nil {
		return nil, err
	}
	type raw struct{ kind, thread, ref, snip string }
	var rs []raw
	for rows.Next() {
		var r raw
		if rows.Scan(&r.kind, &r.thread, &r.ref, &r.snip) == nil {
			rs = append(rs, r)
		}
	}
	rows.Close()
	granted := grantedSet(o.UserID)
	threads := map[string]*appThread{}
	var out []searchHit
	for _, r := range rs {
		t, seen := threads[r.thread]
		if !seen {
			t, _ = threadByID(r.thread)
			threads[r.thread] = t
		}
		if t == nil || t.UserID != o.UserID || (t.isMain() && !granted[t.MainBot]) {
			continue
		}
		if o.Bot != "" && (!granted[o.Bot] || !isActiveParticipant(t.ID, o.Bot)) {
			continue
		}
		if o.AsBot != "" && !isActiveParticipant(t.ID, o.AsBot) {
			continue
		}
		state := t.State
		if t.isMain() {
			state = "open"
		}
		if o.State != "" && o.State != "all" && o.State != state {
			continue
		}
		h := searchHit{Kind: r.kind, Thread: t}
		h.Snippet, h.HL = searchSnippet(r.snip)
		switch r.kind {
		case "thread":
			if t.isMain() || r.ref != t.ID {
				continue
			}
		case "message":
			m, err := messageByID(t.ID, r.ref)
			if err != nil || (m.Role != "user" && m.Role != "bot") {
				continue
			}
			h.Message = &m
		case "file":
			a, err := appAttachmentByID(r.ref)
			if err != nil || a.UserID != o.UserID || a.ConvID != t.ID || a.MessageID == "" {
				continue
			}
			m, err := messageByID(t.ID, a.MessageID)
			if err != nil {
				continue
			}
			h.Att, h.Message = &a, &m
		default:
			continue
		}
		out = append(out, h)
	}
	return out, nil
}

func searchThreadShort(t *appThread) map[string]any {
	m := map[string]any{"id": t.ID, "kind": t.Kind}
	if t.isMain() {
		m["bot"] = t.MainBot
	} else {
		m["title"] = t.Title
	}
	return m
}

func searchMessageShort(m *clientMessageView) map[string]any {
	out := map[string]any{"id": m.ID, "cursor": m.Cursor, "role": m.Role, "created_at": m.CreatedAt}
	if m.Bot != "" {
		out["bot"] = m.Bot
	}
	return out
}

func serveClientSearch(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	q := r.URL.Query()
	text := q.Get("q")
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > clientSearchQueryMax {
		clientError(w, http.StatusBadRequest, "invalid_request", "q must be 1-200 characters.")
		return
	}
	limit := 20
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			clientError(w, http.StatusBadRequest, "invalid_request", "limit must be 1-50.")
			return
		}
		limit = n
	}
	offset, ok := decodeCursor(q.Get("cursor"))
	if !ok {
		clientError(w, http.StatusBadRequest, "invalid_request", "cursor must come from next_cursor.")
		return
	}
	switch q.Get("state") {
	case "", "all", "open", "archived":
	default:
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be all, open or archived.")
		return
	}
	now := time.Now()
	if !clientLimits.allow("search:"+id.User.ID, clientSearchPerMin, time.Minute, now) {
		w.Header().Set("Retry-After", "30")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Slow down: too many searches.")
		return
	}
	hits, err := clientSearch(text, searchOpts{UserID: id.User.ID, Scope: q.Get("scope"), Bot: q.Get("bot"), ThreadID: q.Get("thread_id"), State: q.Get("state")})
	if err != nil {
		clientError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var next any
	if offset > len(hits) {
		offset = len(hits)
	}
	page := hits[offset:]
	if len(page) > limit {
		page = page[:limit]
		next = encodeCursor(offset + limit)
	}
	results := []map[string]any{}
	for _, h := range page {
		res := map[string]any{"kind": h.Kind, "snippet": h.Snippet, "highlights": h.HL}
		switch h.Kind {
		case "thread":
			res["thread"] = threadViewOf(h.Thread)
		case "message":
			res["thread"] = searchThreadShort(h.Thread)
			res["message"] = searchMessageShort(h.Message)
		case "file":
			res["thread"] = searchThreadShort(h.Thread)
			res["message"] = searchMessageShort(h.Message)
			res["attachment"] = h.Att.view()
		}
		results = append(results, res)
	}
	clientJSON(w, http.StatusOK, map[string]any{"results": results, "next_cursor": next})
}

// botThreadSearch is a bot's thread_search: the person's threads it is in.
func botThreadSearch(a *botActor, query, threadID string, limit int, now time.Time) (map[string]any, error) {
	if strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > clientSearchQueryMax {
		return nil, errors.New("query must be 1-200 characters")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}
	if !clientLimits.allow("botsearch:"+a.Bot, clientBotSearchPerMin, time.Minute, now) {
		return nil, errors.New("too many searches: at most 20 a minute")
	}
	if threadID != "" {
		if _, err := a.threadFor(threadID); err != nil {
			return nil, err
		}
	}
	hits, err := clientSearch(query, searchOpts{UserID: a.UserID, ThreadID: threadID, AsBot: a.Bot})
	if err != nil {
		return nil, err
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := []map[string]any{}
	for _, h := range hits {
		res := map[string]any{"kind": h.Kind, "thread_id": h.Thread.ID, "snippet": h.Snippet}
		if !h.Thread.isMain() {
			res["thread_title"] = h.Thread.Title
		}
		if h.Message != nil {
			res["cursor"] = h.Message.Cursor
			role := h.Message.Role
			if role == "user" {
				role = "person"
			}
			res["role"] = role
			if h.Message.Bot != "" {
				res["bot"] = h.Message.Bot
			}
			res["created_at"] = h.Message.CreatedAt
		}
		out = append(out, res)
	}
	return map[string]any{"results": out}, nil
}

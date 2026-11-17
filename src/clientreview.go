package main

// Review access (docs/design/client-api.md, "Review access"): a long-lived
// pairing link for app review, bound to a throwaway app user who can talk to
// one scripted demo bot that has no agent behind it.
//
// The demo bot is not a row in `bots`. It has no pane, agent, shell, model,
// tools, MCP server, environment, jobs, webhooks or talk key, and getBot
// refuses its name, so no owner path (list_bots, start_bot, send_bot, a bot's
// MCP tools) can start, message or configure it. Its name is not a legal bot
// name either, so no row can be made for it. The client API synthesizes its
// record (clientBotRecord), gives it lasso-owned storage, and answers the
// person's messages with demoBotReply, a pure scripted responder. The reply
// takes the same path a real bot's does: addBotReplyMentions stores it,
// appends the stream event, pushes, and runs the thread hooks.

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// clientDemoBot is the bot's name and its display name: the app shows a
	// bot's name. It is deliberately not a legal real bot name (uppercase and
	// a space), so nothing in `bots` can collide with it.
	clientDemoBot            = "Demo Bot"
	clientDemoBotDescription = "A sample bot that shows how Lasso works"
	clientReviewUser         = "App Review"
	clientReviewDevicesMax   = 25
	clientReviewDaysDefault  = 30
	clientReviewDaysMax      = 365
	clientReviewQuotaMB      = 50
	clientReviewQuotaFiles   = 200
	clientReviewQuoteMax     = 60
)

// clientDemoReplyDelay is how long the demo bot waits before answering, so the
// app shows a message being written rather than an instant echo. A test may
// shorten it.
var clientDemoReplyDelay = time.Second

func isClientDemoBot(name string) bool { return name == clientDemoBot }

// clientDemoRoot is where the demo person's uploads live: lasso's own
// directory, never a real bot's folder, because the demo bot has none.
func clientDemoRoot() string { return filepath.Join(lassoDir(), "client-review") }

// clientBotRecord is getBot for the client API: the demo bot's record is
// synthesized, every other name is a real bot's row. A real bot record never
// exists for the demo bot (getBot refuses its name).
func clientBotRecord(name string) (*botRecord, error) {
	if isClientDemoBot(name) {
		return &botRecord{Name: name, Host: "local", Dir: clientDemoRoot()}, nil
	}
	return getBot(name)
}

// --- the scripted responder ------------------------------------------------------------

// demoReplyIn is what the demo bot answers: the person's text, the names of
// the files they attached, and how the message was sent.
type demoReplyIn struct {
	Text        string
	Attachments []string
	Mode        string // text | voice
}

var demoWordRE = regexp.MustCompile(`[a-z0-9']+`)

// demoBotReply is the demo bot's whole intelligence: a keyword match, else a
// short acknowledgement. Plain, friendly, one to three sentences, and it
// describes the app. Never a model, never anything but this function.
func demoBotReply(in demoReplyIn) string {
	text := strings.TrimSpace(in.Text)
	lower := strings.ToLower(text)
	words := map[string]bool{}
	for _, w := range demoWordRE.FindAllString(lower, -1) {
		words[w] = true
	}
	has := func(ws ...string) bool {
		for _, w := range ws {
			if words[w] {
				return true
			}
		}
		return false
	}
	phrase := func(ps ...string) bool {
		for _, p := range ps {
			if strings.Contains(lower, p) {
				return true
			}
		}
		return false
	}
	voice := in.Mode == "voice"

	// A file was sent: acknowledge it by name. This comes first because the
	// file is the point of the message.
	if files := demoFileList(in.Attachments); files != "" {
		return fmt.Sprintf("Got it, I can see %s. In Lasso, files you send stay with the conversation so a bot can open them, and a bot can send files back to you the same way.", files)
	}
	if phrase("who are you", "what are you", "what is lasso", "what's lasso", "what is this app", "what is lasso app") || (has("what", "who") && has("lasso")) {
		return "I'm the Lasso demo bot, a sample that shows how Lasso works. Lasso is an app for chatting with AI bots from your phone: message them, start threads, share files, and call them hands-free."
	}
	if has("help") || phrase("what can you do", "what can i do", "how does this work") {
		return "You can chat with me, start a thread for a longer topic, send pictures and files, or call me and talk hands-free. Try asking about threads, files, or calls."
	}
	if has("file", "files", "photo", "photos", "picture", "pictures", "image", "images", "attachment", "attachments", "upload", "uploads", "document", "documents") {
		return "You can send me a picture or a file with the attach button, and Lasso keeps it with this conversation. Bots can send files back to you the same way."
	}
	if has("thread", "threads") || phrase("new topic", "side conversation") {
		return "Threads are separate conversations you start from a message, and they can include more than one bot. Your main chat stays one-to-one with each bot."
	}
	if has("call", "calls", "calling", "voice") || phrase("talk hands-free", "hands free") {
		return "Tap the call button in a chat and talk normally: Lasso turns your speech into text and reads my answers back, so it works hands-free. Try it whenever you like."
	}
	if has("hello", "hi", "hey", "hiya", "howdy", "greetings", "yo") {
		return "Hi! I'm the Lasso demo bot. Ask me what Lasso can do, or try sending a picture."
	}
	if has("thanks", "thank", "thankyou") || phrase("thank you", "thx") {
		return "You're welcome! Anything else you'd like to try? A thread, a file, or a call all work here."
	}
	quoted := demoQuote(text)
	if quoted == "" {
		quoted = "that"
	} else {
		quoted = `"` + quoted + `"`
	}
	if voice {
		return fmt.Sprintf("I heard %s. I'm the Lasso demo bot, so my answers are scripted: ask me what Lasso can do, or try a thread, a file, or a call.", quoted)
	}
	return fmt.Sprintf("You said %s. I'm the Lasso demo bot, so my replies are scripted: ask me what Lasso can do, or try a thread, a file, or a call.", quoted)
}

// demoFileList names the files a message carried, for the acknowledgement.
func demoFileList(names []string) string {
	var clean []string
	for _, n := range names {
		if n = demoQuote(strings.TrimSpace(n)); n != "" {
			clean = append(clean, `"`+n+`"`)
		}
	}
	switch len(clean) {
	case 0:
		return ""
	case 1:
		return clean[0]
	case 2:
		return clean[0] + " and " + clean[1]
	default:
		return strings.Join(clean[:2], ", ") + fmt.Sprintf(" and %d more", len(clean)-2)
	}
}

// demoQuote makes text safe to quote back: one line, no control or invisible
// formatting character, at most clientReviewQuoteMax characters.
func demoQuote(s string) string {
	var sb strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
			space = sb.Len() > 0
			continue
		case clientHostileRune(r):
			continue
		}
		if space {
			sb.WriteByte(' ')
			space = false
		}
		sb.WriteRune(r)
	}
	out := strings.TrimSpace(sb.String())
	if utf8.RuneCountInString(out) > clientReviewQuoteMax {
		r := []rune(out)
		out = strings.TrimSpace(string(r[:clientReviewQuoteMax])) + "…"
	}
	return out
}

// --- the demo bot's replies ------------------------------------------------------------

// clientDemoReplySoon schedules the demo bot's answer to a person's message:
// about a second later, asynchronously, so the send answers at once.
func clientDemoReplySoon(convID, msgID string) {
	time.AfterFunc(clientDemoReplyDelay, func() { clientDemoReply(convID, msgID) })
}

// clientDemoReply is the demo bot's turn: it re-reads the message and re-checks
// that the conversation still exists, the demo bot is still in it, the person
// is enabled and review access is still on, then posts the scripted answer
// through the ordinary bot-reply path (stored message, stream event, push,
// thread hooks, call id).
func clientDemoReply(convID, msgID string) {
	m, err := messageByID(convID, msgID)
	if err != nil || m.Role != "user" {
		return
	}
	t, err := threadByID(convID)
	if err != nil || !isActiveParticipant(t.ID, clientDemoBot) {
		return
	}
	u, err := getAppUser(t.UserID)
	if err != nil || u.Disabled || !u.Review || !clientReviewOn() {
		return
	}
	if _, err := getAppGrant(t.UserID, clientDemoBot); err != nil {
		return
	}
	names := make([]string, 0, len(m.Attachments))
	for _, a := range m.Attachments {
		names = append(names, a.Name)
	}
	reply := demoBotReply(demoReplyIn{Text: m.Text, Attachments: names, Mode: m.Mode})
	_, _ = addBotReplyMentions(t.conv(clientDemoBot), reply, true, m.CallID, m.ID, nil, nil, time.Now())
}

// --- review access storage ---------------------------------------------------------------

const clientReviewSchema = `
CREATE TABLE IF NOT EXISTS app_review (
	id         TEXT PRIMARY KEY,              -- 'review': one review access per lasso
	enabled    INTEGER NOT NULL DEFAULT 0,
	user_id    TEXT NOT NULL DEFAULT '',      -- the demo person while enabled
	days       INTEGER NOT NULL DEFAULT 0,    -- how long the link lives from its mint
	code_hash  TEXT NOT NULL DEFAULT '',      -- the pairing code, only ever as its SHA-256
	minted_by  TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT '',
	expires_at TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL DEFAULT ''
);
`

const clientReviewKey = "review"

type clientReviewState struct {
	Enabled   bool
	UserID    string
	Days      int
	CodeHash  string
	MintedBy  string
	CreatedAt string
	ExpiresAt string
	UpdatedAt string
}

func loadClientReview() clientReviewState {
	s := clientReviewState{Days: clientReviewDaysDefault}
	if db == nil {
		return s
	}
	var en int
	if db.QueryRow(`SELECT enabled, user_id, days, code_hash, minted_by, created_at, expires_at, updated_at
		FROM app_review WHERE id = ?`, clientReviewKey).
		Scan(&en, &s.UserID, &s.Days, &s.CodeHash, &s.MintedBy, &s.CreatedAt, &s.ExpiresAt, &s.UpdatedAt) != nil {
		return s
	}
	s.Enabled = en != 0
	if s.Days <= 0 {
		s.Days = clientReviewDaysDefault
	}
	return s
}

// clientReviewOn is whether review access is enabled. A review device's token
// is refused while it is off, so turning it off ends every device at once even
// if a device row survived.
func clientReviewOn() bool { return loadClientReview().Enabled }

// reviewCodeRow reads the review row inside a transaction and answers whether
// the presented code is its hash.
func reviewCodeRow(q rowQuerier, h string) (clientReviewState, bool) {
	var s clientReviewState
	var en int
	if q.QueryRow(`SELECT enabled, user_id, days, code_hash, minted_by, created_at, expires_at FROM app_review WHERE id = ?`, clientReviewKey).
		Scan(&en, &s.UserID, &s.Days, &s.CodeHash, &s.MintedBy, &s.CreatedAt, &s.ExpiresAt) != nil {
		return s, false
	}
	s.Enabled = en != 0
	return s, s.Enabled && s.CodeHash != "" && hashEqual(s.CodeHash, h)
}

// reviewDeviceCount is how many devices are paired to the demo person now
// (revoked ones do not count, so revoking frees a place).
func reviewDeviceCount(userID string) int {
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM app_devices WHERE user_id = ? AND revoked_at = ''`, userID).Scan(&n)
	return n
}

// createReviewUser makes the throwaway person review access signs in as.
func createReviewUser(now time.Time) (*appUser, error) {
	u := &appUser{ID: clientID("u_", 8), Name: clientReviewUser, Review: true, QuotaMB: clientReviewQuotaMB,
		QuotaFiles: clientReviewQuotaFiles, CreatedAt: nowStamp(now), UpdatedAt: nowStamp(now)}
	_, err := db.Exec(`INSERT INTO app_users (id, name, email, disabled, created_at, updated_at, quota_mb, quota_files, review)
		VALUES (?, ?, '', 0, ?, ?, ?, ?, 1)`, u.ID, u.Name, u.CreatedAt, u.UpdatedAt, u.QuotaMB, u.QuotaFiles)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// reviewUsers are the people flagged as review access (normally none or one).
func reviewUsers() []string {
	rows, err := db.Query(`SELECT id FROM app_users WHERE review = 1`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// enableClientReview turns review access on: the demo person, their one grant
// and a fresh multi-use pairing code. Enabling while it is already on re-mints
// the link (the old code stops working; paired devices stay paired) and moves
// the expiry. The code is returned once and stored only as its hash.
func enableClientReview(days int, mintedBy, base string, now time.Time) (map[string]any, error) {
	rv := loadClientReview()
	if days <= 0 {
		days = rv.Days
		if days <= 0 {
			days = clientReviewDaysDefault
		}
	}
	if days < 1 || days > clientReviewDaysMax {
		return nil, fmt.Errorf("days must be 1-%d", clientReviewDaysMax)
	}
	var u *appUser
	if rv.Enabled && rv.UserID != "" {
		if got, err := getAppUser(rv.UserID); err == nil && got.Review && !got.Disabled {
			u = got
		}
	}
	if u == nil {
		// Nothing of an earlier run survives: a disabled review access has no
		// person, and a half-finished one is swept before a new person is made.
		for _, id := range reviewUsers() {
			atts, err := deleteAppUserData(id)
			if err != nil {
				return nil, err
			}
			clientRemoveFiles(atts)
		}
		var err error
		if u, err = createReviewUser(now); err != nil {
			return nil, err
		}
	}
	if _, err := setAppGrant(u.ID, clientDemoBot, false, now); err != nil {
		return nil, err
	}
	code := clientRandomToken(clientPairPrefix, 16)
	stamp := nowStamp(now)
	expires := nowStamp(now.Add(time.Duration(days) * 24 * time.Hour))
	created := rv.CreatedAt
	if created == "" || rv.UserID != u.ID {
		created = stamp
	}
	if _, err := db.Exec(`INSERT INTO app_review (id, enabled, user_id, days, code_hash, minted_by, created_at, expires_at, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET enabled = 1, user_id = excluded.user_id, days = excluded.days, code_hash = excluded.code_hash,
			minted_by = excluded.minted_by, created_at = excluded.created_at, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		clientReviewKey, u.ID, days, tokenHash(code), mintedBy, created, expires, stamp); err != nil {
		return nil, err
	}
	out := map[string]any{"review": clientReviewView(now), "code": code}
	if base != "" {
		out["link"] = base + "/client/pair#code=" + code
		out["deep_link"] = "lasso://pair?code=" + code + "&server=" + url.QueryEscape(base)
	}
	return out, nil
}

// disableClientReview turns review access off: the code stops working, every
// device paired through it is signed out, and the demo person's threads,
// messages and files are deleted. Nothing of the review person is left.
func disableClientReview(now time.Time) error {
	ids := reviewUsers()
	if rv := loadClientReview(); rv.UserID != "" {
		seen := false
		for _, id := range ids {
			seen = seen || id == rv.UserID
		}
		if !seen {
			ids = append(ids, rv.UserID)
		}
	}
	for _, id := range ids {
		atts, err := deleteAppUserData(id)
		if err != nil {
			return err
		}
		clientRemoveFiles(atts)
	}
	_, err := db.Exec(`INSERT INTO app_review (id, enabled, user_id, days, code_hash, minted_by, created_at, expires_at, updated_at)
		VALUES (?, 0, '', ?, '', '', '', '', ?)
		ON CONFLICT(id) DO UPDATE SET enabled = 0, user_id = '', code_hash = '', expires_at = '', updated_at = excluded.updated_at`,
		clientReviewKey, loadClientReview().Days, nowStamp(now))
	return err
}

// clientReviewView is the owner's view of review access: state, expiry and
// device count, never the code.
func clientReviewView(now time.Time) map[string]any {
	rv := loadClientReview()
	v := map[string]any{
		"enabled":     rv.Enabled,
		"days":        rv.Days,
		"max_devices": clientReviewDevicesMax,
		"bot":         map[string]any{"name": clientDemoBot, "description": clientDemoBotDescription},
	}
	if rv.Enabled {
		v["user_id"] = rv.UserID
		v["user_name"] = clientReviewUser
		v["created_at"] = rv.CreatedAt
		v["expires_at"] = rv.ExpiresAt
		if exp, ok := parseStamp(rv.ExpiresAt); ok {
			v["expired"] = !now.Before(exp)
		}
		v["devices"] = reviewDeviceCount(rv.UserID)
	} else {
		v["devices"] = 0
	}
	return v
}

// redeemReviewPairing pairs one more device through the review code. The code
// is multi-use until it expires; past clientReviewDevicesMax devices pairing is
// refused (the oldest device is never evicted). It runs inside the caller's
// transaction, which holds lasso's only connection, so two uses cannot both
// take the last place.
func redeemReviewPairing(tx *sql.Tx, rv clientReviewState, label, platform string, now time.Time) (string, *appDevice, *appUser, error) {
	if !rv.Enabled || rv.CodeHash == "" || rv.UserID == "" {
		return "", nil, nil, errPairingRefused
	}
	if exp, ok := parseStamp(rv.ExpiresAt); !ok || !now.Before(exp) {
		return "", nil, nil, errPairingRefused
	}
	u, err := scanAppUser(tx.QueryRow(`SELECT `+appUserCols+` FROM app_users WHERE id = ?`, rv.UserID))
	if err != nil || u.Disabled || !u.Review {
		return "", nil, nil, errPairingRefused
	}
	var n int
	if tx.QueryRow(`SELECT COUNT(*) FROM app_devices WHERE user_id = ? AND revoked_at = ''`, u.ID).Scan(&n) != nil {
		return "", nil, nil, errPairingRefused
	}
	if n >= clientReviewDevicesMax {
		return "", nil, nil, cerr(http.StatusForbidden, "device_limit",
			fmt.Sprintf("This review link has already paired %d devices. Revoke one in lasso, or ask for a new link.", clientReviewDevicesMax))
	}
	token := clientRandomToken(clientTokenPrefix, 32)
	d := &appDevice{ID: clientID("d_", 8), UserID: u.ID, Label: label, Platform: platform, PairedBy: rv.MintedBy,
		CreatedAt: nowStamp(now), LastSeenAt: nowStamp(now)}
	if _, err := tx.Exec(`INSERT INTO app_devices (id, user_id, token_hash, label, platform, paired_by, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.UserID, tokenHash(token), d.Label, d.Platform, d.PairedBy, d.CreatedAt, d.LastSeenAt); err != nil {
		return "", nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, nil, err
	}
	return token, d, u, nil
}

// --- the owner's API ----------------------------------------------------------------------

// clientReviewPatch is update_settings' client_review: the PUT body of
// /api/client-review.
type clientReviewPatch struct {
	Enabled *bool  `json:"enabled" jsonschema:"true turns review access on (or re-mints its link) and returns the link once; false turns it off and deletes the demo person's devices, threads, messages and files"`
	Days    *int   `json:"days,omitempty" jsonschema:"how long the pairing link lives, 1-365 days from now (default 30)"`
	BaseURL string `json:"base_url,omitempty" jsonschema:"the public origin the phone reaches lasso at, such as https://lasso.example.com; the link is built on it"`
}

// serveClientReview is /api/client-review (behind UI_AUTH, off machine
// callers, like /api/client-push and /api/app-users):
//
//	GET  /api/client-review       off/on, expiry, days, device count; never the code
//	PUT  /api/client-review       {"enabled":true,"days":30} enables (or re-mints) and returns the link once
//	                              {"enabled":false} turns it off and deletes everything of the demo person
func serveClientReview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"review": clientReviewView(now)})
	case http.MethodPut, http.MethodPost:
		var in struct {
			Enabled *bool  `json:"enabled"`
			Days    *int   `json:"days"`
			BaseURL string `json:"base_url"`
		}
		if !decodeBody(w, r, &in) {
			return
		}
		if in.Enabled == nil {
			http.Error(w, "enabled is required: true to turn review access on, false to turn it off", http.StatusBadRequest)
			return
		}
		if !*in.Enabled {
			if err := disableClientReview(now); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"review": clientReviewView(now)})
			return
		}
		base := requestOrigin(r)
		if strings.TrimSpace(in.BaseURL) != "" {
			var err error
			if base, err = pairingBase(in.BaseURL); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		days := 0
		if in.Days != nil {
			days = *in.Days
		}
		out, err := enableClientReview(days, pairingMinter(r), base, now)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		clientHeaders(w)
		writeJSON(w, out)
	default:
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
	}
}

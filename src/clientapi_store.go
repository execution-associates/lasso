package main

// The phone-app client API's storage (docs/design/client-api.md): app users,
// their grants to bots, their paired devices, one-time pairings, and the
// conversations, messages, events, attachments and calls of the API itself.
//
// Everything here is keyed by ids lasso made. A credential is a device token,
// stored only as its SHA-256; a conversation is one user's with one bot, and
// every call that reaches one checks the token's user against it.

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const clientSchema = `
CREATE TABLE IF NOT EXISTS app_users (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	email      TEXT NOT NULL DEFAULT '',
	disabled   INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS app_grants (
	user_id    TEXT NOT NULL,
	bot        TEXT NOT NULL,
	owner      INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	PRIMARY KEY (user_id, bot)
);
CREATE INDEX IF NOT EXISTS app_grants_bot ON app_grants(bot);
CREATE TABLE IF NOT EXISTS app_devices (
	id           TEXT PRIMARY KEY,
	user_id      TEXT NOT NULL,
	token_hash   TEXT NOT NULL UNIQUE,
	label        TEXT NOT NULL DEFAULT '',
	platform     TEXT NOT NULL DEFAULT '',
	paired_by    TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	last_seen_at TEXT NOT NULL DEFAULT '',
	revoked_at   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS app_devices_user ON app_devices(user_id);
CREATE TABLE IF NOT EXISTS app_pairings (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	code_hash  TEXT NOT NULL UNIQUE,
	minted_by  TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	used_at    TEXT NOT NULL DEFAULT '',
	device_id  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS app_convs (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	bot        TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (user_id, bot)
);
CREATE TABLE IF NOT EXISTS app_messages (
	seq           INTEGER PRIMARY KEY AUTOINCREMENT,
	id            TEXT NOT NULL UNIQUE,
	conv_id       TEXT NOT NULL,
	role          TEXT NOT NULL,
	text          TEXT NOT NULL,
	done          INTEGER NOT NULL DEFAULT 1,
	mode          TEXT NOT NULL DEFAULT 'text',
	call_id       TEXT NOT NULL DEFAULT '',
	attachments   TEXT NOT NULL DEFAULT '[]',
	state         TEXT NOT NULL DEFAULT '',
	event_id      INTEGER NOT NULL DEFAULT 0,
	failed_reason TEXT NOT NULL DEFAULT '',
	client_id     TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS app_messages_conv ON app_messages(conv_id, seq);
CREATE UNIQUE INDEX IF NOT EXISTS app_messages_client_id ON app_messages(conv_id, client_id) WHERE client_id != '';
CREATE TABLE IF NOT EXISTS app_events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	conv_id    TEXT NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS app_events_conv ON app_events(conv_id, seq);
CREATE TABLE IF NOT EXISTS app_attachments (
	id         TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL,
	bot        TEXT NOT NULL,
	conv_id    TEXT NOT NULL,
	direction  TEXT NOT NULL,
	name       TEXT NOT NULL,
	mime       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	path       TEXT NOT NULL,
	message_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS app_attachments_conv ON app_attachments(conv_id);
CREATE TABLE IF NOT EXISTS app_calls (
	id          TEXT PRIMARY KEY,
	conv_id     TEXT NOT NULL,
	user_id     TEXT NOT NULL,
	bot         TEXT NOT NULL,
	state       TEXT NOT NULL,
	end_reason  TEXT NOT NULL DEFAULT '',
	started_at  TEXT NOT NULL,
	ended_at    TEXT NOT NULL DEFAULT '',
	touched_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS app_calls_conv ON app_calls(conv_id, state);
`

const (
	clientTokenPrefix  = "lcd_"
	clientPairPrefix   = "lpc_"
	clientPairTTL      = 10 * time.Minute
	clientNameMax      = 64
	clientEmailMax     = 254
	clientLabelMax     = 64
	clientEventKeep    = 48 * time.Hour
	clientCallIdle     = 30 * time.Minute
	clientCallMaxAge   = 4 * time.Hour
	clientOrphanUpload = 24 * time.Hour
)

var (
	errClientNotFound = errors.New("not found")
	errClientInvalid  = errors.New("invalid request")
)

// --- ids and tokens -----------------------------------------------------------

func clientID(prefix string, nBytes int) string { return prefix + randHex(nBytes) }

// clientRandomToken is prefix + 32 random bytes, base64url.
func clientRandomToken(prefix string, nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

// hashEqual compares two hex hashes in constant time.
func hashEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func cleanLine(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return s, true
}

// --- users ----------------------------------------------------------------------

type appUser struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

const appUserCols = `id, name, email, disabled, created_at, updated_at`

func scanAppUser(row interface{ Scan(...any) error }) (*appUser, error) {
	var u appUser
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	return &u, err
}

func createAppUser(name, email string, now time.Time) (*appUser, error) {
	n, ok := cleanLine(name, clientNameMax)
	if !ok {
		return nil, fmt.Errorf("name must be 1-%d characters, one line", clientNameMax)
	}
	email = strings.TrimSpace(email)
	if email != "" {
		if e, ok := cleanLine(email, clientEmailMax); !ok || !strings.Contains(e, "@") {
			return nil, errors.New("email is not valid")
		}
	}
	u := &appUser{ID: clientID("u_", 8), Name: n, Email: email, CreatedAt: nowStamp(now), UpdatedAt: nowStamp(now)}
	_, err := db.Exec(`INSERT INTO app_users (id, name, email, disabled, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`,
		u.ID, u.Name, u.Email, u.CreatedAt, u.UpdatedAt)
	return u, err
}

func getAppUser(id string) (*appUser, error) {
	return scanAppUser(db.QueryRow(`SELECT `+appUserCols+` FROM app_users WHERE id = ?`, id))
}

func listAppUsers() ([]*appUser, error) {
	rows, err := db.Query(`SELECT ` + appUserCols + ` FROM app_users ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*appUser{}
	for rows.Next() {
		u, err := scanAppUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

type appUserPatch struct {
	Name     *string `json:"name"`
	Email    *string `json:"email"`
	Disabled *bool   `json:"disabled"`
}

func updateAppUser(id string, p appUserPatch, now time.Time) (*appUser, error) {
	u, err := getAppUser(id)
	if err != nil {
		return nil, err
	}
	if p.Name != nil {
		n, ok := cleanLine(*p.Name, clientNameMax)
		if !ok {
			return nil, fmt.Errorf("name must be 1-%d characters, one line", clientNameMax)
		}
		u.Name = n
	}
	if p.Email != nil {
		e := strings.TrimSpace(*p.Email)
		if e != "" {
			if c, ok := cleanLine(e, clientEmailMax); !ok || !strings.Contains(c, "@") {
				return nil, errors.New("email is not valid")
			}
		}
		u.Email = e
	}
	if p.Disabled != nil {
		u.Disabled = *p.Disabled
	}
	u.UpdatedAt = nowStamp(now)
	_, err = db.Exec(`UPDATE app_users SET name = ?, email = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		u.Name, u.Email, boolInt(u.Disabled), u.UpdatedAt, id)
	return u, err
}

// deleteAppUser removes a user and everything of theirs: devices, grants,
// pairings, conversations with their messages, events, attachments and calls.
// The files of their uploads stay in the bots' folders for botClientForget to
// remove; it returns the rows it dropped so the caller can.
func deleteAppUser(id string) ([]appAttachment, error) {
	atts, _ := listAppAttachmentsOfUser(id)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM app_events WHERE conv_id IN (SELECT id FROM app_convs WHERE user_id = ?)`,
		`DELETE FROM app_messages WHERE conv_id IN (SELECT id FROM app_convs WHERE user_id = ?)`,
		`DELETE FROM app_calls WHERE user_id = ?`,
		`DELETE FROM app_attachments WHERE user_id = ?`,
		`DELETE FROM app_convs WHERE user_id = ?`,
		`DELETE FROM app_pairings WHERE user_id = ?`,
		`DELETE FROM app_devices WHERE user_id = ?`,
		`DELETE FROM app_grants WHERE user_id = ?`,
		`DELETE FROM app_users WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return nil, err
		}
	}
	return atts, tx.Commit()
}

// deleteClientDataOfBot forgets a deleted bot's grants and conversations.
func deleteClientDataOfBot(bot string) {
	for _, q := range []string{
		`DELETE FROM app_events WHERE conv_id IN (SELECT id FROM app_convs WHERE bot = ?)`,
		`DELETE FROM app_messages WHERE conv_id IN (SELECT id FROM app_convs WHERE bot = ?)`,
		`DELETE FROM app_calls WHERE bot = ?`,
		`DELETE FROM app_attachments WHERE bot = ?`,
		`DELETE FROM app_convs WHERE bot = ?`,
		`DELETE FROM app_grants WHERE bot = ?`,
	} {
		_, _ = db.Exec(q, bot)
	}
}

// --- grants ---------------------------------------------------------------------

type appGrant struct {
	UserID    string `json:"user_id,omitempty"`
	Bot       string `json:"bot"`
	Owner     bool   `json:"owner"`
	CreatedAt string `json:"created_at"`
}

func setAppGrant(userID, bot string, owner bool, now time.Time) (*appGrant, error) {
	if _, err := getAppUser(userID); err != nil {
		return nil, err
	}
	if _, err := getBot(bot); err != nil {
		return nil, fmt.Errorf("no bot named %q", bot)
	}
	_, err := db.Exec(`INSERT INTO app_grants (user_id, bot, owner, created_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, bot) DO UPDATE SET owner = excluded.owner`, userID, bot, boolInt(owner), nowStamp(now))
	if err != nil {
		return nil, err
	}
	return getAppGrant(userID, bot)
}

func getAppGrant(userID, bot string) (*appGrant, error) {
	var g appGrant
	err := db.QueryRow(`SELECT user_id, bot, owner, created_at FROM app_grants WHERE user_id = ? AND bot = ?`, userID, bot).
		Scan(&g.UserID, &g.Bot, &g.Owner, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	return &g, err
}

func deleteAppGrant(userID, bot string) error {
	res, err := db.Exec(`DELETE FROM app_grants WHERE user_id = ? AND bot = ?`, userID, bot)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errClientNotFound
	}
	return nil
}

func listAppGrants(userID string) ([]appGrant, error) {
	rows, err := db.Query(`SELECT bot, owner, created_at FROM app_grants WHERE user_id = ? ORDER BY bot`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appGrant{}
	for rows.Next() {
		var g appGrant
		if err := rows.Scan(&g.Bot, &g.Owner, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- devices and pairings -------------------------------------------------------

type appDevice struct {
	ID         string `json:"id"`
	UserID     string `json:"user_id,omitempty"`
	Label      string `json:"label"`
	Platform   string `json:"platform"`
	PairedBy   string `json:"paired_by"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	RevokedAt  string `json:"revoked_at,omitempty"`
}

func listAppDevices(userID string) ([]appDevice, error) {
	rows, err := db.Query(`SELECT id, user_id, label, platform, paired_by, created_at, last_seen_at, revoked_at FROM app_devices WHERE user_id = ? ORDER BY created_at, id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appDevice{}
	for rows.Next() {
		var d appDevice
		if err := rows.Scan(&d.ID, &d.UserID, &d.Label, &d.Platform, &d.PairedBy, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func revokeAppDevice(userID, deviceID string, now time.Time) error {
	res, err := db.Exec(`UPDATE app_devices SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at = ''`, nowStamp(now), deviceID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errClientNotFound
	}
	return nil
}

type appPairing struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	MintedBy  string `json:"minted_by"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	UsedAt    string `json:"used_at,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
}

// mintAppPairing makes a one-time code for the user. The code is returned once
// and stored only as its hash.
func mintAppPairing(userID, mintedBy string, now time.Time) (string, *appPairing, error) {
	u, err := getAppUser(userID)
	if err != nil {
		return "", nil, err
	}
	if u.Disabled {
		return "", nil, errors.New("the user is disabled")
	}
	code := clientRandomToken(clientPairPrefix, 16)
	p := &appPairing{ID: clientID("p_", 8), UserID: userID, MintedBy: mintedBy, CreatedAt: nowStamp(now), ExpiresAt: nowStamp(now.Add(clientPairTTL))}
	_, err = db.Exec(`INSERT INTO app_pairings (id, user_id, code_hash, minted_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, userID, tokenHash(code), mintedBy, p.CreatedAt, p.ExpiresAt)
	return code, p, err
}

func listAppPairings(userID string, now time.Time) ([]appPairing, error) {
	rows, err := db.Query(`SELECT id, user_id, minted_by, created_at, expires_at, used_at, device_id FROM app_pairings
		WHERE user_id = ? AND created_at > ? ORDER BY created_at DESC`, userID, nowStamp(now.Add(-24*time.Hour)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []appPairing{}
	for rows.Next() {
		var p appPairing
		if err := rows.Scan(&p.ID, &p.UserID, &p.MintedBy, &p.CreatedAt, &p.ExpiresAt, &p.UsedAt, &p.DeviceID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func cancelAppPairing(userID, id string) error {
	res, err := db.Exec(`DELETE FROM app_pairings WHERE id = ? AND user_id = ? AND used_at = ''`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errClientNotFound
	}
	return nil
}

// errPairingRefused is every way a pairing can fail: unknown code, expired,
// already used, user gone or disabled, bad body. One error, so nothing says
// which.
var errPairingRefused = errors.New("pairing refused")

// redeemAppPairing exchanges a code for a device token. The code's use and the
// device's creation are one transaction, so a code works once even raced.
func redeemAppPairing(code, label, platform string, now time.Time) (token string, dev *appDevice, user *appUser, err error) {
	code = strings.TrimSpace(code)
	l, okLabel := cleanLine(label, clientLabelMax)
	if !okLabel {
		l = "phone"
	}
	pl := strings.ToLower(strings.TrimSpace(platform))
	if pl != "ios" && pl != "android" {
		pl = "other"
	}
	if !strings.HasPrefix(code, clientPairPrefix) || len(code) > 64 {
		return "", nil, nil, errPairingRefused
	}
	tx, err := db.Begin()
	if err != nil {
		return "", nil, nil, err
	}
	defer tx.Rollback()
	var p appPairing
	var storedHash string
	err = tx.QueryRow(`SELECT id, user_id, code_hash, minted_by, expires_at FROM app_pairings WHERE code_hash = ? AND used_at = ''`, tokenHash(code)).
		Scan(&p.ID, &p.UserID, &storedHash, &p.MintedBy, &p.ExpiresAt)
	if err != nil || !hashEqual(storedHash, tokenHash(code)) {
		return "", nil, nil, errPairingRefused
	}
	if exp, ok := parseStamp(p.ExpiresAt); !ok || !now.Before(exp) {
		return "", nil, nil, errPairingRefused
	}
	u, err := scanAppUser(tx.QueryRow(`SELECT `+appUserCols+` FROM app_users WHERE id = ?`, p.UserID))
	if err != nil || u.Disabled {
		return "", nil, nil, errPairingRefused
	}
	token = clientRandomToken(clientTokenPrefix, 32)
	d := &appDevice{ID: clientID("d_", 8), UserID: u.ID, Label: l, Platform: pl, PairedBy: p.MintedBy, CreatedAt: nowStamp(now), LastSeenAt: nowStamp(now)}
	res, err := tx.Exec(`UPDATE app_pairings SET used_at = ?, device_id = ? WHERE id = ? AND used_at = ''`, nowStamp(now), d.ID, p.ID)
	if err != nil {
		return "", nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", nil, nil, errPairingRefused
	}
	if _, err := tx.Exec(`INSERT INTO app_devices (id, user_id, token_hash, label, platform, paired_by, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.UserID, tokenHash(token), d.Label, d.Platform, d.PairedBy, d.CreatedAt, d.LastSeenAt); err != nil {
		return "", nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, nil, err
	}
	return token, d, u, nil
}

// clientIdentity is who a request's token proves: a device of an enabled user.
type clientIdentity struct {
	User   *appUser
	Device appDevice
}

// authenticateDeviceToken finds the device a token belongs to. A malformed,
// unknown, revoked or disabled-user token all read the same: no identity.
func authenticateDeviceToken(tok string, now time.Time) (*clientIdentity, bool) {
	if !strings.HasPrefix(tok, clientTokenPrefix) || len(tok) > 128 {
		return nil, false
	}
	h := tokenHash(tok)
	var id clientIdentity
	var stored string
	var disabled bool
	u := &appUser{}
	err := db.QueryRow(`SELECT d.id, d.user_id, d.token_hash, d.label, d.platform, d.paired_by, d.created_at, d.last_seen_at,
			u.id, u.name, u.email, u.disabled, u.created_at, u.updated_at
		FROM app_devices d JOIN app_users u ON u.id = d.user_id WHERE d.token_hash = ? AND d.revoked_at = ''`, h).
		Scan(&id.Device.ID, &id.Device.UserID, &stored, &id.Device.Label, &id.Device.Platform, &id.Device.PairedBy, &id.Device.CreatedAt, &id.Device.LastSeenAt,
			&u.ID, &u.Name, &u.Email, &disabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil || !hashEqual(stored, h) || disabled {
		return nil, false
	}
	u.Disabled = disabled
	id.User = u
	// last_seen is a courtesy for the owner's device list: once a minute is
	// plenty, and a read-heavy app should not write on every request.
	if seen, ok := parseStamp(id.Device.LastSeenAt); !ok || now.Sub(seen) >= time.Minute {
		_, _ = db.Exec(`UPDATE app_devices SET last_seen_at = ? WHERE id = ?`, nowStamp(now), id.Device.ID)
	}
	return &id, true
}

// stillAuthorized re-checks a stream's identity: the device unrevoked, the
// user enabled and still granted the bot.
func stillAuthorized(deviceID, userID, bot string) bool {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM app_devices d JOIN app_users u ON u.id = d.user_id
		JOIN app_grants g ON g.user_id = u.id AND g.bot = ?
		WHERE d.id = ? AND d.user_id = ? AND d.revoked_at = '' AND u.disabled = 0`, bot, deviceID, userID).Scan(&n)
	return err == nil && n == 1
}

// --- conversations ----------------------------------------------------------------

type appConv struct {
	ID        string
	UserID    string
	Bot       string
	CreatedAt string
}

func convFor(userID, bot string, now time.Time) (*appConv, error) {
	c := &appConv{ID: clientID("c_", 12), UserID: userID, Bot: bot, CreatedAt: nowStamp(now)}
	if _, err := db.Exec(`INSERT OR IGNORE INTO app_convs (id, user_id, bot, created_at) VALUES (?, ?, ?, ?)`, c.ID, userID, bot, c.CreatedAt); err != nil {
		return nil, err
	}
	err := db.QueryRow(`SELECT id, user_id, bot, created_at FROM app_convs WHERE user_id = ? AND bot = ?`, userID, bot).
		Scan(&c.ID, &c.UserID, &c.Bot, &c.CreatedAt)
	return c, err
}

func convByID(id string) (*appConv, error) {
	var c appConv
	err := db.QueryRow(`SELECT id, user_id, bot, created_at FROM app_convs WHERE id = ?`, id).Scan(&c.ID, &c.UserID, &c.Bot, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	return &c, err
}

// --- messages and events ----------------------------------------------------------

type clientAttachmentView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type clientMessageView struct {
	ID             string                 `json:"id"`
	ConversationID string                 `json:"conversation_id"`
	Cursor         string                 `json:"cursor"`
	Role           string                 `json:"role"` // user | bot
	Text           string                 `json:"text"`
	Done           bool                   `json:"done"`
	Mode           string                 `json:"mode"`
	CallID         string                 `json:"call_id,omitempty"`
	Attachments    []clientAttachmentView `json:"attachments"`
	State          string                 `json:"state,omitempty"` // user messages: queued | delivered | failed
	FailedReason   string                 `json:"failed_reason,omitempty"`
	CreatedAt      string                 `json:"created_at"`
}

const appMessageCols = `seq, id, conv_id, role, text, done, mode, call_id, attachments, state, failed_reason, created_at`

func scanAppMessage(row interface{ Scan(...any) error }) (clientMessageView, error) {
	var m clientMessageView
	var seq int64
	var atts string
	if err := row.Scan(&seq, &m.ID, &m.ConversationID, &m.Role, &m.Text, &m.Done, &m.Mode, &m.CallID, &atts, &m.State, &m.FailedReason, &m.CreatedAt); err != nil {
		return m, err
	}
	m.Cursor = fmt.Sprint(seq)
	m.Attachments = []clientAttachmentView{}
	_ = json.Unmarshal([]byte(atts), &m.Attachments)
	if m.Attachments == nil {
		m.Attachments = []clientAttachmentView{}
	}
	if m.Role == "bot" {
		m.State = ""
	}
	return m, nil
}

func messageByID(convID, id string) (clientMessageView, error) {
	m, err := scanAppMessage(db.QueryRow(`SELECT `+appMessageCols+` FROM app_messages WHERE conv_id = ? AND id = ?`, convID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return m, errClientNotFound
	}
	return m, err
}

// pageMessages answers up to limit messages of a conversation, oldest first:
// those before the cursor (older), or after it (newer). Neither: the newest.
func pageMessages(convID string, before, after int64, limit int) ([]clientMessageView, error) {
	var rows *sql.Rows
	var err error
	asc := false
	switch {
	case after > 0:
		asc = true
		rows, err = db.Query(`SELECT `+appMessageCols+` FROM app_messages WHERE conv_id = ? AND seq > ? ORDER BY seq LIMIT ?`, convID, after, limit)
	case before > 0:
		rows, err = db.Query(`SELECT `+appMessageCols+` FROM app_messages WHERE conv_id = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, convID, before, limit)
	default:
		rows, err = db.Query(`SELECT `+appMessageCols+` FROM app_messages WHERE conv_id = ? ORDER BY seq DESC LIMIT ?`, convID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []clientMessageView{}
	for rows.Next() {
		m, err := scanAppMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !asc {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// clientWake wakes the streams of a conversation as soon as an event lands.
// Another lasso on the same db appends without waking this one; every stream
// also looks again once a second.
var clientWake = newWakeSet()

// appendClientEvent records a persisted stream event and wakes the streams.
func appendClientEvent(convID, typ string, data any, now time.Time) int64 {
	b, _ := json.Marshal(data)
	res, err := db.Exec(`INSERT INTO app_events (conv_id, type, data, created_at) VALUES (?, ?, ?, ?)`, convID, typ, string(b), nowStamp(now))
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	clientWake.notify(convID)
	return id
}

type clientEventRow struct {
	Seq  int64
	Type string
	Data string
}

func eventsAfter(convID string, after int64, limit int) ([]clientEventRow, error) {
	rows, err := db.Query(`SELECT seq, type, data FROM app_events WHERE conv_id = ? AND seq > ? ORDER BY seq LIMIT ?`, convID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clientEventRow
	for rows.Next() {
		var e clientEventRow
		if err := rows.Scan(&e.Seq, &e.Type, &e.Data); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func oldestEventSeq(convID string) int64 {
	var s sql.NullInt64
	_ = db.QueryRow(`SELECT MIN(seq) FROM app_events WHERE conv_id = ?`, convID).Scan(&s)
	return s.Int64
}

func newestEventSeq(convID string) int64 {
	var s sql.NullInt64
	_ = db.QueryRow(`SELECT MAX(seq) FROM app_events WHERE conv_id = ?`, convID).Scan(&s)
	return s.Int64
}

// addBotMessage stores a bot's message in a conversation and announces it.
func addBotMessage(c *appConv, text string, done bool, callID string, atts []clientAttachmentView, now time.Time) (clientMessageView, error) {
	if atts == nil {
		atts = []clientAttachmentView{}
	}
	ab, _ := json.Marshal(atts)
	mode := "text"
	if callID != "" {
		mode = "voice"
	}
	id := clientID("m_", 12)
	if _, err := db.Exec(`INSERT INTO app_messages (id, conv_id, role, text, done, mode, call_id, attachments, state, created_at)
		VALUES (?, ?, 'bot', ?, ?, ?, ?, ?, '', ?)`, id, c.ID, text, boolInt(done), mode, callID, string(ab), nowStamp(now)); err != nil {
		return clientMessageView{}, err
	}
	for _, a := range atts {
		_, _ = db.Exec(`UPDATE app_attachments SET message_id = ? WHERE id = ? AND conv_id = ?`, id, a.ID, c.ID)
	}
	m, err := messageByID(c.ID, id)
	if err != nil {
		return m, err
	}
	appendClientEvent(c.ID, "message", m, now)
	if callID != "" {
		_, _ = db.Exec(`UPDATE app_calls SET touched_at = ? WHERE id = ? AND state = 'open'`, nowStamp(now), callID)
	}
	return m, nil
}

// --- attachments ------------------------------------------------------------------

type appAttachment struct {
	ID        string
	UserID    string
	Bot       string
	ConvID    string
	Direction string // in (the person's) | out (the bot's)
	Name      string
	Mime      string
	Size      int64
	Path      string // relative to the bot's folder
	MessageID string
	CreatedAt string
}

const appAttachmentCols = `id, user_id, bot, conv_id, direction, name, mime, size, path, message_id, created_at`

func scanAppAttachment(row interface{ Scan(...any) error }) (appAttachment, error) {
	var a appAttachment
	err := row.Scan(&a.ID, &a.UserID, &a.Bot, &a.ConvID, &a.Direction, &a.Name, &a.Mime, &a.Size, &a.Path, &a.MessageID, &a.CreatedAt)
	return a, err
}

func (a appAttachment) view() clientAttachmentView {
	return clientAttachmentView{ID: a.ID, Name: a.Name, Type: a.Mime, Size: a.Size, URL: "/client/v1/attachments/" + a.ID}
}

func insertAppAttachment(a appAttachment) error {
	_, err := db.Exec(`INSERT INTO app_attachments (`+appAttachmentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.Bot, a.ConvID, a.Direction, a.Name, a.Mime, a.Size, a.Path, a.MessageID, a.CreatedAt)
	return err
}

func appAttachmentByID(id string) (appAttachment, error) {
	a, err := scanAppAttachment(db.QueryRow(`SELECT `+appAttachmentCols+` FROM app_attachments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, errClientNotFound
	}
	return a, err
}

func listAppAttachmentsOfUser(userID string) ([]appAttachment, error) {
	rows, err := db.Query(`SELECT `+appAttachmentCols+` FROM app_attachments WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []appAttachment
	for rows.Next() {
		a, err := scanAppAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- calls --------------------------------------------------------------------------

type appCall struct {
	ID        string `json:"id"`
	ConvID    string `json:"conversation_id"`
	UserID    string `json:"-"`
	Bot       string `json:"-"`
	State     string `json:"state"` // open | ended
	EndReason string `json:"end_reason,omitempty"`
	StartedAt string `json:"started_at"`
	EndedAt   string `json:"ended_at,omitempty"`
}

const appCallCols = `id, conv_id, user_id, bot, state, end_reason, started_at, ended_at`

func scanAppCall(row interface{ Scan(...any) error }) (*appCall, error) {
	var c appCall
	err := row.Scan(&c.ID, &c.ConvID, &c.UserID, &c.Bot, &c.State, &c.EndReason, &c.StartedAt, &c.EndedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errClientNotFound
	}
	return &c, err
}

func appCallByID(id string) (*appCall, error) {
	return scanAppCall(db.QueryRow(`SELECT `+appCallCols+` FROM app_calls WHERE id = ?`, id))
}

func openCallOf(convID string) (*appCall, error) {
	return scanAppCall(db.QueryRow(`SELECT `+appCallCols+` FROM app_calls WHERE conv_id = ? AND state = 'open' ORDER BY started_at DESC LIMIT 1`, convID))
}

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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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
CREATE TABLE IF NOT EXISTS app_reply_tokens (
	token_hash TEXT PRIMARY KEY,
	conv_id    TEXT NOT NULL,
	bot        TEXT NOT NULL,
	message_id TEXT NOT NULL,
	call_id    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	closed_at  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS app_reply_tokens_conv ON app_reply_tokens(conv_id);
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

// cleanLine validates a one-line label (a display name, a device label, an
// email): UTF-8, 1..max characters, and none of the characters that could
// reshape the markup or the header a bot reads it in (< > " \\), a control
// character, or an invisible formatting character (a bidi override, a line or
// paragraph separator).
func cleanLine(s string, max int) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if clientHostileRune(r) {
			return "", false
		}
	}
	return s, true
}

// clientHostileRune is a character that has no place in a name or a label:
// controls, invisible formatting and separators, and the characters of markup
// and quoting.
func clientHostileRune(r rune) bool {
	switch r {
	case '<', '>', '"', '\\', '`', 0x7f:
		return true
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) ||
		unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r)
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
		// Messages of theirs still waiting for a bot go with them.
		`DELETE FROM bot_events WHERE kind = 'client' AND json_extract(meta, '$.user_id') = ?`,
		`DELETE FROM app_reply_tokens WHERE conv_id IN (SELECT id FROM app_convs WHERE user_id = ?)`,
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
		`DELETE FROM app_reply_tokens WHERE bot = ?`,
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
	// What the person sent this bot and the bot has not taken yet is not
	// delivered: the grant it was sent under is gone.
	dropClientEvents(`bot = ? AND json_extract(meta, '$.user_id') = ?`, bot, userID)
	return nil
}

// dropClientEvents withdraws the client messages a bot has not taken yet (or
// has taken but not acknowledged) that match where: the row is kept as dropped,
// so the person's message reads as failed, but its text and attributes are
// blanked. A spool file already written to another host is withdrawn by the
// spool loop when it finds its event no longer claimed.
func dropClientEvents(where string, args ...any) {
	_, _ = db.Exec(`UPDATE bot_events SET status = 'dropped', reason = 'access revoked', content = '', meta = '', done_at = ?
		WHERE kind = 'client' AND status IN ('pending', 'claimed') AND `+where, append([]any{nowStamp(time.Now())}, args...)...)
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
	// Messages the revoked device sent that no bot has taken yet are not
	// delivered.
	dropClientEvents(`json_extract(meta, '$.device_id') = ?`, deviceID)
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
	// Width and Height are the pixel size of an image, read from its header;
	// absent when the file is not an image or its size could not be read.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`
	// URL is absent once the file has expired (Expired is then true): the
	// message keeps the name, type and size to show a "file expired" marker.
	URL     string `json:"url,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

type clientMessageView struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id"`
	Cursor         string `json:"cursor"`
	Role           string `json:"role"` // user | bot
	Text           string `json:"text"`
	Done           bool   `json:"done"`
	Mode           string `json:"mode"`
	CallID         string `json:"call_id,omitempty"`
	// ClientID is the client_id the sending client gave a person's message; only
	// the person's own messages carry one, and only that person ever reads them.
	ClientID string `json:"client_id,omitempty"`
	// InReplyTo is the id of the person's message a bot message answers.
	InReplyTo    string                 `json:"in_reply_to,omitempty"`
	Attachments  []clientAttachmentView `json:"attachments"`
	State        string                 `json:"state,omitempty"` // user messages: queued | delivered | failed
	FailedReason string                 `json:"failed_reason,omitempty"`
	CreatedAt    string                 `json:"created_at"`
}

const appMessageCols = `seq, id, conv_id, role, text, done, mode, call_id, attachments, state, failed_reason, client_id, in_reply_to, created_at`

func scanAppMessage(row interface{ Scan(...any) error }) (clientMessageView, error) {
	var m clientMessageView
	var seq int64
	var atts string
	if err := row.Scan(&seq, &m.ID, &m.ConversationID, &m.Role, &m.Text, &m.Done, &m.Mode, &m.CallID, &atts, &m.State, &m.FailedReason, &m.ClientID, &m.InReplyTo, &m.CreatedAt); err != nil {
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
		m.ClientID = ""
	} else {
		m.InReplyTo = ""
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
	return addBotReply(c, text, done, callID, "", atts, now)
}

// addBotReply is addBotMessage for a message that answers one of the person's:
// inReplyTo is that message's id, callID the call it was sent in (empty for a
// text message, whatever call happens to be open).
func addBotReply(c *appConv, text string, done bool, callID, inReplyTo string, atts []clientAttachmentView, now time.Time) (clientMessageView, error) {
	if atts == nil {
		atts = []clientAttachmentView{}
	}
	ab, _ := json.Marshal(atts)
	mode := "text"
	if callID != "" {
		mode = "voice"
	}
	id := clientID("m_", 12)
	if _, err := db.Exec(`INSERT INTO app_messages (id, conv_id, role, text, done, mode, call_id, attachments, state, in_reply_to, created_at)
		VALUES (?, ?, 'bot', ?, ?, ?, ?, ?, '', ?, ?)`, id, c.ID, text, boolInt(done), mode, callID, string(ab), inReplyTo, nowStamp(now)); err != nil {
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
	Width     int    // pixels, for an image whose header could be read
	Height    int
	MessageID string
	CreatedAt string
	ExpiredAt string // set once the file was deleted by the retention window
}

const appAttachmentCols = `id, user_id, bot, conv_id, direction, name, mime, size, path, width, height, message_id, created_at, expired_at`

func scanAppAttachment(row interface{ Scan(...any) error }) (appAttachment, error) {
	var a appAttachment
	err := row.Scan(&a.ID, &a.UserID, &a.Bot, &a.ConvID, &a.Direction, &a.Name, &a.Mime, &a.Size, &a.Path, &a.Width, &a.Height, &a.MessageID, &a.CreatedAt, &a.ExpiredAt)
	return a, err
}

func (a appAttachment) view() clientAttachmentView {
	if a.ExpiredAt != "" {
		return clientAttachmentView{ID: a.ID, Name: a.Name, Type: a.Mime, Size: a.Size, Width: a.Width, Height: a.Height, Expired: true}
	}
	return clientAttachmentView{ID: a.ID, Name: a.Name, Type: a.Mime, Size: a.Size, Width: a.Width, Height: a.Height, URL: "/client/v1/attachments/" + a.ID}
}

func insertAppAttachment(a appAttachment) error {
	_, err := db.Exec(`INSERT INTO app_attachments (`+appAttachmentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.UserID, a.Bot, a.ConvID, a.Direction, a.Name, a.Mime, a.Size, a.Path, a.Width, a.Height, a.MessageID, a.CreatedAt, a.ExpiredAt)
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

// callOfMessage is the call a message was sent in ("" for a text message or an
// unknown id).
func callOfMessage(convID, msgID string) string {
	if msgID == "" {
		return ""
	}
	var call string
	_ = db.QueryRow(`SELECT call_id FROM app_messages WHERE conv_id = ? AND id = ?`, convID, msgID).Scan(&call)
	return call
}

// --- reply tokens -------------------------------------------------------------------
//
// Every message lasso hands a claude bot carries a reply_token: random, made
// for that message, delivered only in that message's attributes. client_reply
// must present the token of a message of the conversation it writes to that is
// still open, and end_call one of the call's conversation. A bot that is
// steered by one person, and so has never been shown another person's tokens,
// cannot write into that other person's conversation.

const (
	replyTokenPrefix = "lrt_"
	// clientReplyWindow is how long after delivery a message stays open to
	// answers. A message may wait in the queue for up to a day before the bot
	// takes it; the window starts when the bot does.
	clientReplyWindow = 6 * time.Hour
	// clientReplyQueued bounds a token whose message was never delivered.
	clientReplyQueued = 26 * time.Hour
)

var errReplyToken = errors.New("client_reply needs the reply_token of a message from this conversation that you have not finished answering: the one in the attributes of the message you are handling, used while it is open (a reply with done=true closes it, and it expires six hours after it reached you)")

type replyToken struct {
	ConvID, Bot, MessageID, CallID string
	Expires                        time.Time
	Closed                         bool
}

// issueReplyToken makes the token for one person's message.
func issueReplyToken(conv *appConv, msgID, callID string, now time.Time) string {
	tok := clientRandomToken(replyTokenPrefix, 18)
	_, _ = db.Exec(`INSERT INTO app_reply_tokens (token_hash, conv_id, bot, message_id, call_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		tokenHash(tok), conv.ID, conv.Bot, msgID, callID, nowStamp(now), nowStamp(now.Add(clientReplyQueued)))
	return tok
}

// startReplyWindow opens the answering window when the bot takes a message.
func startReplyWindow(tok string, now time.Time) {
	if tok == "" {
		return
	}
	_, _ = db.Exec(`UPDATE app_reply_tokens SET expires_at = ? WHERE token_hash = ? AND closed_at = ''`, nowStamp(now.Add(clientReplyWindow)), tokenHash(tok))
}

func lookupReplyToken(tok string) (replyToken, bool) {
	var t replyToken
	var exp, closed string
	if !strings.HasPrefix(tok, replyTokenPrefix) || len(tok) > 64 {
		return t, false
	}
	h := tokenHash(tok)
	var stored string
	err := db.QueryRow(`SELECT token_hash, conv_id, bot, message_id, call_id, expires_at, closed_at FROM app_reply_tokens WHERE token_hash = ?`, h).
		Scan(&stored, &t.ConvID, &t.Bot, &t.MessageID, &t.CallID, &exp, &closed)
	if err != nil || !hashEqual(stored, h) {
		return t, false
	}
	t.Expires, _ = parseStamp(exp)
	t.Closed = closed != ""
	return t, true
}

// checkReplyToken says whether tok lets bot write into conv now: it belongs to
// that conversation and bot, has not expired, and (unless allowClosed, which
// hanging up uses after the answer is out) has not been closed by a final
// reply.
func checkReplyToken(tok, bot, convID string, allowClosed bool, now time.Time) error {
	t, ok := lookupReplyToken(strings.TrimSpace(tok))
	if !ok || t.Bot != bot || t.ConvID != convID || !now.Before(t.Expires) || (t.Closed && !allowClosed) {
		return errReplyToken
	}
	return nil
}

// closeReplyToken consumes the token on a final reply; false when it was
// already closed (a racing second final reply).
func closeReplyToken(tok string, now time.Time) bool {
	res, err := db.Exec(`UPDATE app_reply_tokens SET closed_at = ? WHERE token_hash = ? AND closed_at = ''`, nowStamp(now), tokenHash(strings.TrimSpace(tok)))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}

func reopenReplyToken(tok string) {
	_, _ = db.Exec(`UPDATE app_reply_tokens SET closed_at = '' WHERE token_hash = ?`, tokenHash(strings.TrimSpace(tok)))
}

// --- quotas and retention ------------------------------------------------------------

// The defaults are the documented limits; the environment can change them
// (LASSO_CLIENT_QUOTA_MB, LASSO_CLIENT_QUOTA_FILES, LASSO_CLIENT_RETENTION_DAYS).
const (
	clientQuotaMBDefault       = 500
	clientQuotaFilesDefault    = 3000
	clientRetentionDaysDefault = 30
	clientOutFilesPerConv      = 200
	clientOutBytesPerConv      = 200 << 20
	clientOutBytesPerBot       = 2 << 30
	clientCallsPerMin          = 6
	clientCallsPerHour         = 60
	clientQuotaMBEnv           = "LASSO_CLIENT_QUOTA_MB"
	clientQuotaFilesEnv        = "LASSO_CLIENT_QUOTA_FILES"
	clientRetentionDaysEnv     = "LASSO_CLIENT_RETENTION_DAYS"
	clientQuotaMsg             = "Your storage for this app is full. Uploaded files are deleted after %d days; wait for older ones to expire, or ask the owner."
	clientQuotaCode            = "quota_exceeded"
)

func envPositive(name string, def int) int {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func clientQuotaBytes() int64 {
	return int64(envPositive(clientQuotaMBEnv, clientQuotaMBDefault)) << 20
}
func clientQuotaFiles() int { return envPositive(clientQuotaFilesEnv, clientQuotaFilesDefault) }
func clientRetention() time.Duration {
	return time.Duration(envPositive(clientRetentionDaysEnv, clientRetentionDaysDefault)) * 24 * time.Hour
}

// clientQuotaMu serialises the check and the insert of a stored file, so two
// uploads at once cannot both pass the last free byte.
var clientQuotaMu sync.Mutex

// userStoredUploads is what a person's uploads occupy now (expired ones no
// longer count).
func userStoredUploads(userID string) (bytes int64, files int) {
	_ = db.QueryRow(`SELECT COALESCE(SUM(size), 0), COUNT(*) FROM app_attachments WHERE user_id = ? AND direction = 'in' AND expired_at = ''`, userID).Scan(&bytes, &files)
	return
}

// expireAttachment deletes nothing itself: it marks the row expired and
// rewrites the message that shows it, so the app can render a "file expired"
// marker. The caller removes the file.
func expireAttachment(a appAttachment, now time.Time) {
	res, err := db.Exec(`UPDATE app_attachments SET expired_at = ?, path = '' WHERE id = ? AND expired_at = ''`, nowStamp(now), a.ID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n != 1 || a.MessageID == "" {
		return
	}
	var raw string
	if db.QueryRow(`SELECT attachments FROM app_messages WHERE id = ?`, a.MessageID).Scan(&raw) != nil {
		return
	}
	var views []clientAttachmentView
	if json.Unmarshal([]byte(raw), &views) != nil {
		return
	}
	for i := range views {
		if views[i].ID == a.ID {
			views[i] = a.view()
			views[i].Expired = true
			views[i].URL = ""
		}
	}
	b, _ := json.Marshal(views)
	_, _ = db.Exec(`UPDATE app_messages SET attachments = ? WHERE id = ?`, string(b), a.MessageID)
}

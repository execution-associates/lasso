package main

// Alert channels: where a bot's liveness alerts (bothealth.go) go. Six kinds,
// each a dispatcher over one shared outbound client:
//
//	webhook    a signed JSON POST (X-Lasso-Signature, HMAC-SHA256)
//	slack      an incoming webhook
//	discord    a channel webhook
//	telegram   a bot's sendMessage to a chat
//	email      SMTP (STARTTLS, implicit TLS, or plain on a trusted relay)
//	heartbeat  a ping URL (Monicron, healthchecks.io…) pinged while its bot is
//	           up; silence is the alert, so it also covers lasso itself dying
//
// A channel's secret fields (URLs that are themselves credentials, tokens,
// passwords) are encrypted at rest with a key derived from lasso's per-install
// age identity, decrypted only to send, and never returned: a listing says
// which secret fields are set, and a save that leaves one out keeps it.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// alertEvent is what a channel is told. It is also the webhook body.
type alertEvent struct {
	ID          string `json:"id"`
	Type        string `json:"type"` // down | recovered | reminder | test
	Bot         string `json:"bot,omitempty"`
	Host        string `json:"host,omitempty"`
	State       string `json:"state,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Since       string `json:"since,omitempty"`
	At          string `json:"at"`
	DowntimeSec int64  `json:"downtime_sec,omitempty"`
	// Path is the bot's page in lasso; URL is it made absolute with the
	// alerts' link_base, when one is set.
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
}

func (ev alertEvent) subject() string {
	switch ev.Type {
	case alertTest:
		return "lasso: test alert"
	case alertRecovered:
		return fmt.Sprintf("%s recovered", ev.Bot)
	case alertReminder:
		return fmt.Sprintf("%s is still %s", ev.Bot, ev.State)
	}
	return fmt.Sprintf("%s is %s", ev.Bot, ev.State)
}

// detail is the event's body as plain text lines.
func (ev alertEvent) detail() []string {
	if ev.Type == alertTest {
		return []string{"This is a test from lasso's bot alerts. If you can read it, the channel works."}
	}
	var out []string
	if ev.Reason != "" {
		out = append(out, ev.Reason)
	}
	if ev.Type == alertRecovered && ev.DowntimeSec > 0 {
		out = append(out, "Down for "+humanDuration(time.Duration(ev.DowntimeSec)*time.Second)+".")
	}
	if ev.Type != alertRecovered && ev.Since != "" {
		out = append(out, "Down since "+ev.Since+".")
	}
	if ev.Host != "" && ev.Host != "local" {
		out = append(out, "Host: "+ev.Host)
	}
	if ev.URL != "" {
		out = append(out, ev.URL)
	}
	return out
}

// --- kinds -----------------------------------------------------------------------

// alertField is one field of a kind's form. Every value is a string; Secret
// fields are write-only.
type alertField struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Secret   bool     `json:"secret,omitempty"`
	Required bool     `json:"required,omitempty"`
	Help     string   `json:"help,omitempty"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
}

type alertKind struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Help   string       `json:"help"`
	Fields []alertField `json:"fields"`
	// validate checks the merged values (config and secrets alike).
	validate func(v map[string]string) error
	// target is a hint safe to show: never a secret.
	target func(v map[string]string) string
	send   func(ctx context.Context, v map[string]string, ev alertEvent) error
}

var alertKinds = []alertKind{
	{
		ID: "webhook", Label: "Webhook",
		Help: "A signed JSON POST to any URL. Verify X-Lasso-Signature: sha256=HMAC-SHA256(secret, X-Lasso-Timestamp + \".\" + body).",
		Fields: []alertField{
			{Key: "url", Label: "URL", Secret: true, Required: true},
			{Key: "signing_secret", Label: "Signing secret", Secret: true, Help: "Signs every request; leave empty to send unsigned."},
			{Key: "authorization", Label: "Authorization header", Secret: true, Help: "Sent as-is, e.g. Bearer …"},
		},
		validate: func(v map[string]string) error { return checkAlertURL(v["url"]) },
		target:   func(v map[string]string) string { return urlHostHint(v["url"]) },
		send:     sendWebhook,
	},
	{
		ID: "slack", Label: "Slack", Help: "A Slack incoming webhook.",
		Fields:   []alertField{{Key: "webhook_url", Label: "Webhook URL", Secret: true, Required: true}},
		validate: func(v map[string]string) error { return checkAlertURL(v["webhook_url"]) },
		target:   func(v map[string]string) string { return urlHostHint(v["webhook_url"]) },
		send:     sendSlack,
	},
	{
		ID: "discord", Label: "Discord", Help: "A Discord channel webhook.",
		Fields:   []alertField{{Key: "webhook_url", Label: "Webhook URL", Secret: true, Required: true}},
		validate: func(v map[string]string) error { return checkAlertURL(v["webhook_url"]) },
		target:   func(v map[string]string) string { return urlHostHint(v["webhook_url"]) },
		send:     sendDiscord,
	},
	{
		ID: "telegram", Label: "Telegram", Help: "A message from your Telegram bot to a chat.",
		Fields: []alertField{
			{Key: "chat_id", Label: "Chat ID", Required: true, Help: "A numeric id, or @channelname."},
			{Key: "thread_id", Label: "Topic ID", Help: "For a forum topic; leave empty otherwise."},
			{Key: "bot_token", Label: "Bot token", Secret: true, Required: true},
		},
		validate: validateTelegram,
		target:   func(v map[string]string) string { return "chat " + v["chat_id"] },
		send:     sendTelegram,
	},
	{
		ID: "email", Label: "Email", Help: "Mail through an SMTP server.",
		Fields: []alertField{
			{Key: "host", Label: "SMTP host", Required: true},
			{Key: "port", Label: "Port", Default: "587"},
			{Key: "security", Label: "Security", Default: "starttls", Options: []string{"starttls", "tls", "none"},
				Help: "starttls (587), tls (465), or none for a relay on a trusted network."},
			{Key: "username", Label: "Username"},
			{Key: "password", Label: "Password", Secret: true},
			{Key: "from", Label: "From", Required: true},
			{Key: "to", Label: "To", Required: true, Help: "One or more addresses, comma-separated."},
		},
		validate: validateEmail,
		target:   func(v map[string]string) string { return v["to"] },
		send:     sendEmail,
	},
	{
		ID: "heartbeat", Label: "Heartbeat",
		Help: "Pinged while one bot is up, so its monitor (Monicron, healthchecks.io) goes down when the bot or lasso does. On an outage it also posts the reason to <url>/report. While the bot is stopped or held, a Monicron monitor is paused (<url>/pause, which needs the ping secret) instead of going down.",
		Fields: []alertField{
			{Key: "bot", Label: "Bot", Required: true},
			{Key: "every", Label: "Ping every (seconds)", Default: "60"},
			{Key: "on_hold", Label: "While stopped or held", Default: "pause", Options: []string{"pause", "silent"},
				Help: "pause: pause the monitor (Monicron's <url>/pause and /resume). silent: stop pinging, so the monitor goes down."},
			{Key: "url", Label: "Ping URL", Secret: true, Required: true},
			{Key: "ping_secret", Label: "Ping secret", Secret: true,
				Help: "Sent as Authorization: Bearer on every request. Monicron requires it once the monitor has one, and always to pause."},
		},
		validate: validateHeartbeat,
		target:   func(v map[string]string) string { return v["bot"] + " → " + urlHostHint(v["url"]) },
		send:     sendHeartbeatEvent,
	},
}

func alertKindByID(id string) *alertKind {
	for i := range alertKinds {
		if alertKinds[i].ID == id {
			return &alertKinds[i]
		}
	}
	return nil
}

func (k *alertKind) field(key string) *alertField {
	for i := range k.Fields {
		if k.Fields[i].Key == key {
			return &k.Fields[i]
		}
	}
	return nil
}

func urlHostHint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// --- storage -----------------------------------------------------------------------

type alertChannel struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Config     map[string]string `json:"config"`
	Enabled    bool              `json:"enabled"`
	SecretsSet []string          `json:"secrets_set"`
	Target     string            `json:"target,omitempty"`
	LastTestAt string            `json:"last_test_at,omitempty"`
	LastError  string            `json:"last_error,omitempty"`
	CreatedAt  string            `json:"created_at"`
	UpdatedAt  string            `json:"updated_at"`
	secrets    map[string]string
}

func (c *alertChannel) values() map[string]string {
	v := map[string]string{}
	for k, x := range c.Config {
		v[k] = x
	}
	for k, x := range c.secrets {
		v[k] = x
	}
	if k := alertKindByID(c.Kind); k != nil {
		for _, f := range k.Fields {
			if v[f.Key] == "" && f.Default != "" {
				v[f.Key] = f.Default
			}
		}
	}
	return v
}

func (c *alertChannel) heartbeatBot() string { return c.Config["bot"] }

func (c *alertChannel) heartbeatEvery() time.Duration {
	n, err := strconv.Atoi(c.Config["every"])
	if err != nil || n <= 0 {
		n = 60
	}
	return time.Duration(n) * time.Second
}

// view fills the fields a client sees.
func (c *alertChannel) view() {
	c.SecretsSet = []string{}
	for k, v := range c.secrets {
		if v != "" {
			c.SecretsSet = append(c.SecretsSet, k)
		}
	}
	slices.Sort(c.SecretsSet)
	if k := alertKindByID(c.Kind); k != nil {
		c.Target = k.target(c.values())
	}
	if c.Config == nil {
		c.Config = map[string]string{}
	}
}

const alertChannelCols = `id, name, kind, config, secret, enabled, last_test_at, last_error, created_at, updated_at`

func scanAlertChannel(row interface{ Scan(...any) error }) (*alertChannel, error) {
	var c alertChannel
	var cfg, sec string
	var enabled int
	if err := row.Scan(&c.ID, &c.Name, &c.Kind, &cfg, &sec, &enabled, &c.LastTestAt, &c.LastError, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	_ = json.Unmarshal([]byte(cfg), &c.Config)
	c.secrets = map[string]string{}
	if sec != "" {
		plain, err := alertDecrypt(sec)
		if err != nil {
			// A key that changed reads as no secrets: the channel shows them
			// unset rather than failing every listing.
			log.Printf("alerts:   channel %d: secrets unreadable: %v", c.ID, err)
		} else {
			_ = json.Unmarshal(plain, &c.secrets)
		}
	}
	c.view()
	return &c, nil
}

// listAlertChannels lists every channel. withSecrets decrypts them (sending
// only); a listing for a client never needs them, but view() needs to know
// which are set, so they are always read and only ever dropped by JSON.
func listAlertChannels(withSecrets bool) ([]*alertChannel, error) {
	_ = withSecrets
	rows, err := db.Query(`SELECT ` + alertChannelCols + ` FROM alert_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*alertChannel
	for rows.Next() {
		c, err := scanAlertChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

var errAlertChannelNotFound = errors.New("no such alert channel")

func getAlertChannel(id int64) (*alertChannel, error) {
	c, err := scanAlertChannel(db.QueryRow(`SELECT `+alertChannelCols+` FROM alert_channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errAlertChannelNotFound
	}
	return c, err
}

// alertChannelInput is a create or save. On a save, a nil Config keeps the
// stored one, and a secret key left out keeps its value ("" clears it).
type alertChannelInput struct {
	Name    *string           `json:"name"`
	Kind    string            `json:"kind"`
	Enabled *bool             `json:"enabled"`
	Config  map[string]string `json:"config"`
	Secrets map[string]string `json:"secrets"`
}

// apply builds the channel the input describes over base (nil for a create).
func (in alertChannelInput) apply(base *alertChannel) (*alertChannel, error) {
	c := &alertChannel{Enabled: true, Config: map[string]string{}, secrets: map[string]string{}}
	if base != nil {
		cp := *base
		c = &cp
		c.Config = map[string]string{}
		for k, v := range base.Config {
			c.Config[k] = v
		}
		c.secrets = map[string]string{}
		for k, v := range base.secrets {
			c.secrets[k] = v
		}
		if in.Kind != "" && in.Kind != base.Kind {
			return nil, errors.New("a channel's kind cannot change; make a new one")
		}
	} else {
		c.Kind = in.Kind
	}
	k := alertKindByID(c.Kind)
	if k == nil {
		return nil, errors.New("kind must be webhook, slack, discord, telegram, email or heartbeat")
	}
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if c.Name == "" {
		c.Name = k.Label
	}
	if len([]rune(c.Name)) > 60 || hasControl(c.Name) {
		return nil, errors.New("name must be at most 60 characters on one line")
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Config != nil {
		c.Config = map[string]string{}
	}
	for key, v := range in.Config {
		f := k.field(key)
		if f == nil || f.Secret {
			return nil, fmt.Errorf("%s has no setting %q", k.Label, key)
		}
		v = strings.TrimSpace(v)
		if len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") || hasControl(v) {
			return nil, fmt.Errorf("%s is not usable", f.Label)
		}
		if v != "" {
			c.Config[key] = v
		}
	}
	for key, v := range in.Secrets {
		f := k.field(key)
		if f == nil || !f.Secret {
			return nil, fmt.Errorf("%s has no secret %q", k.Label, key)
		}
		v = strings.TrimSpace(v)
		if len(v) > 4096 || strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("%s is not usable", f.Label)
		}
		if v == "" {
			delete(c.secrets, key)
		} else {
			c.secrets[key] = v
		}
	}
	vals := c.values()
	for _, f := range k.Fields {
		if f.Required && vals[f.Key] == "" {
			return nil, fmt.Errorf("%s needs %s", k.Label, strings.ToLower(f.Label))
		}
		if len(f.Options) > 0 && vals[f.Key] != "" && !slices.Contains(f.Options, vals[f.Key]) {
			return nil, fmt.Errorf("%s must be one of %s", f.Label, strings.Join(f.Options, ", "))
		}
	}
	if err := k.validate(vals); err != nil {
		return nil, err
	}
	c.view()
	return c, nil
}

func saveAlertChannel(c *alertChannel) error {
	now := nowStamp(time.Now())
	cfg, _ := json.Marshal(c.Config)
	sec := ""
	if len(c.secrets) > 0 {
		plain, _ := json.Marshal(c.secrets)
		var err error
		if sec, err = alertEncrypt(plain); err != nil {
			return err
		}
	}
	c.UpdatedAt = now
	if c.ID == 0 {
		c.CreatedAt = now
		res, err := db.Exec(`INSERT INTO alert_channels (name, kind, config, secret, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.Name, c.Kind, string(cfg), sec, boolInt(c.Enabled), c.CreatedAt, c.UpdatedAt)
		if err != nil {
			return err
		}
		c.ID, _ = res.LastInsertId()
		return nil
	}
	_, err := db.Exec(`UPDATE alert_channels SET name = ?, config = ?, secret = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		c.Name, string(cfg), sec, boolInt(c.Enabled), c.UpdatedAt, c.ID)
	return err
}

// deleteAlertChannel removes it and every reference to it.
func deleteAlertChannel(id int64) error {
	if _, err := db.Exec(`DELETE FROM alert_channels WHERE id = ?`, id); err != nil {
		return err
	}
	g := loadAlertSettings()
	if i := slices.Index(g.Channels, id); i >= 0 {
		g.Channels = slices.Delete(g.Channels, i, i+1)
		b, _ := json.Marshal(g)
		_ = setSetting(alertSettingsKey, string(b))
	}
	list, _ := listBots()
	for _, r := range list {
		if r.Alerts == nil {
			continue
		}
		if i := slices.Index(r.Alerts.Channels, id); i >= 0 {
			r.Alerts.Channels = slices.Delete(r.Alerts.Channels, i, i+1)
			_, _ = db.Exec(`UPDATE bots SET alerts = ? WHERE name = ?`, botAlertsJSON(r.Alerts), r.Name)
		}
	}
	return nil
}

func setAlertChannelStatus(id int64, test bool, errText string) {
	if test {
		_, _ = db.Exec(`UPDATE alert_channels SET last_test_at = ?, last_error = ? WHERE id = ?`, nowStamp(time.Now()), errText, id)
		return
	}
	_, _ = db.Exec(`UPDATE alert_channels SET last_error = ? WHERE id = ?`, errText, id)
}

// --- secrets at rest -----------------------------------------------------------------

// alertKey is AES-256 derived (HKDF-SHA256) from lasso's own age identity on
// this machine, <lassoDir>/age.txt, made when there is none. The identity is
// never replaced, so the key is stable for the install's life.
var alertKeyCache struct {
	sync.Mutex
	path string
	key  []byte
}

func alertKey() ([]byte, error) {
	path := filepath.Join(lassoDir(), "age.txt")
	alertKeyCache.Lock()
	defer alertKeyCache.Unlock()
	if alertKeyCache.path == path && alertKeyCache.key != nil {
		return alertKeyCache.key, nil
	}
	if _, _, err := botAgeRecipient(&localBackend{}); err != nil {
		return nil, fmt.Errorf("lasso's age key: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("lasso's age key: %w", err)
	}
	identity := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(l, "AGE-SECRET-KEY-") {
			identity = l
		}
	}
	if identity == "" {
		return nil, fmt.Errorf("%s holds no AGE-SECRET-KEY line", path)
	}
	key, err := hkdf.Key(sha256.New, []byte(identity), nil, "lasso alert channel secrets v1", 32)
	if err != nil {
		return nil, err
	}
	alertKeyCache.path, alertKeyCache.key = path, key
	return key, nil
}

func alertEncrypt(plain []byte) (string, error) {
	key, err := alertKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

func alertDecrypt(s string) ([]byte, error) {
	enc, ok := strings.CutPrefix(s, "v1:")
	if !ok {
		return nil, errors.New("unknown format")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	key, err := alertKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("too short")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
}

// --- sending ----------------------------------------------------------------------------

// alertHTTP is every channel's client: short, and never following a redirect
// (a webhook that redirects is misconfigured, and following one would carry
// the body somewhere nobody chose).
var alertHTTP = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// telegramAPIBase is Telegram's Bot API. A seam for tests.
var telegramAPIBase = "https://api.telegram.org"

// scrubAlertErr is an error as it may be logged and stored: never a URL (a
// webhook's URL is its credential, a Telegram token rides in the path) and
// never a secret's value.
func scrubAlertErr(err error, secrets map[string]string) string {
	if err == nil {
		return ""
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	s := err.Error()
	for _, v := range secrets {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// postJSON POSTs body and reads at most 4 KiB of the answer; a non-2xx is an
// error carrying the answer's start (scrubbed by the caller).
func postJSON(ctx context.Context, rawURL string, body []byte, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("bad URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lasso/"+lassoSemver)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := alertHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return out, fmt.Errorf("HTTP %d %s", resp.StatusCode, snippet)
	}
	return out, nil
}

// webhookSignature is what X-Lasso-Signature carries: the timestamp is inside
// the MAC so a receiver can refuse a replay older than a few minutes.
func webhookSignature(secret, timestamp string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(timestamp))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func sendWebhook(ctx context.Context, v map[string]string, ev alertEvent) error {
	body, _ := json.Marshal(ev)
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	h := map[string]string{
		"X-Lasso-Event":     ev.Type,
		"X-Lasso-Delivery":  ev.ID,
		"X-Lasso-Timestamp": ts,
	}
	if s := v["signing_secret"]; s != "" {
		h["X-Lasso-Signature"] = webhookSignature(s, ts, body)
	}
	if a := v["authorization"]; a != "" {
		h["Authorization"] = a
	}
	_, err := postJSON(ctx, v["url"], body, h)
	return err
}

func sendSlack(ctx context.Context, v map[string]string, ev alertEvent) error {
	icon := map[string]string{alertDown: ":red_circle:", alertReminder: ":red_circle:", alertRecovered: ":large_green_circle:", alertTest: ":white_check_mark:"}[ev.Type]
	title := icon + " *" + slackEscape(ev.subject()) + "*"
	text := title
	for _, l := range ev.detail() {
		text += "\n" + slackEscape(l)
	}
	body, _ := json.Marshal(map[string]any{
		"text": ev.subject(),
		"blocks": []any{map[string]any{
			"type": "section",
			"text": map[string]string{"type": "mrkdwn", "text": text},
		}},
	})
	_, err := postJSON(ctx, v["webhook_url"], body, nil)
	return err
}

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func sendDiscord(ctx context.Context, v map[string]string, ev alertEvent) error {
	color := map[string]int{alertDown: 0xd93025, alertReminder: 0xd93025, alertRecovered: 0x1e8e3e, alertTest: 0x5865f2}[ev.Type]
	embed := map[string]any{
		"title":       ev.subject(),
		"description": strings.Join(ev.detail(), "\n"),
		"color":       color,
		"timestamp":   ev.At,
	}
	if ev.URL != "" {
		embed["url"] = ev.URL
	}
	body, _ := json.Marshal(map[string]any{
		"embeds":           []any{embed},
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	u, err := url.Parse(v["webhook_url"])
	if err != nil {
		return errors.New("bad URL")
	}
	q := u.Query()
	q.Set("wait", "true")
	u.RawQuery = q.Encode()
	_, err = postJSON(ctx, u.String(), body, nil)
	return err
}

func validateTelegram(v map[string]string) error {
	id := v["chat_id"]
	if _, err := strconv.ParseInt(id, 10, 64); err != nil && !(strings.HasPrefix(id, "@") && len(id) > 1) {
		return errors.New("chat ID must be a number or @channelname")
	}
	if t := v["thread_id"]; t != "" {
		if _, err := strconv.ParseInt(t, 10, 64); err != nil {
			return errors.New("topic ID must be a number")
		}
	}
	if tok := v["bot_token"]; strings.ContainsAny(tok, "/?# ") || !strings.Contains(tok, ":") {
		return errors.New("that is not a Telegram bot token (it looks like 123456:ABC…)")
	}
	return nil
}

func sendTelegram(ctx context.Context, v map[string]string, ev alertEvent) error {
	text := "<b>" + html.EscapeString(ev.subject()) + "</b>"
	for _, l := range ev.detail() {
		text += "\n" + html.EscapeString(l)
	}
	msg := map[string]any{
		"chat_id":                  v["chat_id"],
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if t, err := strconv.ParseInt(v["thread_id"], 10, 64); err == nil {
		msg["message_thread_id"] = t
	}
	body, _ := json.Marshal(msg)
	out, err := postJSON(ctx, telegramAPIBase+"/bot"+v["bot_token"]+"/sendMessage", body, nil)
	if err != nil {
		// Telegram explains a refusal in "description".
		var r struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(out, &r) == nil && r.Description != "" {
			return errors.New("telegram: " + r.Description)
		}
	}
	return err
}

func alertSplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func validateEmail(v map[string]string) error {
	if strings.ContainsAny(v["host"], " /:@") {
		return errors.New("SMTP host must be a host name, without a port")
	}
	if p, err := strconv.Atoi(v["port"]); err != nil || p < 1 || p > 65535 {
		return errors.New("port must be 1-65535")
	}
	if _, err := mail.ParseAddress(v["from"]); err != nil {
		return errors.New("from is not an email address")
	}
	to := alertSplitList(v["to"])
	if len(to) == 0 || len(to) > 20 {
		return errors.New("to must list 1-20 addresses")
	}
	for _, a := range to {
		if _, err := mail.ParseAddress(a); err != nil {
			return fmt.Errorf("%q is not an email address", a)
		}
	}
	if v["password"] != "" && v["username"] == "" {
		return errors.New("a password needs a username")
	}
	return nil
}

// alertSMTPTLS is the TLS config for a server name. A seam for tests.
var alertSMTPTLS = func(host string) *tls.Config { return &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12} }

func sendEmail(ctx context.Context, v map[string]string, ev alertEvent) error {
	host := v["host"]
	addr := net.JoinHostPort(host, v["port"])
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	var conn net.Conn
	var err error
	if v["security"] == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, alertSMTPTLS(host))
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if err := c.Hello("lasso"); err != nil {
		return err
	}
	if v["security"] == "starttls" || v["security"] == "" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS (choose tls for port 465, or none for a trusted relay)")
		}
		if err := c.StartTLS(alertSMTPTLS(host)); err != nil {
			return err
		}
	}
	if user := v["username"]; user != "" {
		// PlainAuth itself refuses to send a password over plaintext to anything
		// but localhost.
		if err := c.Auth(smtp.PlainAuth("", user, v["password"], host)); err != nil {
			return err
		}
	}
	from, _ := mail.ParseAddress(v["from"])
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	var to []string
	for _, a := range alertSplitList(v["to"]) {
		pa, _ := mail.ParseAddress(a)
		to = append(to, pa.String())
		if err := c.Rcpt(pa.Address); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(emailMessage(from.String(), to, ev)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// emailMessage is the RFC 5322 message. Every header value is either from a
// validated config (no CR or LF) or encoded, so nothing can add a header.
func emailMessage(from string, to []string, ev alertEvent) []byte {
	var b strings.Builder
	rnd := make([]byte, 8)
	_, _ = rand.Read(rnd)
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "lasso"
	}
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", "[lasso] "+ev.subject()))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s.%s@%s>\r\n", strconv.FormatInt(time.Now().UnixNano(), 36), hex.EncodeToString(rnd), hostname)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\nAuto-Submitted: auto-generated\r\n\r\n")
	b.WriteString(ev.subject() + "\r\n\r\n")
	for _, l := range ev.detail() {
		b.WriteString(strings.ReplaceAll(l, "\n", "\r\n") + "\r\n")
	}
	return []byte(b.String())
}

func validateHeartbeat(v map[string]string) error {
	if !botNameRE.MatchString(v["bot"]) {
		return errors.New("bot must be a bot's name")
	}
	if n, err := strconv.Atoi(v["every"]); err != nil || n < 30 || n > 86400 {
		return errors.New("ping every must be 30-86400 seconds")
	}
	if v["on_hold"] == "pause" && v["ping_secret"] == "" {
		return errors.New("pausing the monitor needs its ping secret (or set while stopped or held to silent)")
	}
	return checkAlertURL(v["url"])
}

// heartbeatHeaders authenticates a heartbeat's requests with its ping secret.
func heartbeatHeaders(v map[string]string) map[string]string {
	if sec := v["ping_secret"]; sec != "" {
		return map[string]string{"Authorization": "Bearer " + sec}
	}
	return nil
}

func heartbeatSub(v map[string]string, path string) string {
	return strings.TrimRight(v["url"], "/") + "/" + path
}

// sendHeartbeatEvent is a transition on a heartbeat channel: a ping when the
// bot recovers (or for a test), and the outage's reason to <url>/report, which
// Monicron stores without counting it as a ping.
func sendHeartbeatEvent(ctx context.Context, v map[string]string, ev alertEvent) error {
	switch ev.Type {
	case alertRecovered, alertTest:
		return heartbeatPing(ctx, v, ev)
	}
	body, _ := json.Marshal(map[string]any{"status": "down", "bot": ev.Bot, "state": ev.State, "reason": ev.Reason, "since": ev.Since})
	_, err := postJSON(ctx, heartbeatSub(v, "report"), body, heartbeatHeaders(v))
	return err
}

func heartbeatPing(ctx context.Context, v map[string]string, ev alertEvent) error {
	body, _ := json.Marshal(map[string]any{"status": "up", "bot": ev.Bot, "state": ev.State, "since": ev.Since})
	_, err := postJSON(ctx, v["url"], body, heartbeatHeaders(v))
	return err
}

// heartbeatSetPaused pauses the monitor (with the reason as its report) or
// resumes it. A resume pings first and after: a ping to a paused Monicron
// monitor refreshes its window without resuming it, so the resumed monitor
// never sees a stale last ping, and the second ping marks it up. up is false
// when the bot is not up (a hold released on a bot that is gone): then the
// monitor is only resumed, and goes down by itself.
func heartbeatSetPaused(ctx context.Context, v map[string]string, paused, up bool, ev alertEvent, reason string) error {
	h := heartbeatHeaders(v)
	if paused {
		body, _ := json.Marshal(map[string]any{"report": map[string]any{"status": "paused", "bot": ev.Bot, "reason": reason, "at": ev.At}})
		_, err := postJSON(ctx, heartbeatSub(v, "pause"), body, h)
		return err
	}
	if up {
		if err := heartbeatPing(ctx, v, ev); err != nil {
			return err
		}
	}
	if _, err := postJSON(ctx, heartbeatSub(v, "resume"), []byte("{}"), h); err != nil {
		return err
	}
	if up {
		return heartbeatPing(ctx, v, ev)
	}
	return nil
}

// --- heartbeats --------------------------------------------------------------------------

// heartbeatState is per channel: the last ping, whether a request is in
// flight, the last failure, and the pause state lasso last set (absent =
// unknown, so a restarted lasso re-asserts it once; Monicron's pause and resume
// are idempotent).
var heartbeatState = struct {
	sync.Mutex
	last    map[int64]time.Time
	running map[int64]bool
	failing map[int64]string
	paused  map[int64]bool
}{last: map[int64]time.Time{}, running: map[int64]bool{}, failing: map[int64]string{}, paused: map[int64]bool{}}

// heartbeatWantsPause: the bot is down on purpose. A held bot that is alerted
// down (deaf) is not: its monitor must go down with it.
func heartbeatWantsPause(r *botRecord, st botAlertState, now time.Time) (bool, string) {
	switch {
	case r.Stopped:
		return true, "stopped by hand"
	case botHeld(r, now) && !st.Down:
		reason := "held"
		if r.HoldReason != "" {
			reason += ": " + r.HoldReason
		}
		return true, reason
	}
	return false, ""
}

// botHeartbeats keeps every enabled heartbeat bound to r truthful: paused
// while the bot is stopped or held (on_hold pause), silent while it is
// unhealthy or down, and pinged every `every` while it is up.
func botHeartbeats(all []*alertChannel, r *botRecord, st botAlertState, now time.Time) {
	wantPause, reason := heartbeatWantsPause(r, st, now)
	up := st.State == "ok" && !st.Down
	for _, c := range all {
		if !c.Enabled || c.Kind != "heartbeat" || c.heartbeatBot() != r.Name {
			continue
		}
		v := c.values()
		pauseMode := v["on_hold"] != "silent"
		ev := alertEvent{Type: "ping", Bot: r.Name, State: st.State, Since: st.Since, At: nowStamp(now)}

		heartbeatState.Lock()
		if heartbeatState.running[c.ID] {
			heartbeatState.Unlock()
			continue
		}
		// In silent mode lasso never pauses, so the monitor is always "resumed".
		desired := pauseMode && wantPause
		current, known := heartbeatState.paused[c.ID]
		switch {
		case !known || current != desired:
			if !pauseMode && !known {
				// Nothing to assert: a silent heartbeat never paused anything.
				heartbeatState.paused[c.ID] = false
				heartbeatState.Unlock()
				continue
			}
			heartbeatState.running[c.ID] = true
			heartbeatState.last[c.ID] = now
			heartbeatState.Unlock()
			heartbeatRun(c, func(ctx context.Context) error {
				err := heartbeatSetPaused(ctx, v, desired, up, ev, reason)
				// A failed pause is retried next tick (and nothing is pinged
				// meanwhile). A failed resume is not: pings must go on, and
				// the error stays on the channel for the human to see.
				if err == nil || !desired {
					heartbeatState.Lock()
					heartbeatState.paused[c.ID] = desired
					heartbeatState.Unlock()
				}
				return err
			})
			continue
		case wantPause || !up:
			heartbeatState.Unlock()
			continue
		case now.Sub(heartbeatState.last[c.ID]) < c.heartbeatEvery():
			heartbeatState.Unlock()
			continue
		}
		heartbeatState.running[c.ID] = true
		heartbeatState.last[c.ID] = now
		heartbeatState.Unlock()
		heartbeatRun(c, func(ctx context.Context) error { return heartbeatPing(ctx, v, ev) })
	}
}

// heartbeatRun does one heartbeat request in the background. Only a change in
// its outcome is written and logged: a dead monitor would otherwise log every
// minute.
func heartbeatRun(c *alertChannel, do func(context.Context) error) {
	alertWG.Add(1)
	go func() {
		defer alertWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		msg := scrubAlertErr(do(ctx), c.secrets)
		heartbeatState.Lock()
		heartbeatState.running[c.ID] = false
		changed := heartbeatState.failing[c.ID] != msg
		heartbeatState.failing[c.ID] = msg
		heartbeatState.Unlock()
		if changed {
			setAlertChannelStatus(c.ID, false, msg)
			if msg != "" {
				log.Printf("alerts:   heartbeat %q: %s", c.Name, msg)
			}
		}
	}()
}

// --- dispatch ------------------------------------------------------------------------------

// alertRetryDelays is the wait before each attempt. A seam for tests.
var alertRetryDelays = []time.Duration{0, 10 * time.Second, 60 * time.Second}

var alertWG sync.WaitGroup // tests wait for deliveries

func newAlertID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// dispatchAlert sends ev to each channel in the background, retrying, and
// records each outcome in alert_deliveries.
func dispatchAlert(ev alertEvent, channels []*alertChannel) {
	if ev.ID == "" {
		ev.ID = newAlertID()
	}
	if ev.URL == "" && ev.Path != "" {
		if base := loadAlertSettings().LinkBase; base != "" {
			ev.URL = strings.TrimRight(base, "/") + ev.Path
		}
	}
	for _, c := range channels {
		alertWG.Add(1)
		go func(c *alertChannel) {
			defer alertWG.Done()
			attempts, err := deliverAlert(c, ev)
			recordAlertDelivery(c, ev, attempts, err)
		}(c)
	}
}

func deliverAlert(c *alertChannel, ev alertEvent) (int, error) {
	k := alertKindByID(c.Kind)
	if k == nil {
		return 0, errors.New("unknown kind")
	}
	var err error
	for i, d := range alertRetryDelays {
		if d > 0 {
			time.Sleep(d)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		err = k.send(ctx, c.values(), ev)
		cancel()
		if err == nil {
			return i + 1, nil
		}
	}
	return len(alertRetryDelays), err
}

func recordAlertDelivery(c *alertChannel, ev alertEvent, attempts int, err error) {
	status, msg := "sent", ""
	if err != nil {
		status, msg = "failed", scrubAlertErr(err, c.secrets)
		log.Printf("alerts:   %s to %q failed: %s", ev.Type, c.Name, msg)
	}
	_, _ = db.Exec(`INSERT INTO alert_deliveries (at, bot, channel_id, channel_name, channel_kind, event, status, error, attempts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nowStamp(time.Now()), ev.Bot, c.ID, c.Name, c.Kind, ev.Type, status, msg, attempts)
	_, _ = db.Exec(`DELETE FROM alert_deliveries WHERE id <= (SELECT id FROM alert_deliveries ORDER BY id DESC LIMIT 1 OFFSET 1000)`)
	if ev.Type != alertTest {
		setAlertChannelStatus(c.ID, false, msg)
	}
}

// testAlertChannel sends a test now, once, and reports the outcome.
func testAlertChannel(c *alertChannel) error {
	k := alertKindByID(c.Kind)
	if k == nil {
		return errors.New("unknown kind")
	}
	ev := alertEvent{ID: newAlertID(), Type: alertTest, At: nowStamp(time.Now())}
	if c.Kind == "heartbeat" {
		ev.Bot = c.heartbeatBot()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	err := k.send(ctx, c.values(), ev)
	if err != nil {
		return errors.New(scrubAlertErr(err, c.secrets))
	}
	return nil
}

type alertDelivery struct {
	ID          int64  `json:"id"`
	At          string `json:"at"`
	Bot         string `json:"bot,omitempty"`
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	ChannelKind string `json:"channel_kind"`
	Event       string `json:"event"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	Attempts    int    `json:"attempts"`
}

func listAlertDeliveries(bot string, limit int) []alertDelivery {
	q := `SELECT id, at, bot, channel_id, channel_name, channel_kind, event, status, error, attempts FROM alert_deliveries`
	args := []any{}
	if bot != "" {
		q += ` WHERE bot = ?`
		args = append(args, bot)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return []alertDelivery{}
	}
	defer rows.Close()
	out := []alertDelivery{}
	for rows.Next() {
		var d alertDelivery
		if rows.Scan(&d.ID, &d.At, &d.Bot, &d.ChannelID, &d.ChannelName, &d.ChannelKind, &d.Event, &d.Status, &d.Error, &d.Attempts) == nil {
			out = append(out, d)
		}
	}
	return out
}

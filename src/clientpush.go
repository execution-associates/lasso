package main

// Push notifications to the phone apps (docs/design/client-threads.md, "Push
// notifications"): APNs (token auth, HTTP/2) for iPhones and FCM HTTP v1
// (data-only) for Android, to a person's devices that are not in the
// foreground, when a bot writes a finished message or proposes a bot.
//
// Credentials are named in the client_push setting, never stored: a variable
// in lasso's environment or an op:// reference, read with lookupSecret and
// held in memory. With nothing configured nothing runs. A device token is a
// bearer capability to buzz a phone: it is never returned to a client or the
// owner and never logged; a push logs the provider, the reason code and the
// device id only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	clientPushSettingKey   = "client_push"
	clientPushDefaultTopic = "com.orangecountyai.Lasso.ios"
	clientPushQueueMax     = 1000
	clientPushQuietWindow  = 30 * time.Second
	clientPushBadgeEvery   = 60 * time.Second
	clientPushPreviewMax   = 180
	clientPresencePerMin   = 60
)

// clientPushRetries are the waits before the second and third attempt.
var clientPushRetries = []time.Duration{2 * time.Second, 10 * time.Second}

// --- configuration -------------------------------------------------------------

type clientPushConfig struct {
	APNs struct {
		TeamID string `json:"team_id,omitempty"`
		KeyID  string `json:"key_id,omitempty"`
		Topic  string `json:"topic,omitempty"`
		KeyRef string `json:"key_ref,omitempty"`
	} `json:"apns"`
	FCM struct {
		CredentialsRef string `json:"credentials_ref,omitempty"`
	} `json:"fcm"`
}

func loadClientPushConfig() clientPushConfig {
	var c clientPushConfig
	if db == nil {
		return c
	}
	if raw, _ := getSetting(clientPushSettingKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

var apnsIDRE = regexp.MustCompile(`^[A-Z0-9]{10}$`)
var bundleIDRE = regexp.MustCompile(`^[A-Za-z0-9.-]{1,155}$`)

func (c clientPushConfig) validate() error {
	a := c.APNs
	if a.TeamID != "" && !apnsIDRE.MatchString(a.TeamID) {
		return errors.New("apns.team_id: the 10-character Apple team id")
	}
	if a.KeyID != "" && !apnsIDRE.MatchString(a.KeyID) {
		return errors.New("apns.key_id: the 10-character key id of the .p8 key")
	}
	if a.Topic != "" && !bundleIDRE.MatchString(a.Topic) {
		return errors.New("apns.topic: the app's bundle id")
	}
	if a.KeyRef != "" && !validKeyRef(a.KeyRef) {
		return errors.New("apns.key_ref: an environment variable name or an op://vault/item/field reference")
	}
	if c.FCM.CredentialsRef != "" && !validKeyRef(c.FCM.CredentialsRef) {
		return errors.New("fcm.credentials_ref: an environment variable name or an op://vault/item/field reference")
	}
	return nil
}

func (c clientPushConfig) topic() string {
	if c.APNs.Topic != "" {
		return c.APNs.Topic
	}
	return clientPushDefaultTopic
}

// Provider states.
const (
	pushUnconfigured = "unconfigured"
	pushUnreadable   = "unreadable"
	pushReady        = "ready"
	pushFailing      = "failing"
)

// clientPushSecret resolves a key reference; a var so tests stand in for it.
var clientPushSecret = lookupSecret

type pushSecretEntry struct {
	ref, value string
	err        error
	at         time.Time
}

var pushSecrets = struct {
	sync.Mutex
	m map[string]pushSecretEntry
}{m: map[string]pushSecretEntry{}}

// pushSecret is a provider's secret: the APNs key held until its reference
// changes, the FCM credentials for ten minutes; a failure is remembered a
// minute, so a status read does not run `op read` on every request.
func pushSecret(provider, ref string, ttl time.Duration) (string, error) {
	pushSecrets.Lock()
	e, ok := pushSecrets.m[provider]
	pushSecrets.Unlock()
	if ok && e.ref == ref {
		if e.err != nil && time.Since(e.at) < time.Minute {
			return "", e.err
		}
		if e.err == nil && (ttl == 0 || time.Since(e.at) < ttl) {
			return e.value, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opReadTimeout+time.Second)
	defer cancel()
	v, err := clientPushSecret(ctx, ref)
	pushSecrets.Lock()
	pushSecrets.m[provider] = pushSecretEntry{ref: ref, value: v, err: err, at: time.Now()}
	pushSecrets.Unlock()
	return v, err
}

func resetPushSecrets() {
	pushSecrets.Lock()
	pushSecrets.m = map[string]pushSecretEntry{}
	pushSecrets.Unlock()
	apnsJWTs.Lock()
	apnsJWTs.m = map[string]apnsJWT{}
	apnsJWTs.Unlock()
	fcmTokens.Lock()
	fcmTokens.m = map[string]fcmAccessToken{}
	fcmTokens.Unlock()
}

// pushFailingNow marks a provider refused for lasso's credentials, until a
// send through it works.
var pushFailingNow sync.Map // provider -> reason

// clientPushState is a provider's state and, when not ready, why.
func clientPushState(provider string) (state, reason string) {
	c := loadClientPushConfig()
	switch provider {
	case "apns":
		if c.APNs.KeyRef == "" || c.APNs.TeamID == "" || c.APNs.KeyID == "" {
			return pushUnconfigured, ""
		}
		pem, err := pushSecret("apns", c.APNs.KeyRef, 0)
		if err != nil {
			return pushUnreadable, missingKeyHint(c.APNs.KeyRef, err)
		}
		if _, err := parseAPNsKey(pem); err != nil {
			return pushUnreadable, "the APNs key is not a .p8 private key"
		}
	case "fcm":
		if c.FCM.CredentialsRef == "" {
			return pushUnconfigured, ""
		}
		raw, err := pushSecret("fcm", c.FCM.CredentialsRef, 10*time.Minute)
		if err != nil {
			return pushUnreadable, missingKeyHint(c.FCM.CredentialsRef, err)
		}
		if _, err := parseFCMCredentials(raw); err != nil {
			return pushUnreadable, "the Firebase credentials are not a service account JSON"
		}
	default:
		return pushUnconfigured, ""
	}
	if r, ok := pushFailingNow.Load(provider); ok {
		return pushFailing, r.(string)
	}
	return pushReady, ""
}

// pushConfigured is the cheap check (no secret read): a provider has its
// references set.
func pushConfigured(provider string) bool {
	c := loadClientPushConfig()
	switch provider {
	case "apns":
		return c.APNs.KeyRef != "" && c.APNs.TeamID != "" && c.APNs.KeyID != ""
	case "fcm":
		return c.FCM.CredentialsRef != ""
	}
	return false
}

func providerForPlatform(platform string) string {
	switch platform {
	case "ios":
		return "apns"
	case "android":
		return "fcm"
	}
	return "none"
}

// --- tokens --------------------------------------------------------------------

type appPushToken struct {
	ID, DeviceID, UserID, Provider, Token, Environment, AppID string
	CreatedAt, UpdatedAt, LastOKAt, LastError, LastErrorAt    string
}

const appPushTokenCols = `id, device_id, user_id, provider, token, environment, app_id, created_at, updated_at, last_ok_at, last_error, last_error_at`

func scanPushToken(row interface{ Scan(...any) error }) (appPushToken, error) {
	var t appPushToken
	err := row.Scan(&t.ID, &t.DeviceID, &t.UserID, &t.Provider, &t.Token, &t.Environment, &t.AppID, &t.CreatedAt, &t.UpdatedAt, &t.LastOKAt, &t.LastError, &t.LastErrorAt)
	return t, err
}

func pushTokenOfDevice(deviceID string) (appPushToken, bool) {
	t, err := scanPushToken(db.QueryRow(`SELECT `+appPushTokenCols+` FROM app_push_tokens WHERE device_id = ?`, deviceID))
	return t, err == nil
}

func deletePushTokenOfDevice(deviceID string) {
	_, _ = db.Exec(`DELETE FROM app_push_tokens WHERE device_id = ?`, deviceID)
}

var (
	apnsTokenRE = regexp.MustCompile(`^[0-9A-Fa-f]{64,200}$`)
	fcmTokenRE  = regexp.MustCompile(`^[A-Za-z0-9:_-]+$`)
)

// clientPushView is /me's push: what this device can get.
func clientPushView(id *clientIdentity) map[string]any {
	provider := providerForPlatform(id.Device.Platform)
	out := map[string]any{"provider": provider, "available": false, "registered": false}
	if tok, ok := pushTokenOfDevice(id.Device.ID); ok {
		out["registered"] = true
		if tok.Provider == "apns" && tok.Environment != "" {
			out["environment"] = tok.Environment
		}
	}
	if provider == "none" {
		out["reason"] = "unsupported_platform"
		return out
	}
	switch st, _ := clientPushState(provider); st {
	case pushReady, pushFailing:
		out["available"] = true
	case pushUnreadable:
		out["reason"] = "unreadable"
	default:
		out["reason"] = "not_configured"
	}
	return out
}

func serveClientDevicePush(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	switch r.Method {
	case http.MethodPut:
		var in struct {
			Provider    string `json:"provider"`
			Token       string `json:"token"`
			Environment string `json:"environment"`
			AppID       string `json:"app_id"`
		}
		if !clientDecode(w, r, 8<<10, &in) {
			return
		}
		want := providerForPlatform(id.Device.Platform)
		if in.Provider != want || want == "none" {
			clientError(w, http.StatusBadRequest, "invalid_request", "provider must be apns on an iPhone and fcm on Android.")
			return
		}
		env := ""
		switch in.Provider {
		case "apns":
			if !apnsTokenRE.MatchString(in.Token) {
				clientError(w, http.StatusBadRequest, "invalid_request", "An APNs token is 64-200 hexadecimal characters.")
				return
			}
			in.Token = strings.ToLower(in.Token)
			if in.Environment != "production" && in.Environment != "sandbox" {
				clientError(w, http.StatusBadRequest, "invalid_request", "environment must be production or sandbox.")
				return
			}
			env = in.Environment
		case "fcm":
			if len(in.Token) > 4096 || !fcmTokenRE.MatchString(in.Token) {
				clientError(w, http.StatusBadRequest, "invalid_request", "An FCM token is 1-4096 of A-Za-z0-9:_-.")
				return
			}
		}
		if in.AppID != "" && !bundleIDRE.MatchString(in.AppID) {
			clientError(w, http.StatusBadRequest, "invalid_request", "app_id must be a bundle or application id.")
			return
		}
		stamp := nowStamp(time.Now())
		tx, err := db.Begin()
		if err != nil {
			clientFail(w, err)
			return
		}
		defer tx.Rollback()
		// One token per device; a token registered to another device moves here.
		if _, err := tx.Exec(`DELETE FROM app_push_tokens WHERE token = ? AND device_id != ?`, in.Token, id.Device.ID); err != nil {
			clientFail(w, err)
			return
		}
		if _, err := tx.Exec(`INSERT INTO app_push_tokens (id, device_id, user_id, provider, token, environment, app_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id) DO UPDATE SET provider = excluded.provider, token = excluded.token, environment = excluded.environment,
				app_id = excluded.app_id, updated_at = excluded.updated_at, last_error = '', last_error_at = ''`,
			clientID("pt_", 8), id.Device.ID, id.User.ID, in.Provider, in.Token, env, in.AppID, stamp, stamp); err != nil {
			clientFail(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			clientFail(w, err)
			return
		}
		clientJSON(w, http.StatusOK, map[string]any{"push": clientPushView(id)})
	case http.MethodDelete:
		deletePushTokenOfDevice(id.Device.ID)
		clientJSON(w, http.StatusOK, map[string]any{"push": clientPushView(id)})
	default:
		clientMethod(w, "PUT, DELETE")
	}
}

func serveClientPresence(w http.ResponseWriter, r *http.Request, id *clientIdentity) {
	var in struct {
		State string `json:"state"`
	}
	if !clientDecode(w, r, 4<<10, &in) {
		return
	}
	if in.State != "foreground" && in.State != "background" {
		clientError(w, http.StatusBadRequest, "invalid_request", "state must be foreground or background.")
		return
	}
	now := time.Now()
	if !clientLimits.allow("presence:"+id.Device.ID, clientPresencePerMin, time.Minute, now) {
		w.Header().Set("Retry-After", "30")
		clientError(w, http.StatusTooManyRequests, "rate_limited", "Slow down.")
		return
	}
	_, _ = db.Exec(`UPDATE app_devices SET presence = ?, presence_at = ? WHERE id = ?`, in.State, nowStamp(now), id.Device.ID)
	clientJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deviceForeground: the person stream heartbeat is fresh and the app says it
// is in front.
func deviceForeground(presence, seen string, now time.Time) bool {
	t, ok := parseStamp(seen)
	return presence == "foreground" && ok && now.Sub(t) < clientForegroundWindow
}

// --- deciding ------------------------------------------------------------------

type pushTarget struct {
	tok      appPushToken
	platform string
}

// pushTargets are a person's devices that have a token, are not revoked and
// are not in the foreground.
func pushTargets(userID string, now time.Time) []pushTarget {
	rows, err := db.Query(`SELECT `+prefixCols("p.", appPushTokenCols)+`, d.platform, d.presence, d.stream_seen_at FROM app_push_tokens p
		JOIN app_devices d ON d.id = p.device_id WHERE p.user_id = ? AND d.user_id = ? AND d.revoked_at = ''`, userID, userID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []pushTarget
	for rows.Next() {
		var t appPushToken
		var platform, presence, seen string
		if rows.Scan(&t.ID, &t.DeviceID, &t.UserID, &t.Provider, &t.Token, &t.Environment, &t.AppID, &t.CreatedAt, &t.UpdatedAt, &t.LastOKAt, &t.LastError, &t.LastErrorAt,
			&platform, &presence, &seen) != nil {
			continue
		}
		if deviceForeground(presence, seen, now) {
			continue
		}
		out = append(out, pushTarget{tok: t, platform: platform})
	}
	return out
}

// pushNote is what a push says, before it is shaped for a provider.
type pushNote struct {
	Kind       string // message | proposal | badge | test
	ThreadID   string
	MessageID  string
	ProposalID string
	Bot        string
	Title      string
	Subtitle   string
	Body       string
	Badge      int
	Quiet      bool
}

var pushLastSent = struct {
	sync.Mutex
	thread map[string]time.Time // device + thread -> last push
	badge  map[string]time.Time // device -> last badge-only push
	badgeN map[string]int       // user -> last badge pushed
}{thread: map[string]time.Time{}, badge: map[string]time.Time{}, badgeN: map[string]int{}}

// clientEventAppended is where pushes are decided: a bot's finished message
// and a new proposal, in a thread that is not muted.
func clientEventAppended(threadID, userID, typ string, data any, now time.Time) {
	if userID == "" || (!pushConfigured("apns") && !pushConfigured("fcm")) {
		return
	}
	switch typ {
	case "message":
		m, ok := data.(clientMessageView)
		if !ok || m.Role != "bot" || !m.Done {
			return
		}
		t, err := threadByID(threadID)
		if err != nil || t.Muted {
			return
		}
		body := clientPreview(m.Text, clientPushPreviewMax)
		if body == "" {
			switch n := len(m.Attachments); {
			case n == 1 && strings.HasPrefix(m.Attachments[0].Type, "image/"):
				body = "Sent a photo"
			case n == 1:
				body = "Sent a file"
			case n > 1:
				body = fmt.Sprintf("Sent %d files", n)
			}
		}
		clientPushNote(t, pushNote{Kind: "message", ThreadID: t.ID, MessageID: m.ID, Bot: m.Bot, Title: m.Bot, Body: body}, "New message", now)
	case "proposal_created":
		mp, _ := data.(map[string]any)
		p, ok := mp["proposal"].(clientProposalView)
		if !ok {
			return
		}
		t, err := threadByID(threadID)
		if err != nil || t.Muted {
			return
		}
		clientPushNote(t, pushNote{Kind: "proposal", ThreadID: t.ID, ProposalID: p.ID, Bot: p.By, Title: p.By, Body: "Suggests adding " + p.Bot}, "New suggestion", now)
	}
}

func clientPushNote(t *appThread, n pushNote, blind string, now time.Time) {
	if !clientPushPreviews(t.UserID) {
		n.Body = blind
	} else if !t.isMain() {
		n.Subtitle = t.Title
	}
	_, n.Badge = clientUnreadBadge(t.UserID)
	pushLastSent.Lock()
	pushLastSent.badgeN[t.UserID] = n.Badge
	pushLastSent.Unlock()
	for _, tg := range pushTargets(t.UserID, now) {
		note := n
		key := tg.tok.DeviceID + "\x00" + t.ID
		pushLastSent.Lock()
		if last, ok := pushLastSent.thread[key]; ok && now.Sub(last) < clientPushQuietWindow {
			note.Quiet = true
		}
		pushLastSent.thread[key] = now
		pushLastSent.Unlock()
		enqueuePush(pushJob{tok: tg.tok, note: note})
	}
}

// clientBadgeMaybeDown pushes the badge alone when it went down (a read on
// another device, a deletion, a mute), at most once a minute per device.
func clientBadgeMaybeDown(userID string, before int, now time.Time) {
	if !pushConfigured("apns") && !pushConfigured("fcm") {
		return
	}
	_, after := clientUnreadBadge(userID)
	if after >= before {
		return
	}
	pushLastSent.Lock()
	pushLastSent.badgeN[userID] = after
	pushLastSent.Unlock()
	for _, tg := range pushTargets(userID, now) {
		pushLastSent.Lock()
		last, ok := pushLastSent.badge[tg.tok.DeviceID]
		if ok && now.Sub(last) < clientPushBadgeEvery {
			pushLastSent.Unlock()
			continue
		}
		pushLastSent.badge[tg.tok.DeviceID] = now
		pushLastSent.Unlock()
		enqueuePush(pushJob{tok: tg.tok, note: pushNote{Kind: "badge", Badge: after}})
	}
}

// --- sending -------------------------------------------------------------------

type pushJob struct {
	tok  appPushToken
	note pushNote
}

type pushQueue struct {
	once sync.Once
	ch   chan pushJob
}

var pushQueues = map[string]*pushQueue{"apns": {ch: make(chan pushJob, clientPushQueueMax)}, "fcm": {ch: make(chan pushJob, clientPushQueueMax)}}

// enqueuePush hands a push to its provider's worker, started on first use; a
// full queue drops its oldest.
func enqueuePush(j pushJob) {
	q, ok := pushQueues[j.tok.Provider]
	if !ok || !pushConfigured(j.tok.Provider) {
		return
	}
	q.once.Do(func() { go pushWorker(j.tok.Provider, q.ch) })
	pushPending.Add(1)
	for {
		select {
		case q.ch <- j:
			return
		default:
			select {
			case <-q.ch:
				pushPending.Add(-1) // the oldest, dropped
			default:
			}
		}
	}
}

// pushPending counts pushes queued or being sent, so a caller can wait until
// every decided push is out (pushWaitIdle).
var pushPending atomic.Int64

// pushWaitIdle waits until no push is queued or in flight, at most d; false
// when some still are.
func pushWaitIdle(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for pushPending.Load() > 0 {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
	return true
}

func pushWorker(provider string, ch chan pushJob) {
	for j := range ch {
		if pushStillWanted(j, time.Now()) {
			res := sendPushWithRetry(j)
			if !res.Dropped {
				recordPushResult(j.tok, res)
			}
		}
		pushPending.Add(-1)
	}
}

// pushStillWanted re-checks a queued push just before it goes out: the token
// is still this device's, the device is not revoked, and it has not come to
// the foreground since the push was decided (a push waits in the queue, and
// a retry waits seconds). The owner's test push is always wanted.
func pushStillWanted(j pushJob, now time.Time) bool {
	if j.note.Kind == "test" {
		return true
	}
	var token, presence, seen, revoked string
	if db.QueryRow(`SELECT p.token, d.presence, d.stream_seen_at, d.revoked_at FROM app_push_tokens p JOIN app_devices d ON d.id = p.device_id
		WHERE p.id = ? AND p.device_id = ?`, j.tok.ID, j.tok.DeviceID).Scan(&token, &presence, &seen, &revoked) != nil {
		return false
	}
	return token == j.tok.Token && revoked == "" && !deviceForeground(presence, seen, now)
}

// pushResult is a provider's answer to one push.
type pushResult struct {
	OK        bool
	Status    int
	Reason    string // the provider's reason code
	DeleteTok bool   // the token is dead
	Creds     bool   // refused for lasso's credentials or configuration
	Transient bool
	Dropped   bool // not sent: no longer wanted when its retry came round
}

func sendPushWithRetry(j pushJob) pushResult {
	var res pushResult
	for attempt := 0; attempt <= len(clientPushRetries); attempt++ {
		if attempt > 0 {
			time.Sleep(clientPushRetries[attempt-1])
			if !pushStillWanted(j, time.Now()) {
				return pushResult{Dropped: true}
			}
		}
		switch j.tok.Provider {
		case "apns":
			res = sendAPNs(j)
		case "fcm":
			res = sendFCM(j)
		default:
			return pushResult{Reason: "unsupported"}
		}
		if !res.Transient {
			return res
		}
	}
	return res
}

// recordPushResult keeps a push's outcome on its token: last_ok_at, or the
// reason code; a dead token is deleted. It logs the provider, the reason and
// the device only.
func recordPushResult(tok appPushToken, res pushResult) {
	stamp := nowStamp(time.Now())
	switch {
	case res.OK:
		pushFailingNow.Delete(tok.Provider)
		_, _ = db.Exec(`UPDATE app_push_tokens SET last_ok_at = ? WHERE id = ?`, stamp, tok.ID)
		return
	case res.DeleteTok:
		_, _ = db.Exec(`DELETE FROM app_push_tokens WHERE id = ?`, tok.ID)
		log.Printf("push:     %s %s for device %s: token deleted", tok.Provider, res.Reason, tok.DeviceID)
		return
	case res.Creds:
		pushFailingNow.Store(tok.Provider, res.Reason)
	}
	_, _ = db.Exec(`UPDATE app_push_tokens SET last_error = ?, last_error_at = ? WHERE id = ?`, clientMetaClean(res.Reason, 64), stamp, tok.ID)
	log.Printf("push:     %s %s for device %s", tok.Provider, clientMetaClean(res.Reason, 64), tok.DeviceID)
}

// --- the owner's side ----------------------------------------------------------

// clientPushDeviceView is a device's push as the owner sees it: never the token.
func clientPushDeviceView(deviceID string) map[string]any {
	t, ok := pushTokenOfDevice(deviceID)
	if !ok {
		return nil
	}
	v := map[string]any{"provider": t.Provider}
	for k, s := range map[string]string{"environment": t.Environment, "app_id": t.AppID, "last_ok_at": t.LastOKAt, "last_error": t.LastError} {
		if s != "" {
			v[k] = s
		}
	}
	return v
}

func clientPushConfigView() map[string]any {
	c := loadClientPushConfig()
	ap := map[string]any{"team_id": c.APNs.TeamID, "key_id": c.APNs.KeyID, "topic": c.APNs.Topic, "key_ref": c.APNs.KeyRef, "default_topic": clientPushDefaultTopic}
	st, why := clientPushState("apns")
	ap["state"] = st
	if why != "" {
		ap["reason"] = why
	}
	fc := map[string]any{"credentials_ref": c.FCM.CredentialsRef}
	st, why = clientPushState("fcm")
	fc["state"] = st
	if why != "" {
		fc["reason"] = why
	}
	if st == pushReady || st == pushFailing {
		if raw, err := pushSecret("fcm", c.FCM.CredentialsRef, 10*time.Minute); err == nil {
			if sa, err := parseFCMCredentials(raw); err == nil {
				fc["project_id"] = sa.ProjectID
			}
		}
	}
	return map[string]any{"apns": ap, "fcm": fc}
}

// serveClientPushConfig is /api/client-push (behind UI_AUTH, off machine
// callers like /api/app-users):
//
//	GET  /api/client-push        the stored choices (references, never values) and each provider's state
//	PUT  /api/client-push        merge-patch them ("" clears a field)
//	POST /api/client-push/test   {user_id, device_id}: one push, and the provider's answer
func serveClientPushConfig(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSuffix(r.URL.Path, "/") == "/api/client-push/test" {
		serveClientPushTest(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, clientPushConfigView())
	case http.MethodPut, http.MethodPost:
		var patch struct {
			APNs map[string]*string `json:"apns"`
			FCM  map[string]*string `json:"fcm"`
		}
		if !decodeBody(w, r, &patch) {
			return
		}
		c := loadClientPushConfig()
		set := func(dst *string, m map[string]*string, key string) {
			if v, ok := m[key]; ok {
				if v == nil {
					*dst = ""
				} else {
					*dst = strings.TrimSpace(*v)
				}
			}
		}
		for k := range patch.APNs {
			switch k {
			case "team_id", "key_id", "topic", "key_ref":
			default:
				http.Error(w, "unknown apns field "+k, http.StatusBadRequest)
				return
			}
		}
		for k := range patch.FCM {
			if k != "credentials_ref" {
				http.Error(w, "unknown fcm field "+k, http.StatusBadRequest)
				return
			}
		}
		set(&c.APNs.TeamID, patch.APNs, "team_id")
		set(&c.APNs.KeyID, patch.APNs, "key_id")
		set(&c.APNs.Topic, patch.APNs, "topic")
		set(&c.APNs.KeyRef, patch.APNs, "key_ref")
		set(&c.FCM.CredentialsRef, patch.FCM, "credentials_ref")
		if err := c.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(c)
		if err := setSetting(clientPushSettingKey, string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resetPushSecrets()
		pushFailingNow.Delete("apns")
		pushFailingNow.Delete("fcm")
		writeJSON(w, clientPushConfigView())
	default:
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
	}
}

func serveClientPushTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		UserID   string `json:"user_id"`
		DeviceID string `json:"device_id"`
	}
	if !decodeBody(w, r, &in) {
		return
	}
	tok, ok := pushTokenOfDevice(in.DeviceID)
	if !ok || tok.UserID != in.UserID {
		http.Error(w, "that device has no push token", http.StatusNotFound)
		return
	}
	if !pushConfigured(tok.Provider) {
		http.Error(w, tok.Provider+" is not configured", http.StatusBadRequest)
		return
	}
	_, badge := clientUnreadBadge(tok.UserID)
	res := sendPushWithRetry(pushJob{tok: tok, note: pushNote{Kind: "test", Title: "lasso", Body: "Push works.", Badge: badge}})
	recordPushResult(tok, res)
	out := map[string]any{"ok": res.OK, "status": res.Status, "provider": tok.Provider}
	if res.Reason != "" {
		out["reason"] = res.Reason
	}
	if res.DeleteTok {
		out["token_deleted"] = true
	}
	writeJSON(w, out)
}

func truncUTF8(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// clientPushPatch is update_settings' client_push: the PUT body of
// /api/client-push.
type clientPushPatch struct {
	APNs map[string]*string `json:"apns,omitempty" jsonschema:"team_id, key_id, topic, key_ref"`
	FCM  map[string]*string `json:"fcm,omitempty" jsonschema:"credentials_ref"`
}

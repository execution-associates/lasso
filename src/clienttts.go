package main

// The text-to-speech relay (docs/design/client-tts.md): the phone apps speak a
// bot's reply with Inworld's TTS-2 without holding the API key. lasso keeps one
// upstream WebSocket per app stream, forwards one sentence at a time and hands
// the audio back as raw PCM.
//
// This file is the engine's settings, its state, the monthly usage that caps
// the bill, the voice catalog, GET /client/v1/tts and the owner's
// /api/client-tts. The app's stream is clienttts_stream.go; the Inworld side
// (the only file that knows their protocol) is clienttts_inworld.go.
//
// The key is named in the client_tts setting by reference, never stored: a
// variable in lasso's environment or an op:// reference, read with lookupSecret
// and held in memory. It is never logged, returned or sent anywhere but the
// fixed Inworld host. With nothing configured nothing runs: no goroutine, no
// connection.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	clientTTSSettingKey = "client_tts"
	ttsEngineInworld    = "inworld"

	// Limits. Every one is a constant so the doc can name it.
	ttsMaxTextChars        = 1000 // runes in one sentence; Inworld takes 2000 UTF-16 units
	ttsMaxQueued           = 20   // sentences queued or in flight on one stream
	ttsMaxIDLen            = 64
	ttsMaxIDsPerStream     = 5000 // distinct ids one stream may use
	ttsStreamOpensPerMin   = 12   // per device
	ttsFrameMaxBytes       = 32 << 10
	ttsBadFramesMax        = 10
	ttsDefaultSampleRate   = 24000
	ttsVoiceCacheTTL       = 10 * time.Minute
	ttsVoiceFailTTL        = 30 * time.Second
	ttsVoicesMax           = 400
	ttsTestPhrase          = "Text to speech works."
	ttsDeletedUser         = "_deleted"
	ttsOwnerTestUser       = "_owner_test"
	ttsMonthlyPerPerson    = 500_000
	ttsMonthlyTotal        = 1_600_000
	ttsMonthlyLimitCeiling = 100_000_000
)

// ttsTuning holds what tests shorten or move; tune() reads it and setTune
// changes it.
type ttsTuning struct {
	Lifetime, Idle, Start, PingEvery, AuthEvery, Write, FirstAudio, ChunkStall, Sentence, Failing time.Duration
	QueueBytes, SentencesPerMin, CharsPerMin, PerDevice, PerPerson, Total                         int
	Now                                                                                           func() time.Time
}

var ttsTune = struct {
	sync.RWMutex
	t ttsTuning
}{t: ttsTuning{
	Lifetime: 30 * time.Minute, Idle: 2 * time.Minute, Start: 10 * time.Second, PingEvery: 20 * time.Second, AuthEvery: 2 * time.Second,
	Write: 20 * time.Second, FirstAudio: 1500 * time.Millisecond, ChunkStall: 10 * time.Second, Sentence: 60 * time.Second, Failing: 30 * time.Second,
	QueueBytes: 1 << 20, SentencesPerMin: 90, CharsPerMin: 12000, PerDevice: 2, PerPerson: 4, Total: 8, Now: time.Now,
}}

func tune() ttsTuning {
	ttsTune.RLock()
	defer ttsTune.RUnlock()
	return ttsTune.t
}

func setTune(f func(*ttsTuning)) {
	ttsTune.Lock()
	defer ttsTune.Unlock()
	f(&ttsTune.t)
}

// ttsSampleRates are the rates a device may ask for.
var ttsSampleRates = map[int]bool{16000: true, 24000: true, 48000: true}

// ttsModel is one model the app can choose; Upstream is Inworld's id for it.
type ttsModel struct {
	ID       string
	Name     string
	Upstream string
	Default  bool
}

var ttsModels = []ttsModel{
	{ID: "tts-2-realtime", Name: "TTS-2 Realtime", Upstream: "inworld-tts-2", Default: true},
	{ID: "tts-2-flash", Name: "TTS-2 Flash", Upstream: "inworld-tts-2-flash"},
}

func ttsModelByID(id string) (ttsModel, bool) {
	for _, m := range ttsModels {
		if m.ID == id {
			return m, true
		}
	}
	return ttsModel{}, false
}

// --- configuration -----------------------------------------------------------

type clientTTSInworld struct {
	KeyRef                     string `json:"key_ref,omitempty"`
	DefaultModel               string `json:"default_model,omitempty"`
	DefaultVoice               string `json:"default_voice,omitempty"`
	MonthlyCharacterLimit      int    `json:"monthly_character_limit,omitempty"`
	TotalMonthlyCharacterLimit int    `json:"total_monthly_character_limit,omitempty"`
	Enabled                    *bool  `json:"enabled,omitempty"`
}

type clientTTSConfig struct {
	Inworld clientTTSInworld `json:"inworld"`
}

func loadClientTTSConfig() clientTTSConfig {
	var c clientTTSConfig
	if db == nil {
		return c
	}
	if raw, _ := getSetting(clientTTSSettingKey); raw != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	return c
}

func (c clientTTSInworld) enabled() bool { return c.Enabled == nil || *c.Enabled }

func (c clientTTSInworld) model() ttsModel {
	if m, ok := ttsModelByID(c.DefaultModel); ok {
		return m
	}
	return ttsModels[0]
}

// personLimit is the characters one person may have synthesized in a month,
// never above the total.
func (c clientTTSInworld) personLimit() int {
	n := c.MonthlyCharacterLimit
	if n <= 0 {
		n = ttsMonthlyPerPerson
	}
	if t := c.totalLimit(); n > t {
		n = t
	}
	return n
}

// totalLimit is the characters everyone together may have synthesized in a
// month: the ceiling that keeps the bill under the card's limit.
func (c clientTTSInworld) totalLimit() int {
	if c.TotalMonthlyCharacterLimit > 0 {
		return c.TotalMonthlyCharacterLimit
	}
	return ttsMonthlyTotal
}

func (c clientTTSConfig) validate() error {
	i := c.Inworld
	if i.KeyRef != "" && !validKeyRef(i.KeyRef) {
		return errors.New("inworld.key_ref: an environment variable name or an op://vault/item/field reference")
	}
	if i.DefaultModel != "" {
		if _, ok := ttsModelByID(i.DefaultModel); !ok {
			return errors.New("inworld.default_model: tts-2-realtime or tts-2-flash")
		}
	}
	if i.DefaultVoice != "" && !ttsVoiceIDRE(i.DefaultVoice) {
		return errors.New("inworld.default_voice: a voice id (letters, digits, . _ : - and spaces, at most 128)")
	}
	if i.MonthlyCharacterLimit < 0 || i.MonthlyCharacterLimit > ttsMonthlyLimitCeiling {
		return fmt.Errorf("inworld.monthly_character_limit: 0 (the default) to %d", ttsMonthlyLimitCeiling)
	}
	if i.TotalMonthlyCharacterLimit < 0 || i.TotalMonthlyCharacterLimit > ttsMonthlyLimitCeiling {
		return fmt.Errorf("inworld.total_monthly_character_limit: 0 (the default) to %d", ttsMonthlyLimitCeiling)
	}
	return nil
}

func ttsVoiceIDRE(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:- ", r)) {
			return false
		}
	}
	return true
}

// --- the key -----------------------------------------------------------------

// clientTTSSecret resolves a key reference; a var so tests stand in for it.
var clientTTSSecret = lookupSecret

type ttsSecretEntry struct {
	ref, value string
	err        error
	at         time.Time
}

var ttsSecrets = struct {
	sync.Mutex
	e ttsSecretEntry
}{}

// ttsKey is the API key: held until its reference changes (a failure is
// remembered a minute, so a status read does not run `op read` on every
// request). It must be a header-safe token.
func ttsKey(ref string) (string, error) {
	ttsSecrets.Lock()
	e := ttsSecrets.e
	ttsSecrets.Unlock()
	if e.ref == ref && !e.at.IsZero() {
		if e.err != nil && time.Since(e.at) < time.Minute {
			return "", e.err
		}
		if e.err == nil {
			return e.value, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), opReadTimeout+time.Second)
	defer cancel()
	v, err := clientTTSSecret(ctx, ref)
	v = strings.TrimSpace(v)
	if err == nil {
		if v == "" || len(v) > 512 {
			err = errors.New("the key is empty or too long")
		}
		for _, r := range v {
			if r <= ' ' || r >= 0x7f {
				err = errors.New("the key is not a plain header token")
				break
			}
		}
		if err != nil {
			v = ""
		}
	}
	ttsSecrets.Lock()
	ttsSecrets.e = ttsSecretEntry{ref: ref, value: v, err: err, at: time.Now()}
	ttsSecrets.Unlock()
	return v, err
}

func resetTTSSecrets() {
	ttsSecrets.Lock()
	ttsSecrets.e = ttsSecretEntry{}
	ttsSecrets.Unlock()
	ttsVoices.Lock()
	ttsVoices.list, ttsVoices.at, ttsVoices.failedAt, ttsVoices.refreshing = nil, time.Time{}, time.Time{}, false
	ttsVoices.Unlock()
}

// --- state -------------------------------------------------------------------

// ttsFailingAt is when the upstream last refused lasso's key or could not be
// reached repeatedly; zero when the last attempt worked.
var ttsFailingAt atomic.Int64

func ttsMarkFailing() { ttsFailingAt.Store(tune().Now().UnixNano()) }
func ttsMarkWorking() { ttsFailingAt.Store(0) }
func ttsFailingNow() bool {
	at := ttsFailingAt.Load()
	return at != 0 && tune().Now().Sub(time.Unix(0, at)) < tune().Failing
}

// clientTTSState is the engine's availability for the app: reason is "" when
// it is available, else not_configured, unreadable or failing. The quota is the
// person's, so it is checked by the callers that know the person.
func clientTTSState() (key string, reason string, cfg clientTTSInworld) {
	cfg = loadClientTTSConfig().Inworld
	if cfg.KeyRef == "" || !cfg.enabled() {
		return "", "not_configured", cfg
	}
	k, err := ttsKey(cfg.KeyRef)
	if err != nil {
		return "", "unreadable", cfg
	}
	if ttsFailingNow() {
		return k, "failing", cfg
	}
	return k, "", cfg
}

// --- usage -------------------------------------------------------------------

const clientTTSSchema = `
CREATE TABLE IF NOT EXISTS app_tts_usage (
	user_id    TEXT NOT NULL,
	month      TEXT NOT NULL,
	engine     TEXT NOT NULL,
	characters INTEGER NOT NULL DEFAULT 0,
	audio_ms   INTEGER NOT NULL DEFAULT 0,
	sentences  INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, month, engine)
);
`

var ttsUsageMu sync.Mutex

func ttsMonth(t time.Time) string { return t.UTC().Format("2006-01") }

// ttsUsed is the characters a person, and everyone, has had synthesized this
// month.
func ttsUsed(userID string) (person, total int) { return ttsUsedIn(userID, ttsMonth(tune().Now())) }

func ttsUsedIn(userID, m string) (person, total int) {
	_ = db.QueryRow(`SELECT COALESCE(SUM(characters),0) FROM app_tts_usage WHERE month = ? AND engine = ?`, m, ttsEngineInworld).Scan(&total)
	_ = db.QueryRow(`SELECT COALESCE(SUM(characters),0) FROM app_tts_usage WHERE month = ? AND engine = ? AND user_id = ?`, m, ttsEngineInworld, userID).Scan(&person)
	return
}

var errTTSQuota = errors.New("tts monthly quota reached")

// ttsReserve counts chars against the person's and the total's monthly limits
// and records them, or refuses with errTTSQuota. It is called just before the
// text is sent upstream, so what is counted is what was sent. It reads the clock
// once, so the month checked is the month recorded, and answers that month for
// a refund.
func ttsReserve(userID string, chars int, cfg clientTTSInworld) (string, error) {
	ttsUsageMu.Lock()
	defer ttsUsageMu.Unlock()
	now := tune().Now()
	month := ttsMonth(now)
	person, total := ttsUsedIn(userID, month)
	if person+chars > cfg.personLimit() || total+chars > cfg.totalLimit() {
		return month, errTTSQuota
	}
	_, err := db.Exec(`INSERT INTO app_tts_usage (user_id, month, engine, characters, sentences, updated_at) VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(user_id, month, engine) DO UPDATE SET characters = characters + excluded.characters, sentences = sentences + 1, updated_at = excluded.updated_at`,
		userID, month, ttsEngineInworld, chars, nowStamp(now))
	return month, err
}

// ttsRefund takes back a reservation whose text never left lasso.
func ttsRefund(userID string, chars int, month string) {
	ttsUsageMu.Lock()
	defer ttsUsageMu.Unlock()
	_, _ = db.Exec(`UPDATE app_tts_usage SET characters = MAX(0, characters - ?), sentences = MAX(0, sentences - 1) WHERE user_id = ? AND month = ? AND engine = ?`,
		chars, userID, month, ttsEngineInworld)
}

func ttsAddAudio(userID string, ms int) {
	ttsUsageMu.Lock()
	defer ttsUsageMu.Unlock()
	_, _ = db.Exec(`UPDATE app_tts_usage SET audio_ms = audio_ms + ? WHERE user_id = ? AND month = ? AND engine = ?`,
		ms, userID, ttsMonth(tune().Now()), ttsEngineInworld)
}

// ttsFoldUsageStmts run when a person is deleted (one parameter, their id):
// their usage moves onto a placeholder, so deleting them removes their id but
// never lowers what this month's bill counts.
var ttsFoldUsageStmts = []string{
	`INSERT INTO app_tts_usage (user_id, month, engine, characters, audio_ms, sentences, updated_at)
		SELECT '` + ttsDeletedUser + `', month, engine, SUM(characters), SUM(audio_ms), SUM(sentences), MAX(updated_at) FROM app_tts_usage WHERE user_id = ? GROUP BY month, engine
		ON CONFLICT(user_id, month, engine) DO UPDATE SET characters = characters + excluded.characters, audio_ms = audio_ms + excluded.audio_ms,
			sentences = sentences + excluded.sentences, updated_at = excluded.updated_at`,
	`DELETE FROM app_tts_usage WHERE user_id = ?`,
}

// ttsCharWindow is the per-person characters-per-minute limit.
type ttsCharWindow struct {
	mu   sync.Mutex
	hits map[string][]ttsCharHit
}

type ttsCharHit struct {
	at time.Time
	n  int
}

var ttsChars = &ttsCharWindow{hits: map[string][]ttsCharHit{}}

func (w *ttsCharWindow) allow(key string, n, limit int, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	hs := w.hits[key]
	i, sum := 0, 0
	for i < len(hs) && now.Sub(hs[i].at) >= time.Minute {
		i++
	}
	hs = hs[i:]
	for _, h := range hs {
		sum += h.n
	}
	if sum+n > limit {
		w.hits[key] = hs
		return false
	}
	w.hits[key] = append(hs, ttsCharHit{now, n})
	if len(w.hits) > 4096 {
		for k, v := range w.hits {
			if len(v) == 0 || now.Sub(v[len(v)-1].at) >= time.Minute {
				delete(w.hits, k)
			}
		}
	}
	return true
}

// ttsCountChars is Inworld's count: one per character, two for one outside the
// Basic Multilingual Plane.
func ttsCountChars(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// ttsCleanText makes a sentence safe to forward: valid UTF-8, control and
// invisible formatting characters out, line breaks and runs of space one space.
// It reports false for text that is not UTF-8 or leaves nothing to say.
func ttsCleanText(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	var b strings.Builder
	space := true // trims the start
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029 || r == ' ' || r == 0xA0:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		case r == utf8.RuneError || unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) || unicode.Is(unicode.Cs, r):
		case unicode.IsSpace(r):
			if !space {
				b.WriteByte(' ')
				space = true
			}
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := strings.TrimRight(b.String(), " ")
	return out, out != ""
}

// --- voices ------------------------------------------------------------------

type ttsVoice struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Gender      *string `json:"gender"`
	Language    string  `json:"language"`
	Description string  `json:"description"`
}

var ttsVoices = struct {
	sync.Mutex
	list       []ttsVoice
	at         time.Time
	failedAt   time.Time
	refreshing bool
}{}

// ttsVoiceList is the catalog the app may choose from, cached ten minutes. It
// never makes a caller wait for a refresh: a stale list is answered at once and
// refreshed in the background; only with no list at all does the first caller
// wait (at most wait) for Inworld, and a caller who finds a fetch already under
// way gets the fallback. When Inworld cannot be asked the answer is the last
// list, else the configured default voice alone (never a voice the owner has not
// named).
func ttsVoiceList(key string, cfg clientTTSInworld, wait time.Duration) []ttsVoice {
	ttsVoices.Lock()
	list, at, failedAt, busy := ttsVoices.list, ttsVoices.at, ttsVoices.failedAt, ttsVoices.refreshing
	if list != nil && time.Since(at) < ttsVoiceCacheTTL {
		ttsVoices.Unlock()
		return list
	}
	recentlyFailed := !failedAt.IsZero() && time.Since(failedAt) < ttsVoiceFailTTL
	if busy || recentlyFailed {
		ttsVoices.Unlock()
		if list != nil {
			return list
		}
		return ttsVoiceFallback(cfg)
	}
	ttsVoices.refreshing = true
	ttsVoices.Unlock()
	fetch := func(ctx context.Context) []ttsVoice {
		vs, err := inworldListVoices(ctx, key)
		ttsVoices.Lock()
		defer ttsVoices.Unlock()
		ttsVoices.refreshing = false
		if err != nil || len(vs) == 0 {
			ttsVoices.failedAt = time.Now()
			return nil
		}
		sort.SliceStable(vs, func(i, j int) bool { return strings.ToLower(vs[i].Name) < strings.ToLower(vs[j].Name) })
		if len(vs) > ttsVoicesMax {
			vs = vs[:ttsVoicesMax]
		}
		ttsVoices.list, ttsVoices.at, ttsVoices.failedAt = vs, time.Now(), time.Time{}
		return vs
	}
	if list != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			fetch(ctx)
		}()
		return list
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if vs := fetch(ctx); vs != nil {
		return vs
	}
	return ttsVoiceFallback(cfg)
}

func ttsVoiceFallback(cfg clientTTSInworld) []ttsVoice {
	if cfg.DefaultVoice == "" {
		return []ttsVoice{}
	}
	return []ttsVoice{{ID: cfg.DefaultVoice, Name: cfg.DefaultVoice, Language: "en"}}
}

// ttsDefaultVoice is the configured voice, else the first of the catalog.
func ttsDefaultVoice(cfg clientTTSInworld, vs []ttsVoice) string {
	if cfg.DefaultVoice != "" {
		return cfg.DefaultVoice
	}
	if len(vs) > 0 {
		return vs[0].ID
	}
	return ""
}

func ttsHasVoice(vs []ttsVoice, id string) bool {
	for _, v := range vs {
		if v.ID == id {
			return true
		}
	}
	return false
}

// --- GET /client/v1/tts ------------------------------------------------------

func ttsModelViews() []map[string]any {
	out := []map[string]any{}
	for _, m := range ttsModels {
		v := map[string]any{"id": m.ID, "name": m.Name}
		if m.Default {
			v["default"] = true
		}
		out = append(out, v)
	}
	return out
}

func serveClientTTS(w http.ResponseWriter, id *clientIdentity) {
	key, reason, cfg := clientTTSState()
	person, total := ttsUsed(id.User.ID)
	if reason == "" && (person >= cfg.personLimit() || total >= cfg.totalLimit()) {
		reason = "quota_exceeded"
	}
	eng := map[string]any{
		"id": ttsEngineInworld, "name": "Inworld", "available": reason == "",
		"models": ttsModelViews(), "voices": []ttsVoice{}, "default_voice": nil,
		"sample_rate": ttsDefaultSampleRate, "format": "pcm_s16le",
		"usage": map[string]any{"characters_this_month": person, "limit_characters": cfg.personLimit()},
	}
	if reason == "" {
		eng["reason"] = nil
	} else {
		eng["reason"] = reason
	}
	// The catalog is shown whenever the key reads (a failing or over-quota
	// engine still lists what it would offer); never without one.
	if key != "" {
		vs := ttsVoiceList(key, cfg, 3*time.Second)
		eng["voices"] = vs
		if d := ttsDefaultVoice(cfg, vs); d != "" {
			eng["default_voice"] = d
		}
	}
	for _, m := range ttsModels {
		if m.ID == cfg.model().ID {
			eng["default_model"] = m.ID
		}
	}
	clientJSON(w, http.StatusOK, map[string]any{"engines": []any{eng}})
}

// --- the owner's side --------------------------------------------------------

func clientTTSConfigView(people bool) map[string]any {
	c := loadClientTTSConfig().Inworld
	_, reason, _ := clientTTSState()
	state := "ready"
	switch reason {
	case "not_configured":
		state = "unconfigured"
	case "unreadable":
		state = "unreadable"
	case "failing":
		state = "failing"
	}
	_, total := ttsUsed("")
	in := map[string]any{
		"key_ref": c.KeyRef, "default_model": c.model().ID, "default_voice": c.DefaultVoice,
		"monthly_character_limit": c.personLimit(), "total_monthly_character_limit": c.totalLimit(),
		"enabled": c.enabled(), "state": state,
		"default_monthly_character_limit": ttsMonthlyPerPerson, "default_total_monthly_character_limit": ttsMonthlyTotal,
	}
	if c.KeyRef != "" {
		if _, err := ttsKey(c.KeyRef); err != nil {
			in["reason"] = missingKeyHint(c.KeyRef, nil)
		}
	}
	out := map[string]any{"inworld": in, "month": ttsMonth(tune().Now()), "models": ttsModelViews(),
		"usage": map[string]any{"characters_this_month": total, "limit_characters": c.totalLimit()}}
	if people {
		out["usage"].(map[string]any)["people"] = ttsPeopleUsage()
	}
	return out
}

func ttsPeopleUsage() []map[string]any {
	rows, err := db.Query(`SELECT t.user_id, COALESCE(u.name, ''), t.characters, t.audio_ms, t.sentences FROM app_tts_usage t
		LEFT JOIN app_users u ON u.id = t.user_id WHERE t.month = ? AND t.engine = ? ORDER BY t.characters DESC`, ttsMonth(tune().Now()), ttsEngineInworld)
	out := []map[string]any{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var ch, ms, n int
		if rows.Scan(&id, &name, &ch, &ms, &n) != nil {
			continue
		}
		out = append(out, map[string]any{"user_id": id, "name": name, "characters": ch, "audio_ms": ms, "sentences": n})
	}
	return out
}

// serveClientTTSConfig is /api/client-tts (behind UI_AUTH, off machine callers
// like /api/client-push):
//
//	GET  /api/client-tts        the stored choices (a reference, never the key), the state and this month's usage per person
//	PUT  /api/client-tts        merge-patch {"inworld": {...}} ("" or null clears a field)
//	POST /api/client-tts/test   synthesize one short phrase and report the upstream's answer
func serveClientTTSConfig(w http.ResponseWriter, r *http.Request) { serveClientTTSConfigAs(w, r, true) }

// serveClientTTSConfigNoPeople is the same for the settings tools, which never
// show a person's usage to a bot's session.
func serveClientTTSConfigNoPeople(w http.ResponseWriter, r *http.Request) {
	serveClientTTSConfigAs(w, r, false)
}

func serveClientTTSConfigAs(w http.ResponseWriter, r *http.Request, people bool) {
	if strings.TrimSuffix(r.URL.Path, "/") == "/api/client-tts/test" {
		serveClientTTSTest(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, clientTTSConfigView(people))
	case http.MethodPut, http.MethodPost:
		var patch struct {
			Inworld map[string]json.RawMessage `json:"inworld"`
		}
		if !decodeBody(w, r, &patch) {
			return
		}
		c := loadClientTTSConfig()
		if err := applyTTSPatch(&c.Inworld, patch.Inworld); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(c)
		if err := setSetting(clientTTSSettingKey, string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resetTTSSecrets()
		ttsMarkWorking()
		writeJSON(w, clientTTSConfigView(people))
	default:
		http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
	}
}

func applyTTSPatch(c *clientTTSInworld, m map[string]json.RawMessage) error {
	str := func(dst *string, k string, raw json.RawMessage) error {
		var s *string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("inworld.%s: a string", k)
		}
		if s == nil {
			*dst = ""
		} else {
			*dst = strings.TrimSpace(*s)
		}
		return nil
	}
	num := func(dst *int, k string, raw json.RawMessage) error {
		var n *int
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("inworld.%s: a whole number (0 or null for the default)", k)
		}
		if n == nil {
			*dst = 0
		} else {
			*dst = *n
		}
		return nil
	}
	for k, raw := range m {
		var err error
		switch k {
		case "key_ref":
			err = str(&c.KeyRef, k, raw)
		case "default_model":
			err = str(&c.DefaultModel, k, raw)
		case "default_voice":
			err = str(&c.DefaultVoice, k, raw)
		case "monthly_character_limit":
			err = num(&c.MonthlyCharacterLimit, k, raw)
		case "total_monthly_character_limit":
			err = num(&c.TotalMonthlyCharacterLimit, k, raw)
		case "enabled":
			var b *bool
			if err = json.Unmarshal(raw, &b); err != nil {
				err = errors.New("inworld.enabled: true or false")
			}
			c.Enabled = b
		default:
			err = fmt.Errorf("unknown inworld field %s", k)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// serveClientTTSTest synthesizes ttsTestPhrase through the same upstream code a
// stream uses and reports how it went. The phrase is fixed, so the route is not
// a way to speak arbitrary text; it counts toward the month like any other.
func serveClientTTSTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Model string `json:"model"`
		Voice string `json:"voice"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &in) {
		return
	}
	key, reason, cfg := clientTTSState()
	if key == "" {
		http.Error(w, "the Inworld key is "+map[string]string{"not_configured": "not configured", "unreadable": "unreadable"}[reason], http.StatusBadRequest)
		return
	}
	model := cfg.model()
	if in.Model != "" {
		m, ok := ttsModelByID(in.Model)
		if !ok {
			http.Error(w, "model: tts-2-realtime or tts-2-flash", http.StatusBadRequest)
			return
		}
		model = m
	}
	voice := in.Voice
	if voice == "" {
		voice = ttsDefaultVoice(cfg, ttsVoiceList(key, cfg, 3*time.Second))
	}
	if !ttsVoiceIDRE(voice) {
		http.Error(w, "no voice: set default_voice", http.StatusBadRequest)
		return
	}
	res := ttsSelfTest(r.Context(), key, cfg, model, voice)
	writeJSON(w, res)
}

// clientTTSPatch is update_settings' client_tts: the PUT body of
// /api/client-tts.
type clientTTSPatch struct {
	Inworld map[string]any `json:"inworld,omitempty" jsonschema:"key_ref, default_model (tts-2-realtime or tts-2-flash), default_voice, monthly_character_limit, total_monthly_character_limit, enabled"`
}

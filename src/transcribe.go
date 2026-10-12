package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Speech to text for lasso's own clients (docs/design/transcription.md).
//
// POST /api/transcribe takes a PCM16 WAV body and answers its transcript from
// the provider the `transcribe` setting names: Groq, OpenAI, or any server
// speaking OpenAI's /audio/transcriptions (a whisper server on loopback or the
// tailnet, so audio never has to leave the box). It is a core route rather
// than a plugin because a plugin sandbox cannot reach a loopback server (isb
// egress refuses IP literals) and a plugin call body is cut at 4 MiB.
//
// Privacy: the audio stays in memory and goes straight to the provider; the
// transcript, the audio and the request's headers are never logged or written
// anywhere. The provider key is resolved from lasso's environment or the
// `secret` CLI, held in memory for transcribeKeyTTL, never stored in lasso.db.

const (
	transcribeSettingKey  = "transcribe"
	transcribeMaxBytes    = 4 << 20
	transcribeMaxSeconds  = 120
	transcribeMaxInFlight = 2
	transcribePromptMax   = 512
	transcribeKeyTTL      = 10 * time.Minute
	// transcribeAnswerMax bounds what is read of a provider's answer.
	transcribeAnswerMax = 1 << 20
)

const (
	transcribeOff              = "off"
	transcribeGroq             = "groq"
	transcribeOpenAI           = "openai"
	transcribeOpenAICompatible = "openai-compatible"
)

// transcribeProviderNames is the accepted set, in the order an error lists it.
var transcribeProviderNames = []string{transcribeOff, transcribeGroq, transcribeOpenAI, transcribeOpenAICompatible}

// transcribeDefaults is each provider's base URL, model and key variable when
// the setting names none. A var so tests can point a provider at a fake.
var transcribeDefaults = map[string]transcribeConfig{
	transcribeGroq:   {BaseURL: "https://api.groq.com/openai/v1", Model: "whisper-large-v3-turbo", KeyName: "GROQ_API_KEY"},
	transcribeOpenAI: {BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini-transcribe", KeyName: "OPENAI_API_KEY"},
}

var (
	// transcribeUpstreamTimeout bounds one provider round trip (504 past it).
	// A var so the timeout test need not wait a minute.
	transcribeUpstreamTimeout = 60 * time.Second
	// transcribeSecretLookup resolves a provider key (lookupSecret); a test
	// seam.
	transcribeSecretLookup = lookupSecret
	transcribeHTTPClient   = &http.Client{}
	// transcribeSlots is the global in-flight limit: a slot is taken before
	// the body is read, so at most transcribeMaxInFlight bodies are in memory.
	transcribeSlots = make(chan struct{}, transcribeMaxInFlight)

	transcribeModelRE    = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,128}$`)
	transcribeKeyNameRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	transcribeLanguageRE = regexp.MustCompile(`^[A-Za-z]{2,3}([-_][A-Za-z0-9]{2,8})?$`)
)

// transcribeConfig is the `transcribe` setting as stored: only explicit
// choices, an empty field meaning the provider's default.
type transcribeConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	KeyName  string `json:"key_name,omitempty"`
}

// transcribeTarget is a config resolved against its provider's defaults.
type transcribeTarget struct {
	Provider string
	Model    string
	BaseURL  string
	KeyName  string // "" = send no Authorization header
}

// loadTranscribeConfig reads the setting; unset reads as off.
func loadTranscribeConfig() (transcribeConfig, error) {
	raw, _ := getSetting(transcribeSettingKey)
	if raw == "" {
		return transcribeConfig{Provider: transcribeOff}, nil
	}
	var c transcribeConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return transcribeConfig{Provider: transcribeOff}, errors.New("the transcribe setting is unreadable; set it again in lasso's settings")
	}
	if c.Provider == "" {
		c.Provider = transcribeOff
	}
	return c, nil
}

// validate checks a config a caller wants stored.
func (c transcribeConfig) validate() error {
	if !slices.Contains(transcribeProviderNames, c.Provider) {
		return fmt.Errorf("transcribe.provider must be one of %s", strings.Join(transcribeProviderNames, ", "))
	}
	if c.Model != "" && !transcribeModelRE.MatchString(c.Model) {
		return errors.New("transcribe.model: letters, digits and ._:/@+- only, at most 128")
	}
	if c.KeyName != "" && !transcribeKeyNameRE.MatchString(c.KeyName) {
		return errors.New("transcribe.key_name: an environment variable name (letters, digits, underscore)")
	}
	if c.BaseURL != "" {
		if err := validTranscribeBaseURL(c.BaseURL); err != nil {
			return err
		}
	}
	if c.Provider == transcribeOpenAICompatible && c.BaseURL == "" {
		return errors.New("transcribe.base_url is required for openai-compatible")
	}
	return nil
}

func validTranscribeBaseURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("transcribe.base_url must be an http or https URL, e.g. http://127.0.0.1:8080/v1")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("transcribe.base_url must carry no credentials, query or fragment")
	}
	return nil
}

// target resolves the config, or says why transcription is unavailable.
func (c transcribeConfig) target() (transcribeTarget, string) {
	switch c.Provider {
	case transcribeOff, "":
		return transcribeTarget{Provider: transcribeOff}, "transcription is off: choose a provider in lasso's settings (transcribe)"
	case transcribeGroq, transcribeOpenAI, transcribeOpenAICompatible:
	default:
		return transcribeTarget{Provider: c.Provider}, fmt.Sprintf("unknown transcription provider %q", c.Provider)
	}
	d := transcribeDefaults[c.Provider]
	t := transcribeTarget{Provider: c.Provider, Model: d.Model, BaseURL: d.BaseURL, KeyName: d.KeyName}
	if c.Model != "" {
		t.Model = c.Model
	}
	if c.BaseURL != "" {
		t.BaseURL = c.BaseURL
	}
	if c.KeyName != "" {
		t.KeyName = c.KeyName
	}
	if t.BaseURL == "" {
		return t, "transcription provider " + c.Provider + " has no base_url"
	}
	if validTranscribeBaseURL(t.BaseURL) != nil {
		return t, "transcription provider " + c.Provider + " has an invalid base_url"
	}
	return t, ""
}

// ---- the key cache ------------------------------------------------------------

type cachedTranscribeKey struct {
	value string
	at    time.Time
}

var transcribeKeys = struct {
	sync.Mutex
	m map[string]cachedTranscribeKey
}{m: map[string]cachedTranscribeKey{}}

// transcribeKey resolves the key named name, from memory when it was resolved
// within transcribeKeyTTL. Failures are not cached, so a key set a moment ago
// is picked up on the next request.
func transcribeKey(ctx context.Context, name string) (string, error) {
	transcribeKeys.Lock()
	if k, ok := transcribeKeys.m[name]; ok && time.Since(k.at) < transcribeKeyTTL {
		transcribeKeys.Unlock()
		return k.value, nil
	}
	transcribeKeys.Unlock()
	v, err := transcribeSecretLookup(ctx, name)
	if err != nil {
		return "", err
	}
	transcribeKeys.Lock()
	transcribeKeys.m[name] = cachedTranscribeKey{value: v, at: time.Now()}
	transcribeKeys.Unlock()
	return v, nil
}

// forgetTranscribeKeys drops every cached key, so a settings change (or a
// rotated secret the human re-saves the setting for) takes effect at once.
func forgetTranscribeKeys() {
	transcribeKeys.Lock()
	clear(transcribeKeys.m)
	transcribeKeys.Unlock()
}

func missingKeyReason(name string) string {
	return fmt.Sprintf("no key for transcription: set %s in lasso's environment or `secret set %s`", name, name)
}

// ---- WAV ------------------------------------------------------------------------

// wavInfo is what transcription needs from a WAV header.
type wavInfo struct {
	Channels   int
	SampleRate int
	DataBytes  int // the data chunk's bytes actually present
}

func (w wavInfo) durationMS() int64 {
	return int64(w.DataBytes) * 1000 / int64(w.SampleRate*w.Channels*2)
}

var errNotPCM16WAV = errors.New("not a PCM16 WAV")

// parseWAV reads a RIFF/WAVE header: PCM (or EXTENSIBLE with a PCM subformat),
// 16-bit, 1-2 channels, 8-48 kHz. The data chunk's size is clamped to the
// bytes present, so a streaming writer's placeholder size reads as what was
// actually sent.
func parseWAV(b []byte) (wavInfo, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return wavInfo{}, errNotPCM16WAV
	}
	var info wavInfo
	haveFmt := false
	p := 12
	for p+8 <= len(b) {
		id := string(b[p : p+4])
		size := int(binary.LittleEndian.Uint32(b[p+4 : p+8]))
		body := p + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return wavInfo{}, errNotPCM16WAV
			}
			f := b[body:]
			format := binary.LittleEndian.Uint16(f[0:2])
			info.Channels = int(binary.LittleEndian.Uint16(f[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			blockAlign := int(binary.LittleEndian.Uint16(f[12:14]))
			bits := int(binary.LittleEndian.Uint16(f[14:16]))
			if format == 0xFFFE { // WAVE_FORMAT_EXTENSIBLE: the subformat GUID starts with the real tag
				if size < 40 || body+26 > len(b) {
					return wavInfo{}, errNotPCM16WAV
				}
				format = binary.LittleEndian.Uint16(f[24:26])
			}
			if format != 1 || bits != 16 || info.Channels < 1 || info.Channels > 2 ||
				info.SampleRate < 8000 || info.SampleRate > 48000 || blockAlign != info.Channels*2 {
				return wavInfo{}, errNotPCM16WAV
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return wavInfo{}, errNotPCM16WAV
			}
			info.DataBytes = min(size, len(b)-body)
			return info, nil
		}
		p = body + size + size&1 // chunks are padded to an even length
	}
	return wavInfo{}, errNotPCM16WAV
}

// ---- the routes -----------------------------------------------------------------

type transcribeStatus struct {
	Available  bool     `json:"available"`
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	MaxSeconds int      `json:"max_seconds"`
	MaxBytes   int      `json:"max_bytes"`
	Formats    []string `json:"formats"`
	Reason     string   `json:"reason,omitempty"`
}

type transcribeResult struct {
	Text      string `json:"text"`
	Engine    string `json:"engine"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Language  string `json:"language"`
	AudioMS   int64  `json:"audio_ms"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// serveTranscribe is /api/transcribe: GET says whether it works and its
// limits, POST transcribes one WAV.
func serveTranscribe(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, transcribeAvailability(r.Context()))
	case http.MethodPost:
		transcribePost(w, r)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// transcribeAvailability is GET's answer. It resolves the key too (cached),
// since a configured provider with no key is not available.
func transcribeAvailability(ctx context.Context) transcribeStatus {
	st := transcribeStatus{
		MaxSeconds: transcribeMaxSeconds,
		MaxBytes:   transcribeMaxBytes,
		Formats:    []string{"audio/wav"},
	}
	cfg, err := loadTranscribeConfig()
	t, reason := cfg.target()
	st.Provider, st.Model = t.Provider, t.Model
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	if reason != "" {
		st.Reason = reason
		return st
	}
	if t.KeyName != "" {
		if _, err := transcribeKey(ctx, t.KeyName); err != nil {
			st.Reason = missingKeyReason(t.KeyName)
			return st
		}
	}
	st.Available = true
	return st
}

func transcribePost(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cfg, err := loadTranscribeConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	t, reason := cfg.target()
	if reason != "" {
		http.Error(w, reason, http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	language := q.Get("language")
	if language != "" && !transcribeLanguageRE.MatchString(language) {
		http.Error(w, "language must be a language code like en or pt-BR", http.StatusBadRequest)
		return
	}
	prompt := q.Get("prompt")
	if len([]rune(prompt)) > transcribePromptMax {
		http.Error(w, fmt.Sprintf("prompt is longer than %d characters", transcribePromptMax), http.StatusBadRequest)
		return
	}

	select {
	case transcribeSlots <- struct{}{}:
		defer func() { <-transcribeSlots }()
	default:
		http.Error(w, fmt.Sprintf("already %d transcriptions in flight; try again shortly", transcribeMaxInFlight), http.StatusTooManyRequests)
		return
	}

	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, transcribeMaxBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("audio is larger than %d bytes", transcribeMaxBytes), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "reading the audio failed", http.StatusBadRequest)
		return
	}
	if len(audio) == 0 {
		http.Error(w, "empty body: send the recording as audio/wav", http.StatusBadRequest)
		return
	}
	info, err := parseWAV(audio)
	if err != nil {
		http.Error(w, "the body must be a PCM16 WAV (RIFF/WAVE, 16-bit, 1-2 channels, 8-48 kHz)", http.StatusUnsupportedMediaType)
		return
	}
	audioMS := info.durationMS()
	if audioMS > transcribeMaxSeconds*1000 {
		http.Error(w, fmt.Sprintf("audio is longer than %d seconds", transcribeMaxSeconds), http.StatusRequestEntityTooLarge)
		return
	}
	if info.DataBytes == 0 {
		http.Error(w, "the WAV holds no audio", http.StatusBadRequest)
		return
	}

	key := ""
	if t.KeyName != "" {
		if key, err = transcribeKey(r.Context(), t.KeyName); err != nil {
			http.Error(w, missingKeyReason(t.KeyName), http.StatusServiceUnavailable)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), transcribeUpstreamTimeout)
	defer cancel()
	text, status, msg := transcribeUpstream(ctx, t, key, audio, language, prompt)
	if status != 0 {
		if r.Context().Err() != nil {
			return // the caller went away; nobody to answer
		}
		http.Error(w, msg, status)
		return
	}
	writeJSON(w, transcribeResult{
		Text:      text,
		Engine:    "lasso",
		Provider:  t.Provider,
		Model:     t.Model,
		Language:  language,
		AudioMS:   audioMS,
		ElapsedMS: time.Since(start).Milliseconds(),
	})
}

// transcribeUpstream sends one WAV to the provider's /audio/transcriptions.
// The multipart body is the fields, then the audio straight from memory, then
// the closing boundary, so it carries a Content-Length without a second copy
// of the audio. A non-zero status is the HTTP error to answer with.
func transcribeUpstream(ctx context.Context, t transcribeTarget, key string, audio []byte, language, prompt string) (string, int, string) {
	var head, tail bytes.Buffer
	mw := multipart.NewWriter(&head)
	fields := [][2]string{{"response_format", "json"}}
	if t.Model != "" {
		fields = append(fields, [2]string{"model", t.Model})
	}
	if language != "" {
		fields = append(fields, [2]string{"language", language})
	}
	if prompt != "" {
		fields = append(fields, [2]string{"prompt", prompt})
	}
	for _, f := range fields {
		_ = mw.WriteField(f[0], f[1])
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="audio.wav"`)
	h.Set("Content-Type", "audio/wav")
	if _, err := mw.CreatePart(h); err != nil {
		return "", http.StatusInternalServerError, "building the request failed"
	}
	closer := multipart.NewWriter(&tail)
	_ = closer.SetBoundary(mw.Boundary())
	_ = closer.Close()

	endpoint := strings.TrimRight(t.BaseURL, "/") + "/audio/transcriptions"
	body := func() io.Reader {
		return io.MultiReader(bytes.NewReader(head.Bytes()), bytes.NewReader(audio), bytes.NewReader(tail.Bytes()))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body())
	if err != nil {
		return "", http.StatusInternalServerError, "building the request failed"
	}
	req.ContentLength = int64(head.Len() + len(audio) + tail.Len())
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(body()), nil }
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := transcribeHTTPClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", http.StatusGatewayTimeout, fmt.Sprintf("%s did not answer within %s", t.Provider, transcribeUpstreamTimeout)
		}
		log.Printf("transcribe: %s unreachable", t.Provider)
		return "", http.StatusBadGateway, t.Provider + " is unreachable"
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, transcribeAnswerMax))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", http.StatusGatewayTimeout, fmt.Sprintf("%s did not answer within %s", t.Provider, transcribeUpstreamTimeout)
		}
		return "", http.StatusBadGateway, t.Provider + "'s answer was cut off"
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("transcribe: %s answered %d", t.Provider, resp.StatusCode)
		return "", http.StatusBadGateway, providerErrorMessage(t.Provider, resp.StatusCode, raw)
	}
	var out struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Text == nil {
		return "", http.StatusBadGateway, t.Provider + " answered without a transcript"
	}
	return strings.TrimSpace(*out.Text), 0, ""
}

// providerErrorMessage says what a provider refused, without echoing anything
// about the key: an auth failure is named as such and nothing more.
func providerErrorMessage(provider string, status int, raw []byte) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Sprintf("%s refused the key (%d)", provider, status)
	}
	var e struct {
		Error any `json:"error"`
	}
	msg := ""
	if json.Unmarshal(raw, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			msg, _ = v["message"].(string)
		}
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return fmt.Sprintf("%s answered %d", provider, status)
	}
	return fmt.Sprintf("%s answered %d: %s", provider, status, msg)
}

// ---- settings ---------------------------------------------------------------------

// transcribeConfigView is the setting as the Settings section shows it: the
// stored choices resolved against the provider's defaults. Key NAMES only:
// a key's value is never read here, let alone returned.
type transcribeConfigView struct {
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	BaseURL   string   `json:"base_url"`
	KeyName   string   `json:"key_name"`
	Providers []string `json:"providers"`
	Reason    string   `json:"reason,omitempty"`
}

// transcribeConfigPatch is a POST to /api/transcribe/config: only the named
// fields change, "" resets one to the provider's default, and changing the
// provider resets every field the patch does not name (a groq model means
// nothing to openai).
type transcribeConfigPatch struct {
	Provider *string `json:"provider,omitempty" jsonschema:"off, groq, openai or openai-compatible. Changing it resets model, base_url and key_name unless they are given too."`
	Model    *string `json:"model,omitempty" jsonschema:"The provider's model; \"\" for the provider's default (groq whisper-large-v3-turbo, openai gpt-4o-mini-transcribe)."`
	BaseURL  *string `json:"base_url,omitempty" jsonschema:"The OpenAI-compatible API base, e.g. http://127.0.0.1:8080/v1. Required for openai-compatible."`
	KeyName  *string `json:"key_name,omitempty" jsonschema:"The NAME of the environment variable or secret holding the API key (never the key). Defaults GROQ_API_KEY / OPENAI_API_KEY; openai-compatible sends no key without one."`
}

func transcribeConfigViewOf(c transcribeConfig, loadErr error) transcribeConfigView {
	t, reason := c.target()
	v := transcribeConfigView{
		Provider: t.Provider, Model: t.Model, BaseURL: t.BaseURL, KeyName: t.KeyName,
		Providers: transcribeProviderNames, Reason: reason,
	}
	if t.Provider == transcribeOff {
		v.Reason = ""
	}
	if loadErr != nil {
		v.Reason = loadErr.Error()
	}
	return v
}

// serveTranscribeConfig is /api/transcribe/config, the `transcribe` section
// of the settings (update_settings writes it through here).
func serveTranscribeConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, err := loadTranscribeConfig()
		writeJSON(w, transcribeConfigViewOf(c, err))
	case http.MethodPost:
		var p transcribeConfigPatch
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, _ := loadTranscribeConfig() // an unreadable stored value is replaced
		if p.Provider != nil && *p.Provider != c.Provider {
			c = transcribeConfig{Provider: *p.Provider}
		}
		if p.Model != nil {
			c.Model = strings.TrimSpace(*p.Model)
		}
		if p.BaseURL != nil {
			c.BaseURL = strings.TrimSpace(*p.BaseURL)
		}
		if p.KeyName != nil {
			c.KeyName = strings.TrimSpace(*p.KeyName)
		}
		if err := c.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(c)
		if err := setSetting(transcribeSettingKey, string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		forgetTranscribeKeys()
		writeJSON(w, transcribeConfigViewOf(c, nil))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

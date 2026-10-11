package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// makeWAV builds a PCM16 WAV of the given shape, silent.
func makeWAV(rate, channels int, ms int) []byte {
	data := rate * channels * 2 * ms / 1000
	return wavWith(1, 16, rate, channels, data, data, nil)
}

// wavWith builds a WAV with full control: format tag, bit depth, the data
// size the header DECLARES versus the bytes actually present, and an extra
// chunk placed before the data.
func wavWith(format, bits, rate, channels, declared, present int, extra []byte) []byte {
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + present + len(extra)))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	le(uint32(16))
	le(uint16(format))
	le(uint16(channels))
	le(uint32(rate))
	le(uint32(rate * channels * bits / 8))
	le(uint16(channels * bits / 8))
	le(uint16(bits))
	b.Write(extra)
	b.WriteString("data")
	le(uint32(declared))
	b.Write(make([]byte, present))
	return b.Bytes()
}

// fakeTranscriber is an OpenAI-compatible /audio/transcriptions that records
// what arrived.
type fakeTranscriber struct {
	srv *httptest.Server
	mu  sync.Mutex
	got struct {
		path, auth, fileName, fileType string
		fields                         map[string]string
		file                           []byte
		contentLength                  int64
	}
	calls   atomic.Int32
	handler func(w http.ResponseWriter, r *http.Request) // nil = answer {"text":" hello "}
}

func newFakeTranscriber(t *testing.T) *fakeTranscriber {
	f := &fakeTranscriber{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if f.handler != nil {
			f.handler(w, r)
			return
		}
		f.record(r)
		writeJSON(w, map[string]string{"text": " hello "})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTranscriber) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got.path = r.URL.Path
	f.got.auth = r.Header.Get("Authorization")
	f.got.contentLength = r.ContentLength
	f.got.fields = map[string]string{}
	mr, err := r.MultipartReader()
	if err != nil {
		return
	}
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			f.got.fileName, f.got.fileType, f.got.file = p.FileName(), p.Header.Get("Content-Type"), b
			continue
		}
		f.got.fields[p.FormName()] = string(b)
	}
}

// setupTranscribe opens a db, stores cfg (nil = unset), points groq and openai
// at the fake, and stands in for the secret lookup with keys.
func setupTranscribe(t *testing.T, cfg *transcribeConfig, keys map[string]string) (*fakeTranscriber, *atomic.Int32) {
	t.Helper()
	openTestDB(t)
	f := newFakeTranscriber(t)
	oldDefaults := transcribeDefaults
	transcribeDefaults = map[string]transcribeConfig{}
	for k, v := range oldDefaults {
		v.BaseURL = f.srv.URL + "/" + k + "/v1"
		transcribeDefaults[k] = v
	}
	lookups := &atomic.Int32{}
	oldLookup := transcribeSecretLookup
	transcribeSecretLookup = func(_ context.Context, name string) (string, error) {
		lookups.Add(1)
		if v, ok := keys[name]; ok {
			return v, nil
		}
		return "", errors.New("exit status 1")
	}
	forgetTranscribeKeys()
	t.Cleanup(func() {
		transcribeDefaults = oldDefaults
		transcribeSecretLookup = oldLookup
		forgetTranscribeKeys()
	})
	if cfg != nil {
		b, _ := json.Marshal(cfg)
		if err := setSetting(transcribeSettingKey, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	return f, lookups
}

func postTranscribe(t *testing.T, query string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/transcribe"+query, bytes.NewReader(body))
	r.Header.Set("Content-Type", "audio/wav")
	w := httptest.NewRecorder()
	serveTranscribe(w, r)
	return w
}

func getTranscribe(t *testing.T) transcribeStatus {
	t.Helper()
	w := httptest.NewRecorder()
	serveTranscribe(w, httptest.NewRequest(http.MethodGet, "/api/transcribe", nil))
	var st transcribeStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET /api/transcribe: %d %s", w.Code, w.Body.String())
	}
	return st
}

func TestTranscribeOffByDefault(t *testing.T) {
	_, _ = setupTranscribe(t, nil, nil)
	st := getTranscribe(t)
	if st.Available || st.Provider != "off" || st.Reason == "" {
		t.Fatalf("GET = %+v", st)
	}
	if st.MaxSeconds != 120 || st.MaxBytes != 4194304 || len(st.Formats) != 1 || st.Formats[0] != "audio/wav" {
		t.Fatalf("limits = %+v", st)
	}
	w := postTranscribe(t, "", makeWAV(16000, 1, 500))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "off") {
		t.Fatalf("POST while off = %d %q", w.Code, w.Body.String())
	}
}

func TestTranscribeEachProvider(t *testing.T) {
	cases := []struct {
		name     string
		cfg      transcribeConfig
		keys     map[string]string
		wantAuth string
		wantPath string
		model    string
	}{
		{"groq", transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "gk"}, "Bearer gk", "/groq/v1/audio/transcriptions", "whisper-large-v3-turbo"},
		{"openai", transcribeConfig{Provider: "openai", Model: "whisper-1"}, map[string]string{"OPENAI_API_KEY": "ok"}, "Bearer ok", "/openai/v1/audio/transcriptions", "whisper-1"},
		{"compatible with key", transcribeConfig{Provider: "openai-compatible", BaseURL: "{srv}/local/v1/", Model: "parakeet", KeyName: "LOCAL_ASR_KEY"}, map[string]string{"LOCAL_ASR_KEY": "lk"}, "Bearer lk", "/local/v1/audio/transcriptions", "parakeet"},
		{"compatible without key", transcribeConfig{Provider: "openai-compatible", BaseURL: "{srv}/local/v1", Model: "whisper"}, nil, "", "/local/v1/audio/transcriptions", "whisper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := setupTranscribe(t, nil, tc.keys)
			cfg := tc.cfg
			cfg.BaseURL = strings.ReplaceAll(cfg.BaseURL, "{srv}", f.srv.URL)
			b, _ := json.Marshal(cfg)
			_ = setSetting(transcribeSettingKey, string(b))

			if st := getTranscribe(t); !st.Available || st.Provider != cfg.Provider || st.Model != tc.model {
				t.Fatalf("GET = %+v", st)
			}
			wav := makeWAV(16000, 1, 1500)
			w := postTranscribe(t, "?language=en&prompt="+url.QueryEscape("lasso, herdr"), wav)
			if w.Code != http.StatusOK {
				t.Fatalf("POST = %d %s", w.Code, w.Body.String())
			}
			var res transcribeResult
			if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
				t.Fatal(err)
			}
			if res.Text != "hello" || res.Engine != "lasso" || res.Provider != cfg.Provider || res.Model != tc.model ||
				res.Language != "en" || res.AudioMS != 1500 || res.ElapsedMS < 0 {
				t.Fatalf("answer = %+v", res)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			g := f.got
			if g.path != tc.wantPath || g.auth != tc.wantAuth {
				t.Fatalf("path %q auth %q", g.path, g.auth)
			}
			if g.fields["model"] != tc.model || g.fields["language"] != "en" || g.fields["prompt"] != "lasso, herdr" || g.fields["response_format"] != "json" {
				t.Fatalf("fields = %v", g.fields)
			}
			if g.fileName != "audio.wav" || g.fileType != "audio/wav" || !bytes.Equal(g.file, wav) {
				t.Fatalf("file %q %q %d bytes", g.fileName, g.fileType, len(g.file))
			}
			if g.contentLength <= int64(len(wav)) {
				t.Fatalf("no Content-Length on the upstream request: %d", g.contentLength)
			}
		})
	}
}

func TestTranscribeLimits(t *testing.T) {
	f, _ := setupTranscribe(t, &transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "k"})
	cases := []struct {
		name  string
		query string
		body  []byte
		want  int
	}{
		{"over 4 MiB", "", append(makeWAV(16000, 1, 1000), make([]byte, transcribeMaxBytes)...), http.StatusRequestEntityTooLarge},
		{"over 120 s", "", makeWAV(8000, 1, 121_000), http.StatusRequestEntityTooLarge},
		{"empty", "", nil, http.StatusBadRequest},
		{"no samples", "", makeWAV(16000, 1, 0), http.StatusBadRequest},
		{"not a WAV", "", []byte("OggS\x00\x02 definitely not a wav file"), http.StatusUnsupportedMediaType},
		{"24-bit", "", wavWith(1, 24, 16000, 1, 4800, 4800, nil), http.StatusUnsupportedMediaType},
		{"float", "", wavWith(3, 16, 16000, 1, 3200, 3200, nil), http.StatusUnsupportedMediaType},
		{"three channels", "", wavWith(1, 16, 16000, 3, 9600, 9600, nil), http.StatusUnsupportedMediaType},
		{"96 kHz", "", wavWith(1, 16, 96000, 1, 19200, 19200, nil), http.StatusUnsupportedMediaType},
		{"long prompt", "?prompt=" + strings.Repeat("x", transcribePromptMax+1), makeWAV(16000, 1, 500), http.StatusBadRequest},
		{"bad language", "?language=" + url.QueryEscape("en; rm"), makeWAV(16000, 1, 500), http.StatusBadRequest},
		{"exactly 120 s", "", makeWAV(8000, 1, 120_000), http.StatusOK},
		{"stereo 48 kHz", "", makeWAV(48000, 2, 2000), http.StatusOK},
		{"prompt at the limit", "?prompt=" + strings.Repeat("é", transcribePromptMax), makeWAV(16000, 1, 500), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.calls.Load()
			w := postTranscribe(t, tc.query, tc.body)
			if w.Code != tc.want {
				t.Fatalf("%d %q, want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.want != http.StatusOK && f.calls.Load() != before {
				t.Fatal("a refused request reached the provider")
			}
		})
	}
}

func TestTranscribeConcurrencyLimit(t *testing.T) {
	f, _ := setupTranscribe(t, &transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "k"})
	release := make(chan struct{})
	arrived := make(chan struct{}, 4)
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		arrived <- struct{}{}
		<-release
		writeJSON(w, map[string]string{"text": "ok"})
	}
	wav := makeWAV(16000, 1, 500)
	var wg sync.WaitGroup
	codes := make([]int, transcribeMaxInFlight)
	for i := range transcribeMaxInFlight {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = postTranscribe(t, "", wav).Code
		}()
	}
	for range transcribeMaxInFlight {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("the in-flight requests never reached the provider")
		}
	}
	if w := postTranscribe(t, "", wav); w.Code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", w.Code)
	}
	close(release)
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Fatalf("in-flight request %d = %d", i, c)
		}
	}
	// The slots are free again.
	if w := postTranscribe(t, "", wav); w.Code != http.StatusOK {
		t.Fatalf("after release = %d", w.Code)
	}
}

func TestTranscribeUpstreamTimeout(t *testing.T) {
	f, _ := setupTranscribe(t, &transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "k"})
	old := transcribeUpstreamTimeout
	transcribeUpstreamTimeout = 100 * time.Millisecond
	t.Cleanup(func() { transcribeUpstreamTimeout = old })
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		// Read the body first: the server only notices the client hanging up
		// once it is reading the connection again.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}
	if w := postTranscribe(t, "", makeWAV(16000, 1, 500)); w.Code != http.StatusGatewayTimeout {
		t.Fatalf("slow provider = %d %q, want 504", w.Code, w.Body.String())
	}
}

func TestTranscribeMissingKey(t *testing.T) {
	f, _ := setupTranscribe(t, &transcribeConfig{Provider: "openai"}, nil)
	st := getTranscribe(t)
	if st.Available || !strings.Contains(st.Reason, "OPENAI_API_KEY") {
		t.Fatalf("GET without a key = %+v", st)
	}
	w := postTranscribe(t, "", makeWAV(16000, 1, 500))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "OPENAI_API_KEY") {
		t.Fatalf("POST without a key = %d %q", w.Code, w.Body.String())
	}
	if f.calls.Load() != 0 {
		t.Fatal("reached the provider without a key")
	}
}

func TestTranscribeKeyCached(t *testing.T) {
	_, lookups := setupTranscribe(t, &transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "k"})
	for range 3 {
		if w := postTranscribe(t, "", makeWAV(16000, 1, 200)); w.Code != http.StatusOK {
			t.Fatalf("POST = %d", w.Code)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("key looked up %d times, want 1", n)
	}
	// A settings write drops the cache.
	postTranscribeConfig(t, `{"model":"whisper-large-v3"}`, http.StatusOK)
	postTranscribe(t, "", makeWAV(16000, 1, 200))
	if n := lookups.Load(); n != 2 {
		t.Fatalf("key looked up %d times after a settings write, want 2", n)
	}
}

func TestTranscribeProviderError(t *testing.T) {
	f, _ := setupTranscribe(t, &transcribeConfig{Provider: "groq"}, map[string]string{"GROQ_API_KEY": "sk-secret"})
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid API Key sk-secret"}}`)
	}
	w := postTranscribe(t, "", makeWAV(16000, 1, 200))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "sk-secret") {
		t.Fatalf("401 upstream = %d %q", w.Code, w.Body.String())
	}
	f.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}
	w = postTranscribe(t, "", makeWAV(16000, 1, 200))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "model not found") {
		t.Fatalf("400 upstream = %d %q", w.Code, w.Body.String())
	}
	f.handler = func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{"nope":1}`) }
	if w := postTranscribe(t, "", makeWAV(16000, 1, 200)); w.Code != http.StatusBadGateway {
		t.Fatalf("answer without text = %d", w.Code)
	}
}

func TestParseWAV(t *testing.T) {
	// A LIST chunk (odd-sized, so padded) before the data is skipped.
	list := append([]byte("LIST"), 3, 0, 0, 0, 'a', 'b', 'c', 0)
	info, err := parseWAV(wavWith(1, 16, 16000, 1, 32000, 32000, list))
	if err != nil || info.durationMS() != 1000 {
		t.Fatalf("with LIST: %+v %v", info, err)
	}
	// A streaming writer's placeholder size reads as the bytes present.
	info, err = parseWAV(wavWith(1, 16, 16000, 1, 0xFFFFFFFF, 16000, nil))
	if err != nil || info.DataBytes != 16000 || info.durationMS() != 500 {
		t.Fatalf("placeholder size: %+v %v", info, err)
	}
	// A header declaring more than 120 s is judged by what was sent.
	info, _ = parseWAV(wavWith(1, 16, 8000, 1, 8000*2*200, 8000*2*10, nil))
	if info.durationMS() != 10_000 {
		t.Fatalf("over-declared: %d ms", info.durationMS())
	}

	// WAVE_FORMAT_EXTENSIBLE with a PCM subformat is PCM.
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(0))
	b.WriteString("WAVEfmt ")
	le(uint32(40))
	le(uint16(0xFFFE))
	le(uint16(2))
	le(uint32(44100))
	le(uint32(44100 * 4))
	le(uint16(4))
	le(uint16(16))
	le(uint16(22))            // cbSize
	le(uint16(16))            // valid bits
	le(uint32(3))             // channel mask
	le(uint16(1))             // subformat: PCM
	b.Write(make([]byte, 14)) // rest of the GUID
	b.WriteString("data")
	le(uint32(44100 * 4))
	b.Write(make([]byte, 44100*4))
	if info, err := parseWAV(b.Bytes()); err != nil || info.Channels != 2 || info.durationMS() != 1000 {
		t.Fatalf("extensible: %+v %v", info, err)
	}

	for _, bad := range [][]byte{nil, []byte("RIFF"), []byte("RIFF\x00\x00\x00\x00WAVE"), []byte("RIFF\x00\x00\x00\x00WAVEdata\x00\x00\x00\x00")} {
		if _, err := parseWAV(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func postTranscribeConfig(t *testing.T, body string, want int) transcribeConfigView {
	t.Helper()
	w := httptest.NewRecorder()
	serveTranscribeConfig(w, httptest.NewRequest(http.MethodPost, "/api/transcribe/config", strings.NewReader(body)))
	if w.Code != want {
		t.Fatalf("POST config %s = %d %q, want %d", body, w.Code, w.Body.String(), want)
	}
	var v transcribeConfigView
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	return v
}

func TestTranscribeConfig(t *testing.T) {
	_, _ = setupTranscribe(t, nil, nil)

	for _, bad := range []string{
		`{"provider":"whisperx"}`,
		`{"provider":"openai-compatible"}`,
		`{"provider":"openai-compatible","base_url":"ftp://x/v1"}`,
		`{"provider":"openai-compatible","base_url":"http://u:p@127.0.0.1:8080/v1"}`,
		`{"provider":"groq","key_name":"GROQ KEY; rm"}`,
		`{"provider":"groq","model":"a b"}`,
	} {
		postTranscribeConfig(t, bad, http.StatusBadRequest)
	}
	if raw, _ := getSetting(transcribeSettingKey); raw != "" {
		t.Fatalf("a refused config was stored: %s", raw)
	}

	v := postTranscribeConfig(t, `{"provider":"groq"}`, http.StatusOK)
	if v.Provider != "groq" || v.Model != "whisper-large-v3-turbo" || v.KeyName != "GROQ_API_KEY" {
		t.Fatalf("groq defaults = %+v", v)
	}
	v = postTranscribeConfig(t, `{"model":"whisper-large-v3"}`, http.StatusOK)
	if v.Provider != "groq" || v.Model != "whisper-large-v3" {
		t.Fatalf("model patch = %+v", v)
	}
	// Changing the provider resets what the patch does not name.
	v = postTranscribeConfig(t, `{"provider":"openai"}`, http.StatusOK)
	if v.Model != "gpt-4o-mini-transcribe" || v.KeyName != "OPENAI_API_KEY" {
		t.Fatalf("provider switch kept groq's fields: %+v", v)
	}
	v = postTranscribeConfig(t, `{"provider":"openai-compatible","base_url":"http://127.0.0.1:8080/v1"}`, http.StatusOK)
	if v.KeyName != "" || v.BaseURL != "http://127.0.0.1:8080/v1" || v.Reason != "" {
		t.Fatalf("compatible = %+v", v)
	}

	// The same section over update_settings, key names only.
	model := "parakeet-tdt"
	_, out, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{Transcribe: &transcribeConfigPatch{Model: &model}})
	if err != nil {
		t.Fatalf("update_settings: %v", err)
	}
	if m, _ := out["transcribe"].(map[string]any); m["model"] != "parakeet-tdt" {
		t.Fatalf("update_settings transcribe = %v", out)
	}
	_, out, err = getSettingsTool(context.Background(), nil, getSettingsIn{Section: "transcribe"})
	if err != nil {
		t.Fatalf("get_settings: %v", err)
	}
	if m, _ := out["transcribe"].(map[string]any); m["provider"] != "openai-compatible" || m["model"] != "parakeet-tdt" {
		t.Fatalf("get_settings transcribe = %v", out)
	}
	bad := "smtp://x"
	if _, _, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{Transcribe: &transcribeConfigPatch{BaseURL: &bad}}); err == nil {
		t.Fatal("update_settings accepted a bad base_url")
	}
}

// lookupSecret is the shared resolver: the environment wins over the CLI.
func TestLookupSecretPrefersEnv(t *testing.T) {
	t.Setenv("LASSO_TEST_TRANSCRIBE_KEY", "from-env")
	if v, err := lookupSecret(context.Background(), "LASSO_TEST_TRANSCRIBE_KEY"); err != nil || v != "from-env" {
		t.Fatalf("lookupSecret = %q %v", v, err)
	}
}

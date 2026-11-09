package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Internal AI (docs/design/llm.md): one OpenAI-compatible endpoint that lasso
// uses for its own small errands, starting with naming new agents and terminal
// tabs (autotitle.go, terminaltitle.go). It is a lasso-host setting, like
// `transcribe`: the endpoint is reached from here, whichever host the agent was
// created on, and the API key is named, never stored in lasso.db: a variable in
// lasso's environment or a 1Password op:// reference (`op read`).
//
// GET  /api/llm/config        the stored choices and whether they are usable
// POST /api/llm/config        merge-patch them
// GET  /api/llm/models        the endpoint's /models, for the Settings picker
// POST /api/llm/test          one tiny completion, to prove the key works

const llmSettingKey = "llm"

const (
	// llmModelsMaxBytes and llmAnswerMaxBytes bound what lasso reads from the
	// endpoint, so a wrong URL pointing at something enormous cannot fill memory.
	llmModelsMaxBytes = 1 << 20
	llmAnswerMaxBytes = 1 << 20
	llmKeyTTL         = 10 * time.Minute
	llmModelsTTL      = 60 * time.Second
)

// Shared with transcription, which speaks the same OpenAI-compatible shape.
var (
	modelNameRE  = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,128}$`)
	secretNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	// opRefRE is a 1Password secret reference, op://vault/item/field (or
	// .../section/field). Spaces are allowed, as 1Password names have them;
	// control characters are not.
	opRefRE = regexp.MustCompile(`^op://[^/\x00-\x1f\x7f]+(/[^/\x00-\x1f\x7f]+){2,3}$`)
)

// isOpRef reports whether a key reference is a 1Password op:// reference.
func isOpRef(ref string) bool { return strings.HasPrefix(ref, "op://") }

// validKeyRef reports whether a settings key_name is an environment variable
// name or a well-formed op:// reference.
func validKeyRef(ref string) bool {
	if isOpRef(ref) {
		return len(ref) <= 512 && opRefRE.MatchString(ref)
	}
	return secretNameRE.MatchString(ref)
}

// missingKeyHint says how to supply the key a reference names.
func missingKeyHint(ref string, err error) string {
	if isOpRef(ref) {
		if err != nil {
			return err.Error()
		}
		return "`op read " + ref + "` failed"
	}
	return "set " + ref + " in lasso's environment"
}

var (
	// llmModelsTimeout bounds the Settings picker's fetch; llmChatTimeout bounds
	// one completion (the titler's own 90s budget).
	llmModelsTimeout = 10 * time.Second
	llmChatTimeout   = 90 * time.Second
	llmHTTPClient    = &http.Client{}
	llmSecretLookup  = lookupSecret
	llmModelsCache   = struct {
		sync.Mutex
		sig    string
		at     time.Time
		models []string
	}{}
	llmKeys = struct {
		sync.Mutex
		m map[string]cachedLLMKey
	}{m: map[string]cachedLLMKey{}}
)

type cachedLLMKey struct {
	value string
	at    time.Time
}

// llmConfig is the `llm` setting as stored: only explicit choices.
type llmConfig struct {
	BaseURL string `json:"base_url,omitempty"`
	Model   string `json:"model,omitempty"`
	KeyName string `json:"key_name,omitempty"`
}

// The states GET /api/llm/config reports.
const (
	llmStateUnconfigured = "unconfigured"
	llmStateIncomplete   = "incomplete"
	llmStateReady        = "ready"
	llmStateUnreadable   = "unreadable"
)

// loadLLMConfig reads the setting; unset reads as the zero config.
func loadLLMConfig() (llmConfig, error) {
	if db == nil {
		return llmConfig{}, nil
	}
	raw, _ := getSetting(llmSettingKey)
	if raw == "" {
		return llmConfig{}, nil
	}
	var c llmConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return llmConfig{}, errors.New("the llm setting is unreadable; set it again in lasso's settings")
	}
	return c, nil
}

// validate checks a config a caller wants stored. An empty base URL clears the
// endpoint; a base URL without a model is a draft the Settings tab can hold
// while someone is still picking one (state "incomplete").
func (c llmConfig) validate() error {
	if c.BaseURL != "" {
		if err := validAPIBaseURL(c.BaseURL); err != nil {
			return fmt.Errorf("llm.base_url %s", err)
		}
	}
	if c.Model != "" && !modelNameRE.MatchString(c.Model) {
		return errors.New("llm.model: letters, digits and ._:/@+- only, at most 128")
	}
	if c.KeyName != "" && !validKeyRef(c.KeyName) {
		return errors.New("llm.key_name: an environment variable name (letters, digits, underscore) or an op://vault/item/field reference")
	}
	return nil
}

// configured reports whether the endpoint can be used: a base URL and a model.
func (c llmConfig) configured() bool { return c.BaseURL != "" && c.Model != "" }

// state is what GET /api/llm/config answers, and what the Settings group shows.
func (c llmConfig) state() string {
	switch {
	case c.BaseURL == "":
		return llmStateUnconfigured
	case c.Model == "":
		return llmStateIncomplete
	default:
		return llmStateReady
	}
}

// validAPIBaseURL checks an OpenAI-compatible API root as lasso stores one: an
// http(s) URL with no credentials, query or fragment. Transcription's base_url
// is checked by the same rule.
func validAPIBaseURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an http or https URL, e.g. http://127.0.0.1:8080/v1")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must carry no credentials, query or fragment")
	}
	return nil
}

// llmConfigured reports whether lasso's own AI errands should use an endpoint.
// A nil db (tests, shutdown) reads as no, which keeps the CLI chain as it was.
func llmConfigured() bool {
	c, err := loadLLMConfig()
	return err == nil && c.configured()
}

// ---- the key cache ------------------------------------------------------------

// llmKey resolves the key named name, from memory when it was resolved within
// llmKeyTTL. Failures are not cached, so a key set a moment ago is picked up on
// the next request. The value never leaves this process.
func llmKey(ctx context.Context, name string) (string, error) {
	llmKeys.Lock()
	if k, ok := llmKeys.m[name]; ok && time.Since(k.at) < llmKeyTTL {
		llmKeys.Unlock()
		return k.value, nil
	}
	llmKeys.Unlock()
	v, err := llmSecretLookup(ctx, name)
	if err != nil {
		return "", err
	}
	llmKeys.Lock()
	llmKeys.m[name] = cachedLLMKey{value: v, at: time.Now()}
	llmKeys.Unlock()
	return v, nil
}

// forgetLLMKeys drops every cached key, so a settings change takes effect at
// once.
func forgetLLMKeys() {
	llmKeys.Lock()
	clear(llmKeys.m)
	llmKeys.Unlock()
}

// ---- the endpoint calls -------------------------------------------------------

// llmURL joins the configured API root with a path.
func llmURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// llmReason turns an endpoint's failed answer into one line, capped and with
// the key scrubbed out — an error is the one place a provider's echo could
// otherwise carry the credential into a log or a toast.
func llmReason(what string, status int, raw []byte, key string) error {
	msg := ""
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			msg = v
		case map[string]any:
			msg, _ = v["message"].(string)
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(strings.SplitN(string(raw), "\n", 2)[0])
	}
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[redacted]")
	}
	if r := []rune(msg); len(r) > 200 {
		msg = string(r[:200]) + "…"
	}
	if msg == "" {
		return fmt.Errorf("%s answered %d", what, status)
	}
	return fmt.Errorf("%s answered %d: %s", what, status, msg)
}

// llmModelIDs fetches the endpoint's /models for the Settings picker, using the
// short cache unless refresh is set.
func llmModelIDs(ctx context.Context, refresh bool) ([]string, error) {
	c, err := loadLLMConfig()
	if err != nil {
		return nil, err
	}
	if c.BaseURL == "" {
		return nil, errors.New("no internal AI endpoint is configured")
	}
	sig := c.BaseURL + "\x00" + c.KeyName
	llmModelsCache.Lock()
	if !refresh && llmModelsCache.sig == sig && time.Since(llmModelsCache.at) < llmModelsTTL {
		models := append([]string(nil), llmModelsCache.models...)
		llmModelsCache.Unlock()
		return models, nil
	}
	llmModelsCache.Unlock()

	key := ""
	if c.KeyName != "" {
		if key, err = llmKey(ctx, c.KeyName); err != nil {
			return nil, fmt.Errorf("no key for internal AI: %s", missingKeyHint(c.KeyName, err))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, llmModelsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, llmURL(c.BaseURL, "models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := llmHTTPClient.Do(req)
	if err != nil {
		return nil, errors.New("the endpoint is unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, llmModelsMaxBytes+1))
	if err != nil {
		return nil, errors.New("reading the endpoint's answer failed")
	}
	if len(raw) > llmModelsMaxBytes {
		return nil, fmt.Errorf("the endpoint's /models answer is larger than %d bytes", llmModelsMaxBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, llmReason("the endpoint", resp.StatusCode, raw, key)
	}
	models, err := parseLLMModels(raw)
	if err != nil {
		return nil, err
	}
	llmModelsCache.Lock()
	llmModelsCache.sig, llmModelsCache.at, llmModelsCache.models = sig, time.Now(), models
	llmModelsCache.Unlock()
	return models, nil
}

// parseLLMModels reads the two shapes an OpenAI-compatible /models serves:
// OpenAI's {"data":[{"id":…}]} and Ollama's {"models":[{"name":…}]}.
func parseLLMModels(raw []byte) ([]string, error) {
	var v struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errors.New("the endpoint's /models answer is not JSON")
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range v.Data {
		add(m.ID)
	}
	for _, m := range v.Models {
		add(firstNonEmpty(m.ID, m.Name))
	}
	if len(out) == 0 {
		return nil, errors.New("the endpoint listed no models")
	}
	sort.Strings(out)
	return out, nil
}

// ---- one completion -----------------------------------------------------------

// llmComplete sends one non-streaming chat completion and returns its text.
// The instruction is the whole conversation (a single user turn): the titler's
// rules already tell the model not to attempt the task.
func llmComplete(ctx context.Context, instruction string) (string, error) {
	c, err := loadLLMConfig()
	if err != nil {
		return "", err
	}
	if !c.configured() {
		return "", errors.New("no internal AI endpoint is configured (set one in lasso's settings)")
	}
	key := ""
	if c.KeyName != "" {
		if key, err = llmKey(ctx, c.KeyName); err != nil {
			return "", fmt.Errorf("no key for internal AI: %s", missingKeyHint(c.KeyName, err))
		}
	}
	body, err := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    []map[string]string{{"role": "user", "content": instruction}},
		"temperature": 0,
		"max_tokens":  200,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, llmURL(c.BaseURL, "chat/completions"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := llmHTTPClient.Do(req)
	if err != nil {
		return "", errors.New("the endpoint is unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, llmAnswerMaxBytes+1))
	if err != nil {
		return "", errors.New("reading the endpoint's answer failed")
	}
	if len(raw) > llmAnswerMaxBytes {
		return "", fmt.Errorf("the endpoint's answer is larger than %d bytes", llmAnswerMaxBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return "", llmReason("the endpoint", resp.StatusCode, raw, key)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", errors.New("the endpoint's chat answer is not JSON")
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", errors.New("the endpoint answered no text")
	}
	return out.Choices[0].Message.Content, nil
}

// llmTitle is the titler's entry point: one completion under the titler's own
// budget, so a hung endpoint falls through to the CLIs like a hung one does.
func llmTitle(instruction string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), llmChatTimeout)
	defer cancel()
	return llmComplete(ctx, instruction)
}

// ---- routes -------------------------------------------------------------------

// llmConfigView is the setting as the Settings group shows it. Key NAMES only:
// a key's value is never read here, let alone returned.
type llmConfigView struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	KeyName string `json:"key_name"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
}

// llmConfigPatch is a POST to /api/llm/config: only the named fields change,
// "" clears one.
type llmConfigPatch struct {
	BaseURL *string `json:"base_url,omitempty" jsonschema:"The OpenAI-compatible API root, e.g. http://127.0.0.1:8080/v1. \"\" turns internal AI off."`
	Model   *string `json:"model,omitempty" jsonschema:"The model id the endpoint serves (GET /api/llm/models lists them)."`
	KeyName *string `json:"key_name,omitempty" jsonschema:"Where the API key comes from, never the key itself: the NAME of an environment variable in lasso's environment, or a 1Password reference op://vault/item/field read with op read. Empty sends no Authorization header."`
}

func llmConfigViewOf(c llmConfig, loadErr error) llmConfigView {
	v := llmConfigView{BaseURL: c.BaseURL, Model: c.Model, KeyName: c.KeyName, State: c.state()}
	if loadErr != nil {
		v.State, v.Reason = llmStateUnreadable, loadErr.Error()
	}
	return v
}

// serveLLMConfig is /api/llm/config, the `llm` section of the settings
// (update_settings writes it through here).
func serveLLMConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, err := loadLLMConfig()
		writeJSON(w, llmConfigViewOf(c, err))
	case http.MethodPost:
		var p llmConfigPatch
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, _ := loadLLMConfig() // an unreadable stored value is replaced
		if p.BaseURL != nil {
			c.BaseURL = strings.TrimSpace(*p.BaseURL)
		}
		if p.Model != nil {
			c.Model = strings.TrimSpace(*p.Model)
		}
		if p.KeyName != nil {
			c.KeyName = strings.TrimSpace(*p.KeyName)
		}
		if err := c.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(c)
		if err := setSetting(llmSettingKey, string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		forgetLLMKeys()
		llmModelsCache.Lock()
		llmModelsCache.sig, llmModelsCache.at, llmModelsCache.models = "", time.Time{}, nil
		llmModelsCache.Unlock()
		writeJSON(w, llmConfigViewOf(c, nil))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// serveLLMModels is /api/llm/models: the endpoint's catalog for the picker.
// A fetch failure is reported in the body rather than as an HTTP error, since
// the group around it is still usable (a model can be typed in by hand).
func serveLLMModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	models, err := llmModelIDs(r.Context(), r.URL.Query().Get("refresh") != "")
	if err != nil {
		writeJSON(w, map[string]any{"models": []string{}, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"models": models})
}

// serveLLMTest is /api/llm/test: one tiny completion, so the Settings group
// can prove the endpoint and key work.
func serveLLMTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), min(llmChatTimeout, 20*time.Second))
	defer cancel()
	start := time.Now()
	_, err := llmComplete(ctx, "Reply with the single word: ok")
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "elapsed_ms": time.Since(start).Milliseconds()})
}

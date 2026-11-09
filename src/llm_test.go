package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeLLM is an OpenAI-compatible endpoint: /v1/models and
// /v1/chat/completions. The handlers can be replaced per test.
type fakeLLM struct {
	srv         *httptest.Server
	models      http.HandlerFunc
	chat        http.HandlerFunc
	lastAuth    string
	lastChatReq map[string]any
}

func newFakeLLM(t *testing.T) *fakeLLM {
	t.Helper()
	f := &fakeLLM{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		if f.models != nil {
			f.models(w, r)
			return
		}
		io.WriteString(w, `{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-a"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		f.lastChatReq = map[string]any{}
		_ = json.Unmarshal(body, &f.lastChatReq)
		if f.chat != nil {
			f.chat(w, r)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"name: SSO loop\ntitle: Fix the SSO loop"}}]}`)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// setupLLM opens a db, clears every cache the endpoint's answers live in, and
// stands in for the secret lookup with keys.
func setupLLM(t *testing.T, cfg *llmConfig, keys map[string]string) *fakeLLM {
	t.Helper()
	openTestDB(t)
	f := newFakeLLM(t)
	oldLookup := llmSecretLookup
	llmSecretLookup = func(_ context.Context, name string) (string, error) {
		if v, ok := keys[name]; ok {
			return v, nil
		}
		return "", errors.New("exit status 1")
	}
	t.Cleanup(func() {
		llmSecretLookup = oldLookup
		forgetLLMKeys()
		llmModelsCache.Lock()
		llmModelsCache.sig, llmModelsCache.models = "", nil
		llmModelsCache.Unlock()
	})
	if cfg != nil {
		b, _ := json.Marshal(*cfg)
		if err := setSetting(llmSettingKey, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func configureLLM(t *testing.T, f *fakeLLM, model string) {
	t.Helper()
	b, _ := json.Marshal(llmConfig{BaseURL: f.srv.URL + "/v1", Model: model, KeyName: "LLM_KEY"})
	if err := setSetting(llmSettingKey, string(b)); err != nil {
		t.Fatal(err)
	}
	llmModelsCache.Lock()
	llmModelsCache.sig, llmModelsCache.models = "", nil
	llmModelsCache.Unlock()
}

func getLLMConfig(t *testing.T) llmConfigView {
	t.Helper()
	w := httptest.NewRecorder()
	serveLLMConfig(w, httptest.NewRequest(http.MethodGet, "/api/llm/config", nil))
	var v llmConfigView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET /api/llm/config: %d %s", w.Code, w.Body.String())
	}
	return v
}

func postLLMConfig(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	serveLLMConfig(w, httptest.NewRequest(http.MethodPost, "/api/llm/config", strings.NewReader(body)))
	return w
}

func TestLLMOffByDefault(t *testing.T) {
	_ = setupLLM(t, nil, nil)
	if v := getLLMConfig(t); v.State != llmStateUnconfigured || v.BaseURL != "" {
		t.Fatalf("GET = %+v, want unconfigured", v)
	}
	if llmConfigured() {
		t.Fatal("llmConfigured is on with no setting")
	}
	w := httptest.NewRecorder()
	serveLLMModels(w, httptest.NewRequest(http.MethodGet, "/api/llm/models", nil))
	var out struct {
		Models []string `json:"models"`
		Error  string   `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Models) != 0 || !strings.Contains(out.Error, "no internal AI endpoint") {
		t.Fatalf("GET /api/llm/models = %d %s", w.Code, w.Body.String())
	}
}

func TestLLMConfigValidationAndState(t *testing.T) {
	f := setupLLM(t, nil, nil)
	for _, body := range []string{
		`{"base_url":"ftp://x/v1"}`,
		`{"base_url":"http://u:p@127.0.0.1:8080/v1"}`,
		`{"base_url":"http://127.0.0.1:8080/v1?x=1"}`,
		`{"base_url":"http://127.0.0.1:8080/v1","model":"a b"}`,
		`{"base_url":"http://127.0.0.1:8080/v1","key_name":"LLM KEY; rm"}`,
	} {
		if w := postLLMConfig(t, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", body, w.Code, w.Body.String())
		}
	}

	// A base URL without a model is a draft the group can hold: saved, but not
	// usable yet.
	w := postLLMConfig(t, `{"base_url":"`+f.srv.URL+`/v1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("draft save = %d %s", w.Code, w.Body.String())
	}
	if v := getLLMConfig(t); v.State != llmStateIncomplete || v.BaseURL == "" {
		t.Fatalf("draft = %+v, want incomplete", v)
	}
	if llmConfigured() {
		t.Fatal("a draft endpoint reads as configured")
	}

	w = postLLMConfig(t, `{"base_url":"`+f.srv.URL+`/v1","model":"model-a","key_name":"LLM_KEY"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save = %d %s", w.Code, w.Body.String())
	}
	v := getLLMConfig(t)
	if v.State != llmStateReady || v.Model != "model-a" || v.KeyName != "LLM_KEY" {
		t.Fatalf("saved = %+v", v)
	}
	if !llmConfigured() {
		t.Fatal("a base URL and model read as unconfigured")
	}
	// The answer carries the key's NAME, never a value.
	if strings.Contains(w.Body.String(), "sk-") {
		t.Fatalf("config answer leaked a key: %s", w.Body.String())
	}
}

func TestLLMModelsSortedAndDeduped(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")
	models, err := llmModelIDs(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "model-a" || models[1] != "model-b" {
		t.Fatalf("models = %v", models)
	}
	if f.lastAuth != "Bearer sk-live" {
		t.Errorf("Authorization = %q", f.lastAuth)
	}
}

func TestLLMModelsAcceptsOllamaShape(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")
	f.models = func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"models":[{"name":"llama3"},{"name":"qwen3"}]}`)
	}
	models, err := llmModelIDs(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "llama3,qwen3" {
		t.Fatalf("models = %v", models)
	}
}

// An endpoint's error is shown, but never with the key in it.
func TestLLMErrorScrubsTheKey(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-secret"})
	configureLLM(t, f, "model-a")
	f.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"bad key sk-secret"}}`)
	}
	_, err := llmComplete(context.Background(), "name this")
	if err == nil {
		t.Fatal("a 401 answered no error")
	}
	if strings.Contains(err.Error(), "sk-secret") || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error = %q", err)
	}
}

func TestLLMCompleteSendsOneChatTurn(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")
	out, err := llmComplete(context.Background(), "Name the task")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Fix the SSO loop") {
		t.Fatalf("answer = %q", out)
	}
	if f.lastChatReq["model"] != "model-a" {
		t.Errorf("model = %v", f.lastChatReq["model"])
	}
	msgs, _ := f.lastChatReq["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", f.lastChatReq["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "Name the task" {
		t.Errorf("message = %v", first)
	}
	if f.lastChatReq["temperature"] != float64(0) {
		t.Errorf("temperature = %v", f.lastChatReq["temperature"])
	}
	if f.lastAuth != "Bearer sk-live" {
		t.Errorf("Authorization = %q", f.lastAuth)
	}
}

// A configured endpoint is asked FIRST — that is why it was configured — and a
// usable answer from it means no CLI runs at all.
func TestTitlerAsksLLMFirst(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")
	cliCalls := 0
	old := titlerRunner
	titlerRunner = func(titler, string) (string, error) {
		cliCalls++
		return "name: CLI\ntitle: From the CLI\n", nil
	}
	t.Cleanup(func() { titlerRunner = old })

	titles, err := generateAgentTitle("fix the SSO login loop")
	if err != nil {
		t.Fatal(err)
	}
	if titles.Title != "Fix the SSO loop" || titles.Name != "SSO loop" {
		t.Fatalf("titles = %+v", titles)
	}
	if cliCalls != 0 {
		t.Fatalf("CLIs ran %d times after the endpoint answered", cliCalls)
	}
	msgs, _ := f.lastChatReq["messages"].([]any)
	first, _ := msgs[0].(map[string]any)
	if !strings.Contains(first["content"].(string), "fix the SSO login loop") {
		t.Errorf("the prompt did not reach the endpoint: %v", first["content"])
	}
}

// A broken endpoint is one failure in the chain, not the end of it: the CLIs
// still name the agent.
func TestTitlerFallsBackToCLIWhenLLMFails(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")
	f.chat = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"boom"}`)
	}
	old := titlerRunner
	titlerRunner = func(titler, string) (string, error) {
		return "name: CLI\ntitle: From the CLI\n", nil
	}
	t.Cleanup(func() { titlerRunner = old })

	titles, err := generateAgentTitle("fix the SSO login loop")
	if err != nil {
		t.Fatal(err)
	}
	if titles.Title != "From the CLI" {
		t.Fatalf("titles = %+v, want the CLI's", titles)
	}
}

// get_settings section llm lists the endpoint's models, while the all-sections
// read stays offline; update_settings writes the same field the Settings group
// does.
func TestSettingsLLMSection(t *testing.T) {
	f := setupLLM(t, nil, map[string]string{"LLM_KEY": "sk-live"})
	configureLLM(t, f, "model-a")

	_, out, err := getSettingsTool(context.Background(), nil, getSettingsIn{Section: "llm"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out["llm"].(map[string]any)
	if m["model"] != "model-a" {
		t.Fatalf("section llm = %v", out["llm"])
	}
	models, _ := m["models"].([]string)
	if len(models) != 2 {
		t.Fatalf("models = %v", m["models"])
	}

	_, all, err := getSettingsTool(context.Background(), nil, getSettingsIn{})
	if err != nil {
		t.Fatal(err)
	}
	am, _ := all["llm"].(map[string]any)
	if _, ok := am["models"]; ok {
		t.Fatalf("the all-sections read fetched models: %v", am)
	}

	base := f.srv.URL + "/v1"
	_, up, err := updateSettingsTool(context.Background(), nil, updateSettingsIn{
		LLM: &llmConfigPatch{BaseURL: &base, Model: ptr("model-b")},
	})
	if err != nil {
		t.Fatal(err)
	}
	um, _ := up["llm"].(map[string]any)
	if um["model"] != "model-b" || um["state"] != llmStateReady {
		t.Fatalf("update_settings llm = %v", up["llm"])
	}
}

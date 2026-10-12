package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDecodeBotMeta(t *testing.T) {
	ok := []string{``, `null`, `{}`, `{"input":"voice","engine":"lasso","model":"whisper-large-v3-turbo"}`}
	for _, raw := range ok {
		if _, err := decodeBotMeta(json.RawMessage(raw)); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	if m, _ := decodeBotMeta(json.RawMessage(`{}`)); m != nil {
		t.Error("{} is not none")
	}

	many := map[string]string{}
	for i := range botMetaMaxKeys + 1 {
		many["k_"+strings.Repeat("a", i+1)] = "v"
	}
	manyJSON, _ := json.Marshal(many)
	bad := []string{
		`[]`, `"voice"`, `{"input":1}`, `{"input":{"a":"b"}}`, `{"input":true}`,
		`{"Input":"voice"}`, `{"in-put":"voice"}`, `{"input2":"voice"}`,
		`{"` + strings.Repeat("a", 33) + `":"x"}`,
		string(manyJSON),
		`{"note":"` + strings.Repeat("x", botMetaMaxBytes) + `"}`,
	}
	for _, raw := range bad {
		if _, err := decodeBotMeta(json.RawMessage(raw)); err == nil {
			t.Errorf("%.60s accepted", raw)
		}
	}
	// Exactly at the key limit is fine.
	sixteen := map[string]string{}
	for i := range botMetaMaxKeys {
		sixteen["k_"+strings.Repeat("a", i+1)] = "v"
	}
	b, _ := json.Marshal(sixteen)
	if _, err := decodeBotMeta(b); err != nil {
		t.Errorf("16 keys refused: %v", err)
	}
}

func TestStoreBotMetaKeepsNewest(t *testing.T) {
	openTestDB(t)
	base := time.UnixMilli(1_700_000_000_000)
	for i := range botMetaKeep + 20 {
		if err := storeBotMeta("ava", fmt.Sprintf("msg %d", i), base.Add(time.Duration(i)*time.Second), map[string]string{"n": fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = storeBotMeta("other", "x", base, map[string]string{"n": "x"})
	var n, oldest int64
	db.QueryRow(`SELECT COUNT(*), MIN(sent_at) FROM bot_message_meta WHERE bot = 'ava'`).Scan(&n, &oldest)
	if n != botMetaKeep || oldest != base.Add(20*time.Second).UnixMilli() {
		t.Fatalf("kept %d rows, oldest %d", n, oldest)
	}
	db.QueryRow(`SELECT COUNT(*) FROM bot_message_meta WHERE bot = 'other'`).Scan(&n)
	if n != 1 {
		t.Fatalf("another bot's rows pruned: %d", n)
	}
	if err := deleteBot("ava"); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM bot_message_meta WHERE bot = 'ava'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a deleted bot kept %d meta rows", n)
	}
}

func TestAnnotateBotMeta(t *testing.T) {
	openTestDB(t)
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return t0.Add(d).Format(time.RFC3339Nano) }
	items := func() []chatItem {
		return []chatItem{
			{Kind: "user", ID: "u1", At: at(0), Text: "same words"},
			{Kind: "agent", ID: "a1", At: at(time.Second), Text: "same words"},
			{Kind: "user", ID: "u2", At: at(time.Minute), Text: "other words"},
			{Kind: "user", ID: "u3", At: at(2 * time.Minute), Text: "same words"},
			{Kind: "user", ID: "u4", At: at(3 * time.Minute), Text: "typed later"},
		}
	}
	voice := map[string]string{"input": "voice", "engine": "lasso"}
	typed := map[string]string{"input": "keyboard"}

	// The same words sent twice with meta: each row annotates its own message.
	// sent_at is taken before the RPC, but omp's clock may run behind ours.
	_ = storeBotMeta("bo", "  same words\n", t0.Add(2*time.Second), voice)
	_ = storeBotMeta("bo", "same words", t0.Add(2*time.Minute), typed)
	got := items()
	annotateBotMeta("bo", got)
	if !maps.Equal(got[0].Meta, voice) || !maps.Equal(got[3].Meta, typed) {
		t.Fatalf("u1 %v u3 %v", got[0].Meta, got[3].Meta)
	}
	if got[1].Meta != nil || got[2].Meta != nil || got[4].Meta != nil {
		t.Fatalf("unrelated rows annotated: %+v", got)
	}

	// A row sent long after a message cannot claim it (beyond the skew).
	_ = storeBotMeta("late", "same words", t0.Add(10*time.Second), voice)
	got = items()
	annotateBotMeta("late", got)
	if got[0].Meta != nil || !maps.Equal(got[3].Meta, voice) {
		t.Fatalf("skew: u1 %v u3 %v", got[0].Meta, got[3].Meta)
	}

	// Two rows for one message: the second finds nothing left to claim.
	_ = storeBotMeta("dup", "typed later", t0.Add(3*time.Minute), voice)
	_ = storeBotMeta("dup", "typed later", t0.Add(3*time.Minute+time.Second), typed)
	got = items()
	annotateBotMeta("dup", got)
	if !maps.Equal(got[4].Meta, voice) {
		t.Fatalf("dup: %v", got[4].Meta)
	}

	// A bot without rows changes nothing.
	got = items()
	annotateBotMeta("nobody", got)
	for _, it := range got {
		if it.Meta != nil {
			t.Fatalf("annotated without rows: %+v", it)
		}
	}
}

// End to end: the send handler stores meta only for a confirmed send, and the
// chat it reads back carries it on the user's row and on nothing else.
func TestOmpBotSendMeta(t *testing.T) {
	r, _ := useOmpBotEnv(t, "metta")
	b, _ := botBackend("local")
	if err := startBot(b, r, true); err != nil {
		t.Fatal(err)
	}
	ompWaitFor(t, "idle", func() bool { return botStatus(b, r, nil).State == "idle" })

	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		serveBotChat(w, httptest.NewRequest(http.MethodPost, "/api/bots/metta/chat/send", bytes.NewBufferString(body)), b, r, "send")
		return w
	}
	if w := send(`{"text":"hi","meta":{"Input":"voice"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad meta = %d", w.Code)
	}
	if w := send(`{"text":"hello there","meta":{"input":"voice","engine":"lasso"}}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), chatSent) {
		t.Fatalf("send = %d %s", w.Code, w.Body.String())
	}
	ompWaitFor(t, "the answer", func() bool { return botStatus(b, r, nil).LastText == "done: hello there" })
	if w := send(`{"text":"no meta here"}`); w.Code != http.StatusOK {
		t.Fatalf("plain send = %d", w.Code)
	}
	ompWaitFor(t, "the second answer", func() bool { return botStatus(b, r, nil).LastText == "done: no meta here" })

	w := httptest.NewRecorder()
	serveBotChat(w, httptest.NewRequest(http.MethodGet, "/api/bots/metta/chat", nil), b, r, "")
	var chat struct {
		Items []chatItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &chat); err != nil {
		t.Fatal(err)
	}
	var annotated []string
	for _, it := range chat.Items {
		if it.Meta != nil {
			annotated = append(annotated, it.Kind+":"+it.Text)
			if it.Meta["input"] != "voice" || it.Meta["engine"] != "lasso" {
				t.Fatalf("meta = %v", it.Meta)
			}
		}
	}
	if len(annotated) != 1 || annotated[0] != "user:hello there" {
		t.Fatalf("annotated rows = %q (items %+v)", annotated, chat.Items)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM bot_message_meta WHERE bot = 'metta'`).Scan(&n)
	if n != 1 {
		t.Fatalf("stored %d meta rows, want 1 (the refused and the plain send store none)", n)
	}
}

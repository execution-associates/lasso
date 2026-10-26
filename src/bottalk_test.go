package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A 1x1 PNG.
const talkTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// testTalkBot is testJobBot's bot with Talk on and a channel that has the
// reply tool, listening now. The wait reads no herdr: look answers what the
// test puts in it.
func testTalkBot(t *testing.T) (*botRecord, string, func(talkLook)) {
	t.Helper()
	rec := testJobBot(t)
	if err := botTalkEnable(rec); err != nil {
		t.Fatal(err)
	}
	key, _, _ := botTalkSettings(rec.Name)
	botChannelSeenAt.Delete(rec.Name)
	botChannelSeen(rec.Name, time.Now(), true)
	var mu sync.Mutex
	look := talkLook{state: "working"}
	set := func(l talkLook) { mu.Lock(); look = l; mu.Unlock() }
	prevLook, prevPoll, prevLocal, prevIdle := botTalkLookFn, botTalkPoll, botTalkLookLocal, botTalkIdleGrace
	botTalkLookFn = func(Backend, *botRecord, bool) talkLook { mu.Lock(); defer mu.Unlock(); return look }
	botTalkPoll, botTalkLookLocal, botTalkIdleGrace = 10*time.Millisecond, 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		botTalkLookFn, botTalkPoll, botTalkLookLocal, botTalkIdleGrace = prevLook, prevPoll, prevLocal, prevIdle
	})
	return rec, key, set
}

func talkPost(t *testing.T, srv *httptest.Server, path, key, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// talkChatID waits for the request's event and answers its chat_id.
func talkChatID(t *testing.T, bot string, after int64) (string, int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var chat string
		var id int64
		if db.QueryRow(`SELECT chat_id, id FROM bot_events WHERE bot = ? AND kind = 'talk' AND id > ? ORDER BY id DESC LIMIT 1`, bot, after).Scan(&chat, &id) == nil {
			return chat, id
		}
		if time.Now().After(deadline) {
			t.Fatal("no talk event queued")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Role             string `json:"role"`
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// readSSE reads a whole talk stream: the chunks, whether [DONE] came, and
// how many keepalive comments.
func readSSE(t *testing.T, r io.Reader) (chunks []sseChunk, done bool, comments int) {
	t.Helper()
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ":"):
			comments++
		case line == "data: [DONE]":
			done = true
		case strings.HasPrefix(line, "data: "):
			var c sseChunk
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c); err != nil {
				t.Fatalf("bad chunk %q: %v", line, err)
			}
			chunks = append(chunks, c)
		}
	}
	return
}

func sseText(chunks []sseChunk) (content, reasoning string) {
	for _, c := range chunks {
		if len(c.Choices) > 0 {
			content += c.Choices[0].Delta.Content
			reasoning += c.Choices[0].Delta.ReasoningContent
		}
	}
	return
}

func TestBotTalkTurn(t *testing.T) {
	msg := func(role, text string) talkMessage {
		b, _ := json.Marshal(text)
		return talkMessage{Role: role, Content: b}
	}
	got := botTalkTurn([]talkMessage{
		msg("system", "you are a model"), msg("user", "old"), msg("assistant", "old answer"),
		msg("user", "one"), msg("system", "late system"), msg("user", "two"),
	})
	if len(got) != 2 || string(got[0].Content) != `"one"` || string(got[1].Content) != `"two"` {
		t.Fatalf("turn = %+v", got)
	}
	if got := botTalkTurn([]talkMessage{msg("user", "q"), msg("assistant", "a")}); len(got) != 0 {
		t.Errorf("a history ending on the assistant has no turn: %+v", got)
	}
}

func TestBotTalkParts(t *testing.T) {
	text, imgs, err := botTalkParts(json.RawMessage(`"plain words"`))
	if err != nil || text != "plain words" || imgs != nil {
		t.Fatalf("string content = %q %v %v", text, imgs, err)
	}
	parts := `[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + talkTestPNG + `"}},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}},{"type":"file","file":{}}]`
	text, imgs, err = botTalkParts(json.RawMessage(parts))
	if err != nil || len(imgs) != 1 || imgs[0].ext != "png" {
		t.Fatalf("parts = %q %v %v", text, imgs, err)
	}
	for _, want := range []string{"look", "Image link (not opened by lasso): https://example.com/a.png", "left out"} {
		if !strings.Contains(text, want) {
			t.Errorf("text %q lacks %q", text, want)
		}
	}
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if _, _, err := botTalkParts(json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/svg+xml;base64,` + svg + `"}}]`)); err == nil {
		t.Error("an SVG was accepted")
	}
	if _, _, err := botTalkParts(json.RawMessage(`[{"type":"image_url","image_url":{"url":"data:image/png,notbase64"}}]`)); err == nil {
		t.Error("a non-base64 data URL was accepted")
	}
	five := `[` + strings.Repeat(`{"type":"image_url","image_url":{"url":"data:image/png;base64,`+talkTestPNG+`"}},`, 4) +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,` + talkTestPNG + `"}}]`
	if _, _, err := botTalkParts(json.RawMessage(five)); err == nil {
		t.Error("five images were accepted")
	}
}

func TestBotTalkSaveImages(t *testing.T) {
	dir := t.TempDir()
	b := &localBackend{}
	now := time.Now()
	old := filepath.Join(botTalkDir(dir), "1000-aaaa-1.png")
	_ = os.MkdirAll(botTalkDir(dir), 0o700)
	_ = os.WriteFile(old, []byte("x"), 0o600)
	png, _ := base64.StdEncoding.DecodeString(talkTestPNG)
	paths, err := botTalkSaveImages(b, dir, "abcd1234", []talkImage{{ext: "png", data: png}}, now)
	if err != nil || len(paths) != 1 {
		t.Fatal(paths, err)
	}
	if st, err := os.Stat(paths[0]); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("saved image: %v %v", st, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("a week-old image was kept: %v", err)
	}
	if got := botTalkContent("hi", paths); !strings.HasSuffix(got, "Attached image: "+paths[0]) || !strings.HasPrefix(got, "hi\n\n") {
		t.Errorf("content = %q", got)
	}
}

func TestBotTalkAuth(t *testing.T) {
	rec, key, _ := testTalkBot(t)
	other := &botRecord{Name: "quiet", Host: "local"}
	_ = other.normalize()
	_ = insertBot(other)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	get := func(path, key string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	code, wrong := get("/talk/jess/v1/models", "nope")
	if code != http.StatusUnauthorized || !strings.Contains(wrong, `"error"`) {
		t.Fatalf("wrong key = %d %s", code, wrong)
	}
	// Talk off and no such bot read exactly like a wrong key.
	for _, path := range []string{"/talk/quiet/v1/models", "/talk/ghost/v1/models"} {
		if code, body := get(path, key); code != http.StatusUnauthorized || body != wrong {
			t.Errorf("%s = %d %s", path, code, body)
		}
	}
	code, body := get("/talk/jess/v1/models", key)
	var models struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal([]byte(body), &models)
	if code != 200 || models.Object != "list" || len(models.Data) != 1 || models.Data[0].ID != rec.Name {
		t.Fatalf("models = %d %s", code, body)
	}
	// A rotated key retires the old one; turning Talk off retires any.
	_ = botTalkRotate(rec)
	if code, _ := get("/talk/jess/v1/models", key); code != http.StatusUnauthorized {
		t.Errorf("old key after rotate = %d", code)
	}
	_ = setBotTalkKey(rec.Name, "")
	if err := botTalkRotate(rec); err == nil {
		t.Error("rotate with Talk off")
	}
}

func TestBotTalkRefused(t *testing.T) {
	rec, key, _ := testTalkBot(t)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	body := `{"model":"jess","messages":[{"role":"user","content":"hi"}]}`
	// A connected channel from before Talk.
	botChannelSeenAt.Delete(rec.Name)
	botChannelSeen(rec.Name, time.Now(), false)
	resp := talkPost(t, srv, "/talk/jess/v1/chat/completions", key, body)
	msg, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(msg), "Restart the bot") {
		t.Fatalf("old channel = %d %s", resp.StatusCode, msg)
	}
	// A stopped bot.
	_, _ = db.Exec(`UPDATE bots SET stopped = 1 WHERE name = ?`, rec.Name)
	resp = talkPost(t, srv, "/talk/jess/v1/chat/completions", key, body)
	msg, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(msg), "stopped") {
		t.Fatalf("stopped = %d %s", resp.StatusCode, msg)
	}
	// Nothing to deliver.
	_, _ = db.Exec(`UPDATE bots SET stopped = 0 WHERE name = ?`, rec.Name)
	resp = talkPost(t, srv, "/talk/jess/v1/chat/completions", key, `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"a"}]}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no user turn = %d", resp.StatusCode)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bot_events WHERE kind = 'talk'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d talk events queued by refused requests", n)
	}
	// An omp bot cannot turn Talk on.
	if err := botTalkEnable(&botRecord{Name: "o", Runtime: botRuntimeOmp}); err == nil {
		t.Error("Talk enabled on an omp bot")
	}
}

func TestBotTalkStream(t *testing.T) {
	rec, key, _ := testTalkBot(t)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	type result struct {
		chunks   []sseChunk
		done     bool
		comments int
	}
	got := make(chan result, 1)
	go func() {
		resp := talkPost(t, srv, "/talk/jess/v1/chat/completions", key,
			`{"model":"x","stream":true,"messages":[{"role":"system","content":"ignored"},{"role":"user","content":"what's up?"}]}`)
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("content type %q", ct)
		}
		c, d, n := readSSE(t, resp.Body)
		got <- result{c, d, n}
	}()
	chat, id := talkChatID(t, rec.Name, 0)
	var content string
	_ = db.QueryRow(`SELECT content FROM bot_events WHERE id = ?`, id).Scan(&content)
	if content != "what's up?" {
		t.Errorf("delivered %q", content)
	}
	if err := botTalkReply(rec.Name, chat, "On it.", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := botTalkReply(rec.Name, chat, "All quiet.", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := <-got
	text, _ := sseText(r.chunks)
	if text != "On it.\n\nAll quiet." || !r.done {
		t.Fatalf("stream = %q done=%v", text, r.done)
	}
	if len(r.chunks) < 3 || r.chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Errorf("first chunk = %+v", r.chunks[0])
	}
	last := r.chunks[len(r.chunks)-1]
	if fr := last.Choices[0].FinishReason; fr == nil || *fr != "stop" {
		t.Errorf("last chunk = %+v", last)
	}
	var state string
	var untaken int
	_ = db.QueryRow(`SELECT state FROM bot_talks WHERE chat_id = ?`, chat).Scan(&state)
	_ = db.QueryRow(`SELECT COUNT(*) FROM bot_talk_replies WHERE taken = 0`).Scan(&untaken)
	if state != "answered" || untaken != 0 {
		t.Errorf("state %q, %d untaken", state, untaken)
	}
	// A reply for no conversation is refused.
	if err := botTalkReply(rec.Name, "nope", "x", true, time.Now()); err != errTalkUnknownChat {
		t.Errorf("unknown chat = %v", err)
	}
}

func TestBotTalkJSON(t *testing.T) {
	rec, key, _ := testTalkBot(t)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	got := make(chan string, 1)
	go func() {
		resp := talkPost(t, srv, "/talk/jess/v1/chat/completions", key, `{"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()
	chat, _ := talkChatID(t, rec.Name, 0)
	_ = botTalkReply(rec.Name, chat, "Hello.", true, time.Now())
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role, Content string
			} `json:"message"`
		} `json:"choices"`
	}
	body := <-got
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &out); err != nil || out.Object != "chat.completion" || out.Choices[0].Message.Content != "Hello." {
		t.Fatalf("body = %s (%v)", body, err)
	}
}

func TestBotTalkLateReply(t *testing.T) {
	rec, key, _ := testTalkBot(t)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	// The client leaves before the bot answers.
	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan struct{})
	go func() {
		defer close(left)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/talk/jess/v1/chat/completions",
			strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"slow one"}]}`))
		req.Header.Set("Authorization", "Bearer "+key)
		if resp, err := srv.Client().Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	chat1, id1 := talkChatID(t, rec.Name, 0)
	cancel()
	<-left
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		_ = db.QueryRow(`SELECT state FROM bot_talks WHERE chat_id = ?`, chat1).Scan(&state)
		if state == "left" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %q", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := botTalkReply(rec.Name, chat1, "Found it: 42.", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The next request carries it ahead of its own answer.
	got := make(chan string, 1)
	go func() {
		resp := talkPost(t, srv, "/talk/jess/v1/chat/completions", key, `{"stream":true,"messages":[{"role":"user","content":"and?"}]}`)
		defer resp.Body.Close()
		c, _, _ := readSSE(t, resp.Body)
		text, _ := sseText(c)
		got <- text
	}()
	chat2, _ := talkChatID(t, rec.Name, id1)
	_ = botTalkReply(rec.Name, chat2, "That's all.", true, time.Now())
	text := <-got
	if !strings.HasPrefix(text, "(An answer to an earlier message") || !strings.Contains(text, "Found it: 42.") || !strings.HasSuffix(text, "That's all.") {
		t.Fatalf("carried = %q", text)
	}
}

// talkItems is a transcript: the delivery for chat, its rows, then another
// delivery and a row of its own.
func talkItems(chat string) []chatItem {
	return []chatItem{
		{Kind: "incoming", ID: "a", Text: `<channel source="lasso-channel" trigger="talk" chat_id="` + chat + `">hi</channel>`},
		{Kind: "agent", ID: "t", Text: "hmm, secrets", Thinking: true},
		{Kind: "tool", ID: "b", Tool: &chatTool{CallID: "c1", Name: "Bash", Title: "Bash", Family: "shell", Subject: "ls /home/stephan/clients"}},
		{Kind: "tool", ID: "c", Tool: &chatTool{CallID: "c2", Name: "mcp__gmail__search", Title: "mcp__gmail__search", Family: "generic", Subject: "from:acme"}},
		{Kind: "tool", ID: "d", Tool: &chatTool{CallID: "c3", Name: botChannelReplyToolID, Title: botChannelReplyToolID, Family: "generic"}},
		{Kind: "agent", ID: "e", Text: "There are three folders."},
		{Kind: "incoming", ID: "f", Text: `<channel source="lasso-channel" trigger="talk" chat_id="other">x</channel>`},
		{Kind: "tool", ID: "g", Tool: &chatTool{CallID: "c4", Name: "Read", Title: "Read", Family: "read", Subject: "other-client.txt"}},
	}
}

func TestBotTalkSteps(t *testing.T) {
	steps, prose, found := botTalkSteps(talkItems("abc"), "abc", false)
	if !found || prose != "There are three folders." {
		t.Fatalf("found=%v prose=%q", found, prose)
	}
	var plain []string
	for _, s := range steps {
		plain = append(plain, s.text)
	}
	if strings.Join(plain, "|") != "Running a command|Using a tool" {
		t.Fatalf("plain = %v", plain)
	}
	steps, _, _ = botTalkSteps(talkItems("abc"), "abc", true)
	var detailed []string
	for _, s := range steps {
		detailed = append(detailed, s.text)
	}
	joined := strings.Join(detailed, "|")
	if !strings.Contains(joined, "Bash · ls /home/stephan/clients") || !strings.Contains(joined, "There are three folders.") {
		t.Errorf("detailed = %v", detailed)
	}
	// Never the thinking, the reply call, or anything after the next delivery.
	for _, bad := range []string{"secrets", "reply", "other-client"} {
		if strings.Contains(joined, bad) {
			t.Errorf("detailed shows %q: %v", bad, detailed)
		}
	}
	if _, _, found := botTalkSteps(talkItems("abc"), "zzz", false); found {
		t.Error("found a delivery that is not there")
	}
}

func TestBotTalkActivityAndFallback(t *testing.T) {
	rec, key, look := testTalkBot(t)
	srv := httptest.NewServer(http.HandlerFunc(serveBotTalk))
	defer srv.Close()
	got := make(chan []sseChunk, 1)
	go func() {
		resp := talkPost(t, srv, "/talk/jess/v1/chat/completions", key, `{"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		defer resp.Body.Close()
		c, _, _ := readSSE(t, resp.Body)
		got <- c
	}()
	chat, id := talkChatID(t, rec.Name, 0)
	_, _ = db.Exec(`UPDATE bot_events SET status = 'delivered' WHERE id = ?`, id)
	// The bot works (a step shows), then goes idle having answered only in
	// its terminal: that prose is the answer.
	look(talkLook{state: "working", items: talkItems(chat)[:3]})
	time.Sleep(100 * time.Millisecond)
	look(talkLook{state: "idle", items: talkItems(chat)})
	chunks := <-got
	content, reasoning := sseText(chunks)
	if reasoning != "Running a command\nUsing a tool\n" {
		t.Errorf("reasoning = %q", reasoning)
	}
	if content != "There are three folders." {
		t.Errorf("content = %q", content)
	}
}

func TestBotChannelReplyTool(t *testing.T) {
	rec, _, _ := testTalkBot(t)
	tok, _ := ensureBotChannelToken(rec.Name)
	now := time.Now()
	if _, err := enqueueBotTalk(rec.Name, "c0ffee00", "hi", "", now); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(serveBotChannel))
	defer srv.Close()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ch := &botChannelServer{bot: rec.Name, base: srv.URL, token: tok, out: outW, client: srv.Client(), waitSeconds: 1}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go ch.serve(ctx, inR)
	lines := bufio.NewScanner(outR)
	send := func(s string) { _, _ = io.WriteString(inW, s+"\n") }
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	lines.Scan()
	var init struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
				Tools        map[string]any `json:"tools"`
			} `json:"capabilities"`
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	_ = json.Unmarshal(lines.Bytes(), &init)
	if init.Result.Capabilities.Tools == nil || !strings.Contains(init.Result.Instructions, `trigger="talk"`) {
		t.Fatalf("initialize = %s", lines.Bytes())
	}
	// A Talk caller must never answer the session's permission prompts.
	if _, ok := init.Result.Capabilities.Experimental["claude/channel/permission"]; ok {
		t.Fatal("the channel declares permission relay")
	}
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	lines.Scan()
	if !strings.Contains(lines.Text(), `"name":"reply"`) {
		t.Fatalf("tools/list = %s", lines.Text())
	}
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"reply","arguments":{"chat_id":"c0ffee00","text":"Hi there."}}}`)
	lines.Scan()
	if !strings.Contains(lines.Text(), `"sent"`) || strings.Contains(lines.Text(), `"isError":true`) {
		t.Fatalf("reply = %s", lines.Text())
	}
	var text string
	var done bool
	if err := db.QueryRow(`SELECT text, done FROM bot_talk_replies WHERE chat_id = 'c0ffee00'`).Scan(&text, &done); err != nil || text != "Hi there." || !done {
		t.Fatalf("stored reply = %q %v %v", text, done, err)
	}
	send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"reply","arguments":{"chat_id":"nope","text":"x"}}}`)
	lines.Scan()
	if !strings.Contains(lines.Text(), `"isError":true`) || !strings.Contains(lines.Text(), "chat_id") {
		t.Errorf("reply to an unknown chat = %s", lines.Text())
	}
	// Its polls say it has the tool.
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		botChannelSeenAt.Delete(rec.Name)
		var talk bool
		_, _ = db.Exec(`UPDATE bots SET channel_talk = 0 WHERE name = ?`, rec.Name)
		time.Sleep(50 * time.Millisecond)
		_ = db.QueryRow(`SELECT channel_talk FROM bots WHERE name = ?`, rec.Name).Scan(&talk)
		if talk {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the channel's poll did not say it has the reply tool")
		}
	}
}

func TestBotTalkThroughTheSpool(t *testing.T) {
	b, rec, dir := testRemoteBot(t)
	now := time.Now()
	if _, err := enqueueBotTalk(rec.Name, "beefcafe", "hi", "", now); err != nil {
		t.Fatal(err)
	}
	spool := botSpoolDir(dir)
	// The channel on the bot's host leaves its reply in the outbox.
	ch := &botChannelServer{bot: rec.Name, spool: spool}
	if err := ch.replySpool(botTalkOutboxEntry{ChatID: "beefcafe", Text: "From exa.", Done: true}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(botSpoolOutbox(spool), "junk.json"), []byte("not json"), 0o600)
	if err := botSpoolSync(b, rec, now); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := db.QueryRow(`SELECT text FROM bot_talk_replies WHERE chat_id = 'beefcafe'`).Scan(&text); err != nil || text != "From exa." {
		t.Fatalf("collected %q %v", text, err)
	}
	if ents, _ := os.ReadDir(botSpoolOutbox(spool)); len(ents) != 0 {
		t.Errorf("outbox not emptied: %v", ents)
	}
	// Its heartbeat says it has the tool; an older one's does not.
	if !botSpoolBeatTalk("2026-10-26T00:00:00Z 123 talk\n") || botSpoolBeatTalk("2026-10-26T00:00:00Z 123\n") {
		t.Error("botSpoolBeatTalk")
	}
}

func TestBotTalkSettingsAPI(t *testing.T) {
	rec, _, _ := testTalkBot(t)
	_ = setBotTalkKey(rec.Name, "")
	srv := httptest.NewServer(http.HandlerFunc(serveBots))
	defer srv.Close()
	post := func(path, body string) botTalkView {
		resp, err := srv.Client().Post(srv.URL+"/api/bots/jess/"+path, "application/json", bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v botTalkView
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return v
	}
	v := post("talk/enable", "")
	if !v.Enabled || !strings.HasPrefix(v.Key, "lt-") || v.Path != "/talk/jess/v1" || v.Model != "jess" || v.Activity != "plain" {
		t.Fatalf("enabled = %+v", v)
	}
	if again := post("talk/enable", ""); again.Key != v.Key {
		t.Error("enable twice changed the key")
	}
	if rot := post("talk/rotate", ""); rot.Key == v.Key || rot.Key == "" {
		t.Error("rotate kept the key")
	}
	if d := post("talk", `{"activity":"detailed"}`); d.Activity != "detailed" {
		t.Errorf("activity = %q", d.Activity)
	}
	if off := post("talk/disable", ""); off.Enabled || off.Key != "" {
		t.Errorf("disabled = %+v", off)
	}
}

func TestTalkPassesTheGates(t *testing.T) {
	for name, list := range map[string][]string{"machinePaths": machinePaths, "selfCredentialed": selfCredentialed, "crossSiteExempt": crossSiteExempt} {
		if !pathUnder("/talk/jess/v1/chat/completions", list) {
			t.Errorf("%s lacks /talk", name)
		}
	}
	if strings.Contains(strings.Join(botClaudeArgv(&botRecord{Name: "jess", Host: "local", channelToken: "tok"}, "/d"), " "), "--allowedTools "+botChannelReplyToolID) == false {
		t.Error("the launch does not allow the reply tool")
	}
}

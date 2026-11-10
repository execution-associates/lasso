package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Contract additions found by the first client: client_id echoed on a person's
// message, in_reply_to on a bot's, call stamping that follows the answered
// message, and pixel sizes on image attachments.

// rawMessages reads a conversation's history as generic JSON, to check which
// keys are present at all.
func (c *clientTest) rawMessages(token, cid string) []map[string]any {
	c.t.Helper()
	var page struct{ Messages []map[string]any }
	c.get("/client/v1/conversations/"+cid+"/messages", token, &page)
	return page.Messages
}

// replyTokens claims what the bot has been handed and answers message id ->
// reply token.
func replyTokens(t *testing.T) map[string]string {
	t.Helper()
	evs, err := claimBotEvents("jess", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range evs {
		if e.Meta["reply_token"] != "" {
			out[e.Meta["message_id"]] = e.Meta["reply_token"]
		}
	}
	return out
}

func TestClientMessageEchoesClientID(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", true, "jess")
	_, bob := c.person("Bob", false, "jess")
	cid := c.conv(ann, "jess")

	ch, stop, _ := c.openStream(ann, cid, "")
	defer stop()
	nextEvent(t, ch, "hello")

	resp, m := c.send(ann, cid, `{"text":"hi","client_id":"opt-1"}`)
	if resp.StatusCode != 202 || m.ClientID != "opt-1" {
		t.Fatalf("send: %d %+v", resp.StatusCode, m)
	}
	if ev := nextEvent(t, ch, "message"); !strings.Contains(ev.Data, `"client_id":"opt-1"`) {
		t.Fatalf("message event = %s", ev.Data)
	}
	// A retry answers the original message, client_id included.
	resp, again := c.send(ann, cid, `{"text":"hi","client_id":"opt-1"}`)
	if resp.StatusCode != 200 || again.ID != m.ID || again.ClientID != "opt-1" {
		t.Fatalf("replay: %d %+v", resp.StatusCode, again)
	}
	// A message sent without one has no key at all.
	c.send(ann, cid, `{"text":"no id"}`)
	// The bot's messages never carry one.
	tok := replyTokens(t)[m.ID]
	if _, err := botClientReply("jess", cid, tok, "hello", true, nil, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	msgs := c.rawMessages(ann, cid)
	if len(msgs) != 3 {
		t.Fatalf("history = %v", msgs)
	}
	if msgs[0]["client_id"] != "opt-1" {
		t.Fatalf("history lost client_id: %v", msgs[0])
	}
	if _, ok := msgs[1]["client_id"]; ok {
		t.Fatalf("a message sent without client_id has one: %v", msgs[1])
	}
	if _, ok := msgs[2]["client_id"]; ok {
		t.Fatalf("a bot message has client_id: %v", msgs[2])
	}
	// Another person's conversation shows nothing of it.
	if got := c.rawMessages(bob, c.conv(bob, "jess")); len(got) != 0 {
		t.Fatalf("bob sees %v", got)
	}
}

func TestClientReplyNamesWhatItAnswers(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", true, "jess")
	cid := c.conv(ann, "jess")

	// A text message sent before any call: the reply names it, has no call.
	_, t1 := c.send(ann, cid, `{"text":"first"}`)
	// A call opens; a text message sent while it is open is still a text one.
	_, body := c.do("POST", "/client/v1/conversations/"+cid+"/calls", ann, "")
	var out struct{ Call appCall }
	_ = json.Unmarshal(body, &out)
	call := out.Call
	_, t2 := c.send(ann, cid, `{"text":"typed during the call"}`)
	_, v1 := c.send(ann, cid, `{"text":"spoken","mode":"voice","call_id":"`+call.ID+`"}`)
	_, v2 := c.send(ann, cid, `{"text":"spoken again","mode":"voice","call_id":"`+call.ID+`"}`)
	toks := replyTokens(t)

	interim, err := botClientReply("jess", cid, toks[t1.ID], "on it", false, nil, false, time.Now())
	if err != nil || interim.InReplyTo != t1.ID || interim.CallID != "" || interim.Done || interim.Mode != "text" {
		t.Fatalf("interim to a text message: %v %+v", err, interim)
	}
	final, err := botClientReply("jess", cid, toks[t1.ID], "done", true, nil, false, time.Now())
	if err != nil || final.InReplyTo != t1.ID || final.CallID != "" {
		t.Fatalf("final to a text message: %v %+v", err, final)
	}
	typed, err := botClientReply("jess", cid, toks[t2.ID], "ok", true, nil, false, time.Now())
	if err != nil || typed.InReplyTo != t2.ID || typed.CallID != "" || typed.Mode != "text" {
		t.Fatalf("reply to a text message sent during a call: %v %+v", err, typed)
	}
	vi, err := botClientReply("jess", cid, toks[v1.ID], "one moment", false, nil, false, time.Now())
	if err != nil || vi.InReplyTo != v1.ID || vi.CallID != call.ID || vi.Mode != "voice" {
		t.Fatalf("interim to a voice message: %v %+v", err, vi)
	}
	vf, err := botClientReply("jess", cid, toks[v1.ID], "here you go", true, nil, false, time.Now())
	if err != nil || vf.InReplyTo != v1.ID || vf.CallID != call.ID {
		t.Fatalf("final to a voice message: %v %+v", err, vf)
	}

	// Hanging up keeps the call id on the last words, and the call_ended event.
	if err := botClientEndCall("jess", call.ID, toks[v1.ID], "Bye.", time.Now()); err != nil {
		t.Fatal(err)
	}
	var last clientMessageView
	for _, m := range c.rawMessagesView(ann, cid) {
		last = m
	}
	if last.Text != "Bye." || last.CallID != call.ID || last.Role != "bot" {
		t.Fatalf("last words = %+v", last)
	}

	// A reply after the call ended still belongs to the call its message was
	// sent in.
	late, err := botClientReply("jess", cid, toks[v2.ID], "late", false, nil, false, time.Now())
	if err != nil || late.CallID != call.ID || late.InReplyTo != v2.ID {
		t.Fatalf("late reply: %v %+v", err, late)
	}

	// A person's message never carries in_reply_to.
	for _, m := range c.rawMessages(ann, cid) {
		if m["role"] == "user" {
			if _, ok := m["in_reply_to"]; ok {
				t.Fatalf("user message has in_reply_to: %v", m)
			}
		}
	}
	// omp: the turn names the message, and the call follows it.
	clientBotTurnMessage(clientTurn{Bot: "jess", ConvID: cid, MsgID: v1.ID}, "omp voice", true)
	clientBotTurnMessage(clientTurn{Bot: "jess", ConvID: cid, MsgID: t2.ID}, "omp text", true)
	msgs := c.rawMessagesView(ann, cid)
	om := msgs[len(msgs)-2]
	ot := msgs[len(msgs)-1]
	if om.Text != "omp voice" || om.InReplyTo != v1.ID || om.CallID != call.ID {
		t.Fatalf("omp voice reply = %+v", om)
	}
	if ot.Text != "omp text" || ot.InReplyTo != t2.ID || ot.CallID != "" {
		t.Fatalf("omp text reply = %+v", ot)
	}
}

func (c *clientTest) rawMessagesView(token, cid string) []clientMessageView {
	c.t.Helper()
	var page struct{ Messages []clientMessageView }
	c.get("/client/v1/conversations/"+cid+"/messages", token, &page)
	return page.Messages
}

func TestClientImageSize(t *testing.T) {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 7, 3))
	_ = png.Encode(&buf, img)
	pngData := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	_ = jpeg.Encode(&buf, img, nil)
	jpgData := append([]byte(nil), buf.Bytes()...)
	buf.Reset()
	_ = gif.Encode(&buf, image.NewPaletted(image.Rect(0, 0, 5, 9), color.Palette{color.Black, color.White}), nil)
	gifData := append([]byte(nil), buf.Bytes()...)

	// WebP headers: extended, lossless and lossy.
	riff := func(chunk string, payload []byte) []byte {
		b := []byte("RIFF\x00\x00\x00\x00WEBP" + chunk)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(payload)))
		return append(b, payload...)
	}
	vp8x := riff("VP8X", append([]byte{0, 0, 0, 0}, 0x0f, 0x01, 0x00, 0xc7, 0x00, 0x00)) // 272 x 200
	vp8l := riff("VP8L", append([]byte{0x2f}, binary.LittleEndian.AppendUint32(nil, 99|(49<<14))...))
	vp8 := riff("VP8 ", []byte{0x10, 0, 0, 0x9d, 0x01, 0x2a, 64, 0, 32, 0, 0, 0}) // 64 x 32

	for _, c := range []struct {
		name, mime string
		data       []byte
		w, h       int
	}{
		{"png", "image/png", pngData, 7, 3},
		{"jpeg", "image/jpeg", jpgData, 7, 3},
		{"gif", "image/gif", gifData, 5, 9},
		{"webp extended", "image/webp", vp8x, 272, 200},
		{"webp lossless", "image/webp", vp8l, 100, 50},
		{"webp lossy", "image/webp", vp8, 64, 32},
		{"truncated png", "image/png", pngData[:12], 0, 0},
		{"garbage webp", "image/webp", []byte("RIFFxxxxWEBPVP8 short"), 0, 0},
		{"not an image", "text/plain", []byte("hello"), 0, 0},
		{"pdf", "application/pdf", pngData, 0, 0},
	} {
		if w, h := clientImageSize(c.mime, c.data); w != c.w || h != c.h {
			t.Errorf("%s: got %dx%d, want %dx%d", c.name, w, h, c.w, c.h)
		}
	}
	// Only the head is looked at: a header followed by a megabyte of anything.
	big := append(append([]byte(nil), pngData[:33]...), make([]byte, 3<<20)...)
	if w, h := clientImageSize("image/png", big); w != 7 || h != 3 {
		t.Errorf("large file: %dx%d", w, h)
	}
}

func TestClientAttachmentDimensions(t *testing.T) {
	c := newClientTest(t)
	_, ann := c.person("Ann", true, "jess")
	cid := c.conv(ann, "jess")

	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 40, 30)))
	pngData := append([]byte(nil), buf.Bytes()...)

	resp, a := c.upload(ann, cid, "photo.png", pngData)
	if resp.StatusCode != 201 || a.Width != 40 || a.Height != 30 {
		t.Fatalf("upload: %d %+v", resp.StatusCode, a)
	}
	// An image whose header cannot be read still uploads, without a size.
	resp, bad := c.upload(ann, cid, "broken.png", pngData[:20])
	if resp.StatusCode != 201 || bad.Width != 0 || bad.Height != 0 || bad.Type != "image/png" {
		t.Fatalf("unreadable image: %d %+v", resp.StatusCode, bad)
	}
	// A file that is not an image has none.
	resp, txt := c.upload(ann, cid, "note.txt", []byte("plain words"))
	if resp.StatusCode != 201 || txt.Width != 0 {
		t.Fatalf("text upload: %d %+v", resp.StatusCode, txt)
	}
	_, raw := c.do("POST", "/client/v1/conversations/"+cid+"/attachments?name=n.txt", ann, "more words")
	if strings.Contains(string(raw), `"width"`) || strings.Contains(string(raw), `"height"`) {
		t.Fatalf("a text file reports a size: %s", raw)
	}

	// The message carries it, in the send response and in the history.
	body, _ := json.Marshal(map[string]any{"text": "look", "attachments": []string{a.ID, bad.ID}})
	resp, m := c.send(ann, cid, string(body))
	if resp.StatusCode != 202 || len(m.Attachments) != 2 || m.Attachments[0].Width != 40 || m.Attachments[0].Height != 30 || m.Attachments[1].Width != 0 {
		t.Fatalf("send: %d %+v", resp.StatusCode, m.Attachments)
	}
	hist := c.rawMessagesView(ann, cid)
	if got := hist[0].Attachments[0]; got.Width != 40 || got.Height != 30 {
		t.Fatalf("history = %+v", got)
	}

	// What the bot sends is measured the same way, and the marker for an
	// expired file keeps the size so the layout holds.
	outbox := filepath.Join(c.root, clientOutboxRel)
	_ = os.MkdirAll(outbox, 0o755)
	_ = os.WriteFile(filepath.Join(outbox, "chart.png"), pngData, 0o644)
	toks := replyTokens(t)
	bm, err := botClientReply("jess", cid, toks[m.ID], "chart", true, []string{filepath.Join(clientOutboxRel, "chart.png")}, false, time.Now())
	if err != nil || len(bm.Attachments) != 1 || bm.Attachments[0].Width != 40 || bm.Attachments[0].Height != 30 {
		t.Fatalf("bot image: %v %+v", err, bm.Attachments)
	}
	stored, _ := appAttachmentByID(bm.Attachments[0].ID)
	expireAttachment(stored, time.Now())
	var got clientMessageView
	for _, h := range c.rawMessagesView(ann, cid) {
		if h.ID == bm.ID {
			got = h
		}
	}
	if x := got.Attachments[0]; !x.Expired || x.URL != "" || x.Width != 40 || x.Height != 30 {
		t.Fatalf("expired marker = %+v", x)
	}
}

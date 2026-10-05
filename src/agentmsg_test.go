package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReadFramedReply(t *testing.T) {
	tok, body, tr, err := readFramedReply(strings.NewReader("lasso-reply abc123\nline one\nline two\n"))
	if err != nil || tok != "abc123" || body != "line one\nline two\n" || tr {
		t.Fatalf("got %q %q %v %v", tok, body, tr, err)
	}
	for _, bad := range []string{"", "hello\nworld", "lasso-reply\nx", "lasso-reply a b\nx"} {
		if _, _, _, err := readFramedReply(strings.NewReader(bad)); err == nil {
			t.Errorf("%q: want a framing error", bad)
		}
	}
	big := "lasso-reply t\n" + strings.Repeat("x", replyInboxMaxBody+10)
	_, body, tr, err = readFramedReply(strings.NewReader(big))
	if err != nil || !tr || len(body) != replyInboxMaxBody {
		t.Fatalf("oversized: len %d truncated %v err %v", len(body), tr, err)
	}
}

func TestComposeAgentMessage(t *testing.T) {
	m := composeAgentMessage("m_1", "Stephan", "  do the thing  ", "tok", "tcpADDR", 4242, nil)
	for _, want := range []string{"Message m_1 from Stephan", "do the thing", "{ echo 'lasso-reply tok'; cat /tmp/reply-m_1.md; } | tailcat tcpADDR 4242", `reply_message with token "tok"`} {
		if !strings.Contains(m, want) {
			t.Errorf("message lacks %q:\n%s", want, m)
		}
	}
	m = composeAgentMessage("m_2", "x", "hi", "tok", "", 0, errors.New("no tailcat"))
	if strings.Contains(m, "tailcat ") || !strings.Contains(m, "reply_message") {
		t.Errorf("no inbox should leave only the MCP route:\n%s", m)
	}
	if m = composeAgentMessage("m_3", "x", "hi", "", "", 0, nil); strings.Contains(m, "reply") {
		t.Errorf("a one-way message carries no reply footer:\n%s", m)
	}
}

func TestRepliesAreScopedAndMarkedRead(t *testing.T) {
	openTestDB(t)
	defer closeTestDB()
	now := time.Now()
	if err := insertOutbox(outboxMsg{ID: "m_a", SenderClient: "alice", Host: "local", Target: "bot", Body: "q", CreatedAt: now}, "tokA"); err != nil {
		t.Fatal(err)
	}
	if err := insertOutbox(outboxMsg{ID: "m_b", SenderClient: "bob", Host: "local", Target: "bot", Body: "q", CreatedAt: now}, "tokB"); err != nil {
		t.Fatal(err)
	}
	if _, err := recordReply("nope", "x", "mcp", false); err == nil {
		t.Fatal("unknown token accepted")
	}
	if _, err := recordReply("tokA", "  ", "mcp", false); err == nil {
		t.Fatal("empty reply accepted")
	}
	if id, err := recordReply("tokA", "answer A", "tailcat", false); err != nil || id != "m_a" {
		t.Fatalf("reply A: %q %v", id, err)
	}
	if _, err := recordReply("tokB", "answer B", "mcp", false); err != nil {
		t.Fatal(err)
	}
	rs, err := queryReplies("alice", "", false)
	if err != nil || len(rs) != 1 || rs[0].Body != "answer A" || rs[0].To != "bot" {
		t.Fatalf("alice sees %+v %v", rs, err)
	}
	if rs, _ = queryReplies("alice", "", false); len(rs) != 0 {
		t.Fatalf("a returned reply should be read: %+v", rs)
	}
	if rs, _ = queryReplies("alice", "m_a", true); len(rs) != 1 {
		t.Fatalf("include_read: %+v", rs)
	}
	if messageOwnedBy("alice", "m_b") {
		t.Fatal("alice owns bob's message")
	}
	// The token is never stored in the clear.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM agent_outbox WHERE token_hash IN ('tokA','tokB')`).Scan(&n)
	if n != 0 {
		t.Fatal("tokens stored in the clear")
	}
}

func TestGetRepliesWakesOnArrival(t *testing.T) {
	openTestDB(t)
	defer closeTestDB()
	if err := insertOutbox(outboxMsg{ID: "m_w", Host: "local", Target: "bot", Body: "q", CreatedAt: time.Now()}, "tokW"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = recordReply("tokW", "late answer", "tailcat", false)
	}()
	start := time.Now()
	_, out, err := getRepliesTool(context.Background(), &mcp.CallToolRequest{}, getRepliesIn{MessageID: "m_w", TimeoutSeconds: 10})
	if err != nil || len(out.Replies) != 1 || out.Replies[0].Body != "late answer" {
		t.Fatalf("got %+v %v", out, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waited for the timeout instead of waking on the reply")
	}
	if _, _, err := getRepliesTool(context.Background(), &mcp.CallToolRequest{}, getRepliesIn{MessageID: "m_missing"}); err == nil {
		t.Fatal("unknown message id accepted")
	}
}

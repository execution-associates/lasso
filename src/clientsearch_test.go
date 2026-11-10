package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

type searchResp struct {
	Results []struct {
		Kind       string         `json:"kind"`
		Thread     map[string]any `json:"thread"`
		Message    map[string]any `json:"message"`
		Attachment map[string]any `json:"attachment"`
		Snippet    string         `json:"snippet"`
		Highlights [][2]int       `json:"highlights"`
	} `json:"results"`
	NextCursor *string `json:"next_cursor"`
}

func (c *clientTest) search(token, query string) (int, searchResp) {
	c.t.Helper()
	resp, b := c.do("GET", "/client/v1/search?"+query, token, "")
	var out searchResp
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func TestClientSearch(t *testing.T) {
	c := newClientTest(t)
	c.addBot("mark")
	annID, ann := c.person("Ann", false, "jess", "mark")
	_, bob := c.person("Bob", false, "jess", "mark")
	cid := c.conv(ann, "jess")
	c.send(ann, cid, `{"text":"Permits in Irvine need a plan check"}`)
	c.send(bob, c.conv(bob, "jess"), `{"text":"Permits in Irvine for bob"}`)
	tid := c.newThread(ann, `{"bots":["mark"],"title":"Kitchen permits"}`)
	_, up := c.upload(ann, tid, "irvine-permit-form.png", pngBytes())
	c.sendIn(ann, tid, `{"text":"form attached","attachments":["`+up.ID+`"]}`)
	q := func(s string) string { return "q=" + url.QueryEscape(s) }

	code, res := c.search(ann, q("irv"))
	if code != 200 {
		t.Fatalf("search: %d", code)
	}
	kinds := map[string]int{}
	for _, r := range res.Results {
		kinds[r.Kind]++
		if strings.Contains(r.Snippet, "bob") {
			t.Fatal("bob's message in ann's results")
		}
	}
	if kinds["message"] != 1 || kinds["file"] != 1 || kinds["thread"] != 0 {
		t.Fatalf("kinds = %v (%+v)", kinds, res)
	}
	_, res = c.search(ann, q("permits")+"&scope=threads")
	if len(res.Results) != 1 || res.Results[0].Thread["id"] != tid || res.Results[0].Thread["title"] != "Kitchen permits" {
		t.Fatalf("threads scope = %+v", res)
	}
	// Bob, with the same words, sees only his own.
	_, rb := c.search(bob, q("permits irvine"))
	if len(rb.Results) != 1 || !strings.Contains(rb.Results[0].Snippet, "bob") {
		t.Fatalf("bob = %+v", rb)
	}
	for _, scope := range []string{"all", "threads", "messages", "files"} {
		_, rb := c.search(bob, q("kitchen form plan check")+"&scope="+scope)
		if len(rb.Results) != 0 {
			t.Errorf("bob sees ann's %s: %+v", scope, rb)
		}
	}
	// Highlights are code points into the snippet.
	_, res = c.search(ann, q("plan"))
	if len(res.Results) != 1 {
		t.Fatalf("plan = %+v", res)
	}
	r0 := res.Results[0]
	runes := []rune(r0.Snippet)
	if len(r0.Highlights) != 1 || string(runes[r0.Highlights[0][0]:r0.Highlights[0][1]]) != "plan" {
		t.Fatalf("highlight = %+v in %q", r0.Highlights, r0.Snippet)
	}
	c.send(ann, cid, `{"text":"🏠 café é renovation"}`)
	_, res = c.search(ann, q("renovation"))
	if len(res.Results) != 1 {
		t.Fatalf("emoji = %+v", res)
	}
	r0 = res.Results[0]
	runes = []rune(r0.Snippet)
	if h := r0.Highlights[0]; string(runes[h[0]:h[1]]) != "renovation" {
		t.Fatalf("emoji highlight %v in %q", h, r0.Snippet)
	}
	// FTS syntax is literal.
	for _, s := range []string{`"permits`, `permits*`, `NEAR(permits irvine)`, `-permits`, `body:permits`, `permits OR bob`} {
		code, _ := c.search(ann, q(s))
		if code != 200 {
			t.Errorf("%q: %d", s, code)
		}
	}
	if _, r := c.search(ann, q(`permits OR bob`)); len(r.Results) != 0 {
		t.Fatalf("OR was an operator: %+v", r)
	}
	if code, _ := c.search(ann, q(`"*"`)); code != 400 {
		t.Fatalf("empty query: %d", code)
	}
	if code, _ := c.search(ann, "q="); code != 400 {
		t.Fatal("no q")
	}
	// An ungranted main thread's messages disappear and return with the grant.
	_ = deleteAppGrant(annID, "jess")
	if _, r := c.search(ann, q("plan")); len(r.Results) != 0 {
		t.Fatal("an ungranted main thread's message was found")
	}
	_, _ = setAppGrant(annID, "jess", false, time.Now())
	if _, r := c.search(ann, q("plan")); len(r.Results) != 1 {
		t.Fatal("the message did not return with the grant")
	}
	// A bot's search covers only its threads.
	c.sendIn(ann, tid, `{"text":"hi mark"}`)
	tok := clientEventsOf(claim(t, "mark"), "message")[0].Meta["reply_token"]
	a, _ := botActorFromToken("mark", tok, true, time.Now())
	out, err := botThreadSearch(a, "permits", "", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out["results"].([]map[string]any) {
		if r["thread_id"] != tid {
			t.Fatalf("mark found %v", r)
		}
	}
	if len(out["results"].([]map[string]any)) == 0 {
		t.Fatal("mark found nothing in its own thread")
	}
	if _, err := botThreadSearch(a, "permits", cid, 0, time.Now()); err == nil {
		t.Fatal("mark searched jess's main thread")
	}
	// A deleted thread vanishes.
	c.jsonDo("DELETE", "/client/v1/threads/"+tid, ann, "")
	if _, r := c.search(ann, q("kitchen form")); len(r.Results) != 0 {
		t.Fatalf("deleted thread found: %+v", r)
	}
	// Rate limit.
	clientLimits = newClientWindow()
	for i := 0; i < clientSearchPerMin; i++ {
		c.search(ann, q("x"))
	}
	if code, _ := c.search(ann, q("x")); code != 429 {
		t.Fatalf("rate limit: %d", code)
	}
}

func TestFTSQueryQuotesEverything(t *testing.T) {
	got, ok := ftsQuery(`permit "irvine* NEAR(x) a b c d e f g h i`)
	if !ok || got != `"permit" "irvine" "NEAR(x)" "a" "b" "c" "d" "e"*` {
		t.Fatalf("%q", got)
	}
	s, hl := searchSnippet(strings.Repeat("ab ", 70) + "hit")
	if len([]rune(s)) != clientSearchSnippet || len(hl) != 0 {
		t.Fatalf("cut snippet %d %v", len([]rune(s)), hl)
	}
}

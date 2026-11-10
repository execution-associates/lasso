package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// callBatch calls browser_batch and returns the result.
func callBatch(t *testing.T, sess *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "browser_batch", Arguments: args})
	if err != nil {
		t.Fatalf("browser_batch: %v", err)
	}
	return res
}

// batchTexts is each content item as text; an image shows as "[image]".
func batchTexts(res *mcp.CallToolResult) []string {
	var out []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			out = append(out, v.Text)
		case *mcp.ImageContent:
			out = append(out, "[image]")
		}
	}
	return out
}

func steps(ss ...map[string]any) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func batchSession(t *testing.T) (*mcp.ClientSession, *browserMCPBridge) {
	t.Helper()
	openTestDB(t)
	b := testBrowserMCP(t, "ok", 0)
	return mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil), b
}

func TestBrowserBatchListedAndWithdrawn(t *testing.T) {
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "", "", false)
	sess := mustConnect(t, srv.URL, nil)
	tl := listTools(t, sess)["browser_batch"]
	if tl == nil {
		t.Fatal("browser_batch is not listed")
	}
	sch, _ := json.Marshal(tl.InputSchema)
	for _, w := range []string{`"steps"`, `"maxItems":25`, `"minItems":1`, `"continue_on_error"`, `"quiet"`, `"Which browser to run this in`, `"required":["steps"]`} {
		if !strings.Contains(string(sch), w) {
			t.Errorf("schema lacks %s: %s", w, sch)
		}
	}
	b.cfg.Binary = "off"
	sess = mustConnect(t, srv.URL, nil)
	if listTools(t, sess)["browser_batch"] != nil {
		t.Error("browser_batch is still listed after the browser tools were withdrawn")
	}
}

func TestBrowserBatchOrderHeadersAndNames(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"steps": steps(
		map[string]any{"tool": "plain", "args": map[string]any{"x": 1}},
		map[string]any{"tool": "browser_plain", "args": map[string]any{"x": 2}},
		map[string]any{"tool": "snap"},
	)})
	if res.IsError || res.StructuredContent != nil {
		t.Fatalf("res = %+v", res)
	}
	want := []string{"### step 1: plain", `args={"x":1}`, "### step 2: plain", `args={"x":2}`, "### step 3: snap", "took a screenshot", "[image]"}
	got := batchTexts(res)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
	img := res.Content[len(res.Content)-1].(*mcp.ImageContent)
	if string(img.Data) != string(fakeJPEG) {
		t.Errorf("image bytes changed: %v", img.Data)
	}
}

func TestBrowserBatchRefusedBeforeAnyStepRuns(t *testing.T) {
	sess, b := batchSession(t)
	many := make([]any, 26)
	for i := range many {
		many[i] = map[string]any{"tool": "plain"}
	}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"unknown":    {map[string]any{"steps": steps(map[string]any{"tool": "plain"}, map[string]any{"tool": "nope"})}, `step 2: unknown browser tool "nope"`},
		"nested":     {map[string]any{"steps": steps(map[string]any{"tool": "browser_batch"})}, "step 1: browser_batch cannot be nested"},
		"nested raw": {map[string]any{"steps": steps(map[string]any{"tool": "batch"})}, "cannot be nested"},
		"browser":    {map[string]any{"steps": steps(map[string]any{"tool": "plain", "args": map[string]any{"browser": "work"}})}, "set `browser` once on the batch"},
		"empty":      {map[string]any{"steps": []any{}}, "1 to 25"},
		"too many":   {map[string]any{"steps": many}, "1 to 25"},
		"args array": {map[string]any{"steps": steps(map[string]any{"tool": "plain", "args": []any{1}})}, "`args` must be an object"},
		"bad page":   {map[string]any{"pageId": "x", "steps": steps(map[string]any{"tool": "plain"})}, "`pageId` must be a number"},
	} {
		res := callBatch(t, sess, c.args)
		got := strings.Join(batchTexts(res), "\n")
		if !res.IsError || !strings.Contains(got, c.want) || strings.Contains(got, "### step") {
			t.Errorf("%s: IsError=%v %q, want %q", name, res.IsError, got, c.want)
		}
	}
	if b.liveChildren() != 0 || len(b.snapshot()) != 0 {
		t.Errorf("a refused batch spawned: children=%d sessions=%d", b.liveChildren(), len(b.snapshot()))
	}
}

func TestBrowserBatchPageIDInjection(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"pageId": 7, "steps": steps(
		map[string]any{"tool": "echo", "args": map[string]any{"x": 1}}, // takes pageId, none given
		map[string]any{"tool": "echo", "args": map[string]any{"pageId": 3}},
		map[string]any{"tool": "plain"}, // schema has no pageId
		map[string]any{"tool": "list_pages"},
	)})
	got := strings.Join(batchTexts(res), "\n")
	if !strings.Contains(got, `"args":{"pageId":7,"x":1}`) || !strings.Contains(got, `"args":{"pageId":3}`) {
		t.Errorf("injection: %s", got)
	}
	if !strings.Contains(got, "### step 3: plain\nargs={}") || strings.Count(got, "pageId") != 2 {
		t.Errorf("pageId reached a tool without the property: %s", got)
	}
}

func TestBrowserBatchNewPageIDCarried(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"pageId": 7, "steps": steps(
		map[string]any{"tool": "close_page"},
		map[string]any{"tool": "new_page", "quiet": true},
		map[string]any{"tool": "close_page"},
		map[string]any{"tool": "close_page", "args": map[string]any{"pageId": 2}},
		map[string]any{"tool": "new_page", "quiet": true},
		map[string]any{"tool": "close_page"},
	)})
	got := batchTexts(res)
	if res.IsError {
		t.Fatalf("%q", got)
	}
	want := []string{
		"### step 1: close_page", `args={"pageId":7}`,
		"### step 2: new_page — ok",
		"### step 3: close_page", `args={"pageId":13}`,
		"### step 4: close_page", `args={"pageId":2}`,
		"### step 5: new_page — ok",
		"### step 6: close_page", `args={"pageId":14}`,
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestBrowserBatchStopsOnError(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"steps": steps(
		map[string]any{"tool": "plain"},
		map[string]any{"tool": "fail", "quiet": true}, // a failure keeps its output
		map[string]any{"tool": "plain"},
		map[string]any{"tool": "plain"},
	)})
	got := batchTexts(res)
	want := []string{"### step 1: plain", "args={}", "### step 2: fail — ERROR", "it failed", "stopped after step 2; steps 3-4 not run"}
	if !res.IsError || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("IsError=%v got %q\nwant %q", res.IsError, got, want)
	}
	res = callBatch(t, sess, map[string]any{"steps": steps(map[string]any{"tool": "fail"}, map[string]any{"tool": "plain"})})
	if g := batchTexts(res); g[len(g)-1] != "stopped after step 1; step 2 not run" {
		t.Errorf("last line = %q", g[len(g)-1])
	}
}

func TestBrowserBatchContinueOnError(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"continue_on_error": true, "steps": steps(
		map[string]any{"tool": "fail"},
		map[string]any{"tool": "plain"},
	)})
	got := batchTexts(res)
	want := []string{"### step 1: fail — ERROR", "it failed", "### step 2: plain", "args={}"}
	if !res.IsError || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("IsError=%v got %q", res.IsError, got)
	}
}

func TestBrowserBatchQuiet(t *testing.T) {
	sess, _ := batchSession(t)
	res := callBatch(t, sess, map[string]any{"steps": steps(
		map[string]any{"tool": "snap", "quiet": true},
		map[string]any{"tool": "snap"},
	)})
	want := []string{"### step 1: snap — ok", "### step 2: snap", "took a screenshot", "[image]"}
	if got := batchTexts(res); res.IsError || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func TestBrowserBatchRoutesToABrowser(t *testing.T) {
	f := testFleet(t)
	if _, err := f.create("Work", "", ""); err != nil {
		t.Fatal(err)
	}
	b := testBrowserMCP(t, "ok", 0)
	sess := mustConnect(t, mcpServerWith(t, b, "", "", false).URL, nil)
	res := callBatch(t, sess, map[string]any{"browser": "work", "steps": steps(map[string]any{"tool": "echo", "args": map[string]any{"x": 1}})})
	got := strings.Join(batchTexts(res), "\n")
	if res.IsError || !strings.Contains(got, "ws://127.0.0.1:1/cdp/p/work") {
		t.Errorf("did not run in work: %s", got)
	}
	if strings.Contains(got, `"browser"`) {
		t.Errorf("browser reached the child: %s", got)
	}
	if pids := b.childPIDs(); len(pids["work"]) != 1 || len(pids[defaultBrowserProfile]) != 0 {
		t.Errorf("children = %v", pids)
	}
	res = callBatch(t, sess, map[string]any{"browser": "nope", "steps": steps(map[string]any{"tool": "plain"})})
	if g := strings.Join(batchTexts(res), "\n"); !res.IsError || !strings.Contains(g, `no browser "nope"`) {
		t.Errorf("unknown browser: %q", g)
	}
}

// browser_batch meets the same gate as every browser call: a refused request
// runs no step and spawns nothing.
func TestBrowserBatchGate(t *testing.T) {
	prev := oauthCfg
	oauthCfg = oauthConf{}
	t.Cleanup(func() { oauthCfg = prev })
	testFleet(t)
	b := testBrowserMCP(t, "ok", 0)
	srv := mcpServerWith(t, b, "u", "p", true)
	forged := http.Header{browserGateHeader: {"ok"}, browserRefusalHeader: {""}}
	for name, rt := range map[string]http.RoundTripper{
		"none":   nil,
		"wrong":  hdrRT{user: "u", pass: "wrong"},
		"forged": hdrRT{h: forged},
	} {
		sess := mustConnect(t, srv.URL, rt)
		res := callBatch(t, sess, map[string]any{"steps": steps(map[string]any{"tool": "plain"})})
		if g := strings.Join(batchTexts(res), "\n"); !res.IsError || !strings.Contains(g, "UI_AUTH") || strings.Contains(g, "### step") {
			t.Errorf("%s: %q", name, g)
		}
	}
	if b.liveChildren() != 0 || len(b.snapshot()) != 0 {
		t.Errorf("refused batches spawned: children=%d sessions=%d", b.liveChildren(), len(b.snapshot()))
	}
	sess := mustConnect(t, srv.URL, hdrRT{user: "u", pass: "p"})
	if res := callBatch(t, sess, map[string]any{"steps": steps(map[string]any{"tool": "plain"})}); res.IsError {
		t.Errorf("with the credentials: %q", batchTexts(res))
	}
}

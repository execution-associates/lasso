package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// browser_batch runs several of the mirrored browser tools, in order, in ONE
// call: a sequence the caller already knows costs one model round trip instead
// of one per action. It is only calls the caller could make one at a time, run
// back to back through the same session's child for one browser, under the
// same gate; it adds nothing a caller could not already do.

const (
	browserBatchName     = browserToolPrefix + "batch"
	browserBatchMaxSteps = 25
)

const browserBatchDescription = "Run several browser_* tools in order, in ONE call and ONE browser. Use it whenever the next few actions are already known (visit several pages and evaluate_script on each; fill + click + wait_for; open a page, work in it and close it) instead of one call per action. " +
	"Results come back per step, in order, each under a `### step N: <tool>` header. The batch stops at the first failed step unless continue_on_error is true. " +
	"A new_page step's page becomes the default pageId for the steps after it (they need not pass one), so open, use and close_page can be a single batch. `quiet` drops a successful step's output and keeps only its header. " +
	"A decision that depends on an earlier step's output needs a separate call. At most 25 steps. The whole request is validated before any step runs."

// browserBatchSelected finds the page a new_page step opened: chrome-devtools-mcp
// lists the pages as `<id>: <url> [selected]`.
var browserBatchSelected = regexp.MustCompile(`(?m)^(\d+): .*\[selected\]\s*$`)

// browserBatchTool is the tool as /mcp lists it.
func browserBatchTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        browserBatchName,
		Title:       "Browser: batch",
		Description: browserBatchDescription,
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				browserToolParam: map[string]any{"type": "string", "description": browserToolParamDesc},
				"pageId":         map[string]any{"type": "number", "description": "Default pageId for steps whose tool takes one and whose args do not set it. A page opened by a new_page step replaces it for the steps after that."},
				"steps": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": browserBatchMaxSteps,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tool":  map[string]any{"type": "string", "description": "A browser tool on this server, with or without the browser_ prefix (navigate_page or browser_navigate_page). Not browser_batch."},
							"args":  map[string]any{"type": "object", "description": "That tool's arguments, without `browser` (set it once on the batch)."},
							"quiet": map[string]any{"type": "boolean", "description": "On success, drop this step's output and keep only its header."},
						},
						"required": []string{"tool"},
					},
					"description": "The steps, run in order (1 to 25).",
				},
				"continue_on_error": map[string]any{"type": "boolean", "description": "Keep going after a failed step instead of stopping. Default false."},
			},
			"required": []string{"steps"},
		},
	}
}

type browserBatchStep struct {
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args"`
	Quiet bool            `json:"quiet"`

	child string                     // the child's own tool name, once validated
	args  map[string]json.RawMessage // parsed args, never nil once validated
}

type browserBatchRequest struct {
	PageID          json.RawMessage    `json:"pageId"`
	Steps           []browserBatchStep `json:"steps"`
	ContinueOnError bool               `json:"continue_on_error"`
}

// browserBatchPlan validates raw (the batch's arguments, `browser` already
// stripped) against the registered tools. nothing has run yet when it fails.
func (b *browserMCPBridge) browserBatchPlan(raw json.RawMessage) (*browserBatchRequest, error) {
	var in browserBatchRequest
	if len(raw) == 0 {
		return nil, fmt.Errorf("`steps` is required: 1 to %d steps", browserBatchMaxSteps)
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("invalid arguments: %v", err)
	}
	if n := len(in.Steps); n == 0 || n > browserBatchMaxSteps {
		return nil, fmt.Errorf("`steps` must have 1 to %d steps, got %d", browserBatchMaxSteps, n)
	}
	if len(in.PageID) > 0 && string(in.PageID) != "null" {
		var n float64
		if json.Unmarshal(in.PageID, &n) != nil {
			return nil, fmt.Errorf("`pageId` must be a number")
		}
	} else {
		in.PageID = nil
	}
	b.regMu.Lock()
	known := b.regChild
	b.regMu.Unlock()
	for i := range in.Steps {
		st := &in.Steps[i]
		name := strings.TrimSpace(st.Tool)
		if name == "" {
			return nil, fmt.Errorf("step %d: `tool` is required", i+1)
		}
		child := name
		if !known[child] {
			child = strings.TrimPrefix(name, browserToolPrefix)
		}
		if child == "batch" || name == browserBatchName {
			return nil, fmt.Errorf("step %d: %s cannot be nested in a batch", i+1, browserBatchName)
		}
		if !known[child] {
			return nil, fmt.Errorf("step %d: unknown browser tool %q", i+1, st.Tool)
		}
		st.child = child
		st.args = map[string]json.RawMessage{}
		if a := strings.TrimSpace(string(st.Args)); a != "" && a != "null" {
			if err := json.Unmarshal(st.Args, &st.args); err != nil || st.args == nil {
				return nil, fmt.Errorf("step %d (%s): `args` must be an object", i+1, st.Tool)
			}
		}
		if _, has := st.args[browserToolParam]; has {
			return nil, fmt.Errorf("step %d (%s): `args` must not contain `browser`; set `browser` once on the batch", i+1, st.Tool)
		}
	}
	return &in, nil
}

// batch is browser_batch's handler. It applies forward's gate and routing in
// forward's order, with the batch's own validation between routing and the
// first thing that could spawn a child.
func (b *browserMCPBridge) batch() mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if why := browserToolRefusal(req); why != "" {
			return browserMCPToolError(why), nil
		}
		browser, args, refusal := browserRoute(req.Params.Arguments, false)
		if refusal != "" {
			return browserMCPToolError(refusal), nil
		}
		in, err := b.browserBatchPlan(args)
		if err != nil {
			return browserMCPToolError(err.Error()), nil
		}
		s, err := b.sessionFor(req.Session)
		if err != nil {
			return browserMCPToolError(err.Error()), nil
		}
		s.use(1)
		defer s.use(-1)
		child, err := s.child(ctx, browser)
		if err != nil {
			return browserMCPToolError(err.Error()), nil
		}
		b.regMu.Lock()
		takesPage := b.regPageID
		b.regMu.Unlock()
		return runBrowserBatch(ctx, child, in, takesPage), nil
	}
}

func runBrowserBatch(ctx context.Context, child *browserMCPChild, in *browserBatchRequest, takesPage map[string]bool) *mcp.CallToolResult {
	out := &mcp.CallToolResult{}
	page := in.PageID // the default pageId; a new_page step replaces it
	n := len(in.Steps)
	text := func(s string) { out.Content = append(out.Content, &mcp.TextContent{Text: s}) }
	stopped := func(done int, why string) {
		out.IsError = true
		if done >= n {
			return
		}
		if done == n-1 {
			text(fmt.Sprintf("%s after step %d; step %d not run", why, done, n))
		} else {
			text(fmt.Sprintf("%s after step %d; steps %d-%d not run", why, done, done+1, n))
		}
	}
	for i := range in.Steps {
		if ctx.Err() != nil {
			stopped(i, "cancelled")
			return out
		}
		st := &in.Steps[i]
		if _, has := st.args["pageId"]; !has && page != nil && takesPage[st.child] {
			st.args["pageId"] = page
		}
		p := &mcp.CallToolParams{Name: st.child}
		if len(st.args) > 0 {
			raw, _ := json.Marshal(st.args)
			p.Arguments = json.RawMessage(raw) // as forward does: a []byte would be base64
		}
		res, err := child.client.CallTool(ctx, p)
		header := fmt.Sprintf("### step %d: %s", i+1, st.child)
		if err != nil {
			text(header + " — ERROR")
			text(err.Error())
			stopped(i+1, "stopped")
			return out
		}
		failed := res.IsError
		if !failed && st.child == "new_page" {
			// Depends on chrome-devtools-mcp's text format (`13: about:blank
			// [selected]`); if that changes the id is simply not found and a
			// later step without a pageId gets the child's own refusal.
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					if m := browserBatchSelected.FindStringSubmatch(tc.Text); m != nil {
						if id, e := strconv.Atoi(m[1]); e == nil {
							page = json.RawMessage(strconv.Itoa(id))
						}
						break
					}
				}
			}
		}
		switch {
		case failed:
			text(header + " — ERROR")
			out.Content = append(out.Content, res.Content...)
			out.IsError = true
			if !in.ContinueOnError {
				stopped(i+1, "stopped")
				return out
			}
		case st.Quiet:
			text(header + " — ok")
		default:
			text(header)
			out.Content = append(out.Content, res.Content...)
		}
	}
	return out
}

// browserBatchIndex derives, from the child's tool list, which tool names
// exist and which of them take a pageId.
func browserBatchIndex(tools []*mcp.Tool) (children, takesPage map[string]bool) {
	children, takesPage = map[string]bool{}, map[string]bool{}
	for _, t := range tools {
		children[t.Name] = true
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		raw, _ := json.Marshal(t.InputSchema)
		_ = json.Unmarshal(raw, &schema)
		if _, ok := schema.Properties["pageId"]; ok {
			takesPage[t.Name] = true
		}
	}
	return children, takesPage
}

// registerBatch adds browser_batch beside the mirrored tools (regMu held),
// unless the child ships a tool that is itself browser_batch, which wins.
func (b *browserMCPBridge) registerBatch(srv *mcp.Server, names map[string]bool) {
	if names[browserBatchName] {
		log.Printf("browser-mcp: chrome-devtools-mcp has its own %q tool; lasso's batch is not registered", browserBatchName)
		return
	}
	srv.AddTool(browserBatchTool(), b.batch())
	names[browserBatchName] = true
}

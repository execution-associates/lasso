package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// shared_browser: how an agent starts one of lasso's browsers (browser.go,
// cdpproxy.go) and finds its raw CDP endpoints. Driving a page is the
// browser_* tools' job (browsermcp.go), on this same server; this tool is for
// the agent that wants Playwright or another CDP client, or wants to know
// whether the browser tools can work at all.

const sharedBrowserDescription = "Start one of lasso's browsers and get its raw CDP endpoints. To DRIVE a page you do not need this: the browser_* tools on this server (browser_new_page, browser_navigate_page, browser_click, browser_take_snapshot, …) start the browser themselves, each with an optional `browser`. Use this for Playwright or another Chrome DevTools Protocol client: `ws_endpoint` is the CDP websocket (`chromium.connectOverCDP(<ws_endpoint>)`), `http_endpoint` its HTTP base (/json/list, /json/version), and GET `browsers_url` lists every browser with its own CDP endpoint — the discovery path for a CDP client that speaks no MCP. A browser is a real Chromium that a human is watching live in lasso's Browser tab, with its own cookies and logins (or a remote browser lasso dials). Their Browser tab shows ONE page, whichever was opened most recently, so opening a new page puts them on it (the page they were on keeps running out of sight). Open your own page rather than navigating one you did not open, unless the human asked you to work in theirs, and close the pages you opened when you are done. A browser lasso launches runs on lasso's machine, not necessarily yours: `localhost` inside it means lasso's machine. `pages` lists the pages open right now. If `available` is false, `note` says why (usually no Chromium installed there); if `browser_tools` is false, `browser_tools_reason` says why this server has no browser_* tools (usually chrome-devtools-mcp not installed on lasso's machine)."

type sharedBrowserIn struct {
	Start   *bool  `json:"start,omitempty" jsonschema:"Start the browser if it is not running (default true). Pass false to only report its state."`
	Browser string `json:"browser,omitempty" jsonschema:"Which browser (id or display name; list_browsers shows them). Each browser is its own Chromium with its own cookies, logins and endpoints. Omit for the default browser."`
}

type sharedBrowserOut struct {
	Available    bool          `json:"available"`
	Running      bool          `json:"running"`
	WSEndpoint   string        `json:"ws_endpoint"`   // absolute CDP websocket URL, e.g. ws://lasso.example:8190/cdp
	WSPath       string        `json:"ws_path"`       // /cdp or /cdp/p/<id> — prefix lasso's own URL when ws_endpoint is empty
	HTTPEndpoint string        `json:"http_endpoint"` // the CDP HTTP base (…/json/list, /json/version)
	BrowsersURL  string        `json:"browsers_url"`  // GET the CDP listing: every browser and its endpoint
	BrowserTools bool          `json:"browser_tools"` // this server's browser_* tools can run (chrome-devtools-mcp is installed and not switched off)
	ToolsReason  string        `json:"browser_tools_reason,omitempty"`
	Pages        []browserPage `json:"pages"`
	Browser      string        `json:"browser"` // the browser these endpoints reach
	Note         string        `json:"note,omitempty"`
}

func sharedBrowserTool(ctx context.Context, req *mcp.CallToolRequest, in sharedBrowserIn) (*mcp.CallToolResult, sharedBrowserOut, error) {
	cs := callerFrom(req)
	// The browser is on lasso's machine. A credential confined to some other
	// host has no business driving pages from there — it would reach whatever
	// lasso's machine can, which is exactly the boundary its scope draws.
	if !cs.allows("local") {
		return nil, sharedBrowserOut{}, fmt.Errorf("lasso's browsers run on lasso's own machine, which is outside this credential's reach (%s)", cs.reachSummary())
	}
	profile, err := resolveProfile(in.Browser)
	if strings.TrimSpace(in.Browser) == "" {
		// The default needs no lookup, and must not need the database either.
		profile, err = defaultBrowserProfile, nil
	}
	if err != nil {
		return nil, sharedBrowserOut{}, err
	}
	out := sharedBrowserOut{WSPath: cdpPathFor(profile), Pages: []browserPage{}, Browser: profile}
	_, out.ToolsReason, out.BrowserTools = browserMCP.resolve()
	var notes []string
	m, err := browserFor(profile)
	if err != nil {
		out.Note = err.Error()
		if profile == defaultBrowserProfile {
			out.Note = "the shared browser is not configured on this lasso"
		}
		return nil, out, nil
	}
	start := in.Start == nil || *in.Start
	if start {
		m.touch() // an agent about to connect: don't let the idle stop race it
		if _, err := m.ensure(ctx); err != nil {
			notes = append(notes, "could not start: "+err.Error())
		}
	}
	st := m.status()
	out.Available, out.Running, out.Pages = st.Available, st.Running, st.Pages
	if !st.Available || (!st.Running && st.Reason != "" && start) {
		if st.Reason != "" && !strings.Contains(strings.Join(notes, " "), st.Reason) {
			notes = append(notes, st.Reason)
		}
	}
	if !st.Running && !start {
		notes = append(notes, "not running; call with start:true (or just connect to /cdp, which starts it)")
	}
	if base := sharedBrowserBase(req); base != "" {
		out.HTTPEndpoint = base + cdpPathFor(profile)
		out.WSEndpoint = "ws" + strings.TrimPrefix(base, "http") + cdpPathFor(profile)
		out.BrowsersURL = base + cdpBrowsersPath
	} else {
		notes = append(notes, "lasso could not tell which URL you reached it on: prefix lasso's own URL to ws_path (ws://<lasso-host>/cdp, wss:// behind TLS)")
	}
	if m.cfg.AuthRequired {
		notes = append(notes, "/cdp requires the same credentials as /mcp: send your Authorization header (bearer token, or UI_AUTH basic) on the websocket")
	}
	out.Note = strings.Join(notes, "; ")
	return nil, out, nil
}

// sharedBrowserBase is the http(s)://host the MCP request reached lasso on, as
// withRequestBase recorded it; "" when the request did not come through it
// (a test server, a transport with no HTTP request).
func sharedBrowserBase(req *mcp.CallToolRequest) string {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return ""
	}
	b := strings.TrimRight(req.Extra.Header.Get(lassoBaseHeader), "/")
	if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
		return ""
	}
	return b
}

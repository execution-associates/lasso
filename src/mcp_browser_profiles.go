package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lasso's browsers and their tabs over MCP: an agent can create, rename,
// re-point and delete browsers (browserprofiles.go, where a browser is stored
// as a "profile"), and open, show, list and close tabs in any of them, with the
// tab it opens appearing on the human's screen the way open_file puts a file
// there — a `browser-open` SSE event every visible lasso tab acts on
// (lib/browser-profiles.ts).
//
// These tools manage WHICH browsers and pages exist and which page the human
// is looking at. Driving a page (clicking, typing, reading it) is the browser_*
// tools' job (browsermcp.go), each taking `browser`, or raw CDP's at
// /cdp/p/<id>.

// browserOut is a browser as the tools report it, with an absolute CDP
// endpoint when lasso could tell which URL the caller reached it on.
type browserOut struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	CDPURL     string        `json:"cdp_url,omitempty"` // a remote browser lasso dials instead of launching
	Default    bool          `json:"default"`
	Running    bool          `json:"running"`
	Tabs       []browserPage `json:"tabs"`
	WSEndpoint string        `json:"ws_endpoint"` // CDP websocket for THIS browser
	Note       string        `json:"note,omitempty"`
}

func profileOut(req *mcp.CallToolRequest, st browserProfileStatus) browserOut {
	o := browserOut{ID: st.ID, Name: st.Name, CDPURL: st.CDPURL, Default: st.Default,
		Running: st.Running, Tabs: st.Pages, WSEndpoint: st.WSPath}
	if o.Tabs == nil {
		o.Tabs = []browserPage{}
	}
	if base := sharedBrowserBase(req); base != "" {
		o.WSEndpoint = "ws" + strings.TrimPrefix(base, "http") + st.WSPath
	}
	if !st.Running && st.Reason != "" {
		o.Note = st.Reason
	}
	return o
}

// requireLocalBrowser is every browser tool's gate: the browsers run on
// lasso's own machine, and a credential confined elsewhere does not reach it.
func requireLocalBrowser(req *mcp.CallToolRequest) error {
	cs := callerFrom(req)
	if !cs.allows("local") {
		return fmt.Errorf("lasso's browsers run on lasso's own machine, which is outside this credential's reach (%s)", cs.reachSummary())
	}
	return nil
}

// ---- list_browsers -----------------------------------------------------------

const listBrowsersDescription = "List lasso's BROWSERS. Each is a separate browser: its own Chromium on lasso's machine with its own persistent cookies and logins, or a remote browser lasso dials (`cdp_url` set). The human sees them in lasso's Browser tab and picks one at the bottom of it. `default` marks the one the browser tools use when you name none. For each you get its open `tabs` (only while it runs) and `ws_endpoint` for raw CDP/Playwright. To drive one, pass its `id` (or name) as `browser` to the browser_* tools on this server. A client with no MCP at all gets the same list from `GET <lasso>/cdp/browsers`, each entry carrying its own ws_url."

type listBrowsersIn struct{}

type listBrowsersOut struct {
	Browsers []browserOut `json:"browsers"`
}

func listBrowsersTool(ctx context.Context, req *mcp.CallToolRequest, _ listBrowsersIn) (*mcp.CallToolResult, listBrowsersOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, listBrowsersOut{}, err
	}
	out := listBrowsersOut{Browsers: []browserOut{}}
	for _, st := range sharedBrowsers.statuses() {
		out.Browsers = append(out.Browsers, profileOut(req, st))
	}
	return nil, out, nil
}

// ---- create / update / delete -------------------------------------------------

const createBrowserDescription = "Create a browser: a separate Chromium on lasso's machine with its own persistent cookies and logins. It appears in the human's Browser-tab browser picker at once and starts on first use (a browser_* tool call, open_browser_tab, or connecting to its CDP endpoint). `id` is optional and derived from the name when omitted; it is what you pass as `browser` to the browser_* tools, and what appears in its CDP URL (/cdp/p/<id>). The browser tools you already have drive it at once: no reconnect. With `cdp_url` it is a REMOTE browser instead: a Chromium already running elsewhere (another machine, a container), reached at its DevTools HTTP endpoint, e.g. http://100.79.171.47:9222. lasso launches nothing and never stops it; how it runs, where its traffic goes and how it is secured is up to whoever runs it, and its logins are whatever that browser holds."

type createBrowserIn struct {
	Name   string `json:"name" jsonschema:"Display name, e.g. \"Work\" or \"US exit\"."`
	ID     string `json:"id,omitempty" jsonschema:"Optional id: 1-32 lowercase letters, digits or dashes. Derived from the name when omitted."`
	CDPURL string `json:"cdp_url,omitempty" jsonschema:"Optional: make this a remote browser lasso dials instead of launching — its DevTools HTTP base, http://host:port or https://host[:port]."`
}

func createBrowserTool(ctx context.Context, req *mcp.CallToolRequest, in createBrowserIn) (*mcp.CallToolResult, browserOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, browserOut{}, err
	}
	if sharedBrowsers == nil {
		return nil, browserOut{}, errors.New("browsers are not configured on this lasso")
	}
	p, err := sharedBrowsers.create(in.Name, in.ID, in.CDPURL)
	if err != nil {
		return nil, browserOut{}, err
	}
	browserProfilesChanged()
	return nil, profileOut(req, sharedBrowsers.statusOf(p)), nil
}

const updateBrowserDescription = "Rename a browser and/or change its `cdp_url`. Pass only what changes; an omitted field is left alone. The default browser can be renamed and pointed elsewhere too. `cdp_url` points it at a remote browser (\"\" switches it back to one lasso launches); the change detaches whatever it was using, and its page ids are gone."

type updateBrowserIn struct {
	Browser string  `json:"browser" jsonschema:"The browser to change: its id or display name."`
	Name    *string `json:"name,omitempty" jsonschema:"New display name."`
	CDPURL  *string `json:"cdp_url,omitempty" jsonschema:"New remote browser address (http://host:port), or \"\" for a browser lasso launches. Omit to leave it unchanged."`
}

func updateBrowserTool(ctx context.Context, req *mcp.CallToolRequest, in updateBrowserIn) (*mcp.CallToolResult, browserOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, browserOut{}, err
	}
	if strings.TrimSpace(in.Browser) == "" {
		return nil, browserOut{}, errors.New("browser is required")
	}
	if in.Name == nil && in.CDPURL == nil {
		return nil, browserOut{}, errors.New("nothing to change: pass name and/or cdp_url")
	}
	if sharedBrowsers == nil {
		return nil, browserOut{}, errors.New("browsers are not configured on this lasso")
	}
	id, err := resolveProfile(in.Browser)
	if err != nil {
		return nil, browserOut{}, err
	}
	st, err := sharedBrowsers.edit(ctx, id, in.Name, in.CDPURL)
	if err != nil {
		return nil, browserOut{}, err
	}
	return nil, profileOut(req, st), nil
}

const deleteBrowserDescription = "Delete a browser. Its Chromium is stopped (closing its tabs and any session driving it) and its data directory is DELETED, with every cookie and login in it — this cannot be undone, so only do it when the human asked. The default browser cannot be deleted."

type deleteBrowserIn struct {
	Browser string `json:"browser" jsonschema:"The browser to delete: its id or display name."`
}

type deleteBrowserOut struct {
	Deleted string `json:"deleted"` // the id that was deleted
}

func deleteBrowserTool(ctx context.Context, req *mcp.CallToolRequest, in deleteBrowserIn) (*mcp.CallToolResult, deleteBrowserOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, deleteBrowserOut{}, err
	}
	if strings.TrimSpace(in.Browser) == "" {
		return nil, deleteBrowserOut{}, errors.New("browser is required")
	}
	if sharedBrowsers == nil {
		return nil, deleteBrowserOut{}, errors.New("browsers are not configured on this lasso")
	}
	id, err := resolveProfile(in.Browser)
	if err != nil {
		return nil, deleteBrowserOut{}, err
	}
	if err := sharedBrowsers.remove(ctx, id); err != nil {
		return nil, deleteBrowserOut{}, err
	}
	browserProfilesChanged()
	return nil, deleteBrowserOut{Deleted: id}, nil
}

// ---- tabs --------------------------------------------------------------------

const listBrowserTabsDescription = "List the open tabs of lasso's browsers, per browser. With `browser`, just that one (starting nothing: a stopped browser has no tabs). Without it, every browser. Tab ids are CDP target ids, usable with show_browser_tab and close_browser_tab (and as targetId over CDP)."

type listBrowserTabsIn struct {
	Browser string `json:"browser,omitempty" jsonschema:"Only this browser (id or display name). Omit for every browser."`
}

type browserTabsOfBrowser struct {
	Browser string        `json:"browser"`
	Name    string        `json:"name"`
	Running bool          `json:"running"`
	Tabs    []browserPage `json:"tabs"`
}

type listBrowserTabsOut struct {
	Browsers []browserTabsOfBrowser `json:"browsers"`
}

func listBrowserTabsTool(ctx context.Context, req *mcp.CallToolRequest, in listBrowserTabsIn) (*mcp.CallToolResult, listBrowserTabsOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, listBrowserTabsOut{}, err
	}
	out := listBrowserTabsOut{Browsers: []browserTabsOfBrowser{}}
	add := func(st browserProfileStatus) {
		tabs := st.Pages
		if tabs == nil {
			tabs = []browserPage{}
		}
		out.Browsers = append(out.Browsers, browserTabsOfBrowser{Browser: st.ID, Name: st.Name, Running: st.Running, Tabs: tabs})
	}
	if strings.TrimSpace(in.Browser) != "" {
		id, err := resolveProfile(in.Browser)
		if err != nil {
			return nil, listBrowserTabsOut{}, err
		}
		st, err := sharedBrowsers.profileStatus(id)
		if err != nil {
			return nil, listBrowserTabsOut{}, err
		}
		add(st)
		return nil, out, nil
	}
	for _, st := range sharedBrowsers.statuses() {
		add(st)
	}
	return nil, out, nil
}

const openBrowserTabDescription = "Open a NEW tab in one of lasso's browsers and put it on the human's screen: every visible lasso tab switches its Browser tab to that browser and tab (opening the sidebar if needed), the way open_file shows a file. Use it when the human asks you to open a page for them, or in a particular browser (\"open this in my work browser\"). Starts the browser if it is stopped. The page loads from that browser — LASSO's machine for a browser lasso launches — so `localhost` means lasso's machine there. To then drive the page, use the browser_* tools with `browser` set to this one (find the tab with browser_list_pages by its URL), or `ws_endpoint` over CDP. Check `delivered`: 0 means no lasso tab is open and the human did NOT see it (the tab is still open in the browser). A browser stops after lasso's idle timeout with nothing connected to it, and its tabs close with it. `surface` picks the view: \"agent\" (default) the live Chromium, \"iframe\" the page embedded the way a terminal link opens, loaded by the human's own browser (their cookies and their `localhost`, not the browser's), for a page they will read or click themselves. Pass your $HERDR_PANE_ID as pane_id so the human is told which agent opened it."

type openBrowserTabIn struct {
	URL     string `json:"url" jsonschema:"The page to open: a full http(s) URL. A bare host[:port] gets https:// (http:// for localhost/127.0.0.1). about:blank opens an empty tab."`
	Browser string `json:"browser,omitempty" jsonschema:"The browser (id or display name) to open it in. Omit for the default browser."`
	Show    *bool  `json:"show,omitempty" jsonschema:"Put the tab on the human's screen (default true). false opens it quietly, for a page you will work in without switching their view."`
	Surface string `json:"surface,omitempty" jsonschema:"How the human's Browser tab shows it: \"agent\" (default) the live Chromium view, \"iframe\" the page embedded in an iframe. \"live\" and \"embed\" are accepted too. An http:// page on an https lasso cannot be embedded, so it shows in the live view."`
	PaneID  string `json:"pane_id,omitempty" jsonschema:"Your own herdr pane id ($HERDR_PANE_ID), used only to tell the human which agent opened the tab."`
}

type browserTabOut struct {
	TabID      string `json:"tab_id"`
	Browser    string `json:"browser"`
	URL        string `json:"url"`
	Title      string `json:"title,omitempty"`
	Delivered  int    `json:"delivered"` // lasso tabs the show request reached; 0 = nobody saw it
	WSEndpoint string `json:"ws_endpoint,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

// browserOpenEvent is the `browser-open` SSE payload the Browser tab acts on.
type browserOpenEvent struct {
	Profile string `json:"profile"`
	TabID   string `json:"tab_id"`
	URL     string `json:"url"`
	From    string `json:"from"`
	// Mode is the view the human's client switches to (live|embed). Empty is
	// live, which is what a UI that predates the field does anyway.
	Mode string `json:"mode,omitempty"`
}

// browserSurfaceMode maps a tool's `surface` to the browser_mode vocabulary
// the UI speaks: the UI's labels (agent, iframe) or the stored names (live,
// embed), omitted meaning agent.
func browserSurfaceMode(surface string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(surface)) {
	case "", "agent", browserModeLive:
		return browserModeLive, nil
	case "iframe", browserModeEmbed:
		return browserModeEmbed, nil
	}
	return "", fmt.Errorf("surface %q: must be \"agent\" or \"iframe\" (or \"live\"/\"embed\")", surface)
}

// browserOpenBroadcast hands the event to every connected tab and reports how
// many took it; 0 before the server is up (tests swap it).
var browserOpenBroadcast = func(ev browserOpenEvent) int {
	if srvHub == nil {
		return 0
	}
	return srvHub.broadcast("browser-open", ev)
}

// normalizeTabURL turns what an agent passes into a URL Chromium will load.
// Schemes are limited to the web and about:blank; file:, chrome: and
// javascript: are refused — this is a tool for showing a human a page, and
// those are not pages to show.
func normalizeTabURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("url is required")
	}
	if s == "about:blank" {
		return s, nil
	}
	if !strings.Contains(s, "://") {
		host := s
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		h := strings.ToLower(host)
		if h == "localhost" || strings.HasPrefix(h, "localhost:") || strings.HasPrefix(h, "127.") || strings.HasPrefix(h, "[::1]") {
			s = "http://" + s
		} else {
			s = "https://" + s
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("url %q is not a URL lasso can open", raw)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("url %q: only http(s) pages (or about:blank) can be opened this way", raw)
	}
	return u.String(), nil
}

// callerName is who the human is told opened something: the agent's name when
// pane_id resolves, "an agent" otherwise, with the reason in detail.
func callerName(ctx context.Context, req *mcp.CallToolRequest, paneID string) (from, detail string) {
	from = "an agent"
	if strings.TrimSpace(paneID) == "" {
		return from, ""
	}
	who, err := resolveCallerAgent(ctx, callerFrom(req), "", paneID)
	switch {
	case err != nil:
		return from, "could not name you: " + err.Error()
	case who.Found:
		if name := firstNonEmpty(who.Agent.SidebarName, who.Agent.Title); name != "" {
			from = name
		}
		return from, ""
	default:
		return from, "could not name you: " + who.Detail
	}
}

func openBrowserTabTool(ctx context.Context, req *mcp.CallToolRequest, in openBrowserTabIn) (*mcp.CallToolResult, browserTabOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, browserTabOut{}, err
	}
	u, err := normalizeTabURL(in.URL)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	mode, err := browserSurfaceMode(in.Surface)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	id, err := resolveProfile(in.Browser)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	m, err := browserFor(id)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	m.touch() // opened for someone to look at: don't let the idle stop race it
	wasRunning := m.current() != nil
	p, err := m.ensure(ctx)
	if err != nil {
		return nil, browserTabOut{}, fmt.Errorf("could not start browser %q: %w", id, err)
	}
	// A browser this call just launched has its own about:blank, which would sit
	// in the human's tab strip beside the page they were sent. Its id is taken
	// BEFORE opening, so the new tab (about:blank until it commits) is not it.
	var launchTabs []string
	if !wasRunning {
		if pages, err := browserPages(p); err == nil {
			for _, pg := range pages {
				if pg.URL == "about:blank" {
					launchTabs = append(launchTabs, pg.ID)
				}
			}
		}
	}
	var t struct {
		ID    string `json:"id"`
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	// PUT: GET on /json/new is refused since Chromium 111. The query is the
	// URL itself, unescaped by Chromium.
	if err := devtoolsDo(p, http.MethodPut, "/json/new?"+url.PathEscape(u), &t); err != nil {
		if devtoolsNotFound(err) && p.remote() {
			return nil, browserTabOut{}, fmt.Errorf("browser %q is a remote browser that does not open tabs over the DevTools HTTP endpoint (PUT /json/new answered 404); use its existing page through the browser_* tools (browser %q) or CDP instead: %w", id, id, err)
		}
		return nil, browserTabOut{}, fmt.Errorf("open %s in browser %q: %w", u, id, err)
	}
	for _, tid := range launchTabs {
		_ = devtoolsDo(p, http.MethodGet, "/json/close/"+tid, nil)
	}
	st, _ := sharedBrowsers.profileStatus(id)
	out := browserTabOut{TabID: t.ID, Browser: id, URL: u, Title: t.Title, WSEndpoint: profileOut(req, st).WSEndpoint}
	if in.Show != nil && !*in.Show {
		out.Detail = "opened without showing it (show:false)"
		return nil, out, nil
	}
	from, detail := callerName(ctx, req, in.PaneID)
	out.Delivered = browserOpenBroadcast(browserOpenEvent{Profile: id, TabID: t.ID, URL: u, From: from, Mode: mode})
	if out.Delivered == 0 {
		out.Detail = "no lasso tab is open, so nobody saw it (the tab is open in the browser)"
	} else {
		out.Detail = detail
	}
	return nil, out, nil
}

// browserTabID is what a CDP target id looks like; anything else would be a
// path segment lasso has no business putting in a devtools URL.
var browserTabID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// findTab looks a tab up in a RUNNING browser, starting nothing.
func findTab(browserArg, tabID string) (string, *browserProc, browserPage, error) {
	if !browserTabID.MatchString(strings.TrimSpace(tabID)) {
		return "", nil, browserPage{}, fmt.Errorf("tab_id %q is not a tab id (list_browser_tabs shows them)", tabID)
	}
	tabID = strings.TrimSpace(tabID)
	id, err := resolveProfile(browserArg)
	if err != nil {
		return "", nil, browserPage{}, err
	}
	m, err := browserFor(id)
	if err != nil {
		return "", nil, browserPage{}, err
	}
	p := m.current()
	if p == nil {
		return "", nil, browserPage{}, fmt.Errorf("browser %q is not running, so it has no tabs", id)
	}
	pages, err := browserPages(p)
	if err != nil {
		return "", nil, browserPage{}, err
	}
	for _, pg := range pages {
		if pg.ID == tabID {
			return id, p, pg, nil
		}
	}
	return "", nil, browserPage{}, fmt.Errorf("no tab %q in browser %q (list_browser_tabs shows the open ones)", tabID, id)
}

const showBrowserTabDescription = "Put an EXISTING tab of one of lasso's browsers on the human's screen: every visible lasso tab switches its Browser tab to that browser and tab. Use it to point them at a page you have been working in. `surface` picks the view as on open_browser_tab: \"agent\" (default) or \"iframe\". `delivered` 0 means no lasso tab is open, so the human did NOT see it."

type showBrowserTabIn struct {
	TabID   string `json:"tab_id" jsonschema:"The tab to show (from list_browser_tabs or open_browser_tab)."`
	Browser string `json:"browser,omitempty" jsonschema:"The tab's browser (id or display name). Omit for the default browser."`
	Surface string `json:"surface,omitempty" jsonschema:"How the human's Browser tab shows it: \"agent\" (default) the live Chromium view, \"iframe\" the page embedded in an iframe. \"live\" and \"embed\" are accepted too. An http:// page on an https lasso cannot be embedded, so it shows in the live view."`
	PaneID  string `json:"pane_id,omitempty" jsonschema:"Your own herdr pane id ($HERDR_PANE_ID), to tell the human who is showing it."`
}

func showBrowserTabTool(ctx context.Context, req *mcp.CallToolRequest, in showBrowserTabIn) (*mcp.CallToolResult, browserTabOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, browserTabOut{}, err
	}
	mode, err := browserSurfaceMode(in.Surface)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	id, _, pg, err := findTab(in.Browser, in.TabID)
	if err != nil {
		return nil, browserTabOut{}, err
	}
	from, detail := callerName(ctx, req, in.PaneID)
	out := browserTabOut{TabID: pg.ID, Browser: id, URL: pg.URL, Title: pg.Title}
	out.Delivered = browserOpenBroadcast(browserOpenEvent{Profile: id, TabID: pg.ID, URL: pg.URL, From: from, Mode: mode})
	if out.Delivered == 0 {
		out.Detail = "no lasso tab is open, so nobody saw it"
	} else {
		out.Detail = detail
	}
	return nil, out, nil
}

const closeBrowserTabDescription = "Close a tab in one of lasso's browsers. Close the tabs you opened when you are done; don't close the human's or another agent's unless asked."

type closeBrowserTabIn struct {
	TabID   string `json:"tab_id" jsonschema:"The tab to close (from list_browser_tabs or open_browser_tab)."`
	Browser string `json:"browser,omitempty" jsonschema:"The tab's browser (id or display name). Omit for the default browser."`
}

type closeBrowserTabOut struct {
	Closed  string `json:"closed"`
	Browser string `json:"browser"`
}

func closeBrowserTabTool(ctx context.Context, req *mcp.CallToolRequest, in closeBrowserTabIn) (*mcp.CallToolResult, closeBrowserTabOut, error) {
	if err := requireLocalBrowser(req); err != nil {
		return nil, closeBrowserTabOut{}, err
	}
	id, p, pg, err := findTab(in.Browser, in.TabID)
	if err != nil {
		return nil, closeBrowserTabOut{}, err
	}
	if err := devtoolsDo(p, http.MethodGet, "/json/close/"+pg.ID, nil); err != nil {
		if devtoolsNotFound(err) && p.remote() {
			return nil, closeBrowserTabOut{}, fmt.Errorf("browser %q is a remote browser that does not close tabs over the DevTools HTTP endpoint (/json/close answered 404): %w", id, err)
		}
		return nil, closeBrowserTabOut{}, fmt.Errorf("close tab %s: %w", pg.ID, err)
	}
	return nil, closeBrowserTabOut{Closed: pg.ID, Browser: id}, nil
}

// registerBrowserProfileTools adds the browser and tab tools to lasso's MCP
// server.
func registerBrowserProfileTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "list_browsers", Description: listBrowsersDescription}, listBrowsersTool)
	mcp.AddTool(s, &mcp.Tool{Name: "create_browser", Description: createBrowserDescription}, createBrowserTool)
	mcp.AddTool(s, &mcp.Tool{Name: "update_browser", Description: updateBrowserDescription}, updateBrowserTool)
	mcp.AddTool(s, &mcp.Tool{Name: "delete_browser", Description: deleteBrowserDescription}, deleteBrowserTool)
	mcp.AddTool(s, &mcp.Tool{Name: "list_browser_tabs", Description: listBrowserTabsDescription}, listBrowserTabsTool)
	mcp.AddTool(s, &mcp.Tool{Name: "open_browser_tab", Description: openBrowserTabDescription}, openBrowserTabTool)
	mcp.AddTool(s, &mcp.Tool{Name: "show_browser_tab", Description: showBrowserTabDescription}, showBrowserTabTool)
	mcp.AddTool(s, &mcp.Tool{Name: "close_browser_tab", Description: closeBrowserTabDescription}, closeBrowserTabTool)
}

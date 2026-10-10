package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// lasso's Settings screen over MCP: get_settings reads every section the
// Settings tab shows, update_settings writes them. Each write goes through the
// SAME HTTP handler the Settings tab calls, in-process, so the validation, the
// per-field merges, the rev bumps and the fan-outs are the browser's exactly —
// a second copy of those rules would drift from the first.
//
// Plugins are the exception and stay read-only here. Enabling one approves its
// permissions, trusting one runs it on the host outside its sandbox, and
// installing or updating one changes what an approval covers: those need a
// human looking at the approval dialog, and /mcp is open by default, so an
// agent (or a page an agent read) must not be able to grant them.

// settingsSections are what get_settings can return, in display order.
var settingsSections = []string{"ui", "agents", "repos", "theme", "notifications", "browser", "plugins"}

// mcpSettingsClientID is the writer identity update_settings stamps on a
// ui_state patch. A deliberate settings change is a human-equivalent act, so it
// also carries user_intent: without it the sidebar-layout claim (uilock.go)
// would refuse sidebar_collapsed / sidebar_pct from a non-owner.
const mcpSettingsClientID = "mcp-settings"

// callSettingsAPI runs one of lasso's own HTTP handlers in-process and decodes
// its JSON answer. A non-2xx status is returned as an error carrying the
// handler's own message, which is what a human would have seen as a toast.
func callSettingsAPI(ctx context.Context, h http.HandlerFunc, method, path string, body any) (any, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequestWithContext(ctx, method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h(rec, r)
	raw := bytes.TrimSpace(rec.Body.Bytes())
	if rec.Code < 200 || rec.Code > 299 {
		msg := string(raw)
		var je struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &je) == nil && je.Error != "" {
			msg = je.Error
		}
		if msg == "" {
			msg = http.StatusText(rec.Code)
		}
		return nil, fmt.Errorf("%s %s: %s", method, strings.SplitN(path, "?", 2)[0], msg)
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: undecodable answer: %w", path, err)
	}
	return out, nil
}

// withHostQuery names the host a per-host handler acts on. Empty means
// "local", the default every other MCP tool uses — not the handler's own
// default, which is whichever host the UI happens to be viewing.
func withHostQuery(path, host string) string {
	if host == "" {
		host = "local"
	}
	return path + "?host=" + url.QueryEscape(host)
}

// requireSettingsReach gates every settings tool: lasso's settings live in the
// lasso host's own db, so a caller must reach "local", and a per-host section
// (agents, repos) additionally the host it names.
func requireSettingsReach(req *mcp.CallToolRequest, host string) error {
	cs := callerFrom(req)
	if !cs.allows("local") {
		return fmt.Errorf("lasso's settings live on lasso's own machine, which is outside this credential's reach (%s)", cs.reachSummary())
	}
	if host != "" {
		return cs.requireHost(host)
	}
	return nil
}

// ---- get_settings -------------------------------------------------------------

const getSettingsDescription = "Read lasso's settings — everything the Settings tab shows. Sections: `ui` (the synced UI preferences: appearance_mode, palette_light/palette_dark, theme_atmosphere, typography, chat_text, terminal_text, browser_mode, terminal_links_in_sidebar, files_click_navigates, sidebar_tabs, usage_hidden/usage_order/usage_compact, creator_default_host, agents_group_host/agents_group_repo, chat_sidebar, onboarding_done, …), `agents` (the creator defaults of `host` — repos_root, branch_prefix, default_agent, default_terminal_workspace, scratch_setup — plus `auto_title`), `repos` (each repo under `host`'s repo roots with its copy_files and setup), `theme` (herdr's current theme, the selectable `themes`, sync_agent_themes, theme_sync_off hosts, and the installable theme `catalog`), `notifications` (registered push devices by id and label; endpoints are never shown), `browser` (the browsers' status; list_browsers and the other browser tools manage them), and `plugins` (READ-ONLY: plugin state; enabling, trusting, installing and updating plugins is done by a human in lasso's Settings tab, never over MCP). Omit `section` for all of them. update_settings changes them."

type getSettingsIn struct {
	Section string `json:"section,omitempty" jsonschema:"One of ui, agents, repos, theme, notifications, browser, plugins. Omit for every section."`
	Host    string `json:"host,omitempty" jsonschema:"Whose per-host settings the agents and repos sections show: local or an ssh alias from list_hosts. Omit for the box lasso runs on (local)."`
}

func getSettingsTool(ctx context.Context, req *mcp.CallToolRequest, in getSettingsIn) (*mcp.CallToolResult, map[string]any, error) {
	if err := requireSettingsReach(req, in.Host); err != nil {
		return nil, nil, err
	}
	want := settingsSections
	if s := strings.TrimSpace(in.Section); s != "" {
		if !slices.Contains(settingsSections, s) {
			return nil, nil, fmt.Errorf("unknown section %q (one of %s)", s, strings.Join(settingsSections, ", "))
		}
		want = []string{s}
	}
	out := map[string]any{}
	var errs []string
	for _, s := range want {
		v, err := readSettingsSection(ctx, s, in.Host)
		if err != nil {
			// One unreachable host must not hide the other sections.
			errs = append(errs, fmt.Sprintf("%s: %v", s, err))
			continue
		}
		out[s] = v
	}
	if len(errs) > 0 {
		if len(errs) == len(want) {
			return nil, nil, errors.New(strings.Join(errs, "; "))
		}
		out["errors"] = errs
	}
	return nil, out, nil
}

func readSettingsSection(ctx context.Context, section, host string) (any, error) {
	switch section {
	case "ui":
		return callSettingsAPI(ctx, serveUIState, http.MethodGet, "/api/ui-state", nil)
	case "agents":
		c, err := callSettingsAPI(ctx, serveAgentConfig, http.MethodGet, withHostQuery("/api/agent-config", host), nil)
		if err != nil {
			return nil, err
		}
		m, _ := c.(map[string]any)
		out := map[string]any{"auto_title": autoTitleEnabled()}
		// Only the settings, not this lasso's agent log riding in the same answer.
		for _, k := range []string{"repos_root", "branch_prefix", "default_agent", "default_terminal_workspace", "scratch_setup"} {
			if v, ok := m[k]; ok {
				out[k] = v
			}
		}
		return out, nil
	case "repos":
		return callSettingsAPI(ctx, serveRepos, http.MethodGet, withHostQuery("/api/repos", host), nil)
	case "theme":
		t, err := callSettingsAPI(ctx, serveTheme, http.MethodGet, "/api/theme", nil)
		if err != nil {
			return nil, err
		}
		m, _ := t.(map[string]any)
		out := map[string]any{}
		// Drop the rendered CSS and xterm palettes: they are paint, not settings.
		for k, v := range m {
			if k != "css" && k != "xterm" {
				out[k] = v
			}
		}
		out["catalog"] = themeCatalog()
		return out, nil
	case "notifications":
		cfg, err := callSettingsAPI(ctx, servePushConfig, http.MethodGet, "/api/push", nil)
		if err != nil {
			return nil, err
		}
		m, _ := cfg.(map[string]any)
		return map[string]any{"devices": m["devices"]}, nil
	case "browser":
		if sharedBrowsers == nil {
			return map[string]any{"available": false}, nil
		}
		return callSettingsAPI(ctx, sharedBrowsers.serveStatus, http.MethodGet, "/api/browser", nil)
	case "plugins":
		if plugins == nil {
			return map[string]any{"available": false}, nil
		}
		v, err := callSettingsAPI(ctx, plugins.serveAPI, http.MethodGet, "/api/plugins", nil)
		if err != nil {
			return nil, err
		}
		if m, ok := v.(map[string]any); ok {
			m["note"] = "read-only over MCP: a human enables, trusts, installs and updates plugins in lasso's Settings tab"
		}
		return v, nil
	}
	return nil, fmt.Errorf("unknown section %q", section)
}

// ---- update_settings ----------------------------------------------------------

const updateSettingsDescription = "Change lasso's settings — what the Settings tab changes, with the same validation; every open lasso tab updates live. Pass only what changes; an omitted field is left alone. `ui` is a PATCH of the synced UI preferences get_settings shows under `ui` (only the keys you send change; typography/chat_text/terminal_text/theme_atmosphere merge per field, \"\" or null deletes one; sidebar_tabs is replaced whole; pinned agents change only through `agent_pins: {<key>: true|false}`, the background gallery through `remember_background`/`forget_background`). `agent_defaults` and `repo` write `host`'s own creator settings (\"\" clears a field). `theme` switches herdr's theme fleet-wide (refused while an appearance palette is set — change ui.palette_light/palette_dark/appearance_mode instead); `sync_theme_now` pushes the current theme to every host; `install_theme_url` installs an Omarchy theme from a URL. `remove_push_device` forgets a notification device by the id get_settings lists. Plugin approvals and trust are NOT settable here: they need a human in lasso's Settings tab. Returns the affected sections as they now read."

type agentDefaultsIn struct {
	ReposRoot                *string `json:"repos_root,omitempty" jsonschema:"Directories scanned for git repos, one per line."`
	BranchPrefix             *string `json:"branch_prefix,omitempty" jsonschema:"Prefix for new agents' branch names."`
	DefaultAgent             *string `json:"default_agent,omitempty" jsonschema:"The agent CLI the New dialog preselects (claude, codex, opencode, omp, pi); \"\" follows the last one used."`
	DefaultTerminalWorkspace *string `json:"default_terminal_workspace,omitempty" jsonschema:"The herdr workspace new terminals open in."`
	ScratchSetup             *string `json:"scratch_setup,omitempty" jsonschema:"Shell commands run in a new scratch agent's workspace before it starts."`
}

type repoSettingsIn struct {
	Path      string  `json:"path" jsonschema:"The repository's absolute path on host (list_repos or get_settings section repos shows them)."`
	CopyFiles *string `json:"copy_files,omitempty" jsonschema:"Globs of untracked files copied into each new worktree, one per line."`
	Setup     *string `json:"setup,omitempty" jsonschema:"Shell commands run in each new worktree before the agent starts."`
}

type themeSyncIn struct {
	Host    string `json:"host" jsonschema:"local or an ssh alias."`
	Enabled bool   `json:"enabled" jsonschema:"false stops every theme write lasso makes to that host; true resumes (and converges it now)."`
}

type updateSettingsIn struct {
	Host             string           `json:"host,omitempty" jsonschema:"Whose creator settings agent_defaults and repo write: local or an ssh alias. Omit for the box lasso runs on (local)."`
	UI               map[string]any   `json:"ui,omitempty" jsonschema:"A patch of the ui section, e.g. {\"appearance_mode\":\"dark\",\"palette_dark\":\"tokyo-night\",\"terminal_text\":{\"size\":15}}."`
	AutoTitle        *bool            `json:"auto_title,omitempty" jsonschema:"Name new agents from their prompt."`
	AgentDefaults    *agentDefaultsIn `json:"agent_defaults,omitempty" jsonschema:"host's creator defaults."`
	Repo             *repoSettingsIn  `json:"repo,omitempty" jsonschema:"One repository's per-repo settings on host."`
	Theme            string           `json:"theme,omitempty" jsonschema:"herdr theme name to switch to (get_settings section theme lists them)."`
	SyncAgentThemes  *bool            `json:"sync_agent_themes,omitempty" jsonschema:"Mirror the theme into the agent CLIs' own theme files."`
	ThemeSync        *themeSyncIn     `json:"theme_sync,omitempty" jsonschema:"Turn theme writes to one host on or off."`
	SyncThemeNow     bool             `json:"sync_theme_now,omitempty" jsonschema:"Push the current theme to every reachable host now (runs in the background; the outcome arrives as a toast in lasso)."`
	InstallThemeURL  string           `json:"install_theme_url,omitempty" jsonschema:"Install an Omarchy theme from this URL (a GitHub repo or archive)."`
	RemovePushDevice string           `json:"remove_push_device,omitempty" jsonschema:"Forget the notification device with this id (from get_settings section notifications)."`
}

func updateSettingsTool(ctx context.Context, req *mcp.CallToolRequest, in updateSettingsIn) (*mcp.CallToolResult, map[string]any, error) {
	if err := requireSettingsReach(req, in.Host); err != nil {
		return nil, nil, err
	}
	out := map[string]any{}
	did := false

	if in.UI != nil {
		patch := make(map[string]any, len(in.UI)+2)
		for k, v := range in.UI {
			patch[k] = v
		}
		patch["client_id"] = mcpSettingsClientID
		patch["user_intent"] = true
		v, err := callSettingsAPI(ctx, serveUIState, http.MethodPost, "/api/ui-state", patch)
		if err != nil {
			return nil, nil, err
		}
		out["ui"] = v
		did = true
	}

	if in.AutoTitle != nil || in.AgentDefaults != nil {
		if in.AutoTitle != nil {
			if _, err := callSettingsAPI(ctx, serveAutoTitle, http.MethodPost, "/api/auto-title", map[string]any{"enabled": *in.AutoTitle}); err != nil {
				return nil, nil, err
			}
		}
		if d := in.AgentDefaults; d != nil {
			p := defaultsPatch{
				ReposRoot: d.ReposRoot, BranchPrefix: d.BranchPrefix, DefaultAgent: d.DefaultAgent,
				DefaultTerminalWorkspace: d.DefaultTerminalWorkspace, ScratchSetup: d.ScratchSetup,
			}
			if _, err := callSettingsAPI(ctx, serveAgentConfig, http.MethodPost, withHostQuery("/api/agent-config", in.Host), p); err != nil {
				return nil, nil, err
			}
		}
		v, err := readSettingsSection(ctx, "agents", in.Host)
		if err != nil {
			return nil, nil, err
		}
		out["agents"] = v
		did = true
	}

	if r := in.Repo; r != nil {
		if strings.TrimSpace(r.Path) == "" {
			return nil, nil, errors.New("repo.path is required")
		}
		if r.CopyFiles == nil && r.Setup == nil {
			return nil, nil, errors.New("repo: nothing to change, pass copy_files and/or setup")
		}
		v, err := callSettingsAPI(ctx, serveRepoConfig, http.MethodPost, withHostQuery("/api/repo-config", in.Host),
			repoConfigPatch{Path: r.Path, CopyFiles: r.CopyFiles, Setup: r.Setup})
		if err != nil {
			return nil, nil, err
		}
		out["repo"] = v
		did = true
	}

	if in.InstallThemeURL != "" {
		v, err := callSettingsAPI(ctx, serveOmarchyThemes, http.MethodPost, "/api/omarchy-themes", map[string]any{"url": in.InstallThemeURL})
		if err != nil {
			return nil, nil, err
		}
		if m, ok := v.(map[string]any); ok {
			out["installed_theme"] = m["installed"]
		}
		did = true
	}

	if in.Theme != "" || in.SyncAgentThemes != nil || in.ThemeSync != nil {
		body := map[string]any{}
		if in.Theme != "" {
			body["name"] = in.Theme
		}
		if in.SyncAgentThemes != nil {
			body["sync_agent_themes"] = *in.SyncAgentThemes
		}
		if in.ThemeSync != nil {
			body["theme_sync_host"] = in.ThemeSync.Host
			body["theme_sync"] = in.ThemeSync.Enabled
		}
		if _, err := callSettingsAPI(ctx, serveThemeSet, http.MethodPost, "/api/theme-set", body); err != nil {
			return nil, nil, err
		}
		did = true
	}

	if in.SyncThemeNow {
		// quiet:false — an agent asked, and the human should see that it landed.
		if _, err := callSettingsAPI(ctx, serveThemeSync, http.MethodPost, "/api/theme-sync", map[string]any{}); err != nil {
			return nil, nil, err
		}
		out["theme_sync_started"] = true
		did = true
	}

	if in.Theme != "" || in.SyncAgentThemes != nil || in.ThemeSync != nil || in.InstallThemeURL != "" {
		v, err := readSettingsSection(ctx, "theme", "")
		if err != nil {
			return nil, nil, err
		}
		out["theme"] = v
	}

	if id := strings.TrimSpace(in.RemovePushDevice); id != "" {
		if err := removePushDeviceByID(id); err != nil {
			return nil, nil, err
		}
		v, err := readSettingsSection(ctx, "notifications", "")
		if err != nil {
			return nil, nil, err
		}
		out["notifications"] = v
		did = true
	}

	if !did {
		return nil, nil, errors.New("nothing to change: pass at least one setting (get_settings shows them)")
	}
	return nil, out, nil
}

// removePushDeviceByID forgets the push subscription whose endpoint digest is
// id — the same id servePushConfig lists. The endpoint itself is a bearer
// capability and is never handed to a client, so the id is the only handle.
func removePushDeviceByID(id string) error {
	subs, err := listPushSubscriptions()
	if err != nil {
		return err
	}
	for _, s := range subs {
		sum := sha256.Sum256([]byte(s.Endpoint))
		if b64.EncodeToString(sum[:4]) == id {
			return deletePushSubscription(s.Endpoint)
		}
	}
	return fmt.Errorf("no notification device %q (get_settings section notifications lists them)", id)
}

// registerSettingsTools adds get_settings and update_settings to lasso's MCP
// server.
func registerSettingsTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "get_settings", Description: getSettingsDescription}, getSettingsTool)
	mcp.AddTool(s, &mcp.Tool{Name: "update_settings", Description: updateSettingsDescription}, updateSettingsTool)
}

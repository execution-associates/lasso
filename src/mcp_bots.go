package main

// Bots over MCP: listing them, switching them on and off, and — so a bot can
// configure itself when its human asks — reading and changing one bot's
// settings and environment. Every bot reaches these through the "lasso" server
// lasso puts in its MCP config (botLassoMCP). The rest of the Bots view
// (create, delete, conversations, chat, skills, OAuth) is mcp_botsmanage.go.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listBotsIn struct{}

type botSummary struct {
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Dir         string   `json:"dir"`
	Workspace   string   `json:"workspace"`
	Runtime     string   `json:"runtime" jsonschema:"omp (omp itself in a herdr pane, driven through lasso's bridge) or claude (Claude Code in a herdr pane)"`
	Model       string   `json:"model,omitempty"`
	State       string   `json:"state" jsonschema:"stopped, starting, idle, working or blocked"`
	WaitingFor  string   `json:"waiting_for,omitempty" jsonschema:"why a blocked bot is waiting, in its own words"`
	PaneID      string   `json:"pane_id,omitempty" jsonschema:"a claude bot's herdr pane; message any bot with send_agent (to = the bot's name)"`
	KeepRunning bool     `json:"keep_running"`
	Channels    []string `json:"channels,omitempty"`
	LastText    string   `json:"last_text,omitempty"`
	LastAt      string   `json:"last_at,omitempty"`
}

type listBotsOut struct {
	Bots []botSummary `json:"bots"`
}

type botNameIn struct {
	Name  string `json:"name" jsonschema:"the bot's name, as list_bots shows it"`
	Fresh bool   `json:"fresh,omitempty" jsonschema:"start a new conversation instead of resuming the bot's last one"`
}

type botStateOut struct {
	Bot botSummary `json:"bot"`
}

func summarizeBot(v botView) botSummary {
	return botSummary{
		Name: v.Name, Host: mcpHostName(v.Host), Dir: v.Dir, Workspace: v.Workspace, Runtime: v.Runtime, Model: v.Model,
		State: v.State, WaitingFor: v.WaitingFor, PaneID: v.PaneID, KeepRunning: v.KeepRunning,
		Channels: v.channels(), LastText: v.LastText, LastAt: v.LastAt,
	}
}

func registerBotTools(s *mcp.Server) {
	registerBotJobTools(s)
	registerBotJobExtraTools(s)
	registerBotTalkTools(s)
	registerBotManageTools(s)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_bots",
		Description: "List lasso's bots: long-lived agent sessions, each with its own folder and MCP servers, that lasso keeps running. A bot's runtime is omp (omp itself in a herdr pane, driven through lasso's bridge) or claude (Claude Code in a herdr pane). Each entry has its live state (stopped/starting/idle/working/blocked) and the newest line of its conversation. Only bots on hosts your credential reaches are listed. Talk to a running bot with send_agent, its name as `to`.",
	}, listBotsTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_bot",
		Description: "Read one bot's whole configuration: runtime, model, effort, permission mode, MCP servers (and which are channels), extra args, its folder, its environment variables (a secret's value is never shown), its own skills and its OAuth sign-ins. A bot asked to change itself reads this first.",
	}, getBotTool)
	mcp.AddTool(s, &mcp.Tool{
		Name: "update_bot",
		Description: "Change a bot's settings: runtime, folder, model, effort, permission mode, MCP servers (the complete list, replacing the current one), strict MCP, extra args, launch task, keep-running, notifications, workspace. Only the fields you pass change. Lasso rewrites the bot's launch script and MCP config; a running bot picks the change up when it restarts (restart_needed). " +
			"runtime and dir change only while the bot is stopped: the call is refused while it runs, so stop_bot first, update_bot, then start_bot. A runtime switch rewrites the launch task and MCP config for the new runtime and keeps the MCP servers (signed-in OAuth ones included), the environment and secrets, the jobs and .claude/skills; " +
			"model and permission mode reset to the new runtime's default unless passed in the same call, and effort is kept only when the new runtime has that level. The bot starts a new conversation on its next start (the two keep different session files). " +
			"Switching to claude with only an AGENTS.md writes a CLAUDE.md that imports it (@AGENTS.md), and switching to omp with only a CLAUDE.md copies it to AGENTS.md. A claude bot signs in to Claude with the host's own Claude Code login, or with a CLAUDE_CODE_OAUTH_TOKEN secret (from `claude setup-token`) set with set_bot_env. " +
			"Its instructions (AGENTS.md for an omp bot, CLAUDE.md for a claude bot) and skills are files in its folder, edited directly or with add_bot_skill / remove_bot_skill.",
	}, updateBotTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_bot_env",
		Description: "Set one of a bot's environment variables, plain or secret (encrypted by fnox; the value never comes back). A running bot sees it after a restart. Prefer asking the human to type a secret into lasso's Bots view over passing one through a conversation.",
	}, setBotEnvTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "unset_bot_env",
		Description: "Remove one of a bot's environment variables. A running bot sees the change after a restart.",
	}, unsetBotEnvTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_bot_avatar",
		Description: "Set a bot's picture (its avatar in lasso's Bots view and its notifications) from an image file on its host, or clear it. PNG, JPEG, WebP or GIF, at most 2 MB; a square image looks best. A bot asked to change its own picture downloads or generates the image to a file, then calls this with its own name.",
	}, setBotAvatarTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "start_bot",
		Description: "Start a stopped bot. It resumes its last conversation unless fresh is true.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botStateOut, error) {
		return botLifecycleTool(req, in, "start")
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "stop_bot",
		Description: "Stop a bot: end its process (a claude bot's herdr pane closes) and keep it stopped (keep-running will not relaunch it) until it is started again. Its conversation stays on disk.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botStateOut, error) {
		return botLifecycleTool(req, in, "stop")
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "restart_bot",
		Description: "Restart a bot, resuming its conversation unless fresh is true. Use it after its settings changed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botStateOut, error) {
		return botLifecycleTool(req, in, "restart")
	})
}

type getBotOut struct {
	Bot    botSummary     `json:"bot"`
	Config *botRecord     `json:"config"`
	Folder string         `json:"folder" jsonschema:"the bot's folder on its host: its instructions (AGENTS.md for omp, CLAUDE.md for claude) and .claude/skills/ are files there to edit directly"`
	Env    []botEnvVar    `json:"env" jsonschema:"the bot's environment variables; a secret's value is never shown"`
	Skills []botSkill     `json:"skills" jsonschema:"the bot's own project skills"`
	OAuth  map[string]any `json:"oauth,omitempty" jsonschema:"OAuth sign-ins by MCP server; begin_bot_oauth starts one, which the human approves in their browser"`
}

type updateBotIn struct {
	Name           string          `json:"name" jsonschema:"the bot's name"`
	Runtime        *string         `json:"runtime,omitempty" jsonschema:"omp or claude; only while the bot is stopped"`
	Dir            *string         `json:"dir,omitempty" jsonschema:"the bot's folder on its host (absolute or ~/...); only while the bot is stopped"`
	Model          *string         `json:"model,omitempty" jsonschema:"model: a claude alias or full name for a claude bot, an omp provider/model selector (e.g. anthropic/claude-opus-5) for an omp bot; empty for the default"`
	Effort         *string         `json:"effort,omitempty" jsonschema:"claude: low, medium, high, xhigh or max. omp: off, minimal, low, medium, high, xhigh, max or auto. Empty for the default"`
	PermissionMode *string         `json:"permission_mode,omitempty" jsonschema:"claude: acceptEdits, auto, bypassPermissions, manual, dontAsk or plan. omp (its approval mode): yolo, write or always-ask. Empty for the default"`
	MCP            *[]botMCPServer `json:"mcp,omitempty" jsonschema:"the COMPLETE list of MCP servers (it replaces the current one, so read it with get_bot first). channel:true makes a server a Claude Code channel; for a server that needs OAuth set oauth:true and ask the human to sign in from lasso's Bots view"`
	StrictMCP      *bool           `json:"strict_mcp,omitempty" jsonschema:"claude bots: true for only these MCP servers, no claude.ai connectors or user-level servers"`
	ExtraArgs      *[]string       `json:"extra_args,omitempty" jsonschema:"extra CLI arguments (claude's or omp's), one element per argument"`
	LaunchTask     *string         `json:"launch_task,omitempty" jsonschema:"the mise task in the bot's folder that launches it (a mode, such as another provider); empty or bot for the generated one. It must exist, set its environment, and end with exec mise run bot -- \"$@\""`
	KeepRunning    *bool           `json:"keep_running,omitempty" jsonschema:"relaunch the bot if it stops unexpectedly"`
	Notify         *bool           `json:"notify,omitempty" jsonschema:"push a notification to the human's devices each time the bot finishes a reply"`
	AutoApprove    *bool           `json:"auto_approve,omitempty" jsonschema:"claude bots: answer every permission prompt Yes (Claude Code's safety checks still ask in bypass mode), so the bot never waits on one; questions and plan approval still reach the human"`
	Workspace      *string         `json:"workspace,omitempty" jsonschema:"the herdr workspace its tab opens in, on its next start"`
}

type botEnvIn struct {
	Name   string `json:"name" jsonschema:"the bot's name"`
	Key    string `json:"key" jsonschema:"the variable's name"`
	Value  string `json:"value,omitempty" jsonschema:"its value (set_bot_env only)"`
	Secret bool   `json:"secret,omitempty" jsonschema:"store it encrypted (fnox's default provider) instead of plain"`
}

type botChangeOut struct {
	OK            bool   `json:"ok"`
	RestartNeeded bool   `json:"restart_needed" jsonschema:"the bot is running and reads this only at launch: restart it (a bot restarting itself runs mise run restart in its folder)"`
	Note          string `json:"note,omitempty"`
}

// botForCaller resolves a bot the caller's credential may reach, answering an
// unreachable one exactly like a missing one.
func botForCaller(req *mcp.CallToolRequest, name string) (*botRecord, Backend, error) {
	rec, err := getBot(name)
	if err != nil || callerFrom(req).requireHost(rec.Host) != nil {
		return nil, nil, fmt.Errorf("no bot named %q", name)
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		return nil, nil, err
	}
	return rec, b, nil
}

func getBotTool(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, getBotOut, error) {
	rec, b, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, getBotOut{}, err
	}
	dir := expandTildeOn(b, rec.Dir)
	env, _ := botEnvList(b, dir)
	shown := []botEnvVar{}
	for _, v := range env {
		if !strings.HasPrefix(v.Key, botOAuthKeyPrefix) {
			shown = append(shown, v)
		}
	}
	oauth := map[string]any{}
	for name, row := range listBotOAuth(rec.Name) {
		oauth[name] = map[string]any{"status": row.Status, "error": row.Error, "issuer": row.Issuer}
	}
	return nil, getBotOut{
		Bot:    summarizeBot(botStatus(b, rec, readClaudeSessions(b))),
		Config: rec,
		Folder: dir,
		Env:    shown,
		Skills: listSkillDir(b, filepath.Join(dir, ".claude", "skills")),
		OAuth:  oauth,
	}, nil
}

func updateBotTool(ctx context.Context, req *mcp.CallToolRequest, in updateBotIn) (*mcp.CallToolResult, botChangeOut, error) {
	rec, b, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, botChangeOut{}, err
	}
	next := *rec
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	if in.Runtime != nil {
		next.Runtime = strings.TrimSpace(*in.Runtime)
		if next.Runtime != botRuntimeClaude && next.Runtime != botRuntimeOmp {
			return nil, botChangeOut{}, fmt.Errorf("runtime must be omp or claude")
		}
	}
	set(&next.Dir, in.Dir)
	switched := next.Runtime != rec.Runtime
	if (switched || next.Dir != rec.Dir) && botRunning(b, rec) {
		return nil, botChangeOut{}, fmt.Errorf("%s is running: stop_bot it before changing its runtime or folder, then start_bot it", rec.Name)
	}
	if switched {
		// The model, permission mode and effort names are the runtime's own,
		// like the Bots view's runtime picker clears them.
		next.Model, next.PermissionMode = "", ""
		if next.Effort != "" && normalizeEffort(next.Runtime, next.Effort) == "" {
			next.Effort = ""
		}
	}
	set(&next.Model, in.Model)
	set(&next.Effort, in.Effort)
	set(&next.PermissionMode, in.PermissionMode)
	set(&next.Workspace, in.Workspace)
	set(&next.LaunchTask, in.LaunchTask)
	if in.MCP != nil {
		next.MCP = *in.MCP
	}
	if in.StrictMCP != nil {
		next.StrictMCP = *in.StrictMCP
	}
	if in.ExtraArgs != nil {
		next.ExtraArgs = *in.ExtraArgs
	}
	if in.KeepRunning != nil {
		next.KeepRunning = *in.KeepRunning
	}
	if in.Notify != nil {
		next.Notify = *in.Notify
	}
	if in.AutoApprove != nil {
		next.AutoApprove = *in.AutoApprove
	}
	if err := next.normalize(); err != nil {
		return nil, botChangeOut{}, err
	}
	if err := botCheckLaunchTask(b, &next); err != nil {
		return nil, botChangeOut{}, err
	}
	if err := updateBot(&next); err != nil {
		return nil, botChangeOut{}, err
	}
	note := ""
	if switched {
		// The other runtime's session is no conversation for this one.
		next.LastSessionID = ""
		if err := setBotSession(next.Name, ""); err != nil {
			return nil, botChangeOut{}, err
		}
		note = fmt.Sprintf("%s now runs %s; start_bot starts it on a new conversation", next.Name, next.Runtime)
	}
	if err := botMaterialize(b, &next); err != nil {
		return nil, botChangeOut{}, err
	}
	return nil, botChangeOut{OK: true, RestartNeeded: botRunning(b, &next), Note: note}, nil
}

func setBotEnvTool(ctx context.Context, req *mcp.CallToolRequest, in botEnvIn) (*mcp.CallToolResult, botChangeOut, error) {
	rec, b, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, botChangeOut{}, err
	}
	if strings.HasPrefix(in.Key, botOAuthKeyPrefix) {
		return nil, botChangeOut{}, fmt.Errorf("%s* variables are lasso's own OAuth credentials", botOAuthKeyPrefix)
	}
	dir := expandTildeOn(b, rec.Dir)
	if err := botEnvSet(b, dir, in.Key, in.Value, in.Secret); err != nil {
		return nil, botChangeOut{}, err
	}
	if err := botWriteTask(b, rec, dir); err != nil {
		return nil, botChangeOut{}, err
	}
	return nil, botChangeOut{OK: true, RestartNeeded: botRunning(b, rec)}, nil
}

func unsetBotEnvTool(ctx context.Context, req *mcp.CallToolRequest, in botEnvIn) (*mcp.CallToolResult, botChangeOut, error) {
	rec, b, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, botChangeOut{}, err
	}
	if strings.HasPrefix(in.Key, botOAuthKeyPrefix) {
		return nil, botChangeOut{}, fmt.Errorf("%s* variables are lasso's own OAuth credentials", botOAuthKeyPrefix)
	}
	dir := expandTildeOn(b, rec.Dir)
	if err := botEnvUnset(b, dir, in.Key); err != nil {
		return nil, botChangeOut{}, err
	}
	if err := botWriteTask(b, rec, dir); err != nil {
		return nil, botChangeOut{}, err
	}
	return nil, botChangeOut{OK: true, RestartNeeded: botRunning(b, rec)}, nil
}

type setBotAvatarIn struct {
	Name  string `json:"name" jsonschema:"the bot's name"`
	Path  string `json:"path,omitempty" jsonschema:"an image file on the bot's host (absolute, or ~/…): PNG, JPEG, WebP or GIF, at most 2 MB. Download or generate it to a file first."`
	Clear bool   `json:"clear,omitempty" jsonschema:"remove the picture, back to the first letter of its name"`
}

func setBotAvatarTool(ctx context.Context, req *mcp.CallToolRequest, in setBotAvatarIn) (*mcp.CallToolResult, botChangeOut, error) {
	rec, b, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, botChangeOut{}, err
	}
	if in.Clear {
		if err := clearBotAvatar(b, rec); err != nil {
			return nil, botChangeOut{}, err
		}
		return nil, botChangeOut{OK: true}, nil
	}
	path := expandTildeOn(b, strings.TrimSpace(in.Path))
	if !filepath.IsAbs(path) {
		return nil, botChangeOut{}, fmt.Errorf("path must be absolute or start with ~/")
	}
	fi, err := b.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() > botAvatarMax {
		return nil, botChangeOut{}, fmt.Errorf("%s is not an image file of at most %d MB", path, botAvatarMax>>20)
	}
	data, err := b.ReadFile(path)
	if err != nil {
		return nil, botChangeOut{}, err
	}
	if err := storeBotAvatar(b, rec, data); err != nil {
		return nil, botChangeOut{}, err
	}
	return nil, botChangeOut{OK: true, Note: "the new picture shows in lasso right away; no restart needed"}, nil
}

func listBotsTool(ctx context.Context, req *mcp.CallToolRequest, _ listBotsIn) (*mcp.CallToolResult, listBotsOut, error) {
	cs := callerFrom(req)
	list, err := listBots()
	if err != nil {
		return nil, listBotsOut{}, err
	}
	var mine []*botRecord
	for _, r := range list {
		if cs.requireHost(r.Host) == nil {
			mine = append(mine, r)
		}
	}
	out := listBotsOut{Bots: []botSummary{}}
	for _, v := range botStatuses(mine) {
		out.Bots = append(out.Bots, summarizeBot(v))
	}
	return nil, out, nil
}

func botLifecycleTool(req *mcp.CallToolRequest, in botNameIn, action string) (*mcp.CallToolResult, botStateOut, error) {
	rec, err := getBot(in.Name)
	if err != nil {
		return nil, botStateOut{}, fmt.Errorf("no bot named %q", in.Name)
	}
	if err := callerFrom(req).requireHost(rec.Host); err != nil {
		// Out of reach reads the same as absent.
		return nil, botStateOut{}, fmt.Errorf("no bot named %q", in.Name)
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		return nil, botStateOut{}, err
	}
	switch action {
	case "start":
		err = startBot(b, rec, in.Fresh)
	case "stop":
		err = stopBot(b, rec)
	case "restart":
		err = restartBot(b, rec, in.Fresh)
	}
	if err != nil && !errors.Is(err, errBotRunning) {
		return nil, botStateOut{}, err
	}
	if latest, e := getBot(rec.Name); e == nil {
		rec = latest
	}
	return nil, botStateOut{Bot: summarizeBot(botStatus(b, rec, readClaudeSessions(b)))}, nil
}

package main

// Bots over MCP: listing them and switching them on and off. Creating a bot and
// editing what it runs with (its MCP servers, env, secrets, CLAUDE.md) stays in
// lasso's Bots view, behind a human, the way enabling a plugin does: those
// settings decide what the bot can reach.

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type listBotsIn struct{}

type botSummary struct {
	Name        string   `json:"name"`
	Host        string   `json:"host"`
	Dir         string   `json:"dir"`
	Workspace   string   `json:"workspace"`
	Model       string   `json:"model,omitempty"`
	State       string   `json:"state" jsonschema:"stopped, starting, idle, working or blocked"`
	WaitingFor  string   `json:"waiting_for,omitempty" jsonschema:"why a blocked bot is waiting, in Claude Code's words"`
	PaneID      string   `json:"pane_id,omitempty" jsonschema:"the bot's herdr pane; message it with send_agent (to = the bot's name)"`
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
		Name: v.Name, Host: v.Host, Dir: v.Dir, Workspace: v.Workspace, Model: v.Model,
		State: v.State, WaitingFor: v.WaitingFor, PaneID: v.PaneID, KeepRunning: v.KeepRunning,
		Channels: v.channels(), LastText: v.LastText, LastAt: v.LastAt,
	}
}

func registerBotTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_bots",
		Description: "List lasso's bots: long-lived Claude Code sessions, each with its own folder, MCP servers and channels, that lasso launches in herdr and brings back after a restart. Each entry has its live state (stopped/starting/idle/working/blocked) and the newest line of its conversation. Only bots on hosts your credential reaches are listed. Talk to a running bot with send_agent, its name as `to`.",
	}, listBotsTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "start_bot",
		Description: "Start a stopped bot. It resumes its last conversation unless fresh is true.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botStateOut, error) {
		return botLifecycleTool(req, in, "start")
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "stop_bot",
		Description: "Stop a bot: close its herdr pane and keep it stopped (keep-running will not relaunch it) until it is started again. Its conversation stays on disk.",
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

package main

// The lasso channel tools on /mcp: get_bot_channel, enable_bot_webhook,
// rotate_bot_webhook_key and disable_bot_webhook, everything the Connections
// view's Lasso channel panel does (botpush.go). Like the other bot tools they
// resolve the bot through botForCaller, so a bot out of the caller's reach
// reads as missing.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const botPushHelp = "Every bot has a lasso channel: it is how messages reach the bot without being asked (its channel MCP servers, jobs, Talk, the phone app), delivered as a Claude Code channel event to a claude bot and through lasso's omp plugin to an omp bot. It is always on. Its webhook is the optional door for anything else: when on, POST a message to <lasso>/hooks/bots/<bot> with the webhook key as a Bearer token (or ?key=); a plain-text body is the message, or JSON {content, source?, id?, meta?}. Whoever holds the key can send the bot messages; the bot is told they come from outside, not from its human."

type botPushOut struct {
	Channel botPushView `json:"channel" jsonschema:"the runtime lasso delivers through (claude or omp); whether lasso can deliver to the bot at all, with the reason when not; sources: everything that messages the bot through the channel now (its channel MCP servers, jobs, Talk, the phone app), each on or off; and webhook: whether it is on, its path (after lasso's address) and key"`
}

func botPushToolOut(rec *botRecord) (*mcp.CallToolResult, botPushOut, error) {
	v, err := botPushViewOf(rec)
	if err != nil {
		return nil, botPushOut{}, err
	}
	return nil, botPushOut{Channel: v}, nil
}

func registerBotChannelTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_bot_channel",
		Description: "Read a bot's lasso channel: what sends the bot messages now, how lasso delivers them, and whether its webhook is on, with the path and key. " + botPushHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botPushOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botPushOut{}, err
		}
		return botPushToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "enable_bot_webhook",
		Description: "Turn the webhook of a bot's lasso channel on, with a new key (no change when it is already on). Takes effect at once, no restart. " + botPushHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botPushOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botPushOut{}, err
		}
		if err := botPushEnable(rec); err != nil {
			return nil, botPushOut{}, err
		}
		return botPushToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rotate_bot_webhook_key",
		Description: "Give the webhook of a bot's lasso channel a new key: the old one stops working at once, so every sender using it must be given the new one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botPushOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botPushOut{}, err
		}
		if err := botPushRotate(rec); err != nil {
			return nil, botPushOut{}, err
		}
		return botPushToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "disable_bot_webhook",
		Description: "Turn the webhook of a bot's lasso channel off: its key stops working and is forgotten. The channel itself stays on: jobs, Talk, the phone app and the bot's own channel servers are not affected.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botPushOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botPushOut{}, err
		}
		if err := setBotPushKey(rec.Name, ""); err != nil {
			return nil, botPushOut{}, err
		}
		return botPushToolOut(rec)
	})
}

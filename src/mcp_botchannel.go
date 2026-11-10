package main

// The lasso channel tools on /mcp: get_bot_channel, enable_bot_channel,
// rotate_bot_channel_key and disable_bot_channel, everything the Connections
// view's Lasso channel panel does (botpush.go). Like the other bot tools they
// resolve the bot through botForCaller, so a bot out of the caller's reach
// reads as missing.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const botPushHelp = "The lasso channel is the one way to push a message into a bot whatever it runs: POST the message to <lasso>/hooks/bots/<bot> with the channel key as a Bearer token (or ?key=); a plain-text body is the message, or JSON {content, source?, id?, meta?}. Lasso delivers it as a Claude Code channel event to a claude bot and through its omp plugin to an omp bot. Whoever holds the key can send the bot messages; the bot is told they come from outside, not from its human."

type botPushOut struct {
	Channel botPushView `json:"channel" jsonschema:"whether the webhook is on, its path (after lasso's address) and key, the runtime lasso delivers through (claude or omp), and whether lasso can deliver to the bot at all, with the reason when not"`
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
		Description: "Read a bot's lasso channel: whether its webhook is on, the path and key, and how lasso delivers. " + botPushHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botPushOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botPushOut{}, err
		}
		return botPushToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "enable_bot_channel",
		Description: "Turn a bot's lasso channel webhook on, with a new key (no change when it is already on). Takes effect at once, no restart. " + botPushHelp,
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
		Name:        "rotate_bot_channel_key",
		Description: "Give a bot's lasso channel webhook a new key: the old one stops working at once, so every sender using it must be given the new one.",
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
		Name:        "disable_bot_channel",
		Description: "Turn a bot's lasso channel webhook off: its key stops working and is forgotten. Jobs, Talk and the bot's own channel servers are not affected.",
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

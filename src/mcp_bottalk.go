package main

// The Talk tools on /mcp: get_bot_talk, enable_bot_talk, update_bot_talk,
// rotate_bot_talk_key and disable_bot_talk, everything the Talk tab does
// (bottalk.go). Like the other bot tools they resolve the bot through
// botForCaller, so a bot out of the caller's reach reads as missing.

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const botTalkHelp = "Talk is a bot's OpenAI-compatible door (Chat Completions at <lasso>/talk/<bot>/v1, Bearer key, model = the bot's name) for a chat app on someone's phone, such as Conduit: their messages reach the bot through lasso's channel and its replies come back to the app. Claude bots only. Whoever holds the key can ask the bot for anything its instructions allow."

type botTalkOut struct {
	Talk botTalkView `json:"talk" jsonschema:"whether Talk is on, its path (after lasso's address), key, model name, activity mode, and state: ready, waiting (the bot is starting; messages wait), restart (its channel predates Talk: restart the bot) or unavailable, with the reason"`
}

type updateBotTalkIn struct {
	Name     string `json:"name" jsonschema:"the bot's name"`
	Activity string `json:"activity" jsonschema:"what the chat app shows while the bot works: plain (only the kind of each step, the default) or detailed (each step's subject too: file names, commands)"`
}

func botTalkToolOut(rec *botRecord) (*mcp.CallToolResult, botTalkOut, error) {
	v, err := botTalkViewOf(rec, time.Now())
	if err != nil {
		return nil, botTalkOut{}, err
	}
	return nil, botTalkOut{Talk: v}, nil
}

func registerBotTalkTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_bot_talk",
		Description: "Read a bot's Talk settings: on or off, its path and key, the model name a chat app uses, and its activity mode. " + botTalkHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botTalkOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botTalkOut{}, err
		}
		return botTalkToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "enable_bot_talk",
		Description: "Turn Talk on for a bot, with a new key (no change when it is already on). A running bot whose channel predates Talk needs a restart before it takes messages. " + botTalkHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botTalkOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botTalkOut{}, err
		}
		if err := botTalkEnable(rec); err != nil {
			return nil, botTalkOut{}, err
		}
		return botTalkToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_bot_talk",
		Description: "Change what a bot's Talk shows while the bot works: plain or detailed activity.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateBotTalkIn) (*mcp.CallToolResult, botTalkOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botTalkOut{}, err
		}
		if err := setBotTalkActivity(rec.Name, in.Activity); err != nil {
			return nil, botTalkOut{}, err
		}
		return botTalkToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "rotate_bot_talk_key",
		Description: "Give a bot's Talk a new key: the old one stops working at once, so every chat app using it must be given the new one.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botTalkOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botTalkOut{}, err
		}
		if err := botTalkRotate(rec); err != nil {
			return nil, botTalkOut{}, err
		}
		return botTalkToolOut(rec)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "disable_bot_talk",
		Description: "Turn Talk off for a bot: its key stops working and is forgotten.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, botTalkOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, botTalkOut{}, err
		}
		if err := setBotTalkKey(rec.Name, ""); err != nil {
			return nil, botTalkOut{}, err
		}
		return botTalkToolOut(rec)
	})
}

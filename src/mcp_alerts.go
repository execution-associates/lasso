package main

// Bot liveness alerts over MCP (bothealth.go, alertchannels.go): holding a bot
// down on purpose, the alert channels, and what was sent. A bot's own alert
// settings are update_bot's `alerts`; the defaults are update_settings'.

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type holdBotIn struct {
	Name    string `json:"name" jsonschema:"the bot's name"`
	Minutes int    `json:"minutes,omitempty" jsonschema:"hold for this many minutes"`
	Until   string `json:"until,omitempty" jsonschema:"hold until this RFC 3339 time; with neither minutes nor until, the hold lasts until release_bot"`
	Reason  string `json:"reason,omitempty" jsonschema:"why, shown in the Bots view"`
}

type holdBotOut struct {
	OK   bool        `json:"ok"`
	Hold botHoldInfo `json:"hold"`
}

type alertChannelIDIn struct {
	ID int64 `json:"id" jsonschema:"the channel's id, from list_alert_channels"`
}

type alertChannelsOut struct {
	Channels []*alertChannel `json:"channels"`
	Kinds    []alertKind     `json:"kinds" jsonschema:"each kind's fields: config fields are plain, secret fields are write-only"`
}

type createAlertChannelIn struct {
	Name    string            `json:"name,omitempty" jsonschema:"a label for the channel"`
	Kind    string            `json:"kind" jsonschema:"webhook, slack, discord, telegram, email or heartbeat"`
	Enabled *bool             `json:"enabled,omitempty"`
	Config  map[string]string `json:"config,omitempty" jsonschema:"the kind's plain fields (list_alert_channels lists them): telegram chat_id/thread_id; email host/port/security/username/from/to; heartbeat bot/every"`
	Secrets map[string]string `json:"secrets,omitempty" jsonschema:"the kind's secret fields, write-only: webhook url/signing_secret/authorization; slack and discord webhook_url; telegram bot_token; email password; heartbeat url"`
}

type updateAlertChannelIn struct {
	ID      int64             `json:"id" jsonschema:"the channel's id"`
	Name    *string           `json:"name,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
	Config  map[string]string `json:"config,omitempty" jsonschema:"the COMPLETE plain fields (they replace the current ones); omit to keep them"`
	Secrets map[string]string `json:"secrets,omitempty" jsonschema:"only the secrets that change: a key left out keeps its value, an empty string clears it"`
}

type alertChannelOut struct {
	Channel *alertChannel `json:"channel"`
}

type testAlertOut struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type listBotAlertsIn struct {
	Bot string `json:"bot,omitempty" jsonschema:"one bot's name; omit for every bot"`
}

func registerAlertTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "hold_bot",
		Description: "Hold a bot down on purpose: while held, lasso does not relaunch it (keep-running) and its being gone alerts nobody, but a bot that is up and not hearing its channel still alerts. Lasts for minutes, until a time, or until release_bot.",
	}, holdBotTool)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "release_bot",
		Description: "End a bot's hold (hold_bot): keep-running and alerts apply again from the next check.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in botNameIn) (*mcp.CallToolResult, holdBotOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, holdBotOut{}, err
		}
		if err := setBotHold(rec.Name, 0, ""); err != nil {
			return nil, holdBotOut{}, err
		}
		return nil, holdBotOut{OK: true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_bot_alerts",
		Description: "Bot liveness: each bot's health as lasso last saw it, whether it is alerted down and since when, its hold, and the most recent alert deliveries (sent or failed, per channel).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listBotAlertsIn) (*mcp.CallToolResult, map[string]any, error) {
		cs := callerFrom(req)
		if in.Bot != "" {
			if _, _, err := botForCaller(req, in.Bot); err != nil {
				return nil, nil, err
			}
		}
		return nil, alertsOverview(in.Bot, func(host string) bool { return cs.requireHost(host) == nil }), nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_alert_channels",
		Description: "List the channels bot alerts go to (webhook, slack, discord, telegram, email, heartbeat), with each kind's fields. Secrets are never shown: secrets_set names the ones that are set.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, alertChannelsOut, error) {
		if err := requireSettingsReach(req, ""); err != nil {
			return nil, alertChannelsOut{}, err
		}
		list, err := listAlertChannels(false)
		if err != nil {
			return nil, alertChannelsOut{}, err
		}
		if list == nil {
			list = []*alertChannel{}
		}
		return nil, alertChannelsOut{Channels: list, Kinds: alertKinds}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_alert_channel",
		Description: "Add a channel for bot alerts. A webhook POSTs signed JSON (X-Lasso-Signature: sha256=HMAC-SHA256(signing_secret, X-Lasso-Timestamp + \".\" + body)); a heartbeat is pinged while its one bot is up (a Monicron or healthchecks ping URL), so its monitor goes down with the bot or with lasso. Route a non-heartbeat channel with update_settings alerts.channels (the default) or update_bot alerts. Try it with test_alert_channel.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createAlertChannelIn) (*mcp.CallToolResult, alertChannelOut, error) {
		if err := requireSettingsReach(req, ""); err != nil {
			return nil, alertChannelOut{}, err
		}
		name := in.Name
		c, err := createAlertChannel(alertChannelInput{Name: &name, Kind: in.Kind, Enabled: in.Enabled, Config: in.Config, Secrets: in.Secrets})
		if err != nil {
			return nil, alertChannelOut{}, err
		}
		return nil, alertChannelOut{Channel: c}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_alert_channel",
		Description: "Change an alert channel. Secrets you leave out are kept; an empty string clears one. The kind cannot change.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateAlertChannelIn) (*mcp.CallToolResult, alertChannelOut, error) {
		if err := requireSettingsReach(req, ""); err != nil {
			return nil, alertChannelOut{}, err
		}
		c, err := updateAlertChannel(in.ID, alertChannelInput{Name: in.Name, Enabled: in.Enabled, Config: in.Config, Secrets: in.Secrets})
		if err != nil {
			return nil, alertChannelOut{}, err
		}
		return nil, alertChannelOut{Channel: c}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_alert_channel",
		Description: "Delete an alert channel, and drop it from the default channels and every bot's custom list.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in alertChannelIDIn) (*mcp.CallToolResult, testAlertOut, error) {
		if err := requireSettingsReach(req, ""); err != nil {
			return nil, testAlertOut{}, err
		}
		if _, err := getAlertChannel(in.ID); err != nil {
			return nil, testAlertOut{}, err
		}
		if err := deleteAlertChannel(in.ID); err != nil {
			return nil, testAlertOut{}, err
		}
		return nil, testAlertOut{OK: true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "test_alert_channel",
		Description: "Send a test alert through a channel now and say whether it went (a heartbeat test is one ping). The error never contains a secret.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in alertChannelIDIn) (*mcp.CallToolResult, testAlertOut, error) {
		if err := requireSettingsReach(req, ""); err != nil {
			return nil, testAlertOut{}, err
		}
		out, err := runAlertChannelTest(in.ID)
		if err != nil {
			return nil, testAlertOut{}, err
		}
		msg, _ := out["error"].(string)
		return nil, testAlertOut{OK: out["ok"] == true, Error: msg}, nil
	})
}

func holdBotTool(ctx context.Context, req *mcp.CallToolRequest, in holdBotIn) (*mcp.CallToolResult, holdBotOut, error) {
	rec, _, err := botForCaller(req, in.Name)
	if err != nil {
		return nil, holdBotOut{}, err
	}
	until, reason, err := botHoldInput{Minutes: in.Minutes, Until: in.Until, Reason: in.Reason}.resolve(time.Now())
	if err != nil {
		return nil, holdBotOut{}, err
	}
	if err := setBotHold(rec.Name, until, reason); err != nil {
		return nil, holdBotOut{}, fmt.Errorf("hold %s: %w", rec.Name, err)
	}
	rec.HoldUntil, rec.HoldReason = until, reason
	return nil, holdBotOut{OK: true, Hold: botHoldView(rec, time.Now())}, nil
}

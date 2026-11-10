package main

// The phone app's people over MCP (clientapi_admin.go, docs/design/client-api.md):
// everything /api/app-users does, and the two tools an omp bot answers a person
// with beyond its text. The user tools need a fleet-wide credential: app users
// and their devices are not any one host's.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const appUsersHelp = "App users are the people who use lasso's phone app to talk to bots: each has their own paired devices and reaches only the bots granted to them. A grant's owner flag says the person is that bot's owner, its human; everyone else the bot treats as an outside person."

func appAdmin(req *mcp.CallToolRequest) (string, error) {
	c := callerFrom(req)
	if !c.Fleet {
		return "", errors.New("app users are managed by a credential with fleet reach")
	}
	if c.ClientID != "" {
		return "mcp:" + c.ClientID, nil
	}
	return "local", nil
}

type appUserIDIn struct {
	ID string `json:"id" jsonschema:"the app user's id, from list_app_users"`
}

type listAppUsersIn struct {
	Bot string `json:"bot,omitempty" jsonschema:"only the users granted this bot"`
}

type appUsersOut struct {
	Users []appUserView `json:"users"`
}

type appUserOut struct {
	User appUserView `json:"user"`
}

type createAppUserIn struct {
	Name  string `json:"name" jsonschema:"the person's display name, 1-64 characters; bots see it"`
	Email string `json:"email,omitempty" jsonschema:"optional, for the owner's records"`
}

type updateAppUserIn struct {
	ID       string  `json:"id" jsonschema:"the app user's id"`
	Name     *string `json:"name,omitempty"`
	Email    *string `json:"email,omitempty"`
	Disabled *bool   `json:"disabled,omitempty" jsonschema:"true turns the person off: every request and open stream of theirs stops at once; false turns them back on"`
}

type appGrantIn struct {
	ID    string `json:"id" jsonschema:"the app user's id"`
	Bot   string `json:"bot" jsonschema:"the bot's name"`
	Owner bool   `json:"owner,omitempty" jsonschema:"true makes this person the bot's owner (its human); the bot then treats their words as its human's. Default false: an outside person"`
}

type appPairingIn struct {
	ID      string `json:"id" jsonschema:"the app user's id"`
	BaseURL string `json:"base_url,omitempty" jsonschema:"the public origin the phone reaches lasso at, such as https://lasso.example.com; the link is built on it. Without it no link is made, only the code"`
}

type appDeviceIn struct {
	ID       string `json:"id" jsonschema:"the app user's id"`
	DeviceID string `json:"device_id" jsonschema:"the device's id, from list_app_users"`
}

type okOut struct {
	OK bool `json:"ok"`
}

func registerAppUserTools(s *mcp.Server) {
	userOut := func(id string) (*mcp.CallToolResult, appUserOut, error) {
		u, err := getAppUser(id)
		if err != nil {
			return nil, appUserOut{}, fmt.Errorf("no app user %q", id)
		}
		v, err := appUserViewOf(u)
		return nil, appUserOut{User: v}, err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_app_users",
		Description: "List the people who use lasso's phone app, each with the bots granted to them (and whether they own it) and their paired devices. " + appUsersHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listAppUsersIn) (*mcp.CallToolResult, appUsersOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUsersOut{}, err
		}
		vs, err := appUserViews(in.Bot)
		return nil, appUsersOut{Users: vs}, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_app_user",
		Description: "Add a person who will use lasso's phone app. They can reach no bot until set_app_user_grant, and no device is paired until create_app_pairing. " + appUsersHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in createAppUserIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		u, err := createAppUser(in.Name, in.Email, time.Now())
		if err != nil {
			return nil, appUserOut{}, err
		}
		return userOut(u.ID)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_app_user",
		Description: "Rename an app user, change their email, or disable or enable them. Disabling takes effect on their next request and ends their open streams.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateAppUserIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		if _, err := updateAppUser(in.ID, appUserPatch{Name: in.Name, Email: in.Email, Disabled: in.Disabled}, time.Now()); err != nil {
			return nil, appUserOut{}, err
		}
		return userOut(in.ID)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "delete_app_user",
		Description: "Delete an app user: their devices stop working and their conversations, messages and uploaded files are removed.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appUserIDIn) (*mcp.CallToolResult, okOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, okOut{}, err
		}
		atts, err := deleteAppUser(in.ID)
		if err != nil {
			return nil, okOut{}, err
		}
		clientRemoveFiles(atts)
		return nil, okOut{OK: true}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_app_user_grant",
		Description: "Let an app user reach a bot (or change whether they own it). Only granted bots are visible to them. " + appUsersHelp,
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appGrantIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		if _, _, err := botForCaller(req, in.Bot); err != nil {
			return nil, appUserOut{}, err
		}
		if _, err := setAppGrant(in.ID, in.Bot, in.Owner, time.Now()); err != nil {
			return nil, appUserOut{}, err
		}
		return userOut(in.ID)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "revoke_app_user_grant",
		Description: "Take an app user's access to a bot away. It takes effect on their next request and ends their open streams to it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appGrantIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		if err := deleteAppGrant(in.ID, in.Bot); err != nil {
			return nil, appUserOut{}, err
		}
		return userOut(in.ID)
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_app_pairing",
		Description: "Make a one-time pairing for an app user: a code (valid 10 minutes, usable once) the phone app exchanges for its own device token, and with base_url a link that opens the app. The code is shown only in this answer; give it to the person yourself, never post it anywhere shared.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appPairingIn) (*mcp.CallToolResult, appPairingOut, error) {
		by, err := appAdmin(req)
		if err != nil {
			return nil, appPairingOut{}, err
		}
		base := ""
		if in.BaseURL != "" {
			if base, err = pairingBase(in.BaseURL); err != nil {
				return nil, appPairingOut{}, err
			}
		}
		out, err := mintAppPairingOut(in.ID, by, base, time.Now())
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "revoke_app_device",
		Description: "Revoke one paired device of an app user: its token stops working on the next request and its open streams end.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in appDeviceIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		if err := revokeAppDevice(in.ID, in.DeviceID, time.Now()); err != nil {
			return nil, appUserOut{}, err
		}
		return userOut(in.ID)
	})

	// The two tools an omp bot has beyond its answer text (a claude bot has
	// them on its channel).
	type clientReplyIn struct {
		Name           string   `json:"name" jsonschema:"your own name, the bot's"`
		ConversationID string   `json:"conversation_id" jsonschema:"the conversation id from the header of the person's message"`
		Text           string   `json:"text,omitempty" jsonschema:"a caption; your answer text is delivered on its own, do not repeat it"`
		Done           *bool    `json:"done,omitempty" jsonschema:"false for an interim note; true (the default) closes your turn"`
		Attachments    []string `json:"attachments,omitempty" jsonschema:"paths of files inside your folder to send (images, PDF, text, audio, video, zip; at most 4, 20 MB each)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_reply",
		Description: "Send a message with files to a person using lasso's phone app, in the conversation named in their message's header. The app shows images inline. Lasso reads the files from the bot's folder once, refuses anything outside it and its credentials, and sends a copy. Your normal answer text already reaches the person; use this for attachments. It works only for the conversation you are answering at that moment.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in clientReplyIn) (*mcp.CallToolResult, okOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, okOut{}, err
		}
		err = mcpClientReply(rec.Name, in.ConversationID, in.Text, in.Done == nil || *in.Done, in.Attachments)
		return nil, okOut{OK: err == nil}, err
	})
	type clientEndCallIn struct {
		Name      string `json:"name" jsonschema:"your own name, the bot's"`
		CallID    string `json:"call_id" jsonschema:"the call id from the header of the person's voice message"`
		LastWords string `json:"last_words,omitempty" jsonschema:"a short goodbye the app speaks before it hangs up"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_end_call",
		Description: "Hang up a voice call with a person using lasso's phone app: the app speaks last_words, if any, and ends the call. Use it when they say goodbye or the conversation is finished, never in the middle of an answer.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in clientEndCallIn) (*mcp.CallToolResult, okOut, error) {
		rec, _, err := botForCaller(req, in.Name)
		if err != nil {
			return nil, okOut{}, err
		}
		err = mcpClientEndCall(rec.Name, in.CallID, in.LastWords)
		return nil, okOut{OK: err == nil}, err
	})
}

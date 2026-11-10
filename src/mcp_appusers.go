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

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const appUsersHelp = "App users are the people who use lasso's phone app to talk to bots: each has their own paired devices and reaches only the bots granted to them. A grant's owner flag says the person is that bot's owner, its human; everyone else the bot treats as an outside person."

// appUserAdminTools are the tools that manage app users and their devices.
// Granting ownership, pairing a device and reading the user list are the
// owner's, so an anonymous /mcp caller never has them (see appAdmin).
var appUserAdminTools = map[string]bool{
	"list_app_users": true, "create_app_user": true, "update_app_user": true, "delete_app_user": true,
	"set_app_user_grant": true, "revoke_app_user_grant": true, "create_app_pairing": true, "revoke_app_device": true,
}

// errAppAdminCredential is why an anonymous caller is refused.
var errAppAdminCredential = errors.New("the app-user tools need a real MCP credential (a lasso token with fleet reach, from MCP_OAUTH); an anonymous /mcp caller, which is what a local bot is, cannot use them. Manage app users through the owner's /api/app-users instead")

// appAdmin says who is managing app users, or refuses. /mcp is open to any
// local process when MCP_OAUTH is unset, and a bot on lasso's machine reaches
// it that way, so a caller with no credential (no ClientID) is refused: a bot
// steered by an outside person must not be able to make that person an owner or
// pair a device for the owner's own user.
func appAdmin(req *mcp.CallToolRequest) (string, error) {
	c := callerFrom(req)
	if c.ClientID == "" {
		return "", errAppAdminCredential
	}
	if !c.Fleet {
		return "", errors.New("app users are managed by a credential with fleet reach")
	}
	return "mcp:" + c.ClientID, nil
}

// hideAppUserTools drops the app-user tools from the tool list an anonymous
// caller sees, so a bot's own MCP view does not advertise what it cannot use.
func hideAppUserTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if err != nil || method != "tools/list" {
			return res, err
		}
		if ex := req.GetExtra(); ex != nil && ex.TokenInfo != nil && ex.TokenInfo.UserID != "" {
			return res, err
		}
		if lr, ok := res.(*mcp.ListToolsResult); ok {
			kept := make([]*mcp.Tool, 0, len(lr.Tools))
			for _, t := range lr.Tools {
				if !appUserAdminTools[t.Name] {
					kept = append(kept, t)
				}
			}
			lr.Tools = kept
		}
		return res, err
	}
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
	// QuotaMB and QuotaFiles override the stored-upload limits for this person;
	// null clears the override. Their schema is written out in updateAppUserSchema
	// (a Go pointer cannot tell absent from null).
	QuotaMB    optInt `json:"quota_mb"`
	QuotaFiles optInt `json:"quota_files"`
}

// updateAppUserSchema is update_app_user's input schema, spelled out so quota_mb
// and quota_files can be null (clear the override) as well as absent.
func updateAppUserSchema() *jsonschema.Schema {
	quota := func(desc string, max int) *jsonschema.Schema {
		one := float64(1)
		m := float64(max)
		return &jsonschema.Schema{Types: []string{"integer", "null"}, Minimum: &one, Maximum: &m, Description: desc}
	}
	return &jsonschema.Schema{
		Type:     "object",
		Required: []string{"id"},
		Properties: map[string]*jsonschema.Schema{
			"id":          {Type: "string", Description: "the app user's id"},
			"name":        {Type: "string"},
			"email":       {Type: "string"},
			"disabled":    {Type: "boolean", Description: "true turns the person off: every request and open stream of theirs stops at once; false turns them back on"},
			"quota_mb":    quota("this person's stored-upload limit in MB, replacing the default (500); null clears the override", clientQuotaMBMax),
			"quota_files": quota("this person's stored-upload limit in files, replacing the default (3000); null clears the override", clientQuotaFilesMax),
		},
	}
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
		Description: "Rename an app user, change their email, disable or enable them, or override their storage quota (quota_mb, quota_files; null clears the override). Disabling takes effect on their next request and ends their open streams. The result shows their storage use and the file retention they chose.",
		InputSchema: updateAppUserSchema(),
	}, func(ctx context.Context, req *mcp.CallToolRequest, in updateAppUserIn) (*mcp.CallToolResult, appUserOut, error) {
		if _, err := appAdmin(req); err != nil {
			return nil, appUserOut{}, err
		}
		if _, err := updateAppUser(in.ID, appUserPatch{Name: in.Name, Email: in.Email, Disabled: in.Disabled, QuotaMB: in.QuotaMB, QuotaFiles: in.QuotaFiles}, time.Now()); err != nil {
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
		Attachments    []string `json:"attachments,omitempty" jsonschema:"paths of files in your outbox, .lasso/client/outbox/ in your folder, to send (images, PDF, text, audio, video, zip; at most 4, 20 MB each)"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "client_reply",
		Description: "Send a message with files to a person using lasso's phone app, in the conversation named in their message's header. The app shows images inline. Attach only files in your outbox, .lasso/client/outbox/ in your folder (copy or write the file there first): lasso reads from nowhere else, sends a copy and removes the file from the outbox once it is sent. Your normal answer text already reaches the person; use this for attachments. It works only for the conversation you are answering at that moment: you cannot start one with a person who has not written to you.",
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

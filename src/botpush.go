package main

// The lasso channel's webhook: POST /hooks/bots/<bot>, the one harness-neutral
// way to push a message into a bot. Anything that can send a POST can use it:
// the body is the message, queued as a bot_events row of kind "push"
// (enqueueBotPush), and the bot's runtime decides how it arrives: lasso's
// channel for Claude Code (botchannel.go), lasso's bridge for omp
// (botOmpDeliver).
//
// Two credentials open it, and they are different on purpose:
//
//   - The bot's channel key (bots.push_key, "lc-…"): the one a human hands to
//     an outside sender. Off until turned on, shown in the Connections view,
//     rotatable. It can only add a message; it reads nothing.
//   - The bot's channel token: lasso's own, which it gives the bot's channel
//     servers on an omp bot (botOmpMCPJSON) so they can deliver without a
//     human turning the webhook on. It never leaves the bot's 0600 mcp.json.
//
// A push that came in on the key is marked (botPushViaKey) so the bot is told
// it is from outside; a wrong key, a webhook that is off and a missing bot all
// read as not found.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// botPushMetaMax bounds how many attributes a push carries;
	// botPushValueMax is how much of each one a bot is shown.
	botPushMetaMax  = 32
	botPushValueMax = 2000
	// botPushViaKey is the meta entry lasso alone writes on a push that came
	// in on the channel key. Its name is not a valid attribute name, so no
	// sender can set it.
	botPushViaKey = "_via"
)

func newBotPushKey() string { return "lc-" + botRandomKey(32) }

// botPushKey reads the bot's channel key ("" = the webhook is off).
func botPushKey(name string) (string, error) {
	var key string
	err := db.QueryRow(`SELECT push_key FROM bots WHERE name = ?`, name).Scan(&key)
	return key, err
}

func setBotPushKey(name, key string) error {
	_, err := db.Exec(`UPDATE bots SET push_key = ? WHERE name = ?`, key, name)
	return err
}

// serveBotPush is POST /hooks/bots/<bot> (serveBotHook has read the key). The
// body is the message as plain text, or JSON {content, source?, id?, meta?}
// when the request says application/json: source names the sender, id is its
// own id for the message (enqueueBotPush drops a copy of one still
// undelivered), and meta is attributes the bot is shown.
func serveBotPush(w http.ResponseWriter, r *http.Request, bot, key string) {
	var pushKey, token string
	if err := db.QueryRow(`SELECT push_key, channel_token FROM bots WHERE name = ?`, bot).Scan(&pushKey, &token); err != nil {
		http.NotFound(w, r)
		return
	}
	viaKey := tokenEqual(key, pushKey)
	if !viaKey && !tokenEqual(key, token) {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, botHookBodyMax+8<<10+1))
	if err != nil {
		http.Error(w, "could not read the body", http.StatusBadRequest)
		return
	}
	var in struct {
		Content string            `json:"content"`
		Source  string            `json:"source,omitempty"`
		ID      string            `json:"id,omitempty"`
		Meta    map[string]string `json:"meta,omitempty"`
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/json" {
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "the body is not the JSON object {content, source?, id?, meta?}", http.StatusBadRequest)
			return
		}
	} else {
		in.Content = string(body)
	}
	in.Content = strings.TrimSpace(in.Content)
	if in.Content == "" {
		http.Error(w, "the message is empty", http.StatusBadRequest)
		return
	}
	if !utf8.ValidString(in.Content) {
		http.Error(w, "the message must be text (UTF-8)", http.StatusUnsupportedMediaType)
		return
	}
	if len(in.Content) > botHookBodyMax {
		http.Error(w, fmt.Sprintf("the message is over %d KB", botHookBodyMax>>10), http.StatusRequestEntityTooLarge)
		return
	}
	in.Source = clientMetaClean(strings.TrimSpace(in.Source), 64)
	if in.Source == "" {
		in.Source = "webhook"
	}
	if len(in.ID) > 200 || len(in.Meta) > botPushMetaMax {
		http.Error(w, "id or meta is too long", http.StatusBadRequest)
		return
	}
	for k := range in.Meta {
		if !botChannelMetaKeyRE.MatchString(k) {
			http.Error(w, fmt.Sprintf("meta %q: names are a-z, 0-9 and _, starting with a letter", k), http.StatusBadRequest)
			return
		}
	}
	if viaKey {
		if in.Meta == nil {
			in.Meta = map[string]string{}
		}
		in.Meta[botPushViaKey] = "webhook"
	}
	id, status, err := enqueueBotPush(bot, in.Content, in.Source, in.ID, in.Meta, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": status != "dropped", "event_id": id, "status": status})
}

// --- /api/bots/<name>/channel ---------------------------------------------------

// botPushView is the lasso channel as the Connections view and the MCP tools
// show it.
type botPushView struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
	Key     string `json:"key,omitempty"`
	// Runtime says how lasso delivers: claude (a Claude Code channel event)
	// or omp (lasso's omp plugin).
	Runtime string `json:"runtime"`
	// Available is false when lasso cannot deliver to this bot at all.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

func botPushViewOf(rec *botRecord) (botPushView, error) {
	key, err := botPushKey(rec.Name)
	if err != nil {
		return botPushView{}, err
	}
	v := botPushView{Enabled: key != "", Path: "/hooks/bots/" + rec.Name, Key: key, Runtime: botRuntimeClaude}
	if rec.isOmp() {
		v.Runtime = botRuntimeOmp
	}
	v.Available, v.Reason = botChannelOffered(rec)
	return v, nil
}

// botPushEnable turns the webhook on with a new key; already on, it changes
// nothing.
func botPushEnable(rec *botRecord) error {
	if ok, why := botChannelOffered(rec); !ok {
		return errors.New("lasso cannot deliver to this bot: " + why)
	}
	key, err := botPushKey(rec.Name)
	if err != nil || key != "" {
		return err
	}
	return setBotPushKey(rec.Name, newBotPushKey())
}

// botPushRotate gives a bot whose webhook is on a new key: the old one stops
// working at once.
func botPushRotate(rec *botRecord) error {
	key, err := botPushKey(rec.Name)
	if err != nil {
		return err
	}
	if key == "" {
		return errors.New("the channel's webhook is off for this bot")
	}
	return setBotPushKey(rec.Name, newBotPushKey())
}

// serveBotPushSettings is the Connections view's Lasso channel panel:
//
//	GET  channel            the view
//	POST channel/enable     on, with a new key
//	POST channel/rotate     a new key
//	POST channel/disable    off; the key is forgotten
func serveBotPushSettings(w http.ResponseWriter, r *http.Request, rec *botRecord, rest string) {
	if (r.Method == http.MethodGet) != (rest == "") {
		http.Error(w, "GET channel, or POST channel/enable, channel/rotate or channel/disable", http.StatusMethodNotAllowed)
		return
	}
	var err error
	switch {
	case r.Method == http.MethodGet:
	case r.Method != http.MethodPost:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		return
	case rest == "enable":
		err = botPushEnable(rec)
	case rest == "rotate":
		err = botPushRotate(rec)
	case rest == "disable":
		err = setBotPushKey(rec.Name, "")
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	v, err := botPushViewOf(rec)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

package main

// /api/alerts: bot liveness alerts (bothealth.go, alertchannels.go), behind
// UI_AUTH like the rest of /api.
//
//	GET    /api/alerts                     every bot's health and alert state, recent deliveries
//	GET    /api/alerts/settings            the global defaults; PUT patches them
//	GET    /api/alerts/kinds               each channel kind's form
//	GET    /api/alerts/channels            channels, secrets never included (secrets_set names them)
//	POST   /api/alerts/channels            {name, kind, enabled?, config, secrets}
//	POST   /api/alerts/channels/test       the same body, sent as a test without saving
//	PUT    /api/alerts/channels/<id>       {name?, enabled?, config?, secrets?}: a secret left out is kept, "" clears it
//	DELETE /api/alerts/channels/<id>
//	POST   /api/alerts/channels/<id>/test  send a test through the saved channel
//
// A bot's own alert settings are its `alerts` field (PUT /api/bots/<name>),
// and its hold is POST|DELETE /api/bots/<name>/hold.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// alertSettingsPatch changes only what it names.
type alertSettingsPatch struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	Channels     *[]int64 `json:"channels,omitempty"`
	DownAfter    *int     `json:"down_after,omitempty"`
	UpAfter      *int     `json:"up_after,omitempty"`
	RemindEvery  *int     `json:"remind_every,omitempty"`
	RemindMax    *int     `json:"remind_max,omitempty"`
	HandleWithin *int     `json:"handle_within,omitempty"`
	LinkBase     *string  `json:"link_base,omitempty"`
}

func applyAlertSettingsPatch(p alertSettingsPatch) (alertSettings, error) {
	s := loadAlertSettings()
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.Channels != nil {
		s.Channels = *p.Channels
	}
	if p.DownAfter != nil {
		s.DownAfter = *p.DownAfter
	}
	if p.UpAfter != nil {
		s.UpAfter = *p.UpAfter
	}
	if p.RemindEvery != nil {
		s.RemindEvery = *p.RemindEvery
	}
	if p.RemindMax != nil {
		s.RemindMax = *p.RemindMax
	}
	if p.HandleWithin != nil {
		s.HandleWithin = *p.HandleWithin
	}
	if p.LinkBase != nil {
		s.LinkBase = *p.LinkBase
	}
	if err := s.validate(); err != nil {
		return s, err
	}
	if err := checkAlertChannelIDs(s.Channels, false); err != nil {
		return s, err
	}
	b, _ := json.Marshal(s)
	if err := setSetting(alertSettingsKey, string(b)); err != nil {
		return s, err
	}
	return s, nil
}

// checkAlertChannelIDs refuses ids that name no channel, and heartbeats (they
// are bound to one bot, never routed).
func checkAlertChannelIDs(ids []int64, _ bool) error {
	if len(ids) == 0 {
		return nil
	}
	all, err := listAlertChannels(false)
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			return errors.New("a channel is listed twice")
		}
		seen[id] = true
		var found *alertChannel
		for _, c := range all {
			if c.ID == id {
				found = c
			}
		}
		if found == nil {
			return errors.New("channel " + strconv.FormatInt(id, 10) + " does not exist")
		}
		if found.Kind == "heartbeat" {
			return errors.New("a heartbeat belongs to its bot and is not routed; leave it out of the list")
		}
	}
	return nil
}

// botAlertRow is one bot in the overview.
type botAlertRow struct {
	Bot    string        `json:"bot"`
	Host   string        `json:"host"`
	Health *botHealth    `json:"health,omitempty"`
	Alert  botAlertState `json:"alert"`
	Hold   botHoldInfo   `json:"hold"`
	Mode   string        `json:"mode"` // inherit | off | custom
}

func alertsOverview(onlyBot string, reach func(host string) bool) map[string]any {
	list, _ := listBots()
	now := time.Now()
	rows := []botAlertRow{}
	for _, r := range list {
		if onlyBot != "" && r.Name != onlyBot {
			continue
		}
		if reach != nil && !reach(r.Host) {
			continue
		}
		st := loadBotAlertState(r.Name)
		mode := "inherit"
		if r.Alerts != nil && r.Alerts.Mode != "" {
			mode = r.Alerts.Mode
		}
		rows = append(rows, botAlertRow{Bot: r.Name, Host: r.Host, Health: botHealthOf(r.Name), Alert: st, Hold: botHoldView(r, now), Mode: mode})
	}
	return map[string]any{
		"settings":   loadAlertSettings(),
		"bots":       rows,
		"deliveries": listAlertDeliveries(onlyBot, 50),
	}
}

func createAlertChannel(in alertChannelInput) (*alertChannel, error) {
	c, err := in.apply(nil)
	if err != nil {
		return nil, err
	}
	if err := saveAlertChannel(c); err != nil {
		return nil, err
	}
	return c, nil
}

func updateAlertChannel(id int64, in alertChannelInput) (*alertChannel, error) {
	base, err := getAlertChannel(id)
	if err != nil {
		return nil, err
	}
	c, err := in.apply(base)
	if err != nil {
		return nil, err
	}
	if err := saveAlertChannel(c); err != nil {
		return nil, err
	}
	return c, nil
}

// runAlertChannelTest sends a test through a saved channel and records it.
func runAlertChannelTest(id int64) (map[string]any, error) {
	c, err := getAlertChannel(id)
	if err != nil {
		return nil, err
	}
	terr := testAlertChannel(c)
	msg := ""
	if terr != nil {
		msg = terr.Error()
	}
	setAlertChannelStatus(c.ID, true, msg)
	status := "sent"
	if terr != nil {
		status = "failed"
	}
	_, _ = db.Exec(`INSERT INTO alert_deliveries (at, bot, channel_id, channel_name, channel_kind, event, status, error, attempts) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		nowStamp(time.Now()), "", c.ID, c.Name, c.Kind, alertTest, status, msg)
	return map[string]any{"ok": terr == nil, "error": msg}, nil
}

func serveAlerts(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/alerts"), "/")
	switch {
	case rest == "":
		if r.Method != http.MethodGet {
			http.Error(w, "GET", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, alertsOverview(r.URL.Query().Get("bot"), nil))
	case rest == "kinds":
		writeJSON(w, map[string]any{"kinds": alertKinds})
	case rest == "settings":
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, loadAlertSettings())
		case http.MethodPut, http.MethodPatch:
			var p alertSettingsPatch
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&p); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			s, err := applyAlertSettingsPatch(p)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, s)
		default:
			http.Error(w, "GET or PUT", http.StatusMethodNotAllowed)
		}
	case rest == "channels":
		switch r.Method {
		case http.MethodGet:
			list, err := listAlertChannels(false)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if list == nil {
				list = []*alertChannel{}
			}
			writeJSON(w, map[string]any{"channels": list})
		case http.MethodPost:
			in, ok := decodeAlertChannelInput(w, r)
			if !ok {
				return
			}
			c, err := createAlertChannel(in)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"channel": c})
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
	case rest == "channels/test":
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		in, ok := decodeAlertChannelInput(w, r)
		if !ok {
			return
		}
		var base *alertChannel
		if id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64); err == nil {
			// A draft edit of a saved channel: its stored secrets fill in what
			// the form left blank.
			if base, err = getAlertChannel(id); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
		}
		c, err := in.apply(base)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		terr := testAlertChannel(c)
		msg := ""
		if terr != nil {
			msg = terr.Error()
		}
		writeJSON(w, map[string]any{"ok": terr == nil, "error": msg})
	case strings.HasPrefix(rest, "channels/"):
		idStr, action, _ := strings.Cut(strings.TrimPrefix(rest, "channels/"), "/")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch {
		case action == "test" && r.Method == http.MethodPost:
			out, err := runAlertChannelTest(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			writeJSON(w, out)
		case action == "" && r.Method == http.MethodPut:
			in, ok := decodeAlertChannelInput(w, r)
			if !ok {
				return
			}
			c, err := updateAlertChannel(id, in)
			if errors.Is(err, errAlertChannelNotFound) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			} else if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"channel": c})
		case action == "" && r.Method == http.MethodDelete:
			if _, err := getAlertChannel(id); err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			if err := deleteAlertChannel(id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "PUT, DELETE or POST …/test", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func decodeAlertChannelInput(w http.ResponseWriter, r *http.Request) (alertChannelInput, bool) {
	var in alertChannelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return in, false
	}
	return in, true
}

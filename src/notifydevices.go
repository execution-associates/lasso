package main

// Which notifications each device takes (notify.go's kinds). A device is one
// browser (its id lives in that browser's localStorage) or one Mac app; a push
// subscription names the device it belongs to, so Web Push filters on the
// server, and a live client reads its own device's choices to filter what the
// SSE channel hands it.
//
// Only explicit choices are stored, as {kind: bool}: a kind the device never
// touched follows the catalog's default, so a kind added later reaches every
// device the way the catalog says.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const notifyDeviceSchema = `
CREATE TABLE IF NOT EXISTS notify_devices (
  id         TEXT PRIMARY KEY,
  label      TEXT NOT NULL DEFAULT '',
  -- JSON {kind: bool}: only the kinds this device chose; the rest default.
  kinds      TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
`

// notifyDeviceIDRE is the shape a client's device id must have: random, url
// safe, long enough not to collide, short enough to be a key.
var notifyDeviceIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

type notifyDevice struct {
	ID     string          `json:"id"`
	Label  string          `json:"label,omitempty"`
	Kinds  map[string]bool `json:"-"`
	Update string          `json:"updated_at,omitempty"`
}

// loadNotifyDevice reads one device; a device never seen has no choices.
func loadNotifyDevice(id string) (notifyDevice, error) {
	d := notifyDevice{ID: id, Kinds: map[string]bool{}}
	if db == nil || id == "" {
		return d, nil
	}
	var kinds string
	err := db.QueryRow(`SELECT label, kinds, updated_at FROM notify_devices WHERE id = ?`, id).
		Scan(&d.Label, &kinds, &d.Update)
	if errors.Is(err, sql.ErrNoRows) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if kinds != "" {
		_ = json.Unmarshal([]byte(kinds), &d.Kinds)
	}
	return d, nil
}

func saveNotifyDevice(d notifyDevice) error {
	if db == nil {
		return errors.New("no db")
	}
	kinds, err := json.Marshal(d.Kinds)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO notify_devices (id, label, kinds, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET label=excluded.label, kinds=excluded.kinds, updated_at=excluded.updated_at`,
		d.ID, d.Label, string(kinds), time.Now().UTC().Format(time.RFC3339))
	return err
}

// notifyDeviceView is what a device's settings read: the catalog, and this
// device's resolved choice for every kind in it.
type notifyDeviceView struct {
	Kinds  []notifKindInfo `json:"kinds"`
	Device struct {
		ID    string          `json:"id"`
		Label string          `json:"label,omitempty"`
		Wants map[string]bool `json:"wants"`
	} `json:"device"`
}

func viewNotifyDevice(d notifyDevice) notifyDeviceView {
	var v notifyDeviceView
	v.Kinds = notifKindCatalog
	v.Device.ID, v.Device.Label = d.ID, d.Label
	v.Device.Wants = map[string]bool{}
	for _, k := range notifKindCatalog {
		v.Device.Wants[string(k.ID)] = notifWants(d.Kinds, k.ID)
	}
	return v
}

// serveNotify is GET /api/notify?device=<id>: the catalog and that device's
// choices.
func serveNotify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("device")
	if id != "" && !notifyDeviceIDRE.MatchString(id) {
		http.Error(w, "device: 8-64 characters of A-Z a-z 0-9 _ -", http.StatusBadRequest)
		return
	}
	d, err := loadNotifyDevice(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, viewNotifyDevice(d))
}

// serveNotifyDevice is POST /api/notify/device {id, label?, kinds?}: kinds is
// merged per kind, and a null brings a kind back to its default. An unknown
// kind refuses the whole write, like ui_state's validated fields.
func serveNotifyDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID    string           `json:"id"`
		Label *string          `json:"label"`
		Kinds map[string]*bool `json:"kinds"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !notifyDeviceIDRE.MatchString(req.ID) {
		http.Error(w, "id: 8-64 characters of A-Z a-z 0-9 _ -", http.StatusBadRequest)
		return
	}
	for k := range req.Kinds {
		if !notifKindKnown(k) {
			http.Error(w, "unknown kind "+k, http.StatusBadRequest)
			return
		}
	}
	d, err := loadNotifyDevice(req.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if req.Label != nil {
		d.Label = clipNotifyText(strings.TrimSpace(*req.Label), 60)
	}
	for k, v := range req.Kinds {
		if v == nil {
			delete(d.Kinds, k)
		} else {
			d.Kinds[k] = *v
		}
	}
	if err := saveNotifyDevice(d); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, viewNotifyDevice(d))
}

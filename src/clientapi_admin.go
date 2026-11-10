package main

// Managing the people who use the phone app (docs/design/client-api.md):
// /api/app-users, behind UI_AUTH like the rest of /api, and the matching tools
// on /mcp (mcp_appusers.go) share this file's functions.
//
//	GET    /api/app-users[?bot=]                 every user with grants and devices (a bot's, with ?bot=)
//	POST   /api/app-users                        {name, email?}
//	GET    /api/app-users/<id>                   one user
//	PUT    /api/app-users/<id>                   {name?, email?, disabled?, quota_mb?, quota_files?}
//	DELETE /api/app-users/<id>                   the user and everything of theirs
//	PUT    /api/app-users/<id>/grants/<bot>      {owner?}: let the user reach the bot
//	DELETE /api/app-users/<id>/grants/<bot>      take the grant away
//	GET    /api/app-users/<id>/pairings          recent pairings (never their codes)
//	POST   /api/app-users/<id>/pairings          {base_url?}: a one-time code, valid 10 minutes
//	DELETE /api/app-users/<id>/pairings/<pid>    cancel an unused pairing
//	GET    /api/app-users/<id>/devices           the user's paired devices
//	DELETE /api/app-users/<id>/devices/<did>     revoke one: its token stops working at once

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type appUserView struct {
	*appUser
	Grants  []appGrant      `json:"grants"`
	Devices []appDeviceView `json:"devices"`
	// QuotaMB and QuotaFiles are the owner's override for this person (null:
	// the default applies); Storage is what they use against what they may.
	// FileRetentionDays is their own choice, within the owner's maximum
	// (null: they keep their files until they delete them).
	QuotaMB           *int           `json:"quota_mb"`
	QuotaFiles        *int           `json:"quota_files"`
	FileRetentionDays *int           `json:"file_retention_days"`
	Storage           map[string]any `json:"storage"`
}

func appUserViewOf(u *appUser) (appUserView, error) {
	gs, err := listAppGrants(u.ID)
	if err != nil {
		return appUserView{}, err
	}
	ds, err := listAppDevices(u.ID)
	if err != nil {
		return appUserView{}, err
	}
	dvs := make([]appDeviceView, 0, len(ds))
	for i := range ds {
		ds[i].UserID = ""
		dvs = append(dvs, appDeviceView{appDevice: ds[i], Push: clientPushDeviceView(ds[i].ID)})
	}
	v := appUserView{appUser: u, Grants: gs, Devices: dvs, Storage: clientStorageView(u)}
	if u.QuotaMB > 0 {
		v.QuotaMB = &u.QuotaMB
	}
	if u.QuotaFiles > 0 {
		v.QuotaFiles = &u.QuotaFiles
	}
	if d := clientRetentionDaysOf(u); d > 0 {
		v.FileRetentionDays = &d
	}
	return v, nil
}

// appDeviceView is a device as the owner sees it, with its push registration
// (never the token itself; null without one).
type appDeviceView struct {
	appDevice
	Push map[string]any `json:"push"`
}

func appUserViews(bot string) ([]appUserView, error) {
	us, err := listAppUsers()
	if err != nil {
		return nil, err
	}
	out := []appUserView{}
	for _, u := range us {
		v, err := appUserViewOf(u)
		if err != nil {
			return nil, err
		}
		if bot != "" {
			has := false
			for _, g := range v.Grants {
				has = has || g.Bot == bot
			}
			if !has {
				continue
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// appPairingOut is a freshly made pairing: the one time its code is shown.
type appPairingOut struct {
	Pairing  *appPairing `json:"pairing"`
	Code     string      `json:"code"`
	Link     string      `json:"link"`
	DeepLink string      `json:"deep_link"`
}

// pairingBase checks a public address given for the link: an origin only.
func pairingBase(base string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("base_url must be an origin such as https://lasso.example.com")
	}
	return u.Scheme + "://" + u.Host, nil
}

func mintAppPairingOut(userID, mintedBy, base string, now time.Time) (appPairingOut, error) {
	var out appPairingOut
	code, p, err := mintAppPairing(userID, mintedBy, now)
	if err != nil {
		return out, err
	}
	out.Pairing, out.Code = p, code
	if base != "" {
		out.Link = base + "/client/pair#code=" + code
		out.DeepLink = "lasso://pair?code=" + code + "&server=" + url.QueryEscape(base)
	}
	return out, nil
}

// requestOrigin is the address the owner reached lasso at, for the pairing
// link when none is given: behind a proxy or tunnel, the forwarded host and
// scheme; on loopback, plain http (the link is then only good on this machine).
func requestOrigin(r *http.Request) string {
	host := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	if host == "" {
		host = r.Host
	}
	scheme := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if scheme != "https" && scheme != "http" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + host
}

// pairingMinter names who made a pairing: the verified caller when the owner
// came through a network listener, "local" otherwise. Never anything the
// request says about itself.
func pairingMinter(r *http.Request) string {
	if c, ok := callerOf(r); ok && c.Source != string(roleLocal) && c.Name != "" {
		return c.Name
	}
	return "local"
}

func appErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errClientNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func serveAppUsers(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/app-users"), "/")
	now := time.Now()
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			vs, err := appUserViews(r.URL.Query().Get("bot"))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"users": vs})
		case http.MethodPost:
			var in struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			}
			if !decodeBody(w, r, &in) {
				return
			}
			u, err := createAppUser(in.Name, in.Email, now)
			if err != nil {
				appErr(w, err)
				return
			}
			v, _ := appUserViewOf(u)
			writeJSON(w, map[string]any{"user": v})
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
		return
	}
	parts := strings.Split(rest, "/")
	u, err := getAppUser(parts[0])
	if err != nil {
		appErr(w, err)
		return
	}
	switch {
	case len(parts) == 1:
		switch r.Method {
		case http.MethodGet:
			v, err := appUserViewOf(u)
			if err != nil {
				appErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"user": v})
		case http.MethodPut:
			var p appUserPatch
			if !decodeBody(w, r, &p) {
				return
			}
			u, err := updateAppUser(u.ID, p, now)
			if err != nil {
				appErr(w, err)
				return
			}
			v, _ := appUserViewOf(u)
			writeJSON(w, map[string]any{"user": v})
		case http.MethodDelete:
			atts, err := deleteAppUser(u.ID)
			if err != nil {
				appErr(w, err)
				return
			}
			clientRemoveFiles(atts)
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
		}
	case len(parts) == 3 && parts[1] == "grants":
		switch r.Method {
		case http.MethodPut:
			var in struct {
				Owner bool `json:"owner"`
			}
			if r.ContentLength != 0 && !decodeBody(w, r, &in) {
				return
			}
			g, err := setAppGrant(u.ID, parts[2], in.Owner, now)
			if err != nil {
				appErr(w, err)
				return
			}
			g.UserID = ""
			writeJSON(w, map[string]any{"grant": g})
		case http.MethodDelete:
			if err := deleteAppGrant(u.ID, parts[2]); err != nil {
				appErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "PUT or DELETE", http.StatusMethodNotAllowed)
		}
	case parts[1] == "pairings" && len(parts) <= 3:
		switch {
		case len(parts) == 2 && r.Method == http.MethodGet:
			ps, err := listAppPairings(u.ID, now)
			if err != nil {
				appErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"pairings": ps})
		case len(parts) == 2 && r.Method == http.MethodPost:
			var in struct {
				BaseURL string `json:"base_url"`
			}
			if r.ContentLength != 0 && !decodeBody(w, r, &in) {
				return
			}
			base := requestOrigin(r)
			if in.BaseURL != "" {
				var err error
				if base, err = pairingBase(in.BaseURL); err != nil {
					appErr(w, err)
					return
				}
			}
			out, err := mintAppPairingOut(u.ID, pairingMinter(r), base, now)
			if err != nil {
				appErr(w, err)
				return
			}
			clientHeaders(w)
			writeJSON(w, out)
		case len(parts) == 3 && r.Method == http.MethodDelete:
			if err := cancelAppPairing(u.ID, parts[2]); err != nil {
				appErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
		}
	case parts[1] == "devices" && len(parts) <= 3:
		switch {
		case len(parts) == 2 && r.Method == http.MethodGet:
			ds, err := listAppDevices(u.ID)
			if err != nil {
				appErr(w, err)
				return
			}
			for i := range ds {
				ds[i].UserID = ""
			}
			writeJSON(w, map[string]any{"devices": ds})
		case len(parts) == 3 && r.Method == http.MethodDelete:
			if err := revokeAppDevice(u.ID, parts[2], now); err != nil {
				appErr(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true})
		default:
			http.Error(w, "GET or DELETE", http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

// decodeBody reads a small JSON body, answering 400 itself on failure.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := jsonDecodeLimited(w, r, 16<<10, v); err != nil {
		http.Error(w, fmt.Sprintf("bad body: %v", err), http.StatusBadRequest)
		return false
	}
	return true
}

func jsonDecodeLimited(w http.ResponseWriter, r *http.Request, max int64, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v)
}

package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Daily Claude Code update restarts (docs/design/bots.md, "Claude Code
// updates"). Claude Code updates itself in the background, but a running
// session keeps the version it started with until it is restarted ("Update
// installed · Restart to update"). A bot runs for weeks, so with this setting
// on, the bot runner restarts each claude bot whose running version is older
// than the one installed on its host, once a day at a time of day the human
// picks, in a timezone they pick.
//
// The running version is Claude Code's own registry entry
// (~/.claude/sessions/<pid>.json, "version"); the installed one is
// `claude --version` in a login shell on the bot's host, which is what the
// bot's `mise run bot` would launch. A bot is restarted only while it is idle
// (no turn, no question waiting), so the check is repeated every tick for
// botUpdateWindow after the chosen time; a bot still busy when the window
// closes waits for the next day. omp bots are not Claude Code and are skipped.
//
// GET  /api/bot-updates   the setting, the next run, and each running claude
//                         bot's running and installed version
// POST /api/bot-updates   merge-patch the setting

const botUpdateSettingKey = "bot_updates"

const (
	// botUpdateWindow is how long after the chosen time a busy bot is waited
	// for. Longer would restart a bot in the middle of its human's day.
	botUpdateWindow = 3 * time.Hour
	// botUpdateInstalledTTL caches `claude --version` per host: the window
	// re-checks every tick, and an ssh round-trip each time is waste.
	botUpdateInstalledTTL = 5 * time.Minute
	botUpdateDefaultTime  = "04:00"
)

var botUpdateTimeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// botUpdateConfig is the `bot_updates` setting as stored.
type botUpdateConfig struct {
	Enabled  bool   `json:"enabled"`
	Time     string `json:"time"`     // HH:MM, 24-hour, in Timezone
	Timezone string `json:"timezone"` // an IANA name, e.g. America/Los_Angeles
}

// loadBotUpdateConfig reads the setting, filling unset fields with the
// defaults (04:00 in this machine's zone). An unreadable value reads as off.
func loadBotUpdateConfig() (botUpdateConfig, error) {
	c := botUpdateConfig{}
	if db != nil {
		if raw, _ := getSetting(botUpdateSettingKey); raw != "" {
			if err := json.Unmarshal([]byte(raw), &c); err != nil {
				return botUpdateConfig{Time: botUpdateDefaultTime, Timezone: localZoneName()},
					errors.New("the bot_updates setting is unreadable; set it again in lasso's settings")
			}
		}
	}
	if c.Time == "" {
		c.Time = botUpdateDefaultTime
	}
	if c.Timezone == "" {
		c.Timezone = localZoneName()
	}
	return c, nil
}

func (c botUpdateConfig) validate() error {
	if !botUpdateTimeRE.MatchString(c.Time) {
		return errors.New("time: HH:MM on a 24-hour clock, e.g. 04:00")
	}
	if _, err := loadZone(c.Timezone); err != nil {
		return errors.New("timezone: an IANA name such as America/Los_Angeles or UTC")
	}
	return nil
}

// loadZone is time.LoadLocation without its "Local" and "" (UTC) spellings,
// which would make a stored zone mean whatever the machine says later.
func loadZone(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return nil, errors.New("no zone")
	}
	return time.LoadLocation(name)
}

// localZoneName is this machine's IANA zone: $TZ, else /etc/localtime's link
// into zoneinfo, else UTC. time.Local only ever calls itself "Local".
func localZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if _, err := loadZone(tz); err == nil {
			return tz
		}
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			name := target[i+len("zoneinfo/"):]
			if _, err := loadZone(name); err == nil {
				return name
			}
		}
	}
	return "UTC"
}

// scheduledAt is the most recent chosen time at or before now, in the zone.
// time.Date normalizes a time a DST jump skips (02:30 on spring-forward day
// runs at 03:30).
func (c botUpdateConfig) scheduledAt(now time.Time) (time.Time, bool) {
	loc, err := loadZone(c.Timezone)
	if err != nil || !botUpdateTimeRE.MatchString(c.Time) {
		return time.Time{}, false
	}
	hh := int(c.Time[0]-'0')*10 + int(c.Time[1]-'0')
	mm := int(c.Time[3]-'0')*10 + int(c.Time[4]-'0')
	l := now.In(loc)
	at := time.Date(l.Year(), l.Month(), l.Day(), hh, mm, 0, 0, loc)
	if at.After(now) {
		at = time.Date(l.Year(), l.Month(), l.Day()-1, hh, mm, 0, 0, loc)
	}
	return at, true
}

// dueDay says whether now is inside a day's window, and which day (its date
// in the chosen zone), so a bot is tried at most once per day.
func (c botUpdateConfig) dueDay(now time.Time) (string, bool) {
	if !c.Enabled {
		return "", false
	}
	at, ok := c.scheduledAt(now)
	if !ok || now.Sub(at) >= botUpdateWindow {
		return "", false
	}
	return at.Format("2006-01-02"), true
}

// nextRun is when the next window opens.
func (c botUpdateConfig) nextRun(now time.Time) (time.Time, bool) {
	at, ok := c.scheduledAt(now)
	if !ok {
		return time.Time{}, false
	}
	loc := at.Location()
	return time.Date(at.Year(), at.Month(), at.Day()+1, at.Hour(), at.Minute(), 0, 0, loc), true
}

// botUpdateOutdated reports whether a bot running `running` should restart
// onto `installed`. Only a strictly newer install counts, so a host whose PATH
// finds an older claude than the bot runs never restarts it every day.
func botUpdateOutdated(running, installed string) bool {
	return running != "" && installed != "" && versionLess(running, installed)
}

var botUpdates = struct {
	sync.Mutex
	tried     map[string]string // bot -> the day it was last restarted for an update
	installed map[string]botInstalledVersion
}{tried: map[string]string{}, installed: map[string]botInstalledVersion{}}

type botInstalledVersion struct {
	version string
	err     string
	at      time.Time
}

// botClaudeVersion runs `claude --version` on the host. A seam for tests.
var botClaudeVersion = func(b Backend) (string, error) {
	home, _ := b.HomeDir()
	out, err := botRun(b, home, "claude", []string{"--version"}, nil)
	if err != nil {
		return "", err
	}
	return toolVersion(string(out)), nil
}

// botClaudeInstalled is the host's installed Claude Code version, cached.
func botClaudeInstalled(b Backend, now time.Time) (string, error) {
	botUpdates.Lock()
	c, ok := botUpdates.installed[b.Name()]
	botUpdates.Unlock()
	if ok && now.Sub(c.at) < botUpdateInstalledTTL && now.Sub(c.at) >= 0 {
		if c.err != "" {
			return "", errors.New(c.err)
		}
		return c.version, nil
	}
	v, err := botClaudeVersion(b)
	c = botInstalledVersion{version: v, at: now}
	if err != nil {
		c.err = err.Error()
	} else if v == "" {
		c.err = "claude --version printed no version"
		err = errors.New(c.err)
	}
	botUpdates.Lock()
	botUpdates.installed[b.Name()] = c
	botUpdates.Unlock()
	return v, err
}

// botUpdateRestart restarts a bot onto the installed Claude Code. A seam for
// tests. restartBot marks the bot stopped before it starts it again, so a
// start that fails clears the mark: an unattended restart must never leave a
// bot down that keep-running would otherwise bring back.
var botUpdateRestart = func(b Backend, r *botRecord) error {
	err := restartBot(b, r, false)
	if err != nil && !errors.Is(err, errBotRunning) {
		_ = setBotStopped(r.Name, false)
	}
	return err
}

// botUpdateTick runs on the bot runner after the rest of botTick. Inside a
// day's window it restarts at most one bot per tick: the first idle, running
// claude bot whose installed Claude Code is newer than the one it runs.
// sessions is botTick's per-host registry read, filled in here for hosts it
// did not need.
func botUpdateTick(list []*botRecord, sessions map[string][]claudeSessionEntry, now time.Time) {
	cfg, err := loadBotUpdateConfig()
	if err != nil {
		return
	}
	day, ok := cfg.dueDay(now)
	if !ok {
		return
	}
	for _, r := range list {
		if r.isOmp() || r.Stopped || botHeld(r, now) {
			continue
		}
		botUpdates.Lock()
		done := botUpdates.tried[r.Name] == day
		botUpdates.Unlock()
		if done {
			continue
		}
		b, err := botBackend(r.Host)
		if err != nil {
			continue
		}
		if _, ok := findBotPane(b, r); !ok {
			continue
		}
		if _, ok := sessions[r.Host]; !ok {
			sessions[r.Host] = readClaudeSessions(b)
		}
		s, found := findBotSession(sessions[r.Host], r.Name, expandTildeOn(b, r.Dir))
		if !found {
			continue
		}
		installed, err := botClaudeInstalled(b, now)
		if err != nil || !botUpdateOutdated(s.Version, installed) {
			continue
		}
		// Mid-turn, or waiting on its human: try again next tick.
		if s.agentStatus() != "idle" {
			continue
		}
		botUpdates.Lock()
		botUpdates.tried[r.Name] = day
		botUpdates.Unlock()
		log.Printf("bots:     %s runs Claude Code %s, %s is installed; restarting (daily update)", r.Name, s.Version, installed)
		if err := botUpdateRestart(b, r); err != nil && !errors.Is(err, errBotRunning) {
			log.Printf("bots:     update restart %s: %v", r.Name, err)
		}
		return
	}
}

// botUpdateBotView is one running claude bot in GET /api/bot-updates.
type botUpdateBotView struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Running   string `json:"running,omitempty"`
	Installed string `json:"installed,omitempty"`
	Outdated  bool   `json:"outdated"`
	Error     string `json:"error,omitempty"`
}

type botUpdateView struct {
	Enabled  bool               `json:"enabled"`
	Time     string             `json:"time"`
	Timezone string             `json:"timezone"`
	NextRun  string             `json:"next_run,omitempty"` // RFC 3339, while enabled
	InWindow bool               `json:"in_window"`          // today's run is under way
	WindowM  int                `json:"window_minutes"`
	Reason   string             `json:"reason,omitempty"`
	Bots     []botUpdateBotView `json:"bots"`
}

// botUpdateBots lists every running claude bot with its versions, so the
// Settings group can say who the next run would restart.
func botUpdateBots(now time.Time) []botUpdateBotView {
	out := []botUpdateBotView{}
	list, err := listBots()
	if err != nil {
		return out
	}
	sessions := map[string][]claudeSessionEntry{}
	for _, r := range list {
		if r.isOmp() || r.Stopped {
			continue
		}
		b, err := botBackend(r.Host)
		if err != nil {
			continue
		}
		if _, ok := findBotPane(b, r); !ok {
			continue
		}
		if _, ok := sessions[r.Host]; !ok {
			sessions[r.Host] = readClaudeSessions(b)
		}
		v := botUpdateBotView{Name: r.Name, Host: r.Host}
		if s, ok := findBotSession(sessions[r.Host], r.Name, expandTildeOn(b, r.Dir)); ok {
			v.Running = s.Version
		}
		installed, err := botClaudeInstalled(b, now)
		if err != nil {
			v.Error = lastLines(err.Error(), 1)
		}
		v.Installed = installed
		v.Outdated = botUpdateOutdated(v.Running, installed)
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func botUpdateViewOf(c botUpdateConfig, loadErr error, now time.Time, withBots bool) botUpdateView {
	v := botUpdateView{Enabled: c.Enabled, Time: c.Time, Timezone: c.Timezone, WindowM: int(botUpdateWindow / time.Minute), Bots: []botUpdateBotView{}}
	if loadErr != nil {
		v.Reason = loadErr.Error()
	}
	if c.Enabled {
		if next, ok := c.nextRun(now); ok {
			v.NextRun = next.Format(time.RFC3339)
		}
		_, v.InWindow = c.dueDay(now)
	}
	if withBots {
		v.Bots = botUpdateBots(now)
	}
	return v
}

// botUpdatePatch is a POST to /api/bot-updates: only the named fields change.
type botUpdatePatch struct {
	Enabled  *bool   `json:"enabled,omitempty" jsonschema:"Restart claude bots once a day when a newer Claude Code is installed on their host."`
	Time     *string `json:"time,omitempty" jsonschema:"The time of day, HH:MM on a 24-hour clock, e.g. 04:00. A bot busy then is waited for up to 3 hours."`
	Timezone *string `json:"timezone,omitempty" jsonschema:"The IANA timezone the time is in, e.g. America/Los_Angeles, Europe/Berlin or UTC."`
}

// serveBotUpdates is /api/bot-updates, the `bot_updates` section of the
// settings (update_settings writes it through here).
func serveBotUpdates(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c, err := loadBotUpdateConfig()
		writeJSON(w, botUpdateViewOf(c, err, time.Now(), r.URL.Query().Get("bots") != "0"))
	case http.MethodPost:
		var p botUpdatePatch
		if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&p); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		c, _ := loadBotUpdateConfig() // an unreadable stored value is replaced
		if p.Enabled != nil {
			c.Enabled = *p.Enabled
		}
		if p.Time != nil {
			c.Time = strings.TrimSpace(*p.Time)
		}
		if p.Timezone != nil {
			c.Timezone = strings.TrimSpace(*p.Timezone)
		}
		if err := c.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		b, _ := json.Marshal(c)
		if err := setSetting(botUpdateSettingKey, string(b)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, botUpdateViewOf(c, nil, time.Now(), false))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

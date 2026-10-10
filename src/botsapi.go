package main

// /api/bots: the Bots view's API, behind UI_AUTH like the rest of /api.
//
//	GET    /api/bots                          every bot with its live state
//	POST   /api/bots                          create {bot fields…, start?}
//	PUT    /api/bots/order                    {names}: the list's order, as dragged
//	GET    /api/bots/skill-library?host=      skills a bot can copy in (~/.claude/skills)
//	GET    /api/bots/<name>                   one bot, plus its launch script
//	PUT    /api/bots/<name>                   save {bot fields…, restart?}
//	DELETE /api/bots/<name>                   forget a stopped bot (its folder stays)
//	POST   /api/bots/<name>/start|stop|restart {fresh?}
//	GET    /api/bots/<name>/env               keys; secrets redacted
//	PUT    /api/bots/<name>/env               {key, value, secret}
//	DELETE /api/bots/<name>/env?key=
//	GET    /api/bots/<name>/skills            the folder's project skills
//	POST   /api/bots/<name>/skills            {from}: copy a skill directory in
//	DELETE /api/bots/<name>/skills?name=
//
// CLAUDE.md is a file in the bot's folder and goes through /api/file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

func serveBots(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/bots"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			list, err := listBots()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"bots": botStatuses(list)})
		case http.MethodPost:
			serveBotCreate(w, r)
		default:
			http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
		}
		return
	}
	if rest == "order" {
		if r.Method != http.MethodPut {
			http.Error(w, "PUT", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || len(in.Names) > 500 {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := reorderBots(in.Names); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	if rest == "skill-library" {
		serveBotSkillLibrary(w, r)
		return
	}
	name, action, _ := strings.Cut(rest, "/")
	rec, err := getBot(name)
	if errors.Is(err, errBotNotFound) {
		http.Error(w, "bot not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	switch action {
	case "":
		serveBotOne(w, r, b, rec)
	case "start", "stop", "restart":
		serveBotLifecycle(w, r, b, rec, action)
	case "env":
		serveBotEnv(w, r, b, rec)
	case "skills":
		serveBotSkills(w, r, b, rec)
	default:
		http.NotFound(w, r)
	}
}

// botInput is the editable half of a bot plus the request's own switches.
type botInput struct {
	botRecord
	Start   bool `json:"start"`
	Restart bool `json:"restart"`
}

func decodeBotInput(r *http.Request) (*botInput, error) {
	var in botInput
	in.KeepRunning = true
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&in); err != nil {
		return nil, fmt.Errorf("bad body: %w", err)
	}
	return &in, nil
}

func serveBotCreate(w http.ResponseWriter, r *http.Request) {
	in, err := decodeBotInput(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rec := in.botRecord
	rec.ID, rec.LastSessionID, rec.Stopped = 0, "", !in.Start
	if err := rec.normalize(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := insertBot(&rec); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// The row stays even when the folder cannot be written: the settings page
	// is where the human fixes what was wrong.
	if err := botMaterialize(b, &rec); err != nil {
		writeJSON(w, map[string]any{"bot": botStatus(b, &rec, nil), "error": err.Error()})
		return
	}
	if in.Start {
		if err := startBot(b, &rec, true); err != nil {
			writeJSON(w, map[string]any{"bot": botStatus(b, &rec, nil), "error": err.Error()})
			return
		}
	}
	writeJSON(w, map[string]any{"bot": botStatus(b, &rec, readClaudeSessions(b))})
}

func serveBotOne(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	switch r.Method {
	case http.MethodGet:
		v := botStatus(b, rec, readClaudeSessions(b))
		dir := expandTildeOn(b, rec.Dir)
		writeJSON(w, map[string]any{
			"bot":       v,
			"dir_path":  dir,
			"launch":    botTaskScript(rec, dir, botEnvKeys(b, dir)),
			"claude_md": filepath.Join(dir, "CLAUDE.md"),
		})
	case http.MethodPut:
		in, err := decodeBotInput(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := in.botRecord
		// Identity and runtime state are not editable here.
		next.ID, next.Host, next.Name = rec.ID, rec.Host, rec.Name
		next.LastSessionID, next.Stopped, next.CreatedAt = rec.LastSessionID, rec.Stopped, rec.CreatedAt
		if err := next.normalize(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := updateBot(&next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := botMaterialize(b, &next); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if in.Restart {
			if _, running := findBotPane(b, &next); running {
				if err := restartBot(b, &next, false); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
			}
		}
		writeJSON(w, map[string]any{"bot": botStatus(b, &next, readClaudeSessions(b))})
	case http.MethodDelete:
		if _, running := findBotPane(b, rec); running {
			http.Error(w, "stop the bot before deleting it", http.StatusConflict)
			return
		}
		if err := deleteBot(rec.Name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "dir": rec.Dir})
	default:
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

func serveBotLifecycle(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Fresh bool `json:"fresh"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in)
	var err error
	switch action {
	case "start":
		err = startBot(b, rec, in.Fresh)
	case "stop":
		err = stopBot(b, rec)
	case "restart":
		err = restartBot(b, rec, in.Fresh)
	}
	switch {
	case errors.Is(err, errBotRunning):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if latest, err := getBot(rec.Name); err == nil {
		rec = latest
	}
	writeJSON(w, map[string]any{"bot": botStatus(b, rec, readClaudeSessions(b))})
}

func serveBotEnv(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	dir := expandTildeOn(b, rec.Dir)
	switch r.Method {
	case http.MethodGet:
		vars, err := botEnvList(b, dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"vars": vars, "fnox_file": botFnoxFile(dir)})
	case http.MethodPut:
		var in struct {
			Key    string `json:"key"`
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if err := botEnvSet(b, dir, in.Key, in.Value, in.Secret); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := botWriteTask(b, rec, dir); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "restart_needed": botRunning(b, rec)})
	case http.MethodDelete:
		if err := botEnvUnset(b, dir, r.URL.Query().Get("key")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := botWriteTask(b, rec, dir); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "restart_needed": botRunning(b, rec)})
	default:
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

// botRunning: an env change reaches a running bot only on its next start.
func botRunning(b Backend, r *botRecord) bool {
	_, ok := findBotPane(b, r)
	return ok
}

// --- skills ------------------------------------------------------------------

var botSkillNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type botSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

// listSkillDir lists the skills (directories with a SKILL.md) under root.
func listSkillDir(b Backend, root string) []botSkill {
	out := []botSkill{}
	ents, err := b.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if !botSkillNameRE.MatchString(e.Name) {
			continue
		}
		p := filepath.Join(root, e.Name)
		fi, err := b.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		md, err := b.ReadFile(filepath.Join(p, "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, botSkill{Name: e.Name, Description: skillDescription(string(md)), Path: p})
	}
	return out
}

// skillDescription is the frontmatter's description line, if there is one.
func skillDescription(md string) string {
	if !strings.HasPrefix(md, "---") {
		return ""
	}
	for _, line := range strings.Split(md, "\n")[1:] {
		if strings.TrimSpace(line) == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			return previewText(strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	return ""
}

func serveBotSkillLibrary(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		host = "local"
	}
	b, err := botBackend(host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	home, err := b.HomeDir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"skills": listSkillDir(b, filepath.Join(claudeDir(home), "skills"))})
}

func serveBotSkills(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	root := filepath.Join(expandTildeOn(b, rec.Dir), ".claude", "skills")
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"skills": listSkillDir(b, root)})
	case http.MethodPost:
		var in struct {
			From string `json:"from"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		src := filepath.Clean(expandTildeOn(b, strings.TrimSpace(in.From)))
		if !filepath.IsAbs(src) {
			http.Error(w, "from must be an absolute path or start with ~/", http.StatusBadRequest)
			return
		}
		name := filepath.Base(src)
		if !botSkillNameRE.MatchString(name) {
			http.Error(w, "not a skill directory name", http.StatusBadRequest)
			return
		}
		if _, err := b.Stat(filepath.Join(src, "SKILL.md")); err != nil {
			http.Error(w, "no SKILL.md in "+src, http.StatusBadRequest)
			return
		}
		dst := filepath.Join(root, name)
		if _, err := b.Stat(dst); err == nil {
			http.Error(w, "the bot already has a skill named "+name, http.StatusConflict)
			return
		}
		if err := copyTree(b, src, dst, &copyBudget{files: 2000, bytes: 50 << 20}); err != nil {
			_ = b.RemoveAll(dst)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"skills": listSkillDir(b, root)})
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if !botSkillNameRE.MatchString(name) {
			http.Error(w, "bad skill name", http.StatusBadRequest)
			return
		}
		if err := b.RemoveAll(filepath.Join(root, name)); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"skills": listSkillDir(b, root)})
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

type copyBudget struct{ files, bytes int64 }

// copyTree copies a directory on b, skipping symlinks (a skill that links out
// of itself would copy whatever it points at) and hidden entries, within a
// budget so a mistaken path cannot copy a home directory.
func copyTree(b Backend, src, dst string, budget *copyBudget) error {
	if err := b.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	ents, err := b.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		s, d := filepath.Join(src, e.Name), filepath.Join(dst, e.Name)
		fi, err := b.Lstat(s)
		if err != nil || !fi.Mode().IsRegular() && !fi.IsDir() {
			continue
		}
		if fi.IsDir() {
			if err := copyTree(b, s, d, budget); err != nil {
				return err
			}
			continue
		}
		budget.files--
		budget.bytes -= fi.Size()
		if budget.files < 0 || budget.bytes < 0 {
			return fmt.Errorf("%s is too large to copy as a skill", src)
		}
		data, err := b.ReadFile(s)
		if err != nil {
			return err
		}
		if err := b.WriteFile(d, data, fi.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

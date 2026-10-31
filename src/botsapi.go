package main

// /api/bots: the Bots view's API, behind UI_AUTH like the rest of /api.
//
//	GET    /api/bots                          every bot with its live state
//	POST   /api/bots                          create {bot fields…, start?}
//	PUT    /api/bots/order                    {names}: the list's order, as dragged
//	GET    /api/bots/skill-library?host=      skills a bot can copy in (~/.claude/skills)
//	GET    /api/bots/oauth/callback           an OAuth redirect lands here (botoauth.go)
//	POST   /api/bots/oauth/finish             {url}: the address a localhost redirect left the browser on
//	GET    /api/bots/<name>/avatar            its picture; PUT (raw image body) / DELETE
//	GET    /api/bots/<name>/oauth             each signed-in server's status
//	POST   /api/bots/<name>/oauth             {server, origin}: begin a sign-in → {authorize_url, state, localhost}
//	DELETE /api/bots/<name>/oauth?server=     sign out
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
//	GET    /api/bots/<name>/chat[?before=]    an omp bot's chat; user rows carry the meta they were sent with
//	POST   /api/bots/<name>/chat/send         {text, meta?}: meta is {key: string}, ≤16 keys, ≤1 KB (botmeta.go)
//	POST   /api/bots/<name>/chat/stop|answer  abort the turn; {id, value?, confirmed?, cancelled?}
//	…      /api/bots/<name>/jobs[/…]          scheduled jobs and webhooks (botjobs.go)
//
// CLAUDE.md is a file in the bot's folder and goes through /api/file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
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
	if rest == "oauth/callback" || rest == "oauth/finish" {
		serveBotOAuthReturn(w, r, rest == "oauth/callback")
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
	if jobs, ok := strings.CutPrefix(action, "jobs"); ok && (jobs == "" || strings.HasPrefix(jobs, "/")) {
		serveBotJobs(w, r, rec, strings.TrimPrefix(jobs, "/"))
		return
	}
	if talk, ok := strings.CutPrefix(action, "talk"); ok && (talk == "" || strings.HasPrefix(talk, "/")) {
		serveBotTalkSettings(w, r, rec, strings.TrimPrefix(talk, "/"))
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
	case "oauth":
		serveBotOAuth(w, r, b, rec)
	case "avatar":
		serveBotAvatar(w, r, b, rec)
	case "model":
		serveBotModel(w, r, b, rec)
	case "sessions", "sessions/new", "sessions/resume":
		serveBotSessions(w, r, b, rec, strings.TrimPrefix(strings.TrimPrefix(action, "sessions"), "/"))
	case "chat", "chat/send", "chat/stop", "chat/answer":
		serveBotChat(w, r, b, rec, strings.TrimPrefix(strings.TrimPrefix(action, "chat"), "/"))
	default:
		http.NotFound(w, r)
	}
}

// botInput is the editable half of a bot plus the request's own switches.
type botInput struct {
	botRecord
	Start   bool `json:"start"`
	Restart bool `json:"restart"`
	// Instructions (create only) are the bot's first AGENTS.md or CLAUDE.md,
	// in place of the stub. A folder that already has one keeps it.
	Instructions string `json:"instructions"`
}

const botInstructionsMax = 256 << 10

// decodeBotInput reads a create or save body over base, so a field the body
// leaves out keeps base's value (a create's defaults, or a save's current row).
func decodeBotInput(r *http.Request, base botRecord) (*botInput, error) {
	in := botInput{botRecord: base}
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&in); err != nil {
		return nil, fmt.Errorf("bad body: %w", err)
	}
	return &in, nil
}

func serveBotCreate(w http.ResponseWriter, r *http.Request) {
	// A new bot runs omp unless the body says claude.
	in, err := decodeBotInput(r, botRecord{KeepRunning: true, Notify: true, Runtime: botRuntimeOmp})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := botCreate(in.botRecord, in.Instructions, in.Start)
	if err != nil {
		http.Error(w, err.Error(), err.(botModelErr).status)
		return
	}
	writeJSON(w, res)
}

// botCreate makes a bot, for the API and create_bot alike: the row, then its
// folder, then (start) its first launch. Validation failures are errors
// (botModelErr, with the HTTP status); once the row exists, a folder or start
// failure comes back in the result's "error", since the settings page is
// where the human fixes what was wrong.
func botCreate(rec botRecord, instructions string, start bool) (map[string]any, error) {
	rec.ID, rec.LastSessionID, rec.Stopped, rec.AvatarImage = 0, "", !start, ""
	if err := rec.normalize(); err != nil {
		return nil, botModelErr{err, http.StatusBadRequest}
	}
	b, err := botBackend(rec.Host)
	if err != nil {
		return nil, botModelErr{err, http.StatusBadGateway}
	}
	if len(instructions) > botInstructionsMax {
		return nil, botModelErr{errors.New("instructions are longer than 256 KB"), http.StatusBadRequest}
	}
	if err := insertBot(&rec); err != nil {
		return nil, botModelErr{err, http.StatusConflict}
	}
	// The instructions go in first, so botMaterialize finds the file and
	// writes no stub.
	note, err := botSeedInstructions(b, &rec, instructions)
	if err != nil {
		return map[string]any{"bot": botStatus(b, &rec, nil), "error": err.Error()}, nil
	}
	if err := botMaterialize(b, &rec); err != nil {
		return map[string]any{"bot": botStatus(b, &rec, nil), "error": err.Error()}, nil
	}
	if start {
		if err := startBot(b, &rec, true); err != nil {
			return map[string]any{"bot": botStatus(b, &rec, nil), "error": err.Error()}, nil
		}
	}
	out := map[string]any{"bot": botStatus(b, &rec, readClaudeSessions(b))}
	if note != "" {
		out["note"] = note
	}
	return out, nil
}

// botSeedInstructions writes a new bot's instructions file from the create
// form. It never replaces one already in the folder (a bot pointed at an
// existing folder keeps its file) and says so instead.
func botSeedInstructions(b Backend, r *botRecord, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	dir := expandTildeOn(b, r.Dir)
	path := filepath.Join(dir, botInstructionsFile(r))
	if _, err := b.Stat(path); err == nil {
		return "the folder already has " + botInstructionsFile(r) + ", which was kept", nil
	}
	if err := b.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := b.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", botInstructionsFile(r), err)
	}
	return "", nil
}

func serveBotOne(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	switch r.Method {
	case http.MethodGet:
		v := botStatus(b, rec, readClaudeSessions(b))
		dir := expandTildeOn(b, rec.Dir)
		botLoadChannelToken(rec)
		// The server lasso adds for the bot's own settings tools, shown as a
		// read-only connection: "" when it adds none (another host, MCP_OAUTH,
		// or a server of the human's own named lasso).
		lassoMCP := ""
		if url, ok := botLassoMCP(rec); ok && !slices.ContainsFunc(rec.MCP, func(m botMCPServer) bool { return m.Name == "lasso" }) {
			lassoMCP = url
		}
		writeJSON(w, map[string]any{
			"bot":      v,
			"dir_path": dir,
			"launch":   botLaunchScript(b, rec, dir, botEnvKeys(b, dir)),
			// The instructions file: CLAUDE.md for claude, AGENTS.md for omp.
			"claude_md": filepath.Join(dir, botInstructionsFile(rec)),
			"lasso_mcp": lassoMCP,
			// lasso's channel, shown as a read-only connection when the bot gets it.
			"lasso_channel": rec.channelToken != "" && slices.Contains(rec.channelGrants(), botChannelName),
		})
	case http.MethodPut:
		in, err := decodeBotInput(r, *rec)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		next := in.botRecord
		// Identity and runtime state are not editable here.
		next.ID, next.Host, next.Name = rec.ID, rec.Host, rec.Name
		next.LastSessionID, next.Stopped, next.CreatedAt = rec.LastSessionID, rec.Stopped, rec.CreatedAt
		next.AvatarImage = rec.AvatarImage
		if err := next.normalize(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if next.Runtime != rec.Runtime && botRunning(b, rec) {
			http.Error(w, "stop the bot before changing its runtime", http.StatusConflict)
			return
		}
		if err := updateBot(&next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if next.Runtime != rec.Runtime {
			// The other runtime's session is no conversation for this one.
			next.LastSessionID = ""
			if err := setBotSession(next.Name, ""); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := botMaterialize(b, &next); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if in.Restart {
			if botRunning(b, &next) {
				if err := restartBot(b, &next, false); err != nil {
					http.Error(w, err.Error(), http.StatusBadGateway)
					return
				}
			}
		}
		writeJSON(w, map[string]any{"bot": botStatus(b, &next, readClaudeSessions(b))})
	case http.MethodDelete:
		// ?folder=1 also deletes the bot's folder.
		out, err := botDelete(b, rec, r.URL.Query().Get("folder") == "1")
		if err != nil {
			http.Error(w, err.Error(), err.(botModelErr).status)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET, PUT or DELETE", http.StatusMethodNotAllowed)
	}
}

// botDelete forgets a bot, for the API and delete_bot alike. withFolder also
// deletes its folder, checked before anything is touched so a refused folder
// leaves the bot as it was. It works from any state: a running bot is stopped
// first (marked stopped before its pane closes, so keep-running cannot bring
// it back in between), and then the row goes.
func botDelete(b Backend, rec *botRecord, withFolder bool) (map[string]any, error) {
	dir := expandTildeOn(b, rec.Dir)
	if withFolder {
		if err := botFolderRemovable(b, dir); err != nil {
			return nil, botModelErr{err, http.StatusBadRequest}
		}
	}
	if botRunning(b, rec) {
		if err := stopBot(b, rec); err != nil {
			return nil, botModelErr{fmt.Errorf("stop it before deleting: %w", err), http.StatusBadGateway}
		}
	}
	if err := deleteBot(rec.Name); err != nil {
		return nil, botModelErr{err, http.StatusInternalServerError}
	}
	out := map[string]any{"ok": true, "dir": rec.Dir}
	if withFolder {
		// The bot is already forgotten, so a failure here is reported, not
		// a reason to answer an error.
		if err := b.RemoveAll(dir); err != nil {
			out["folder_error"] = err.Error()
		} else {
			out["folder_deleted"] = true
		}
	}
	return out, nil
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
		// lasso's own OAuth credentials are the Connections tab's business.
		shown := vars[:0]
		for _, v := range vars {
			if !strings.HasPrefix(v.Key, botOAuthKeyPrefix) {
				shown = append(shown, v)
			}
		}
		writeJSON(w, map[string]any{"vars": shown, "fnox_file": botFnoxFile(dir)})
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
	if r.isOmp() {
		return botOmpRunning(b, r)
	}
	_, ok := findBotPane(b, r)
	return ok
}

// botFolderRemovable refuses a folder that deleting with its bot would be a
// disaster for: the root, the host's home or anything above it, lasso's own
// directory or anything above it, or something that is not a directory. What
// is left is the human's choice, made in the delete dialog with the path in
// front of them.
func botFolderRemovable(b Backend, dir string) error {
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) || strings.Count(dir, "/") < 2 {
		return fmt.Errorf("not deleting %s: too close to the root", dir)
	}
	within := func(inner, outer string) bool {
		return inner == outer || strings.HasPrefix(inner+"/", outer+"/")
	}
	if home, err := b.HomeDir(); err == nil && home != "" && within(filepath.Clean(home), dir) {
		return fmt.Errorf("not deleting %s: it holds the home directory", dir)
	}
	if b.Name() == "local" && within(filepath.Clean(lassoDir()), dir) {
		return fmt.Errorf("not deleting %s: it holds lasso's own directory", dir)
	}
	st, err := b.Stat(dir)
	if err != nil {
		return fmt.Errorf("the folder %s: %w", dir, err)
	}
	if !st.IsDir() {
		return fmt.Errorf("not deleting %s: not a directory", dir)
	}
	return nil
}

// botLaunchScript is the generated launch task, as the Launch tab shows it.
func botLaunchScript(b Backend, r *botRecord, dir string, envKeys []string) string {
	if r.isOmp() {
		sock, _ := botOmpSocketOn(b, r.Name)
		return botOmpTaskScript(r, dir, sock, envKeys)
	}
	return botTaskScript(r, dir, envKeys)
}

// serveBotModel changes a bot's model and thinking level, the chat header's
// pickers: POST {model?, thinking?}. A running omp bot switches at once over
// RPC (and lasso keeps the change in its settings when omp reports it); a
// claude bot, or a stopped one, saves it for its next start.
func serveBotModel(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Model    *string `json:"model"`
		Thinking *string `json:"thinking"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	next, restart, err := botSetModel(b, rec, in.Model, in.Thinking)
	if err != nil {
		http.Error(w, err.Error(), err.(botModelErr).status)
		return
	}
	writeJSON(w, map[string]any{
		"bot":            botStatus(b, next, readClaudeSessions(b)),
		"restart_needed": restart,
	})
}

type botModelErr struct {
	error
	status int
}

// botSetModel is the chat header's model and thinking pickers, for the API
// and set_bot_model alike: a running omp bot switches at once over RPC, and
// anything else is saved for the next start (restart_needed when it runs).
// Errors are botModelErr, carrying the HTTP status.
func botSetModel(b Backend, rec *botRecord, model, thinking *string) (*botRecord, bool, error) {
	next := *rec
	if model != nil {
		next.Model = strings.TrimSpace(*model)
	}
	if thinking != nil {
		next.Effort = strings.TrimSpace(*thinking)
	}
	if err := next.normalize(); err != nil {
		return nil, false, botModelErr{err, http.StatusBadRequest}
	}
	live := false
	if rec.isOmp() && ompBotFor(rec.Name).ensure() && ompBotFor(rec.Name).snapshot().ready {
		m, t := "", ""
		if next.Model != rec.Model && next.Model != "" {
			m = next.Model
		}
		if next.Effort != rec.Effort && next.Effort != "" {
			t = next.Effort
		}
		if err := botOmpSetModel(rec.Name, m, t); err != nil {
			return nil, false, botModelErr{err, http.StatusBadGateway}
		}
		// Blank means "the default", which a running session cannot go back
		// to: that takes a restart.
		live = (next.Model != "" || next.Model == rec.Model) && (next.Effort != "" || next.Effort == rec.Effort)
	}
	if err := updateBot(&next); err != nil {
		return nil, false, botModelErr{err, http.StatusInternalServerError}
	}
	if err := botMaterialize(b, &next); err != nil {
		return nil, false, botModelErr{err, http.StatusBadGateway}
	}
	if live {
		ompBotFor(rec.Name).refreshState()
	}
	return &next, !live && botRunning(b, &next), nil
}

// serveBotSessions lists a bot's conversations (GET), starts a new one (POST
// new) and resumes an earlier one (POST resume {id}).
func serveBotSessions(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord, op string) {
	if op == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET", http.StatusMethodNotAllowed)
			return
		}
		list := botSessions(b, rec)
		if list == nil {
			list = []botSessionInfo{}
		}
		writeJSON(w, map[string]any{"sessions": list})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var err error
	switch op {
	case "new":
		err = botNewSession(b, rec)
	case "resume":
		var in struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil || strings.TrimSpace(in.ID) == "" {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		err = botResumeSession(b, rec, strings.TrimSpace(in.ID))
	}
	switch {
	case errors.Is(err, errBotBusy):
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

// serveBotChat is an omp bot's chat: it has a session file and a socket but
// no pane, so it cannot use /api/chat. A claude bot's chat stays there.
func serveBotChat(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord, op string) {
	if !rec.isOmp() {
		http.Error(w, "a claude bot's chat is its pane's: /api/chat", http.StatusBadRequest)
		return
	}
	if op == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "GET", http.StatusMethodNotAllowed)
			return
		}
		out := botOmpChat(b, rec, r.URL.Query().Get("before"))
		out.PendingUI = ompBotFor(rec.Name).snapshot().pending
		writeChat(w, out)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Text      string  `json:"text"`
		ID        string  `json:"id"`
		Value     *string `json:"value"`
		Confirmed *bool   `json:"confirmed"`
		Cancelled bool    `json:"cancelled"`
		// Meta rides a send: what the client says about the message (how it
		// was input), shown on its row in the chat (botmeta.go).
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var err error
	switch op {
	case "send":
		if strings.TrimSpace(in.Text) == "" {
			http.Error(w, "text is required", http.StatusBadRequest)
			return
		}
		meta, merr := decodeBotMeta(in.Meta)
		if merr != nil {
			http.Error(w, merr.Error(), http.StatusBadRequest)
			return
		}
		sentAt := time.Now()
		if err = botOmpSend(rec.Name, in.Text, true); err == nil && meta != nil {
			if serr := storeBotMeta(rec.Name, in.Text, sentAt, meta); serr != nil {
				log.Printf("bots: %s: storing a message's meta failed: %v", rec.Name, serr)
			}
		}
	case "stop":
		if !ompBotFor(rec.Name).snapshot().streaming {
			writeJSON(w, map[string]any{"outcome": "refused", "detail": "the bot is not working"})
			return
		}
		err = botOmpAbort(rec.Name)
	case "answer":
		err = botOmpAnswer(rec.Name, in.ID, in.Value, in.Confirmed, in.Cancelled)
	}
	if err != nil {
		writeJSON(w, map[string]any{"outcome": "refused", "detail": err.Error()})
		return
	}
	outcome := "sent"
	if op == "send" {
		outcome = chatSent
	}
	writeJSON(w, map[string]any{"outcome": outcome})
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

// skillDescription is the frontmatter's description, if there is one: on its
// own line, or a YAML block scalar (description: > / |) on the indented lines
// after it.
func skillDescription(md string) string {
	if !strings.HasPrefix(md, "---") {
		return ""
	}
	lines := strings.Split(md, "\n")[1:]
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			break
		}
		v, ok := strings.CutPrefix(line, "description:")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v != "" && strings.Trim(v, ">|-+") != "" {
			return previewText(strings.Trim(v, `"'`))
		}
		var body []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "---" || (next != "" && next[0] != ' ' && next[0] != '\t') {
				break
			}
			body = append(body, strings.TrimSpace(next))
		}
		return previewText(strings.Join(body, " "))
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
		if err := botAddSkill(b, root, in.From); err != nil {
			http.Error(w, err.Error(), err.(botModelErr).status)
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

// botAddSkill copies the skill directory from (on the bot's host) into root,
// the bot's .claude/skills, for the API and add_bot_skill alike.
func botAddSkill(b Backend, root, from string) error {
	src := filepath.Clean(expandTildeOn(b, strings.TrimSpace(from)))
	if !filepath.IsAbs(src) {
		return botModelErr{errors.New("from must be an absolute path or start with ~/"), http.StatusBadRequest}
	}
	name := filepath.Base(src)
	if !botSkillNameRE.MatchString(name) {
		return botModelErr{errors.New("not a skill directory name"), http.StatusBadRequest}
	}
	if _, err := b.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return botModelErr{errors.New("no SKILL.md in " + src), http.StatusBadRequest}
	}
	dst := filepath.Join(root, name)
	if _, err := b.Stat(dst); err == nil {
		return botModelErr{errors.New("the bot already has a skill named " + name), http.StatusConflict}
	}
	if err := copyTree(b, src, dst, &copyBudget{files: 2000, bytes: 50 << 20}); err != nil {
		_ = b.RemoveAll(dst)
		return botModelErr{err, http.StatusBadRequest}
	}
	return nil
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

// --- OAuth ---------------------------------------------------------------------

func serveBotOAuth(w http.ResponseWriter, r *http.Request, b Backend, rec *botRecord) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"servers": listBotOAuth(rec.Name)})
	case http.MethodPost:
		var in struct {
			Server string `json:"server"`
			Origin string `json:"origin"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		start, err := startBotOAuth(rec, in.Server, in.Origin)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, start)
	case http.MethodDelete:
		if err := signOutBotOAuth(b, rec, r.URL.Query().Get("server")); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "restart_needed": botRunning(b, rec)})
	default:
		http.Error(w, "GET, POST or DELETE", http.StatusMethodNotAllowed)
	}
}

// serveBotOAuthReturn completes a sign-in: the authorization server's
// redirect (callback, a page in the browser tab it opened) or a pasted
// address (finish, JSON for the settings page).
func serveBotOAuthReturn(w http.ResponseWriter, r *http.Request, callback bool) {
	var q url.Values
	if callback {
		q = r.URL.Query()
	} else {
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		u, err := url.Parse(strings.TrimSpace(in.URL))
		if err != nil || u.RawQuery == "" {
			http.Error(w, "paste the whole address from the browser's address bar, with its ?code=…&state=…", http.StatusBadRequest)
			return
		}
		q = u.Query()
	}
	p, err := finishBotOAuth(q)
	if !callback {
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "bot": p.bot, "server": p.server})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	title, body := "Signed in", "You can close this tab and go back to lasso."
	if err != nil {
		title, body = "Sign-in failed", err.Error()
	} else {
		body = p.server + " is connected for " + p.bot + ". " + body
	}
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title>
<body style="font:15px system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 16px;color:#222;background:#fafafa">
<h1 style="font-size:20px">%s</h1><p>%s</p>
<script>try{window.opener&&window.opener.postMessage({lassoBotOAuth:%t},location.origin)}catch(e){}</script>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(body), err == nil)
}

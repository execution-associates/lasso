package main

// Bots: long-lived Claude Code sessions lasso defines, launches and keeps
// running (docs/design/bots.md). A bot is a row here plus a folder on its host:
//
//	<dir>/CLAUDE.md            the bot's instructions (a real file, edited in place)
//	<dir>/.claude/skills/      its project skills
//	<dir>/mise.toml            its env — plain and age-encrypted — written ONLY by
//	                           the mise CLI (botenv.go), never parsed here
//	<dir>/.mise/tasks/bot      the launch script, generated from this row on every
//	                           save (botTaskScript)
//	<dir>/.lasso/mcp.json      its MCP servers, generated (botMCPJSON)
//
// The whole launch is `mise run bot` in <dir>: mise loads the env (decrypting
// secrets in memory), then runs the task, which execs claude with the bot's
// flags. That one short command is also what herdr types to bring the bot back
// after a restart (resume_argv, botloop.go), so a restored bot gets its secrets
// and its channel grants exactly as a fresh launch does.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const botsSchema = `
CREATE TABLE IF NOT EXISTS bots (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	host            TEXT NOT NULL,
	name            TEXT NOT NULL UNIQUE,
	dir             TEXT NOT NULL,
	workspace       TEXT NOT NULL DEFAULT '',
	model           TEXT NOT NULL DEFAULT '',
	effort          TEXT NOT NULL DEFAULT '',
	permission_mode TEXT NOT NULL DEFAULT '',
	mcp             TEXT NOT NULL DEFAULT '[]',
	strict_mcp      INTEGER NOT NULL DEFAULT 0,
	extra_args      TEXT NOT NULL DEFAULT '[]',
	avatar          TEXT NOT NULL DEFAULT '',
	keep_running    INTEGER NOT NULL DEFAULT 1,
	stopped         INTEGER NOT NULL DEFAULT 0,
	last_session_id TEXT NOT NULL DEFAULT '',
	position        INTEGER NOT NULL DEFAULT 0,
	created_at      TEXT NOT NULL,
	updated_at      TEXT NOT NULL
);
`

// botMCPServer is one entry of a bot's .lasso/mcp.json. Channel marks a server
// that is also a Claude Code channel: it gets its own
// --dangerously-load-development-channels grant on the launch line.
type botMCPServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"` // stdio | http | sse
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Channel bool              `json:"channel,omitempty"`
	// OAuth: lasso signs the bot in (botoauth.go) and claude reads the token
	// through headersHelper. The rest are for servers without dynamic client
	// registration: a pre-registered client id, the redirect it was registered
	// with, and the scope to ask for.
	OAuth         bool   `json:"oauth,omitempty"`
	OAuthClientID string `json:"oauth_client_id,omitempty"`
	OAuthRedirect string `json:"oauth_redirect,omitempty"`
	OAuthScope    string `json:"oauth_scope,omitempty"`
}

type botRecord struct {
	ID             int64          `json:"id"`
	Host           string         `json:"host"`
	Name           string         `json:"name"`
	Dir            string         `json:"dir"`
	Workspace      string         `json:"workspace"`
	Model          string         `json:"model"`
	Effort         string         `json:"effort"`
	PermissionMode string         `json:"permission_mode"`
	MCP            []botMCPServer `json:"mcp"`
	StrictMCP      bool           `json:"strict_mcp"`
	ExtraArgs      []string       `json:"extra_args"`
	Avatar         string         `json:"avatar"`
	KeepRunning    bool           `json:"keep_running"`
	// Stopped is the human's Stop: keep-running leaves a stopped bot alone
	// until Start clears it.
	Stopped       bool   `json:"stopped"`
	LastSessionID string `json:"last_session_id"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// botDefaultWorkspace is the herdr workspace a bot's tab goes into when its
// row names none.
const botDefaultWorkspace = "Bots"

var (
	// The name is the herdr agent name, the --name, the tab label and the
	// default folder, so it stays lowercase and path- and shell-safe.
	botNameRE      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	botModelRE     = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{0,80}$`)
	botMCPNameRE   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	botEnvKeyRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	botPermissions = []string{"", "acceptEdits", "auto", "bypassPermissions", "manual", "dontAsk", "plan"}
)

var errBotNotFound = errors.New("bot not found")

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// normalize fills defaults and validates the row. It never touches the host.
func (r *botRecord) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	if !botNameRE.MatchString(r.Name) {
		return fmt.Errorf("name must be 1-40 lowercase letters, digits or dashes, starting with a letter or digit")
	}
	// Words the API and the view's URLs use for themselves (/api/bots/order,
	// /bots/manage).
	switch r.Name {
	case "order", "skill-library", "manage", "new", "oauth":
		return fmt.Errorf("%q is reserved; pick another name", r.Name)
	}
	if r.Host == "" {
		r.Host = "local"
	}
	r.Dir = strings.TrimSpace(r.Dir)
	if r.Dir == "" {
		r.Dir = "~/bots/" + r.Name
	}
	if !strings.HasPrefix(r.Dir, "/") && !strings.HasPrefix(r.Dir, "~/") {
		return fmt.Errorf("folder must be an absolute path or start with ~/")
	}
	if r.Dir == "/" || r.Dir == "~/" || hasControl(r.Dir) || strings.ContainsRune(r.Dir, '\'') {
		return fmt.Errorf("folder %q is not usable", r.Dir)
	}
	r.Workspace = strings.TrimSpace(r.Workspace)
	if r.Workspace == "" {
		r.Workspace = botDefaultWorkspace
	}
	if len(r.Workspace) > 64 || hasControl(r.Workspace) {
		return fmt.Errorf("workspace must be at most 64 characters")
	}
	r.Model = strings.TrimSpace(r.Model)
	if !botModelRE.MatchString(r.Model) {
		return fmt.Errorf("model %q is not a model name", r.Model)
	}
	if e := strings.TrimSpace(r.Effort); e != "" {
		if r.Effort = normalizeEffort("claude", e); r.Effort == "" {
			return fmt.Errorf("effort %q is not a claude effort level", e)
		}
	}
	okPerm := false
	for _, p := range botPermissions {
		if r.PermissionMode == p {
			okPerm = true
		}
	}
	if !okPerm {
		return fmt.Errorf("permission mode %q is not one claude accepts", r.PermissionMode)
	}
	if len(r.ExtraArgs) > 32 {
		return fmt.Errorf("at most 32 extra arguments")
	}
	for _, a := range r.ExtraArgs {
		if a == "" || len(a) > 256 || hasControl(a) {
			return fmt.Errorf("extra argument %q is not usable", a)
		}
	}
	if r.ExtraArgs == nil {
		r.ExtraArgs = []string{}
	}
	if len([]rune(r.Avatar)) > 8 || hasControl(r.Avatar) {
		return fmt.Errorf("avatar must be at most 8 characters")
	}
	if len(r.MCP) > 32 {
		return fmt.Errorf("at most 32 MCP servers")
	}
	seen := map[string]bool{}
	for i := range r.MCP {
		s := &r.MCP[i]
		if !botMCPNameRE.MatchString(s.Name) {
			return fmt.Errorf("MCP server name %q must be letters, digits, _ or -", s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("MCP server %q is listed twice", s.Name)
		}
		seen[s.Name] = true
		switch s.Type {
		case "", "stdio":
			s.Type = "stdio"
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("MCP server %q needs a command", s.Name)
			}
			s.URL, s.Headers = "", nil
		case "http", "sse":
			if !strings.HasPrefix(s.URL, "https://") && !strings.HasPrefix(s.URL, "http://") {
				return fmt.Errorf("MCP server %q needs an http(s) URL", s.Name)
			}
			s.Command, s.Args, s.Env = "", nil, nil
			if hasControl(s.OAuthClientID+s.OAuthRedirect+s.OAuthScope) || len(s.OAuthClientID) > 256 || len(s.OAuthScope) > 1024 {
				return fmt.Errorf("MCP server %q: bad OAuth settings", s.Name)
			}
			if s.OAuthRedirect != "" && !strings.HasPrefix(s.OAuthRedirect, "https://") && !strings.HasPrefix(s.OAuthRedirect, "http://") {
				return fmt.Errorf("MCP server %q: the OAuth redirect must be an http(s) URL", s.Name)
			}
		default:
			return fmt.Errorf("MCP server %q: type must be stdio, http or sse", s.Name)
		}
		for k := range s.Env {
			if !botEnvKeyRE.MatchString(k) {
				return fmt.Errorf("MCP server %q: env key %q is not a variable name", s.Name, k)
			}
		}
	}
	if r.MCP == nil {
		r.MCP = []botMCPServer{}
	}
	return nil
}

// channels is the bot's channel servers, in list order.
func (r *botRecord) channels() []string {
	var out []string
	for _, s := range r.MCP {
		if s.Channel {
			out = append(out, s.Name)
		}
	}
	return out
}

// --- the generated files ----------------------------------------------------

func botTaskPath(dir string) string    { return filepath.Join(dir, ".mise", "tasks", "bot") }
func botRestartPath(dir string) string { return filepath.Join(dir, ".mise", "tasks", "restart") }

// botRestartMarker is the file mise run restart leaves for the bot task to find.
func botRestartMarker(dir string) string { return filepath.Join(dir, ".lasso", "restart") }
func botMCPPath(dir string) string       { return filepath.Join(dir, ".lasso", "mcp.json") }

// botMCPJSON renders .lasso/mcp.json. Secrets never belong in it: a server that
// needs one names it as ${VAR}, which Claude Code expands from its own env —
// the env mise loaded from the bot's mise.toml.
//
// signedIn names the servers lasso holds OAuth tokens for: those get a
// headersHelper that prints the current access token (botOAuthHelper).
func botMCPJSON(r *botRecord, dir string, signedIn map[string]bool) []byte {
	servers := map[string]any{}
	for _, s := range r.MCP {
		e := map[string]any{"type": s.Type}
		if s.Type == "stdio" {
			e["command"] = s.Command
			e["args"] = append([]string{}, s.Args...)
			if len(s.Env) > 0 {
				e["env"] = s.Env
			}
		} else {
			e["url"] = s.URL
			if len(s.Headers) > 0 {
				e["headers"] = s.Headers
			}
			if s.OAuth && signedIn[s.Name] {
				e["headersHelper"] = botOAuthHelper(dir, s.Name)
			}
		}
		servers[s.Name] = e
	}
	out, _ := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	return append(out, '\n')
}

// botClaudeArgv is claude's argv for the bot, without the trailing "$@".
// dir is the expanded folder (the mcp.json path is absolute).
func botClaudeArgv(r *botRecord, dir string) []string {
	argv := []string{"claude", "--mcp-config", botMCPPath(dir)}
	if r.StrictMCP {
		argv = append(argv, "--strict-mcp-config")
	}
	// One grant per channel. `--channels` is accepted but delivers nothing for
	// server: entries and does not split on commas, so it is no substitute.
	for _, ch := range r.channels() {
		argv = append(argv, "--dangerously-load-development-channels", "server:"+ch)
	}
	if r.Model != "" {
		argv = append(argv, "--model", r.Model)
	}
	if r.Effort != "" {
		argv = append(argv, "--effort", r.Effort)
	}
	if r.PermissionMode != "" {
		argv = append(argv, "--permission-mode", r.PermissionMode)
	}
	argv = append(argv, "--name", r.Name)
	// lasso's note about how the bot runs, with any --append-system-prompt of
	// the human's own after it: claude takes one, so the two are joined.
	prompt := botSystemPrompt(r, dir)
	extra := []string{}
	for i := 0; i < len(r.ExtraArgs); i++ {
		a := r.ExtraArgs[i]
		if a == "--append-system-prompt" && i+1 < len(r.ExtraArgs) {
			prompt += "\n\n" + r.ExtraArgs[i+1]
			i++
			continue
		}
		if v, ok := strings.CutPrefix(a, "--append-system-prompt="); ok {
			prompt += "\n\n" + v
			continue
		}
		extra = append(extra, a)
	}
	argv = append(argv, "--append-system-prompt", prompt)
	return append(argv, extra...)
}

// botSystemPrompt tells the bot what it is and how it runs, so it can answer
// questions about itself and restart itself when its human asks.
func botSystemPrompt(r *botRecord, dir string) string {
	return fmt.Sprintf(`You are the bot %q: a long-lived Claude Code session that lasso manages. You run in a herdr pane (HERDR_PANE_ID) in %s, launched there with `+"`mise run bot`"+`. When herdr restarts it brings you back with `+"`mise run bot -- --resume <this session>`"+`, and lasso relaunches you if you stop unexpectedly, so this conversation outlives any one process.

- CLAUDE.md in that folder is your standing instructions. Project skills go in .claude/skills/ there.
- lasso generates .lasso/mcp.json, .mise/config.toml and .mise/tasks/ from its Bots settings and overwrites them on every save: change those through lasso, not by editing the files.
- Your environment and secrets come from fnox.toml through mise, and only reach you at launch.
- You CAN restart yourself, and it is safe: run `+"`mise run restart`"+` in %s with your shell tool. That is the supported way, made for exactly this; it is not killing a process or closing a pane by hand. It ends this session a few seconds after your turn finishes and starts it again in the same pane, on this same conversation. Do it whenever your human asks you to restart, and when you need a new skill, MCP server, setting or variable to take effect; say you are restarting before you run it.`,
		r.Name, dir, dir)
}

// botRestartScript renders .mise/tasks/restart: the bot restarting itself,
// with no lasso involved (a bot on another host may not reach one). It cannot
// leave a helper behind to relaunch claude — Claude Code reaps whatever a tool
// call starts, setsid or not — so it leaves a marker for the bot task, which
// is still running in the pane, and types /exit. Typed during the bot's own
// turn, /exit waits in the queue until that turn has finished.
func botRestartScript(dir string) string {
	return `#!/bin/sh
#MISE description="Restart this bot on the same conversation"
# Generated by lasso; it rewrites this file on every save.
[ -n "${HERDR_PANE_ID:-}" ] || { echo "not running in a herdr pane" >&2; exit 1; }
touch ` + shellQuote(botRestartMarker(dir)) + `
herdr pane send-text "$HERDR_PANE_ID" "/exit" && herdr pane send-keys "$HERDR_PANE_ID" Enter
echo "Restarting once this turn ends; the conversation continues afterwards."
`
}

// botTaskScript renders .mise/tasks/bot. It is POSIX sh because mise runs a
// file task as an executable, and everything a restored bot needs happens here
// rather than in lasso, so a herdr restart brings the bot back whole even while
// lasso is down: the herdr name (a restored pane comes back unnamed), and the
// two blocking dialogs an unattended launch cannot answer itself.
//
// envKeys are the fnox variables mise hands the task (and only the task).
// interactive keeps the terminal: a task that receives secrets otherwise gets
// its output redacted line by line and no stdin, and claude needs the TTY.
func botTaskScript(r *botRecord, dir string, envKeys []string) string {
	q := shellQuote
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\n#MISE description=%q\n", "Run the "+r.Name+" bot")
	if len(envKeys) > 0 {
		keys, _ := json.Marshal(envKeys)
		fmt.Fprintf(&b, "#MISE secrets=%s\n#MISE interactive=true\n", keys)
	}
	b.WriteString("# Generated by lasso from the bot's settings; lasso rewrites it on every save.\n")
	fmt.Fprintf(&b, "NAME=%s\n", q(r.Name))
	b.WriteString(`unset CLAUDECODE CLAUDE_CODE_CHILD_SESSION CLAUDE_CODE_SESSION_ID
if [ -n "${HERDR_PANE_ID:-}" ] && command -v herdr >/dev/null 2>&1; then
  # Claim the herdr agent name. It only succeeds once herdr has detected claude,
  # a few seconds after exec, so it retries.
  (
    for _ in $(seq 1 30); do
      herdr agent rename "$HERDR_PANE_ID" "$NAME" >/dev/null 2>&1 && break
      sleep 2
    done
  ) &
  # The folder-trust dialog starts on its DECLINE option, so Enter alone would
  # refuse it: move down, re-read, and confirm only on the Yes line.
  (
    for _ in $(seq 1 90); do
      if herdr pane read "$HERDR_PANE_ID" --source visible 2>/dev/null | grep -q 'Is this a project you created or one you trust'; then
        herdr pane send-keys "$HERDR_PANE_ID" Down >/dev/null 2>&1
        sleep 1
        if herdr pane read "$HERDR_PANE_ID" --source visible 2>/dev/null | grep -q '❯ Yes, I trust this folder'; then
          herdr pane send-keys "$HERDR_PANE_ID" Enter >/dev/null 2>&1
        fi
        break
      fi
      sleep 1
    done
  ) &
`)
	if len(r.channels()) > 0 {
		b.WriteString(`  # --dangerously-load-development-channels stops on a menu every launch, and
  # nothing pre-accepts it. Answer it only once its own wording is on screen.
  (
    for _ in $(seq 1 90); do
      if herdr pane read "$HERDR_PANE_ID" --source visible 2>/dev/null | grep -q 'I am using this for local development'; then
        herdr pane send-keys "$HERDR_PANE_ID" Enter >/dev/null 2>&1
        break
      fi
      sleep 1
    done
  ) &
`)
	}
	b.WriteString("fi\n")
	b.WriteString("# Launched without a TTY (mise run from a script), claude would read EOF as\n# an empty first turn.\n[ ! -t 0 ] && exec 0</dev/null\n")
	argv := botClaudeArgv(r, dir)
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = q(a)
	}
	quoted[0] = "claude"
	// "$@" last, so `mise run bot -- --resume <id>` (herdr's restore) wins.
	// Not exec'd: when the bot restarts itself (mise run restart) it leaves
	// the marker and exits, and this script — still the pane's foreground
	// job — relaunches the whole task, so a restart also picks up new env,
	// MCP servers and settings. A plain exit ends here as before.
	fmt.Fprintf(&b, "marker=%s\nrm -f \"$marker\"\n", q(botRestartMarker(dir)))
	fmt.Fprintf(&b, "%s \"$@\"\nstatus=$?\n", strings.Join(quoted, " "))
	b.WriteString(`if [ -f "$marker" ]; then
  rm -f "$marker"
  exec mise run bot -- --continue
fi
exit $status
`)
	return b.String()
}

// botLaunchCommand is what lasso types into a fresh pane, and resumeArgv is the
// same command as herdr stores it. Both stay short: the typed line has to fit
// maxTypedLaunch, and herdr refuses an argv with quotes or paths up front.
func botLaunchCommand(sessionID string) string {
	if sessionID == "" {
		return "mise run bot"
	}
	return "mise run bot -- --resume " + sessionID
}

func botResumeArgv(sessionID string) []string {
	return []string{"mise", "run", "bot", "--", "--resume", sessionID}
}

// validateResumeArgv mirrors herdr's validate_resume_argv (agent_resume.rs), so
// a report herdr would refuse is caught before it is sent.
func validateResumeArgv(argv []string) error {
	if len(argv) == 0 || len(argv) > 64 {
		return fmt.Errorf("resume argv must have 1-64 elements")
	}
	total := 0
	for _, a := range argv {
		total += len(a)
		if strings.ContainsRune(a, '\'') || hasControl(a) {
			return fmt.Errorf("resume argv element %q has a quote or control character", a)
		}
	}
	if total > 8192 {
		return fmt.Errorf("resume argv is longer than 8192 bytes")
	}
	first := argv[0]
	if first == "" || strings.HasPrefix(first, "-") {
		return fmt.Errorf("resume argv must start with a command name")
	}
	for _, c := range first {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return fmt.Errorf("resume argv must start with a plain command name, got %q", first)
		}
	}
	return nil
}

// botMaterialize writes the bot's folder on its host: lasso's mise config, a
// fnox.toml if there is none, the generated task and mcp.json, and a CLAUDE.md
// if there is none. It never overwrites CLAUDE.md, skills, fnox.toml or the
// bot's own mise.toml.
func botMaterialize(b Backend, r *botRecord) error {
	dir := expandTildeOn(b, r.Dir)
	for _, d := range []string{dir, filepath.Dir(botTaskPath(dir)), filepath.Dir(botMCPPath(dir))} {
		if err := b.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := botEnvInit(b, dir); err != nil {
		return err
	}
	if err := botWriteTask(b, r, dir); err != nil {
		return err
	}
	signedIn := map[string]bool{}
	for name, row := range listBotOAuth(r.Name) {
		signedIn[name] = row.Status != ""
	}
	if err := b.WriteFile(botMCPPath(dir), botMCPJSON(r, dir, signedIn), 0o644); err != nil {
		return fmt.Errorf("write mcp.json: %w", err)
	}
	md := filepath.Join(dir, "CLAUDE.md")
	if _, err := b.Stat(md); err != nil {
		stub := "# " + r.Name + "\n\nDescribe what this bot does, who it works for, and what it must never do.\n"
		if err := b.WriteFile(md, []byte(stub), 0o644); err != nil {
			return fmt.Errorf("write CLAUDE.md: %w", err)
		}
	}
	return nil
}

// botWriteTask regenerates .mise/tasks/bot, granting the variables fnox.toml
// defines now. Every env change calls it, or a new variable never reaches
// the bot.
func botWriteTask(b Backend, r *botRecord, dir string) error {
	for path, body := range map[string]string{
		botTaskPath(dir):    botTaskScript(r, dir, botEnvKeys(b, dir)),
		botRestartPath(dir): botRestartScript(dir),
	} {
		if err := b.WriteFile(path, []byte(body), 0o755); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		if _, ok := b.(*localBackend); ok {
			// os.WriteFile applies the mode only when it creates the file.
			_ = os.Chmod(path, 0o755)
		}
	}
	return nil
}

// --- storage -----------------------------------------------------------------

const botCols = `id, host, name, dir, workspace, model, effort, permission_mode, mcp, strict_mcp, extra_args, avatar, keep_running, stopped, last_session_id, created_at, updated_at`

func scanBot(row interface{ Scan(...any) error }) (*botRecord, error) {
	var r botRecord
	var mcp, extra string
	var strict, keep, stopped int
	if err := row.Scan(&r.ID, &r.Host, &r.Name, &r.Dir, &r.Workspace, &r.Model, &r.Effort, &r.PermissionMode,
		&mcp, &strict, &extra, &r.Avatar, &keep, &stopped, &r.LastSessionID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.StrictMCP, r.KeepRunning, r.Stopped = strict != 0, keep != 0, stopped != 0
	_ = json.Unmarshal([]byte(mcp), &r.MCP)
	_ = json.Unmarshal([]byte(extra), &r.ExtraArgs)
	if r.MCP == nil {
		r.MCP = []botMCPServer{}
	}
	if r.ExtraArgs == nil {
		r.ExtraArgs = []string{}
	}
	return &r, nil
}

func listBots() ([]*botRecord, error) {
	// The human's order (dragged in the list), then creation order.
	rows, err := db.Query(`SELECT ` + botCols + ` FROM bots ORDER BY position, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*botRecord
	for rows.Next() {
		r, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func getBot(name string) (*botRecord, error) {
	r, err := scanBot(db.QueryRow(`SELECT `+botCols+` FROM bots WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errBotNotFound
	}
	return r, err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func insertBot(r *botRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	r.CreatedAt, r.UpdatedAt = now, now
	mcp, _ := json.Marshal(r.MCP)
	extra, _ := json.Marshal(r.ExtraArgs)
	// A new bot goes to the end of the list.
	res, err := db.Exec(`INSERT INTO bots (host, name, dir, workspace, model, effort, permission_mode, mcp, strict_mcp, extra_args, avatar, keep_running, stopped, last_session_id, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM bots), ?, ?)`,
		r.Host, r.Name, r.Dir, r.Workspace, r.Model, r.Effort, r.PermissionMode, string(mcp), boolInt(r.StrictMCP),
		string(extra), r.Avatar, boolInt(r.KeepRunning), boolInt(r.Stopped), r.LastSessionID, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return fmt.Errorf("a bot named %q already exists", r.Name)
		}
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

// updateBot saves the editable fields. Host and name are identity and never
// change here; last_session_id has its own writer.
func updateBot(r *botRecord) error {
	r.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	mcp, _ := json.Marshal(r.MCP)
	extra, _ := json.Marshal(r.ExtraArgs)
	_, err := db.Exec(`UPDATE bots SET dir = ?, workspace = ?, model = ?, effort = ?, permission_mode = ?, mcp = ?, strict_mcp = ?,
		extra_args = ?, avatar = ?, keep_running = ?, updated_at = ? WHERE name = ?`,
		r.Dir, r.Workspace, r.Model, r.Effort, r.PermissionMode, string(mcp), boolInt(r.StrictMCP),
		string(extra), r.Avatar, boolInt(r.KeepRunning), r.UpdatedAt, r.Name)
	return err
}

func setBotSession(name, sessionID string) error {
	_, err := db.Exec(`UPDATE bots SET last_session_id = ? WHERE name = ?`, sessionID, name)
	return err
}

func setBotStopped(name string, stopped bool) error {
	_, err := db.Exec(`UPDATE bots SET stopped = ? WHERE name = ?`, boolInt(stopped), name)
	return err
}

// reorderBots stores the list's order: names first, in that order, then any
// bot the list did not name (one created meanwhile) after them.
func reorderBots(names []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE bots SET position = position + ?`, len(names)+1); err != nil {
		return err
	}
	for i, n := range names {
		if _, err := tx.Exec(`UPDATE bots SET position = ? WHERE name = ?`, i+1, n); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func deleteBot(name string) error {
	_, _ = db.Exec(`DELETE FROM bot_mcp_oauth WHERE bot = ?`, name)
	_, err := db.Exec(`DELETE FROM bots WHERE name = ?`, name)
	return err
}

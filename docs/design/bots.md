# Bots

A bot is a long-lived Claude Code session that lasso defines, launches in herdr and keeps running. Examples are an assistant with mail and chat channels, or a watcher on a schedule. The Bots view lists them like a messaging app, and each one's settings page edits what it runs with.

Code:

- `bots.go`: the row, validation and the generated files.
- `botenv.go`: mise env and secrets.
- `claudesessions.go`: Claude Code's session registry.
- `botruntime.go`: launch, stop, status and the loop.
- `botsapi.go`: `/api/bots`.
- `mcp_bots.go`: `list_bots`, `start_bot`, `stop_bot` and `restart_bot`.

The frontend is `BotsView.tsx`, `BotSettings.tsx`, `BotsManage.tsx` and `BotParts.tsx`, with `lib/bots.ts` (the list poll, unread) and the `/bots/…` routes in `lib/url.ts`.

## What a bot is

**A row in `lasso.db`.** The `bots` table holds the name, host, folder, herdr workspace, model, effort, permission mode, MCP servers, extra args, avatar, keep-running, stopped and the last session id.

**A folder on its host,** default `~/bots/<name>`, where claude runs:

| file | owner | what |
|---|---|---|
| `CLAUDE.md` | the human | instructions. Created as a stub once and never overwritten by a save |
| `.claude/skills/` | the human | project skills, copied in from `~/.claude/skills` or a path |
| `mise.toml` | the mise CLI | env vars, plain and age-encrypted |
| `.mise/tasks/bot` | lasso | the launch script, regenerated from the row on every save |
| `.lasso/mcp.json` | lasso | the MCP servers, regenerated on every save |

The whole launch is `mise run bot` in the folder. mise loads the env, decrypting secrets in memory, and then the task execs claude with these flags:

- `--mcp-config .lasso/mcp.json`
- `--dangerously-load-development-channels server:<x>`, one per channel
- `--model`, `--effort`, `--permission-mode` and `--name <bot>`
- the bot's extra args
- `"$@"` last, so `-- --resume <id>` wins

## Invariants

- **The same short command launches and restores.** herdr restores a pane by typing its `resume_argv` into a fresh shell in the pane's saved folder. It restores no env and allows no path or quote in the argv. `mise run bot -- --resume <id>` fits all of that and is the full launch, so a restored bot comes back with its secrets and channel grants. A bare `claude --resume` would bring it back deaf.
- **The task script does the unattended parts itself**, so a restore needs no lasso:
  - **Claim the herdr agent name,** with retries. A restored pane comes back unnamed, and the rename only succeeds after herdr detects claude.
  - **Answer the folder-trust dialog.** It starts on its decline option, so the script moves Down, re-reads the screen and presses Enter only on the `❯ Yes` line.
  - **Answer the development-channel menu.** It fires on every launch, and nothing pre-accepts it.

  Each answer matches the dialog's own wording before sending a key, so a blind keystroke can never answer a permission prompt.
- **One grant per channel.** `--channels` is accepted, but it delivers nothing for `server:` entries and doesn't split on commas. A missing grant still subscribes and acks, and then hears nothing.
- **`--mcp-config`, not a project `.mcp.json`.** A project file asks a human to approve it on every fresh start. Secrets for a server are written as `${VAR}`, which Claude Code expands from the env mise loaded, so `mcp.json` never holds one.
- **Env goes only through the mise CLI.** Lasso never parses or writes the TOML:
  - plain values: `mise set --file`
  - secrets: `mise set --age-encrypt --stdin KEY`, value on stdin, never argv
  - listing: `mise set`, filtered to rows whose source is this folder's file, so a secret reads `[redacted]` and its value never reaches a client
  - setup: `mise settings set --local experimental true`, because age values need it, then `mise trust`

  The host's age identity is `~/.config/mise/age.txt`. Lasso creates it only when a human clicks, and never replaces one, since that would orphan every secret already encrypted to it.
- **Resume is reported by lasso, not by a hook.** On each tick the loop reports the bot's session to herdr with `pane.report_agent_session`:
  - fields: source `lasso`, agent `claude` (herdr's detected agent, never the bot name), a rising `seq` (nanoseconds), and the argv above
  - herdr refuses the report until it has detected claude in the pane, and refuses a seq-less report once it holds one from this source; a refusal is retried on the next tick
  - `validateResumeArgv` mirrors herdr's rules: at most 64 elements and 8192 bytes, no `'` or control characters, and a plain first element
- **The session id comes from Claude Code's own registry,** `~/.claude/sessions/<pid>.json`, matched on the bot's `--name` and folder for a live pid. The fallback is herdr's `agent_session`. The registry's `status` (`busy`/`shell` → working, `waiting` → blocked, `idle`) and `waitingFor` also drive the list's state and its "Needs your input" line. The `.key` files beside it, the bearer tokens for each session's cc-socks inbox, are never opened.
- **Messages are typed into the pane** (`chatSubmit`), as the chat view does, so the bot reads them as its user's own words. cc-socks delivers every frame inside a `<cross-session-message>` peer envelope with a permission-laundering warning, and can hold or refuse it.
- **Identity is the name, never a pane id.** A bot's pane is resolved by `agent.get {target: name}`, and the answer's name must match. The fallback is the claude pane in the bot's folder, for a restored pane whose rename hasn't landed. Pane ids change on every relaunch and restore.
- **Bots are not agent records.** They live in their own table, so the agent reaper, which tombstones by pane id, never touches them.
- **Keep-running waits a minute.** A keep-running bot is relaunched only after its pane has been missing for 6 consecutive ticks (10s each), and never within 90s of lasso launching it. That gives herdr's own restore the first chance, rather than racing it with a second copy. **Stop** sets `stopped` and closes the pane, and keep-running leaves a stopped bot alone until Start.
- **One lasso runs the loop.** Two lassos on one `lasso.db` (titan's dev and production) would both relaunch the same bot, so the loop belongs to whoever holds the flock on `<lassoDir>/bots.runner.lock`. Interactive Start and Stop work from either.
- **Creating and editing stay human-only.** The MCP tools can list, start, stop and restart a bot. Its MCP servers, env, secrets and CLAUDE.md decide what it can reach, so those are edited only in the Bots view, as plugin approval is.

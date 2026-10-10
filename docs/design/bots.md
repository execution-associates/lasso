# Bots

A bot is a long-lived Claude Code session that lasso defines, launches in herdr and keeps running. Examples are an assistant with mail and chat channels, or a watcher on a schedule. The Bots view lists them like a messaging app, and each one's settings page edits what it runs with.

Code:

- `bots.go`: the row, validation and the generated files.
- `botenv.go`: env and secrets (fnox, through mise).
- `botkey.go`: lasso's own age identity.
- `claudesessions.go`: Claude Code's session registry.
- `botruntime.go`: launch, stop, status and the loop.
- `botsapi.go`: `/api/bots`.
- `botavatar.go`: the bot's picture.
- `mcp_bots.go`: `list_bots`, `get_bot`, `update_bot`, `set_bot_env`, `unset_bot_env`, `set_bot_avatar`, `start_bot`, `stop_bot` and `restart_bot`.

The frontend is `BotsView.tsx`, `BotSettings.tsx`, `BotsManage.tsx` and `BotParts.tsx`, with `lib/bots.ts` (the list poll, unread) and the `/bots/…` routes in `lib/url.ts`. The Bots app is `public/manifest-bots.json`, the manifest swap in `main.tsx` and `BotsApp` in `App.tsx`. Notifications ride the shared Web Push path (`notifications.md`), with `sw.js` honouring the payload's `url` and `icon`.

## What a bot is

**A row in `lasso.db`.** The `bots` table holds the name, host, folder, herdr workspace, model, effort, permission mode, MCP servers, extra args, avatar, the picture (`avatar_image`), notify, keep-running, stopped and the last session id.

**A folder on its host,** default `~/bots/<name>`, where claude runs:

| file | owner | what |
|---|---|---|
| `CLAUDE.md` | the human | instructions. Created as a stub once and never overwritten by a save |
| `.claude/skills/` | the human | project skills, copied in from `~/.claude/skills` or a path |
| `fnox.toml` | the fnox CLI | env vars, each held by a provider. Written once by lasso, then only through `fnox` |
| `.mise/config.toml` | lasso | `min_version`, experimental mode and `[secrets.fnox]`, rewritten on every save |
| `.mise/tasks/bot` | lasso | the launch script, regenerated from the row on every save and on every env change |
| `mise.toml` | the human | optional (tools, say). Lasso never writes or trusts it |
| `.lasso/mcp.json` | lasso | the MCP servers, plus lasso's own as `lasso` (below), regenerated on every save |
| `.lasso/avatar.<ext>` | lasso | the picture, written by `storeBotAvatar` only |

The whole launch is `mise run bot` in the folder. mise asks fnox for the variables the task lists (`#MISE secrets=[…]`) and hands them to that task alone, and the task execs claude with these flags:

- `--mcp-config .lasso/mcp.json`
- `--dangerously-load-development-channels server:<x>`, one per channel
- `--model`, `--effort`, `--permission-mode` and `--name <bot>`
- the bot's extra args
- `--append-system-prompt`: lasso's note on how the bot runs (below)
- `"$@"` last, so `-- --resume <id>` wins

## Invariants

- **The same short command launches and restores.** herdr restores a pane by typing its `resume_argv` into a fresh shell in the pane's saved folder. It restores no env and allows no path or quote in the argv. `mise run bot -- --resume <id>` fits all of that and is the full launch, so a restored bot comes back with its secrets and channel grants. A bare `claude --resume` would bring it back deaf.
- **The task script does the unattended parts itself**, so a restore needs no lasso:
  - **Claim the herdr agent name,** with retries. A restored pane comes back unnamed, and the rename only succeeds after herdr detects claude.
  - **Answer the folder-trust dialog.** It starts on its decline option, so the script moves Down, re-reads the screen and presses Enter only on the `❯ Yes` line.
  - **Answer the development-channel menu.** It fires on every launch, and nothing pre-accepts it.

  Each answer matches the dialog's own wording before sending a key, so a blind keystroke can never answer a permission prompt.
- **One grant per channel.** `--channels` is accepted, but it delivers nothing for `server:` entries and doesn't split on commas. A missing grant still subscribes and acks, and then hears nothing.
- **`--mcp-config`, not a project `.mcp.json`.** A project file asks a human to approve it on every fresh start. Secrets for a server are written as `${VAR}`, which Claude Code expands from the env mise handed the task, so `mcp.json` never holds one.
- **Env is fnox's, resolved by mise.** mise (≥ 2026.10.4) asks fnox (≥ 1.39) for the keys a task lists, and hands them to that task only. The shell, `mise env` and other tasks never see them. `botCheckTools` refuses an older host and names the upgrade command.
  - **Lasso writes `fnox.toml` once,** with two providers: `lasso` (age, `key_file` = lasso's key, the default) and `plain`. After that it touches the file only through the fnox CLI: `fnox set KEY [--provider plain]` with the value on stdin, never argv, and `fnox remove KEY`. That leaves a human free to add a provider (1Password, a vault, a keychain) and make it the default, and lasso keeps working.
  - **Listing** is `fnox list --full --sources`, a fixed-width table sliced by its header. Only this folder's rows are kept, since fnox also lists the global config's, and only a `plain` provider's key is shown, because for that provider it is the value. A secret's value never reaches a client.
  - **The task carries `#MISE interactive=true`.** A task that receives secrets otherwise gets its output redacted line by line and no stdin, and claude needs the TTY. **Every env change regenerates the task's `secrets` list,** or a new variable never reaches the bot. A running bot sees the change on its next start.
  - **Lasso's age identity** is `<lassoDir>/age.txt` (0600) on each host, generated natively (X25519 and bech32, as age-keygen writes it, so no age binary is needed) the first time a bot there gets a fnox.toml. It is one per install, never baked into the binary, where it would be public. It is never replaced, since that would orphan every secret encrypted to it.
  - **Lasso trusts only its own `.mise/config.toml`** (`mise trust`). A bot folder's own `mise.toml` is the human's to trust.
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
- **MCP OAuth: lasso is the client, Claude Code only reads the token** (`botoauth.go`). Claude Code's own sign-in keeps tokens in one credentials file keyed by server URL, so every session on a host would share one account per service. Instead, lasso runs the MCP authorization flow:
  - the steps: the 401's `resource_metadata`, then the well-known protected-resource document, then RFC 8414 metadata, then dynamic registration, then PKCE S256 with a `resource` indicator
  - the redirect: back to lasso's own `/api/bots/oauth/callback`, on the origin the browser sent (`location.origin`, since behind a proxy the server cannot tell)

  **Fallbacks and storage:**
  - **Localhost-only servers:** when registration refuses lasso's redirect, it registers `http://localhost:53682/callback` instead, and the human pastes the address the browser failed to load into `POST /api/bots/oauth/finish`.
  - **State:** a pending sign-in is single-use, held in memory and expires after 15 minutes.
  - **Secrets:** the access token, refresh token and client secret go to the bot's fnox under `LASSO_OAUTH_<SERVER>_*`. `bot_mcp_oauth` in `lasso.db` holds only what is not secret: client id, endpoints, expiry and status.
  - **How claude gets the token:** the server's `headersHelper` runs `fnox get` on the access token. Claude Code runs it on every connection and again after a 401, and the bot loop refreshes tokens within 5 minutes of expiry. The `LASSO_OAUTH_` keys are kept out of the task's `secrets` grant, so the refresh token never reaches claude's environment, and the Environment tab hides them.
  - **Token check:** a token with a quote, backslash, space or non-ASCII character is refused, because the helper prints it inside JSON.
- **Every bot is told how it runs.** lasso always passes `--append-system-prompt` with a short note. It covers who the bot is, that it runs via `mise run bot` in a herdr pane and comes back after restarts, which files lasso regenerates, where its env comes from, how to configure itself, and how to restart itself. A human's own `--append-system-prompt` in the extra args is joined onto it, since claude takes only one. The note says outright that restarting is safe and supported: without that, a bot under a user CLAUDE.md that forbids killing processes declined to do it.
- **A bot restarts itself without a helper process.** Claude Code reaps everything a tool call starts, `setsid` included, so nothing spawned from inside the bot can outlive it to relaunch it. So:
  - **The task waits instead of exec'ing.** `.mise/tasks/bot` runs claude and waits for it.
  - **`mise run restart`** (`.mise/tasks/restart`) touches `.lasso/restart` and types `/exit`, which queues behind the current turn.
  - **On exit with the marker present,** the task re-runs `mise run bot -- --continue`: the whole task, so new env, MCP servers and settings take effect. A plain exit ends the task as before.
- **A bot configures itself through lasso's MCP.** A human asks the bot in its chat ("switch to Opus", "add this server") rather than opening its settings, so the bot needs a way in:
  - **The tools:** `get_bot` reads the whole configuration (env keys without secret values, project skills, OAuth status). `update_bot` changes only the fields passed (model, effort, permission mode, MCP servers, strict MCP, extra args, keep-running, notifications, avatar, workspace), with `mcp` the complete list that replaces the current one, and answers `restart_needed`. `set_bot_env`/`unset_bot_env` go through the same fnox path as the Environment tab and refuse `LASSO_OAUTH_*`. `set_bot_avatar` takes a file on the bot's host. Every tool resolves the bot through `callerFrom(req).requireHost`, and a bot out of reach reads exactly like a missing one.
  - **The injected server:** `botMCPJSON` adds `lasso`, an http server at this lasso's own listen address + `/mcp` (`botLassoMCP`, a wildcard bind rewritten to `127.0.0.1`). It is added only for a bot on `local`, since loopback on another host is somewhere else, and never while `MCP_OAUTH` gates `/mcp`, since a bot has no credential to present. A server the human named `lasso` wins.
  - **The system prompt** names the tools and the bot's own name to pass. It says that `CLAUDE.md` and `.claude/skills/` are files it edits directly, that an OAuth sign-in is the human's in the Bots view, and that settings, servers and variables take effect after `mise run restart`.
  - **Creating and deleting stay human-only.** A new bot's host, folder and first grants decide what it can reach, and deleting forgets it. Those are approvals, as a plugin's are, so no tool does them, and OAuth sign-in needs the human's browser anyway.
- **A picture is raster only, told by its bytes** (`botavatar.go`). PNG, JPEG, WebP or GIF, at most 2 MB, typed by `http.DetectContentType` rather than by name or the client's header, and never SVG. An SVG is a document that can carry script, and `/api/bots/<name>/avatar` serves it from lasso's own origin. GET re-sniffs before serving and sends `nosniff` and `default-src 'none'`. PUT takes the raw image as the body and DELETE clears it. The file is `.lasso/avatar.<ext>` in the bot's folder, with any other extension removed. The row's `avatar_image` holds the file name plus a `?v=` revision, so the URL can be cached as immutable. The text avatar stays as the fallback.
- **A settings save decodes over the current row.** `PUT /api/bots/<name>` decodes into a copy of the stored record, so a field the body omits keeps its value, and a client that predates a field cannot reset it. Identity, runtime state and `avatar_image` are copied back from the stored row whatever the body says. A create decodes over the defaults (keep-running and notify on).
- **A bot's answer is a notification** (`botNotifyCheck`, from the bot loop). It fires when a bot with `notify` on is idle and its newest prose row is its own, with a timestamp newer than the last one told about.
  - **Baseline:** the first sighting of each bot after lasso starts only records that timestamp, so a restart does not replay every bot's last answer.
  - **Payload:** kind `bot_message`, title the bot's name, body the message preview, tag `bot:<name>` so a run of answers leaves the newest, `url` `/bots/<name>`, and `icon` the picture's URL when there is one.
  - **Delivery:** it goes to every registered device, through the same path as a blocked agent. Since only one lasso runs the loop, it is sent once.
  - **Skip:** the service worker drops one whose chat is focused and visible in some window, except on iOS, where a push that shows nothing costs the origin its permission.
- **The Bots app is a second manifest for the same origin.** Under `/bots`, `main.tsx` points the manifest link at `/manifest-bots.json` (id and scope `/bots`, start URL `/bots?app=bots`, name "Lasso Bots") and the iOS title at "Bots" before render. It is done client-side so it works under Vite as well as from the embedded bundle, and is in place before a browser reads it at install time. App mode is decided once at boot: a `/bots` path in a standalone window, or `?app=bots`, renders `BotsApp`, the Bots view alone with no Shell, footer or terminal. An installed app is its own device to the browser, with its own permission and push subscription, so the list header carries a bell (`NotifyBell`) that runs `enablePush`/`disablePush` from the click.
- **The compact layout follows the view's own width.** `BotsView` measures itself with a `ResizeObserver` and folds the list away below 760 px. A wide right sidebar or a narrow window then gets the phone layout as well, which a viewport query would miss. Folded, the list is its own page, the other pages carry **‹ Bots**, and the chat's title becomes `BotSwitcher`: every bot with its state, then All bots, Manage bots and New bot.

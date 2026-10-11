# Bots

A bot is a long-lived agent session that lasso defines, launches and keeps running. Its **runtime** is `omp` (the default for a new bot: omp in RPC mode under lasso's bot host, see "omp bots" below) or `claude` (Claude Code in a herdr pane; everything else on this page up to "omp bots" describes that runtime). Examples are an assistant with mail and chat channels, or a watcher on a schedule. The Bots view lists them like a messaging app, and each one's settings page edits what it runs with.

Code:

- `bots.go`: the row, validation and the generated files.
- `botomp_files.go`: an omp bot's generated files, system prompt and job-event message.
- `bothost.go`: `lasso bot-host`, the process that owns an omp bot's pipes.
- `botomp.go`: lasso's side of an omp bot: the socket connection, start/stop, status, keep-running and job delivery.
- `botattach.go`: `lasso bot-attach`, an omp bot's herdr pane.
- `botenv.go`: env and secrets (fnox, through mise).
- `botkey.go`: lasso's own age identity.
- `claudesessions.go`: Claude Code's session registry.
- `botruntime.go`: launch, stop, status and the loop.
- `botsapi.go`: `/api/bots`.
- `botavatar.go`: the bot's picture.
- `mcp_bots.go`: `list_bots`, `get_bot`, `update_bot`, `set_bot_env`, `unset_bot_env`, `set_bot_avatar`, `start_bot`, `stop_bot` and `restart_bot`.
- `botjobs.go`: jobs (schedules and webhooks), their queue, the scheduler, `/hooks/bots/…` and `/bot-channel/…`.
- `botchannel.go`: `lasso channel`, the stdio channel server claude runs.
- `cron.go`: the 5-field cron parser and next-fire calculation.
- `mcp_botjobs.go`: `list_bot_jobs`, `create_bot_job`, `update_bot_job`, `delete_bot_job` and `run_bot_job`.

The frontend is `BotsView.tsx`, `BotSettings.tsx`, `BotJobs.tsx` (with `lib/cron.ts`), `BotsManage.tsx` and `BotParts.tsx`, with `lib/bots.ts` (the list poll, unread) and the `/bots/…` routes in `lib/url.ts`. The Bots app is `public/manifest-bots.json`, the manifest swap in `main.tsx` and `BotsApp` in `App.tsx`. Notifications ride the shared Web Push path (`notifications.md`), with `sw.js` honouring the payload's `url` and `icon`.

## What a bot is

**A row in `lasso.db`.** The `bots` table holds the name, runtime, host, folder, herdr workspace, model, effort, permission mode, MCP servers, extra args, the picture (`avatar_image`), notify, keep-running, stopped and the last session id.

**A folder on its host,** default `~/bots/<name>`, where claude runs:

| file | owner | what |
|---|---|---|
| `CLAUDE.md` | the human | instructions. Created as a stub once and never overwritten by a save |
| `.claude/skills/` | the human | project skills, copied in from `~/.claude/skills` or a path |
| `fnox.toml` | the fnox CLI | env vars, each held by a provider. Written once by lasso, then only through `fnox` |
| `.mise/config.toml` | lasso | `min_version`, experimental mode and `[secrets.fnox]`, rewritten on every save |
| `.mise/tasks/bot` | lasso | the launch script, regenerated from the row on every save and on every env change |
| `mise.toml` | the human | optional (tools, say). Lasso never writes or trusts it |
| `.lasso/mcp.json` | lasso | the MCP servers, plus lasso's own as `lasso` and `lasso-channel` (below), regenerated on every save. Mode 0600: it holds the channel token |
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
- **A mode is a launch task, not a second generator.** `launch_task` (`""` = `bot`) names the mise task every start goes through: lasso's typed launch, keep-running's relaunch, the `resume_argv` it reports (`mise run <task> -- --resume <id>`) and the self-restart's relaunch (`mise run <task> -- --continue`), so a restore or restart never drops the bot out of its mode. lasso writes only `bot` and `restart`. A mode is the human's own task that sets an environment and ends `exec mise run bot -- "$@"`: it nests (env, secrets, the terminal and the args all pass through, measured), so the grants and dialogs stay in one generated place. A save naming a task that `mise tasks ls --json` does not list in the folder is refused, which keeps keep-running from relaunching a command that fails. A folder mise cannot list is not refused. The name is a plain word (it rides in `resume_argv` and a typed line) and never `restart`. **A provider switch needs a fresh session:** resumed on DeepSeek, an Anthropic session failed with a 422 on a `tool_definition` content block, while a fresh one answered.
- **Every bot is told how it runs.** lasso always passes `--append-system-prompt` with a short note. It covers who the bot is, that it runs via `mise run bot` in a herdr pane and comes back after restarts, which files lasso regenerates, where its env comes from, how to configure itself, and how to restart itself. A human's own `--append-system-prompt` in the extra args is joined onto it, since claude takes only one. The note says outright that restarting is safe and supported: without that, a bot under a user CLAUDE.md that forbids killing processes declined to do it.
- **A bot restarts itself without a helper process.** Claude Code reaps everything a tool call starts, `setsid` included, so nothing spawned from inside the bot can outlive it to relaunch it. So:
  - **The task waits instead of exec'ing.** `.mise/tasks/bot` runs claude and waits for it.
  - **`mise run restart`** (`.mise/tasks/restart`) touches `.lasso/restart` and types `/exit`, which queues behind the current turn.
  - **On exit with the marker present,** the task re-runs its launch task, `mise run <launch task> -- --continue`: the whole task, so new env, MCP servers and settings take effect. A plain exit ends the task as before.
- **A bot configures itself through lasso's MCP.** A human asks the bot in its chat ("switch to Opus", "add this server") rather than opening its settings, so the bot needs a way in:
  - **The tools:** `get_bot` reads the whole configuration (env keys without secret values, project skills, OAuth status). `update_bot` changes only the fields passed (model, effort, permission mode, MCP servers, strict MCP, extra args, keep-running, notifications, workspace), with `mcp` the complete list that replaces the current one, and answers `restart_needed`. `set_bot_env`/`unset_bot_env` go through the same fnox path as the Environment tab and refuse `LASSO_OAUTH_*`. `set_bot_avatar` takes a file on the bot's host. Every tool resolves the bot through `callerFrom(req).requireHost`, and a bot out of reach reads exactly like a missing one.
  - **The injected server:** `botMCPJSON` adds `lasso`, an http server at this lasso's own listen address + `/mcp` (`botLassoMCP`, a wildcard bind rewritten to `127.0.0.1`). It is added only for a bot on `local`, since loopback on another host is somewhere else, and never while `MCP_OAUTH` gates `/mcp`, since a bot has no credential to present. A server the human named `lasso` wins.
  - **The system prompt** names the tools and the bot's own name to pass. It says that `CLAUDE.md` and `.claude/skills/` are files it edits directly, that an OAuth sign-in is the human's in the Bots view, and that settings, servers and variables take effect after `mise run restart`.
  - **Creating and deleting stay human-only.** A new bot's host, folder and first grants decide what it can reach, and deleting forgets it. Those are approvals, as a plugin's are, so no tool does them, and OAuth sign-in needs the human's browser anyway.
- **Delete works from any state.** `DELETE /api/bots/<name>` stops a running bot first through `stopBot`, which marks it stopped before closing its pane, so keep-running cannot relaunch it between the close and the row going. The settings footer and the list's right-click menu share one confirmation (`DeleteBotDialog`). Its **Also delete its folder** checkbox (off every time the dialog opens, the path shown) sends `?folder=1`: `botFolderRemovable` refuses the root's neighbours, the host's home or anything above it, lasso's own directory or anything above it, and a non-directory, all before the bot is touched; the folder goes only after the bot is stopped and forgotten, and a failure then is reported as `folder_error` rather than undoing the delete.
- **A picture is raster only, told by its bytes** (`botavatar.go`). PNG, JPEG, WebP or GIF, at most 2 MB, typed by `http.DetectContentType` rather than by name or the client's header, and never SVG. An SVG is a document that can carry script, and `/api/bots/<name>/avatar` serves it from lasso's own origin. GET re-sniffs before serving and sends `nosniff` and `default-src 'none'`. PUT takes the raw image as the body and DELETE clears it. The file is `.lasso/avatar.<ext>` in the bot's folder, with any other extension removed. The row's `avatar_image` holds the file name plus a `?v=` revision, so the URL can be cached as immutable. Without a picture the avatar is the name's first letter: there is no text avatar. The row keeps an `avatar` text column that nothing shows.
- **A settings save decodes over the current row.** `PUT /api/bots/<name>` decodes into a copy of the stored record, so a field the body omits keeps its value, and a client that predates a field cannot reset it. Identity, runtime state and `avatar_image` are copied back from the stored row whatever the body says. A create decodes over the defaults (keep-running and notify on).
- **A bot's answer is a notification** (`botNotifyCheck`, from the bot loop). It fires when a bot with `notify` on is idle and its newest prose row is its own, with a timestamp newer than the last one told about.
  - **Baseline:** the first sighting of each bot after lasso starts only records that timestamp, so a restart does not replay every bot's last answer.
  - **Payload:** kind `bot_message`, title the bot's name, body the message preview, tag `bot:<name>` so a run of answers leaves the newest, `url` `/bots/<name>`, and `icon` the picture's URL when there is one.
  - **Delivery:** it goes to every registered device, through the same path as a blocked agent. Since only one lasso runs the loop, it is sent once.
  - **Skip:** the service worker drops one whose chat is focused and visible in some window, except on iOS, where a push that shows nothing costs the origin its permission.
- **The Bots app is a second manifest for the same origin.** Under `/bots`, `main.tsx` points the manifest link at `/manifest-bots.json` (id and scope `/bots`, start URL `/bots?app=bots`, name "Lasso Bots") and the iOS title at "Bots" before render. It is done client-side so it works under Vite as well as from the embedded bundle, and is in place before a browser reads it at install time. App mode is decided once at boot: a `/bots` path in a standalone window, or `?app=bots`, renders `BotsApp`, the Bots view alone with no Shell, footer or terminal. An installed app is its own device to the browser, with its own permission and push subscription, so the list header carries a bell (`NotifyBell`) that runs `enablePush`/`disablePush` from the click.
- **The compact layout follows the view's own width.** `BotsView` measures itself with a `ResizeObserver` and folds the list away below 760 px. A wide right sidebar or a narrow window then gets the phone layout as well, which a viewport query would miss. Folded, the list is its own page, the other pages carry **‹ Bots**, and the chat's title becomes `BotSwitcher`: every bot with its state, then All bots, Manage bots and New bot.

## Jobs

A job is a message lasso delivers into a bot's session on a schedule, from a webhook, or when run by hand. Jobs replace a per-bot everloop: they live in `lasso.db`, so a bot needs no timers or spool of its own, and changes need no restart.

- **The channel is lasso's own binary, spawned by claude.** Claude Code channels are stdio MCP servers that the session starts itself, so neither `/mcp` nor any HTTP route can be one. `botMCPJSON` adds `lasso-channel` (`lasso channel --bot <name>`, this binary's resolved path) with `LASSO_URL` (lasso's loopback address) and `LASSO_CHANNEL_TOKEN` in its env, and the launch line grants it like any channel. The process declares `experimental["claude/channel"]`, offers no tools, long-polls `GET /bot-channel/<bot>/next` (25s), writes each event as `notifications/claude/channel` and acks it. Its MCP is hand-rolled JSON-RPC, since the SDK has no way to declare the capability or send that method.
- **The token is per bot,** made on first need (`bots.channel_token`) and kept, so a running channel survives a lasso restart. `/bot-channel/` is exempt from UI_AUTH and checks only that token, so the channel works under `MCP_OAUTH` too, unlike the `lasso` server. `.lasso/mcp.json` is written 0600 because it holds it.
- **Local bots only.** The channel reaches lasso over loopback, the same reason `lasso` is only added for `local`. A job for another host's bot is refused (`botChannelOffered`), and a server of the bot's own named `lasso-channel` wins.
- **Delivery is claim, notify, ack: at least once.** `bot_events` is both the queue and the history. A claim that is never acked is handed out again after 60s. The event carries `job`, `trigger` (`schedule`, `run` or `webhook`), `event_id`, `count` and `fired_at` as tag attributes, so a redelivery is recognisable.
- **Firings merge.** A schedule or Run now whose job already has an undelivered (not yet claimed) schedule/run event bumps its `count` instead of queuing another, so a busy or disconnected bot gets one event, not a backlog. Webhooks never merge (each body differs). At most 50 undelivered events per bot (the oldest go), and anything not picked up within a day is dropped.
- **A stopped bot's firings are dropped and logged,** never queued, so starting a bot after a week does not replay a week of sweeps. A webhook to a paused job is accepted and logged as dropped.
- **The scheduler is the bot loop's** (`botJobsTick` from `botTick`), so only the lasso holding `bots.runner.lock` fires schedules. `next_at` is stored per job. A fire missed while lasso was down fires once on the next tick, and the schedule carries on from now. Webhooks and Run now queue from whichever lasso answers, and the channel's long-poll looks at the db every 2s as well as waking on its own lasso's events.
- **The runner rewrites each local bot's task and mcp.json once when it takes the loop** (`botSyncChannelFiles`), so a bot saved before jobs existed, or one whose mcp.json names a lasso binary an update replaced, gets the current channel on its next start without a save.
- **Schedules are 5-field cron in an IANA zone,** several expressions joined with `;` so the builder can say "7:47 AM and 9:15 AM". Vixie rules: both day fields restricted means either matches. A time a spring-forward gap skips does not fire that day, and one a fall-back repeats fires once. `time/tzdata` is embedded. The Jobs tab never shows cron unless the human picks Custom: `lib/cron.ts` writes it from the builder and reads it back as a sentence. The server's `jobs/preview` is the authority on validity and next fires.
- **A one-time run (`once_at`) is the alternative to cron,** for a date 5-field cron cannot name (it has no year; a yearly job would fire again). Stored as UTC, entered as wall-clock time in the job's zone. It fires once, late if lasso was down at the time, and then has no `next_at`; the job stays, enabled, as a record. A new or changed one in the past is refused, but a save that leaves an already-fired one alone is not.
- **A webhook's key is its only credential.** `POST /hooks/bots/<bot>/<job>` is exempt from UI_AUTH; the key comes as `?key=` or a bearer token, compared in constant time. A wrong key, a missing job and a job without a webhook all answer 404. The body (≤64 KB, UTF-8) is appended to the message under a line saying it came from the caller, and the channel's instructions tell the bot to treat it as data, never instructions.

### Watches (both runtimes)

A job with a `command` is a watch (`botjobcmd.go`): everloop's `--command` semantics, moved into lasso so a bot needs no timers of its own. Change detection is cheap and belongs in a script; the bot's judgement is expensive and should run only when there is something to judge.

- **Silence is the feature.** Each firing runs the command; exit 0 with no stdout delivers nothing and adds nothing to the history. The run is still recorded on the job (`last_run_at`, `last_run_result`, `last_run_exit`, `last_run_ms`, `last_run_note`), which is what lets the card say "checked 3m ago, quiet".
- **Output is an event, and runs accumulate.** Exit 0 with output queues an event whose body is the job's message (a standing preamble) and then the output. A run that finds the job's watch event still undelivered APPENDS to it under its own `[run N of M, time]` header (`bot_events.runs` keeps the runs, `content` is re-rendered): every firing carries different output, so overwriting would make a watch lie about what happened during an outage. Bounded at 20 runs / 64 KB per event and 16 KB per stream per run, oldest first out, the loss stated in the body. Plain jobs' merge-by-count and watch events never merge into each other. `count` is the number of runs, and the channel adds `watch="true"`.
- **Failures are damped.** A non-zero exit, a timeout, or a command that could not start counts toward `fail_streak`; only the 1st, 2nd, 4th, 8th… consecutive failure is reported, with stderr, and the first success after a reported failure adds one recovery notice. A failure-only event has no preamble ("judge this" applied to a stack trace is nonsense). `run_status` is the newest health-changing run's (`error`/`timeout`, or none after a recovery) and reaches the bot as `status`, so it reads a diagnostic, not an instruction.
- **One run per job at a time, in the background.** `botJobsTick` moves `next_at` on and starts the command in a goroutine (`startBotJobCommand`); a firing that finds the last run still going is skipped, like a systemd timer whose unit is still active. The command runs under `/bin/sh -c` in its own process group (a timeout kills the group, so a hung pipeline cannot hold the pipe) with `Pdeathsig`, so a dead lasso takes its watches with it. Timeout default 60s, at most an hour.
- **A stopped bot's watch does not run.** Most watches keep state and consume what they report; running one with nobody listening would lose the change. The firing is dropped and logged like any other.
- **Run now waits up to 20s** for the command and answers what came of it (`pending`, `quiet`, `failed`, `dropped`), else `running`; a second press while it runs is `busy`.
- **Webhooks ignore the command.** A webhook delivers its body after the message, as for any job.
- **The environment is a systemd user unit's, not lasso's** (`botCommandEnv`): `minimalChildEnv`'s allowlist plus `XDG_RUNTIME_DIR`, `DBUS_SESSION_BUS_ADDRESS`, `SSH_AUTH_SOCK` and `OP_VAULT`, PATH led by `~/.local/bin` and mise's shims, and `LASSO_BOT`/`LASSO_JOB`. Never `UI_AUTH`, `MCP_OAUTH` or `LASSO_MCP_TOKEN`, and never the bot's fnox env. Scripts fetch their own secrets (`secret KEY`), exactly as they did under everloop.
- **Trust: a watch is arbitrary shell as lasso's user.** Anyone who can create or edit a job (the Jobs tab, `/api/bots/<name>/jobs`, or `create_bot_job`/`update_bot_job` on `/mcp`, which a bot may call for itself) can run any command lasso's user can. That is the same trust as `/mcp`'s `create_agent` and a bot running with `bypassPermissions`, and it is why jobs sit behind UI_AUTH and `/mcp`'s gate like those do. A webhook caller cannot set or change a command; it only supplies a body.

## omp bots

An omp bot (runtime `omp`) is omp in RPC mode (`omp --mode rpc-ui`, JSONL on stdin and stdout). There is no terminal: lasso drives it over the protocol instead of typing into a pane and reading the screen.

```
lasso (either instance) --unix socket--> lasso bot-host (systemd user unit lasso-bot-<name>)
                                              '-- pipes --> mise run <task> [-- -c] --> exec omp --mode rpc-ui ...
```

**Its folder** (`botomp_files.go`) differs where omp differs from Claude Code:

| file | owner | what |
|---|---|---|
| `AGENTS.md` | the human | instructions, stubbed once. omp reads `AGENTS.md`; it does not read a root `CLAUDE.md` |
| `.omp/mcp.json` | lasso | omp's project MCP config: the bot's servers plus `lasso`, 0600 |
| `.mise/tasks/bot` | lasso | `exec omp --mode rpc-ui --session-dir .lasso/sessions …`, with the same `#MISE secrets`/`interactive` lines as a claude bot's |
| `.mise/tasks/restart` | lasso | touches `.lasso/restart` and nothing else |
| `.lasso/sessions/` | omp | the session files, `<time>_<session id>.jsonl` |

`fnox.toml`, `.mise/config.toml`, `.claude/skills/`, the avatar and `launch_task` modes work as for a claude bot.

**Invariants:**

- **The bot host owns the process, not lasso.** `lasso bot-host` (`bothost.go`) is started with `systemd-run --user --unit lasso-bot-<name> --collect`, with a systemd user unit's environment (`botCommandEnv`, never lasso's credentials). It survives lasso restarts, so a release never ends a turn. The unit name is the uniqueness lock: dev and production lasso cannot start two copies. With no user manager, start is refused with the reason; there is no detached-orphan fallback.
- **A host outlives the lasso that started it, so it says which protocol it speaks.** `host_hello` carries `protocol` (`botHostProtocol`) and the lasso `version` it runs. A host keeps the binary it was started from until it restarts, so after a release a running bot's host may be older than lasso. When the protocol differs, the bot's view reads `host_outdated` and the runner restarts the host onto the current binary once the bot is idle (ready, not streaming, no open question; `botOmpUpgrade`), continuing the session. It is not a Stop: the bot is never marked stopped. A per-bot lock keeps an upgrade from landing between a job prompt and its ack. **Bump `botHostProtocol` whenever a `host_*` frame or its meaning changes**; a version-only difference restarts nothing.
- **omp's RPC rides fds 3 and 4, never the task's stdin.** A task that receives secrets runs `interactive`, and mise connects no stdin to an interactive task whose output is not a terminal, so omp read EOF at once and exited 0 the moment a bot had a secret. The host passes a pipe pair as fds 3 and 4 with `LASSO_BOT_RPC=fd`; mise hands extra fds to the task unchanged, and the task script moves them onto omp's stdin and stdout only when that variable is set, so `mise run bot` by hand keeps its own stdio. mise's own output goes to the host's log. The host stops reading omp's stdout 2s after omp exits, so a process omp started that still holds it cannot hang the host.
- **The socket is the bot.** `<lassoDir>/bots/<name>.sock`, dir 0700 and socket 0600, because it is full control of the session. Client lines go to omp's stdin; omp's stdout lines go to every client, so both lassos on one `lasso.db` can watch and send. Responses are matched by id (`lasso-<pid>-<n>`), so each lasso ignores the other's.
- **A reconnecting lasso loses nothing.** On connect the host sends `host_hello`: ready, streaming, and every `extension_ui_request` (select, confirm, input, editor) not yet answered. An answer passing through drops it and is broadcast as `host_ui_answered`, so every open chat's card goes.
- **omp never sees herdr.** The host strips `HERDR_*` from omp's environment. herdr's omp extension would otherwise claim whatever pane those name, and herdr would run a second omp on the session when restoring that pane.
- **Self-restart is the host's.** `mise run restart` leaves `.lasso/restart`. Once omp is ready and not streaming, the host removes the marker, closes omp's stdin (omp's clean exit) and runs the launch task again with `-- -c`, so new env, servers and settings apply on the same session. Any other exit ends the host and the unit; keep-running is lasso's.
- **Stop is abort, EOF, then the group.** `host_stop` (or SIGTERM from `systemctl --user stop`) sends `abort`, closes stdin, and kills omp's process group after 5s. lasso falls back to `systemctl --user stop` after 8s.
- **Status comes from the protocol.** `agent_start` until a terminal `agent_end` is working; an open question is blocked, with its title as `waiting_for`; before omp's `ready` is starting. A prompt omp accepts marks the bot working at once (unless omp says `agentInvoked: false`), so a poll does not read idle before `agent_start`. The session id comes from `get_state`, asked on ready and after each turn.
- **The chat is the session file.** `/api/bots/<name>/chat` reads `<time>_<session id>.jsonl` with the chat view's omp parser (`fillChatLog`), and adds `pending_ui`. `chat/send` sends `prompt`, as a `steer` while the bot works (a human's words land mid-run); `chat/stop` sends `abort`; `chat/answer` sends the `extension_ui_response`. An approval is a `select` of Approve/Deny and is answered by label. The payload's `pane_id` is `bot:<name>`, so AskCard never types keystrokes for it.
- **Jobs are prompts.** The runner's tick runs a delivery loop per connected bot: `claimBotEvents`, one `prompt` per event (`followUp` while working), then `ackBotEvents` and `botChannelSeen`. A prompt omp does not accept leaves its events claimed, to be handed out again: at least once, as with the channel. The event's header is a plain line (`[lasso job "<name>" trigger=… event=… count=… fired_at=…]`), never a tag: omp treats XML tags in its context as system-authored, and a webhook body must not read as that. The instructions that the channel's initialize carries for claude are in the omp bot's system prompt instead.
- **Keep-running backs off.** A keep-running omp bot whose unit is not active is relaunched by the runner after 10s, then 20s, doubling to 5 minutes, reset once it has been up 10 minutes. There is no herdr restore to wait for.
- **send_agent reaches it by name.** A local omp bot named as `to` gets the same relayed message and reply inbox as one prompt (`sendToOmpBot`), refused while it is waiting on a question.
- **Extra args go on the omp command, minus the ones that would take it away from lasso.** `--mode`, `-p`/`--print`, `--no-session`, `--session-dir`, `-c`/`--continue`, `-r`/`--resume`, `--fork`, `--export`, `--cwd`, `--profile` and `--alias` are refused (`botOmpReservedArgs`); an `--append-system-prompt` is joined onto lasso's note.
- **Local only, runtime fixed while running.** An omp bot must be on `local` (the socket is on lasso's machine). A save that changes `runtime` is refused while the bot runs, and switching starts a new conversation, since the two runtimes keep different session files.
- **Signed-in MCP servers use omp's `!command` header.** omp has no `headersHelper`; the Authorization value is `!printf 'Bearer %s' "$(fnox … get LASSO_OAUTH_<S>_ACCESS)"`. omp also loads the user's own `~/.omp/agent/mcp.json` servers, so strict MCP does not apply to an omp bot.

### Model, thinking and context

- **What runs is omp's word, not the row's.** `get_state` (asked on ready, after every turn and on `model_changed` / `thinking_level_changed`) gives the model (name, provider/id, context window), the thinking levels that model takes, the current level and `contextUsage`. They reach the Bots view as `live_model`, `thinking`, `thinking_levels` (`off` + the model's rungs + `auto`) and `context`; a claude bot's view carries only its newest turn's tokens, since Claude Code reports no window.
- **Changing them.** `POST /api/bots/<name>/model {model?, thinking?}`: a running omp bot switches at once (`set_model {provider, modelId}`, `set_thinking_level {level}`; a model must be provider/model) and the row and task are saved; a claude or stopped bot only saves, and the answer's `restart_needed` lets the view offer a restart. The pane's `/model` and `/thinking` send the same RPCs.
- **One source of truth, whoever changed it.** lasso keeps the row in step with omp's change events (the web UI, the pane, the bot's own `/switch`), writing only the column that changed, serialised, so a save never puts back a value it predates. The first reading on a connection is a baseline (launch's resolution of an alias or the default is never pinned) unless `host_hello` says the model or thinking changed since omp launched (`model_changed` / `thinking_changed`, protocol 3): then it is a change made while no lasso was connected, and it is kept.
- **The pane's status line** sits above its input: model · thinking · ctx used/window % · state, redrawn in place on every change and cut to the terminal's width.

### omp bots in herdr

An omp bot has no terminal of its own, so lasso gives it a herdr pane running **`lasso bot-attach <name>`** (`botattach.go`), a viewer connected to the bot host's socket. That makes the bot a herdr agent like any other: the sidebar shows its state, `herdr agent prompt`/`read`/`wait`/`send-keys` work on it, and herdr's restore brings the pane back.

- **The viewer is the reporter.** It calls `pane.report_agent` (source `lasso:bot`, agent `omp`) on every state change, with `agent_session_id`, `agent_session_path` (the session file, so lasso's own chat overlay can read the pane too) and `resume_argv` `["lasso","bot-attach","<name>"]`. It renames the pane to the bot's name, which is what herdr's commands and `findBotPane` address, and calls `pane.release_agent` on exit. State: blocked while a question is open (the question as `message`), working from the moment it sends a prompt (herdr's `agent prompt --wait` gives up if it does not see working within seconds), unknown while the bot host is unreachable, else idle.
- **Agent `omp`, with `HERDR_AGENT=omp`.** herdr prompts only an agent it knows whose process is the pane's foreground, so a custom label is report-only (`agent_not_ready`). The viewer re-execs itself with `HERDR_AGENT=omp`, herdr's documented hint for a wrapper, so the foreground process counts. **herdr never starts its own omp in the pane:** the `resume_argv` reported with every state wins over herdr's built-in omp resume. Measured on an isolated herdr 0.9.3 server (separate socket and state): after `herdr server stop` and a fresh start, herdr re-ran `lasso bot-attach` in the restored pane, the bot still had exactly one omp (its host's), and the restored pane took prompts.
- **What the pane does.** It prints the end of the session file on connect, then each finished message and tool call as plain lines on the normal screen (so `agent read` sees the conversation). Input runs raw with bracketed paste on: a paste is one message, Enter sends a prompt (a steer while the bot works), Esc aborts, a question is answered by its number (or an option's label, or free text where it offers one), Ctrl-C closes the viewer and leaves the bot running. It reconnects by itself across a host restart or upgrade.
- **lasso owns the pane's lifecycle.** Start opens it in a tab of the bot's workspace (typed with this binary's own path; the `lasso` on PATH may be older), failure costing only the pane. Stop and Delete close it. A keep-running bot whose pane has been missing for `botMissesBeforeRelaunch` ticks gets it reopened, after herdr's own restore has had its chance. The bot's view carries the pane as `pane_id`, so the Bots view's terminal button opens it.
- **send_agent still goes over RPC**, never through the pane: a bot's name resolves to the bot before any pane lookup.


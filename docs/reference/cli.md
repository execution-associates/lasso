---
title: CLI
description: Every lasso subcommand, its flags and its aliases.
order: 96
---

The `lasso` binary is both the server and its own control surface. A bare `lasso`, or any invocation whose first argument starts with `-`, runs the server in the foreground. Any other first word is a subcommand; an unknown one prints the usage and exits 2.

```bash
lasso -h          # the subcommand list (also: lasso help, lasso --help)
lasso serve -h    # every server flag
```

## Summary

| command | aliases | what it does |
| --- | --- | --- |
| `lasso [flags]` | | Run the server in the foreground. |
| `lasso serve [flags]` | | Same, explicitly. |
| `lasso start [flags]` | `up` | Start the server in the background. |
| `lasso stop` | `down` | Stop the background server. |
| `lasso restart [flags]` | | Stop it if it is running, then start it. |
| `lasso status` | | Say whether the background server is running, and its URL. |
| `lasso update [--no-restart]` | | Update to the latest release. |
| `lasso doctor` | | Check the local install. |
| `lasso version` | `--version`, `-v` | Print the version. |
| `lasso connect [flags]` | | Register lasso's MCP servers with this machine's agent CLIs. |
| `lasso mcp [tool] [flags]` | | Call any of lasso's MCP tools from a shell. |
| `lasso notify [flags] <message>` | | Push a notification to the human running lasso. |
| `lasso open [flags] <path>` | | Show a file in the human's sidebar file viewer. |
| `lasso closeme` | | Close the calling agent itself. |
| `lasso mcp-client <cmd>` | | Manage per-host MCP credentials. |
| `lasso mcp-group <cmd>` | | Manage host groups. |
| `lasso plugin <cmd>` | `plugins` | Manage plugins. |

## Running the server

### `lasso` and `lasso serve`

Run the server in the foreground, logging to stderr. Both take the server flags listed in [Configuration](./configuration.md#server-flags). The server prints a `UI: http://<addr>` line once it has bound its port.

### `lasso start` / `lasso up`

Starts `lasso serve` detached in its own session, with any flags you pass forwarded to it, so the background server honors the same flags as a foreground one:

```bash
lasso start -listen 127.0.0.1:8090 -theme ocai
```

It writes the server's PID to `~/.lasso/lasso.pid` and its output to `~/.lasso/lasso.log` (truncated on each start), then waits up to five seconds for the `UI:` line and prints the URL. If a live PID is already recorded, it says so and does nothing. These two files are always under `~/.lasso`, even when `LASSO_DIR` points elsewhere.

### `lasso stop` / `lasso down`

Sends SIGTERM to the recorded PID, waits up to five seconds for it to exit, and removes the PID file. A stale PID file is cleaned up and reported as "not running".

### `lasso restart`

`lasso stop` (when a server is running) followed by `lasso start` with the flags you pass.

### `lasso status`

Prints `lasso: running (pid N) → <url>` or `lasso: stopped`. The URL is the last `UI:` line in `~/.lasso/lasso.log`. It only knows about a server started with `lasso start`; a systemd-supervised lasso reads as stopped.

## Maintenance

### `lasso update`

| flag | effect |
| --- | --- |
| `--no-restart` | Replace the binary but restart nothing. |

How it updates depends on how lasso was installed:

- **A release binary** (the install script, or a mise `ubi:` install): downloads the latest GitHub release for this platform, verifies it against the release's `checksums.txt`, and atomically replaces the running binary. It then restarts the `lasso start` daemon if its PID file is live, or, on Linux, any systemd service whose main process is this binary. When already up to date, it still restarts a systemd-run lasso left on an old binary by an earlier `--no-restart`.
- **A systemd-supervised git checkout** (a source checkout with a `.git`, run by an active `systemctl --user` unit): runs `git pull --ff-only` in the checkout and `systemctl --user restart <unit>`, which rebuilds from source. The checkout defaults to the binary's directory (`LASSO_SRC_DIR` overrides it) and the unit to `lasso` (`LASSO_SYSTEMD_UNIT` overrides it).

See [Updating](../deployment/updating.md) for the details.

### `lasso doctor`

Takes no flags. Prints one line per check, marked ✓ (pass), ⚠ (warn) or ✗ (fail), and exits 1 if any check failed. Only a missing herdr and an unwritable state directory are failures; everything else warns.

| check | fails or warns when |
| --- | --- |
| herdr binary | `herdr` is not on `PATH` (fails, with an install hint). Otherwise shows its version and path. |
| herdr daemon | The herdr socket (`HERDR_SOCKET_PATH`, else `~/.config/herdr/herdr.sock`) does not answer, or the running server is older than the installed binary (warns). Otherwise shows its version and protocol. |
| herdr protocol | The daemon's protocol differs from the one this lasso targets (warns: `herdr X speaks protocol N, lasso targets M — update one to match`). |
| agent integrations | herdr's integration is missing for any of `claude`, `codex`, `opencode`, `omp`, `pi` (warns). Install one with `herdr integration install <agent>`; without it herdr falls back to reading the screen to tell whether that agent is working, idle or blocked. |
| state dir | lasso's state directory (`~/.lasso`, or `$LASSO_DIR`) is not writable (fails). |
| lasso server | `127.0.0.1:8090` is held by a process other than a `lasso start` daemon (warns). Otherwise reports the daemon running or stopped. A lasso under systemd on the default port shows up as that other process. |
| lasso on PATH | The directory holding the binary is not on `PATH` (warns). |
| latest release | A newer release exists (warns, and suggests `lasso update`), or GitHub could not be asked. |

doctor does not check for `ttyd`, and it looks only at the default socket and port.

### `lasso version`

Prints the version. `lasso --version` and `lasso -v` do the same.

## For agents and scripts

These subcommands are clients of a running lasso. They find it the same way:

| variable | effect |
| --- | --- |
| `LASSO_URL` | Full base URL, for a lasso not on plain-HTTP loopback (a tunnel, a TLS terminator). Wins over `LASSO_LISTEN`. |
| `LASSO_LISTEN` | `host:port` of the local lasso. Default `127.0.0.1:8090`. |
| `LASSO_MCP_TOKEN` | Bearer token, sent when set (for a lasso with `MCP_OAUTH`). |
| `UI_AUTH` | `user:pass`, sent as basic auth when set. |

`lasso closeme` and the `plugin` commands differ slightly; see their sections.

### `lasso connect`

Registers two streamable-HTTP MCP servers with every supported agent CLI found on this machine (Claude Code, Codex, OpenCode, omp): `lasso` at `<url>/mcp` and `lasso-browser` at `<url>/browser-mcp`. It probes the server first: `/mcp` must answer or nothing is registered, and `lasso-browser` is registered only when lasso reports that `/browser-mcp` can serve. Re-running it is safe: an identical entry is left alone and a different one is replaced.

| flag | default | effect |
| --- | --- | --- |
| `-url <url>` | `$LASSO_URL`, else `http://$LASSO_LISTEN`, else `http://127.0.0.1:8090` | The URL **this** machine reaches lasso on. On another machine, use lasso's tunnel or tailnet URL. |
| `-token <token>` | `$LASSO_MCP_TOKEN` | Register entries carrying `Authorization: Bearer <token>`. With no token but `UI_AUTH` set, entries carry basic credentials. |
| `-header 'Name: value'` | | An extra header to register and probe with. Repeatable. |
| `-only a,b` | all found | Only these CLIs: `claude`, `codex`, `opencode`, `omp`, `pi`. |
| `-scope <s>` | `user` | Claude Code scope: `user`, `local` or `project`. |
| `-browser` | `true` | `-browser=false` skips `lasso-browser`. |
| `-remove` | `false` | Unregister both servers from every CLI instead. |
| `-dry-run` | `false` | Print what would be run or written; change nothing. |
| `-force` | `false` | Skip the probe and register even if lasso does not answer. |

Behind Cloudflare Access, a remote machine also needs a service token:

```bash
lasso connect -url https://lasso.example.com \
  -header 'CF-Access-Client-Id: <id>.access' \
  -header 'CF-Access-Client-Secret: <secret>'
```

Each CLI is registered through its own `mcp add` command where it has one. Claude Code and OpenCode take headers only on that command line, so header values are briefly visible in the process list while those commands run. Codex and omp get their headers written straight into their config files (`config.toml`, `~/.omp/agent/mcp.json`), and lasso's first rewrite of a config file leaves a `.bak` copy beside it. Secrets are masked in everything `connect` prints. `CLAUDE_CONFIG_DIR`, `CODEX_HOME` and `XDG_CONFIG_HOME` are honored. See [Connecting agents](../getting-started/connect-agents.md).

### `lasso mcp`

Calls lasso's MCP tools from a shell.

```bash
lasso mcp                      # list the tools this lasso serves
lasso mcp <tool> -h            # one tool's flags
lasso mcp <tool> [flags]       # call it
lasso mcp -json <tool> ...     # print the whole MCP result envelope
```

| flag (before the tool name) | default | effect |
| --- | --- | --- |
| `-json` | `false` | Print the full result envelope, not just the structured output. Also accepted after the tool name. |
| `-timeout <dur>` | `2m0s` | Give up after this long. |

Tool names take either spelling (`list-hosts` or `list_hosts`), and flags take kebab or snake case. The flags come from the input schema the running server advertises, so they include plugin tools and follow the server automatically; a flag you leave out is absent from the call, not zero. Output is indented on a terminal and compact when piped. The exit code is 1 when the tool reports an error. See [`lasso mcp` and friends](../mcp/cli.md).

### `lasso notify`

Pushes a notification to the human running lasso, through the `notify` MCP tool.

```bash
lasso notify "the migration drops 2 columns. Safe to run on prod?"
make test 2>&1 | tail -5 | lasso notify     # message from stdin (up to 8 KiB)
```

| flag | default | effect |
| --- | --- | --- |
| `-title <text>` | your agent's name, resolved from your pane | The headline. |
| `-pane <id>` | `$HERDR_PANE_ID` | Your herdr pane id. |
| `-host <alias>` | resolved from the pane | The host you run on. |

Exits 0 only when a device actually took the notification. When nothing is subscribed it prints the reason on stderr and exits 1, so a script can tell "delivered" from "nobody was listening". It waits up to 45 seconds. See [Notifications](../concepts/notifications.md).

### `lasso open`

Shows a file in the sidebar file viewer of every visible lasso tab, through the `open_file` MCP tool. Flags may come before or after the path.

```bash
lasso open notes.md -line 40
```

| flag | default | effect |
| --- | --- | --- |
| `-line <n>` | | Scroll to and select this 1-based line. |
| `-host <alias>` | your own host | The host the file lives on. |
| `-pane <id>` | `$HERDR_PANE_ID` | Your pane id, to say who opened it. |

A relative path is resolved against this shell's working directory before the call. A `~/` path is passed through and expanded against the target host's home. A directory opens the file tree there instead of the viewer. Without a per-host credential (`LASSO_MCP_TOKEN`), "your own host" is lasso's own machine, so on a remote machine pass `-host`. Exits 1 when no lasso tab is open to show it.

### `lasso closeme`

Closes the agent this command runs in: it POSTs `$HERDR_PANE_ID` to the local lasso's `/api/agent/close`, which performs the same soft close as the UI and the `close_agent` tool (stop the agent process, then close its pane). Takes no arguments. Fails if `$HERDR_PANE_ID` is unset.

It always talks to `http://$LASSO_LISTEN` (default `127.0.0.1:8090`) on the same machine, sending `UI_AUTH` as basic auth when set. It does not read `LASSO_URL` or `LASSO_MCP_TOKEN`.

## Credentials and groups

`mcp-client` and `mcp-group` only matter when the server runs with `MCP_OAUTH` set: with it unset, `/mcp` is open and every caller sees the whole fleet. Both write `lasso.db` directly, so they work whether or not the server is running. Their flags use two dashes. The full model is in [Agent scope](../mcp/agent-scope.md).

### `lasso mcp-client`

```bash
lasso mcp-client add --host <alias> [--fleet] [--name <label>]
lasso mcp-client list                         # alias: ls
lasso mcp-client token <client_id> [--ttl <duration>]
lasso mcp-client rm <client_id>               # aliases: remove, revoke
```

| flag | effect |
| --- | --- |
| `--host` | The host this credential's agents run on: `local` for lasso's own machine, else an ssh-config alias exactly as `list_hosts` shows it. |
| `--fleet` | Let those agents address every host lasso can reach. Without it, they reach their own host plus whatever that host's groups reach. |
| `--name` | A label for the listing. |
| `--ttl` | Lifetime of a minted token: `90d`, `12h`, `30m`, `2w`. Omit it, or pass `never`, for a token that does not expire. |

`add` prints a `client_id` and `client_secret` once, for the `client_credentials` grant; the secret is stored hashed. `token` mints a bearer token directly, for pasting into an MCP client that cannot run a grant. `rm` deletes the client and every token issued to it.

### `lasso mcp-group`

```bash
lasso mcp-group add <name>
lasso mcp-group rm <name>                     # aliases: remove, delete
lasso mcp-group list                          # alias: ls
lasso mcp-group add-member <group> <host|@group>...
lasso mcp-group rm-member <group> <host|@group>      # alias: remove-member
lasso mcp-group grant <from-group> <to-group>
lasso mcp-group revoke <from-group> <to-group>
lasso mcp-group reach <host>
```

Members are hosts (an ssh-config alias or `local`), or another group written with a leading `@`. Every host in a group may reach every other host in it, nested groups included. `grant A B` lets A's hosts reach B's and not the reverse, and grants do not chain. `reach` prints every host a given host can address and the rule that allows it.

## `lasso plugin`

Manages plugins. Alias: `lasso plugins`. Unlike `mcp-client`, these talk to the **running** server's `/api/plugins` (enabling a plugin starts a process only the server can own), found through `LASSO_URL` or `LASSO_LISTEN` and sending `UI_AUTH` when set. Flags accept one or two dashes.

| command | effect |
| --- | --- |
| `list [-json]` | List plugins with their state, trust, MCP status, source and tools. Alias: `ls`. |
| `enable <name>` | Print the plugin's current permissions and approve exactly those. |
| `disable <name>` | Unload it and withdraw the approval. |
| `trust <name>` | Run its MCP server on the host, outside the sandbox. Prints a warning. |
| `untrust <name>` | Back into a microsandbox microVM (the default). |
| `restart <name>` | Restart its MCP server. |
| `reload` | Rescan the plugins directory and print the listing. |
| `install <source> [--ref R] [-y] [--no-enable]` | Install from GitHub: `owner/repo`, `owner/repo/sub/dir`, or `https://github.com/owner/repo[/tree/<ref>/<subdir>]`. Shows the permissions, then asks. Without a terminal it refuses unless `-y`. `--no-enable` installs it disabled. |
| `update <name> [-y]` | Re-fetch a GitHub install's source and ref, and say whether the permissions change. |
| `uninstall <name> [--purge-data]` | Remove a GitHub install. Its data directory is kept unless `--purge-data`. |
| `link <path> [--enable]` | Use a local checkout in place. `--enable` approves it immediately. |
| `unlink <name>` | Forget a linked checkout. Its files are left alone. |
| `log <name> [-n 200] [-f]` | Its MCP server's recent output. Aliases: `logs`; `--lines` for `-n`, `--follow` for `-f`. |
| `data-dir <name>` | Print its writable data directory. |

See [Plugins](../plugins/index.md).

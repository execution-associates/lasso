---
title: Configuration
description: Every lasso server flag with its default, and every environment variable lasso reads.
order: 97
---

lasso has no config file. It is configured by command-line flags, environment variables, and the settings you change in its UI (which live in `lasso.db`; see [Files and directories](./files.md)).

Flags go after `lasso`, `lasso serve`, `lasso start` or `lasso restart`. Where a flag has an environment variable, the variable sets the default and a flag on the command line wins. Credentials (`UI_AUTH`, `MCP_OAUTH`) are environment-only, so they never show up in `ps`.

```bash
lasso serve -h    # the authoritative list, with the defaults this build uses
```

## Server flags

### Network and auth

| flag | env | default | effect |
| --- | --- | --- | --- |
| `-listen` | | `127.0.0.1:8090` | Address the web server binds. lasso refuses to start on a non-loopback address unless `UI_AUTH` is set, `-require-access-header` is on, or `-insecure-no-auth` is passed. Never bind `0.0.0.0` without one of them: the terminal is a writable shell. |
| `-insecure-no-auth` | | `false` | Permit a non-loopback bind with no auth at all. Only for a private interface such as a tailnet address. |
| `-require-access-header` | `LASSO_REQUIRE_ACCESS_HEADER` | `false` | Answer 403 to every request without a non-empty `Cf-Access-Authenticated-User-Email` header, before any other auth check. Counts as auth for the non-loopback rule. Only safe behind an edge that strips client-supplied `Cf-Access-*` headers, such as Cloudflare Access. The variable is on for `1`, `true`, `yes` or `on`. |
| `-access-allowed-emails` | `LASSO_ACCESS_ALLOWED_EMAILS` | empty | Comma-separated allowlist for `-require-access-header`, compared case-insensitively. Empty lets any identity Access vouched for through. |
| `-disable-self-update` | `LASSO_DISABLE_SELF_UPDATE` | `false` | Turn off the in-app self-update of a supervised source checkout: `POST /api/self-update` answers 403 and the UI hides the action. Use it where an agent must not be able to restart its own front door. Same truthy values as above. |
| `-dev` | | `false` | Development mode: if the port is busy, bind the next free one (up to 50 tries); tee the log to `LASSO_DEV_LOG`; mount `/api/log`; never self-update. `mise run dev` sets it. |

See [Deployment](../deployment/index.md) for which combination fits your setup, and [Cloudflare](../deployment/cloudflare.md) for the Access header gate.

### Terminals and herdr

| flag | env | default | effect |
| --- | --- | --- | --- |
| `-herdr-sock` | `HERDR_SOCKET_PATH` | `~/.config/herdr/herdr.sock` | The local herdr server's unix socket. |
| `-term-cmd` | | `herdr` | Command ttyd runs in the main terminal. |
| `-shell-cmd` | | empty | Command for the out-of-herdr Terminal tab. Empty means `$SHELL`, then `bash`, then `sh`. On a remote host the tab runs `ssh <host>` instead. |
| `-term-nice` | | `0` | When non-zero, launch the herdr terminal at this nice level (needs `RLIMIT_NICE` to lower it). `0` disables. |
| `-term-no-swap` | | `false` | Launch the herdr terminal in a transient systemd scope with `MemorySwapMax=0`, so its memory is never swapped out. |
| `-spawn-ttyd` | | `true` | Spawn and supervise ttyd as a child process, one per host and role, each on its own unix socket. `false` proxies to an external ttyd on `-ttyd-port` instead, with no Terminal tab. |
| `-ttyd-port` | | `7682` | Loopback port of the external ttyd. Only used with `-spawn-ttyd=false`. |
| `-poll` | | `2s` | Fallback poll interval for noticing a pane's working directory change. |
| `-theme` | | `auto` | `auto` follows herdr's `config.toml` live, falling back to `execution-associates` when nothing is configured. Any theme name forces that theme; `lasso serve -h` lists them. See [Theming](../web-ui/theming.md). |

ttyd must be installed and on `PATH` for the default `-spawn-ttyd=true`.

### Shared browser

| flag | env | default | effect |
| --- | --- | --- | --- |
| `-browser` | `LASSO_BROWSER` | search | Chromium for the shared browser: a path or a `PATH` name. Empty searches `PATH` (`chromium`, `chromium-browser`, `google-chrome-stable`, `google-chrome`, `chrome`), then Playwright's cache, then the macOS app bundles. `off` disables the shared browser. |
| `-browser-idle` | `LASSO_BROWSER_IDLE` | `15m` | Stop Chromium after this long with no `/cdp` client connected. `0` never stops it. |
| `-browser-cpu` | `LASSO_BROWSER_CPU` | `200%` | `CPUQuota` of the systemd user scope Chromium runs in (Linux). `off` or empty lifts it. |
| `-browser-mem` | `LASSO_BROWSER_MEM` | `2G` | `MemoryHigh` of that scope (Linux). `off` or empty lifts it. With both limits off, no scope is used. |
| `-browser-scale` | `LASSO_BROWSER_SCALE` | `2` | Device scale factor Chromium renders at, a number in (0, 4]. `1` is Chromium's default; higher is sharper on a HiDPI screen and costs raster CPU and larger agent screenshots. |
| `-browser-mcp` | `LASSO_BROWSER_MCP` | `chrome-devtools-mcp` on `PATH` | The chrome-devtools-mcp binary behind `/browser-mcp`: a path or a `PATH` name. `off` disables the endpoint. There is no `npx` fallback. |
| `-browser-mcp-max` | `LASSO_BROWSER_MCP_MAX` | `0` | Cap on live chrome-devtools-mcp processes, counted across every session and profile. `0` means no limit. |

The CPU and memory caps apply only when `systemd-run` and a user systemd manager (`XDG_RUNTIME_DIR`) are available; otherwise Chromium runs uncapped. Setting one of these variables to an empty string is the same as `off`. See [Shared browser](../concepts/shared-browser.md).

## Environment variables

### Server

| variable | effect |
| --- | --- |
| `UI_AUTH` | `user:pass`. Turns on HTTP basic auth for the UI and every route except those listed in [HTTP routes](./http-routes.md#auth-gates). Also lets a non-loopback bind start. |
| `MCP_OAUTH` | `client_id:client_secret`. Turns lasso into an OAuth 2.1 authorization server for its own `/mcp`, and gates `/mcp`, `/cdp` and `/browser-mcp` on a bearer token (or the `UI_AUTH` credentials). Unset, `/mcp` is open and every OAuth route answers 404. See [MCP OAuth](../mcp/oauth.md). |
| `MCP_OAUTH_REDIRECT_URIS` | Comma-separated allowlist of redirect URIs for the pre-registered `MCP_OAUTH` client. Unset, that client accepts any `https` or loopback callback (the consent screen shows the target). |
| `LASSO_DIR` | lasso's state directory. Default `~/.lasso`. Moves `lasso.db`, plugins, plugin data, installed themes, the browser profiles, and the worktree, scratch, upload and prompt directories. It does not move `lasso start`'s PID and log files, which stay in `~/.lasso`. When set, lasso uses this same path on remote hosts too, instead of each host's `~/.lasso`. |
| `LASSO_PUSH_CONTACT` | The contact the VAPID JWT names to push services, e.g. `mailto:you@example.com`. Default: the origin the device subscribed from. |
| `LASSO_ISB` | The isb CLI for sandboxed plugins (1.0 or later): a path or a `PATH` name. When set, nothing else is tried. Default: the first new-enough of `isb` on `PATH` and the newest mise install of isb. `off` disables sandboxed plugins (trusted ones still run). |
| `ISB_SERVE_SOCKET` | Where lasso looks for `isb serve`'s unix socket to check it is running before starting a plugin sandbox. Default `$XDG_RUNTIME_DIR/isb/serve.sock`. |
| `LASSO_BROWSER_ARGS` | Extra Chromium flags, split on whitespace. `--no-sandbox` here is the knowing workaround for a Chromium whose sandbox cannot start. |
| `LASSO_BROWSER_MCP_ARGS` | Extra chrome-devtools-mcp flags, split on whitespace (e.g. `--slim`). |
| `HERDR_SOCKET_PATH` | Default for `-herdr-sock`. |
| `HERDR_CONFIG_PATH` | The local herdr `config.toml` lasso reads its theme from and writes theme changes to. Default `$XDG_CONFIG_HOME/herdr/config.toml`, else `~/.config/herdr/config.toml`, which is herdr's own order. On a remote host lasso resolves the same chain from that host's environment. |
| `SHELL` | The Terminal tab's shell when `-shell-cmd` is empty. |
| `XDG_RUNTIME_DIR` | Its presence (with `systemd-run`) is what lets lasso cap the shared browser in a systemd user scope. |
| `LASSO_COMPOSER_GUARD` | On by default. Before the chat view types a message into an agent pane, lasso checks that pane's input box holds no unsent draft and refuses if it does. `false` or `0` turns that check off, as an escape hatch if a harness's screen layout changes and the check misfires. |

### Usage footer credentials

The usage footer reads each provider's credentials the way that provider's own tools do. These variables are consulted as part of that:

| variable | effect |
| --- | --- |
| `CODEX_HOME` | Where Codex keeps `auth.json`. Default `~/.codex`. |
| `KIMI_CODE_HOME` | Where Kimi Code keeps `credentials/kimi-code.json`. Default `~/.kimi-code`. |
| `ZAI_API_KEY`, `GLM_API_KEY` | A Z.ai API key, tried in that order. |
| `ANTHROPIC_BASE_URL` with `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` | Used for Z.ai only when the base URL points at Z.ai, mirroring Z.ai's Claude Code setup. |

### CLI clients

Read by `lasso notify`, `open`, `mcp`, `connect`, `plugin` and `closeme` (see [CLI](./cli.md#for-agents-and-scripts) for which reads what):

| variable | effect |
| --- | --- |
| `LASSO_URL` | Full base URL of the lasso to talk to. Wins over `LASSO_LISTEN`. |
| `LASSO_LISTEN` | `host:port` of a local lasso. Default `127.0.0.1:8090`. |
| `LASSO_MCP_TOKEN` | Bearer token to send, for a lasso with `MCP_OAUTH`. Also `lasso connect`'s default `-token`. |
| `UI_AUTH` | Sent as basic auth when set. |
| `HERDR_PANE_ID` | The caller's pane, set by herdr in every pane. `closeme` requires it; `notify` and `open` use it to name the calling agent. |
| `CLAUDE_CONFIG_DIR`, `CODEX_HOME`, `XDG_CONFIG_HOME` | Where `lasso connect` finds Claude Code's, Codex's and OpenCode's config. |

### Source installs and development

| variable | effect |
| --- | --- |
| `LASSO_SRC_DIR` | The git checkout `lasso update` and the in-app self-update pull. Default: the directory holding the running binary. |
| `LASSO_SYSTEMD_UNIT` | The `systemctl --user` unit they restart. Default `lasso`. |
| `LASSO_DEV_LOG` | With `-dev`, the file the backend and browser logs are teed into, truncated at startup. Default `/tmp/lasso-dev.log`. |

### What lasso sets for others

A plugin's MCP server gets `LASSO_PLUGIN_DATA`, its writable data directory (see [Plugins](../plugins/index.md#data-and-logs)). None of lasso's own credentials (`UI_AUTH`, `MCP_OAUTH`, `LASSO_MCP_TOKEN`) reach the processes it starts for others: Chromium gets lasso's environment minus those three, and chrome-devtools-mcp and trusted plugin servers get a minimal allowlisted environment. The Terminal tab's shell has herdr's session variables (`HERDR_ENV`, `HERDR_PANE_ID`, `HERDR_SESSION`) removed, so commands like `herdr update` that refuse to run inside a herdr session work there.

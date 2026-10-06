---
title: Files and directories
description: What lasso reads and writes on disk, on its own machine and on the hosts it drives.
order: 98
nav_title: Files
---

lasso keeps its own state in one directory and one SQLite database. Beyond that it reads a few files it does not own (your ssh config, provider credentials) and, when theme sync is on, writes theme files for herdr and the agent CLIs on every host it reaches.

## The state directory

`~/.lasso`, or `$LASSO_DIR` when set. lasso creates it, and its `worktrees`, `scratch` and `uploads` subdirectories, on first use.

| path | what it holds |
| --- | --- |
| `lasso.db` | The state database (below). SQLite in WAL mode, so `lasso.db-wal` and `lasso.db-shm` sit beside it while it is open. |
| `worktrees/<repo>/<branch>/` | Git worktrees for agents created on a branch. `<repo>` is the repository's directory name and `<branch>` the last segment of the branch name, both slugified, with a suffix added if the directory exists. |
| `scratch/<title>-<suffix>/` | Working directories for scratch agents (no repo). The random suffix keeps two agents with the same title apart. |
| `uploads/<id>/` | Files attached in the New dialog, staged until the agent's working directory exists. |
| `uploads/dropped-files/` | Files pasted, dropped or attached into the terminal, and custom backgrounds uploaded in Settings. A file sent to a remote host lands in that host's `~/.lasso`. |
| `prompts/<agent-id>.md` | The initial prompt staged for an agent, removed when lasso closes that agent. |
| `plugins/<name>/` | Installed and hand-placed plugins, each with a `plugin.json`. `plugins/.staging/` holds GitHub installs being previewed. `plugins/.runner.lock` is held by the one lasso that runs this directory's plugin MCP servers, and names its pid and listen address. |
| `plugin-data/<name>/` | Each plugin's one writable directory (mode 0700). Survives uninstall unless you purge it. |
| `omarchy/themes/<name>/` | Omarchy themes installed from a git URL in Settings. |
| `browser-profile/` | The default shared-browser profile (mode 0700): cookies, logins, and a `lasso-browser.pid`. Two lasso instances cannot share it. |
| `browser-profiles/<id>/` | One directory per additional browser profile. |
| `settings.json` | `theme.resolved` (`light` or `dark`), written by theme sync for tools that want a light/dark cue. Other keys are left alone. |
| `lasso.pid`, `lasso.log` | The `lasso start` daemon's PID and output. Always in `~/.lasso`, even when `LASSO_DIR` is set. |

Remote hosts get a `~/.lasso` of their own (on that host; the same `$LASSO_DIR` path when that is set) for the agents lasso creates there: `worktrees/`, `scratch/`, `uploads/`, `prompts/`, `settings.json`, and a `lasso.db` holding that host's creator settings. lasso writes the remote database with the host's own `sqlite3` over SSH, so the lasso binary need not be installed there.

## `lasso.db`

One SQLite file holds everything lasso remembers. It is local to the machine lasso runs on. Back it up with the rest of `~/.lasso`; deleting it resets lasso's settings, forgets its agent records, unregisters every push device and invalidates every OAuth credential.

| table | contents |
| --- | --- |
| `settings` | Global key/value settings (below). |
| `host_state` | Per host: the last repo, agent and agent type picked in the New dialog. |
| `repo_state` | Per host and repo: copy-files globs, setup script, last base branch. |
| `agents` | One record per agent lasso created: host, title, repo, branch, harness, options, working directory, herdr workspace and pane, boot status. A closed agent is marked closed, not deleted. |
| `oauth_clients`, `oauth_codes`, `oauth_tokens` | MCP OAuth clients (including per-host `lasso mcp-client` credentials), authorization codes and tokens, all stored as SHA-256 hashes. Used only with `MCP_OAUTH`. |
| `mcp_groups`, `mcp_group_members`, `mcp_group_grants` | Host groups and grants from `lasso mcp-group`. |
| `push_subscriptions` | Devices registered for Web Push, with the outcome of the last push to each. |

Notable `settings` keys:

| key | contents |
| --- | --- |
| `ui_state` | UI state shared by every browser: theme backdrops and appearance, sidebar layout and tab order, typography, browser mode, and similar. |
| `repos_root`, `branch_prefix`, `default_agent`, `scratch_setup` | New dialog defaults. |
| `push_vapid_private` | The VAPID private key identifying this server to push services, generated once. If it changes, every registered device stops receiving notifications. |
| `plugins`, `plugin_sources` | Plugin approvals (permission fingerprints), trust and VM flags; where each plugin was installed from. |
| `browser_profiles`, `browser_default_profile_name`, `browser_default_cdp_url` | Shared-browser profiles, and the default profile's name and remote address. |
| `sync_agent_themes`, `theme_sync_off`, `theme_hub`, `theme_written:<host>` | Fleet theme sync: the agent-theme toggle, hosts opted out, and what lasso last wrote where. |
| `omarchy_installed` | Omarchy themes installed from a URL. |
| `auto_title_agents`, `terminal_workspace` | The auto-title toggle; the New terminal form's default workspace. |

## Temporary files

In the OS temp directory (`$TMPDIR`, usually `/tmp`):

| path | what it is |
| --- | --- |
| `lasso-ttyd-<pid>-<host>.sock` | The unix socket of the ttyd serving the herdr terminal for one host. |
| `lasso-shell-<pid>-<host>.sock` | The unix socket of the ttyd serving the Terminal tab for one host. |
| `lasso-ctl-<pid>-<host>.sock` | The SSH ControlMaster socket lasso keeps open to one remote host. |
| `lasso-dev.log` | With `-dev` only: the combined backend and browser log (`LASSO_DEV_LOG` moves it). |

Keying the sockets by PID is what lets several lasso instances run side by side without colliding.

lasso also caches the last good usage numbers in your user cache directory, at `lasso/usage-cache.json` (`~/.cache/lasso/usage-cache.json` on Linux).

## Files lasso reads

| path | why |
| --- | --- |
| `~/.ssh/config` | The host list: every concrete `Host` alias (patterns with `*`, `?` or `!` are skipped). `Include` directives are not followed. SSH itself uses the full config, so `ProxyJump` and friends work. |
| herdr's socket | `~/.config/herdr/herdr.sock` by default (`-herdr-sock`, `HERDR_SOCKET_PATH`). |
| herdr's `config.toml` | The theme, read live. Resolved as `$HERDR_CONFIG_PATH`, else `$XDG_CONFIG_HOME/herdr/config.toml`, else `~/.config/herdr/config.toml`, from the environment of the host that owns it. |
| `~/.claude/.credentials.json`, `~/.claude/settings.json` | Claude Code usage; Z.ai credentials when Claude Code is configured for Z.ai. |
| `~/.codex/auth.json` (`$CODEX_HOME`) | Codex usage. lasso writes refreshed tokens back to it (mode 0600). |
| `~/.kimi-code/credentials/kimi-code.json` (`$KIMI_CODE_HOME`) | Kimi Code usage. Refreshed tokens are written back (mode 0600). |
| `~/.config/openusage/zai.json`, `~/.config/zai/key.json` | Z.ai usage. |
| Omarchy background directories | Under `~/.config/omarchy/`, `~/.local/share/omarchy/themes/` and `/usr/share/omarchy/themes/`, for an installed theme's wallpapers. |

## Files lasso writes outside its directory

### herdr's `config.toml`

When you change the theme, lasso writes the `[theme]` section of herdr's `config.toml` on every reachable host whose sync is on. For a theme herdr does not know by name, it writes a supported base name plus a `[theme.custom]` block reproducing the palette, and tags every generated line with a `# lasso-theme` comment. Switching away deletes exactly those tagged lines, so overrides you typed survive and a token you set yourself is never overwritten. See [Theming](../web-ui/theming.md).

### Agent CLI theme files

With **Sync agent themes** on (the default), each theme change is mirrored into the agent CLIs' own theme files on every synced host, so agents render in step with herdr:

| tool | files |
| --- | --- |
| Claude Code | `~/.claude/themes/herdr.json`, and `"theme": "custom:herdr"` in `~/.claude/settings.json` |
| OpenCode | `~/.config/opencode/themes/herdr.json`, the `theme` key in `~/.config/opencode/tui.json`, and a mode hint in `~/.local/state/opencode/kv.json` |
| omp (Oh My Pi) | `~/.omp/agent/themes/herdr.json`, and the theme slots in `~/.omp/agent/config.yml` |
| Ghostty | `~/.config/ghostty/themes/herdr`, and `theme = herdr` in a Ghostty config file that already exists (never created) |
| lasso | `theme.resolved` in `~/.lasso/settings.json` |

A file that does not parse is left alone rather than rewritten. Turn this off under Settings → Themes → Fleet sync, globally or per host.

### Agent CLI MCP config

`lasso connect` registers lasso's MCP servers with each agent CLI: through `claude mcp add` (Claude Code's `~/.claude.json`, or `.mcp.json` for project scope), `codex mcp add` and Codex's `config.toml`, `opencode mcp add`, and omp's `~/.omp/agent/mcp.json`. The first time it rewrites a config file it leaves a `.bak` copy. See [CLI](./cli.md#lasso-connect).

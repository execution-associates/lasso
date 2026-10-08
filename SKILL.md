---
name: lasso
description: Use lasso, the web UI and MCP server over herdr, to spawn and manage coding agents across machines, notify the human who runs it, show them a file, and drive the shared browser they watch live. Use when you have lasso's MCP tools (create_agent, list_agents, notify, open_file, shared_browser, ...) or the `lasso` CLI, when asked to "spin up an agent", "start a worktree agent", "close yourself", "ping me", "open it for me", "show me in the browser", or when $HERDR_PANE_ID is set inside a lasso-created pane.
---

# Using lasso as an agent

lasso runs on one machine and fronts a fleet: a browser UI over [herdr](https://herdr.dev) for the human, and an MCP server for you. Through it you can create other coding agents on any host it can reach, find and close agents (yourself included), notify the human, put a file in front of them, and drive a real Chromium they watch live.

Full documentation: `docs/` in this repo, starting at [docs/index.md](docs/index.md). The tool reference is [docs/mcp/tools.md](docs/mcp/tools.md).

## How you reach it

There are three MCP servers, usually registered by `lasso connect`:

| Server | URL | What it is for |
| --- | --- | --- |
| `lasso` | `<lasso>/mcp` | Agents, hosts, `notify`, `open_file`, browser profiles and tabs, settings |
| `lasso-browser` | `<lasso>/browser-mcp` | chrome-devtools-mcp's tools (navigate, click, fill, screenshot, console, network) against the shared browser |
| `lasso-herdr` | `<lasso>/herdr-mcp` | herdr's socket API, one tool per herdr method (`pane_list`, `pane_read`, `agent_prompt`, ...), on any host lasso drives; pass `host` (or `machine`), omit it for your own host |

If your tools are missing, check `claude mcp list` (or your CLI's equivalent) and ask the human to run `lasso connect`. Every MCP tool also has a shell form: `lasso mcp` lists them, `lasso mcp <tool> -h` shows flags, and `lasso notify`, `lasso open` and `lasso closeme` are shortcuts for the common ones.

## Rules that matter

- **lasso's `/mcp` does not drive herdr directly.** It creates, lists, inspects, messages and closes agents. For herdr's own methods (prompt an agent, read a pane, split one, send keys) use the `lasso-herdr` tools on any host, `herdr` in a shell on the same machine, or your harness's own agent messaging.
- **You must pass `$HERDR_PANE_ID` yourself.** The MCP server runs in lasso's process and cannot see your environment. Pass it as `pane_id` to `whoami`, `notify`, `open_file` and `close_agent`.
- **`host` defaults to your own host**, not lasso's. Pass a host only to act on another machine, and only one `list_hosts` returns.
- **An empty listing is usually scope, not an outage.** Your credential bounds which hosts you can see.
- **Close lasso agents with `close_agent` (or `lasso closeme`), never `herdr pane close`.** Closing the pane directly leaves the agent record and staged prompt files behind.

## Creating agents

1. `list_hosts` to pick a host (omit `host` for your own). An entry with state `probing` or `timeout` is not down; call again shortly or pass `refresh: true`.
2. For a git agent, `list_repos` for a repo path and `list_branches` for a base branch.
3. `create_agent`:
   - `type`: `git` (a fresh worktree on a new branch off `base_branch`, default the repo's HEAD) or `scratch` (an empty workspace).
   - `agent`: `claude` (default), `codex`, `opencode`, `omp`, or `pi`. Optional `model`, `effort`, `extra_args`.
   - `prompt`: the initial task. Omit it and the CLI comes up idle for the human.
   - `title`, `branch_name`, `branch_prefix`, `notes` (written to `NOTES.md` in the work dir), `plan_mode` (claude, opencode, omp), `advisor` (omp).
   - `focus: true` switches the human's view to the new pane; the default leaves them where they are.

It returns at once with the agent's `id`, workspace and root pane; the agent boots asynchronously. Check on it with `get_agent` (status: `working`, `idle`, `blocked`, `unknown`, or `failed`). `list_agents` also shows foreign herdr sessions lasso did not create; address those by `sidebar_name` or `root_pane`.

To bring many repos up to date, call `create_agent` once per repo.

## Finishing and closing yourself

```bash
lasso closeme                 # close the calling agent (uses $HERDR_PANE_ID)
```

Or over MCP: `close_agent` with `pane_id` set to your `$HERDR_PANE_ID`, or `whoami` first and then `close_agent` with the returned `id` and `host`. `remove_worktree: true` deletes a git agent's worktree too, discarding uncommitted work, so only use it once your work is committed or pushed.

## Reaching the human

**notify** pushes to their phone, even locked. Use it only when you need them: a decision only they can make, a blocking question, a long job finishing while they are away. Nothing is rate-limited, so every call buzzes.

```bash
lasso notify "the migration drops 2 columns. Safe to run on prod?"
```

Check the result: `sent: false` (or a non-zero exit from the CLI) means no device is registered and nobody received it. Don't claim you notified them.

**open_file** shows a file in the Files panel beside their terminal, in every visible lasso tab. Use it when they say "open it for me" or you want them to review something. Pass an absolute or `~/` path (the CLI resolves relative ones), optional `line`. A directory opens the file tree there. Check `delivered`: `0` means no lasso tab is open and they did not see it.

```bash
lasso open docs/plan.md -line 40
```

**Status card.** Put a one-line summary of what you are doing on your pane's card in herdr's sidebar and lasso's pane switcher, and update it when you change phase:

```bash
herdr pane report-metadata "$HERDR_PANE_ID" --source agent:self --token summary="<what you're doing>" --ttl-ms 1800000
```

Don't use `herdr pane report-agent`; it overrides herdr's own status detection.

## The shared browser

A real Chromium on lasso's machine that the human watches, and can click in, from lasso's Browser tab.

- Call `shared_browser` to start it and get the `/browser-mcp` URL (`mcp_endpoint`), the raw CDP websocket (`ws_endpoint`, for Playwright's `connectOverCDP`), `profiles_url` (a plain `GET` listing every profile and its own CDP websocket), and the open pages. `available: false` or `mcp_available: false` come with a reason.
- **Open your own page** (`new_page` on `lasso-browser`) instead of navigating one you didn't open. The human's tab shows the most recently opened page, so opening one puts them on it.
- **Close the pages you opened** when you are done.
- `localhost` inside the browser means lasso's machine, not yours.
- **Logged-in accounts are the human's.** Reading is fine; posting, sending, accepting or buying needs their go-ahead.
- **Profiles** are separate Chromiums with their own cookies, logins and proxy. Every `lasso-browser` tool takes an optional `profile` (id or display name; omitted means the default). Page ids belong to one profile, so pass the same `profile` on every call about a page. Manage profiles with `list_browser_profiles`, `create_browser_profile`, `update_browser_profile`, `delete_browser_profile`.
- To put a page on the human's screen from `/mcp`, use `open_browser_tab`; `show_browser_tab`, `list_browser_tabs` and `close_browser_tab` manage existing tabs.

## Settings

`get_settings` reads everything lasso's Settings tab shows (UI preferences, agent defaults, per-repo setup, theme, notification devices, browser, plugins) and `update_settings` changes it, with the same validation the tab applies. Pass only what changes; `ui` is a patch. Plugins are read-only: enabling, trusting, installing or updating one needs the human in the Settings tab, so ask them.

## Plugin tools

Enabled plugins add tools to `/mcp` named `<plugin>__<tool>`. They appear in your tool list and in `lasso mcp`. Treat them like any other tool on lasso.

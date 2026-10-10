---
title: Tools reference
description: Every tool lasso's /mcp server registers, with its parameters, defaults, return values and scope rules.
order: 41
nav_title: Tools
---

This page lists every tool on lasso's `/mcp` server. The tool descriptions an agent sees are longer and written for a model; this is the same contract for people. Every tool is also callable from a shell as `lasso mcp <tool>` (see [Shell commands](./cli.md)), with the parameters below as flags.

A parameter is **required** unless marked optional. Optional parameters that are omitted are absent, not zero: an empty `host` and no `host` at all can mean different things, as noted per tool.

## How `host` and scope work

Most tools take an optional `host`: `local` (the box lasso runs on) or an SSH-config alias exactly as [`list_hosts`](#list_hosts) shows it.

- **Omitting `host` targets your own host.** For a caller with a [per-host credential](./agent-scope.md), that is the host the credential was issued for. For any other caller (no `MCP_OAUTH`, the `UI_AUTH` basic credentials, or the main `MCP_OAUTH` client) it is `local`. `whoami`, `close_agent` and `notify` handle an omitted host differently, as described under each.
- **A host must be addressable.** lasso can address the local box and the hosts with a concrete alias in the SSH config it reads. Anything else is refused up front, rather than answered from stale records.
- **A host must be within your reach.** A self-scoped credential reaches its own host plus whatever its [host groups](./agent-scope.md#groups-reach-between-hosts) add. A fleet-scoped credential, or an unidentified caller, reaches every addressable host. A refusal names the credential's host and reach and how to widen it.
- **Browser tools and plugin tools need `local` in your reach**, because the shared browser and plugins run on lasso's own machine. A credential scoped to some other host gets a tool error.

Scope only applies when `MCP_OAUTH` is set. With it unset, every caller is unidentified and reaches every addressable host.

Several tools take a `pane_id`: your own herdr pane id, the value of `$HERDR_PANE_ID` in your shell (for example `p_82`). The server runs in lasso's process, not your shell, so it cannot read your environment; you pass it. The public form `w<workspace>-<n>` is accepted too.

## Identity and discovery

### `list_hosts`

Lists the hosts lasso can drive: the local box plus reachable, protocol-compatible SSH hosts, filtered to the ones your credential may address. Use a returned `host` as the `host` argument of the other tools.

| parameter | type | | description |
| --- | --- | --- | --- |
| `refresh` | boolean | optional | Re-probe every configured SSH host instead of answering from the background probe's cache. Slower. Use it when a host you just brought up is missing. |

Returns `active` (the host lasso booted on, which answers for a caller that names none), `hosts`, and `probing`. Each entry in `hosts` has:

| field | meaning |
| --- | --- |
| `host` | the value to pass as `host` (`local` or an alias) |
| `label` | display name (the local hostname for `local`) |
| `reachable`, `running`, `compatible` | SSH reachable, herdr server up, herdr protocol matches the herdr on lasso's machine |
| `version` | the host's herdr version, when known |
| `state` | empty when the probe finished; `probing` while the first probe is still in flight; `timeout` when it ran out of time. Neither means the host is down. |
| `err` | the probe's failure detail, if any |

The answer is immediate and may be partial. Top-level `probing: true` means at least one entry is still filling in: call again in a second or two (or with `refresh: true`) instead of reporting those hosts as unavailable.

### `list_repos`

Lists the git repositories under the host's configured repo roots.

| parameter | type | | description |
| --- | --- | --- | --- |
| `host` | string | optional | Host to list repos on. Defaults to your own host. |
| `refresh` | boolean | optional | Re-scan the repo roots instead of answering from the cache. Use it when a repo you just cloned is missing. |

Returns `root` (the configured repo roots that were scanned) and `repos`, each with `path`, `name` and, when lasso has one recorded, `last_base_branch`. Pass a `path` as `repo` to `create_agent`. You can also pass any absolute repo path to `create_agent` directly; this only enumerates the configured roots.

### `list_branches`

Lists a repository's local and remote branches plus its default branch, for choosing a `base_branch`.

| parameter | type | | description |
| --- | --- | --- | --- |
| `repo` | string | | Absolute path to the git repository. |
| `host` | string | optional | Host the repo lives on. Defaults to your own host. |
| `refresh` | boolean | optional | Re-read the branches instead of answering from the cache. Use it when a branch you just created or fetched is missing. |

Returns `branches` (local), `remote_branches` (remote-tracking) and `default`.

### `whoami`

Identifies the calling agent's own lasso agent record, typically so it can then close itself.

| parameter | type | | description |
| --- | --- | --- | --- |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`. Without it the answer is `found: false` with an explanation. |
| `host` | string | optional | The host you run on. |

With no `host`: a caller whose credential names its host searches that host alone. Otherwise every host lasso can address (within your reach) is searched, because pane ids are only unique per host and your pane is not necessarily on lasso's box. A unique match resolves. A pane id that matches agents on several hosts returns `found: false` naming the candidate hosts, so call again with `host` set to the one you run on.

Returns `found`, and on success `agent` (an [agent object](#the-agent-object)). Pass **both** `agent.id` and `agent.host` to `close_agent`. When it cannot resolve, `found` is false and `detail` says why; it does not error.

## Agents

### `create_agent`

Spawns a coding agent (`claude`, `codex`, `opencode`, `omp` or `pi`) in its own herdr workspace. `type: "git"` creates a fresh git worktree off `base_branch` under a new branch; `type: "scratch"` creates an empty workspace. It returns immediately with the new agent's id, workspace and root pane, and the agent boots asynchronously. To bring many repos up to date, call it once per repo.

| parameter | type | | description |
| --- | --- | --- | --- |
| `type` | string | | `"git"` (a new worktree off `base_branch`) or `"scratch"` (an empty workspace). |
| `repo` | string | optional | Absolute path to the git repository. Required when `type` is `"git"`. |
| `host` | string | optional | Host to create the agent on. Defaults to your own host. |
| `prompt` | string | optional | The agent's initial task. Omit it to launch the CLI idle, waiting for a prompt from the human or from `herdr agent prompt`, which parks a ready agent on a branch without starting work. |
| `title` | string | optional | Short title for the agent and worktree. Defaults to the prompt's first line, or `"Untitled agent"` with no prompt. |
| `base_branch` | string | optional | Branch or ref to branch the new worktree off. Defaults to the repo's HEAD. |
| `branch_name` | string | optional | Name for the new branch. Defaults to a slug of the title. |
| `branch_prefix` | string | optional | Prefix for the new branch, e.g. `"worktree"` gives `worktree/<name>`. |
| `agent` | string | optional | Which agent to launch: `"claude"` (default), `"codex"`, `"opencode"`, `"omp"` (Oh My Pi) or `"pi"`. |
| `model` | string | optional | Passed to the CLI's `--model`. Omit for the harness default. |
| `effort` | string | optional | Thinking/reasoning level (see below). Omit for the CLI's default. |
| `extra_args` | string | optional | Extra CLI flags appended verbatim to the launch command. |
| `notes` | string | optional | Extra notes, written to `NOTES.md` in the work dir and referenced in the prompt. |
| `plan_mode` | boolean | optional | Start the agent in plan mode (see below). |
| `advisor` | boolean | optional | Turn on omp's per-turn advisor runtime (`--advisor`): a background pass that reviews each turn and injects notes. omp only; dropped for every other harness. |
| `focus` | boolean | optional | Switch the herdr view to the new pane as it boots. Defaults to false, so spawning an agent does not pull a watching human away from their pane. |

**`effort` levels depend on the harness:**

| harness | levels |
| --- | --- |
| claude | `low`, `medium`, `high`, `xhigh`, `max` |
| codex | `minimal`, `low`, `medium`, `high`, `xhigh` |
| pi | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` |
| omp | pi's levels plus `auto` (it picks per turn) |
| opencode | no effort setting |

A level the chosen harness does not list is **dropped** and the agent launches at the CLI's default, because an unknown level makes the CLI exit at launch and would fail the boot. The returned agent's `effort` shows what actually took.

**`model` examples** from the tool description: `fable`, `opus`, `sonnet` or `haiku` for claude; `gpt-5.6-sol` or `gpt-5.6-terra` for codex; provider/model selectors for omp such as `anthropic/claude-opus-5` or `openai-codex/gpt-5.6-sol`. omp and pi also match other patterns against their authenticated provider catalogs.

**`plan_mode`** applies to claude, opencode and omp, and is dropped for codex and pi (lasso cannot start either in a plan mode from the launch line). The agent researches and proposes a plan but edits nothing until the plan is approved; answering questions does not need approval. When it wants to execute, it parks on a gate that lasso reports as status `blocked` in `get_agent` and `list_agents`. From another agent, review and answer it with herdr: `herdr agent wait <target> --until blocked`, `herdr agent read <target>`, then `herdr agent send-keys` (a blocked agent refuses `herdr agent prompt`). Or leave it parked for the human watching the pane.

- claude and opencode show a numbered "Would you like to proceed?" prompt; option `1` accepts. herdr reports `blocked` natively.
- omp shows a Plan Review overlay chosen with the arrow keys, not numbers. herdr's own detection reports it as idle, so lasso recognizes the overlay itself and reports `blocked`; poll `get_agent` for an omp gate rather than waiting on herdr. Enter accepts the highlighted default, "Approve and execute". Revising an omp plan ("Refine plan") needs a human in the pane.

Returns an [agent object](#the-agent-object). Poll `get_agent` and watch `boot_status` to tell a still-booting agent from one that is up.

### `list_agents`

Lists the agents on a host, each with its live status and its `sidebar_name` (the name shown in the herdr pane switcher, which is the handle a human is most likely to use).

| parameter | type | | description |
| --- | --- | --- | --- |
| `host` | string | optional | Host to list agents on. Defaults to your own host, not lasso's. |

It covers both the agents lasso created (`lasso_created: true`, addressable by `id`) and **foreign herdr sessions** lasso did not create, such as long-lived bots in their own panes (`lasso_created: false`, no `id`; address them by `sidebar_name` or `root_pane`). Only agents herdr still has a pane for are listed: a lasso agent whose pane or workspace was closed is reconciled away on this call, so every id returned is one you can inspect and close.

Returns `host`, `agents` (an array of [agent objects](#the-agent-object), empty when the host has none) and, when something went wrong, `herdr_error`. A `herdr_error` means the listing is **partial**: herdr could not be enumerated, so live statuses, sidebar names and foreign sessions are missing, nothing was reconciled, and the host may have agents this call cannot see.

### `get_agent`

Gets one agent's details and live status. It does not read the agent's terminal; `read_agent` does.

| parameter | type | | description |
| --- | --- | --- | --- |
| `agent_id` | string | optional | The agent's lasso id (from `create_agent` or `list_agents`). |
| `to` | string | optional | A target: a lasso agent id, a sidebar/display name, or a herdr pane id, including a foreign session lasso did not create. Takes precedence over `agent_id` when both are given. |
| `host` | string | optional | Host the agent is on. Defaults to your own host. |

One of `agent_id` or `to` is needed. Targets resolve in order: exact lasso id, then exact pane id, then a case-insensitive name. A name that matches more than one agent is refused with the candidates listed; re-target by id or pane id.

Returns `agent`, an [agent object](#the-agent-object). `status` is `working`, `idle`, `blocked` or `unknown`, or `failed` for a boot that never came up.

### `close_agent`

Stops an agent: kills the agent process in its pane, then (unless `close_pane` is false) closes the pane.

| parameter | type | | description |
| --- | --- | --- | --- |
| `agent_id` | string | optional | The agent's id. Either this or `pane_id` is required. |
| `pane_id` | string | optional | A herdr pane id instead of an id. Pass your own `$HERDR_PANE_ID` to close yourself without calling `whoami` first. |
| `host` | string | optional | Host the agent is on: pass the `host` that `whoami` or `list_agents` returned with the agent. |
| `close_pane` | boolean | optional | Close the herdr pane after killing the process. Defaults to true; false leaves it open as a bare shell. |
| `remove_worktree` | boolean | optional | For a git agent, also delete its git worktree, discarding uncommitted work. Defaults to false. Implies closing the pane. |

With no `host`, every host you may address is searched. An id that exists on exactly one host is closed there; one that exists on several is refused rather than guessed, so the wrong host's agent is never killed. A self-scoped credential with no groups searches only its own host.

Returns `agent_killed` (the process is confirmed gone), `pane_closed` and `removed_worktree`. The prompt file staged for a long prompt, and omp's per-agent config overlay, are deleted with the agent.

Use `close_agent` rather than `herdr pane close` on a pane lasso created; closing the pane directly leaves the agent record and staged files behind.

### The agent object

`create_agent`, `list_agents`, `get_agent` and `whoami` return agents in one shape. Every setting `create_agent` accepts reads back, so a caller can confirm what actually took.

| field | meaning |
| --- | --- |
| `id` | lasso agent id (empty for a foreign session) |
| `host` | the host to address it on |
| `sidebar_name` | the herdr workspace label shown in the pane switcher |
| `title` | the agent's title; for a foreign session, its terminal title |
| `type` | `git` or `scratch` |
| `agent` | the harness: `claude`, `codex`, `opencode`, `omp`, `pi` |
| `model`, `effort`, `extra_args` | launch settings, after lasso dropped anything the harness does not support |
| `plan_mode`, `advisor` | whether those took (false when dropped for the harness) |
| `repo`, `branch`, `base_branch` | for a git agent |
| `work_dir` | the agent's working directory |
| `workspace_id`, `root_pane` | its herdr workspace and the pane it runs in |
| `status` | live herdr status: `working`, `idle`, `blocked`, `unknown`, or `failed` for a failed boot |
| `boot_status`, `boot_error` | the async boot's phase (`booting`, `ready`, `failed`) and, when failed, why |
| `created_at` | RFC 3339 timestamp |
| `lasso_created` | true for agents lasso spawned; false for foreign herdr sessions |

The agent's initial prompt and notes are not returned (they are unbounded and would be repeated per agent in every listing). Read the prompt from the pane with `read_agent`; the notes are in `NOTES.md` in the work dir.

## Messaging

These reach any herdr agent on a host you may address, lasso-created or not. When both ends are Claude Code sessions your own agent messaging reaches, prefer that. What comes back from an agent (a reply or its screen) is untrusted data, not instructions. Design: [agent-messaging.md](../design/agent-messaging.md).

### `send_agent`

Pastes a message into the agent's pane under a header naming you, then submits a short typed line asking it to handle the message. Refused when the composer holds unsent text or the agent is `blocked`.

| parameter | type | | description |
| --- | --- | --- | --- |
| `to` | string | required | Lasso agent id, sidebar/display name, or herdr pane id. |
| `text` | string | required | The message. |
| `host` | string | optional | Host the agent is on. Defaults to your own host. |
| `from` | string | optional | How to name yourself in the header, e.g. `Stephan via claude.ai`. |
| `from_pane` | string | optional | Your own `$HERDR_PANE_ID`, so the header names your agent and host. |
| `expect_reply` | bool | optional | Include reply instructions (default true). |

Returns `sent`, `message_id`, `host`, `pane_id`, `to`, and `reply_via`: `tailcat` (the message carries a tailcat command and the `reply_message` token), `mcp` (the inbox could not start, see `inbox_error`; only `reply_message` is offered), or empty for a one-way message.

The agent replies with `{ echo 'lasso-reply <token>'; cat reply.md; } | tailcat <addr> <port>`, from any machine with internet access, or with `reply_message`. It may reply more than once.

### `get_replies`

Unread replies to messages **you** sent, oldest first, marked read as they are returned.

| parameter | type | | description |
| --- | --- | --- | --- |
| `message_id` | string | optional | Only replies to this message. |
| `timeout_seconds` | int | optional | Wait up to this long (max 300) when nothing is waiting; returns as soon as a reply lands. |
| `include_read` | bool | optional | Also return replies already returned. |

Each reply has `message_id`, `to`, `host`, `body`, `via` (`tailcat` or `mcp`), `received_at`, and `truncated` when it was cut at 1 MiB. Messages and replies are kept 30 days.

### `read_agent`

The agent's terminal. `source` is `recent_unwrapped` (default, scrollback with soft wraps joined), `recent`, or `visible`; `lines` defaults to 80, max 1000. Takes `to` and `host` like `send_agent`. Returns `text`, `status`, `host`, `pane_id`.

### `wait_agent`

Waits until the agent reaches `status` (default `idle`, which also matches `done`) or `timeout_ms` passes (default 60000, max 600000). Returns `status` and `matched`.

### `reply_message`

Answers a message lasso delivered to you, by the `token` in its footer, with `text`. The same as the tailcat command, for an agent that has lasso's MCP tools.

## Human-facing

### `notify`

Pushes a notification to the human who runs this lasso, on their phone if they have lasso on their home screen with notifications enabled (see [Notifications](../concepts/notifications.md)). Use it when you genuinely need them: a decision only they can make, a question that blocks you, or a long job finishing while they are away. It reaches a locked device, so an agent that pings on every step teaches the human to ignore it.

| parameter | type | | description |
| --- | --- | --- | --- |
| `message` | string | | What to tell the human in one or two sentences, read on a lock screen. Say what you need: "the auth migration is green, ready to merge?" rather than "please look at lasso". |
| `title` | string | optional | Headline. Defaults to your agent's name, resolved from `pane_id`. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`. Titles the notification with your agent's name and makes it open on your host. Without it the notification still goes out, unattributed. |
| `host` | string | optional | The host you run on. Omit to resolve it from `pane_id`, as `whoami` does. |

The title is clipped to 70 characters and the message to 400. Nothing is collapsed or rate-limited: one call is one notification. An unresolvable `pane_id` is not an error; the message goes out titled `lasso` and the reason comes back in `detail`.

Returns `sent`, `transports` (the channels that took it), `title` (the headline the human sees) and `detail`. **Check `sent`**: false means no device is registered and the human did not get it, so do not report that you notified them. `lasso notify "<message>"` in a shell makes the same call.

### `open_file`

Opens a file in the human's sidebar file viewer (the Files panel beside their terminal), in every lasso tab they have open. Use it to show them something: a plan you just wrote, a file to review, the place a bug lives.

| parameter | type | | description |
| --- | --- | --- | --- |
| `path` | string | | Absolute path of the file or directory, or one starting with `~/` (expanded against the target host's home). Relative paths are refused because the server cannot see your working directory; `lasso open <path>` in a shell resolves one for you. |
| `host` | string | optional | Host the file lives on. Defaults to your own host, which is where your files are. |
| `line` | integer | optional | 1-based line to scroll to and select. Ignored for directories, images, PDFs and videos. A markdown file opened at a line shows its raw editor instead of the rendered preview. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, used only to tell the human which agent opened it; without it they are told "an agent". |

The file must exist; a missing path is an error, so write it first. A directory opens the sidebar's file tree rooted there instead of the viewer.

Returns `delivered` (the number of lasso tabs the request reached), `path` (the resolved absolute path), `host`, `dir` (true for a directory) and `detail`. **`delivered: 0` means no lasso tab is open and the human did not see it.** A tab that is hidden (a phone in a pocket, a background browser tab) receives the request and ignores it, and a viewer holding unsaved edits is not replaced: the human gets a prompt offering to open the file instead.

## Browser

These tools manage the [shared browser](../concepts/shared-browser.md): which profiles exist, which pages are open, and which one the human is looking at. Driving a page (clicking, typing, reading it) is the job of [`/browser-mcp`](./browser-mcp.md) or raw CDP. All of them require a caller whose reach includes `local`, because the browsers run on lasso's machine.

A `profile` argument takes a profile's id (e.g. `work`) or its display name; omitted means the default profile.

### `shared_browser`

Gets, and by default starts, the shared browser, and says where to connect.

| parameter | type | | description |
| --- | --- | --- | --- |
| `start` | boolean | optional | Start the browser if it is not running. Defaults to true; false only reports its state. |
| `profile` | string | optional | Browser profile. Omit for the default. |

Returns:

| field | meaning |
| --- | --- |
| `mcp_endpoint` | absolute `/browser-mcp` URL: add it as a streamable-HTTP MCP server (`claude mcp add --transport http lasso-browser <mcp_endpoint>`). It drives every profile. |
| `mcp_available`, `mcp_reason` | whether `/browser-mcp` can serve, and why not (usually chrome-devtools-mcp not installed on lasso's machine) |
| `ws_endpoint` | absolute CDP websocket URL for this profile (`/cdp`, or `/cdp/p/<id>`), e.g. for Playwright's `chromium.connectOverCDP()` |
| `ws_path` | the path alone, to prefix with lasso's URL when `ws_endpoint` is empty |
| `http_endpoint` | the CDP HTTP base (`…/json/list`, `…/json/version`) |
| `profiles_url` | `GET` it for every profile and its own CDP endpoint (`/cdp/profiles`), the discovery path for a CDP client with no MCP server |
| `available`, `running` | whether a Chromium is installed, and whether it is running |
| `pages` | the pages open now (`id`, `url`, `title`) |
| `profile` | the profile these endpoints drive |
| `note` | why something is unavailable, and reminders such as which credentials `/browser-mcp` and `/cdp` need |

### `list_browser_profiles`

Lists the shared browser's profiles. Takes no parameters.

Returns `profiles`, each with `id`, `name`, `cdp_url` (for a remote browser), `default`, `running`, `tabs` (only while it runs), `mcp_endpoint` (the one `/browser-mcp` URL; pass this profile's `id` as `profile` to its tools), `ws_endpoint` (this profile's own CDP websocket) and `note`.

### `create_browser_profile`

Creates a profile: a separate Chromium with its own persistent cookies and logins. It appears in the Browser tab's profile picker at once and starts on first use. The `/browser-mcp` server an agent already has drives it immediately, with no reconnect.

| parameter | type | | description |
| --- | --- | --- | --- |
| `name` | string | | Display name, e.g. `"Work"` or `"US exit"`. |
| `id` | string | optional | 1-32 lowercase letters, digits or dashes. Derived from the name when omitted. It is the `profile` value for `/browser-mcp` tools and appears in the CDP URL `/cdp/p/<id>`. |
| `cdp_url` | string | optional | Makes the profile a remote browser lasso dials instead of launching: its DevTools HTTP base, `http://host:port` or `https://host[:port]`. |

Returns the profile, in the same shape as a `list_browser_profiles` entry.

### `update_browser_profile`

Renames a profile and/or changes its `cdp_url`. Pass only what changes.

| parameter | type | | description |
| --- | --- | --- | --- |
| `profile` | string | | The profile to change. |
| `name` | string | optional | New display name. |
| `cdp_url` | string | optional | New remote browser address, or `""` for a browser lasso launches. Omit to leave it unchanged. |

At least one of `name` or `cdp_url` is needed. A `cdp_url` change lets go of the browser the profile was using, so `/browser-mcp` page ids for that profile are gone (call `list_pages` again). The default profile can be renamed and pointed at a remote browser too. Returns the updated profile.

### `delete_browser_profile`

Deletes a profile: stops its Chromium (closing its tabs and any session driving it) and **deletes its profile directory**, with every cookie and login in it. This cannot be undone, so only do it when the human asked. The default profile cannot be deleted.

| parameter | type | | description |
| --- | --- | --- | --- |
| `profile` | string | | The profile to delete. |

Returns `deleted`, the id that was removed.

### `list_browser_tabs`

Lists open tabs, per profile.

| parameter | type | | description |
| --- | --- | --- | --- |
| `profile` | string | optional | Only this profile. Starts nothing: a stopped profile has no tabs. Omit for every profile. |

Returns `profiles`, each with `profile` (the id), `name`, `running` and `tabs` (`id`, `url`, `title`). Tab ids are CDP target ids, usable with `show_browser_tab`, `close_browser_tab`, and as `targetId` over CDP.

### `open_browser_tab`

Opens a **new** tab and puts it on the human's screen: every visible lasso tab switches its Browser tab to that profile and page, opening the sidebar if needed. Use it when the human asks you to open a page for them, or in a particular profile. It starts the profile's browser if needed. The page loads from lasso's machine, through the profile's proxy if it has one, so `localhost` means lasso's machine.

| parameter | type | | description |
| --- | --- | --- | --- |
| `url` | string | | A full `http(s)` URL. A bare `host[:port]` gets `https://` (`http://` for `localhost` and `127.x`). `about:blank` opens an empty tab. |
| `profile` | string | optional | Profile to open it in. Omit for the default. |
| `show` | boolean | optional | Put the tab on the human's screen. Defaults to true; false opens it quietly. |
| `surface` | string | optional | Which view shows it: `agent` (default), the live Chromium, or `iframe`, the page embedded the way a terminal link opens, loaded by the human's own browser (their cookies and their `localhost`). `live` and `embed` are accepted too; anything else is refused. An `http://` page on an https lasso cannot be embedded, so it stays on the live view. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, used only to tell the human which agent opened it. |

Returns `tab_id`, `profile`, `url`, `title`, `delivered`, `mcp_endpoint` and `detail`. `delivered: 0` means no lasso tab is open and the human did not see it (the tab is still open in the browser). To drive the page afterwards, use the `/browser-mcp` tools with the same `profile` (find the tab with `list_pages` by its URL). A profile's browser stops after lasso's idle timeout with nothing connected, and its tabs close with it, so keep a `/browser-mcp` or CDP session open while you still need the page.

### `show_browser_tab`

Puts an **existing** tab on the human's screen.

| parameter | type | | description |
| --- | --- | --- | --- |
| `tab_id` | string | | The tab to show (from `list_browser_tabs` or `open_browser_tab`). |
| `profile` | string | optional | The tab's profile. Omit for the default. |
| `surface` | string | optional | `agent` (default) or `iframe`, as on `open_browser_tab`. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, to tell the human who is showing it. |

Returns the same shape as `open_browser_tab`. `delivered: 0` means nobody saw it.

### `close_browser_tab`

Closes a tab. Close the tabs you opened when you are done; leave the human's and other agents' tabs alone unless asked.

| parameter | type | | description |
| --- | --- | --- | --- |
| `tab_id` | string | | The tab to close. |
| `profile` | string | optional | The tab's profile. Omit for the default. |

Returns `closed` (the tab id) and `profile`.

## Settings

These tools read and change what lasso's Settings tab shows. Each write runs through the same handler the Settings tab calls, so the validation is the same and every open lasso tab updates live. Both require a caller whose reach includes `local`, because the settings live in lasso's own database; `agents` and `repos` also need the `host` they name.

**Plugins are read-only here.** Enabling a plugin approves its permissions, trusting one runs it outside its sandbox, and installing or updating one changes what an approval covers. Those need a human in the Settings tab, so no MCP tool does them.

### `get_settings`

| parameter | type | | description |
| --- | --- | --- | --- |
| `section` | string | optional | One of `ui`, `agents`, `repos`, `theme`, `notifications`, `browser`, `plugins`. Omit for all of them. |
| `host` | string | optional | Whose creator settings `agents` and `repos` show. Defaults to `local`. |

Returns one key per section:

| section | contents |
| --- | --- |
| `ui` | The synced UI preferences (`ui_state`): appearance mode and palettes, backdrops, typography, chat and terminal text, browser mode, sidebar tabs, usage tracking, creator host, grid grouping and the rest. |
| `agents` | `host`'s creator defaults (`repos_root`, `branch_prefix`, `default_agent`, `default_terminal_workspace`, `scratch_setup`) and `auto_title`. |
| `repos` | Each repo under `host`'s repo roots with its `copy_files` and `setup`. |
| `theme` | herdr's current theme, the selectable `themes`, `sync_agent_themes`, `theme_sync_off` and the installable `catalog`. |
| `notifications` | Registered push `devices`, by id and label. Endpoints are never returned. |
| `browser` | The shared browser's status. Profiles have their own tools. |
| `plugins` | Each plugin's state, read-only. |

A section that fails (an unreachable host, say) is listed under `errors` and the others are still returned.

### `update_settings`

Pass only what changes; an omitted field is left alone. Returns the affected sections as they now read.

| parameter | type | | description |
| --- | --- | --- | --- |
| `ui` | object | optional | A patch of the `ui` section. Only the keys you send change. `typography`, `chat_text`, `terminal_text` and `theme_atmosphere` merge per field, and `""` or `null` deletes one; `sidebar_tabs` is replaced whole. Pinned agents change through `agent_pins: {<key>: true\|false}` and the background gallery through `remember_background` / `forget_background`. |
| `auto_title` | boolean | optional | Name new agents from their prompt. |
| `agent_defaults` | object | optional | `host`'s creator defaults: any of `repos_root`, `branch_prefix`, `default_agent`, `default_terminal_workspace`, `scratch_setup`. `""` clears one. |
| `repo` | object | optional | `{path, copy_files?, setup?}`: one repository's per-repo settings on `host`. |
| `host` | string | optional | Whose creator settings `agent_defaults` and `repo` write. Defaults to `local`. |
| `theme` | string | optional | Switch herdr's theme fleet-wide. Refused while an appearance palette is set; change `ui.palette_light` / `palette_dark` / `appearance_mode` instead. |
| `sync_agent_themes` | boolean | optional | Mirror the theme into agent CLIs' own theme files. |
| `theme_sync` | object | optional | `{host, enabled}`: stop or resume theme writes to one host. |
| `sync_theme_now` | boolean | optional | Push the current theme to every reachable host. Runs in the background; the outcome arrives as a toast in lasso. |
| `install_theme_url` | string | optional | Install an Omarchy theme from a URL. |
| `remove_push_device` | string | optional | Forget the notification device with this id. |

## Plugin tools

Enabled plugins add tools named `<plugin>__<tool>`. Their parameters and results are whatever the plugin's own MCP server defines; `lasso mcp` lists them alongside the built-ins. See [Plugins](../plugins/index.md).

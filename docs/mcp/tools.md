---
title: Tools reference
description: Every tool lasso's /mcp server registers, with its parameters, defaults, return values and scope rules.
order: 41
nav_title: Tools
---

This page lists every tool on lasso's `/mcp` server. The tool descriptions an agent sees are longer and written for a model; this is the same contract for people. Every tool is also callable from a shell as `lasso mcp <tool>` (see [Shell commands](./cli.md)), with the parameters below as flags.

A parameter is **required** unless marked optional. Optional parameters that are omitted are absent, not zero: an empty `host` and no `host` at all can mean different things, as noted per tool.

## How `host` and scope work

Most tools take an optional `host`: lasso's own host or an SSH-config alias, exactly as [`list_hosts`](#list_hosts) shows it.

- **lasso's own host is called by its hostname.** On a box named `titan`, `list_hosts` lists it as `titan`, and every result, error and description that names it says `titan`, because to an MCP client "local" would mean the client's own machine. A `host` (or `machine`) argument takes the hostname in any case, and `local` too, as an alias. If an alias in lasso's SSH config also answers to the hostname (in any case), the alias keeps that name: lasso's host is then called `local` throughout, the one name that cannot be mistaken for it.
- **Omitting `host` targets your own host.** For a caller with a [per-host credential](./agent-scope.md), that is the host the credential was issued for. For any other caller (no `MCP_OAUTH`, the `UI_AUTH` basic credentials, or the main `MCP_OAUTH` client) it is lasso's own host. `whoami`, `close_agent` and `notify` handle an omitted host differently, as described under each.
- **A host must be addressable.** lasso can address its own host and the hosts with a concrete alias in the SSH config it reads. Anything else is refused up front, rather than answered from stale records.
- **A host must be within your reach.** A self-scoped credential reaches its own host plus whatever its [host groups](./agent-scope.md#groups-reach-between-hosts) add. A fleet-scoped credential, or an unidentified caller, reaches every addressable host. A refusal names the credential's host and reach and how to widen it.
- **Browser tools and plugin tools need lasso's own host in your reach**, because the browsers and plugins run on lasso's own machine. A credential scoped to some other host gets a tool error.

Scope only applies when `MCP_OAUTH` is set. With it unset, every caller is unidentified and reaches every addressable host.

Several tools take a `pane_id`: your own herdr pane id, the value of `$HERDR_PANE_ID` in your shell (for example `p_82`). The server runs in lasso's process, not your shell, so it cannot read your environment; you pass it. The public form `w<workspace>-<n>` is accepted too.

## Identity and discovery

### `list_hosts`

Lists the hosts lasso can drive: lasso's own host plus reachable, protocol-compatible SSH hosts, filtered to the ones your credential may address. Use a returned `host` as the `host` argument of the other tools.

| parameter | type | | description |
| --- | --- | --- | --- |
| `refresh` | boolean | optional | Re-probe every configured SSH host instead of answering from the background probe's cache. Slower. Use it when a host you just brought up is missing. |

Returns `active` (the host lasso booted on, which answers for a caller that names none, named the same way), `hosts`, and `probing`. Each entry in `hosts` has:

| field | meaning |
| --- | --- |
| `host` | the value to pass as `host` (lasso's hostname, or an alias) |
| `label` | display name (the same as `host`) |
| `reachable`, `running`, `compatible` | SSH reachable, herdr server up, herdr protocol matches the herdr on lasso's machine |
| `version` | the host's herdr version, when known |
| `state` | empty when the probe finished; `probing` while the first probe is still in flight; `timeout` when it ran out of time. Neither means the host is down. |
| `err` | the probe's failure detail, if any |
| `sessions` | the host's herdr sessions (`herdr session list`), each its own herdr server: `name`, `running`, `socket`, `default` (herdr's default session), and `current` on the one lasso drives there, which is the session every tool acts on |
| `sessions_error` | why a host that answered has no roster, such as a herdr too old to list sessions |

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

Returns an [agent object](#the-agent-object) with `status` `booting`. To wait for the agent to finish its task, call `wait_agent` right away: it does not match `idle` until the boot is done and the prompt's first turn has started (see [`wait_agent`](#wait_agent)).

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

Returns `agent`, an [agent object](#the-agent-object). `status` is `working`, `idle`, `done`, `blocked` or `unknown`, `booting` for a lasso agent that has not started yet, or `failed` for a boot that never came up.

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
| `status` | live herdr status (`working`, `idle`, `done`, `blocked`, `unknown`); `booting` while a lasso agent's boot runs and, when it was given a prompt, until its first turn has started; `failed` for a failed boot |
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

Safe to call right after `create_agent`. A lasso agent reads `booting` until its boot is done and, when it was given a prompt, until its first turn has started: lasso has seen it `working`, `blocked` or `done`, or herdr's `agent.get` reports a `completion_seq` (a turn that started and finished between two polls). So `idle` means the task's turn finished, not that the CLI just drew its first screen. `booting` gives way after 10 minutes, so a boot stranded by a lasso restart, or a prompt that never took, does not hold every wait to its timeout. A failed boot returns at once with `status` `failed` and `matched` false. An agent created without a prompt matches `idle` as soon as its boot is done and the CLI is at rest.

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

Opens a file in front of the human, in one of two places, picked by `app`: the lasso web app's sidebar file viewer (the Files panel beside their terminal) in every lasso tab they have open, or a window of its own in the Lasso Mac app on the Mac they are at. To show it in both, call it twice. Use it to show them something: a plan you just wrote, a file to review, the place a bug lives.

| parameter | type | | description |
| --- | --- | --- | --- |
| `path` | string | | Absolute path of the file or directory, or one starting with `~/` (expanded against the target host's home). Relative paths are refused because the server cannot see your working directory; `lasso open <path>` in a shell resolves one for you. |
| `host` | string | optional | Host the file lives on. Defaults to your own host, which is where your files are. |
| `line` | integer | optional | 1-based line to scroll to and select. Ignored for directories, images, PDFs, videos and audio. A markdown file opened at a line shows its raw editor instead of the rendered preview. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, used only to tell the human which agent opened it; without it they are told "an agent". |
| `app` | string | | `web` (the web app's sidebar viewer) or `mac` (a window in the Lasso Mac app). Required. |

The file must exist; a missing path is an error, so write it first. A directory opens the web sidebar's file tree rooted there instead of the viewer; the Mac app opens nothing for one.

Returns `delivered` (the number of lasso tabs or Mac apps the request reached), `web` and `mac` (the same number under the app it went to), `path` (the resolved absolute path), `host`, `dir` (true for a directory) and `detail`. **`delivered: 0` means the app you picked is not open and the human did not see it.** A tab that is hidden (a phone in a pocket, a background browser tab), or a Mac nobody is using, receives the request and ignores it, and a viewer holding unsaved edits is not replaced: the human gets a prompt offering to open the file instead.

## Browser

These tools manage lasso's [browsers](../concepts/shared-browser.md): which browsers exist, which pages are open, and which one the human is looking at. Each browser is separate: its own Chromium on lasso's machine with its own cookies and logins, or a remote browser lasso dials. Driving a page (clicking, typing, reading it) is the job of the `browser_*` tools (below) or raw CDP. All of them require a caller whose reach includes `local`, because the browsers run on lasso's machine.

A `browser` argument takes a browser's id (e.g. `work`) or its display name; omitted means the default browser. An unknown browser is an error listing the real ones.

### `browser_*` tools

chrome-devtools-mcp's tools, served on `/mcp` with a `browser_` prefix: `browser_new_page`, `browser_navigate_page`, `browser_click`, `browser_fill`, `browser_take_snapshot`, `browser_take_screenshot`, `browser_list_pages`, `browser_close_page` and the rest. Each takes chrome-devtools-mcp's own parameters plus an optional `browser`, and returns chrome-devtools-mcp's own result (screenshots as images). A `pageId` belongs to the browser it came from, so pass the same `browser` on every call about that page.

They are present when chrome-devtools-mcp is installed on lasso's machine; [`shared_browser`](#shared_browser) says why when they are not. Besides reach to lasso's own host, a call needs what `/cdp` needs: no foreign `Origin`, and under `UI_AUTH` alone the `UI_AUTH` basic credentials on the connection. A call that falls short is a tool error. See [Browser tools](./browser.md) for the details.

### `shared_browser`

Gets, and by default starts, a browser, and returns its raw CDP endpoints. Driving a page needs none of this (the `browser_*` tools start the browser themselves); it is for Playwright and other CDP clients, and for finding out whether the browser tools can run.

| parameter | type | | description |
| --- | --- | --- | --- |
| `start` | boolean | optional | Start the browser if it is not running. Defaults to true; false only reports its state. |
| `browser` | string | optional | Which browser. Omit for the default. |

Returns:

| field | meaning |
| --- | --- |
| `browser` | the browser these endpoints reach |
| `ws_endpoint` | absolute CDP websocket URL for this browser (`/cdp`, or `/cdp/p/<id>`), e.g. for Playwright's `chromium.connectOverCDP()` |
| `ws_path` | the path alone, to prefix with lasso's URL when `ws_endpoint` is empty |
| `http_endpoint` | the CDP HTTP base (`…/json/list`, `…/json/version`) |
| `browsers_url` | `GET` it for every browser and its own CDP endpoint (`/cdp/browsers`), the discovery path for a CDP client with no MCP |
| `browser_tools`, `browser_tools_reason` | whether this server's `browser_*` tools can run, and why not (usually chrome-devtools-mcp not installed on lasso's machine) |
| `available`, `running` | whether a Chromium is installed (or the remote browser answers), and whether it is running |
| `pages` | the pages open now (`id`, `url`, `title`) |
| `note` | why something is unavailable, and reminders such as which credentials `/cdp` needs |

### `list_browsers`

Lists lasso's browsers. Takes no parameters.

Returns `browsers`, each with `id`, `name`, `cdp_url` (for a remote browser), `default`, `running`, `tabs` (only while it runs), `ws_endpoint` (this browser's own CDP websocket) and `note`. Pass an `id` as `browser` to the `browser_*` tools to drive it.

### `create_browser`

Creates a browser: a separate Chromium with its own persistent cookies and logins. It appears in the Browser tab's browser picker at once and starts on first use. The `browser_*` tools drive it immediately, with no reconnect.

| parameter | type | | description |
| --- | --- | --- | --- |
| `name` | string | | Display name, e.g. `"Work"` or `"US exit"`. |
| `id` | string | optional | 1-32 lowercase letters, digits or dashes. Derived from the name when omitted. It is the `browser` value for the browser tools and appears in the CDP URL `/cdp/p/<id>`. |
| `cdp_url` | string | optional | Makes it a remote browser lasso dials instead of launching: its DevTools HTTP base, `http://host:port` or `https://host[:port]`. |

Returns the browser, in the same shape as a `list_browsers` entry.

### `update_browser`

Renames a browser and/or changes its `cdp_url`. Pass only what changes.

| parameter | type | | description |
| --- | --- | --- | --- |
| `browser` | string | | The browser to change. |
| `name` | string | optional | New display name. |
| `cdp_url` | string | optional | New remote browser address, or `""` for a browser lasso launches. Omit to leave it unchanged. |

At least one of `name` or `cdp_url` is needed. A `cdp_url` change lets go of the browser it was using, so its page ids are gone (call `browser_list_pages` again). The default browser can be renamed and pointed at a remote browser too. Returns the updated browser.

### `delete_browser`

Deletes a browser: stops its Chromium (closing its tabs and any session driving it) and **deletes its data directory**, with every cookie and login in it. This cannot be undone, so only do it when the human asked. The default browser cannot be deleted.

| parameter | type | | description |
| --- | --- | --- | --- |
| `browser` | string | | The browser to delete. |

Returns `deleted`, the id that was removed.

### `list_browser_tabs`

Lists open tabs, per browser.

| parameter | type | | description |
| --- | --- | --- | --- |
| `browser` | string | optional | Only this browser. Starts nothing: a stopped browser has no tabs. Omit for every browser. |

Returns `browsers`, each with `browser` (the id), `name`, `running` and `tabs` (`id`, `url`, `title`). Tab ids are CDP target ids, usable with `show_browser_tab`, `close_browser_tab`, and as `targetId` over CDP.

### `open_browser_tab`

Opens a **new** tab and puts it on the human's screen: every visible lasso tab switches its Browser tab to that browser and page, opening the sidebar if needed. Use it when the human asks you to open a page for them, or in a particular browser. It starts the browser if needed. The page loads from that browser, which for a browser lasso launches is lasso's machine, so `localhost` means lasso's machine there.

| parameter | type | | description |
| --- | --- | --- | --- |
| `url` | string | | A full `http(s)` URL. A bare `host[:port]` gets `https://` (`http://` for `localhost` and `127.x`). `about:blank` opens an empty tab. |
| `browser` | string | optional | Browser to open it in. Omit for the default. |
| `show` | boolean | optional | Put the tab on the human's screen. Defaults to true; false opens it quietly. |
| `surface` | string | optional | Which view shows it: `agent` (default), the live Chromium, or `iframe`, the page embedded the way a terminal link opens, loaded by the human's own browser (their cookies and their `localhost`). `live` and `embed` are accepted too; anything else is refused. An `http://` page on an https lasso cannot be embedded, so it stays on the live view. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, used only to tell the human which agent opened it. |

Returns `tab_id`, `browser`, `url`, `title`, `delivered`, `ws_endpoint` and `detail`. `delivered: 0` means no lasso tab is open and the human did not see it (the tab is still open in the browser). To drive the page afterwards, use the `browser_*` tools with the same `browser` (find the tab with `browser_list_pages` by its URL). A browser stops after lasso's idle timeout with nothing connected, and its tabs close with it.

### `show_browser_tab`

Puts an **existing** tab on the human's screen.

| parameter | type | | description |
| --- | --- | --- | --- |
| `tab_id` | string | | The tab to show (from `list_browser_tabs` or `open_browser_tab`). |
| `browser` | string | optional | The tab's browser. Omit for the default. |
| `surface` | string | optional | `agent` (default) or `iframe`, as on `open_browser_tab`. |
| `pane_id` | string | optional | Your `$HERDR_PANE_ID`, to tell the human who is showing it. |

Returns the same shape as `open_browser_tab`. `delivered: 0` means nobody saw it.

### `close_browser_tab`

Closes a tab. Close the tabs you opened when you are done; leave the human's and other agents' tabs alone unless asked.

| parameter | type | | description |
| --- | --- | --- | --- |
| `tab_id` | string | | The tab to close. |
| `browser` | string | optional | The tab's browser. Omit for the default. |

Returns `closed` (the tab id) and `browser`.

## Bots

These manage lasso's [bots](../web-ui/bots.md): long-lived agent sessions lasso keeps running. A bot's runtime is `omp` (omp in a herdr pane, driven through lasso's bridge) or `claude` (Claude Code in a herdr pane), on any host lasso drives. A bot is named by `name`, as `list_bots` shows it. A bot on a host outside your reach is answered as if it did not exist. Every bot on lasso's own machine has these tools through an MCP server named `lasso`, so a bot can change its own settings when its human asks. Everything the Bots view does has a tool here; signing in to an OAuth server still ends with the human approving at the provider. Design: [bots.md](../design/bots.md).

To talk to a running bot, use [`send_agent`](#send_agent) with its name as `to`.

### `list_bots`

Lists the bots on hosts within your reach. No parameters. Each entry has `name`, `host`, `dir`, `runtime`, `model`, `state` (`stopped`, `starting`, `idle`, `working` or `blocked`), `waiting_for` (why a blocked bot waits), `pane_id` (claude bots), `keep_running`, `channels`, and its conversation's newest line as `last_text` and `last_at`.

### `get_bot`

One bot's whole configuration: `bot` (the `list_bots` entry), `config` (its stored record: model, effort, permission mode, MCP servers and which are channels, strict MCP, extra args, avatar, picture, keep running, notify), `folder`, `env` (a secret's value is never shown), `skills` (its project skills) and `oauth` (each OAuth server's sign-in status). Takes `name`.

### `update_bot`

Changes a bot's settings. Only the fields you pass change.

| parameter | type | | description |
| --- | --- | --- | --- |
| `name` | string | | The bot. |
| `runtime` | string | optional | `omp` or `claude`. Only while the bot is stopped (refused otherwise: `stop_bot`, `update_bot`, `start_bot`). Model and permission mode reset to the new runtime's default unless passed in the same call; effort is kept when the new runtime has that level. MCP servers (signed-in ones too), env, jobs and `.claude/skills` carry over, and the next start is a new conversation. A switch to claude with only an `AGENTS.md` writes a `CLAUDE.md` of `@AGENTS.md`; a switch to omp with only a `CLAUDE.md` copies it to `AGENTS.md`. |
| `dir` | string | optional | The bot's folder on its host. Only while the bot is stopped. |
| `model` | string | optional | A claude bot's Claude model (alias or full name), or an omp bot's provider/model selector such as `anthropic/claude-opus-5`. `""` for the default. |
| `effort` | string | optional | claude: `low`, `medium`, `high`, `xhigh` or `max`. omp: `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` or `auto`. `""` for the default. |
| `permission_mode` | string | optional | claude: `acceptEdits`, `auto`, `bypassPermissions`, `manual`, `dontAsk` or `plan`. omp (its approval mode): `yolo`, `write` or `always-ask`. `""` for the default. |
| `mcp` | array | optional | The **complete** list of MCP servers. It replaces the current one, so read it with `get_bot` first. `channel: true` makes a server a channel; a server that needs OAuth takes `oauth: true` and is signed in with `begin_bot_oauth`. |
| `strict_mcp` | boolean | optional | claude bots: only these MCP servers, without claude.ai connectors or user-level servers. |
| `extra_args` | array | optional | Extra `claude` or `omp` arguments, one element per argument. |
| `keep_running` | boolean | optional | Relaunch the bot when it stops unexpectedly. |
| `notify` | boolean | optional | Push a notification to the human's devices each time the bot finishes a reply. |
| `auto_approve` | boolean | optional | claude bots: answer every permission prompt Yes, its subagents' included, so the bot never waits on one. Bypass mode alone still asks for some commands (Claude Code's safety checks, such as an `rm` on a variable path). `AskUserQuestion` and plan approval still reach the human. Read at launch. |

lasso rewrites the bot's launch script and MCP config. Returns `ok` and `restart_needed`, true when the bot is running and reads the change only at launch. A bot restarting itself runs `mise run restart` in its folder. Its instructions (`AGENTS.md` for an omp bot, `CLAUDE.md` for a claude bot) and skills are files in its folder, edited directly rather than through this tool.

A claude bot signs in to Claude with its host's Claude Code login. On a host with none, give it a `CLAUDE_CODE_OAUTH_TOKEN` secret with `set_bot_env` (the value `claude setup-token` prints, run once by the human wherever they are logged in); its launch marks Claude Code's first-run setup done on that host, so nothing waits on a screen.

### `set_bot_env`

Sets one of a bot's environment variables: `name`, `key`, `value`, and `secret: true` to store it encrypted by fnox (the value never comes back). `LASSO_OAUTH_*` keys are lasso's own OAuth credentials and are refused. Returns `ok` and `restart_needed`. A secret is better typed into the Bots view by the human than passed through a conversation.

### `unset_bot_env`

Removes one of a bot's environment variables: `name` and `key`. `LASSO_OAUTH_*` keys are refused. Returns `ok` and `restart_needed`.

### `set_bot_avatar`

Sets a bot's picture, shown in the Bots view and on its notifications, from an image file on the bot's host. No restart is needed.

| parameter | type | | description |
| --- | --- | --- | --- |
| `name` | string | | The bot. |
| `path` | string | optional | An absolute or `~/` path to a PNG, JPEG, WebP or GIF of at most 2 MB. The type is read from the file's content. |
| `clear` | boolean | optional | Remove the picture, back to the text avatar. |

### `list_bot_jobs`, `create_bot_job`, `update_bot_job`, `delete_bot_job`, `run_bot_job`, `rotate_bot_job_key`, `list_bot_job_events`, `preview_bot_schedule`

A bot's jobs: messages lasso delivers into its session (a claude bot's through its `lasso-channel`, an omp bot's as prompts), on a schedule, from a webhook, or when run by hand. A job with a `command` is a watch: each firing runs the command in the bot's folder on the bot's host (as lasso's user, or on another host the user lasso connects as; minimal environment) and delivers only what it prints, with failures reported damped (1st, 2nd, 4th, 8th… in a row, plus one recovery notice). Each takes the bot's `name`, and all but `list_bot_jobs` take `job`. Changes apply at once, with no restart.

- `list_bot_jobs` returns `jobs` (each with `message`, `cron`, `once_at`, `timezone`, `enabled`, `webhook`, `webhook_path`, `webhook_key`, `next_at`, `last`, `queued`, and for a watch `command`, `timeout`, `running`, `fail_streak` and the newest run's `last_run_at`, `last_run_result` (`quiet`, `output`, `error`, `timeout`), `last_run_exit`, `last_run_ms`, `last_run_note`) and `channel` (`available`, `connected`). A webhook's URL is lasso's own origin + `webhook_path` + `?key=` + `webhook_key`.
- `create_bot_job` takes `message`, `cron` (5-field cron, several joined with `;`; empty for none), `once_at` (a single run instead of `cron`: `2026-11-20T08:00` read in `timezone`, or RFC 3339; must be in the future), `timezone` (IANA, default UTC), `webhook`, `paused`, `command` and `timeout` (seconds one run may take, 1-3600, default 60).
- `update_bot_job` changes only what it is passed: `rename`, `message`, `cron`, `once_at`, `timezone`, `webhook`, `enabled`, `command` (empty makes it an ordinary job), `timeout`.
- `run_bot_job` fires the job now (a paused one too) and returns `event_id` and `status`: `pending`, or `dropped` when the bot is stopped. A watch runs its command, waiting up to 20 seconds, and can also answer `quiet` (no output, nothing delivered), `failed` (with `exit` and `detail`; `event_id` is set when the failure was reported rather than damped), `running` (still going; it delivers only if it prints) or `busy`.

- `rotate_bot_job_key` gives a webhook job a new key (the old URL stops working) and returns the job; `list_bot_job_events` returns its last 20 firings; `preview_bot_schedule` takes `cron` or `once_at` and `timezone` and answers `next` (the next fires) or `error`.

A claude bot on another host has jobs when that host's lasso can deliver them (`channel.available` says why not); lasso writes them into the bot's folder over SSH and nothing on that host connects to lasso. Such a bot has no `lasso` server, so these tools are its human's to call, not its own.

### `get_bot_talk`, `enable_bot_talk`, `update_bot_talk`, `rotate_bot_talk_key`, `disable_bot_talk`

A claude bot's Talk: an OpenAI-compatible door (Chat Completions at lasso's origin + `/talk/<bot>/v1`, the key as a Bearer token, the bot's name as the model) for a chat app on someone's phone, such as Conduit. Their messages reach the bot through its `lasso-channel` and its replies go back to the app. Each takes the bot's `name` and returns `talk`:
- `enabled`, `path`, `key` and `model`
- `activity`: `plain` or `detailed`
- `state`, one of:
  - `ready`
  - `waiting`: the bot is starting, and messages wait
  - `restart`: its channel predates Talk; restart the bot
  - `unavailable`
- `reason`
- `last_at`

- `enable_bot_talk` turns Talk on with a new key. It changes nothing when Talk is already on, and refuses an omp bot.
- `update_bot_talk` takes `activity`. It sets what the app shows while the bot works: `plain` (only the kind of each step, the default) or `detailed` (each step's subject too: file names, commands).
- `rotate_bot_talk_key` replaces the key; the old one stops working at once. `disable_bot_talk` turns Talk off and forgets the key.

Whoever holds the key can ask the bot for anything its instructions allow, so a bot's CLAUDE.md should say what it does for them.

### `create_bot`, `delete_bot`, `reorder_bots`

- `create_bot` makes a bot as the Bots view does: `name`, and optionally `host`, `runtime` (`omp` by default), `dir` (default `~/bots/<name>`; an existing folder keeps its instructions and skills), `workspace`, `model`, `effort`, `permission_mode`, `mcp`, `strict_mcp`, `extra_args`, `keep_running` and `notify` (both default true), `instructions` (its first `AGENTS.md` or `CLAUDE.md`) and `start`. Returns `bot`, plus `error` when the row was made but its folder or first start failed, and `note`.
- `delete_bot` stops the bot if it runs and forgets it, with its jobs. `delete_folder: true` also deletes its folder, refused for a folder that holds the host's home or lasso's own directory.
- `reorder_bots` takes `names`, the Bots view's order.

### `set_bot_model`

The chat header's model and thinking pickers: `name`, `model`, `thinking`. A running omp bot switches at once; anything else is saved for the next start (`restart_needed`).

### `list_bot_sessions`, `switch_bot_session`

A bot's conversations, newest first (`id`, `title`, `preview`, `updated_at`, `messages`, `current`), and switching: `new: true` or an `id` to resume. An omp bot switches in place, a claude bot restarts onto it; refused mid-turn.

### `read_bot_chat`, `send_bot`, `stop_bot_turn`, `answer_bot`

An omp bot's chat, as the Bots view shows it. `read_bot_chat` returns the newest page (`before` pages back) and `pending_ui`, the questions and approvals it waits on; `answer_bot` answers one by `id` with `value` (an option's label, or text), `confirmed`, or `cancelled`. `send_bot` sends a message (a steer while it works), and `stop_bot_turn` stops the current turn. Each answers `outcome`, `refused` with `detail` when it did not land. A claude bot's chat is its pane's: use `read_agent` and `send_agent`.

### `list_skill_library`, `add_bot_skill`, `remove_bot_skill`

`list_skill_library` lists a `host`'s `~/.claude/skills`. `add_bot_skill` copies a skill directory (`from`, with a `SKILL.md`, on the bot's host) into the bot's `.claude/skills`; `remove_bot_skill` deletes one by `skill`. Both return the bot's `skills`.

### `begin_bot_oauth`, `finish_bot_oauth`, `sign_out_bot_oauth`

Signing one of a bot's MCP servers (`oauth: true`) in. `begin_bot_oauth` takes `name`, `server` and optionally `origin`, the address the human opens lasso at (default: the one this MCP request reached lasso on). It returns `authorize_url` for the human to open and approve; the provider sends the browser back to lasso, which completes the sign-in. When `localhost` is true the browser lands on an address that does not load, and `finish_bot_oauth` takes that whole address as `url`. `sign_out_bot_oauth` deletes a server's tokens. The tokens stay in the bot's fnox and never appear in a reply; `get_bot`'s `oauth` shows the status.

### `start_bot`, `stop_bot`, `restart_bot`

Each takes `name`. `start_bot` and `restart_bot` resume the bot's last conversation unless `fresh` is true. `stop_bot` ends its process (a claude bot's pane closes) and keeps it stopped (keep-running leaves it alone) until it is started again; the conversation stays on disk. Each returns `bot`, the bot's `list_bots` entry after the change.

## Settings

These tools read and change what lasso's Settings tab shows. Each write runs through the same handler the Settings tab calls, so the validation is the same and every open lasso tab updates live. Both require a caller whose reach includes lasso's own host, because the settings live in lasso's own database; `agents` and `repos` also need the `host` they name.

**Plugin approvals stay with the human.** Enabling a plugin approves its permissions, trusting one runs it outside its sandbox, its isolation moves it between sandboxes, and installing or updating one changes what an approval covers. Those need a human looking at the approval dialog in the Settings tab, and `/mcp` is open by default, so no MCP tool does them: an agent, or a page an agent read, must not be able to grant itself a plugin. What only takes power away or reads is here: disable, restart, uninstall, unlink, reload and the log.

### `get_settings`

| parameter | type | | description |
| --- | --- | --- | --- |
| `section` | string | optional | One of `ui`, `agents`, `repos`, `theme`, `notifications`, `browser`, `transcribe`, `plugins`. Omit for all of them. |
| `host` | string | optional | Whose creator settings `agents` and `repos` show. Defaults to lasso's own host. |
| `plugin_log` | string | optional | A plugin's name: also return its server's recent log lines under `plugin_log`. |
| `lines` | integer | optional | How many `plugin_log` lines, default 200, at most 500. |

Returns one key per section:

| section | contents |
| --- | --- |
| `ui` | The synced UI preferences (`ui_state`): appearance mode and palettes, backdrops, typography, chat and terminal text, browser mode, sidebar tabs, usage tracking, creator host, grid grouping and the rest. |
| `agents` | `host`'s creator defaults (`repos_root`, `branch_prefix`, `default_agent`, `default_terminal_workspace`, `scratch_setup`) and `auto_title`. |
| `repos` | Each repo under `host`'s repo roots with its `copy_files` and `setup`. |
| `theme` | herdr's current theme, the selectable `themes`, `sync_agent_themes`, `theme_sync_off` and the installable `catalog`. |
| `notifications` | Registered push `devices`, by id and label, each with its notify `device` id and `kinds`. Endpoints are never returned. |
| `browser` | The browsers' status. `list_browsers` and the other browser tools manage them. |
| `transcribe` | The speech-to-text provider `/api/transcribe` uses: `provider` (`off`, `groq`, `openai`, `openai-compatible`), `model`, `base_url` and `key_name`, the NAME of the key's variable. Never a key. |
| `plugins` | Each plugin's state. |

A section that fails (an unreachable host, say) is listed under `errors` and the others are still returned.

The sections are lasso's stored settings as the Settings tab reads them, so a host stored inside one (`ui.creator_default_host`, `theme_sync_off`, an `agent_pins` key) is lasso's internal key: `local` for lasso's own host. Write those values back in the same form. The `host` arguments take either name.

### `update_settings`

Pass only what changes; an omitted field is left alone. Returns the affected sections as they now read.

| parameter | type | | description |
| --- | --- | --- | --- |
| `ui` | object | optional | A patch of the `ui` section. Only the keys you send change. `typography`, `chat_text`, `terminal_text`, `phone_text` and `theme_atmosphere` merge per field, and `""` or `null` deletes one; `sidebar_tabs` is replaced whole. Pinned agents change through `agent_pins: {<key>: true\|false}`, dismissed agent cards through `card_dismiss: {<key>: <transcript_at ms>\|null}`, and the background gallery through `remember_background` / `forget_background`. |
| `auto_title` | boolean | optional | Name new agents from their prompt. |
| `agent_defaults` | object | optional | `host`'s creator defaults: any of `repos_root`, `branch_prefix`, `default_agent`, `default_terminal_workspace`, `scratch_setup`. `""` clears one. |
| `repo` | object | optional | `{path, copy_files?, setup?}`: one repository's per-repo settings on `host`. |
| `host` | string | optional | Whose creator settings `agent_defaults` and `repo` write. Defaults to lasso's own host. |
| `theme` | string | optional | Switch herdr's theme fleet-wide. Refused while an appearance palette is set; change `ui.palette_light` / `palette_dark` / `appearance_mode` instead. |
| `sync_agent_themes` | boolean | optional | Mirror the theme into agent CLIs' own theme files. |
| `theme_sync` | object | optional | `{host, enabled}`: stop or resume theme writes to one host. |
| `sync_theme_now` | boolean | optional | Push the current theme to every reachable host. Runs in the background; the outcome arrives as a toast in lasso. |
| `install_theme_url` | string | optional | Install an Omarchy theme from a URL. |
| `remove_push_device` | string | optional | Forget the notification device with this id. |
| `transcribe` | object | optional | Any of `provider`, `model`, `base_url`, `key_name`; only the fields given change, `""` resets one to the provider's default, and changing `provider` resets the fields not given. `key_name` names a variable or secret; it never holds a key. |
| `telemetry` | boolean | optional | Anonymous usage analytics on or off. |
| `notify_device` | object | optional | `{id, label?, kinds?}`: one device's label and which notification kinds interrupt it (`kind: true\|false\|null`, null back to the default). `id` is a device's `device` in `notifications`. |
| `test_push` | boolean | optional | Send a test notification to every subscribed device; returns each one's result. |
| `browser` | object | optional | `{browser?, action}`: `start`, `stop` or `restart` one of lasso's browsers (default the default one). |
| `plugin` | object | optional | `{name, action, purge_data?}`: `disable`, `restart`, `uninstall` (a GitHub install; `purge_data` also deletes its data) or `unlink` (a linked one). Anything else is refused. |
| `reload_plugins` | boolean | optional | Rescan the plugins directory. |

## Plugin tools

Enabled plugins add tools named `<plugin>__<tool>`. Their parameters and results are whatever the plugin's own MCP server defines; `lasso mcp` lists them alongside the built-ins. See [Plugins](../plugins/index.md).

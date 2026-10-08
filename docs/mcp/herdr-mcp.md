---
title: The herdr MCP server
description: herdr's own socket API as MCP tools at /herdr-mcp, one tool per herdr method, on any host lasso drives.
order: 44
nav_title: Herdr MCP
---

`/herdr-mcp` serves [herdr](https://herdr.dev)'s socket API as MCP tools: **one tool per herdr method**, generated from the schema the installed herdr binary prints (`herdr api schema --json`). It is the tool surface of the standalone [herdr-mcp](https://github.com/Orange-County-AI/herdr-mcp) bridge, served from lasso itself, so there is no second process to deploy.

Use it when an agent needs herdr's raw API (split a pane, send keys, read a pane's scrollback, run a herdr plugin action) rather than lasso's higher-level agent tools on [`/mcp`](./tools.md).

## Adding it

```bash
claude mcp add --transport http lasso-herdr http://127.0.0.1:8090/herdr-mcp
```

[`lasso connect`](../getting-started/connect-agents.md) registers it as `lasso-herdr` alongside `lasso` and `lasso-browser`, once lasso's `/herdr-mcp` lists herdr's tools. `-herdr=false` skips it. From a shell, `lasso mcp -herdr` lists and calls the same tools (`lasso mcp -herdr pane-list -host gigachad`).

## The tools

A herdr method becomes a tool by replacing `.` and `-` with `_`: `pane.list` is `pane_list`, `agent.read` is `agent_read`, `pane.wait_for_output` is `pane_wait_for_output`. The names, titles, descriptions and read-only/destructive annotations are the ones herdr-mcp produces, so a client written for the bridge works unchanged. herdr 0.9.3 yields 94 method tools.

Left out, as herdr-mcp leaves them out: `events.subscribe` (a stream, which a tool call cannot return), the lifecycle reports an agent makes about itself (`pane.report_agent`, `pane.report_agent_session`, `pane.report_metadata`, `workspace.report_metadata`, `pane.clear_agent_authority`, `pane.release_agent`), `pane.graphics.*`, and `server.ssh_agent.register`.

Also carried over from the bridge:

- **Argument checks before the call.** An unknown argument is refused with the accepted names and the nearest match (`did you mean "source"?`), and an enum value outside the allowed set is refused with the allowed values.
- **Argument aliases.** `worktree_create` takes `repo`/`repo_path` for `cwd`, `agent_prompt` takes `prompt`/`message`/`body` for `text`, `agent_start` takes `agent`/`harness` for `kind`. The result notes the mapping.
- **Agent readiness.** `agent_start` waits (up to its `timeout_ms`, default 30s) for the new agent to be interactive before answering, so the next `agent_prompt` is not typed into a harness still drawing its first screen. `agent_wait` on an agent that is still launching waits for it to become addressable first.

One tool is not a herdr method: **`machine_list`** lists the hosts you may pass as `host`. It answers what `/mcp`'s `list_hosts` answers for the same credential.

## Hosts

Every tool takes an optional `host`: `local` (the box lasso runs on) or an ssh alias from lasso's ssh config, exactly as `list_hosts` shows them. Omit it for **your own host**, which is the host your credential names, or `local` when the caller is unidentified. Calls go through lasso's own connection to that host's herdr, the same one the web UI and `/mcp` use.

`machine` is accepted as an alias of `host`, so a herdr-mcp caller's `{"machine": "minime"}` still routes. It names a lasso host, not a herdr saved-machine profile id; on a fleet where the ssh alias and the herdr machine label match, they are the same thing.

- **IDs are per host.** Two hosts can both have `w1:p1` or an agent named `reviewer`. List on the host you mean to drive.
- **A host that is down fails that call only**, as a tool error naming the host. Other hosts keep answering, and the call never falls back to another host. A connection error does not prove a mutation was not applied: check before retrying.
- **The attached-client methods** (`client_window_title_set`/`_clear`, `client_shell_surface_set`, `popup_close`, `product_announcement_dismiss`, `release_notes_dismiss`, `server_live_handoff`) act on the herdr client attached to a session. There is none at the far end of an SSH-forwarded socket, so these take no `host` and run on lasso's own box.

## Authentication and reach

`/herdr-mcp` is gated exactly like `/mcp`: open by default, exempt from `UI_AUTH`, and with `MCP_OAUTH` set it needs a lasso bearer token or the `UI_AUTH` basic credentials. Every call then checks the target host against the caller's credential, the same rule as `/mcp`'s tools: a `self`-scoped credential reaches its own host plus whatever its [host groups](./agent-scope.md) add, and is refused anywhere else. A host-scoped credential that works on `/mcp` works here unchanged.

The attached-client methods run on `local`, so they need a caller whose reach includes lasso's own machine.

## Keeping up with herdr

lasso reads herdr's schema at startup (retrying every 30s until it loads) and again every 5 minutes. When the document's digest changes, the tool set is swapped in place: new methods appear, removed ones stop being advertised, and connected clients receive `tools/list_changed`. No restart. The digest, not the protocol number, is the trigger, because herdr has added methods inside one protocol version.

If the schema cannot be read (herdr missing or mid-upgrade), the tools already registered stay. Until the first load succeeds `/herdr-mcp` serves only `machine_list`, and `lasso connect` does not register it. The tools follow the installed **binary**; when that is newer than the running herdr server, lasso logs a warning and calls to methods the server does not know yet fail with herdr's own error until herdr restarts.

## Timeouts and outages

Each call opens its own connection to the host's herdr socket, and lasso keeps one SSH connection per remote host, health-checked and redialled when it dies. There is no queue in front of herdr: a call made while herdr is down fails at once with the reason instead of waiting for it to come back.

A call waits at least 60s for herdr's answer (longer for the slow mutations lasso already allows more time, such as `worktree_create`). The blocking methods (`agent_wait`, `agent_prompt`, `events_wait`, `pane_wait_for_output`) wait up to 15 minutes, or the call's own `timeout_ms` plus 15s when given. A client that disconnects ends its call.

## The standalone herdr-mcp

You no longer need to run herdr-mcp beside lasso: `/herdr-mcp` serves the same tools on lasso's port, behind lasso's auth. The standalone bridge still works and is still the choice where there is no lasso, for example on a machine that runs only herdr.

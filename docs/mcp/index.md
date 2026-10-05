---
title: MCP
description: The two MCP servers lasso exposes, what each is for, how agents connect to them, and how they are secured.
order: 40
nav_title: Overview
---

lasso exposes its features to agents over the [Model Context Protocol](https://modelcontextprotocol.io). An agent connected to lasso can spawn, inspect and close **other** agents on any host lasso can reach, push a notification to your phone, open a file in your sidebar, and drive the shared browser you are watching.

## Two servers

lasso serves two streamable-HTTP MCP servers from the same port:

| name | path | what it is for |
| --- | --- | --- |
| `lasso` | `/mcp` | Orchestration: hosts, repos and branches, creating and closing agents, `whoami`, `notify`, `open_file`, and managing the shared browser's profiles and tabs. Enabled plugins add their tools here. |
| `lasso-browser` | `/browser-mcp` | Google's [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp) tool set (navigate, click, fill, screenshot, console, network, performance traces), already pointed at lasso's [shared browser](../concepts/shared-browser.md). |

They are two servers because they are two jobs. An agent that only orchestrates is not handed thirty browser tools, an agent that only browses is not handed the power to spawn agents, and each can be added, gated or removed on its own.

- [Tools reference](./tools.md) lists every tool on `/mcp` with its parameters.
- [The browser MCP server](./browser-mcp.md) covers `/browser-mcp` and the raw CDP endpoint at `/cdp`.
- [Shell commands](./cli.md) covers `lasso mcp`, `lasso notify`, `lasso open` and `lasso closeme`, which reach the same tools from a terminal.

## Connecting an agent

On the machine an agent runs on:

```bash
lasso connect
```

It registers both servers with every agent CLI it finds there (Claude Code, Codex, OpenCode, omp), after checking that lasso answers. On a machine other than lasso's, pass the URL that machine reaches lasso on: `lasso connect -url https://lasso.example.com`. See [Connect your agents](../getting-started/connect-agents.md) for the details and the manual equivalent, which for Claude Code is:

```bash
claude mcp add --transport http lasso         http://127.0.0.1:8090/mcp
claude mcp add --transport http lasso-browser http://127.0.0.1:8090/browser-mcp
```

Other CLIs add the same two URLs as streamable-HTTP MCP servers.

## lasso orchestrates agents; it does not talk to them

The `/mcp` tools create, list, inspect and close agents. There is deliberately no tool to prompt an agent, read its screen, or wait for it to finish. That is herdr's job:

```bash
herdr agent prompt <target> "..."
herdr agent read <target>
herdr agent wait <target>
```

A Claude Code session can also use its own native agent messaging. `get_agent` reports an agent's configuration and live status (`working`, `idle`, `blocked`, `unknown`, or `failed` for a boot that never came up), which is enough to decide when to reach for herdr.

## Plugin tools

An enabled [plugin](../plugins/index.md) with an MCP server has its tools mirrored onto `/mcp` as `<plugin>__<tool>`, for example `hello__greet`. They appear and disappear as plugins are enabled and disabled, and connected clients are told the tool list changed. Calling one requires a caller whose reach includes lasso's own machine, like the browser tools.

## What the server tells the model

Every MCP session receives a short set of instructions at `initialize`, so an agent learns the house rules without reading these docs:

- `notify` reaches the human's phone. Use it only for a decision, a blocking question, or a long job finishing while they are away, and check the reply: `"sent": false` means nobody received it.
- The shared browser has profiles, each its own Chromium with its own cookies and optional proxy. One `/browser-mcp` URL drives all of them through a `profile` argument, so a new profile never needs a new MCP server. `open_browser_tab` puts a page on the human's screen.
- lasso does not talk to agents. Use herdr (`herdr agent prompt` / `read` / `wait`) or the harness's native messaging.
- Host reach is bounded by the calling credential, so an empty listing usually means containment is working, not an outage.
- An agent in a lasso-created pane passes its `$HERDR_PANE_ID` to `whoami` to find its own record, and shuts itself down with `close_agent`, never `herdr pane close` (which leaves the agent record and staged prompt files behind).
- To show the human what it is doing, an agent puts a one-line summary on its pane's status card with `herdr pane report-metadata "$HERDR_PANE_ID" --source agent:self --token summary="<what you're doing>" --ttl-ms 1800000`, and does not use `herdr pane report-agent`, which overrides herdr's own status detection.

`/browser-mcp` sends its own instructions: the human's Browser tab shows one page (the most recently opened), so open your own page and close it when done; `localhost` means lasso's machine; and the accounts logged into the browser are the human's, so posting, sending, accepting or buying needs their go-ahead.

## Authentication at a glance

`/mcp` is **open by default**. That is the same trust model as lasso's file endpoints: fine on loopback, on a private tailnet, or behind an edge gate such as Cloudflare Access, and not fine on an address strangers can reach.

| setup | `/mcp` | `/browser-mcp` and `/cdp` |
| --- | --- | --- |
| nothing set | open | open |
| `UI_AUTH=user:pass` only | open | `UI_AUTH` basic credentials |
| `MCP_OAUTH=client_id:secret` | a lasso bearer token, or the `UI_AUTH` basic credentials | the same, and a token's scope must include lasso's own machine |
| `-require-access-header` | every request also needs Cloudflare Access's identity header | same |

- **`MCP_OAUTH`** turns lasso into a small OAuth 2.1 authorization server for its own `/mcp`. It is what makes per-host credentials, scoping and groups take effect. See [OAuth](./oauth.md).
- **Cloudflare Access** in front of lasso gates everything at the edge. With `MCP_OAUTH` unset, claude.ai and Claude Desktop connectors can sign in through Access's Managed OAuth. See [OAuth](./oauth.md#claudeai-and-claude-desktop-connectors) and [Cloudflare deployment](../deployment/cloudflare.md).
- **Per-host credentials and groups** bound which hosts' agents a caller can see and manage. See [Agent scope](./agent-scope.md).

Regardless of auth, `/browser-mcp` and `/cdp` refuse any request that a different website's page sends from your browser, so a site you visit cannot drive the shared browser through a lasso on your own machine.

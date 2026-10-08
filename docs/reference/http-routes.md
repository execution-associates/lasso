---
title: HTTP routes
description: lasso's route table, and which auth gate guards each route.
order: 99
---

Everything lasso serves comes from one listener (`-listen`). The browser UI uses the `/api/*` routes; agents use `/mcp`, `/herdr-mcp`, `/browser-mcp` and `/cdp`. The `/api/*` routes are lasso's own UI API, not a stable public interface: they can change between releases. Script lasso through [`lasso mcp`](../mcp/cli.md) or the MCP server instead.

## Auth gates

A request passes through these layers, outermost first:

1. **Internal CDP token.** lasso's own chrome-devtools-mcp children reach `/cdp` with a random per-process token, honored on `/cdp` paths only, ahead of every other gate.
2. **The Access header gate**, when `-require-access-header` is on: every route, with no exemptions, answers 403 without a non-empty `Cf-Access-Authenticated-User-Email` header (or with one not in `-access-allowed-emails`). Off by default, and nothing reads the header while it is off.
3. **UI_AUTH basic auth**, when `UI_AUTH` is set: every route **except** `/mcp`, `/herdr-mcp`, `/cdp`, `/browser-mcp`, `/.well-known/oauth-protected-resource`, `/.well-known/oauth-authorization-server`, `/oauth/register` and `/oauth/token` (and their subpaths). `/oauth/authorize` stays behind it on purpose: that is where a human approves a client.
4. **Per-route gates** on the exempt routes:

| route | gate | open by default? | with `UI_AUTH` only | with `MCP_OAUTH` |
| --- | --- | --- | --- | --- |
| `/mcp` | `withMCPAuth` | yes | open | bearer token or `UI_AUTH` basic credentials |
| `/herdr-mcp` | `withMCPAuth` | yes | open | same as `/mcp`; each call's host is checked against the token's scope |
| `/cdp` | `withCDPAuth` | yes | `UI_AUTH` basic | bearer token or `UI_AUTH` basic; the token's scope must include lasso's own machine |
| `/browser-mcp` | `withBrowserMCPAuth` | yes | `UI_AUTH` basic | same as `/cdp` |

`/cdp` and `/browser-mcp` also refuse any request whose `Origin` names a different website, before any credential check and whatever the auth settings. That is what stops a page you visit from driving a lasso on your own machine. Under `MCP_OAUTH` without `UI_AUTH`, lasso's own Browser tab (same origin, no `Authorization` header) still reaches `/cdp`.

So on a loopback or tailnet lasso with no `UI_AUTH` and no `MCP_OAUTH`, **everything is open** to whoever can reach the port. That is the intended trust model for a private network; put an edge gate (Cloudflare Access) or `UI_AUTH` in front of anything wider. See [Security](../security.md).

## Routes

### Terminals and live state

| route | purpose |
| --- | --- |
| `/terminal/<host>/` | Reverse proxy (HTTP and WebSocket) to the ttyd running herdr for one host. |
| `/shell/<host>/` | Reverse proxy to the ttyd running the Terminal tab's shell for one host. Absent with `-spawn-ttyd=false`. |
| `/api/events` | Server-sent events: focused pane, layout, theme and state revisions, pushed to the browser. |
| `/api/host` | `POST`: attach the calling tab to a host (spawns its terminals if needed). It switches nothing for other tabs. |
| `/api/hosts` | The host list from `~/.ssh/config`, with each host's reachability and herdr version. |
| `/api/active`, `/api/panes`, `/api/all-panes` | The focused pane; one host's panes; panes across the fleet. A pane's `transcript_at` is its log's mtime on whichever host has the log. |
| `/api/focus`, `/api/close`, `/api/rename`, `/api/workspace-rename` | Focus, close or rename herdr panes, tabs and workspaces. |
| `/api/clients`, `/api/term-claim` | The browsers attached to this lasso; which tab owns the terminal's size. |
| `/api/ui-state` | Read and patch the shared UI state stored in `lasso.db`. |

Most `/api/*` routes act on the calling tab's host, sent as the `X-Lasso-Host` header or a `?host=` parameter. See [Hosts](../concepts/hosts.md).

### Files and diffs

| route | purpose |
| --- | --- |
| `/api/files`, `/api/file` | List a directory; read a file. |
| `/api/file-write`, `/api/file-rename`, `/api/file-delete`, `/api/file-upload` | Write, rename, delete, upload. |
| `/api/paste-file` | Save a pasted or dropped file into `uploads/dropped-files` on the target host. |
| `/api/diff`, `/api/diff-file` | The focused repo's changed files; one file's diff. |

**These read and write arbitrary absolute paths as the user lasso runs as**, on the calling tab's host or any host named in the request. Anyone who can reach them can read your SSH keys and overwrite your shell profile. They are only as safe as the gate in front of lasso.

### Agents

| route | purpose |
| --- | --- |
| `/api/create-agent`, `/api/agent-upload` | Create an agent; stage its attachments. |
| `/api/agent/close`, `/api/agent/reopen`, `/api/agent-history` | Close an agent (what `lasso closeme` calls); reopen a closed agent's directory; past agents for the pane switcher. |
| `/api/create-terminal`, `/api/workspaces` | Open a plain terminal; list workspaces to put it in. |
| `/api/agent-config`, `/api/repo-config`, `/api/repos`, `/api/repo-branches`, `/api/auto-title` | New dialog settings, repos and branches. |
| `/api/chat`, `/api/chat/send`, `/api/chat/answer` | The chat view: read an agent's transcript, send it a message, answer its question dialog. `/api/chat`'s `host` is the pane's machine; the log may be served from another host lasso drives, named by `served_by`. An empty answer carries `unavailable: {reason, checked, unanswered}`. |

### Settings, themes and maintenance

| route | purpose |
| --- | --- |
| `/api/theme`, `/api/theme-set`, `/api/theme-sync` | The resolved theme; change it; push it to every host now. |
| `/api/omarchy-themes`, `/omarchy/bg/…`, `/omarchy/thumb/…` | The theme catalog; background images and thumbnails. |
| `/api/version`, `/api/self-update` | Versions and herdr compatibility; update a supervised source checkout (403 with `-disable-self-update`). |
| `/api/host-update`, `/api/host-provision` | Update herdr on a host; install and start herdr on a host that lacks it. |
| `/api/usage` | Provider rate-limit windows for the usage footer. |
| `/api/frameable` | Whether a URL can be framed, for the Browser tab's iframe mode. |
| `/api/browser`, `/api/browser/profiles[/…]` | Shared-browser status and profiles. |
| `/api/push`, `/api/push/subscribe`, `/api/push/unsubscribe`, `/api/push/test` | Web Push devices. `subscribe` accepts only an `https` endpoint with a valid P-256 key. |
| `/api/log` | With `-dev` only: browser log sink. |

### Plugins

| route | purpose |
| --- | --- |
| `/api/plugins`, `/api/plugins/…` | The plugin listing and its actions (enable, trust, install, logs, …). Listed in [Writing a plugin](../plugins/authoring.md#http-api). |
| `/plugins/<name>/…` | An enabled plugin's tab pages and font files. Every response, 404s included, carries `Content-Security-Policy: sandbox …`, which makes the page an opaque origin that cannot use your session. |

### MCP, OAuth and the browser

| route | purpose |
| --- | --- |
| `/mcp` | lasso's MCP server (streamable HTTP): agents, hosts, notify, open_file, browser profiles and tabs, plugin tools. See [MCP tools](../mcp/tools.md). |
| `/browser-mcp` | chrome-devtools-mcp against the shared browser, one URL for every profile. `/browser-mcp/<id>` pins one profile. See [Browser MCP](../mcp/browser-mcp.md). |
| `/cdp`, `/cdp/p/<id>` | Raw Chrome DevTools Protocol WebSocket for the default profile, or for profile `<id>`. |
| `/cdp/profiles` | `GET`: every browser profile and the CDP address of each, for a client with no MCP server. Never starts a browser. Same gates as `/cdp`. See [Browser MCP](../mcp/browser-mcp.md#discovering-profiles-over-cdp). |
| `/.well-known/oauth-protected-resource[/…]`, `/.well-known/oauth-authorization-server[/…]` | OAuth discovery documents. 404 unless `MCP_OAUTH` is set. |
| `/oauth/register`, `/oauth/token` | Dynamic client registration; token endpoint. 404 unless `MCP_OAUTH` is set. |
| `/oauth/authorize` | The consent screen. Behind `UI_AUTH` and the Access gate. 404 unless `MCP_OAUTH` is set. |

### Static

| route | purpose |
| --- | --- |
| `/assets/…` | Hashed frontend build assets, cached long-term. |
| `/sw.js` | The service worker, served `no-store` so it is never stale. |
| `/` | The single-page app; any unmatched path serves it. |

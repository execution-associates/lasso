---
title: The browser tools
description: How agents drive lasso's browsers through the browser_* tools on lasso's own /mcp, or directly over CDP at /cdp, and the etiquette of a browser a human is watching.
order: 42
nav_title: Browser tools
---

lasso runs headless Chromiums on its own machine (and can dial remote ones), the [shared browsers](../concepts/shared-browser.md), and streams them into the Browser tab. Agents drive the same browsers in one of two ways:

- **The `browser_*` tools on `/mcp`**: Google's [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp) tools (navigate, click, fill, type, screenshot, snapshot, console, network, performance traces, and more), served by lasso's own MCP server beside `list_agents` and the rest. Nothing to install on the agent's machine, and no second MCP server to add.
- **`/cdp`**: the raw Chrome DevTools Protocol, for Playwright or any other CDP client.

Both reach the browsers the human is watching. They see the same pages you do and can click in them.

## Getting them

An agent connected to lasso's `/mcp` already has them:

```bash
claude mcp add --transport http lasso http://127.0.0.1:8090/mcp
```

`lasso connect` does this for every agent CLI on a machine (see [Connect your agents](../getting-started/connect-agents.md)). Settings shows the URL, a copyable command, and how many agents are using a browser.

## Tool names

Every chrome-devtools-mcp tool `x` is served on `/mcp` as **`browser_x`**: `browser_new_page`, `browser_navigate_page`, `browser_click`, `browser_fill`, `browser_take_snapshot`, `browser_take_screenshot`, `browser_list_pages`, `browser_close_page`, and so on. The prefix is what marks a tool as chrome-devtools-mcp's; lasso's own browser tools (`list_browsers`, `open_browser_tab`, `shared_browser`, …) do not start with it.

Descriptions are chrome-devtools-mcp's own, with one change: where a description or argument description names a sibling tool whose name has an underscore (`list_pages`, `take_snapshot`), the name is rewritten to its `browser_` form so the model reads names that exist. Bare words like "click" or "fill" are left alone, since they are English as often as they are tools.

The tool list is learned once per lasso process from chrome-devtools-mcp itself (a short probe at startup, repeated when the binary changes) and announced with `tools/list_changed` when it changes.

## The `browser` argument

Each [browser](../concepts/shared-browser.md) is separate: its own Chromium with its own cookies, logins and pages, or a remote browser lasso dials. Every `browser_*` tool takes an optional **`browser`** argument, a browser's id (like `work`) or its display name, and runs in that browser. Omitted, it uses the default browser.

Browsers are looked up when the call runs, so a browser created, renamed or deleted later needs no reconnect.

- **An unknown browser is a tool error** listing the current browsers. lasso never falls back to another browser, which would act with the wrong logins.
- **Page ids belong to one browser.** A `pageId` from `browser_list_pages` or `browser_new_page` in `work` means nothing in the default browser. Keep passing the same `browser` on every call about that page.

Browsers themselves are managed with lasso's other tools: [`list_browsers`](./tools.md#list_browsers), `create_browser`, `update_browser` and `delete_browser`. [`open_browser_tab`](./tools.md#open_browser_tab), `show_browser_tab`, `list_browser_tabs` and `close_browser_tab` take the same `browser` argument.

## How sessions and processes work

chrome-devtools-mcp only speaks stdio, so lasso bridges it: each MCP session gets **its own chrome-devtools-mcp process per browser it actually uses**.

- **Lazy.** Connecting to `/mcp` starts nothing. The process for a browser starts on the session's first `browser_*` call for that browser.
- **Per session.** chrome-devtools-mcp keeps per-client state (the selected page, console and network buffers, emulation settings), so each agent gets its own and nobody steers anyone else's page.
- **Browser restarts.** When a browser stops or restarts (an idle stop, a remote browser restarting, a crash), only that browser's process in each session is closed. The next call to that browser starts a fresh one, with new page ids. The session's processes for other browsers carry on.
- **Idle sessions.** A session that makes no browser call for **30 minutes** has its processes stopped; the session itself stays connected, and its next browser call starts a fresh process (as after a browser restart).
- **Session end.** When the client closes its session, or lasso stops, every process the session holds goes with it.
- **The browser's own idle stop.** A browser lasso launched stops after `-browser-idle` (15 minutes by default) with no `/cdp` client connected, and its open tabs close with it. A connected chrome-devtools-mcp process holds a CDP connection, which keeps the browser up.

Each process runs with a minimal environment (none of lasso's secrets such as `UI_AUTH`, `MCP_OAUTH` or `LASSO_MCP_TOKEN`) and with usage statistics and CrUX lookups turned off.

## Installing chrome-devtools-mcp

Install it on **lasso's** machine, not the agent's:

```bash
npm i -g chrome-devtools-mcp        # or: mise use -g npm:chrome-devtools-mcp
```

lasso does not fall back to `npx chrome-devtools-mcp@latest`: fetching an unpinned package at runtime, on the machine holding the browsers' logins, is a supply-chain risk. Without it, `/mcp` simply has no `browser_*` tools; everything else on it works. [`shared_browser`](./tools.md#shared_browser) reports `browser_tools: false` with the reason in `browser_tools_reason`, and Settings says what is missing. Once it is installed, the next `/mcp` session gets the tools, with nothing to re-register. The browsers themselves also need Chromium or Chrome on lasso's machine (see [Shared browser](../concepts/shared-browser.md)).

| flag | env | default | effect |
| --- | --- | --- | --- |
| `-browser-mcp` | `LASSO_BROWSER_MCP` | `chrome-devtools-mcp` on `PATH` | The chrome-devtools-mcp to run: a path, a PATH name, or `off` to serve no browser tools. |
| | `LASSO_BROWSER_MCP_ARGS` | | Extra chrome-devtools-mcp flags, split on whitespace and appended after lasso's own (e.g. `--slim`). A later duplicate wins. |
| `-browser-mcp-max` | `LASSO_BROWSER_MCP_MAX` | `0` (no limit) | Cap on live chrome-devtools-mcp processes, counted across every session and browser. Past it, a tool call that would start another fails with an error naming this setting; connecting never does. |

**Screenshots** come back as JPEG (quality 80), at most 1280 px on a side. The browser renders at `-browser-scale` 2 by default, and an unbounded PNG would cost an agent about four times the image tokens for no gain. Pass a different `--screenshotFormat` and friends in `LASSO_BROWSER_MCP_ARGS` to override.

## Authentication

The browser tools are full control of a real browser, so a call must meet `/cdp`'s standard, which is stricter than the rest of `/mcp`. A call that does not is answered with a **tool error** saying why; lasso's other tools on the same connection are unaffected.

| lasso's setup | what a browser tool call needs |
| --- | --- |
| neither `UI_AUTH` nor `MCP_OAUTH` | nothing (open) |
| `UI_AUTH` only | the `UI_AUTH` basic credentials on the MCP connection. `/mcp` itself stays open in this setup; its browser tools do not. |
| `MCP_OAUTH` | what `/mcp` already requires (a lasso bearer token or the `UI_AUTH` basic credentials), and a per-host token's reach must include lasso's own machine (`local`). |

Under `UI_AUTH` alone, an agent sends its basic credentials as a header:

```bash
claude mcp add --transport http --header "Authorization: Basic <base64 user:pass>" \
  lasso https://lasso.example.com/mcp
```

Under `MCP_OAUTH`, a token comes from `lasso mcp-client token`; see [Agent scope](./agent-scope.md) and [OAuth](./oauth.md). With [`-require-access-header`](../deployment/cloudflare.md), every request also needs Cloudflare Access's identity header.

lasso's own chrome-devtools-mcp processes reach `/cdp` over loopback with a random per-process internal token, not with the caller's credential. The caller's credential, reach and Origin are checked on every browser tool call, which is the only door those processes open.

### The Origin guard

Whatever the auth setting, a browser tool call whose request carries an `Origin` header naming a site other than lasso itself is refused, and so is any such request to `/cdp`. A request with no `Origin` (a CLI, an agent's MCP client, Playwright) is allowed. This is what stops a web page you visit from driving the shared browsers through a lasso on your own machine.

## Raw CDP for Playwright and other clients

| path | what it is |
| --- | --- |
| `/cdp` | websocket to the default browser's **browser** target |
| `/cdp/devtools/...` | passthrough to page and browser targets |
| `/cdp/json`, `/cdp/json/...` | passthrough (`/json/list`, `/json/version`), with every websocket URL in the answer rewritten to point back through `/cdp` |
| `/cdp/p/<id>`, `/cdp/p/<id>/devtools/...`, `/cdp/p/<id>/json/...` | the same for browser `<id>` |
| `/cdp/browsers` | lasso's own JSON listing of every browser and where to connect to it (below); `/cdp/p/<id>/browsers` answers the same |

```js
const browser = await chromium.connectOverCDP("ws://127.0.0.1:8090/cdp")
```

Use `wss://` when lasso is on HTTPS. The address is stable: it survives Chromium being stopped or restarted, so it is safe to put in an agent's config. Connecting also starts the browser if it is stopped. [`shared_browser`](./tools.md#shared_browser) on `/mcp` starts a browser and returns these endpoints as absolute URLs.

`/cdp` takes the same credentials as the browser tools (basic under `UI_AUTH`, a bearer token whose reach includes `local` under `MCP_OAUTH`); for Playwright, pass them as a header: `chromium.connectOverCDP(url, { headers: { Authorization: "Bearer <token>" } })`. With `MCP_OAUTH` set and `UI_AUTH` unset, lasso's own Browser tab still reaches `/cdp` without a token.

### Discovering browsers over CDP

A client that speaks only CDP can find every browser without MCP:

```bash
curl -s http://127.0.0.1:8090/cdp/browsers
```

The answer is `{"browsers": [...]}`, the default browser first. Each entry has `id`, `name`, `default`, `running`, `started_at` (while running), `tabs` (the open pages, while running), `ws_path` and `ws_url` (the websocket to hand `connectOverCDP`), `http_path` and `http_url` (the same prefix for `/json/list` and `/json/version`), and a `note` when something is wrong, such as why a stopped browser cannot start. `/cdp/profiles` answers the same list under a `profiles` key. Only `GET` (and `HEAD`) are accepted, and the response is never cached, so a new browser shows up at once. Listing never starts a browser: one that is not running is reported as stopped. The route sits behind the same authentication and Origin check as the rest of `/cdp`.

## What access means

The browser tools and `/cdp` are **full control of a real browser**: every page, and every site its browsers are logged into. `localhost` inside a browser lasso launched is lasso's machine. Log a browser into an account only if you are happy for every agent that can reach these tools to act as you there, and give `local` reach only to hosts you would let browse as you from lasso's machine.

## Etiquette

The browsers are shared with a human who is watching, and often with other agents. `/mcp`'s instructions carry these rules to every session, and they are worth following from CDP too:

- **Open your own page** (`browser_new_page`) rather than navigating one you did not open, unless the human asked you to work in theirs. The human's Browser tab shows one page, the most recently opened, so opening a page puts them on it; the page they were on keeps running out of sight.
- **Close the pages you opened** (`browser_close_page`, or [`close_browser_tab`](./tools.md#close_browser_tab)) when you are done.
- **`localhost` means lasso's machine**, not yours.
- **The logged-in accounts are the human's.** Reading is fine. Posting, sending, accepting or buying anything needs their go-ahead first.

To deliberately put a page in front of the human, use [`open_browser_tab`](./tools.md#open_browser_tab) or [`show_browser_tab`](./tools.md#show_browser_tab).

---
title: The browser MCP server
description: How agents drive lasso's shared browser through /browser-mcp, or directly over CDP at /cdp, and the etiquette of a browser a human is watching.
order: 42
nav_title: Browser MCP
---

lasso runs one headless Chromium on its own machine, the [shared browser](../concepts/shared-browser.md), and streams it into the Browser tab. Agents drive the same browser in one of two ways:

- **`/browser-mcp`**: an MCP server with Google's [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp) tools (navigate, click, fill, type, screenshot, snapshot, console, network, performance traces, and more). Nothing to install on the agent's machine.
- **`/cdp`**: the raw Chrome DevTools Protocol, for Playwright or any other CDP client.

Both reach the browser the human is watching. They see the same pages you do and can click in them.

## Adding it

```bash
claude mcp add --transport http lasso-browser http://127.0.0.1:8090/browser-mcp
```

Other agents (Codex, OpenCode, omp) add the same URL as a streamable-HTTP MCP server. `lasso connect` does this for every agent CLI on a machine (see [Connect your agents](../getting-started/connect-agents.md)). Settings shows the exact URL, a copyable command, and how many agents are using the browser.

An agent already connected to `/mcp` can call [`shared_browser`](./tools.md#shared_browser), which starts the browser and returns the absolute `/browser-mcp` URL, the profile's CDP endpoint and the open pages.

## One URL drives every profile

A [profile](../concepts/shared-browser.md) is a separate Chromium with its own cookies, logins and optional proxy. Every tool on `/browser-mcp` takes an optional **`profile`** argument, a profile's id (like `work`) or its display name, and runs in that profile's browser. Omitted, it uses the default profile.

Profiles are looked up when the call runs, not when the session starts, so a profile created, renamed or deleted later needs no new MCP server and no reconnect. Add `lasso-browser` once, at user scope, and leave it.

- **An unknown profile is a tool error** listing the current profiles. lasso never falls back to another profile, which would act in the wrong browser with the wrong logins.
- **Page ids belong to one profile.** A `pageId` from `list_pages` or `new_page` in `work` means nothing to the default profile. Keep passing the same `profile` on every call about that page.
- **Pinned per-profile URLs.** `/browser-mcp/<id>` gives a session pinned to one profile: its tools take no `profile` argument and every call runs there. A `profile` sent anyway must name the pinned profile, or the call is refused with a pointer to bare `/browser-mcp`. An unknown `<id>` is a 404.

Profiles themselves are managed from `/mcp`: [`list_browser_profiles`](./tools.md#list_browser_profiles), `create_browser_profile`, `update_browser_profile` and `delete_browser_profile`.

## How sessions and processes work

chrome-devtools-mcp only speaks stdio, so lasso bridges it: each MCP session gets **its own chrome-devtools-mcp process per profile it actually uses**.

- **Lazy.** Connecting starts nothing. lasso answers `initialize` and the tool list from a copy it learned once per lasso process, so a user-scope entry loaded by every agent session on a machine costs nothing until one of them browses. The process for a profile starts on the first tool call for that profile.
- **Per session.** chrome-devtools-mcp keeps per-client state (the selected page, console and network buffers, emulation settings), so each agent gets its own and nobody steers anyone else's page.
- **Profile restarts.** When a profile's browser stops or restarts (an idle stop, a remote browser restarting, a crash), only that profile's process in each session is closed. The next call to that profile starts a fresh one, with new page ids. The session's processes for other profiles carry on.
- **Session end.** A session ends when the client closes it, after **30 minutes with no requests** (a long tool call does not count as idle), or when lasso stops. Every process the session holds goes with it.
- **The browser's own idle stop.** A profile's Chromium stops after `-browser-idle` (15 minutes by default) with no `/cdp` client connected, and its open tabs close with it. A connected chrome-devtools-mcp process holds a CDP connection, which keeps the browser up.

Each process runs with a minimal environment (none of lasso's secrets such as `UI_AUTH`, `MCP_OAUTH` or `LASSO_MCP_TOKEN`) and with usage statistics and CrUX lookups turned off.

## Installing chrome-devtools-mcp

Install it on **lasso's** machine, not the agent's:

```bash
npm i -g chrome-devtools-mcp        # or: mise use -g npm:chrome-devtools-mcp
```

lasso does not fall back to `npx chrome-devtools-mcp@latest`: fetching an unpinned package at runtime, on the machine holding the browser's logged-in profiles, is a supply-chain risk. Without it, a new session on `/browser-mcp` is answered with **503** and the reason, `shared_browser` reports `mcp_available: false`, and Settings says what is missing. The browser itself also needs Chromium or Chrome on lasso's machine (see [Shared browser](../concepts/shared-browser.md)).

| flag | env | default | effect |
| --- | --- | --- | --- |
| `-browser-mcp` | `LASSO_BROWSER_MCP` | `chrome-devtools-mcp` on `PATH` | The chrome-devtools-mcp to run: a path, a PATH name, or `off` to disable `/browser-mcp` (it then answers 503). |
| | `LASSO_BROWSER_MCP_ARGS` | | Extra chrome-devtools-mcp flags, split on whitespace and appended after lasso's own (e.g. `--slim`). A later duplicate wins. |
| `-browser-mcp-max` | `LASSO_BROWSER_MCP_MAX` | `0` (no limit) | Cap on live chrome-devtools-mcp processes, counted across every session and profile. Past it, a tool call that would start another fails with an error naming this setting; connecting never does. |

**Screenshots** come back as JPEG (quality 80), at most 1280 px on a side. The browser renders at `-browser-scale` 2 by default, and an unbounded PNG would cost an agent about four times the image tokens for no gain. Pass a different `--screenshotFormat` and friends in `LASSO_BROWSER_MCP_ARGS` to override.

## Raw CDP for Playwright and other clients

| path | what it is |
| --- | --- |
| `/cdp` | websocket to the default profile's **browser** target |
| `/cdp/devtools/...` | passthrough to page and browser targets |
| `/cdp/json`, `/cdp/json/...` | passthrough (`/json/list`, `/json/version`), with every websocket URL in the answer rewritten to point back through `/cdp` |
| `/cdp/p/<id>`, `/cdp/p/<id>/devtools/...`, `/cdp/p/<id>/json/...` | the same for profile `<id>` |
| `/cdp/profiles` | lasso's own JSON listing of every profile and where to connect to it (below); `/cdp/p/<id>/profiles` answers the same |

```js
const browser = await chromium.connectOverCDP("ws://127.0.0.1:8090/cdp")
```

Use `wss://` when lasso is on HTTPS. The address is stable: it survives Chromium being stopped or restarted, so it is safe to put in an agent's config. Connecting also starts the browser if it is stopped.

### Discovering profiles over CDP

A client that speaks only CDP can find every profile without adding an MCP server:

```bash
curl -s http://127.0.0.1:8090/cdp/profiles
```

The answer is `{"profiles": [...]}`, the default profile first. Each entry has `id`, `name`, `default`, `cdp_url` (for a remote browser), `running`, `started_at` (while running), `tabs` (the open pages, while running), `ws_path` and `ws_url` (the websocket to hand `connectOverCDP`), `http_path` and `http_url` (the same prefix for `/json/list` and `/json/version`), and a `note` when something is wrong, such as why a stopped profile cannot start. Only `GET` (and `HEAD`) are accepted, and the response is never cached, so a new profile shows up at once. Listing never starts a browser: a profile that is not running is reported as stopped. The route sits behind the same authentication and Origin check as the rest of `/cdp`.

## Authentication

`/browser-mcp` and `/cdp` follow the same rules:

| lasso's setup | what a client must send |
| --- | --- |
| neither `UI_AUTH` nor `MCP_OAUTH` | nothing (open) |
| `UI_AUTH` only | the `UI_AUTH` basic credentials. This is stricter than `/mcp`, which stays open with `UI_AUTH` alone, because these endpoints are full control of a browser. |
| `MCP_OAUTH` | a lasso bearer token or the `UI_AUTH` basic credentials, as for `/mcp`. A per-host token must include lasso's own machine in its reach, or it gets **403**. |

With `MCP_OAUTH` set and `UI_AUTH` unset, lasso's own Browser tab still reaches `/cdp` without a token. With [`-require-access-header`](../deployment/cloudflare.md), every request also needs Cloudflare Access's identity header.

A gated agent sends its credential as a header:

```bash
claude mcp add --transport http --header "Authorization: Bearer <token>" \
  lasso-browser https://lasso.example.com/browser-mcp
```

For Playwright, pass the same header: `chromium.connectOverCDP(url, { headers: { Authorization: "Bearer <token>" } })`. A token comes from `lasso mcp-client token`; see [Agent scope](./agent-scope.md) and [OAuth](./oauth.md).

lasso's own chrome-devtools-mcp processes reach `/cdp` over loopback with a random per-process internal token, not with the caller's credential. The caller's credential and scope are checked on every request to `/browser-mcp`, which is the only door those processes open.

### The Origin guard

Whatever the auth setting, `/browser-mcp` and `/cdp` refuse a request whose `Origin` header names a site other than lasso itself. A request with no `Origin` (a CLI, an agent's MCP client, Playwright) is allowed. This is what stops a web page you visit from opening a websocket to a lasso on your own machine and driving the shared browser, and it runs before anything else.

## What access means

`/cdp`, and `/browser-mcp` which drives it, is **full control of a real browser**: every page, and every site its profiles are logged into. `localhost` inside it is lasso's machine. Log the shared browser into an account only if you are happy for every agent that can reach these endpoints to act as you there, and give `local` reach only to hosts you would let browse as you from lasso's machine.

## Etiquette

The browser is shared with a human who is watching, and often with other agents. `/browser-mcp` sends these rules to every session, and they are worth following from CDP too:

- **Open your own page** (`new_page`) rather than navigating one you did not open, unless the human asked you to work in theirs. The human's Browser tab shows one page, the most recently opened, so opening a page puts them on it; the page they were on keeps running out of sight.
- **Close the pages you opened** (`close_page`, or [`close_browser_tab`](./tools.md#close_browser_tab)) when you are done.
- **`localhost` means lasso's machine**, not yours.
- **The logged-in accounts are the human's.** Reading is fine. Posting, sending, accepting or buying anything needs their go-ahead first.

To deliberately put a page in front of the human, use [`open_browser_tab`](./tools.md#open_browser_tab) or [`show_browser_tab`](./tools.md#show_browser_tab) on `/mcp`.

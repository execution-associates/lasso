---
title: The shared browser
description: A real Chromium on lasso's machine that you watch in the Browser tab while agents drive it, with profiles, proxies and resource caps.
order: 23
---

The shared browser is a headless Chromium that lasso runs on **its own machine**. You see it in the sidebar's Browser tab, streamed live, and can click, scroll and type in it. Your agents drive the very same browser over the Chrome DevTools Protocol (CDP) or through the `lasso-browser` MCP server. An agent testing a login flow opens its page there, and you watch it happen, and can take over, from your desk or your phone.

![the Browser tab showing a running dev server](../assets/screenshots/browser.png)

## What you and agents each see

- **Agents** reach it at `/browser-mcp` (chrome-devtools-mcp's tools, one MCP URL for every profile) or raw CDP at `/cdp`, for Playwright or any CDP client; a plain `GET /cdp/profiles` lists every profile and its CDP address. Details are in [The browser MCP](../mcp/browser-mcp.md).
- **You** see it in the Browser tab's **Agent** mode, which shows a tab strip of the current profile's pages and follows the newest one. When an agent opens a page you land on it; open one yourself and the agents can see it too.
- **`localhost` means lasso's machine.** A bare port typed into Agent mode (`5173`) opens `http://localhost:5173` on lasso's machine, whatever host your browser tab is on. There is one shared browser per lasso, not one per host.

The Browser tab has a second mode, **Iframe**, which is a plain iframe in your own browser with nothing in between and none of the sharing. Which mode the tab opens in is stored on the server, so every device agrees. Links you click in a terminal open in the Browser tab too, in Iframe mode unless the page can only be shown in Agent mode. [Sidebar](../web-ui/sidebar.md) covers both modes, the toolbar and the terminal-link behavior.

Because Agent mode is a real browser streamed to you rather than a page framed inside lasso, none of an iframe's limits apply: no mixed-content block, no private-network block, no site refusing to be framed.

## Finding Chromium

Install Google Chrome or Chromium on the machine lasso runs on. lasso takes the first of:

1. `-browser` / `LASSO_BROWSER`: a path or a name on `PATH`. If it doesn't exist, lasso reports that rather than silently picking something else. `off` disables the shared browser.
2. `chromium`, `chromium-browser`, `google-chrome-stable`, `google-chrome`, `chrome` on `PATH`.
3. The newest Playwright Chromium in `~/.cache/ms-playwright`.
4. On macOS, `Google Chrome.app` or `Chromium.app` in `/Applications`.

With none found, the Browser tab falls back to Iframe mode and says why. Installing Chromium later brings Agent mode back on its own.

On **Ubuntu 23.10 and later**, Chromium's sandbox needs unprivileged user namespaces, which Ubuntu grants only to binaries with an AppArmor profile. Playwright's download, or any Chromium you unpacked yourself, fails with *"No usable sandbox!"*, and Settings shows that message. lasso does not fall back to `--no-sandbox` on its own. Install a packaged Google Chrome or your distro's Chromium (Ubuntu ships profiles for those), add an AppArmor profile granting `userns` to your binary, or knowingly run unsandboxed with `LASSO_BROWSER_ARGS=--no-sandbox`. Running lasso as root adds `--no-sandbox` automatically, because Chromium refuses to start as root without it.

## It costs nothing until used

Chromium starts on the first `/cdp` connection, when you open the Browser tab in Agent mode, when an agent calls the `shared_browser` MCP tool, or from **Settings → General → Terminal & browser**. It stops again after `-browser-idle` (default 15 minutes) with no CDP client connected. The Browser tab only holds a connection while it is visible, so a closed sidebar or a backgrounded phone doesn't keep it alive.

The `/cdp` address is stable: it survives Chromium stopping, restarting and relaunching, so it is safe to put in an agent's configuration.

## Resource caps

Headless Chromium on a machine without a GPU renders in software and will take several cores if you let it. On Linux, when `systemd-run` is on `PATH` and a user systemd manager is reachable (`XDG_RUNTIME_DIR` is set), lasso launches Chromium inside a transient `systemd-run --user --scope` with a CPU quota and a memory high-water mark. Without those, on macOS, or with both limits set to `off`, it runs uncapped. Settings shows which. If a capped launch fails, lasso retries once uncapped.

The defaults are a quota of `200%` CPU (two cores) and a `2G` memory high-water mark, and Chromium renders at device scale factor `2` so the Browser tab is sharp on a HiDPI screen. `-browser-cpu`, `-browser-mem`, `-browser-scale`, `-browser-idle`, `-browser` and `LASSO_BROWSER_ARGS` change them; [Configuration](../reference/configuration.md#shared-browser) lists each with its environment variable.

The caps apply **per profile**: each running profile is its own Chromium under its own scope.

## Profiles

A **profile** is a separate shared browser with its own cookies, logins, storage and optional proxy. Each profile is its own Chromium process, started on first use and idle-stopped on its own, so an unused profile costs nothing.

- The **default** profile always exists and can't be deleted (it can be renamed). Its data lives in `~/.lasso/browser-profile`.
- Other profiles live in `~/.lasso/browser-profiles/<id>`. An id is 1 to 32 lowercase letters, digits or dashes; names are unique, ignoring case. A lasso can have up to 32 profiles.
- **Deleting a profile deletes its directory**, and every login in it.

You pick and manage profiles from the bar along the bottom of the Browser tab in Agent mode. Agents manage them with the `list_browser_profiles`, `create_browser_profile`, `update_browser_profile` and `delete_browser_profile` MCP tools, and choose which profile a browser tool runs in by passing `profile` (an id or display name) to it. One `/browser-mcp` URL drives every profile, including ones created later.

Cookies and logins persist across restarts. Open tabs do not: an idle stop closes them.

## Proxy

A profile can send all its traffic through a proxy: `socks5://`, `socks4://`, `http://` or `https://` followed by `host` and optionally `:port`. Set the default profile's in **Settings → General → Terminal & browser → Proxy**, and other profiles' from the profile bar or `update_browser_profile`.

- With `socks5://`, DNS is resolved through the proxy too.
- A comma-separated fallback list in Chromium's own syntax is accepted, for example `socks5://127.0.0.1:1080,direct://` to go direct when the proxy refuses. `direct://` may only come last.
- Proxies that need a username and password are not supported: Chromium can't authenticate to a SOCKS proxy and ignores credentials in its proxy flag.
- Changing a proxy restarts that profile's Chromium and reopens the pages it had open.

## Running two lassos

Two lasso instances can't share one browser profile. If a second lasso (a dev build next to your main one, say) finds the profile in use by a live lasso, it refuses to launch rather than taking the browser away from the first. Give the second instance its own `LASSO_DIR`.

## Security

Whoever can reach `/cdp` or `/browser-mcp` has full control of that browser: they can read every page, type into it and navigate it, **including every site its profile is logged into**. Only log a profile into an account you're happy for every agent that can reach lasso to act as you on.

- `/cdp` and `/browser-mcp` are open by default (fine on loopback, a private tailnet or behind Cloudflare Access). With `UI_AUTH` set they need its basic credentials, which makes them stricter than `/mcp`, which `UI_AUTH` does not gate. With `MCP_OAUTH` set they take what `/mcp` takes (a lasso bearer token or the `UI_AUTH` credentials), and a per-host token must include lasso's own machine in its scope.
- Whatever the auth setting, lasso refuses a `/cdp` or `/browser-mcp` request that a **different website** sends from your browser, so a page you visit can't drive a lasso running on your own machine.
- Chromium and lasso's chrome-devtools-mcp processes get an environment without lasso's credentials (`UI_AUTH`, `MCP_OAUTH`, `LASSO_MCP_TOKEN`).

See [Security](../security.md) for the full model and [The browser MCP](../mcp/browser-mcp.md) for how an agent authenticates.

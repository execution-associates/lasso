---
title: Architecture
description: How the lasso binary, ttyd, herdr, SSH, the shared browser and the phone fit together.
order: 100
---

![lasso architecture: the browser and MCP clients reach the lasso binary through Cloudflare Access; lasso drives the local herdr over its socket and remote herdrs through an SSH pool, keeps state in lasso.db, and sends Web Push to the phone](../assets/architecture/lasso.svg)

lasso is one Go binary. It embeds its React frontend, keeps its state in a SQLite file, and drives [herdr](https://herdr.dev) (the terminal multiplexer the agents actually run in) on its own machine and on every machine you can SSH to. It does not run agents itself: herdr owns the panes and the processes, and lasso orchestrates around it, including relaying messages into panes and carrying replies back over tailcat.

## The pieces

**The web server.** One listener serves the embedded single-page app, the `/api/*` routes behind it, and the agent-facing endpoints: the MCP server at `/mcp`, herdr's socket API as MCP tools at `/herdr-mcp`, chrome-devtools-mcp at `/browser-mcp`, and raw CDP at `/cdp`. Live state (the focused pane, the layout, theme and settings revisions) reaches the browser over server-sent events at `/api/events`. See [HTTP routes](./http-routes.md).

**Terminals through ttyd.** The terminal column is a real terminal: lasso spawns [ttyd](https://github.com/tsl0922/ttyd) running `herdr` (or `herdr --remote <host>` for a remote host) and reverse-proxies it, WebSocket included, at `/terminal/<host>/`. The Terminal tab is a second ttyd running a plain shell (or `ssh <host>`) at `/shell/<host>/`. Each ttyd listens on its own unix socket named by lasso's PID and the host, so several lasso instances never collide, and a host keeps its terminals warm: switching back to a host re-points the proxy at a ttyd that is already running instead of spawning a new one. Terminals for hosts nobody is looking at are retired after a while.

**herdr over its socket.** lasso talks to the local herdr server over its unix socket (`-herdr-sock`) to read panes and focus, follow events, and create workspaces and worktrees for new agents.

**Remote hosts over SSH.** For each remote host, lasso opens one SSH ControlMaster connection with the system `ssh` binary, so your `~/.ssh/config` (ProxyJump, identities, known hosts) applies exactly as on the command line. Everything for that host rides it: the remote herdr socket forwarded to a local one, file operations over SFTP, and git commands. A remote herdr must speak the same protocol as the herdr on lasso's machine. A host whose herdr lasso cannot drive (a different version, or not running) still gets theme writes over a files-only SSH connection.

**Per-tab hosts.** Which host a view drives is a property of the browser tab, not of the server: each tab sends its host with every request (`X-Lasso-Host` or `?host=`). Two tabs can sit on two machines at once, and an agent's MCP calls default to the caller's own host. See [Hosts](../concepts/hosts.md).

**State in `lasso.db`.** Agent records, New Agent defaults, the shared UI state, OAuth clients and tokens, push devices and the VAPID key, plugin approvals and browser profiles all live in one SQLite file in `~/.lasso`. Each remote host keeps its own creator settings in its own `~/.lasso/lasso.db`, written with that host's `sqlite3`. See [Files and directories](./files.md).

**Web Push to the phone.** lasso signs Web Push messages with its VAPID key and sends them through the browser vendor's push service (Apple's, for an iPhone), which delivers them to the service worker on the device even with no tab open. See [Notifications](../concepts/notifications.md).

**The shared browser.** A headless Chromium on lasso's machine, started on first use and stopped when idle, optionally capped in a systemd user scope. The Browser tab streams it, and agents drive it through `/cdp` or through `/browser-mcp`, where lasso runs one chrome-devtools-mcp process per MCP session per profile that session uses. Each profile is its own Chromium. See [Shared browser](../concepts/shared-browser.md).

**Plugins.** A plugin's tab pages are served from `/plugins/<name>/` as an opaque, sandboxed origin; its MCP server runs in an [isb](https://github.com/execution-associates/isb) sandbox, a container by default or a VM if you choose (or, if you trust it, on the host), and its tools are mirrored onto `/mcp`. See [Plugins](../plugins/index.md).

## Background work

Some work runs whether or not a browser is open:

- **Host refresher.** Probes every host in `~/.ssh/config` on an interval, so the host picker opens on warm data. Each completed probe also converges that host's theme if it drifted while the host was asleep.
- **Repo cache warmer.** Keeps every reachable host's repo and branch lists fresh for the New dialog.
- **Agent reaper.** Reconciles agent records against herdr's panes, marking an agent closed only after its pane has been missing from several successful listings in a row.
- **Blocked-agent watcher.** Looks across the fleet for agents waiting on a human and sends notifications. It polls nothing while no device is registered.
- **SSH reaper.** Cleans up SSH control masters left behind by killed `herdr --remote` clients.

## The diagram

The diagram above is [reladraw](https://www.npmjs.com/package/reladraw) source at [`docs/assets/architecture/lasso.reladraw`](https://github.com/execution-associates/lasso/blob/main/docs/assets/architecture/lasso.reladraw). Edit it and run `mise run diagram`, which renders it to SVG inside a separate Debian incus container, since reladraw is fetched from npm at render time. See [Development](./development.md).

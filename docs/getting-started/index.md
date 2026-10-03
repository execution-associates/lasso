---
title: Getting started
description: Install lasso, start it, open the UI and check the install with lasso doctor.
order: 10
nav_title: Getting started
---

lasso is a single binary. It runs on the machine where your coding agents live, drives that machine's [herdr](https://herdr.dev) session, and serves a web UI on loopback. This page takes you from nothing to a working UI on `http://127.0.0.1:8090`.

## Requirements

- **Linux or macOS**, on `amd64` or `arm64`.
- **herdr**, the terminal multiplexer lasso is a UI over. The installer below installs it for you if it is missing.
- **ttyd** on `PATH`. lasso serves its terminals by spawning `ttyd` processes, and nothing installs it for you: use your package manager (`apt install ttyd`, `brew install ttyd`, or your distro's equivalent).
- **ssh** and an `~/.ssh/config`, only if you want lasso to drive other machines too. Those machines need a running herdr of the same protocol, `git`, and the `sqlite3` CLI, but not lasso itself. See [What a remote host needs](../concepts/hosts.md#what-a-remote-host-needs).
- **Chrome or Chromium**, only for the [shared browser](../concepts/shared-browser.md). Everything else works without it.

## Install

```bash
curl -fsSL https://executionassociates.com/install-lasso | sh
```

The script downloads the latest release binary for your platform from GitHub, verifies it against the release's `checksums.txt`, and installs it as `~/.local/bin/lasso`. It then installs herdr (from `https://herdr.dev/install.sh`) if `herdr` is not already on your `PATH`.

| Variable | Effect |
| --- | --- |
| `LASSO_INSTALL_DIR` | Install somewhere other than `~/.local/bin`. |
| `LASSO_SKIP_HERDR=1` | Don't install herdr; just print the command to install it yourself. |

```bash
curl -fsSL https://executionassociates.com/install-lasso | LASSO_INSTALL_DIR=/usr/local/bin sh
```

If the install directory is not on your `PATH`, the script says so and prints the `export PATH=...` line to add.

## Start it

```bash
lasso start
```

`lasso start` (alias `lasso up`) runs the server in the background, writes its PID to `~/.lasso/lasso.pid` and its log to `~/.lasso/lasso.log`, and prints the URL once it has bound:

```text
lasso started (pid 41234) → http://127.0.0.1:8090
```

The other lifecycle commands:

| Command | What it does |
| --- | --- |
| `lasso status` | Whether the background server is running, and its URL. |
| `lasso stop` (alias `down`) | Stop the background server. |
| `lasso restart` | Stop it if running, then start it. |
| `lasso serve` | Run in the foreground instead. A bare `lasso` does the same. |

`start`, `restart` and `serve` accept the server flags, for example `lasso start -listen 127.0.0.1:9000`. `lasso serve -h` lists them, and [Configuration](../reference/configuration.md) describes each one. To keep lasso running across reboots, run it as a systemd unit: see [Running under systemd](../deployment/systemd.md).

## Open the UI

Open `http://127.0.0.1:8090` in a browser on the same machine. You get herdr's terminal in the main column, a sidebar of tabs on the right (Files, Browser, Terminal, Usage, Settings and others), and a footer with **New**, the host menu (server icon), the sidebar toggle and your agents' usage budgets. [The web UI](../web-ui/index.md) walks through the layout.

lasso binds to loopback by default, and that is deliberate: the terminal is a writable shell as you, and the MCP endpoints can spawn agents. It refuses to listen on a non-loopback address unless you configure authentication (or explicitly opt out with `-insecure-no-auth`, which is only safe on a private interface). To reach it from another machine or a phone, see [Deployment](../deployment/index.md); for a phone specifically you also need HTTPS ([On your phone](./phone.md)).

## Check the install

```bash
lasso doctor
```

`lasso doctor` prints one line per check and exits non-zero if a hard requirement fails: herdr missing from `PATH`, or `~/.lasso` not writable. It also warns when the herdr server is unreachable or speaks a different protocol, when herdr's agent integrations are missing, when another process holds port 8090, when the binary's directory is not on `PATH`, and when a newer release exists. [CLI reference](../reference/cli.md#lasso-doctor) lists every check.

doctor does not check for `ttyd`. If the UI loads but the terminal stays blank, check that `ttyd` is installed and look in lasso's log (`~/.lasso/lasso.log` for `lasso start`, `journalctl --user -u lasso` under systemd).

## herdr version

This lasso release targets **herdr protocol 22**, the protocol herdr has spoken since v0.9.0, and is tested against herdr v0.9.2. lasso talks to herdr over its unix socket (`~/.config/herdr/herdr.sock`, or `$HERDR_SOCKET_PATH`), so the protocol is what has to match:

- `lasso doctor` warns when the local herdr speaks a different protocol.
- Another machine is only selectable in the host menu when its herdr speaks the same protocol as the herdr on lasso's own machine. The menu offers to update or set up herdr on a host that is behind. See [Hosts](../concepts/hosts.md#setting-up-and-updating-herdr-on-a-host).

Upgrade herdr on lasso's machine and on your other hosts together. Upgrading a herdr server across a protocol change can end the processes running in its panes, so checkpoint active agents before you approve a herdr restart. [Updating](../deployment/updating.md) covers both lasso and herdr updates.

## Running lasso inside herdr

lasso's main terminal runs `herdr`. If you start lasso from a shell that is itself inside a herdr pane, herdr refuses to launch nested by default and the terminal won't come up. Allow it in `~/.config/herdr/config.toml`:

```toml
[experimental]
allow_nested = true
```

Starting lasso from outside herdr (a plain shell, systemd, `lasso start` from an ssh session) needs nothing.

## Next steps

- [Connect your agents](./connect-agents.md) so they can use lasso's MCP servers.
- [Set lasso up on your phone](./phone.md), with notifications when an agent is blocked.
- Read [Concepts](../concepts/index.md) for how hosts, agents and the shared browser fit together.

<div align="center">

<img src="brand/lasso-wordmark.png" alt="lasso: the Execution Associates EXA monogram beside the word lasso, over a blue-hour Orange County coast" width="460">

**Run coding agents on every machine you own. Watch them from a browser tab.
Answer them from your phone.**

[Docs](docs/index.md) · [Install](#install) · [For agents](SKILL.md)

</div>

<table align="center">
<tr>
<td align="center" valign="top">
<img src="docs/assets/screenshots/hero.png" width="605" alt="lasso on a desktop: the herdr terminal in the middle, herdr's workspace list on the left, the Files browser on the right, and the usage footer along the bottom">
</td>
<td align="center" valign="top">
<img src="docs/assets/screenshots/mobile-dial.png" width="170" alt="lasso on a phone: the same workspace, with the radial input dial open over the terminal">
</td>
</tr>
<tr>
<td align="center"><sub>Your desk: the whole fleet, the diff, the files, the budget.</sub></td>
<td align="center"><sub>Pocket: same workspace, real keys.</sub></td>
</tr>
</table>

<p align="center"><sub>Real UI captures from isolated demo workspaces. Identifying details are redacted.</sub></p>

An agent that has been waiting forty minutes on a `y/n` you never saw is an
agent doing nothing. lasso is a single Go binary that puts your coding agents
(every SSH-reachable machine, its [herdr](https://herdr.dev) session, every
pane) in a browser tab, and in a phone app that **buzzes when one of them needs
you**.

- **Your real terminal, anywhere.** Your actual herdr session over `ttyd`, with
  lasso around it, never in front of it.
- **The phone is a first-class client.** Home-screen app, a radial dial for
  Esc/Ctrl-C/Tab/arrows, a chat view you can answer by dictation, and photo
  upload straight to the agent's host.
- **It tells you when an agent is stuck.** Web Push to a locked phone when an
  agent blocks on an approval or a question, on any host.
- **The sidebar follows the focused pane.** A file editor and live git diff on
  the machine that pane is actually working on.
- **A browser you and your agents share.** A real Chromium you watch and click
  in while an agent drives it.
- **Agents orchestrating agents.** An MCP server lets one agent spawn, list,
  inspect and close the others, across the fleet, and `/herdr-mcp` serves
  herdr's own API as MCP tools on any host (no separate herdr-mcp to run).

## Install

```bash
curl -fsSL https://executionassociates.com/install-lasso | sh
lasso start                 # run it in the background
open http://127.0.0.1:8090
```

lasso needs [herdr](https://herdr.dev) and [ttyd](https://github.com/tsl0922/ttyd) on
the same machine. Run `lasso doctor`
if anything looks off. Then register lasso's MCP servers with the agent CLIs on
the machine:

```bash
lasso connect
```

To reach it from a phone you need an HTTPS origin: see
[Deployment](docs/deployment/index.md). Read the
[security model](docs/security.md) first; the terminal is a writable shell and
lasso never belongs on a public interface.

## Documentation

| | |
| --- | --- |
| [Getting started](docs/getting-started/index.md) | Install, first run, connecting agents, the phone app |
| [Concepts](docs/concepts/index.md) | Hosts, agents and worktrees, the shared browser, notifications |
| [Web UI](docs/web-ui/index.md) | The terminal, the sidebar, the New dialog, theming |
| [MCP server](docs/mcp/index.md) | Tools, the browser and herdr MCPs, the CLI, OAuth, agent scope |
| [Plugins](docs/plugins/index.md) | Installing plugins, and [writing one](docs/plugins/authoring.md) |
| [Deployment](docs/deployment/index.md) | systemd, Cloudflare Tunnel and Access, tailnet, updating |
| [Security](docs/security.md) · [Troubleshooting](docs/troubleshooting.md) | |
| [Reference](docs/reference/index.md) | CLI, configuration, files, HTTP routes, architecture, development |

Agents working with lasso should read [SKILL.md](SKILL.md).

## Building from source

```bash
mise run build      # build the frontend (src/web/dist) then the binary
./lasso             # serves on 127.0.0.1:8090
```

Frontend tooling runs inside a per-worktree incus container, never on the host.
See [Development](docs/reference/development.md).

## License

lasso is licensed under the [Apache License 2.0](LICENSE), the same license as
herdr. See [NOTICE](NOTICE) for the copyright notice.

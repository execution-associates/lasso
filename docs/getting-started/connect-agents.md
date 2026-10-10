---
title: Connect your agents
description: Register lasso's MCP servers with the agent CLIs on a machine, with lasso connect or by hand.
order: 11
nav_title: Connect agents
---

lasso exposes two MCP servers, and an agent CLI needs both registered to use everything lasso offers:

| Server name | URL | What it gives an agent |
| --- | --- | --- |
| `lasso` | `<lasso URL>/mcp` | Create, list, inspect and close agents on any host; `whoami`; `notify` the human; `open_file` in the human's sidebar; lasso's [browsers](../concepts/shared-browser.md) and their tabs; and the `browser_*` tools, chrome-devtools-mcp's (navigate, click, fill, screenshot, console, network, ...) against those browsers. See [MCP tools](../mcp/tools.md) and [Browser tools](../mcp/browser.md). |
| `lasso-herdr` | `<lasso URL>/herdr-mcp` | herdr's socket API, one tool per herdr method (`pane_list`, `pane_split`, `agent_prompt`, ...), on any host lasso drives. See [The herdr MCP](../mcp/herdr-mcp.md). |

`lasso-herdr` is a separate server because ninety raw herdr methods are a different job from orchestrating agents and driving a browser.

## lasso connect

```bash
lasso connect
```

`lasso connect` finds the supported agent CLIs installed on this machine and registers every server with each of them, through each CLI's own mechanism. Re-running it is safe: an entry that is already identical is left alone, and a different one is replaced. "Identical" is decided by reading what the CLI actually stored, so a hand edit is noticed.

Before it registers anything it asks the server:

- `/mcp` must answer, with the same headers the CLIs will send. If it doesn't, nothing is registered, lasso connect prints the likeliest reason, and it exits 1.
- It asks `shared_browser` whether `/mcp`'s browser tools can run, which needs Chrome or Chromium and `chrome-devtools-mcp` installed on **lasso's** machine. When they can't, `lasso` is registered anyway and the output says what is missing; installing it later needs no re-registration.
- `lasso-herdr` is registered only when `/herdr-mcp` lists herdr's tools, which needs herdr installed on **lasso's** machine (lasso reads the tool list from `herdr api schema --json`). `-herdr=false` skips it.

The probe never launches the browser.

An entry named `lasso-browser` (an MCP server at `<lasso URL>/browser-mcp`, which lasso does not serve) is removed from every detected CLI on every run, since the browser tools are on `/mcp`; the output mentions it only when there was one to remove.

### Which CLIs it handles

| CLI | Detected when | How it registers |
| --- | --- | --- |
| Claude Code | `claude` is on `PATH` | `claude mcp add --transport http --scope <scope>`. An existing entry is removed first with `claude mcp remove`, since Claude Code refuses to add a name that exists. |
| Codex | `codex` is on `PATH` | `codex mcp add <name> --url <url>`, which replaces in place. Headers are then written into that entry's `http_headers` in Codex's `config.toml`. |
| OpenCode | `opencode` is on `PATH` | `opencode mcp add <name> --url <url> --header K=V`, run from your home directory so it writes the global config. |
| Oh My Pi (`omp`) | `omp` is on `PATH`, or `~/.omp/agent` exists | Edits `~/.omp/agent/mcp.json` directly (omp has no `mcp` subcommand), keeping any other keys you put on the entry. |
| Pi (`pi`) | `pi` is on `PATH` | Skipped: pi has no MCP client. Its agents can call lasso's tools from the shell with [`lasso mcp`](../mcp/cli.md). |

The output has one line per CLI and server, with a status such as `added`, `updated`, `unchanged`, `skipped: not installed` or `failed: <reason>`.

### Flags

| Flag | Default | Effect |
| --- | --- | --- |
| `-url <url>` | `$LASSO_URL`, else `http://$LASSO_LISTEN`, else `http://127.0.0.1:8090` | The URL **this machine** reaches lasso on. A trailing `/mcp`, `/herdr-mcp` or `/browser-mcp` is dropped, since every server hangs off the base URL. |
| `-token <token>` | `$LASSO_MCP_TOKEN` | Bearer token to register, sent as `Authorization: Bearer <token>`. |
| `-header 'Name: value'` | none | An extra header to probe with and register. Repeatable. A header with the same name as one lasso adds (such as `Authorization`) replaces it. |
| `-only a,b` | all | Only these CLIs: any of `claude`, `codex`, `opencode`, `omp`, `pi`. |
| `-scope <s>` | `user` | Claude Code scope: `user`, `local` or `project`. Ignored by the other CLIs. |
| `-remove` | off | Unregister both servers (and any `lasso-browser` entry) from every CLI instead. |
| `-dry-run` | off | Print every command it would run and every file edit it would make, and change nothing. |
| `-force` | off | Skip the probe and register even if lasso does not answer. |

`lasso connect -h` prints the same reference.

### Authentication

With no flags, the entries carry whatever credential the environment offers:

- `$LASSO_MCP_TOKEN` (or `-token`) becomes `Authorization: Bearer <token>`.
- Otherwise `$UI_AUTH` (`user:pass`) becomes `Authorization: Basic ...`.
- Otherwise there is no auth header, which is right for a loopback lasso with `/mcp` open.

Secrets are masked in everything lasso connect prints. Claude Code and OpenCode only accept headers on their command line, so for those two the header values are briefly visible in this machine's process list while the command runs. Codex and omp get theirs written straight into their config files, and the first rewrite of a config file leaves a `.bak` copy beside it.

For how tokens are minted and scoped per host, see [Agent scope](../mcp/agent-scope.md) and [MCP OAuth](../mcp/oauth.md).

### Where Claude Code's entry lands

| `-scope` | File |
| --- | --- |
| `user` | `~/.claude.json` (or `$CLAUDE_CONFIG_DIR/.claude.json`), top-level `mcpServers` |
| `local` | The same file, under this directory's project entry |
| `project` | `./.mcp.json` in the current directory |

`user` is the default and usually what you want: add the servers once and every Claude Code session on the machine has them. A session that never calls a browser tool costs nothing on lasso's side.

## Connecting from another machine

On a machine other than lasso's, `127.0.0.1` would point that machine's agents at themselves. Pass the URL that machine actually reaches lasso on:

```bash
# over a tailnet
lasso connect -url http://myhost:8090

# behind Cloudflare Access, with a service token
lasso connect -url https://lasso.example.com \
  -header 'CF-Access-Client-Id: <id>.access' \
  -header 'CF-Access-Client-Secret: <secret>'
```

The lasso binary on that machine is only used as the installer here; it does not need a server of its own running. When lasso connect can't reach `/mcp`, its hint depends on the URL: for an `https://` URL with no `CF-Access-Client-Id` header it suggests a service token, and for a loopback URL it asks whether lasso is running there.

## Undo

```bash
lasso connect -remove
```

This removes `lasso`, `lasso-herdr` and any `lasso-browser` entry from every detected CLI. Claude Code and Codex are asked to remove them (`claude mcp remove`, `codex mcp remove`); omp's `mcp.json` is edited. OpenCode has no remove command, so lasso deletes the entry from whichever of `opencode.jsonc`, `opencode.json` or `config.json` holds it. A file that contains comments or trailing commas is refused rather than rewritten (a rewrite would drop them); delete `mcp.lasso` and `mcp.lasso-herdr` (and `mcp.lasso-browser`, if present) from it by hand.

Combine with `-dry-run` to see what would be removed first.

## Registering by hand

Any MCP client that speaks streamable HTTP can add the URLs itself. For Claude Code:

```bash
claude mcp add --transport http lasso       http://127.0.0.1:8090/mcp
claude mcp add --transport http lasso-herdr http://127.0.0.1:8090/herdr-mcp
```

For Codex and OpenCode:

```bash
codex mcp add lasso --url http://127.0.0.1:8090/mcp
codex mcp add lasso-herdr --url http://127.0.0.1:8090/herdr-mcp

opencode mcp add lasso --url http://127.0.0.1:8090/mcp
opencode mcp add lasso-herdr --url http://127.0.0.1:8090/herdr-mcp
```

For omp, in `~/.omp/agent/mcp.json`:

```json
{
  "mcpServers": {
    "lasso": { "type": "http", "url": "http://127.0.0.1:8090/mcp" },
    "lasso-herdr": { "type": "http", "url": "http://127.0.0.1:8090/herdr-mcp" }
  }
}
```

A gated lasso needs the credential as a header on each entry, for example `--header "Authorization: Bearer <token>"` with `claude mcp add`. Under `UI_AUTH` alone `/mcp` is open, but its browser tools need the `UI_AUTH` basic credentials (`--header "Authorization: Basic <base64 user:pass>"`).

Settings → General → Terminal & browser in the web UI also shows lasso's MCP URL and a ready-to-copy `claude mcp add` command for it.

## Next

- [MCP overview](../mcp/index.md): what the three servers are for and how they are secured.
- [MCP tools](../mcp/tools.md): every tool on `/mcp` and its parameters.
- [The lasso CLI for agents](../mcp/cli.md): `lasso notify`, `lasso open`, `lasso mcp`, for agents without MCP.

---
title: Shell commands
description: Reach lasso's MCP tools from a terminal with lasso mcp, lasso notify, lasso open and lasso closeme.
order: 43
nav_title: Shell commands
---

Most agents spend their time in a terminal, and not every agent has lasso's MCP server configured. These subcommands give a shell the same tools, with the same behavior and the same descriptions, by talking to the running lasso server:

| command | what it does |
| --- | --- |
| `lasso mcp` | list and call every tool on `/mcp` |
| `lasso notify` | push a notification to the human (the [`notify`](./tools.md#notify) tool) |
| `lasso open` | show a file in the human's sidebar viewer (the [`open_file`](./tools.md#open_file) tool) |
| `lasso closeme` | close the agent this shell runs in |

## Finding the server

`lasso mcp`, `lasso notify` and `lasso open` are MCP clients of `/mcp`. They find it, and authenticate, from the environment:

| variable | meaning |
| --- | --- |
| `LASSO_URL` | Full base URL of lasso, e.g. `https://lasso.example.com`, when it is not on plain http loopback. Wins over `LASSO_LISTEN`. |
| `LASSO_LISTEN` | `host:port` of a local lasso. Default `127.0.0.1:8090`. |
| `LASSO_MCP_TOKEN` | A bearer token, for a lasso with `MCP_OAUTH` set (mint one with `lasso mcp-client token`, see [Agent scope](./agent-scope.md)). Sent as `Authorization: Bearer`. |
| `UI_AUTH` | `user:pass`, sent as basic auth when there is no token. |

`/mcp` is open by default, so usually none of these is needed. These commands send no Cloudflare Access headers, so they cannot pass a lasso started with `-require-access-header`.

A per-host token also decides what "your own host" means for tools that default `host`. Without one, the caller is unidentified and the default host is `local`, lasso's own box. On a remote machine with no per-host credential, pass `-host <alias>` where a tool takes one.

## `lasso mcp`

```text
lasso mcp                       list the tools this lasso serves
lasso mcp <tool> -h             show one tool's flags
lasso mcp <tool> [flags]        call it
lasso mcp <tool> -json          print the whole MCP result envelope
```

Examples:

```bash
lasso mcp list-hosts
lasso mcp list-agents -host myhost
lasso mcp create-agent -type git -repo ~/src/app -prompt "fix the flaky test" -agent codex
lasso mcp get-agent -to "Fix the push flow"
lasso mcp shared-browser -start=false
```

- **The flags come from the server.** Each tool's flags are built from the input schema the running server advertises, not from a table in the binary. The `lasso` you type may be older or newer than the one answering (after a `lasso update`, or with `LASSO_URL` pointing at another machine), and the flags always match the server. Plugin tools (`<plugin>__<tool>`) show up the same way.
- **Either spelling works**, for tool names and flags: `list-hosts` or `list_hosts`, `-agent-id` or `-agent_id`. Listings use the kebab-case form.
- **Omitted flags are absent from the call**, not sent as zero values, so the tool's own default applies. This matters: an omitted `host` and an empty `host` are different requests.
- **Booleans** can be written bare (`-focus`) or explicitly (`-focus=false`). A list parameter takes its flag once per element. An object parameter takes JSON.
- **Required parameters** are checked locally before the call, mirroring the server's own validation.

Flags before the tool name:

| flag | default | effect |
| --- | --- | --- |
| `-json` | off | Print the full MCP result envelope instead of just the structured output. Also accepted after the tool name, unless that tool has a parameter called `json`. |
| `-timeout <dur>` | `2m0s` | Give up after this long. A tool argument whose name contains `timeout` extends the deadline to cover it (plus 15 seconds), so a tool asked to wait longer is not cut off. |

**Output.** Every lasso tool returns structured output, and that JSON is what is printed: indented on a terminal, compact when piped, so `| jq` needs no flag.

**Exit codes.** `0` on success. `1` when the tool refused (its error on stderr) or when the server could not be reached. `2` for a usage error, such as an unknown tool or a bad flag. `-h` prints help to stdout and exits `0`.

## `lasso notify`

```text
lasso notify [flags] <message...>
<command> | lasso notify [flags]        read the message from stdin
```

| flag | default | effect |
| --- | --- | --- |
| `-title <text>` | your agent's name | Headline. |
| `-pane <id>` | `$HERDR_PANE_ID` | Your herdr pane id, used to name you and to open the notification on your host. |
| `-host <alias>` | resolved from the pane | The host you run on. |

With no message argument it reads one from stdin (up to 8 KiB), so `make test 2>&1 | tail -5 | lasso notify` works. A terminal on stdin with no message is a usage error rather than a hang.

It reaches a locked phone, so use it when you actually need the human: a decision only they can make, a question that blocks you, a long job finishing while they are away.

**Exit codes are the contract:**

| code | meaning |
| --- | --- |
| `0` | A device took the notification. Prints `notified: "<title>" via <transports>`. |
| `1` | Nothing was delivered (no device is subscribed), with the reason on stderr; or lasso could not be reached. Do not tell the human you notified them. |
| `2` | Usage error: no message, or a bad flag. |

The whole round trip is bounded at 45 seconds. See [Notifications](../concepts/notifications.md) for setting up a device.

## `lasso open`

```text
lasso open [flags] <path> [flags]
```

| flag | default | effect |
| --- | --- | --- |
| `-line <n>` | | Scroll to, and select, this 1-based line. |
| `-host <alias>` | your own host | The host the file lives on. |
| `-pane <id>` | `$HERDR_PANE_ID` | Your herdr pane id, to say who opened it. |

Flags may come before or after the path. One path at a time.

- A **relative path** is resolved against this shell's working directory, on the machine the command runs on, before the call. A `~/` path is passed through and expanded against the target host's home.
- A **directory** opens the sidebar's file tree there instead of the viewer.
- The **host** defaults to your own host. Without a per-host credential (`LASSO_MCP_TOKEN`) that is `local`, lasso's box. On a remote fleet machine without one, pass `-host <alias>`, or the path is looked for on the lasso host.

Exit codes: `0` when at least one lasso tab received it (prints `opened file <path> on <host> in N lasso tabs`), `1` when no lasso tab is open (nobody saw it; reason on stderr) or the call failed, `2` for a usage error. The round trip is bounded at 30 seconds.

## `lasso closeme`

```bash
lasso closeme
```

Closes the lasso agent this shell runs in: it kills the agent process and closes its herdr pane, the same teardown as [`close_agent`](./tools.md#close_agent). It takes no arguments. It reads `$HERDR_PANE_ID`, which herdr exports into every pane, and fails if that is unset (you are not in a lasso-managed pane).

Unlike the other three, `closeme` is not an MCP client. It POSTs the pane id to the local lasso's `/api/agent/close`:

- It always talks to a lasso on **this machine**: `LASSO_LISTEN` (default `127.0.0.1:8090`) over plain http. `LASSO_URL` and `LASSO_MCP_TOKEN` are not used.
- It sends the `UI_AUTH` basic credentials when that is set.
- The pane is always closed, and the git worktree is kept.
- If the agent was spawned on this machine by a different lasso, the local lasso picks up that peer's record for the pane and closes it.

Closing your own pane kills the shell the command runs in, so the connection may drop before the reply arrives. Only a failure to reach the server, or an error status from it, is reported.

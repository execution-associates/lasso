---
title: Agents
description: What lasso does when it creates an agent, which agent CLIs it can launch, and how agent records track herdr's panes through to closing.
order: 22
---

An **agent** is an AI coding CLI running in a herdr pane. herdr runs it and knows its state; lasso adds a way to create one in a single step, on any [host](./hosts.md), and keeps a record of what it created so other agents and the UI can find, inspect and close it.

You create agents from the [New dialog](../web-ui/new-agent.md) or with the `create_agent` MCP tool ([MCP tools](../mcp/tools.md)). Both run the same code.

## Harnesses

lasso can launch five agent CLIs, which it calls harnesses. The CLI has to be installed on the host the agent runs on.

| Harness | Id | Launch command lasso types | Plan mode | Effort levels |
| --- | --- | --- | --- | --- |
| Claude Code | `claude` | `claude --allow-dangerously-skip-permissions` | yes (`--permission-mode plan`) | `low`, `medium`, `high`, `xhigh`, `max` (`--effort`) |
| Codex | `codex` | `codex --dangerously-bypass-approvals-and-sandbox` | no | `minimal`, `low`, `medium`, `high`, `xhigh` (`-c model_reasoning_effort=`) |
| OpenCode | `opencode` | `opencode --auto` | yes (`--agent plan`) | none |
| Oh My Pi | `omp` | `omp --yolo` | yes (via a config overlay) | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, `auto` (`--thinking`) |
| Pi | `pi` | `pi --approve` | no | `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` (`--thinking`) |

Read the launch commands before you rely on lasso in a sensitive repo: Codex, OpenCode, omp and pi start with their approval prompts bypassed, and Claude Code starts with permission-bypass mode available to switch into. An agent lasso creates can generally run commands as you on its host.

The rest of the launch line:

- **Model** goes to the CLI's `--model` flag. It is free text; the dialog suggests common choices, and for omp it suggests the models that host's omp configuration assigns to roles.
- **Effort** must be one of the harness's levels. A level the harness doesn't list is dropped (the agent starts at the CLI's default) instead of passed through, since an unknown level would make the CLI exit at launch.
- **Plan mode** starts the agent planning: it researches and proposes, and only stops at a gate when it wants to execute. It is dropped for Codex and pi, which lasso can't start in a plan mode.
- **Advisor** (omp only) adds `--advisor`, omp's per-turn review pass. Dropped for every other harness.
- **Extra args** are appended verbatim, for flags lasso has no field for.
- **Prompt** is the agent's first instruction. It is optional: with no prompt, the CLI comes up idle and waits for you (or `herdr agent prompt`) in its pane. A long or multi-line prompt is staged to a file on the host and read in by the launch command.

`claude` is the default when no harness is named.

## Git agents and scratch agents

Every agent is one of two types.

### Git agents

A **git** agent gets its own branch and git worktree, off a repo and base branch you choose:

1. **Branch.** The name defaults to a slug of the title (the dialog proposes one from the title's first few words plus a short random suffix), with an optional prefix: `feature` + `fix-login` becomes `feature/fix-login`. If the branch exists already, lasso appends `-2`, `-3`, and so on. The base defaults to the repo's `HEAD`.
2. **Worktree.** lasso asks herdr to create the worktree at `~/.lasso/worktrees/<repo>/<branch-slug>` on the agent's host, and herdr opens a workspace for it labeled with the agent's title.
3. **Setup.** In the new pane's shell, lasso copies in the repo's configured **copy files** (glob patterns relative to the repo, comma or newline separated, `**` not supported, existing files never overwritten: typically `.env` and similar untracked files), then runs the repo's **setup script**.
4. **Launch.** It types the agent's launch command into the same pane.

Repos come from the host's **repos root** setting: one or more directories (one per line, default `~/projects`), each scanned one level deep for git repositories. Copy files and the setup script are per repo. These settings live on each host, in that host's own `~/.lasso/lasso.db`, and are edited in Settings → General → Agents with "Configuring host" set to the host.

### Scratch agents

A **scratch** agent has no repo. It gets a fresh empty directory, `~/.lasso/scratch/<title-slug>-<random>`, and opens as a **tab in the host's shared `Scratch` workspace** rather than a workspace of its own, so throwaway agents don't bury the sidebar. The host's **scratch setup** script, if set, runs before the agent starts.

### For both

- **Notes**, if given, are written to `NOTES.md` in the work dir and referenced in the prompt.
- **Attachments** added in the New dialog are moved into the work dir.
- **Focus.** The dialog switches the herdr view to the new agent's pane, since creating one from the UI is an explicit "take me there". The MCP tool does not, unless the caller passes `focus: true`, so an agent spawning another doesn't yank you away from what you were watching.

## Creating returns fast; booting happens after

Creating an agent has two halves. The first is quick and durable: lasso works out the branch, writes the agent's record, and has herdr create the worktree or scratch tab. As soon as the record has a workspace and a pane, the create returns its id.

The rest (copy files, setup script, launching the CLI) is the **boot**, and it runs in the background. The record's boot status moves from `booting` to `ready`, or to `failed` with the reason. A boot that fails surfaces as status `failed` in `get_agent` and `list_agents` instead of looking like a healthy idle agent.

If lasso dies mid-create (an update restart, a dropped ssh connection), the half-made agent is left as a visible failed record. Retrying the same create from the dialog picks the interrupted attempt back up, reattaching to the worktree if herdr had already made it, instead of colliding with it.

## Titles and auto-titling

An agent's title is the first line of its prompt (pasted image paths are skipped), or the title passed to `create_agent`, or `Untitled agent` when there is neither. The branch, the work dir and the workspace label are all named from it.

A prompt's first line is written for the agent, not for a sidebar, so lasso then asks a local agent CLI to summarize the whole prompt into a few words and renames the agent to that. It tries `claude`, then `codex`, `opencode`, `omp` and `pi`, using the first one that answers (each gets up to 90 seconds before lasso moves on to the next). Auto-titling:

- runs on **lasso's machine**, whatever host the agent is on;
- changes only the **display name** (the herdr workspace label and the record's title), never the branch or directory, which already exist;
- never overrides a title an MCP caller passed explicitly;
- can be turned off in Settings → General → Agents → **Auto-title new agents from their prompt**.

## Agent records

lasso keeps a record of every agent it creates, in `lasso.db` on lasso's machine: id, host, title, type, harness, model, effort, plan mode, repo, branch, base, work dir, herdr workspace and root pane, prompt, boot status and creation time. That record is what lets `list_agents`, `get_agent`, `close_agent` and `whoami` answer with more than herdr knows.

**Status** comes from herdr, live: `working`, `idle`, `blocked` (waiting on a human: a tool approval, a plan gate, a question) or `unknown`, overridden by `failed` when the boot failed. herdr reports these most reliably when its agent integration is installed for that CLI (`herdr integration install <agent>`; `lasso doctor` checks). lasso also recognizes omp's plan-review overlay itself and reports it as `blocked`.

**Panes lasso didn't create** (an agent you started by hand in herdr, a long-running bot) are listed too, marked `lasso_created: false` and addressed by their sidebar name or pane id. They have no record, so they appear and disappear with their pane, and lasso never closes or modifies them on its own.

## Closing an agent

To end an agent, close it through lasso rather than closing its pane in herdr:

- The `close_agent` MCP tool kills the agent process, then closes its pane (pass `close_pane: false` to leave the pane open as a bare shell). With `remove_worktree: true` it also deletes a git agent's worktree, which discards uncommitted work.
- `lasso closeme`, run inside the agent's own pane, closes the calling agent: it sends `$HERDR_PANE_ID` to the local lasso, which runs the same close. See [The lasso CLI for agents](../mcp/cli.md).
- The UI's agent close does the same.

Closing a pane directly in herdr works too, but the record only catches up through reconciliation (below), and any prompt file lasso staged for that agent is left behind.

## How records follow herdr

herdr's panes can disappear for many reasons lasso never sees. So lasso **reconciles** its records against herdr's pane listings and marks a record closed (a tombstone, not a deletion) once its pane is gone. A closed record drops out of `list_agents` but stays in the database as history.

Reconciliation is careful, because "no pane" has two causes and only one is "the agent is gone":

- Only a **successful** listing from a host counts. An unreachable host returns nothing, which says nothing about its agents.
- It is **per host**, since pane ids are only unique within one herdr.
- An **empty** listing never closes anything.
- A pane must be missing from **two consecutive** listings.
- A record still **booting** is left alone for its first 10 minutes, and a record with no pane yet is never closed.

It runs whenever `list_agents` is called and on a 5-minute background pass (with one at startup), so records catch up even when nobody is looking.

## Talking to agents is herdr's job

lasso creates, lists, inspects and closes agents. It has no tool to prompt an agent, read its screen or wait for it. herdr does that directly: `herdr agent prompt`, `herdr agent read`, `herdr agent wait` (or herdr's own MCP tools), and Claude Code has its own agent messaging. An orchestrating agent uses lasso to spawn workers and herdr to talk to them.

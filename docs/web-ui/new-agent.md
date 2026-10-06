---
title: The New dialog
description: Every field of the New dialog for starting a coding agent or a plain terminal on any host, its defaults, and the settings that shape it.
order: 33
nav_title: New agent
---

The **New** dialog starts a coding agent in a fresh herdr pane, or opens a plain terminal, on any host lasso can reach. Open it with the footer's **New** button, `⌘O` (agent form) or `⌘I` (terminal form), or the input dial's **New** target on a phone.

It has two tabs, **Agent** and **Terminal**. From the chat view or the Grid only the agent form is offered, titled **New agent**, since those views have no terminal to show a new shell in. `⌘↩` (or `Ctrl↩`) submits either form from any field.

## Choosing the host

The host picker sits at the bottom of the dialog, beside the create button. A host that is unreachable, not running herdr, or running an incompatible herdr is listed as unavailable and cannot be picked. When the dialog opens, the host is chosen in this order:

1. the host pinned in **Settings → General → Agents → New agent/terminal host**;
2. the host you last left the picker on in this browser;
3. the host you last created something on;
4. the host this browser tab is on.

If that host has since become unusable, the dialog falls back to this tab's host. After a successful create, lasso moves this browser tab to that host and focuses the new pane.

## The Agent form

### Git or Scratch

The first choice is the kind of agent:

- **Git** creates a new git worktree and branch off a repository, and runs the agent in it. lasso uses herdr's worktree support, so the worktree appears in herdr under its repository's workspace.
- **Scratch** creates a fresh empty directory under `~/.lasso/scratch/` and opens the agent as a new tab in the host's shared **Scratch** workspace. Use it for work that is not tied to a repository.

### Fields

| Field | Applies to | Default | What it does |
| --- | --- | --- | --- |
| **Prompt (optional)** | both | empty | The instruction handed to the agent. Its first line becomes the agent's title, which names the branch, the directory and the herdr workspace. Paste a screenshot here and it is uploaded to the selected host and its path is inserted. Leave it empty to start the agent idle, waiting for you to type in its pane; it is then named "Untitled agent". |
| **Repository** | Git | the repository you used last on that host, if it still exists | The repository to branch from. The list comes from scanning the host's **Git repos directories** (see below). |
| **Base branch** | Git | the base branch you used last with that repository, else the repository's default branch (from `origin/HEAD`), else `main` or `master`, else the first branch | The branch the new worktree starts from. |
| **AI agent** | both | the host's **Default agent**, else the agent you used last on that host, else Claude Code | The harness to launch: **Claude Code**, **Codex**, **OpenCode**, **Oh My Pi** or **Pi**. |
| **Model** | both | `default` (no flag) | Free text with suggestions for the selected harness. Blank passes no model flag, so the CLI uses its own default. Changing the harness clears it. |
| **Thinking effort** | Claude Code, Codex, Oh My Pi, Pi | `default` (no flag) | The harness's reasoning level. Hidden for harnesses without one. Changing the harness clears it. |

The levels offered for **Thinking effort**:

| Harness | Levels |
| --- | --- |
| Claude Code | low, medium, high, xhigh, max |
| Codex | minimal, low, medium, high, xhigh |
| Oh My Pi | off, minimal, low, medium, high, xhigh, max, auto |
| Pi | off, minimal, low, medium, high, xhigh, max |

### Advanced

The **Advanced** button reveals more fields. Whether it is open is remembered.

| Field | Applies to | Default | What it does |
| --- | --- | --- | --- |
| **Start in plan mode** | Claude Code, OpenCode, Oh My Pi | off | Launches the agent in its plan mode. Hidden for harnesses without one. |
| **Advisor** | Oh My Pi | off | Turns on omp's advisor (`--advisor`), a background pass that reviews each turn. |
| **Extra CLI args** | both | empty | Appended to the agent's launch command as typed. |
| **Branch prefix** | Git | the host's saved branch prefix, empty unless set | Prepended to the branch name with a `/`, for example `feat/`. |
| **Branch name** | Git | generated: the first four words of the title, slugged, plus a short random suffix | The branch to create. The line under it shows the final branch name. |
| **Attachments** | both | none | Files to give the agent. They are uploaded to the selected host, moved into the agent's working directory, and their absolute paths are appended to the prompt. |

### Remembered between openings

The dialog remembers, per host and in this browser, what you last selected: Git or Scratch, the repository, the agent, and that agent's model, effort, extra args, plan mode and advisor settings, plus the branch prefix and whether Advanced is open. Model, effort and the other harness-specific values are only restored for the same harness. The prompt and attachments are never kept.

### What happens when you create

**Create agent** returns as soon as the worktree (or scratch directory), the herdr pane and lasso's record of the agent exist. The rest happens in the background in that pane:

1. For a Git agent, the repository's **Copy files into worktree** globs are copied from the repository into the new worktree.
2. Attachments are moved into the working directory.
3. The setup commands run in the pane's shell: the repository's **Setup commands** for a Git agent, the host's **Scratch setup commands** for a Scratch agent.
4. The agent's CLI is launched with your prompt, model, effort and flags. A long or multi-line prompt is staged in a file and read by the launch command, so it is never typed into the shell.

Git worktrees are created under `~/.lasso/worktrees/<repository>/` on the agent's host. If a create is interrupted (lasso restarting during an update, a dropped connection), the dialog retries automatically, and a resubmitted create for the same branch resumes the interrupted attempt rather than making a second worktree.

With **Auto-title new agents** on (the default), lasso then asks a local agent CLI to summarise the whole prompt into a short name and renames the herdr workspace to it. The branch and directory keep the names derived from the prompt's first line. See [Settings](./sidebar.md#general).

Agents can also be created by other agents through the `create_agent` MCP tool, which takes the same choices. See [MCP tools](../mcp/tools.md) and [Agents](../concepts/agents.md).

## The Terminal form

The Terminal tab opens a plain shell, optionally running a command, as a new herdr tab.

| Field | Default | What it does |
| --- | --- | --- |
| **Command (optional)** | empty | A command to run in the new shell. Leave it blank for an interactive shell. A multi-line command runs as one script. |
| **Working directory (optional)** | the workspace's default | An absolute path or `~/…` on the selected host. |
| **Workspace** | the host's **Default workspace** (initially **Scratch**) | The herdr workspace to add the tab to. Each workspace shows its tab count. **+ New workspace…** creates one. |
| **Workspace name (optional)** | `Scratch` | Shown only for a new workspace. |
| **Tab name (optional)** | named from the command | Blank names the tab after the command: a short command as typed, a longer one by AI. A bare shell gets the next number. |

**Create terminal** opens the tab, focuses it, and moves this browser tab to the selected host if needed. If the command or the tab name could not be applied, the terminal is still created and a notice says what went wrong.

## Settings that shape the dialog

These live in **Settings → General → Agents**. The **Configuring host** picker chooses which host you are editing; each host stores its own values in its own `~/.lasso/lasso.db`. Changes save automatically.

**New Agent defaults**

| Setting | Default | What it does |
| --- | --- | --- |
| **Git repos directories** | `~/projects` | One directory per line. The Repository picker scans each one, one level deep, for git repositories. |
| **Default agent** | **Auto (use last used)** | The agent the form starts on. Auto starts on the one you picked last time. |
| **Scratch setup commands** | empty | Run in the pane before the agent starts in a Scratch agent's directory, for example `uv venv`. |

**New terminal defaults**

| Setting | Default | What it does |
| --- | --- | --- |
| **Default workspace** | `Scratch` | The workspace the Terminal form selects. If it does not exist, the form offers to create it. |

**Per-repository setup**

Pick a **Repository**, then set:

| Setting | Default | What it does |
| --- | --- | --- |
| **Copy files into worktree (globs)** | empty | Comma- or newline-separated globs, matched in the repository and copied into each new worktree. Use it for files git does not carry, such as `.env, .env.local`. |
| **Setup commands** | empty | Run in the worktree's shell before the agent starts, for example `bun install`. |

Both apply to every agent created from that repository, whoever creates it.

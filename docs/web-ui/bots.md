---
title: Bots
description: The Bots view, where lasso's long-lived Claude Code sessions read like a messaging app, and every setting a bot runs with.
order: 35
nav_title: Bots
---

A **bot** is a Claude Code session that lasso launches in a herdr pane and keeps running. Each bot has its own folder with its instructions (`CLAUDE.md`), skills and environment, plus its own model, MCP servers and channels. An assistant that answers your mail is a bot, and so is a watcher that runs on a schedule. The **Bots** view lists them like a messaging app: the bots down the left, and the selected one's conversation on the right.

Open it from the footer's view menu (it names the view you are in) on a desktop, or from the view picker on a phone (see [On a phone](#on-a-phone)). Like Chat and the Grid, Bots covers the terminal without unmounting it. Leaving the view and coming back finds it on the same page, with the conversation's scroll and draft where you left them.

The address bar follows the view, so these links work and Back moves between them:

| Path | Page |
| --- | --- |
| `/bots` | the list |
| `/bots/<name>` | that bot's conversation |
| `/bots/<name>/settings` | its settings |
| `/bots/manage` | every bot as a table |
| `/bots/manage/new` | the creator |

## The list

Each row shows the bot's avatar, its name, its newest message (prefixed **You:** when it was yours), how long ago that was, and its state:

| Mark | State |
| --- | --- |
| animated orb | working, or starting |
| plain dot | idle |
| red **Needs your input** | blocked on a question or a permission prompt |
| hollow ring | stopped |

A filled dot at the end of the row means the bot has said something since you last had its conversation open **in this browser**. Unread is kept per browser, so your phone and your desktop each keep their own.

The search box filters by name, host, workspace and the newest message. `⌘K` puts the cursor in it. The **+** in the header creates a bot, and **Manage bots** at the bottom opens the table.

## Talking to a bot

The conversation is the [chat view](./terminal.md#chat-view) pointed at the bot's pane, with two differences:

- **Messages from its channels appear as cards.** A bot with channels (mail, Mattermost, iMessage and texts, its own scheduled loops) is mostly answering messages that arrive by themselves. Each one shows where it came from (**Email**, **Mattermost**, **Scheduled** and so on), who sent it, and its subject or thread. A long one is clamped, with **Show more**.
- **The header is the bot's.** It shows the bot's name and state and a gear that opens its settings. Pinning and ending the pane are not offered here: a bot is started and stopped from its settings, and lasso relaunches it if its pane is closed under it.

Everything else works as in Chat, including two composer features that apply to every conversation:

- **Stop.** While the agent works, an empty composer's send button becomes **Stop**, which interrupts the turn (one `Esc` in its pane). If the agent was not working, or lasso could not tell whether the interrupt landed, it says so above the composer.
- **Up next.** A message sent while the agent works is held, shown as **Up next** above the composer, and sent once the agent is idle, one at a time and in order. Remove one with its ✕. Pressing **Stop** puts the held messages **on hold**: nothing goes out until you press **Send queued messages**. The queue lives in this browser tab, so it is lost if you close the tab first. When lasso cannot confirm that a held message was delivered, the queue pauses and says so, and nothing is resent automatically.

A stopped bot has no conversation to show. Its page offers **Start**, which resumes its last session, and **Start fresh**, which begins a new one.

## Settings

The gear in the conversation's header opens `/bots/<name>/settings`. **General** and **Connections** are the bot's own settings and are saved together by the footer's **Save**. When the bot is running, **Save & restart** saves them and relaunches it; if it is in the middle of a turn, lasso asks first. The other tabs edit files in the bot's folder and save as you change them.

### General

| Field | What it does |
| --- | --- |
| **Name** | Set when the bot is created, then fixed: lowercase letters, digits and dashes, up to 40. It names the herdr agent, the default folder and the address. |
| **Avatar** | An emoji or up to 8 characters. Empty shows the name's first letter on a colour taken from the name. |
| **Host** | Set when the bot is created: the machine it runs on and where its folder lives. |
| **Workspace** | The herdr workspace its pane opens in. Default **Bots**. |
| **Folder** | Its working directory. Default `~/bots/<name>`. |
| **Model** | Free text with Claude Code's model suggestions. Blank uses Claude Code's default. |
| **Thinking effort** | Claude Code's effort level, or default. |
| **Permission mode** | What it may do without asking (`acceptEdits`, `auto`, `plan`, `dontAsk`, `manual`, `bypassPermissions`, or Claude Code's default). |
| **Extra CLI args** | Added to its `claude` command, one argument per line. |
| **Keep running** | Relaunch it when its session ends or herdr restarts, resuming the same conversation. |

### Connections

The MCP servers the bot gets. Each has a name and a type: **stdio** (a command, its arguments one per line, and environment variables) or **http**/**sse** (a URL and headers). Tick **Channel** on a server that also delivers messages for the bot to answer.

**Only these servers** starts the bot with these servers and nothing else, without your claude.ai connectors or user-level MCP servers.

Keep secrets out of this page. Write `${VAR}` in a server's environment or headers, and set `VAR` under **Environment**, where it can be stored encrypted.

### Skills

The bot's project skills, in `.claude/skills` in its folder. Add one from the host's own `~/.claude/skills`, or from any skill directory on the host by path. Adding copies the skill in, so later edits to the original do not reach the bot. Removing asks for a second click.

### Instructions

The bot's `CLAUDE.md`: who it is and how it works. Edit it here and **Save CLAUDE.md** (or `⌘S`). A running bot reads it again on its next restart.

### Environment

Variables in the bot's `fnox.toml`, which mise hands to the bot, and nothing else, when it launches. Tick **Secret** to keep a value in the file's default provider. Unless you changed it, that is lasso's own age key (`~/.lasso/age.txt` on the bot's host), and the value is decrypted only at launch. Afterwards it shows as `●●●●●●●●` and can only be replaced, never read back. To keep secrets in 1Password, a vault or elsewhere, add a provider to the bot's `fnox.toml` with `fnox provider add` and make it `default_provider`. Changes reach a running bot on its next restart. Bots need mise 2026.10.4 and fnox 1.39 or newer on their host.

A host with no age key yet cannot store secrets. The page says so and offers **Create encryption key**, which asks before writing `~/.config/mise/age.txt` on that host. Every bot on the host uses that one key, so back it up.

### Launch

Start, stop and restart the bot (**Restart fresh** begins a new session), and **Open terminal** to switch to its pane in the terminal. Below them is the launch script lasso generates from the settings. The bot runs as `mise run bot` in its folder, and lasso rewrites the script on every save, so edit the settings rather than the file.

### Deleting a bot

**Delete bot…** in the footer is available once the bot is stopped. lasso forgets the bot, but its folder stays on the host with its `CLAUDE.md`, skills and environment.

## Creating a bot

The **+** in the list's header, or **New bot** on the management page, opens the creator. It has the **General** and **Connections** tabs. The bot is created stopped and lands on its settings, so you can write its instructions, add skills and set its environment before you start it.

## Manage bots

`/bots/manage` lists every bot with its host, workspace, model, number of channels, state and when it was last active, with **Start** or **Stop** and a settings button on each. At desktop widths it is a table; on a phone it is a card per bot.

## On a phone

Below 768 px the view is one column at a time: the list, then the bot you pick, its settings or the management page, each with **‹ Bots** to go back.

Bots covers the input dial, so the list's header has its own **Switch view** button, which opens the same view picker as the dial's **Chat** button.

---
title: Bots
description: The Bots view, where lasso's long-lived Claude Code sessions read like a messaging app, and every setting a bot runs with.
order: 35
nav_title: Bots
---

A **bot** is a Claude Code session that lasso launches in a herdr pane and keeps running. Each bot has its own folder with its instructions (`CLAUDE.md`), skills and environment, plus its own model, MCP servers and channels. An assistant that answers your mail is a bot, and so is a watcher that runs on a schedule. The **Bots** view lists them like a messaging app: the bots down the left, and the selected one's conversation on the right.

Open it with `⌘.` (press it again to go back to the terminal), from the footer's view menu (it names the view you are in) on a desktop, or from the view picker on a phone (see [On a phone](#on-a-phone)). It can also be [its own app](#the-bots-app) on your phone or desktop. Like Chat and the Grid, Bots covers the terminal without unmounting it. Leaving the view and coming back finds it on the same page, with the conversation's scroll and draft where you left them.

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

Drag a bot by the grip at its left edge to reorder the list; the order is saved for everyone. New bots go to the end. The **+** in the header creates a bot, the bell beside it turns [notifications](#notifications) on or off for this device, and **Manage bots** at the bottom opens the table.

### When the view is narrow

The list folds away when the Bots view itself is narrower than 760 px. That happens on a phone, in a narrow window, or beside a wide right sidebar. The list then becomes its own screen, and every other page gets a **‹ Bots** button that goes back to it. In a conversation, the bot's name in the header becomes a switcher: it opens a menu of every bot with its state, plus **All bots**, **Manage bots** and **New bot**.

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
| **Avatar** | A picture, set once the bot exists by clicking the avatar (see [Pictures](#pictures)). Without one it shows the name's first letter on a colour taken from the name. |
| **Host** | Set when the bot is created: the machine it runs on and where its folder lives. |
| **Workspace** | The herdr workspace its pane opens in. Default **Bots**. |
| **Folder** | Its working directory. Default `~/bots/<name>`. |
| **Model** | Free text with Claude Code's model suggestions. Blank uses Claude Code's default. |
| **Thinking effort** | Claude Code's effort level, or default. |
| **Permission mode** | What it may do without asking (`acceptEdits`, `auto`, `plan`, `dontAsk`, `manual`, `bypassPermissions`, or Claude Code's default). |
| **Extra CLI args** | Added to its `claude` command, one argument per line. |
| **Keep running** | Relaunch it when its session ends or herdr restarts, resuming the same conversation. |
| **Notify me when it answers** | Send a notification each time it finishes a reply. On by default. See [Notifications](#notifications). |

#### Pictures

Click the avatar under Avatar to upload a PNG, JPEG, WebP or GIF of up to 2 MB (a square one looks best). It shows in the list, the conversation and the bot's notifications. Click it again to replace the picture, or the small × on its corner to remove it. The picture is saved as soon as you pick it, not with the form. lasso keeps it in the bot's folder as `.lasso/avatar.<ext>`. Without a picture the name's first letter shows, and SVG images are not accepted.

### Connections

The MCP servers the bot gets, as a grid of cards. Each card shows the server's name, a **CHANNEL** badge or its type, its command or URL, and its environment-variable count or sign-in state. A dot marks a card with unsaved changes. The chips above filter the grid: **All**, **Channels**, or **Tools** (everything that is not a channel), each with its count, and the choice is remembered in this browser. **Add connection** under Channels starts the new server as a channel.

Click a card, or **Add connection**, to edit it in a dialog. A server is **stdio** (a command, its arguments one per line, and environment variables) or **http**/**sse** (a URL and headers). Tick **Channel** on a server that also delivers messages for the bot to answer. **Done** keeps the change in the page and the footer's **Save** writes it; **Remove connection** takes the server out. Closing the dialog on a new server that was never filled in drops it.

A dashed **lasso** card, *added by lasso*, is the server lasso gives every bot on its own machine for its settings tools. A dashed **lasso-channel** card is the channel that delivers its [jobs](#jobs). Both are shown so the grid is complete, and cannot be edited.

**Only these servers** starts the bot with these servers and nothing else, without your claude.ai connectors or user-level MCP servers.

Keep secrets out of this page. Write `${VAR}` in a server's environment or headers, and set `VAR` under **Environment**, where it can be stored encrypted.

**Signing in with OAuth.** For an http or sse server that asks you to log in, tick **Sign in with OAuth**, save, then press **Sign in**.

- **What happens:** the server's login page opens in a new tab. Once you approve, it sends you back to lasso, which finishes the sign-in by itself. lasso discovers the server's sign-in settings and registers itself as a client automatically.
- **When the redirect won't load:** some servers will only send you back to `localhost`. When lasso runs on a VPS, that page fails to load. Copy the address from that tab's address bar (it holds `?code=…&state=…`), paste it into the box under the server, and press **Finish**.
- **Where the tokens go:** lasso keeps the tokens in the bot's `fnox.toml`, encrypted like any other secret, and refreshes them before they expire. Claude reads the current one each time it connects. Each bot signs in separately, so two bots can use different accounts on the same service.
- **A running bot needs a restart:** the first sign-in, and a sign-out, change how the bot connects, and a running bot only reads that when it starts. The panel says so; restart it from **Launch**. Later token refreshes need no restart.
- **Servers that don't register clients automatically:** open **Server without automatic client registration** and enter the client ID, the redirect URI it was registered with, and the scope.

### Jobs

Scheduled prompts and webhooks that lasso delivers into the bot's session, shown as a grid of square cards (as many to a row as fit). Each card shows the job's name, its schedule in plain words with the time zone ("Every day at 6 AM, noon and 6 PM · PT"), when it runs next and when it last ran (✓ delivered, ✗ dropped: hover for why), and the start of its message. Badges mark a **webhook**, a **paused** job, and **queued ×N** when it fired while the bot was busy or not listening. Enabled jobs come first, by next run, then jobs with no schedule, then paused ones. **Run now** fires a job at once (a paused one too). **Copy URL** copies a webhook's address. The **⋯** menu pauses, resumes, duplicates or deletes. Changes take effect immediately, with no save of the bot and no restart.

Click a card, or **New job**, to edit it:

- **Message:** the instruction delivered each time the job fires.
- **Schedule:** **Every…** (minutes or hours), **Daily**, **Weekdays**, **Weekly** (pick days), **Monthly** (pick a day), each with one or more times, or **Custom** for a cron expression. The time zone defaults to your browser's. A box under it reads the schedule back as a sentence with its next three run times, or says why it is not valid.
- **Webhook:** turn it on and save to get a URL. Anything that POSTs to it fires the job, and the request body is delivered after the message, marked as coming from the caller. The URL's `key` is its only credential, so it is masked until **Show**, and **Rotate key** replaces it (the old URL stops working at once). A caller that sends headers can use `Authorization: Bearer <key>` instead. From outside the tailnet, the hostname's edge (Cloudflare Access) must let `/hooks/` through.
- **Recent:** the job's last deliveries: when, what fired it, and whether it was delivered or dropped.

The line under the description says whether the bot is listening. Jobs reach a bot through lasso's own channel, which it gets from its next start: a bot that was running before lasso had jobs offers **Restart**. A job that fires while the bot is **stopped** is dropped, not saved for later, and one nobody picks up within a day is dropped too. Firings that pile up while the bot is busy arrive as one delivery with a count. Jobs are offered only to bots on lasso's own machine.

### Skills

**Always available** lists your user-level skills, from `~/.claude/skills` on the bot's host, in a scrolling list. Claude Code loads them in every session, so every bot has them without adding anything.

**This bot only** is the bot's project skills, in `.claude/skills` in its folder, which no other session sees. To add one, paste anything into **Add a skill**: a URL, a GitHub repo, a path, a `SKILL.md`, or a description of what the skill should do. Then press **Ask <bot> to install it**. The request goes to the running bot as a chat message, and the bot fetches or writes the skill into its own `.claude/skills`; you can follow along in its chat. The list updates once the skill is there. Removing a skill asks for a second click.

### Instructions

The bot's `CLAUDE.md`: who it is and how it works. Edit it here and **Save CLAUDE.md** (or `⌘S`). A running bot reads it again on its next restart.

### Environment

Variables in the bot's `fnox.toml`, which mise hands to the bot, and nothing else, when it launches. Tick **Secret** to keep a value in the file's default provider. Unless you changed it, that is lasso's own age key (`~/.lasso/age.txt` on the bot's host), and the value is decrypted only at launch. Afterwards it shows as `●●●●●●●●` and can only be replaced, never read back. To keep secrets in 1Password, a vault or elsewhere, add a provider to the bot's `fnox.toml` with `fnox provider add` and make it `default_provider`. Changes reach a running bot on its next restart. Bots need mise 2026.10.4 and fnox 1.39 or newer on their host.

A host with no age key yet cannot store secrets. The page says so and offers **Create encryption key**, which asks before writing `~/.config/mise/age.txt` on that host. Every bot on the host uses that one key, so back it up.

### Launch

Start, stop and restart the bot (**Restart fresh** begins a new session), and **Open terminal** to switch to its pane in the terminal. The bot can also restart itself: ask it to, and it runs `mise run restart`, which relaunches it in the same pane on the same conversation once its turn ends. Below them is the launch script lasso generates from the settings. The bot runs as `mise run bot` in its folder, and lasso rewrites the script on every save, so edit the settings rather than the file.

**Launch task** picks the mise task that starts the bot: `bot`, or a mode of your own in its folder. Start, keep-running, herdr's restore and the bot's own restart all go through it. A mode is a task that sets an environment and hands off to `bot`, so the channel grants and launch dialogs stay lasso's. For example, `.mise/tasks/deepseek` runs the bot on DeepSeek's Anthropic-compatible API:

```sh
#!/bin/sh
#MISE description="Run on DeepSeek"
#MISE secrets=["DEEPSEEK_API_KEY"]
#MISE interactive=true
export ANTHROPIC_BASE_URL="https://api.deepseek.com/anthropic"
export ANTHROPIC_AUTH_TOKEN="$DEEPSEEK_API_KEY"
unset ANTHROPIC_API_KEY DEEPSEEK_API_KEY
exec mise run bot -- --model sonnet "$@"
```

Add `DEEPSEEK_API_KEY` as a secret under **Environment**, then pick `deepseek` here. `#MISE interactive=true` is required: without it a task that receives secrets loses the terminal. Keep `"$@"` last, since a restore appends `--resume <id>`. Two limits to know:

- **A conversation started on Anthropic may not resume on another provider.** DeepSeek refuses some of the content Claude Code stores, so after switching, use **Restart fresh**.
- **claude.ai connectors are off under another provider's token.** Claude Code turns them off whenever `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` is set, so put any connector the bot needs among its own MCP servers.

A launch task that the folder doesn't define is refused when you save.

### Deleting a bot

**Delete bot…** in the footer works in any state: a running bot is stopped first, which closes its pane. lasso forgets the bot, but its folder stays on the host with its `CLAUDE.md`, skills and environment.

**Right-click a bot in the list** (long-press on a touch screen) for the same actions without opening it: Open chat, Settings…, Start, or Restart and Stop while it runs, and Delete….

## Asking a bot to change itself

A bot can change its own settings when you ask it to in its conversation: "switch to Opus", "add the GitHub MCP server", "set `API_URL` to …", "use this picture". Every bot on lasso's own machine gets lasso's MCP server, named `lasso`, and its tools read and change that bot's settings:

- the **General** settings: model, effort, permission mode, extra args, keep running, avatar and workspace
- its MCP servers and channels
- its environment variables, plain or secret
- its picture, from an image file it downloaded or generated

It edits its `CLAUDE.md` and `.claude/skills` directly, as files in its folder. Most changes take effect after a restart, and the bot restarts itself to apply them. Some things stay with you:

- **Signing in** to an OAuth server. The bot can add the server, but you sign in from **Connections**.
- **Creating and deleting** bots.
- **Bots on other hosts and gated servers.** A bot on another host doesn't get the `lasso` server, and neither does any bot while lasso's `/mcp` requires OAuth (`MCP_OAUTH`). Add the server by hand under **Connections** in that case. A server you named `lasso` yourself is kept as is.

It is safer to type a secret into **Environment** yourself than to paste it into a conversation.

## Notifications

lasso can notify you each time a bot finishes a reply. The notification is titled with the bot's name, shows the start of its message and its picture, and opens that bot's conversation. A newer answer from the same bot replaces the older notification rather than stacking.

- **Turn it on for each device.** The bell in the list's header turns notifications on or off for the device you are on. It does the same as **Settings → General → Notifications**, and it is the only switch in the [Bots app](#the-bots-app), which counts as a device of its own.
- **Turn it off for a bot.** Untick **Notify me when it answers** in that bot's **General** settings.
- **No duplicates for what you're reading.** No notification appears while that bot's conversation is focused and on screen, except on iPhone and iPad, which always show one.

Every device with notifications on gets every notification, including [blocked agents](../concepts/notifications.md) from the rest of lasso.

## The Bots app

The Bots view can be installed as an app of its own, called **Bots**. It opens straight to your bots, with no terminal, footer or other views, and its notifications arrive under its own icon.

1. Open lasso at `/bots` (or any page of the Bots view) over HTTPS.
2. Install it: in Safari, **Share → Add to Home Screen**; in Chrome, **Install**.
3. Open **Bots** from its icon and press the bell to turn on notifications. On an iPhone or iPad this has to happen inside the installed app.

Opening a bot's terminal from the app leaves it for lasso itself. Adding `?app=bots` to a `/bots` address in an ordinary tab shows the same Bots-only page.

## Creating a bot

The **+** in the list's header, or **New bot** on the management page, opens the creator. It has the **General** and **Connections** tabs. The bot is created stopped and lands on its settings, so you can write its instructions, add skills and set its environment before you start it.

## Manage bots

`/bots/manage` lists every bot with its host, workspace, model, number of channels, state and when it was last active, with **Start** or **Stop** and a settings button on each. At desktop widths it is a table; on a phone it is a card per bot.

## On a phone

On a phone the view is one column at a time, as in [When the view is narrow](#when-the-view-is-narrow): the list, then the bot you pick, its settings or the management page, each with **‹ Bots** to go back.

Bots covers the input dial, so the list's header and each bot's own header have a **Switch view** button, which opens the same view picker as the dial's **Chat** button.

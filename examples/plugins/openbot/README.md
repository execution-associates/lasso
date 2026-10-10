# OpenBot

A chat for one long-running agent, as a lasso main-window view. It is modelled on CopilotKit's [OpenMuse](https://github.com/CopilotKit/openmuse) and points at Jessica, Stephan's assistant: a Claude Code session in herdr, not a model behind an API. So OpenBot reads the session's transcript and types into its pane, through lasso, the way lasso's own Chat view does.

## What it does

- **Header.** The agent's name, plus a status line that names what it is doing: `Working…`, the running tool, `Needs your input · <question>`, `Messages on hold`, or `Here when you need me`.
- **Conversation.** Your messages, its replies rendered as Markdown, and tool calls as cards. A burst of the same tool collapses into one card with a count, and MCP tools read as `Gmail · search threads`. Its reasoning is folded away.
- **Incoming messages.** The mail, texts, Mattermost posts and scheduled loops a Claude Code channel delivered, labelled by source, sender and subject. For an assistant these are most of what it is answering.
- **Choice cards.** When the agent asks with AskUserQuestion, its options are buttons. A tap answers the question in the agent's pane, and lasso first checks that the question is still on screen.
- **Composer.** Enter sends and Shift+Enter adds a newline (on a phone, the button sends). While the agent works, Send becomes Stop.
- **Follow-ups queue.** Anything sent while the agent works waits under "Up next" and goes out in order once it is idle. Stop holds the queue ("Messages on hold") until you press "Send queued messages".
- **Scrolling.** The view follows the conversation while you are at the bottom and shows "Latest messages" when you are not. Scrolling up loads earlier history.
- **Looks like lasso.** It is painted with lasso's theme tokens and fonts through the plugin SDK, light or dark, and follows lasso live.

Remote images in the agent's prose are shown as links, not loaded, because they are often someone else's mail. Raw HTML in it is shown as text.

## Install

From a lasso checkout:

```sh
lasso plugin link examples/plugins/openbot
```

Then approve it in Settings → General → Plugins. The dialog lists the one thing it asks for:

> Read the chat and type into agent `jessica` on `local`

That grant is the whole of OpenBot's power, and it is large. It covers everything in Jessica's transcript, which includes your mail and messages. It also lets OpenBot type into her pane as you. Pick **View → OpenBot** in the footer, or the view picker on a phone.

## Pointing it at another agent

Change two things together:

1. `plugin.json`: `"agents": [{ "name": "<agent>", "host": "<host>" }]`. The name is the one `herdr agent rename` set; the host defaults to `local`.
2. `ui/index.html`: the `openbot-agent` and `openbot-host` meta tags.

Editing `agents` makes the plugin `needs_approval` until you approve it again. The page naming an agent grants nothing on its own.

## Building

The page in `ui/` is committed, so installing needs no build. After changing `web/src`:

```sh
mise run openbot:build     # into ui/openbot.js + ui/openbot.css
mise run openbot:check     # biome format + lint fixes
```

Both run in this worktree's dev container, never on the host. `ui/index.html` is written by hand rather than generated: it loads lasso's SDK from `/plugins/_sdk/`, which only exists when lasso serves the page. The app is built as a classic IIFE script because a module script would be a CORS fetch, and the page's sandboxed origin cannot make one.

## How it talks to lasso

The page has no access to lasso except through the `lasso-plugin/1` bridge (see `docs/plugins/authoring.md`, "Agent chat"):

| | |
|---|---|
| `chat.read` | polled every 2 s while the view is visible, plus `before` for older pages |
| `chat.send` | each message, one at a time; only `confirmed` clears it, and `uncertain` is never resent automatically |
| `chat.answer` | a choice-card tap |
| `chat.stop` | Stop |

`web/src/merge.ts` and `web/src/Cards.tsx` are ports of lasso's `lib/chat-merge.ts`, `lib/chat-queue.ts` and `ChatView.tsx` cards, since a sandboxed page cannot import lasso's modules.

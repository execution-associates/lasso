---
title: Notifications
description: What lasso notifies you about, how each device chooses what may interrupt it, how agents send one on purpose, and how delivery is reported honestly.
order: 24
---

An agent blocked on a tool approval stops until a human answers, and nothing in a browser tab can tell you unless you're already looking at it. lasso closes that gap with one notification pipeline that reaches you wherever you are: **Web Push** to a registered device with no lasso tab open, including a locked phone, and a live toast (or, in the [Mac app](../getting-started/desktop.md), a sound and a macOS notification) in every lasso window that is open. To set a device up, follow [On your phone](../getting-started/phone.md); this page explains what gets sent and when.

## What triggers a notification

### An agent blocks

lasso watches every host it can reach for an agent whose status changes **into `blocked`**: a tool-approval prompt, Codex's "Action Required", a plan gate (including omp's plan-review overlay, which lasso detects itself), a question waiting on you. herdr supplies the status; installing herdr's integration for each agent CLI (`herdr integration install <agent>`) makes it more reliable.

- **Titled for the lock screen.** The title is the agent's name (its workspace label). The body says which CLI, on which host, and what it was last working on: `claude is blocked on myhost — <task>`.
- **One entry per agent.** Notifications for the same pane replace each other on the device instead of stacking.
- **At most once per five minutes per agent.** An agent that answers one approval only to raise the next buzzes once, not every time.
- **Nothing is missed across a restart.** An agent that blocked while lasso was restarting, or while you were turning notifications on, is announced on the first check.
- **A host that comes back isn't a flood.** lasso remembers each pane's last status for ten minutes after it disappears, so an unreachable host reconnecting is not read as all of its agents having just blocked.

The watcher checks every 10 seconds, but **only while at least one device is registered**. With none, it touches no host; the whole feature costs one local database count.

### An agent asks for you

An agent can notify you deliberately, when it needs a decision, is blocked on a question, or has finished a long job while you were away:

```bash
lasso notify "the migration drops 2 columns, safe to run on prod?"
```

or with the `notify` tool on lasso's MCP server. Both are the same call ([`lasso notify`](../mcp/cli.md), [`notify`](../mcp/tools.md)).

- The notification is titled with the calling agent's name, resolved from its `$HERDR_PANE_ID`, and opens on that agent's host. An agent that can't be identified still gets its message through, titled `lasso`.
- Deliberate messages are **never collapsed or rate-limited**: two messages are two things the agent chose to say.
- `-title` overrides the headline, and a message can be piped in on stdin: `make test 2>&1 | tail -5 | lasso notify`.

### A bot answers

A [bot](../web-ui/bots.md) with **Notify me when it answers** on (the default) sends a notification each time it finishes a reply with something new.

- **Titled with the bot's name**, with the start of its message as the body and the bot's picture as the icon.
- **One entry per bot.** A newer answer replaces the older notification.
- **No replay on restart.** The first time lasso sees each bot after it starts, it only notes the bot's latest message, so a restart doesn't announce every bot's last answer again.
- **Not for what you're reading.** No notification is shown while that bot's conversation is focused and on screen. iPhone and iPad are the exception and always show one, because iOS withdraws permission from a site whose pushes show nothing.

### An agent finishes a turn

Off by default. A device that turns on **An agent finished a turn** hears from any agent that stops working with its turn done, titled like a blocked agent's.

A bot is a herdr agent too, so its pane finishing is the same turn as the bot's reply. **A bot's turn is announced once**: a device that takes **A bot replied** hears the reply and not the finished turn; a device that takes only finished turns still hears that the bot finished. A bot whose **Notify me when it answers** is off says nothing about its turns, but is still announced when it waits on you.

### lasso's own notices

lasso's own news, such as a theme a host would not take or a sync that finished, shows as a toast in open lasso windows. Only errors are also pushed to your devices: a result is a toast's job, not a buzz.

### The test button

Settings → General → Notifications → **Send a test notification** sends one through exactly the same path, to every registered device, whatever each device has chosen.

## Choosing what interrupts each device

Every device chooses which kinds of notification may interrupt it, in Settings (the full UI's **This device is told about**, the [simple app](../getting-started/phone.md#the-simple-app)'s and the Mac app's **Notify me about**):

| Kind | Default |
| --- | --- |
| An agent is waiting on you | on |
| An agent finished a turn | off |
| An agent pinged you | on |
| A bot replied | on |
| lasso's own notices | on |

- **A device** is one browser (a phone's home-screen app, a laptop's browser) or one Mac running the Mac app. The choice is stored on lasso against that device, so it holds for both its pushes and its open windows.
- **Only what you changed is stored.** A kind you never touched follows the default above, and so does a kind a later release adds.
- **A choice governs interruptions, never a list.** It decides whether a device is pushed, shown a toast, played a sound or shown a macOS notification. The simple app's and the Mac app's Inbox list everything that is waiting either way, because turning a kind off must never hide something that needs you.
- A phone that turned on push before devices had choices gets the defaults until it registers again, which it does the next time its Settings opens.

## Delivery is reported honestly

A notification that reached nobody is not "sent". Both the `notify` tool and `lasso notify` tell the caller whether any device took it:

- The tool's reply carries `sent`. `sent: false` means **no device received it**, and `detail` says why (usually that nothing is registered yet). An open lasso window or Mac app that took it counts as received.
- `lasso notify` exits **non-zero** when nothing was delivered, printing the reason on stderr, so a script can tell "delivered" from "nobody was listening".

An agent should check that answer before telling you it pinged you.

## Opening a notification

Tapping a notification opens lasso on the **host** the agent runs on. It deliberately doesn't focus the agent's pane: herdr's focus is shared by every client of that session, so a notification that moved it would move it for everyone watching.

A bot's notification opens **that bot's conversation** instead. Either way, an open lasso window is reused rather than a new one opened.

## Devices

Each device registers for push from Settings → General → Notifications, the simple app's Settings, or the bell in the Bots list's header, and gets every notification of the kinds it takes. The Settings list shows each device with the outcome of its last push. Unticking the box on a device removes it.

- A push service answering that a subscription **is gone** (HTTP 404 or 410) removes that device automatically.
- Any other failure (a rejected credential, the push service being down) is recorded on the device and shown in Settings as `last push failed: <reason>`, and the device keeps being tried. A transient outage never deletes a healthy device.
- A push that a device doesn't collect within 30 minutes is dropped by the push service.
- An open lasso window shows nothing while its tab is hidden: there, the phone's push is the interruption.

A subscription belongs to the **origin** it was made on, so the same phone using two lasso URLs is two devices and gets everything twice.

## Privacy and keys

- **Payloads are encrypted end to end** (RFC 8291). Apple's, Google's or Mozilla's push service relays a blob it can't read, and the payload carries everything the device needs to display it, so the device never calls back to lasso to render one.
- **The server key** (the VAPID keypair identifying your lasso to push services) is generated once and stored in `~/.lasso/lasso.db`. Every device's subscription is pinned to it, so it never changes. Losing or replacing `lasso.db` means every device must enable notifications again.
- **The contact.** Push requests name a contact for the push provider. By default that is the HTTPS origin you subscribed from. Set `LASSO_PUSH_CONTACT=mailto:you@example.com` if you'd rather your push provider saw an email address.

lasso's service worker exists only to show notifications. It caches nothing and never serves lasso offline.

---
title: Notifications
description: When lasso pushes a notification, how agents send one on purpose, and how Web Push delivery is reported honestly.
order: 24
---

An agent blocked on a tool approval stops until a human answers, and nothing in a browser tab can tell you unless you're already looking at it. lasso closes that gap with **Web Push**: notifications that reach a registered device with no lasso tab open, including a locked phone. To set a device up, follow [On your phone](../getting-started/phone.md); this page explains what gets sent and when.

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

### The test button

Settings → General → Notifications → **Send a test notification** sends one through exactly the same path, to every registered device.

## Delivery is reported honestly

A notification that reached nobody is not "sent". Both the `notify` tool and `lasso notify` tell the caller whether any device took it:

- The tool's reply carries `sent`. `sent: false` means **no device received it**, and `detail` says why (usually that nothing is registered yet).
- `lasso notify` exits **non-zero** when nothing was delivered, printing the reason on stderr, so a script can tell "delivered" from "nobody was listening".

An agent should check that answer before telling you it pinged you.

## Opening a notification

Tapping a notification opens lasso on the **host** the agent runs on. It deliberately doesn't focus the agent's pane: herdr's focus is shared by every client of that session, so a notification that moved it would move it for everyone watching.

## Devices

Each device registers itself from Settings → General → Notifications, and **every registered device gets every notification**. The Settings list shows each device with the outcome of its last push. Unticking the box on a device removes it.

- A push service answering that a subscription **is gone** (HTTP 404 or 410) removes that device automatically.
- Any other failure (a rejected credential, the push service being down) is recorded on the device and shown in Settings as `last push failed: <reason>`, and the device keeps being tried. A transient outage never deletes a healthy device.
- A push that a device doesn't collect within 30 minutes is dropped by the push service.

A subscription belongs to the **origin** it was made on, so the same phone using two lasso URLs is two devices and gets everything twice.

## Privacy and keys

- **Payloads are encrypted end to end** (RFC 8291). Apple's, Google's or Mozilla's push service relays a blob it can't read, and the payload carries everything the device needs to display it, so the device never calls back to lasso to render one.
- **The server key** (the VAPID keypair identifying your lasso to push services) is generated once and stored in `~/.lasso/lasso.db`. Every device's subscription is pinned to it, so it never changes. Losing or replacing `lasso.db` means every device must enable notifications again.
- **The contact.** Push requests name a contact for the push provider. By default that is the HTTPS origin you subscribed from. Set `LASSO_PUSH_CONTACT=mailto:you@example.com` if you'd rather your push provider saw an email address.

lasso's service worker exists only to show notifications. It caches nothing and never serves lasso offline.

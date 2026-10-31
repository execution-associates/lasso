---
title: On your phone
description: The simple phone app at /m, the home-screen install over HTTPS, and push notifications chosen per device.
order: 12
nav_title: Phone setup
---

lasso works as a phone app. On a phone it opens the **simple app** at `/m`: an Inbox of what is waiting on you, your agents and bots as conversations, and a short Settings screen. The full UI, with the herdr terminal and an input dial for the keys a touch keyboard lacks, is one tap away (see [The simple app](#the-simple-app)). Its most useful trick on a phone is buzzing you when an agent is stuck waiting on you. That needs three things, in this order: an HTTPS origin, the home-screen install, and notifications turned on from inside the installed app.

## 1. Reach lasso over HTTPS

Service workers and the Push API only exist in a **secure context**: an `https://` origin, or `localhost` itself. Reached at a plain-HTTP address such as `http://myhost:8090` over a tailnet, a phone's browser doesn't merely refuse permission; the APIs are absent, and lasso's Settings says *"This browser can't do Web Push"*. A tailnet is private, but privacy isn't what the browser checks.

Two ways to get a real HTTPS origin without exposing lasso publicly:

- **Tailscale.** Start lasso with `-tailnet` and it serves HTTPS on your node's `*.ts.net` name, with the phone identified by the tailnet. See [Tailscale](../deployment/tailnet.md).
- **Cloudflare Tunnel + Access.** Publish lasso on a hostname of yours behind Cloudflare Access, with `-listen-access`. See [Cloudflare](../deployment/cloudflare.md).

HTTPS also helps two smaller things: Files-tab downloads, which some browsers block from a plain-HTTP page, and terminal copy, which otherwise falls back to a legacy clipboard method (see [Why HTTPS matters](../deployment/index.md#why-https-matters)). Uploads and dictation work either way.

## 2. Add it to the home screen

On **iPhone or iPad** (iOS 16.4 or later), open your HTTPS lasso URL in Safari, tap the Share button, and choose **Add to Home Screen**. iOS only offers push to a web app launched from the home screen, never to a Safari tab, so this step is not optional there.

On **Android** and desktop browsers, push works in an ordinary tab. Installing lasso as an app (Chrome's "Add to Home screen" or "Install app") is still nicer: it opens full screen from its own icon.

The home-screen app is a shortcut to your lasso, not an offline app. It caches nothing, so the machine running lasso has to be up and reachable to open it.

**Behind Cloudflare Access, sign in once inside the installed app.** On iOS the home-screen app keeps its own cookies, apart from Safari's, so the first launch shows Cloudflare's login page. Sign in there, and the app stays signed in for the Access application's session duration (up to a month). On a tailnet there is nothing to sign in to.

When the Access session ends while lasso is open, the next request comes back 401 and a bar at the top says *"You're signed out. Sign in again to keep working."* Tap **Sign in again**: lasso reloads, which runs the Access login in the same window, inside the installed app too.

## 3. Turn on notifications

1. Launch lasso **from the home-screen icon** (on iOS, the Safari tab won't do).
2. In the simple app, open **Settings** and turn on **Notifications**. In the full UI it is **Settings → General → Notifications → Push notifications to this device**. The browser asks for permission; allow it.
3. Choose what may interrupt this phone under **Notify me about** (in the full UI, **This device is told about**). The choices belong to this device: a laptop and a phone can want different things.
4. In the full UI, **Send a test notification** sends one to every registered device and reports how many took it, or why it couldn't send.

The full UI's Settings explains the state it finds:

| What Settings says | What it means |
| --- | --- |
| "On iOS, notifications only work from a Home Screen web app..." | You are in a Safari tab. Add lasso to the home screen and open it from there. |
| "This browser can't do Web Push..." | No service worker or Push API: almost always a plain-HTTP origin. Use an HTTPS origin. |
| "Notifications are blocked for this site..." | Permission was denied earlier. Allow notifications for the site in the browser's (or iOS's) settings, then turn them on again. |

Below the checkbox, the full UI lists every registered device with the outcome of its last push, so a device that has quietly stopped working shows `last push failed: <reason>` instead of looking healthy. Turning notifications off on a device unregisters it.

## What you'll be notified about

Each device picks from the same list, and starts with these defaults:

| Kind | Default | When |
| --- | --- | --- |
| An agent is waiting on you | on | An agent stops mid-task on a tool approval, a plan gate or a question, on any host lasso can reach. |
| An agent finished a turn | off | An agent stops working with its turn done. |
| An agent pinged you | on | An agent asks for you with `lasso notify "..."` or the `notify` MCP tool. |
| A bot replied | on | A bot finishes a turn with something new to say, when that bot's **Notify me when it answers** is on. |
| lasso's own notices | on | Errors from lasso itself, like a theme a host would not take. |

A choice decides only what interrupts the phone. The Inbox lists everything that is waiting either way. Opening a notification lands lasso on the host the agent runs on, or in the bot's conversation.

A bot's notification opens its conversation in whichever app the phone uses, simple or full. [Notifications](../concepts/notifications.md) explains exactly when one is sent and how delivery is reported.

## The simple app

A phone (a touch screen under 768 px wide) that opens lasso's address goes to `/m`, the simple app. It has four tabs:

- **Inbox.** What is waiting on you across lasso's Claude Code and omp agents: questions to answer, agents blocked with nothing to pick from, finished turns, and lasso's notices. It opens on the newest card; swipe sideways or use the arrows to page through the rest. Answer a question with a tap, reply to a finished turn, or **Acknowledge** a card that needs nothing more. While a reply to a card is half written, its Acknowledge is greyed out until you send the reply or clear it. It lists the same cards as the [Mac app](./desktop.md) for the same lasso: what counts, from when, and Acknowledge are all lasso's, shared by every device. Only which cards this phone has seen (the dot on the tab) and an unsent reply stay on this phone.
- **Agents.** Every agent on every host, each opening as a conversation you can read and answer, with any images, audio and video it shared. The round **+** at the bottom right starts a new one: Git (a branch and worktree of a repository you pick by typing part of its name, from a base branch) or Scratch (a fresh folder), the host when lasso drives more than one, the harness, model and thinking effort (and, for Oh My Pi, a model preset), and a prompt whose first line names the agent. It opens on the same choices as the full lasso's New agent in that browser, and lands you in the new agent's conversation, which shows it starting until its first message. Plan mode, extra CLI arguments, the advisor and file attachments are in the full lasso's creator.
- **Bots.** Your [bots](../web-ui/bots.md) and their conversations.
- **Settings.** Notifications and their kinds, appearance, **Text**, the lasso this phone is connected to, and **Switch to the advanced view**.

### Text

**Settings → Text** sets how the simple app shows what you read: the conversations and the Inbox's cards. Each control is a pair of − and + buttons, and the preview at the top of the section shows a reply set exactly as a conversation would set it.

| Setting | Default | Range |
| --- | --- | --- |
| Size | 17px | 14–24px |
| Weight | 400 | 300–700 |
| Line height | 1.60 | 1.3–2.1 |
| Letter spacing | 0 | −0.02–0.08em |
| Reading panel | 70% | off–100% |

Size moves everything in a conversation and on a card together, the tool rows, captions and labels around the prose included, so nothing is left behind at the old size. The reading panel is a wash of your theme's background behind a conversation; it only shows when the theme has a backdrop. **Reset to defaults** clears every field.

These settings are lasso's, not this phone's: every phone using the simple app gets them. They are separate from the full lasso's **Settings → Chat text**, so a size picked for a phone held at arm's length does not resize the chat on your desktop, and the other way round. The typeface is shared: **Settings → Typography → Chat** in the full lasso sets it for both.

Secondary text (captions, an agent's reasoning, a tool row's detail) is held to WCAG AA contrast, 4.5:1, against your theme's background and its cards. A theme whose muted colour already reaches that, lasso's own light and dark included, is shown as it is.

Simple or advanced is remembered per device, so choosing the full UI on a phone does not change your laptop. From the full UI, **Simple view** in the view menu (and in Settings → General) goes back. A desktop browser can open `/m` too: `⌘⇧M` in the full UI goes there, and `⌘⇧M` in the simple app comes back.

## Things to know

- **A subscription belongs to an origin.** A `ts.net` install and a Cloudflare-hostname install of the same lasso are two different apps; enabling push in both registers the phone twice and you get every notification twice. Pick one origin per device.
- **Notifications arrive off your network.** They come from Apple's or Google's push service, not from lasso, and the payload carries everything the phone needs to show them. Only *opening* one needs lasso to be reachable (on a tailnet origin, that means Tailscale connected).
- **Keep `lasso.db`.** The key that identifies your lasso to push services is generated once and stored in `~/.lasso/lasso.db`. If that database is lost or replaced, every device has to enable notifications again.

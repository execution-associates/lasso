---
title: On your phone
description: Put lasso on your phone's home screen over HTTPS and turn on push notifications for blocked agents.
order: 12
nav_title: Phone setup
---

lasso works as a phone app: added to the home screen it launches full screen from its own icon, with an input dial for the keys a touch keyboard lacks and a view picker (see [Terminal](../web-ui/terminal.md)). Its most useful trick on a phone is buzzing you when an agent is stuck waiting on you. That needs three things, in this order: an HTTPS origin, the home-screen install, and push enabled from inside the installed app.

## 1. Reach lasso over HTTPS

Service workers and the Push API only exist in a **secure context**: an `https://` origin, or `localhost` itself. Reached at a plain-HTTP address such as `http://myhost:8090` over a tailnet, a phone's browser doesn't merely refuse permission; the APIs are absent, and lasso's Settings says *"This browser can't do Web Push"*. A tailnet is private, but privacy isn't what the browser checks.

Two ways to get a real HTTPS origin without exposing lasso publicly:

- **Tailscale.** Keep lasso on loopback and let `tailscale serve` terminate TLS on your node's `*.ts.net` name. See [Over your tailnet](../deployment/tailnet.md).
- **Cloudflare Tunnel + Access.** Keep lasso on loopback and publish it on a hostname of yours behind Cloudflare Access. See [Cloudflare](../deployment/cloudflare.md).

HTTPS also helps two smaller things: Files-tab downloads, which some browsers block from a plain-HTTP page, and terminal copy, which otherwise falls back to a legacy clipboard method (see [Why HTTPS matters](../deployment/index.md#why-https-matters)). Uploads and dictation work either way.

## 2. Add it to the home screen

On **iPhone or iPad** (iOS 16.4 or later), open your HTTPS lasso URL in Safari, tap the Share button, and choose **Add to Home Screen**. iOS only offers push to a web app launched from the home screen, never to a Safari tab, so this step is not optional there.

On **Android** and desktop browsers, push works in an ordinary tab. Installing lasso as an app (Chrome's "Add to Home screen" or "Install app") is still nicer: it opens full screen from its own icon.

The home-screen app is a shortcut to your lasso, not an offline app. It caches nothing, so the machine running lasso has to be up and reachable to open it.

## 3. Turn on notifications

1. Launch lasso **from the home-screen icon** (on iOS, the Safari tab won't do).
2. Open **Settings → General → Notifications**.
3. Tick **Push notifications to this device**. The browser asks for permission; allow it.
4. Press **Send a test notification**. It appears once at least one device is registered, sends to every registered device, and reports how many took it, or why it couldn't send.

Settings explains the state it finds:

| What Settings says | What it means |
| --- | --- |
| "On iOS, notifications only work from a Home Screen web app..." | You are in a Safari tab. Add lasso to the home screen and open it from there. |
| "This browser can't do Web Push..." | No service worker or Push API: almost always a plain-HTTP origin. Use an HTTPS origin. |
| "Notifications are blocked for this site..." | Permission was denied earlier. Allow notifications for the site in the browser's (or iOS's) settings, then tick the box again. |

Below the checkbox, Settings lists every registered device with the outcome of its last push, so a device that has quietly stopped working shows `last push failed: <reason>` instead of looking healthy. Unticking the box on a device unregisters it.

## What you'll be notified about

Three things, on every registered device:

- **An agent that blocks**: stops mid-task waiting on a tool approval, a plan gate or a question, on any host lasso can reach. lasso watches for this in the background, but only while at least one device is registered.
- **An agent that asks for you** with `lasso notify "..."` or the `notify` MCP tool.
- **A bot that answers**, when that bot's **Notify me when it answers** is on.

Opening a notification lands lasso on the host the agent runs on, or in the bot's conversation.

To get your [bots](../web-ui/bots.md) as an app of their own, install the [Bots app](../web-ui/bots.md#the-bots-app) the same way from lasso's `/bots` page, then press the bell inside it. [Notifications](../concepts/notifications.md) explains exactly when one is sent and how delivery is reported.

## Things to know

- **A subscription belongs to an origin.** A `ts.net` install and a Cloudflare-hostname install of the same lasso are two different apps; enabling push in both registers the phone twice and you get every notification twice. Pick one origin per device.
- **Notifications arrive off your network.** They come from Apple's or Google's push service, not from lasso, and the payload carries everything the phone needs to show them. Only *opening* one needs lasso to be reachable (on a tailnet origin, that means Tailscale connected).
- **Keep `lasso.db`.** The key that identifies your lasso to push services is generated once and stored in `~/.lasso/lasso.db`. If that database is lost or replaced, every device has to enable notifications again.

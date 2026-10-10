---
title: Tailscale
description: Reach lasso from your own devices over a tailnet, either by binding to the tailscale address or through tailscale serve for real HTTPS.
order: 63
nav_title: Tailnet
---

If your devices are already on a [Tailscale](https://tailscale.com) tailnet, you can reach lasso without any public hostname. WireGuard encrypts and authenticates the traffic, and only devices on your tailnet can connect.

There are two ways to do it. Binding to the tailscale address is one flag, but gives you plain HTTP. `tailscale serve` takes one more command and gives you HTTPS, which you need for push notifications.

Either way, **every device on the tailnet can reach lasso**, with no login in front of the terminal, `/mcp` or `/cdp`. That is fine on a tailnet of your own devices. If the tailnet includes other people or machines you don't fully trust, add [`UI_AUTH`](#adding-a-login) or use [Cloudflare Access](./cloudflare.md) instead.

## Binding to your tailscale address

```bash
lasso serve -listen "$(tailscale ip -4):8090" -insecure-no-auth
```

lasso refuses a non-loopback bind unless you either set `UI_AUTH` or pass `-insecure-no-auth`. The flag is your statement that the interface is private. Never use it on a public address.

Then open `http://<machine>:8090/` from any tailnet device (the MagicDNS name works).

This is plain HTTP, so the browser does not treat it as a secure context:

- push notifications are impossible (Settings says *"This browser can't do Web Push"*),
- Files-tab downloads may be blocked by the browser,
- terminal copy uses a legacy clipboard fallback.

If you want any of those, use `tailscale serve` below.

## Real HTTPS with `tailscale serve`

Tailscale can issue a real certificate for your machine's MagicDNS name and terminate TLS for you. Enable **MagicDNS** and **HTTPS certificates** in the tailnet admin console, then keep lasso on loopback and let `tailscale serve` front it:

```bash
lasso serve -listen 127.0.0.1:8090
tailscale serve --bg --https=8090 http://127.0.0.1:8090
```

lasso is now at `https://<machine>.<tailnet>.ts.net:8090`. That origin has a publicly trusted certificate, so browsers treat it as secure and Apple's push service accepts it. Add *that* URL to your phone's home screen and enable notifications from it; see [On your phone](../getting-started/phone.md).

To turn it off:

```bash
tailscale serve --https=8090 off
```

Because lasso still binds to loopback, it needs neither `UI_AUTH` nor `-insecure-no-auth`. But the loopback check protects nothing here: `tailscale serve` hands every tailnet device a path to it. Treat this exactly like the plain tailnet bind for access purposes.

## Push and origins

A push subscription belongs to the **origin** it was made on. `https://<machine>.<tailnet>.ts.net:8090` and `https://lasso.example.com` are two different apps as far as the phone is concerned, even though they reach the same lasso. If you enable notifications in both, the phone is registered twice and gets every notification twice. Pick one origin per device.

Notifications still arrive when the phone is off the tailnet. They are delivered by the phone's push service, not by lasso, and the payload carries everything needed to show them. Only opening one needs the tailnet, since that loads the app.

## Adding a login

To put a password in front of the UI, set `UI_AUTH` in lasso's environment, never on the command line:

```bash
UI_AUTH='you:a-long-random-password' lasso serve -listen "$(tailscale ip -4):8090"
```

(With `UI_AUTH` set, `-insecure-no-auth` is not needed.) In a systemd unit, put it in an `EnvironmentFile`; see [systemd](./systemd.md#secrets-go-in-an-environment-file).

`UI_AUTH` covers the UI, the API, the terminals, `/cdp` and `/mcp`'s `browser_*` tools. It does **not** cover the rest of `/mcp`, which stays open so agent CLIs keep working without credentials. To gate `/mcp` too, set `MCP_OAUTH`; see [MCP OAuth](../mcp/oauth.md). Agent CLIs then authenticate with a bearer token or the same `UI_AUTH` credentials.

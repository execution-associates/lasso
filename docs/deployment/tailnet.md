---
title: Tailscale
description: Serve lasso on your tailnet over HTTPS with -tailnet, with every caller identified by the tailnet and only you let in by default.
order: 63
nav_title: Tailnet
---

If your devices are on a [Tailscale](https://tailscale.com) tailnet, lasso can serve itself there directly, with no public hostname. With `-tailnet`, lasso binds this node's tailnet addresses, serves HTTPS with the certificate tailscaled issues for the node's MagicDNS name, and asks tailscaled who is behind every connection. By default only you, the node's owner, get in.

## 1. Turn on MagicDNS and HTTPS certificates

In the tailnet admin console, on the **DNS** page, enable **MagicDNS** and **HTTPS Certificates**. lasso needs both: the certificate is issued for the node's MagicDNS name, and that name is the address you open.

## 2. Let lasso ask tailscaled for the certificate (Linux)

On Linux, tailscaled hands out certificates only to root and to its operator. Make the user lasso runs as the operator:

```bash
sudo tailscale set --operator=$USER
```

On macOS, lasso talks to the Tailscale app through its `tailscale` CLI.

## 3. Start lasso with `-tailnet`

```bash
lasso serve -tailnet
```

Under systemd, set `LASSO_TAILNET=1` in the unit's environment instead (see [systemd](./systemd.md)). `-listen` stays on `127.0.0.1:8090`, so lasso's own CLI and the agents on this machine keep using loopback. `-tailnet` adds HTTPS listeners on every tailnet address of the node, on the same port as `-listen` unless you pass `-tailnet-port` (env `LASSO_TAILNET_PORT`). Startup logs the address:

```text
tailnet:  https://myhost.example-tailnet.ts.net:8090 (callers identified by tailscaled's whois)
```

lasso exits at startup when it cannot serve the tailnet: with no MagicDNS name, no tailnet address, no certificate, or neither tailscaled's socket nor the `tailscale` CLI to ask. The message names the missing piece.

## 4. Open it

Open `https://<node>.<tailnet>.ts.net:8090` from any device on your tailnet. The certificate is publicly trusted, so browsers treat the page as a secure context: push notifications, Files-tab downloads and the clipboard all work. Use the name, not the `100.x` address, which the certificate does not cover.

## Who gets in

lasso asks tailscaled who owns the device on the other end of each connection. Only that answer counts: no forwarded header is read on this listener, so nothing on the path can claim to be someone else.

- **By default, only the node's owner.** The tailnet login that owns this machine gets in from any of its devices. Everyone else on the tailnet, including people a device is shared with and your own tagged servers, gets 403.
- **A tagged node has no owner**, so on a node tagged in the admin console nobody gets in until `LASSO_ALLOW` names them. Startup says so.
- **`LASSO_ALLOW` names who may in**, comma-separated, in lasso's environment. Setting it replaces the owner default, so list yourself:

  ```bash
  LASSO_ALLOW=you@example.com,partner@example.com,tag:ci
  ```

  A person is their tailnet login. A tagged device is matched by any of its tags (`tag:ci`) and is a **machine**: it reaches `/mcp`, `/herdr-mcp` and `/cdp` only. Prefix the entry with `ui:` (`ui:tag:ci`) to let it into the UI and `/api` as well. [Security](../security.md#lasso_allow) has the full rule.
- **The tailnet policy is the outer wall.** Grant only the devices that should reach lasso access to this node's port in your tailnet policy file, so the rest cannot open a connection at all. `LASSO_ALLOW` is the wall inside it.
- **Never forward lasso's port onto a machine lasso drives.** An agent machine joins as itself, through the tailnet, as a tagged machine or under its owner's login. See [Deployment](./index.md#a-machine-lasso-drives-is-not-a-client).

Agents on a tagged machine get the whole fleet once admitted. To narrow one to its own host, set `MCP_OAUTH` and give it a per-host lasso token; see [Agent scope](../mcp/agent-scope.md).

`UI_AUTH` basic auth is still supported, and a caller the tailnet identified never needs it.

## Your phone, push and the Mac app

Nothing more to set up. A phone with Tailscale connected opens the `ts.net` URL, adds it to the home screen and turns on push like any HTTPS origin (see [On your phone](../getting-started/phone.md)). The Mac app, given the same URL, sends no credentials: the tailnet identifies it.

## Already using `tailscale serve`?

The simplest move is to switch: turn the old forward off and serve on the same port.

```bash
tailscale serve --https=8090 off
lasso serve -tailnet
```

The URL is unchanged, so phones keep their home-screen app and their push subscription.

To keep `tailscale serve` in front instead, give it a loopback port of its own with `-listen-serve` (env `LASSO_LISTEN_SERVE`):

```bash
lasso serve -listen-serve 127.0.0.1:8092
tailscale serve --bg --https=8090 http://127.0.0.1:8092
```

On that port lasso trusts the `Tailscale-User-Login` header `tailscale serve` sets, so **point nothing else at it**. lasso refuses a non-loopback `-listen-serve`. A request with no user (a tagged device, or Funnel traffic) is refused, and the same owner default and `LASSO_ALLOW` apply. Never point `tailscale serve` at the `-listen` port: that hands every tailnet device the loopback listener's trust.

## Push and origins

A push subscription belongs to the **origin** it was made on. `https://<node>.<tailnet>.ts.net:8090` and `https://lasso.example.com` are two different apps as far as the phone is concerned, even though they reach the same lasso. If you enable notifications in both, the phone is registered twice and gets every notification twice. Pick one origin per device.

Notifications still arrive when the phone is off the tailnet. They are delivered by the phone's push service, not by lasso, and the payload carries everything needed to show them. Only opening one needs the tailnet, since that loads the app.

## The bare tailnet bind (deprecated)

`lasso serve -listen "$(tailscale ip -4):8090" -insecure-no-auth` still works, but it is deprecated: it is plain HTTP, so push cannot work, and every device on the tailnet gets in with no identity at all. Use `-tailnet`.

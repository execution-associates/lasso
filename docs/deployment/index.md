---
title: Deployment
description: How to run lasso so you can reach it from other devices without exposing a writable shell to the internet.
order: 60
nav_title: Overview
---

lasso listens on `127.0.0.1:8090` by default, which is fine for a browser on the same machine and useless for your phone. This section covers the ways to reach it from elsewhere, how to keep it running, and how to keep it up to date.

## Choosing a setup

There are two ways to reach lasso from your other devices. Both give you a real HTTPS origin, so the phone app and push notifications work, and both identify every caller before lasso answers.

| setup | flag | who can reach it | who gets in by default | good for |
| --- | --- | --- | --- | --- |
| **[Tailscale](./tailnet.md)** | `-tailnet` | devices on your tailnet | you, the node's owner | your own devices, with no public hostname |
| **[Cloudflare tunnel + Access](./cloudflare.md)** | `-listen-access` | anyone who passes your Access policy, from anywhere | whoever your Access policy admits | devices off your tailnet, claude.ai connectors, agents on other networks |
| **Local only** (default) | `-listen` | this machine | whoever runs on it | a laptop or desktop you sit at |

The two can run side by side on one lasso. A few questions settle it:

- **Are all your devices on a tailnet?** Use `-tailnet`. Nothing to sign in to, and nobody but you gets in until you name them in `LASSO_ALLOW`.
- **Do you need it from a device that is not on your tailnet**, or from a hosted MCP client such as a claude.ai connector? Use Cloudflare.
- **Do other people or agent machines need in?** Name them in `LASSO_ALLOW` (see [Security](../security.md#lasso_allow)), and for agents, consider per-host [scope](../mcp/agent-scope.md).

## The listeners

The left column of lasso is a **writable shell** on your machine. The file endpoints read and write any path as the user lasso runs as, `/mcp` lets a caller spawn agents, and `/cdp` and `/mcp`'s browser tools drive a real browser with whatever logins it holds. [Security](../security.md) has the full picture. So lasso never binds a public address, and each way in is a listener with a role that says who it trusts:

| listener | flag (env) | binds | trusts |
| --- | --- | --- | --- |
| local | `-listen` | `127.0.0.1:8090` by default | whoever connects; `UI_AUTH` and `MCP_OAUTH` apply when set |
| tailnet | `-tailnet` (`LASSO_TAILNET=1`), `-tailnet-port` | this node's tailnet addresses, over HTTPS | the person or tagged device tailscaled names for the connection |
| access | `-listen-access` (`LASSO_LISTEN_ACCESS`) | a loopback address that `cloudflared` targets | a request carrying a Cloudflare Access assertion that verifies |
| serve | `-listen-serve` (`LASSO_LISTEN_SERVE`) | a loopback address that `tailscale serve` targets | the user `tailscale serve` names |

The network listeners (tailnet, access, serve) fail closed: a request without the proof its front door adds gets 403. lasso refuses to start with a non-loopback `-listen-access` or `-listen-serve`.

lasso also refuses a non-loopback `-listen` unless one of these is true:

| condition | what it means |
| --- | --- |
| `UI_AUTH=user:pass` is set in the environment | Every route needs HTTP basic auth, except `/mcp` (open unless `MCP_OAUTH` is set), `/herdr-mcp`, `/cdp`, the bot webhooks and the OAuth handshake endpoints. |
| `-require-access-header` (deprecated) | Every request needs a Cloudflare Access identity. Use `-listen-access` instead; see [Cloudflare](./cloudflare.md#the-header-gate-deprecated). |
| `-insecure-no-auth` (deprecated) | You are binding to a private interface and accept that anything on that network can reach it, with no identity. Use `-tailnet` instead. |

Without one of them, lasso exits with:

```text
refusing to listen on non-loopback "100.64.0.5:8090" without auth — set UI_AUTH=user:pass, pass -require-access-header when Cloudflare Access fronts this hostname, or pass -insecure-no-auth to bind bare (only safe on a private interface like tailscale0)
```

## Other reverse proxies

lasso answers only to names it knows: IP addresses, `localhost`, this machine's hostname, its tailnet names, and the names in `LASSO_HOSTNAMES`. A request for any other name gets **421** (see [Security](../security.md#the-local-guard)). To put another reverse proxy (nginx, Caddy) in front of `-listen` under a hostname, add that hostname, comma-separated, with `*.example.com` for a whole domain:

```bash
LASSO_HOSTNAMES=lasso.internal.example.com
```

lasso cannot tell who is behind such a proxy: `-listen` trusts whoever connects, so the proxy's own access control is all that protects you. Prefer the two setups above, where lasso checks the caller itself.

## A machine lasso drives is not a client

**Never forward lasso's port onto a machine lasso drives**: no `ssh -R`, no tunnel, no socat from lasso's box to a host in its SSH config. On that machine, every agent lasso runs and every other process would reach the loopback listener's trust, and lasso could not tell them from you. An agent on another machine reaches lasso the way your phone does, through the tailnet or through Access, as a machine named in `LASSO_ALLOW`.

Your tailnet policy is the outer wall: a machine that has no business with lasso should not be able to open a connection to it at all.

## Why HTTPS matters

Several browser features only exist in a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts): an `https://` origin, or `localhost` / `127.0.0.1`. On a plain-HTTP address such as `http://myhost:8090`:

- **Push notifications don't work.** Service workers and the Push API are absent, and Settings says *"This browser can't do Web Push"*. Nothing lasso does can change this.
- **Files-tab downloads may not arrive.** A download is an ordinary link to the file, which lasso serves with `Content-Disposition: attachment`, but some browsers warn about or block downloads from a plain-HTTP page. Viewing files still works.
- **Terminal copy falls back** to a legacy clipboard write (`execCommand("copy")`), since the Clipboard API is absent.

Uploads, dictation, and everything else work over plain HTTP. Both `-tailnet` and Cloudflare serve HTTPS.

## Keeping it running and up to date

- [systemd](./systemd.md): run lasso as a `systemd --user` service, or use the built-in `lasso start` background mode.
- [Updating](./updating.md): `lasso update`, what it restarts, and updating herdr on your other hosts.

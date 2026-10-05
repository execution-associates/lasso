---
title: Deployment
description: How to run lasso so you can reach it from other devices without exposing a writable shell to the internet.
order: 60
nav_title: Overview
---

lasso listens on `127.0.0.1:8090` by default, which is fine for a browser on the same machine and useless for your phone. This section covers the ways to reach it from elsewhere, how to keep it running, and how to keep it up to date.

## The rule: never bind it publicly

The left column of lasso is a **writable shell** on your machine. The file endpoints read and write any path as the user lasso runs as, `/mcp` lets a caller spawn agents, and `/cdp` and `/browser-mcp` drive a real browser with whatever logins it holds. Anyone who reaches the port can do all of that. [Security](../security.md) has the full picture.

So never bind lasso to `0.0.0.0` or a public address. On a VPS that is the open internet.

lasso enforces part of this itself. On startup it refuses a non-loopback `-listen` address unless one of these is true:

| condition | what it means |
| --- | --- |
| `UI_AUTH=user:pass` is set in the environment | Every route needs HTTP basic auth, except `/mcp` (open unless `MCP_OAUTH` is set) and the OAuth handshake endpoints. |
| `-require-access-header` (or `LASSO_REQUIRE_ACCESS_HEADER=1`) | Every request needs a Cloudflare Access identity header. Only safe behind Cloudflare Access; see [Cloudflare](./cloudflare.md#requiring-the-access-identity-in-lasso). |
| `-insecure-no-auth` | You are binding to a private interface (such as your tailscale IP) and accept that anything on that network can reach it. |

Without one of them, lasso exits with:

```text
refusing to listen on non-loopback "100.64.0.5:8090" without auth — set UI_AUTH=user:pass, pass -require-access-header when Cloudflare Access fronts this hostname, or pass -insecure-no-auth to bind bare (only safe on a private interface like tailscale0)
```

The check looks only at the address lasso **binds**. A loopback lasso with something forwarding to it (a Cloudflare tunnel, `tailscale serve`, an SSH port forward, a reverse proxy) is exactly as exposed as whatever that forwarder exposes, and lasso cannot tell. The forwarder's own access control is what protects you in those setups.

## Choosing a setup

| setup | who can reach it | HTTPS | auth comes from | good for |
| --- | --- | --- | --- | --- |
| **Local only** (default) | this machine | not needed (loopback counts as secure) | nothing | a laptop or desktop you sit at |
| **[Cloudflare tunnel + Access](./cloudflare.md)** | anyone who passes your Access policy, from anywhere | yes | Cloudflare Access | the phone app, push, sharing across networks, claude.ai connectors |
| **[Tailnet bind](./tailnet.md#binding-to-your-tailscale-address)** | every device on your tailnet | no | the tailnet (plus optional `UI_AUTH`) | quick access from your own devices when push isn't needed |
| **[`tailscale serve`](./tailnet.md#real-https-with-tailscale-serve)** | every device on your tailnet | yes | the tailnet (plus optional `UI_AUTH`) | the phone app and push without Cloudflare |

A few questions settle it:

- **Do you want notifications on your phone?** Then you need HTTPS. Pick Cloudflare or `tailscale serve`. A plain-HTTP tailnet address cannot do Web Push at all.
- **Do you need to reach it from a device that is not on your tailnet**, or connect a hosted MCP client such as a claude.ai connector? Pick Cloudflare.
- **Is your tailnet shared** with people or machines you wouldn't hand a shell to? Either set `UI_AUTH` or use Cloudflare Access, which can restrict by identity.

## Why HTTPS matters

Several browser features only exist in a [secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts): an `https://` origin, or `localhost` / `127.0.0.1`. On a plain-HTTP address such as `http://myhost:8090`:

- **Push notifications don't work.** Service workers and the Push API are absent, and Settings says *"This browser can't do Web Push"*. Nothing lasso does can change this.
- **Files-tab downloads may not arrive.** A download is an ordinary link to the file, which lasso serves with `Content-Disposition: attachment`, but some browsers warn about or block downloads from a plain-HTTP page. Viewing files still works.
- **Terminal copy falls back** to a legacy clipboard write (`execCommand("copy")`), since the Clipboard API is absent.

Uploads, dictation, and everything else work over plain HTTP.

## Keeping it running and up to date

- [systemd](./systemd.md): run lasso as a `systemd --user` service, or use the built-in `lasso start` background mode.
- [Updating](./updating.md): `lasso update`, what it restarts, and updating herdr on your other hosts.

---
title: Cloudflare tunnel and Access
description: Publish lasso on an HTTPS hostname through a Cloudflare tunnel, gated by Cloudflare Access, with no open port.
order: 62
nav_title: Cloudflare
---

This is the recommended way to reach lasso from anywhere. lasso stays on loopback, `cloudflared` makes an outbound connection to Cloudflare, and Cloudflare serves your hostname over HTTPS. **Cloudflare Access** decides who gets through, so it is the thing guarding the writable shell, `/mcp`, and the shared browser.

Because the result is a real `https://` origin, the phone app, push notifications and Files-tab downloads all work.

## 1. Keep lasso on loopback

```bash
lasso serve -listen 127.0.0.1:8090
```

(Or `-listen 127.0.0.1:8090` in your [systemd unit](./systemd.md).) A loopback bind needs no `UI_AUTH` and no `-insecure-no-auth`, and no port is ever reachable from the network.

## 2. Create the tunnel

With [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) installed and logged in to your account:

```bash
cloudflared tunnel create lasso
cloudflared tunnel route dns lasso lasso.example.com
```

Then point the tunnel's ingress at lasso, in `~/.cloudflared/config.yml`:

```yaml
tunnel: <tunnel-id>
credentials-file: /home/you/.cloudflared/<tunnel-id>.json

ingress:
  - hostname: lasso.example.com
    service: http://127.0.0.1:8090
  - service: http_status:404
```

Run it with `cloudflared tunnel run lasso`, or install it as a service. Websockets (the terminals, the shared browser) pass through a tunnel without extra configuration.

**Do not stop here.** Until step 3 is done, anyone who knows the hostname has a shell on your machine.

## 3. Put Cloudflare Access in front

In Cloudflare Zero Trust, add a **self-hosted application** for `lasso.example.com` (the whole hostname, not a path) with a policy that allows only you, for example an *Allow* policy that includes `you@example.com`.

Access now asks for a login before any request reaches lasso, including the terminals' websocket upgrades and every API and MCP route. Open `https://lasso.example.com` and you should get Cloudflare's login page first.

## Agents on other machines: service tokens

An agent CLI on another machine can't do an interactive Access login. Give it a **service token** instead:

1. In Zero Trust, create a service token. You get a client ID and a client secret.
2. Add a policy to the lasso application with the action **Service Auth** that includes that token.
3. On the other machine, register lasso's MCP servers with the token's headers:

```bash
lasso connect -url https://lasso.example.com \
  -header 'CF-Access-Client-Id: <id>.access' \
  -header 'CF-Access-Client-Secret: <secret>'
```

`lasso connect` probes lasso through Access with those headers before it registers anything, and masks the secret in everything it prints. Claude Code and OpenCode only accept headers on their command line, so the values are briefly visible in that machine's process list while `lasso connect` runs them. See [Connecting agents](../getting-started/connect-agents.md).

## claude.ai and Claude Desktop connectors: Managed OAuth

Hosted MCP clients such as claude.ai custom connectors and Claude Desktop use OAuth. By default lasso implements no OAuth at all, and that is deliberate: it lets **Access** be the OAuth authorization server.

Turn on **Managed OAuth** on the lasso Access application. Access then handles the connector's dynamic client registration and the login against your existing Access policy, and issues the tokens. lasso just sees an authenticated request on `/mcp`. Add `https://lasso.example.com/mcp` as the connector URL.

Without Managed OAuth, the connector fails while registering, with a message like *"Couldn't register with lasso's sign-in service"*.

Setting `MCP_OAUTH` in lasso changes this: lasso becomes its own authorization server and expects bearer tokens **it** issued, so an Access-issued token is refused and connectors relying on Managed OAuth stop working. Use one or the other. [MCP OAuth](../mcp/oauth.md) explains `MCP_OAUTH` and when you want it (per-host agent credentials and [scope](../mcp/agent-scope.md)).

## Requiring the Access identity in lasso

Access in front is enough on its own. If you also want lasso to refuse anything that did not come through Access, it can check for the identity header Cloudflare adds:

```bash
lasso serve -listen 127.0.0.1:8090 \
  -require-access-header \
  -access-allowed-emails you@example.com,ops@example.com \
  -disable-self-update
```

| flag | env | effect |
| --- | --- | --- |
| `-require-access-header` | `LASSO_REQUIRE_ACCESS_HEADER=1` | Every request without a non-empty `Cf-Access-Authenticated-User-Email` header gets **403**. This covers every route: the UI, `/api/*`, the file endpoints, `/terminal/` and `/shell/` and their websockets, `/mcp`, `/cdp`, `/browser-mcp`, and the OAuth endpoints. It runs before `UI_AUTH` and the MCP OAuth check. |
| `-access-allowed-emails` | `LASSO_ACCESS_ALLOWED_EMAILS` | Comma-separated allowlist, compared case-insensitively. Empty means any identity Access vouched for. |
| `-disable-self-update` | `LASSO_DISABLE_SELF_UPDATE=1` | Turns off the in-app update action (`POST /api/self-update` answers 403 and the button is hidden), so an agent working through the UI cannot rebuild and restart its own front door. |

The `LASSO_REQUIRE_ACCESS_HEADER` and `LASSO_DISABLE_SELF_UPDATE` variables accept `1`, `true`, `yes` or `on`.

With `-require-access-header` set, lasso also accepts a **non-loopback bind without `UI_AUTH`**, since the edge identity is the authentication. Startup logs an `access:` line naming the gate and who it admits.

> **This header is only trustworthy behind an edge that strips it from clients.** `Cf-Access-Authenticated-User-Email` is just a request header; anyone can send it with `curl -H`. It means something only because Cloudflare, on a hostname Access protects, drops any copy the client sent and adds its own after verifying the login. If any path reaches lasso without going through that edge (a bare port, a second proxy that forwards client headers as-is, another process on the same machine), the gate can be bypassed. That is why lasso ignores the header completely unless the flag is set.

Things to know before turning it on:

- **lasso's own CLI stops working against it.** `lasso notify`, `lasso open`, `lasso mcp` and `lasso closeme` talk to lasso over loopback and send no Access header, so they get 403. The same is true of anything else on the machine that calls lasso directly. lasso's own chrome-devtools-mcp processes are the exception: they reach `/cdp` with an internal token that is checked ahead of this gate.
- **Service tokens carry no user email.** Cloudflare identifies a service-token request without adding the user-email header, so with the gate on, agents connecting through a service token are refused.

If either matters to you, leave the gate off and rely on Access at the edge.

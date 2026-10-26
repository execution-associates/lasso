---
title: Cloudflare tunnel and Access
description: Publish lasso on an HTTPS hostname through a Cloudflare tunnel, gated by Cloudflare Access, with lasso verifying every request.
order: 62
nav_title: Cloudflare
---

This is the way to reach lasso from anywhere, including devices that are not on your tailnet and hosted MCP clients such as claude.ai connectors. `cloudflared` makes an outbound connection to Cloudflare, Cloudflare serves your hostname over HTTPS, and **Cloudflare Access** decides who gets through. lasso then checks Access's signed assertion on every request, on a loopback port that only `cloudflared` targets.

Because the result is a real `https://` origin, the phone app, push notifications and Files-tab downloads all work.

## 1. Create the tunnel

With [cloudflared](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/) installed and logged in to your account:

```bash
cloudflared tunnel create lasso
cloudflared tunnel route dns lasso lasso.example.com
```

Point the tunnel's ingress at lasso's **access listener**, `127.0.0.1:8091`, in `~/.cloudflared/config.yml`:

```yaml
tunnel: <tunnel-id>
credentials-file: /home/you/.cloudflared/<tunnel-id>.json

ingress:
  - hostname: lasso.example.com
    service: http://127.0.0.1:8091
  - service: http_status:404
```

Run it with `cloudflared tunnel run lasso`, or install it as a service. Websockets (the terminals, the shared browser) pass through a tunnel without extra configuration.

Leave lasso's own `-listen` (`127.0.0.1:8090`) out of the tunnel. That listener trusts whoever connects to it, and the access listener exists so the tunnel never has to point there.

## 2. Put Cloudflare Access in front

In Cloudflare Zero Trust, add a **self-hosted application**:

- **Domain:** `lasso.example.com`, the whole hostname, not a path.
- **Policy:** an *Allow* policy that includes only you, for example the email `you@example.com`.
- **Session duration:** up to **1 month**. Each time the session ends, the browser and the phone app have to sign in again.
- **Managed OAuth:** on. It is what lets claude.ai and Claude Desktop connectors sign in (step 6).

With the application saved, Access asks for a login before any request reaches lasso, including the terminals' websocket upgrades and every API and MCP route.

## 3. Copy the application's Audience tag

Open the application's overview and copy its **Application Audience (AUD) Tag**. You also need your team name: the `<team>` in `<team>.cloudflareaccess.com`, shown in Zero Trust's settings.

## 4. Start lasso with the access listener

```bash
LASSO_ACCESS_TEAM=<team> LASSO_ACCESS_AUD=<aud tag> \
  lasso serve -listen-access 127.0.0.1:8091
```

Under systemd, keep the three settings in the unit's environment, in its `EnvironmentFile` or a drop-in:

```ini
# ~/.config/systemd/user/lasso.service.d/30-access.conf
[Service]
Environment=LASSO_LISTEN_ACCESS=127.0.0.1:8091
Environment=LASSO_ACCESS_TEAM=<team>
Environment=LASSO_ACCESS_AUD=<aud tag>
```

Then `systemctl --user daemon-reload && systemctl --user restart lasso`. Startup logs the check it runs:

```text
access:   127.0.0.1:8091 verifies every request against https://<team>.cloudflareaccess.com (aud <aud tag>)
```

`LASSO_ACCESS_TEAM` takes the team name, `<team>.cloudflareaccess.com` or the full `https://` URL. `LASSO_ACCESS_AUD` takes several tags, comma-separated, when more than one Access application fronts the same lasso. lasso refuses to start when `-listen-access` is set without both, or names a non-loopback address.

Open `https://lasso.example.com`. You get Cloudflare's login page, then lasso.

### lasso verifies every request

On the access listener, every request must carry a `Cf-Access-Jwt-Assertion` that lasso verifies itself: signed with one of your team's current keys (RS256), issued by your team, for this application's Audience tag, and unexpired. A request without one, or with one that fails, gets 403 before anything else looks at it. So a second tunnel, a hostname with no Access application, or a local process that finds the port all fail closed.

- **People** (the assertion names an email) get everything: the UI, the terminals, the API and the MCP servers.
- **Service tokens** are machines: they reach `/mcp`, `/herdr-mcp` and `/cdp` only.
- **`LASSO_ALLOW`** narrows further, when set: only the emails and `service:<client id>` entries it lists get in, and `ui:service:<client id>` lets a service token into the UI too. Unset, whoever your Access policy admits gets in. See [Security](../security.md#lasso_allow).

A verified caller needs no `UI_AUTH` and no `MCP_OAUTH` credential of its own. The loopback `-listen` is untouched: lasso's own CLI and the agents on this machine keep using `127.0.0.1:8090` as before.

## 5. Agents on other machines: service tokens

An agent CLI on another machine can't do an interactive Access login. Give it a **service token**:

1. In Zero Trust, create a service token. You get a client ID and a client secret.
2. Add a policy to the lasso application with the action **Service Auth** that includes that token.
3. On the other machine, register lasso's MCP servers with the token's headers:

```bash
lasso connect -url https://lasso.example.com \
  -header 'CF-Access-Client-Id: <id>.access' \
  -header 'CF-Access-Client-Secret: <secret>'
```

`lasso connect` probes lasso through Access with those headers before it registers anything, and masks the secret in everything it prints. Claude Code and OpenCode only accept headers on their command line, so the values are briefly visible in that machine's process list while `lasso connect` runs them. See [Connecting agents](../getting-started/connect-agents.md).

A service token alone sees the whole fleet. To hold that machine to its own host, set `MCP_OAUTH`, mint it a per-host lasso token, and send that token as `X-Lasso-Token` beside the service token:

```bash
lasso connect -url https://lasso.example.com \
  -header 'CF-Access-Client-Id: <id>.access' \
  -header 'CF-Access-Client-Secret: <secret>' \
  -header 'X-Lasso-Token: <lasso token>'
```

The service token decides *whether* the machine reaches lasso; the lasso token decides *what it may see* once inside. See [Agent scope](../mcp/agent-scope.md#installing-on-a-host).

## 6. claude.ai and Claude Desktop connectors: Managed OAuth

Hosted MCP clients such as claude.ai custom connectors and Claude Desktop sign in with OAuth. With **Managed OAuth** on the Access application, Access is their authorization server: it handles the connector's registration and the login against your Access policy, and issues the tokens. lasso verifies the Access assertion on each request, sees you, and ignores Access's bearer token. Add `https://lasso.example.com/mcp` as the connector URL.

This works whether or not `MCP_OAUTH` is set. Without Managed OAuth, the connector fails while registering, with a message like *"Couldn't register with lasso's sign-in service"*. A connector carries no host, so it sees every host lasso can reach. [MCP OAuth](../mcp/oauth.md) covers lasso's own tokens.

## 7. Webhooks and Talk from outside (only if you need them)

A [bot job's webhook](../web-ui/bots.md#jobs) carries its own key in the URL, but a sender on the internet (GitHub, say) cannot sign in to Access. Skip this step unless webhooks come from outside your devices.

Add a second self-hosted Access application for `lasso.example.com/hooks/bots/` with a single **Bypass** policy that includes everyone. The more specific path wins, so only webhooks skip the login. A bypassed request carries no Access assertion, and the access listener lets `/hooks/bots/`, `/bot-channel/` and `/talk/` through without one: each checks its own key, and nothing else on the listener is reachable that way.

A bot's [Talk](../web-ui/bots.md#talk) door is the same case: a chat app on a client's phone cannot sign in to Access, and the bot's Talk key is its credential. Bypass `lasso.example.com/talk/` the same way, or leave it behind Access and give each phone an Access service token as custom headers (`CF-Access-Client-Id`, `CF-Access-Client-Secret`) where its app allows them.

## Turning off the in-app update

`-disable-self-update` (env `LASSO_DISABLE_SELF_UPDATE=1`) turns off the in-app update action: `POST /api/self-update` answers 403 and the button is hidden, so an agent working through the UI cannot rebuild and restart its own front door.

## The header gate (deprecated)

`-require-access-header` (env `LASSO_REQUIRE_ACCESS_HEADER=1`), with `-access-allowed-emails`, still works but is deprecated. It applies to the `-listen` listener itself, so it also refuses lasso's own CLI and every other process on the machine. When `LASSO_ACCESS_TEAM` and `LASSO_ACCESS_AUD` are set it verifies the `Cf-Access-Jwt-Assertion`; without them it only checks that a `Cf-Access-Authenticated-User-Email` header is present, which anything that reaches the port can send. Move to `-listen-access` and point the tunnel at it.

---
title: OAuth
description: Gate /mcp in lasso itself with MCP_OAUTH, the OAuth flows it supports, and how to connect claude.ai and Claude Desktop either through lasso's OAuth or Cloudflare Access.
order: 44
nav_title: OAuth
---

By default `/mcp` is unauthenticated, which is fine on loopback, on a private tailnet, or behind an edge gate like Cloudflare Access. Setting **`MCP_OAUTH`** turns lasso into a small OAuth 2.1 authorization server for its own `/mcp`, so remote MCP clients can authenticate to lasso directly. It is also what makes [per-host credentials and scope](./agent-scope.md) take effect.

## Enabling it

```bash
MCP_OAUTH='lasso-mcp:<a long random secret>' lasso serve
```

The value is `client_id:client_secret`, split on the first colon, so a base64 secret is fine. It is read from the **environment only**, never a flag, so it does not show up in `ps`. Optionally:

| variable | effect |
| --- | --- |
| `MCP_OAUTH` | `client_id:client_secret` of the pre-registered client. Unset: `/mcp` is open and every OAuth route answers 404. |
| `MCP_OAUTH_REDIRECT_URIS` | Comma-separated callback URLs. When set, the pre-registered client may only redirect to exactly these. |

Under systemd, put it in a `0600` environment file rather than the unit:

```ini
# ~/.config/systemd/user/lasso.service.d/20-mcp-oauth.conf
[Service]
EnvironmentFile=%h/.config/lasso/mcp-oauth.env
```

```bash
# ~/.config/lasso/mcp-oauth.env  (chmod 600)
MCP_OAUTH=lasso-mcp:<secret>
```

Then `systemctl --user daemon-reload && systemctl --user restart lasso`. See [systemd](../deployment/systemd.md). At startup lasso logs one line saying whether OAuth is on, with the client id and the redirect rule:

```text
mcp:      OAuth on — client_id=lasso-mcp, grants=client_credentials+authorization_code, redirects: any https/loopback redirect (shown on the consent screen)
```

To check it on loopback:

```bash
curl -s -u "$MCP_OAUTH" -d grant_type=client_credentials \
  http://127.0.0.1:8090/oauth/token
```

## What `/mcp` accepts

With `MCP_OAUTH` set, a request to `/mcp` passes with **either**:

- a valid **bearer token** issued by lasso, or
- the **`UI_AUTH` basic credentials**, when `UI_AUTH` is set. This lets the lasso CLI, curl, and anything already holding `UI_AUTH` keep working without an OAuth dance.

Anything else gets **401** with a `WWW-Authenticate: Bearer` challenge pointing at `/.well-known/oauth-protected-resource`, which is how an OAuth-capable MCP client discovers where to sign in. An MCP session is pinned to the client id its token resolved to: a later request on that session with a different client's token is refused.

The `browser_*` tools and `/cdp` accept the same credentials, with one addition: a per-host token must include lasso's own machine in its reach. See [Browser tools](./browser.md#authentication).

## Grants

| grant | for | who may use it |
| --- | --- | --- |
| `client_credentials` | machines: scripts, CLIs, agent sessions | The `MCP_OAUTH` client, and per-host clients made with `lasso mcp-client add`. Never a client that registered itself. No refresh token is issued; the client mints another with its secret. |
| `authorization_code` with PKCE (S256) | people: claude.ai and Claude Desktop connectors, other interactive clients | Any client, including self-registered ones, after a human approves the consent screen. |
| `refresh_token` | renewing an authorization-code session | The client the token was issued to. Each refresh **rotates**: the presented refresh token is consumed and a new pair is issued. |

Both kinds exist because claude.ai and Claude Desktop custom connectors cannot use `client_credentials`: they require the authorization-code flow with per-connection user consent. Their "OAuth Client ID / Client Secret" fields are a pre-registered client for that flow.

| lifetime | |
| --- | --- |
| access token | 1 hour |
| refresh token | 30 days |
| authorization code | 5 minutes, single use |

Tokens from `lasso mcp-client token` are the exception: they default to never expiring (see [Agent scope](./agent-scope.md#installing-on-a-host)).

## Routes

| route | gate | purpose |
| --- | --- | --- |
| `/.well-known/oauth-protected-resource` | open | RFC 9728 resource metadata: names lasso as the authorization server |
| `/.well-known/oauth-authorization-server` | open | RFC 8414 server metadata: endpoints, grants, `S256`, scope `mcp` |
| `/oauth/register` | open | Dynamic Client Registration (RFC 7591) |
| `/oauth/token` | open | the token endpoint for all three grants |
| `/oauth/authorize` | **`UI_AUTH` / Access** | the consent screen; issues authorization codes |

The metadata, registration and token endpoints are the credential-less half of the handshake, so they sit outside `UI_AUTH`. All of them still sit behind Cloudflare Access when [`-require-access-header`](../deployment/cloudflare.md) is set. With `MCP_OAUTH` unset, every one of them answers 404.

## Registration and consent

**Registration is open**, as the MCP spec expects: any client may register a name and its redirect URIs. Redirect URIs must be `https`, or `http` on a loopback host. A client that registers with `token_endpoint_auth_method: "none"` is public and proves itself with PKCE alone; any other gets a generated secret.

That is safe because **`/oauth/authorize` is gated** by `UI_AUTH`, and by Access when it fronts lasso. A registered client gets no token until a human passes that door and clicks **Approve** on the consent screen, which shows the client's name and the exact URL the code will be sent to. Only approve a redirect target you recognize.

**Redirect URI rules:**

- A self-registered client may only redirect to the URIs it registered, matched exactly.
- The pre-registered `MCP_OAUTH` client accepts any `https` callback, or `http` on loopback, because a connector's callback is not known when you mint the secret. The consent screen shows the target.
- Set `MCP_OAUTH_REDIRECT_URIS` to lock the pre-registered client to an exact allowlist instead.

## Storage

Authorization codes, access tokens, refresh tokens and registered clients' secrets are stored in `lasso.db` as **SHA-256 hashes**, so a copy of the database yields no usable credential, and connectors survive lasso restarts and self-updates. Expired codes and tokens are purged as the token endpoint is used.

## claude.ai and Claude Desktop connectors

There are two ways to connect a claude.ai or Claude Desktop custom connector to `https://lasso.example.com/mcp`. Pick one: they are mutually exclusive.

### Through Cloudflare Access Managed OAuth (`MCP_OAUTH` unset)

When Cloudflare Access fronts lasso's hostname, enable **Managed OAuth** on the Access application. Access then acts as the OAuth 2.1 authorization server: it handles registration and the authorization-code flow, runs the login against your existing Access policy, and issues the tokens. lasso sees an authenticated Access session and needs no OAuth of its own.

This only works while `MCP_OAUTH` is **unset**. lasso keeps every OAuth route at 404 in that state precisely so that clients find Access's metadata instead of lasso's. Without Managed OAuth, the connector fails at registration ("Couldn't register with lasso's sign-in service"). Managed OAuth is a setting on the Access application, not a tunnel change. See [Cloudflare deployment](../deployment/cloudflare.md).

### Through lasso's own OAuth (`MCP_OAUTH` set)

1. Set `MCP_OAUTH` as above and restart lasso.
2. In claude.ai, add a custom connector with the URL `https://lasso.example.com/mcp`.
3. Under **Advanced settings**, enter the `MCP_OAUTH` client id and secret as the OAuth Client ID and Client Secret. (Leaving them empty also works: the connector registers itself through `/oauth/register`.)
4. Connect. Your browser lands on lasso's consent screen (behind `UI_AUTH` or Access), you check the redirect target and click **Approve**, and the connector gets its tokens.

Once `MCP_OAUTH` is set, `/mcp` demands a bearer token lasso issued. A connector holding Access's token instead is refused, so a connector set up through Access Managed OAuth stops working. With lasso's own OAuth, the connector's requests to `/mcp` and the OAuth endpoints must reach lasso without being stopped by an Access login; the consent screen is the only step that happens in your browser.

Connector tokens identify no host, so a connector sees every host lasso can address. Containment applies to [per-host credentials](./agent-scope.md).

## Claude Code and other machine clients

A client that can only carry a header takes a bearer token. Mint a long-lived one for a per-host client:

```bash
lasso mcp-client add --host myhost
lasso mcp-client token <client_id>
claude mcp add --transport http --scope user lasso https://lasso.example.com/mcp \
  --header "Authorization: Bearer <token>"
```

A client that can run the `client_credentials` grant posts its id and secret (basic auth or form fields) to `/oauth/token` and gets an access token valid for an hour. The lasso CLI reads a token from `LASSO_MCP_TOKEN` (see [Shell commands](./cli.md#finding-the-server)).

[Agent scope](./agent-scope.md) covers per-host clients, what they can see, and how to revoke them.

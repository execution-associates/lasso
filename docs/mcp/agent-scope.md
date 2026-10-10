---
title: Agent scope
description: Bound which hosts' agents an MCP caller can see and manage, with per-host credentials and host groups.
order: 45
nav_title: Agent scope
---

This is the operator runbook for per-host MCP credentials: who an agent can see, and how to provision it.

## The model in one paragraph

Two bounds decide which agents an MCP caller can see and manage, and both apply. **What lasso can address** is the local box plus the concrete aliases in the SSH config lasso reads. Membership comes from that config, never from lasso's agent records, so a host whose alias was removed stops being addressable instead of resolving to a target no call can reach. **What this caller may address** is per credential: one OAuth client per host, with the host carried in every token, so a caller's host is derived from its credential and cannot be asserted by the caller. Scope is `self` (that host only, the default) or `fleet` (everything lasso can address). **Groups** add reach to named sets of *other* hosts on top of `self`, the middle ground between those two ends.

MCP itself offers nothing here: a client's `clientInfo` is a self-asserted name and version, and no header identifies the caller. That is why identity comes from a credential rather than a tool argument.

## Prerequisite: `MCP_OAUTH` must be set

**Without it none of this applies.** While `MCP_OAUTH` is unset, `/mcp` is open, every caller is unidentified, and an unidentified caller is fleet-scoped. Provisioned per-host credentials are inert, and lasso logs a startup warning when that combination exists. The same is true of a caller using the `UI_AUTH` basic credentials on `/mcp`: it carries no host.

Set `MCP_OAUTH=client_id:client_secret` in lasso's environment (never a flag) and restart; [OAuth](./oauth.md#enabling-it) shows how under systemd. Verify on loopback, which sidesteps any edge gate:

```bash
journalctl --user -u lasso -n 20 | grep 'mcp:'      # "OAuth on — client_id=..., ..."
curl -s -u "$MCP_OAUTH" -d grant_type=client_credentials \
  http://127.0.0.1:8090/oauth/token | jq -r .access_token
```

To roll back, unset `MCP_OAUTH` and restart.

### Trade-off: this displaces Access Managed OAuth for connectors

While `MCP_OAUTH` is unset the origin implements no OAuth at all, which is what lets **Cloudflare Access Managed OAuth** act as the authorization server for claude.ai and Claude Desktop connectors: they authenticate to Access, and lasso just sees an authenticated session. Once `MCP_OAUTH` is set, lasso demands a bearer token *it* issued, and a connector's token is Access's, so that connector stops working. A verified Access identity is not accepted as a caller on `/mcp`. Either connect the connector through lasso's own OAuth instead (see [OAuth](./oauth.md#claudeai-and-claude-desktop-connectors)) or go without it.

## Provisioning a host

```bash
lasso mcp-client add --host myhost              # scope self: that host only
lasso mcp-client add --host local --fleet       # the lasso host: whole fleet
lasso mcp-client add --host myhost --name ci    # a label for the listing
lasso mcp-client list                           # incl. how many tokens each holds
lasso mcp-client token <client_id> [--ttl 90d]  # bearer token; default no expiry
lasso mcp-client rm <client_id>                 # also drops its outstanding tokens
```

`--host` takes `local` (the box lasso runs on) or an SSH-config alias exactly as `list_hosts` shows it; a host lasso cannot address is refused up front. The secret prints once and is stored hashed. Only these clients and the `MCP_OAUTH` client may use the `client_credentials` grant. A client that registered itself through open Dynamic Client Registration may not, since that path takes no human approval.

`lasso mcp-client` writes to `lasso.db` directly, so it works whether or not the server is running.

## Groups: reach between hosts

`self` and `fleet` are the two ends. A **group** is the middle: a named set of hosts whose members may see and manage each other's agents, plus **directed grants** between groups for when reach should run one way only.

The rules, in the order they apply:

- **Mutual inside a group.** Every host in a group's member closure may address every other host in that closure. Symmetric, because "these boxes work together" is symmetric.
- **Directed between groups.** `grant A B` lets A's hosts reach B's hosts and *not* the reverse, and it is **not transitive**: A→B plus B→C gives A nothing in C. One hop, always.
- **Members are hosts** (SSH aliases, or the literal `local`), never client credentials. Re-key a host (`mcp-client rm` + `add`) and its membership is untouched, which is why membership is not stored on the client.
- **Additive on top of `self`.** A self-scoped credential keeps its own host and gains whatever its host's groups add. `fleet` and unidentified callers are unchanged; they already reach everything.
- **Reach is always intersected with what lasso can address**, so a member whose SSH alias has since been removed goes inert rather than becoming a target no call could reach. The same goes for a dangling reference to a deleted group.

```bash
lasso mcp-group add <name>
lasso mcp-group rm <name>            # cascades: its members, its grants, and refs to it
lasso mcp-group list                 # groups, members (tree), grants
lasso mcp-group add-member <group> <host|@group>...
lasso mcp-group rm-member <group> <host|@group>
lasso mcp-group grant  <from-group> <to-group>   # directed
lasso mcp-group revoke <from-group> <to-group>
lasso mcp-group reach <host>         # effective reach, and why each host is reachable
```

A bare name is a host; the `@` sigil marks a subgroup reference. That is CLI syntax only: a host and a group may share a name and stay distinct. Host members are checked against the addressable set when added.

Worked example: two boxes, `build-a` and `build-b`, work as one stack.

```bash
lasso mcp-group add build-stack
lasso mcp-group add-member build-stack build-a build-b
```

`build-a` and `build-b` now list and manage each other's agents with their existing self-scoped credentials. The lasso host, provisioned `--fleet`, saw both before and still does. **Nobody in build-stack sees the lasso host**: reach is mutual only among members, and it is not one. A group is not a back door to the fleet-scoped host. To give one group one-way reach into another:

```bash
lasso mcp-group add ci
lasso mcp-group grant ci build-stack   # ci drives build-stack; build-stack cannot drive ci
```

**Nesting is the sharp edge.** Nest `@H` inside `G` and every host in H becomes mutual with *all* of G's closure, not just with G's direct members: nesting merges reach, it does not layer it. If that is not what you want, don't nest; make a grant instead. `lasso mcp-group reach <host>` prints the effective set and the reason for each entry. Run it after any structural edit.

Group edits apply on the caller's **next tool call**. Reach is resolved per request when the token is verified, so there is no token to re-mint, no session to restart, and nothing to change on the affected hosts. That also means `rm-member` revokes immediately.

One wrinkle, inherited from credential hosts: `local` is the literal name of the box lasso runs on, and if that box also has an SSH alias pointing at itself, the alias is a **distinct member**. Adding one does not add the other. Use the name the credential uses (`mcp-client list` shows it).

## Defaults that follow the credential

With a per-host credential, tools that take an optional `host` default to the credential's host, not to lasso's box. `list_hosts` shows only the hosts the credential may address. `whoami` searches only the credential's host, which also means a pane id that exists on several hosts still resolves. `close_agent` with no `host` searches every host the caller may reach. See [Tools reference](./tools.md#how-host-and-scope-work).

## `/herdr-mcp` follows the same scope

[`/herdr-mcp`](./herdr-mcp.md) takes `/mcp`'s credentials and applies the same host check to every call: with no `host` a call lands on the credential's own host, and a host outside its reach is refused with the same explanation `/mcp` gives. Its attached-client methods (`popup_close`, `server_live_handoff`, ...) always run on `local`, so they need a caller whose reach includes lasso's machine.

## The browsers follow the same scope

The [browser tools](./tools.md#browser) on `/mcp` (the `browser_*` tools, `list_browsers`, `open_browser_tab` and the rest) and the raw CDP endpoint they hand out, [`/cdp`](./browser.md#raw-cdp-for-playwright-and-other-clients), drive browsers running on **lasso's own machine**, so all of them require a caller whose reach includes `local`. Plugin tools have the same requirement. A `self`-scoped credential for another host gets a tool error, and the same bearer token presented to `/cdp` directly is refused with 403: the check is on the endpoint, not only in the tool. Fleet scope, or a group or grant that brings in `local`, opens it.

The browser tools need nothing beyond the credential a host already uses for `/mcp`. With `MCP_OAUTH` unset the reach check does not apply: the `browser_*` tools and `/cdp` are open, or need the `UI_AUTH` basic credentials when that is set. That last case is stricter than the rest of `/mcp`, which stays open under `UI_AUTH` alone, because the `browser_*` tools are a way into `/cdp` and take `/cdp`'s rule (see [Browser tools](./browser.md#authentication)). lasso's own chrome-devtools-mcp processes reach `/cdp` with an internal per-process token, not with the caller's credential. The caller's credential and reach are checked on every browser tool call, which is the only door those processes open.

Reaching `local` here is a bigger grant than it reads: a browser acts with whatever it is logged into, and `localhost` inside a browser lasso launches is lasso's machine. Give it only to hosts you would let browse as you from there.

## Installing on a host

When Cloudflare Access fronts lasso, a remote box needs **two** credentials, and the split is the useful part: an Access service token decides *whether* that box may talk to lasso at all; the lasso credential decides *what it may see* once inside.

```bash
# on lasso's machine: provision the host, then mint it a token
lasso mcp-client add --host myhost
lasso mcp-client token <client_id>          # no --ttl => never expires

# on the box (keep the token in that box's own secret store)
claude mcp add --transport http --scope user lasso \
  https://lasso.example.com/mcp \
  --header "Authorization: Bearer $LASSO_MCP_TOKEN" \
  --header "CF-Access-Client-Id: $LASSO_ACCESS_ID" \
  --header "CF-Access-Client-Secret: $LASSO_ACCESS_SECRET"
```

`lasso connect -url https://lasso.example.com -token <token> -header "CF-Access-Client-Id: ..." -header "CF-Access-Client-Secret: ..."` registers both servers with every agent CLI on the box in one go (see [Connect your agents](../getting-started/connect-agents.md)). Exporting `LASSO_MCP_TOKEN` and `LASSO_URL` on the box also lets [`lasso mcp`, `lasso notify` and `lasso open`](./cli.md) authenticate.

`token` defaults to **no expiry**, because it sits unattended in a host's config, where a rolling expiry is an outage on a timer rather than a security win. Pass `--ttl 90d` (or `2w`, `12h`, `30m`) for a dated one. To revoke: `lasso mcp-client rm <client_id>` drops the client and every token issued to it; then `add` a replacement. One client is one host, so the blast radius is exactly the host you are re-keying. `lasso mcp-client list` shows how many tokens each host holds and how many never expire.

The client id and secret still work for `client_credentials` where a caller can run a grant; the minted token is for clients that can only carry a header.

Sanity check from that box: `list_agents` with no `host` should return **its own** host, and naming another host should be refused with an explanation of the credential's reach.

## Known gaps

1. **Group membership is hand-maintained, not derived.** Groups close the gap between `self` and `fleet`: a middle-tier box with several SSH aliases of its own, too narrow at `self` and too broad at `fleet`, gets exactly the hosts you put in its group. But you have to enumerate them. Nothing reads that box's SSH config and turns it into a group, so the two drift apart silently as its config changes. Auditing is manual (`lasso mcp-group reach <host>`), and deriving membership automatically runs straight into gap 2.
2. **Alias names are not identities.** The same alias can resolve to *different machines* from two boxes' SSH configs, and *one machine* can carry different names on each. Name-based matching would hand a credential the wrong machine in the first case and miss the match in the second. Any derivation from a remote SSH config would have to resolve both sides with `ssh -G`, match on HostName, and have a human confirm the mapping.

## What this is not

Not a sandbox. A fleet-scoped agent is one message away from proxying for a contained one, and a secret on a box can be exfiltrated from it. Where a trust zone is a real boundary, enforce it at the network (block that box's path to lasso, for example with Access policy or by binding lasso to loopback) and treat credential scope as defence in depth.

---
title: Security model
description: What anyone who can reach lasso can do, which gates exist, and how to deploy it safely.
order: 90
nav_title: Security
---

lasso is built to give you, and the agents you run, full control of your machines from a browser. The same power is available to anyone else who can reach it. This page lays out what is exposed, what guards each part, and the setups that are safe.

The short version: **whoever lasso lets in can act as you on every machine lasso can reach.** lasso runs no login of its own. Keep it on loopback, and reach it from elsewhere through a front door that proves who is calling, your tailnet (`-tailnet`) or Cloudflare Access (`-listen-access`), which lasso checks on every request.

## What reaching lasso gives you

| surface | what it can do |
| --- | --- |
| **The terminal** (`/terminal/`) | A writable shell on lasso's machine, and on any SSH host the tab switches to. Type anything you could type yourself. |
| **The file endpoints** (`/api/file`, `/api/files`, `/api/file-write`, `/api/file-rename`, `/api/file-delete`, `/api/file-upload`) | Read, write, rename, delete and upload **any absolute path** the lasso user can access, on lasso's machine or on any host lasso can reach over SSH (a request can name the host). |
| **`/mcp`** | Create and close agents on any reachable host, list repositories and branches, push notifications to your phone, open files in your sidebar, manage the shared browsers. Enabled plugins add their own tools. |
| **`/herdr-mcp`** | herdr's whole socket API on any reachable host: split and close panes, type into any terminal (`pane_send_text`, `pane_send_keys`), start and prompt agents, run herdr plugin actions. Typing into a terminal is running commands as you. |
| **`/cdp` and `/mcp`'s `browser_*` tools** | Full control of the shared Chromiums: read every open page, type into it, navigate it, run scripts in it. That includes **every site the browsers are logged into**. |
| **The rest of `/api/*`** | Everything the UI does: switching hosts, updating herdr on remote hosts, changing themes across the fleet, managing plugins. |

The SSH reach matters. lasso uses your `~/.ssh/config` and keys, so a lasso that can reach ten hosts hands all ten to whoever reaches lasso.

## The gates

### The local guard

A lasso on loopback trusts whoever connects, which leaves one way in from outside: a web page in **your own browser**. It has two roads, and the local guard closes both on every listener, ahead of every credential check.

- **DNS rebinding.** A page on `evil.example` re-resolves its own name to `127.0.0.1` and then talks to lasso as a same-origin page. The one thing that gives it away is the `Host` header, which still names `evil.example`. lasso answers only to IP addresses, `localhost` and `*.localhost`, this machine's hostname, its tailnet names, and the names listed in `LASSO_HOSTNAMES` (comma-separated, `*.example.com` for a domain). Any other `Host` gets **421 Misdirected Request**. The access listener skips this check: the Access assertion it verifies already proves the request came through Cloudflare, so the public hostname needs no listing.
- **Cross-site requests.** A page that stays on its own origin can still fire a form `POST` at lasso, or open a websocket to a terminal, and neither is stopped by CORS. So a request that can change something (any method but `GET`, `HEAD` and `OPTIONS`, and any websocket upgrade) must come from lasso's own origin. One whose `Origin` (or, without one, `Sec-Fetch-Site`) names another site gets **403**. The OAuth registration and token endpoints, the bot webhooks and the bot channel are exempt, since their callers are cross-origin by design and carry their own credentials.

Agents, the CLI, curl and the Mac app send neither header and a `Host` lasso knows, so the guard never touches them.

### Listener roles and the caller check

Each listener has a role that says which front door is in front of it, and lasso checks that door's proof itself:

| listener | flag | proof | the caller is |
| --- | --- | --- | --- |
| local | `-listen` | none: loopback | whoever connected; `UI_AUTH` and `MCP_OAUTH` apply |
| access | `-listen-access` | a `Cf-Access-Jwt-Assertion` lasso verifies against your team's keys and the application's Audience tag | the email in it (a person), or the service token's client ID (a machine) |
| tailnet | `-tailnet` | tailscaled's answer for the connection's real peer address | the device owner's login (a person), or the device's tags (a machine) |
| serve | `-listen-serve` | the `Tailscale-User-Login` header `tailscale serve` sets | that login (a person) |

The network listeners fail closed. A request without the proof gets 403 before anything else looks at it, so a missing Access application, a second tunnel, or a stray proxy pointed at the port lets nobody in. The access and serve listeners must be loopback addresses, since their proof only means something when the front door is the one thing that can reach them. On the tailnet listener, lasso never reads a forwarded header: only the socket peer counts.

A caller a network listener verified needs nothing else: it satisfies `UI_AUTH`, `MCP_OAUTH`'s check on `/mcp`, the browser tools' credential rule and the deprecated header gate. The local listener keeps its trust: anything on lasso's machine that can connect to `127.0.0.1:8090` is in, which is what lets lasso's own CLI and local agents work with no credentials. On a shared machine, set `UI_AUTH` and `MCP_OAUTH`. lasso refuses a non-loopback `-listen` unless `UI_AUTH` is set or one of the [deprecated gates](#deprecated-gates) is on; see [Deployment](./deployment/index.md#the-listeners).

### `LASSO_ALLOW`

Among the callers a network listener proved, `LASSO_ALLOW` decides who may in. It is one comma-separated list, read from the environment, that applies to every network listener. The local listener never reads it.

| entry | matches |
| --- | --- |
| `you@example.com` | a person: an Access email or a tailnet login |
| `tag:agents` | a tagged tailnet device carrying that tag |
| `service:<client id>` | a Cloudflare Access service token |
| `ui:tag:agents`, `ui:service:<client id>` | that machine, let into the UI and `/api` as well |

**Unset**, each front door's own policy rules. On the access listener, whoever Access admitted gets in, since an Access policy is an explicit grant. On the tailnet and serve listeners, only the tailnet login that owns this node gets in, because "anyone on the tailnet" includes people a device is shared with and every tagged device. A tagged node has no owner, so it admits nobody until `LASSO_ALLOW` names them. **Set**, only what it lists gets in, on every network listener, so listing yourself for the tailnet means also listing any service token you want to keep working through Access.

**Machines stay off `/api`.** A machine (a service token or a tagged device) reaches `/mcp`, `/herdr-mcp`, `/cdp` and the OAuth and webhook endpoints, and gets 403 everywhere else, unless its entry carries `ui:` or it presents a lasso token with the `ui` scope. The UI's API has no per-host checks, so an agent machine with it would reach every host's files and terminals; the MCP servers are where host scope is enforced.

### Tokens and `X-Lasso-Token`

Two credentials of lasso's own sit beside the front doors:

- **`UI_AUTH=user:pass`** turns on HTTP basic auth for the UI, the API, the terminals, `/cdp` and `/mcp`'s `browser_*` tools (required per call, while the rest of `/mcp` stays open). It is read **from the environment only**, never a flag, because a command line is visible to every user on the machine through `ps` and `/proc`. It does not cover `/mcp` or `/herdr-mcp`.
- **`MCP_OAUTH=client_id:client_secret`** (environment only too) makes lasso a small OAuth 2.1 authorization server for its own `/mcp`. `/mcp` then requires a bearer token lasso issued, the `UI_AUTH` credentials, or a caller a network listener verified. It is what makes **per-host credentials** take effect: a lasso token is bound to one host and limits which hosts' agents its holder can see and manage. `/herdr-mcp`, `/cdp` and the `browser_*` tools take the same rule, and a per-host token reaches the browsers only if its scope includes lasso's own machine. Without `MCP_OAUTH`, provisioned per-host credentials are **not enforced** and lasso logs a warning at startup. See [MCP OAuth](./mcp/oauth.md) and [Agent scope](./mcp/agent-scope.md).

A token's scope still applies behind a front door. A verified caller with no lasso token gets the whole fleet; one that also sends a lasso token gets that token's host scope. When `Authorization` is already taken, as it is behind Access's Managed OAuth, send the lasso token in **`X-Lasso-Token`** instead: lasso moves it into `Authorization` before any check reads it. On a network listener, a bearer token lasso did not issue (Access's own, say) is dropped rather than refused, so it never stands in the way of the verified identity.

A plain lasso token reaches the MCP surfaces only. A token whose consent granted the **`ui` scope** is a device token: it also reaches the UI and `/api` and satisfies `UI_AUTH`. Only the consent screen grants it, and the screen says so in as many words.

The OAuth discovery, registration and token endpoints are open (they are the credential-less half of the handshake). The consent page, `/oauth/authorize`, stays behind `UI_AUTH` or a front door, so registering a client gets nobody a token until a human who can already get into lasso approves it.

### A machine lasso drives is not a client

Never forward lasso's port onto a host lasso manages, with an `ssh -R`, a tunnel or a socat. On that host, every agent lasso runs there and every other process would reach the loopback listener's trust, and lasso could not tell them from you. Agents on other machines come in through a network listener, as machines named in `LASSO_ALLOW`, with a lasso token for host scope. Your tailnet policy is the outer wall: a machine with no business reaching lasso should not be able to connect to it at all.

### Deprecated gates

Two older gates still work and are deprecated:

- **`-insecure-no-auth`** lets `-listen` bind a non-loopback address with no auth at all. Every device that can reach the address gets in, with no identity, over plain HTTP. Use `-tailnet`.
- **`-require-access-header`** (with `-access-allowed-emails`) makes the `-listen` listener answer 403 to any request without a Cloudflare Access identity, on every route, so lasso's own CLI is refused too. With `LASSO_ACCESS_TEAM` and `LASSO_ACCESS_AUD` set it verifies the `Cf-Access-Jwt-Assertion`; without them it only checks that a `Cf-Access-Authenticated-User-Email` header is present, which anyone who reaches the port can send. Use `-listen-access`.

### The browser's origin guard

`/cdp` and the `browser_*` tools refuse any request whose `Origin` names a different website than the one lasso is being reached on, before any authentication runs. Without this, a web page you happen to visit could open a connection to a lasso on your own machine (loopback, no auth) and drive the shared browser with your logins. Requests with no `Origin`, such as an agent's MCP client or Playwright, are unaffected.

lasso's own chrome-devtools-mcp processes (the ones behind the `browser_*` tools) reach `/cdp` over loopback with a random token minted for each lasso process. It is held only in memory and in those processes' arguments, and is accepted only on `/cdp`. They also start with a minimal environment that does not include `UI_AUTH`, `MCP_OAUTH`, or other secrets of lasso's.

lasso never falls back to `npx chrome-devtools-mcp@latest`: fetching an unpinned package at runtime, on the machine holding the browsers' logins, is a supply-chain risk. It also never adds Chromium's `--no-sandbox` on its own, except when running as root, where Chromium will not start without it.

### Plugins

A plugin's manifest is written by its author, so it grants nothing by itself:

- **A new plugin is disabled.** Enabling it approves exactly the permissions the listing shows: its tabs, image, command, network hosts, environment variable names, and which secret may go to which host. If the manifest later asks for more, the plugin stops loading until you approve again.
- **Its MCP server runs in an [isb](https://github.com/execution-associates/isb) sandbox**: an unprivileged container by default, running as uid 1000, with the plugin directory mounted read-only, its own data directory, nothing else from your machine, and no network except the host names it listed, enforced by isb's egress proxy. Secrets never enter the sandbox: the guest holds a placeholder, and isb puts the real value on the wire only toward the secret's approved hosts.
- **VM** (`lasso plugin vm <name> on`) runs that sandbox as a virtual machine with its own kernel, so a kernel exploit inside it does not reach your machine. A container shares your kernel. Use a VM for a plugin you have reason to distrust.
- **Trusted** (run on the host instead of in a sandbox) is a flag only you can set, as is VM. A plugin cannot ask for either in its manifest. Even trusted, it gets a minimal environment, never `UI_AUTH`, `MCP_OAUTH` or `LASSO_MCP_TOKEN`.
- **Egress is by name, and a name is the client's word.** isb's proxy passes TLS through without decrypting it, so it trusts the name the client sends: code that can reach one allowed host on a shared front end (a CDN) can ask it for another site it serves. Allow only hosts you would trust with the traffic. Code inside can also use a secret against its approved hosts; the sandbox protects the value, not the account's powers.
- **Its tabs are sandboxed.** Plugin pages are served with a sandboxing Content-Security-Policy that makes them an opaque origin, so they cannot use your session to reach lasso's API. They talk to lasso only through a small message bridge (read the focused pane and the theme, open a file in the viewer, call the plugin's own tools, show a toast). A tab can open a file only while it is on screen, and it cannot overwrite unsaved edits.
- **Installing from GitHub grants nothing either.** The checkout is validated in a staging area, and "install and enable" approves exactly the permissions the preview showed. The `lasso-plugin` GitHub topic is not a reviewed catalog.

See [Plugins](./plugins/index.md).

### Push notifications

A push subscription is a URL lasso will POST to on its own, so registering one is behind `UI_AUTH` like the rest of the UI. lasso accepts only `https` endpoints with a valid key. A stored endpoint is effectively a capability to push to that device, so it is never returned to a browser and never logged; Settings and the logs identify devices by a digest. Payloads are encrypted end to end: the push service relays a blob it cannot read.

## Recommended deployments

| setup | safe when |
| --- | --- |
| Loopback only (default) | Always, as long as nothing forwards to the port. Other users on the same machine can still reach `127.0.0.1`, so on a shared machine set `UI_AUTH` and `MCP_OAUTH`. |
| [Tailnet](./deployment/tailnet.md) (`-tailnet`) | Your tailnet policy lets only the devices you mean reach this node, and `LASSO_ALLOW` is unset (you alone) or names only people and machines you would hand a shell to. This is the simplest setup for your own devices. |
| [Cloudflare tunnel + Access](./deployment/cloudflare.md) (`-listen-access`) | The tunnel targets the access listener, never `-listen`; the Access application covers the whole hostname; and its policy admits only you, plus service tokens you issued. This is the setup for reaching lasso from anywhere. |
| Another reverse proxy in front of `-listen` | The proxy authenticates every request itself. lasso cannot see who is behind it. |
| Public bind | Never. |

A few more habits worth keeping:

- **Log the shared browser into accounts carefully.** Every agent that can reach `/cdp` or the `browser_*` tools can act as you on those sites, in every one of [lasso's browsers](./concepts/shared-browser.md): separate browsers separate cookies, not access. Log in only where you are happy for your agents to act as you.
- **Keep secrets out of argv.** `UI_AUTH`, `MCP_OAUTH` and `LASSO_MCP_TOKEN` belong in the environment.
- **Turn off the in-app update on shared boxes** with `-disable-self-update`, so an agent working through the UI cannot rebuild and restart lasso.

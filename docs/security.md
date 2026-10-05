---
title: Security model
description: What anyone who can reach lasso can do, which gates exist, and how to deploy it safely.
order: 90
nav_title: Security
---

lasso is built to give you, and the agents you run, full control of your machines from a browser. The same power is available to anyone else who can reach it. This page lays out what is exposed, what guards each part, and the setups that are safe.

The short version: **whoever can reach lasso's port can act as you on every machine lasso can reach.** Keep it on loopback, and reach it through something that authenticates you (Cloudflare Access or a tailnet you trust).

## What reaching lasso gives you

| surface | what it can do |
| --- | --- |
| **The terminals** (`/terminal/`, `/shell/`) | A writable shell on lasso's machine, and on any SSH host the tab switches to. Type anything you could type yourself. |
| **The file endpoints** (`/api/file`, `/api/files`, `/api/file-write`, `/api/file-rename`, `/api/file-delete`, `/api/file-upload`) | Read, write, rename, delete and upload **any absolute path** the lasso user can access, on lasso's machine or on any host lasso can reach over SSH (a request can name the host). |
| **`/mcp`** | Create and close agents on any reachable host, list repositories and branches, push notifications to your phone, open files in your sidebar, manage browser profiles. Enabled plugins add their own tools. |
| **`/cdp` and `/browser-mcp`** | Full control of the shared Chromium: read every open page, type into it, navigate it, run scripts in it. That includes **every site its profiles are logged into**. |
| **The rest of `/api/*`** | Everything the UI does: switching hosts, updating herdr on remote hosts, changing themes across the fleet, managing plugins. |

The SSH reach matters. lasso uses your `~/.ssh/config` and keys, so a lasso that can reach ten hosts hands all ten to whoever reaches lasso.

## The gates

### Bind address

lasso binds `127.0.0.1:8090` by default. It **refuses** to bind a non-loopback address unless `UI_AUTH` is set, `-require-access-header` is on, or you pass `-insecure-no-auth` to say the network is private. See [Deployment](./deployment/index.md#the-rule-never-bind-it-publicly).

The check sees only the bind address. A tunnel, `tailscale serve`, an SSH forward or a reverse proxy pointing at a loopback lasso exposes it to whoever that forwarder admits, and lasso cannot tell the difference.

### `UI_AUTH`: basic auth

`UI_AUTH=user:pass` turns on HTTP basic auth for the UI, the API, the terminals, `/cdp` and `/browser-mcp`. It is read **from the environment only**, never from a flag, because a command line is visible to every user on the machine through `ps` and `/proc`. In a systemd unit, use an `EnvironmentFile` that only you can read.

`UI_AUTH` does **not** cover `/mcp`, which is open by default so agent CLIs connect without credentials.

### `MCP_OAUTH`: gating `/mcp`

`MCP_OAUTH=client_id:client_secret` (environment only, like `UI_AUTH`) makes lasso a small OAuth 2.1 authorization server for its own `/mcp`. `/mcp` then requires a bearer token lasso issued, or the `UI_AUTH` credentials. It also enables per-host credentials, which limit which hosts' agents a caller can see and manage. With `MCP_OAUTH` set, `/cdp` and `/browser-mcp` follow the same rule, and a per-host credential reaches them only if its scope includes lasso's own machine.

The OAuth discovery, registration and token endpoints are open (they are the credential-less half of the handshake). The consent page, `/oauth/authorize`, stays behind `UI_AUTH` or Access, so registering a client gets nobody a token until a human who can already get into lasso approves it.

Without `MCP_OAUTH`, per-host credentials you have provisioned are **not enforced**: every caller is anonymous and sees the whole fleet. lasso logs a warning at startup when that is the case. See [MCP OAuth](./mcp/oauth.md) and [Agent scope](./mcp/agent-scope.md).

### The Cloudflare Access header gate

`-require-access-header` makes lasso answer 403 to any request without a `Cf-Access-Authenticated-User-Email` header, optionally restricted to `-access-allowed-emails`. It runs ahead of every other check, on every route.

That header is only meaningful behind Cloudflare Access, which strips any copy a client sends and adds its own after verifying the login. Behind anything else, anyone can send it. lasso therefore ignores the header entirely unless the flag is set. See [Cloudflare](./deployment/cloudflare.md#requiring-the-access-identity-in-lasso).

### The browser's origin guard

`/cdp` and `/browser-mcp` refuse any request whose `Origin` names a different website than the one lasso is being reached on, before any authentication runs. Without this, a web page you happen to visit could open a connection to a lasso on your own machine (loopback, no auth) and drive the shared browser with your logins. Requests with no `Origin`, such as an agent's MCP client or Playwright, are unaffected.

lasso's own chrome-devtools-mcp processes (the ones behind `/browser-mcp`) reach `/cdp` over loopback with a random token minted for each lasso process. It is held only in memory and in those processes' arguments, and is accepted only on `/cdp`. They also start with a minimal environment that does not include `UI_AUTH`, `MCP_OAUTH`, or other secrets of lasso's.

lasso never falls back to `npx chrome-devtools-mcp@latest`: fetching an unpinned package at runtime, on the machine holding the browser's logged-in profiles, is a supply-chain risk. It also never adds Chromium's `--no-sandbox` on its own, except when running as root, where Chromium will not start without it.

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
| Loopback only (default) | Always, as long as nothing forwards to the port. Other users on the same machine can still reach `127.0.0.1`, so on a shared machine set `UI_AUTH`. |
| [Cloudflare tunnel + Access](./deployment/cloudflare.md) | The Access application covers the whole hostname and its policy admits only you (plus service tokens you issued). This is the recommended setup for reaching lasso from anywhere. |
| [Tailnet](./deployment/tailnet.md) (bind or `tailscale serve`) | Every device and person on the tailnet is someone you would hand a shell to. Otherwise add `UI_AUTH` and `MCP_OAUTH`, or use Access. |
| Public bind | Never. |

A few more habits worth keeping:

- **Log the shared browser into accounts carefully.** Every agent that can reach `/cdp` or `/browser-mcp` can act as you on those sites, in every [browser profile](./concepts/shared-browser.md): profiles separate cookies and proxies, not access. Log in only where you are happy for your agents to act as you.
- **Keep secrets out of argv.** `UI_AUTH`, `MCP_OAUTH` and `LASSO_MCP_TOKEN` belong in the environment.
- **Turn off the in-app update on shared boxes** with `-disable-self-update`, so an agent working through the UI cannot rebuild and restart lasso.

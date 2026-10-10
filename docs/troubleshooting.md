---
title: Troubleshooting
description: Common problems running lasso, what causes them, and how to fix them.
order: 91
---

Start with `lasso doctor`. Most problems below show up in its output or in lasso's startup log (`journalctl --user -u lasso`, or `~/.lasso/lasso.log` for a `lasso start` daemon).

## `lasso doctor`

```bash
lasso doctor
```

It prints one line per check, with ✓ for pass, ⚠ for a warning and ✗ for a failure, and exits non-zero if any check failed. [CLI reference](./reference/cli.md#lasso-doctor) explains each check. Doctor looks at the default socket (`HERDR_SOCKET_PATH`, else `~/.config/herdr/herdr.sock`) and the default port only: a lasso under systemd on port 8090 shows as "in use by another process", and one on a non-default `-listen` is invisible to it.

## lasso won't start

**`refusing to listen on non-loopback "..." without auth`**
You passed a `-listen` address that isn't loopback. Set `UI_AUTH=user:pass` in the environment, pass `-require-access-header` if Cloudflare Access fronts the hostname, or pass `-insecure-no-auth` if the address is on a private network such as your tailnet. See [Deployment](./deployment/index.md#the-rule-never-bind-it-publicly).

**`listen 127.0.0.1:8090: ... address already in use`**
Another process holds the port, often a second lasso (a `lasso start` daemon beside a systemd unit, for example). Find it with `ss -ltnp 'sport = :8090'` (`lasso status` only knows about a `lasso start` daemon), or pick another port with `-listen`.

**`ttyd: terminal on ...: exec: "ttyd": executable file not found in $PATH`**
lasso serves its terminals through [ttyd](https://github.com/tsl0922/ttyd), which must be installed on lasso's machine and on `PATH`. Install it from your package manager. Under systemd, check that the unit's `PATH` includes where it lives.

## The terminal says it is nested, or herdr won't start in it

If lasso itself runs inside a herdr pane (for example, you started it from a herdr session), the herdr in lasso's terminal sees that it is inside another herdr session and refuses to start. Allow nesting in `~/.config/herdr/config.toml`:

```toml
[experimental]
allow_nested = true
```

Running lasso as a [systemd service](./deployment/systemd.md) avoids the problem entirely.

## Herdr version mismatch

lasso talks to herdr over its socket using a specific protocol version. When they disagree, terminals and agent operations break in confusing ways.

- `lasso doctor` reports `herdr X speaks protocol N, lasso targets M — update one to match`. Update herdr (`herdr update`, which works from lasso's **Terminal** tab) or update lasso (`lasso update`).
- In the host menu, a remote host whose protocol differs from lasso's machine is greyed out. If it is behind, it gets an **update** button; see [Updating](./deployment/updating.md#updating-herdr).
- A host marked **restart needed** has a newer herdr installed than the server it runs. Restart herdr on that host.

## A host shows "timed out" or is missing

The host menu lists every concrete `Host` alias in `~/.ssh/config`, probing each over SSH in the background.

- **timed out** means the probe hit its deadline, so lasso doesn't know whether the host is up. It is shown as unknown, not as a failure. A sleeping laptop looks like this. Hosts that keep failing are probed less often, backing off to once every 15 minutes; the refresh button at the top of the menu probes every host immediately.
- **Missing hosts**: lasso reads only `~/.ssh/config` itself. It does not follow `Include` directives and skips wildcard patterns (`Host *.internal`), so give each machine you want listed a concrete alias in the main file.
- **Unreachable** with an SSH error: lasso runs `ssh` non-interactively (`BatchMode=yes`), so a host that needs a password or an unlocked key it can't reach fails. Under systemd, make sure the unit can reach your SSH agent (`SSH_AUTH_SOCK`).

**`<host>: the sqlite3 CLI is not installed, but lasso needs it ...`**
Each host keeps its own New dialog settings in its own `~/.lasso/lasso.db`, which lasso reads and writes over SSH with that host's `sqlite3`. Install it on the host (`sudo apt-get install -y sqlite3`, `sudo dnf install -y sqlite`, or your platform's equivalent). Until then that host's repository picker is empty. See [What a remote host needs](./concepts/hosts.md#what-a-remote-host-needs).

## Push notifications

**Settings says "This browser can't do Web Push"**
The page isn't in a secure context. Push needs `https://` (or `localhost`); a plain-HTTP tailnet address like `http://myhost:8090` cannot do it at all. Reach lasso through a [Cloudflare tunnel](./deployment/cloudflare.md) or [`tailscale serve`](./deployment/tailnet.md#real-https-with-tailscale-serve).

**On iOS, Settings asks you to add lasso to the Home Screen**
iOS only allows Web Push for a site launched from the Home Screen. Use Safari's Share sheet, **Add to Home Screen**, then enable notifications from the installed app. See [On your phone](./getting-started/phone.md).

**Every notification arrives twice**
The device is registered from two origins, for example both `https://lasso.example.com` and a `ts.net` address. Each origin is a separate subscription. Turn notifications off in one of them (Settings lists registered devices).

**`lasso notify` exits non-zero with `not delivered`**
No device took the notification, usually because none is registered; the message after the dash says why. That is deliberate, so an agent never reports pinging you when nothing was delivered.

## Downloads and copying over plain HTTP

A Files-tab download is an ordinary link to the file, served with `Content-Disposition: attachment`, but some browsers warn about or block downloads from a plain-HTTP page. Terminal copy falls back to a legacy clipboard write there, since the Clipboard API only exists in a secure context. Both work fully on `localhost` and over HTTPS, and viewing files works everywhere. See [Why HTTPS matters](./deployment/index.md#why-https-matters).

## The Browser tab

**`no Chromium found — install chromium (or Google Chrome), or set LASSO_BROWSER to its path`**
The shared browser needs Chromium or Google Chrome on lasso's machine. Until there is one, the tab offers only **Iframe** mode.

**`... cannot start its sandbox on this machine` / "No usable sandbox!"**
On Ubuntu 23.10 and later, AppArmor lets only binaries with an AppArmor profile create the user namespaces Chromium's sandbox needs. Playwright's Chromium, or any build you unpacked yourself, has none. lasso will not fall back to `--no-sandbox` on its own. Fix it one of these ways:

- install a packaged Google Chrome or your distribution's Chromium, which ships with a profile, and point `LASSO_BROWSER` at it if lasso finds the other one first;
- write an AppArmor profile that grants `userns` to the binary you want to use;
- knowingly run it unsandboxed with `LASSO_BROWSER_ARGS=--no-sandbox`. Every page an agent opens then runs with your full user privileges.

**`the browser data directory ... is in use by another lasso (pid N); give a second instance its own LASSO_DIR`**
Two lasso instances on one machine can't share a browser's data directory. Start the second with its own data directory, such as `LASSO_DIR=~/.lasso-second`.

**Iframe mode shows an error instead of the page**
Iframe mode loads the page in your own browser, so your browser's rules apply:

- *mixed content*: lasso is on `https://` and the page is `http://` (a bare port like `5173` becomes `http://<lasso's hostname>:5173`). Open it in a new tab, reach lasso over plain HTTP on loopback or your tailnet, or switch to **Agent** mode.
- *doesn't allow other sites to embed it*: the site forbids framing. Open it in a new tab.
- *blocked embedding a private page*: lasso is on a public address and the page is on a private or tailnet one, which browsers refuse. Open lasso through its tailnet address, or open the page in a new tab.

**Agent** mode has none of these limits, because the page runs in the shared browser on lasso's machine. There, a bare port means `localhost` on lasso's machine.

## No `browser_*` tools on `/mcp`

The `browser_*` tools are Google's chrome-devtools-mcp, run by lasso on its own machine. When it can't run them, `/mcp` simply has none, and the `shared_browser` tool's `browser_tools_reason` (and **Settings → General → Terminal & browser**) says why:

- `chrome-devtools-mcp is not installed on lasso's machine` — install it there (not on the agent's machine): `npm i -g chrome-devtools-mcp` or `mise use -g npm:chrome-devtools-mcp`, or set `LASSO_BROWSER_MCP` to its path. lasso deliberately has no `npx` fallback. The tools appear on the next MCP session's initialize, with no restart.
- `the browser tools are disabled` — `LASSO_BROWSER_MCP=off` or `-browser-mcp off` is set.

`lasso connect` prints the same reason.

## A `browser_*` call is refused

The browser tools are gated like `/cdp`, more strictly than the rest of `/mcp`, and a call that falls short comes back as a tool error:

- `the browser tools need lasso's UI_AUTH credentials` — lasso has `UI_AUTH` set without `MCP_OAUTH`. `/mcp` itself is open then, but the browser tools need the `UI_AUTH` basic credentials on the agent's MCP connection (an `Authorization: Basic …` header; `lasso connect` registers them when `UI_AUTH` is in its environment).
- `… outside this credential's reach` — under `MCP_OAUTH`, a per-host credential whose scope does not include lasso's own machine, where the browsers run.
- `the browser tools refuse a cross-origin request` — the request carried an `Origin` from another website.

## Plugins

A plugin's MCP server runs in an isb sandbox. When one can't start for a reason a retry won't fix, its MCP status in Settings and `lasso plugin list` reads `unavailable` with one of these reasons. Fix the cause, then restart it (`lasso plugin restart <name>`, or Restart in Settings). Its tabs, themes and fonts keep working meanwhile. See [Plugins](./plugins/index.md#isb).

**`isb not found: install it (mise use -g github:execution-associates/isb) or set LASSO_ISB`**
lasso looked for `isb` on its own `PATH` and in mise's installs and found neither. Install it as the message says, or point `LASSO_ISB` at the binary. A systemd unit's `PATH` is often shorter than your shell's.

**`isb 1.0 or later is required (found ...)`**
Every isb lasso found is older than 1.0. Upgrade it. When `LASSO_ISB` is set, only that one is tried.

**`isb not found at LASSO_ISB="..."`** or **`isb not found: LASSO_ISB="..." is not on PATH`**
`LASSO_ISB` names a path that doesn't exist or a command that isn't on lasso's `PATH`. Fix or unset it.

**`sandboxed plugins are disabled (LASSO_ISB=off)`**
Sandboxed servers are switched off on purpose. Unset `LASSO_ISB`, or trust the plugin if you mean to run it on the host.

**`isb serve is not running (it runs the egress proxy plugin sandboxes need): ...`**
lasso checks `isb serve`'s health on its unix socket (`$ISB_SERVE_SOCKET`, else `$XDG_RUNTIME_DIR/isb/serve.sock`) before creating a sandbox, because a sandbox created without it has no working network. Start `isb serve` as the same user lasso runs as, with the same `XDG_RUNTIME_DIR` and `XDG_STATE_HOME`.

**`plugin servers run in the other lasso using this plugins directory (pid N, listening on ...)`**
Two lassos share one `LASSO_DIR`, and the other one holds `plugins/.runner.lock`, so it runs the servers. This is deliberate: both would otherwise fight over the same sandbox names. Stop the other lasso and this one takes over within about 10 seconds, or give one of them its own `LASSO_DIR`.

**`secret NAME could not be resolved (not in lasso's environment, and `secret NAME` failed)`**
An approved secret has no value. Put it in lasso's environment (for a systemd unit, in the unit's environment), then restart the server.

**`the path ... contains ':', which isb's mount syntax cannot express`**
The plugin directory or its data directory has a colon in its path. Move it, or set `LASSO_DIR` to a path without one.

**A plugin is `invalid`: `"..." is an IP address; the sandbox's egress allows host names only`** (or **`is a wildcard over a single label`**)
isb's egress allows host names only. The manifest's `network` entries and secret hosts must be names such as `api.example.com` or `*.example.com`, never an IP address or `*.com`.

**The server fails to start, or exits at once**
Run `lasso plugin log <name>`: `isb create`'s output and the server's stderr are there. The image must contain `sh` and `sleep` (or `tail`), since lasso keeps the container alive with them; a distroless image without a shell does not work. In a VM, the default image has `python3` but no node or bun, so a plugin that needs those in a VM must name a `vm_image`.

**Requests from inside the sandbox fail**
Only the hosts in the manifest are reachable. On a host with ufw, the egress proxy also needs `sudo isb host setup --sandbox-egress`. TLS to a secret's hosts goes through isb's proxy, so a runtime with its own trust store must be told to trust `/etc/isb/egress-ca.crt`, a client that pins certificates fails, and only HTTP/1.1 works toward those hosts (gRPC does not).

## MCP clients

**claude.ai or Claude Desktop says "Couldn't register with lasso's sign-in service"**
The connector needs an OAuth server. With lasso behind Cloudflare Access and `MCP_OAUTH` unset, enable **Managed OAuth** on the Access application so Access plays that role. See [Cloudflare](./deployment/cloudflare.md#claudeai-and-claude-desktop-connectors-managed-oauth).

**`list_agents` comes back empty or short**
Usually this is scope working as intended. With `MCP_OAUTH` set, a per-host credential sees only its own host (plus any host groups it belongs to), so an agent on one machine does not see agents elsewhere. Check what the credential is allowed with `lasso mcp-client list`; see [Agent scope](./mcp/agent-scope.md).

**Everything returns 403 after turning on `-require-access-header`**
Requests that don't come through Cloudflare Access carry no identity header. That includes lasso's own CLI (`lasso notify`, `lasso mcp`, `lasso open`, `lasso closeme`) calling it over loopback, and agents using a Cloudflare service token. See [Cloudflare](./deployment/cloudflare.md#requiring-the-access-identity-in-lasso).

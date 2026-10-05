---
title: Writing a plugin
description: The plugin manifest, sidebar tabs and their message bridge, MCP servers in isb sandboxes, themes and fonts.
order: 51
nav_title: Writing plugins
---

A plugin adds to lasso in any of four ways:

- **Sidebar tabs**: a web page of its own in lasso's right-hand sidebar, next to Files, Browser and Settings.
- **MCP tools**: tools that show up on lasso's `/mcp` server as `<plugin>__<tool>`, so every agent connected to lasso can call them. `lasso mcp` lists them too.
- **Themes**: Omarchy-format palettes that become ordinary lasso themes (see [Themes](#themes)).
- **Fonts**: font files that become choices in Settings' Typography section (see [Fonts](#fonts)).

A plugin's MCP server runs in an **[isb](https://github.com/execution-associates/isb) sandbox** by default: an unprivileged container, or a VM with its own kernel if you ask for one. It does not run on your machine as you. Nothing a plugin ships runs until you enable it, and enabling it approves exactly the permissions lasso shows you. This page is for plugin authors; to install and manage plugins, see [Plugins](./index.md).

A complete working example is in [`examples/plugins/hello`](https://github.com/execution-associates/lasso/tree/main/examples/plugins/hello): one tab and a stdlib-only Python MCP server. [`examples/plugins/harbor`](https://github.com/execution-associates/lasso/tree/main/examples/plugins/harbor) is an appearance-only plugin: one theme and one font.

## Layout

```
<LASSO_DIR or ~/.lasso>/plugins/
  hello/
    plugin.json        the manifest (required)
    ui/index.html      a tab's page (any files you like)
    server.py          the MCP server (anything that speaks MCP over stdio)
```

A plugin gets there in one of three ways (see [Installing and sharing](#installing-and-sharing)):

- **From GitHub**: `lasso plugin install owner/repo[/subdir]`, or Settings → General → Plugins → Install from GitHub.
- **Linked**: `lasso plugin link <path>` uses a checkout where it is, without copying it. This is the way to develop one.
- **By hand**: put its directory there. The directory name **is** the plugin's name.

From a checkout of the lasso repository:

```sh
cp -r examples/plugins/hello ~/.lasso/plugins/
lasso plugin list
lasso plugin enable hello
```

## The manifest

```json
{
  "name": "hello",
  "version": "0.1.0",
  "description": "one line",
  "min_lasso_version": "4.2.0",
  "platforms": ["linux", "darwin"],
  "tabs": [
    { "id": "main", "label": "Hello", "icon": "sparkles", "entry": "ui/index.html" },
    { "id": "docs", "label": "Docs", "icon": "book", "url": "https://example.com" }
  ],
  "mcp": {
    "image": "python:3.12-slim",
    "vm_image": "images:ubuntu/24.04/cloud",
    "command": ["python3", "-u", "server.py"],
    "network": ["api.example.com:443"],
    "env": { "LOG_LEVEL": "info" },
    "secrets": [ { "name": "EXAMPLE_TOKEN", "hosts": ["api.example.com"] } ]
  }
}
```

| field | |
|---|---|
| `name` | Required. `^[a-z][a-z0-9-]{0,31}$`, and it must equal the directory name. |
| `version`, `description` | Shown in Settings. |
| `min_lasso_version` | Optional `X.Y.Z`. On an older lasso the plugin is `invalid` with "needs lasso >= X (this is Y)". A development build (`-dev`) always passes. |
| `platforms` | Optional list of `linux` and `darwin` (`macos` is accepted for `darwin`). On any other OS the plugin is `invalid` with the reason. |
| `tabs[].id` | `^[a-z][a-z0-9-]{0,31}$`, unique within the plugin. The tab's global id is `plugin:<name>:<id>`. |
| `tabs[].label` | 1-64 characters. |
| `tabs[].icon` | A name from lasso's curated set: `activity bell book book-open bot box calendar chart clock cloud code cpu dashboard database file-text flask folder gauge git-branch globe hammer heart image layers link list mail map message music notebook package puzzle rocket search server shield sparkles star terminal wrench zap` (see `PLUGIN_ICONS` in [`src/web/src/lib/plugins.ts`](https://github.com/execution-associates/lasso/blob/main/src/web/src/lib/plugins.ts)). An unknown name falls back to a puzzle piece and is never an error. |
| `tabs[].entry` | A file inside the plugin directory, served by lasso. Relative, with no `..` and no hidden segments. |
| `tabs[].url` | An `http(s)` URL, framed as-is. A tab has exactly one of `entry` or `url`. |
| `mcp.image` | Required with `mcp`. The OCI image the container is built from. A plain reference is a Docker Hub image (`python:3.12-slim` is `docker:python:3.12-slim`); an isb prefix (`docker:`, `ghcr:`, `quay:`, `oci:`, or `images:` for a system image) is used as written. The image must have `sh` and `sleep` (or `tail`): see [The sandbox](#the-sandbox). |
| `mcp.vm_image` | Optional. The VM image to boot when the operator runs the plugin in a VM, e.g. `images:debian/13/cloud`. An OCI image cannot boot as a VM. Without it, lasso uses `images:ubuntu/24.04/cloud`, which has `python3` but no node or bun: a node or bun plugin that should run in a VM needs a `vm_image` with its runtime. |
| `mcp.command` | Required with `mcp`. The argv of a stdio MCP server. It runs as uid 1000 with the plugin directory as its working directory, mounted read-only at `/plugin`. |
| `mcp.network` | The only hosts the server may reach, each `host[:port]` (the port defaults to 443). `*.example.com` covers every name below `example.com`, not `example.com` itself. A host must be a name: an IP address, or a wildcard over a single label (`*.com`), makes the manifest invalid. Empty or absent means **no network at all**. |
| `mcp.env` | Plain, non-secret environment variables. |
| `mcp.secrets` | Secrets the server needs, each with the host names it may be sent to (the same name rules as `network`; sent on port 443). A secret's hosts are reachable too. Two secrets may not differ only in case. |
| `themes` | Up to 32 themes. See [Themes](#themes). |
| `fonts` | Up to 16 fonts. See [Fonts](#fonts). |

Neither `min_lasso_version` nor `platforms` is a permission, so neither is in the fingerprint.

An invalid manifest never loads anything. The plugin is listed as `invalid` with the reason, in Settings and in `lasso plugin list`.

## Approval and trust

A manifest is written by whoever wrote the directory, so nothing in it can grant itself anything. You grant it, in Settings → General → Plugins or with `lasso plugin`.

- **A new plugin is disabled.** Enabling it (Settings shows the exact permissions in a dialog first) approves the permissions shown: its tabs' entries and URLs, the image and the VM image, the command, the network allowlist, the env variable names, each secret with the hosts it may go to, its theme ids, and its fonts' ids, families and categories. lasso stores a fingerprint of that set in its own database, never in the plugin directory.
- **If a later edit changes any of those**, the plugin reads as `needs_approval`. Its tabs and its MCP server stop loading until you approve the new set. Changes to the version, the description, a tab's label or icon, a theme's label or palette, a font's license, or the font files themselves do not need re-approval. lasso rescans the directory every 10 seconds, and on every Settings visit, so an edit is noticed without a reload.
- **Isolation** is a container (the default) or a VM. A VM has its own kernel, so a kernel exploit inside it does not reach your machine, at the cost of a much slower start than a container's few seconds. It is a flag in lasso's database that only you can set (`lasso plugin vm <name> on|off`, or Settings' Container / VM switch). A manifest can name a `vm_image` but cannot ask for a VM, and flipping the switch restarts the server.
- **Trusted** runs the MCP server directly on your machine, as your user, outside the sandbox. It is a flag in lasso's database that only you can set (`lasso plugin trust <name>`, or the Settings toggle). A manifest field cannot set it. Trusted wins over the VM switch. Even a trusted server gets a minimal environment (PATH, HOME, locale, XDG directories) plus its own `env` and secrets. It never gets lasso's `UI_AUTH`, `MCP_OAUTH` or `LASSO_MCP_TOKEN`.

```sh
lasso plugin list [-json]
lasso plugin enable|disable|trust|untrust|restart <name>
lasso plugin vm <name> on|off
lasso plugin reload
```

These talk to the running lasso (found through `LASSO_URL` or `LASSO_LISTEN`, and sending `UI_AUTH` if it is set). Enabling a plugin starts a process, and only the server can do that.

## Installing and sharing

```sh
lasso plugin install <source> [--ref R] [-y] [--no-enable]
lasso plugin update <name> [-y]
lasso plugin uninstall <name> [--purge-data]
lasso plugin link <path> [--enable]
lasso plugin unlink <name>
lasso plugin log <name> [-n 200] [-f]
lasso plugin data-dir <name>
```

**Install** takes `owner/repo`, `owner/repo/sub/dir`, or `https://github.com/owner/repo[/tree/<ref>/sub/dir]`. Only GitHub is supported. `--ref` pins a branch, a tag or a commit. lasso shallow-clones the repository into a hidden staging directory (`plugins/.staging/`), with git hooks off and submodules not fetched. It deletes `.git`, and refuses a checkout over 50 MB or 5000 files, or one whose plugin directory holds a symlink pointing outside it. Then it validates the manifest exactly as the scanner does, and shows you every permission, the source and the exact commit. Nothing is installed until you confirm. "Install and enable" approves exactly the fingerprint the preview showed; if the staged manifest differs from it, the confirm is refused and you preview again. A preview you do not confirm expires after 10 minutes. The plugin lands in `plugins/<name>/` (the name comes from its manifest), and lasso records its source, ref and commit. Without a terminal, `install` refuses unless you pass `-y`.

**Update** re-fetches the recorded source and ref, shows the same preview plus the current commit, and says whether the permissions change. Confirming swaps the new directory in; if that fails, the old one is put back. Unchanged permissions keep their approval. Changed ones read `needs_approval` until you approve them.

**Uninstall** removes a GitHub install's directory, forgets its approval, trust and isolation, and deletes any of its secrets left in isb's store. It keeps the plugin's data directory unless you pass `--purge-data`. A hand-placed plugin is never deleted by lasso: delete the directory yourself.

**Link** registers a local checkout (an absolute path holding `plugin.json`) by the name in its manifest. It is served and mounted from that path, and edits there take effect on the next rescan. **Unlink** forgets it and leaves the files alone. A link whose path disappears is listed as `invalid` with the reason. A name can only belong to one plugin: install and link refuse a name that is already installed, linked or hand-placed.

`lasso plugin list` shows each plugin's source: `github owner/repo@abc1234`, `linked <path>`, or `local`.

### Sharing a plugin

Put `plugin.json` at the root of a public GitHub repository (or in a subdirectory, and install it as `owner/repo/sub/dir`), and tag the repository with the GitHub topic **`lasso-plugin`** so people can find it. A topic is self-applied and nobody reviews it. The listing is not what keeps you safe: the approval dialog and the sandbox are.

## Data directory

The plugin directory is read-only to the plugin. Its one writable place is `<LASSO_DIR or ~/.lasso>/plugin-data/<name>/` (mode 0700, created when its MCP server first starts). In the sandbox it is mounted read-write at `/data`, and `LASSO_PLUGIN_DATA=/data`. The server runs as uid 1000, and its files land owned by your user when lasso runs as uid 1000 (isb maps guest 1000 to host 1000). A trusted server gets `LASSO_PLUGIN_DATA=<the host path>`. Keep state there, not in the plugin directory: an update replaces the plugin directory, and uninstall keeps the data directory unless asked. `lasso plugin data-dir <name>` prints the path.

## Tabs

Each tab lands in the sidebar's strip before Settings. You choose which tabs show and in what order, built-in and plugin alike, in Settings → General → Sidebar & usage. That layout is lasso's `ui_state` (`sidebar_tabs`), so every browser on the same lasso follows it. Settings itself cannot be hidden, and a hidden Files or Browser tab still opens when something needs it (an agent opening a file, a terminal link) until you pick another tab. A disabled plugin's entries are kept in the layout, so re-enabling it puts its tab back where it was.

A tab with an `entry` is served from `/plugins/<name>/<path>` and framed in the sidebar. Every response carries

```
Content-Security-Policy: sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads
```

That makes your page an **opaque origin**, even if someone opens its URL directly. Your scripts run, but they cannot read lasso's cookies or call lasso's API. That restriction is deliberate: lasso's file endpoints read and write any path on the machine, so a page that could call them would own the machine. Files are served `no-cache`, so an edit shows on the next load. Hidden files (`.env`) and symlinks that point outside the plugin directory are never served.

A tab with a `url` is framed as-is, under the same iframe `sandbox`, and the site must allow framing. It gets **no bridge**: it is someone else's site, and it has no business calling your plugin's tools.

A plugin tab mounts the first time it is selected and stays mounted after, like the built-in tabs, so switching away and back does not reload your page.

### The bridge

A page reaches lasso by posting messages to its parent. The protocol is `lasso-plugin/1`:

```js
// request
window.parent.postMessage({ lasso: 1, id: 7, method: "context.get", params: {} }, "*")
// response
{ lasso: 1, id: 7, result: { ... } }      // or { lasso: 1, id: 7, error: "message" }
// pushes, sent once after load and again whenever the value changes
{ lasso: 1, event: "context", data: { ... } }
{ lasso: 1, event: "theme", data: { ... } }
```

The parent answers only the frame the message came from, and routes each request by that frame. Plugin A can never make a call as plugin B. Pushes are posted with target origin `"*"` (an opaque origin cannot be named), so they carry nothing secret.

| method | params | result |
|---|---|---|
| `context.get` | | `{ host, cwd, cwd_host, pane_id, agent, plugin, tab }`, the focused pane |
| `theme.get` | | `{ dark, colors: { background, foreground, ... } }` |
| `file.open` | `{ path, line?, host? }` | Opens the file in the Files viewer, the same way an agent's `open_file` does. Unsaved edits are protected. `host` defaults to `cwd_host`. |
| `tool.call` | `{ tool, arguments }` | Calls one of **this plugin's own** MCP tools and returns its `CallToolResult`. `tool` may be un-prefixed (`greet`) or prefixed (`hello__greet`). Another plugin's tool is refused. |
| `toast` | `{ message }` | A short toast, prefixed with the plugin's name. |

Any other method answers `error: "unknown method"`.

## Themes

```json
"themes": [
  { "id": "harbor-night", "label": "Harbor Night", "dir": "themes/harbor-night" }
]
```

| field | |
|---|---|
| `id` | `^[a-z][a-z0-9-]{0,47}$`, unique within the plugin. It is the theme's key everywhere: in the theme picker, in herdr's `config.toml`, and on every host the theme is synced to. |
| `label` | Optional, at most 64 characters. Defaults to the id title-cased. |
| `dir` | A directory inside the plugin (same path rules as a tab's `entry`) holding an [Omarchy theme](https://github.com/basecamp/omarchy): `colors.toml` (or a legacy `alacritty.toml`), an optional `light.mode` marker, and an optional `backgrounds/` of `.jpg`/`.jpeg`/`.png`/`.webp`/`.avif` wallpapers. The palette must parse, or the manifest is invalid. |

An enabled plugin's theme is a first-class lasso theme. It goes through the same registry as the official Omarchy themes and the ones installed from a URL, so it paints the chrome and the terminals, can be herdr's theme or an appearance palette, is written into every agent CLI's theme file, syncs across the fleet, and its wallpapers show in the backdrop gallery (served under `/omarchy/bg/<id>/<file>`, read through the plugin directory so a symlink out of it is never followed).

**Existing themes win.** The order is lasso's built-ins, then the official Omarchy themes, then themes installed from a URL, then plugins. A plugin theme whose id is already taken, or is another spelling of a taken one, is **skipped**, not an error: the rest of the plugin loads, and the listing carries a warning (`key_taken` on the theme, a line in `warnings`), shown in Settings and `lasso plugin list`. When two plugins claim one id, the plugin whose name sorts first wins and the other gets the warning.

Palettes are re-read on every rescan (every 10 seconds), so an edit to `colors.toml` shows up without a reload. If the edited theme is the one herdr is wearing, lasso rewrites herdr's config and re-syncs the fleet as if you had picked it again. Disabling the plugin withdraws the theme; a selection naming it is kept, and lasso keeps painting the palette it last wrote to herdr's config, the same as for a theme only a newer lasso knows.

## Fonts

```json
"fonts": [
  {
    "id": "space-mono",
    "family": "Space Mono",
    "category": "mono",
    "faces": [
      { "file": "fonts/space-mono-latin-400-normal.woff2", "weight": 400, "style": "normal" },
      { "file": "fonts/space-mono-latin-700-normal.woff2", "weight": 700, "style": "normal" }
    ],
    "license": "OFL-1.1"
  }
]
```

| field | |
|---|---|
| `id` | `^[a-z][a-z0-9-]{0,31}$`, unique within the plugin. The font's global id is `plugin:<name>:<id>`. |
| `family` | `^[A-Za-z0-9 _-]{1,64}$`. The CSS family name lasso declares the faces under. |
| `category` | `sans`, `serif`, `display` or `mono`. |
| `faces` | 1-8 faces. `file` is a file inside the plugin ending in `.woff2`, `.woff`, `.ttf` or `.otf`, at most 5 MB; `weight` is 100-900 in steps of 100; `style` is `normal` or `italic`. |
| `license` | Optional, at most 64 characters, shown in Settings. Ship the license text in the plugin too. |

Face files are served from `/plugins/<name>/<file>` with their font Content-Type, and only while the plugin is enabled. Each font becomes an option in Settings' **Typography** section, one choice per slot:

| slot | where it applies | offers |
|---|---|---|
| Interface (`sans`) | body and UI text | every font |
| Display (`display`) | display headings | every font |
| Labels (`label`) | the small all-caps labels | every font |
| Code (`mono`) | code, the file viewer and editor, diffs | `mono` fonts only |
| Terminal (`terminal`) | every terminal | `mono` fonts only |

The choice is lasso's `ui_state.typography` (`{sans, display, label, mono, terminal}`, each a global id or absent), so every browser on the same lasso follows it. Each slot is saved on its own, so two devices changing two slots do not overwrite each other. Your family always goes first, with the slot's usual stack behind it, so a missing glyph or a slow load falls back to the normal look. The terminal keeps its Nerd Font as the fallback, so the icons TUIs draw still render. If a plugin is disabled, slots naming its fonts fall back to lasso's default and come back when it is re-enabled.

### Why no CSS

A plugin cannot ship a stylesheet, and that is deliberate. CSS that could restyle lasso could also hide the warnings in the plugin approval dialog or paint a fake prompt over the terminal. So a plugin supplies values, and lasso writes every line of CSS itself from values it has checked. The family regex is what keeps a family name from breaking out of the `@font-face` rule it is written into. Colours come from a palette lasso parses into its own theme model. There is no field that reaches the page as raw CSS.

## MCP tools

`mcp.command` must be an MCP server over **stdio**: newline-delimited JSON-RPC on stdin/stdout, with logs on stderr. lasso keeps one long-lived instance per plugin. It lists the tools and mirrors each onto `/mcp` as `<plugin>__<tool>`, with the description prefixed `[plugin <name>] `. Arguments and results pass through untouched. A mirrored name must fit `^[a-zA-Z0-9_-]{1,64}$`, and a tool whose input schema is not an object is skipped, with a line in lasso's log.

- Plugin tools run on lasso's machine. A caller whose MCP credential does not reach lasso's own machine (a per-host `self`-scoped client for another host) is refused.
- If the server dies, lasso removes its tools and restarts it with backoff (1s, doubling to 60s), then re-adds them. A restart recreates the sandbox. A death is noticed at once: the `isb exec` lasso talks through exits when the server inside does.
- A server that cannot start for a reason a retry will not fix goes `unavailable` until you act. The causes are no isb 1.0 or later on the machine, `isb serve` not running, and a secret that did not resolve. Fix the cause, then use Restart (or `lasso plugin restart <name>`).

## The sandbox

An untrusted plugin's server runs in an isb sandbox named `lasso-plugin-<name>`, labelled `owner=lasso` and `lasso.plugin=<name>`, outside every isb org. lasso creates it as

```
isb create -i docker:<image> --idmap auto -l owner=lasso -l lasso.plugin=<name> \
  -c 'oci.entrypoint=sh -c "sleep infinity || exec tail -f /dev/null"' \
  -v <plugin dir>:/plugin:ro -v <data dir>:/data \
  [-e K=V ...] -e LASSO_PLUGIN_DATA=/data \
  (--egress none | --egress <host:port> ...) \
  [--secret NAME=lasso-plugin-<name>.<name lowercased>@host,host ...] \
  lasso-plugin-<name>
```

(in a VM: `-i <vm_image or images:ubuntu/24.04/cloud> --vm`, no idmap and no entrypoint), and talks to the server through

```
isb exec -i -T -w /plugin -u 1000:1000 lasso-plugin-<name> -- <command...>
```

whose stdin and stdout are the server's. Every start first removes a sandbox of that name (a crashed lasso leaves one behind), and every stop removes it again. Shutdown removes them all before lasso exits.

- **Isolation.** A container shares your machine's kernel but has its own users (root inside is an unprivileged uid outside), filesystem and network. A VM has its own kernel too. The plugin directory is read-only, its data directory is the only writable mount, and nothing else from your machine is mounted.
- **The image needs `sh` and `sleep` or `tail`.** An OCI image's process is the container's init, and an image's own default command (python's REPL, say) exits at once without a terminal, taking the container with it. So the init is `sleep infinity` (`tail -f /dev/null` where `sleep` cannot take `infinity`), and the server runs beside it through `isb exec`. A distroless image without a shell does not work.
- **Egress** is isb's: the sandbox has no network of its own, only a proxy run by `isb serve` that lets through connections to the approved names. In isb's words: the guest can send packets only to the proxy; it resolves only the allowed names (everything else is NXDOMAIN); and the proxy lets a connection through only if the name the client says is on the list, on the port it connected to, then connects to that name as the host resolves it, never to an address the guest chose. With no network entries and no secrets the sandbox has **no network at all**. A secret's hosts are on the list too (port 443).
- **What egress does not guarantee.** "The name is the client's word": the proxy does not decrypt TLS it only passes through, so it trusts the SNI or `Host` the client sends, and code that can reach one allowed host on a shared front end (a CDN) can ask that front end for another site it hosts. Allow only hosts you would trust with the traffic. Code inside can also **use** a secret against its approved hosts: the secret protects the value, not the account's powers.
- **Secrets never enter the guest.** lasso resolves each approved secret from its own environment, or failing that from `secret NAME` (10s timeout), and writes it on stdin (never argv) into isb's secret store as `lasso-plugin-<name>.<secret name lowercased>`, overwriting it on every start. The guest's `NAME` holds a placeholder (`isb_placeholder_...`). isb's proxy swaps it for the value only on TLS connections to that secret's approved hosts, verified against the real certificate, and turns the value back into the placeholder in anything those hosts send back. The store entries are deleted when the sandbox is (stop, disable, shutdown); uninstall and unlink also delete any a crashed lasso left. Toward a secret's hosts isb speaks **HTTP/1.1 only** (so gRPC fails), a client that **pins the host's certificate fails**, the request line and headers are rewritten but a body is not, and plain HTTP to an approved host carries the placeholder, never the value. isb installs its per-sandbox CA in the guest's system store and points `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO` and `NODE_EXTRA_CA_CERTS` at it; a runtime with a trust store of its own must be told to trust `/etc/isb/egress-ca.crt`.
- **VMs and `/data`.** In a VM the data directory is shared over virtiofs with no uid translation: a file the guest's root creates there is owned by root on your machine, setuid bit included. The server runs as uid 1000, which the default VM image gives no sudo, so only a privilege escalation inside the VM can do that. Treat `<data dir>` as untrusted: never execute anything from it on the host.
- **Logs.** The server's stderr (and `isb create`'s progress lines) stream into lasso's log and a ring of the last 500 lines per plugin, kept across restarts. `lasso plugin log <name>` (or Settings' Logs button) shows it; `-f` follows it. When a server dies, its last lines are in the status.
- **Cost.** A container starts in seconds; a VM boots its own kernel and takes considerably longer. A first start also downloads its image; lasso allows ten minutes for a launch.

### Setting up isb

lasso needs **isb 1.0 or later** and **`isb serve` running as the same user as lasso** (it runs the egress proxy, and the CLI and the daemon share each sandbox's CA through `$XDG_STATE_HOME/isb`). isb is found through `LASSO_ISB` (a path or a name on PATH, or `off` to disable sandboxed plugins; when set, nothing else is tried), else the first of `isb` on PATH and the newest `~/.local/share/mise/installs/github-execution-associates-isb/*/isb` that is new enough. lasso checks the daemon's `/healthz` on its unix socket (`$ISB_SERVE_SOCKET`, else `$XDG_RUNTIME_DIR/isb/serve.sock`). On a host with ufw, the proxy also needs

```sh
sudo isb host setup --sandbox-egress
```

Without a usable isb, tabs still work, and every untrusted MCP server reads `unavailable` with the reason (`isb 1.0 or later is required (found 0.7.0 at …)`, `isb serve is not running …`).

### Two lassos on one plugins directory

Only one lasso at a time runs the MCP servers for a plugins directory: whichever holds the lock file `plugins/.runner.lock`. A second lasso using the same `LASSO_DIR` (a development build beside the production one) still serves every plugin's tabs, themes and fonts, and lists each MCP server as `unavailable`, naming the lasso that runs it. When that lasso stops, the other takes the servers over within a rescan (10 seconds). Without this the two would keep deleting each other's sandboxes, which share one name per plugin.

## HTTP API

These are for the Settings pane and the CLI. They are behind `UI_AUTH` like the rest of the UI.

| | |
|---|---|
| `GET /api/plugins` | `{ dir, sandbox: {kind: "isb", available, path?, version?, serve_running, reason?}, plugins: [...] }`. Each plugin carries `trusted`, `vm` (the operator's choice), `isolation` (`host`, `container` or `vm`: trusted wins), `permissions.mcp.vm_image?`, `themes: [{id, label, key_taken?}]`, `fonts: [{id, global_id, family, category, license?, faces?: [{url, weight, style}]}]` (`faces` only while enabled), `warnings: [string]`, and `permissions.themes` / `permissions.fonts`. |
| `POST /api/plugins/reload` | Rescan the directory and return the listing. |
| `POST /api/plugins/<name>/enable` | Approve the current permissions. The optional body `{fingerprint}` is refused with 409 if the manifest has changed since that listing; the Settings dialog always sends the fingerprint of what it showed. |
| `POST /api/plugins/<name>/disable` | |
| `POST /api/plugins/<name>/trust` | `{trusted: bool}` |
| `POST /api/plugins/<name>/isolation` | `{vm: bool}`. A flip restarts the server; stored, but without effect, while trusted. |
| `POST /api/plugins/<name>/restart` | |
| `POST /api/plugins/<name>/call` | `{tool, arguments}`. The tool must be this plugin's own. Returns the `CallToolResult`. |
| `POST /api/plugins/install/preview` | `{source, ref?}` → `{token, name, version, description, source, ref, commit, fingerprint, permissions, themes, fonts, warnings}`. Clones into staging; installs nothing. |
| `POST /api/plugins/install/confirm` | `{token, fingerprint, enable}` → the listing. 409 if `fingerprint` is not the staged manifest's; 404 for an unknown or expired token. |
| `POST /api/plugins/install/cancel` | `{token}` → `{ok: true}`. Discards an install **or update** preview. |
| `POST /api/plugins/link` | `{path, enable?}` → the listing. `enable` approves the manifest as read at that moment. |
| `POST /api/plugins/<name>/unlink` | Linked plugins only. |
| `POST /api/plugins/<name>/uninstall` | `{purge_data?}`. GitHub installs only. |
| `POST /api/plugins/<name>/update/preview` | The install preview plus `current_commit` and `changes_permissions`. GitHub installs only. |
| `POST /api/plugins/<name>/update/confirm` | `{token, fingerprint}` → the listing. 409/404 as for install. |
| `GET /api/plugins/<name>/log?lines=200` | `{name, sandboxed, lines: [string], note?}`. `note` says why there is nothing (no MCP server, no usable isb, nothing logged since lasso started). |

Each plugin in the listing also carries `source: {kind: "github"|"linked"|"local", source?, ref?, commit?, installed_at?, updated_at?, path?, url?}` (`url` is the GitHub tree at the installed commit) and `data_dir`. Refusals other than those named (a bad source, a failed clone, a name that is taken, the wrong kind of plugin for the operation) are 400 with the reason as the body.

Every change bumps `plugins_rev` in the `/api/events` frames, so every open tab refetches the listing. It also bumps when the plugin themes change (a palette edited in place included), which is what makes browsers refetch the theme catalog (`GET /api/omarchy-themes`, where a plugin theme has `source: "plugin"` and `plugin: "<name>"`).

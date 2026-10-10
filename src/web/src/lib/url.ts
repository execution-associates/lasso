// Small helpers for reflecting app state in the URL query string (e.g.
// ?host=minime). We use real query params rather than the hash so links are
// conventional and the fragment stays free. Updates use replaceState so they
// don't pile up history entries on every host change.
//
// Only state lasso OWNS belongs here: the active host, and which face of the
// left column this tab shows (the PATH: / terminal, /chat, /agents, /bots…,
// and /view/<plugin>/<id> for a plugin's main view). herdr's
// focused pane does not — it is one global per herdr session, shared
// with the TUI and every other lasso client, so a URL that named it would let a
// browser Back re-point it for everyone (and a shared link steal focus on open).

export function getQueryParam(key: string): string | null {
  return new URLSearchParams(window.location.search).get(key)
}

// setQueryParam sets (or, when value is null/empty, removes) one query param,
// leaving the path and other params untouched. The hash is intentionally
// dropped — we've migrated off fragment-based state. Uses replaceState so a
// plain host change doesn't pile up history entries.
export function setQueryParam(key: string, value: string | null) {
  writeQueryParams({ [key]: value })
}

// setQueryParams sets (or removes, when null/empty) several params in one
// history operation — so clearing the params we no longer honor is a single
// replace rather than one per key.
export function setQueryParams(params: Record<string, string | null>) {
  writeQueryParams(params)
}

function writeQueryParams(params: Record<string, string | null>) {
  const url = new URL(window.location.href)
  for (const [key, value] of Object.entries(params)) {
    if (value == null || value === "") url.searchParams.delete(key)
    else url.searchParams.set(key, value)
  }
  const qs = url.searchParams.toString()
  window.history.replaceState(null, "", url.pathname + (qs ? `?${qs}` : ""))
}

// The left column's face lives in the path, one history entry per change, so
// Back from a chat opened off the grid returns to the grid. Only the VIEW is
// named: /chat is "the chat of whatever pane herdr has focused", never a pane,
// for the reason above. A plugin's view is its global id,
// `plugin:<name>:<id>`, at /view/<name>/<id>; whether that plugin is enabled
// is the App's question (it falls back to the terminal), not the URL's.
export type BuiltinLeftView = "terminal" | "chat" | "agents" | "bots"
export type LeftView = BuiltinLeftView | `plugin:${string}:${string}`

const viewPaths: Record<BuiltinLeftView, string> = {
  terminal: "/",
  chat: "/chat",
  agents: "/agents",
  bots: "/bots",
}

// The Bots view has pages of its own under /bots, and unlike /chat they name
// lasso's OWN things (a bot is a row in lasso.db, not herdr's focus), so a
// link to one is fine to share and a Back between them is fine to take:
//
//	/bots                    the list (on a phone; at md+ it is beside a chat)
//	/bots/<name>             that bot's conversation
//	/bots/<name>/settings    its settings
//	/bots/manage             every bot as a table
//	/bots/manage/new         the creator
//
// "manage" is the one word a bot's name cannot claim here: a bot called that
// is reached from the list, not by its path.
export type BotsRoute =
  | { page: "list" }
  | { page: "chat"; name: string }
  | { page: "settings"; name: string }
  | { page: "manage" }
  | { page: "new" }

// Bot names are ^[a-z0-9][a-z0-9-]{0,39}$ (bots.go), so a segment needs no
// escaping and the pattern is the whole check.
const botsPath = /^\/bots(?:\/([a-z0-9][a-z0-9-]{0,39})(?:\/(settings|new))?)?$/

export function parseBotsPath(path: string): BotsRoute | null {
  const m = botsPath.exec(path.replace(/\/+$/, ""))
  if (!m) return null
  const [, name, sub] = m
  if (!name) return { page: "list" }
  if (name === "manage") {
    if (sub === "new") return { page: "new" }
    return sub ? null : { page: "manage" }
  }
  if (sub === "new") return null
  return sub ? { page: "settings", name } : { page: "chat", name }
}

export function botsRoutePath(r: BotsRoute): string {
  switch (r.page) {
    case "list":
      return "/bots"
    case "chat":
      return `/bots/${r.name}`
    case "settings":
      return `/bots/${r.name}/settings`
    case "manage":
      return "/bots/manage"
    case "new":
      return "/bots/manage/new"
  }
}

// Where the Bots view last was, so leaving for the terminal and coming back
// (the view menu, Back) lands on the same page rather than its list. Seeded
// from the URL on load and on every popstate that names a bots page.
let lastBotsPath = "/bots"

export function botsRouteNow(): BotsRoute {
  return parseBotsPath(lastBotsPath) ?? { page: "list" }
}

// writeBotsRoute moves within the Bots view: a history entry per page (push),
// or a replace for a correction the reader did not ask for.
export function writeBotsRoute(r: BotsRoute, push: boolean) {
  const path = botsRoutePath(r)
  lastBotsPath = path
  if (window.location.pathname === path) return
  const url = path + window.location.search
  if (push) window.history.pushState(null, "", url)
  else window.history.replaceState(null, "", url)
}

// Plugin and view ids are both ^[a-z][a-z0-9-]{0,31}$ (plugins.go), so a
// path segment needs no escaping and the pattern is the whole check.
const pluginViewPath =
  /^\/view\/([a-z][a-z0-9-]{0,31})\/([a-z][a-z0-9-]{0,31})$/

export function isPluginView(v: LeftView): v is `plugin:${string}:${string}` {
  return v.startsWith("plugin:")
}

function pathOf(view: LeftView): string {
  if (isPluginView(view)) {
    const [, name, id] = view.split(":")
    return `/view/${name}/${id}`
  }
  if (view === "bots") return lastBotsPath
  return viewPaths[view]
}

export function leftViewFromPath(): LeftView {
  const p = window.location.pathname.replace(/\/+$/, "")
  if (p === viewPaths.chat) return "chat"
  if (p === viewPaths.agents) return "agents"
  if (parseBotsPath(p)) {
    lastBotsPath = p
    return "bots"
  }
  const m = pluginViewPath.exec(p)
  if (m) return `plugin:${m[1]}:${m[2]}`
  return "terminal"
}

// writeLeftView puts the view in the path, keeping the query (?host=). push
// adds a history entry; the first sync of a page load replaces instead, so an
// unknown path is normalized without leaving a Back step to it.
export function writeLeftView(view: LeftView, push: boolean) {
  const path = pathOf(view)
  if (window.location.pathname === path) return
  const url = path + window.location.search
  if (push) window.history.pushState(null, "", url)
  else window.history.replaceState(null, "", url)
}

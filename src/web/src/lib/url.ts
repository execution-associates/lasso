// Small helpers for reflecting app state in the URL query string (e.g.
// ?host=minime). We use real query params rather than the hash so links are
// conventional and the fragment stays free. Updates use replaceState so they
// don't pile up history entries on every host change.
//
// Only state lasso OWNS belongs here: the active host, and which face of the
// left column this tab shows (the PATH: / terminal, /chat, /agents). herdr's
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
// for the reason above.
export type LeftView = "terminal" | "chat" | "agents"

const viewPaths: Record<LeftView, string> = {
  terminal: "/",
  chat: "/chat",
  agents: "/agents",
}

export function leftViewFromPath(): LeftView {
  const p = window.location.pathname.replace(/\/+$/, "")
  if (p === viewPaths.chat) return "chat"
  if (p === viewPaths.agents) return "agents"
  return "terminal"
}

// writeLeftView puts the view in the path, keeping the query (?host=). push
// adds a history entry; the first sync of a page load replaces instead, so an
// unknown path is normalized without leaving a Back step to it.
export function writeLeftView(view: LeftView, push: boolean) {
  const path = viewPaths[view]
  if (window.location.pathname === path) return
  const url = path + window.location.search
  if (push) window.history.pushState(null, "", url)
  else window.history.replaceState(null, "", url)
}

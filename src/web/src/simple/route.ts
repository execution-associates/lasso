import { lsGet, lsSet } from "@/lib/app-store"

// The simple app lives under /m. Its path names the view, and for a chat the
// agent or bot it reads, so Back and a reload land where the human was.
export type SimpleRoute =
  | { view: "inbox" }
  | { view: "agents" }
  | { view: "agent"; host: string; pane: string }
  | { view: "bots" }
  | { view: "bot"; name: string }
  | { view: "settings" }

export const SIMPLE_BASE = "/m"

export function isSimplePath(path: string): boolean {
  return path === SIMPLE_BASE || path.startsWith(`${SIMPLE_BASE}/`)
}

export function parseSimplePath(path: string): SimpleRoute {
  const parts = path
    .slice(SIMPLE_BASE.length)
    .split("/")
    .filter(Boolean)
    .map(decodeURIComponent)
  switch (parts[0]) {
    case "agents":
      if (parts.length >= 3)
        return { view: "agent", host: parts[1], pane: parts[2] }
      return { view: "agents" }
    case "bots":
      if (parts[1]) return { view: "bot", name: parts[1] }
      return { view: "bots" }
    case "settings":
      return { view: "settings" }
    default:
      return { view: "inbox" }
  }
}

export function simplePath(r: SimpleRoute): string {
  const e = encodeURIComponent
  switch (r.view) {
    case "inbox":
      return SIMPLE_BASE
    case "agents":
      return `${SIMPLE_BASE}/agents`
    case "agent":
      return `${SIMPLE_BASE}/agents/${e(r.host)}/${e(r.pane)}`
    case "bots":
      return `${SIMPLE_BASE}/bots`
    case "bot":
      return `${SIMPLE_BASE}/bots/${e(r.name)}`
    case "settings":
      return `${SIMPLE_BASE}/settings`
  }
}

// Simple or advanced is a choice about the screen in front of someone, so it
// is kept per device: a phone set to simple must not turn the laptop's lasso
// into the phone app, which a shared ui_state field would. Unset means the
// default: simple on a phone, the full UI everywhere else.
const UI_KEY = "lasso-ui"

export type UIChoice = "simple" | "advanced"

export function uiChoice(): UIChoice | null {
  const v = lsGet(UI_KEY)
  return v === "simple" || v === "advanced" ? v : null
}

export function isPhone(): boolean {
  return window.matchMedia("(max-width: 767px) and (pointer: coarse)").matches
}

export function wantsSimple(): boolean {
  return (uiChoice() ?? (isPhone() ? "simple" : "advanced")) === "simple"
}

// Switching records the choice for this device and loads the other app.
export function switchUI(to: UIChoice) {
  lsSet(UI_KEY, to)
  window.location.assign(to === "simple" ? SIMPLE_BASE : "/")
}

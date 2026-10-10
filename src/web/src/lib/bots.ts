import { keepPreviousData, useQuery } from "@tanstack/react-query"
import * as React from "react"

import { api, type BotState, type BotView } from "@/lib/api"
import { lsGet, lsSet } from "@/lib/app-store"
import { queryClient } from "@/lib/query"

// The Bots view's shared state: the list (polled while the view is on screen),
// how a bot is drawn when it has no avatar of its own, and which bots have
// said something this viewer has not seen.

export const botsKey = ["bots"] as const
export const botKey = (name: string) => ["bot", name] as const

// useBots polls the list every 3s while `active`: a bot's state (working,
// blocked) and its newest line move without anything pushing them, which is
// the same reason useAgents polls the fleet.
export function useBots(active: boolean) {
  return useQuery({
    queryKey: botsKey,
    queryFn: () => api.bots.list(),
    refetchInterval: active ? 3000 : false,
    refetchIntervalInBackground: false,
    placeholderData: keepPreviousData,
    staleTime: 0,
  })
}

// After any change made from this tab (create, save, start, stop, delete):
// the list and the one bot's detail are both stale. Resolves once the list
// has been read again, which a page about a NEW bot has to wait for (the view
// sends a link to a bot the list does not have back to the list).
export async function invalidateBots(name?: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: botsKey }),
    name ? queryClient.invalidateQueries({ queryKey: botKey(name) }) : null,
  ])
}

export const BOT_NAME_RE = /^[a-z0-9][a-z0-9-]{0,39}$/

// Claude Code's permission modes, as bots.go accepts them ("" = claude's own
// default).
export const BOT_PERMISSION_MODES: { value: string; label: string }[] = [
  { value: "", label: "default" },
  { value: "acceptEdits", label: "acceptEdits" },
  { value: "auto", label: "auto" },
  { value: "plan", label: "plan" },
  { value: "dontAsk", label: "dontAsk" },
  { value: "manual", label: "manual" },
  { value: "bypassPermissions", label: "bypassPermissions" },
]

export function botRunning(b: BotView): boolean {
  return b.state !== "stopped"
}

// What the avatar circle shows: the bot's own (an emoji or a few letters),
// else its initial.
export function botAvatarText(b: Pick<BotView, "avatar" | "name">): string {
  const a = b.avatar?.trim()
  if (a) return a
  return (b.name.charAt(0) || "?").toUpperCase()
}

// A hue from the name, so a bot with no avatar keeps one colour wherever it is
// drawn and two bots rarely share one. Saturation and lightness are fixed and
// moderate so white text reads on it under every theme.
export function botHue(name: string): number {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0
  return h % 360
}

export function stateLabel(state: BotState, waiting?: string): string {
  switch (state) {
    case "stopped":
      return "Stopped"
    case "starting":
      return "Starting…"
    case "working":
      return "Working"
    case "blocked":
      return waiting ? `Needs your input · ${waiting}` : "Needs your input"
    default:
      return "Idle"
  }
}

// relativeTime says how long ago, in the coarse steps a chat list uses.
export function relativeTime(iso: string | undefined, now: number): string {
  if (!iso) return ""
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ""
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 60) return "now"
  const m = Math.round(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.round(m / 60)
  if (h < 24) return `${h}h`
  const d = Math.round(h / 24)
  if (d < 7) return `${d}d`
  return new Date(t).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

// A clock that ticks once a minute, for relative times that must not freeze.
export function useMinuteClock(): number {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 60_000)
    return () => clearInterval(t)
  }, [])
  return now
}

// Unread is per VIEWER: the newest message time this browser has seen in each
// bot's conversation. It is a convenience, not shared state — another device
// keeps its own — so it lives in localStorage (lsGet/lsSet already swallow a
// private window's refusal) rather than ui_state.
const SEEN_KEY = "lasso-bots-seen"

function readSeen(): Record<string, string> {
  try {
    const v = JSON.parse(lsGet(SEEN_KEY) ?? "{}")
    return v && typeof v === "object" ? (v as Record<string, string>) : {}
  } catch {
    return {}
  }
}

const seenListeners = new Set<() => void>()
let seen = readSeen()

export function markBotSeen(name: string, at: string | undefined) {
  if (!at || seen[name] === at) return
  seen = { ...seen, [name]: at }
  lsSet(SEEN_KEY, JSON.stringify(seen))
  for (const l of seenListeners) l()
}

export function useBotsSeen(): Record<string, string> {
  return React.useSyncExternalStore(
    React.useCallback((cb: () => void) => {
      seenListeners.add(cb)
      return () => seenListeners.delete(cb)
    }, []),
    () => seen
  )
}

// Unread: the bot spoke (or something reached it) after the last time this
// viewer had its conversation open. A bot this browser has never listed
// starts read (seedBotsSeen), so a first visit is not a wall of dots.
export function botUnread(b: BotView, seenMap: Record<string, string>) {
  if (!b.last_at || b.last_kind === "user") return false
  const s = seenMap[b.name]
  if (!s) return false
  return Date.parse(b.last_at) > Date.parse(s)
}

export function seedBotsSeen(bots: BotView[]) {
  const missing = bots.filter((b) => b.last_at && !seen[b.name])
  if (missing.length === 0) return
  seen = { ...seen }
  for (const b of missing) seen[b.name] = b.last_at as string
  lsSet(SEEN_KEY, JSON.stringify(seen))
  for (const l of seenListeners) l()
}

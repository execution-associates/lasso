import { useQuery } from "@tanstack/react-query"

import {
  type AlertChannel,
  api,
  type BotHealth,
  type BotHold,
  type BotView,
} from "@/lib/api"
import { invalidateBots } from "@/lib/bots"
import { qk, queryClient } from "@/lib/query"

// Bot liveness alerts, the client half: the shared queries, which health
// states are worth a mark, and how a hold or a duration reads.

export function useAlertChannels(enabled = true) {
  return useQuery({
    queryKey: qk.alertChannels,
    queryFn: () => api.alerts.channels(),
    enabled,
  })
}

export function useAlertKinds(enabled = true) {
  return useQuery({
    queryKey: qk.alertKinds,
    queryFn: () => api.alerts.kinds(),
    enabled,
    staleTime: Number.POSITIVE_INFINITY,
  })
}

export function useAlertSettings(enabled = true) {
  return useQuery({
    queryKey: qk.alertSettings,
    queryFn: () => api.alerts.settings(),
    enabled,
  })
}

export function invalidateAlerts() {
  return queryClient.invalidateQueries({ queryKey: qk.alerts })
}

// A heartbeat is bound to its bot and never routed, so the default list and a
// bot's own list offer every other channel only.
export function routableChannels(list?: AlertChannel[]): AlertChannel[] {
  return (list ?? []).filter((c) => c.kind !== "heartbeat")
}

// The states a mark is drawn for: ok, starting and stopped say nothing the
// bot's own state mark does not.
export function healthBad(h?: BotHealth): boolean {
  return (
    !!h &&
    (h.state === "gone" ||
      h.state === "shell" ||
      h.state === "deaf" ||
      h.state === "unknown")
  )
}

export function healthLabel(h: BotHealth): string {
  switch (h.state) {
    case "gone":
      return "Down"
    case "shell":
      return "Shell"
    case "deaf":
      return "Not listening"
    case "unknown":
      return "Unknown"
    case "ok":
      return "Healthy"
    case "starting":
      return "Starting"
    case "stopped":
      return "Stopped"
  }
}

// Unknown is a host lasso could not read, not a bot known to be broken.
export function healthTone(h: BotHealth): "bad" | "warn" {
  return h.state === "unknown" ? "warn" : "bad"
}

export function holdText(h?: BotHold): string {
  if (!h?.held) return ""
  const until = h.until
    ? `until ${new Date(h.until).toLocaleString(undefined, {
        dateStyle: "short",
        timeStyle: "short",
      })}`
    : "until released"
  return h.reason ? `Held ${until}: ${h.reason}` : `Held ${until}`
}

// The hold calls take effect at once (not through the settings save), so
// both the bot queries and the alerts overview are re-read after one.
export async function holdBot(
  name: string,
  body: { minutes?: number; until?: string; reason?: string } = {}
) {
  await api.bots.hold(name, body)
  await Promise.all([invalidateBots(name), invalidateAlerts()])
}

export async function releaseBot(name: string) {
  await api.bots.release(name)
  await Promise.all([invalidateBots(name), invalidateAlerts()])
}

export function botHeld(b: BotView): boolean {
  return !!b.hold?.held
}

// Seconds as the shortest honest phrase: 90 → "1m 30s", 3600 → "1h".
export function formatSeconds(sec: number): string {
  if (!sec) return "0s"
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  return (
    [d && `${d}d`, h && `${h}h`, m && `${m}m`, s && `${s}s`]
      .filter(Boolean)
      .join(" ") || "0s"
  )
}

// The forms edit durations in minutes; the API speaks seconds.
export function minutesOf(sec: number | undefined): string {
  if (sec === undefined) return ""
  return String(Math.round((sec / 60) * 100) / 100)
}

// undefined for an empty or unusable field (inherit / leave unchanged).
export function secondsOf(min: string): number | undefined {
  const t = min.trim()
  if (t === "") return undefined
  const n = Number(t)
  if (!Number.isFinite(n) || n < 0) return undefined
  return Math.round(n * 60)
}

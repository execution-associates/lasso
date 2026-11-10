import { useQuery } from "@tanstack/react-query"

import { type AppUser, api } from "@/lib/api"
import { queryClient } from "@/lib/query"

// The People screens' shared state and rules: the people who use the phone
// app (clientapi_admin.go), the checks the API makes restated for the form, and
// how a pairing link's base is judged.

export const peopleKey = ["app-users"] as const

export function usePeople(active = true) {
  return useQuery({
    queryKey: peopleKey,
    queryFn: () => api.appUsers.list(),
    refetchInterval: active ? 5000 : false,
    refetchIntervalInBackground: false,
    staleTime: 0,
  })
}

export function invalidatePeople() {
  return queryClient.invalidateQueries({ queryKey: peopleKey })
}

export const PERSON_NAME_MAX = 64

// clientHostileRune (clientapi_store.go): controls, invisible formatting,
// line and paragraph separators, private-use and surrogate code points, and
// the characters of markup and quoting.
const HOSTILE = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\p{Co}\p{Cs}<>"\\`]/u

// personNameProblem is cleanLine's rule: 1 to 64 characters after trimming,
// none of them hostile. "" means the name is fine.
export function personNameProblem(name: string): string {
  const n = name.trim()
  if (n === "") return "Enter a name."
  if ([...n].length > PERSON_NAME_MAX)
    return `Keep the name to ${PERSON_NAME_MAX} characters.`
  if (HOSTILE.test(n))
    return "Use plain text: no line breaks, quotes, backslashes, backticks or angle brackets."
  return ""
}

export function personEmailProblem(email: string): string {
  const e = email.trim()
  if (e === "") return ""
  if ([...e].length > 254 || HOSTILE.test(e) || !e.includes("@"))
    return "That is not an email address."
  return ""
}

// pairingBaseProblem is pairingBase's rule: an origin, scheme and host only.
export function pairingBaseProblem(base: string): string {
  let u: URL
  try {
    u = new URL(base.trim())
  } catch {
    return "Enter an address such as https://lasso.example.com."
  }
  if (u.protocol !== "https:" && u.protocol !== "http:")
    return "Use an http or https address."
  if (u.username || u.password || u.search || u.hash)
    return "Give the address only: scheme and host, nothing after."
  if (u.pathname !== "/")
    return "Give the address only: scheme and host, nothing after."
  return ""
}

export function normalizeBase(base: string): string {
  return base.trim().replace(/\/+$/, "")
}

export type BaseWarning = {
  // "blocked": a phone cannot reach it at all; "caution": only some phones can.
  level: "blocked" | "caution"
  text: string
}

// pairingBaseWarning says whether a phone can be expected to reach the address
// the link will point at. Loopback and unspecified addresses never work from a
// phone; private, link-local, tailnet and .local names work on that network
// only; plain http is the owner's call to make but worth saying.
export function pairingBaseWarning(base: string): BaseWarning | null {
  let u: URL
  try {
    u = new URL(base.trim())
  } catch {
    return null
  }
  const host = u.hostname.replace(/^\[|\]$/g, "").toLowerCase()
  if (
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "::1" ||
    host === "::" ||
    /^127\./.test(host) ||
    host === "0.0.0.0"
  )
    return {
      level: "blocked",
      text: "This address points back at the machine it is typed on, so a phone cannot open it. Use the address your phone reaches lasso at (a public name, or your tailnet name).",
    }
  const octets = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/)
  if (octets) {
    const a = Number(octets[1])
    const b = Number(octets[2])
    if (
      a === 10 ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      (a === 169 && b === 254)
    )
      return {
        level: "caution",
        text: "This is a private network address: the phone has to be on the same network to reach it.",
      }
    if (a === 100 && b >= 64 && b <= 127)
      return {
        level: "caution",
        text: "This is a tailnet address: the phone has to be signed in to the tailnet to reach it.",
      }
  }
  if (/^f[cd][0-9a-f]{2}:/.test(host) || /^fe80:/.test(host))
    return {
      level: "caution",
      text: "This is a private network address: the phone has to be on the same network to reach it.",
    }
  if (host.endsWith(".ts.net"))
    return {
      level: "caution",
      text: "This is a tailnet name: the phone has to be signed in to the tailnet to reach it.",
    }
  if (host.endsWith(".local") || !host.includes("."))
    return {
      level: "caution",
      text: "This name only resolves on the local network: the phone has to be on it too.",
    }
  if (u.protocol === "http:")
    return {
      level: "caution",
      text: "This address is not https, so the link and the phone's connection are not encrypted.",
    }
  return null
}

// The origin the owner is using, as the default for a link.
export function ownOrigin(): string {
  return window.location.origin
}

export function activeDevices(u: AppUser) {
  return u.devices.filter((d) => !d.revoked_at)
}

// The newest time any of the person's live devices spoke to lasso, or "".
export function lastSeen(u: AppUser): string {
  let best = ""
  for (const d of activeDevices(u)) {
    if (d.last_seen_at && d.last_seen_at > best) best = d.last_seen_at
  }
  return best
}

// m:ss for a countdown.
export function clock(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`
}

export function platformLabel(p: string): string {
  return p === "ios" ? "iPhone / iPad" : p === "android" ? "Android" : "Phone"
}

// What a bot's client state means for the owner, or "" when nothing needs
// saying. Stopped and starting resolve by themselves; unavailable is the
// bot's own limit (clientapi_bot.go gives the reason).
export function botClientNote(
  client: { state: string; reason?: string } | undefined
): string {
  if (!client) return ""
  switch (client.state) {
    case "unavailable":
      return client.reason || "Cannot take messages from the phone app yet."
    case "stopped":
      return "Stopped: it answers once started."
    default:
      return ""
  }
}

// Bot job schedules (botjobs.go): 5-field cron, several expressions joined
// with ";". The Jobs tab never shows cron unless the human picks Custom: the
// builder writes it, and humanize() reads it back as a sentence. The server
// stays the authority on validity and next fire times (jobs/preview).

export type Repeat =
  | "every"
  | "daily"
  | "weekdays"
  | "weekly"
  | "monthly"
  | "custom"

export type Builder = {
  repeat: Repeat
  everyN: number
  everyUnit: "minutes" | "hours"
  // "HH:MM", 24-hour.
  times: string[]
  // 0 = Sunday.
  days: number[]
  dom: number
  // Custom mode's text.
  cron: string
}

// The intervals the builder offers: the ones cron can actually space evenly.
export const EVERY_MINUTES = [1, 2, 3, 5, 10, 15, 20, 30]
export const EVERY_HOURS = [1, 2, 3, 4, 6, 8, 12]

const DAY_LONG = [
  "Sundays",
  "Mondays",
  "Tuesdays",
  "Wednesdays",
  "Thursdays",
  "Fridays",
  "Saturdays",
]
const DAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"]
const DOW_NAMES = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"]

const SHORTHANDS: Record<string, string> = {
  "@hourly": "0 * * * *",
  "@daily": "0 0 * * *",
  "@midnight": "0 0 * * *",
  "@weekly": "0 0 * * 0",
  "@monthly": "0 0 1 * *",
  "@yearly": "0 0 1 1 *",
  "@annually": "0 0 1 1 *",
}

export function blankBuilder(): Builder {
  return {
    repeat: "daily",
    everyN: 15,
    everyUnit: "minutes",
    times: ["09:00"],
    days: [1],
    dom: 1,
    cron: "",
  }
}

// --- reading cron ------------------------------------------------------------

// A field's values, or null when it is not a plain list (a step or a range
// that does not read as a list).
function values(
  field: string,
  lo: number,
  hi: number,
  names?: string[]
): number[] | null {
  const out = new Set<number>()
  for (const item of field.split(",")) {
    const [range, step] = item.split("/")
    if (step !== undefined) return null
    const num = (s: string) => {
      const n = names?.indexOf(s.toLowerCase()) ?? -1
      const v = n >= 0 ? n + (lo === 1 ? 1 : 0) : Number(s)
      return Number.isInteger(v) && v >= lo && v <= hi ? v : Number.NaN
    }
    if (range === "*") return null
    if (range.includes("-")) {
      const [a, b] = range.split("-").map(num)
      if (Number.isNaN(a) || Number.isNaN(b) || a > b) return null
      for (let v = a; v <= b; v++) out.add(v)
    } else {
      const v = num(range)
      if (Number.isNaN(v)) return null
      out.add(v)
    }
  }
  return [...out].sort((a, b) => a - b)
}

type Expr = {
  min: string
  hour: string
  dom: string
  month: string
  dow: string
}

function split(cron: string): Expr[] | null {
  const out: Expr[] = []
  for (const raw of cron.split(";")) {
    let e = raw.trim().toLowerCase()
    if (!e) continue
    e = SHORTHANDS[e] ?? e
    const f = e.split(/\s+/)
    if (f.length !== 5) return null
    out.push({ min: f[0], hour: f[1], dom: f[2], month: f[3], dow: f[4] })
  }
  return out.length ? out : null
}

// What one expression means, when it is a shape a sentence can carry.
type Shape =
  | { kind: "every"; n: number; unit: "minutes" | "hours"; at: number }
  | { kind: "at"; days: Days; times: [number, number][] }

type Days =
  | { kind: "daily" }
  | { kind: "weekdays" }
  | { kind: "weekends" }
  | { kind: "week"; days: number[] }
  | { kind: "month"; doms: number[] }

function stepOf(field: string): number | null {
  if (field === "*") return 1
  const m = /^\*\/(\d+)$/.exec(field)
  return m ? Number(m[1]) : null
}

function shapeOf(e: Expr): Shape | null {
  const anyDay = e.dom === "*" && e.month === "*" && e.dow === "*"
  const minStep = stepOf(e.min)
  if (anyDay && e.hour === "*" && minStep !== null && 60 % minStep === 0)
    return { kind: "every", n: minStep, unit: "minutes", at: 0 }
  const hourStep = stepOf(e.hour)
  const mins = values(e.min, 0, 59)
  if (anyDay && hourStep !== null && 24 % hourStep === 0 && mins?.length === 1)
    return { kind: "every", n: hourStep, unit: "hours", at: mins[0] }
  if (e.month !== "*") return null
  const hours = values(e.hour, 0, 23)
  if (!mins || !hours || mins.length * hours.length > 12) return null
  const times: [number, number][] = []
  for (const h of hours) for (const m of mins) times.push([h, m])
  let days: Days
  if (e.dom === "*" && e.dow === "*") days = { kind: "daily" }
  else if (e.dom === "*") {
    let dows = values(e.dow, 0, 7, DOW_NAMES)
    if (!dows) return null
    dows = [...new Set(dows.map((d) => (d === 7 ? 0 : d)))].sort(
      (a, b) => a - b
    )
    const key = dows.join(",")
    if (dows.length === 7) days = { kind: "daily" }
    else if (key === "1,2,3,4,5") days = { kind: "weekdays" }
    else if (key === "0,6") days = { kind: "weekends" }
    else days = { kind: "week", days: dows }
  } else if (e.dow === "*") {
    const doms = values(e.dom, 1, 31)
    if (!doms || doms.length > 4) return null
    days = { kind: "month", doms }
  } else return null
  return { kind: "at", days, times }
}

const daysKey = (d: Days) =>
  d.kind === "week"
    ? `week:${d.days.join(",")}`
    : d.kind === "month"
      ? `month:${d.doms.join(",")}`
      : d.kind

// Every expression's shape, merged: one interval, or one set of days with
// all the times. null when the schedule is beyond a sentence.
function shapes(cron: string): Shape | null {
  const exprs = split(cron)
  if (!exprs) return null
  const parts = exprs.map(shapeOf)
  if (parts.some((p) => !p)) return null
  const list = parts as Shape[]
  if (list.length === 1) return list[0]
  if (list.some((p) => p.kind === "every")) return null
  const at = list as Extract<Shape, { kind: "at" }>[]
  const key = daysKey(at[0].days)
  if (at.some((p) => daysKey(p.days) !== key)) return null
  const seen = new Map<number, [number, number]>()
  for (const p of at) for (const t of p.times) seen.set(t[0] * 60 + t[1], t)
  const times = [...seen.entries()].sort((a, b) => a[0] - b[0]).map((e) => e[1])
  return { kind: "at", days: at[0].days, times }
}

// --- saying it -------------------------------------------------------------------

export function formatTime(h: number, m: number): string {
  if (m === 0 && h === 0) return "midnight"
  if (m === 0 && h === 12) return "noon"
  const suffix = h < 12 ? "AM" : "PM"
  const h12 = h % 12 === 0 ? 12 : h % 12
  return m === 0
    ? `${h12} ${suffix}`
    : `${h12}:${String(m).padStart(2, "0")} ${suffix}`
}

function joinAnd(xs: string[]): string {
  if (xs.length <= 1) return xs.join("")
  return `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`
}

function ordinal(n: number): string {
  const s =
    n % 100 >= 11 && n % 100 <= 13
      ? "th"
      : (["th", "st", "nd", "rd"][n % 10] ?? "th")
  return `${n}${s}`
}

export type Humanized = { long: string; short: string }

// humanize reads a schedule as a sentence ("Every day at 6 AM, noon and
// 6 PM") and a compact form ("Daily · 6 AM, noon, 6 PM"), or null when it is
// beyond one.
export function humanize(cron: string): Humanized | null {
  const s = shapes(cron)
  if (!s) return null
  if (s.kind === "every") {
    if (s.unit === "minutes") {
      const t = s.n === 1 ? "Every minute" : `Every ${s.n} minutes`
      return { long: t, short: t }
    }
    const base = s.n === 1 ? "Every hour" : `Every ${s.n} hours`
    const t =
      s.at === 0
        ? `${base}, on the hour`
        : `${base} at :${String(s.at).padStart(2, "0")}`
    return { long: t, short: t }
  }
  const times = s.times.map(([h, m]) => formatTime(h, m))
  let lead: string
  let short: string
  switch (s.days.kind) {
    case "daily":
      lead = "Every day"
      short = "Daily"
      break
    case "weekdays":
      lead = short = "Weekdays"
      break
    case "weekends":
      lead = short = "Weekends"
      break
    case "week":
      lead = joinAnd(s.days.days.map((d) => DAY_LONG[d]))
      short = s.days.days.map((d) => DAY_SHORT[d]).join(", ")
      break
    case "month":
      lead = `On the ${joinAnd(s.days.doms.map(ordinal))} of every month`
      short = `Monthly · ${s.days.doms.map(ordinal).join(", ")}`
      break
  }
  return {
    long: `${lead} at ${joinAnd(times)}`,
    short: `${short} · ${times.join(", ")}`,
  }
}

// --- the builder -------------------------------------------------------------------

const hhmm = (h: number, m: number) =>
  `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`

// fromCron picks the builder that writes this schedule, or Custom.
export function fromCron(cron: string): Builder {
  const b = blankBuilder()
  const s = cron.trim() ? shapes(cron) : null
  if (!s) return cron.trim() ? { ...b, repeat: "custom", cron } : b
  if (s.kind === "every") {
    const list = s.unit === "minutes" ? EVERY_MINUTES : EVERY_HOURS
    if (!list.includes(s.n) || (s.unit === "hours" && s.at !== 0))
      return { ...b, repeat: "custom", cron }
    return { ...b, repeat: "every", everyN: s.n, everyUnit: s.unit }
  }
  const times = s.times.map(([h, m]) => hhmm(h, m))
  switch (s.days.kind) {
    case "daily":
      return { ...b, repeat: "daily", times }
    case "weekdays":
      return { ...b, repeat: "weekdays", times }
    case "weekends":
      return { ...b, repeat: "weekly", times, days: [0, 6] }
    case "week":
      return { ...b, repeat: "weekly", times, days: s.days.days }
    case "month":
      return s.days.doms.length === 1
        ? { ...b, repeat: "monthly", times, dom: s.days.doms[0] }
        : { ...b, repeat: "custom", cron }
  }
}

// toCron writes the builder's schedule: times sharing a minute share an
// expression (6 AM, noon and 6 PM is "0 6,12,18 * * *").
export function toCron(b: Builder): string {
  if (b.repeat === "custom") return b.cron.trim()
  if (b.repeat === "every") {
    if (b.everyUnit === "minutes")
      return b.everyN === 1 ? "* * * * *" : `*/${b.everyN} * * * *`
    return b.everyN === 1 ? "0 * * * *" : `0 */${b.everyN} * * *`
  }
  const byMinute = new Map<number, number[]>()
  for (const t of [...new Set(b.times)].sort()) {
    const [h, m] = t.split(":").map(Number)
    if (Number.isNaN(h) || Number.isNaN(m)) continue
    byMinute.set(m, [...(byMinute.get(m) ?? []), h])
  }
  const dom = b.repeat === "monthly" ? String(b.dom) : "*"
  const dow =
    b.repeat === "weekdays"
      ? "1-5"
      : b.repeat === "weekly"
        ? [...b.days].sort((x, y) => x - y).join(",")
        : "*"
  return [...byMinute.entries()]
    .sort((x, y) => x[0] - y[0])
    .map(
      ([m, hs]) => `${m} ${hs.sort((x, y) => x - y).join(",")} ${dom} * ${dow}`
    )
    .join("; ")
}

// builderProblem says why the builder cannot save yet, or "".
export function builderProblem(b: Builder): string {
  if (b.repeat === "custom")
    return b.cron.trim() ? "" : "Write a cron expression."
  if (b.repeat === "every") return ""
  if (b.times.length === 0) return "Add a time."
  if (b.repeat === "weekly" && b.days.length === 0) return "Pick a day."
  return ""
}

// --- time zones ------------------------------------------------------------------

export function localTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"
  } catch {
    return "UTC"
  }
}

// The zone's short name: "PT" for America/Los_Angeles.
export function zoneAbbr(tz: string): string {
  if (!tz || tz === "UTC") return "UTC"
  for (const style of ["shortGeneric", "short"] as const) {
    try {
      const part = new Intl.DateTimeFormat("en-US", {
        timeZone: tz,
        timeZoneName: style,
      })
        .formatToParts(new Date())
        .find((p) => p.type === "timeZoneName")
      if (part?.value && !part.value.startsWith("GMT")) return part.value
    } catch {}
  }
  return tz.split("/").pop()?.replace(/_/g, " ") ?? tz
}

export function timeZones(): string[] {
  try {
    const list = (
      Intl as unknown as { supportedValuesOf?: (k: string) => string[] }
    ).supportedValuesOf?.("timeZone")
    if (list?.length) return list.includes("UTC") ? list : ["UTC", ...list]
  } catch {}
  return ["UTC", localTimeZone()]
}

// --- when ------------------------------------------------------------------------

// whenText says when an instant is, relative to now: "in 1h 42m", "4h ago",
// "today 6:00 PM", "tomorrow 7:47 AM", "Oct 14, 9:30 AM".
export function whenText(iso: string | undefined, now: number): string {
  if (!iso) return ""
  const t = Date.parse(iso)
  if (Number.isNaN(t)) return ""
  const diff = t - now
  const abs = Math.abs(diff)
  const mins = Math.round(abs / 60_000)
  if (abs < 60_000) return diff >= 0 ? "in under a minute" : "just now"
  if (mins < 120) {
    const h = Math.floor(mins / 60)
    const m = mins % 60
    const span = h ? (m ? `${h}h ${m}m` : `${h}h`) : `${m}m`
    return diff >= 0 ? `in ${span}` : `${span} ago`
  }
  const d = new Date(t)
  const clock = d.toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  })
  const day = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((day(d) - day(new Date(now))) / 86_400_000)
  if (days === 0) return `today ${clock}`
  if (days === 1) return `tomorrow ${clock}`
  if (days === -1) return `yesterday ${clock}`
  if (diff < 0 && days > -7) return `${Math.round(abs / 86_400_000)}d ago`
  return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })}, ${clock}`
}

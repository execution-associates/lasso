// Anonymous usage analytics, the browser's half (docs/reference/analytics.md).
//
// The page never talks to PostHog: it posts a few typed events to its own
// lasso's /api/telemetry, which checks them against the allowlist and forwards
// them only while the server's setting is on. So no third-party script or
// cookie ever reaches lasso's origin, and "off" is decided in one place.
//
// Never in the way: every call here returns at once, nothing throws, a failed
// post is forgotten, and while the server says analytics is off nothing is
// queued or posted at all.

import { clientID } from "./client-id"

export type TelemetryView =
  | "terminal"
  | "chat"
  | "grid"
  | "bots"
  | "browser"
  | "files"
  | "scratch"
  | "settings"
  | "simple_inbox"
  | "simple_agents"
  | "new_form"

export type TelemetryFeature =
  | "shortcut_used"
  | "paste_upload"
  | "terminal_zoom"
  | "browser_new_tab"
  | "plugin_enabled"
  | "theme_changed"
  | "voice_input"
  | "file_saved"
  | "bot_created"
  | "agent_pinned"

export type TelemetryInboxAction =
  | "acknowledge"
  | "kill"
  | "reply"
  | "answer"
  | "stop"
  | "rename"

type Harness = "claude" | "codex" | "opencode" | "omp" | "pi" | "other"
const HARNESSES: Harness[] = ["claude", "codex", "opencode", "omp", "pi"]

export type TelemetryEvent =
  | { event: "view_opened"; props: { view: TelemetryView } }
  | {
      event: "agent_created"
      props: { harness: Harness; kind: "git" | "scratch" }
    }
  | { event: "inbox_action"; props: { action: TelemetryInboxAction } }
  | { event: "feature_used"; props: { feature: TelemetryFeature } }
  | { event: "onboarding_completed"; props: Record<string, never> }

const QUEUE_MAX = 50
const POST_MAX = 20
const FLUSH_MS = 10_000
const VIEW_EVERY_MS = 10 * 60 * 1000

// null until the server has said; a failed read counts as off.
let active: boolean | null = null
let loading: Promise<void> | null = null
let queue: TelemetryEvent[] = []
let timer: ReturnType<typeof setTimeout> | undefined
const lastView = new Map<TelemetryView, number>()

// The phone surfaces: the simple app at /m, or the full UI on a small touch
// screen.
function app(): "web" | "phone" {
  try {
    if (window.location.pathname.startsWith("/m")) return "phone"
    if (window.matchMedia("(pointer: coarse) and (max-width: 767px)").matches)
      return "phone"
  } catch {
    // no window (a test) or no matchMedia
  }
  return "web"
}

function loadActive(): Promise<void> {
  if (!loading)
    loading = fetch("/api/telemetry/config", {
      headers: { "X-Requested-With": "XMLHttpRequest" },
    })
      .then((r) => (r.ok ? r.json() : null))
      .then((c: { active?: boolean } | null) => {
        if (active === null) active = c?.active === true
      })
      .catch(() => {
        if (active === null) active = false
      })
  return loading
}

// The Settings toggle (and the tour) report the server's answer here, so a
// switch-off stops this tab at once and drops whatever it had queued.
export function setTelemetryActive(on: boolean) {
  active = on
  if (!on) {
    queue = []
    lastView.clear()
  }
}

function schedule(ms = FLUSH_MS) {
  if (timer !== undefined) return
  timer = setTimeout(() => {
    timer = undefined
    void flush()
  }, ms)
}

async function flush() {
  if (active === null) await loadActive()
  if (!active) {
    queue = []
    return
  }
  const events = queue.splice(0, POST_MAX)
  if (events.length === 0) return
  try {
    await fetch("/api/telemetry", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-Requested-With": "XMLHttpRequest",
        "X-Lasso-Client": clientID(),
      },
      body: JSON.stringify({ app: app(), events }),
      keepalive: true,
    })
  } catch {
    // Forgotten: analytics never retries and never tells anyone.
  }
  if (queue.length) schedule()
}

if (typeof window !== "undefined")
  window.addEventListener("pagehide", () => {
    if (active && queue.length) {
      clearTimeout(timer)
      timer = undefined
      void flush()
    }
  })

export function track(ev: TelemetryEvent) {
  try {
    if (active === false) return
    queue.push(ev)
    if (queue.length > QUEUE_MAX) queue.shift()
    schedule()
  } catch {
    // never in the way
  }
}

// At most once per view per 10 minutes from this tab.
export function trackView(view: TelemetryView) {
  if (active === false) return
  const now = Date.now()
  const last = lastView.get(view)
  if (last !== undefined && now - last < VIEW_EVERY_MS) return
  lastView.set(view, now)
  track({ event: "view_opened", props: { view } })
}

export function trackFeature(feature: TelemetryFeature) {
  track({ event: "feature_used", props: { feature } })
}

export function trackInbox(action: TelemetryInboxAction) {
  track({ event: "inbox_action", props: { action } })
}

// Only the CLI's name from a closed list and the agent's kind; a custom
// command line is "other".
export function trackAgentCreated(agent: string | undefined, type: string) {
  const a = (agent ?? "").trim().toLowerCase() as Harness
  track({
    event: "agent_created",
    props: {
      harness: HARNESSES.includes(a) ? a : "other",
      kind: type === "scratch" ? "scratch" : "git",
    },
  })
}

// For tests: what would be posted next, and a reset.
export const _telemetryTest = {
  queued: () => queue.slice(),
  reset: () => {
    active = null
    loading = null
    queue = []
    lastView.clear()
    clearTimeout(timer)
    timer = undefined
  },
  setActive: (v: boolean | null) => {
    active = v
  },
}

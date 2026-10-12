import { afterEach, beforeEach, describe, expect, test } from "bun:test"

import {
  _telemetryTest,
  setTelemetryActive,
  track,
  trackAgentCreated,
  trackFeature,
  trackInbox,
  trackView,
} from "@/lib/telemetry"

// The spec's allowlist for what a browser may post (the server re-checks it).
const ALLOWED = {
  view_opened: {
    view: new Set([
      "terminal",
      "chat",
      "grid",
      "bots",
      "browser",
      "files",
      "scratch",
      "settings",
      "simple_inbox",
      "simple_agents",
      "new_form",
    ]),
  },
  agent_created: {
    harness: new Set(["claude", "codex", "opencode", "omp", "pi", "other"]),
    kind: new Set(["git", "scratch"]),
  },
  inbox_action: {
    action: new Set([
      "acknowledge",
      "kill",
      "reply",
      "answer",
      "stop",
      "rename",
    ]),
  },
  feature_used: {
    feature: new Set([
      "shortcut_used",
      "paste_upload",
      "terminal_zoom",
      "browser_new_tab",
      "plugin_enabled",
      "theme_changed",
      "voice_input",
      "file_saved",
      "bot_created",
      "agent_pinned",
    ]),
  },
  onboarding_completed: {},
}

let calls = []
const realFetch = globalThis.fetch
beforeEach(() => {
  calls = []
  globalThis.fetch = (url, init) => {
    calls.push([url, init])
    return Promise.resolve(new Response(null, { status: 204 }))
  }
  _telemetryTest.reset()
})
afterEach(() => {
  globalThis.fetch = realFetch
  _telemetryTest.reset()
})

describe("telemetry", () => {
  test("off queues and posts nothing", () => {
    setTelemetryActive(false)
    trackView("terminal")
    trackFeature("file_saved")
    trackAgentCreated("claude", "git")
    expect(_telemetryTest.queued()).toEqual([])
    expect(calls).toEqual([])
  })

  test("a view is reported at most once per ten minutes", () => {
    setTelemetryActive(true)
    trackView("grid")
    trackView("grid")
    trackView("chat")
    expect(_telemetryTest.queued().map((e) => e.props.view)).toEqual([
      "grid",
      "chat",
    ])
  })

  test("a custom agent command becomes other", () => {
    setTelemetryActive(true)
    trackAgentCreated("my-wrapper --key=secret", "scratch")
    trackAgentCreated("Codex", "git")
    expect(_telemetryTest.queued().map((e) => e.props)).toEqual([
      { harness: "other", kind: "scratch" },
      { harness: "codex", kind: "git" },
    ])
  })

  test("every event a caller can make is on the allowlist", () => {
    setTelemetryActive(true)
    for (const v of ALLOWED.view_opened.view) trackView(v)
    for (const f of ALLOWED.feature_used.feature) trackFeature(f)
    for (const a of ALLOWED.inbox_action.action) trackInbox(a)
    for (const h of ["claude", "codex", "opencode", "omp", "pi", "aider", ""])
      trackAgentCreated(h, "git")
    track({ event: "onboarding_completed", props: {} })
    const q = _telemetryTest.queued()
    expect(q.length).toBeGreaterThan(0)
    for (const e of q) {
      const spec = ALLOWED[e.event]
      expect(spec).toBeDefined()
      for (const [k, v] of Object.entries(e.props)) {
        expect(spec[k]).toBeDefined()
        expect(spec[k].has(v)).toBe(true)
      }
    }
  })

  test("the queue is capped", () => {
    setTelemetryActive(true)
    for (let i = 0; i < 200; i++) trackFeature("shortcut_used")
    expect(_telemetryTest.queued().length).toBe(50)
  })

  test("switching off drops what was queued", () => {
    setTelemetryActive(true)
    trackFeature("theme_changed")
    setTelemetryActive(false)
    expect(_telemetryTest.queued()).toEqual([])
  })
})

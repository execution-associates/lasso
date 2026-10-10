import { describe, expect, test } from "bun:test"

import {
  blankBuilder,
  fromCron,
  humanize,
  onceText,
  toCron,
  zoneLocalInput,
} from "@/lib/cron"

describe("humanize", () => {
  test("reads the common shapes as sentences", () => {
    const cases = {
      "*/15 * * * *": "Every 15 minutes",
      "* * * * *": "Every minute",
      "0 * * * *": "Every hour, on the hour",
      "30 */2 * * *": "Every 2 hours at :30",
      "0 6,12,18 * * *": "Every day at 6 AM, noon and 6 PM",
      "47 7 * * *": "Every day at 7:47 AM",
      "30 9 * * 1-5": "Weekdays at 9:30 AM",
      "0 10 * * 0,6": "Weekends at 10 AM",
      "0 9 * * 1,4": "Mondays and Thursdays at 9 AM",
      "0 10 1 * *": "On the 1st of every month at 10 AM",
      "0 0 1,15 * *": "On the 1st and 15th of every month at midnight",
      "47 7 * * *; 15 9 * * *": "Every day at 7:47 AM and 9:15 AM",
      "@daily": "Every day at midnight",
    }
    for (const [cron, want] of Object.entries(cases))
      expect(humanize(cron)?.long).toBe(want)
    expect(humanize("0 6,12,18 * * *")?.short).toBe("Daily · 6 AM, noon, 6 PM")
  })

  test("gives up on what a sentence cannot carry", () => {
    for (const cron of [
      "*/7 9-17 * * 1-5",
      "0 9 * 1 *",
      "0 9 13 * 5",
      "47 7 * * *; */5 * * * *",
      "nope",
    ])
      expect(humanize(cron)).toBeNull()
  })
})

describe("builder", () => {
  test("writes cron that reads back to the same builder", () => {
    const b = {
      ...blankBuilder(),
      repeat: "daily",
      times: ["18:00", "06:00", "12:00"],
    }
    expect(toCron(b)).toBe("0 6,12,18 * * *")
    expect(fromCron(toCron(b))).toMatchObject({
      repeat: "daily",
      times: ["06:00", "12:00", "18:00"],
    })
    const mixed = { ...b, times: ["07:47", "09:15"] }
    expect(toCron(mixed)).toBe("15 9 * * *; 47 7 * * *")
    expect(fromCron(toCron(mixed)).times).toEqual(["07:47", "09:15"])
    expect(
      toCron({ ...b, repeat: "weekly", days: [4, 1], times: ["09:30"] })
    ).toBe("30 9 * * 1,4")
    expect(toCron({ ...b, repeat: "monthly", dom: 1, times: ["10:00"] })).toBe(
      "0 10 1 * *"
    )
    expect(
      toCron({ ...b, repeat: "every", everyN: 15, everyUnit: "minutes" })
    ).toBe("*/15 * * * *")
    expect(fromCron("*/15 * * * *")).toMatchObject({
      repeat: "every",
      everyN: 15,
      everyUnit: "minutes",
    })
    expect(fromCron("0 9 * * 1-5").repeat).toBe("weekdays")
    expect(fromCron("*/7 * * * *").repeat).toBe("custom")
  })
})

describe("one-time runs", () => {
  test("show and edit on the job's clock", () => {
    const iso = "2026-11-20T16:00:00Z"
    expect(zoneLocalInput(iso, "UTC")).toBe("2026-11-20T16:00")
    expect(zoneLocalInput(iso, "America/Los_Angeles")).toBe("2026-11-20T08:00")
    expect(onceText(iso, "America/Los_Angeles")).toBe(
      "Once, Nov 20, 2026, 8:00 AM"
    )
    expect(zoneLocalInput("nope", "UTC")).toBe("")
  })
})

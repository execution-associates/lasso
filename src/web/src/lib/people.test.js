import { describe, expect, test } from "bun:test"

import {
  clock,
  pairingBaseProblem,
  pairingBaseWarning,
  personNameProblem,
} from "@/lib/people"

describe("person names, as the API checks them", () => {
  test("accepts ordinary names", () => {
    for (const n of ["Ann", "  Ann Rivera ", "Zoë O'Neil", "李雷"])
      expect(personNameProblem(n)).toBe("")
  })
  test("refuses empty, long, and hostile names", () => {
    expect(personNameProblem("   ")).not.toBe("")
    expect(personNameProblem("x".repeat(65))).not.toBe("")
    expect(personNameProblem("x".repeat(64))).toBe("")
    for (const n of ["a\nb", "<b>", 'say "hi"', "a\\b", "a`b", "a‮b"])
      expect(personNameProblem(n)).not.toBe("")
  })
})

describe("the link's address", () => {
  test("must be an origin", () => {
    expect(pairingBaseProblem("https://lasso.example.com")).toBe("")
    expect(pairingBaseProblem("https://lasso.example.com/")).toBe("")
    expect(pairingBaseProblem("http://100.1.2.3:8090")).toBe("")
    for (const b of [
      "lasso.example.com",
      "ftp://x.com",
      "https://x.com/a",
      "https://x.com?q=1",
      "https://u@x.com",
    ])
      expect(pairingBaseProblem(b)).not.toBe("")
  })
  test("loopback is blocked, private and tailnet addresses are cautioned", () => {
    for (const b of [
      "http://127.0.0.1:8090",
      "http://localhost:8090",
      "http://[::1]:8090",
      "http://0.0.0.0:1",
    ])
      expect(pairingBaseWarning(b)?.level).toBe("blocked")
    for (const b of [
      "http://192.168.1.5:8090",
      "http://10.0.0.2",
      "http://172.20.1.1",
      "http://100.86.22.100",
      "https://titan.tail1234.ts.net",
      "https://mac.local",
      "https://http-only-host",
    ])
      expect(pairingBaseWarning(b)?.level).toBe("caution")
    expect(pairingBaseWarning("https://lasso.example.com")).toBeNull()
    expect(pairingBaseWarning("http://lasso.example.com")?.level).toBe(
      "caution"
    )
  })
})

test("the countdown reads m:ss", () => {
  expect(clock(600_000)).toBe("10:00")
  expect(clock(59_001)).toBe("1:00")
  expect(clock(-5)).toBe("0:00")
})

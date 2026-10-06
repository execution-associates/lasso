import { describe, expect, test } from "bun:test"

import { mergeItems } from "@/lib/chat-merge"

const row = (id) => ({ id, kind: "agent", text: id })
const tool = (id, state) => ({
  id,
  kind: "tool",
  tool: { call_id: id, state },
})
const ids = (items) => items.map((it) => it.id)

describe("mergeItems", () => {
  test("appends rows newer than everything on screen", () => {
    const prev = [row("a"), row("b")]
    expect(ids(mergeItems(prev, [row("b"), row("c")]))).toEqual(["a", "b", "c"])
  })

  test("puts older rows from a widened page above the screen, not below it", () => {
    // The live window grew backwards (a call/result pair at its edge) and
    // carried rows older than any on screen.
    const prev = [row("c"), row("d"), row("e")]
    const page = [row("a"), row("b"), row("c"), row("d"), row("e"), row("f")]
    expect(ids(mergeItems(prev, page))).toEqual(["a", "b", "c", "d", "e", "f"])
  })

  test("keeps rows the page no longer covers, in place", () => {
    const prev = [row("old1"), row("old2"), row("c")]
    expect(ids(mergeItems(prev, [row("c"), row("d")]))).toEqual([
      "old1",
      "old2",
      "c",
      "d",
    ])
  })

  test("fills a gap between rows already on screen", () => {
    const prev = [row("a"), row("c")]
    expect(ids(mergeItems(prev, [row("a"), row("b"), row("c")]))).toEqual([
      "a",
      "b",
      "c",
    ])
  })

  test("updates a row in place and refuses a state regression", () => {
    const prev = [tool("t1", "completed"), tool("t2", "running")]
    const out = mergeItems(prev, [
      tool("t1", "running"),
      tool("t2", "completed"),
    ])
    expect(out.map((it) => it.tool.state)).toEqual(["completed", "completed"])
  })

  test("returns the same array when nothing changed", () => {
    const prev = [row("a"), row("b")]
    expect(mergeItems(prev, [prev[1]])).toBe(prev)
  })
})

import { describe, expect, test } from "bun:test"

import { api } from "@/lib/api"
import { enterChordSeq, terminalPasteHost } from "@/lib/terminal"

describe("terminalPasteHost", () => {
  test("keeps a local pane's paste on the active host", () => {
    expect(terminalPasteHost("local", "local")).toBe("local")
  })

  test("routes a native nested attach to its pane filesystem host", () => {
    expect(terminalPasteHost("local", "ticket500")).toBe("ticket500")
  })

  test("falls back to the active host until focused-pane metadata arrives", () => {
    expect(terminalPasteHost("ticket500", null)).toBe("ticket500")
    expect(terminalPasteHost(null, null)).toBeUndefined()
  })
})

describe("enterChordSeq", () => {
  const k = (mods) => ({
    shiftKey: false,
    altKey: false,
    ctrlKey: false,
    metaKey: false,
    ...mods,
  })

  test("sends herdr a real modified Enter as CSI-u", () => {
    expect(enterChordSeq(k({ shiftKey: true }))).toBe("\x1b[13;2u")
    expect(enterChordSeq(k({ ctrlKey: true }))).toBe("\x1b[13;5u")
    expect(enterChordSeq(k({ ctrlKey: true, shiftKey: true }))).toBe(
      "\x1b[13;6u"
    )
    expect(enterChordSeq(k({ ctrlKey: true, altKey: true }))).toBe(
      "\x1b[13;7u"
    )
  })

  test("leaves plain, Alt-only and Meta chords to xterm", () => {
    expect(enterChordSeq(k({}))).toBeNull()
    expect(enterChordSeq(k({ altKey: true }))).toBeNull()
    expect(
      enterChordSeq(k({ metaKey: true, ctrlKey: true }))
    ).toBeNull()
  })
})

test("pasteFile sends the selected terminal host and the file's name", async () => {
  const originalFetch = globalThis.fetch
  let requested = ""
  globalThis.fetch = (input) => {
    requested = String(input)
    return Promise.resolve(
      new Response(
        JSON.stringify({
          path: "/home/stephan/.lasso/uploads/dropped-files/notes.pdf",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } }
      )
    )
  }

  try {
    const result = await api.pasteFile(
      new Blob(["pdf bytes"], { type: "application/pdf" }),
      "ticket500",
      "Q3 notes.pdf"
    )
    expect(requested).toBe("/api/paste-file?name=Q3%20notes.pdf&host=ticket500")
    expect(result.path).toStartWith("/home/stephan/")
  } finally {
    globalThis.fetch = originalFetch
  }
})

test("pasteFile without a name leaves the server to stamp one", async () => {
  const originalFetch = globalThis.fetch
  let requested = ""
  globalThis.fetch = (input) => {
    requested = String(input)
    return Promise.resolve(
      new Response(JSON.stringify({ path: "/tmp/clipboard.png" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    )
  }

  try {
    await api.pasteFile(new Blob(["png bytes"], { type: "image/png" }))
    expect(requested).toBe("/api/paste-file")
  } finally {
    globalThis.fetch = originalFetch
  }
})

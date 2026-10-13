import { describe, expect, test } from "bun:test"

import {
  CONFLICT_SAVE_ERROR,
  fileDocReducer,
  fileStateReport,
  initialFileDoc,
  isDirty,
} from "@/lib/file-doc"

const run = (s, ...actions) => actions.reduce(fileDocReducer, s)
const loaded = (text = "a", mtime = 1) =>
  run(initialFileDoc("raw"), { type: "loaded", text, mtime })

describe("load", () => {
  test("starts unloaded and clean", () => {
    const s = initialFileDoc("rich")
    expect(s.loaded).toBe(false)
    expect(isDirty(s)).toBe(false)
    expect(fileStateReport(s).mode).toBe("rich")
  })

  test("a read makes the buffer the disk", () => {
    const s = loaded("hello", 5)
    expect(s.loaded).toBe(true)
    expect(s.draft).toBe("hello")
    expect(s.mtime).toBe(5)
    expect(isDirty(s)).toBe(false)
  })

  test("a missing file is loaded and missing, not an error", () => {
    const s = run(initialFileDoc("raw"), {
      type: "load-failed",
      error: "not found",
      missing: true,
    })
    expect(s.loaded).toBe(true)
    expect(s.missing).toBe(true)
    expect(s.loadError).toBe(null)
  })

  test("any other failure is an error", () => {
    const s = run(initialFileDoc("raw"), {
      type: "load-failed",
      error: "HTTP 502",
      missing: false,
    })
    expect(s.loadError).toBe("HTTP 502")
    expect(s.missing).toBe(false)
  })

  test("reset forgets the old file", () => {
    const s = run(
      loaded("x"),
      { type: "edit", draft: "y" },
      { type: "reset", mode: "rich" }
    )
    expect(s).toEqual(initialFileDoc("rich"))
  })
})

describe("changed on disk", () => {
  test("a clean buffer follows the disk", () => {
    const s = run(loaded("a", 1), { type: "disk", text: "b", mtime: 2 })
    expect(s.draft).toBe("b")
    expect(s.text).toBe("b")
    expect(s.mtime).toBe(2)
    expect(s.conflict).toBe(false)
  })

  test("the same content only moves the mtime", () => {
    const before = loaded("a", 1)
    const s = run(before, { type: "disk", text: "a", mtime: 9 })
    expect(s.draft).toBe(before.draft)
    expect(s.mtime).toBe(9)
  })

  test("a dirty buffer is never replaced, and raises a conflict", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "mine" },
      { type: "disk", text: "theirs", mtime: 2 }
    )
    expect(s.draft).toBe("mine")
    expect(s.conflict).toBe(true)
    expect(s.disk).toEqual({ text: "theirs", mtime: 2 })
    // The base is untouched: a save would still be refused.
    expect(s.mtime).toBe(1)
    expect(isDirty(s)).toBe(true)
  })

  test("a dirty buffer over an unchanged disk is not a conflict", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "mine" },
      { type: "disk", text: "a", mtime: 3 }
    )
    expect(s.conflict).toBe(false)
    expect(s.mtime).toBe(3)
    expect(s.draft).toBe("mine")
  })

  test("the disk catching up with the buffer makes it clean", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "disk", text: "b", mtime: 4 }
    )
    expect(isDirty(s)).toBe(false)
    expect(s.conflict).toBe(false)
    expect(s.mtime).toBe(4)
  })

  test("a later disk version updates the pending conflict", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "mine" },
      { type: "disk", text: "t1", mtime: 2 },
      { type: "disk", text: "t2", mtime: 3 }
    )
    expect(s.disk).toEqual({ text: "t2", mtime: 3 })
    expect(s.draft).toBe("mine")
  })

  test("is ignored while a save is in flight", () => {
    const start = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" }
    )
    expect(run(start, { type: "disk", text: "c", mtime: 2 })).toBe(start)
  })

  test("a deleted file keeps the buffer", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "disk-missing" }
    )
    expect(s.missing).toBe(true)
    expect(s.draft).toBe("b")
  })

  test("a file that comes back is no longer missing", () => {
    const s = run(
      loaded("a", 1),
      { type: "disk-missing" },
      { type: "disk", text: "a", mtime: 2 }
    )
    expect(s.missing).toBe(false)
  })
})

describe("answering a conflict", () => {
  const conflicted = () =>
    run(
      loaded("a", 1),
      { type: "edit", draft: "mine" },
      { type: "disk", text: "theirs", mtime: 2 }
    )

  test("takeDisk drops the buffer for the disk", () => {
    const s = run(conflicted(), { type: "loaded", text: "theirs", mtime: 2 })
    expect(s.draft).toBe("theirs")
    expect(s.conflict).toBe(false)
    expect(isDirty(s)).toBe(false)
  })

  test("keepMine keeps the buffer and takes the disk's mtime as the base", () => {
    const s = run(conflicted(), { type: "keep-mine" })
    expect(s.conflict).toBe(false)
    expect(s.draft).toBe("mine")
    expect(s.mtime).toBe(2)
    expect(s.text).toBe("theirs")
    expect(isDirty(s)).toBe(true)
    // The same disk version seen again is not a new conflict.
    const again = run(s, { type: "disk", text: "theirs", mtime: 2 })
    expect(again.conflict).toBe(false)
  })

  test("keepMine without a conflict changes nothing", () => {
    const s = loaded("a", 1)
    expect(run(s, { type: "keep-mine" })).toBe(s)
  })
})

describe("save", () => {
  test("saving then saved moves the base to what was written", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" }
    )
    expect(s.saving).toBe(true)
    const done = run(s, { type: "save-ok", content: "b", mtime: 7 })
    expect(done.saving).toBe(false)
    expect(done.text).toBe("b")
    expect(done.mtime).toBe(7)
    expect(isDirty(done)).toBe(false)
  })

  test("typing during the save stays dirty against what was written", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" },
      { type: "edit", draft: "bc" },
      { type: "save-ok", content: "b", mtime: 7 }
    )
    expect(isDirty(s)).toBe(true)
    expect(s.text).toBe("b")
  })

  test("a 409 is a conflict with the disk's mtime", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" },
      { type: "save-conflict", mtime: 5 }
    )
    expect(s.saving).toBe(false)
    expect(s.conflict).toBe(true)
    expect(s.saveError).toBe(CONFLICT_SAVE_ERROR)
    expect(s.disk).toEqual({ text: null, mtime: 5 })
    expect(s.draft).toBe("b")
    // keepMine then overwrites on purpose: the next base is the disk's mtime.
    const kept = run(s, { type: "keep-mine" })
    expect(kept.mtime).toBe(5)
    expect(kept.saveError).toBe(null)
  })

  test("a refused save learns the disk's text from the next read", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" },
      { type: "save-conflict", mtime: 5 },
      { type: "disk", text: "theirs", mtime: 5 }
    )
    expect(s.conflict).toBe(true)
    expect(s.disk).toEqual({ text: "theirs", mtime: 5 })
  })

  test("a 409 for a disk that only got touched clears on the next read", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" },
      { type: "save-conflict", mtime: 5 },
      { type: "disk", text: "a", mtime: 5 }
    )
    expect(s.conflict).toBe(false)
    expect(s.saveError).toBe(null)
    expect(s.mtime).toBe(5)
  })

  test("any other failure is a save error, not a conflict", () => {
    const s = run(
      loaded("a", 1),
      { type: "edit", draft: "b" },
      { type: "save-start" },
      { type: "save-failed", error: "permission denied" }
    )
    expect(s.saveError).toBe("permission denied")
    expect(s.conflict).toBe(false)
  })
})

describe("report", () => {
  test("carries every field the native window reads", () => {
    const r = fileStateReport(
      run(loaded("a", 3), { type: "mode", mode: "rich" })
    )
    expect(r).toEqual({
      dirty: false,
      mode: "rich",
      saving: false,
      saveError: null,
      conflict: false,
      loaded: true,
      missing: false,
      mtime: 3,
    })
  })
})

import { describe, expect, test } from "bun:test"

import { fuzzyFilter, fuzzyScore, splitLineSuffix } from "@/lib/fuzzy"

const FILES = [
  "src/main.go",
  "src/filefind.go",
  "src/web/src/App.tsx",
  "src/web/src/components/FilesPanel.tsx",
  "src/web/src/components/FileViewer.tsx",
  "src/web/src/lib/fuzzy.ts",
  "docs/design/file-viewer-host.md",
]

const top = (q) => fuzzyFilter(FILES, q, 10).map((r) => r.path)

describe("fuzzyScore", () => {
  test("requires every character in order", () => {
    expect(fuzzyScore("src/main.go", "mgo")).not.toBeNull()
    expect(fuzzyScore("src/main.go", "ogm")).toBeNull()
  })

  test("is case-insensitive and highlights the name", () => {
    const m = fuzzyScore("src/web/src/App.tsx", "app")
    expect(m.positions).toEqual([12, 13, 14])
  })

  test("every whitespace-separated term must match", () => {
    expect(fuzzyScore("src/web/src/App.tsx", "web app")).not.toBeNull()
    expect(fuzzyScore("src/web/src/App.tsx", "web zzz")).toBeNull()
  })
})

describe("fuzzyFilter", () => {
  test("a match in the file name beats one across directories", () => {
    expect(top("fv")[0]).toBe("src/web/src/components/FileViewer.tsx")
  })

  test("an exact name wins", () => {
    expect(top("main.go")[0]).toBe("src/main.go")
  })

  test("word starts beat scattered letters", () => {
    expect(top("fp")[0]).toBe("src/web/src/components/FilesPanel.tsx")
  })

  test("a path fragment narrows by directory", () => {
    expect(top("docs file")).toEqual(["docs/design/file-viewer-host.md"])
  })

  test("an empty query keeps every file", () => {
    expect(top("").length).toBe(FILES.length)
  })
})

describe("splitLineSuffix", () => {
  test("takes a trailing :line and ignores a column", () => {
    expect(splitLineSuffix("app.tsx:42")).toEqual({
      query: "app.tsx",
      line: 42,
    })
    expect(splitLineSuffix("app.tsx:42:7")).toEqual({
      query: "app.tsx",
      line: 42,
    })
    expect(splitLineSuffix("app.tsx")).toEqual({ query: "app.tsx", line: null })
  })
})

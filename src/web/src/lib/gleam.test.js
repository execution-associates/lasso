import { describe, expect, test } from "bun:test"
import { StringStream } from "@codemirror/language"

import { gleam } from "@/lib/gleam"

// Tokenize lines with one carried state, as CodeMirror does, returning
// [text, style] pairs with whitespace dropped.
function tokens(...lines) {
  const state = gleam.startState(2)
  const out = []
  for (const line of lines) {
    const stream = new StringStream(line, 2, 2)
    while (!stream.eol()) {
      const style = gleam.token(stream, state)
      if (stream.current().trim()) out.push([stream.current(), style])
      stream.start = stream.pos
    }
  }
  return out
}

describe("gleam tokenizer", () => {
  test("a function definition", () => {
    expect(tokens("pub fn add(x: Int) -> Int { x + 1 }")).toEqual([
      ["pub", "keyword"],
      ["fn", "keyword"],
      ["add", "variableName.function.definition"],
      ["(", null],
      ["x", "variableName"],
      [":", null],
      ["Int", "typeName"],
      [")", null],
      ["->", "operator"],
      ["Int", "typeName"],
      ["{", null],
      ["x", "variableName"],
      ["+", "operator"],
      ["1", "number"],
      ["}", null],
    ])
  })

  test("calls, pipes, attributes, comments and literals", () => {
    expect(tokens('@external(erlang, "m", "f") // note')).toEqual([
      ["@external", "meta"],
      ["(", null],
      ["erlang", "variableName"],
      [",", null],
      ['"m"', "string"],
      [",", null],
      ['"f"', "string"],
      [")", null],
      ["// note", "comment"],
    ])
    expect(tokens("x |> io.println(True, Nil, 1.5e3, 0xFF, 2 <=. 3)")).toEqual([
      ["x", "variableName"],
      ["|>", "operator"],
      ["io", "variableName"],
      [".", null],
      ["println", "variableName.function"],
      ["(", null],
      ["True", "bool"],
      [",", null],
      ["Nil", "atom"],
      [",", null],
      ["1.5e3", "number"],
      [",", null],
      ["0xFF", "number"],
      [",", null],
      ["2", "number"],
      ["<=.", "operator"],
      ["3", "number"],
      [")", null],
    ])
  })

  test("a string spanning lines, with an escaped quote", () => {
    expect(tokens('let s = "a \\" b', 'c" <> d')).toEqual([
      ["let", "keyword"],
      ["s", "variableName"],
      ["=", "operator"],
      ['"a \\" b', "string"],
      ['c"', "string"],
      ["<>", "operator"],
      ["d", "variableName"],
    ])
  })
})

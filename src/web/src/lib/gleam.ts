// A CodeMirror stream tokenizer for Gleam. There is no CM6 or legacy mode for
// it, and the grammar is small enough that a hand-rolled tokenizer covers what
// highlighting needs: comments, strings, numbers, keywords, attributes,
// Upper-case names (types and constructors) and calls.
import type { StreamParser } from "@codemirror/language"

const KEYWORDS = new Set([
  "as",
  "assert",
  "auto",
  "case",
  "const",
  "delegate",
  "derive",
  "echo",
  "else",
  "fn",
  "if",
  "implement",
  "import",
  "let",
  "macro",
  "opaque",
  "panic",
  "pub",
  "test",
  "todo",
  "type",
  "use",
])

type GleamState = {
  // Inside a string that runs past the end of a line.
  inString: boolean
  // The previous token was `fn`, so the next name is a definition.
  afterFn: boolean
}

function readString(stream: Parameters<StreamParser<GleamState>["token"]>[0]) {
  let escaped = false
  let ch = stream.next()
  while (ch != null) {
    if (ch === '"' && !escaped) return true
    escaped = !escaped && ch === "\\"
    ch = stream.next()
  }
  return false
}

export const gleam: StreamParser<GleamState> = {
  name: "gleam",
  startState: () => ({ inString: false, afterFn: false }),
  token(stream, state) {
    if (state.inString) {
      state.inString = !readString(stream)
      return "string"
    }
    if (stream.eatSpace()) return null

    if (stream.match("//")) {
      stream.skipToEnd()
      return "comment"
    }
    if (stream.eat('"')) {
      state.inString = !readString(stream)
      return "string"
    }
    if (
      stream.match(/^0[xX][0-9a-fA-F_]+/) ||
      stream.match(/^0[oO][0-7_]+/) ||
      stream.match(/^0[bB][01_]+/) ||
      stream.match(/^\d[\d_]*(\.[\d_]*)?([eE]-?\d[\d_]*)?/)
    ) {
      return "number"
    }
    if (stream.match(/^@[a-z_][a-zA-Z0-9_]*/)) return "meta"

    if (stream.match(/^[A-Z][a-zA-Z0-9_]*/)) {
      state.afterFn = false
      const word = stream.current()
      if (word === "True" || word === "False") return "bool"
      if (word === "Nil") return "atom"
      return "typeName"
    }
    if (stream.match(/^[a-z_][a-zA-Z0-9_]*/)) {
      const word = stream.current()
      if (KEYWORDS.has(word)) {
        state.afterFn = word === "fn"
        return "keyword"
      }
      if (state.afterFn) {
        state.afterFn = false
        return "variableName.function.definition"
      }
      if (stream.match(/^\s*\(/, false)) return "variableName.function"
      return "variableName"
    }

    state.afterFn = false
    if (
      stream.match(
        /^(\|>|->|<-|<>|\.\.|[<>]=?\.|[-+*/]\.|[=!<>]=|&&|\|\||[-+*/%<>=!|])/
      )
    ) {
      return "operator"
    }
    stream.next()
    return null
  },
  languageData: {
    commentTokens: { line: "//" },
    closeBrackets: { brackets: ["(", "[", "{", '"'] },
  },
}

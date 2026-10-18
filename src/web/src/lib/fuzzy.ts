// Fuzzy file matching for the file viewer's quick open (⌘P), in the spirit of
// VS Code's: the query's characters must appear in order, a match inside the
// file's NAME beats one spread across its directories, and runs of adjacent
// characters and starts of words (after / _ - . or a camelCase hump) score
// higher than scattered ones. Whitespace splits the query into terms that must
// all match, so "web app" finds src/web/src/App.tsx.
//
// It runs over every file under the tree's root (up to 50,000) on each
// keystroke, so a term is matched in one linear pass plus one backward pass
// (fzf's v1 shape), never a full alignment.

export interface FuzzyResult {
  path: string
  score: number
  // Indexes into `path` to highlight, ascending.
  positions: number[]
}

function isBoundary(text: string, i: number): boolean {
  if (i === 0) return true
  const prev = text[i - 1]
  if (
    prev === "/" ||
    prev === "_" ||
    prev === "-" ||
    prev === "." ||
    prev === " "
  )
    return true
  const c = text[i]
  // A camelCase hump: lower then upper.
  return (
    prev === prev.toLowerCase() &&
    prev !== prev.toUpperCase() &&
    c === c.toUpperCase() &&
    c !== c.toLowerCase()
  )
}

// The tightest occurrence of `term` in lower[from:], as positions, or null.
// Forward to the first point where the whole term has matched, back from
// there to the latest possible start, then forward again from that start.
function locate(lower: string, term: string, from: number): number[] | null {
  let qi = 0
  let end = -1
  for (let i = from; i < lower.length; i++) {
    if (lower[i] === term[qi] && ++qi === term.length) {
      end = i
      break
    }
  }
  if (end < 0) return null
  let start = end
  qi = term.length - 1
  for (let i = end; i >= from; i--) {
    if (lower[i] === term[qi] && --qi < 0) {
      start = i
      break
    }
  }
  const pos: number[] = []
  qi = 0
  for (let i = start; i <= end && qi < term.length; i++) {
    if (lower[i] === term[qi]) {
      pos.push(i)
      qi++
    }
  }
  return pos
}

function scorePositions(text: string, pos: number[], nameStart: number) {
  let s = 0
  for (let k = 0; k < pos.length; k++) {
    const i = pos[k]
    s += 1
    if (isBoundary(text, i)) s += 8
    if (i === nameStart) s += 6
    if (k > 0) {
      const gap = i - pos[k - 1] - 1
      s += gap === 0 ? 6 : -Math.min(gap, 8) * 0.5
    }
  }
  return s
}

function matchTerm(
  path: string,
  lower: string,
  nameStart: number,
  term: string
): { score: number; positions: number[] } | null {
  // The file's name first: that is what a human types.
  const inName = locate(lower, term, nameStart)
  if (inName) {
    let score = scorePositions(path, inName, nameStart) + 20
    const name = lower.slice(nameStart)
    if (name === term) score += 50
    else if (name.startsWith(term)) score += 15
    return { score, positions: inName }
  }
  const anywhere = locate(lower, term, 0)
  if (!anywhere) return null
  return {
    score: scorePositions(path, anywhere, nameStart),
    positions: anywhere,
  }
}

export function fuzzyScore(path: string, query: string): FuzzyResult | null {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean)
  if (terms.length === 0) return { path, score: 0, positions: [] }
  const lower = path.toLowerCase()
  const nameStart = path.lastIndexOf("/") + 1
  let score = 0
  const positions = new Set<number>()
  for (const term of terms) {
    const m = matchTerm(path, lower, nameStart, term)
    if (!m) return null
    score += m.score
    for (const p of m.positions) positions.add(p)
  }
  return {
    path,
    score,
    positions: [...positions].sort((a, b) => a - b),
  }
}

// The best `limit` matches, best first; ties go to the shorter path, then
// alphabetical, so the order is stable while typing.
export function fuzzyFilter(
  paths: readonly string[],
  query: string,
  limit: number
): FuzzyResult[] {
  const out: FuzzyResult[] = []
  for (const p of paths) {
    const m = fuzzyScore(p, query)
    if (m) out.push(m)
  }
  out.sort(
    (a, b) =>
      b.score - a.score ||
      a.path.length - b.path.length ||
      (a.path < b.path ? -1 : a.path > b.path ? 1 : 0)
  )
  return out.slice(0, limit)
}

// VS Code's "file.ts:42": a trailing :line (and an ignored :column) goes to
// that line once the file opens.
export function splitLineSuffix(query: string): {
  query: string
  line: number | null
} {
  const m = /^(.*?):(\d+)(?::\d+)?$/.exec(query.trim())
  if (!m) return { query, line: null }
  const line = Number(m[2])
  return { query: m[1], line: line > 0 ? line : null }
}

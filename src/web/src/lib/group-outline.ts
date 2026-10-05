// Outlines for groups of cards laid out in one continuous grid. A group is a
// run of consecutive cards, so once the grid wraps it is not a rectangle: it
// can start mid-row and end mid-row, and its outline has to hug the cells it
// holds (an L, a step) rather than box the rows it touches.
//
// Each group's shape is the union of its cells grown by `pad`, plus bridge
// rects across the gutters between neighbouring cells of the SAME group, which
// is what merges them into one shape; cells of different groups get no bridge,
// so as long as `pad` is under half the gutter their outlines never touch. The
// union is traced into a polygon and drawn as one SVG path with every corner
// rounded, the inward (concave) corners of an L included, which a union of
// rounded rects cannot do.

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface Cell extends Box {
  group: string
}

// Rows and columns come from the cells' own positions, so this needs no idea of
// how many columns the grid resolved to. A couple of pixels of slack absorbs
// subpixel layout.
function bands(vals: number[]): number[] {
  const out: number[] = []
  for (const v of [...vals].sort((a, b) => a - b)) {
    if (!out.length || v - out[out.length - 1] > 2) out.push(v)
  }
  return out
}

function bandOf(bs: number[], v: number): number {
  let best = 0
  for (let i = 0; i < bs.length; i++) {
    if (Math.abs(bs[i] - v) < Math.abs(bs[best] - v)) best = i
  }
  return best
}

// The rects whose union is each group's shape, keyed by group.
function groupRects(cells: Cell[], pad: number): Map<string, Box[]> {
  const out = new Map<string, Box[]>()
  const rows = bands(cells.map((c) => c.y))
  const cols = bands(cells.map((c) => c.x))
  const at = new Map<string, Cell>()
  for (const c of cells) at.set(`${bandOf(rows, c.y)},${bandOf(cols, c.x)}`, c)
  const same = (a: Cell, r: number, c: number) => {
    const b = at.get(`${r},${c}`)
    return b && b.group === a.group ? b : undefined
  }
  for (const c of cells) {
    let list = out.get(c.group)
    if (!list) out.set(c.group, (list = []))
    list.push({
      x: c.x - pad,
      y: c.y - pad,
      w: c.w + 2 * pad,
      h: c.h + 2 * pad,
    })
    const r = bandOf(rows, c.y)
    const k = bandOf(cols, c.x)
    const right = same(c, r, k + 1)
    const down = same(c, r + 1, k)
    if (right) {
      const top = Math.min(c.y, right.y)
      const bottom = Math.max(c.y + c.h, right.y + right.h)
      list.push({
        x: c.x + c.w,
        y: top - pad,
        w: right.x - (c.x + c.w),
        h: bottom - top + 2 * pad,
      })
    }
    if (down) {
      const left = Math.min(c.x, down.x)
      const rightEdge = Math.max(c.x + c.w, down.x + down.w)
      list.push({
        x: left - pad,
        y: c.y + c.h,
        w: rightEdge - left + 2 * pad,
        h: down.y - (c.y + c.h),
      })
    }
    // Where four cells of one group meet, the square of gutter between them
    // is inside the shape; neither bridge reaches it.
    const diag = right && down ? same(c, r + 1, k + 1) : undefined
    if (diag) {
      list.push({
        x: c.x + c.w,
        y: c.y + c.h,
        w: diag.x - (c.x + c.w),
        h: diag.y - (c.y + c.h),
      })
    }
  }
  return out
}

type Pt = [number, number]

function uniq(vals: number[]): number[] {
  const out: number[] = []
  for (const v of [...vals].sort((a, b) => a - b)) {
    if (!out.length || v - out[out.length - 1] > 0.01) out.push(v)
  }
  return out
}

// The boundary loops of a union of rects, as polygons with no collinear
// vertices. Coordinates are compressed to the rects' own edges, so every
// compressed cell is wholly in or out; each in-cell side facing an out-cell is
// a boundary edge, directed clockwise (on screen) around the inside.
function unionLoops(rects: Box[]): Pt[][] {
  const xs = uniq(rects.flatMap((r) => [r.x, r.x + r.w]))
  const ys = uniq(rects.flatMap((r) => [r.y, r.y + r.h]))
  const inside = (i: number, j: number) => {
    if (i < 0 || j < 0 || i >= xs.length - 1 || j >= ys.length - 1) return false
    const cx = (xs[i] + xs[i + 1]) / 2
    const cy = (ys[j] + ys[j + 1]) / 2
    return rects.some(
      (r) => cx > r.x && cx < r.x + r.w && cy > r.y && cy < r.y + r.h
    )
  }
  const key = (p: Pt) => `${p[0]},${p[1]}`
  const from = new Map<string, Pt[]>()
  const add = (a: Pt, b: Pt) => {
    const k = key(a)
    const list = from.get(k)
    if (list) list.push(b)
    else from.set(k, [b])
  }
  for (let i = 0; i < xs.length - 1; i++) {
    for (let j = 0; j < ys.length - 1; j++) {
      if (!inside(i, j)) continue
      const x0 = xs[i]
      const x1 = xs[i + 1]
      const y0 = ys[j]
      const y1 = ys[j + 1]
      if (!inside(i, j - 1)) add([x0, y0], [x1, y0])
      if (!inside(i + 1, j)) add([x1, y0], [x1, y1])
      if (!inside(i, j + 1)) add([x1, y1], [x0, y1])
      if (!inside(i - 1, j)) add([x0, y1], [x0, y0])
    }
  }
  const loops: Pt[][] = []
  for (const [k0, outs] of from) {
    while (outs.length) {
      const start = k0.split(",").map(Number) as Pt
      const loop: Pt[] = [start]
      let next = outs.pop() as Pt
      while (key(next) !== k0) {
        loop.push(next)
        const more = from.get(key(next))
        if (!more?.length) break
        next = more.pop() as Pt
      }
      // Drop the vertices that only split a straight side.
      const n = loop.length
      const corners = loop.filter((p, i) => {
        const a = loop[(i + n - 1) % n]
        const b = loop[(i + 1) % n]
        return (
          (b[0] - p[0]) * (p[1] - a[1]) - (b[1] - p[1]) * (p[0] - a[0]) !== 0
        )
      })
      if (corners.length >= 4) loops.push(corners)
    }
  }
  return loops
}

const fmt = (v: number) => Math.round(v * 100) / 100

// One closed subpath per loop, every corner an arc of `radius` (shrunk where
// a side is too short to hold two of them). Convex corners sweep one way and
// concave ones the other, which falls out of the turn direction.
function roundedPath(loop: Pt[], radius: number): string {
  const n = loop.length
  const len = (a: Pt, b: Pt) => Math.hypot(b[0] - a[0], b[1] - a[1])
  const toward = (a: Pt, b: Pt, d: number): Pt => {
    const l = len(a, b)
    return [a[0] + ((b[0] - a[0]) * d) / l, a[1] + ((b[1] - a[1]) * d) / l]
  }
  let d = ""
  for (let i = 0; i <= n; i++) {
    const v = loop[i % n]
    const prev = loop[(i + n - 1) % n]
    const next = loop[(i + 1) % n]
    const r = Math.min(radius, len(prev, v) / 2, len(v, next) / 2)
    const a = toward(v, prev, r)
    const b = toward(v, next, r)
    const turn =
      (v[0] - prev[0]) * (next[1] - v[1]) - (v[1] - prev[1]) * (next[0] - v[0])
    if (i === 0) {
      d += `M${fmt(b[0])} ${fmt(b[1])}`
      continue
    }
    d += `L${fmt(a[0])} ${fmt(a[1])}`
    d += `A${fmt(r)} ${fmt(r)} 0 0 ${turn > 0 ? 1 : 0} ${fmt(b[0])} ${fmt(b[1])}`
  }
  return `${d}Z`
}

// Every group's outline at one level, as one SVG path: its centre line sits
// `pad` out from the cells, so a stroke of width s drawn on it fills the band
// [pad - s/2, pad + s/2] around them.
export function groupOutlines(
  cells: Cell[],
  pad: number,
  radius: number
): string {
  let d = ""
  for (const rects of groupRects(cells, pad).values()) {
    for (const loop of unionLoops(rects)) d += roundedPath(loop, radius)
  }
  return d
}

// Where each separately drawn piece of a group begins, as indexes into `cells`
// (which must be in grid order). A group that wraps stays ONE outline only
// when its rows overlap in some column; one that ends a row on the right and
// resumes on the left of the next comes out as two disjoint shapes, and the
// second needs its own legend or nothing says what it is.
export function pieceStarts(cells: Cell[]): number[] {
  const rows = bands(cells.map((c) => c.y))
  const cols = bands(cells.map((c) => c.x))
  const out: number[] = []
  // The previous row-run of each group: its row and column span.
  const last = new Map<string, { row: number; from: number; to: number }>()
  cells.forEach((c, i) => {
    const row = bandOf(rows, c.y)
    const col = bandOf(cols, c.x)
    const prev = last.get(c.group)
    if (!prev) {
      out.push(i)
      last.set(c.group, { row, from: col, to: col })
      return
    }
    if (prev.row === row) {
      prev.to = col
      return
    }
    // A new row of the same group: it joins the run above only if the two
    // share a column, so find how far right this run reaches first.
    let to = col
    for (let j = i + 1; j < cells.length; j++) {
      const d = cells[j]
      if (d.group !== c.group || bandOf(rows, d.y) !== row) break
      to = bandOf(cols, d.x)
    }
    if (prev.row !== row - 1 || prev.from > to) out.push(i)
    last.set(c.group, { row, from: col, to })
  })
  return out
}

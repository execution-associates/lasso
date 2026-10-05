// Outlines for groups of cards laid out in one continuous grid. A group is a
// run of consecutive cards, so once the grid wraps it is not a rectangle: it
// can start mid-row and end mid-row, and its outline has to hug the cells it
// holds (an L, a step) rather than box the rows it touches.
//
// The outline is drawn as a ring: the union of every cell grown by `pad`,
// minus the same union grown by `pad - stroke`. Neighbouring cells of one
// group are joined by bridge rects across the gutter between them, which is
// what merges them into one shape; cells of different groups get no bridge,
// so as long as `pad` is under half the gutter their outlines never touch.
// Painting white "outer" and black "inner" rects into one SVG mask produces
// the ring with no polygon clipping at all.

export interface Box {
  x: number
  y: number
  w: number
  h: number
}

export interface MaskRect extends Box {
  r: number
}

export interface Cell extends Box {
  group: string
}

export interface Ring {
  outer: MaskRect[]
  inner: MaskRect[]
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

export function groupRing(
  cells: Cell[],
  pad: number,
  stroke: number,
  radius: number
): Ring {
  const ring: Ring = { outer: [], inner: [] }
  const rows = bands(cells.map((c) => c.y))
  const cols = bands(cells.map((c) => c.x))
  const at = new Map<string, Cell>()
  for (const c of cells) at.set(`${bandOf(rows, c.y)},${bandOf(cols, c.x)}`, c)
  const same = (a: Cell, r: number, c: number) => {
    const b = at.get(`${r},${c}`)
    return b && b.group === a.group ? b : undefined
  }
  const ri = Math.max(0, radius - stroke)

  for (const c of cells) {
    ring.outer.push({
      x: c.x - pad,
      y: c.y - pad,
      w: c.w + 2 * pad,
      h: c.h + 2 * pad,
      r: radius,
    })
    ring.inner.push({
      x: c.x - pad + stroke,
      y: c.y - pad + stroke,
      w: c.w + 2 * (pad - stroke),
      h: c.h + 2 * (pad - stroke),
      r: ri,
    })
    const r = bandOf(rows, c.y)
    const k = bandOf(cols, c.x)
    const right = same(c, r, k + 1)
    const down = same(c, r + 1, k)
    if (right) {
      const top = Math.min(c.y, right.y)
      const bottom = Math.max(c.y + c.h, right.y + right.h)
      const x = c.x + c.w
      const w = right.x - x
      ring.outer.push({ x, y: top - pad, w, h: bottom - top + 2 * pad, r: 0 })
      ring.inner.push({
        x,
        y: top - pad + stroke,
        w,
        h: bottom - top + 2 * (pad - stroke),
        r: 0,
      })
    }
    if (down) {
      const left = Math.min(c.x, down.x)
      const rightEdge = Math.max(c.x + c.w, down.x + down.w)
      const y = c.y + c.h
      const h = down.y - y
      ring.outer.push({
        x: left - pad,
        y,
        w: rightEdge - left + 2 * pad,
        h,
        r: 0,
      })
      ring.inner.push({
        x: left - pad + stroke,
        y,
        w: rightEdge - left + 2 * (pad - stroke),
        h,
        r: 0,
      })
    }
    // Where four cells of one group meet, the square of gutter between them
    // is inside the shape; neither bridge reaches it.
    const diag = right && down ? same(c, r + 1, k + 1) : undefined
    if (diag) {
      const sq = {
        x: c.x + c.w,
        y: c.y + c.h,
        w: diag.x - (c.x + c.w),
        h: diag.y - (c.y + c.h),
        r: 0,
      }
      ring.outer.push(sq)
      ring.inner.push(sq)
    }
  }
  return ring
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

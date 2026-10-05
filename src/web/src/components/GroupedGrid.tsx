import * as React from "react"
import {
  type Box,
  type Cell,
  groupOutlines,
  pieceStarts,
} from "@/lib/group-outline"
import { cn } from "@/lib/utils"

// The agents grid with its groups drawn as outlines rather than as sections.
// Sections broke the grid at every group: a machine with one agent cost a
// header row plus a whole row of mostly empty width. Here every card flows
// through ONE grid in group order, and each group is a ring traced around the
// cells it occupies, labelled on its own top border like a fieldset legend.
// A group that wraps keeps one outline that steps around the row break.
//
// Two levels nest (machine, then repo inside it): the outer ring sits further
// out in the gutter than the inner one. Everything is painted in an overlay
// SVG measured from the cards' layout positions, so the cards themselves stay
// plain grid items (the FLIP reorder animation and the composers inside them
// are untouched).

export interface GroupLabel {
  name: string
  title?: string
  count: number
  blocked: number
  working: number
  // A later piece of a wrapped group: the name only, marked as continued.
  cont?: boolean
}

export interface GridGroup {
  key: string
  label: GroupLabel
  items: string[]
  // A second level inside this group; its items, concatenated, are `items`.
  children?: GridGroup[]
}

// Strokes are thick enough to read over a busy wallpaper, and each group's
// area is tinted too, so a region still reads as one when its line is lost
// behind a photo.
const STROKE_OUTER = 2
const STROKE_INNER = 1.5
const RADIUS = 10
// Gutters are sized to fit the rings: two nested rings need room for both on
// each side, plus clear space between neighbouring groups' outer rings.
const PAD_ONE = 8
const PAD_OUTER = 12
const PAD_INNER = 6
const LABEL_FONT_PX = 12
const LABEL_INSET = 12

function labelText(l: GroupLabel): string {
  if (l.cont) return `${l.name} cont.`
  let s = `${l.name} ${l.count}`
  if (l.blocked > 0) s += ` · ${l.blocked} blocked`
  if (l.working > 0) s += ` · ${l.working} working`
  return s
}

let measureCtx: CanvasRenderingContext2D | null | undefined
function textWidth(text: string, font: string): number {
  if (measureCtx === undefined)
    measureCtx = document.createElement("canvas").getContext("2d")
  if (!measureCtx) return text.length * 6.5
  measureCtx.font = font
  return measureCtx.measureText(text).width
}

interface PlacedLabel {
  key: string
  x: number
  y: number
  // The outer label, then (when the inner group starts on the same card, so
  // both legends would land on top of each other) the inner one beside it.
  parts: GroupLabel[]
  kinds: ("outer" | "inner")[]
}

interface Layout {
  // One ring per level, each painted in its own colour: machines in the
  // foreground tone, repos in the accent, so a nested pair reads as two
  // different things rather than a double line.
  outlines: string[]
  cuts: Box[]
  labels: PlacedLabel[]
  w: number
  h: number
}

export function GroupedGrid({
  groups,
  render,
  gridClass,
}: {
  groups: GridGroup[]
  // Must return the card keyed by `key` and carrying `data-grid-key={key}` on
  // its root, as a DIRECT child of the grid: a wrapper element would become
  // the FLIP hook's measuring parent and silently stop every reorder slide.
  render: (key: string) => React.ReactNode
  gridClass: string
}) {
  const wrapRef = React.useRef<HTMLDivElement>(null)
  const gridRef = React.useRef<HTMLDivElement>(null)
  const maskId = React.useId()
  const [layout, setLayout] = React.useState<Layout | null>(null)
  const nested = groups.some((g) => g.children)
  const order = React.useMemo(() => groups.flatMap((g) => g.items), [groups])

  const measure = React.useCallback(() => {
    const wrap = wrapRef.current
    const grid = gridRef.current
    if (!wrap || !grid) return
    const els = Array.from(
      grid.querySelectorAll<HTMLElement>(":scope > [data-grid-key]")
    )
    const box = new Map<string, Box>()
    for (const el of els) {
      // Layout offsets, not getBoundingClientRect: they ignore the transform a
      // FLIP animation puts on a moving card, so the ring is drawn where the
      // card is going rather than chasing it every frame.
      box.set(el.dataset.gridKey ?? "", {
        x: el.offsetLeft,
        y: el.offsetTop,
        w: el.offsetWidth,
        h: el.offsetHeight,
      })
    }
    const cellsFor = (gs: GridGroup[]): Cell[] =>
      gs.flatMap((g) =>
        g.items.flatMap((k) => {
          const b = box.get(k)
          return b ? [{ ...b, group: g.key }] : []
        })
      )
    const font = `${LABEL_FONT_PX}px ${getComputedStyle(wrap).fontFamily}`
    const outerPad = nested ? PAD_OUTER : PAD_ONE
    // The stroke is centred on the path, so the path runs half a stroke inside
    // `pad`: the line's outer edge lands where the outline belongs.
    const outerLine = groupOutlines(
      cellsFor(groups),
      outerPad - STROKE_OUTER / 2,
      RADIUS - STROKE_OUTER / 2
    )
    const inner = groups.flatMap((g) => g.children ?? [])
    const innerLine = inner.length
      ? groupOutlines(
          cellsFor(inner),
          PAD_INNER - STROKE_INNER / 2,
          RADIUS - 3 - STROKE_INNER / 2
        )
      : ""

    const labels: PlacedLabel[] = []
    const cuts: Box[] = []
    const place = (
      key: string,
      first: Box,
      pad: number,
      parts: GroupLabel[],
      kinds: PlacedLabel["kinds"]
    ) => {
      const x = first.x - pad + LABEL_INSET
      const y = first.y - pad
      const w =
        parts.reduce((n, p) => n + textWidth(labelText(p), font), 0) +
        (parts.length - 1) * 14
      labels.push({ key, x, y, parts, kinds })
      // Cut the stroke under the legend so the text sits in a gap in the line.
      // The cut spans both rings' lines, since an outer legend straddles the
      // inner ring's top edge too.
      cuts.push({ x: x - 6, y: y - 9, w: w + 20, h: 18 })
    }
    // Every legend is pinned to the card that starts its piece. An outer and
    // an inner legend starting on the same card would sit 6px apart and
    // overprint, so they merge into one legend on the outer line.
    const at = new Map<
      string,
      { outer?: GroupLabel; inner?: GroupLabel; key: string }
    >()
    const mark = (level: "outer" | "inner", gs: GridGroup[]) => {
      for (const g of gs) {
        const cells = g.items.flatMap((k) => {
          const b = box.get(k)
          return b ? [{ ...b, group: g.key, k }] : []
        })
        // One group's cells alone: pieceStarts reads rows and columns off
        // the cells it is given, which for a single group is still the grid.
        for (const i of pieceStarts(cells)) {
          const k = cells[i].k
          const slot = at.get(k) ?? { key: `${g.key}@${k}` }
          slot[level] = i === 0 ? g.label : { ...g.label, cont: true }
          at.set(k, slot)
        }
      }
    }
    mark("outer", groups)
    mark("inner", inner)
    for (const [k, slot] of at) {
      const first = box.get(k)
      if (!first) continue
      if (slot.outer && slot.inner)
        place(
          slot.key,
          first,
          outerPad,
          [slot.outer, slot.inner],
          ["outer", "inner"]
        )
      else if (slot.outer)
        place(slot.key, first, outerPad, [slot.outer], ["outer"])
      else if (slot.inner)
        place(slot.key, first, PAD_INNER, [slot.inner], ["inner"])
    }
    setLayout({
      outlines: inner.length ? [outerLine, innerLine] : [outerLine],
      cuts,
      labels,
      w: wrap.offsetWidth,
      h: wrap.offsetHeight,
    })
  }, [groups, nested])

  // Re-measure when the order changes (layout effect, so the first paint
  // already has its rings) and whenever the grid or a card resizes: a resize
  // can change the column count, which re-wraps every group.
  // biome-ignore lint/correctness/useExhaustiveDependencies: order is the trigger for a re-measure.
  React.useLayoutEffect(() => {
    measure()
  }, [measure, order])
  // biome-ignore lint/correctness/useExhaustiveDependencies: order is the trigger; a new card must be observed too.
  React.useEffect(() => {
    const wrap = wrapRef.current
    const grid = gridRef.current
    if (!wrap || !grid) return
    let frame = 0
    const ro = new ResizeObserver(() => {
      cancelAnimationFrame(frame)
      frame = requestAnimationFrame(measure)
    })
    ro.observe(wrap)
    for (const el of grid.children) ro.observe(el)
    return () => {
      cancelAnimationFrame(frame)
      ro.disconnect()
    }
  }, [measure, order])

  const pad = nested ? PAD_OUTER : PAD_ONE
  return (
    <div
      ref={wrapRef}
      // isolate: the tint layer sits at -z-10 behind the cards, which must
      // stay inside this stacking context rather than drop behind the page.
      className="relative isolate"
      // Room above the first row and around the edges for the outermost ring
      // and its legend.
      style={{ padding: `${pad + 8}px ${pad + 2}px ${pad + 2}px` }}
    >
      <div
        ref={gridRef}
        className={cn(
          gridClass,
          nested ? "gap-x-8 gap-y-10" : "gap-x-5 gap-y-8"
        )}
      >
        {order.map(render)}
      </div>
      {layout && (
        <>
          <svg
            aria-hidden="true"
            className="pointer-events-none absolute top-0 left-0 -z-10"
            width={layout.w}
            height={layout.h}
          >
            {layout.outlines.map((d, level) => (
              <path
                // biome-ignore lint/suspicious/noArrayIndexKey: the level IS the identity
                key={level}
                d={d}
                fill="currentColor"
                className={
                  level === 0 ? "text-foreground/[0.07]" : "text-primary/[0.09]"
                }
              />
            ))}
          </svg>
          <svg
            aria-hidden="true"
            className="pointer-events-none absolute top-0 left-0"
            width={layout.w}
            height={layout.h}
          >
            <defs>
              <mask id={maskId} maskUnits="userSpaceOnUse">
                <rect width={layout.w} height={layout.h} fill="white" />
                {layout.cuts.map((r, i) => (
                  <rect
                    // biome-ignore lint/suspicious/noArrayIndexKey: geometry with no identity, rebuilt on every measure
                    key={i}
                    x={r.x}
                    y={r.y}
                    width={r.w}
                    height={r.h}
                    fill="black"
                  />
                ))}
              </mask>
            </defs>
            <g mask={`url(#${maskId})`}>
              {layout.outlines.map((d, level) => (
                <path
                  // biome-ignore lint/suspicious/noArrayIndexKey: the level IS the identity
                  key={level}
                  d={d}
                  fill="none"
                  stroke="currentColor"
                  strokeWidth={level === 0 ? STROKE_OUTER : STROKE_INNER}
                  className={
                    level === 0 ? "text-foreground/75" : "text-primary/90"
                  }
                />
              ))}
            </g>
          </svg>
          {layout.labels.map((l) => (
            <div
              key={l.key}
              className="pointer-events-none absolute flex -translate-y-1/2 items-center gap-3.5 whitespace-nowrap rounded-full bg-background/85 px-1.5 py-0.5 leading-none shadow-sm"
              style={{ left: l.x, top: l.y, fontSize: LABEL_FONT_PX }}
            >
              {l.parts.map((p, i) => (
                <Legend
                  key={`${l.key}:${p.name}`}
                  label={p}
                  inner={l.kinds[i] === "inner"}
                />
              ))}
            </div>
          ))}
        </>
      )}
    </div>
  )
}

function Legend({ label, inner }: { label: GroupLabel; inner: boolean }) {
  if (label.cont)
    return (
      <span title={label.title}>
        <span
          className={cn(
            "font-medium",
            inner ? "text-primary" : "text-foreground"
          )}
        >
          {label.name}
        </span>
        <span className="ml-1.5 text-muted-foreground">cont.</span>
      </span>
    )
  return (
    <span title={label.title}>
      <span
        className={cn(
          "font-medium",
          inner ? "text-primary" : "text-foreground"
        )}
      >
        {label.name}
      </span>
      <span className="ml-1.5 text-muted-foreground">{label.count}</span>
      {label.blocked > 0 && (
        <span className="text-destructive"> · {label.blocked} blocked</span>
      )}
      {label.working > 0 && (
        <span className="text-primary"> · {label.working} working</span>
      )}
    </span>
  )
}

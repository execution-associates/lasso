import * as React from "react"

// Paging the Inbox by swipe, with the card under the finger.
//
// The card follows a horizontal drag; let go past a quarter of the width (or
// with a flick) and it slides out while the next one slides in from the other
// side, otherwise it springs back. The arrows page with the same slide. A
// gesture that starts mostly vertical is left to the card's scroll, and one
// that starts in a text field is left to the field: moving a cursor or
// selecting words must never page away. With reduced motion the page simply
// changes.

const OUT_MS = 170
const IN_MS = 230

type Phase =
  | { kind: "idle" }
  | { kind: "drag"; dx: number }
  | { kind: "out"; dir: 1 | -1 }
  | { kind: "enter"; dir: 1 | -1 }
  | { kind: "in" }
  | { kind: "settle" }

function reducedMotion() {
  return window.matchMedia("(prefers-reduced-motion: reduce)").matches
}

function startsInField(target: EventTarget | null): boolean {
  return (
    target instanceof Element &&
    target.closest("input, textarea, select, [contenteditable]") !== null
  )
}

export function useSwipePager({
  canPrev,
  canNext,
  onPage,
}: {
  canPrev: boolean
  canNext: boolean
  // dir 1 is the next card, -1 the previous one.
  onPage: (dir: 1 | -1) => void
}) {
  const [phase, setPhase] = React.useState<Phase>({ kind: "idle" })
  const width = React.useRef(390)
  const el = React.useRef<HTMLDivElement | null>(null)
  const start = React.useRef<{
    x: number
    y: number
    t: number
    axis: "x" | "y" | null
  } | null>(null)
  const timers = React.useRef<ReturnType<typeof setTimeout>[]>([])
  const later = (fn: () => void, ms: number) =>
    timers.current.push(setTimeout(fn, ms))
  React.useEffect(() => () => timers.current.forEach(clearTimeout), [])

  const busy = phase.kind === "out" || phase.kind === "enter"

  const page = (dir: 1 | -1) => {
    if (dir === 1 ? !canNext : !canPrev) return
    if (reducedMotion()) {
      onPage(dir)
      setPhase({ kind: "idle" })
      return
    }
    width.current = el.current?.clientWidth || width.current
    setPhase({ kind: "out", dir })
    later(() => {
      onPage(dir)
      // The new card starts off the opposite edge, then slides in on the
      // next frame, once it has been painted there.
      setPhase({ kind: "enter", dir })
      requestAnimationFrame(() =>
        requestAnimationFrame(() => {
          setPhase({ kind: "in" })
          later(() => setPhase({ kind: "idle" }), IN_MS)
        })
      )
    }, OUT_MS)
  }

  const onTouchStart = (e: React.TouchEvent) => {
    if (busy || e.touches.length !== 1 || startsInField(e.target)) {
      start.current = null
      return
    }
    const t = e.touches[0]
    start.current = { x: t.clientX, y: t.clientY, t: e.timeStamp, axis: null }
    width.current = el.current?.clientWidth || width.current
  }

  const onTouchMove = (e: React.TouchEvent) => {
    const s = start.current
    if (!s) return
    const t = e.touches[0]
    const dx = t.clientX - s.x
    const dy = t.clientY - s.y
    if (!s.axis) {
      if (Math.abs(dx) < 8 && Math.abs(dy) < 8) return
      s.axis = Math.abs(dx) > Math.abs(dy) * 1.2 ? "x" : "y"
    }
    if (s.axis !== "x") return
    // Past the first or last card the drag resists, so the edge is felt.
    const edge = dx > 0 ? !canPrev : !canNext
    setPhase({ kind: "drag", dx: edge ? dx * 0.25 : dx })
  }

  const onTouchEnd = (e: React.TouchEvent) => {
    const s = start.current
    start.current = null
    if (s?.axis !== "x") return
    const dx = e.changedTouches[0].clientX - s.x
    const speed = Math.abs(dx) / Math.max(1, e.timeStamp - s.t)
    const far = Math.abs(dx) > width.current * 0.25 || speed > 0.5
    const dir: 1 | -1 = dx < 0 ? 1 : -1
    if (far && (dir === 1 ? canNext : canPrev)) {
      page(dir)
      return
    }
    setPhase({ kind: "settle" })
    later(() => setPhase({ kind: "idle" }), IN_MS)
  }

  const w = width.current
  let style: React.CSSProperties
  switch (phase.kind) {
    case "drag":
      style = {
        transform: `translate3d(${phase.dx}px,0,0)`,
        transition: "none",
      }
      break
    case "out":
      style = {
        transform: `translate3d(${-phase.dir * w}px,0,0)`,
        opacity: 0.4,
        transition: `transform ${OUT_MS}ms cubic-bezier(0.4,0,1,1), opacity ${OUT_MS}ms linear`,
      }
      break
    case "enter":
      style = {
        transform: `translate3d(${phase.dir * w * 0.6}px,0,0)`,
        opacity: 0.4,
        transition: "none",
      }
      break
    case "in":
    case "settle":
      style = {
        transform: "translate3d(0,0,0)",
        opacity: 1,
        transition: `transform ${IN_MS}ms cubic-bezier(0.2,0.8,0.2,1), opacity ${IN_MS}ms linear`,
      }
      break
    default:
      style = {}
  }

  return {
    ref: el,
    style,
    // touch-action pan-y: the browser scrolls vertically and hands horizontal
    // moves to these handlers.
    handlers: {
      onTouchStart,
      onTouchMove,
      onTouchEnd,
      onTouchCancel: onTouchEnd,
    },
    page,
  }
}

import * as React from "react"

// Paging the Inbox like a carousel: the cards sit side by side and the strip
// follows the finger (or a mouse drag), so the next card slides in beside the
// one leaving. Let go past a quarter of the width, or with a flick, and it
// snaps to that card, otherwise it springs back. The arrows and a horizontal
// trackpad swipe page with the same slide. A gesture that starts mostly
// vertical is left to the card's scroll, and one that starts in a text field
// is left to the field: moving a cursor or selecting words must never page
// away. With reduced motion the page simply changes.

const SLIDE_MS = 260
// A trackpad swipe pages once it has moved this far, then waits for its
// momentum to die down before another can.
const WHEEL_PX = 60
const WHEEL_QUIET_MS = 250

type Phase =
  | { kind: "idle" }
  | { kind: "drag"; dx: number }
  | { kind: "slide"; dir: 1 | -1 }
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
  // The slide ends after a timeout, by which time the list may have moved:
  // page with the newest callback, not the one from when the slide began.
  const onPageRef = React.useRef(onPage)
  onPageRef.current = onPage
  const start = React.useRef<{
    id: number
    x: number
    y: number
    t: number
    axis: "x" | "y" | null
  } | null>(null)
  // Set once a drag has moved the strip, so the click that ends a mouse drag
  // on a button does not also press it.
  const dragged = React.useRef(false)
  const wheel = React.useRef({ acc: 0, locked: false, last: 0 })
  const timers = React.useRef<ReturnType<typeof setTimeout>[]>([])
  const later = (fn: () => void, ms: number) =>
    timers.current.push(setTimeout(fn, ms))
  React.useEffect(() => () => timers.current.forEach(clearTimeout), [])

  const busy = phase.kind === "slide"

  const page = (dir: 1 | -1) => {
    if (busy || (dir === 1 ? !canNext : !canPrev)) return
    if (reducedMotion()) {
      onPageRef.current(dir)
      setPhase({ kind: "idle" })
      return
    }
    setPhase({ kind: "slide", dir })
    // Paging and dropping the offset land in one render, so the card that
    // slid into the middle is simply there when the strip recentres.
    later(() => {
      onPageRef.current(dir)
      setPhase({ kind: "idle" })
    }, SLIDE_MS)
  }

  const release = (dx: number, dt: number) => {
    const speed = Math.abs(dx) / Math.max(1, dt)
    const far = Math.abs(dx) > width.current * 0.25 || speed > 0.5
    const dir: 1 | -1 = dx < 0 ? 1 : -1
    if (far && (dir === 1 ? canNext : canPrev)) {
      page(dir)
      return
    }
    setPhase({ kind: "settle" })
    later(() => setPhase({ kind: "idle" }), SLIDE_MS)
  }

  const onPointerDown = (e: React.PointerEvent) => {
    start.current = null
    if (busy || !e.isPrimary || e.button !== 0 || startsInField(e.target))
      return
    start.current = {
      id: e.pointerId,
      x: e.clientX,
      y: e.clientY,
      t: e.timeStamp,
      axis: null,
    }
    dragged.current = false
    width.current = el.current?.clientWidth || width.current
  }

  const onPointerMove = (e: React.PointerEvent) => {
    const s = start.current
    if (!s || e.pointerId !== s.id) return
    const dx = e.clientX - s.x
    const dy = e.clientY - s.y
    if (!s.axis) {
      if (Math.abs(dx) < 8 && Math.abs(dy) < 8) return
      s.axis = Math.abs(dx) > Math.abs(dy) * 1.2 ? "x" : "y"
      if (s.axis === "y") {
        start.current = null
        return
      }
      // Keep the drag when the pointer leaves the strip, and drop the text
      // selection a mouse drag started.
      el.current?.setPointerCapture(e.pointerId)
      window.getSelection()?.removeAllRanges()
      dragged.current = true
    }
    // Past the first or last card the drag resists, so the edge is felt.
    const edge = dx > 0 ? !canPrev : !canNext
    setPhase({ kind: "drag", dx: edge ? dx * 0.25 : dx })
  }

  const onPointerUp = (e: React.PointerEvent) => {
    const s = start.current
    start.current = null
    if (!s || e.pointerId !== s.id || s.axis !== "x") return
    release(e.clientX - s.x, e.timeStamp - s.t)
  }

  // The browser took the gesture over: put the strip back.
  const onPointerCancel = (e: React.PointerEvent) => {
    const s = start.current
    start.current = null
    if (!s || e.pointerId !== s.id || s.axis !== "x") return
    release(0, 1)
  }

  const onClickCapture = (e: React.MouseEvent) => {
    if (!dragged.current) return
    dragged.current = false
    e.preventDefault()
    e.stopPropagation()
  }

  const onWheel = (e: React.WheelEvent) => {
    const w = wheel.current
    if (Math.abs(e.deltaX) <= Math.abs(e.deltaY)) return
    if (e.timeStamp - w.last > WHEEL_QUIET_MS) {
      w.acc = 0
      w.locked = false
    }
    w.last = e.timeStamp
    if (w.locked || busy) return
    w.acc += e.deltaX
    if (Math.abs(w.acc) < WHEEL_PX) return
    w.locked = true
    page(w.acc > 0 ? 1 : -1)
  }

  // The strip holds three slots (previous, current, next); at rest it shows
  // the middle one.
  let style: React.CSSProperties
  switch (phase.kind) {
    case "drag":
      style = {
        transform: `translate3d(calc(-100% + ${phase.dx}px),0,0)`,
        transition: "none",
        userSelect: "none",
      }
      break
    case "slide":
      style = {
        transform: `translate3d(${-100 - phase.dir * 100}%,0,0)`,
        transition: `transform ${SLIDE_MS}ms cubic-bezier(0.2,0.8,0.2,1)`,
      }
      break
    case "settle":
      style = {
        transform: "translate3d(-100%,0,0)",
        transition: `transform ${SLIDE_MS}ms cubic-bezier(0.2,0.8,0.2,1)`,
      }
      break
    default:
      style = { transform: "translate3d(-100%,0,0)" }
  }

  return {
    ref: el,
    style,
    // touch-action pan-y on the element: the browser scrolls vertically and
    // hands horizontal moves to these handlers.
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp,
      onPointerCancel,
      onClickCapture,
      onWheel,
    },
    page,
  }
}

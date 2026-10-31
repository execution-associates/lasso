import * as React from "react"
import { cn } from "@/lib/utils"

type View = { s: number; x: number; y: number }
const RESET: View = { s: 1, x: 0, y: 0 }
const MAX_SCALE = 20

// Safari's trackpad pinch: non-standard, and the only pinch it reports.
type GestureEvent = Event & { scale: number; clientX: number; clientY: number }

// The file viewer's image, zoomable in place. A trackpad pinch reaches the page
// as a ctrl+wheel (Chrome, Firefox) or as gesture events (Safari); both are
// taken here with preventDefault, which is what stops the browser zooming the
// whole page instead. Ctrl+scroll on a mouse zooms the same way. Once zoomed,
// two-finger scroll and dragging pan, double-click toggles between fitted and
// actual size (at least 2×), and the badge resets. Zoom never goes below the
// fitted size, and panning stops at the image's edges.
export function ZoomableImage({
  src,
  alt,
  fit,
}: {
  src: string
  alt: string
  // Fit the viewer's height as well as its width (the gallery's viewer).
  fit: boolean
}) {
  const boxRef = React.useRef<HTMLDivElement>(null)
  const imgRef = React.useRef<HTMLImageElement>(null)
  const [view, setView] = React.useState<View>(RESET)
  // Read by the native listeners, which outlive any one render.
  const viewRef = React.useRef(view)
  const [dragging, setDragging] = React.useState(false)

  // Keep the image covering the box where it is bigger than the box, and
  // inside it where it is smaller.
  const clamp = React.useCallback((v: View): View => {
    const box = boxRef.current
    const img = imgRef.current
    if (!box || !img || v.s <= 1) return RESET
    const cur = viewRef.current
    const C = box.getBoundingClientRect()
    const R = img.getBoundingClientRect()
    // The untransformed layout: the rect includes the current transform, whose
    // origin is the image's centre.
    const w = R.width / cur.s
    const h = R.height / cur.s
    const cx = R.left + R.width / 2 - cur.x
    const cy = R.top + R.height / 2 - cur.y
    const axis = (
      t: number,
      half: number,
      c: number,
      lo: number,
      hi: number
    ) => {
      const a = lo + half - c
      const b = hi - half - c
      return Math.min(Math.max(a, b), Math.max(Math.min(a, b), t))
    }
    return {
      s: v.s,
      x: axis(v.x, (w * v.s) / 2, cx, C.left, C.right),
      y: axis(v.y, (h * v.s) / 2, cy, C.top, C.bottom),
    }
  }, [])

  const apply = React.useCallback(
    (v: View) => {
      const next = clamp(v)
      viewRef.current = next
      setView(next)
    },
    [clamp]
  )

  // Scale by `factor`, keeping the point under (px, py) where it is.
  const zoomAt = React.useCallback(
    (px: number, py: number, factor: number) => {
      const img = imgRef.current
      if (!img) return
      const cur = viewRef.current
      const s = Math.min(MAX_SCALE, Math.max(1, cur.s * factor))
      if (s === cur.s) return
      const R = img.getBoundingClientRect()
      const cx = R.left + R.width / 2 - cur.x
      const cy = R.top + R.height / 2 - cur.y
      apply({
        s,
        x: px - cx - ((px - cx - cur.x) * s) / cur.s,
        y: py - cy - ((py - cy - cur.y) * s) / cur.s,
      })
    },
    [apply]
  )

  // Native, non-passive listeners: React's onWheel is passive, so its
  // preventDefault could not stop the page zoom.
  React.useEffect(() => {
    const box = boxRef.current
    if (!box) return
    const onWheel = (e: WheelEvent) => {
      const unit = e.deltaMode === 1 ? 16 : e.deltaMode === 2 ? 400 : 1
      if (e.ctrlKey) {
        e.preventDefault()
        // A pinch sends small deltas, a mouse notch ~100: capped so one notch
        // is a step, not a leap.
        const d = Math.max(-25, Math.min(25, e.deltaY * unit))
        zoomAt(e.clientX, e.clientY, Math.exp(-d * 0.01))
        return
      }
      const cur = viewRef.current
      if (cur.s <= 1) return // not zoomed: let the viewer scroll
      e.preventDefault()
      apply({
        s: cur.s,
        x: cur.x - e.deltaX * unit,
        y: cur.y - e.deltaY * unit,
      })
    }
    let base = 1
    const onGestureStart = (e: Event) => {
      e.preventDefault()
      base = viewRef.current.s
    }
    const onGestureChange = (e: Event) => {
      e.preventDefault()
      const g = e as GestureEvent
      zoomAt(g.clientX, g.clientY, (base * g.scale) / viewRef.current.s)
    }
    const stop = (e: Event) => e.preventDefault()
    box.addEventListener("wheel", onWheel, { passive: false })
    box.addEventListener("gesturestart", onGestureStart)
    box.addEventListener("gesturechange", onGestureChange)
    box.addEventListener("gestureend", stop)
    // The box resizing (sidebar dragged, window resized) can leave a zoomed
    // image short of an edge; re-clamp.
    const ro = new ResizeObserver(() => {
      if (viewRef.current.s > 1) apply(viewRef.current)
    })
    ro.observe(box)
    return () => {
      box.removeEventListener("wheel", onWheel)
      box.removeEventListener("gesturestart", onGestureStart)
      box.removeEventListener("gesturechange", onGestureChange)
      box.removeEventListener("gestureend", stop)
      ro.disconnect()
    }
  }, [apply, zoomAt])

  // Dragging pans while zoomed.
  const drag = React.useRef<{ id: number; x: number; y: number } | null>(null)
  const onPointerDown = (e: React.PointerEvent) => {
    if (e.button !== 0 || viewRef.current.s <= 1) return
    e.currentTarget.setPointerCapture(e.pointerId)
    drag.current = { id: e.pointerId, x: e.clientX, y: e.clientY }
    setDragging(true)
  }
  const onPointerMove = (e: React.PointerEvent) => {
    const d = drag.current
    if (!d || d.id !== e.pointerId) return
    const cur = viewRef.current
    apply({ s: cur.s, x: cur.x + e.clientX - d.x, y: cur.y + e.clientY - d.y })
    d.x = e.clientX
    d.y = e.clientY
  }
  const endDrag = (e: React.PointerEvent) => {
    if (drag.current?.id !== e.pointerId) return
    drag.current = null
    setDragging(false)
  }

  const onDoubleClick = (e: React.MouseEvent) => {
    const img = imgRef.current
    if (!img) return
    if (viewRef.current.s > 1) {
      apply(RESET)
      return
    }
    const actual = img.naturalWidth / img.getBoundingClientRect().width
    zoomAt(e.clientX, e.clientY, Math.max(2, Math.min(MAX_SCALE, actual)))
  }

  const zoomed = view.s > 1
  return (
    <div
      ref={boxRef}
      className={cn("vimg", fit && "fit", zoomed && "zoomed")}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={endDrag}
      onPointerCancel={endDrag}
      onDoubleClick={onDoubleClick}
      style={{
        cursor: zoomed ? (dragging ? "grabbing" : "grab") : "zoom-in",
      }}
    >
      <img
        ref={imgRef}
        src={src}
        alt={alt}
        draggable={false}
        style={
          zoomed
            ? {
                transform: `translate(${view.x}px, ${view.y}px) scale(${view.s})`,
              }
            : undefined
        }
      />
      {zoomed && (
        <button
          type="button"
          title="Back to fitted (double-click)"
          className="absolute top-2 right-2 rounded bg-black/60 px-1.5 py-0.5 text-[11px] text-white tabular-nums hover:bg-black/75"
          onPointerDown={(e) => e.stopPropagation()}
          onDoubleClick={(e) => e.stopPropagation()}
          onClick={() => apply(RESET)}
        >
          {Math.round(view.s * 100)}%
        </button>
      )}
    </div>
  )
}

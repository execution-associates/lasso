import * as React from "react"

import { QR_QUIET, qrEncode, qrPath } from "@/lib/qr"
import { cn } from "@/lib/utils"

// The code as SVG: sharp at any size. Always black modules on a white quiet
// zone, whatever the theme, because scanners need dark-on-light with a margin;
// a dark theme gets a white tile, not an inverted code.
export function QRCode({
  value,
  className,
  label = "QR code",
}: {
  value: string
  className?: string
  label?: string
}) {
  const code = React.useMemo(() => qrEncode(value), [value])
  const d = React.useMemo(() => qrPath(code), [code])
  const n = code.size + 2 * QR_QUIET
  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${n} ${n}`}
      shapeRendering="crispEdges"
      className={cn("block aspect-square bg-white", className)}
    >
      <rect width={n} height={n} fill="#ffffff" />
      <path d={d} fill="#000000" />
    </svg>
  )
}

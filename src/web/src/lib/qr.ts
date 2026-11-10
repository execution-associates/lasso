import { encode } from "uqr"

// A QR code made in the browser from the text itself. The pairing link carries
// a one-time credential, so nothing here (and nothing that calls it) may send
// the text anywhere: uqr is a pure encoder with no dependencies.

// The quiet zone the spec asks scanners to find around a code, in modules.
export const QR_QUIET = 4

export type QrCode = {
  // rows[y][x] is true for a dark module; no border (the quiet zone is drawn
  // by the renderer).
  rows: boolean[][]
  size: number
}

// Error correction M, raised to the highest level that still fits the same
// version (boostEcc), so the code is no bigger than M's and is as forgiving as
// the space allows.
export function qrEncode(text: string): QrCode {
  const r = encode(text, { ecc: "M", boostEcc: true, border: 0 })
  return { rows: r.data, size: r.size }
}

// The dark modules as one SVG path: a run of dark modules in a row is a single
// `h` segment, in module units (the viewBox is size + 2 * QR_QUIET wide).
export function qrPath(code: QrCode): string {
  const parts: string[] = []
  for (let y = 0; y < code.size; y++) {
    const row = code.rows[y]
    let x = 0
    while (x < code.size) {
      if (!row[x]) {
        x++
        continue
      }
      const start = x
      while (x < code.size && row[x]) x++
      parts.push(
        `M${start + QR_QUIET} ${y + QR_QUIET}h${x - start}v1h-${x - start}z`
      )
    }
  }
  return parts.join("")
}

import { describe, expect, test } from "bun:test"
import jsQR from "jsqr"

import { QR_QUIET, qrEncode, qrPath } from "@/lib/qr"

// Rasterize the way a screen would show the SVG: white quiet zone, black
// modules, `scale` pixels to a module.
function raster(text, scale) {
  const code = qrEncode(text)
  const n = code.size + 2 * QR_QUIET
  const w = n * scale
  const px = new Uint8ClampedArray(w * w * 4).fill(255)
  for (let y = 0; y < code.size; y++)
    for (let x = 0; x < code.size; x++) {
      if (!code.rows[y][x]) continue
      for (let dy = 0; dy < scale; dy++)
        for (let dx = 0; dx < scale; dx++) {
          const i =
            (((y + QR_QUIET) * scale + dy) * w + (x + QR_QUIET) * scale + dx) *
            4
          px[i] = px[i + 1] = px[i + 2] = 0
        }
    }
  return { px, w }
}

// Rasterize the path string itself (what QRCode draws), so a bug in qrPath
// shows up as a failed decode rather than passing on the matrix alone.
function rasterPath(text, scale) {
  const code = qrEncode(text)
  const n = code.size + 2 * QR_QUIET
  const w = n * scale
  const px = new Uint8ClampedArray(w * w * 4).fill(255)
  for (const m of qrPath(code).matchAll(/M(\d+) (\d+)h(\d+)v1h-\d+z/g)) {
    const [x, y, len] = [Number(m[1]), Number(m[2]), Number(m[3])]
    for (let dy = 0; dy < scale; dy++)
      for (let dx = 0; dx < len * scale; dx++) {
        const i = ((y * scale + dy) * w + x * scale + dx) * 4
        px[i] = px[i + 1] = px[i + 2] = 0
      }
  }
  return { px, w }
}

const LINKS = [
  "https://lasso.example.com/client/pair#code=lpc_AbCdEfGhIjKlMnOpQrStUv",
  "http://127.0.0.1:8090/client/pair#code=lpc_0123456789abcdefghijkl",
  "https://lasso.tail1234.ts.net:8443/client/pair#code=lpc_-_-_-_-_-_-_-_-_-_-_",
]

describe("the pairing QR code", () => {
  for (const link of LINKS) {
    test(`decodes back to ${link.slice(0, 40)}…`, () => {
      for (const scale of [4, 8]) {
        const { px, w } = raster(link, scale)
        expect(jsQR(px, w, w)?.data).toBe(link)
      }
    })
    test(`its SVG path decodes to the same link (${link.length} chars)`, () => {
      const { px, w } = rasterPath(link, 6)
      expect(jsQR(px, w, w)?.data).toBe(link)
    })
  }

  test("survives damage that error correction covers", () => {
    const link = LINKS[0]
    const { px, w } = raster(link, 8)
    // Blank a 3% patch away from the finder patterns.
    const c = Math.floor(w / 2)
    const r = Math.floor(w * 0.08)
    for (let y = c - r; y < c + r; y++)
      for (let x = c - r; x < c + r; x++) {
        const i = (y * w + x) * 4
        px[i] = px[i + 1] = px[i + 2] = 255
      }
    expect(jsQR(px, w, w)?.data).toBe(link)
  })

  test("is no larger than a version-8 code for a typical link", () => {
    expect(qrEncode(LINKS[0]).size).toBeLessThanOrEqual(49)
  })
})

// The chrome's character: a still film grain and two pools of light in the
// palette's own colors, quantized through an 8x8 Bayer matrix into 2px cells
// (the dithered look), plus the derived tokens fx.css paints sheens, glows and
// depth from. docs/design/theme-atmosphere.md, "Character layer".
//
// Rendered, not run. Each change of palette, texture level or viewport size
// draws the light into one viewport-sized image (at one pixel per 2px cell)
// and the grain into a 256px tile, and hands both to CSS as blob URLs.
// Grounds (.fx-ground) paint them viewport-fixed, so the light reads as one
// field across the sidebar, the footer and the bots list, while the terminal
// and every reading surface — plates — never carry any of it. Nothing
// animates, so nothing loops, and there is no live GL context to lose.

import type { Texture } from "@/lib/api"
import { isLightSurface } from "@/lib/contrast"

export type CharacterInput = {
  // The chrome follows a palette and the texture is not off.
  on: boolean
  texture: Texture
  base: string // the palette's background
  a: string // blue, else cyan
  b: string // magenta, else green
  c: string // yellow, else red
}

const STRENGTH: Record<Texture, number> = { off: 0, subtle: 0.55, full: 1 }

let last = ""
let glowURL = ""
let grainURL = ""
let pending: number | undefined
let current: CharacterInput | null = null
let resizeHooked = false

function rgb(hex: string): [number, number, number] | null {
  if (!/^#[0-9a-f]{6}$/i.test(hex)) return null
  const n = Number.parseInt(hex.slice(1), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}

function mix(a: string, b: string, t: number): string {
  const x = rgb(a)
  const y = rgb(b)
  if (!x || !y) return a
  return `#${x
    .map((v, i) =>
      Math.round(v + (y[i] - v) * t)
        .toString(16)
        .padStart(2, "0")
    )
    .join("")}`
}

// bayer8 is the 8x8 ordered-dither matrix, built by the usual recursion.
const BAYER = (() => {
  let m = [[0]]
  for (let n = 1; n < 8; n *= 2) {
    const next: number[][] = []
    for (let y = 0; y < n * 2; y++) {
      next.push([])
      for (let x = 0; x < n * 2; x++) {
        const q = [
          [0, 2],
          [3, 1],
        ][Math.floor(y / n)][Math.floor(x / n)]
        next[y].push(m[y % n][x % n] * 4 + q)
      }
    }
    m = next
  }
  return m.map((row) => row.map((v) => (v + 0.5) / 64))
})()

function toBlobURL(canvas: HTMLCanvasElement): Promise<string> {
  return new Promise((resolve) =>
    canvas.toBlob((b) => resolve(b ? URL.createObjectURL(b) : ""), "image/png")
  )
}

// drawGlow renders the two pools: one from beyond the top-right corner, one
// from the bottom-left, each a ramp between two of the palette's accents.
async function drawGlow(p: CharacterInput, light: boolean, k: number) {
  const w = Math.max(1, Math.ceil(window.innerWidth / 2))
  const h = Math.max(1, Math.ceil(window.innerHeight / 2))
  const canvas = document.createElement("canvas")
  canvas.width = w
  canvas.height = h
  const ctx = canvas.getContext("2d")
  const A = rgb(p.a)
  const B = rgb(p.b)
  const C = rgb(p.c)
  if (!ctx || !A || !B || !C) return ""
  const img = ctx.createImageData(w, h)
  const glow = (light ? 0.22 : 0.3) * k
  const step = glow / 5
  const asp = w / h
  for (let y = 0; y < h; y++) {
    const v = y / h
    for (let x = 0; x < w; x++) {
      const u = x / w
      const d1 = Math.hypot((u - 0.95) * asp, v + 0.05)
      const d2 = Math.hypot((u - 0.02) * asp, v - 1.05)
      const g1 = Math.max(0, 1 - d1 / 1.05) ** 1.7
      const g2 = Math.max(0, 1 - d2 / 0.8) ** 1.9 * 0.75
      const th = BAYER[y & 7][x & 7]
      const q1 = Math.min(
        glow,
        Math.max(0, Math.floor((g1 * glow) / step + th) * step)
      )
      const q2 = Math.min(
        glow,
        Math.max(0, Math.floor((g2 * glow) / step + th) * step)
      )
      const a = Math.min(1, q1 + q2)
      if (a <= 0) continue
      const t1 = Math.min(1, d1)
      const t2 = Math.min(1, d2 * 1.4)
      const i = (y * w + x) * 4
      for (let ch = 0; ch < 3; ch++) {
        const c1 = B[ch] + (A[ch] - B[ch]) * t1
        const c2 = C[ch] + (B[ch] - C[ch]) * t2
        img.data[i + ch] = (c1 * q1 + c2 * q2) / a
      }
      img.data[i + 3] = a * 255
    }
  }
  ctx.putImageData(img, 0, 0)
  return toBlobURL(canvas)
}

// drawGrain renders a 256px tile of single-pixel specks: light and dark on a
// dark canvas, dark only on a light one (white specks read as dust on paper).
async function drawGrain(light: boolean, k: number) {
  const size = 256
  const canvas = document.createElement("canvas")
  canvas.width = size
  canvas.height = size
  const ctx = canvas.getContext("2d")
  if (!ctx) return ""
  const img = ctx.createImageData(size, size)
  const amount = (light ? 0.05 : 0.075) * Math.max(k, 0.6)
  for (let i = 0; i < size * size; i++) {
    const g = (Math.random() - 0.5) * amount
    const v = light ? 0 : g > 0 ? 255 : 0
    img.data[i * 4] = v
    img.data[i * 4 + 1] = v
    img.data[i * 4 + 2] = v
    img.data[i * 4 + 3] = Math.abs(g) * 255
  }
  ctx.putImageData(img, 0, 0)
  return toBlobURL(canvas)
}

async function render() {
  pending = undefined
  const p = current
  const root = document.documentElement
  if (!p?.on) return
  const light = isLightSurface(p.base) === true
  const k = STRENGTH[p.texture]
  const key = JSON.stringify([p, light, window.innerWidth, window.innerHeight])
  if (key === last) return
  last = key
  const [glow, grain] = await Promise.all([
    drawGlow(p, light, k),
    drawGrain(light, k),
  ])
  // A newer render has been asked for meanwhile: let it win.
  if (key !== last) {
    for (const u of [glow, grain]) if (u) URL.revokeObjectURL(u)
    return
  }
  for (const u of [glowURL, grainURL]) if (u) URL.revokeObjectURL(u)
  glowURL = glow
  grainURL = grain
  root.style.setProperty("--fx-glow", glow ? `url("${glow}")` : "none")
  root.style.setProperty("--fx-grain", grain ? `url("${grain}")` : "none")
}

function schedule(delay: number) {
  if (pending !== undefined) clearTimeout(pending)
  pending = window.setTimeout(() => void render(), delay)
}

// applyCharacter is called from applyAtmosphere, the one repaint chokepoint:
// the palette, the backdrop and the texture level all land there.
export function applyCharacter(p: CharacterInput) {
  current = p
  const root = document.documentElement
  if (!p.on) {
    root.removeAttribute("data-fx")
    return
  }
  const light = isLightSurface(p.base) === true
  root.setAttribute("data-fx", p.texture)
  root.style.setProperty("--fx-a", p.a)
  root.style.setProperty("--fx-b", p.b)
  root.style.setProperty("--fx-c", p.c)
  root.style.setProperty(
    "--fx-k",
    String(STRENGTH[p.texture] * (light ? 0.6 : 1))
  )
  root.style.setProperty(
    "--fx-ink",
    light ? mix(p.base, "#000000", 0.55) : mix(p.base, "#000000", 0.6)
  )
  if (!resizeHooked) {
    resizeHooked = true
    window.addEventListener("resize", () => schedule(250))
  }
  schedule(60)
}

// What a plugin page is told about lasso's look, so it can match it: the
// scheme, the terminal palette, lasso's own design tokens and fonts. A plugin
// frame is an opaque origin and cannot read this document's CSS, so the bridge
// carries it (theme.get / the `theme` push / fonts.get), and the plugin SDK
// (/plugins/_sdk/lasso.js) applies it under the SAME custom property names
// lasso uses. Nothing here is secret: it is what is on the human's screen.

import { paletteColors } from "@/lib/theme"

// The tokens shared, by lasso's own names. An allowlist rather than every
// custom property on <html>: these are the stable vocabulary (shadcn's plus
// lasso's status colors, type, radius, elevation, chat text); the rest are
// internals a plugin must not come to depend on.
export const PLUGIN_THEME_TOKENS = [
  "--background",
  "--foreground",
  "--card",
  "--card-foreground",
  "--popover",
  "--popover-foreground",
  "--primary",
  "--primary-foreground",
  "--secondary",
  "--secondary-foreground",
  "--muted",
  "--muted-foreground",
  "--accent",
  "--accent-foreground",
  "--destructive",
  "--border",
  "--input",
  "--ring",
  "--color-good",
  "--color-warn",
  "--color-bad",
  "--font-sans",
  "--font-display",
  "--font-label",
  "--font-mono",
  "--font-chat",
  "--radius",
  "--shadow-elev-sm",
  "--shadow-elev-md",
  "--shadow-elev-pop",
  "--shadow-elev-modal",
  "--chat-size",
  "--chat-weight",
  "--chat-leading",
  "--chat-tracking",
  "--chat-measure",
] as const

const FONT_SLOTS = [
  "--font-sans",
  "--font-display",
  "--font-label",
  "--font-mono",
  "--font-chat",
]

function tokens(): Record<string, string> {
  const cs = getComputedStyle(document.documentElement)
  const out: Record<string, string> = {}
  for (const name of PLUGIN_THEME_TOKENS) {
    // Computed custom properties come back with var() already substituted.
    const v = cs.getPropertyValue(name).trim()
    if (v) out[name] = v
  }
  return out
}

// A face of a font lasso is using, as the bridge hands it over.
export interface PluginFontFace {
  family: string
  weight: string
  style: string
  unicode_range?: string
  data: ArrayBuffer
}

interface FaceRef {
  family: string
  weight: string
  style: string
  range: string
  url: string
}

// The first family named in a stack: the one lasso means. The rest are
// fallbacks the plugin's own system already has.
function firstFamily(stack: string): string {
  const first = stack.split(",")[0]?.trim() ?? ""
  return first.replace(/^["']|["']$/g, "")
}

// Whether a unicode-range covers one code point. Only the faces covering
// Latin are sent: the Vietnamese, Cyrillic and Greek subsets would multiply
// the bytes for text a plugin page almost never sets.
function rangeCovers(range: string, cp: number): boolean {
  if (!range) return true
  for (const raw of range.split(",")) {
    const t = raw.trim().toUpperCase().replace(/^U\+/, "")
    if (!t) continue
    let lo: number
    let hi: number
    if (t.includes("?")) {
      lo = Number.parseInt(t.replace(/\?/g, "0"), 16)
      hi = Number.parseInt(t.replace(/\?/g, "F"), 16)
    } else if (t.includes("-")) {
      const [a, b] = t.split("-")
      lo = Number.parseInt(a, 16)
      hi = Number.parseInt(b, 16)
    } else {
      lo = hi = Number.parseInt(t, 16)
    }
    if (cp >= lo && cp <= hi) return true
  }
  return false
}

// faceRefs walks this document's @font-face rules for the families the font
// slots name. Every sheet that declares a face lasso uses is lasso's own
// (Vite's bundle, the typography <style>), so cssRules is readable; a sheet
// that is not is skipped.
function faceRefs(): FaceRef[] {
  const cs = getComputedStyle(document.documentElement)
  const families = new Set(
    FONT_SLOTS.map((s) => firstFamily(cs.getPropertyValue(s))).filter(Boolean)
  )
  const out: FaceRef[] = []
  const seen = new Set<string>()
  for (const sheet of Array.from(document.styleSheets)) {
    let rules: CSSRuleList
    try {
      rules = sheet.cssRules
    } catch {
      continue
    }
    for (const rule of Array.from(rules)) {
      if (!(rule instanceof CSSFontFaceRule)) continue
      const family = firstFamily(rule.style.getPropertyValue("font-family"))
      if (!families.has(family)) continue
      const range = rule.style.getPropertyValue("unicode-range").trim()
      if (!rangeCovers(range, 0x41)) continue
      // The first url() of src is the best format the bundle offers (woff2).
      const m = /url\(\s*["']?([^"')]+)["']?\s*\)/.exec(
        rule.style.getPropertyValue("src")
      )
      if (!m) continue
      const url = new URL(m[1], sheet.href ?? document.baseURI)
      if (url.origin !== window.location.origin) continue
      if (seen.has(url.href)) continue
      seen.add(url.href)
      out.push({
        family,
        weight: rule.style.getPropertyValue("font-weight").trim() || "400",
        style: rule.style.getPropertyValue("font-style").trim() || "normal",
        range,
        url: url.href,
      })
    }
  }
  return out
}

// Bounds on what one fonts.get may carry; past them the rest of the faces are
// left out and the page falls back to the stack's next family.
const MAX_FACES = 40
const MAX_FONT_BYTES = 6 * 1024 * 1024

const fontBytes = new Map<string, Promise<ArrayBuffer | null>>()

function fetchFont(url: string): Promise<ArrayBuffer | null> {
  let p = fontBytes.get(url)
  if (!p) {
    p = fetch(url)
      .then((r) => (r.ok ? r.arrayBuffer() : null))
      .catch(() => null)
    fontBytes.set(url, p)
  }
  return p
}

// pluginFontFaces fetches (this document is same-origin with lasso, and
// authenticated) the faces of the fonts lasso is using and hands the bytes
// over. The frame cannot fetch them itself: fonts load in CORS mode, an
// opaque origin is cross-origin to everything, and lasso's files sit behind
// its auth. The page registers them with the FontFace API.
export async function pluginFontFaces(): Promise<PluginFontFace[]> {
  const refs = faceRefs().slice(0, MAX_FACES)
  const bufs = await Promise.all(refs.map((r) => fetchFont(r.url)))
  const out: PluginFontFace[] = []
  let total = 0
  refs.forEach((r, i) => {
    const data = bufs[i]
    if (!data || total + data.byteLength > MAX_FONT_BYTES) return
    total += data.byteLength
    out.push({
      family: r.family,
      weight: r.weight,
      style: r.style,
      unicode_range: r.range || undefined,
      // A copy: the cached buffer is shared by every frame that asks.
      data: data.slice(0),
    })
  })
  return out
}

// fontsKey names the current set of faces, so a page reloads fonts only when
// a typography change actually swapped them.
function fontsKey(): string {
  return faceRefs()
    .map((r) => r.url)
    .sort()
    .join("\n")
}

let lastKeyInput = ""
let lastKey = ""
function hashKey(s: string): string {
  if (s === lastKeyInput) return lastKey
  let h = 5381
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0
  lastKeyInput = s
  lastKey = (h >>> 0).toString(36)
  return lastKey
}

// The theme a plugin page is told. `colors` is the terminal palette's hexes
// (unchanged from the first protocol); `tokens` and `fonts_key` were added
// after, so a page written before them keeps working.
export function themeSnapshot() {
  return {
    dark: document.documentElement.classList.contains("dark"),
    colors: paletteColors(),
    tokens: tokens(),
    fonts_key: hashKey(fontsKey()),
  }
}

// One observer for every plugin frame: appearance changes arrive by several
// routes (a palette <style>, typography and chat text set on <html>, the dark
// class), and watching what they all write is simpler and surer than hooking
// each. Debounced, and each listener diffs before it pushes.
const listeners = new Set<() => void>()
let observer: MutationObserver | null = null
let timer: ReturnType<typeof setTimeout> | null = null

export function onAppearanceChanged(fn: () => void): () => void {
  listeners.add(fn)
  if (!observer) {
    observer = new MutationObserver(() => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => {
        timer = null
        for (const l of listeners) l()
      }, 150)
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class", "style", "data-theme"],
    })
    observer.observe(document.head, {
      childList: true,
      subtree: true,
      characterData: true,
    })
  }
  return () => {
    listeners.delete(fn)
    if (listeners.size === 0 && observer) {
      observer.disconnect()
      observer = null
    }
  }
}

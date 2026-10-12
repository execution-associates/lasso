import * as React from "react"

import type { PhoneText, PhoneTextField, PhoneTextPatch } from "@/lib/api"
import { CHAT_TEXT_DEFAULTS, CHAT_TEXT_SPEC } from "@/lib/chat-text"
import { ensureContrast } from "@/lib/contrast"
import { onThemeApplied } from "@/lib/theme"
import { patchUIState } from "@/lib/ui-state"

// Phone text: how the simple phone app (/m) sets what it shows you to read —
// the conversations, and the Inbox's cards. ui_state.phone_text, merged per
// field like chat_text and held to the same ranges (the server's
// phoneTextRanges is picked out of chatTextRanges), over the defaults below.
//
// It is its own store, not chat_text, because a phone is read in one narrow
// column at arm's length: its body text wants to be larger than a desktop
// chat's (17px, the size phones set body text at, against the chat's 15px),
// and a size picked on one screen must not resize the other. There is no
// column width (the phone's column is the screen) and no plugin style.
//
// The values are written as custom properties on the simple app's root
// (phoneTextStyle), never on <html>: the --chat-* ones override the chat's
// inside the app, so every .chat-text rule in index.css applies unchanged, and
// --phone-*-scale carry the size to the smaller text around the prose (see
// .phone-text in index.css).

export const PHONE_TEXT_FIELDS: PhoneTextField[] = [
  "size",
  "weight",
  "line_height",
  "letter_spacing",
  "backing",
]

export const PHONE_TEXT_DEFAULTS: Required<Record<PhoneTextField, number>> = {
  size: 17,
  weight: 400,
  line_height: 1.6,
  letter_spacing: 0,
  // Stronger than the desktop's 0.55: on a phone the conversation is the
  // whole screen, so a backdrop sits behind every line of it.
  backing: 0.7,
}

// The controls' spans and steps, inside the stored ranges (CHAT_TEXT_SPEC's
// min and max, which restate the server's). Weight offers what lasso's own
// face has, 300-700.
export const PHONE_TEXT_CONTROLS: Record<
  PhoneTextField,
  { span: [number, number]; step: number; label: string }
> = {
  size: { span: [14, 24], step: 1, label: "Size" },
  weight: { span: [300, 700], step: 100, label: "Weight" },
  line_height: { span: [1.3, 2.1], step: 0.1, label: "Line height" },
  letter_spacing: { span: [-0.02, 0.08], step: 0.01, label: "Letter spacing" },
  backing: { span: [0, 1], step: 0.1, label: "Reading panel" },
}

// The chat's formats, except letter spacing at the phone's step: two places.
export function formatPhoneText(f: PhoneTextField, v: number): string {
  if (f === "letter_spacing") return `${v > 0 ? "+" : ""}${v.toFixed(2)}em`
  return CHAT_TEXT_SPEC[f].format(v)
}

function valid(field: PhoneTextField, v: unknown): v is number {
  const s = CHAT_TEXT_SPEC[field]
  return typeof v === "number" && Number.isFinite(v) && v >= s.min && v <= s.max
}

// effectivePhoneText is what each field resolves to, and whether the human set
// it (Settings enables Reset on that).
export function effectivePhoneText(text: PhoneText): {
  values: Required<Record<PhoneTextField, number>>
  set: Record<PhoneTextField, boolean>
} {
  const values = { ...PHONE_TEXT_DEFAULTS }
  const set = {} as Record<PhoneTextField, boolean>
  for (const f of PHONE_TEXT_FIELDS) {
    set[f] = valid(f, text[f])
    if (set[f]) values[f] = text[f] as number
  }
  return { values, set }
}

// phoneTextStyle is the simple app root's custom properties. Every value has
// passed `valid` (or is a default), and each is written through the chat's own
// css() so the units can never disagree with the chat's.
export function phoneTextStyle(
  text: PhoneText,
  muted?: string
): React.CSSProperties {
  const { values } = effectivePhoneText(text)
  const style: Record<string, string> = {}
  for (const f of PHONE_TEXT_FIELDS) {
    const spec = CHAT_TEXT_SPEC[f]
    style[spec.prop] = spec.css(values[f])
  }
  // The text around the prose scales with it. The chat view's own sizes were
  // set beside a 15px chat, the Inbox's beside the phone's 17px, so each has
  // its own reference and reads as designed at its own default.
  style["--phone-chat-scale"] = String(values.size / CHAT_TEXT_DEFAULTS.size)
  style["--phone-card-scale"] = String(values.size / PHONE_TEXT_DEFAULTS.size)
  if (muted) style["--phone-muted"] = muted
  return style as React.CSSProperties
}

// setPhoneText writes the named fields only — the server merges per field.
// null clears a field back to the default.
export function setPhoneText(patch: PhoneTextPatch) {
  patchUIState({ phone_text: patch })
}

export function resetPhoneText() {
  const patch: PhoneTextPatch = {}
  for (const f of PHONE_TEXT_FIELDS) patch[f] = null
  setPhoneText(patch)
}

// Secondary text on a phone is read, not glanced at: every caption, the
// reasoning, a tool row's detail. The chrome holds the theme's muted colour to
// 3.2:1 (lib/theme.ts), a bar set so the brightness hierarchy survives on a
// desktop; here it is lifted to WCAG AA for body text against both the canvas
// and the cards, and only when it falls short, so a palette whose muted colour
// already reads (lasso's own flat light and dark among them) is left exactly as
// it is.
const PHONE_MUTED_CONTRAST = 4.5

function readMuted(): string | undefined {
  const cs = getComputedStyle(document.documentElement)
  const muted = cs.getPropertyValue("--h-muted").trim()
  const bg = cs.getPropertyValue("--h-bg").trim()
  const panel = cs.getPropertyValue("--h-panel").trim() || bg
  if (!muted || !bg) return undefined
  const lifted = ensureContrast(
    ensureContrast(muted, bg, PHONE_MUTED_CONTRAST),
    panel,
    PHONE_MUTED_CONTRAST
  )
  return lifted === muted ? undefined : lifted
}

// usePhoneMuted follows the palette: the mode class on <html>, the palette
// style node lib/theme.ts rewrites in <head>, and refreshTheme finishing.
export function usePhoneMuted(): string | undefined {
  const [muted, setMuted] = React.useState(readMuted)
  React.useEffect(() => {
    const update = () => setMuted(readMuted())
    const obs = new MutationObserver(update)
    obs.observe(document.documentElement, { attributes: true })
    obs.observe(document.head, {
      childList: true,
      subtree: true,
      characterData: true,
    })
    const off = onThemeApplied(update)
    update()
    return () => {
      obs.disconnect()
      off()
    }
  }, [])
  return muted
}

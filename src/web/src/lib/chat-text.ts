import type {
  ChatText,
  ChatTextField,
  ChatTextPatch,
  ChatTextValues,
  PluginChatStyleInfo,
  PluginsPayload,
} from "@/lib/api"
import { qk, queryClient } from "@/lib/query"
import { patchUIState, uiStateNow } from "@/lib/ui-state"

// Chat text: how the chat view sets its prose — size, weight, leading,
// tracking, the column's width and the reading panel behind it. Three layers,
// each field resolved on its own: what the human set (ui_state.chat_text)
// over the plugin chat style they picked (chat_text.preset) over lasso's
// defaults below.
//
// Like typography, a plugin contributes NUMBERS here, never CSS. Each value is
// re-checked against RANGES (the server's chatTextRanges, restated) before it
// is written into a custom property on <html>; index.css owns every rule that
// reads them (.md-chat, .chat-bubble-text, .chat-column).

export const CHAT_TEXT_FIELDS: ChatTextField[] = [
  "size",
  "weight",
  "line_height",
  "letter_spacing",
  "width",
  "backing",
]

interface FieldSpec {
  min: number
  max: number
  // The slider's step. Weight steps by 100 because lasso's own faces are
  // static at 300-700: anything between renders as the nearest one.
  step: number
  label: string
  hint: string
  format: (v: number) => string
  // The custom property and how a value is written into it.
  prop: string
  css: (v: number) => string
}

// The server allows weight 100-900 (a plugin font may carry them); the slider
// offers what lasso's own interface face actually has.
export const CHAT_TEXT_SLIDER: Record<ChatTextField, [number, number]> = {
  size: [12, 22],
  weight: [300, 700],
  line_height: [1.3, 2.1],
  letter_spacing: [-0.02, 0.08],
  width: [36, 96],
  backing: [0, 1],
}

export const CHAT_TEXT_SPEC: Record<ChatTextField, FieldSpec> = {
  size: {
    min: 11,
    max: 24,
    step: 0.5,
    label: "Size",
    hint: "agent replies and your messages",
    format: (v) => `${v}px`,
    prop: "--chat-size",
    css: (v) => `${v}px`,
  },
  weight: {
    min: 100,
    max: 900,
    step: 100,
    label: "Weight",
    hint: "heavier strokes hold up better over a backdrop",
    format: (v) => String(Math.round(v)),
    prop: "--chat-weight",
    css: (v) => String(Math.round(v)),
  },
  line_height: {
    min: 1.2,
    max: 2.2,
    step: 0.05,
    label: "Line height",
    hint: "space between lines",
    format: (v) => v.toFixed(2),
    prop: "--chat-leading",
    css: (v) => String(v),
  },
  letter_spacing: {
    min: -0.05,
    max: 0.15,
    step: 0.005,
    label: "Letter spacing",
    hint: "space between letters",
    format: (v) => `${v > 0 ? "+" : ""}${v.toFixed(3)}em`,
    prop: "--chat-tracking",
    css: (v) => `${v}em`,
  },
  width: {
    min: 30,
    max: 100,
    step: 2,
    label: "Column width",
    hint: "the widest a line may run on a wide screen",
    format: (v) => `${v}rem`,
    prop: "--chat-measure",
    css: (v) => `${v}rem`,
  },
  backing: {
    min: 0,
    max: 1,
    step: 0.05,
    label: "Reading panel",
    hint: "a wash of the theme background behind the conversation, so the backdrop stops competing with the words",
    format: (v) => (v === 0 ? "off" : `${Math.round(v * 100)}%`),
    prop: "--chat-backing",
    css: (v) => String(v),
  },
}

// lasso's defaults: a step up from the 13.5px/1.6 the chat used to share with
// the agents grid, and a half-strength panel, which only shows under a
// backdrop (over a flat theme it is the background it already sits on).
export const CHAT_TEXT_DEFAULTS: Required<ChatTextValues> = {
  size: 15,
  weight: 400,
  line_height: 1.7,
  letter_spacing: 0,
  width: 56,
  backing: 0.55,
}

function valid(field: ChatTextField, v: unknown): v is number {
  const s = CHAT_TEXT_SPEC[field]
  return typeof v === "number" && Number.isFinite(v) && v >= s.min && v <= s.max
}

function pluginsNow(): PluginsPayload | undefined {
  return queryClient.getQueryData<PluginsPayload>(qk.plugins)
}

export function chatTextNow(): ChatText {
  return uiStateNow().chat_text ?? {}
}

// resolveChatStyles is every chat style lasso may use: enabled plugins only,
// numbers out of range dropped field by field.
export function resolveChatStyles(
  data: PluginsPayload | undefined
): PluginChatStyleInfo[] {
  const out: PluginChatStyleInfo[] = []
  for (const p of data?.plugins ?? []) {
    if (p.state !== "enabled") continue
    for (const s of p.chat_styles ?? []) {
      if (typeof s?.global_id !== "string" || !s.global_id) continue
      const style: PluginChatStyleInfo = {
        id: s.id,
        global_id: s.global_id,
        label: typeof s.label === "string" && s.label ? s.label : s.id,
        font: typeof s.font === "string" && s.font ? s.font : undefined,
      }
      for (const f of CHAT_TEXT_FIELDS) if (valid(f, s[f])) style[f] = s[f]
      out.push(style)
    }
  }
  return out
}

// presetFor is the picked style, or null when none is picked or its plugin is
// off right now (the choice is kept and comes back with the plugin).
export function presetFor(
  text: ChatText,
  styles: PluginChatStyleInfo[]
): PluginChatStyleInfo | null {
  if (!text.preset) return null
  return styles.find((s) => s.global_id === text.preset) ?? null
}

// effectiveChatText is the value every field resolves to, and which layer it
// came from — Settings shows a field the human set with a reset button.
export function effectiveChatText(
  text: ChatText,
  styles: PluginChatStyleInfo[]
): {
  values: Required<ChatTextValues>
  source: Record<ChatTextField, "set" | "style" | "default">
} {
  const preset = presetFor(text, styles)
  const values = { ...CHAT_TEXT_DEFAULTS }
  const source = {} as Record<ChatTextField, "set" | "style" | "default">
  for (const f of CHAT_TEXT_FIELDS) {
    if (valid(f, text[f])) {
      values[f] = text[f]
      source[f] = "set"
    } else if (preset && valid(f, preset[f])) {
      values[f] = preset[f]
      source[f] = "style"
    } else source[f] = "default"
  }
  return { values, source }
}

// chatPresetFontID is the font the picked style names, for typography.ts: the
// chat slot wears it unless the human picked a chat font themselves.
export function chatPresetFontID(): string | undefined {
  return presetFor(chatTextNow(), resolveChatStyles(pluginsNow()))?.font
}

// applyChatText is the one chokepoint. Every field is always pinned (the
// defaults included), so index.css needs no fallback values of its own to
// keep in sync with this file.
export function applyChatText() {
  const { values } = effectiveChatText(
    chatTextNow(),
    resolveChatStyles(pluginsNow())
  )
  const root = document.documentElement
  for (const f of CHAT_TEXT_FIELDS) {
    const spec = CHAT_TEXT_SPEC[f]
    const want = spec.css(values[f])
    if (root.style.getPropertyValue(spec.prop) !== want)
      root.style.setProperty(spec.prop, want)
  }
}

function signature(): string {
  return JSON.stringify([chatTextNow(), resolveChatStyles(pluginsNow())])
}

// subscribeChatText applies now and on every change to its own slice — the
// stored choice (here or from another browser over ui_state_rev) or the styles
// on offer (a plugin enabled, disabled or edited over plugins_rev).
export function subscribeChatText(): () => void {
  let last = signature()
  applyChatText()
  return queryClient.getQueryCache().subscribe((event) => {
    const k = event.query.queryKey[0]
    if (k !== qk.uiState[0] && k !== qk.plugins[0]) return
    const now = signature()
    if (now === last) return
    last = now
    applyChatText()
  })
}

// setChatText writes the named fields only — the server merges per field.
// null clears a field back to the style's value or the default.
export function setChatText(patch: ChatTextPatch) {
  patchUIState({ chat_text: patch })
}

// pickChatStyle switches to a style (or "" for none) and clears every field
// set by hand, so what shows is the style as its author made it; adjusting a
// slider afterwards layers on top of it again.
export function pickChatStyle(id: string) {
  const patch: ChatTextPatch = { preset: id }
  for (const f of CHAT_TEXT_FIELDS) patch[f] = null
  setChatText(patch)
}

import type {
  TerminalText,
  TerminalTextField,
  TerminalTextPatch,
} from "@/lib/api"
import { qk, queryClient } from "@/lib/query"
import { setTermTextOptions } from "@/lib/theme"
import { patchUIState, uiStateNow, uiStateSettled } from "@/lib/ui-state"

// Terminal text: the terminals' font size, weight, line height and letter
// spacing, from ui_state.terminal_text over lasso's defaults (what ttyd starts
// xterm with). Applied as xterm options to every terminal iframe through
// lib/theme.ts:setTermTextOptions, beside the typeface lib/typography.ts picks.
//
// A size change re-fits the terminal, which resizes the pty: every client
// attached to the same herdr session sees it reflow, exactly as when this
// window is resized.

export const TERMINAL_TEXT_FIELDS: TerminalTextField[] = [
  "size",
  "weight",
  "line_height",
  "letter_spacing",
]

export interface TerminalFieldSpec {
  // The server's terminalTextRanges, restated: a value outside is dropped.
  min: number
  max: number
  // The slider's span and step, narrower than what may be stored.
  slider: [number, number]
  step: number
  label: string
  hint: string
  format: (v: number) => string
}

export const TERMINAL_TEXT_SPEC: Record<TerminalTextField, TerminalFieldSpec> =
  {
    size: {
      min: 8,
      max: 32,
      slider: [10, 24],
      step: 1,
      label: "Size",
      hint: "bigger text means fewer columns and rows",
      format: (v) => `${v}px`,
    },
    weight: {
      min: 100,
      max: 900,
      slider: [300, 700],
      step: 100,
      label: "Weight",
      hint: "lasso's terminal font has regular and bold only; other weights take the nearest unless the chosen font has them",
      format: (v) => String(Math.round(v)),
    },
    line_height: {
      min: 1,
      max: 2,
      slider: [1, 1.6],
      step: 0.05,
      label: "Line height",
      hint: "space between rows; box-drawing lines show gaps above 1",
      format: (v) => v.toFixed(2),
    },
    letter_spacing: {
      min: -2,
      max: 8,
      slider: [-1, 4],
      step: 1,
      label: "Letter spacing",
      hint: "extra pixels between columns",
      format: (v) => `${v > 0 ? "+" : ""}${v}px`,
    },
  }

// Mirrors the ttyd launch (main.go: -t fontSize=14) and xterm's own defaults,
// so with nothing set a terminal is exactly what it was before this existed.
export const TERMINAL_TEXT_DEFAULTS: Required<TerminalText> = {
  size: 14,
  weight: 400,
  line_height: 1,
  letter_spacing: 0,
}

function valid(field: TerminalTextField, v: unknown): v is number {
  const s = TERMINAL_TEXT_SPEC[field]
  return typeof v === "number" && Number.isFinite(v) && v >= s.min && v <= s.max
}

export function effectiveTerminalText(text: TerminalText): {
  values: Required<TerminalText>
  set: Record<TerminalTextField, boolean>
} {
  const values = { ...TERMINAL_TEXT_DEFAULTS }
  const set = {} as Record<TerminalTextField, boolean>
  for (const f of TERMINAL_TEXT_FIELDS) {
    set[f] = valid(f, text[f])
    if (set[f]) values[f] = text[f] as number
  }
  return { values, set }
}

function terminalTextNow(): TerminalText {
  return uiStateNow().terminal_text ?? {}
}

export function applyTerminalText() {
  // Before the stored choice has arrived, leave xterm as ttyd started it:
  // pinning the defaults first and the real values a beat later would refit
  // the pty twice.
  if (!uiStateSettled()) return
  const { values } = effectiveTerminalText(terminalTextNow())
  setTermTextOptions({
    fontSize: values.size,
    fontWeight: values.weight,
    lineHeight: values.line_height,
    letterSpacing: values.letter_spacing,
  })
}

// subscribeTerminalText applies now and whenever the stored choice changes
// (here, or from another browser over ui_state_rev).
export function subscribeTerminalText(): () => void {
  let last = ""
  const run = () => {
    const now = JSON.stringify([uiStateSettled(), terminalTextNow()])
    if (now === last) return
    last = now
    applyTerminalText()
  }
  run()
  return queryClient.getQueryCache().subscribe((event) => {
    if (event.query.queryKey[0] !== qk.uiState[0]) return
    run()
  })
}

// setTerminalText writes the named fields only; null restores the default.
export function setTerminalText(patch: TerminalTextPatch) {
  patchUIState({ terminal_text: patch })
}

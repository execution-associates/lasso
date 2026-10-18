import { ChevronDown, ChevronUp, X } from "lucide-react"
import * as React from "react"
import { NO_AUTOCORRECT } from "@/components/ui/input"

// Find in a rendered Markdown preview, the preview's half of the file viewer's
// ⌘F (the raw editor uses CodeMirror's own search panel). Matches are painted
// with the CSS Custom Highlight API, so the rendered DOM is never touched:
// wrapping matches in <mark> would fight React for nodes it owns and break
// mermaid diagrams mid-render. Case-insensitive, and a match must sit inside
// one text node, so a phrase split by formatting ("foo **bar**") is not found.

const ALL = "lasso-find"
const CURRENT = "lasso-find-current"
// Enough for any document a human reads; past it the count says "+".
const MAX_MATCHES = 5000

export const previewFindSupported =
  typeof CSS !== "undefined" && "highlights" in CSS

function findRanges(root: HTMLElement, query: string): Range[] {
  const needle = query.toLowerCase()
  const out: Range[] = []
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const hay = (n.nodeValue ?? "").toLowerCase()
    for (
      let i = hay.indexOf(needle);
      i >= 0 && out.length < MAX_MATCHES;
      i = hay.indexOf(needle, i + needle.length)
    ) {
      const r = document.createRange()
      r.setStart(n, i)
      r.setEnd(n, i + needle.length)
      out.push(r)
    }
    if (out.length >= MAX_MATCHES) break
  }
  return out
}

function clearHighlights() {
  CSS.highlights.delete(ALL)
  CSS.highlights.delete(CURRENT)
}

export function PreviewFind({
  target,
  source,
  focusSeq,
  seed,
  onClose,
}: {
  // The rendered document to search.
  target: React.RefObject<HTMLElement | null>
  // The Markdown being rendered: a change re-runs the search over the new DOM.
  source: string
  // Bumped by every ⌘F, so pressing it again refocuses and selects the query.
  focusSeq: number
  // Text selected in the preview when ⌘F was pressed, searched for at once.
  seed: string | null
  onClose: () => void
}) {
  const [query, setQuery] = React.useState("")
  const [ranges, setRanges] = React.useState<Range[]>([])
  const [current, setCurrent] = React.useState(0)
  const input = React.useRef<HTMLInputElement>(null)

  // biome-ignore lint/correctness/useExhaustiveDependencies: focusSeq is the re-focus trigger
  React.useEffect(() => {
    if (seed) setQuery(seed)
    input.current?.focus()
    input.current?.select()
  }, [focusSeq])

  // Re-run after the preview has painted the new source (and again shortly
  // after, for content that renders late, like mermaid diagrams).
  // biome-ignore lint/correctness/useExhaustiveDependencies: source is the re-search trigger
  React.useEffect(() => {
    const root = target.current
    if (!root || !query) {
      setRanges([])
      return
    }
    const run = () => setRanges(findRanges(root, query))
    const frame = requestAnimationFrame(run)
    const late = window.setTimeout(run, 500)
    return () => {
      cancelAnimationFrame(frame)
      window.clearTimeout(late)
    }
  }, [target, query, source])

  // A new query starts at the first match; a re-render keeps the place.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on query change only
  React.useEffect(() => {
    setCurrent(0)
  }, [query])
  const at = ranges.length ? Math.min(current, ranges.length - 1) : -1

  React.useEffect(() => {
    if (ranges.length === 0) {
      clearHighlights()
      return
    }
    CSS.highlights.set(ALL, new Highlight(...ranges))
    const r = ranges[at]
    CSS.highlights.set(CURRENT, new Highlight(r))
    r.startContainer.parentElement?.scrollIntoView({
      block: "center",
      inline: "nearest",
    })
  }, [ranges, at])

  React.useEffect(() => clearHighlights, [])

  const step = (by: number) => {
    if (ranges.length === 0) return
    setCurrent((at + by + ranges.length) % ranges.length)
  }

  return (
    <div className="flex flex-none items-center gap-1 border-border border-b bg-card px-2 py-1">
      <input
        ref={input}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.nativeEvent.isComposing) {
            e.preventDefault()
            step(e.shiftKey ? -1 : 1)
          } else if (e.key === "Escape") {
            // Stopped here: the viewer closes itself on Escape at the document.
            e.preventDefault()
            e.stopPropagation()
            onClose()
          }
        }}
        placeholder="Find"
        aria-label="Find in file"
        {...NO_AUTOCORRECT}
        className="h-6 min-w-0 flex-1 rounded border border-border bg-background px-2 text-[12px] outline-none focus:border-primary"
      />
      <span className="w-16 flex-none text-center text-[11.5px] text-muted-foreground tabular-nums">
        {query
          ? ranges.length
            ? `${at + 1}/${ranges.length}${ranges.length >= MAX_MATCHES ? "+" : ""}`
            : "no results"
          : ""}
      </span>
      <FindButton label="previous match (⇧↩)" onClick={() => step(-1)}>
        <ChevronUp className="size-3.5" />
      </FindButton>
      <FindButton label="next match (↩)" onClick={() => step(1)}>
        <ChevronDown className="size-3.5" />
      </FindButton>
      <FindButton label="close (Esc)" onClick={onClose}>
        <X className="size-3.5" />
      </FindButton>
    </div>
  )
}

function FindButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      onClick={onClick}
      className="flex items-center rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
    >
      {children}
    </button>
  )
}

import { ChevronRight } from "lucide-react"
import * as React from "react"
import { cn } from "@/lib/utils"

// A user turn this long is almost never something the human typed: it is a
// compaction summary, a pasted log or a skill body the harness injected. Shown
// whole it buries the conversation around it, so it starts clipped to its first
// lines. Whether it overflows is MEASURED, not guessed from the text: a bubble's
// width runs from a phone to a desktop, so a character count clips short text on
// one and misses long text on the other. A layout effect runs before paint, so
// the toggle never pops in after the bubble is already on screen.
//
// Shared by the single chat and the agents grid's cards. `compact` is the card:
// smaller type and a shorter fold, since a card shows a transcript's tail and
// one summary would otherwise fill it.
export function UserBubble({
  text,
  compact = false,
}: {
  text: string
  compact?: boolean
}) {
  const body = React.useRef<HTMLDivElement>(null)
  const [open, setOpen] = React.useState(false)
  const [overflows, setOverflows] = React.useState(false)
  React.useLayoutEffect(() => {
    const el = body.current
    if (!el || open) return
    const check = () => setOverflows(el.scrollHeight > el.clientHeight + 1)
    check()
    const ro = new ResizeObserver(check)
    ro.observe(el)
    return () => ro.disconnect()
  }, [open])
  return (
    <div className="flex justify-end">
      <div
        className={cn(
          "rounded-xl rounded-br-sm border border-primary/20 bg-primary/8 text-foreground leading-snug",
          compact
            ? "max-w-[90%] px-2.5 py-1.5 text-[12.5px]"
            : "max-w-[85%] px-3 py-2 text-[13.5px]"
        )}
      >
        <div
          ref={body}
          className={cn(
            "whitespace-pre-wrap break-words",
            // The single chat sets this from Settings → Chat text (index.css).
            !compact && "chat-bubble-text",
            !open && "overflow-hidden",
            !open && (compact ? "max-h-[8em]" : "max-h-[16em]"),
            !open &&
              overflows &&
              "[mask-image:linear-gradient(to_bottom,black_65%,transparent)]"
          )}
        >
          {text}
        </div>
        {(overflows || open) && (
          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className={cn(
              "mt-1 flex items-center gap-1 text-muted-foreground hover:text-foreground",
              compact ? "text-[11px]" : "text-[11.5px]"
            )}
          >
            <ChevronRight
              className={cn(
                "size-3 shrink-0 transition-transform",
                open && "rotate-90"
              )}
            />
            {open ? "Show less" : "Show more"}
          </button>
        )}
      </div>
    </div>
  )
}

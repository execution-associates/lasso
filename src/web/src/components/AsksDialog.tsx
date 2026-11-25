import { Check, ChevronLeft, ChevronRight, X } from "lucide-react"
import * as React from "react"
import { AskCard } from "@/components/AsksTab"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import type { Ask } from "@/lib/api"
import { useAsks } from "@/lib/asks"
import { blurHerdrTerminal } from "@/lib/terminal"
import { cn } from "@/lib/utils"

// The Inbox as a dialog: one ask per slide in a strip that scrolls sideways
// and snaps, so a two-finger swipe on a trackpad moves exactly one ask. Each
// slide is as wide as the window, so only the one being read is on screen and
// its neighbour comes into view with the swipe. Answering an ask takes it out of the
// strip, which brings the next one into its place. The last slide is always
// the "nothing left" one, so the strip never ends on a bounce.

type Filter = "waiting" | "later" | "finished"

const FILTER_LABEL: Record<Filter, string> = {
  waiting: "Waiting",
  later: "Later",
  finished: "Finished",
}

function inFilter(a: Ask, f: Filter): boolean {
  if (f === "waiting") return a.state === "new" || a.state === "seen"
  if (f === "later") return a.state === "snoozed"
  return !["new", "seen", "snoozed"].includes(a.state)
}

// Slide is one stop of the strip: as wide and as tall as the dialog's body,
// so the dialog keeps one size whatever an ask holds. A long ask scrolls
// inside its slide.
function Slide({
  children,
  center,
}: {
  children: React.ReactNode
  center?: boolean
}) {
  return (
    <div
      className={cn(
        "flex h-full w-full flex-none snap-center snap-always flex-col overflow-y-auto",
        center && "items-center justify-center gap-3 p-6 text-center"
      )}
    >
      {children}
    </div>
  )
}

export function AsksDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const inbox = useAsks(open)
  const [filter, setFilter] = React.useState<Filter>("waiting")
  const [index, setIndex] = React.useState(0)
  const [now, setNow] = React.useState(() => Date.now())
  const strip = React.useRef<HTMLDivElement>(null)
  const all = inbox.data?.asks ?? []
  const asks = all.filter((a) => inFilter(a, filter))
  const count = (f: Filter) => all.filter((a) => inFilter(a, f)).length
  // The slides: every ask of the filter, then the closing one.
  const slides = asks.length + 1
  const at = Math.min(index, slides - 1)

  React.useEffect(() => {
    if (!open) return
    setNow(Date.now())
    setFilter("waiting")
    setIndex(0)
    const t = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(t)
  }, [open])

  const go = React.useCallback((i: number, smooth = true) => {
    const el = strip.current?.children[i] as HTMLElement | undefined
    el?.scrollIntoView({
      behavior: smooth ? "smooth" : "auto",
      inline: "center",
      block: "nearest",
    })
  }, [])

  // Which slide is centred, read from the strip itself: a swipe, a key and a
  // click all end in a scroll, so this is the one place the index is set.
  const onScroll = React.useCallback(() => {
    const box = strip.current
    if (!box) return
    const mid = box.scrollLeft + box.clientWidth / 2
    let best = 0
    let bestGap = Number.POSITIVE_INFINITY
    Array.from(box.children).forEach((c, i) => {
      const el = c as HTMLElement
      const gap = Math.abs(el.offsetLeft + el.offsetWidth / 2 - mid)
      if (gap < bestGap) {
        bestGap = gap
        best = i
      }
    })
    setIndex(best)
  }, [])

  // A new filter starts at its first slide.
  const pick = (f: Filter) => {
    setFilter(f)
    setIndex(0)
    requestAnimationFrame(() => go(0, false))
  }

  React.useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA")) return
      if (e.key === "ArrowRight") {
        e.preventDefault()
        go(Math.min(at + 1, slides - 1))
      } else if (e.key === "ArrowLeft") {
        e.preventDefault()
        go(Math.max(at - 1, 0))
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [open, at, slides, go])

  const noOperator = inbox.data?.operator === null
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        aria-describedby={undefined}
        // One block of one size: the header, the strip and the footer.
        className="flex h-[min(34rem,84vh)] w-[min(44rem,92vw)] max-w-none flex-col gap-0 overflow-hidden p-0 sm:max-w-none"
        onOpenAutoFocus={(e) => {
          e.preventDefault()
          blurHerdrTerminal()
        }}
      >
        {/* Title, the filters in the middle, then the count and close. */}
        <div className="grid flex-none grid-cols-[1fr_auto_1fr] items-center gap-3 border-border border-b px-4 py-2">
          <DialogTitle className="font-medium text-sm">Inbox</DialogTitle>
          <DialogDescription className="sr-only">
            What bots are waiting on you to decide, one at a time.
          </DialogDescription>
          <div className="flex items-center justify-center gap-1 text-xs">
            {(["waiting", "later", "finished"] as Filter[]).map((f) => (
              <button
                key={f}
                type="button"
                aria-pressed={filter === f}
                className={cn(
                  "rounded px-1.5 py-0.5 text-muted-foreground hover:text-foreground",
                  filter === f && "bg-muted text-foreground"
                )}
                onClick={() => pick(f)}
              >
                {FILTER_LABEL[f]} {count(f)}
              </button>
            ))}
          </div>
          <div className="flex items-center justify-end gap-2">
            <span className="font-label text-muted-foreground text-xs tabular-nums">
              {asks.length > 0 && at < asks.length
                ? `${at + 1} / ${asks.length}`
                : ""}
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Close"
              onClick={() => onOpenChange(false)}
            >
              <X />
            </Button>
          </div>
        </div>

        <div
          ref={strip}
          onScroll={onScroll}
          className="no-scrollbar relative flex min-h-0 w-full flex-1 snap-x snap-mandatory overflow-x-auto overscroll-x-contain"
        >
          {asks.map((a, i) => (
            <Slide key={a.id}>
              <AskCard
                ask={a}
                now={now}
                large
                current={i === at}
                hotkeys
                onLeave={() => onOpenChange(false)}
              />
            </Slide>
          ))}
          <Slide center>
            {noOperator ? (
              <p className="text-muted-foreground text-sm">
                {inbox.data?.reason} Open the Inbox tab in the sidebar to
                choose.
              </p>
            ) : (
              <>
                {filter === "waiting" && (
                  <div className="flex items-center gap-2 text-base">
                    <Check className="size-4 text-good" />
                    {asks.length === 0
                      ? "Nothing is waiting on you."
                      : "That is all of them."}
                  </div>
                )}
                <div className="flex flex-col gap-1 text-sm">
                  {(["waiting", "later", "finished"] as Filter[])
                    .filter((f) => f !== filter && count(f) > 0)
                    .map((f) => (
                      <button
                        key={f}
                        type="button"
                        className="text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                        onClick={() => pick(f)}
                      >
                        {FILTER_LABEL[f]} · {count(f)}
                      </button>
                    ))}
                </div>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => onOpenChange(false)}
                >
                  Close
                </Button>
              </>
            )}
          </Slide>
        </div>

        {/* The header's three columns again, so the dots sit under the filters. */}
        <div className="grid flex-none grid-cols-[1fr_auto_1fr] items-center gap-3 border-border border-t px-3 py-1.5 text-muted-foreground text-xs">
          <div className="flex items-center">
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Previous ask"
              disabled={at === 0}
              onClick={() => go(at - 1)}
            >
              <ChevronLeft />
            </Button>
          </div>
          <div className="flex min-w-0 items-center justify-center gap-1.5">
            {Array.from({ length: slides }, (_, i) => i).map((i) => (
              <button
                key={i}
                type="button"
                aria-label={
                  i < asks.length ? `Ask ${i + 1}` : "The end of the list"
                }
                className={cn(
                  "size-1.5 rounded-full bg-muted-foreground/40",
                  i === at && "bg-foreground"
                )}
                onClick={() => go(i)}
              />
            ))}
          </div>
          <div className="flex items-center justify-end gap-2">
            <span className="hidden font-label sm:inline">
              ← → · 1-4 · ⏎ · esc
            </span>
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Next ask"
              disabled={at >= slides - 1}
              onClick={() => go(at + 1)}
            >
              <ChevronRight />
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

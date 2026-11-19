import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronDown, RefreshCw } from "lucide-react"
import * as React from "react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { fieldClass } from "@/components/ui/field"
import { Input, NO_AUTOCORRECT } from "@/components/ui/input"
import { api, type OmpModel } from "@/lib/api"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// One selectable row. `value` is what the Model field takes: "" (no --model
// flag), an @role alias, or a selector.
type Row = {
  value: string
  label: string
  detail?: string
  group: string
}

// "1M", "200K": a context window short enough for a row.
function ctx(n?: number): string | undefined {
  if (!n) return undefined
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(1)}M ctx`
  return `${Math.round(n / 1000)}K ctx`
}

// Every whitespace-separated word of the query must appear somewhere in the
// row (label, value or detail), so "opus 5" and "anthropic fable" both narrow.
function matches(row: Row, words: string[]): boolean {
  const hay = `${row.value} ${row.label} ${row.detail ?? ""}`.toLowerCase()
  return words.every((w) => hay.includes(w))
}

// The creator's model field for omp: a button that opens a dialog listing every
// model omp can run on the host (`omp models --json`, /api/omp-models), plus
// the @role aliases the host's omp config defines, filtered as you type. A
// query that matches nothing exactly can still be used as typed, since omp
// takes fuzzy and custom selectors the catalog doesn't list.
export function OmpModelPicker({
  id,
  host,
  value,
  onValueChange,
  suggestions,
  aliases,
  prefetch,
}: {
  id?: string
  host: string
  value: string
  onValueChange: (value: string) => void
  // The harness's model_suggestions: its @role aliases come first, in the
  // order the host's omp config lists them.
  suggestions?: string[] | null
  // @role alias -> the model it resolves to (the harness's model_aliases).
  aliases?: Record<string, string> | null
  // Start reading the catalog before the dialog opens (the read takes a second
  // or more), so it is usually there by the time someone clicks.
  prefetch: boolean
}) {
  const [open, setOpen] = React.useState(false)
  const [query, setQuery] = React.useState("")
  const [active, setActive] = React.useState(0)
  const listRef = React.useRef<HTMLDivElement>(null)
  const queryClient = useQueryClient()

  const catalog = useQuery({
    queryKey: qk.ompModels(host),
    queryFn: () => api.ompModels(host),
    enabled: prefetch || open,
    staleTime: 5 * 60_000,
  })
  const [refreshing, setRefreshing] = React.useState(false)
  const refresh = async () => {
    setRefreshing(true)
    try {
      const fresh = await api.ompModels(host, true)
      queryClient.setQueryData(qk.ompModels(host), fresh)
    } catch {
      // The stale list stays; the next open reads again.
    } finally {
      setRefreshing(false)
    }
  }

  const allRows = React.useMemo<Row[]>(() => {
    const rows: Row[] = [
      {
        value: "",
        label: "default",
        detail: "omp's own default model",
        group: "",
      },
    ]
    for (const alias of suggestions ?? []) {
      if (!alias.startsWith("@")) continue
      rows.push({
        value: alias,
        label: alias,
        detail: aliases?.[alias],
        group: "Roles",
      })
    }
    for (const m of (catalog.data?.models ?? []) as OmpModel[]) {
      rows.push({
        value: m.selector,
        label: m.name || m.selector,
        detail: [m.selector, ctx(m.context_window)].filter(Boolean).join(" · "),
        group: m.provider,
      })
    }
    return rows
  }, [suggestions, aliases, catalog.data])

  const trimmed = query.trim()
  const rows = React.useMemo(() => {
    const words = trimmed.toLowerCase().split(/\s+/).filter(Boolean)
    if (!words.length) return allRows
    const hits = allRows.filter((r) => r.value !== "" && matches(r, words))
    // A typed selector that isn't a listed value is still a valid --model
    // (omp resolves fuzzy names and custom models), so offer it as is — last,
    // so Enter on a query that matches something takes the first match.
    if (!allRows.some((r) => r.value === trimmed)) {
      hits.push({
        value: trimmed,
        label: `Use “${trimmed}”`,
        group: "As typed",
      })
    }
    return hits
  }, [allRows, trimmed])

  // Reset the highlight to the first row whenever the list changes shape, and
  // to the current value when the dialog opens.
  React.useEffect(() => {
    if (!open) return
    if (trimmed) {
      setActive(0)
      return
    }
    const i = rows.findIndex((r) => r.value === value)
    setActive(i < 0 ? 0 : i)
  }, [open, trimmed, rows, value])

  React.useEffect(() => {
    if (!open) return
    listRef.current
      ?.querySelector<HTMLElement>(`[data-index="${active}"]`)
      ?.scrollIntoView({ block: "nearest" })
  }, [active, open])

  const choose = (v: string) => {
    onValueChange(v)
    setOpen(false)
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault()
      setActive((a) => Math.min(a + 1, rows.length - 1))
    } else if (e.key === "ArrowUp") {
      e.preventDefault()
      setActive((a) => Math.max(a - 1, 0))
    } else if (e.key === "PageDown") {
      e.preventDefault()
      setActive((a) => Math.min(a + 10, rows.length - 1))
    } else if (e.key === "PageUp") {
      e.preventDefault()
      setActive((a) => Math.max(a - 10, 0))
    } else if (e.key === "Enter") {
      // Enter picks a row and goes no further: React carries a portaled
      // dialog's keys up to the creator, whose ⌘/Ctrl+Enter creates the agent.
      e.preventDefault()
      e.stopPropagation()
      const row = rows[active]
      if (row) choose(row.value)
    }
  }

  const modelCount = catalog.data?.models.length ?? 0
  const status = catalog.isPending
    ? "Reading omp's models…"
    : catalog.isError
      ? `Couldn't read omp's models: ${(catalog.error as Error).message}`
      : catalog.data?.error
        ? `Couldn't read omp's models: ${catalog.data.error}`
        : `${modelCount} models`

  return (
    <>
      <button
        id={id}
        type="button"
        aria-haspopup="dialog"
        onClick={() => {
          setQuery("")
          setOpen(true)
        }}
        className={cn(
          fieldClass,
          "flex h-8 items-center gap-1 text-left md:text-sm"
        )}
      >
        <span
          className={cn(
            "min-w-0 flex-1 truncate",
            !value && "text-muted-foreground"
          )}
          // The suffix tells selectors sharing a provider apart.
          style={{ direction: "rtl", textAlign: "left" }}
          title={value || "default"}
        >
          <span dir="ltr">{value || "default"}</span>
        </span>
        <ChevronDown className="size-4 shrink-0 text-muted-foreground" />
      </button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="flex max-h-[min(80vh,40rem)] flex-col gap-3 sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Model</DialogTitle>
            <DialogDescription>
              Every model omp can run on{" "}
              {host === "local" ? "this machine" : host}.
            </DialogDescription>
          </DialogHeader>
          <Input
            autoFocus
            value={query}
            placeholder="Filter models…"
            {...NO_AUTOCORRECT}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            role="combobox"
            aria-expanded
            aria-controls={id ? `${id}-list` : undefined}
          />
          <div
            ref={listRef}
            id={id ? `${id}-list` : undefined}
            role="listbox"
            className="-mx-1 min-h-0 flex-1 overflow-y-auto px-1"
          >
            {rows.map((r, i) => (
              <React.Fragment key={`${r.group}\u0000${r.value}`}>
                {r.group && r.group !== rows[i - 1]?.group && (
                  <div className="px-2 pt-2 pb-1 font-medium text-muted-foreground text-xs">
                    {r.group}
                  </div>
                )}
                <button
                  type="button"
                  role="option"
                  aria-selected={i === active}
                  data-index={i}
                  onClick={() => choose(r.value)}
                  onMouseMove={() => setActive(i)}
                  className={cn(
                    "flex w-full flex-col items-start rounded-md px-2 py-1.5 text-left text-sm outline-none",
                    i === active && "bg-primary text-primary-foreground"
                  )}
                >
                  <span className="flex w-full items-center gap-2">
                    <span className="min-w-0 flex-1 truncate">{r.label}</span>
                    {r.value === value && (
                      <span className="shrink-0 text-xs opacity-70">
                        current
                      </span>
                    )}
                  </span>
                  {r.detail && (
                    <span
                      className={cn(
                        "w-full truncate text-xs",
                        i === active
                          ? "text-primary-foreground/80"
                          : "text-muted-foreground"
                      )}
                    >
                      {r.detail}
                    </span>
                  )}
                </button>
              </React.Fragment>
            ))}
          </div>
          <div className="flex items-center gap-2 text-muted-foreground text-xs">
            <span className="min-w-0 flex-1 truncate" title={status}>
              {status}
            </span>
            <Button
              type="button"
              variant="ghost"
              size="xs"
              onClick={refresh}
              disabled={refreshing || catalog.isPending}
              title="Read omp's model list again"
            >
              <RefreshCw className={cn(refreshing && "animate-spin")} />
              Refresh
            </Button>
          </div>
        </DialogContent>
      </Dialog>
    </>
  )
}

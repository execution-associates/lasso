import { useQuery } from "@tanstack/react-query"
import { Check, ChevronDown } from "lucide-react"
import { Popover } from "radix-ui"
import * as React from "react"
import { toast } from "sonner"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { api, type BotView } from "@/lib/api"
import { invalidateBots } from "@/lib/bots"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// The chat header's model, thinking and context (docs/web-ui/bots.md). An omp
// bot switches model and thinking at once (over RPC) and lasso keeps the change
// in its settings; a claude bot saves it and offers a restart, since Claude
// Code cannot switch from outside. Compact puts all three in one chip that
// opens a panel, for a narrow view.

const FALLBACK_LEVELS: Record<string, string[]> = {
  claude: ["low", "medium", "high", "xhigh", "max"],
  omp: ["off", "minimal", "low", "medium", "high", "xhigh", "max", "auto"],
}

export function formatTokens(n: number): string {
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}M`
}

// What the bar shows and offers, from the bot's view and its host's harness
// table (the model suggestions are omp's role models on that host).
function useModelBar(bot: BotView) {
  const config = useQuery({
    queryKey: qk.agentConfig(bot.host),
    queryFn: () => api.agentConfig(bot.host),
    staleTime: 60_000,
  })
  const harness = config.data?.harnesses?.find((h) => h.id === bot.runtime)
  const live = bot.live_model
  const model = live ? `${live.provider}/${live.id}` : bot.model
  // What runs, and "(default)" when the bot names none: the runtime chose
  // it, so it is what the bot gets until a model is picked.
  const modelLabel = bot.model
    ? live?.name || bot.model
    : live?.name
      ? `${live.name} (default)`
      : "default model"
  const thinking = bot.thinking || bot.effort
  const thinkingLabel = bot.effort
    ? thinking
    : thinking
      ? `${thinking} (default)`
      : "default"
  const levels =
    bot.thinking_levels ??
    (harness?.effort_levels?.length
      ? harness.effort_levels
      : (FALLBACK_LEVELS[bot.runtime] ?? []))
  const suggestions = Array.from(
    new Set([...(model ? [model] : []), ...(harness?.model_suggestions ?? [])])
  )
  const [busy, setBusy] = React.useState(false)
  const set = async (patch: { model?: string; thinking?: string }) => {
    setBusy(true)
    try {
      const res = await api.bots.setModel(bot.name, patch)
      if (res.restart_needed) {
        toast(`${bot.name} uses it from its next start`, {
          action: {
            label: "Restart now",
            onClick: () => {
              void api.bots
                .restart(bot.name, false)
                .finally(() => void invalidateBots(bot.name))
            },
          },
        })
      }
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
      void invalidateBots(bot.name)
    }
  }
  return {
    model,
    modelLabel,
    thinking,
    thinkingLabel,
    levels,
    suggestions,
    busy,
    set,
  }
}

const chip =
  "flex h-7 min-w-0 items-center gap-1 rounded-lg border border-border/60 bg-background/30 px-2 text-[11.5px] text-foreground hover:bg-accent disabled:opacity-60"

export function BotModelBar({
  bot,
  compact,
}: {
  bot: BotView
  compact?: boolean
}) {
  const bar = useModelBar(bot)
  if (compact) return <CompactBar bot={bot} bar={bar} />
  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <ModelPicker bar={bar}>
        <button
          type="button"
          className={cn(chip, "max-w-56")}
          disabled={bar.busy}
          title={bar.model || "the runtime's default model"}
        >
          <span className="truncate">{bar.modelLabel}</span>
          <ChevronDown className="size-3 shrink-0 opacity-60" />
        </button>
      </ModelPicker>
      <ThinkingPicker bar={bar}>
        <button type="button" className={chip} disabled={bar.busy}>
          <span className="text-muted-foreground">thinking</span>
          <span>{bar.thinkingLabel}</span>
          <ChevronDown className="size-3 shrink-0 opacity-60" />
        </button>
      </ThinkingPicker>
      <ContextMeter bot={bot} />
    </div>
  )
}

type Bar = ReturnType<typeof useModelBar>

// ContextMeter is how full the bot's context is: a bar with used / window and
// a percentage when the window is known (omp), the tokens alone otherwise.
export function ContextMeter({ bot, wide }: { bot: BotView; wide?: boolean }) {
  const c = bot.context
  if (!c?.tokens) return null
  if (!c.window) {
    return (
      <span
        className="text-[11px] text-muted-foreground"
        title="context used by the newest turn"
      >
        {formatTokens(c.tokens)} ctx
      </span>
    )
  }
  const pct = c.percent ?? (c.tokens / c.window) * 100
  const tone =
    pct >= 90 ? "bg-destructive" : pct >= 70 ? "bg-amber-500" : "bg-primary"
  return (
    <span
      className="flex shrink-0 items-center gap-1.5 text-[11px] text-muted-foreground"
      title={`${c.tokens.toLocaleString()} of ${c.window.toLocaleString()} tokens`}
    >
      <span
        className={cn(
          "h-1.5 overflow-hidden rounded-full bg-muted",
          wide ? "w-24" : "w-14"
        )}
      >
        <span
          className={cn("block h-full rounded-full", tone)}
          style={{ width: `${Math.min(100, Math.max(2, pct))}%` }}
        />
      </span>
      {formatTokens(c.tokens)} / {formatTokens(c.window)} ·{" "}
      {pct < 1 ? pct.toFixed(1) : Math.round(pct)}%
    </span>
  )
}

function ThinkingPicker({
  bar,
  children,
}: {
  bar: Bar
  children: React.ReactNode
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>{children}</DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-36">
        <DropdownMenuLabel className="text-[11px] text-muted-foreground">
          Thinking
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup
          value={bar.thinking}
          onValueChange={(thinking) => void bar.set({ thinking })}
        >
          {bar.levels.map((l) => (
            <DropdownMenuRadioItem key={l} value={l}>
              {l}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

// ModelPicker is a filterable list of the host's models plus whatever is
// typed: model names churn faster than releases, so the list is a hint.
function ModelPicker({
  bar,
  children,
}: {
  bar: Bar
  children: React.ReactNode
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>{children}</Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={4}
          className="isolate z-50 w-72 overflow-hidden rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-elev-pop outline-none"
        >
          <ModelList
            bar={bar}
            onPicked={(model) => {
              setOpen(false)
              void bar.set({ model })
            }}
          />
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}

function ModelList({
  bar,
  onPicked,
}: {
  bar: Bar
  onPicked: (model: string) => void
}) {
  const [q, setQ] = React.useState("")
  const query = q.trim()
  const shown = bar.suggestions.filter(
    (s) => !query || s.toLowerCase().includes(query.toLowerCase())
  )
  return (
    <div className="flex flex-col gap-1">
      <input
        {...NO_AUTOCORRECT}
        // biome-ignore lint/a11y/noAutofocus: a picker opened to type into
        autoFocus
        value={q}
        onChange={(e) => setQ(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && query) onPicked(query)
        }}
        placeholder="provider/model…"
        className="rounded-md border border-border bg-background px-2 py-1.5 font-mono text-[12px] outline-none"
      />
      <div className="max-h-60 overflow-y-auto">
        {shown.map((s) => (
          <button
            key={s}
            type="button"
            onClick={() => onPicked(s)}
            title={s}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left font-mono text-[12px] hover:bg-accent"
          >
            <Check
              className={cn(
                "size-3.5 shrink-0",
                s === bar.model ? "opacity-100" : "opacity-0"
              )}
            />
            <span className="truncate">{s}</span>
          </button>
        ))}
        {query && !bar.suggestions.includes(query) && (
          <button
            type="button"
            onClick={() => onPicked(query)}
            className="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-[12px] hover:bg-accent"
          >
            <span className="size-3.5 shrink-0" />
            Use <span className="truncate font-mono">{query}</span>
          </button>
        )}
      </div>
      <p className="px-2 pb-1 text-[10.5px] text-muted-foreground">
        {bar.model === "" ? "Runs its default model. " : ""}
        Applies now on a running omp bot; a Claude Code bot takes it at its next
        start.
      </p>
    </div>
  )
}

// CompactBar is the whole bar as one chip, for a narrow view: model, thinking
// and context in a line, opening a panel with the pickers.
function CompactBar({ bot, bar }: { bot: BotView; bar: Bar }) {
  const [open, setOpen] = React.useState(false)
  const [picking, setPicking] = React.useState(false)
  const c = bot.context
  const pct =
    c?.window && c.tokens ? (c.percent ?? (c.tokens / c.window) * 100) : null
  return (
    <Popover.Root
      open={open}
      onOpenChange={(o) => {
        setOpen(o)
        if (!o) setPicking(false)
      }}
    >
      <Popover.Trigger asChild>
        <button
          type="button"
          className={cn(chip, "w-full justify-between")}
          disabled={bar.busy}
        >
          <span className="truncate">
            {bar.modelLabel} · {bar.thinkingLabel}
            {pct !== null
              ? ` · ${Math.round(pct)}% ctx`
              : c?.tokens
                ? ` · ${formatTokens(c.tokens)} ctx`
                : ""}
          </span>
          <ChevronDown className="size-3 shrink-0 opacity-60" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={4}
          className="isolate z-50 w-[min(20rem,calc(100vw-2rem))] rounded-lg border border-border bg-popover p-2 text-[12.5px] text-popover-foreground shadow-elev-pop outline-none"
        >
          {picking ? (
            <ModelList
              bar={bar}
              onPicked={(model) => {
                setPicking(false)
                setOpen(false)
                void bar.set({ model })
              }}
            />
          ) : (
            <div className="flex flex-col gap-2">
              <button
                type="button"
                onClick={() => setPicking(true)}
                className="flex items-center justify-between gap-2 rounded-md px-1 py-1 hover:bg-accent"
              >
                <span className="text-muted-foreground">Model</span>
                <span className="truncate">{bar.modelLabel} ›</span>
              </button>
              <div className="flex flex-col gap-1 px-1">
                <span className="text-muted-foreground">Thinking</span>
                <div className="flex flex-wrap gap-1">
                  {bar.levels.map((l) => (
                    <button
                      key={l}
                      type="button"
                      onClick={() => void bar.set({ thinking: l })}
                      className={cn(
                        "rounded-md border px-2 py-0.5 text-[11.5px]",
                        l === bar.thinking
                          ? "border-primary text-primary"
                          : "border-border hover:bg-accent"
                      )}
                    >
                      {l}
                    </button>
                  ))}
                </div>
              </div>
              <div className="flex items-center justify-between gap-2 px-1">
                <span className="text-muted-foreground">Context</span>
                <ContextMeter bot={bot} wide />
                {!c?.tokens && <span className="text-muted-foreground">—</span>}
              </div>
            </div>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}

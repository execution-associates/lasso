import {
  ChevronLeft,
  GripVertical,
  LayoutGrid,
  ListChecks,
  Play,
  Plus,
  RotateCcw,
  Settings,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { BotAvatar, BotStateMark, startBot } from "@/components/BotParts"
import { BotSettings } from "@/components/BotSettings"
import { BotsManage } from "@/components/BotsManage"
import { ChatView } from "@/components/ChatView"
import { Button } from "@/components/ui/button"
import { Orb } from "@/components/ui/orb"
import { api, type BotView } from "@/lib/api"
import { moveTabToHost } from "@/lib/app-store"
import {
  botsKey,
  botUnread,
  markBotSeen,
  relativeTime,
  seedBotsSeen,
  useBots,
  useBotsSeen,
  useMinuteClock,
} from "@/lib/bots"
import { queryClient } from "@/lib/query"
import {
  type BotsRoute,
  botsRouteNow,
  parseBotsPath,
  writeBotsRoute,
} from "@/lib/url"
import { cn } from "@/lib/utils"

// The Bots view: lasso's long-lived Claude Code sessions as a messaging app.
// The list of bots on the left, the selected one's conversation on the right
// (ChatView by address, with the channel deliveries a bot mostly answers), and
// its settings, the management table and the creator as pages of the same
// view under /bots (lib/url.ts). Below md it is one column at a time: the list,
// then whatever was picked from it, with a Back.

function BotRow({
  bot,
  selected,
  unread,
  now,
  onPick,
  dragging,
  onGrab,
}: {
  bot: BotView
  selected: boolean
  unread: boolean
  now: number
  onPick: () => void
  dragging: boolean
  // pointerdown on the grip: starts a drag (useBotDrag).
  onGrab: (e: React.PointerEvent) => void
}) {
  const preview = bot.last_text
    ? `${bot.last_kind === "user" ? "You: " : ""}${bot.last_text}`
    : bot.state === "stopped"
      ? "Stopped"
      : "No messages yet"
  return (
    <div
      data-bot-row={bot.name}
      className={cn(
        "group/row fx-row fx-in relative flex items-center rounded-lg",
        dragging && "z-10 bg-card shadow-lg ring-1 ring-border"
      )}
    >
      {/* The grip: dragging it reorders the list. Its own element, so a tap on
          the row still opens the bot and a scroll on a phone still scrolls. */}
      <span
        role="presentation"
        onPointerDown={onGrab}
        title="Drag to reorder"
        className="absolute inset-y-0 left-0 flex w-4 cursor-grab touch-none items-center justify-center text-muted-foreground/50 opacity-0 active:cursor-grabbing group-hover/row:opacity-100 max-md:opacity-100"
      >
        <GripVertical className="size-3.5" />
      </span>
      <button
        type="button"
        onClick={onPick}
        aria-current={selected ? "page" : undefined}
        className={cn(
          "flex w-full items-center gap-2.5 rounded-lg py-2 pr-2 pl-4 text-left hover:bg-accent/60",
          selected && "bg-accent"
        )}
      >
        <BotAvatar bot={bot} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex min-w-0 items-center gap-1.5">
            <span
              className={cn(
                "min-w-0 truncate text-[13px] text-foreground",
                unread ? "font-semibold" : "font-medium"
              )}
            >
              {bot.name}
            </span>
            <BotStateMark bot={bot} className="min-w-0" />
            <span className="ml-auto shrink-0 text-[11px] text-muted-foreground">
              {relativeTime(bot.last_at, now)}
            </span>
          </span>
          <span className="flex min-w-0 items-center gap-1.5">
            <span
              className={cn(
                "min-w-0 flex-1 truncate text-[12px]",
                unread ? "text-foreground" : "text-muted-foreground"
              )}
            >
              {preview}
            </span>
            {unread && (
              <span
                role="img"
                aria-label="Unread"
                className="size-2 shrink-0 rounded-full bg-primary"
              />
            )}
          </span>
        </span>
      </button>
    </div>
  )
}

// What the right side shows for a bot that is not running: there is no pane,
// so no conversation to read, and the one useful thing is to start it.
function StoppedBot({
  bot,
  lead,
  onSettings,
}: {
  bot: BotView
  lead: React.ReactNode
  onSettings: () => void
}) {
  const [busy, setBusy] = React.useState(false)
  const start = async (fresh: boolean) => {
    setBusy(true)
    await startBot(bot.name, fresh)
    setBusy(false)
  }
  const starting = bot.state === "starting"
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        {lead}
        <span className="min-w-0 truncate font-medium text-[12.5px] text-foreground">
          {bot.name}
        </span>
        <span className="ml-auto" />
        <BotStateMark bot={bot} words />
        <SettingsButton onClick={onSettings} />
      </div>
      <div className="flex min-h-0 flex-1 flex-col items-center justify-center gap-3 px-4 text-center">
        <BotAvatar bot={bot} size={64} />
        <div className="font-medium text-[15px] text-foreground">
          {bot.name}
        </div>
        {starting ? (
          <div className="flex items-center gap-2 text-[13px] text-muted-foreground">
            <Orb state="working" px={16} />
            Starting…
          </div>
        ) : (
          <>
            <p className="max-w-sm text-[13px] text-muted-foreground">
              {bot.name} is stopped. Start it to pick up where it left off, or
              start fresh for a new session.
            </p>
            {bot.error && (
              <p className="max-w-sm text-[12px] text-destructive">
                {bot.error}
              </p>
            )}
            <div className="flex flex-wrap items-center justify-center gap-2">
              <Button disabled={busy} onClick={() => void start(false)}>
                {busy ? <Orb state="working" px={14} on="accent" /> : <Play />}
                Start
              </Button>
              <Button
                variant="outline"
                disabled={busy}
                onClick={() => void start(true)}
              >
                <RotateCcw />
                Start fresh
              </Button>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function SettingsButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="Bot settings"
      aria-label="Bot settings"
      className="flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
    >
      <Settings className="size-4" />
    </button>
  )
}

// The phone's way back to the list, at the left of every page's header.
function BackToList({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="Back to bots"
      aria-label="Back to bots"
      className="-ml-1 flex h-7 shrink-0 items-center rounded-lg pr-1.5 text-[12px] text-muted-foreground hover:bg-accent hover:text-foreground md:hidden"
    >
      <ChevronLeft className="size-4" />
      Bots
    </button>
  )
}

// useBotDrag reorders the list by dragging a row's grip: pointer events rather
// than HTML drag and drop, which a phone's touch never fires. The order shown
// while dragging is local; on release it is saved and the list re-read, and
// until then the cached list is rewritten so the next poll does not flash the
// old order back.
function useBotDrag(bots: BotView[]) {
  const [dragging, setDragging] = React.useState<string | null>(null)
  const [names, setNames] = React.useState<string[] | null>(null)
  const order = React.useMemo(() => {
    if (!names) return bots
    const by = new Map(bots.map((b) => [b.name, b]))
    const out = names.flatMap((n) => {
      const b = by.get(n)
      return b ? [b] : []
    })
    // A bot that appeared mid-drag goes at the end.
    for (const b of bots) if (!names.includes(b.name)) out.push(b)
    return out
  }, [bots, names])

  const grab = (e: React.PointerEvent, name: string) => {
    if (e.button !== 0) return
    e.preventDefault()
    const list = (e.currentTarget as HTMLElement).closest(".overflow-y-auto")
    if (!list) return
    let current = bots.map((b) => b.name)
    setDragging(name)
    setNames(current)
    const move = (ev: PointerEvent) => {
      // The slot is the number of OTHER rows whose middle is above the pointer.
      const rows = [
        ...list.querySelectorAll<HTMLElement>("[data-bot-row]"),
      ].filter((r) => r.dataset.botRow !== name)
      let slot = 0
      for (const r of rows) {
        const box = r.getBoundingClientRect()
        if (ev.clientY > box.top + box.height / 2) slot++
      }
      const rest = current.filter((n) => n !== name)
      const next = [...rest.slice(0, slot), name, ...rest.slice(slot)]
      if (next.join("\n") !== current.join("\n")) {
        current = next
        setNames(next)
      }
    }
    const up = () => {
      window.removeEventListener("pointermove", move)
      window.removeEventListener("pointerup", up)
      window.removeEventListener("pointercancel", up)
      setDragging(null)
      const before = bots.map((b) => b.name).join("\n")
      if (current.join("\n") === before) {
        setNames(null)
        return
      }
      queryClient.setQueryData<{ bots: BotView[] }>(botsKey, (old) => {
        if (!old) return old
        const rank = new Map(current.map((n, i) => [n, i]))
        return {
          ...old,
          bots: [...old.bots].sort(
            (a, b) =>
              (rank.get(a.name) ?? rank.size) - (rank.get(b.name) ?? rank.size)
          ),
        }
      })
      setNames(null)
      api.bots
        .reorder(current)
        .catch((err) => toast.error(`could not save the order: ${err.message}`))
        .finally(
          () => void queryClient.invalidateQueries({ queryKey: botsKey })
        )
    }
    window.addEventListener("pointermove", move)
    window.addEventListener("pointerup", up)
    window.addEventListener("pointercancel", up)
  }

  return { order, dragging, grab }
}

export function BotsView({
  active,
  onShowTerminal,
  onOpenViews,
  className,
}: {
  // On screen. The view stays mounted while hidden (so a conversation keeps
  // its scroll and draft), and every poll in it stops then.
  active: boolean
  // Switch the left column to the terminal (Settings → Open terminal).
  onShowTerminal: () => void
  // The phone's view picker (App's), since this view covers the input dial
  // that otherwise opens it.
  onOpenViews: () => void
  className?: string
}) {
  const [route, setRoute] = React.useState<BotsRoute>(botsRouteNow)
  const go = React.useCallback((r: BotsRoute, push = true) => {
    setRoute(r)
    writeBotsRoute(r, push)
  }, [])
  // Back and forward between the view's own pages. The App's own popstate
  // handler sets the VIEW; this one the page inside it. Read from the path
  // directly: the listeners run in mount order, child first.
  React.useEffect(() => {
    const onPop = () => {
      const r = parseBotsPath(window.location.pathname)
      if (r) setRoute(r)
    }
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

  const { data, isPending, error } = useBots(active)
  // The server's order, which is the human's: dragged in this list.
  const bots = React.useMemo(() => data?.bots ?? [], [data])
  const seenMap = useBotsSeen()
  React.useEffect(() => {
    if (data?.bots) seedBotsSeen(data.bots)
  }, [data])
  const now = useMinuteClock()

  const drag = useBotDrag(bots)

  const routeName =
    route.page === "chat" || route.page === "settings" ? route.name : null
  const current = routeName ? bots.find((b) => b.name === routeName) : undefined
  // A link to a bot that is not there (deleted, renamed, mistyped) lands on
  // the list, once the list has answered.
  React.useEffect(() => {
    if (!routeName || isPending || error) return
    if (!bots.some((b) => b.name === routeName)) go({ page: "list" }, false)
  }, [routeName, bots, isPending, error, go])

  // Reading a bot's conversation marks what it has said as seen.
  React.useEffect(() => {
    if (!active || route.page !== "chat" || !current) return
    markBotSeen(current.name, current.last_at)
  }, [active, route.page, current])

  const openTerminal = React.useCallback(
    async (b: BotView) => {
      if (!b.pane_id) return
      try {
        await moveTabToHost(b.host)
        await api.focus({ pane_id: b.pane_id })
        onShowTerminal()
      } catch (e) {
        toast.error(`could not open ${b.name}: ${(e as Error).message}`)
      }
    },
    [onShowTerminal]
  )

  const toList = () => go({ page: "list" })
  const back = <BackToList onClick={toList} />
  const listScreen = route.page === "list"

  let page: React.ReactNode
  if (route.page === "manage") {
    page = (
      <BotsManage
        bots={bots}
        lead={back}
        now={now}
        onOpen={(name) => go({ page: "chat", name })}
        onSettings={(name) => go({ page: "settings", name })}
        onNew={() => go({ page: "new" })}
      />
    )
  } else if (route.page === "new") {
    page = (
      <BotSettings
        key="new"
        mode="new"
        lead={back}
        onCreated={(name) => go({ page: "settings", name })}
        onClose={toList}
      />
    )
  } else if (route.page === "settings") {
    page = (
      <BotSettings
        key={`settings:${route.name}`}
        mode="edit"
        name={route.name}
        lead={back}
        onClose={() => go({ page: "chat", name: route.name })}
        onDeleted={() => go({ page: "list" }, false)}
        onOpenTerminal={openTerminal}
      />
    )
  } else if (route.page === "chat" && current) {
    page =
      current.pane_id && current.state !== "stopped" ? (
        <ChatView
          key={`${current.name}\u0000${current.host}\u0000${current.pane_id}`}
          address={{ host: current.host, paneID: current.pane_id }}
          incoming
          variant="bot"
          active={active}
          title={current.name}
          placeholder={`Message ${current.name}…`}
          headerLead={back}
          headerExtra={
            <>
              <BotStateMark bot={current} words />
              <SettingsButton
                onClick={() => go({ page: "settings", name: current.name })}
              />
            </>
          }
          className="min-w-0 flex-1"
        />
      ) : (
        <StoppedBot
          bot={current}
          lead={back}
          onSettings={() => go({ page: "settings", name: current.name })}
        />
      )
  } else {
    // The list's own page at md+, where the list is already beside it.
    page = (
      <div className="flex h-full flex-col items-center justify-center gap-3 px-4 text-center text-[13px] text-muted-foreground">
        {isPending ? (
          <Orb state="working" px={20} />
        ) : bots.length === 0 ? (
          <>
            <p className="max-w-sm">
              A bot is a Claude Code session lasso keeps running in its own
              folder, with its own instructions, connections and secrets.
            </p>
            <Button onClick={() => go({ page: "new" })}>
              <Plus />
              New bot
            </Button>
          </>
        ) : (
          <p>Pick a bot to talk to it.</p>
        )}
      </div>
    )
  }

  return (
    <div
      className={cn(
        "vsurface flex h-full min-h-0 w-full bg-background",
        className
      )}
    >
      {/* The list. A column at md+; below md the whole screen, and only on
          the list page. */}
      <nav
        aria-label="Bots"
        className={cn(
          "fx-ground flex min-h-0 w-full flex-none flex-col border-border bg-card md:w-72 md:border-r",
          !listScreen && "max-md:hidden"
        )}
      >
        <div className="fx-headline flex flex-none items-center gap-1 border-border border-b px-2.5 py-1.5">
          <span className="font-medium text-[13px] text-foreground">Bots</span>
          <span className="ml-auto" />
          <button
            type="button"
            onClick={() => go({ page: "new" })}
            title="New bot"
            aria-label="New bot"
            className="flex size-7 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <Plus className="size-4" />
          </button>
          {/* The view picker, which a phone reaches from the input dial
              everywhere else — and the dial is under this view. */}
          <button
            type="button"
            onClick={onOpenViews}
            title="Switch view"
            aria-label="Switch view"
            className="flex size-7 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground md:hidden"
          >
            <LayoutGrid className="size-4" />
          </button>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-1.5 py-1">
          {error && (
            <div className="px-2 py-2 text-[12px] text-destructive">
              could not list bots: {(error as Error).message}
            </div>
          )}
          {!isPending && !error && bots.length === 0 && (
            <div className="px-2 py-3 text-[12px] text-muted-foreground">
              No bots yet. Create one with +.
            </div>
          )}
          {drag.order.map((b) => (
            <BotRow
              key={b.name}
              bot={b}
              now={now}
              selected={routeName === b.name}
              unread={botUnread(b, seenMap) && routeName !== b.name}
              onPick={() => go({ page: "chat", name: b.name })}
              dragging={drag.dragging === b.name}
              onGrab={(e) => drag.grab(e, b.name)}
            />
          ))}
        </div>
        <div className="flex-none border-border border-t p-1.5">
          <button
            type="button"
            onClick={() => go({ page: "manage" })}
            aria-current={route.page === "manage" ? "page" : undefined}
            className={cn(
              "flex w-full items-center gap-2 rounded-lg px-2 py-1.5 text-[12.5px] text-muted-foreground hover:bg-accent hover:text-foreground",
              route.page === "manage" && "bg-accent text-foreground"
            )}
          >
            <ListChecks className="size-4" />
            Manage bots
          </button>
        </div>
      </nav>
      {/* The page: a conversation, settings, the table or the creator. */}
      <div
        className={cn(
          "flex min-h-0 min-w-0 flex-1 flex-col",
          listScreen && "max-md:hidden"
        )}
      >
        {page}
      </div>
    </div>
  )
}

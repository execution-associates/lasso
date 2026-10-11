import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ChevronLeft,
  ChevronRight,
  Inbox as InboxIcon,
  MessageSquare,
  Monitor,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { EndAgentDialog } from "@/components/AgentParts"
import { BotAvatar } from "@/components/BotParts"
import { Markdown, resolveMarkdownSrc } from "@/components/Markdown"
import { paneKey } from "@/lib/agents"
import { api, type ChatPayload, type HostPane } from "@/lib/api"
import { relativeTime, useMinuteClock } from "@/lib/bots"
import { dismissCard } from "@/lib/ui-state"
import { cn } from "@/lib/utils"
import { BotAskForm, PaneAskForm } from "@/simple/AskForm"
import {
  dropNotice,
  type InboxItem,
  lastAgentText,
  markSeen,
  simpleAgentName,
  simpleWorkspace,
  useInbox,
  usePaneChat,
} from "@/simple/inbox"
import {
  Card,
  ConnectionLine,
  EmptyState,
  Loading,
  Screen,
  ScrollBody,
  StatusDot,
} from "@/simple/parts"
import { ReplyBox } from "@/simple/ReplyBox"
import type { SimpleRoute } from "@/simple/route"
import { SharedFiles, sharedFiles } from "@/simple/SharedFiles"

// The Inbox opens on its newest message, never a list, and pages through the
// rest in Inbox order: the arrows, or a horizontal swipe. Landing on a card is
// what marks it seen.
export function InboxView({ go }: { go: (r: SimpleRoute) => void }) {
  const inbox = useInbox()
  const { items } = inbox
  // The card on screen, by key, so a list reordering under it does not move
  // the reader. When it leaves (answered, discarded) the next one takes its
  // place.
  const [currentKey, setCurrentKey] = React.useState<string | null>(null)
  const lastIndex = React.useRef(0)
  let index = items.findIndex((i) => i.key === currentKey)
  if (index < 0) index = Math.min(lastIndex.current, items.length - 1)
  const item = index >= 0 ? items[index] : undefined
  React.useEffect(() => {
    lastIndex.current = Math.max(0, index)
  }, [index])
  React.useEffect(() => {
    if (item) markSeen(item)
  }, [item])

  const page = (d: number) => {
    const next = items[index + d]
    if (next) setCurrentKey(next.key)
  }

  // A horizontal swipe pages; anything mostly vertical is the card's scroll.
  const touch = React.useRef<{ x: number; y: number } | null>(null)
  const onTouchStart = (e: React.TouchEvent) => {
    const t = e.touches[0]
    touch.current = { x: t.clientX, y: t.clientY }
  }
  const onTouchEnd = (e: React.TouchEvent) => {
    const s = touch.current
    touch.current = null
    if (!s) return
    const t = e.changedTouches[0]
    const dx = t.clientX - s.x
    const dy = t.clientY - s.y
    if (Math.abs(dx) > 70 && Math.abs(dx) > Math.abs(dy) * 1.6)
      page(dx < 0 ? 1 : -1)
  }

  return (
    <Screen
      title="Inbox"
      status={
        <ConnectionLine
          error={inbox.error}
          unlisted={inbox.unlisted}
          onRetry={inbox.refetch}
        />
      }
      trailing={
        items.length > 1 ? (
          <Pager
            index={index}
            count={items.length}
            onPrev={() => page(-1)}
            onNext={() => page(1)}
          />
        ) : null
      }
    >
      {!item ? (
        inbox.loading ? (
          <Loading />
        ) : (
          <EmptyState
            icon={<InboxIcon className="size-12" strokeWidth={1.4} />}
            title="Nothing waiting on you."
          >
            Questions, finished turns and notices from your claude and omp
            agents land here.
          </EmptyState>
        )
      ) : (
        <ScrollBody className="pt-2">
          <div onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
            <ItemCard key={item.key} item={item} go={go} />
          </div>
        </ScrollBody>
      )}
    </Screen>
  )
}

function Pager({
  index,
  count,
  onPrev,
  onNext,
}: {
  index: number
  count: number
  onPrev: () => void
  onNext: () => void
}) {
  const btn =
    "flex size-10 items-center justify-center rounded-full text-foreground active:bg-muted disabled:opacity-25"
  return (
    <div className="flex items-center">
      <button
        type="button"
        aria-label="Previous"
        disabled={index <= 0}
        onClick={onPrev}
        className={btn}
      >
        <ChevronLeft className="size-5" />
      </button>
      <span className="min-w-[3.2em] text-center font-label text-[12px] text-muted-foreground tabular-nums">
        {index + 1}/{count}
      </span>
      <button
        type="button"
        aria-label="Next"
        disabled={index >= count - 1}
        onClick={onNext}
        className={btn}
      >
        <ChevronRight className="size-5" />
      </button>
    </div>
  )
}

function useNow() {
  return useMinuteClock()
}

function ago(ms: number, now: number) {
  return ms > 0 ? relativeTime(new Date(ms).toISOString(), now) : ""
}

function KindLabel({ item }: { item: InboxItem }) {
  const text = {
    ask: "Asks you",
    blocked: "Blocked",
    done: "Finished",
    "bot-ask": "Asks you",
    "bot-reply": "Replied",
    notice: "Notice",
  }[item.kind]
  return (
    <span
      className={cn(
        "font-label text-[11px] uppercase tracking-wide",
        item.kind === "ask" ||
          item.kind === "bot-ask" ||
          item.kind === "blocked"
          ? "text-destructive"
          : "text-muted-foreground"
      )}
    >
      {text}
    </span>
  )
}

function ItemCard({
  item,
  go,
}: {
  item: InboxItem
  go: (r: SimpleRoute) => void
}) {
  switch (item.kind) {
    case "ask":
    case "blocked":
    case "done":
      return <PaneCard item={item} go={go} />
    case "bot-ask":
    case "bot-reply":
      return <BotCard item={item} go={go} />
    case "notice":
      return (
        <Card className="flex flex-col gap-3 p-5">
          <KindLabel item={item} />
          <div
            className={cn(
              "font-semibold text-[17px]",
              item.notice.level === "error"
                ? "text-destructive"
                : "text-foreground"
            )}
          >
            {item.notice.title}
          </div>
          {item.notice.detail && (
            <div className="whitespace-pre-wrap break-words text-[14.5px] text-muted-foreground">
              {item.notice.detail}
            </div>
          )}
          <Actions>
            <ActionButton primary onClick={() => dropNotice(item.key)}>
              Discard
            </ActionButton>
          </Actions>
        </Card>
      )
  }
}

function Actions({ children }: { children: React.ReactNode }) {
  return (
    <div className="mt-1 flex flex-wrap items-center justify-end gap-2 border-border/60 border-t pt-4">
      {children}
    </div>
  )
}

function ActionButton({
  children,
  onClick,
  primary,
  danger,
  className,
}: {
  children: React.ReactNode
  onClick: () => void
  primary?: boolean
  danger?: boolean
  className?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex h-10 items-center gap-1.5 rounded-full px-4 font-medium text-[14px]",
        primary
          ? "bg-primary text-primary-foreground"
          : danger
            ? "bg-secondary text-destructive"
            : "bg-secondary text-secondary-foreground",
        className
      )}
    >
      {children}
    </button>
  )
}

function CardHeader({
  avatar,
  name,
  sub,
  status,
}: {
  avatar?: React.ReactNode
  name: string
  sub: string
  status?: string
}) {
  return (
    <div className="flex items-center gap-3">
      {avatar}
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <StatusDot status={status} />
          <span className="min-w-0 truncate font-semibold text-[17px] text-foreground">
            {name}
          </span>
        </div>
        <div className="truncate text-[13px] text-muted-foreground">{sub}</div>
      </div>
    </div>
  )
}

// The rows of the newest turn: what the agent said and the files it showed,
// from the human's last message on.
function lastTurn(chat: ChatPayload | undefined) {
  const items = chat?.items ?? []
  let start = items.length
  while (start > 0 && items[start - 1].kind !== "user") start--
  return items.slice(start)
}

function lastToolLine(chat: ChatPayload | undefined): string {
  const items = chat?.items ?? []
  for (let i = items.length - 1; i >= 0; i--) {
    const t = items[i].tool
    if (t) return [t.title, t.subject].filter(Boolean).join(" · ")
  }
  return ""
}

function AgentProse({
  text,
  chat,
}: {
  text: string
  chat: ChatPayload | undefined
}) {
  const dataHost = chat?.served_by || chat?.host
  const cwd = chat?.cwd
  const resolve = React.useCallback(
    (src: string | undefined) =>
      cwd ? resolveMarkdownSrc(src, `${cwd}/`, dataHost ?? null) : src,
    [cwd, dataHost]
  )
  // A long message is clipped so the reply and the card's actions stay in
  // reach; whether it overflows is measured, not guessed from its length.
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
  if (!text) return null
  return (
    <div className="flex flex-col gap-1">
      <div
        ref={body}
        className={cn(
          "md-body md-chat text-[15px]",
          !open && "max-h-[22rem] overflow-hidden",
          !open &&
            overflows &&
            "[mask-image:linear-gradient(to_bottom,black_70%,transparent)]"
        )}
      >
        <Markdown source={text} resolveImageSrc={resolve} />
      </div>
      {(overflows || open) && (
        <button
          type="button"
          onClick={() => setOpen((o) => !o)}
          className="self-start py-1.5 font-medium text-[14px] text-primary"
        >
          {open ? "Show less" : "Show all"}
        </button>
      )}
    </div>
  )
}

function PaneCard({
  item,
  go,
}: {
  item: Extract<InboxItem, { pane: HostPane }>
  go: (r: SimpleRoute) => void
}) {
  const now = useNow()
  const queryClient = useQueryClient()
  const p = item.pane
  const chatQ = usePaneChat(p)
  const chat = chatQ.data ?? undefined
  const [confirmKill, setConfirmKill] = React.useState(false)
  const name = item.bot?.name ?? simpleAgentName(p)
  // The workspace, unless the name already is it.
  const ws = simpleWorkspace(p)
  const sub = [
    ws !== name ? ws : "",
    p.host_label || p.host,
    ago(p.transcript_at ?? 0, now),
  ]
    .filter(Boolean)
    .join(" · ")
  const turn = lastTurn(chat)
  const files = sharedFiles(turn)
  const text = item.kind === "done" ? lastAgentText(chat) : ""
  const refresh = () =>
    void queryClient.invalidateQueries({ queryKey: ["simple"] })

  const discard = () => dismissCard(paneKey(p), p.transcript_at ?? 0)
  const kill = async () => {
    try {
      await api.close([p.pane_id], p.host)
      toast.success(`Ended ${name}`)
    } catch (e) {
      toast.error(`Could not end ${name}: ${(e as Error).message}`)
    }
    refresh()
  }
  const focus = async () => {
    try {
      await api.focusOn(p.host, {
        pane_id: p.pane_id,
        workspace_id: p.workspace_id,
      })
      toast.success(`Focused ${name} in herdr`)
    } catch (e) {
      toast.error(`Could not focus ${name}: ${(e as Error).message}`)
    }
  }

  return (
    <Card className="flex flex-col gap-4 p-5">
      <div className="flex items-center justify-between gap-2">
        <KindLabel item={item} />
        <span className="font-label text-[11px] text-muted-foreground">
          {p.agent}
        </span>
      </div>
      <CardHeader
        avatar={item.bot ? <BotAvatar bot={item.bot} size={40} /> : undefined}
        name={name}
        sub={sub}
        status={p.agent_status}
      />
      {item.kind === "ask" && item.ask && (
        <PaneAskForm
          tool={item.ask}
          host={chat?.host ?? p.host}
          paneID={p.pane_id}
          onSent={refresh}
        />
      )}
      {item.kind === "blocked" && (
        <div className="flex flex-col gap-2">
          <p className="text-[15px] text-foreground leading-snug">
            Waiting on you, with no choices lasso can show here. Answer it in
            herdr.
          </p>
          {lastToolLine(chat) && (
            <p className="truncate rounded-xl bg-background/50 px-3 py-2 font-mono text-[12.5px] text-muted-foreground">
              {lastToolLine(chat)}
            </p>
          )}
        </div>
      )}
      {item.kind === "done" && (
        <>
          {chatQ.isLoading ? (
            <div className="h-16 animate-pulse rounded-xl bg-muted/50" />
          ) : (
            <AgentProse text={text} chat={chat} />
          )}
          <SharedFiles files={files} host={chat?.served_by || p.host} />
          <ReplyBox
            placeholder={`Reply to ${name}`}
            host={chat?.host ?? p.host}
            send={async (t) => {
              const r = await api.chatSend(chat?.host ?? p.host, p.pane_id, t)
              if (r.outcome === "confirmed") refresh()
              return r
            }}
          />
        </>
      )}
      <Actions>
        <ActionButton danger onClick={() => setConfirmKill(true)}>
          Kill agent
        </ActionButton>
        {item.kind === "blocked" ? (
          <ActionButton onClick={() => void focus()}>
            <Monitor className="size-4" />
            Focus in herdr
          </ActionButton>
        ) : (
          <ActionButton
            onClick={() => go({ view: "agent", host: p.host, pane: p.pane_id })}
          >
            <MessageSquare className="size-4" />
            Chat
          </ActionButton>
        )}
        {/* Answer is the card's one primary action while a question is open. */}
        <ActionButton primary={item.kind !== "ask"} onClick={discard}>
          Discard
        </ActionButton>
      </Actions>
      <EndAgentDialog
        open={confirmKill}
        onOpenChange={setConfirmKill}
        name={name}
        host={p.host_label || p.host}
        onEnd={() => void kill()}
      />
    </Card>
  )
}

function BotCard({
  item,
  go,
}: {
  item: Extract<InboxItem, { kind: "bot-ask" | "bot-reply" }>
  go: (r: SimpleRoute) => void
}) {
  const now = useNow()
  const queryClient = useQueryClient()
  const b = item.bot
  // A reply's card reads the bot's chat for the whole message and its files;
  // the list carries only a one-line preview.
  const chatQ = useQuery({
    queryKey: ["simple", "bot-chat", b.name, b.last_at ?? ""],
    queryFn: () => api.bots.chat(b.name),
    enabled: item.kind === "bot-reply",
    staleTime: Number.POSITIVE_INFINITY,
  })
  const chat = chatQ.data
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["bots"] })
    void queryClient.invalidateQueries({ queryKey: ["simple"] })
  }
  const text = lastAgentText(chat) || b.last_text || ""
  const files = sharedFiles(lastTurn(chat))
  const discard = () =>
    item.kind === "bot-ask"
      ? dismissCard(item.key, 0)
      : dismissCard(item.key, item.at)

  return (
    <Card className="flex flex-col gap-4 p-5">
      <KindLabel item={item} />
      <CardHeader
        avatar={<BotAvatar bot={b} size={40} />}
        name={b.name}
        sub={["bot", b.host, ago(item.at, now)].filter(Boolean).join(" · ")}
        status={b.state}
      />
      {item.kind === "bot-ask" && item.pending && (
        <BotAskForm bot={b.name} question={item.pending} onSent={refresh} />
      )}
      {item.kind === "bot-reply" && (
        <>
          <AgentProse text={text} chat={chat} />
          <SharedFiles files={files} host={chat?.served_by || b.host} />
          <ReplyBox
            placeholder={`Message ${b.name}`}
            send={async (t) => {
              const r = await api.bots.chatSend(b.name, t)
              if (r.outcome === "confirmed") refresh()
              return r
            }}
          />
        </>
      )}
      <Actions>
        <ActionButton onClick={() => go({ view: "bot", name: b.name })}>
          <MessageSquare className="size-4" />
          Chat
        </ActionButton>
        <ActionButton primary={item.kind !== "bot-ask"} onClick={discard}>
          Discard
        </ActionButton>
      </Actions>
    </Card>
  )
}

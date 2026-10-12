import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ChevronLeft,
  ChevronRight,
  Inbox as InboxIcon,
  MessageSquare,
} from "lucide-react"
import * as React from "react"
import { createPortal } from "react-dom"
import { toast } from "sonner"

import { EndAgentDialog } from "@/components/AgentParts"
import { BotAvatar } from "@/components/BotParts"
import { Markdown, resolveMarkdownSrc } from "@/components/Markdown"
import { paneKey } from "@/lib/agents"
import { api, type ChatPayload, type HostPane } from "@/lib/api"
import { relativeTime, useMinuteClock } from "@/lib/bots"
import { ackCard } from "@/lib/ui-state"
import { cn } from "@/lib/utils"
import { BotAskForm, PaneAskForm } from "@/simple/AskForm"
import {
  ackNotice,
  botChatQuery,
  chatQuery,
  type InboxItem,
  inboxCursor,
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
import { useSwipePager } from "@/simple/swipe"

// Where a card puts its reply box and actions: a bar pinned above the tab bar,
// so they hold still while the reader pages and a card's content loads.
const FooterSlot = React.createContext<HTMLElement | null>(null)

function CardFooter({ children }: { children: React.ReactNode }) {
  const slot = React.useContext(FooterSlot)
  return slot ? createPortal(children, slot) : null
}

// The Inbox opens on its newest message, never a list, and pages through the
// rest in Inbox order: the arrows, or a horizontal swipe. Landing on a card is
// what marks it seen.
export function InboxView({ go }: { go: (r: SimpleRoute) => void }) {
  const inbox = useInbox()
  const { items } = inbox
  // The card on screen, by key, so a list reordering under it does not move
  // the reader. When it leaves (answered, acknowledged) the next one takes its
  // place.
  // It starts where the Inbox was left (inboxCursor), so Back from a chat
  // opened on a card returns to that card.
  const [currentKey, setCurrentKey] = React.useState<string | null>(
    inboxCursor.key
  )
  const lastIndex = React.useRef(inboxCursor.index)
  let index = items.findIndex((i) => i.key === currentKey)
  if (index < 0) index = Math.min(lastIndex.current, items.length - 1)
  const item = index >= 0 ? items[index] : undefined
  React.useEffect(() => {
    lastIndex.current = Math.max(0, index)
    // Only a card on screen moves the cursor: an empty list while loading
    // must not forget where the reader was.
    if (!item) return
    inboxCursor.index = index
    inboxCursor.key = item.key
  }, [index, item])
  // Back from a chat where the human submitted something: that card is dealt
  // with, so land on the one after it (or, at the end, the one before). A card
  // that has already left the list needs nothing: its place is the next one.
  React.useEffect(() => {
    if (!inboxCursor.chatFromInbox || items.length === 0) return
    inboxCursor.chatFromInbox = false
    if (!inboxCursor.advance) return
    inboxCursor.advance = false
    const at = items.findIndex((i) => i.key === inboxCursor.key)
    if (at < 0) return
    const next = items[at + 1] ?? items[at - 1]
    if (next) setCurrentKey(next.key)
  }, [items])
  React.useEffect(() => {
    if (item) markSeen(item)
  }, [item])

  // Every card's content is read ahead, so paging lands on a card that is
  // already whole rather than one that grows under the reader. Each read is
  // keyed by the transcript's moment, so it is fetched once per turn.
  useQueries({
    queries: items.flatMap((i) =>
      "pane" in i
        ? [chatQuery(i.pane)]
        : i.kind === "bot-reply"
          ? [botChatQuery(i.bot)]
          : []
    ),
  })
  const [footer, setFooter] = React.useState<HTMLElement | null>(null)

  // A chat opened from a card remembers that it came from the Inbox.
  const openFromCard = (r: SimpleRoute) => {
    if (r.view === "agent" || r.view === "bot") inboxCursor.chatFromInbox = true
    go(r)
  }

  const swipe = useSwipePager({
    canPrev: index > 0,
    canNext: index < items.length - 1,
    onPage: (d) => {
      const next = items[index + d]
      if (next) setCurrentKey(next.key)
    },
  })

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
            onPrev={() => swipe.page(-1)}
            onNext={() => swipe.page(1)}
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
        <FooterSlot.Provider value={footer}>
          {/* The card under the finger: it follows a horizontal drag (the
              browser keeps the vertical scroll), and the ScrollBody inside is
              keyed by the card, so each one opens at its top. */}
          <div
            ref={swipe.ref}
            style={swipe.style}
            {...swipe.handlers}
            className="flex min-h-0 flex-1 touch-pan-y flex-col will-change-transform"
          >
            <ScrollBody key={item.key} className="phone-cards pt-2 pb-4">
              <ItemCard item={item} go={openFromCard} />
            </ScrollBody>
          </div>
          <div
            ref={setFooter}
            className="flex flex-none flex-col gap-2.5 border-border/60 border-t px-4 pt-3 pb-[calc(env(safe-area-inset-bottom)+96px)]"
          />
        </FooterSlot.Provider>
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
    notice:
      item.kind === "notice" && item.notice.kind === "agent_message"
        ? "Message"
        : "Notice",
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
          {item.notice.body && (
            <div className="whitespace-pre-wrap break-words text-[14.5px] text-muted-foreground">
              {item.notice.body}
            </div>
          )}
          <CardFooter>
            <Actions>
              <ActionButton primary onClick={() => ackNotice(item.notice)}>
                Acknowledge
              </ActionButton>
            </Actions>
          </CardFooter>
        </Card>
      )
  }
}

// A card whose reply is half written is not acknowledged out from under it:
// Acknowledge waits until the reply is sent or cleared.
const HELD_BY_DRAFT = "Send or clear your reply first"

function Actions({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex flex-wrap items-center justify-end gap-2">
      {children}
    </div>
  )
}

// heldBy, when set, greys the button out and says why: aria-disabled rather
// than disabled, so it stays focusable and a tap can still explain itself (a
// title never shows on a touch screen).
function ActionButton({
  children,
  onClick,
  primary,
  danger,
  heldBy,
  className,
}: {
  children: React.ReactNode
  onClick: () => void
  primary?: boolean
  danger?: boolean
  heldBy?: string
  className?: string
}) {
  return (
    <button
      type="button"
      aria-disabled={heldBy ? true : undefined}
      title={heldBy}
      onClick={() => (heldBy ? toast(heldBy) : onClick())}
      className={cn(
        // Buttons share the row, so one that wraps takes the full width
        // rather than hanging off the end of a short line.
        "flex h-11 flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-full px-3 font-medium text-[14.5px]",
        primary
          ? "bg-primary text-primary-foreground"
          : danger
            ? "bg-secondary text-destructive"
            : "bg-secondary text-secondary-foreground",
        heldBy && "opacity-40",
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
  if (!text) return null
  return (
    <div className="md-body md-chat">
      <Markdown source={text} resolveImageSrc={resolve} />
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
  const [drafting, setDrafting] = React.useState(false)
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

  const ack = () => ackCard(item.ackKey, item.ackAt)
  const kill = async () => {
    try {
      await api.close([p.pane_id], p.host)
      toast.success(`Ended ${name}`)
    } catch (e) {
      toast.error(`Could not end ${name}: ${(e as Error).message}`)
    }
    refresh()
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
            <div className="h-24 animate-pulse rounded-xl bg-muted/50" />
          ) : (
            <AgentProse text={text} chat={chat} />
          )}
          <SharedFiles files={files} host={chat?.served_by || p.host} />
        </>
      )}
      <CardFooter>
        {item.kind === "done" && (
          <ReplyBox
            draftKey={`pane:${paneKey(p)}`}
            placeholder="Reply…"
            host={chat?.host ?? p.host}
            send={async (t) => {
              const r = await api.chatSend(chat?.host ?? p.host, p.pane_id, t)
              if (r.outcome === "confirmed") refresh()
              return r
            }}
            onDraftChange={setDrafting}
          />
        )}
        <Actions>
          <ActionButton danger onClick={() => setConfirmKill(true)}>
            Kill agent
          </ActionButton>
          <ActionButton
            onClick={() => go({ view: "agent", host: p.host, pane: p.pane_id })}
          >
            <MessageSquare className="size-4" />
            Chat
          </ActionButton>
          {/* Answer is the card's one primary action while a question is open. */}
          <ActionButton
            primary={item.kind !== "ask"}
            heldBy={drafting ? HELD_BY_DRAFT : undefined}
            onClick={ack}
          >
            Acknowledge
          </ActionButton>
        </Actions>
      </CardFooter>
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
    ...botChatQuery(b),
    enabled: item.kind === "bot-reply",
  })
  const chat = chatQ.data
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ["bots"] })
    void queryClient.invalidateQueries({ queryKey: ["simple"] })
  }
  const text = lastAgentText(chat) || b.last_text || ""
  const files = sharedFiles(lastTurn(chat))
  const ack = () => ackCard(item.ackKey, item.ackAt)
  const [drafting, setDrafting] = React.useState(false)

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
        </>
      )}
      <CardFooter>
        {item.kind === "bot-reply" && (
          <ReplyBox
            draftKey={`bot:${b.name}`}
            placeholder="Reply…"
            send={async (t) => {
              const r = await api.bots.chatSend(b.name, t)
              if (r.outcome === "confirmed") refresh()
              return r
            }}
            onDraftChange={setDrafting}
          />
        )}
        <Actions>
          <ActionButton onClick={() => go({ view: "bot", name: b.name })}>
            <MessageSquare className="size-4" />
            Chat
          </ActionButton>
          <ActionButton
            primary={item.kind !== "bot-ask"}
            heldBy={drafting ? HELD_BY_DRAFT : undefined}
            onClick={ack}
          >
            Acknowledge
          </ActionButton>
        </Actions>
      </CardFooter>
    </Card>
  )
}

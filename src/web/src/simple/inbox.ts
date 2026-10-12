import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query"
import * as React from "react"
import {
  api,
  type BotPendingUI,
  type BotView,
  type ChatAsk,
  type ChatPayload,
  type ChatTool,
  type HostPane,
  type LiveNotification,
} from "@/lib/api"
import { lsGet, lsSet } from "@/lib/app-store"
import { useBots } from "@/lib/bots"
import { NOTIFICATION_EVENT } from "@/lib/notify"
import { ackCard, useUIState } from "@/lib/ui-state"
import {
  botForPane,
  inboxEntries,
  inboxNotices,
  noticeAckKey,
} from "@/simple/inbox-rule"

export { botForPane }

// The Inbox: what waits on the human, across lasso's claude and omp agents.
// Which entries it holds is inbox-rule.ts, shared with the Mac app and read
// only from lasso's state, so every device lists the same things; this file
// adds the presentation: an ask's choices, and this device's seen marks.
//
// Order: asks, then blocked with no choices, then finished turns, then
// notices; newest first inside each.

export type InboxItem =
  | {
      kind: "ask" | "blocked" | "done"
      key: string
      pane: HostPane
      // The bot this pane runs, for a claude-runtime bot (it enters as its pane).
      bot?: BotView
      at: number
      // The open question, for an ask.
      ask?: ChatTool
      // What Acknowledge writes (ui_state.card_dismiss).
      ackKey: string
      ackAt: number
    }
  | {
      kind: "bot-ask" | "bot-reply"
      key: string
      bot: BotView
      at: number
      pending?: BotPendingUI
      ackKey: string
      ackAt: number
    }
  // lasso's own notice, or an agent's deliberate message for the human.
  | { kind: "notice"; key: string; notice: LiveNotification; at: number }

// The agent's name: herdr's agent label, else its tab's name, else its
// pane's, else its workspace's, else what it is working on.
export function simpleAgentName(p: HostPane): string {
  return (
    p.agent_label ||
    named(p.tab_label) ||
    named(p.pane_label) ||
    simpleWorkspace(p) ||
    p.terminal_title ||
    p.pane_id
  )
}

// herdr numbers a tab or pane nobody named ("1"), which names nothing.
function named(label: string | undefined): string {
  return label && !/^\d+$/.test(label) ? label : ""
}

// The workspace's name, unless it is the shared Scratch or herdr's "~" for a
// workspace never given one.
export function simpleWorkspace(p: HostPane): string {
  const w = p.workspace_label ?? ""
  return w && w !== "~" && w !== "Scratch" ? w : ""
}

// The open question in a chat: the newest tool row's ask, while that call is
// still running and nothing has been picked or typed.
export function openAsk(chat: ChatPayload | undefined): ChatTool | undefined {
  const items = chat?.items ?? []
  for (let i = items.length - 1; i >= 0; i--) {
    const t = items[i].tool
    if (!t) continue
    if (!t.ask) return undefined
    const answered = t.ask.questions.some(
      (q) => (q.selected?.length ?? 0) > 0 || q.custom
    )
    return t.state === "running" && !answered ? t : undefined
  }
  return undefined
}

// A short stable hash of an ask, so a changed question is a new item and a
// pick in progress never rides onto it.
export function askHash(ask: ChatAsk | undefined): string {
  if (!ask) return ""
  const s = JSON.stringify(
    ask.questions.map((q) => [q.question, q.options.map((o) => o.label)])
  )
  let h = 5381
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0
  return (h >>> 0).toString(36)
}

// The newest thing the agent said, for a finished turn's card.
export function lastAgentText(chat: ChatPayload | undefined): string {
  const items = chat?.items ?? []
  for (let i = items.length - 1; i >= 0; i--) {
    const it = items[i]
    if (it.kind === "agent" && !it.thinking && it.text?.trim()) return it.text
    if (it.kind === "user") return ""
  }
  return ""
}

// --- Seen marks (this device only) -----------------------------------------
//
// Seen only decides the tab's dot ("new since you looked"). It never changes
// which entries the Inbox holds or how many: a seen item stays listed until
// it is answered, acknowledged, or no longer waiting.

const SEEN_KEY = "lasso-m-seen"

function readMap(key: string): Record<string, number> | null {
  try {
    const v = JSON.parse(lsGet(key) ?? "null")
    return v && typeof v === "object" ? v : null
  } catch {
    return null
  }
}

let seenMarks = readMap(SEEN_KEY) ?? {}
const seenListeners = new Set<() => void>()

export function markSeen(item: InboxItem) {
  if ((seenMarks[item.key] ?? -1) >= item.at) return
  seenMarks = { ...seenMarks, [item.key]: item.at }
  // Only what can still be listed is worth keeping.
  const keys = Object.keys(seenMarks)
  if (keys.length > 300)
    for (const k of keys.slice(0, keys.length - 300)) delete seenMarks[k]
  lsSet(SEEN_KEY, JSON.stringify(seenMarks))
  for (const l of seenListeners) l()
}

export function useSeenMarks(): Record<string, number> {
  return React.useSyncExternalStore(
    React.useCallback((cb: () => void) => {
      seenListeners.add(cb)
      return () => seenListeners.delete(cb)
    }, []),
    () => seenMarks
  )
}

export function isUnseen(item: InboxItem, seen: Record<string, number>) {
  return (seen[item.key] ?? -1) < item.at
}

// --- Where the Inbox was (this page's life) ---------------------------------
//
// The card on screen outlives the Inbox view, so a chat opened from a card
// comes Back to that card. If the human submitted something in that chat (a
// message, an answer), they dealt with the card, and Back lands on the next.

export const inboxCursor = {
  key: null as string | null,
  index: 0,
  // A chat was opened from the Inbox and has not come back yet.
  chatFromInbox: false,
  // Something was submitted in that chat.
  advance: false,
}

export function noteChatSubmitted() {
  if (inboxCursor.chatFromInbox) inboxCursor.advance = true
}

// --- Notices (lasso's, the same on every device) ---------------------------
//
// lasso keeps its own notices and agents' deliberate messages (the notify
// tool) for the Inbox (GET /api/notices). Every one is listed, whatever this
// device's choices: those decide interruptions, not the Inbox. Acknowledge is
// shared, as a card's is.

function useNotices() {
  const q = useQuery({
    queryKey: ["simple", "notices"],
    queryFn: () => api.notices(),
    placeholderData: keepPreviousData,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
  const { refetch } = q
  // A new one arrives live: read the list again rather than keep a copy.
  React.useEffect(() => {
    const on = () => void refetch()
    window.addEventListener(NOTIFICATION_EVENT, on)
    return () => window.removeEventListener(NOTIFICATION_EVENT, on)
  }, [refetch])
  return q.data?.notices ?? []
}

export function ackNotice(n: LiveNotification) {
  ackCard(noticeAckKey(n.id), n.at)
}

// --- The model ---------------------------------------------------------------

// The fleet listing every view of the simple app reads. One query, polled like
// the full UI's agent list.
export function useFleet() {
  return useQuery({
    queryKey: ["simple", "all-panes"],
    queryFn: () => api.allPanes(),
    placeholderData: keepPreviousData,
    refetchInterval: 5000,
    refetchIntervalInBackground: false,
  })
}

// The chat of a pane, read only while it is a candidate and re-read only when
// its transcript moves (the key carries transcript_at).
export function chatQuery(p: HostPane) {
  return {
    queryKey: ["simple", "chat", p.host, p.pane_id, p.transcript_at ?? 0],
    queryFn: () => api.chat(p.pane_id, undefined, p.host),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 5 * 60_000,
  }
}

// A bot's chat, for its reply card: re-read only when it speaks again.
export function botChatQuery(b: BotView) {
  return {
    queryKey: ["simple", "bot-chat", b.name, b.last_at ?? ""],
    queryFn: () => api.bots.chat(b.name),
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 5 * 60_000,
  }
}

export function usePaneChat(p: HostPane | undefined) {
  return useQuery({
    ...(p ? chatQuery(p) : { queryKey: ["simple", "chat", "none"] }),
    queryFn: p ? chatQuery(p).queryFn : () => null,
    enabled: Boolean(p),
  })
}

// Finished turns count from lasso's shared baseline (ui_state.inbox_since).
// A lasso too old to have one gets this page's load time instead, so the
// Inbox still starts with what is waiting, not with history.
const loadedAt = Date.now()

export function useInbox() {
  const fleet = useFleet()
  const botsQ = useBots(true)
  const ui = useUIState()
  const noticeList = useNotices()
  // Acknowledged entries, shared by every client (ui_state.dismissed_cards).
  const acked = React.useMemo(
    () => ui.dismissed_cards ?? {},
    [ui.dismissed_cards]
  )
  const since = ui.inbox_since ?? loadedAt
  const bots = React.useMemo(() => botsQ.data?.bots ?? [], [botsQ.data])

  // The shared rule decides membership. Before the first listing nothing is
  // known, so nothing is waiting.
  const entries = React.useMemo(
    () =>
      fleet.data
        ? inboxEntries({
            panes: fleet.data.panes ?? [],
            bots,
            dismissed: acked,
            since,
          })
        : [],
    [fleet.data, bots, acked, since]
  )

  // Only a blocked pane's chat decides its group (an ask, or no choices).
  const blockedChats = useQueries({
    queries: entries
      .filter((e) => e.kind === "waiting" && !e.pending)
      .map((e) => chatQuery(e.pane)),
  })

  const items = React.useMemo(() => {
    const asks: InboxItem[] = []
    const stuck: InboxItem[] = []
    const done: InboxItem[] = []
    let bi = 0
    for (const e of entries) {
      const { ackKey, ackAt, at } = e
      if (e.pending && e.bot) {
        asks.push({
          kind: "bot-ask",
          key: ackKey,
          bot: e.bot,
          at,
          pending: e.pending,
          ackKey,
          ackAt,
        })
      } else if (e.kind === "waiting") {
        const ask = openAsk(blockedChats[bi++]?.data)
        if (ask)
          asks.push({
            kind: "ask",
            key: `${e.key}#${askHash(ask.ask)}`,
            pane: e.pane,
            bot: e.bot,
            at,
            ask,
            ackKey,
            ackAt,
          })
        else
          stuck.push({
            kind: "blocked",
            key: e.key,
            pane: e.pane,
            bot: e.bot,
            at,
            ackKey,
            ackAt,
          })
      } else if (e.bot && e.bot.runtime !== "claude") {
        // An omp bot's reply reads from its own chat; a claude bot's chat is
        // its pane's, so it shows as the pane.
        done.push({
          kind: "bot-reply",
          key: ackKey,
          bot: e.bot,
          at,
          ackKey,
          ackAt,
        })
      } else {
        done.push({
          kind: "done",
          key: e.key,
          pane: e.pane,
          bot: e.bot,
          at,
          ackKey,
          ackAt,
        })
      }
    }
    const newest = (a: InboxItem, b: InboxItem) => b.at - a.at
    return [
      ...asks.sort(newest),
      ...stuck.sort(newest),
      ...done,
      ...inboxNotices(noticeList, acked).map(
        (n): InboxItem => ({
          kind: "notice",
          key: noticeAckKey(n.id),
          notice: n,
          at: n.at,
        })
      ),
    ]
  }, [entries, blockedChats, acked, noticeList])

  return {
    items,
    loading: fleet.isLoading,
    error: fleet.error as Error | null,
    unlisted: Object.keys(fleet.data?.errors ?? {}),
    refetch: () => {
      void fleet.refetch()
      void botsQ.refetch()
    },
  }
}

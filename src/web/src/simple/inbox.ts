import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query"
import * as React from "react"
import { paneKey } from "@/lib/agents"
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
import { useUIState } from "@/lib/ui-state"

// The Inbox: what waits on the human, across lasso's claude and omp agents.
// The rules are LassoDesktop's (mac/PLAN.md §13), so the Mac and the phone
// agree on what is waiting; only the presentation differs.
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
    }
  | {
      kind: "bot-ask" | "bot-reply"
      key: string
      bot: BotView
      at: number
      pending?: BotPendingUI
    }
  // lasso's own notice, or an agent's deliberate message for the human.
  | { kind: "notice"; key: string; notice: LiveNotification; at: number }

// The harnesses the Inbox speaks for. No other harness enters it, even when
// herdr reports it blocked: nothing here is designed for one.
const HARNESSES = new Set(["claude", "omp"])

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

export function botForPane(bots: BotView[], p: HostPane): BotView | undefined {
  return bots.find(
    (b) =>
      b.host === p.host &&
      ((b.pane_id && b.pane_id === p.pane_id) || b.name === p.agent_label)
  )
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

// --- Baselines and seen marks (this device only) ---------------------------
//
// A finished turn is waiting when it is newer than this device's baseline for
// the pane. The first run seeds the baseline with every pane's current turn, so
// the Inbox starts with what is waiting, not with history. Seen is separate: it
// only decides the tab's badge, and a seen item stays listed until it is
// answered, acknowledged, or no longer waiting.

const BASELINE_KEY = "lasso-m-baseline"
const SEEN_KEY = "lasso-m-seen"

function readMap(key: string): Record<string, number> | null {
  try {
    const v = JSON.parse(lsGet(key) ?? "null")
    return v && typeof v === "object" ? v : null
  } catch {
    return null
  }
}

let baseline = readMap(BASELINE_KEY)
let seenMarks = readMap(SEEN_KEY) ?? {}
const seenListeners = new Set<() => void>()

function seedBaseline(panes: HostPane[]) {
  if (baseline) return
  baseline = {}
  // A pane herdr reports done has a turn nobody has looked at, so it is
  // waiting now and gets no baseline; every other pane's history is not.
  for (const p of panes)
    if (p.agent_status !== "done") baseline[paneKey(p)] = p.transcript_at ?? 0
  lsSet(BASELINE_KEY, JSON.stringify(baseline))
}

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

// --- Notices (this tab only, as a toast would be) ---------------------------
//
// One-shot notifications the Inbox keeps: lasso's own notices and an agent's
// deliberate message (the notify tool). Every one is listed, whatever this
// device's choices: those decide interruptions, not the Inbox.

const MAX_NOTICES = 50
const KEPT_KINDS = new Set(["lasso_notice", "agent_message"])
let notices: (LiveNotification & { key: string })[] = []
const noticeListeners = new Set<() => void>()

function emitNotices() {
  for (const l of noticeListeners) l()
}

if (typeof window !== "undefined")
  window.addEventListener(NOTIFICATION_EVENT, (e) => {
    const n = (e as CustomEvent<LiveNotification>).detail
    if (!KEPT_KINDS.has(n.kind)) return
    const key = `notice:${n.id}`
    if (notices.some((x) => x.key === key)) return
    notices = [{ ...n, key }, ...notices].slice(0, MAX_NOTICES)
    emitNotices()
  })

export function dropNotice(key: string) {
  notices = notices.filter((n) => n.key !== key)
  emitNotices()
}

function useNotices() {
  return React.useSyncExternalStore(
    React.useCallback((cb: () => void) => {
      noticeListeners.add(cb)
      return () => noticeListeners.delete(cb)
    }, []),
    () => notices
  )
}

// --- The model ---------------------------------------------------------------

function msOf(iso: string | undefined): number {
  const t = iso ? Date.parse(iso) : Number.NaN
  return Number.isNaN(t) ? 0 : t
}

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
function chatQuery(p: HostPane) {
  return {
    queryKey: ["simple", "chat", p.host, p.pane_id, p.transcript_at ?? 0],
    queryFn: () => api.chat(p.pane_id, undefined, p.host),
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

export function useInbox() {
  const fleet = useFleet()
  const botsQ = useBots(true)
  const ui = useUIState()
  const noticeList = useNotices()
  // Acknowledged cards, shared by every client (ui_state.dismissed_cards).
  const acked = ui.dismissed_cards ?? {}

  const panes = React.useMemo(
    () => (fleet.data?.panes ?? []).filter((p) => p.has_agent),
    [fleet.data]
  )
  const bots = botsQ.data?.bots ?? []

  // Seed the baseline once the first real listing lands.
  if (fleet.data && fleet.dataUpdatedAt > 0) seedBaseline(panes)

  // Panes that are candidates: claude/omp, not an omp bot's pane (that one
  // enters as the bot), blocked or finished past the baseline, not acknowledged.
  const candidates = React.useMemo(() => {
    const out: { pane: HostPane; bot?: BotView; blocked: boolean }[] = []
    for (const p of panes) {
      if (!HARNESSES.has(p.agent ?? "")) continue
      const bot = botForPane(bots, p)
      if (bot && bot.runtime !== "claude") continue
      const key = paneKey(p)
      const at = p.transcript_at ?? 0
      const gone = acked[key]
      if (gone !== undefined && at <= gone) continue
      if (p.agent_status === "blocked")
        out.push({ pane: p, bot, blocked: true })
      else if (
        (p.agent_status === "done" || p.agent_status === "idle") &&
        // Before the first listing seeds it nothing is waiting; after, a pane
        // with no baseline enters with its first finished turn.
        baseline &&
        at > (baseline[key] ?? 0)
      )
        out.push({ pane: p, bot, blocked: false })
    }
    return out
  }, [panes, bots, acked])

  // Only a blocked pane's chat decides its group (an ask, or no choices).
  const blockedChats = useQueries({
    queries: candidates.filter((c) => c.blocked).map((c) => chatQuery(c.pane)),
  })

  const items = React.useMemo(() => {
    const asks: InboxItem[] = []
    const stuck: InboxItem[] = []
    const done: InboxItem[] = []
    let bi = 0
    for (const c of candidates) {
      const key = paneKey(c.pane)
      const at = c.pane.transcript_at ?? 0
      if (c.blocked) {
        const chat = blockedChats[bi++]?.data
        const ask = openAsk(chat)
        if (ask)
          asks.push({
            kind: "ask",
            key: `${key}#${askHash(ask.ask)}`,
            pane: c.pane,
            bot: c.bot,
            at,
            ask,
          })
        else stuck.push({ kind: "blocked", key, pane: c.pane, bot: c.bot, at })
      } else done.push({ kind: "done", key, pane: c.pane, bot: c.bot, at })
    }
    for (const b of bots) {
      if (b.runtime === "claude") continue
      const pending = b.pending_ui?.[0]
      if (b.state === "blocked" && pending) {
        const key = `bot:${b.name}#${pending.id}`
        if (acked[key] === undefined)
          asks.push({
            kind: "bot-ask",
            key,
            bot: b,
            at: msOf(b.last_at),
            pending,
          })
        continue
      }
      const at = msOf(b.last_at)
      const key = `bot:${b.name}`
      if (
        b.last_kind === "agent" &&
        at > (botBaseline(b) ?? Number.POSITIVE_INFINITY) &&
        !(acked[key] !== undefined && at <= acked[key])
      )
        done.push({ kind: "bot-reply", key, bot: b, at })
    }
    const newest = (a: InboxItem, b: InboxItem) => b.at - a.at
    return [
      ...asks.sort(newest),
      ...stuck.sort(newest),
      ...done.sort(newest),
      ...noticeList.map(
        (n): InboxItem => ({ kind: "notice", key: n.key, notice: n, at: n.at })
      ),
    ]
  }, [candidates, blockedChats, bots, acked, noticeList])

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

// A bot's finished turn is waiting once it is newer than this device's
// baseline for the bot, which the first sighting of the bot sets.
const BOT_BASELINE_KEY = "lasso-m-bot-baseline"
let botBaselines = readMap(BOT_BASELINE_KEY) ?? {}

function botBaseline(b: BotView): number | undefined {
  if (!(b.name in botBaselines)) {
    botBaselines = { ...botBaselines, [b.name]: msOf(b.last_at) }
    lsSet(BOT_BASELINE_KEY, JSON.stringify(botBaselines))
  }
  return botBaselines[b.name]
}

import type {
  BotPendingUI,
  BotView,
  HostPane,
  LiveNotification,
} from "@/lib/api"

// The Inbox's membership rule, shared with the Mac app (FleetModel.inbox in
// mac/Sources/LassoDesktopKit/Inbox/InboxModel.swift). Everything it reads
// is lasso's, never this device's: the fleet, the bots, ui_state's
// acknowledgements (dismissed_cards) and baseline (inbox_since), and the
// notices lasso keeps (/api/notices). So every device lists the same
// entries, and the Mac's badge equals the phone's count. Both sides are
// pinned by the same cases (mac/Spec/inbox/rules.json, copied beside this
// file as inbox-rules.json; a Go test keeps the two equal).
//
// Pure, with no React and no app state, so it can be tested on its own.

// The harnesses the Inbox speaks for. No other harness enters it, even when
// herdr reports it blocked: nothing here is designed for one.
export const INBOX_HARNESSES = new Set(["claude", "omp"])

export interface InboxEntry {
  kind: "waiting" | "finished"
  // The pane's key (host NUL pane id): what the card pages by.
  key: string
  pane: HostPane
  // The bot running in the pane, if it is one.
  bot?: BotView
  // An omp bot's open question, when that is what waits.
  pending?: BotPendingUI
  // What Acknowledge writes to ui_state.card_dismiss.
  ackKey: string
  ackAt: number
  // When it happened (unix ms), for the order.
  at: number
}

export interface InboxRuleInput {
  panes: HostPane[]
  bots: BotView[]
  dismissed: Record<string, number>
  // ui_state.inbox_since: finished turns count after it.
  since: number
}

export function inboxPaneKey(p: HostPane): string {
  return `${p.host}\u0000${p.pane_id}`
}

// The bot that runs in this pane: by its pane, or by the herdr label a bot's
// pane carries (its name).
export function botForPane(bots: BotView[], p: HostPane): BotView | undefined {
  return bots.find(
    (b) =>
      b.host === p.host &&
      ((b.pane_id !== undefined && b.pane_id === p.pane_id) ||
        b.name === p.agent_label)
  )
}

export function unixMS(iso: string | undefined): number | undefined {
  const t = iso ? Date.parse(iso) : Number.NaN
  return Number.isNaN(t) ? undefined : t
}

// The Inbox, in order: what waits on you, then finished turns, newest first
// in each (ties by pane key).
//
// - A pane enters while it has a claude or omp agent (not a closed record).
// - Waiting: the pane is blocked (key: the pane, up to its transcript_at), or
//   it runs an omp bot with a pending question (key: bot:<name>#<id>, hidden
//   once that question is acknowledged).
// - Finished: the pane is not working (done, idle, or a status herdr does
//   not name, which reads as idle) and its turn is newer than
//   inbox_since. A bot's turn (either runtime) is its last reply (key:
//   bot:<name>, up to last_at); any other agent's is its transcript_at.
// - An acknowledged entry is hidden until its moment passes the value
//   stored: a new turn or a new question brings it back.
export function inboxEntries(input: InboxRuleInput): InboxEntry[] {
  const { panes, bots, dismissed: acked, since } = input
  const waiting: InboxEntry[] = []
  const finished: InboxEntry[] = []
  for (const p of panes) {
    if (!p.has_agent || p.closed) continue
    if (!INBOX_HARNESSES.has((p.agent ?? "").toLowerCase())) continue
    const key = inboxPaneKey(p)
    const bot = botForPane(bots, p)
    const pending = bot?.runtime === "omp" ? bot.pending_ui?.[0] : undefined
    const at = p.transcript_at ?? 0
    const status = p.agent_status
    if (status === "blocked" || pending) {
      if (pending && bot) {
        const ackKey = `bot:${bot.name}#${pending.id}`
        if (acked[ackKey] !== undefined) continue
        waiting.push({
          kind: "waiting",
          key,
          pane: p,
          bot,
          pending,
          ackKey,
          ackAt: 0,
          at,
        })
      } else {
        const d = acked[key]
        if (d !== undefined && at <= d) continue
        waiting.push({
          kind: "waiting",
          key,
          pane: p,
          bot,
          ackKey: key,
          ackAt: at,
          at,
        })
      }
      continue
    }
    if (status === "working") continue
    if (bot) {
      const ms = unixMS(bot.last_at)
      if (bot.last_kind !== "agent" || ms === undefined || ms <= since) continue
      const ackKey = `bot:${bot.name}`
      const d = acked[ackKey]
      if (d !== undefined && ms <= d) continue
      finished.push({
        kind: "finished",
        key,
        pane: p,
        bot,
        ackKey,
        ackAt: ms,
        at: ms,
      })
    } else {
      if (at <= since) continue
      const d = acked[key]
      if (d !== undefined && at <= d) continue
      finished.push({
        kind: "finished",
        key,
        pane: p,
        ackKey: key,
        ackAt: at,
        at,
      })
    }
  }
  const newest = (a: InboxEntry, b: InboxEntry) =>
    a.at !== b.at ? b.at - a.at : a.key < b.key ? -1 : a.key > b.key ? 1 : 0
  return [...waiting.sort(newest), ...finished.sort(newest)]
}

// The notice's Acknowledge key in ui_state.card_dismiss.
export function noticeAckKey(id: string): string {
  return `notice:${id}`
}

// The notices lasso keeps that nobody has acknowledged, newest first.
export function inboxNotices(
  notices: LiveNotification[],
  acked: Record<string, number>
): LiveNotification[] {
  return notices
    .filter((n) => acked[noticeAckKey(n.id)] === undefined)
    .sort((a, b) => b.at - a.at || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
}

import type { ChatItem } from "./lasso"

// Ported from lasso's src/web/src/lib/chat-merge.ts and chat-queue.ts (a
// plugin page cannot import lasso's modules). See those files for the full
// reasoning; the short version is below.

// mergeItems folds a freshly read page into what is on screen: known rows are
// updated in place, new rows land where the page puts them (before the first
// row of the page already shown, else at the end), and a finished tool card
// never regresses to running because an older page parsed it without its
// result.
export function mergeItems(prev: ChatItem[], incoming: ChatItem[]): ChatItem[] {
  if (prev.length === 0) return incoming
  const at = new Map<string, number>()
  for (let i = 0; i < prev.length; i++) at.set(prev[i].id, i)
  let changed = false
  const out: ChatItem[] = []
  const placed = new Map<string, number>()
  let p = 0
  let unseen: ChatItem[] = []
  const put = (it: ChatItem) => {
    placed.set(it.id, out.length)
    out.push(it)
  }
  for (const item of incoming) {
    const i = at.get(item.id)
    if (i === undefined) {
      if (placed.has(item.id) || unseen.some((u) => u.id === item.id)) continue
      unseen.push(item)
      changed = true
      continue
    }
    if (i < p) {
      const j = placed.get(prev[i].id)
      if (j !== undefined && out[j] !== item && !regresses(item, out[j])) {
        out[j] = item
        changed = true
      }
      continue
    }
    while (p < i) put(prev[p++])
    for (const it of unseen) put(it)
    unseen = []
    p++
    if (!sameItem(item, prev[i]) && !regresses(item, prev[i])) {
      put(item)
      changed = true
    } else {
      put(prev[i])
    }
  }
  while (p < prev.length) put(prev[p++])
  for (const it of unseen) put(it)
  return changed ? out : prev
}

function regresses(incoming: ChatItem, current: ChatItem): boolean {
  const a = incoming.tool
  const b = current.tool
  return Boolean(a && b && b.state !== "running" && a.state === "running")
}

// Every poll decodes fresh objects, so identity says nothing; comparing the
// JSON keeps an unchanged row the same object and React skips re-rendering it.
function sameItem(a: ChatItem, b: ChatItem): boolean {
  return a === b || JSON.stringify(a) === JSON.stringify(b)
}

// An echo of a message the pane accepted but the transcript has not recorded
// yet, retired once the transcript's newest user turn moves past `after`.
export interface Echo {
  id: number
  text: string
  after: string
}

export function newestUserID(items: ChatItem[]): string {
  for (let i = items.length - 1; i >= 0; i--) {
    if (items[i].kind === "user") return items[i].id
  }
  return ""
}

// reconcileEchoes retires one echo per user turn that has landed since, and
// re-points the rest at it so two quick sends retire one apart.
export function reconcileEchoes(prev: Echo[], newestUser: string): Echo[] {
  if (prev.length === 0 || newestUser === prev[0].after) return prev
  return prev.slice(1).map((e) => ({ ...e, after: newestUser }))
}

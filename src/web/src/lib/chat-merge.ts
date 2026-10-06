import type { ChatItem } from "@/lib/api"

// mergeItems folds a freshly-read page into what is already on screen: rows that
// are already here are UPDATED in place (a tool card completing, an output
// growing) and rows that are new are placed WHERE THE PAGE PUTS THEM — before
// the first row of the page that is already on screen, or at the end when none
// follows.
//
// Replacing the list instead, which is what a single-page view does, is what
// makes a conversation develop a hole: the live window is the last N kilobytes,
// so once the file grows past it the rows at its start fall out, and they are
// exactly the history someone scrolls up to find.
//
// "Unseen means newest" is NOT safe, although the transcript is append-only: the
// live window is not a fixed size. When its start lands between a tool call and
// its result the server reads a wider window (readLogPage), and that one page
// carries OLDER rows than any on screen. Appended, they were dumped below the
// newest rows mid-turn, pushing what was being read out of sight until a
// remount rebuilt the list in order.
//
// A page is parsed INDEPENDENTLY of its neighbours, so an older page can hold a
// call whose result landed in a newer one and parse it as still running. Letting
// that version through would flip a finished card back to "running" for good, so
// a state regression is refused and the newer parse stands. That is also what
// makes the result independent of the order the pages arrived in, which is not
// something a fetch loop can promise.
export function mergeItems(prev: ChatItem[], incoming: ChatItem[]): ChatItem[] {
  if (prev.length === 0) return incoming
  const at = new Map<string, number>()
  for (let i = 0; i < prev.length; i++) at.set(prev[i].id, i)
  let changed = false
  const out: ChatItem[] = []
  // Where each row landed in `out`, for a page that names a row again after
  // one already passed (it should not; this keeps it an update, not a copy).
  const placed = new Map<string, number>()
  // How much of `prev` has been copied across.
  let p = 0
  // New rows waiting for the on-screen row they precede.
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
      // Out of order relative to the screen: update it where it already is.
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
    if (item !== prev[i] && !regresses(item, prev[i])) {
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

// regresses reports whether an incoming row says LESS than the one on screen: a
// tool that has finished cannot become a tool that is running again.
function regresses(incoming: ChatItem, current: ChatItem): boolean {
  const a = incoming.tool
  const b = current.tool
  return Boolean(a && b && b.state !== "running" && a.state === "running")
}

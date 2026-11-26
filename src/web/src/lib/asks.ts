import { useQuery } from "@tanstack/react-query"
import { toast } from "sonner"
import { type Ask, api } from "@/lib/api"
import { qk, queryClient } from "@/lib/query"

// The operator's Inbox of asks (clientasks_operator.go). One query serves the
// tab and its badge; it is polled, since lasso's own SSE feed carries nothing
// of the phone app's. Faster while the tab is on screen.
export function useAsks(active = false) {
  return useQuery({
    queryKey: qk.asks,
    queryFn: () => api.asks.list(),
    refetchInterval: active ? 4_000 : 30_000,
    staleTime: 2_000,
    // The app turns focus refetches off; the Inbox wants them, since a bot
    // can file a batch of asks while the window is in the background and the
    // 30s poll pauses with it. Opening the dialog or the tab reads afresh too.
    refetchOnWindowFocus: "always",
    refetchOnMount: "always",
  })
}

// The last ask dropped in this tab, while lasso still lets it be brought back
// (the ask's `undo_until`: the bot is told of a drop only after it). ⌘Z and
// the toast's Undo both end here.
let lastDrop: { id: string; until: number } | null = null

export function noteDropped(ask: Ask) {
  if (!ask.undo_until) return
  lastDrop = { id: ask.id, until: new Date(ask.undo_until).getTime() }
  toast("Dropped.", {
    id: "ask-dropped",
    duration: 8000,
    action: { label: "Undo", onClick: () => void undoLastDrop() },
  })
}

// undoLastDrop brings the last dropped ask back; false when there is none it
// could still undo, so a caller's ⌘Z is left to whatever else wants it.
export function undoLastDrop(): boolean {
  const d = lastDrop
  if (!d || Date.now() >= d.until) return false
  lastDrop = null
  toast.dismiss("ask-dropped")
  api.asks
    .reopen(d.id)
    .then(() => toast("Brought back."))
    .catch((e) => toast.error((e as Error).message))
    .finally(() => queryClient.invalidateQueries({ queryKey: qk.asks }))
  return true
}

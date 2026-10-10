import * as React from "react"
import {
  type AskPick,
  bridge,
  type ChatItem,
  type ChatPayload,
  type Target,
} from "./lasso"
import { type Echo, mergeItems, newestUserID, reconcileEchoes } from "./merge"

// How often the transcript is re-read while the page is visible. lasso's own
// chat polls at the same rate; the transcript is written a whole message at a
// time, so faster would mostly re-read the same bytes.
const POLL_MS = 2000

export interface Queued {
  id: number
  text: string
}

export interface ChatState {
  meta: Omit<ChatPayload, "items"> | null
  items: ChatItem[]
  // Why nothing can be shown (not granted, agent not running, lasso SDK
  // missing), or null.
  error: string | null
  loaded: boolean
  running: boolean
  hasMore: boolean
  loadingOlder: boolean
  echoes: Echo[]
  queue: Queued[]
  queuePaused: boolean
  sending: boolean
  // The last send/stop/answer problem, shown above the composer.
  notice: string | null
}

// t must be stable (App memoizes it): every callback below depends on it.
export function useChat(t: Target) {
  const [meta, setMeta] = React.useState<ChatState["meta"]>(null)
  const [items, setItems] = React.useState<ChatItem[]>([])
  const [error, setError] = React.useState<string | null>(null)
  const [loaded, setLoaded] = React.useState(false)
  const [olderStart, setOlderStart] = React.useState<number | null>(null)
  const [loadingOlder, setLoadingOlder] = React.useState(false)
  const [echoes, setEchoes] = React.useState<Echo[]>([])
  const [queue, setQueue] = React.useState<Queued[]>([])
  const [queuePaused, setQueuePaused] = React.useState(false)
  const [sending, setSending] = React.useState(false)
  const [notice, setNotice] = React.useState<string | null>(null)
  const transcript = React.useRef<string | null>(null)
  const seq = React.useRef(0)

  const refresh = React.useCallback(async () => {
    try {
      const page = await bridge.read(t)
      setError(null)
      const { items: incoming = [], ...rest } = page
      const path = rest.path ?? ""
      if (transcript.current !== path) {
        // A different session in the same agent (a relaunch, /clear): start
        // over rather than splice two conversations together.
        transcript.current = path
        setItems(incoming)
        setOlderStart(null)
        setEchoes([])
      } else {
        setItems((prev) => mergeItems(prev, incoming))
      }
      setMeta(rest)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoaded(true)
    }
  }, [t])

  // Poll while the page is visible; a hidden view costs nothing.
  React.useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    let stopped = false
    const tick = async () => {
      if (stopped) return
      if (document.visibilityState === "visible") await refresh()
      if (!stopped) timer = setTimeout(tick, POLL_MS)
    }
    void tick()
    const onVis = () => {
      if (document.visibilityState === "visible") void refresh()
    }
    document.addEventListener("visibilitychange", onVis)
    return () => {
      stopped = true
      clearTimeout(timer)
      document.removeEventListener("visibilitychange", onVis)
    }
  }, [refresh])

  const newestUser = React.useMemo(() => newestUserID(items), [items])
  React.useEffect(() => {
    setEchoes((prev) => reconcileEchoes(prev, newestUser))
  }, [newestUser])

  const running = Boolean(meta?.running)
  const hasMore = olderStart === null ? Boolean(meta?.more) : olderStart > 0

  const loadOlder = React.useCallback(async () => {
    const before = olderStart ?? meta?.start_offset
    if (!before || before <= 0 || loadingOlder) return
    const path = transcript.current
    setLoadingOlder(true)
    try {
      const page = await bridge.read(t, before)
      if (page.path !== path) return
      setItems((prev) => {
        const have = new Set(prev.map((it) => it.id))
        return [...(page.items ?? []).filter((it) => !have.has(it.id)), ...prev]
      })
      setOlderStart(page.start_offset)
    } catch {
      // Leave the reader where they were; asking again retries.
    } finally {
      setLoadingOlder(false)
    }
  }, [olderStart, meta?.start_offset, loadingOlder, t])

  // deliver types one message into the agent's pane. Only "confirmed" clears
  // it: "uncertain" may already have landed and must never be retried by
  // itself, so it pauses the queue and says so.
  const deliver = React.useCallback(
    async (text: string): Promise<boolean> => {
      setSending(true)
      setNotice(null)
      const after = newestUser
      try {
        const res = await bridge.send(t, text)
        if (res.outcome === "confirmed") {
          setEchoes((prev) => [...prev, { id: ++seq.current, text, after }])
          void refresh()
          return true
        }
        setNotice(
          res.outcome === "uncertain"
            ? `Not sure that went through: ${res.detail ?? "check the terminal"}. Nothing will be resent automatically.`
            : (res.detail ?? "The agent's pane did not take the message.")
        )
        return false
      } catch (e) {
        setNotice((e as Error).message)
        return false
      } finally {
        setSending(false)
      }
    },
    [newestUser, refresh, t]
  )

  // send is the composer's action. While the agent works (or messages are
  // already waiting) the message joins the queue, the way OpenMuse holds
  // follow-ups: they go out in order once the agent is idle.
  const send = React.useCallback(
    async (text: string): Promise<boolean> => {
      // Stop holds only what was already waiting: with nothing held, a new
      // message goes out as usual.
      if (queue.length === 0) setQueuePaused(false)
      if (running || queue.length > 0 || sending) {
        setQueue((q) => [...q, { id: ++seq.current, text }])
        return true
      }
      return deliver(text)
    },
    [running, queue.length, sending, deliver]
  )

  // Drain the queue one message at a time whenever the agent is idle.
  React.useEffect(() => {
    if (running || sending || queuePaused || queue.length === 0 || error) return
    const [head] = queue
    void deliver(head.text).then((ok) => {
      if (ok) setQueue((q) => q.filter((m) => m.id !== head.id))
      else setQueuePaused(true)
    })
  }, [running, sending, queuePaused, queue, error, deliver])

  const stop = React.useCallback(async () => {
    // Stopping holds what is queued, so the next message is a decision rather
    // than something that fires the moment the agent goes idle.
    setQueuePaused(true)
    try {
      const res = await bridge.stop(t)
      if (res.outcome !== "sent")
        setNotice(res.detail ?? "Could not stop the agent.")
      void refresh()
    } catch (e) {
      setNotice((e as Error).message)
    }
  }, [refresh, t])

  const answer = React.useCallback(
    (expect: string, labels: string[], picks: AskPick[]) =>
      bridge.answer(t, expect, labels, picks),
    [t]
  )

  const state: ChatState = {
    meta,
    items,
    error,
    loaded,
    running,
    hasMore,
    loadingOlder,
    echoes,
    queue,
    queuePaused,
    sending,
    notice,
  }
  return {
    state,
    send,
    stop,
    answer,
    loadOlder,
    removeQueued: (id: number) => setQueue((q) => q.filter((m) => m.id !== id)),
    resumeQueue: () => {
      setNotice(null)
      setQueuePaused(false)
    },
    dismissNotice: () => setNotice(null),
  }
}

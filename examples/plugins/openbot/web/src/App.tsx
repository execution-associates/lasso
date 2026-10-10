import { ArrowDown, ArrowUp, Square, X } from "lucide-react"
import * as React from "react"
import {
  AskCard,
  ChannelCard,
  channelMessage,
  cx,
  groupRows,
  MarkerRow,
  NoticeRow,
  type Row,
  ThinkingRow,
  ToolCard,
  ToolGroup,
  taskNotification,
} from "./Cards"
import { type ChatItem, type ChatTool, target } from "./lasso"
import { Markdown } from "./Markdown"
import { useChat } from "./useChat"

// OpenBot: OpenMuse's chat, in front of an agent lasso runs in herdr. One
// centred column; a header that says what the agent is doing; the
// conversation; follow-ups that wait their turn; a composer whose Send becomes
// Stop while the agent works.

const STARTERS = [
  "What's on my plate today?",
  "Anything urgent in my inbox?",
  "What did I commit to this week?",
]

// How far from the bottom still counts as "following" the conversation.
const FOLLOW_PX = 100

export function App() {
  const t = React.useMemo(target, [])
  const chat = useChat(t)
  const { state } = chat
  const name = displayName(t.agent)

  return (
    <div className="app">
      <Header name={name} status={statusLine(state)} />
      <Conversation chat={chat} name={name} />
    </div>
  )
}

function displayName(agent: string) {
  return agent.charAt(0).toUpperCase() + agent.slice(1)
}

// pendingAsk is the question the agent is stopped on, if any.
function pendingAsk(items: ChatItem[]): ChatTool | null {
  for (let i = items.length - 1; i >= 0; i--) {
    const tool = items[i].tool
    if (tool?.ask && tool.state === "running") return tool
  }
  return null
}

// statusLine names the current work or the input it needs, OpenMuse's rule for
// the header.
function statusLine(s: ReturnType<typeof useChat>["state"]): string {
  if (s.error) return "Unavailable"
  if (!s.loaded) return "Connecting…"
  const ask = pendingAsk(s.items)
  if (ask) {
    const q = ask.ask?.questions[0]
    return `Needs your input · ${q?.header || q?.question || ask.title}`
  }
  if (s.running) {
    for (let i = s.items.length - 1; i >= 0; i--) {
      const tool = s.items[i].tool
      if (tool?.state === "running") return `${tool.title}…`
      if (s.items[i].kind === "user") break
    }
    return "Working…"
  }
  if (s.queue.length > 0 && s.queuePaused) return "Messages on hold"
  return "Here when you need me"
}

function Header({ name, status }: { name: string; status: string }) {
  return (
    <header className="header">
      <div className="avatar" aria-hidden>
        {name.charAt(0)}
      </div>
      <div className="header-name">{name}</div>
      <div className="header-status" title={status}>
        {status}
      </div>
    </header>
  )
}

function Conversation({
  chat,
  name,
}: {
  chat: ReturnType<typeof useChat>
  name: string
}) {
  const { state } = chat
  const scroller = React.useRef<HTMLDivElement>(null)
  const follow = React.useRef(true)
  const [away, setAway] = React.useState(false)
  // The height before an older page lands above the reader, to hold their
  // place.
  const before = React.useRef<number | null>(null)

  const rows = React.useMemo(() => groupRows(state.items), [state.items])

  const toBottom = React.useCallback((smooth = false) => {
    const el = scroller.current
    if (!el) return
    el.scrollTo({ top: el.scrollHeight, behavior: smooth ? "smooth" : "auto" })
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: re-pin whenever the content changes
  React.useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    if (before.current !== null) {
      el.scrollTop += el.scrollHeight - before.current
      before.current = null
      return
    }
    if (follow.current) toBottom()
  }, [rows, state.echoes, state.queue, state.running, toBottom])

  // A first page that does not fill the screen can never be scrolled up to
  // ask for more, so keep loading until it does (or the history runs out).
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-measure whenever the rows change
  React.useEffect(() => {
    const el = scroller.current
    if (!el || !state.hasMore || state.loadingOlder) return
    if (el.scrollTop < 120) {
      before.current = el.scrollHeight
      void chat.loadOlder()
    }
  }, [rows, state.hasMore, state.loadingOlder, chat.loadOlder])

  const onScroll = () => {
    const el = scroller.current
    if (!el) return
    const gap = el.scrollHeight - el.scrollTop - el.clientHeight
    follow.current = gap < FOLLOW_PX
    setAway(gap >= FOLLOW_PX)
    if (el.scrollTop < 120 && state.hasMore && !state.loadingOlder) {
      before.current = el.scrollHeight
      void chat.loadOlder()
    }
  }

  const send = async (text: string) => {
    follow.current = true
    return chat.send(text)
  }

  const empty =
    state.loaded &&
    !state.error &&
    rows.length === 0 &&
    state.echoes.length === 0

  return (
    <>
      <div className="scroller" ref={scroller} onScroll={onScroll}>
        <div className="column messages">
          {state.loadingOlder && (
            <div className="muted small center">Loading earlier messages…</div>
          )}
          {state.error && (
            <div className="error-notice">
              Can't reach {name}: {state.error}
            </div>
          )}
          {empty && (
            <EmptyState note={state.meta?.note} onPick={(s) => void send(s)} />
          )}
          {rows.map((row) => (
            <RowView
              key={row.kind === "group" ? row.id : row.item.id}
              row={row}
              answer={chat.answer}
            />
          ))}
          {state.echoes.map((e) => (
            <div key={`echo-${e.id}`} className="bubble user pending">
              {e.text}
            </div>
          ))}
          {state.running && !pendingAsk(state.items) && <WorkingDots />}
        </div>
      </div>
      <div className="dock">
        <div className="column">
          {away && (
            <button
              type="button"
              className="latest"
              onClick={() => {
                follow.current = true
                toBottom(true)
              }}
            >
              <ArrowDown className="ico" /> Latest messages
            </button>
          )}
          {state.queue.length > 0 && (
            <QueuePanel
              queue={state.queue}
              paused={state.queuePaused}
              onRemove={chat.removeQueued}
              onResume={chat.resumeQueue}
            />
          )}
          {state.notice && (
            <div className="error-notice dismissable">
              <span>{state.notice}</span>
              <button
                type="button"
                className="icon-button"
                aria-label="Dismiss"
                onClick={chat.dismissNotice}
              >
                <X className="ico" />
              </button>
            </div>
          )}
          <Composer
            disabled={Boolean(state.error)}
            placeholder={
              state.error
                ? "Conversation unavailable"
                : state.loaded
                  ? "Message…"
                  : "Loading conversation…"
            }
            running={state.running}
            onSend={send}
            onStop={() => void chat.stop()}
          />
        </div>
      </div>
    </>
  )
}

function RowView({
  row,
  answer,
}: {
  row: Row
  answer: ReturnType<typeof useChat>["answer"]
}) {
  if (row.kind === "group") return <ToolGroup calls={row.calls} />
  const item = row.item
  switch (item.kind) {
    case "user": {
      const text = item.text ?? ""
      const note = taskNotification(text)
      if (note)
        return (
          <NoticeRow
            summary={note.summary}
            failed={
              note.status && note.status !== "completed"
                ? note.status
                : undefined
            }
            body={note.result}
          />
        )
      // Claude Code records a Stop as a user turn; it is the harness, not
      // something Stephan said.
      if (/^\[Request interrupted by user[^\]]*\]$/.test(text.trim()))
        return <div className="marker">Stopped</div>
      // A compacted session starts with the harness's summary of what came
      // before, as a user turn: fold it rather than show pages of it.
      if (text.startsWith("This session is being continued from a previous"))
        return (
          <NoticeRow summary="Earlier conversation summarized" body={text} />
        )
      const msg = channelMessage(text)
      if (msg) return <ChannelCard msg={msg} />
      return <div className="bubble user">{text}</div>
    }
    case "incoming": {
      const msg = channelMessage(item.text ?? "")
      return msg ? (
        <ChannelCard msg={msg} />
      ) : (
        <ChannelCard
          msg={{
            label: (item.source ?? "Message").replace(/-channel$/, ""),
            from: "",
            subject: "",
            body: item.text ?? "",
          }}
        />
      )
    }
    case "agent":
      return item.thinking ? (
        <ThinkingRow text={item.text ?? ""} />
      ) : (
        <div className="bubble agent">
          <Markdown source={item.text ?? ""} />
        </div>
      )
    case "tool":
      if (!item.tool) return null
      return item.tool.ask ? (
        <AskCard tool={item.tool} answer={answer} />
      ) : (
        <ToolCard tool={item.tool} />
      )
    case "marker":
      return <MarkerRow item={item} />
    default:
      return null
  }
}

function EmptyState({
  note,
  onPick,
}: {
  note?: string
  onPick: (text: string) => void
}) {
  return (
    <div className="empty">
      <div className="empty-tagline">
        A little help. A lot more room for life.
      </div>
      <div className="muted">{note ?? "Tell me what's on your mind."}</div>
      <div className="starters">
        {STARTERS.map((s) => (
          <button
            key={s}
            type="button"
            className="pill"
            onClick={() => onPick(s)}
          >
            {s}
          </button>
        ))}
      </div>
    </div>
  )
}

function WorkingDots() {
  return (
    <output className="working" aria-label="Agent is working">
      <span />
      <span />
      <span />
    </output>
  )
}

function QueuePanel({
  queue,
  paused,
  onRemove,
  onResume,
}: {
  queue: { id: number; text: string }[]
  paused: boolean
  onRemove: (id: number) => void
  onResume: () => void
}) {
  return (
    <div className="card queue">
      <div className="lasso-label">
        {paused ? "Messages on hold" : "Up next"} · Keep this view open until
        sent
      </div>
      {queue.map((m) => (
        <div key={m.id} className="queue-item">
          <span className="queue-text">{m.text}</span>
          <button
            type="button"
            className="icon-button"
            aria-label={`Remove queued message: ${m.text}`}
            onClick={() => onRemove(m.id)}
          >
            <X className="ico" />
          </button>
        </div>
      ))}
      {paused && (
        <button type="button" className="pill primary" onClick={onResume}>
          Send queued messages
        </button>
      )}
    </div>
  )
}

function Composer({
  disabled,
  placeholder,
  running,
  onSend,
  onStop,
}: {
  disabled: boolean
  placeholder: string
  running: boolean
  onSend: (text: string) => Promise<boolean>
  onStop: () => void
}) {
  const [draft, setDraft] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const box = React.useRef<HTMLTextAreaElement>(null)

  // Grow with the text, 44px to 140px, then scroll.
  // biome-ignore lint/correctness/useExhaustiveDependencies: measure on every draft change
  React.useLayoutEffect(() => {
    const el = box.current
    if (!el) return
    el.style.height = "auto"
    el.style.height = `${Math.min(140, Math.max(44, el.scrollHeight))}px`
  }, [draft])

  const submit = async () => {
    const text = draft.trim()
    if (!text || busy || disabled) return
    setBusy(true)
    try {
      // Cleared only once the message is either delivered or queued: a
      // refused or uncertain send keeps the draft.
      if (await onSend(text)) setDraft("")
    } finally {
      setBusy(false)
      box.current?.focus()
    }
  }

  const showStop = running && !draft.trim()
  // On a phone, Enter is a newline and the button sends.
  const coarse =
    typeof window !== "undefined" &&
    window.matchMedia?.("(pointer: coarse)").matches

  return (
    <div className={cx("composer", disabled && "is-disabled")}>
      <textarea
        ref={box}
        rows={1}
        value={draft}
        disabled={disabled}
        placeholder={placeholder}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (
            e.key === "Enter" &&
            !e.shiftKey &&
            !coarse &&
            !e.nativeEvent.isComposing
          ) {
            e.preventDefault()
            void submit()
          }
        }}
      />
      {showStop ? (
        <button
          type="button"
          className="send stop"
          aria-label="Stop reply"
          onClick={onStop}
        >
          <Square className="ico" fill="currentColor" />
        </button>
      ) : (
        <button
          type="button"
          className={cx("send", draft.trim() && "ready")}
          aria-label="Send message"
          disabled={!draft.trim() || busy || disabled}
          onClick={() => void submit()}
        >
          <ArrowUp className="ico" />
        </button>
      )}
    </div>
  )
}

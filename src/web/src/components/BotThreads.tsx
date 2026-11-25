import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowUp,
  FileIcon,
  MessageSquare,
  Paperclip,
  Plus,
  X,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { Markdown } from "@/components/Markdown"
import { Button } from "@/components/ui/button"
import { Orb } from "@/components/ui/orb"
import {
  api,
  type BotThread,
  type BotView,
  operatorFileURL,
  type ThreadAttachment,
  type ThreadMessage,
} from "@/lib/api"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// The Bots view's Threads page: your own threads with one bot, the same ones
// the phone app shows (docs/design/client-threads.md), read and written as the
// operator's app user. Polled, like the Inbox: lasso's SSE feed carries
// nothing of the phone app's.

function clientID(): string {
  return `web-${crypto.randomUUID().replaceAll("-", "").slice(0, 24)}`
}

function when(iso: string): string {
  const d = new Date(iso)
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  return sameDay
    ? d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
    : d.toLocaleDateString([], { month: "short", day: "numeric" })
}

function threadTitle(t: BotThread, bot: string): string {
  if (t.kind === "main") return `${bot} (main)`
  return t.title || "Untitled thread"
}

function systemLine(m: ThreadMessage): string {
  if (m.text) return m.text
  const s = m.system
  if (!s) return ""
  return [s.type.replaceAll("_", " "), s.bot, s.title].filter(Boolean).join(" ")
}

function Attachments({ items }: { items: ThreadAttachment[] }) {
  if (items.length === 0) return null
  return (
    <div className="mt-1.5 flex flex-wrap gap-1.5">
      {items.map((a) => {
        const url = operatorFileURL(a)
        if (!url)
          return (
            <span
              key={a.id}
              className="rounded-md border border-border px-2 py-1 text-[11.5px] text-muted-foreground line-through"
            >
              {a.name}
            </span>
          )
        return a.type.startsWith("image/") ? (
          <a key={a.id} href={url} target="_blank" rel="noreferrer">
            <img
              src={url}
              alt={a.name}
              className="max-h-56 max-w-full rounded-md border border-border"
            />
          </a>
        ) : (
          <a
            key={a.id}
            href={url}
            target="_blank"
            rel="noreferrer"
            className="flex items-center gap-1.5 rounded-md border border-border px-2 py-1 text-[12px] text-foreground hover:bg-accent"
          >
            <FileIcon className="size-3.5 text-muted-foreground" />
            {a.name}
          </a>
        )
      })}
    </div>
  )
}

function MessageRow({ m }: { m: ThreadMessage }) {
  if (m.role === "system")
    return (
      <div className="py-1 text-center text-[11.5px] text-muted-foreground">
        {systemLine(m)}
      </div>
    )
  const mine = m.role === "user"
  return (
    <div className={cn("flex flex-col", mine ? "items-end" : "items-start")}>
      <div
        className={cn(
          "min-w-0 max-w-[min(44rem,88%)] rounded-xl px-3 py-2 text-[13px]",
          mine
            ? "whitespace-pre-wrap break-words bg-primary/15 text-foreground"
            : "border border-border bg-card text-foreground"
        )}
      >
        {mine ? (
          m.text
        ) : m.text ? (
          <Markdown source={m.text} />
        ) : m.done ? null : (
          <Orb state="working" px={14} />
        )}
        <Attachments items={m.attachments} />
      </div>
      <div className="mt-0.5 px-1 text-[10.5px] text-muted-foreground">
        {!mine && m.bot ? `${m.bot} · ` : ""}
        {when(m.created_at)}
        {mine && m.state === "queued" && " · waiting for the bot"}
        {mine && m.state === "failed" && (
          <span className="text-destructive">
            {" "}
            · not delivered{m.failed_reason ? `: ${m.failed_reason}` : ""}
          </span>
        )}
      </div>
    </div>
  )
}

function Composer({
  placeholder,
  onSend,
  upload,
}: {
  placeholder: string
  onSend: (text: string, attachments: string[]) => Promise<void>
  // Absent where there is no thread to hold a file yet (a new thread).
  upload?: (f: File) => Promise<ThreadAttachment>
}) {
  const [text, setText] = React.useState("")
  const [files, setFiles] = React.useState<ThreadAttachment[]>([])
  const [busy, setBusy] = React.useState(false)
  const pick = React.useRef<HTMLInputElement>(null)

  const add = async (list: FileList | File[]) => {
    if (!upload) return
    setBusy(true)
    try {
      for (const f of Array.from(list)) {
        const a = await upload(f)
        setFiles((cur) => [...cur, a])
      }
    } catch (e) {
      toast.error(`could not attach: ${(e as Error).message}`)
    }
    setBusy(false)
  }
  const send = async () => {
    const body = text.trim()
    if (busy || (!body && files.length === 0)) return
    setBusy(true)
    try {
      await onSend(
        body,
        files.map((f) => f.id)
      )
      setText("")
      setFiles([])
    } catch (e) {
      toast.error(`not sent: ${(e as Error).message}`)
    }
    setBusy(false)
  }

  return (
    <form
      className="flex-none border-border border-t px-3 py-2"
      onSubmit={(e) => {
        e.preventDefault()
        void send()
      }}
      onDragOver={(e) => upload && e.preventDefault()}
      onDrop={(e) => {
        if (!upload || e.dataTransfer.files.length === 0) return
        e.preventDefault()
        void add(e.dataTransfer.files)
      }}
    >
      {files.length > 0 && (
        <div className="mb-1.5 flex flex-wrap gap-1.5">
          {files.map((f) => (
            <span
              key={f.id}
              className="flex items-center gap-1 rounded-md border border-border px-2 py-0.5 text-[11.5px] text-foreground"
            >
              {f.name}
              <button
                type="button"
                aria-label={`Remove ${f.name}`}
                onClick={() =>
                  setFiles((cur) => cur.filter((x) => x.id !== f.id))
                }
                className="text-muted-foreground hover:text-foreground"
              >
                <X className="size-3" />
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="flex items-end gap-1.5">
        {upload && (
          <>
            <input
              ref={pick}
              type="file"
              multiple
              className="hidden"
              onChange={(e) => {
                if (e.target.files) void add(e.target.files)
                e.target.value = ""
              }}
            />
            <button
              type="button"
              title="Attach a file"
              aria-label="Attach a file"
              onClick={() => pick.current?.click()}
              className="flex size-8 flex-none items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <Paperclip className="size-4" />
            </button>
          </>
        )}
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (
              e.key === "Enter" &&
              !e.shiftKey &&
              !e.nativeEvent.isComposing
            ) {
              e.preventDefault()
              void send()
            }
          }}
          rows={Math.min(8, Math.max(1, text.split("\n").length))}
          placeholder={placeholder}
          className="min-h-8 flex-1 resize-none rounded-lg border border-border bg-background px-2.5 py-1.5 text-[13px] text-foreground outline-none placeholder:text-muted-foreground focus:border-ring"
        />
        <Button
          type="submit"
          size="icon"
          disabled={busy || (!text.trim() && files.length === 0)}
          aria-label="Send"
          className="size-8 flex-none"
        >
          {busy ? <Orb state="working" px={14} on="accent" /> : <ArrowUp />}
        </Button>
      </div>
    </form>
  )
}

function ThreadPane({
  thread,
  bot,
  active,
}: {
  thread: BotThread
  bot: string
  active: boolean
}) {
  const qc = useQueryClient()
  const { data, error } = useQuery({
    queryKey: qk.threadMessages(thread.id),
    queryFn: () => api.threads.messages(thread.id),
    refetchInterval: active ? 2_000 : false,
  })
  // Pages before the polled newest one, loaded on request.
  const [older, setOlder] = React.useState<{
    messages: ThreadMessage[]
    cursor: string | null
  } | null>(null)
  const newest = data?.messages ?? []
  const first = newest[0]?.cursor
  const earlier = (older?.messages ?? []).filter(
    (m) => !first || Number(m.cursor) < Number(first)
  )
  const messages = [...earlier, ...newest]
  const olderCursor = older ? older.cursor : (data?.older_cursor ?? null)
  const loadOlder = async () => {
    const before = messages[0]?.cursor
    if (!before) return
    try {
      const page = await api.threads.older(thread.id, before)
      setOlder((cur) => ({
        messages: [...page.messages, ...(cur?.messages ?? [])],
        cursor: page.older_cursor,
      }))
    } catch (e) {
      toast.error((e as Error).message)
    }
  }

  // Stay at the bottom while already there; mark what is on screen as read.
  const box = React.useRef<HTMLDivElement>(null)
  const pinned = React.useRef(true)
  const last = newest[newest.length - 1]
  const lastKey = last ? `${last.cursor}:${last.text.length}` : ""
  // biome-ignore lint/correctness/useExhaustiveDependencies: lastKey is the trigger
  React.useLayoutEffect(() => {
    const el = box.current
    if (el && pinned.current) el.scrollTop = el.scrollHeight
  }, [lastKey])
  const lastCursor = last?.cursor
  React.useEffect(() => {
    if (!active || !lastCursor || thread.unread === 0) return
    api.threads
      .read(thread.id, lastCursor)
      .then(() => qc.invalidateQueries({ queryKey: qk.botThreads(bot) }))
      .catch(() => {})
  }, [active, lastCursor, thread.id, thread.unread, bot, qc])

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div
        ref={box}
        onScroll={(e) => {
          const el = e.currentTarget
          pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
        }}
        className="flex min-h-0 flex-1 flex-col gap-2.5 overflow-y-auto px-3 py-3"
      >
        {olderCursor && (
          <button
            type="button"
            onClick={() => void loadOlder()}
            className="self-center text-[11.5px] text-muted-foreground hover:text-foreground"
          >
            Load earlier messages
          </button>
        )}
        {error ? (
          <p className="m-auto text-[12.5px] text-destructive">
            {(error as Error).message}
          </p>
        ) : !data ? (
          <div className="m-auto">
            <Orb state="working" px={18} />
          </div>
        ) : messages.length === 0 ? (
          <p className="m-auto text-[12.5px] text-muted-foreground">
            Nothing here yet. Say something to {bot}.
          </p>
        ) : (
          messages.map((m) => <MessageRow key={m.id} m={m} />)
        )}
      </div>
      <Composer
        placeholder={`Message ${bot}…`}
        upload={(f) => api.threads.upload(thread.id, f)}
        onSend={async (text, attachments) => {
          await api.threads.send(thread.id, {
            text,
            attachments,
            client_id: clientID(),
          })
          pinned.current = true
          await qc.invalidateQueries({
            queryKey: qk.threadMessages(thread.id),
          })
        }}
      />
    </div>
  )
}

export function BotThreads({
  bot,
  threadID,
  active,
  compact,
  lead,
  extra,
  onPick,
  onChat,
  onPeople,
}: {
  bot: BotView
  threadID?: string
  active: boolean
  // The view is narrow: the list and a thread take turns.
  compact: boolean
  lead: React.ReactNode
  extra?: React.ReactNode
  onPick: (thread?: string) => void
  onChat: () => void
  onPeople: () => void
}) {
  const qc = useQueryClient()
  const { data, error, isPending } = useQuery({
    queryKey: qk.botThreads(bot.name),
    queryFn: () => api.threads.list(bot.name),
    refetchInterval: active ? 5_000 : false,
  })
  const threads = data?.threads ?? []
  const [composing, setComposing] = React.useState(false)
  const main = threads.find((t) => t.kind === "main")
  // With room for both, a thread is always open: the main one by default.
  const openID = composing
    ? undefined
    : (threadID ?? (compact ? undefined : main?.id))
  const open = threads.find((t) => t.id === openID)
  const showList = !compact || (!open && !composing)

  const header = (
    <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
      {compact && (open || composing) ? (
        <button
          type="button"
          aria-label="All threads"
          title="All threads"
          onClick={() => {
            setComposing(false)
            onPick(undefined)
          }}
          className="flex size-7 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <ArrowLeft className="size-4" />
        </button>
      ) : (
        lead
      )}
      <span className="min-w-0 truncate font-medium text-[12.5px] text-foreground">
        {open ? threadTitle(open, bot.name) : `${bot.name} · Threads`}
      </span>
      <span className="ml-auto" />
      <Button
        variant="ghost"
        size="sm"
        className="h-7 px-2 text-[12px] text-muted-foreground"
        onClick={onChat}
        title="The bot's own session"
      >
        Session
      </Button>
      {extra}
    </div>
  )

  let body: React.ReactNode
  if (error) {
    body = (
      <p className="m-auto max-w-sm px-4 text-center text-[12.5px] text-muted-foreground">
        {(error as Error).message}
      </p>
    )
  } else if (isPending) {
    body = (
      <div className="m-auto">
        <Orb state="working" px={20} />
      </div>
    )
  } else if (threads.length === 0) {
    body = (
      <div className="m-auto flex max-w-sm flex-col items-center gap-3 px-4 text-center text-[12.5px] text-muted-foreground">
        <p>
          You have no thread with {bot.name}. Threads are the conversations of
          the phone app: give yourself access to this bot under People and it
          appears here.
        </p>
        <Button variant="outline" size="sm" onClick={onPeople}>
          People
        </Button>
      </div>
    )
  } else {
    body = (
      <div className="flex min-h-0 flex-1">
        {showList && (
          <div
            className={cn(
              "flex min-h-0 flex-none flex-col border-border",
              compact ? "w-full" : "w-60 border-r"
            )}
          >
            <button
              type="button"
              onClick={() => setComposing(true)}
              className="flex flex-none items-center gap-1.5 border-border border-b px-3 py-2 text-left text-[12.5px] text-muted-foreground hover:bg-accent hover:text-foreground"
            >
              <Plus className="size-3.5" />
              New thread
            </button>
            <div className="min-h-0 flex-1 overflow-y-auto">
              {threads.map((t) => (
                <button
                  key={t.id}
                  type="button"
                  onClick={() => {
                    setComposing(false)
                    onPick(t.id)
                  }}
                  aria-current={t.id === openID ? "true" : undefined}
                  className={cn(
                    "flex w-full flex-col gap-0.5 border-border/50 border-b px-3 py-2 text-left hover:bg-accent",
                    t.id === openID && "bg-accent"
                  )}
                >
                  <span className="flex items-center gap-1.5">
                    <MessageSquare className="size-3.5 flex-none text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate font-medium text-[12.5px] text-foreground">
                      {threadTitle(t, bot.name)}
                    </span>
                    {t.unread > 0 && (
                      <span className="rounded-full bg-primary px-1.5 text-[10.5px] text-primary-foreground">
                        {t.unread}
                      </span>
                    )}
                    <span className="flex-none text-[10.5px] text-muted-foreground">
                      {when(t.last_activity_at)}
                    </span>
                  </span>
                  {t.last_message && (
                    <span className="truncate text-[11.5px] text-muted-foreground">
                      {t.last_message.role === "user" ? "You: " : ""}
                      {t.last_message.preview}
                    </span>
                  )}
                </button>
              ))}
            </div>
          </div>
        )}
        {composing ? (
          <div className="flex min-h-0 min-w-0 flex-1 flex-col">
            <p className="m-auto max-w-sm px-4 text-center text-[12.5px] text-muted-foreground">
              A new thread with {bot.name}, beside your main one. Its first
              message starts it.
            </p>
            <Composer
              placeholder={`Start a thread with ${bot.name}…`}
              onSend={async (text) => {
                const { thread } = await api.threads.create(
                  bot.name,
                  text,
                  clientID()
                )
                await qc.invalidateQueries({
                  queryKey: qk.botThreads(bot.name),
                })
                setComposing(false)
                onPick(thread.id)
              }}
            />
          </div>
        ) : open ? (
          <ThreadPane
            key={open.id}
            thread={open}
            bot={bot.name}
            active={active}
          />
        ) : (
          !compact && (
            <p className="m-auto text-[12.5px] text-muted-foreground">
              {threadID ? "That thread is not here." : "Pick a thread."}
            </p>
          )
        )}
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      {header}
      {body}
    </div>
  )
}

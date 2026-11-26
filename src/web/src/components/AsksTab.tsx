import { useMutation, useQueryClient } from "@tanstack/react-query"
import { ChevronDown, ChevronRight, ExternalLink } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { type Ask, type AskInbox, api } from "@/lib/api"
import { noteDropped, useAsks } from "@/lib/asks"
import { qk } from "@/lib/query"
import { openBotsRoute } from "@/lib/url"
import { cn } from "@/lib/utils"

// The Inbox tab: what bots are waiting on this person to decide
// (docs/design/client-asks.md). Every string a bot wrote is rendered as plain
// text, never as markdown or HTML.

function age(iso: string, now: number): string {
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 60) return "now"
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

function due(iso: string, now: number): { text: string; late: boolean } {
  const ms = new Date(iso).getTime() - now
  if (ms <= 0) return { text: "past due", late: true }
  const m = Math.round(ms / 60000)
  if (m < 60) return { text: `due in ${m}m`, late: false }
  if (m < 1440) return { text: `due in ${Math.round(m / 60)}h`, late: false }
  return { text: `due in ${Math.round(m / 1440)}d`, late: false }
}

const BLOCKING: Record<Ask["blocking"], string> = {
  money: "blocks money",
  people: "someone is waiting",
  deadline: "has a deadline",
  none: "",
}

const CLOSED: Record<string, string> = {
  answered: "Answered",
  dropped: "Dropped",
  expired: "Expired",
  withdrawn: "Withdrawn",
}

// Later: in an hour, this evening's end, or tomorrow morning.
function laterChoices(): { label: string; until: string }[] {
  const now = new Date()
  const hour = new Date(now.getTime() + 3_600_000)
  const morning = new Date(now)
  morning.setDate(morning.getDate() + 1)
  morning.setHours(8, 0, 0, 0)
  const week = new Date(now.getTime() + 7 * 86_400_000)
  return [
    { label: "In an hour", until: hour.toISOString() },
    { label: "Tomorrow 8am", until: morning.toISOString() },
    { label: "In a week", until: week.toISOString() },
  ]
}

// AskCard is one ask and its answer form: a row of the sidebar's list, or
// (large) a slide of the Inbox dialog. `current` says it is the one being
// read, which is what marks it seen and, with `hotkeys`, what the dialog's
// number keys and Enter act on.
export function AskCard({
  ask,
  now,
  large,
  current = true,
  hotkeys,
  inThread,
  onLeave,
}: {
  ask: Ask
  now: number
  large?: boolean
  current?: boolean
  hotkeys?: boolean
  // Drawn inside the ask's own thread: no way to open the thread it is in.
  inThread?: boolean
  // Called when the card sends the reader elsewhere (its thread), so a
  // dialog holding it can close.
  onLeave?: () => void
}) {
  const queryClient = useQueryClient()
  const open = ask.state === "new" || ask.state === "seen"
  const fallback = ask.options.find((o) => o.default)?.id ?? ""
  const [choice, setChoice] = React.useState(fallback)
  // One radio group per card: the same ask can be on screen twice (the Inbox
  // and its thread), and a shared name would let one uncheck the other.
  const group = `ask-${React.useId()}`
  const [other, setOther] = React.useState("")
  const [preview, setPreview] = React.useState(!!large || !!inThread)
  const [later, setLater] = React.useState(false)
  const done = (p: Promise<{ ask: Ask }>) =>
    p.then(({ ask: next }) => {
      queryClient.setQueryData<AskInbox>(qk.asks, (old) =>
        old
          ? { ...old, asks: old.asks.map((a) => (a.id === next.id ? next : a)) }
          : old
      )
      if (next.discussion_thread_id)
        queryClient.invalidateQueries({
          queryKey: qk.threadMessages(next.discussion_thread_id),
        })
      return queryClient.invalidateQueries({ queryKey: qk.asks })
    })
  const act = useMutation({
    mutationFn: (run: () => Promise<{ ask: Ask }>) => done(run()),
    onError: (e) => {
      toast.error((e as Error).message)
      queryClient.invalidateQueries({ queryKey: qk.asks })
    },
  })
  // A card read on screen is seen: it stops reading as new elsewhere.
  const sawRef = React.useRef(false)
  React.useEffect(() => {
    if (ask.state !== "new" || sawRef.current || !current) return
    sawRef.current = true
    api.asks.seen(ask.id).catch(() => {})
  }, [ask.id, ask.state, current])
  const otherRef = React.useRef<HTMLInputElement>(null)
  // The ask's own thread, not the main one it hangs off (which holds none of
  // its words): lasso makes it, started with the ask, the first time.
  const [opening, setOpening] = React.useState(false)
  const openThread = async () => {
    if (opening) return
    setOpening(true)
    try {
      const { thread } = await api.asks.thread(ask.id)
      await queryClient.invalidateQueries({ queryKey: qk.botThreads(ask.bot) })
      queryClient.invalidateQueries({ queryKey: qk.asks })
      openBotsRoute({ page: "threads", name: ask.bot, thread: thread.id })
      onLeave?.()
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setOpening(false)
    }
  }
  // An ask for files is answered by a message carrying them, in its thread.
  const filesRef = React.useRef<HTMLInputElement>(null)
  const sendFiles = (list: FileList | File[]) => {
    const files = Array.from(list)
    if (files.length === 0) return
    act.mutate(async () => {
      const ids: string[] = []
      for (const f of files)
        ids.push((await api.threads.upload(ask.thread_id, f)).id)
      const { message } = await api.threads.send(ask.thread_id, {
        text: "",
        attachments: ids,
        client_id: `web-ask-${crypto.randomUUID().replaceAll("-", "").slice(0, 20)}`,
      })
      return api.asks.answer(ask.id, { message_id: message.id })
    })
  }

  const usingOther = choice === "\u0000other"
  const canAnswer = usingOther ? other.trim() !== "" : choice !== ""
  const answer = () => {
    if (!canAnswer) return
    act.mutate(() =>
      api.asks.answer(ask.id, usingOther ? { other: other.trim() } : { choice })
    )
  }
  // The dialog's keys: a digit picks that option (the one after the last is
  // the free-text row), Enter answers. Never while a field has the keyboard.
  const answerRef = React.useRef(answer)
  answerRef.current = answer
  React.useEffect(() => {
    if (!hotkeys || !current || !open) return
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA")) return
      if (e.metaKey || e.ctrlKey || e.altKey) return
      const n = Number.parseInt(e.key, 10)
      if (n >= 1 && n <= ask.options.length) {
        e.preventDefault()
        setChoice(ask.options[n - 1].id)
      } else if (n === ask.options.length + 1 && ask.allow_other) {
        e.preventDefault()
        setChoice("\u0000other")
        otherRef.current?.focus()
      } else if (e.key === "Enter") {
        e.preventDefault()
        answerRef.current()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [hotkeys, current, open, ask.options, ask.allow_other])
  const d = ask.deadline && open ? due(ask.deadline, now) : null
  // An agent's question (the lasso-asks plugin) has no bot and no thread: it
  // names the agent, and its answer is typed into that agent's terminal.
  const agent = ask.agent
  const head = [
    agent ? agent.name || "an agent" : ask.bot,
    ask.area,
    ask.subject,
    agent && agent.of > 1 ? `question ${agent.n} of ${agent.of}` : "",
  ]
    .filter(Boolean)
    .join(" · ")
  return (
    <div
      className={cn(
        "flex flex-col gap-2 rounded-md border bg-card p-3 text-sm",
        large && "gap-3 rounded-xl border-0 bg-transparent p-5 text-base",
        !open && !large && "opacity-70"
      )}
    >
      <div className="flex items-baseline gap-2 text-muted-foreground text-xs">
        <span className="min-w-0 truncate">{head}</span>
        <span
          className={cn("ml-auto shrink-0 tabular-nums", d?.late && "text-bad")}
        >
          {d ? d.text : age(ask.created_at, now)}
        </span>
      </div>
      <div
        className={cn("whitespace-pre-wrap font-medium", large && "text-xl")}
      >
        {ask.question}
      </div>
      {ask.why && (
        <div
          className={cn(
            "whitespace-pre-wrap text-muted-foreground text-xs",
            large && "text-sm"
          )}
        >
          {ask.why}
        </div>
      )}
      {open && BLOCKING[ask.blocking] && (
        <div className="text-warn text-xs">{BLOCKING[ask.blocking]}</div>
      )}
      {ask.attachment && (
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-1 text-xs">
            {ask.attachment.preview ? (
              <button
                type="button"
                className="flex items-center gap-1 text-muted-foreground hover:text-foreground"
                onClick={() => setPreview((p) => !p)}
              >
                {preview ? (
                  <ChevronDown className="size-3" />
                ) : (
                  <ChevronRight className="size-3" />
                )}
                {ask.attachment.label || ask.attachment.kind}
              </button>
            ) : (
              <span className="text-muted-foreground">
                {ask.attachment.label || ask.attachment.kind}
              </span>
            )}
            {ask.attachment.url?.startsWith("https://") && (
              <a
                href={ask.attachment.url}
                target="_blank"
                rel="noreferrer noopener"
                className="ml-1 flex items-center gap-0.5 text-muted-foreground underline hover:text-foreground"
              >
                open <ExternalLink className="size-3" />
              </a>
            )}
          </div>
          {preview && ask.attachment.preview && (
            <pre className="max-h-60 overflow-auto whitespace-pre-wrap rounded border bg-muted/40 p-2 font-sans text-xs">
              {ask.attachment.preview}
            </pre>
          )}
          {ask.attachment.kind === "upload" && open && (
            // biome-ignore lint/a11y/noStaticElementInteractions: a drop target beside its button
            <div
              className="flex flex-wrap items-center gap-2 rounded border border-dashed p-2 text-muted-foreground text-xs"
              onDragOver={(e) => e.preventDefault()}
              onDrop={(e) => {
                e.preventDefault()
                sendFiles(e.dataTransfer.files)
              }}
            >
              <input
                ref={filesRef}
                type="file"
                multiple
                className="hidden"
                onChange={(e) => {
                  if (e.target.files) sendFiles(e.target.files)
                  e.target.value = ""
                }}
              />
              <span>Answered with files: drop them here, or</span>
              <Button
                size="sm"
                variant="outline"
                disabled={act.isPending}
                onClick={() => filesRef.current?.click()}
              >
                {act.isPending ? "Sending…" : "Choose files"}
              </Button>
            </div>
          )}
        </div>
      )}

      {open ? (
        <>
          <div className="flex flex-col gap-1">
            {ask.options.map((o) => (
              <label
                key={o.id}
                className="flex cursor-pointer items-center gap-2 rounded px-1 py-0.5 hover:bg-muted/50"
              >
                <input
                  type="radio"
                  name={group}
                  checked={choice === o.id}
                  onChange={() => setChoice(o.id)}
                />
                {hotkeys && (
                  <span className="w-3 font-label text-muted-foreground text-xs tabular-nums">
                    {ask.options.indexOf(o) + 1}
                  </span>
                )}
                <span className="flex min-w-0 flex-col">
                  <span>{o.label}</span>
                  {o.detail && (
                    <span className="text-muted-foreground text-xs">
                      {o.detail}
                    </span>
                  )}
                </span>
                {o.default && (
                  <span className="text-muted-foreground text-xs">
                    recommended
                  </span>
                )}
              </label>
            ))}
            {ask.allow_other && (
              <label className="flex items-center gap-2 rounded px-1 py-0.5">
                <input
                  type="radio"
                  name={group}
                  checked={usingOther}
                  onChange={() => setChoice("\u0000other")}
                />
                <Input
                  ref={otherRef}
                  value={other}
                  placeholder="Answer in your own words"
                  className="h-7 text-sm"
                  onFocus={() => setChoice("\u0000other")}
                  onChange={(e) => setOther(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      e.preventDefault()
                      answer()
                    }
                  }}
                />
              </label>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              size="sm"
              disabled={!canAnswer || act.isPending}
              onClick={answer}
            >
              {hotkeys ? "Answer & next" : "Answer"}
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={act.isPending}
              onClick={() => setLater((l) => !l)}
            >
              Later
            </Button>
            <Button
              size="sm"
              variant="ghost"
              disabled={act.isPending}
              title="You will not answer this. ⌘Z brings it back for a minute."
              onClick={() =>
                act.mutate(async () => {
                  const res = await api.asks.drop(ask.id)
                  noteDropped(res.ask)
                  return res
                })
              }
            >
              Drop
            </Button>
            {!agent && !inThread && (
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto text-muted-foreground"
                title={`Talk this over with ${ask.bot}`}
                disabled={opening}
                onClick={() => void openThread()}
              >
                Open thread
              </Button>
            )}
          </div>
          {later && (
            <div className="flex flex-wrap gap-1">
              {laterChoices().map((c) => (
                <Button
                  key={c.label}
                  size="sm"
                  variant="outline"
                  disabled={act.isPending}
                  onClick={() =>
                    act.mutate(() => api.asks.snooze(ask.id, c.until))
                  }
                >
                  {c.label}
                </Button>
              ))}
            </div>
          )}
          {ask.on_expire && ask.deadline && (
            <div className="text-muted-foreground text-xs">
              If nobody answers: {ask.on_expire}
            </div>
          )}
        </>
      ) : ask.state === "snoozed" ? (
        <div className="flex items-center gap-2 text-muted-foreground text-xs">
          <span>
            Back{" "}
            {ask.snoozed_until
              ? new Date(ask.snoozed_until).toLocaleString([], {
                  dateStyle: "medium",
                  timeStyle: "short",
                })
              : "later"}
          </span>
          <Button
            size="sm"
            variant="ghost"
            disabled={act.isPending}
            onClick={() => act.mutate(() => api.asks.snooze(ask.id, null))}
          >
            Bring back
          </Button>
        </div>
      ) : (
        <div className="text-muted-foreground text-xs">
          {CLOSED[ask.state] ?? ask.state}
          {ask.answer &&
            `: ${ask.answer.label || ask.answer.other || "with a message"}`}
          {ask.closed_at && ` · ${age(ask.closed_at, now)} ago`}
          {ask.resolved_at && (
            <div className="whitespace-pre-wrap text-foreground">
              Resolved{ask.resolution ? `: ${ask.resolution}` : "."}
            </div>
          )}
          {agent && ask.state === "answered" && (
            <div className={cn(agent.delivery !== "sent" && "text-warn")}>
              {agent.delivery === "sent"
                ? "Typed into the agent's terminal."
                : agent.delivery === "waiting" || !agent.delivery
                  ? "Held until this agent's other questions are answered."
                  : `Not typed into the terminal: ${agent.delivery}`}
            </div>
          )}
          {agent && ask.state === "withdrawn" && (
            <div>Settled in the agent's terminal.</div>
          )}
        </div>
      )}
    </div>
  )
}

function Section({
  title,
  asks,
  now,
  folded,
}: {
  title: string
  asks: Ask[]
  now: number
  folded?: boolean
}) {
  const [open, setOpen] = React.useState(!folded)
  if (asks.length === 0) return null
  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        className="flex items-center gap-1 text-left font-label text-[11px] text-muted-foreground uppercase tracking-wide"
        onClick={() => setOpen((o) => !o)}
      >
        {open ? (
          <ChevronDown className="size-3" />
        ) : (
          <ChevronRight className="size-3" />
        )}
        {title} · {asks.length}
      </button>
      {open && asks.map((a) => <AskCard key={a.id} ask={a} now={now} />)}
    </div>
  )
}

export function AsksTab({ active }: { active: boolean }) {
  const queryClient = useQueryClient()
  const inbox = useAsks(active)
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const t = window.setInterval(() => setNow(Date.now()), 30_000)
    return () => window.clearInterval(t)
  }, [active])
  const data = inbox.data
  const asks = data?.asks ?? []
  const waiting = asks.filter((a) => a.state === "new" || a.state === "seen")
  const snoozed = asks.filter((a) => a.state === "snoozed")
  const closed = asks.filter(
    (a) => !["new", "seen", "snoozed"].includes(a.state)
  )
  // The list names its person only when there is none to act as (`operator:
  // null`, with the reason and who could be chosen).
  const noOperator = data?.operator === null
  const choose = (id: string) =>
    api.asks
      .setOperator(id)
      .then(() => queryClient.invalidateQueries({ queryKey: qk.asks }))
      .catch((e) => toast.error((e as Error).message))
  return (
    <div className="flex h-full flex-col gap-3 overflow-y-auto p-3">
      <div className="flex items-center gap-2">
        <span className="font-medium text-sm">Inbox</span>
        <span className="ml-auto text-muted-foreground text-xs">
          {inbox.isError
            ? "couldn't reach lasso"
            : data
              ? noOperator
                ? ""
                : `${data.counts.waiting} waiting on you`
              : "loading…"}
        </span>
      </div>
      {data && noOperator && (
        <div className="flex flex-col gap-2 rounded-md border p-3 text-sm">
          <p className="text-muted-foreground text-xs">{data.reason}</p>
          {(data.candidates ?? []).map((c) => (
            <Button
              key={c.id}
              size="sm"
              variant="outline"
              className="justify-start"
              onClick={() => choose(c.id)}
            >
              This Inbox is {c.name}'s
            </Button>
          ))}
        </div>
      )}
      {data && !noOperator && waiting.length === 0 && (
        <p className="text-muted-foreground text-xs">
          Nothing is waiting on you. When a bot is blocked on a decision of
          yours, it shows up here.
        </p>
      )}
      <Section title="Asks you" asks={waiting} now={now} />
      <Section title="Later" asks={snoozed} now={now} folded />
      <Section title="Finished" asks={closed} now={now} folded />
    </div>
  )
}

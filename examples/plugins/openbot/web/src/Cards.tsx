import {
  AlertTriangle,
  Check,
  ChevronRight,
  Globe,
  Image as ImageIcon,
  ListTodo,
  Loader2,
  Mail,
  Pencil,
  Search,
  SquareTerminal,
  Users,
  Wrench,
  X,
} from "lucide-react"
import * as React from "react"
import type { AskPick, ChatAskQuestion, ChatItem, ChatTool } from "./lasso"
import { Markdown } from "./Markdown"

// The cards a conversation is made of, ported from lasso's ChatView.tsx (a
// plugin page cannot import lasso's components). The behaviour is lasso's:
// grouping by the server's key, an ask answered by option INDEX, colour kept
// for state. The look is OpenMuse's: rounded, airy cards on lasso's tokens.

export function cx(...parts: (string | false | null | undefined)[]) {
  return parts.filter(Boolean).join(" ")
}

export type Row =
  | { kind: "single"; item: ChatItem }
  | { kind: "group"; id: string; calls: ChatTool[] }

// groupRows collapses consecutive calls sharing the server's group key (a burst
// of reads) into one card.
export function groupRows(items: ChatItem[]): Row[] {
  const rows: Row[] = []
  let run: ChatTool[] = []
  let key: string | null = null
  const flush = () => {
    if (run.length === 1) {
      rows.push({
        kind: "single",
        item: { kind: "tool", id: run[0].call_id, tool: run[0] },
      })
    } else if (run.length > 1) {
      rows.push({ kind: "group", id: run[0].call_id, calls: run })
    }
    run = []
    key = null
  }
  for (const item of items) {
    const g = item.kind === "tool" && !item.tool?.ask ? item.tool?.group : null
    if (item.tool && g) {
      if (key !== null && g !== key) flush()
      key = g
      run.push(item.tool)
      continue
    }
    flush()
    rows.push({ kind: "single", item })
  }
  flush()
  return rows
}

const FAMILY_ICON: Record<string, typeof Wrench> = {
  shell: SquareTerminal,
  eval: SquareTerminal,
  read: Pencil,
  write: Pencil,
  edit: Pencil,
  search: Search,
  web: Globe,
  todo: ListTodo,
  task: Users,
  image: ImageIcon,
  generic: Wrench,
}

// An assistant's MCP tools read better as their service and action than as
// the raw name: mcp__triage-channel__triage_mark_handled is "Triage · mark
// handled", mcp__claude_ai_Google_Calendar__list_events "Google Calendar ·
// list events".
function friendlyTitle(tool: ChatTool): { title: string; mail: boolean } {
  const m = tool.name.match(/^mcp__(?:claude_ai_)?(.+?)__(.+)$/)
  if (!m) return { title: tool.title, mail: false }
  const server = m[1].split("-")[0].replace(/_/g, " ")
  const first = server.split(" ")[0].toLowerCase()
  let action = m[2]
  if (action.toLowerCase().startsWith(`${first}_`))
    action = action.slice(first.length + 1)
  return {
    title: `${server.charAt(0).toUpperCase()}${server.slice(1)} · ${action.replace(/[_-]+/g, " ")}`,
    mail: /mail|gmail|email/i.test(server),
  }
}

function StateMark({ state }: { state: ChatTool["state"] }) {
  if (state === "running")
    return <Loader2 className="ico spin state-running" aria-label="Working" />
  if (state === "error")
    return <X className="ico state-error" aria-label="Failed" />
  return <Check className="ico state-done" aria-label="Done" />
}

function Duration({ ms }: { ms?: number }) {
  if (!ms) return null
  const s = Math.round(ms / 1000)
  const text =
    ms < 1000
      ? `${ms}ms`
      : s < 60
        ? `${s}s`
        : `${Math.floor(s / 60)}m ${s % 60}s`
  return <span className="muted small">{text}</span>
}

function ToolBody({ tool }: { tool: ChatTool }) {
  return (
    <div className="tool-body">
      {tool.command && (
        <pre className="tool-pre">
          <span className="prompt">$ </span>
          {tool.command}
        </pre>
      )}
      {tool.diff && tool.diff.length > 0 && (
        <div className="diff">
          {tool.diff.map((l, i) => (
            <div
              // biome-ignore lint/suspicious/noArrayIndexKey: a hunk's lines are positional and never reorder.
              key={`${i}-${l.kind}`}
              className={cx("diff-line", l.kind)}
            >
              {l.kind === "add" ? "+ " : l.kind === "del" ? "− " : "  "}
              {l.text}
            </div>
          ))}
        </div>
      )}
      {tool.error ? (
        <div className="tool-error">{tool.error}</div>
      ) : (
        tool.output && <pre className="tool-pre tool-output">{tool.output}</pre>
      )}
      {tool.images ? (
        <div className="muted small pad">
          {tool.images} image{tool.images === 1 ? "" : "s"}
        </div>
      ) : null}
    </div>
  )
}

export function ToolCard({ tool }: { tool: ChatTool }) {
  const hasBody = Boolean(
    tool.command ||
      tool.diff?.length ||
      tool.output ||
      tool.error ||
      tool.images
  )
  const [open, setOpen] = React.useState(Boolean(tool.error) && hasBody)
  const { title, mail } = friendlyTitle(tool)
  const Icon = mail ? Mail : (FAMILY_ICON[tool.family] ?? Wrench)
  return (
    <div className={cx("card tool", tool.state === "error" && "is-error")}>
      <button
        type="button"
        className="tool-head"
        onClick={() => hasBody && setOpen((v) => !v)}
        aria-expanded={hasBody ? open : undefined}
      >
        <span className="tool-icon">
          <Icon className="ico" />
        </span>
        <span className="tool-text">
          <span className="tool-title">{title}</span>
          {(tool.subject || (tool.state === "running" && "Working…")) && (
            <span className="tool-subject">{tool.subject || "Working…"}</span>
          )}
        </span>
        <span className="tool-meta">
          {tool.result_line && tool.state === "completed" && (
            <span className="muted small mono">{tool.result_line}</span>
          )}
          <Duration ms={tool.duration_ms} />
          <StateMark state={tool.state} />
          {hasBody && (
            <ChevronRight className={cx("ico chev", open && "open")} />
          )}
        </span>
      </button>
      {open && hasBody && <ToolBody tool={tool} />}
    </div>
  )
}

export function ToolGroup({ calls }: { calls: ChatTool[] }) {
  const [open, setOpen] = React.useState(false)
  const title = [...new Set(calls.map((c) => friendlyTitle(c).title))].join(
    " + "
  )
  const last = calls[calls.length - 1]
  const errors = calls.some((c) => c.state === "error")
  const running = calls.some((c) => c.state === "running")
  const Icon = FAMILY_ICON[last.family] ?? Wrench
  return (
    <div className="card tool">
      <button
        type="button"
        className="tool-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <span className="tool-icon">
          <Icon className="ico" />
        </span>
        <span className="tool-text">
          <span className="tool-title">
            {title} <span className="chip">{calls.length}</span>
          </span>
          {last.subject && <span className="tool-subject">{last.subject}</span>}
        </span>
        <span className="tool-meta">
          <StateMark
            state={errors ? "error" : running ? "running" : "completed"}
          />
          <ChevronRight className={cx("ico chev", open && "open")} />
        </span>
      </button>
      {open && (
        <div className="tool-body group-list">
          {calls.map((c) => (
            <div key={c.call_id} className="group-row">
              <span className="muted">›</span>
              <span className="mono truncate">
                {c.subject || c.command || c.name}
              </span>
              <span className="group-state">
                {c.state === "error" ? (
                  <span className="state-error">failed</span>
                ) : c.state === "running" ? (
                  <Loader2 className="ico spin state-running" />
                ) : (
                  <span className="muted">{c.result_line}</span>
                )}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// AskCard is OpenMuse's choice card, backed by Claude's AskUserQuestion: the
// agent has stopped on a question, a tap answers it by typing that selection
// into the dialog in the agent's pane. lasso checks the question is still on
// screen first, so a stale card cannot answer a newer question.
export function AskCard({
  tool,
  answer,
}: {
  tool: ChatTool
  answer: (
    expect: string,
    labels: string[],
    picks: AskPick[]
  ) => Promise<{ outcome: string; detail?: string }>
}) {
  const questions: ChatAskQuestion[] = tool.ask?.questions ?? []
  const [picks, setPicks] = React.useState<number[][]>(() =>
    questions.map(() => [])
  )
  const [sending, setSending] = React.useState(false)
  const [sent, setSent] = React.useState(false)
  const [notice, setNotice] = React.useState<string | null>(null)

  const running = tool.state === "running"
  const oneTap = questions.length === 1 && !questions[0]?.multi
  const hasOptions = questions.some((q) => q.options.length > 0)
  const answerable = running && !sent && !sending && hasOptions
  const ready = questions.every((_, i) => (picks[i]?.length ?? 0) > 0)

  const submit = async (chosen: number[][]) => {
    setSending(true)
    setNotice(null)
    try {
      const res = await answer(
        questions[0]?.question ?? "",
        (questions[0]?.options ?? []).map((o) => o.label),
        chosen.map((selected, i) => ({
          selected,
          multi: Boolean(questions[i]?.multi),
          options: questions[i]?.options.length ?? 0,
        }))
      )
      if (res.outcome === "sent") setSent(true)
      else setNotice(res.detail ?? "The agent did not take the answer.")
    } catch (e) {
      setNotice((e as Error).message)
    } finally {
      setSending(false)
    }
  }

  const pick = (qi: number, oi: number) => {
    if (!answerable) return
    const q = questions[qi]
    const next = picks.map((p, i) => {
      if (i !== qi) return p
      if (!q.multi) return [oi]
      return p.includes(oi)
        ? p.filter((x) => x !== oi)
        : [...p, oi].sort((a, b) => a - b)
    })
    setPicks(next)
    if (oneTap) void submit(next)
  }

  const chosen = (qi: number): Set<string> => {
    const q = questions[qi]
    if (!running) return new Set(q.selected ?? [])
    return new Set(
      (picks[qi] ?? []).map((oi) => q.options[oi]?.label).filter(Boolean)
    )
  }

  if (!tool.ask) return null
  return (
    <div className={cx("card ask", answerable && "is-live")}>
      {answerable && <div className="badge">Your input is needed</div>}
      {questions.map((q, qi) => {
        const sel = chosen(qi)
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: questions are positional for the ask's life.
          <div key={qi} className="ask-q">
            {q.header && <span className="chip">{q.header}</span>}
            <div className="ask-text">{q.question}</div>
            <div className="ask-options">
              {q.options.map((o, oi) => {
                const on = sel.has(o.label)
                return (
                  <button
                    key={o.label}
                    type="button"
                    className={cx("option", on && "on")}
                    disabled={!answerable}
                    aria-pressed={on}
                    onClick={() => pick(qi, oi)}
                  >
                    <span
                      className={cx(
                        "mark",
                        q.multi ? "box" : "dot",
                        on && "on"
                      )}
                    >
                      {on && <Check className="ico tiny" strokeWidth={3} />}
                    </span>
                    <span className="option-text">
                      <span className="option-label">
                        {o.label}
                        {q.recommended === oi && (
                          <span className="chip">Recommended</span>
                        )}
                      </span>
                      {o.description && (
                        <span className="option-desc">{o.description}</span>
                      )}
                      {o.preview && on && (
                        <span className="tool-pre option-preview">
                          {o.preview}
                        </span>
                      )}
                    </span>
                  </button>
                )
              })}
              {q.options.length === 0 && (
                <div className="muted small">
                  This question has no options. Answer it in the terminal.
                </div>
              )}
            </div>
            {q.custom && (
              <div className="muted small">
                Answer: <span className="fg">{q.custom}</span>
              </div>
            )}
          </div>
        )
      })}
      {tool.error && <div className="tool-error">{tool.error}</div>}
      {notice && <div className="tool-error">{notice}</div>}
      {(sending || (sent && running)) && !notice && (
        <div className="muted small">
          {sending ? "Answering…" : "Answer sent. Waiting for the agent…"}
        </div>
      )}
      {!oneTap && running && hasOptions && !sent && (
        <div className="ask-actions">
          <button
            type="button"
            className="pill primary"
            disabled={!ready || sending}
            onClick={() => void submit(picks)}
          >
            {sending ? "Sending…" : "Send answers"}
          </button>
          <span className="muted small">
            {ready ? "" : "Pick one per question"}
          </span>
        </div>
      )}
    </div>
  )
}

export function ThinkingRow({ text }: { text: string }) {
  const [open, setOpen] = React.useState(false)
  const preview = text.split("\n")[0]?.slice(0, 140) ?? ""
  return (
    <div className="fold">
      <button
        type="button"
        className="fold-head"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
      >
        <ChevronRight className={cx("ico chev", open && "open")} />
        <span className="italic">Thinking</span>
        {!open && <span className="faint truncate italic">{preview}</span>}
      </button>
      {open && (
        <div className="fold-body">
          <Markdown source={text} />
        </div>
      )}
    </div>
  )
}

// taskNotification reads the <task-notification> envelope Claude Code writes
// as a user turn when a background task finishes: the harness, not the human.
export function taskNotification(text: string) {
  const t = text.trim()
  if (!t.startsWith("<task-notification>")) return null
  const tag = (name: string) =>
    t.match(new RegExp(`<${name}>([\\s\\S]*?)</${name}>`))?.[1]?.trim() ?? ""
  return {
    summary: tag("summary") || "Background task finished",
    status: tag("status"),
    result: tag("result"),
  }
}

// channelMessage reads the envelope a Claude Code channel delivers a message
// in: `<channel source="gmail-channel" from_name="…" …>body</channel>`. For an
// assistant like Jessica most user turns are these (mail, texts, Mattermost,
// scheduled loops, the hotline), not Stephan typing, so they are shown as
// incoming messages with where they came from rather than as his bubbles.
export function channelMessage(text: string) {
  const t = text.trim()
  const m = t.match(/^<channel\b([^>]*)>([\s\S]*?)<\/channel>$/)
  if (!m) return null
  const attr = (name: string) =>
    decodeEntities(m[1].match(new RegExp(`\\b${name}="([^"]*)"`))?.[1] ?? "")
  const source = attr("source")
  let label = source.replace(/-channel$/, "")
  let from = ""
  let subject = ""
  switch (source) {
    case "gmail-channel":
      label = "Email"
      from = attr("from_name") || attr("from_email")
      subject = attr("subject")
      break
    case "mattermost-channel":
      label = "Mattermost"
      from = attr("user")
      subject = attr("channel_name") && `#${attr("channel_name")}`
      break
    case "triage-channel":
      label = attr("service") || "Message"
      from = attr("sender_name") || attr("sender")
      subject = attr("thread_name") || attr("subject")
      break
    case "everloop":
      label = "Scheduled"
      subject = attr("loop")
      break
    case "hotline":
      label = "Hotline"
      from = attr("from")
      break
  }
  return { label, from, subject, body: m[2].trim() }
}

function decodeEntities(s: string): string {
  return s
    .replace(/&quot;/g, '"')
    .replace(/&#39;|&apos;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&")
}

export function ChannelCard({
  msg,
}: {
  msg: NonNullable<ReturnType<typeof channelMessage>>
}) {
  const [open, setOpen] = React.useState(false)
  const long = msg.body.length > 280 || msg.body.split("\n").length > 4
  return (
    <div className="card incoming">
      <div className="incoming-head">
        <span className="chip">{msg.label}</span>
        {msg.from && <span className="incoming-from">{msg.from}</span>}
        {msg.subject && <span className="muted truncate">{msg.subject}</span>}
      </div>
      {msg.body && (
        <div className={cx("incoming-body", long && !open && "clamped")}>
          {msg.body}
        </div>
      )}
      {long && (
        <button
          type="button"
          className="link-button"
          onClick={() => setOpen((v) => !v)}
        >
          {open ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  )
}

export function NoticeRow({
  summary,
  failed,
  body,
}: {
  summary: string
  failed?: string
  body?: string
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <div className="fold">
      <button
        type="button"
        className="fold-head"
        onClick={() => body && setOpen((v) => !v)}
        aria-expanded={body ? open : undefined}
      >
        {body ? (
          <ChevronRight className={cx("ico chev", open && "open")} />
        ) : (
          <span className="ico" />
        )}
        <span className="truncate">{summary}</span>
        {failed && <span className="state-error">{failed}</span>}
      </button>
      {open && body && (
        <div className="fold-body">
          <Markdown source={body} />
        </div>
      )}
    </div>
  )
}

export function MarkerRow({ item }: { item: ChatItem }) {
  if (item.marker === "interrupted")
    return <div className="marker">Stopped</div>
  return (
    <div className="error-notice">
      <AlertTriangle className="ico" />
      <span>
        {item.text}
        {item.count && item.count > 1 ? ` ×${item.count}` : ""}
      </span>
    </div>
  )
}

import { Check } from "lucide-react"
import * as React from "react"

import { Markdown } from "@/components/Markdown"
import { api, type BotPendingUI, type ChatTool } from "@/lib/api"
import { cn } from "@/lib/utils"

// Answering an agent's question from the simple app. Two rules from
// mac/PLAN.md §13 and §19 shape all of it:
//
// - A tap SELECTS and an explicit Answer SENDS. An answer cannot be taken
//   back, and on a phone a stray tap while scrolling is the common mistake.
// - What is sent is the question as it was when the card was drawn (the
//   capture), never re-read from a chat that may have moved underneath. The
//   server checks `expect`/`labels` against the pane's screen and refuses an
//   answer to a question the agent has moved past. The card is keyed by a
//   hash of the ask, so a new question is a new form and a pick in progress
//   never rides onto it.

const NO_AUTOCORRECT = {
  autoCapitalize: "off",
  autoCorrect: "off",
  spellCheck: false,
} as const

export const choiceClass =
  "flex w-full items-start gap-3 rounded-2xl border px-3.5 py-3 text-left transition-colors"

function Mark({ multi, on }: { multi: boolean; on: boolean }) {
  return (
    <span
      aria-hidden
      className={cn(
        "mt-0.5 flex size-5 shrink-0 items-center justify-center border-2",
        multi ? "rounded-md" : "rounded-full",
        on
          ? "border-primary bg-primary text-primary-foreground"
          : "border-muted-foreground/40"
      )}
    >
      {on && <Check className="size-3" strokeWidth={3.5} />}
    </span>
  )
}

export function AnswerButton({
  disabled,
  sending,
  onClick,
  label = "Answer",
}: {
  disabled: boolean
  sending: boolean
  onClick: () => void
  label?: string
}) {
  return (
    <button
      type="button"
      disabled={disabled || sending}
      onClick={onClick}
      className="h-11 w-full rounded-full bg-primary font-semibold text-[15px] text-primary-foreground disabled:opacity-40"
    >
      {sending ? "Sending…" : label}
    </button>
  )
}

export function PaneAskForm({
  tool,
  host,
  paneID,
  onSent,
}: {
  tool: ChatTool
  host: string
  paneID: string
  onSent: () => void
}) {
  // The capture: frozen at draw time.
  const [capture] = React.useState(() => ({
    host,
    paneID,
    questions: tool.ask?.questions ?? [],
    plan: tool.plan,
  }))
  const qs = capture.questions
  const [picks, setPicks] = React.useState<number[][]>(() => qs.map(() => []))
  const [custom, setCustom] = React.useState<string[]>(() => qs.map(() => ""))
  const [sending, setSending] = React.useState(false)
  const [sent, setSent] = React.useState(false)
  const [notice, setNotice] = React.useState<string | null>(null)
  // A plan is what the answer approves, so it is on the card; a long one is
  // clamped until opened.
  const [planOpen, setPlanOpen] = React.useState(false)
  const planLong = (capture.plan?.text.length ?? 0) > 900

  const hasPreviews = (qi: number) =>
    (qs[qi]?.options ?? []).some((o) => Boolean(o.preview))
  const answered = (qi: number) =>
    (picks[qi]?.length ?? 0) > 0 || Boolean(custom[qi]?.trim())
  const ready = qs.length > 0 && qs.every((_, i) => answered(i))

  const pick = (qi: number, oi: number) => {
    const q = qs[qi]
    if (!q || sent) return
    setPicks((all) =>
      all.map((p, i) => {
        if (i !== qi) return p
        if (!q.multi) return p[0] === oi ? [] : [oi]
        return p.includes(oi)
          ? p.filter((x) => x !== oi)
          : [...p, oi].sort((a, b) => a - b)
      })
    )
    // On a single-select question a pick and a typed answer are one or the other.
    if (!q.multi) setCustom((all) => all.map((c, i) => (i === qi ? "" : c)))
  }

  const send = async () => {
    setSending(true)
    setNotice(null)
    try {
      const res = await api.chatAnswer(
        capture.host,
        capture.paneID,
        qs[0]?.question ?? "",
        (qs[0]?.options ?? []).map((o) => o.label),
        qs.map((q, i) => {
          const text = custom[i]?.trim()
          return {
            selected: q.multi || !text ? (picks[i] ?? []) : [],
            multi: Boolean(q.multi),
            options: q.options.length,
            ...(text ? { custom: text } : {}),
            // A plan's feedback field is typed into only while it still
            // shows the placeholder this card drew.
            ...(text && q.other ? { other: q.other } : {}),
            ...(hasPreviews(i) ? { previews: true } : {}),
          }
        })
      )
      if (res.outcome === "sent") {
        setSent(true)
        onSent()
      } else setNotice(res.detail ?? "The agent's dialog did not take it.")
    } catch (e) {
      setNotice((e as Error).message)
    } finally {
      setSending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {capture.plan && (
        <div className="flex flex-col gap-1.5">
          <div
            className={cn(
              "md-body md-chat overflow-hidden rounded-2xl bg-background/40 px-3.5 py-3",
              planLong && !planOpen && "max-h-64"
            )}
          >
            <Markdown source={capture.plan.text} />
          </div>
          {planLong && (
            <button
              type="button"
              onClick={() => setPlanOpen((v) => !v)}
              className="self-start text-[13px] text-muted-foreground"
            >
              {planOpen ? "Show less" : "Show the whole plan"}
            </button>
          )}
        </div>
      )}
      {qs.map((q, qi) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: questions are positional and fixed for the capture's life.
        <div key={qi} className="flex flex-col gap-2.5">
          {q.header && (
            <span className="self-start rounded-full bg-muted px-2.5 py-0.5 font-label text-[10.5px] text-muted-foreground uppercase tracking-wide">
              {q.header}
            </span>
          )}
          <div className="whitespace-pre-wrap break-words font-medium text-[16px] text-foreground leading-snug">
            {q.question}
          </div>
          <div className="flex flex-col gap-2">
            {q.options.map((o, oi) => {
              const on = picks[qi]?.includes(oi) ?? false
              return (
                <button
                  key={o.label}
                  type="button"
                  disabled={sent}
                  aria-pressed={on}
                  onClick={() => pick(qi, oi)}
                  className={cn(
                    choiceClass,
                    on
                      ? "border-primary/60 bg-primary/10"
                      : "border-border bg-background/40"
                  )}
                >
                  <Mark multi={Boolean(q.multi)} on={on} />
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5">
                      <span className="text-[15px] text-foreground leading-snug">
                        {o.label}
                      </span>
                      {q.recommended === oi && (
                        <span className="rounded-full bg-muted px-2 py-px font-label text-[9.5px] text-muted-foreground uppercase tracking-wide">
                          Recommended
                        </span>
                      )}
                    </span>
                    {o.description && (
                      <span className="mt-0.5 block text-[13px] text-muted-foreground leading-snug">
                        {o.description}
                      </span>
                    )}
                    {o.preview && on && (
                      <span className="mt-2 block overflow-x-auto whitespace-pre rounded-xl bg-background/70 px-2.5 py-2 font-mono text-[11.5px] text-muted-foreground leading-relaxed">
                        {o.preview}
                      </span>
                    )}
                  </span>
                </button>
              )
            })}
          </div>
          {/* claude lays a question with previews out with no "Other" row, so
              it gets no text field: anything else is the terminal's. */}
          {!hasPreviews(qi) && (
            <input
              value={custom[qi] ?? ""}
              disabled={sent}
              onChange={(e) => {
                const v = e.target.value
                setCustom((all) => all.map((c, i) => (i === qi ? v : c)))
                if (v && !q.multi)
                  setPicks((all) => all.map((p, i) => (i === qi ? [] : p)))
              }}
              placeholder={q.other ?? "Or type your own answer"}
              enterKeyHint="done"
              className="h-11 rounded-2xl border border-border bg-background/40 px-3.5 text-[15px] text-foreground outline-none placeholder:text-muted-foreground focus:border-primary/60"
              {...NO_AUTOCORRECT}
            />
          )}
        </div>
      ))}
      {notice && <p className="text-[13px] text-destructive">{notice}</p>}
      {sent ? (
        <p className="text-center text-[13px] text-muted-foreground">
          Answer sent. Waiting for the agent…
        </p>
      ) : (
        <AnswerButton
          disabled={!ready}
          sending={sending}
          onClick={() => void send()}
        />
      )}
    </div>
  )
}

// An omp bot's question (pending_ui), answered over its RPC bridge. The id is
// the capture: the bridge refuses one that is no longer open.
export function BotAskForm({
  bot,
  question,
  onSent,
}: {
  bot: string
  question: BotPendingUI
  onSent: () => void
}) {
  const [choice, setChoice] = React.useState<string | null>(null)
  const [text, setText] = React.useState("")
  const [sending, setSending] = React.useState(false)
  const [sent, setSent] = React.useState(false)
  const [notice, setNotice] = React.useState<string | null>(null)

  const answer = async (
    a: { value: string } | { confirmed: boolean } | { cancelled: true }
  ) => {
    setSending(true)
    setNotice(null)
    try {
      const res = await api.bots.chatAnswer(bot, question.id, a)
      if (res.outcome === "sent") {
        setSent(true)
        onSent()
      } else setNotice(res.detail ?? "The bot did not take the answer.")
    } catch (e) {
      setNotice((e as Error).message)
    } finally {
      setSending(false)
    }
  }

  const [title, ...rest] = question.title.split("\n")
  const detail = [rest.join("\n"), question.message ?? ""]
    .filter(Boolean)
    .join("\n")
  const options =
    question.method === "confirm" ? ["Yes", "No"] : (question.options ?? [])
  const freeText =
    question.method === "input" ||
    question.method === "editor" ||
    options.some((o) => /type your own/i.test(o))

  const submit = () => {
    if (text.trim()) return void answer({ value: text.trim() })
    if (choice === null) return
    if (question.method === "confirm")
      return void answer({ confirmed: choice === "Yes" })
    void answer({ value: choice })
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="whitespace-pre-wrap break-words font-medium text-[16px] text-foreground leading-snug">
        {title}
      </div>
      {detail && (
        <pre className="max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-xl bg-background/60 p-2.5 font-mono text-[12px] text-muted-foreground">
          {detail}
        </pre>
      )}
      <div className="flex flex-col gap-2">
        {options
          .filter((o) => !/type your own/i.test(o))
          .map((o) => {
            const on = choice === o
            return (
              <button
                key={o}
                type="button"
                disabled={sent}
                aria-pressed={on}
                onClick={() => {
                  setChoice(on ? null : o)
                  setText("")
                }}
                className={cn(
                  choiceClass,
                  on
                    ? "border-primary/60 bg-primary/10"
                    : "border-border bg-background/40"
                )}
              >
                <Mark multi={false} on={on} />
                <span className="text-[15px] text-foreground leading-snug">
                  {o}
                </span>
              </button>
            )
          })}
      </div>
      {freeText && (
        <input
          value={text}
          disabled={sent}
          onChange={(e) => {
            setText(e.target.value)
            if (e.target.value) setChoice(null)
          }}
          placeholder={question.placeholder || "Your answer"}
          enterKeyHint="done"
          className="h-11 rounded-2xl border border-border bg-background/40 px-3.5 text-[15px] text-foreground outline-none placeholder:text-muted-foreground focus:border-primary/60"
          {...NO_AUTOCORRECT}
        />
      )}
      {notice && <p className="text-[13px] text-destructive">{notice}</p>}
      {sent ? (
        <p className="text-center text-[13px] text-muted-foreground">
          Answer sent.
        </p>
      ) : (
        <>
          <AnswerButton
            disabled={choice === null && !text.trim()}
            sending={sending}
            onClick={submit}
          />
          <button
            type="button"
            disabled={sending}
            onClick={() => void answer({ cancelled: true })}
            className="self-center py-1 text-[13px] text-muted-foreground"
          >
            Dismiss the question
          </button>
        </>
      )}
    </div>
  )
}

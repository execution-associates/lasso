import { ArrowUp, Paperclip } from "lucide-react"
import * as React from "react"

import type { ChatSendResult } from "@/lib/api"
import { api } from "@/lib/api"
import { cn } from "@/lib/utils"

// Unsent text, per target (a pane or a bot), for this page's life: paging to
// another card and back finds the sentence still there, and text written for
// one agent never sits in another's box.
const drafts = new Map<string, string>()

// A reply typed on a card. Sending is three-valued, as the chat's composer is:
// only `confirmed` clears the draft; `refused` and `uncertain` keep it and say
// why, and nothing is ever resent on the human's behalf.
//
// The paperclip writes each file to the agent's machine (/api/paste-file) and
// puts its path into the message, as the full UI's composer does.
export function ReplyBox({
  draftKey,
  placeholder,
  host,
  send,
  disabled,
  onDraftChange,
}: {
  // Who the reply is for, which is what its draft is kept under.
  draftKey: string
  placeholder: string
  // The machine attachments are written to; absent means no attachments.
  host?: string
  send: (text: string) => Promise<ChatSendResult>
  disabled?: boolean
  // Told whether the box holds anything but whitespace: the card holds its
  // Acknowledge back while a reply is half written.
  onDraftChange?: (drafting: boolean) => void
}) {
  const [text, setTextState] = React.useState(() => drafts.get(draftKey) ?? "")
  const setText = (next: string | ((t: string) => string)) =>
    setTextState((t) => {
      const v = typeof next === "function" ? next(t) : next
      if (v) drafts.set(draftKey, v)
      else drafts.delete(draftKey)
      return v
    })
  const drafting = text.trim() !== ""
  const draftChanged = React.useRef(onDraftChange)
  draftChanged.current = onDraftChange
  React.useEffect(() => draftChanged.current?.(drafting), [drafting])
  const [busy, setBusy] = React.useState(false)
  const [notice, setNotice] = React.useState<string | null>(null)
  const fileRef = React.useRef<HTMLInputElement>(null)
  const area = React.useRef<HTMLTextAreaElement>(null)

  // Grow with the text, up to a third of the screen.
  // biome-ignore lint/correctness/useExhaustiveDependencies: text is the trigger for re-measuring.
  React.useLayoutEffect(() => {
    const el = area.current
    if (!el) return
    el.style.height = "auto"
    el.style.height = `${Math.min(el.scrollHeight, window.innerHeight / 3)}px`
  }, [text])

  const submit = async () => {
    const body = text.trim()
    if (!body || busy) return
    setBusy(true)
    setNotice(null)
    try {
      const res = await send(body)
      if (res.outcome === "confirmed") setText("")
      else
        setNotice(
          res.outcome === "refused"
            ? (res.detail ?? "The agent did not take the message.")
            : (res.detail ??
                "It may have arrived, but the agent did not confirm it. Check before sending again.")
        )
    } catch (e) {
      setNotice((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const attach = async (files: File[]) => {
    if (!host || files.length === 0) return
    setBusy(true)
    setNotice(null)
    try {
      const paths: string[] = []
      for (const f of files)
        paths.push((await api.pasteFile(f, host, f.name)).path)
      setText((t) => [t.trimEnd(), ...paths].filter(Boolean).join(" "))
    } catch (e) {
      setNotice(`Could not attach: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-end gap-2">
        {host && (
          <>
            <button
              type="button"
              aria-label="Attach files"
              disabled={disabled || busy}
              onClick={() => fileRef.current?.click()}
              className="flex size-11 shrink-0 items-center justify-center rounded-full bg-secondary text-secondary-foreground disabled:opacity-40"
            >
              <Paperclip className="size-5" />
            </button>
            <input
              ref={fileRef}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                void attach(Array.from(e.target.files ?? []))
                e.target.value = ""
              }}
            />
          </>
        )}
        <textarea
          ref={area}
          rows={1}
          value={text}
          disabled={disabled}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            // A hardware keyboard's ⌘/Ctrl+Enter sends; Enter breaks the line,
            // as in the chat's composer.
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault()
              void submit()
            }
          }}
          placeholder={placeholder}
          className="max-h-[33vh] min-h-11 min-w-0 flex-1 resize-none rounded-[22px] border border-border bg-background/50 px-4 py-[11px] text-[15px] text-foreground leading-snug outline-none placeholder:text-muted-foreground focus:border-primary/60 disabled:opacity-50"
        />
        <button
          type="button"
          aria-label="Send"
          disabled={disabled || busy || !text.trim()}
          onClick={() => void submit()}
          className={cn(
            "flex size-11 shrink-0 items-center justify-center rounded-full bg-primary text-primary-foreground transition-opacity disabled:opacity-30"
          )}
        >
          <ArrowUp className="size-5" strokeWidth={2.5} />
        </button>
      </div>
      {notice && <p className="px-2 text-[13px] text-destructive">{notice}</p>}
    </div>
  )
}

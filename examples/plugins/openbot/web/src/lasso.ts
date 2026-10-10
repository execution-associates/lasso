// The lasso bridge, typed. window.lasso comes from lasso's SDK
// (/plugins/_sdk/lasso.js, loaded by ui/index.html), which also keeps the page
// in lasso's theme. Everything OpenBot does to the agent goes through the
// chat.* methods: the page is an opaque origin and has no other way in, and
// lasso checks plugin.json's approved `agents` grant on every call.

export interface ChatDiffLine {
  kind: "add" | "del" | "context"
  text: string
}

export interface ChatAskOption {
  label: string
  description?: string
  preview?: string
}

export interface ChatAskQuestion {
  header?: string
  question: string
  options: ChatAskOption[]
  multi?: boolean
  recommended?: number
  selected?: string[]
  custom?: string
}

export interface ChatTool {
  call_id: string
  name: string
  title: string
  family: string
  group?: string
  subject?: string
  command?: string
  state: "running" | "completed" | "error"
  result_line?: string
  output?: string
  diff?: ChatDiffLine[]
  error?: string
  images?: number
  duration_ms?: number
  ask?: { questions: ChatAskQuestion[] }
}

export interface ChatItem {
  // "incoming" is a message a Claude Code channel delivered (mail, texts,
  // Mattermost, a scheduled loop): the <channel …> envelope is its text.
  kind: "user" | "agent" | "tool" | "marker" | "incoming"
  source?: string
  id: string
  at?: string
  text?: string
  thinking?: boolean
  marker?: "interrupted" | "error"
  count?: number
  tool?: ChatTool
}

export interface ChatPayload {
  pane_id: string
  agent: string
  host: string
  title?: string
  model?: string
  path?: string
  start_offset: number
  items: ChatItem[]
  tokens?: number
  running?: boolean
  more?: boolean
  note?: string
  starting?: boolean
}

export type SendOutcome = "confirmed" | "refused" | "uncertain"

export interface AskPick {
  selected: number[]
  multi: boolean
  options: number
}

export interface Target {
  agent: string
  host?: string
}

interface LassoSDK {
  call: <T = unknown>(method: string, params?: object) => Promise<T>
  on: (event: "context" | "theme", fn: (data: unknown) => void) => () => void
  toast: (message: string) => Promise<unknown>
}

declare global {
  interface Window {
    lasso?: LassoSDK
  }
}

function sdk(): LassoSDK {
  if (!window.lasso)
    throw new Error("lasso's SDK did not load: open this page inside lasso")
  return window.lasso
}

// target is the agent named in ui/index.html. It is only which agent the page
// asks for; lasso refuses any the plugin was not granted.
export function target(): Target {
  const meta = (name: string) =>
    document
      .querySelector<HTMLMetaElement>(`meta[name="${name}"]`)
      ?.content.trim() || undefined
  return {
    agent: meta("openbot-agent") ?? "jessica",
    host: meta("openbot-host"),
  }
}

export const bridge = {
  read: (t: Target, before?: number) =>
    sdk().call<ChatPayload>("chat.read", before ? { ...t, before } : t),
  send: (t: Target, text: string) =>
    sdk().call<{ outcome: SendOutcome; detail?: string }>("chat.send", {
      ...t,
      text,
    }),
  answer: (t: Target, expect: string, labels: string[], answers: AskPick[]) =>
    sdk().call<{ outcome: "sent" | "refused"; detail?: string }>(
      "chat.answer",
      { ...t, expect, labels, answers }
    ),
  stop: (t: Target) =>
    sdk().call<{ outcome: "sent" | "refused" | "uncertain"; detail?: string }>(
      "chat.stop",
      t
    ),
  toast: (message: string) => {
    void sdk()
      .toast(message)
      .catch(() => {})
  },
}

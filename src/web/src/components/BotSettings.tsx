import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  Image as ImageIcon,
  KeyRound,
  Play,
  Plus,
  Radio,
  RotateCcw,
  Save,
  Square,
  SquareTerminal,
  Trash2,
  Wrench,
  X,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { JobsTab } from "@/components/BotJobs"
import {
  BotAvatar,
  BotStateMark,
  DeleteBotDialog,
  startBot,
  stopBot,
} from "@/components/BotParts"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { EditableCombobox } from "@/components/ui/editable-combobox"
import { Field, fieldClass, labelClass } from "@/components/ui/field"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import {
  api,
  type BotFields,
  type BotMCPServer,
  type BotOAuthStatus,
  type BotSkill,
  type BotView,
  type HostInfo,
} from "@/lib/api"
import {
  BOT_NAME_RE,
  BOT_PERMISSION_MODES,
  botKey,
  botRunning,
  invalidateBots,
} from "@/lib/bots"
import { groupHosts, memberLabel } from "@/lib/hosts"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// A bot's settings (/bots/<name>/settings) and the creator (/bots/manage/new),
// one form: General and Connections are the bot's row (saved together by the
// footer), and the rest are the bot's FOLDER on its host — its skills, its
// CLAUDE.md, its mise env — each saved where it is edited, since each is its
// own file and none of them needs the row to change.

type Tab =
  | "general"
  | "connections"
  | "jobs"
  | "skills"
  | "instructions"
  | "environment"
  | "launch"

const TABS: { id: Tab; label: string; existing?: boolean }[] = [
  { id: "general", label: "General" },
  { id: "connections", label: "Connections" },
  { id: "jobs", label: "Jobs", existing: true },
  { id: "skills", label: "Skills", existing: true },
  { id: "instructions", label: "Instructions", existing: true },
  { id: "environment", label: "Environment", existing: true },
  { id: "launch", label: "Launch", existing: true },
]

// Claude Code's effort levels, for a server too old to list them.
const FALLBACK_EFFORTS = ["low", "medium", "high", "xhigh", "max"]

// A key/value row being edited. Rows rather than a record so a key can be
// renamed mid-typing without the row jumping, and an empty one can exist.
type KV = { id: number; key: string; value: string }

// One MCP server as the form edits it.
type DraftServer = {
  id: number
  name: string
  type: BotMCPServer["type"]
  command: string
  // One argument per line: an argument may hold spaces, a line may not.
  args: string
  url: string
  env: KV[]
  headers: KV[]
  channel: boolean
  oauth: boolean
  oauth_client_id: string
  oauth_redirect: string
  oauth_scope: string
}

type Draft = {
  name: string
  host: string
  avatar: string
  workspace: string
  dir: string
  model: string
  effort: string
  permission_mode: string
  keep_running: boolean
  notify: boolean
  extra_args: string
  strict_mcp: boolean
  mcp: DraftServer[]
}

let rowSeq = 0
const nextID = () => ++rowSeq

function toKV(rec?: Record<string, string>): KV[] {
  return Object.entries(rec ?? {}).map(([key, value]) => ({
    id: nextID(),
    key,
    value,
  }))
}

function fromKV(rows: KV[]): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const r of rows) if (r.key.trim()) out[r.key.trim()] = r.value
  return Object.keys(out).length > 0 ? out : undefined
}

function lines(text: string): string[] {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
}

function draftOf(b?: BotView): Draft {
  return {
    name: b?.name ?? "",
    host: b?.host ?? "local",
    avatar: b?.avatar ?? "",
    workspace: b?.workspace ?? "",
    dir: b?.dir ?? "",
    model: b?.model ?? "",
    effort: b?.effort ?? "",
    permission_mode: b?.permission_mode ?? "",
    keep_running: b?.keep_running ?? true,
    notify: b?.notify ?? true,
    extra_args: (b?.extra_args ?? []).join("\n"),
    strict_mcp: b?.strict_mcp ?? false,
    mcp: (b?.mcp ?? []).map((s) => ({
      id: nextID(),
      name: s.name,
      type: s.type || "stdio",
      command: s.command ?? "",
      args: (s.args ?? []).join("\n"),
      url: s.url ?? "",
      env: toKV(s.env),
      headers: toKV(s.headers),
      channel: !!s.channel,
      oauth: !!s.oauth,
      oauth_client_id: s.oauth_client_id ?? "",
      oauth_redirect: s.oauth_redirect ?? "",
      oauth_scope: s.oauth_scope ?? "",
    })),
  }
}

function fieldsOf(d: Draft): BotFields {
  return {
    dir: d.dir.trim(),
    workspace: d.workspace.trim(),
    model: d.model.trim(),
    effort: d.effort,
    permission_mode: d.permission_mode,
    keep_running: d.keep_running,
    notify: d.notify,
    avatar: d.avatar.trim(),
    extra_args: lines(d.extra_args),
    strict_mcp: d.strict_mcp,
    mcp: d.mcp.map(serverOf),
  }
}

function serverOf(s: DraftServer): BotMCPServer {
  const base = { name: s.name.trim(), type: s.type, channel: s.channel }
  return s.type === "stdio"
    ? {
        ...base,
        command: s.command.trim(),
        args: lines(s.args),
        env: fromKV(s.env),
      }
    : {
        ...base,
        url: s.url.trim(),
        headers: fromKV(s.headers),
        oauth: s.oauth,
        oauth_client_id: s.oauth_client_id.trim() || undefined,
        oauth_redirect: s.oauth_redirect.trim() || undefined,
        oauth_scope: s.oauth_scope.trim() || undefined,
      }
}

// serverKey is a server's settings in one comparable string, the same for the
// saved row and an unchanged draft, so a card can tell it has unsaved edits.
function serverKey(m: BotMCPServer): string {
  const sorted = (r?: Record<string, string>) =>
    Object.entries(r ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const stdio = (m.type || "stdio") === "stdio"
  return JSON.stringify([
    m.name,
    m.type || "stdio",
    !!m.channel,
    stdio ? (m.command ?? "") : (m.url ?? ""),
    stdio ? (m.args ?? []) : [],
    sorted(stdio ? m.env : m.headers),
    !stdio && !!m.oauth,
    stdio ? "" : (m.oauth_client_id ?? ""),
    stdio ? "" : (m.oauth_redirect ?? ""),
    stdio ? "" : (m.oauth_scope ?? ""),
  ])
}

// ---------------------------------------------------------------------------

function TabStrip({
  tabs,
  value,
  onChange,
}: {
  tabs: { id: Tab; label: string }[]
  value: Tab
  onChange: (t: Tab) => void
}) {
  return (
    <div
      role="tablist"
      className="no-scrollbar flex flex-none overflow-x-auto border-border border-b px-1.5"
    >
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className={cn(
            "flex-none border-b-2 px-3 py-1.5 text-[13px]",
            value === t.id
              ? "border-primary text-primary"
              : "border-transparent text-muted-foreground hover:text-foreground"
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  )
}

function Check({
  id,
  checked,
  onChange,
  children,
}: {
  id: string
  checked: boolean
  onChange: (v: boolean) => void
  children: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-2">
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={(v) => onChange(v === true)}
        className="mt-0.5"
      />
      <label htmlFor={id} className="cursor-pointer text-[13px] leading-snug">
        {children}
      </label>
    </div>
  )
}

// The host picker, grouped as the New dialog's is. Only on create: a bot's
// folder lives on its host, so moving it is a new bot.
function HostSelect({
  value,
  onChange,
}: {
  value: string
  onChange: (h: string) => void
}) {
  const { data } = useQuery({
    queryKey: ["hosts"],
    queryFn: () => api.hosts(),
  })
  const localLabel = data?.local?.hostname || "local"
  const groups = React.useMemo(() => {
    const { groups, localMates, families } = groupHosts(data?.hosts ?? [])
    const opt = (h: HostInfo) => ({
      value: h.alias,
      label: `${memberLabel(h, families)}${h.alias !== memberLabel(h, families) ? ` (${h.alias})` : ""}`,
      disabled: !(h.reachable && h.running && h.compatible),
    })
    return [
      {
        box: localLabel,
        opts: [
          { value: "local", label: `${localLabel} (local)`, disabled: false },
          ...localMates.map(opt),
        ],
      },
      ...groups.map((g) => ({ box: g.label, opts: g.hosts.map(opt) })),
    ]
  }, [data, localLabel])
  return (
    <select
      id="bot-host"
      className={fieldClass}
      value={value}
      onChange={(e) => onChange(e.target.value)}
    >
      {groups.map((g) => (
        <optgroup key={g.box} label={g.box}>
          {g.opts.map((o) => (
            <option key={o.value} value={o.value} disabled={o.disabled}>
              {o.label}
              {o.disabled ? " (unavailable)" : ""}
            </option>
          ))}
        </optgroup>
      ))}
    </select>
  )
}

function KVEditor({
  rows,
  onChange,
  keyPlaceholder,
  valuePlaceholder,
  addLabel,
}: {
  rows: KV[]
  onChange: (rows: KV[]) => void
  keyPlaceholder: string
  valuePlaceholder: string
  addLabel: string
}) {
  const set = (id: number, patch: Partial<KV>) =>
    onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  return (
    <div className="flex flex-col gap-1.5">
      {rows.map((r) => (
        <div key={r.id} className="flex items-center gap-1.5">
          <input
            {...NO_AUTOCORRECT}
            value={r.key}
            onChange={(e) => set(r.id, { key: e.target.value })}
            placeholder={keyPlaceholder}
            aria-label="Name"
            className={cn(fieldClass, "w-2/5 min-w-0 font-mono")}
          />
          <input
            {...NO_AUTOCORRECT}
            value={r.value}
            onChange={(e) => set(r.id, { value: e.target.value })}
            placeholder={valuePlaceholder}
            aria-label="Value"
            className={cn(fieldClass, "min-w-0 flex-1 font-mono")}
          />
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Remove"
            title="Remove"
            onClick={() => onChange(rows.filter((x) => x.id !== r.id))}
          >
            <X />
          </Button>
        </div>
      ))}
      <Button
        variant="ghost"
        size="sm"
        className="self-start"
        onClick={() =>
          onChange([...rows, { id: nextID(), key: "", value: "" }])
        }
      >
        <Plus />
        {addLabel}
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// General

// AvatarPicture is the avatar itself as a button: click it to pick a picture.
// It saves on pick, not with the form: the picture is a file in the bot's
// folder, not a setting. Before the bot exists there is no folder, so it is
// just the avatar.
function AvatarPicture({
  bot,
  shown,
}: {
  bot?: BotView
  shown: Pick<BotView, "avatar" | "name"> & { avatar_image?: string }
}) {
  const input = React.useRef<HTMLInputElement>(null)
  const [busy, setBusy] = React.useState(false)
  if (!bot) return <BotAvatar bot={shown} size={36} />
  const run = async (fn: () => Promise<unknown>, fail: string) => {
    setBusy(true)
    try {
      await fn()
      await invalidateBots(bot.name)
    } catch (e) {
      toast.error(`${fail}: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }
  const label = bot.avatar_image ? "Replace the picture" : "Use a picture"
  return (
    <span className="relative shrink-0">
      <input
        ref={input}
        type="file"
        accept="image/png,image/jpeg,image/webp,image/gif"
        className="hidden"
        aria-label="Choose a picture"
        onChange={(e) => {
          const f = e.target.files?.[0]
          e.target.value = ""
          if (f)
            void run(
              () => api.bots.avatarSet(bot.name, f),
              "could not set the picture"
            )
        }}
      />
      <button
        type="button"
        disabled={busy}
        onClick={() => input.current?.click()}
        title={label}
        aria-label={label}
        className="group relative block rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-60"
      >
        <BotAvatar bot={shown} size={36} />
        <span className="absolute inset-0 flex items-center justify-center rounded-full bg-black/45 text-white opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
          <ImageIcon className="size-4" />
        </span>
      </button>
      {bot.avatar_image && (
        <button
          type="button"
          disabled={busy}
          title="Remove the picture"
          aria-label="Remove the picture"
          onClick={() =>
            void run(
              () => api.bots.avatarClear(bot.name),
              "could not remove the picture"
            )
          }
          className="absolute -top-1 -right-1 flex size-4 items-center justify-center rounded-full border border-border bg-card text-muted-foreground hover:text-foreground"
        >
          <X className="size-2.5" />
        </button>
      )}
    </span>
  )
}

function GeneralTab({
  draft,
  set,
  creating,
  bot,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  creating: boolean
  bot?: BotView
}) {
  // The model suggestions and effort levels are Claude Code's, from the host's
  // own harness table (what the New dialog offers for claude).
  const config = useQuery({
    queryKey: qk.agentConfig(draft.host),
    queryFn: () => api.agentConfig(draft.host),
  })
  const claude = config.data?.harnesses?.find((h) => h.id === "claude")
  const efforts = claude?.effort_levels?.length
    ? claude.effort_levels
    : FALLBACK_EFFORTS
  const nameBad = creating && draft.name !== "" && !BOT_NAME_RE.test(draft.name)
  return (
    <div className="flex flex-col gap-4">
      {creating && (
        <Field
          label="Name"
          htmlFor="bot-name"
          hint={
            nameBad ? (
              <span className="text-destructive">
                Lowercase letters, digits and dashes, starting with a letter or
                digit (40 at most).
              </span>
            ) : (
              "Its herdr pane, its folder and its address here. Fixed once created."
            )
          }
        >
          <input
            {...NO_AUTOCORRECT}
            id="bot-name"
            value={draft.name}
            maxLength={40}
            onChange={(e) => set({ name: e.target.value.toLowerCase() })}
            placeholder="jessica"
            aria-invalid={nameBad || undefined}
            className={fieldClass}
          />
        </Field>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Avatar"
          htmlFor="bot-avatar"
          hint={
            bot
              ? "Click the avatar for a picture, or type an emoji or up to 8 characters. Empty uses the first letter."
              : "An emoji or up to 8 characters; a picture once the bot exists. Empty uses the first letter."
          }
        >
          <div className="flex items-center gap-2">
            <AvatarPicture
              bot={bot}
              shown={{
                name: draft.name || "?",
                avatar: draft.avatar,
                avatar_image: bot?.avatar_image,
              }}
            />
            <input
              {...NO_AUTOCORRECT}
              id="bot-avatar"
              value={draft.avatar}
              onChange={(e) => set({ avatar: e.target.value })}
              placeholder="🤖"
              className={cn(fieldClass, "min-w-0 flex-1")}
            />
          </div>
        </Field>
        {creating && (
          <Field
            label="Host"
            htmlFor="bot-host"
            hint="The machine it runs on, and where its folder lives."
          >
            <HostSelect value={draft.host} onChange={(host) => set({ host })} />
          </Field>
        )}
        <Field
          label="Workspace"
          htmlFor="bot-workspace"
          hint="The herdr workspace its pane opens in."
        >
          <input
            {...NO_AUTOCORRECT}
            id="bot-workspace"
            value={draft.workspace}
            onChange={(e) => set({ workspace: e.target.value })}
            placeholder="Bots"
            className={fieldClass}
          />
        </Field>
        <Field
          label="Folder"
          htmlFor="bot-dir"
          hint="Its working directory, holding CLAUDE.md, skills and mise.toml."
        >
          <input
            {...NO_AUTOCORRECT}
            id="bot-dir"
            value={draft.dir}
            onChange={(e) => set({ dir: e.target.value })}
            placeholder={`~/bots/${draft.name || "<name>"}`}
            className={cn(fieldClass, "font-mono")}
          />
        </Field>
        <Field label="Model" htmlFor="bot-model">
          <EditableCombobox
            id="bot-model"
            value={draft.model}
            onValueChange={(model) => set({ model })}
            suggestions={claude?.model_suggestions ?? []}
            placeholder="default"
            emptyOption="default"
          />
        </Field>
        <Field label="Thinking effort" htmlFor="bot-effort">
          <select
            id="bot-effort"
            className={fieldClass}
            value={draft.effort}
            onChange={(e) => set({ effort: e.target.value })}
          >
            <option value="">default</option>
            {efforts.map((l) => (
              <option key={l} value={l}>
                {l}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Permission mode"
          htmlFor="bot-permission"
          hint="What it may do without asking. A bot nobody is watching usually needs more than the default."
        >
          <select
            id="bot-permission"
            className={fieldClass}
            value={draft.permission_mode}
            onChange={(e) => set({ permission_mode: e.target.value })}
          >
            {BOT_PERMISSION_MODES.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
        </Field>
        <Field
          label="Extra CLI args"
          htmlFor="bot-args"
          hint="Added to its claude command, one argument per line."
        >
          <textarea
            {...NO_AUTOCORRECT}
            id="bot-args"
            rows={2}
            value={draft.extra_args}
            onChange={(e) => set({ extra_args: e.target.value })}
            className={cn(fieldClass, "resize-y font-mono")}
          />
        </Field>
      </div>
      <Check
        id="bot-keep"
        checked={draft.keep_running}
        onChange={(keep_running) => set({ keep_running })}
      >
        Keep running
        <span className="block text-[11.5px] text-muted-foreground">
          Bring it back when its session ends or herdr restarts, resuming the
          same conversation.
        </span>
      </Check>
      <Check
        id="bot-notify"
        checked={draft.notify}
        onChange={(notify) => set({ notify })}
      >
        Notify me when it answers
        <span className="block text-[11.5px] text-muted-foreground">
          A notification on your devices each time it finishes a reply, unless
          you are looking at its chat. Turn on notifications on each device
          first (Bots list, the bell).
        </span>
      </Check>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Connections

// OAuthPanel signs a saved bot in to one of its http/sse servers. The
// authorization server's page opens in a new tab; it normally sends the
// browser back to lasso, which finishes on its own. When it cannot (a server
// that only allows a localhost redirect, which on a VPS goes nowhere), the
// address that tab ended on is pasted here instead: it carries the code.
function OAuthPanel({
  bot,
  server,
  status,
  onChanged,
}: {
  bot: BotView
  server: string
  status?: BotOAuthStatus
  onChanged: () => void
}) {
  const [waiting, setWaiting] = React.useState<{ localhost: boolean } | null>(
    null
  )
  const [pasted, setPasted] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  // A sign-in or sign-out changes the server's headersHelper in the bot's
  // mcp.json, which claude reads only when it starts: a running bot needs a
  // restart to see it. (Later token refreshes do not.)
  const [changed, setChanged] = React.useState(false)

  // The callback page tells its opener it finished.
  React.useEffect(() => {
    if (!waiting) return
    const onMsg = (e: MessageEvent) => {
      if (e.origin !== window.location.origin) return
      if (e.data && typeof e.data.lassoBotOAuth === "boolean") {
        setWaiting(null)
        if (e.data.lassoBotOAuth) setChanged(true)
        onChanged()
      }
    }
    window.addEventListener("message", onMsg)
    const poll = setInterval(onChanged, 3000)
    return () => {
      window.removeEventListener("message", onMsg)
      clearInterval(poll)
    }
  }, [waiting, onChanged])
  // Signed in while waiting (the callback landed, or another tab finished).
  const connectedAt = status?.status === "connected" ? status.expires_at : -1
  const startedAt = React.useRef(-1)
  React.useEffect(() => {
    if (waiting && connectedAt !== startedAt.current && connectedAt !== -1) {
      setWaiting(null)
      setChanged(true)
    }
  }, [waiting, connectedAt])

  const signIn = async () => {
    // Opened inside the click, before any await, or a popup blocker eats it.
    const tab = window.open("about:blank", "_blank")
    setBusy(true)
    try {
      startedAt.current = connectedAt
      const res = await api.bots.oauthStart(bot.name, server)
      if (tab) tab.location.href = res.authorize_url
      else window.open(res.authorize_url, "_blank", "noopener")
      setWaiting({ localhost: res.localhost })
      setPasted("")
    } catch (e) {
      tab?.close()
      toast.error(`could not start signing in: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  const finish = async () => {
    setBusy(true)
    try {
      await api.bots.oauthFinish(pasted.trim())
      setWaiting(null)
      setChanged(true)
      onChanged()
      toast.success(`${server} is connected`)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const signOut = async () => {
    setBusy(true)
    try {
      await api.bots.oauthSignOut(bot.name, server)
      setChanged(true)
      onChanged()
    } catch (e) {
      toast.error(`could not sign out: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  const connected = status?.status === "connected"
  return (
    <div className="flex flex-col gap-2 rounded-lg bg-muted/60 px-3 py-2 text-[12px]">
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={cn(
            "min-w-0 flex-1",
            status?.status === "error"
              ? "text-destructive"
              : "text-muted-foreground"
          )}
        >
          {connected
            ? `Signed in${status?.issuer ? ` at ${new URL(status.issuer).host}` : ""}. lasso refreshes the token before it expires.`
            : status?.status === "error"
              ? `Needs signing in again: ${status.error ?? "the token could not be refreshed"}`
              : "Not signed in."}
        </span>
        <Button
          size="sm"
          variant={connected ? "outline" : "default"}
          disabled={busy}
          onClick={() => void signIn()}
        >
          <KeyRound />
          {connected || status?.status === "error"
            ? "Sign in again"
            : "Sign in"}
        </Button>
        {status?.status && (
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            onClick={() => void signOut()}
          >
            Sign out
          </Button>
        )}
      </div>
      {changed && !waiting && !!bot.pane_id && (
        <p className="border-border border-t pt-2 text-foreground">
          {bot.name} is still running with its old connection settings, so this
          takes effect after a restart: use Restart on the Launch tab. Token
          refreshes later on need no restart.
        </p>
      )}
      {waiting && (
        <div className="flex flex-col gap-1.5 border-border border-t pt-2">
          <p className="text-muted-foreground">
            {waiting.localhost
              ? "This server only sends you back to localhost, which won't load from here. Finish signing in in the new tab, then copy the address from that tab's address bar and paste it below."
              : "Finish signing in in the new tab; this updates by itself. If that tab ends on a page that won't load, copy its address and paste it below."}
          </p>
          <div className="flex gap-1.5">
            <input
              {...NO_AUTOCORRECT}
              value={pasted}
              onChange={(e) => setPasted(e.target.value)}
              placeholder="http://localhost:…/callback?code=…&state=…"
              aria-label="The address the sign-in ended on"
              className={cn(fieldClass, "min-w-0 flex-1 font-mono")}
            />
            <Button
              size="sm"
              disabled={busy || !pasted.includes("state=")}
              onClick={() => void finish()}
            >
              Finish
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

type ConnFilter = "all" | "channels" | "tools"
const CONN_FILTER_KEY = "lasso.bots.connections-filter"

function readConnFilter(): ConnFilter {
  try {
    const v = localStorage.getItem(CONN_FILTER_KEY)
    return v === "channels" || v === "tools" ? v : "all"
  } catch {
    return "all"
  }
}

function blankServer(channel: boolean): DraftServer {
  return {
    id: nextID(),
    name: "",
    type: "stdio",
    command: "",
    args: "",
    url: "",
    env: [],
    headers: [],
    channel,
    oauth: false,
    oauth_client_id: "",
    oauth_redirect: "",
    oauth_scope: "",
  }
}

// The card's second line: the command and its arguments with paths cut to
// their last segment, or the URL's host and path.
function serverSummary(s: DraftServer): string {
  if (s.type === "stdio") {
    const short = (a: string) =>
      a.includes("/") ? `…/${a.split("/").pop()}` : a
    const words = [s.command.trim(), ...lines(s.args)]
      .filter(Boolean)
      .map(short)
    return words.join(" ") || "no command yet"
  }
  try {
    const u = new URL(s.url.trim())
    return u.host + (u.pathname === "/" ? "" : u.pathname)
  } catch {
    return s.url.trim() || "no URL yet"
  }
}

function plural(n: number, one: string) {
  return `${n} ${one}${n === 1 ? "" : "s"}`
}

function ConnectionsTab({
  draft,
  set,
  bot,
  lassoMCP,
  lassoChannel,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  // The saved bot; signing in needs the server saved first.
  bot?: BotView
  // The server lasso adds for the bot's own settings tools, or "".
  lassoMCP?: string
  // Whether lasso adds its channel, which delivers the bot's jobs.
  lassoChannel?: boolean
}) {
  const oauthKey = ["bot-oauth", bot?.name ?? ""]
  const oauth = useQuery({
    queryKey: oauthKey,
    queryFn: () => api.bots.oauth(bot?.name ?? ""),
    enabled: !!bot,
  })
  const queryClient = useQueryClient()
  const refreshOAuth = React.useCallback(
    () =>
      void queryClient.invalidateQueries({
        queryKey: ["bot-oauth", bot?.name ?? ""],
      }),
    [queryClient, bot?.name]
  )
  const [filter, setFilterState] = React.useState<ConnFilter>(readConnFilter)
  const setFilter = (f: ConnFilter) => {
    setFilterState(f)
    try {
      localStorage.setItem(CONN_FILTER_KEY, f)
    } catch {}
  }
  const [editing, setEditing] = React.useState<number | null>(null)
  const setServer = (id: number, patch: Partial<DraftServer>) =>
    set({ mcp: draft.mcp.map((s) => (s.id === id ? { ...s, ...patch } : s)) })
  const saved = new Set((bot?.mcp ?? []).map(serverKey))
  const channels = draft.mcp.filter((s) => s.channel)
  const lassoCard = !!lassoMCP
  const counts = {
    all: draft.mcp.length + (lassoCard ? 1 : 0) + (lassoChannel ? 1 : 0),
    channels: channels.length + (lassoChannel ? 1 : 0),
    tools: draft.mcp.length - channels.length + (lassoCard ? 1 : 0),
  }
  const shown = draft.mcp.filter((s) =>
    filter === "all" ? true : filter === "channels" ? s.channel : !s.channel
  )
  const showLasso = lassoCard && filter !== "channels"
  const showChannel = !!lassoChannel && filter !== "tools"
  const add = () => {
    const s = blankServer(filter === "channels")
    set({ mcp: [...draft.mcp, s] })
    setEditing(s.id)
  }
  // Closing the editor on a server never filled in drops it: Add then Done
  // must not leave an empty card behind.
  const close = () => {
    const s = draft.mcp.find((x) => x.id === editing)
    if (s && !s.name.trim() && !s.command.trim() && !s.url.trim())
      set({ mcp: draft.mcp.filter((x) => x.id !== s.id) })
    setEditing(null)
  }
  const current = draft.mcp.find((x) => x.id === editing)
  const chips: { id: ConnFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "channels", label: "Channels" },
    { id: "tools", label: "Tools" },
  ]
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        The MCP servers this bot gets. A <b>channel</b> also delivers messages
        (mail, chat, a schedule) that the bot answers without being asked. Keep
        secrets out of this page: write{" "}
        <code className="rounded bg-muted px-1 font-mono text-[11.5px]">
          ${"{VAR}"}
        </code>{" "}
        in a server's env or headers and set{" "}
        <code className="rounded bg-muted px-1 font-mono text-[11.5px]">
          VAR
        </code>{" "}
        under Environment, where it is stored encrypted.
      </p>
      <div className="flex flex-wrap items-center gap-1.5">
        {chips.map((c) => (
          <button
            key={c.id}
            type="button"
            aria-pressed={filter === c.id}
            onClick={() => setFilter(c.id)}
            className={cn(
              "rounded-full border px-3 py-1 text-[12px] transition-colors",
              filter === c.id
                ? "border-primary/50 bg-primary/15 text-foreground"
                : "border-border text-muted-foreground hover:text-foreground"
            )}
          >
            {c.label}{" "}
            <span className="text-muted-foreground tabular-nums">
              {counts[c.id]}
            </span>
          </button>
        ))}
        <Button variant="outline" size="sm" className="ml-auto" onClick={add}>
          <Plus />
          Add connection
        </Button>
      </div>
      {shown.length === 0 && !showLasso && !showChannel ? (
        <p className="rounded-lg border border-border border-dashed px-3 py-6 text-center text-[12.5px] text-muted-foreground">
          {filter === "channels"
            ? "No channels yet. A channel is an MCP server that delivers messages to the bot."
            : filter === "tools"
              ? "No tool servers yet."
              : "No connections yet."}
        </p>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(14rem,1fr))] gap-2">
          {shown.map((s) => (
            <ServerCard
              key={s.id}
              server={s}
              status={oauth.data?.servers[s.name.trim()]}
              dirty={!!bot && !saved.has(serverKey(serverOf(s)))}
              onOpen={() => setEditing(s.id)}
            />
          ))}
          {showLasso && <LassoCard url={lassoMCP ?? ""} />}
          {showChannel && <LassoChannelCard />}
        </div>
      )}
      <Check
        id="bot-strict"
        checked={draft.strict_mcp}
        onChange={(strict_mcp) => set({ strict_mcp })}
      >
        Only these servers (strict)
        <span className="block text-[11.5px] text-muted-foreground">
          Off, it also gets your claude.ai connectors and user-level MCP
          servers.
        </span>
      </Check>
      <Dialog open={!!current} onOpenChange={(o) => !o && close()}>
        {current && (
          <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-xl">
            <DialogHeader>
              <DialogTitle className="font-mono">
                {current.name.trim() || "New connection"}
              </DialogTitle>
              <DialogDescription>
                Changes apply when you save the bot.
              </DialogDescription>
            </DialogHeader>
            <ServerFields
              s={current}
              setServer={setServer}
              bot={bot}
              status={oauth.data?.servers[current.name.trim()]}
              refreshOAuth={refreshOAuth}
            />
            <DialogFooter className="sm:justify-between">
              <Button
                variant="ghost"
                className="text-muted-foreground hover:text-destructive"
                onClick={() => {
                  set({ mcp: draft.mcp.filter((x) => x.id !== current.id) })
                  setEditing(null)
                }}
              >
                <Trash2 />
                Remove connection
              </Button>
              <Button onClick={close}>Done</Button>
            </DialogFooter>
          </DialogContent>
        )}
      </Dialog>
    </div>
  )
}

function ServerCard({
  server: s,
  status,
  dirty,
  onOpen,
}: {
  server: DraftServer
  status?: BotOAuthStatus
  dirty: boolean
  onOpen: () => void
}) {
  const stdio = s.type === "stdio"
  const vars = (stdio ? s.env : s.headers).filter((r) => r.key.trim()).length
  const detail =
    !stdio && s.oauth
      ? status?.status === "connected"
        ? "Signed in"
        : status?.status === "error"
          ? "Sign-in failed"
          : "Sign in needed"
      : vars > 0
        ? plural(vars, stdio ? "env var" : "header")
        : ""
  const Icon = s.channel ? Radio : Wrench
  return (
    <button
      type="button"
      onClick={onOpen}
      className="fx-plate flex min-w-0 flex-col gap-1 rounded-lg border border-border bg-card p-3 text-left transition-colors hover:border-primary/40"
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <Icon
          className={cn(
            "size-3.5 shrink-0",
            s.channel ? "text-primary" : "text-muted-foreground"
          )}
        />
        <span className="min-w-0 truncate font-medium font-mono text-[12.5px] text-foreground">
          {s.name.trim() || "unnamed"}
        </span>
        {dirty && (
          <span
            role="img"
            aria-label="Unsaved changes"
            title="Unsaved changes"
            className="size-1.5 shrink-0 rounded-full bg-primary"
          />
        )}
        <span
          className={cn(
            "ml-auto shrink-0 rounded px-1.5 py-px font-medium text-[10px] uppercase tracking-wide",
            s.channel
              ? "bg-primary/15 text-primary"
              : "bg-muted text-muted-foreground"
          )}
        >
          {s.channel ? "channel" : s.type}
        </span>
      </span>
      <span className="truncate font-mono text-[11.5px] text-muted-foreground">
        {s.channel ? `${s.type} · ` : ""}
        {serverSummary(s)}
      </span>
      {detail && (
        <span
          className={cn(
            "text-[11.5px]",
            detail === "Sign in needed" || detail === "Sign-in failed"
              ? "text-amber-500"
              : "text-muted-foreground"
          )}
        >
          {detail}
        </span>
      )}
    </button>
  )
}

// lasso's own server for the bot, shown so the grid is the whole picture, but
// not editable: lasso writes it into the bot's mcp.json on every start.
function LassoCard({ url }: { url: string }) {
  return (
    <div
      className="flex min-w-0 flex-col gap-1 rounded-lg border border-border border-dashed p-3"
      title={url}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <Wrench className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="min-w-0 truncate font-medium font-mono text-[12.5px] text-foreground">
          lasso
        </span>
        <span className="ml-auto shrink-0 text-[10.5px] text-muted-foreground">
          added by lasso
        </span>
      </span>
      <span className="text-[11.5px] text-muted-foreground leading-snug">
        Its own settings tools: get_bot, update_bot, set_bot_env, set_bot_avatar
        and more.
      </span>
    </div>
  )
}

// lasso's channel for the bot, read-only like LassoCard: it delivers the
// Jobs tab's scheduled prompts and webhooks.
function LassoChannelCard() {
  return (
    <div className="flex min-w-0 flex-col gap-1 rounded-lg border border-border border-dashed p-3">
      <span className="flex min-w-0 items-center gap-1.5">
        <Radio className="size-3.5 shrink-0 text-primary" />
        <span className="min-w-0 truncate font-medium font-mono text-[12.5px] text-foreground">
          lasso-channel
        </span>
        <span className="ml-auto shrink-0 text-[10.5px] text-muted-foreground">
          added by lasso
        </span>
      </span>
      <span className="text-[11.5px] text-muted-foreground leading-snug">
        Delivers this bot's jobs: scheduled prompts and webhooks, set up in the
        Jobs tab.
      </span>
    </div>
  )
}

// One server's settings, edited in the Connections dialog.
function ServerFields({
  s,
  setServer,
  bot,
  status,
  refreshOAuth,
}: {
  s: DraftServer
  setServer: (id: number, patch: Partial<DraftServer>) => void
  bot?: BotView
  status?: BotOAuthStatus
  refreshOAuth: () => void
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-end gap-2">
        <Field label="Name" htmlFor={`mcp-name-${s.id}`}>
          <input
            {...NO_AUTOCORRECT}
            id={`mcp-name-${s.id}`}
            value={s.name}
            onChange={(e) => setServer(s.id, { name: e.target.value })}
            placeholder="gmail-channel"
            className={cn(fieldClass, "font-mono")}
          />
        </Field>
        <Field label="Type" htmlFor={`mcp-type-${s.id}`}>
          <select
            id={`mcp-type-${s.id}`}
            className={cn(fieldClass, "w-24")}
            value={s.type}
            onChange={(e) =>
              setServer(s.id, {
                type: e.target.value as DraftServer["type"],
              })
            }
          >
            <option value="stdio">stdio</option>
            <option value="http">http</option>
            <option value="sse">sse</option>
          </select>
        </Field>
      </div>
      {s.type === "stdio" ? (
        <>
          <Field label="Command" htmlFor={`mcp-cmd-${s.id}`}>
            <input
              {...NO_AUTOCORRECT}
              id={`mcp-cmd-${s.id}`}
              value={s.command}
              onChange={(e) => setServer(s.id, { command: e.target.value })}
              placeholder="npx"
              className={cn(fieldClass, "font-mono")}
            />
          </Field>
          <Field
            label="Arguments"
            htmlFor={`mcp-args-${s.id}`}
            hint="One per line."
          >
            <textarea
              {...NO_AUTOCORRECT}
              id={`mcp-args-${s.id}`}
              rows={2}
              value={s.args}
              onChange={(e) => setServer(s.id, { args: e.target.value })}
              className={cn(fieldClass, "resize-y font-mono")}
            />
          </Field>
          <div className="flex flex-col gap-1">
            <span className={labelClass}>Environment</span>
            <KVEditor
              rows={s.env}
              onChange={(env) => setServer(s.id, { env })}
              keyPlaceholder="API_KEY"
              valuePlaceholder="${API_KEY}"
              addLabel="Add variable"
            />
          </div>
        </>
      ) : (
        <>
          <Field label="URL" htmlFor={`mcp-url-${s.id}`}>
            <input
              {...NO_AUTOCORRECT}
              id={`mcp-url-${s.id}`}
              value={s.url}
              onChange={(e) => setServer(s.id, { url: e.target.value })}
              placeholder="https://example.com/mcp"
              className={cn(fieldClass, "font-mono")}
            />
          </Field>
          <div className="flex flex-col gap-1">
            <span className={labelClass}>Headers</span>
            <KVEditor
              rows={s.headers}
              onChange={(headers) => setServer(s.id, { headers })}
              keyPlaceholder="Authorization"
              valuePlaceholder="Bearer ${TOKEN}"
              addLabel="Add header"
            />
          </div>
          <Check
            id={`mcp-oauth-${s.id}`}
            checked={s.oauth}
            onChange={(oauth) => setServer(s.id, { oauth })}
          >
            Sign in with OAuth
            <span className="block text-[11.5px] text-muted-foreground">
              For servers that ask you to log in. lasso keeps the tokens in the
              bot's fnox.toml and refreshes them.
            </span>
          </Check>
          {s.oauth &&
            (bot?.mcp.some((m) => m.name === s.name && m.oauth) ? (
              <OAuthPanel
                bot={bot}
                server={s.name}
                status={status}
                onChanged={refreshOAuth}
              />
            ) : (
              <p className="text-[12px] text-muted-foreground">
                Save, then sign in here.
              </p>
            ))}
          {s.oauth && (
            <details className="text-[12px]">
              <summary className="cursor-pointer text-muted-foreground">
                Server without automatic client registration
              </summary>
              <div className="mt-2 flex flex-col gap-2">
                <Field
                  label="Client ID"
                  htmlFor={`mcp-cid-${s.id}`}
                  hint="Leave empty when the server registers clients itself."
                >
                  <input
                    {...NO_AUTOCORRECT}
                    id={`mcp-cid-${s.id}`}
                    value={s.oauth_client_id}
                    onChange={(e) =>
                      setServer(s.id, { oauth_client_id: e.target.value })
                    }
                    className={cn(fieldClass, "font-mono")}
                  />
                </Field>
                <Field
                  label="Redirect URI it was registered with"
                  htmlFor={`mcp-redir-${s.id}`}
                  hint="Default: this lasso's own /api/bots/oauth/callback. A localhost one works too: you paste where the browser lands."
                >
                  <input
                    {...NO_AUTOCORRECT}
                    id={`mcp-redir-${s.id}`}
                    value={s.oauth_redirect}
                    onChange={(e) =>
                      setServer(s.id, { oauth_redirect: e.target.value })
                    }
                    placeholder={`${window.location.origin}/api/bots/oauth/callback`}
                    className={cn(fieldClass, "font-mono")}
                  />
                </Field>
                <Field label="Scope" htmlFor={`mcp-scope-${s.id}`}>
                  <input
                    {...NO_AUTOCORRECT}
                    id={`mcp-scope-${s.id}`}
                    value={s.oauth_scope}
                    onChange={(e) =>
                      setServer(s.id, { oauth_scope: e.target.value })
                    }
                    placeholder="what the server advertises"
                    className={cn(fieldClass, "font-mono")}
                  />
                </Field>
              </div>
            </details>
          )}
        </>
      )}
      <Check
        id={`mcp-channel-${s.id}`}
        checked={s.channel}
        onChange={(channel) => setServer(s.id, { channel })}
      >
        Channel
        <span className="block text-[11.5px] text-muted-foreground">
          Messages it delivers reach the bot as incoming messages.
        </span>
      </Check>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Skills

function SkillRow({
  skill,
  children,
}: {
  skill: BotSkill
  children?: React.ReactNode
}) {
  return (
    <div className="flex items-start gap-2 rounded-lg border border-border bg-card px-3 py-2">
      <div className="min-w-0 flex-1">
        <div className="font-medium font-mono text-[12.5px] text-foreground">
          {skill.name}
        </div>
        {skill.description && (
          <div className="line-clamp-2 text-[12px] text-muted-foreground">
            {skill.description}
          </div>
        )}
      </div>
      {children}
    </div>
  )
}

function SkillsTab({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const key = ["bot-skills", bot.name]
  const skills = useQuery({
    queryKey: key,
    queryFn: () => api.bots.skills(bot.name),
    // The bot installs a skill on its own time (installSkill), so the list
    // keeps looking while this tab is open.
    refetchInterval: 5000,
  })
  // The host's user-level skills: Claude Code loads ~/.claude/skills in every
  // session, so the bot has them without anything being copied.
  const userSkills = useQuery({
    queryKey: ["bot-skill-library", bot.host],
    queryFn: () => api.bots.skillLibrary(bot.host),
  })
  const [source, setSource] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [removing, setRemoving] = React.useState<string | null>(null)
  const machine = bot.host === "local" ? "this machine" : bot.host
  const running = !!bot.pane_id

  // Whatever was pasted (a URL, a repo, a path, a skill's text, or just a
  // description of one) goes to the bot itself as a request to install it into
  // its own .claude/skills: the bot can fetch, clone or write a skill, which
  // lasso cannot do for an arbitrary source. Typed into its pane like any chat
  // message, so it reads as its human's request.
  const installSkill = async () => {
    if (!bot.pane_id) return
    setBusy(true)
    try {
      const res = await api.chatSend(
        bot.host,
        bot.pane_id,
        `Install a skill for this project only, from what I've pasted below. Put it in .claude/skills/<skill-name>/ under your current folder (its SKILL.md plus any files it needs), not in ~/.claude/skills. If it's a URL, repository or path, fetch or copy it from there; if it's a description, write the skill. Tell me the skill's name when it's installed.\n\n${source.trim()}`
      )
      if (res.outcome === "confirmed") {
        setSource("")
        toast.success(`Sent to ${bot.name}; it's installing the skill now`)
      } else {
        toast.error(
          res.outcome === "uncertain"
            ? `Not sure ${bot.name} got it: ${res.detail || "check its chat"}`
            : res.detail || `${bot.name} did not take the request`
        )
      }
    } catch (e) {
      toast.error(`could not send it: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }

  const run = async (fn: () => Promise<unknown>, fail: string) => {
    setBusy(true)
    try {
      await fn()
      await queryClient.invalidateQueries({ queryKey: key })
      return true
    } catch (e) {
      toast.error(`${fail}: ${(e as Error).message}`)
      return false
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h3 className="font-medium text-[13px] text-foreground">
          Always available
          {userSkills.data && userSkills.data.skills.length > 0 && (
            <span className="ml-1.5 font-normal text-muted-foreground">
              {userSkills.data.skills.length}
            </span>
          )}
        </h3>
        <p className="text-[12.5px] text-muted-foreground">
          Your user-level skills in{" "}
          <code className="font-mono text-[11.5px]">~/.claude/skills</code> on{" "}
          {machine}. Claude Code loads them in every session, this bot included,
          so there is nothing to add. Edit them there and every bot picks the
          change up on its next start.
        </p>
        {userSkills.isPending && <Orb state="working" px={16} />}
        {userSkills.error && (
          <p className="text-[12px] text-destructive">
            {(userSkills.error as Error).message}
          </p>
        )}
        {userSkills.data?.skills.length === 0 && (
          <p className="text-[12.5px] text-muted-foreground">
            None on {machine}.
          </p>
        )}
        {/* A scroll area of one-line rows: a long ~/.claude/skills would
            otherwise push this bot's own skills off the page. */}
        {userSkills.data && userSkills.data.skills.length > 0 && (
          <div className="max-h-72 overflow-y-auto rounded-lg border border-border bg-card">
            {userSkills.data.skills.map((s) => (
              <div
                key={s.path}
                title={s.description || undefined}
                className="flex min-w-0 items-baseline gap-2 border-border/60 border-b px-3 py-1.5 last:border-b-0"
              >
                <span className="shrink-0 font-mono text-[12px] text-foreground">
                  {s.name}
                </span>
                <span className="min-w-0 truncate text-[11.5px] text-muted-foreground">
                  {s.description}
                </span>
              </div>
            ))}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h3 className="font-medium text-[13px] text-foreground">
          This bot only
        </h3>
        <p className="text-[12.5px] text-muted-foreground">
          Project skills in{" "}
          <code className="font-mono text-[11.5px]">.claude/skills</code> of the
          bot's folder, which no other session sees.
        </p>
        {skills.isPending && <Orb state="working" px={16} />}
        {skills.error && (
          <p className="text-[12px] text-destructive">
            {(skills.error as Error).message}
          </p>
        )}
        {skills.data?.skills.length === 0 && (
          <p className="text-[12.5px] text-muted-foreground">None yet.</p>
        )}
        <div className="flex flex-col gap-1.5">
          {skills.data?.skills.map((s) => (
            <SkillRow key={s.name} skill={s}>
              {removing === s.name ? (
                <span className="flex shrink-0 items-center gap-1">
                  <Button
                    variant="destructive"
                    size="sm"
                    disabled={busy}
                    onClick={() =>
                      void run(
                        () => api.bots.skillRemove(bot.name, s.name),
                        `could not remove ${s.name}`
                      ).then(() => setRemoving(null))
                    }
                  >
                    Remove
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setRemoving(null)}
                  >
                    Keep
                  </Button>
                </span>
              ) : (
                <Button
                  variant="ghost"
                  size="icon-sm"
                  title={`Remove ${s.name}`}
                  aria-label={`Remove ${s.name}`}
                  onClick={() => setRemoving(s.name)}
                >
                  <Trash2 />
                </Button>
              )}
            </SkillRow>
          ))}
        </div>
        <Field
          label="Add a skill"
          htmlFor="bot-skill-source"
          hint={
            running
              ? `Paste anything: a URL, a GitHub repo, a path on ${machine}, a SKILL.md, or a description of what the skill should do. ${bot.name} installs it itself; follow along in its chat.`
              : `${bot.name} installs skills itself, so start it first.`
          }
        >
          <div className="flex flex-col gap-1.5">
            <textarea
              {...NO_AUTOCORRECT}
              id="bot-skill-source"
              rows={3}
              value={source}
              onChange={(e) => setSource(e.target.value)}
              placeholder="https://github.com/owner/repo/tree/main/skills/triage"
              className={cn(fieldClass, "resize-y font-mono")}
            />
            <Button
              variant="outline"
              className="self-start"
              disabled={!running || !source.trim() || busy}
              onClick={() => void installSkill()}
            >
              <Plus />
              Ask {bot.name} to install it
            </Button>
          </div>
        </Field>
      </section>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Instructions

function InstructionsTab({ bot, path }: { bot: BotView; path: string }) {
  const file = useQuery({
    queryKey: ["bot-claude-md", bot.host, path],
    queryFn: () => api.fileText(path, bot.host),
    staleTime: 0,
  })
  const [text, setText] = React.useState<string | null>(null)
  const [saving, setSaving] = React.useState(false)
  const value = text ?? file.data ?? ""
  const dirty = text !== null && text !== (file.data ?? "")
  const save = async () => {
    setSaving(true)
    try {
      await api.writeFile(path, value, bot.host)
      await file.refetch()
      setText(null)
      toast.success("CLAUDE.md saved")
    } catch (e) {
      toast.error(`could not save CLAUDE.md: ${(e as Error).message}`)
    } finally {
      setSaving(false)
    }
  }
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <p className="text-[12.5px] text-muted-foreground">
        <code className="font-mono text-[11.5px]">{path}</code> — who the bot is
        and how it works. A running bot reads it again on its next restart.
      </p>
      {file.error ? (
        <p className="text-[12px] text-destructive">
          could not read it: {(file.error as Error).message}
        </p>
      ) : file.isPending ? (
        <Orb state="working" px={16} />
      ) : (
        <textarea
          {...NO_AUTOCORRECT}
          value={value}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if ((e.metaKey || e.ctrlKey) && e.key === "s") {
              e.preventDefault()
              if (dirty) void save()
            }
          }}
          aria-label="CLAUDE.md"
          className={cn(
            fieldClass,
            "min-h-[50vh] flex-1 resize-y font-mono text-[13px] leading-relaxed md:text-[12.5px]"
          )}
        />
      )}
      <div className="flex items-center gap-2">
        <Button disabled={!dirty || saving} onClick={() => void save()}>
          {saving ? <Orb state="working" px={14} on="accent" /> : <Save />}
          Save CLAUDE.md
        </Button>
        {dirty && (
          <Button variant="ghost" onClick={() => setText(null)}>
            Discard changes
          </Button>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Environment

function EnvironmentTab({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const key = ["bot-env", bot.name]
  const env = useQuery({
    queryKey: key,
    queryFn: () => api.bots.env(bot.name),
    staleTime: 0,
  })
  const [newKey, setNewKey] = React.useState("")
  const [newValue, setNewValue] = React.useState("")
  const [newSecret, setNewSecret] = React.useState(false)
  // The row being edited (plain) or replaced (secret), and its new value.
  const [editing, setEditing] = React.useState<{
    key: string
    value: string
  } | null>(null)
  const [busy, setBusy] = React.useState(false)
  // A running bot reads its environment once, at launch.
  const [restartNeeded, setRestartNeeded] = React.useState(false)

  const run = async (
    fn: () => Promise<{ restart_needed?: boolean }>,
    fail: string
  ) => {
    setBusy(true)
    try {
      const res = await fn()
      if (res.restart_needed) setRestartNeeded(true)
      await queryClient.invalidateQueries({ queryKey: key })
      return true
    } catch (e) {
      toast.error(`${fail}: ${(e as Error).message}`)
      return false
    } finally {
      setBusy(false)
    }
  }

  const keyBad = newKey !== "" && !/^[A-Za-z_][A-Za-z0-9_]*$/.test(newKey)

  return (
    <div className="flex flex-col gap-4">
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        Variables in the bot's{" "}
        <code className="font-mono text-[11.5px]">fnox.toml</code>, handed to it
        by mise when it launches and to nothing else. A secret goes to the
        file's default provider (lasso's own age key unless you changed it) and
        is decrypted only at launch; its value is never shown again, only
        replaced. To keep secrets in 1Password, a vault or elsewhere, add a
        provider to that file with{" "}
        <code className="font-mono text-[11.5px]">fnox provider add</code> and
        make it the default.
      </p>
      {restartNeeded && (
        <p className="rounded-lg bg-muted px-3 py-2 text-[12px] text-muted-foreground">
          The bot is running with its old environment. Restart it from Launch
          for the change to reach it.
        </p>
      )}
      {env.isPending && <Orb state="working" px={16} />}
      {env.error && (
        <p className="text-[12px] text-destructive">
          {(env.error as Error).message}
        </p>
      )}
      {env.data && env.data.vars.length === 0 && (
        <p className="text-[12.5px] text-muted-foreground">No variables yet.</p>
      )}
      {env.data && env.data.vars.length > 0 && (
        <div className="flex flex-col divide-y divide-border/60 rounded-lg border border-border bg-card">
          {env.data.vars.map((v) => (
            <div
              key={v.key}
              className="flex flex-wrap items-center gap-2 px-3 py-2"
            >
              <span className="min-w-0 font-mono text-[12.5px] text-foreground">
                {v.key}
              </span>
              {v.secret && (
                <span
                  className="flex items-center gap-1 rounded bg-muted px-1.5 py-px text-[10.5px] text-muted-foreground"
                  title={`Held by the fnox provider "${v.provider ?? ""}"`}
                >
                  <KeyRound className="size-3" />
                  {v.provider && v.provider !== "lasso" ? v.provider : "secret"}
                </span>
              )}
              {editing?.key === v.key ? (
                <span className="flex w-full items-center gap-1.5">
                  <input
                    {...NO_AUTOCORRECT}
                    // biome-ignore lint/a11y/noAutofocus: the field exists only because Edit/Replace was just clicked.
                    autoFocus
                    type={v.secret ? "password" : "text"}
                    value={editing.value}
                    onChange={(e) =>
                      setEditing({ key: v.key, value: e.target.value })
                    }
                    placeholder={v.secret ? "new secret value" : ""}
                    aria-label={`${v.key} value`}
                    className={cn(fieldClass, "min-w-0 flex-1 font-mono")}
                  />
                  <Button
                    size="sm"
                    disabled={busy}
                    onClick={() =>
                      void run(
                        () =>
                          api.bots.envSet(
                            bot.name,
                            v.key,
                            editing.value,
                            v.secret
                          ),
                        `could not set ${v.key}`
                      ).then((ok) => ok && setEditing(null))
                    }
                  >
                    Save
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setEditing(null)}
                  >
                    Cancel
                  </Button>
                </span>
              ) : (
                <>
                  <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-muted-foreground">
                    {v.secret ? "●●●●●●●●" : v.value}
                  </span>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() =>
                      setEditing({
                        key: v.key,
                        value: v.secret ? "" : (v.value ?? ""),
                      })
                    }
                  >
                    {v.secret ? "Replace" : "Edit"}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={busy}
                    title={`Remove ${v.key}`}
                    aria-label={`Remove ${v.key}`}
                    onClick={() =>
                      void run(
                        () => api.bots.envUnset(bot.name, v.key),
                        `could not remove ${v.key}`
                      )
                    }
                  >
                    <Trash2 />
                  </Button>
                </>
              )}
            </div>
          ))}
        </div>
      )}

      <div className="flex flex-col gap-2 rounded-lg border border-border border-dashed p-3">
        <span className={labelClass}>Add a variable</span>
        <div className="flex flex-col gap-1.5 sm:flex-row">
          <input
            {...NO_AUTOCORRECT}
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            placeholder="NAME"
            aria-label="Variable name"
            aria-invalid={keyBad || undefined}
            className={cn(fieldClass, "font-mono sm:w-48")}
          />
          <input
            {...NO_AUTOCORRECT}
            type={newSecret ? "password" : "text"}
            value={newValue}
            onChange={(e) => setNewValue(e.target.value)}
            placeholder="value"
            aria-label="Variable value"
            className={cn(fieldClass, "min-w-0 flex-1 font-mono")}
          />
        </div>
        {keyBad && (
          <p className="text-[11.5px] text-destructive">
            Letters, digits and underscores, not starting with a digit.
          </p>
        )}
        <Check id="bot-env-secret" checked={newSecret} onChange={setNewSecret}>
          Secret (encrypted, never shown again)
        </Check>
        <Button
          className="self-start"
          disabled={busy || !newKey || keyBad || !newValue}
          onClick={() =>
            void run(
              () => api.bots.envSet(bot.name, newKey, newValue, newSecret),
              `could not set ${newKey}`
            ).then((ok) => {
              if (!ok) return
              setNewKey("")
              setNewValue("")
              setNewSecret(false)
            })
          }
        >
          <Plus />
          Add
        </Button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Launch

function LaunchTab({
  bot,
  launch,
  dirPath,
  tasks,
  onOpenTerminal,
}: {
  bot: BotView
  launch: string
  dirPath: string
  tasks: string[] | null
  onOpenTerminal?: (b: BotView) => void
}) {
  const [busy, setBusy] = React.useState(false)
  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    try {
      await fn()
    } finally {
      setBusy(false)
    }
  }
  const restart = (fresh: boolean) =>
    run(async () => {
      try {
        await api.bots.restart(bot.name, fresh)
      } catch (e) {
        toast.error(`could not restart ${bot.name}: ${(e as Error).message}`)
      } finally {
        void invalidateBots(bot.name)
      }
    })
  const running = botRunning(bot)
  const task = bot.launch_task || "bot"
  // The current task stays a choice even when mise could not list the folder.
  const choices = Array.from(new Set(["bot", ...(tasks ?? []), task]))
  const setTask = (next: string) =>
    run(async () => {
      try {
        await api.bots.update(bot.name, {
          ...bot,
          launch_task: next === "bot" ? "" : next,
        })
        if (running)
          toast(
            `${bot.name} switches to mise run ${next} when it restarts. If that changes provider, use Restart fresh.`
          )
      } catch (e) {
        toast.error((e as Error).message)
      } finally {
        void invalidateBots(bot.name)
      }
    })
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <BotStateMark bot={bot} words className="mr-2 text-[12.5px]" />
        {running ? (
          <>
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void run(() => stopBot(bot.name))}
            >
              <Square />
              Stop
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void restart(false)}
            >
              <RotateCcw />
              Restart
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void restart(true)}
            >
              <RotateCcw />
              Restart fresh
            </Button>
          </>
        ) : (
          <>
            <Button
              size="sm"
              disabled={busy}
              onClick={() => void run(() => startBot(bot.name))}
            >
              <Play />
              Start
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void run(() => startBot(bot.name, true))}
            >
              <RotateCcw />
              Start fresh
            </Button>
          </>
        )}
        {bot.pane_id && onOpenTerminal && (
          <Button variant="ghost" size="sm" onClick={() => onOpenTerminal(bot)}>
            <SquareTerminal />
            Open terminal
          </Button>
        )}
      </div>
      {bot.error && <p className="text-[12px] text-destructive">{bot.error}</p>}
      <Field
        label="Launch task"
        htmlFor="bot-task"
        hint={
          <>
            The mise task that starts it, also used to restore and relaunch it.
            Anything other than <code className="font-mono">bot</code> is a mode
            you write in its folder, such as another provider: it sets its
            environment and ends with{" "}
            <code className="font-mono">exec mise run bot -- "$@"</code>. A
            conversation started on one provider may not resume on another:
            after switching provider, restart fresh.
          </>
        }
      >
        <select
          id="bot-task"
          className={cn(fieldClass, "max-w-xs font-mono")}
          value={task}
          disabled={busy}
          onChange={(e) => void setTask(e.target.value)}
        >
          {choices.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
      </Field>
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        The bot runs as <code className="font-mono">mise run {task}</code> in{" "}
        <code className="font-mono">{dirPath}</code>
        {task === "bot" ? (
          <>: mise loads its environment, then runs the script below.</>
        ) : (
          <>
            , which hands off to <code className="font-mono">bot</code>, the
            script below.
          </>
        )}{" "}
        lasso writes that script from these settings on every save; edit the
        settings, not the file.
        {bot.session_id && (
          <>
            {" "}
            Session <code className="font-mono">{bot.session_id}</code>.
          </>
        )}
      </p>
      <pre className="overflow-x-auto whitespace-pre rounded-lg border border-border bg-card px-3 py-2 font-mono text-[11.5px] text-muted-foreground leading-[1.6]">
        {launch}
      </pre>
    </div>
  )
}

// ---------------------------------------------------------------------------

export function BotSettings({
  mode,
  name,
  lead,
  onClose,
  onCreated,
  onDeleted,
  onOpenTerminal,
}: {
  mode: "new" | "edit"
  name?: string
  // The phone's Back, at the left of the header.
  lead?: React.ReactNode
  // Leave the page without saving (to the bot's chat, or the list).
  onClose: () => void
  onCreated?: (name: string) => void
  onDeleted?: () => void
  onOpenTerminal?: (b: BotView) => void
}) {
  const creating = mode === "new"
  const detail = useQuery({
    queryKey: botKey(name ?? ""),
    queryFn: () => api.bots.get(name ?? ""),
    enabled: !creating && !!name,
    refetchInterval: 3000,
  })
  const bot = detail.data?.bot

  // The form starts from the bot as loaded and is not re-seeded by the poll:
  // the poll is for the state (running, working), and replacing a half-edited
  // form with the server's copy would throw the edit away.
  const [draft, setDraft] = React.useState<Draft | null>(() =>
    creating ? draftOf() : null
  )
  const [base, setBase] = React.useState<string>(() =>
    creating ? "" : JSON.stringify(null)
  )
  React.useEffect(() => {
    if (creating || !bot || draft) return
    const d = draftOf(bot)
    setDraft(d)
    setBase(JSON.stringify(fieldsOf(d)))
  }, [creating, bot, draft])
  const set = (patch: Partial<Draft>) =>
    setDraft((d) => (d ? { ...d, ...patch } : d))

  const [tab, setTab] = React.useState<Tab>("general")
  const tabs = TABS.filter((t) => !creating || !t.existing)
  const [saving, setSaving] = React.useState(false)
  const [confirmRestart, setConfirmRestart] = React.useState(false)
  const [confirmDelete, setConfirmDelete] = React.useState(false)

  const dirty =
    !!draft && (creating || JSON.stringify(fieldsOf(draft)) !== base)
  const running = !!bot && botRunning(bot)

  const save = async (restart: boolean) => {
    if (!draft) return
    setSaving(true)
    try {
      if (creating) {
        const res = await api.bots.create({
          ...fieldsOf(draft),
          name: draft.name,
          host: draft.host,
          start: false,
        })
        await invalidateBots(res.bot.name)
        if (res.error) toast.warning(`${res.bot.name} created: ${res.error}`)
        else toast.success(`${res.bot.name} created`)
        onCreated?.(res.bot.name)
        return
      }
      if (!name) return
      const res = await api.bots.update(name, { ...fieldsOf(draft), restart })
      const d = draftOf(res.bot)
      setDraft(d)
      setBase(JSON.stringify(fieldsOf(d)))
      void invalidateBots(name)
      toast.success(restart ? `${name} saved and restarting` : `${name} saved`)
    } catch (e) {
      toast.error(
        `could not ${creating ? "create" : "save"}: ${(e as Error).message}`
      )
    } finally {
      setSaving(false)
    }
  }

  const title = creating ? "New bot" : `${name} settings`
  const createReady = creating && !!draft && BOT_NAME_RE.test(draft.name)
  // The row's own fields are saved by the footer; the folder's tabs save
  // themselves, so the footer only shows where it means something.
  const rowTab = tab === "general" || tab === "connections"

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        {lead}
        {!creating && bot && <BotAvatar bot={bot} size={22} />}
        <span className="min-w-0 truncate font-medium text-[12.5px] text-foreground">
          {title}
        </span>
        <span className="ml-auto" />
        {bot && <BotStateMark bot={bot} words />}
        <button
          type="button"
          onClick={onClose}
          title={creating ? "Cancel" : "Back to the conversation"}
          aria-label={creating ? "Cancel" : "Back to the conversation"}
          className="flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground max-md:hidden"
        >
          <X className="size-4" />
        </button>
      </div>
      <TabStrip tabs={tabs} value={tab} onChange={setTab} />
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto">
        <div
          className={cn(
            "mx-auto flex w-full flex-1 flex-col px-4 py-4",
            // The job cards are a grid that earns a third column.
            tab === "jobs" ? "max-w-5xl" : "max-w-3xl"
          )}
        >
          {detail.error ? (
            <p className="text-[12.5px] text-destructive">
              could not load {name}: {(detail.error as Error).message}
            </p>
          ) : !draft ? (
            <Orb state="working" px={20} />
          ) : tab === "general" ? (
            <GeneralTab draft={draft} set={set} creating={creating} bot={bot} />
          ) : tab === "connections" ? (
            <ConnectionsTab
              draft={draft}
              set={set}
              bot={bot}
              lassoMCP={detail.data?.lasso_mcp}
              lassoChannel={!!detail.data?.lasso_channel}
            />
          ) : !bot || !detail.data ? null : tab === "jobs" ? (
            <JobsTab bot={bot} />
          ) : tab === "skills" ? (
            <SkillsTab bot={bot} />
          ) : tab === "instructions" ? (
            <InstructionsTab bot={bot} path={detail.data.claude_md} />
          ) : tab === "environment" ? (
            <EnvironmentTab bot={bot} />
          ) : (
            <LaunchTab
              bot={bot}
              launch={detail.data.launch}
              dirPath={detail.data.dir_path}
              tasks={detail.data.tasks}
              onOpenTerminal={onOpenTerminal}
            />
          )}
          {creating && (
            <p className="mt-6 text-[12px] text-muted-foreground">
              Skills, instructions and environment are set once the bot exists.
              It is created stopped, so you can finish setting it up first.
            </p>
          )}
        </div>
      </div>
      {(rowTab || (!creating && tab === "launch")) && (
        <div className="flex flex-none flex-wrap items-center gap-2 border-border border-t bg-card px-3 py-2">
          {!creating && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => setConfirmDelete(true)}
            >
              <Trash2 />
              Delete bot…
            </Button>
          )}
          <span className="ml-auto" />
          {rowTab && dirty && !creating && (
            <span className="text-[11.5px] text-muted-foreground">
              Unsaved changes
            </span>
          )}
          {rowTab && !creating && running && (
            <Button
              variant="outline"
              disabled={!dirty || saving}
              onClick={() =>
                bot?.state === "working"
                  ? setConfirmRestart(true)
                  : void save(true)
              }
            >
              Save & restart
            </Button>
          )}
          {rowTab && (
            <Button
              disabled={saving || (creating ? !createReady : !dirty)}
              onClick={() => void save(false)}
            >
              {saving ? <Orb state="working" px={14} on="accent" /> : <Save />}
              {creating ? "Create bot" : "Save"}
            </Button>
          )}
        </div>
      )}

      <AlertDialog open={confirmRestart} onOpenChange={setConfirmRestart}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Restart {name} mid-turn?</AlertDialogTitle>
            <AlertDialogDescription>
              {name} is working right now. Restarting interrupts what it is
              doing; it resumes the same conversation with the new settings.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Not now</AlertDialogCancel>
            <AlertDialogAction onClick={() => void save(true)}>
              Save & restart
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <DeleteBotDialog
        bot={bot ?? null}
        dirPath={detail.data?.dir_path}
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        onDeleted={onDeleted}
      />
    </div>
  )
}

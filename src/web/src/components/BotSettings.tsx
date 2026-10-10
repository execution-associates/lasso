import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  KeyRound,
  Play,
  Plus,
  RotateCcw,
  Save,
  Square,
  SquareTerminal,
  Trash2,
  X,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import {
  BotAvatar,
  BotStateMark,
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
import { EditableCombobox } from "@/components/ui/editable-combobox"
import { Field, fieldClass, labelClass } from "@/components/ui/field"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import {
  ApiError,
  api,
  type BotFields,
  type BotMCPServer,
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
  | "skills"
  | "instructions"
  | "environment"
  | "launch"

const TABS: { id: Tab; label: string; existing?: boolean }[] = [
  { id: "general", label: "General" },
  { id: "connections", label: "Connections" },
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
    avatar: d.avatar.trim(),
    extra_args: lines(d.extra_args),
    strict_mcp: d.strict_mcp,
    mcp: d.mcp.map((s): BotMCPServer => {
      const base = { name: s.name.trim(), type: s.type, channel: s.channel }
      return s.type === "stdio"
        ? {
            ...base,
            command: s.command.trim(),
            args: lines(s.args),
            env: fromKV(s.env),
          }
        : { ...base, url: s.url.trim(), headers: fromKV(s.headers) }
    }),
  }
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

function GeneralTab({
  draft,
  set,
  creating,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
  creating: boolean
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
          hint="An emoji or up to 8 characters. Empty uses the first letter."
        >
          <div className="flex items-center gap-2">
            <BotAvatar
              bot={{ name: draft.name || "?", avatar: draft.avatar }}
              size={32}
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
    </div>
  )
}

// ---------------------------------------------------------------------------
// Connections

function ConnectionsTab({
  draft,
  set,
}: {
  draft: Draft
  set: (patch: Partial<Draft>) => void
}) {
  const setServer = (id: number, patch: Partial<DraftServer>) =>
    set({ mcp: draft.mcp.map((s) => (s.id === id ? { ...s, ...patch } : s)) })
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        The MCP servers this bot gets. Mark one as a <b>channel</b> when it also
        delivers messages (mail, chat, a schedule) that the bot should answer
        without being asked. Keep secrets out of this page: write{" "}
        <code className="rounded bg-muted px-1 font-mono text-[11.5px]">
          ${"{VAR}"}
        </code>{" "}
        in a server's env or headers and set{" "}
        <code className="rounded bg-muted px-1 font-mono text-[11.5px]">
          VAR
        </code>{" "}
        under Environment, where it is stored encrypted.
      </p>
      <Check
        id="bot-strict"
        checked={draft.strict_mcp}
        onChange={(strict_mcp) => set({ strict_mcp })}
      >
        Only these servers (no claude.ai connectors)
        <span className="block text-[11.5px] text-muted-foreground">
          Ignore every other MCP configuration, including your account's
          connectors and user-level servers.
        </span>
      </Check>
      {draft.mcp.map((s) => (
        <div
          key={s.id}
          className="flex flex-col gap-3 rounded-lg border border-border bg-card p-3"
        >
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
            <Button
              variant="ghost"
              size="icon"
              className="ml-auto"
              title="Remove this server"
              aria-label={`Remove ${s.name || "server"}`}
              onClick={() =>
                set({ mcp: draft.mcp.filter((x) => x.id !== s.id) })
              }
            >
              <Trash2 />
            </Button>
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
      ))}
      <Button
        variant="outline"
        className="self-start"
        onClick={() =>
          set({
            mcp: [
              ...draft.mcp,
              {
                id: nextID(),
                name: "",
                type: "stdio",
                command: "",
                args: "",
                url: "",
                env: [],
                headers: [],
                channel: false,
              },
            ],
          })
        }
      >
        <Plus />
        Add server
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Skills

function SkillsTab({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const key = ["bot-skills", bot.name]
  const skills = useQuery({
    queryKey: key,
    queryFn: () => api.bots.skills(bot.name),
  })
  const library = useQuery({
    queryKey: ["bot-skill-library", bot.host],
    queryFn: () => api.bots.skillLibrary(bot.host),
  })
  const [pick, setPick] = React.useState("")
  const [path, setPath] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [removing, setRemoving] = React.useState<string | null>(null)
  const have = new Set((skills.data?.skills ?? []).map((s) => s.name))
  const available = (library.data?.skills ?? []).filter(
    (s) => !have.has(s.name)
  )

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
    <div className="flex flex-col gap-4">
      <p className="text-[12.5px] text-muted-foreground">
        Project skills in{" "}
        <code className="font-mono text-[11.5px]">.claude/skills</code> of the
        bot's folder. Adding one copies it in, so later edits to the original do
        not reach the bot.
      </p>
      <div className="flex flex-col gap-1.5">
        {skills.isPending && <Orb state="working" px={16} />}
        {skills.error && (
          <p className="text-[12px] text-destructive">
            {(skills.error as Error).message}
          </p>
        )}
        {skills.data?.skills.length === 0 && (
          <p className="text-[12.5px] text-muted-foreground">No skills yet.</p>
        )}
        {skills.data?.skills.map((s) => (
          <div
            key={s.name}
            className="flex items-start gap-2 rounded-lg border border-border bg-card px-3 py-2"
          >
            <div className="min-w-0 flex-1">
              <div className="font-medium font-mono text-[12.5px] text-foreground">
                {s.name}
              </div>
              {s.description && (
                <div className="line-clamp-2 text-[12px] text-muted-foreground">
                  {s.description}
                </div>
              )}
            </div>
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
          </div>
        ))}
      </div>
      <Field
        label={`From ${bot.host === "local" ? "this machine's" : `${bot.host}'s`} ~/.claude/skills`}
        htmlFor="bot-skill-pick"
      >
        <div className="flex gap-1.5">
          <select
            id="bot-skill-pick"
            className={cn(fieldClass, "min-w-0 flex-1")}
            value={pick}
            onChange={(e) => setPick(e.target.value)}
          >
            <option value="">
              {library.isPending
                ? "loading…"
                : available.length === 0
                  ? "nothing more to add"
                  : "choose a skill"}
            </option>
            {available.map((s) => (
              <option key={s.path} value={s.path}>
                {s.name}
              </option>
            ))}
          </select>
          <Button
            variant="outline"
            disabled={!pick || busy}
            onClick={() =>
              void run(
                () => api.bots.skillAdd(bot.name, pick),
                "could not add the skill"
              ).then((ok) => ok && setPick(""))
            }
          >
            Add
          </Button>
        </div>
      </Field>
      <Field
        label="Or from a path"
        htmlFor="bot-skill-path"
        hint="A skill directory (holding SKILL.md) on the bot's host."
      >
        <div className="flex gap-1.5">
          <input
            {...NO_AUTOCORRECT}
            id="bot-skill-path"
            value={path}
            onChange={(e) => setPath(e.target.value)}
            placeholder="~/skills/triage"
            className={cn(fieldClass, "min-w-0 flex-1 font-mono")}
          />
          <Button
            variant="outline"
            disabled={!path.trim() || busy}
            onClick={() =>
              void run(
                () => api.bots.skillAdd(bot.name, path.trim()),
                "could not add the skill"
              ).then((ok) => ok && setPath(""))
            }
          >
            Add
          </Button>
        </div>
      </Field>
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
  const [confirmKey, setConfirmKey] = React.useState(false)
  const ageKey = env.data?.age_key ?? true

  const run = async (fn: () => Promise<unknown>, fail: string) => {
    setBusy(true)
    try {
      await fn()
      await queryClient.invalidateQueries({ queryKey: key })
      return true
    } catch (e) {
      const msg =
        e instanceof ApiError && e.status === 412
          ? "this host has no encryption key yet"
          : (e as Error).message
      toast.error(`${fail}: ${msg}`)
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
        <code className="font-mono text-[11.5px]">mise.toml</code>, set when it
        launches. A secret is encrypted to the host's mise age key and only
        decrypted in memory at launch; its value is never shown again, only
        replaced. Changes reach a running bot on its next restart.
      </p>
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
                <span className="flex items-center gap-1 rounded bg-muted px-1.5 py-px text-[10.5px] text-muted-foreground">
                  <KeyRound className="size-3" />
                  secret
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
        {newSecret && !ageKey && (
          <div className="flex flex-col gap-2 rounded-lg bg-muted px-3 py-2 text-[12px] text-muted-foreground">
            <p>
              {bot.host === "local" ? "This machine" : bot.host} has no mise age
              key yet, so there is nothing to encrypt a secret to. Creating one
              writes a private key under{" "}
              <code className="font-mono">~/.config/mise/age.txt</code> on that
              host; every bot there will use it.
            </p>
            <Button
              variant="outline"
              size="sm"
              className="self-start"
              onClick={() => setConfirmKey(true)}
            >
              <KeyRound />
              Create encryption key
            </Button>
          </div>
        )}
        <Button
          className="self-start"
          disabled={
            busy || !newKey || keyBad || (newSecret && !ageKey) || !newValue
          }
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

      <AlertDialog open={confirmKey} onOpenChange={setConfirmKey}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Create an encryption key?</AlertDialogTitle>
            <AlertDialogDescription>
              lasso will generate a mise age key on{" "}
              {bot.host === "local" ? "this machine" : bot.host}. Secrets for
              every bot on that host are encrypted to it, and anyone who can
              read that file can decrypt them. Back it up: a lost key means
              setting every secret again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                void run(
                  () => api.bots.ageKey(bot.name),
                  "could not create the key"
                )
              }
            >
              Create key
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Launch

function LaunchTab({
  bot,
  launch,
  dirPath,
  onOpenTerminal,
}: {
  bot: BotView
  launch: string
  dirPath: string
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
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        The bot runs as <code className="font-mono">mise run bot</code> in{" "}
        <code className="font-mono">{dirPath}</code>: mise loads its
        environment, then runs the script below. lasso writes it from these
        settings on every save; edit the settings, not the file.
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

  const remove = async () => {
    if (!name) return
    try {
      await api.bots.delete(name)
      void invalidateBots(name)
      toast.success(`${name} deleted; its folder is still on ${bot?.host}`)
      onDeleted?.()
    } catch (e) {
      toast.error(`could not delete ${name}: ${(e as Error).message}`)
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
        <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 py-4">
          {detail.error ? (
            <p className="text-[12.5px] text-destructive">
              could not load {name}: {(detail.error as Error).message}
            </p>
          ) : !draft ? (
            <Orb state="working" px={20} />
          ) : tab === "general" ? (
            <GeneralTab draft={draft} set={set} creating={creating} />
          ) : tab === "connections" ? (
            <ConnectionsTab draft={draft} set={set} />
          ) : !bot || !detail.data ? null : tab === "skills" ? (
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
              disabled={running}
              title={running ? "Stop the bot before deleting it" : undefined}
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

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {name}?</AlertDialogTitle>
            <AlertDialogDescription>
              lasso forgets this bot. Its folder (
              {detail.data?.dir_path ?? bot?.dir}) stays on{" "}
              {bot?.host === "local" ? "this machine" : bot?.host}, with its
              CLAUDE.md, skills and environment, and its conversations stay in
              Claude Code's history.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => void remove()}
            >
              Delete bot
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

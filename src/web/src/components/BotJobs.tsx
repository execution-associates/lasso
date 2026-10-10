import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ChevronLeft,
  Copy,
  Eye,
  EyeOff,
  Hourglass,
  MoreHorizontal,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Save,
  Trash2,
  X,
  Zap,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { fieldClass, labelClass } from "@/components/ui/field"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import {
  api,
  type BotChannelState,
  type BotJob,
  type BotJobEvent,
  type BotView,
} from "@/lib/api"
import { botRunning, useMinuteClock } from "@/lib/bots"
import {
  type Builder,
  blankBuilder,
  builderProblem,
  EVERY_HOURS,
  EVERY_MINUTES,
  formatTime,
  fromCron,
  humanize,
  localTimeZone,
  type Repeat,
  timeZones,
  toCron,
  whenText,
  zoneAbbr,
} from "@/lib/cron"
import { cn } from "@/lib/utils"

// A bot's Jobs tab (docs/design/bots.md, "Jobs"): scheduled prompts and
// webhooks lasso delivers into the bot's session through its lasso-channel.
// Jobs are saved where they are edited, each on its own, like the folder's
// tabs: none of them needs the bot's row or a restart.

const JOB_NAME_RE = /^[a-z0-9][a-z0-9-]{0,39}$/

function jobsKey(bot: string) {
  return ["bot-jobs", bot]
}

// Enabled jobs by next fire (the top-left card is the next thing to happen),
// then the ones with no schedule, then the paused.
function sortJobs(jobs: BotJob[]): BotJob[] {
  const rank = (j: BotJob) => (!j.enabled ? 2 : j.next_at ? 0 : 1)
  return [...jobs].sort(
    (a, b) =>
      rank(a) - rank(b) ||
      (a.next_at && b.next_at ? a.next_at.localeCompare(b.next_at) : 0) ||
      a.name.localeCompare(b.name)
  )
}

function hookURL(j: BotJob): string {
  if (!j.webhook_path || !j.webhook_key) return ""
  return `${window.location.origin}${j.webhook_path}?key=${j.webhook_key}`
}

// navigator.clipboard needs a secure context, which a tailnet http:// lasso
// is not, so fall back to the old selection copy.
async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {}
  const ta = document.createElement("textarea")
  ta.value = text
  ta.setAttribute("readonly", "")
  ta.style.position = "fixed"
  ta.style.opacity = "0"
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand("copy")
  } catch {}
  ta.remove()
  return ok
}

async function copyHook(j: BotJob) {
  const url = hookURL(j)
  if (url && (await copyText(url))) toast.success("Webhook URL copied")
  else toast.error("Could not copy; open the job to see the URL")
}

// The schedule as people say it, with the zone: "Every day at 7:47 AM · PT".
function scheduleText(j: { cron: string; timezone: string }, short = false) {
  if (!j.cron) return null
  const h = humanize(j.cron)
  const words = h ? (short ? h.short : h.long) : "Custom schedule"
  return `${words} · ${zoneAbbr(j.timezone)}`
}

// ---------------------------------------------------------------------------

export function JobsTab({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const key = jobsKey(bot.name)
  const jobs = useQuery({
    queryKey: key,
    queryFn: () => api.bots.jobs(bot.name),
    refetchInterval: 5000,
  })
  // null: the grid. A name: that job's editor. "": a new job, seeded.
  const [editing, setEditing] = React.useState<string | null>(null)
  const [seed, setSeed] = React.useState<Partial<BotJob> | null>(null)
  const [deleting, setDeleting] = React.useState<BotJob | null>(null)
  const refresh = () => queryClient.invalidateQueries({ queryKey: key })

  const run = async (j: BotJob) => {
    try {
      const res = await api.bots.jobRun(bot.name, j.name)
      if (res.status === "dropped")
        toast.warning(`${bot.name} is stopped, so ${j.name} was not delivered`)
      else toast.success(`${j.name} queued for ${bot.name}`)
    } catch (e) {
      toast.error(`could not run ${j.name}: ${(e as Error).message}`)
    }
    void refresh()
  }
  const setEnabled = async (j: BotJob, enabled: boolean) => {
    try {
      await api.bots.jobUpdate(bot.name, j.name, { enabled })
      toast.success(`${j.name} ${enabled ? "resumed" : "paused"}`)
    } catch (e) {
      toast.error(`could not change ${j.name}: ${(e as Error).message}`)
    }
    void refresh()
  }
  const duplicate = (j: BotJob) => {
    setSeed({
      ...j,
      name: `${j.name}-copy`.slice(0, 40),
      webhook_key: undefined,
      webhook_path: undefined,
      last: undefined,
    })
    setEditing("")
  }
  const remove = async (j: BotJob) => {
    try {
      await api.bots.jobDelete(bot.name, j.name)
      toast.success(`${j.name} deleted`)
      if (editing === j.name) setEditing(null)
    } catch (e) {
      toast.error(`could not delete ${j.name}: ${(e as Error).message}`)
    }
    void refresh()
  }

  const channel = jobs.data?.channel
  const list = jobs.data?.jobs ?? []
  const current =
    editing === null
      ? undefined
      : editing === ""
        ? null
        : (list.find((j) => j.name === editing) ?? undefined)

  if (editing !== null && current !== undefined) {
    return (
      <JobEditor
        bot={bot}
        job={current}
        seed={current ? null : seed}
        onBack={() => {
          setEditing(null)
          setSeed(null)
        }}
        onSaved={(j) => {
          setSeed(null)
          setEditing(j.name)
          void refresh()
        }}
        onDelete={(j) => setDeleting(j)}
        onRun={(j) => void run(j)}
      />
    )
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start gap-3">
        <p className="min-w-0 flex-1 text-[12.5px] text-muted-foreground leading-relaxed">
          Scheduled prompts and webhooks delivered into {bot.name}'s session.
          Changes apply right away, no restart.
        </p>
        <Button
          size="sm"
          disabled={!channel?.available}
          onClick={() => {
            setSeed(null)
            setEditing("")
          }}
        >
          <Plus />
          New job
        </Button>
      </div>
      {channel && <ChannelNote bot={bot} channel={channel} />}
      {jobs.isPending && <Orb state="working" px={16} />}
      {jobs.error && (
        <p className="text-[12px] text-destructive">
          {(jobs.error as Error).message}
        </p>
      )}
      {jobs.data && (
        <div className="@container">
          <div className="grid @min-[560px]:grid-cols-2 @min-[900px]:grid-cols-3 grid-cols-1 gap-2.5">
            {sortJobs(list).map((j) => (
              <JobCard
                key={j.id}
                job={j}
                onOpen={() => setEditing(j.name)}
                onRun={() => void run(j)}
                onToggle={() => void setEnabled(j, !j.enabled)}
                onDuplicate={() => duplicate(j)}
                onDelete={() => setDeleting(j)}
              />
            ))}
            {channel?.available && (
              <button
                type="button"
                onClick={() => {
                  setSeed(null)
                  setEditing("")
                }}
                className="flex @min-[560px]:aspect-square min-h-24 flex-col items-center justify-center gap-1 rounded-lg border border-border border-dashed bg-card/40 p-3 text-[12.5px] text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground"
              >
                <Plus className="size-4" />
                New job
              </button>
            )}
          </div>
        </div>
      )}
      <AlertDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {deleting?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Its schedule stops, its webhook URL stops working and its history
              goes. {bot.name} keeps whatever it already did.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (deleting) void remove(deleting)
                setDeleting(null)
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// Whether jobs reach the bot right now, and what to do when they don't.
function ChannelNote({
  bot,
  channel,
}: {
  bot: BotView
  channel: BotChannelState
}) {
  const [restarting, setRestarting] = React.useState(false)
  if (!channel.available)
    return (
      <p className="rounded-lg bg-muted px-3 py-2 text-[12px] text-amber-500">
        Jobs aren't available for this bot: {channel.reason}.
      </p>
    )
  if (channel.connected)
    return (
      <p className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
        <span className="size-1.5 rounded-full bg-emerald-500" />
        Channel connected: {bot.name} is listening for jobs.
      </p>
    )
  if (!botRunning(bot))
    return (
      <p className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
        <span className="size-1.5 rounded-full bg-muted-foreground/50" />
        {bot.name} is stopped. Jobs that fire while it is stopped are dropped,
        not saved for later.
      </p>
    )
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg bg-muted px-3 py-2 text-[12px] text-muted-foreground">
      <span className="min-w-0 flex-1">
        {bot.name} isn't listening for jobs yet. It picks up lasso's channel the
        next time it starts.
      </span>
      <Button
        size="sm"
        variant="outline"
        disabled={restarting}
        onClick={async () => {
          setRestarting(true)
          try {
            await api.bots.restart(bot.name)
            toast.success(`${bot.name} restarting`)
          } catch (e) {
            toast.error(`could not restart: ${(e as Error).message}`)
          } finally {
            setRestarting(false)
          }
        }}
      >
        <RefreshCw />
        Restart {bot.name}
      </Button>
    </div>
  )
}

// The last delivery's mark: ✓ delivered, ✗ dropped (why, on hover), or
// waiting for the bot.
function LastMark({ ev }: { ev: BotJobEvent }) {
  if (ev.status === "delivered")
    return (
      <span className="text-emerald-500" title="Delivered">
        ✓
      </span>
    )
  if (ev.status === "dropped")
    return (
      <span className="text-destructive" title={ev.reason || "Dropped"}>
        ✗
      </span>
    )
  return (
    <span className="text-amber-500" title="Waiting for the bot">
      ⏳
    </span>
  )
}

function JobCard({
  job: j,
  onOpen,
  onRun,
  onToggle,
  onDuplicate,
  onDelete,
}: {
  job: BotJob
  onOpen: () => void
  onRun: () => void
  onToggle: () => void
  onDuplicate: () => void
  onDelete: () => void
}) {
  const now = useMinuteClock()
  const long = scheduleText(j)
  const short = scheduleText(j, true)
  const stop = (e: React.SyntheticEvent) => e.stopPropagation()
  return (
    // biome-ignore lint/a11y/useSemanticElements: the card holds its own buttons, which a <button> cannot.
    <div
      role="button"
      tabIndex={0}
      onClick={onOpen}
      onKeyDown={(e) => {
        if (
          e.target === e.currentTarget &&
          (e.key === "Enter" || e.key === " ")
        ) {
          e.preventDefault()
          onOpen()
        }
      }}
      className={cn(
        "fx-plate flex @min-[560px]:aspect-square min-w-0 cursor-pointer flex-col gap-2 overflow-hidden rounded-lg border border-border bg-card p-3 text-left transition-colors hover:border-primary/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        !j.enabled && "opacity-80"
      )}
    >
      <div className="flex min-w-0 items-center gap-1.5">
        <span
          role="img"
          aria-label={j.enabled ? "Enabled" : "Paused"}
          className={cn(
            "size-2 shrink-0 rounded-full",
            j.enabled
              ? "bg-primary"
              : "border border-muted-foreground/60 bg-transparent"
          )}
        />
        <span className="min-w-0 truncate font-medium font-mono text-[12.5px] text-foreground">
          {j.name}
        </span>
        <span className="ml-auto flex shrink-0 items-center gap-1">
          {j.webhook && (
            <span
              className="flex items-center gap-0.5 rounded bg-primary/15 px-1.5 py-px font-medium text-[10px] text-primary uppercase tracking-wide"
              title="Has a webhook URL"
            >
              <Zap className="size-2.5" />
              webhook
            </span>
          )}
          {!j.enabled && (
            <span className="rounded bg-muted px-1.5 py-px font-medium text-[10px] text-muted-foreground uppercase tracking-wide">
              paused
            </span>
          )}
          {j.queued > 0 && (
            <span
              className="flex items-center gap-0.5 rounded bg-amber-500/15 px-1.5 py-px font-medium text-[10px] text-amber-500 tracking-wide"
              title="Fired while the bot was busy or not listening; it gets them as one delivery"
            >
              <Hourglass className="size-2.5" />
              queued ×{j.queued}
            </span>
          )}
          {/* biome-ignore lint/a11y/noStaticElementInteractions: only stops the card's own click. */}
          {/* biome-ignore lint/a11y/useKeyWithClickEvents: the menu trigger inside handles keys. */}
          <span onClick={stop}>
            <DropdownMenu>
              <DropdownMenuTrigger
                className="flex size-6 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-foreground"
                aria-label={`${j.name}: more`}
                onKeyDown={stop}
              >
                <MoreHorizontal className="size-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-40">
                <DropdownMenuItem onSelect={onOpen}>Edit</DropdownMenuItem>
                <DropdownMenuItem onSelect={onToggle}>
                  {j.enabled ? <Pause /> : <Play />}
                  {j.enabled ? "Pause" : "Resume"}
                </DropdownMenuItem>
                <DropdownMenuItem onSelect={onDuplicate}>
                  <Copy />
                  Duplicate
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  onSelect={onDelete}
                  className="text-destructive focus:text-destructive"
                >
                  <Trash2 />
                  Delete…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </span>
        </span>
      </div>

      <div className="text-[13px] text-foreground leading-snug">
        {long ? (
          <>
            <span className="@min-[560px]:hidden">{short}</span>
            <span className="@min-[560px]:inline hidden">{long}</span>
          </>
        ) : (
          <>
            No schedule
            <span className="block text-[11.5px] text-muted-foreground">
              {j.webhook ? "Fires when called" : "Runs when you press Run now"}
            </span>
          </>
        )}
      </div>

      <dl className="grid grid-cols-[auto_1fr] gap-x-2 @min-[560px]:gap-y-0.5 text-[11.5px] text-muted-foreground">
        <dt className="@min-[560px]:block hidden">Next</dt>
        <dd className="@min-[560px]:block hidden truncate">
          {j.next_at ? whenText(j.next_at, now) : "—"}
        </dd>
        <dt className="@min-[560px]:block hidden">Last</dt>
        <dd className="@min-[560px]:block hidden truncate">
          {j.last ? (
            <>
              {whenText(j.last.fired_at, now)} <LastMark ev={j.last} />
            </>
          ) : (
            "never"
          )}
        </dd>
        <dd className="col-span-2 @min-[560px]:hidden truncate">
          {j.next_at ? `Next ${whenText(j.next_at, now)}` : "No next run"}
          {j.last && (
            <>
              {" · last "}
              {whenText(j.last.fired_at, now)} <LastMark ev={j.last} />
            </>
          )}
        </dd>
      </dl>

      {j.message && (
        <p className="@min-[560px]:block hidden min-h-0 flex-1 overflow-hidden text-[12px] text-muted-foreground leading-snug [mask-image:linear-gradient(to_bottom,black_70%,transparent)]">
          {j.message}
        </p>
      )}

      <div className="mt-auto flex items-center gap-1.5 border-border/60 @min-[560px]:border-t @min-[560px]:pt-2 pt-1">
        <Button
          size="xs"
          variant="outline"
          onClick={(e) => {
            stop(e)
            onRun()
          }}
        >
          <Play />
          Run now
        </Button>
        {j.webhook && (
          <Button
            size="xs"
            variant="ghost"
            onClick={(e) => {
              stop(e)
              void copyHook(j)
            }}
          >
            <Copy />
            Copy URL
          </Button>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// The editor

type JobDraft = {
  name: string
  message: string
  enabled: boolean
  scheduled: boolean
  builder: Builder
  timezone: string
  webhook: boolean
}

function draftOf(j?: Partial<BotJob> | null): JobDraft {
  const cron = j?.cron ?? ""
  return {
    name: j?.name ?? "",
    message: j?.message ?? "",
    enabled: j?.enabled ?? true,
    scheduled: j ? !!cron : true,
    builder: cron ? fromCron(cron) : blankBuilder(),
    timezone: j?.timezone || localTimeZone(),
    webhook: j?.webhook ?? false,
  }
}

function fieldsOf(d: JobDraft) {
  return {
    name: d.name.trim(),
    message: d.message,
    enabled: d.enabled,
    cron: d.scheduled ? toCron(d.builder) : "",
    timezone: d.timezone,
    webhook: d.webhook,
  }
}

const REPEATS: { id: Repeat; label: string }[] = [
  { id: "every", label: "Every…" },
  { id: "daily", label: "Daily" },
  { id: "weekdays", label: "Weekdays" },
  { id: "weekly", label: "Weekly" },
  { id: "monthly", label: "Monthly" },
  { id: "custom", label: "Custom" },
]

// Monday first, as a week is read.
const WEEK = [
  { d: 1, l: "M", n: "Monday" },
  { d: 2, l: "T", n: "Tuesday" },
  { d: 3, l: "W", n: "Wednesday" },
  { d: 4, l: "T", n: "Thursday" },
  { d: 5, l: "F", n: "Friday" },
  { d: 6, l: "S", n: "Saturday" },
  { d: 0, l: "S", n: "Sunday" },
]

function Section({
  title,
  aside,
  children,
}: {
  title: string
  aside?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="flex flex-col gap-2.5 rounded-lg border border-border bg-card p-3">
      <div className="flex items-center gap-2">
        <h3 className="font-medium text-[12.5px] text-foreground">{title}</h3>
        <span className="ml-auto" />
        {aside}
      </div>
      {children}
    </section>
  )
}

function Toggle({
  id,
  checked,
  onChange,
  label,
}: {
  id: string
  checked: boolean
  onChange: (v: boolean) => void
  label: string
}) {
  return (
    <span className="flex items-center gap-1.5">
      <Checkbox
        id={id}
        checked={checked}
        onCheckedChange={(v) => onChange(v === true)}
      />
      <label
        htmlFor={id}
        className="cursor-pointer text-[12px] text-muted-foreground"
      >
        {label}
      </label>
    </span>
  )
}

function JobEditor({
  bot,
  job,
  seed,
  onBack,
  onSaved,
  onDelete,
  onRun,
}: {
  bot: BotView
  // null: a new job.
  job: BotJob | null
  seed: Partial<BotJob> | null
  onBack: () => void
  onSaved: (j: BotJob) => void
  onDelete: (j: BotJob) => void
  onRun: (j: BotJob) => void
}) {
  const [draft, setDraft] = React.useState<JobDraft>(() => draftOf(job ?? seed))
  const [base, setBase] = React.useState(() =>
    JSON.stringify(fieldsOf(draftOf(job ?? null)))
  )
  const set = (patch: Partial<JobDraft>) =>
    setDraft((d) => ({ ...d, ...patch }))
  const setB = (patch: Partial<Builder>) =>
    setDraft((d) => ({ ...d, builder: { ...d.builder, ...patch } }))
  const [saving, setSaving] = React.useState(false)

  const fields = fieldsOf(draft)
  const dirty = !job || JSON.stringify(fields) !== base
  const nameBad = !!fields.name && !JOB_NAME_RE.test(fields.name)
  const problem = draft.scheduled ? builderProblem(draft.builder) : ""

  // The server checks the schedule and lists its next fires, so the preview
  // says exactly what the scheduler will do.
  const [preview, setPreview] = React.useState<{
    next?: string[]
    error?: string
  } | null>(null)
  React.useEffect(() => {
    if (!draft.scheduled || !fields.cron) {
      setPreview(null)
      return
    }
    let live = true
    const t = setTimeout(() => {
      api.bots
        .jobPreview(bot.name, fields.cron, draft.timezone)
        .then((p) => live && setPreview(p))
        .catch((e) => live && setPreview({ error: (e as Error).message }))
    }, 250)
    return () => {
      live = false
      clearTimeout(t)
    }
  }, [bot.name, fields.cron, draft.scheduled, draft.timezone])

  const missing = !fields.message.trim() && !fields.webhook
  const canSave =
    dirty &&
    !saving &&
    !!fields.name &&
    !nameBad &&
    !problem &&
    !preview?.error &&
    !missing

  const save = async () => {
    setSaving(true)
    try {
      const res = job
        ? await api.bots.jobUpdate(bot.name, job.name, fields)
        : await api.bots.jobCreate(bot.name, fields)
      setBase(JSON.stringify(fieldsOf(draftOf(res.job))))
      toast.success(`${res.job.name} saved`)
      onSaved(res.job)
    } catch (e) {
      toast.error(`could not save: ${(e as Error).message}`)
    } finally {
      setSaving(false)
    }
  }

  const human = fields.cron ? humanize(fields.cron) : null
  const zones = React.useMemo(timeZones, [])

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="ghost" size="sm" className="-ml-2" onClick={onBack}>
          <ChevronLeft />
          Jobs
        </Button>
        <span className="ml-auto" />
        {job && (
          <>
            <Button variant="outline" size="sm" onClick={() => onRun(job)}>
              <Play />
              Run now
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => onDelete(job)}
            >
              <Trash2 />
              Delete
            </Button>
          </>
        )}
        <Button size="sm" disabled={!canSave} onClick={() => void save()}>
          {saving ? <Orb state="working" px={14} on="accent" /> : <Save />}
          {job ? "Save" : "Create job"}
        </Button>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-0 flex-1 flex-col gap-1">
          <span className={labelClass}>Name</span>
          <input
            {...NO_AUTOCORRECT}
            value={draft.name}
            onChange={(e) => set({ name: e.target.value.toLowerCase() })}
            placeholder="inbox-sweep"
            aria-invalid={nameBad || undefined}
            className={cn(fieldClass, "font-mono")}
          />
        </label>
        <span className="pb-2">
          <Toggle
            id="job-enabled"
            checked={draft.enabled}
            onChange={(enabled) => set({ enabled })}
            label="Enabled"
          />
        </span>
      </div>
      {nameBad && (
        <p className="-mt-1 text-[11.5px] text-destructive">
          Lowercase letters, digits and dashes, starting with a letter or digit.
        </p>
      )}

      <label className="flex flex-col gap-1">
        <span className={labelClass}>Message</span>
        <textarea
          value={draft.message}
          onChange={(e) => set({ message: e.target.value })}
          rows={5}
          placeholder={`What ${bot.name} should do each time this job fires.`}
          className={cn(fieldClass, "resize-y leading-relaxed")}
        />
        <span className="text-[11px] text-muted-foreground">
          {draft.webhook
            ? "A webhook call's body is delivered after this, marked as the caller's."
            : `Delivered to ${bot.name} as your instruction each time the job fires.`}
        </span>
      </label>

      <Section
        title="Schedule"
        aside={
          <Toggle
            id="job-scheduled"
            checked={draft.scheduled}
            onChange={(scheduled) => set({ scheduled })}
            label="On"
          />
        }
      >
        {draft.scheduled ? (
          <ScheduleFields draft={draft} set={set} setB={setB} zones={zones} />
        ) : (
          <p className="text-[12px] text-muted-foreground">
            No schedule: it runs from its webhook or when you press Run now.
          </p>
        )}
        {draft.scheduled && (
          <div className="rounded-md bg-muted/60 px-3 py-2 text-[12px] sm:ml-[6.25rem]">
            {problem ? (
              <span className="text-muted-foreground">{problem}</span>
            ) : preview?.error ? (
              <span className="text-destructive">{preview.error}</span>
            ) : (
              <>
                <span className="block text-foreground">
                  {human
                    ? `${human.long}, ${zoneLong(draft.timezone)}`
                    : "Custom schedule"}
                </span>
                {preview?.next && preview.next.length > 0 && (
                  <span className="block text-muted-foreground">
                    Next:{" "}
                    {preview.next
                      .map((n) => whenText(n, Date.now()))
                      .join(" · ")}
                  </span>
                )}
              </>
            )}
          </div>
        )}
      </Section>

      <Section
        title="Webhook"
        aside={
          <Toggle
            id="job-webhook"
            checked={draft.webhook}
            onChange={(webhook) => set({ webhook })}
            label="On"
          />
        }
      >
        {!draft.webhook ? (
          <p className="text-[12px] text-muted-foreground">
            Off. Turn it on for a URL anything can POST to (CI, a deploy, a
            form) to fire this job.
          </p>
        ) : job?.webhook && job.webhook_key ? (
          <WebhookPanel bot={bot} job={job} />
        ) : (
          <p className="text-[12px] text-muted-foreground">
            Save the job to get its URL.
          </p>
        )}
      </Section>

      {missing && (
        <p className="text-[11.5px] text-muted-foreground">
          A job needs a message, or a webhook whose body becomes the message.
        </p>
      )}

      {job && <JobHistory bot={bot} job={job} />}
    </div>
  )
}

function zoneLong(tz: string): string {
  if (tz === "UTC") return "UTC"
  try {
    const part = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      timeZoneName: "longGeneric",
    })
      .formatToParts(new Date())
      .find((p) => p.type === "timeZoneName")
    if (part?.value) return part.value.replace(/ Time$/, " time")
  } catch {}
  return tz
}

function ScheduleFields({
  draft,
  set,
  setB,
  zones,
}: {
  draft: JobDraft
  set: (p: Partial<JobDraft>) => void
  setB: (p: Partial<Builder>) => void
  zones: string[]
}) {
  const b = draft.builder
  const [adding, setAdding] = React.useState("")
  const pick = (repeat: Repeat) => {
    // Custom starts from what the builder would write, so switching to it
    // shows the cron rather than an empty box.
    if (repeat === "custom" && b.repeat !== "custom")
      setB({ repeat, cron: toCron(b) })
    else setB({ repeat })
  }
  const addTime = () => {
    if (!/^\d\d:\d\d$/.test(adding)) return
    if (!b.times.includes(adding)) setB({ times: [...b.times, adding].sort() })
    setAdding("")
  }
  const atTimes = b.repeat !== "every" && b.repeat !== "custom"
  const pill = (on: boolean) =>
    cn(
      "h-8 rounded-full border px-3 text-[12px] transition-colors",
      on
        ? "border-primary/50 bg-primary/15 text-foreground"
        : "border-border text-muted-foreground hover:text-foreground"
    )
  // Every control is h-8, so a row of chips, fields and buttons shares one
  // baseline.
  const control = cn(fieldClass, "h-8 py-0")
  return (
    <div className="grid grid-cols-1 items-center gap-x-3 gap-y-3 sm:grid-cols-[5.5rem_minmax(0,1fr)]">
      <Label>Repeats</Label>
      <fieldset className="flex flex-wrap gap-1.5" aria-label="Repeats">
        {REPEATS.map((r) => (
          <button
            key={r.id}
            type="button"
            aria-pressed={b.repeat === r.id}
            onClick={() => pick(r.id)}
            className={pill(b.repeat === r.id)}
          >
            {r.label}
          </button>
        ))}
      </fieldset>

      {b.repeat === "every" && (
        <>
          <Label>Every</Label>
          <div className="flex items-center gap-1.5">
            <select
              value={b.everyN}
              onChange={(e) => setB({ everyN: Number(e.target.value) })}
              aria-label="Interval"
              className={cn(control, "w-20")}
            >
              {(b.everyUnit === "minutes" ? EVERY_MINUTES : EVERY_HOURS).map(
                (n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                )
              )}
            </select>
            <select
              value={b.everyUnit}
              onChange={(e) => {
                const everyUnit = e.target.value as Builder["everyUnit"]
                const list =
                  everyUnit === "minutes" ? EVERY_MINUTES : EVERY_HOURS
                setB({
                  everyUnit,
                  everyN: list.includes(b.everyN) ? b.everyN : list[0],
                })
              }}
              aria-label="Unit"
              className={cn(control, "w-28")}
            >
              <option value="minutes">minutes</option>
              <option value="hours">hours</option>
            </select>
          </div>
        </>
      )}

      {b.repeat === "weekly" && (
        <>
          <Label>On</Label>
          <div className="flex gap-1">
            {WEEK.map((w) => {
              const on = b.days.includes(w.d)
              return (
                <button
                  key={w.d}
                  type="button"
                  aria-pressed={on}
                  aria-label={w.n}
                  title={w.n}
                  onClick={() =>
                    setB({
                      days: on
                        ? b.days.filter((x) => x !== w.d)
                        : [...b.days, w.d],
                    })
                  }
                  className={cn(pill(on), "w-8 px-0")}
                >
                  {w.l}
                </button>
              )
            })}
          </div>
        </>
      )}

      {b.repeat === "monthly" && (
        <>
          <Label>On day</Label>
          <div className="flex flex-wrap items-center gap-2">
            <select
              value={b.dom}
              onChange={(e) => setB({ dom: Number(e.target.value) })}
              aria-label="Day of the month"
              className={cn(control, "w-20")}
            >
              {Array.from({ length: 31 }, (_, i) => i + 1).map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
            {b.dom > 28 && (
              <span className="text-[11.5px] text-muted-foreground">
                Months without a {b.dom}th are skipped.
              </span>
            )}
          </div>
        </>
      )}

      {atTimes && (
        <>
          <Label>At</Label>
          <div className="flex flex-wrap items-center gap-1.5">
            {b.times.map((t) => {
              const [h, m] = t.split(":").map(Number)
              return (
                <span
                  key={t}
                  className="flex h-8 items-center gap-1 rounded-full border border-border bg-background pr-1.5 pl-3 text-[12px]"
                >
                  {formatTime(h, m)}
                  <button
                    type="button"
                    aria-label={`Remove ${formatTime(h, m)}`}
                    onClick={() =>
                      setB({ times: b.times.filter((x) => x !== t) })
                    }
                    className="flex size-5 items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground"
                  >
                    <X className="size-3" />
                  </button>
                </span>
              )
            })}
            <span className="flex items-center gap-1.5">
              <input
                type="time"
                step={60}
                value={adding}
                onChange={(e) => setAdding(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && addTime()}
                aria-label="Add a time"
                className={cn(control, "w-32")}
              />
              <Button variant="outline" disabled={!adding} onClick={addTime}>
                <Plus />
                Add
              </Button>
            </span>
          </div>
        </>
      )}

      {b.repeat === "custom" && (
        <>
          <Label htmlFor="job-cron">Cron</Label>
          <div className="flex flex-col gap-1">
            <input
              {...NO_AUTOCORRECT}
              id="job-cron"
              value={b.cron}
              onChange={(e) => setB({ cron: e.target.value })}
              placeholder="0 9 * * 1-5"
              className={cn(control, "font-mono")}
            />
            <span className="text-[11px] text-muted-foreground">
              minute hour day-of-month month day-of-week. Join several with ";".
            </span>
          </div>
        </>
      )}

      <Label htmlFor="job-tz">Time zone</Label>
      <select
        id="job-tz"
        value={draft.timezone}
        onChange={(e) => set({ timezone: e.target.value })}
        className={cn(control, "max-w-sm")}
      >
        {(zones.includes(draft.timezone)
          ? zones
          : [draft.timezone, ...zones]
        ).map((z) => (
          <option key={z} value={z}>
            {z.replace(/_/g, " ")} ({zoneAbbr(z)})
          </option>
        ))}
      </select>
    </div>
  )
}

// A row label in the schedule's two-column grid.
function Label({
  htmlFor,
  children,
}: {
  htmlFor?: string
  children: React.ReactNode
}) {
  const cls = "text-[12px] text-muted-foreground"
  return htmlFor ? (
    <label htmlFor={htmlFor} className={cls}>
      {children}
    </label>
  ) : (
    <span className={cls}>{children}</span>
  )
}

function WebhookPanel({ bot, job }: { bot: BotView; job: BotJob }) {
  const queryClient = useQueryClient()
  const [shown, setShown] = React.useState(false)
  const [confirmRotate, setConfirmRotate] = React.useState(false)
  const url = hookURL(job)
  const masked = url.replace(/key=.*/, "key=••••••••")
  const now = useMinuteClock()
  const lastCall = job.last?.kind === "webhook" ? job.last : undefined
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-1.5">
        <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
          {shown ? url : masked}
        </code>
      </div>
      <div className="flex flex-wrap gap-1.5">
        <Button size="xs" variant="outline" onClick={() => void copyHook(job)}>
          <Copy />
          Copy
        </Button>
        <Button size="xs" variant="ghost" onClick={() => setShown(!shown)}>
          {shown ? <EyeOff /> : <Eye />}
          {shown ? "Hide" : "Show"}
        </Button>
        <Button
          size="xs"
          variant="ghost"
          onClick={() => setConfirmRotate(true)}
        >
          <RefreshCw />
          Rotate key
        </Button>
      </div>
      <div className="flex flex-col gap-1">
        <span className={labelClass}>Try it</span>
        <code className="block overflow-x-auto whitespace-pre rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
          {`curl -X POST '${shown ? url : masked}' \\\n  -d 'deploy finished: v1.2.3'`}
        </code>
        <span className="text-[11px] text-muted-foreground">
          The URL is the only credential: anyone who has it can put text in
          front of {bot.name}. A caller that sends headers can use{" "}
          <code className="font-mono">Authorization: Bearer &lt;key&gt;</code>{" "}
          instead of the query string.
        </span>
      </div>
      {lastCall && (
        <span className="text-[11.5px] text-muted-foreground">
          Last call {whenText(lastCall.fired_at, now)}
          {lastCall.source ? ` from ${lastCall.source}` : ""}{" "}
          <LastMark ev={lastCall} />
        </span>
      )}
      <AlertDialog open={confirmRotate} onOpenChange={setConfirmRotate}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Rotate {job.name}'s key?</AlertDialogTitle>
            <AlertDialogDescription>
              The current URL stops working at once. Anything that calls it
              needs the new one.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={async () => {
                try {
                  await api.bots.jobRotate(bot.name, job.name)
                  toast.success("New key made; the old URL no longer works")
                  setShown(true)
                } catch (e) {
                  toast.error(`could not rotate: ${(e as Error).message}`)
                }
                void queryClient.invalidateQueries({
                  queryKey: jobsKey(bot.name),
                })
              }}
            >
              Rotate
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

const KIND_LABEL: Record<BotJobEvent["kind"], string> = {
  schedule: "schedule",
  webhook: "webhook",
  run: "run now",
}

function JobHistory({ bot, job }: { bot: BotView; job: BotJob }) {
  const events = useQuery({
    queryKey: ["bot-job-events", bot.name, job.name],
    queryFn: () => api.bots.jobEvents(bot.name, job.name),
    refetchInterval: 5000,
  })
  const now = useMinuteClock()
  const list = events.data?.events ?? []
  return (
    <Section title="Recent">
      {list.length === 0 ? (
        <p className="text-[12px] text-muted-foreground">
          Nothing yet. Press Run now to try it.
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-border/60 text-[12px]">
          {list.map((e) => (
            <li
              key={e.id}
              className="flex flex-wrap items-center gap-x-3 py-1.5"
            >
              <span className="w-36 shrink-0 text-muted-foreground tabular-nums">
                {whenText(e.fired_at, now)}
              </span>
              <span className="w-20 shrink-0 text-muted-foreground">
                {KIND_LABEL[e.kind]}
              </span>
              <span className="flex min-w-0 flex-1 items-center gap-1.5">
                <LastMark ev={e} />
                <span
                  className={cn(
                    e.status === "dropped"
                      ? "text-destructive"
                      : "text-foreground"
                  )}
                >
                  {e.status === "claimed"
                    ? "delivering"
                    : e.status === "pending"
                      ? "queued"
                      : e.status}
                  {e.count > 1 ? ` ×${e.count}` : ""}
                </span>
                {(e.reason || (e.count > 1 && e.status !== "dropped")) && (
                  <span className="min-w-0 truncate text-muted-foreground">
                    {e.reason || "merged while busy"}
                  </span>
                )}
                {e.source && (
                  <span className="min-w-0 truncate text-muted-foreground">
                    from {e.source}
                  </span>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

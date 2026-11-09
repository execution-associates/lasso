import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Send, Trash2 } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { Pill } from "@/components/Pill"
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Field, flatFieldClass, labelClass } from "@/components/ui/field"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import {
  healthBad,
  healthLabel,
  healthTone,
  holdText,
  invalidateAlerts,
  minutesOf,
  routableChannels,
  secondsOf,
  useAlertChannels,
  useAlertKinds,
} from "@/lib/alerts"
import {
  type AlertChannel,
  type AlertChannelInput,
  type AlertKind,
  type AlertSettings,
  type AlertsOverview,
  type AlertTestResult,
  api,
} from "@/lib/api"
import { useBots } from "@/lib/bots"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// Settings → Bot alerts: the global defaults, the channels alerts go out on,
// and what was sent lately. A bot's own choices (which channels, its
// thresholds, its health checks, a hold) are in its own settings.

const sep = " · "

function useOverview(active: boolean) {
  return useQuery({
    queryKey: qk.alertsOverview,
    queryFn: () => api.alerts.overview(),
    enabled: active,
    refetchInterval: active ? 10_000 : false,
  })
}

function errText(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

function when(iso?: string): string {
  if (!iso) return ""
  const d = new Date(iso)
  return Number.isNaN(d.getTime())
    ? iso
    : d.toLocaleString(undefined, { dateStyle: "short", timeStyle: "short" })
}

export function BotAlertsSummary({ active }: { active: boolean }) {
  const o = useOverview(active)
  const ch = useAlertChannels(active)
  if (!o.data) return null
  const down = o.data.bots.filter((b) => b.alert.down).length
  const n = ch.data?.channels.length
  return (
    <>
      {o.data.settings.enabled ? "on" : "off"}
      {n !== undefined && `${sep}${n} ${n === 1 ? "channel" : "channels"}`}
      {down > 0 && (
        <>
          {sep}
          <span className="text-bad">{down} down</span>
        </>
      )}
    </>
  )
}

export function BotAlertsSettings({ active }: { active: boolean }) {
  const overview = useOverview(active)
  const channels = useAlertChannels(active)
  const kinds = useAlertKinds(active)
  const [editing, setEditing] = React.useState<{
    kind: AlertKind
    channel?: AlertChannel
  } | null>(null)
  const kindOf = (id: string) => kinds.data?.kinds.find((k) => k.id === id)

  if (overview.error)
    return (
      <p className="mb-4 text-[12px] text-destructive">
        could not load bot alerts: {errText(overview.error)}
      </p>
    )
  if (!overview.data) return null
  return (
    <div className="mb-4 flex flex-col gap-5">
      <GlobalSettings
        settings={overview.data.settings}
        channels={channels.data?.channels}
      />
      <section className="flex flex-col gap-2">
        <div className="flex items-center gap-2">
          <span className={labelClass}>Channels</span>
          <span className="ml-auto" />
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" disabled={!kinds.data}>
                <Plus />
                Add channel
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-44">
              {(kinds.data?.kinds ?? []).map((k) => (
                <DropdownMenuItem
                  key={k.id}
                  onSelect={() => setEditing({ kind: k })}
                >
                  {k.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
        {channels.data && channels.data.channels.length === 0 && (
          <p className="text-[11.5px] text-muted-foreground">
            No channels yet. Without one, a bot going down shows only as a
            notification and in the Inbox.
          </p>
        )}
        {channels.data?.channels.map((c) => (
          <ChannelRow
            key={c.id}
            channel={c}
            kindLabel={kindOf(c.kind)?.label ?? c.kind}
            onEdit={() => {
              const k = kindOf(c.kind)
              if (k) setEditing({ kind: k, channel: c })
            }}
          />
        ))}
      </section>
      <BotsStatus rows={overview.data.bots} />
      <Deliveries deliveries={overview.data.deliveries} />
      {editing && (
        <ChannelDialog
          kind={editing.kind}
          channel={editing.channel}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Global defaults

type SettingsDraft = {
  down_after: string
  up_after: string
  remind_every: string
  remind_max: string
  handle_within: string
  link_base: string
}

function settingsDraftOf(s: AlertSettings): SettingsDraft {
  return {
    down_after: minutesOf(s.down_after),
    up_after: minutesOf(s.up_after),
    remind_every: minutesOf(s.remind_every),
    remind_max: String(s.remind_max),
    handle_within: minutesOf(s.handle_within),
    link_base: s.link_base,
  }
}

function GlobalSettings({
  settings,
  channels,
}: {
  settings: AlertSettings
  channels?: AlertChannel[]
}) {
  const queryClient = useQueryClient()
  const routable = routableChannels(channels)
  // Seeded from the server and re-seeded only when the server's copy moves
  // and nothing here is being edited, so a poll never eats a half-typed value.
  const seed = JSON.stringify(settingsDraftOf(settings))
  const [draft, setDraft] = React.useState<SettingsDraft>(() =>
    settingsDraftOf(settings)
  )
  const [base, setBase] = React.useState(seed)
  const dirty = JSON.stringify(draft) !== base
  React.useEffect(() => {
    if (!dirty && seed !== base) {
      setDraft(JSON.parse(seed) as SettingsDraft)
      setBase(seed)
    }
  }, [seed, base, dirty])

  const save = useMutation({
    mutationFn: (patch: Partial<AlertSettings>) =>
      api.alerts.saveSettings(patch),
    // A toggle or a channel pick saves on its own and leaves a half-edited
    // threshold alone; the threshold save re-seeds the form itself.
    onSuccess: (s) => {
      queryClient.setQueryData(qk.alertSettings, s)
      queryClient.setQueryData<AlertsOverview>(qk.alertsOverview, (o) =>
        o ? { ...o, settings: s } : o
      )
      void invalidateAlerts()
    },
    onError: (e: Error) => toast.error(`could not save: ${e.message}`),
  })

  const saveThresholds = () => {
    const patch: Partial<AlertSettings> = {}
    const durs = [
      ["down_after", "Down after"],
      ["up_after", "Up after"],
      ["remind_every", "Remind every"],
      ["handle_within", "Handle within"],
    ] as const
    for (const [k, label] of durs) {
      const v = secondsOf(draft[k])
      if (v === undefined) {
        toast.error(`${label} needs a number of minutes`)
        return
      }
      patch[k] = v
    }
    const max = Number(draft.remind_max)
    if (!Number.isInteger(max) || max < 0) {
      toast.error("Reminders at most needs a whole number")
      return
    }
    patch.remind_max = max
    patch.link_base = draft.link_base.trim()
    save.mutate(patch, {
      onSuccess: (s) => {
        const d = settingsDraftOf(s)
        setDraft(d)
        setBase(JSON.stringify(d))
        toast.success("Bot alert defaults saved")
      },
    })
  }

  const setD = (patch: Partial<SettingsDraft>) =>
    setDraft((d) => ({ ...d, ...patch }))
  const toggleChannel = (id: number, on: boolean) =>
    save.mutate({
      channels: on
        ? [...settings.channels, id]
        : settings.channels.filter((x) => x !== id),
    })

  const num = (
    key: keyof SettingsDraft,
    label: string,
    hint: string,
    min = 0
  ) => (
    <Field label={label} htmlFor={`alerts-${key}`} hint={hint}>
      <input
        id={`alerts-${key}`}
        type="number"
        min={min}
        step="any"
        inputMode="decimal"
        value={draft[key]}
        onChange={(e) => setD({ [key]: e.target.value })}
        className={flatFieldClass}
      />
    </Field>
  )

  return (
    <section className="flex flex-col gap-3">
      <label
        className="flex cursor-pointer select-none items-center gap-2 text-[13px] text-foreground"
        htmlFor="alerts-enabled"
      >
        <Checkbox
          id="alerts-enabled"
          checked={settings.enabled}
          disabled={save.isPending}
          onCheckedChange={(c) => save.mutate({ enabled: c === true })}
        />
        Alert when a bot goes down or stops listening
      </label>
      <p className="-mt-2 text-[11px] text-muted-foreground">
        lasso checks every bot it runs. Off sends nothing on any channel;
        heartbeats keep pinging.
      </p>
      <div className="flex flex-col gap-1.5">
        <span className={labelClass}>Default channels</span>
        <p className="text-[11px] text-muted-foreground">
          Where every bot's alerts go unless its own settings choose others.
        </p>
        {routable.length === 0 ? (
          <p className="text-[11.5px] text-muted-foreground">
            No channels to choose yet.
          </p>
        ) : (
          routable.map((c) => (
            <label
              key={c.id}
              htmlFor={`alerts-default-${c.id}`}
              className="flex cursor-pointer items-center gap-2 text-[13px]"
            >
              <Checkbox
                id={`alerts-default-${c.id}`}
                checked={settings.channels.includes(c.id)}
                disabled={save.isPending}
                onCheckedChange={(v) => toggleChannel(c.id, v === true)}
              />
              <span className="truncate">{c.name}</span>
              {!c.enabled && (
                <span className="text-[11px] text-muted-foreground">
                  disabled
                </span>
              )}
            </label>
          ))
        )}
      </div>
      <div className="grid @md:grid-cols-2 gap-3">
        {num(
          "down_after",
          "Down after (minutes)",
          "How long a bot must look down before anyone is told."
        )}
        {num(
          "up_after",
          "Up after (minutes)",
          "How long it must look healthy again before a recovery."
        )}
        {num(
          "remind_every",
          "Remind every (minutes)",
          "While still down. 0 = no reminders, else at least 5."
        )}
        {num("remind_max", "Reminders at most", "Per outage, 0-100.")}
        {num(
          "handle_within",
          "Handle within (minutes)",
          "A job or message not taken up by then makes a bot deaf. At least 1.",
          1
        )}
        <Field
          label="Link base"
          htmlFor="alerts-link_base"
          hint="Makes an alert's link to the bot absolute, e.g. https://lasso.example.com. Empty sends no link."
        >
          <input
            {...NO_AUTOCORRECT}
            id="alerts-link_base"
            value={draft.link_base}
            onChange={(e) => setD({ link_base: e.target.value })}
            placeholder="https://…"
            className={cn(flatFieldClass, "font-mono")}
          />
        </Field>
      </div>
      <div className="flex items-center gap-2">
        <Button
          size="sm"
          disabled={!dirty || save.isPending}
          onClick={saveThresholds}
        >
          Save
        </Button>
        {dirty && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setDraft(JSON.parse(base) as SettingsDraft)}
          >
            Revert
          </Button>
        )}
      </div>
    </section>
  )
}

// ---------------------------------------------------------------------------
// Channels

function TestResult({ result }: { result?: AlertTestResult | null }) {
  if (!result) return null
  return result.ok ? (
    <span className="text-[11px] text-good">sent</span>
  ) : (
    <span className="min-w-0 text-[11px] text-destructive [overflow-wrap:anywhere]">
      {result.error || "failed"}
    </span>
  )
}

function ChannelRow({
  channel: c,
  kindLabel,
  onEdit,
}: {
  channel: AlertChannel
  kindLabel: string
  onEdit: () => void
}) {
  const [result, setResult] = React.useState<AlertTestResult | null>(null)
  const [confirmDelete, setConfirmDelete] = React.useState(false)
  const test = useMutation({
    mutationFn: () => api.alerts.testChannel(c.id),
    onMutate: () => setResult(null),
    onSuccess: (r) => {
      setResult(r)
      void invalidateAlerts()
    },
    onError: (e: Error) => setResult({ ok: false, error: e.message }),
  })
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      api.alerts.updateChannel(c.id, { enabled }),
    onSuccess: () => void invalidateAlerts(),
    onError: (e: Error) => toast.error(`could not change: ${e.message}`),
  })
  const del = useMutation({
    mutationFn: () => api.alerts.deleteChannel(c.id),
    onSuccess: () => {
      toast.success(`${c.name} deleted`)
      void invalidateAlerts()
    },
    onError: (e: Error) => toast.error(`could not delete: ${e.message}`),
  })
  return (
    <div className="flex flex-col gap-1 rounded-lg border border-border px-3 py-2">
      <div className="flex min-w-0 items-center gap-2">
        <Checkbox
          checked={c.enabled}
          disabled={toggle.isPending}
          onCheckedChange={(v) => toggle.mutate(v === true)}
          aria-label={`${c.name} enabled`}
          title={c.enabled ? "Enabled" : "Disabled"}
        />
        <span className="min-w-0 truncate font-medium text-[13px] text-foreground">
          {c.name}
        </span>
        <Pill className="px-1.5 text-[10px]">{kindLabel}</Pill>
        <span className="ml-auto" />
        <Button
          variant="ghost"
          size="icon-sm"
          title="Send a test"
          aria-label={`Send a test through ${c.name}`}
          disabled={test.isPending}
          onClick={() => test.mutate()}
        >
          <Send />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          title="Edit"
          aria-label={`Edit ${c.name}`}
          onClick={onEdit}
        >
          <Pencil />
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          title="Delete"
          aria-label={`Delete ${c.name}`}
          onClick={() => setConfirmDelete(true)}
        >
          <Trash2 />
        </Button>
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 text-[11px] text-muted-foreground">
        {c.target && <span className="truncate font-mono">{c.target}</span>}
        {c.last_test_at && <span>tested {when(c.last_test_at)}</span>}
        {c.last_error && (
          <span className="text-destructive [overflow-wrap:anywhere]">
            {c.last_error}
          </span>
        )}
        {test.isPending ? (
          <span>sending…</span>
        ) : (
          <TestResult result={result} />
        )}
      </div>
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {c.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Alerts stop going to it, and bots that chose it lose it.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => del.mutate()}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// The channel form, generated from the kind's fields. Secrets are
// write-only: a saved one shows as a placeholder and is sent only when typed
// over, or as "" when cleared.
function ChannelDialog({
  kind,
  channel,
  onClose,
}: {
  kind: AlertKind
  channel?: AlertChannel
  onClose: () => void
}) {
  const editing = !!channel
  const [name, setName] = React.useState(channel?.name ?? kind.label)
  const [enabled, setEnabled] = React.useState(channel?.enabled ?? true)
  const [values, setValues] = React.useState<Record<string, string>>(() => {
    const v: Record<string, string> = {}
    for (const f of kind.fields)
      if (!f.secret) v[f.key] = channel?.config[f.key] ?? ""
    return v
  })
  const [secrets, setSecrets] = React.useState<Record<string, string>>({})
  const [cleared, setCleared] = React.useState<Set<string>>(new Set())
  const [result, setResult] = React.useState<AlertTestResult | null>(null)
  const bots = useBots(false)
  const heartbeat = kind.id === "heartbeat"

  const body = (): AlertChannelInput => {
    const config: Record<string, string> = {}
    const sec: Record<string, string> = {}
    for (const f of kind.fields) {
      if (f.secret) {
        const typed = (secrets[f.key] ?? "").trim()
        if (typed) sec[f.key] = typed
        else if (cleared.has(f.key)) sec[f.key] = ""
      } else {
        const v = (values[f.key] ?? "").trim()
        if (v) config[f.key] = v
      }
    }
    return { name: name.trim(), kind: kind.id, enabled, config, secrets: sec }
  }

  const save = useMutation({
    mutationFn: () =>
      channel
        ? api.alerts.updateChannel(channel.id, body())
        : api.alerts.createChannel(body()),
    onSuccess: (r) => {
      toast.success(`${r.channel.name} ${editing ? "saved" : "added"}`)
      void invalidateAlerts()
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const test = useMutation({
    mutationFn: () => api.alerts.testDraft(body(), channel?.id),
    onMutate: () => setResult(null),
    onSuccess: setResult,
    onError: (e: Error) => setResult({ ok: false, error: e.message }),
  })

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            {editing ? `Edit ${channel.name}` : `New ${kind.label} channel`}
          </DialogTitle>
          <DialogDescription>{kind.help}</DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault()
            save.mutate()
          }}
        >
          <Field label="Name" htmlFor="alert-ch-name">
            <input
              {...NO_AUTOCORRECT}
              id="alert-ch-name"
              value={name}
              maxLength={60}
              onChange={(e) => setName(e.target.value)}
              className={flatFieldClass}
            />
          </Field>
          {kind.fields.map((f) => {
            const id = `alert-ch-${f.key}`
            const label = f.required ? f.label : `${f.label} (optional)`
            if (f.secret) {
              const saved =
                !!channel?.secrets_set.includes(f.key) && !cleared.has(f.key)
              return (
                <Field key={f.key} label={label} htmlFor={id} hint={f.help}>
                  <div className="flex items-center gap-1.5">
                    <input
                      {...NO_AUTOCORRECT}
                      id={id}
                      type="password"
                      autoComplete="off"
                      value={secrets[f.key] ?? ""}
                      onChange={(e) =>
                        setSecrets((s) => ({ ...s, [f.key]: e.target.value }))
                      }
                      placeholder={
                        saved
                          ? "Saved — type to replace"
                          : cleared.has(f.key)
                            ? "Cleared on save"
                            : ""
                      }
                      className={cn(flatFieldClass, "min-w-0 flex-1 font-mono")}
                    />
                    {!f.required && saved && !secrets[f.key] && (
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => setCleared((s) => new Set(s).add(f.key))}
                      >
                        Clear
                      </Button>
                    )}
                  </div>
                </Field>
              )
            }
            if (heartbeat && f.key === "bot") {
              const names = (bots.data?.bots ?? []).map((b) => b.name)
              const v = values[f.key] ?? ""
              return (
                <Field key={f.key} label={label} htmlFor={id} hint={f.help}>
                  <select
                    id={id}
                    value={v}
                    onChange={(e) =>
                      setValues((s) => ({ ...s, [f.key]: e.target.value }))
                    }
                    className={flatFieldClass}
                  >
                    <option value="">Choose a bot</option>
                    {v && !names.includes(v) && <option value={v}>{v}</option>}
                    {names.map((n) => (
                      <option key={n} value={n}>
                        {n}
                      </option>
                    ))}
                  </select>
                </Field>
              )
            }
            if (f.options?.length) {
              return (
                <Field key={f.key} label={label} htmlFor={id} hint={f.help}>
                  <select
                    id={id}
                    value={values[f.key] ?? ""}
                    onChange={(e) =>
                      setValues((s) => ({ ...s, [f.key]: e.target.value }))
                    }
                    className={flatFieldClass}
                  >
                    <option value="">
                      {f.default ? `default (${f.default})` : "default"}
                    </option>
                    {f.options.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </select>
                </Field>
              )
            }
            return (
              <Field key={f.key} label={label} htmlFor={id} hint={f.help}>
                <input
                  {...NO_AUTOCORRECT}
                  id={id}
                  value={values[f.key] ?? ""}
                  onChange={(e) =>
                    setValues((s) => ({ ...s, [f.key]: e.target.value }))
                  }
                  placeholder={f.default ?? ""}
                  className={flatFieldClass}
                />
              </Field>
            )
          })}
          <label
            htmlFor="alert-ch-enabled"
            className="flex cursor-pointer items-center gap-2 text-[13px]"
          >
            <Checkbox
              id="alert-ch-enabled"
              checked={enabled}
              onCheckedChange={(v) => setEnabled(v === true)}
            />
            Enabled
          </label>
          <div className="flex min-h-5 items-center gap-2">
            {test.isPending ? (
              <span className="text-[11px] text-muted-foreground">
                sending…
              </span>
            ) : (
              <TestResult result={result} />
            )}
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={test.isPending}
              onClick={() => test.mutate()}
            >
              <Send />
              Send test
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {editing ? "Save" : "Add"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---------------------------------------------------------------------------
// Status and history

function BotsStatus({ rows }: { rows: AlertsOverview["bots"] }) {
  if (rows.length === 0) return null
  return (
    <section className="flex flex-col gap-1">
      <span className={labelClass}>Bots</span>
      {rows.map((r) => (
        <div
          key={r.bot}
          className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-[12px]"
        >
          <span className="font-medium text-foreground">{r.bot}</span>
          {r.health && healthBad(r.health) ? (
            <Pill
              tone={healthTone(r.health)}
              title={r.health.reason}
              className="px-1.5 text-[10px]"
            >
              {healthLabel(r.health)}
            </Pill>
          ) : (
            <span className="text-muted-foreground">
              {r.health ? healthLabel(r.health).toLowerCase() : "not checked"}
            </span>
          )}
          {r.alert.down && (
            <span className="text-bad">
              alerted
              {r.alert.down_since && ` since ${when(r.alert.down_since)}`}
            </span>
          )}
          {r.hold.held && (
            <span className="text-muted-foreground">
              {holdText(r.hold).toLowerCase()}
            </span>
          )}
          {r.mode !== "inherit" && (
            <span className="text-muted-foreground">
              {r.mode === "off" ? "alerts off" : "own channels"}
            </span>
          )}
          {r.health?.reason && healthBad(r.health) && (
            <span className="min-w-0 basis-full truncate text-[11px] text-muted-foreground">
              {r.health.reason}
            </span>
          )}
        </div>
      ))}
    </section>
  )
}

const eventLabel: Record<string, string> = {
  down: "down",
  recovered: "recovered",
  reminder: "reminder",
  test: "test",
}

function Deliveries({
  deliveries,
}: {
  deliveries: AlertsOverview["deliveries"]
}) {
  const shown = deliveries.slice(0, 20)
  return (
    <section className="flex flex-col gap-1">
      <span className={labelClass}>Recent deliveries</span>
      {shown.length === 0 ? (
        <p className="text-[11.5px] text-muted-foreground">Nothing sent yet.</p>
      ) : (
        shown.map((d) => (
          <div
            key={d.id}
            className="flex min-w-0 flex-wrap items-baseline gap-x-2 text-[12px]"
          >
            <span className="shrink-0 text-[11px] text-muted-foreground tabular-nums">
              {when(d.at)}
            </span>
            <span className="text-foreground">{d.bot || "—"}</span>
            <span className="text-muted-foreground">
              {eventLabel[d.event] ?? d.event} → {d.channel_name}
            </span>
            <span
              className={cn(
                "text-[11px]",
                d.status === "sent" ? "text-good" : "text-destructive"
              )}
            >
              {d.status}
              {d.attempts > 1 ? ` after ${d.attempts} tries` : ""}
            </span>
            {d.error && (
              <span className="min-w-0 basis-full text-[11px] text-destructive [overflow-wrap:anywhere]">
                {d.error}
              </span>
            )}
          </div>
        ))
      )}
    </section>
  )
}

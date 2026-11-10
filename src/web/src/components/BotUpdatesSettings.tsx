import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"
import { toast } from "sonner"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, flatFieldClass, labelClass } from "@/components/ui/field"
import { api, type BotUpdates, type BotUpdatesPatch } from "@/lib/api"
import { timeZones, zoneAbbr } from "@/lib/cron"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// Settings → Bot updates (botupdates.go): once a day, at a time and in a
// timezone the human picks, lasso restarts each idle claude bot whose host has
// a newer Claude Code installed than the one it runs. Server-level, like the
// bot alerts beside it.

const sep = " · "

function useBotUpdates(active: boolean, bots: boolean) {
  return useQuery({
    queryKey: [...qk.botUpdates, bots ? "bots" : "plain"],
    queryFn: () => api.botUpdates(bots),
    enabled: active,
  })
}

// nextRunText is the next run on the chosen zone's clock, with how far off it
// is: "Tue 04:00 PT, in 7 h".
function nextRunText(d: BotUpdates): string {
  if (!d.next_run) return ""
  const t = Date.parse(d.next_run)
  if (Number.isNaN(t)) return d.next_run
  let when = d.next_run
  try {
    when = new Intl.DateTimeFormat(undefined, {
      timeZone: d.timezone,
      weekday: "short",
      hour: "2-digit",
      minute: "2-digit",
    }).format(new Date(t))
  } catch {}
  const mins = Math.max(0, Math.round((t - Date.now()) / 60_000))
  const away = mins < 60 ? `${mins} min` : `${Math.round(mins / 60)} h`
  return `${when} ${zoneAbbr(d.timezone)}, in ${away}`
}

export function BotUpdatesSummary({ active }: { active: boolean }) {
  const q = useBotUpdates(active, false)
  const d = q.data
  if (!d) return null
  if (!d.enabled) return <>off</>
  return (
    <>
      daily at {d.time} {zoneAbbr(d.timezone)}
      {d.in_window && `${sep}running`}
    </>
  )
}

export function BotUpdatesSettings({ active }: { active: boolean }) {
  const queryClient = useQueryClient()
  const q = useBotUpdates(active, true)
  const d = q.data
  const zones = React.useMemo(timeZones, [])
  // The time is edited locally and saved on blur: a time input reports every
  // keystroke, and a half-typed hour must not be stored.
  const [time, setTime] = React.useState("")
  React.useEffect(() => {
    if (d) setTime(d.time)
  }, [d])

  const save = useMutation({
    mutationFn: (patch: BotUpdatesPatch) => api.setBotUpdates(patch),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: qk.botUpdates }),
    onError: (e: Error) =>
      toast.error(`Couldn't save bot updates: ${e.message}`),
  })

  if (q.error)
    return (
      <p className="mb-4 text-[12px] text-destructive">
        could not load bot updates: {(q.error as Error).message}
      </p>
    )
  if (!d)
    return <p className="mb-4 text-[12px] text-muted-foreground">reading…</p>

  const zoneList = zones.includes(d.timezone) ? zones : [d.timezone, ...zones]
  const hours = Math.round(d.window_minutes / 60)
  return (
    <div className="mb-4 flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <label
          className="flex cursor-pointer select-none items-center gap-2 text-[13px] text-foreground"
          htmlFor="settings-bot-updates"
        >
          <Checkbox
            id="settings-bot-updates"
            checked={d.enabled}
            disabled={save.isPending}
            onCheckedChange={(c) => save.mutate({ enabled: c === true })}
          />
          Restart bots daily onto a newer Claude Code
        </label>
        <p className="text-[11px] text-muted-foreground">
          Claude Code updates itself, but a running bot keeps the version it
          started with until it restarts. Once a day lasso restarts each claude
          bot whose host has a newer one installed, resuming its conversation. A
          bot in the middle of a turn or waiting on you is left alone until it
          is idle, for up to {hours} {hours === 1 ? "hour" : "hours"}; after
          that it waits for the next day. omp bots are not affected.
        </p>
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <Field label="Time of day" htmlFor="settings-bot-updates-time">
          <input
            id="settings-bot-updates-time"
            type="time"
            step={60}
            value={time}
            onChange={(e) => setTime(e.target.value)}
            onBlur={() => {
              if (time && time !== d.time) save.mutate({ time })
            }}
            className={cn(flatFieldClass, "w-32")}
          />
        </Field>
        <Field label="Time zone" htmlFor="settings-bot-updates-tz">
          <select
            id="settings-bot-updates-tz"
            value={d.timezone}
            onChange={(e) => save.mutate({ timezone: e.target.value })}
            className={cn(flatFieldClass, "max-w-xs")}
          >
            {zoneList.map((z) => (
              <option key={z} value={z}>
                {z.replace(/_/g, " ")} ({zoneAbbr(z)})
              </option>
            ))}
          </select>
        </Field>
      </div>
      {d.reason && <p className="text-[11px] text-destructive">{d.reason}</p>}
      {d.enabled && (
        <p className="text-[11px] text-muted-foreground">
          {d.in_window
            ? `Today's run is under way. Next: ${nextRunText(d)}.`
            : `Next run: ${nextRunText(d)}.`}
        </p>
      )}
      <BotVersions bots={d.bots} />
    </div>
  )
}

// BotVersions lists the running claude bots with the Claude Code each runs
// and the one installed on its host, so the next run's restarts are visible.
function BotVersions({ bots }: { bots: BotUpdates["bots"] }) {
  if (bots.length === 0)
    return (
      <p className="text-[11px] text-muted-foreground">
        No claude bots are running.
      </p>
    )
  return (
    <div className="flex flex-col gap-1">
      <span className={labelClass}>Running claude bots</span>
      <ul className="flex flex-col gap-0.5 text-[12px]">
        {bots.map((b) => (
          <li key={`${b.host}/${b.name}`} className="flex flex-wrap gap-x-2">
            <span className="font-medium text-foreground">{b.name}</span>
            {b.host !== "local" && (
              <span className="text-muted-foreground">on {b.host}</span>
            )}
            <span className="font-mono text-muted-foreground">
              {b.running || "?"}
              {b.outdated && b.installed ? ` → ${b.installed}` : ""}
            </span>
            {b.outdated && <span className="text-warn">update pending</span>}
            {b.error && (
              <span className="text-destructive">
                can't read the installed version: {b.error}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  )
}

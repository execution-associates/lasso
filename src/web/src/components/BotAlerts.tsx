import { Plus, X } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { Pill } from "@/components/Pill"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Field, fieldClass, labelClass } from "@/components/ui/field"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import {
  healthBad,
  healthLabel,
  holdBot,
  holdText,
  minutesOf,
  releaseBot,
  routableChannels,
  secondsOf,
  useAlertChannels,
  useAlertSettings,
} from "@/lib/alerts"
import type { BotAlerts, BotView } from "@/lib/api"
import { cn } from "@/lib/utils"

// A bot's own half of liveness alerting, in its settings' General tab: the
// alerts block is part of the bot's row (saved by the footer with the rest),
// the hold is not (it takes effect at once, from the buttons).

let rowSeq = 0
const nextID = () => ++rowSeq

type DraftCheck = { id: number; name: string; url: string; json_field: string }

// The alerts block as the form edits it. Thresholds are minutes as typed,
// "" = inherit the global one.
export type AlertsDraft = {
  mode: "" | "off" | "custom"
  channels: number[]
  down_after: string
  up_after: string
  remind_every: string
  handle_within: string
  checks: DraftCheck[]
}

export function alertsDraftOf(a?: BotAlerts | null): AlertsDraft {
  return {
    mode: a?.mode ?? "",
    channels: a?.channels ?? [],
    down_after: minutesOf(a?.down_after),
    up_after: minutesOf(a?.up_after),
    remind_every: minutesOf(a?.remind_every),
    handle_within: minutesOf(a?.handle_within),
    checks: (a?.health_checks ?? []).map((c) => ({
      id: nextID(),
      name: c.name,
      url: c.url,
      json_field: c.json_field ?? "",
    })),
  }
}

// null when nothing is set, which inherits everything.
export function alertsOf(d: AlertsDraft): BotAlerts | null {
  const out: BotAlerts = {}
  if (d.mode) out.mode = d.mode
  if (d.mode === "custom" && d.channels.length > 0)
    out.channels = [...d.channels].sort((a, b) => a - b)
  const dur = {
    down_after: secondsOf(d.down_after),
    up_after: secondsOf(d.up_after),
    remind_every: secondsOf(d.remind_every),
    handle_within: secondsOf(d.handle_within),
  }
  for (const [k, v] of Object.entries(dur))
    if (v !== undefined) out[k as keyof typeof dur] = v
  const checks = d.checks
    .filter((c) => c.url.trim())
    .map((c) => ({
      name: c.name.trim(),
      url: c.url.trim(),
      ...(c.json_field.trim() ? { json_field: c.json_field.trim() } : {}),
    }))
  if (checks.length > 0) out.health_checks = checks
  return Object.keys(out).length > 0 ? out : null
}

export function BotAlertsSection({
  draft,
  set,
  bot,
}: {
  draft: AlertsDraft
  set: (patch: Partial<AlertsDraft>) => void
  bot?: BotView
}) {
  const channels = useAlertChannels()
  const settings = useAlertSettings()
  const routable = routableChannels(channels.data?.channels)
  const g = settings.data
  const heartbeats = (channels.data?.channels ?? []).filter(
    (c) => c.kind === "heartbeat" && bot && c.config.bot === bot.name
  )
  const toggle = (id: number, on: boolean) =>
    set({
      channels: on
        ? [...draft.channels, id]
        : draft.channels.filter((x) => x !== id),
    })
  const setCheck = (id: number, patch: Partial<DraftCheck>) =>
    set({
      checks: draft.checks.map((c) => (c.id === id ? { ...c, ...patch } : c)),
    })
  const inherit = (sec?: number) =>
    sec === undefined ? "default" : `${minutesOf(sec)} (default)`

  return (
    <section className="fx-tile flex flex-col gap-4 rounded-lg border border-border p-4">
      <div className="flex flex-col gap-0.5">
        <span className="font-medium text-[13px] text-foreground">Alerts</span>
        <p className="text-[11.5px] text-muted-foreground">
          Who hears about it when this bot goes down or stops listening, and
          when it recovers.
          {g && !g.enabled && (
            <span className="text-warn">
              {" "}
              Alerts are off for every bot (Settings, Bot alerts).
            </span>
          )}
        </p>
      </div>
      <Field label="Send to" htmlFor="bot-alerts-mode">
        <select
          id="bot-alerts-mode"
          className={fieldClass}
          value={draft.mode}
          onChange={(e) => set({ mode: e.target.value as AlertsDraft["mode"] })}
        >
          <option value="">The default channels</option>
          <option value="custom">Chosen channels</option>
          <option value="off">Nobody (alerts off for this bot)</option>
        </select>
      </Field>
      {draft.mode === "custom" && (
        <div className="flex flex-col gap-1.5">
          <span className={labelClass}>Channels</span>
          {routable.length === 0 ? (
            <p className="text-[11.5px] text-muted-foreground">
              No channels yet. Add one in Settings, Bot alerts.
            </p>
          ) : (
            routable.map((c) => (
              <div key={c.id} className="flex items-center gap-2">
                <Checkbox
                  id={`bot-alert-ch-${c.id}`}
                  checked={draft.channels.includes(c.id)}
                  onCheckedChange={(v) => toggle(c.id, v === true)}
                />
                <label
                  htmlFor={`bot-alert-ch-${c.id}`}
                  className="flex min-w-0 cursor-pointer items-center gap-1.5 text-[13px]"
                >
                  <span className="truncate">{c.name}</span>
                  <span className="truncate text-[11px] text-muted-foreground">
                    {c.kind}
                    {c.target ? ` · ${c.target}` : ""}
                    {c.enabled ? "" : " · disabled"}
                  </span>
                </label>
              </div>
            ))
          )}
          {/* Channels saved on the bot that are gone or became a heartbeat. */}
          {draft.channels.some((id) => !routable.some((c) => c.id === id)) &&
            channels.data && (
              <p className="text-[11px] text-warn">
                Some chosen channels no longer exist; saving drops them.
              </p>
            )}
        </div>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        <Field
          label="Down after (minutes)"
          htmlFor="bot-alert-down"
          hint="How long it must look down before anyone is told."
        >
          <input
            id="bot-alert-down"
            type="number"
            min={0}
            step="any"
            inputMode="decimal"
            value={draft.down_after}
            onChange={(e) => set({ down_after: e.target.value })}
            placeholder={inherit(g?.down_after)}
            className={fieldClass}
          />
        </Field>
        <Field
          label="Up after (minutes)"
          htmlFor="bot-alert-up"
          hint="How long it must look healthy again before a recovery."
        >
          <input
            id="bot-alert-up"
            type="number"
            min={0}
            step="any"
            inputMode="decimal"
            value={draft.up_after}
            onChange={(e) => set({ up_after: e.target.value })}
            placeholder={inherit(g?.up_after)}
            className={fieldClass}
          />
        </Field>
        <Field
          label="Remind every (minutes)"
          htmlFor="bot-alert-remind"
          hint="While still down. 0 = no reminders, else at least 5."
        >
          <input
            id="bot-alert-remind"
            type="number"
            min={0}
            step="any"
            inputMode="decimal"
            value={draft.remind_every}
            onChange={(e) => set({ remind_every: e.target.value })}
            placeholder={
              g
                ? g.remind_every
                  ? inherit(g.remind_every)
                  : "off (default)"
                : ""
            }
            className={fieldClass}
          />
        </Field>
        <Field
          label="Handle within (minutes)"
          htmlFor="bot-alert-handle"
          hint="A job or message it has not taken up by then makes it deaf. At least 1."
        >
          <input
            id="bot-alert-handle"
            type="number"
            min={1}
            step="any"
            inputMode="decimal"
            value={draft.handle_within}
            onChange={(e) => set({ handle_within: e.target.value })}
            placeholder={inherit(g?.handle_within)}
            className={fieldClass}
          />
        </Field>
      </div>
      <div className="flex flex-col gap-1.5">
        <span className={labelClass}>Health checks</span>
        <p className="text-[11px] text-muted-foreground">
          HTTP endpoints it depends on, such as a bridge's /health. One is
          failing on anything but a 2xx, or, with a JSON field, unless that
          field of the answer is true.
        </p>
        {draft.checks.map((c) => (
          <div key={c.id} className="flex flex-wrap items-center gap-1.5">
            <input
              {...NO_AUTOCORRECT}
              value={c.name}
              onChange={(e) => setCheck(c.id, { name: e.target.value })}
              placeholder="name"
              aria-label="Check name"
              maxLength={40}
              className={cn(fieldClass, "w-28 min-w-0")}
            />
            <input
              {...NO_AUTOCORRECT}
              value={c.url}
              onChange={(e) => setCheck(c.id, { url: e.target.value })}
              placeholder="http://127.0.0.1:8080/health"
              aria-label="Check URL"
              className={cn(fieldClass, "min-w-40 flex-1 font-mono")}
            />
            <input
              {...NO_AUTOCORRECT}
              value={c.json_field}
              onChange={(e) => setCheck(c.id, { json_field: e.target.value })}
              placeholder="json field"
              aria-label="JSON field"
              className={cn(fieldClass, "w-28 min-w-0 font-mono")}
            />
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Remove check"
              title="Remove"
              onClick={() =>
                set({ checks: draft.checks.filter((x) => x.id !== c.id) })
              }
            >
              <X />
            </Button>
          </div>
        ))}
        {draft.checks.length < 8 && (
          <Button
            variant="ghost"
            size="sm"
            className="self-start"
            onClick={() =>
              set({
                checks: [
                  ...draft.checks,
                  { id: nextID(), name: "", url: "", json_field: "" },
                ],
              })
            }
          >
            <Plus />
            Add a check
          </Button>
        )}
      </div>
      {heartbeats.length > 0 && (
        <div className="flex flex-col gap-1">
          <span className={labelClass}>Heartbeats</span>
          {heartbeats.map((c) => (
            <p key={c.id} className="text-[12px] text-muted-foreground">
              {c.name}
              {c.target ? ` · ${c.target}` : ""}
              {c.enabled ? "" : " · disabled"}
              {c.last_error && (
                <span className="text-destructive"> · {c.last_error}</span>
              )}
            </p>
          ))}
        </div>
      )}
    </section>
  )
}

// The bot's health as the runner last saw it, and its hold. The hold buttons
// call the API at once; they are not part of the settings save.
export function BotLivenessSection({ bot }: { bot: BotView }) {
  const [reason, setReason] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const held = !!bot.hold?.held
  const run = async (fn: () => Promise<unknown>, what: string) => {
    setBusy(true)
    try {
      await fn()
      setReason("")
    } catch (e) {
      toast.error(`could not ${what}: ${(e as Error).message}`)
    } finally {
      setBusy(false)
    }
  }
  const h = bot.health
  const reasonArg = reason.trim() ? { reason: reason.trim() } : {}
  return (
    <section className="fx-tile flex flex-col gap-3 rounded-lg border border-border p-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-[13px] text-foreground">Health</span>
        {h ? (
          <Pill
            tone={
              healthBad(h)
                ? h.state === "unknown"
                  ? "warn"
                  : "bad"
                : h.state === "ok"
                  ? "good"
                  : "muted"
            }
          >
            {healthLabel(h)}
          </Pill>
        ) : (
          <span className="text-[11.5px] text-muted-foreground">
            not checked yet
          </span>
        )}
        {h?.since && (
          <span className="text-[11px] text-muted-foreground">
            since {new Date(h.since).toLocaleString()}
          </span>
        )}
      </div>
      {h?.reason && (
        <p className="text-[12px] text-muted-foreground">{h.reason}</p>
      )}
      <div className="flex flex-col gap-1.5">
        <span className={labelClass}>Hold</span>
        <p className="text-[11.5px] text-muted-foreground">
          {held
            ? holdText(bot.hold)
            : "A deliberate down: while held, lasso does not bring it back and its being gone pages nobody. A bot that is up but not listening still alerts."}
        </p>
        {!held && (
          <input
            {...NO_AUTOCORRECT}
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            maxLength={200}
            placeholder="Reason (optional)"
            aria-label="Hold reason"
            className={cn(fieldClass, "max-w-sm")}
          />
        )}
        <div className="flex flex-wrap gap-1.5">
          {held ? (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() =>
                void run(() => releaseBot(bot.name), "release the hold")
              }
            >
              Release
            </Button>
          ) : (
            <>
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() =>
                  void run(
                    () => holdBot(bot.name, { minutes: 60, ...reasonArg }),
                    "hold"
                  )
                }
              >
                Hold 1h
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={busy}
                onClick={() =>
                  void run(() => holdBot(bot.name, reasonArg), "hold")
                }
              >
                Hold until released
              </Button>
            </>
          )}
        </div>
      </div>
    </section>
  )
}

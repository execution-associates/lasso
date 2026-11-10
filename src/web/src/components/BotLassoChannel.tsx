import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Copy, Eye, EyeOff, Radio, RefreshCw } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import { copyText } from "@/components/BotJobs"
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
import { labelClass } from "@/components/ui/field"
import { Orb } from "@/components/ui/orb"
import {
  api,
  type BotChannel,
  type BotChannelSource,
  type BotView,
} from "@/lib/api"
import { cn } from "@/lib/utils"

const channelKey = (name: string) => ["bot-lasso-channel", name]

async function copy(text: string, what: string) {
  if (await copyText(text)) toast.success(`${what} copied`)
  else toast.error(`Could not copy the ${what.toLowerCase()}`)
}

const sourceKind: Record<BotChannelSource["kind"], string> = {
  server: "Connection",
  job: "Job",
  talk: "Talk",
  phone: "People",
}

// The bot's Lasso channel: how messages reach it without being asked. The
// channel is always on; the panel says so, lists what is sending the bot
// messages through it now, and holds the one optional switch, the webhook
// that lets anything else post to it. Everything on it is the server's
// (botpush.go).
export function BotLassoChannel({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const channel = useQuery({
    queryKey: channelKey(bot.name),
    queryFn: () => api.bots.channel(bot.name),
    refetchInterval: 5000,
  })
  const [busy, setBusy] = React.useState(false)
  const act = async (fn: () => Promise<BotChannel>, done?: string) => {
    setBusy(true)
    try {
      queryClient.setQueryData(channelKey(bot.name), await fn())
      if (done) toast.success(done)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const c = channel.data
  const on = c?.sources.filter((s) => s.on) ?? []
  return (
    <div className="flex flex-col gap-4 rounded-lg border border-border p-4">
      <div className="flex flex-col gap-2">
        <span className="flex items-center gap-2">
          <Radio className="size-4 shrink-0 text-primary" />
          <span className="font-medium text-[13px] text-foreground">
            Lasso channel
          </span>
          {c && <Status channel={c} />}
        </span>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          How messages reach {bot.name} without being asked. It is always on:
          lasso hands them to the bot{" "}
          {(c?.runtime ?? bot.runtime) === "claude"
            ? "as Claude Code channel events."
            : "through its omp plugin."}
        </p>
      </div>
      {channel.isPending && <Orb state="working" px={16} />}
      {channel.error && (
        <p className="text-[12px] text-destructive">
          {(channel.error as Error).message}
        </p>
      )}
      {c && !c.available && (
        <p className="text-[12px] text-destructive">
          Lasso cannot deliver messages to this bot: {c.reason}
        </p>
      )}
      {c?.available && (
        <>
          <div className="flex flex-col gap-1.5">
            <span className={labelClass}>Sending {bot.name} messages now</span>
            {c.sources.length === 0 && !c.webhook.enabled ? (
              <p className="text-[12px] text-muted-foreground">
                Nothing yet. Mark a connection below as a channel, add a job
                under Jobs, or turn on the webhook.
              </p>
            ) : (
              <ul className="flex flex-col gap-1">
                {c.sources.map((s) => (
                  <SourceRow key={`${s.kind}:${s.name}`} source={s} />
                ))}
                {c.webhook.enabled && (
                  <SourceRow
                    source={{
                      kind: "server",
                      name: "anything with the key",
                      detail: "set up below",
                      on: true,
                    }}
                    kindLabel="Webhook"
                  />
                )}
              </ul>
            )}
            {c.stopped && on.length > 0 && (
              <p className="text-[11.5px] text-amber-500">
                {bot.name} is stopped, so messages are dropped until it is
                started.
              </p>
            )}
          </div>
          <Webhook bot={bot} channel={c} busy={busy} act={act} />
        </>
      )}
    </div>
  )
}

function Status({ channel }: { channel: BotChannel }) {
  const [label, tone] = !channel.available
    ? ["Unavailable", "bg-destructive/15 text-destructive"]
    : channel.stopped
      ? ["Bot stopped", "bg-amber-500/15 text-amber-500"]
      : ["On", "bg-emerald-500/15 text-emerald-500"]
  return (
    <span
      className={cn(
        "rounded px-1.5 py-px font-medium text-[10px] uppercase tracking-wide",
        tone
      )}
    >
      {label}
    </span>
  )
}

function SourceRow({
  source,
  kindLabel,
}: {
  source: BotChannelSource
  kindLabel?: string
}) {
  return (
    <li className="flex min-w-0 items-baseline gap-2 text-[12px]">
      <span
        className={cn(
          "size-1.5 shrink-0 translate-y-[-1px] rounded-full",
          source.on ? "bg-emerald-500" : "bg-muted-foreground/40"
        )}
      />
      <span className="w-20 shrink-0 text-[10.5px] text-muted-foreground uppercase tracking-wide">
        {kindLabel ?? sourceKind[source.kind]}
      </span>
      <span
        className={cn(
          "shrink-0 font-mono",
          source.on ? "text-foreground" : "text-muted-foreground"
        )}
      >
        {source.name}
      </span>
      <span className="min-w-0 truncate text-muted-foreground">
        {source.on || !source.detail || kindLabel
          ? source.detail
          : `${source.detail} (off)`}
      </span>
    </li>
  )
}

// The channel's webhook: the one part of it that is off until turned on.
function Webhook({
  bot,
  channel,
  busy,
  act,
}: {
  bot: BotView
  channel: BotChannel
  busy: boolean
  act: (fn: () => Promise<BotChannel>, done?: string) => Promise<void>
}) {
  const [shown, setShown] = React.useState(false)
  const [confirm, setConfirm] = React.useState<"rotate" | "off" | null>(null)
  const hook = channel.webhook
  const url = `${window.location.origin}${hook.path}`
  const key = hook.key ?? ""
  const masked = `${key.slice(0, 5)}••••••••`
  const curlWith = (k: string) =>
    `curl -X POST ${url} -H "Authorization: Bearer ${k}" -d 'your message'`
  return (
    <div className="flex flex-col gap-3 border-border border-t pt-4">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-[12.5px] text-foreground">
          Webhook
        </span>
        <span className="text-[11.5px] text-muted-foreground">optional</span>
        {hook.enabled ? (
          <Button
            size="xs"
            variant="destructive"
            className="ml-auto"
            disabled={busy}
            onClick={() => setConfirm("off")}
          >
            Turn off webhook
          </Button>
        ) : (
          <Button
            size="xs"
            className="ml-auto"
            disabled={busy}
            onClick={() =>
              void act(
                () => api.bots.webhookEnable(bot.name),
                `Webhook is on for ${bot.name}`
              )
            }
          >
            Turn on webhook
          </Button>
        )}
      </div>
      <p className="text-[12px] text-muted-foreground leading-relaxed">
        {hook.enabled
          ? `Anything that can send a POST with this key can message ${bot.name}. The bot is told those messages come from outside, not from you.`
          : `Off. Turn it on to get a URL and key that let any script or service message ${bot.name} with a POST. Everything listed above keeps working either way.`}
      </p>
      {hook.enabled && (
        <>
          <Row label="URL">
            <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
              {url}
            </code>
            <Button
              size="xs"
              variant="outline"
              onClick={() => void copy(url, "URL")}
            >
              <Copy />
              Copy
            </Button>
          </Row>
          <Row label="Key">
            <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
              {shown ? key : masked}
            </code>
            <Button
              size="xs"
              variant="outline"
              onClick={() => void copy(key, "Key")}
            >
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
              disabled={busy}
              onClick={() => setConfirm("rotate")}
            >
              <RefreshCw />
              Rotate
            </Button>
          </Row>
          <Row label="Send a message">
            <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
              {curlWith(shown ? key : masked)}
            </code>
            <Button
              size="xs"
              variant="outline"
              onClick={() => void copy(curlWith(key), "curl example")}
            >
              <Copy />
              Copy
            </Button>
          </Row>
          <p className="text-[11px] text-muted-foreground">
            A plain-text body is the message. JSON is also accepted:{" "}
            <code className="font-mono">
              {'{"content": "...", "source": "...", "id": "..."}'}
            </code>
            .
          </p>
        </>
      )}
      <AlertDialog
        open={confirm !== null}
        onOpenChange={(o) => !o && setConfirm(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm === "rotate"
                ? `Rotate ${bot.name}'s webhook key?`
                : `Turn off ${bot.name}'s webhook?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "rotate"
                ? "The current key stops working at once. Every sender using it needs the new one."
                : "The key stops working at once and is forgotten; turning the webhook on again makes a new one. The channel stays on: connections, jobs, Talk and the phone app are not affected."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (confirm === "rotate") {
                  setShown(true)
                  void act(
                    () => api.bots.webhookRotate(bot.name),
                    "New key made; the old one no longer works"
                  )
                } else {
                  void act(
                    () => api.bots.webhookDisable(bot.name),
                    `Webhook is off for ${bot.name}`
                  )
                }
                setConfirm(null)
              }}
            >
              {confirm === "rotate" ? "Rotate" : "Turn off"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1">
      <span className={labelClass}>{label}</span>
      <div className="flex flex-wrap items-center gap-1.5">{children}</div>
    </div>
  )
}

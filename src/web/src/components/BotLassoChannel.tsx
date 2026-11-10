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
import { api, type BotChannel, type BotView } from "@/lib/api"

const channelKey = (name: string) => ["bot-lasso-channel", name]

async function copy(text: string, what: string) {
  if (await copyText(text)) toast.success(`${what} copied`)
  else toast.error(`Could not copy the ${what.toLowerCase()}`)
}

// The bot's Lasso channel: its own webhook, which anything able to POST can
// use to message it. The bot's jobs, Talk and phone-app messages arrive
// through the same channel. Everything on it is the server's (botpush.go).
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
  return (
    <div className="flex flex-col gap-4 rounded-lg border border-border p-4">
      <div className="flex flex-col gap-2">
        <span className="flex items-center gap-1.5">
          <Radio className="size-4 shrink-0 text-primary" />
          <span className="font-medium font-mono text-[12.5px] text-foreground">
            Lasso channel
          </span>
        </span>
        <p className="text-[12.5px] text-muted-foreground leading-relaxed">
          This is {bot.name}'s own push channel: anything that can send a POST
          can message it through its webhook.{" "}
          {bot.runtime === "claude"
            ? "Lasso delivers it as a Claude Code channel event."
            : "Lasso delivers it through lasso's omp plugin."}{" "}
          The bot's jobs, Talk and phone-app messages arrive through the same
          channel.
        </p>
      </div>
      {channel.isPending && <Orb state="working" px={16} />}
      {channel.error && (
        <p className="text-[12px] text-destructive">
          {(channel.error as Error).message}
        </p>
      )}
      {c && !c.available && (
        <p className="text-[12px] text-muted-foreground">{c.reason}</p>
      )}
      {c?.available && !c.enabled && (
        <>
          <p className="text-[12px] text-muted-foreground">
            The webhook is off. Nothing outside lasso can post to this bot.
          </p>
          <div>
            <Button
              size="sm"
              disabled={busy}
              onClick={() =>
                void act(
                  () => api.bots.channelEnable(bot.name),
                  `Lasso channel is on for ${bot.name}`
                )
              }
            >
              <Radio />
              Turn on
            </Button>
          </div>
        </>
      )}
      {c?.available && c.enabled && (
        <ChannelOn bot={bot} channel={c} busy={busy} act={act} />
      )}
    </div>
  )
}

function ChannelOn({
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
  const url = `${window.location.origin}${channel.path}`
  const key = channel.key ?? ""
  const masked = `${key.slice(0, 5)}••••••••`
  const curlWith = (k: string) =>
    `curl -X POST ${url} -H "Authorization: Bearer ${k}" -d 'your message'`
  const curl = curlWith(key)
  return (
    <div className="flex flex-col gap-4">
      <Row label="Webhook URL">
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
          onClick={() => void copy(curl, "curl example")}
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
      <p className="text-[11.5px] text-amber-500">
        Whoever holds the key can send {bot.name} messages. The bot treats them
        as coming from outside, not from its human.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        <Button
          size="xs"
          variant="destructive"
          className="ml-auto"
          disabled={busy}
          onClick={() => setConfirm("off")}
        >
          Turn off
        </Button>
      </div>
      <AlertDialog
        open={confirm !== null}
        onOpenChange={(o) => !o && setConfirm(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {confirm === "rotate"
                ? `Rotate ${bot.name}'s Lasso channel key?`
                : `Turn off the Lasso channel for ${bot.name}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "rotate"
                ? "The current key stops working at once. Every sender using it needs the new one."
                : "The key stops working at once and is forgotten. Turning the channel on again makes a new one."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (confirm === "rotate") {
                  setShown(true)
                  void act(
                    () => api.bots.channelRotate(bot.name),
                    "New key made; the old one no longer works"
                  )
                } else {
                  void act(
                    () => api.bots.channelDisable(bot.name),
                    `Lasso channel is off for ${bot.name}`
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

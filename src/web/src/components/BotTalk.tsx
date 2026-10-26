import { useQuery, useQueryClient } from "@tanstack/react-query"
import { Copy, Eye, EyeOff, MessageCircle, RefreshCw } from "lucide-react"
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
  type BotTalk,
  type BotTalkActivity,
  type BotView,
} from "@/lib/api"
import { useMinuteClock } from "@/lib/bots"
import { whenText } from "@/lib/cron"
import { cn } from "@/lib/utils"

const talkKey = (name: string) => ["bot-talk", name]

async function copy(text: string, what: string) {
  if (await copyText(text)) toast.success(`${what} copied`)
  else toast.error(`Could not copy the ${what.toLowerCase()}`)
}

// The Talk tab: the bot's OpenAI-compatible door, for a chat app on someone's
// phone (Conduit, say). Everything on it is the server's (bottalk.go).
export function TalkTab({ bot }: { bot: BotView }) {
  const queryClient = useQueryClient()
  const talk = useQuery({
    queryKey: talkKey(bot.name),
    queryFn: () => api.bots.talk(bot.name),
    refetchInterval: 5000,
  })
  const [busy, setBusy] = React.useState(false)
  const act = async (fn: () => Promise<BotTalk>, done?: string) => {
    setBusy(true)
    try {
      queryClient.setQueryData(talkKey(bot.name), await fn())
      if (done) toast.success(done)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  const t = talk.data
  return (
    <div className="flex flex-col gap-4">
      <p className="text-[12.5px] text-muted-foreground leading-relaxed">
        Give someone a chat with {bot.name} in an app on their phone that speaks
        the OpenAI API, such as Conduit. Their messages reach {bot.name}'s
        session, and its replies go back to their app.
      </p>
      {talk.isPending && <Orb state="working" px={16} />}
      {talk.error && (
        <p className="text-[12px] text-destructive">
          {(talk.error as Error).message}
        </p>
      )}
      {t && bot.runtime === "omp" && (
        <p className="rounded-lg bg-muted px-3 py-2 text-[12px] text-amber-500">
          Talk works with claude bots; {bot.name} runs omp.
        </p>
      )}
      {t && !t.enabled && bot.runtime !== "omp" && (
        <div>
          <Button
            size="sm"
            disabled={busy}
            onClick={() =>
              void act(
                () => api.bots.talkEnable(bot.name),
                `Talk is on for ${bot.name}`
              )
            }
          >
            <MessageCircle />
            Turn on Talk
          </Button>
        </div>
      )}
      {t?.enabled && <TalkOn bot={bot} talk={t} busy={busy} act={act} />}
    </div>
  )
}

function TalkOn({
  bot,
  talk,
  busy,
  act,
}: {
  bot: BotView
  talk: BotTalk
  busy: boolean
  act: (fn: () => Promise<BotTalk>, done?: string) => Promise<void>
}) {
  const [shown, setShown] = React.useState(false)
  const [confirm, setConfirm] = React.useState<"rotate" | "off" | null>(null)
  const now = useMinuteClock()
  const base = `${window.location.origin}${talk.path}`
  const key = talk.key ?? ""
  const masked = `${key.slice(0, 5)}••••••••`
  const setActivity = (a: BotTalkActivity) => {
    if (a !== talk.activity)
      void act(() => api.bots.talkSetActivity(bot.name, a))
  }
  return (
    <div className="flex flex-col gap-4">
      <TalkStateNote bot={bot} talk={talk} />
      <Row label="Base URL">
        <code className="min-w-0 flex-1 break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
          {base}
        </code>
        <Button
          size="xs"
          variant="outline"
          onClick={() => void copy(base, "Base URL")}
        >
          <Copy />
          Copy
        </Button>
      </Row>
      <Row label="API key">
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
        <Button size="xs" variant="ghost" onClick={() => setConfirm("rotate")}>
          <RefreshCw />
          Rotate
        </Button>
      </Row>
      <Row label="Model">
        <code className="rounded-md bg-muted px-2 py-1.5 font-mono text-[11.5px] text-foreground">
          {talk.model}
        </code>
      </Row>
      <div className="flex flex-col gap-1.5">
        <span className={labelClass}>While {bot.name} works</span>
        <div className="flex flex-wrap gap-1.5">
          {(["plain", "detailed"] as const).map((a) => (
            <Button
              key={a}
              size="xs"
              variant={talk.activity === a ? "secondary" : "ghost"}
              aria-pressed={talk.activity === a}
              disabled={busy}
              onClick={() => setActivity(a)}
            >
              {a === "plain" ? "Plain" : "Detailed"}
            </Button>
          ))}
        </div>
        <span className="text-[11px] text-muted-foreground">
          {talk.activity === "plain"
            ? `Their app shows only the kind of each step ("Reading a file", "Running a command").`
            : "Their app shows each step's subject too: file names and commands from this machine."}
        </span>
      </div>
      <div className="flex flex-col gap-1.5 rounded-lg border border-border px-3 py-2.5">
        <span className={labelClass}>In Conduit</span>
        <ol className="list-decimal space-y-0.5 pl-4 text-[12px] text-muted-foreground">
          <li>
            Add a connection. Provider type:{" "}
            <span className="text-foreground">OpenAI-compatible</span>.
          </li>
          <li>Base URL: the one above.</li>
          <li>
            Authentication:{" "}
            <span className="text-foreground">Bearer token</span>, with the API
            key above.
          </li>
          <li>Pick the model {talk.model} and start a chat.</li>
        </ol>
      </div>
      <p className="text-[11.5px] text-amber-500">
        Whoever holds the key can ask {bot.name} for anything its instructions
        allow. Say in its CLAUDE.md what it may do for them.
      </p>
      <div className="flex flex-wrap items-center gap-3">
        {talk.last_at && (
          <span className="min-w-0 flex-1 text-[11.5px] text-muted-foreground">
            Last message {whenText(talk.last_at, now)}
          </span>
        )}
        <Button
          size="xs"
          variant="destructive"
          className={cn(!talk.last_at && "ml-auto")}
          onClick={() => setConfirm("off")}
        >
          Turn off Talk
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
                ? `Rotate ${bot.name}'s Talk key?`
                : `Turn off Talk for ${bot.name}?`}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {confirm === "rotate"
                ? "The current key stops working at once. Every app using it needs the new one."
                : "The key stops working at once and is forgotten. Turning Talk on again makes a new one."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (confirm === "rotate") {
                  setShown(true)
                  void act(
                    () => api.bots.talkRotate(bot.name),
                    "New key made; the old one no longer works"
                  )
                } else {
                  void act(
                    () => api.bots.talkDisable(bot.name),
                    `Talk is off for ${bot.name}`
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

function TalkStateNote({ bot, talk }: { bot: BotView; talk: BotTalk }) {
  const [restarting, setRestarting] = React.useState(false)
  if (talk.state === "ready")
    return (
      <p className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
        <span className="size-1.5 rounded-full bg-emerald-500" />
        {bot.name} is taking messages.
      </p>
    )
  if (talk.state === "waiting")
    return (
      <p className="flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
        <span className="size-1.5 rounded-full bg-amber-500" />
        {talk.reason}
      </p>
    )
  if (talk.state === "unavailable")
    return (
      <p className="rounded-lg bg-muted px-3 py-2 text-[12px] text-amber-500">
        {talk.reason}
      </p>
    )
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-lg bg-muted px-3 py-2 text-[12px] text-muted-foreground">
      <span className="min-w-0 flex-1">{talk.reason}</span>
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

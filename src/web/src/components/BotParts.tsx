import { toast } from "sonner"
import { Orb } from "@/components/ui/orb"
import { api, type BotView } from "@/lib/api"
import { botAvatarText, botHue, invalidateBots, stateLabel } from "@/lib/bots"
import { cn } from "@/lib/utils"

// What a bot looks like and the two lifecycle calls, shared by the Bots view's
// list, its management table and the settings page.

export function BotAvatar({
  bot,
  size = 36,
  className,
}: {
  bot: Pick<BotView, "avatar" | "name">
  size?: number
  className?: string
}) {
  const own = !!bot.avatar?.trim()
  return (
    <span
      aria-hidden
      className={cn(
        "flex shrink-0 select-none items-center justify-center overflow-hidden rounded-full font-semibold leading-none",
        own ? "bg-muted text-foreground" : "text-white",
        className
      )}
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * (own ? 0.5 : 0.42)),
        background: own ? undefined : `oklch(0.58 0.12 ${botHue(bot.name)}deg)`,
      }}
    >
      {botAvatarText(bot)}
    </span>
  )
}

// BotStateMark: the list's one-glance state. Working is the app's orb, blocked
// says so in words (it is the one state a human must act on), stopped is a
// hollow ring and idle a quiet dot.
export function BotStateMark({
  bot,
  words,
  className,
}: {
  bot: BotView
  // Say the state in words too, not only for blocked (the chat header, the
  // management table).
  words?: boolean
  className?: string
}) {
  const label = stateLabel(bot.state, bot.waiting_for)
  return (
    <span
      title={bot.error || label}
      className={cn(
        "flex shrink-0 items-center gap-1 text-[11px]",
        bot.state === "blocked"
          ? "text-destructive"
          : bot.state === "working" || bot.state === "starting"
            ? "text-primary"
            : "text-muted-foreground",
        className
      )}
    >
      {bot.state === "working" || bot.state === "starting" ? (
        <Orb state="working" px={12} />
      ) : bot.state === "stopped" ? (
        <span className="size-2 rounded-full border border-current" />
      ) : (
        <span className="size-1.5 rounded-full bg-current opacity-70" />
      )}
      {(words || bot.state === "blocked") && (
        <span className="truncate">
          {bot.state === "blocked" && !words ? "Needs your input" : label}
        </span>
      )}
    </span>
  )
}

// Starting a bot from anywhere in the view: the call, the refresh, and a toast
// when it fails.
export async function startBot(name: string, fresh = false) {
  try {
    await api.bots.start(name, fresh)
  } catch (e) {
    toast.error(`could not start ${name}: ${(e as Error).message}`)
  } finally {
    void invalidateBots(name)
  }
}

export async function stopBot(name: string) {
  try {
    await api.bots.stop(name)
  } catch (e) {
    toast.error(`could not stop ${name}: ${(e as Error).message}`)
  } finally {
    void invalidateBots(name)
  }
}

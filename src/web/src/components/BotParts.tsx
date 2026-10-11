import type { CSSProperties } from "react"
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
import { Checkbox } from "@/components/ui/checkbox"
import { Orb } from "@/components/ui/orb"
import { api, type BotView, botAvatarURL } from "@/lib/api"
import {
  botHue,
  botInitial,
  botRunning,
  invalidateBots,
  stateLabel,
} from "@/lib/bots"
import { cn } from "@/lib/utils"

// What a bot looks like and the two lifecycle calls, shared by the Bots view's
// list, its management table and the settings page.

export function BotAvatar({
  bot,
  size = 36,
  className,
}: {
  bot: Pick<BotView, "name"> & { avatar_image?: string }
  size?: number
  className?: string
}) {
  const picture = botAvatarURL(bot)
  if (picture)
    return (
      <img
        src={picture}
        alt=""
        aria-hidden
        draggable={false}
        width={size}
        height={size}
        className={cn("shrink-0 rounded-full object-cover", className)}
        style={{ width: size, height: size }}
      />
    )
  return (
    <span
      aria-hidden
      className={cn(
        "fx-avatar flex shrink-0 select-none items-center justify-center overflow-hidden rounded-full font-semibold text-white leading-none",
        className
      )}
      style={
        {
          width: size,
          height: size,
          fontSize: Math.round(size * 0.42),
          background: `oklch(0.58 0.12 ${botHue(bot.name)}deg)`,
          "--av-h": botHue(bot.name),
        } as CSSProperties
      }
    >
      {botInitial(bot.name)}
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
        <span
          className={cn(
            "size-1.5 rounded-full bg-current opacity-70",
            bot.state === "blocked" && "fx-live"
          )}
        />
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

// The one delete confirmation, from the settings footer and the list's
// right-click menu alike. A running bot can be deleted: lasso stops it first.
export function DeleteBotDialog({
  bot,
  dirPath,
  open,
  onOpenChange,
  onDeleted,
}: {
  bot: BotView | null
  // The folder with ~ expanded, when the caller has it.
  dirPath?: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onDeleted?: () => void
}) {
  // Off every time the dialog opens: deleting a folder is never the default.
  const [withFolder, setWithFolder] = React.useState(false)
  React.useEffect(() => {
    if (open) setWithFolder(false)
  }, [open])
  const where = bot?.host === "local" ? "this machine" : bot?.host
  const folder = dirPath ?? bot?.dir_path ?? bot?.dir
  const omp = bot?.runtime === "omp"
  const remove = async () => {
    if (!bot) return
    try {
      const res = await api.bots.delete(bot.name, withFolder)
      if (res.folder_error)
        toast.warning(
          `${bot.name} deleted, but its folder was not: ${res.folder_error}`
        )
      else if (res.folder_deleted)
        toast.success(`${bot.name} and its folder deleted`)
      else toast.success(`${bot.name} deleted; its folder is still on ${where}`)
      onDeleted?.()
    } catch (e) {
      toast.error(`could not delete ${bot.name}: ${(e as Error).message}`)
    } finally {
      void invalidateBots(bot.name)
    }
  }
  return (
    <AlertDialog open={open && !!bot} onOpenChange={onOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Delete {bot?.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            {bot && botRunning(bot) && (
              <>It is running: deleting stops it and closes its pane. </>
            )}
            lasso forgets this bot.{" "}
            {withFolder ? (
              <>
                Its folder ({folder}) on {where} is deleted too, with everything
                in it: its {omp ? "AGENTS.md" : "CLAUDE.md"}, skills,
                environment and secrets{omp ? ", and its conversations" : ""}.
                This cannot be undone.
              </>
            ) : (
              <>
                Its folder ({folder}) stays on {where}, with its{" "}
                {omp ? "AGENTS.md" : "CLAUDE.md"}, skills and environment, and
                its conversations stay{" "}
                {omp ? "in that folder" : "in Claude Code's history"}.
              </>
            )}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <label
          htmlFor="delete-bot-folder"
          className="flex items-start gap-2 text-[13px]"
        >
          <Checkbox
            id="delete-bot-folder"
            checked={withFolder}
            onCheckedChange={(v) => setWithFolder(v === true)}
            className="mt-0.5"
          />
          <span>
            Also delete its folder
            <span className="block break-all font-mono text-[11.5px] text-muted-foreground">
              {folder}
            </span>
          </span>
        </label>
        <AlertDialogFooter>
          <AlertDialogCancel>Keep it</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            onClick={() => void remove()}
          >
            {withFolder ? "Delete bot and folder" : "Delete bot"}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

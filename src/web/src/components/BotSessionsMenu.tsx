import { useQuery } from "@tanstack/react-query"
import { Check, MessageSquarePlus, Plus } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Orb } from "@/components/ui/orb"
import { api, type BotSession, type BotView } from "@/lib/api"
import { invalidateBots } from "@/lib/bots"
import { qk, queryClient } from "@/lib/query"
import { cn } from "@/lib/utils"

// The chat header's New: a new, empty conversation, or one of the bot's
// earlier ones to resume (docs/web-ui/bots.md). An omp bot switches at once;
// a Claude Code bot restarts into it. Neither is offered mid-turn, so a switch
// never cuts a turn off.

function sessionName(s: BotSession): string {
  if (s.title) return s.title
  if (s.preview) return `"${s.preview}"`
  return "(empty)"
}

function ago(iso: string): string {
  const d = Date.now() - new Date(iso).getTime()
  if (d < 60_000) return "now"
  if (d < 3_600_000) return `${Math.floor(d / 60_000)}m`
  if (d < 86_400_000) return `${Math.floor(d / 3_600_000)}h`
  if (d < 172_800_000) return "yesterday"
  return new Date(iso).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
  })
}

export function BotSessionsMenu({ bot }: { bot: BotView }) {
  const [open, setOpen] = React.useState(false)
  const sessions = useQuery({
    queryKey: ["bot-sessions", bot.name],
    queryFn: () => api.bots.sessions(bot.name),
    enabled: open,
    staleTime: 0,
  })
  const busy = bot.state === "working"
  const claude = bot.runtime !== "omp"
  const after = () => {
    void invalidateBots(bot.name)
    void sessions.refetch()
    // The chat reads the new session's file from its next fetch.
    void queryClient.invalidateQueries({ queryKey: qk.chat("bot", bot.name) })
  }
  const resume = async (s: BotSession) => {
    try {
      await api.bots.resumeSession(bot.name, s.id)
      toast.success(`Resumed ${sessionName(s)}`)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      after()
    }
  }
  const startNew = async () => {
    const previous = sessions.data?.sessions.find((s) => s.current)
    try {
      await api.bots.newSession(bot.name)
      toast.success("Started a new conversation", {
        action: previous
          ? { label: "Undo", onClick: () => void resume(previous) }
          : undefined,
      })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      after()
    }
  }
  const list = sessions.data?.sessions ?? []
  return (
    <DropdownMenu open={open} onOpenChange={setOpen}>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          title="New conversation, or resume an earlier one"
          aria-label="Conversations"
          className="flex size-7 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <MessageSquarePlus className="size-4" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80">
        <DropdownMenuItem disabled={busy} onSelect={() => void startNew()}>
          <Plus />
          New conversation
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuLabel className="text-[11px] text-muted-foreground">
          Resume{claude ? " (restarts the bot)" : ""}
        </DropdownMenuLabel>
        {sessions.isPending ? (
          <div className="flex justify-center py-2">
            <Orb state="working" px={14} />
          </div>
        ) : list.length === 0 ? (
          <p className="px-2 py-1.5 text-[12px] text-muted-foreground">
            No earlier conversations.
          </p>
        ) : (
          <div className="max-h-72 overflow-y-auto">
            {list.map((s) => (
              <DropdownMenuItem
                key={s.id}
                disabled={busy || s.current}
                onSelect={() => void resume(s)}
                className="gap-2"
              >
                <Check
                  className={cn(
                    "size-3.5 shrink-0",
                    s.current ? "opacity-100" : "opacity-0"
                  )}
                />
                <span className="min-w-0 flex-1 truncate">
                  {sessionName(s)}
                </span>
                <span className="shrink-0 text-[11px] text-muted-foreground">
                  {ago(s.updated_at)}
                  {s.messages ? ` · ${s.messages}` : ""}
                </span>
              </DropdownMenuItem>
            ))}
          </div>
        )}
        {busy && (
          <p className="px-2 pt-1 pb-1.5 text-[11px] text-muted-foreground">
            It is mid-turn: stop it first to switch.
          </p>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

import { Play, Plus, Settings, Square } from "lucide-react"
import * as React from "react"
import {
  BotAvatar,
  BotStateMark,
  startBot,
  stopBot,
} from "@/components/BotParts"
import { Button } from "@/components/ui/button"
import type { BotView } from "@/lib/api"
import { botRunning, relativeTime } from "@/lib/bots"

// /bots/manage: every bot at once, with where it runs and what it is wired to,
// for the questions the chat list does not answer ("which bots are on
// minime", "which ones read mail"). A table where there is room for one, a
// card per bot on a phone.

function channelsOf(b: BotView): number {
  return (b.mcp ?? []).filter((s) => s.channel).length
}

function Actions({
  bot,
  onSettings,
}: {
  bot: BotView
  onSettings: () => void
}) {
  const [busy, setBusy] = React.useState(false)
  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    await fn()
    setBusy(false)
  }
  return (
    <span className="flex shrink-0 items-center gap-1">
      {botRunning(bot) ? (
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => void run(() => stopBot(bot.name))}
        >
          <Square />
          Stop
        </Button>
      ) : (
        <Button
          variant="outline"
          size="sm"
          disabled={busy}
          onClick={() => void run(() => startBot(bot.name))}
        >
          <Play />
          Start
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon-sm"
        title={`${bot.name} settings`}
        aria-label={`${bot.name} settings`}
        onClick={onSettings}
      >
        <Settings />
      </Button>
    </span>
  )
}

export function BotsManage({
  bots,
  lead,
  now,
  onOpen,
  onSettings,
  onNew,
}: {
  bots: BotView[]
  lead: React.ReactNode
  now: number
  onOpen: (name: string) => void
  onSettings: (name: string) => void
  onNew: () => void
}) {
  const sorted = React.useMemo(
    () => [...bots].sort((a, b) => a.name.localeCompare(b.name)),
    [bots]
  )
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        {lead}
        <span className="font-medium text-[12.5px] text-foreground">
          Manage bots
        </span>
        <span className="ml-auto" />
        <Button size="sm" onClick={onNew}>
          <Plus />
          New bot
        </Button>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {sorted.length === 0 ? (
          <div className="px-4 py-6 text-center text-[13px] text-muted-foreground">
            No bots yet.
          </div>
        ) : (
          <>
            {/* md+: a table. */}
            <table className="w-full text-left text-[12.5px] max-md:hidden">
              <thead className="text-[11px] text-muted-foreground">
                <tr className="border-border border-b">
                  <th className="px-3 py-2 font-medium">Bot</th>
                  <th className="px-3 py-2 font-medium">Host</th>
                  <th className="px-3 py-2 font-medium">Workspace</th>
                  <th className="px-3 py-2 font-medium">Model</th>
                  <th className="px-3 py-2 font-medium">Channels</th>
                  <th className="px-3 py-2 font-medium">State</th>
                  <th className="px-3 py-2 font-medium">Last active</th>
                  <th className="px-3 py-2" />
                </tr>
              </thead>
              <tbody>
                {sorted.map((b) => (
                  <tr key={b.name} className="border-border/60 border-b">
                    <td className="px-3 py-1.5">
                      <button
                        type="button"
                        onClick={() => onOpen(b.name)}
                        className="flex items-center gap-2 rounded hover:underline"
                      >
                        <BotAvatar bot={b} size={24} />
                        <span className="font-medium text-foreground">
                          {b.name}
                        </span>
                      </button>
                    </td>
                    <td className="px-3 py-1.5 text-muted-foreground">
                      {b.host}
                    </td>
                    <td className="px-3 py-1.5 text-muted-foreground">
                      {b.workspace}
                    </td>
                    <td className="px-3 py-1.5 font-mono text-[11.5px] text-muted-foreground">
                      {b.model || "default"}
                    </td>
                    <td className="px-3 py-1.5 text-muted-foreground">
                      {channelsOf(b)}
                    </td>
                    <td className="px-3 py-1.5">
                      <BotStateMark bot={b} words />
                    </td>
                    <td className="px-3 py-1.5 text-muted-foreground">
                      {relativeTime(b.last_at, now) || "—"}
                    </td>
                    <td className="px-3 py-1.5">
                      <Actions bot={b} onSettings={() => onSettings(b.name)} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {/* Below md: a card per bot. */}
            <div className="flex flex-col gap-2 p-3 md:hidden">
              {sorted.map((b) => (
                <div
                  key={b.name}
                  className="flex flex-col gap-2 rounded-lg border border-border bg-card p-3"
                >
                  <button
                    type="button"
                    onClick={() => onOpen(b.name)}
                    className="flex min-w-0 items-center gap-2.5 text-left"
                  >
                    <BotAvatar bot={b} size={32} />
                    <span className="flex min-w-0 flex-1 flex-col">
                      <span className="truncate font-medium text-[13px] text-foreground">
                        {b.name}
                      </span>
                      <span className="truncate text-[11.5px] text-muted-foreground">
                        {b.host} · {b.workspace} · {b.model || "default model"}
                      </span>
                    </span>
                  </button>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
                    <BotStateMark bot={b} words />
                    <span>
                      {channelsOf(b)} channel{channelsOf(b) === 1 ? "" : "s"}
                    </span>
                    {b.last_at && (
                      <span>active {relativeTime(b.last_at, now)}</span>
                    )}
                    <span className="ml-auto">
                      <Actions bot={b} onSettings={() => onSettings(b.name)} />
                    </span>
                  </div>
                </div>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

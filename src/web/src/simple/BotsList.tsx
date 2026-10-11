import { ChevronRight } from "lucide-react"
import * as React from "react"

import { BotAvatar, startBot } from "@/components/BotParts"
import { ChatView } from "@/components/ChatView"
import type { BotView } from "@/lib/api"
import {
  botUnread,
  markBotSeen,
  relativeTime,
  seedBotsSeen,
  stateLabel,
  useBots,
  useBotsSeen,
  useMinuteClock,
} from "@/lib/bots"
import { BackButton } from "@/simple/AgentsView"
import {
  Card,
  ConnectionLine,
  EmptyState,
  Loading,
  Screen,
  ScrollBody,
  StatusDot,
} from "@/simple/parts"
import type { SimpleRoute } from "@/simple/route"

// lasso's bots, and a chat with each. Creating, deleting and configuring a
// bot stay in the full UI's Bots view; this is for talking to them.

export function BotsList({ go }: { go: (r: SimpleRoute) => void }) {
  const q = useBots(true)
  const bots = q.data?.bots ?? []
  const seen = useBotsSeen()
  const now = useMinuteClock()
  React.useEffect(() => {
    if (q.data) seedBotsSeen(q.data.bots)
  }, [q.data])

  return (
    <Screen
      title="Bots"
      status={
        <ConnectionLine
          error={q.error as Error | null}
          unlisted={[]}
          onRetry={() => void q.refetch()}
        />
      }
    >
      {q.isLoading && bots.length === 0 ? (
        <Loading />
      ) : bots.length === 0 && !q.isLoading ? (
        <EmptyState title="No bots yet">
          Make one in the full lasso's Bots view, then talk to it from here.
        </EmptyState>
      ) : (
        <ScrollBody className="pt-3">
          <Card className="overflow-hidden">
            {bots.map((b, i) => (
              <button
                key={b.name}
                type="button"
                onClick={() => go({ view: "bot", name: b.name })}
                className="flex w-full items-center gap-3 px-4 text-left active:bg-muted/60"
              >
                <BotAvatar bot={b} size={40} />
                <span
                  className={`flex min-h-[72px] min-w-0 flex-1 items-center gap-2 ${i > 0 ? "border-border/60 border-t" : ""}`}
                >
                  <span className="min-w-0 flex-1 py-3">
                    <span className="flex items-center gap-2">
                      <span className="min-w-0 truncate font-semibold text-[16.5px] text-foreground">
                        {b.name}
                      </span>
                      {botUnread(b, seen) && (
                        <span className="size-2 shrink-0 rounded-full bg-primary" />
                      )}
                      <span className="ml-auto shrink-0 text-[12.5px] text-muted-foreground">
                        {relativeTime(b.last_at, now)}
                      </span>
                    </span>
                    <span className="mt-0.5 flex items-center gap-1.5 text-[13.5px] text-muted-foreground">
                      <StatusDot status={b.state} className="size-1.5" />
                      <span className="min-w-0 truncate">
                        {b.state === "idle" || b.state === "working"
                          ? b.last_text || stateLabel(b.state, b.waiting_for)
                          : stateLabel(b.state, b.waiting_for)}
                      </span>
                    </span>
                  </span>
                  <ChevronRight className="size-5 shrink-0 text-muted-foreground/70" />
                </span>
              </button>
            ))}
          </Card>
        </ScrollBody>
      )}
    </Screen>
  )
}

export function BotChat({
  name,
  onBack: back,
}: {
  name: string
  onBack: () => void
}) {
  const q = useBots(true)
  const bot = q.data?.bots.find((b) => b.name === name)
  // Reading the conversation is what marks the bot's reply read.
  React.useEffect(() => {
    if (bot) markBotSeen(bot.name, bot.last_at)
  }, [bot])

  if (!bot)
    return (
      <Screen title={name} onBack={back}>
        {!q.isLoading && (
          <EmptyState title="No such bot">
            It may have been deleted in the full lasso.
          </EmptyState>
        )}
      </Screen>
    )

  const omp = bot.runtime !== "claude"
  if (bot.stopped || (!omp && !bot.pane_id))
    return <StoppedBot bot={bot} onBack={back} />

  return (
    <ChatView
      key={`${bot.name}\u0000${bot.host}\u0000${omp ? "omp" : bot.pane_id}`}
      address={
        omp ? { bot: bot.name } : { host: bot.host, paneID: bot.pane_id ?? "" }
      }
      incoming
      variant="bot"
      title={bot.name}
      headerLead={<BackButton onClick={back} />}
      placeholder={`Message ${bot.name}…`}
      className="min-h-0 flex-1"
    />
  )
}

function StoppedBot({ bot, onBack }: { bot: BotView; onBack: () => void }) {
  const [busy, setBusy] = React.useState(false)
  return (
    <Screen title={bot.name} onBack={onBack}>
      <EmptyState
        icon={<BotAvatar bot={bot} size={64} />}
        title={`${bot.name} is stopped`}
      >
        <button
          type="button"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            try {
              await startBot(bot.name)
            } finally {
              setBusy(false)
            }
          }}
          className="mt-3 h-11 rounded-full bg-primary px-6 font-semibold text-[15px] text-primary-foreground disabled:opacity-40"
        >
          {busy ? "Starting…" : "Start"}
        </button>
      </EmptyState>
    </Screen>
  )
}

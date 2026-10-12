import { useQueryClient } from "@tanstack/react-query"
import { ChevronLeft, ChevronRight, Power } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { EndAgentDialog } from "@/components/AgentParts"
import { BotAvatar } from "@/components/BotParts"
import { ChatView } from "@/components/ChatView"
import { paneKey } from "@/lib/agents"
import { api, type BotView, type HostPane } from "@/lib/api"
import { useBots } from "@/lib/bots"
import { botForPane, simpleAgentName, useFleet } from "@/simple/inbox"
import {
  Card,
  ConnectionLine,
  EmptyState,
  GroupHeader,
  Loading,
  Screen,
  ScrollBody,
  StatusDot,
} from "@/simple/parts"
import type { SimpleRoute } from "@/simple/route"

// Every herdr agent across the machines lasso drives, grouped by herdr
// workspace, each named by its herdr label, else its tab. Tapping one opens
// its conversation; nothing here moves herdr's focus.

interface Group {
  key: string
  label: string
  host: string
  panes: HostPane[]
}

function groupByWorkspace(panes: HostPane[]): Group[] {
  const map = new Map<string, Group>()
  for (const p of panes) {
    const key = `${p.host}\u0000${p.workspace_id ?? p.workspace_label ?? ""}`
    let g = map.get(key)
    if (!g) {
      g = {
        key,
        label:
          p.workspace_label && p.workspace_label !== "~"
            ? p.workspace_label
            : p.tab_label || "workspace",
        host: p.host_label || p.host,
        panes: [],
      }
      map.set(key, g)
    }
    g.panes.push(p)
  }
  // A stable order, so a status change never moves a group under a thumb:
  // machine, then workspace name. What needs the human is the Inbox's job.
  return [...map.values()].sort(
    (a, b) =>
      a.host.localeCompare(b.host) ||
      a.label.localeCompare(b.label, undefined, { sensitivity: "base" })
  )
}

const HARNESS_MARK: Record<string, string> = {
  claude: "CC",
  omp: "omp",
  codex: "cx",
  opencode: "oc",
  gemini: "gm",
}

function HarnessMark({ agent }: { agent?: string }) {
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-xl bg-muted font-label text-[10.5px] text-muted-foreground">
      {HARNESS_MARK[agent ?? ""] ?? (agent ?? "?").slice(0, 2)}
    </span>
  )
}

export function AgentsList({ go }: { go: (r: SimpleRoute) => void }) {
  const fleet = useFleet()
  const bots = useBots(true).data?.bots ?? []
  const panes = React.useMemo(
    () => (fleet.data?.panes ?? []).filter((p) => p.has_agent),
    [fleet.data]
  )
  const groups = React.useMemo(() => groupByWorkspace(panes), [panes])

  return (
    <Screen
      title="Agents"
      status={
        <ConnectionLine
          error={fleet.error as Error | null}
          unlisted={Object.keys(fleet.data?.errors ?? {})}
          onRetry={() => void fleet.refetch()}
        />
      }
    >
      {fleet.isLoading && groups.length === 0 ? (
        <Loading />
      ) : groups.length === 0 && !fleet.isLoading ? (
        <EmptyState title="No agents running">
          Agents you start in herdr, on any machine lasso drives, show up here.
        </EmptyState>
      ) : (
        <ScrollBody>
          {groups.map((g) => (
            <section key={g.key}>
              <GroupHeader>
                {g.label} · on {g.host}
              </GroupHeader>
              <Card className="overflow-hidden">
                {g.panes.map((p, i) => {
                  const bot = botForPane(bots, p)
                  return (
                    <AgentRow
                      key={paneKey(p)}
                      pane={p}
                      bot={bot}
                      divider={i > 0}
                      onOpen={() =>
                        bot && bot.runtime !== "claude"
                          ? go({ view: "bot", name: bot.name })
                          : go({ view: "agent", host: p.host, pane: p.pane_id })
                      }
                    />
                  )
                })}
              </Card>
            </section>
          ))}
        </ScrollBody>
      )}
    </Screen>
  )
}

function AgentRow({
  pane,
  bot,
  divider,
  onOpen,
}: {
  pane: HostPane
  bot?: BotView
  divider: boolean
  onOpen: () => void
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className="flex min-h-[68px] w-full items-center gap-3 px-4 text-left active:bg-muted/60"
    >
      <StatusDot status={pane.agent_status} />
      {bot ? (
        <BotAvatar bot={bot} size={36} />
      ) : (
        <HarnessMark agent={pane.agent} />
      )}
      <span
        className={`flex min-h-[68px] min-w-0 flex-1 items-center gap-2 ${divider ? "border-border/60 border-t" : ""}`}
      >
        <span className="min-w-0 flex-1">
          <span className="block truncate font-semibold text-[16.5px] text-foreground">
            {bot?.name ?? simpleAgentName(pane)}
          </span>
          {(pane.agent_status === "working" ||
            pane.agent_status === "blocked") && (
            <span
              className={`block text-[12.5px] ${pane.agent_status === "blocked" ? "text-destructive" : "text-muted-foreground"}`}
            >
              {pane.agent_status === "blocked" ? "waiting on you" : "working"}
            </span>
          )}
        </span>
        <ChevronRight className="size-5 shrink-0 text-muted-foreground/70" />
      </span>
    </button>
  )
}

export function BackButton({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label="Back"
      className="-ml-1.5 flex h-10 shrink-0 items-center pr-1 text-muted-foreground active:text-foreground"
    >
      <ChevronLeft className="size-6" />
    </button>
  )
}

// One agent's conversation, read by address: the chat view's own rows, ask
// cards and composer, without the full UI's terminal chrome.
export function AgentChat({
  host,
  pane: paneID,
  onBack: back,
}: {
  host: string
  pane: string
  onBack: () => void
}) {
  const fleet = useFleet()
  const queryClient = useQueryClient()
  const pane = fleet.data?.panes?.find(
    (p) => p.host === host && p.pane_id === paneID
  )
  const name = pane ? simpleAgentName(pane) : paneID
  const [confirmKill, setConfirmKill] = React.useState(false)

  const kill = async () => {
    try {
      await api.close([paneID], host)
      toast.success(`Ended ${name}`)
      void queryClient.invalidateQueries({ queryKey: ["simple"] })
      back()
    } catch (e) {
      toast.error(`Could not end ${name}: ${(e as Error).message}`)
    }
  }
  const icon =
    "flex size-10 items-center justify-center rounded-full text-muted-foreground active:bg-muted"

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <ChatView
        key={`${host}\u0000${paneID}`}
        address={{ host, paneID }}
        variant="bot"
        title={name}
        headerLead={<BackButton onClick={back} />}
        headerExtra={
          <button
            type="button"
            aria-label="End agent"
            onClick={() => setConfirmKill(true)}
            className={icon}
          >
            <Power className="size-[18px]" />
          </button>
        }
        placeholder={`Message ${name}…`}
        className="phone-chat min-h-0 flex-1"
      />
      <EndAgentDialog
        open={confirmKill}
        onOpenChange={setConfirmKill}
        name={name}
        host={pane?.host_label || host}
        onEnd={() => void kill()}
      />
    </div>
  )
}

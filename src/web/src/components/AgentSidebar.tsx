import { Pin, PinOff, Power, Search, X } from "lucide-react"
import * as React from "react"
import { AgentLines, EndAgentDialog } from "@/components/AgentParts"
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuTrigger,
} from "@/components/ui/context-menu"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import {
  agentMatches,
  agentName,
  paneKey,
  pinFirst,
  sortAgentsByPriority,
  useAgents,
} from "@/lib/agents"
import type { HostPane } from "@/lib/api"
import { setAgentPinned, useUIState } from "@/lib/ui-state"
import { cn } from "@/lib/utils"

// The chat's docked left sidebar: every agent lasso can reach, across every
// connected machine, this tab's own host first.
//
// Only agents — a pane with no agent is a bare shell or a stray tab, and there
// is no session to read, so listing it would offer a row that selects nothing.
// herdr's own sidebar indexes one machine's workspaces; this one indexes the
// fleet's conversations, which is one per agent pane, and the chat beside it can
// show exactly one of those.
//
// The list, the naming and the focus action come from lib/agents, because the
// sidebar's Agents tab (AgentsTab) is the same list for the widths where a
// docked column does not fit — see max-md:hidden here and md:hidden on that tab.
// Exactly one of the two is reachable at any width.
export function AgentSidebar() {
  const {
    agents,
    isLoading,
    error,
    unlisted,
    current,
    focusAgent,
    closeAgent,
  } = useAgents()
  const { pinned_agents: pinnedKeys } = useUIState()
  const [query, setQuery] = React.useState("")
  // The agents grid's pins lead here too, in the grid's pin order: one pin, one
  // meaning, whichever surface set it (the grid's card or the chat header).
  // Everything after them is always in priority order (blocked first), so the
  // agent that needs an answer is at the top whichever machine it is on.
  const pinnedSet = React.useMemo(() => new Set(pinnedKeys ?? []), [pinnedKeys])
  const shown = React.useMemo(() => {
    const terms = query.toLowerCase().split(/\s+/).filter(Boolean)
    return pinFirst(sortAgentsByPriority(agents), pinnedKeys).filter((p) =>
      agentMatches(p, terms)
    )
  }, [agents, query, pinnedKeys])
  // The row whose pane "Close" in its context menu would end. herdr's pane.close
  // stops the agent outright, so it is asked first, as on the Agents tab.
  const [closing, setClosing] = React.useState<HostPane | null>(null)

  return (
    // vsurface: this sits over the terminal (the chat is an overlay), and under
    // the atmosphere --card is translucent — herdr's pane text would read
    // straight through the list. Same opt-out the chat and the file viewer take;
    // inside the chat that opt-out paints the backdrop rather than going flat,
    // so this column carries the same shading as the conversation beside it.
    <aside className="vsurface flex w-52 flex-none flex-col border-border border-r bg-card max-md:hidden">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        <span className="text-[12px] text-muted-foreground">Agents</span>
        {agents.length > 0 && (
          <span className="ml-auto text-[11px] text-muted-foreground">
            {query ? `${shown.length}/${agents.length}` : agents.length}
          </span>
        )}
      </div>
      {/* Same matching as the ⌘K switcher (agentMatches): name, harness,
          worktree and machine, every term required. */}
      <div className="relative flex-none border-border border-b px-1.5 py-1">
        <Search className="pointer-events-none absolute top-1/2 left-3 size-3 -translate-y-1/2 text-muted-foreground" />
        <input
          {...NO_AUTOCORRECT}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") setQuery("")
          }}
          placeholder="Filter…"
          aria-label="Filter agents"
          className="h-6 w-full rounded border border-input bg-background pr-6 pl-6 text-[12px] outline-none placeholder:text-muted-foreground focus:border-primary"
        />
        {query && (
          <button
            type="button"
            onClick={() => setQuery("")}
            title="Clear filter"
            aria-label="Clear filter"
            className="absolute top-1/2 right-2.5 flex size-4 -translate-y-1/2 items-center justify-center rounded text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <X className="size-3" />
          </button>
        )}
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto p-1">
        {isLoading && (
          <div className="flex items-center justify-center gap-2 py-3 text-[12px] text-muted-foreground">
            <Orb state="working" px={16} />
            loading…
          </div>
        )}
        {error && (
          <div className="px-2 py-3 text-[11.5px] text-destructive">
            could not list agents: {(error as Error).message}
          </div>
        )}
        {!isLoading && !error && agents.length === 0 && (
          <div className="px-2 py-3 text-[11.5px] text-muted-foreground">
            No agents on any connected host.
          </div>
        )}
        {query && agents.length > 0 && shown.length === 0 && (
          <div className="px-2 py-3 text-[11.5px] text-muted-foreground">
            No agents match.
          </div>
        )}
        {shown.map((p) => (
          <Row
            key={paneKey(p)}
            pane={p}
            current={paneKey(p) === current}
            pinned={pinnedSet.has(paneKey(p))}
            onSelect={focusAgent}
            onClose={setClosing}
          />
        ))}
        {/* A host that did not answer is the one reason an agent you expect is
            not here, so it is said rather than left to be inferred. */}
        {unlisted.length > 0 && (
          <div
            className="px-2 py-2 text-[11px] text-muted-foreground"
            title={unlisted.join(", ")}
          >
            {unlisted.length} host{unlisted.length === 1 ? "" : "s"} could not
            be listed
          </div>
        )}
      </div>
      <EndAgentDialog
        open={closing !== null}
        onOpenChange={(open) => {
          if (!open) setClosing(null)
        }}
        name={closing ? agentName(closing) : "this agent"}
        host={closing?.host}
        onEnd={() => {
          if (closing) void closeAgent(closing)
        }}
      />
    </aside>
  )
}

// Pin and Close live in the row's context menu (right-click, or long-press on
// touch) rather than in a button floated over the row, which covered the
// status and the worktree name on hover.
function Row({
  pane,
  current,
  pinned,
  onSelect,
  onClose,
}: {
  pane: HostPane
  current: boolean
  pinned: boolean
  onSelect: (p: HostPane) => void
  onClose: (p: HostPane) => void
}) {
  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <button
          type="button"
          onClick={() => onSelect(pane)}
          aria-current={current ? "true" : undefined}
          className={cn(
            "flex w-full flex-col gap-0.5 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-accent data-[state=open]:bg-accent",
            current && "bg-accent"
          )}
        >
          <AgentLines
            pane={pane}
            current={current}
            meta={
              pinned && (
                <Pin
                  className="size-3 shrink-0 text-primary"
                  aria-label="Pinned"
                />
              )
            }
          />
        </button>
      </ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem
          onSelect={() => setAgentPinned(paneKey(pane), !pinned)}
        >
          {pinned ? <PinOff /> : <Pin />}
          {pinned ? "Unpin" : "Pin to the top"}
        </ContextMenuItem>
        <ContextMenuItem variant="destructive" onSelect={() => onClose(pane)}>
          <Power />
          End agent…
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  )
}

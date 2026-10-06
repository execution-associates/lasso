import { ChevronRight, Pin, PinOff, Power, Search, X } from "lucide-react"
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
  agentNameInGroup,
  buildAgentTree,
  paneKey,
  sortAgentsByPriority,
  useAgents,
} from "@/lib/agents"
import type { HostPane } from "@/lib/api"
import { useApp } from "@/lib/app-store"
import { setAgentPinned, useUIState } from "@/lib/ui-state"
import { cn } from "@/lib/utils"

// The chat's docked left sidebar: every agent lasso can reach, across every
// connected machine, as a tree like herdr's own sidebar: machine → repo →
// agents (buildAgentTree).
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
  const { host: tabHost } = useApp()
  const [query, setQuery] = React.useState("")
  // Collapsed machine and repo nodes, by tree key. Per tab and not persisted:
  // a filter or a new agent can make any of them matter again.
  const [folded, setFolded] = React.useState<Set<string>>(() => new Set())
  const toggleFold = React.useCallback((key: string) => {
    setFolded((prev) => {
      const next = new Set(prev)
      if (!next.delete(key)) next.add(key)
      return next
    })
  }, [])
  // The agents grid's pins lead here too, in the grid's pin order and outside
  // the tree: one pin, one meaning, whichever surface set it (the grid's card
  // or the chat header). Under each machine, the repos and their rows are in
  // priority order (blocked first, a repo ranking by its most urgent agent),
  // so the agent that needs an answer leads its machine.
  const pinnedSet = React.useMemo(() => new Set(pinnedKeys ?? []), [pinnedKeys])
  const { pinned, tree, count } = React.useMemo(() => {
    const terms = query.toLowerCase().split(/\s+/).filter(Boolean)
    const matching = sortAgentsByPriority(agents).filter((p) =>
      agentMatches(p, terms)
    )
    const byKey = new Map(matching.map((p) => [paneKey(p), p]))
    const pinned = (pinnedKeys ?? []).flatMap((k) => byKey.get(k) ?? [])
    const rest = matching.filter((p) => !pinnedSet.has(paneKey(p)))
    return {
      pinned,
      tree: buildAgentTree(rest, tabHost ?? undefined),
      count: matching.length,
    }
  }, [agents, query, pinnedKeys, pinnedSet, tabHost])
  // A filter opens every node: a match hidden under a fold reads as no match.
  const isFolded = (key: string) => !query && folded.has(key)
  // The row whose pane "Close" in its context menu would end. herdr's pane.close
  // stops the agent outright, so it is asked first, as on the Agents tab.
  const [closing, setClosing] = React.useState<HostPane | null>(null)

  return (
    // vsurface: this sits over the terminal (the chat is an overlay), and under
    // the atmosphere --card is translucent — herdr's pane text would read
    // straight through the list. Same opt-out the chat and the file viewer take;
    // inside the chat that opt-out paints the backdrop rather than going flat,
    // so this column carries the same shading as the conversation beside it.
    <aside className="vsurface flex w-60 flex-none flex-col border-border border-r bg-card max-md:hidden">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        <span className="text-[12px] text-muted-foreground">Agents</span>
        {agents.length > 0 && (
          <span className="ml-auto text-[11px] text-muted-foreground">
            {query ? `${count}/${agents.length}` : agents.length}
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
        {query && agents.length > 0 && count === 0 && (
          <div className="px-2 py-3 text-[11.5px] text-muted-foreground">
            No agents match.
          </div>
        )}
        {pinned.map((p) => (
          <Row
            key={paneKey(p)}
            pane={p}
            current={paneKey(p) === current}
            pinned
            onSelect={focusAgent}
            onClose={setClosing}
          />
        ))}
        {tree.map((h) => (
          <section key={h.host} aria-label={h.label} className="mt-1">
            <TreeToggle
              open={!isFolded(h.host)}
              onToggle={() => toggleFold(h.host)}
              className="font-semibold text-[12px] text-foreground"
              title={h.host}
            >
              {h.label}
            </TreeToggle>
            {!isFolded(h.host) &&
              h.groups.map((g) => (
                <div key={g.key} className="ml-2">
                  <TreeToggle
                    open={!isFolded(g.key)}
                    onToggle={() => toggleFold(g.key)}
                    className="text-[12px] text-foreground/80"
                  >
                    {g.label}
                  </TreeToggle>
                  {!isFolded(g.key) && (
                    // The rail is the tree's └: everything on it belongs to
                    // the repo heading above.
                    <div className="ml-2 border-border border-l pl-1">
                      {g.panes.map((p) => (
                        <Row
                          key={paneKey(p)}
                          pane={p}
                          name={agentNameInGroup(p, g)}
                          hideHost
                          current={paneKey(p) === current}
                          pinned={false}
                          onSelect={focusAgent}
                          onClose={setClosing}
                        />
                      ))}
                    </div>
                  )}
                </div>
              ))}
          </section>
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
// A machine or repo heading in the tree: the label, and a chevron that folds
// what is under it.
function TreeToggle({
  open,
  onToggle,
  className,
  title,
  children,
}: {
  open: boolean
  onToggle: () => void
  className?: string
  title?: string
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-expanded={open}
      title={title}
      className={cn(
        "flex w-full min-w-0 items-center gap-1 rounded px-1 py-0.5 text-left hover:bg-accent",
        className
      )}
    >
      <ChevronRight
        className={cn(
          "size-3 shrink-0 text-muted-foreground transition-transform",
          open && "rotate-90"
        )}
      />
      <span className="min-w-0 flex-1 truncate">{children}</span>
    </button>
  )
}

function Row({
  pane,
  name,
  hideHost,
  current,
  pinned,
  onSelect,
  onClose,
}: {
  pane: HostPane
  name?: string
  hideHost?: boolean
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
            name={name}
            hideHost={hideHost}
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

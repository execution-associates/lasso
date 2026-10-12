import { Bot, Inbox, List, Settings } from "lucide-react"
import * as React from "react"

import { AskConfirmContext, ChatSubmitContext } from "@/components/ChatView"
import { AgentChat, AgentsList } from "@/simple/AgentsView"
import { BotChat, BotsList } from "@/simple/BotsList"
import { InboxView } from "@/simple/InboxView"
import {
  isUnseen,
  noteChatSubmitted,
  useInbox,
  useSeenMarks,
} from "@/simple/inbox"
import { TabBar, type TabSpec } from "@/simple/parts"
import { parseSimplePath, type SimpleRoute, simplePath } from "@/simple/route"
import { SimpleSettings } from "@/simple/SimpleSettings"

// The simple app: Inbox, Agents, Bots and Settings, phone-sized and touch
// first. Its own entry under /m, so a wrapper (a Home Screen icon, a
// Capacitor shell) can open it directly. It talks to lasso only through the
// same /api routes as the full UI, from the same origin.

type Tab = "inbox" | "agents" | "bots" | "settings"

function tabOf(r: SimpleRoute): Tab {
  switch (r.view) {
    case "agent":
      return "agents"
    case "bot":
      return "bots"
    default:
      return r.view
  }
}

export function SimpleApp() {
  const [route, setRoute] = React.useState<SimpleRoute>(() =>
    parseSimplePath(window.location.pathname)
  )
  React.useEffect(() => {
    const onPop = () => setRoute(parseSimplePath(window.location.pathname))
    window.addEventListener("popstate", onPop)
    return () => window.removeEventListener("popstate", onPop)
  }, [])

  // Every move inside the app is a history entry marked as ours, so a chat's
  // Back returns to whatever opened it (the Inbox, a list) through history.
  const go = React.useCallback((r: SimpleRoute) => {
    const path = simplePath(r)
    if (path !== window.location.pathname)
      window.history.pushState({ simple: true }, "", path)
    setRoute(r)
  }, [])
  // Back from a chat: the previous entry when this app pushed the chat's, else
  // (opened straight from a link or a reload) the list it belongs to.
  const back = React.useCallback(
    (fallback: SimpleRoute) => {
      if ((window.history.state as { simple?: boolean } | null)?.simple)
        window.history.back()
      else go(fallback)
    },
    [go]
  )

  // The badge means "new since you looked" and is computed here, so it shows
  // from every tab, not only the Inbox.
  const inbox = useInbox()
  const seen = useSeenMarks()
  const unseen = inbox.items.some((i) => isUnseen(i, seen))

  const tabs: TabSpec<Tab>[] = [
    { id: "inbox", label: "Inbox", icon: Inbox, badge: unseen },
    { id: "agents", label: "Agents", icon: List },
    { id: "bots", label: "Bots", icon: Bot },
    { id: "settings", label: "Settings", icon: Settings },
  ]
  // A conversation has its own composer at the bottom, so it is a page of its
  // own with a Back, and the tab bar steps aside.
  const inChat = route.view === "agent" || route.view === "bot"

  return (
    <AskConfirmContext.Provider value={true}>
      <ChatSubmitContext.Provider value={noteChatSubmitted}>
        <div className="chat-overlay fixed inset-0 flex flex-col bg-background pt-[env(safe-area-inset-top)]">
          <main
            // A tablet gets a phone-width column, centred, rather than cards
            // stretched across the screen.
            className={`mx-auto flex min-h-0 w-full max-w-2xl flex-1 flex-col ${inChat ? "pb-[env(safe-area-inset-bottom)]" : ""}`}
          >
            {route.view === "inbox" && <InboxView go={go} />}
            {route.view === "agents" && <AgentsList go={go} />}
            {route.view === "agent" && (
              <AgentChat
                host={route.host}
                pane={route.pane}
                onBack={() => back({ view: "agents" })}
              />
            )}
            {route.view === "bots" && <BotsList go={go} />}
            {route.view === "bot" && (
              <BotChat
                name={route.name}
                onBack={() => back({ view: "bots" })}
              />
            )}
            {route.view === "settings" && <SimpleSettings />}
          </main>
          {!inChat && (
            <TabBar
              tabs={tabs}
              current={tabOf(route)}
              onSelect={(t) => go({ view: t })}
            />
          )}
        </div>
      </ChatSubmitContext.Provider>
    </AskConfirmContext.Provider>
  )
}

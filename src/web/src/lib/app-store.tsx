import * as React from "react"
import { toast } from "sonner"

import { type ActiveState, api } from "@/lib/api"
import {
  handleBrowserOpenEvent,
  handleBrowserProfilesEvent,
} from "@/lib/browser-profiles"
import { subscribeChatText } from "@/lib/chat-text"
import { clientID } from "@/lib/client-id"
import { setTabHost, tabHost, withTabHost } from "@/lib/host"
import { applyMode, subscribeAppearance, watchSystemMode } from "@/lib/mode"
import { handleOpenFileEvent } from "@/lib/open-file"
import { invalidateHostScoped, qk, queryClient } from "@/lib/query"
import { setTermOwner, watchTermIntent } from "@/lib/term-claim"
import { subscribeTerminalText } from "@/lib/terminal-text"
import { applyAtmosphere, refreshTheme, refreshThemeCatalog } from "@/lib/theme"
import { subscribeTypography } from "@/lib/typography"
import { syncUIState } from "@/lib/ui-state"
import { subscribeAtmosphere } from "@/lib/wallpaper"

// App-wide state derived from herdr, kept live over the /api/events SSE stream.
// Components read activeCwd/activePaneID/panesRev reactively and run their own
// effects off them (Files follows the cwd, Diff reloads, the pane switcher's
// cached listing is refreshed on a layout change).
interface AppState {
  activeCwd: string | null
  activePaneID: string | null
  panesRev: number
  themeRev: number
  // The host THIS tab is on ("local" or an alias) and its URL path segment, both
  // straight off this tab's own SSE stream. Another tab moving to another host
  // no longer shows up here — that is the point.
  host: string | null
  hostSlug: string | null
  // The host the focused pane's cwd lives on — normally `host`, but a pane
  // ssh-attached to another host's herdr reports that host instead. Sticky like
  // activeCwd: null only until the first /api/active answer lands.
  cwdHost: string | null
}

// Fired when THIS tab moves to another host, so terminal iframes reload onto
// that host's ttyd session and host-scoped caches are dropped.
export const HOST_CHANGED_EVENT = "lasso:host-changed"

// moveTabToHost points this tab at another host: attach it server-side (pooling
// the connection and spawning its terminals), then record the choice so every
// later request carries it, then reconnect the SSE stream onto that host.
//
// The order matters. Recording the host before the attach returns would leave
// requests addressing a host whose terminals are not up yet; reconnecting the
// stream before recording it would re-subscribe to the old one.
export async function moveTabToHost(host: string) {
  if (tabHost() === host) return
  await api.attachHost(host)
  setTabHost(host)
  window.dispatchEvent(new CustomEvent(HOST_MOVED_EVENT))
}

// Internal: the SSE stream and host-scoped caches listen for this to re-subscribe.
export const HOST_MOVED_EVENT = "lasso:host-moved"

// A server-pushed toast (SSE "notice"): how background work that outlives its
// request — auto-titling a new agent, which finishes long after create-agent
// answered — reports a failure the user would otherwise only see in the log.
interface Notice {
  level?: "error" | "info" | "success"
  title: string
  detail?: string
}

function showNotice(raw: string) {
  let n: Notice
  try {
    n = JSON.parse(raw)
  } catch {
    return
  }
  if (!n?.title) return
  const opts = n.detail ? { description: n.detail } : undefined
  if (n.level === "error") toast.error(n.title, opts)
  else if (n.level === "success") toast.success(n.title, opts)
  else toast.info(n.title, opts)
}

// How long the event stream may carry nothing before it is presumed dead and
// replaced. The server pings every 25s (serveSSE), so this is two missed pings
// plus slack.
const STREAM_STALE_MS = 60000

const AppContext = React.createContext<AppState | undefined>(undefined)

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [state, setState] = React.useState<AppState>({
    activeCwd: null,
    activePaneID: null,
    panesRev: -1,
    themeRev: -1,
    host: null,
    hostSlug: null,
    cwdHost: null,
  })

  // Last host seen on this tab's stream — a change means the tab moved, so the
  // terminals reload and the host-scoped caches are dropped. Tracked in a ref so
  // the SSE handler stays referentially stable.
  const lastHost = React.useRef<string | null>(null)
  // Last seen ui_state_rev — a change means some tab saved the persisted UI
  // prefs; refetch so every open tab converges (see syncUIState's echo guard).
  const lastUIStateRev = React.useRef<number | null>(null)
  // Last seen plugins_rev — the same idea for the plugin listing: a plugin
  // enabled in another browser, or its MCP child dying, re-renders this tab's
  // sidebar strip and Settings (lib/plugins.ts:usePlugins).
  const lastPluginsRev = React.useRef<number | null>(null)

  const apply = React.useCallback((a: ActiveState) => {
    // Who currently owns the shared terminal's size. Kept in a module, not in
    // this store: the resize gate lives inside the ttyd iframes and is not a
    // React consumer.
    setTermOwner(a.term_owner)
    if (a.host) {
      if (lastHost.current !== null && a.host !== lastHost.current) {
        window.dispatchEvent(new CustomEvent(HOST_CHANGED_EVENT))
        // The new host has its own remembered repo/branch/agent + repo list, so
        // drop the cached host-scoped queries; the creator reloads them on open.
        invalidateHostScoped()
      }
      lastHost.current = a.host
    }
    if (typeof a.ui_state_rev === "number") {
      if (
        lastUIStateRev.current !== null &&
        a.ui_state_rev !== lastUIStateRev.current
      ) {
        syncUIState()
      }
      lastUIStateRev.current = a.ui_state_rev
    }
    if (typeof a.plugins_rev === "number") {
      if (
        lastPluginsRev.current !== null &&
        a.plugins_rev !== lastPluginsRev.current
      ) {
        void queryClient.invalidateQueries({ queryKey: qk.plugins })
        // Plugins contribute themes, so the catalog may have changed with
        // no install response to prime it from: re-read both copies of it
        // (lib/theme.ts's own, and the Settings pickers' query).
        void refreshThemeCatalog()
        void queryClient.invalidateQueries({ queryKey: qk.themeCatalog })
      }
      lastPluginsRev.current = a.plugins_rev
    }
    setState((prev) => ({
      activeCwd: a.cwd || prev.activeCwd,
      activePaneID: a.pane_id || prev.activePaneID,
      panesRev: typeof a.panes_rev === "number" ? a.panes_rev : prev.panesRev,
      themeRev: typeof a.theme_rev === "number" ? a.theme_rev : prev.themeRev,
      host: a.host || prev.host,
      hostSlug: a.host_slug || prev.hostSlug,
      cwdHost: a.cwd_host || prev.cwdHost,
    }))
  }, [])

  // Initial state + live SSE updates, re-subscribed whenever this tab moves to
  // another host. An EventSource cannot send a header, so the host rides in the
  // query string; the stream then carries that host's frames only.
  //
  // The stream is also watched, because a dead one is silent: every frame is
  // pushed only on change, so a tab whose stream died simply keeps its last
  // pane and cwd (the Files sidebar stuck on a pane herdr left long ago). Two
  // ways it dies. An EventSource whose reconnect gets a non-200 — a 502 while
  // lasso restarts behind the tunnel, an Access redirect — closes for good and
  // never retries; and a half-open connection (a laptop waking on another
  // network) raises no error at all. So a CLOSED stream is reopened with
  // backoff, one that has carried nothing for STREAM_STALE_MS (the server pings
  // every 25s) is replaced, and a tab coming back into view checks at once.
  React.useEffect(() => {
    let es: EventSource | null = null
    let cancelled = false
    let lastSeen = Date.now()
    let retry: ReturnType<typeof setTimeout> | null = null
    let backoff = 1000
    const seen = () => {
      lastSeen = Date.now()
    }

    const connect = () => {
      if (retry) clearTimeout(retry)
      retry = null
      es?.close()
      seen()
      api
        .active()
        .then((a) => {
          if (!cancelled) apply(a)
        })
        .catch(() => {
          /* SSE will populate */
        })
      // The stream doubles as this tab's presence (uiclients.go), so it carries
      // the client id. An EventSource cannot set a header, hence the query
      // param — the same carrier the host already rides on.
      const url = withTabHost("/api/events")
      const src = new EventSource(
        `${url}${url.includes("?") ? "&" : "?"}client=${encodeURIComponent(clientID())}`
      )
      es = src
      src.addEventListener("open", () => {
        seen()
        backoff = 1000
      })
      src.addEventListener("error", () => {
        // CONNECTING means the browser is retrying on its own; CLOSED means it
        // gave up and nothing will reopen it but us.
        if (cancelled || src !== es || src.readyState !== EventSource.CLOSED)
          return
        if (!retry) {
          retry = setTimeout(start, backoff)
          backoff = Math.min(backoff * 2, 30000)
        }
      })
      src.addEventListener("ping", seen)
      src.addEventListener("active", (e) => {
        seen()
        apply(JSON.parse((e as MessageEvent).data))
      })
      src.addEventListener("notice", (e) => {
        seen()
        showNotice((e as MessageEvent).data)
      })
      // An agent asked to show the human a file (open_file / `lasso open`).
      // lib/open-file.ts gates it on this tab being visible and hands it on.
      src.addEventListener("open-file", (e) => {
        seen()
        handleOpenFileEvent((e as MessageEvent).data)
      })
      // An agent opened (or asked to show) a page in the shared browser, and
      // a profile changed somewhere (lib/browser-profiles.ts).
      src.addEventListener("browser-open", (e) => {
        seen()
        handleBrowserOpenEvent((e as MessageEvent).data)
      })
      src.addEventListener("browser-profiles", () => {
        seen()
        handleBrowserProfilesEvent()
      })
    }

    // A tab that remembers a host from a previous page load must re-attach
    // before subscribing: its terminals may have been retired (or lasso
    // restarted) while it was away, and the iframes are about to ask for them.
    // A reconnect goes through here too, since a dead stream usually means
    // exactly that restart.
    function start() {
      if (cancelled) return
      const remembered = tabHost()
      if (remembered) {
        api
          .attachHost(remembered)
          .catch(() => {
            /* the host may be gone; the stream reports it down */
          })
          .then(() => {
            if (!cancelled) connect()
          })
      } else {
        connect()
      }
    }
    start()

    const check = () => {
      if (cancelled || retry || !es) return
      if (
        es.readyState === EventSource.CLOSED ||
        Date.now() - lastSeen > STREAM_STALE_MS
      )
        start()
    }
    const watchdog = setInterval(check, 15000)
    const onVisible = () => {
      if (document.visibilityState === "visible") check()
    }
    document.addEventListener("visibilitychange", onVisible)
    window.addEventListener("online", check)

    window.addEventListener(HOST_MOVED_EVENT, connect)
    return () => {
      cancelled = true
      if (retry) clearTimeout(retry)
      clearInterval(watchdog)
      document.removeEventListener("visibilitychange", onVisible)
      window.removeEventListener("online", check)
      window.removeEventListener(HOST_MOVED_EVENT, connect)
      es?.close()
    }
  }, [apply])

  // Chrome light/dark follows the stored appearance mode. index.html paints the
  // default class pre-paint (nothing about appearance is in localStorage any
  // more), here we re-assert it from the cache on mount and keep it live as the
  // OS theme flips. An OS flip also changes WHICH palette applies (one is named
  // per scheme — see lib/mode.ts:localPaletteName), so refreshTheme has to
  // re-run on it: without that, a tab wearing a dark palette would keep it
  // through sunrise, chrome tokens and terminals included.
  React.useEffect(() => {
    applyMode()
    watchSystemMode(refreshTheme)
  }, [])

  // Repaint whenever the STORED appearance changes: the first fetch landing (so
  // the pre-paint default converges on what the last human chose), a pick made
  // in this tab, or one made in another browser and delivered by the
  // ui_state_rev bump above. refreshTheme is the full repaint — the html class,
  // the --h-* override, every terminal iframe's palette and the backdrop — and
  // it re-derives everything from the cache, so nothing here remounts and
  // nothing writes back.
  React.useEffect(() => subscribeAppearance(refreshTheme), [])

  // Claim the shared terminal's size whenever a human acts in THIS tab, so the
  // pty follows whoever is actually working rather than whichever background tab
  // last happened to reflow (lib/term-claim.ts).
  React.useEffect(() => watchTermIntent(), [])

  // Repaint the backdrop whenever the persisted per-theme choice changes: the
  // first fetch landing, a pick made in this tab, or one made in another
  // browser and delivered by the ui_state_rev bump above. applyAtmosphere is
  // the chokepoint that reaches both the chrome and every already-loaded
  // terminal iframe, so nothing here has to remount.
  React.useEffect(() => subscribeAtmosphere(applyAtmosphere), [])

  // Apply the typefaces chosen per slot (ui_state.typography) from the fonts
  // enabled plugins contribute, and re-apply whenever either side changes —
  // the same arrangement as the backdrop above (lib/typography.ts).
  React.useEffect(() => subscribeTypography(), [])
  // And how the chat view sets its prose (ui_state.chat_text over a plugin
  // chat style over lasso's defaults), the same way (lib/chat-text.ts).
  React.useEffect(() => subscribeChatText(), [])
  // And the terminals' size, weight, line height and letter spacing
  // (ui_state.terminal_text), written into every xterm (lib/terminal-text.ts).
  React.useEffect(() => subscribeTerminalText(), [])

  // Re-pin the terminals to herdr's theme whenever its theme revision moves
  // (including the priming value, so a reload always converges). The chrome is
  // not repainted here — it's the system-driven Nothing palette. themeRev is a
  // trigger-only dep: refreshTheme() re-fetches /api/theme on each bump (SSE)
  // rather than reading the rev itself.
  // biome-ignore lint/correctness/useExhaustiveDependencies: themeRev is the intentional re-theme trigger
  React.useEffect(() => {
    refreshTheme()
  }, [state.themeRev])

  return <AppContext.Provider value={state}>{children}</AppContext.Provider>
}

export function useApp(): AppState {
  const ctx = React.useContext(AppContext)
  if (ctx === undefined)
    throw new Error("useApp must be used within an AppProvider")
  return ctx
}

// localStorage helpers that never throw (private-mode / disabled storage).
export function lsGet(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}
export function lsSet(key: string, val: string) {
  try {
    localStorage.setItem(key, val)
  } catch {
    /* ignore */
  }
}

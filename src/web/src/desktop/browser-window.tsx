import { QueryClientProvider, useQuery } from "@tanstack/react-query"
import * as React from "react"
import { createRoot } from "react-dom/client"

import "../index.css"
import { LiveBrowser } from "@/components/LiveBrowser"
import { api } from "@/lib/api"
import {
  type BrowserShowRequest,
  DEFAULT_PROFILE,
  profilesOf,
} from "@/lib/browser-profiles"
import { qk, queryClient } from "@/lib/query"

// The Mac app's Browser window (mac/PLAN.md §24): the web Browser tab's Agent
// view (LiveBrowser) and nothing else of the web app. The app serves this
// page to its own web view through a URL scheme it answers with its own
// sign-in, and bridges the page's /cdp websocket natively; it reaches no other
// route. The window's native bar picks the browser and passes agents' show
// requests in, through window.__lassoBrowser:
//
//   select(id)                          show that browser
//   show({profile, tab_id, url, from})  show that browser on that page
//
// The page says it is ready ({op: "ready"}) so the app can hand over what
// arrived before it loaded.

interface NativeShow {
  profile?: string
  tab_id?: string
  url?: string
  from?: string
}

declare global {
  interface Window {
    __lassoBrowser?: {
      select: (id: string) => void
      show: (req: NativeShow) => void
    }
    webkit?: {
      messageHandlers?: Record<string, { postMessage: (m: unknown) => void }>
    }
  }
}

function toApp(m: unknown) {
  window.webkit?.messageHandlers?.lassoPage?.postMessage(m)
}

let seq = 0

function BrowserWindow() {
  const [profile, setProfile] = React.useState(
    () =>
      new URLSearchParams(window.location.search).get("profile") ||
      DEFAULT_PROFILE
  )
  const [show, setShow] = React.useState<BrowserShowRequest | null>(null)

  React.useEffect(() => {
    window.__lassoBrowser = {
      select: (id) => setProfile(id || DEFAULT_PROFILE),
      show: (req) => {
        const id = req.profile || DEFAULT_PROFILE
        setProfile(id)
        setShow({
          profile: id,
          tabId: req.tab_id ?? "",
          url: req.url ?? "",
          from: req.from || "an agent",
          mode: "live",
          seq: ++seq,
        })
      },
    }
    toApp({ op: "ready" })
    return () => {
      window.__lassoBrowser = undefined
    }
  }, [])

  const status = useQuery({
    queryKey: qk.browser,
    queryFn: () => api.browserStatus(),
    retry: false,
    staleTime: 5_000,
    refetchInterval: 30_000,
  })
  const current = profilesOf(status.data).find((p) => p.id === profile)
  const wsPath =
    current?.ws_path ||
    (profile === DEFAULT_PROFILE
      ? "/cdp"
      : `/cdp/p/${encodeURIComponent(profile)}`)

  if (status.isError || status.data?.available === false) {
    const why = status.isError
      ? status.error instanceof Error
        ? status.error.message
        : "status unavailable"
      : status.data?.reason || "no Chromium was found"
    return (
      <div className="flex h-screen items-center justify-center p-6 text-center text-muted-foreground text-sm">
        This lasso has no browser to show: {why}.
      </div>
    )
  }

  return (
    <div className="flex h-screen flex-col bg-background">
      <LiveBrowser
        // A browser is its own Chromium: switching is a fresh connection.
        key={profile}
        profile={profile}
        wsPath={wsPath}
        active
        openRequest={null}
        onOpened={() => {}}
        showRequest={show?.profile === profile ? show : null}
        modeSwitch={null}
        footer={null}
      />
    </div>
  )
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserWindow />
    </QueryClientProvider>
  </React.StrictMode>
)

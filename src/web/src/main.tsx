import { QueryClientProvider } from "@tanstack/react-query"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"

import "./index.css"
import { App } from "@/App"
import { ErrorBoundary } from "@/components/ErrorBoundary"
import { queryClient } from "@/lib/query"

// Under /bots the page offers itself as the Bots app: installing it from here
// ("Add to Home Screen", Chrome's install) uses this manifest, whose start URL
// and scope are the Bots view. Swapped before render so it is in place when a
// browser reads it.
if (window.location.pathname.startsWith("/bots")) {
  document
    .querySelector('link[rel="manifest"]')
    ?.setAttribute("href", "/manifest-bots.json")
  document
    .querySelector('meta[name="apple-mobile-web-app-title"]')
    ?.setAttribute("content", "Bots")
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      {/* A render error with nothing to catch it unmounts the whole tree, and
          a blank window says nothing about what happened or what to do. */}
      <ErrorBoundary
        label="root"
        fallback={(err) => (
          <div className="flex h-screen flex-col items-center justify-center gap-3 p-6 text-center text-muted-foreground text-xs">
            <div className="text-foreground text-sm">lasso hit an error.</div>
            <pre className="max-w-full overflow-auto text-[11px] opacity-70">
              {err.message}
            </pre>
            <button
              type="button"
              className="rounded border border-border px-3 py-1 hover:bg-accent"
              onClick={() => window.location.reload()}
            >
              reload
            </button>
          </div>
        )}
      >
        <App />
      </ErrorBoundary>
    </QueryClientProvider>
  </StrictMode>
)

import path from "node:path"
import tailwindcss from "@tailwindcss/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// In dev, Vite serves the SPA with HMR and proxies the data + terminal routes
// to the running Go server (default :8090; override with LASSO_BACKEND). SSE
// (/api/events, /api/livereload) rides the plain HTTP proxy; the ttyd terminals
// need WebSocket upgrade (ws: true). changeOrigin is off on every route: lasso's
// local guard refuses a write or websocket whose Origin differs from its Host,
// so the browser's Host has to reach it. The Go server still embeds the
// production build (web/dist) for the non-dev binary.
const backend = process.env.LASSO_BACKEND || "http://127.0.0.1:8090"

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  // Three pages: the app, and two of the Mac app's windows, which lasso serves
  // as plain files: the Browser window (the Agent view alone,
  // src/desktop/browser-window.tsx) as /desktop-browser.html, and the file
  // window (one file's editor or rendered markdown,
  // src/desktop/file-window.tsx) as /desktop-file.html.
  build: {
    rollupOptions: {
      input: {
        main: path.resolve(__dirname, "index.html"),
        "desktop-browser": path.resolve(__dirname, "desktop-browser.html"),
        "desktop-file": path.resolve(__dirname, "desktop-file.html"),
      },
    },
  },
  server: {
    // Reached over tailscale by whatever MagicDNS name the machine has, which
    // can change — so don't hardcode it. `true` accepts any Host header, which
    // is safe here because the dev server only listens on loopback, reached
    // from the tailnet through `tailscale serve` (scripts/container-dev-web.sh).
    allowedHosts: true,
    proxy: {
      "/api": { target: backend, changeOrigin: false },
      "/omarchy": { target: backend, changeOrigin: false },
      // Plugin pages (and the SDK under /plugins/_sdk/). Without this Vite
      // answers with lasso's own index.html, whose module scripts a sandboxed
      // frame cannot load, so every plugin tab and view renders blank in dev.
      "/plugins": { target: backend, changeOrigin: false },
      // A bot job's webhook URL is built from the page's origin, so in dev
      // it points here.
      "/hooks": { target: backend, changeOrigin: false },
      "/terminal": { target: backend, changeOrigin: false, ws: true },
      // The shared browser's CDP endpoint. changeOrigin stays OFF: /cdp refuses
      // a websocket whose Origin names a different host than its Host header
      // (cdpOriginAllowed, the cross-site hijacking guard), and rewriting Host
      // to the backend while the page's Origin stays the dev server would fail
      // it. The Host passing through also makes /cdp/json's rewritten
      // websocket URLs point back at this dev server.
      "/cdp": { target: backend, changeOrigin: false, ws: true },
    },
  },
})

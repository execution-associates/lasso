import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// The page itself is ui/index.html, written by hand and never processed: it
// loads lasso's SDK from absolute /plugins/_sdk/ URLs that only exist when
// lasso serves the page, which Vite would otherwise try to resolve. The build
// emits just the app, at fixed names that page references, into ../ui.
//
// A classic script (IIFE), not an ES module: a module script is fetched in
// CORS mode, and the plugin page's origin is opaque (lasso serves it with a
// CSP sandbox), so the browser refuses a module from lasso's own server.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../ui",
    emptyOutDir: false,
    cssCodeSplit: false,
    rollupOptions: {
      input: "src/main.tsx",
      output: {
        format: "iife",
        entryFileNames: "openbot.js",
        chunkFileNames: "openbot-[name].js",
        assetFileNames: "openbot.[ext]",
      },
    },
  },
})

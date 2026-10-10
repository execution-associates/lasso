// lasso plugin SDK (protocol lasso-plugin/1). Served by lasso at
// /plugins/_sdk/lasso.js; load it from a plugin page with
//
//   <link rel="stylesheet" href="/plugins/_sdk/lasso.css">
//   <script src="/plugins/_sdk/lasso.js"></script>
//
// and the page wears lasso's look: its design tokens as CSS custom properties
// under lasso's own names (--background, --primary, --font-sans, --radius, ...),
// the terminal palette as --term-<name>, `dark` on <html> in dark mode, and
// lasso's fonts registered with FontFace. All of it follows lasso live.
//
// It also wraps the bridge: lasso.call(method, params) and the shortcuts
// below, and lasso.on("context" | "theme", fn). See docs/plugins/authoring.md.
;(() => {
  if (window.lasso) return

  const root = document.documentElement
  const TIMEOUT_MS = 15000
  const NAME = /^--[a-z0-9-]{1,64}$/
  let seq = 0
  const pending = new Map()
  const listeners = { context: new Set(), theme: new Set() }
  let theme = null
  let context = null
  let fontsKey = null

  function call(method, params) {
    const id = ++seq
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id)
        reject(new Error(`lasso: ${method} timed out (is this page framed by lasso?)`))
      }, TIMEOUT_MS)
      pending.set(id, { resolve, reject, timer })
      window.parent.postMessage({ lasso: 1, id, method, params: params || {} }, "*")
    })
  }

  // Only lasso's values are written, and only under well-formed names: a
  // value is set with setProperty, so it can never become a rule of its own.
  function applyTheme(t) {
    if (!t || typeof t !== "object") return
    theme = t
    root.classList.toggle("dark", !!t.dark)
    root.style.colorScheme = t.dark ? "dark" : "light"
    for (const [name, value] of Object.entries(t.tokens || {})) {
      if (NAME.test(name) && typeof value === "string") root.style.setProperty(name, value)
    }
    for (const [name, value] of Object.entries(t.colors || {})) {
      const prop = `--term-${String(name).replace(/[A-Z]/g, (c) => `-${c.toLowerCase()}`)}`
      if (NAME.test(prop) && typeof value === "string") root.style.setProperty(prop, value)
    }
    if (t.fonts_key && t.fonts_key !== fontsKey) {
      fontsKey = t.fonts_key
      loadFonts()
    }
  }

  const added = []
  async function loadFonts() {
    const key = fontsKey
    let faces
    try {
      faces = (await call("fonts.get")).faces || []
    } catch {
      return // an older lasso: the stacks fall back to their next family
    }
    if (key !== fontsKey || !("FontFace" in window)) return
    for (const f of added.splice(0)) document.fonts.delete(f)
    for (const face of faces) {
      try {
        const ff = new FontFace(face.family, face.data, {
          weight: face.weight,
          style: face.style,
          unicodeRange: face.unicode_range || undefined,
          display: "swap",
        })
        document.fonts.add(ff)
        added.push(ff)
        ff.load().catch(() => {})
      } catch {
        // one bad face must not cost the rest
      }
    }
  }

  window.addEventListener("message", (e) => {
    if (e.source !== window.parent) return
    const m = e.data
    if (!m || m.lasso !== 1) return
    if (typeof m.event === "string") {
      if (m.event === "theme") applyTheme(m.data)
      if (m.event === "context") context = m.data
      for (const fn of listeners[m.event] || []) {
        try {
          fn(m.data)
        } catch (err) {
          console.error(err)
        }
      }
      return
    }
    const p = pending.get(m.id)
    if (!p) return
    pending.delete(m.id)
    clearTimeout(p.timer)
    if ("error" in m) p.reject(new Error(m.error))
    else p.resolve(m.result)
  })

  window.lasso = {
    call,
    // fn runs on every push of that event; returns an unsubscribe.
    on(event, fn) {
      const set = listeners[event]
      if (!set) throw new Error(`lasso: no event "${event}"`)
      set.add(fn)
      return () => set.delete(fn)
    },
    // The last pushed values (null until the first push).
    get theme() {
      return theme
    },
    get context() {
      return context
    },
    getContext: () => call("context.get"),
    getTheme: () => call("theme.get"),
    openFile: (path, opts) => call("file.open", { path, ...(opts || {}) }),
    tool: (name, args) => call("tool.call", { tool: name, arguments: args || {} }),
    toast: (message) => call("toast", { message }),
  }

  // lasso pushes both on load; asking too covers a script that ran late.
  call("theme.get").then(applyTheme, () => {})
  call("context.get").then((c) => {
    context = c
  }, () => {})
})()

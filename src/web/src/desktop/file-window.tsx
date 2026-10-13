import type { EditorView } from "@codemirror/view"
import * as React from "react"
import { createRoot } from "react-dom/client"

import "../index.css"
import { CodeEditor } from "@/components/CodeEditor"
import { Markdown, resolveMarkdownSrc } from "@/components/Markdown"
import { ApiError, api, FileConflictError } from "@/lib/api"
import {
  type FileDoc,
  type FileDocAction,
  type FileMode,
  type FileStateReport,
  fileDocReducer,
  fileStateReport,
  initialFileDoc,
  sameReport,
} from "@/lib/file-doc"
import { setTelemetryActive } from "@/lib/telemetry"

// The Mac app's file window (mac/PLAN.md §26): one file's editor, or its
// rendered markdown, and nothing else of the web app. The app serves this page
// to its own web view through a URL scheme whose bridge allows exactly three
// requests — this page and its /assets/, GET /api/file for the document and
// its markdown images, and POST /api/file-write for the document — so nothing
// here may reach another route: no app store, no SSE, no ui-state. The window's
// native bar carries every control, and the native side watches the disk and
// calls in through window.__lassoFile:
//
//   changed()        the document changed on disk: refetch, replace a clean
//                    buffer (keeping the scroll), raise a conflict over a dirty
//                    one
//   assetsChanged()  a markdown image changed: reload them all
//   setMode(mode)    "rich" | "raw"
//   save()           write the buffer, refused (409) over a disk it never saw
//   takeDisk()       answer a conflict: drop the buffer for the disk
//   keepMine()       answer a conflict: keep the buffer, overwrite on save
//   draft()          the buffer, for previewing unsaved HTML natively
//   load({path, host, kind, mode, line})   re-target the window
//
// and hears back through the lassoPage handler: {op: "ready"}, {op:
// "file-state", ...} whenever the state the bar shows moves (readOnly: the
// file is over the server's preview cap, so the page holds a placeholder that
// must never be saved), and {op: "file-saved", mtime} after each save.
//
// Rich HTML is rendered natively, in a separate web view; for that kind this
// page shows nothing in rich mode, but keeps the buffer (and the editor, with
// its undo history) alive underneath.

// Analytics are the Mac app's own (Analytics.swift); /api/telemetry is not a
// route this page may reach.
setTelemetryActive(false)

// Follow the system appearance with the app's own tokens: index.css's palette
// is dark on :root and light under .light, and Tailwind's dark variant keys off
// .dark. The web app decides this in lib/mode.ts from ui-state, which this
// page cannot read.
const scheme = window.matchMedia("(prefers-color-scheme: dark)")
function applyScheme() {
  const el = document.documentElement
  el.classList.toggle("dark", scheme.matches)
  el.classList.toggle("light", !scheme.matches)
}
applyScheme()
scheme.addEventListener("change", applyScheme)

type FileKind = "markdown" | "html" | "svg" | "text"

interface Target {
  path: string
  host: string
  kind: FileKind
}

interface LoadRequest {
  path?: string
  host?: string
  kind?: string
  mode?: string
  line?: number | null
}

declare global {
  interface Window {
    __lassoFile?: {
      changed: () => void
      assetsChanged: () => void
      setMode: (mode: FileMode) => void
      save: () => void
      takeDisk: () => void
      keepMine: () => void
      draft: () => string
      load: (req: LoadRequest) => void
    }
    webkit?: {
      messageHandlers?: Record<string, { postMessage: (m: unknown) => void }>
    }
  }
}

function toApp(m: unknown) {
  window.webkit?.messageHandlers?.lassoPage?.postMessage(m)
}

// The same document, whatever kind or mode it is shown in.
function sameFile(a: Target, b: Target): boolean {
  return a.path === b.path && a.host === b.host
}

function kindOf(k: string | null | undefined): FileKind {
  return k === "markdown" || k === "html" || k === "svg" ? k : "text"
}

// Plain text has no rendered form, so it is always raw. A requested line is
// only visible in the editor, so it opens raw too.
function modeFor(
  kind: FileKind,
  mode: string | null | undefined,
  line: number | null
): FileMode {
  if (kind === "text" || line) return "raw"
  if (mode === "raw" || mode === "rich") return mode
  return "rich"
}

function lineOf(v: unknown): number | null {
  const n = typeof v === "number" ? v : Number.parseInt(String(v ?? ""), 10)
  return Number.isFinite(n) && n > 0 ? n : null
}

const query = new URLSearchParams(window.location.search)
const firstLine = lineOf(query.get("line"))
const firstTarget: Target = {
  path: query.get("path") || "",
  host: query.get("host") || "local",
  kind: kindOf(query.get("kind")),
}
const firstMode = modeFor(firstTarget.kind, query.get("mode"), firstLine)

// Where the reader was, taken just before a changed-on-disk read replaces a
// clean buffer and put back once the new text is on screen.
interface Spot {
  editorTop: number | null
  anchor: number | null
  bodyTop: number
}

function FileWindow() {
  const [target, setTarget] = React.useState<Target>(firstTarget)
  const [lineReq, setLineReq] = React.useState({ line: firstLine, seq: 0 })
  // Cache-buster for every /api/file URL the page renders (markdown images, the
  // SVG): bumped when the native watcher says one changed, so the web view
  // refetches instead of reusing the bytes it has.
  const [v, setV] = React.useState(0)

  // The document's state lives in a ref that every action updates
  // synchronously, mirrored into React state to render. The native calls are
  // installed once and read the ref, so a save() fired twice before a render
  // still sees the first one in flight.
  const docRef = React.useRef<FileDoc>(initialFileDoc(firstMode))
  const [doc, setDoc] = React.useState<FileDoc>(docRef.current)
  const apply = React.useCallback((a: FileDocAction) => {
    docRef.current = fileDocReducer(docRef.current, a)
    setDoc(docRef.current)
  }, [])

  const targetRef = React.useRef(target)
  targetRef.current = target
  const viewRef = React.useRef<EditorView | null>(null)
  const bodyRef = React.useRef<HTMLDivElement | null>(null)
  const spot = React.useRef<Spot | null>(null)
  // Each read is numbered so a slow one landing after a newer one (or after a
  // re-target) is dropped instead of winning.
  const readSeq = React.useRef(0)
  // A changed() that arrived mid-save, run again once the save settles.
  const changedAgain = React.useRef(false)
  // The server sent its "too large to preview" placeholder, not the file
  // (a text file over 2 MiB): shown read-only and never saved, or the
  // placeholder would replace the real file on disk.
  const [readOnly, setReadOnlyState] = React.useState(false)
  const readOnlyRef = React.useRef(false)
  const setReadOnly = React.useCallback((v: boolean) => {
    readOnlyRef.current = v
    setReadOnlyState(v)
  }, [])

  const load = React.useCallback(
    async (t: Target) => {
      const seq = ++readSeq.current
      try {
        const r = await api.fileTextStamped(t.path, t.host)
        if (seq !== readSeq.current) return
        setReadOnly(r.tooLarge)
        apply({ type: "loaded", text: r.text, mtime: r.mtime })
      } catch (e) {
        if (seq !== readSeq.current) return
        const missing = e instanceof ApiError && e.status === 404
        apply({ type: "load-failed", error: (e as Error).message, missing })
      }
    },
    [apply, setReadOnly]
  )

  const changed = React.useCallback(async () => {
    const t = targetRef.current
    if (t.kind === "svg") setV((n) => n + 1)
    if (docRef.current.saving) {
      changedAgain.current = true
      return
    }
    const seq = ++readSeq.current
    try {
      const r = await api.fileTextStamped(t.path, t.host)
      if (seq !== readSeq.current) return
      const before = docRef.current
      const replaces =
        before.draft != null &&
        before.draft === before.text &&
        r.text !== before.text
      if (replaces) {
        const view = viewRef.current
        spot.current = {
          editorTop: view ? view.scrollDOM.scrollTop : null,
          anchor: view ? view.state.selection.main.anchor : null,
          bodyTop: bodyRef.current?.scrollTop ?? 0,
        }
      }
      if (r.tooLarge !== readOnlyRef.current) {
        // Grown past (or back under) the preview cap: the buffer becomes the
        // disk's answer either way, since a placeholder is never edited.
        setReadOnly(r.tooLarge)
        apply({ type: "loaded", text: r.text, mtime: r.mtime })
        return
      }
      apply({ type: "disk", text: r.text, mtime: r.mtime })
    } catch (e) {
      if (seq !== readSeq.current) return
      if (e instanceof ApiError && e.status === 404)
        apply({ type: "disk-missing" })
      // Anything else is a blip (a host reconnecting): keep what is shown,
      // and the next change on disk asks again.
    }
  }, [apply, setReadOnly])

  const save = React.useCallback(async () => {
    const s = docRef.current
    if (!s.loaded || s.draft == null || s.saving || readOnlyRef.current) return
    const t = targetRef.current
    const content = s.draft
    apply({ type: "save-start" })
    try {
      const r = await api.writeFile(t.path, content, t.host, s.mtime)
      if (!sameFile(targetRef.current, t)) return
      const mtime = typeof r.mtime === "number" ? r.mtime : null
      apply({ type: "save-ok", content, mtime })
      if (t.kind === "svg") setV((n) => n + 1)
      toApp({ op: "file-saved", mtime })
    } catch (e) {
      if (!sameFile(targetRef.current, t)) return
      if (e instanceof FileConflictError) {
        apply({ type: "save-conflict", mtime: e.mtime })
        // Learn what the disk now holds, so keepMine has a base to adopt.
        changedAgain.current = true
      } else {
        apply({ type: "save-failed", error: (e as Error).message })
      }
    } finally {
      if (changedAgain.current && sameFile(targetRef.current, t)) {
        changedAgain.current = false
        void changed()
      }
    }
  }, [apply, changed])

  // The first file, from the query.
  React.useEffect(() => {
    if (firstTarget.path) void load(firstTarget)
    else apply({ type: "load-failed", error: "no path", missing: false })
  }, [load, apply])

  // Put the reader back where they were once a clean buffer has been replaced.
  // Runs after CodeMirror's own effect has applied the new value (a child's
  // effects run before its parent's), and once more after its next measure.
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new base text is the trigger; the spot is read from the ref
  React.useEffect(() => {
    const at = spot.current
    if (!at) return
    spot.current = null
    const restore = () => {
      const view = viewRef.current
      if (view && at.editorTop != null) {
        if (at.anchor != null) {
          view.dispatch({
            selection: { anchor: Math.min(at.anchor, view.state.doc.length) },
          })
        }
        view.scrollDOM.scrollTop = at.editorTop
      }
      if (bodyRef.current) bodyRef.current.scrollTop = at.bodyTop
    }
    restore()
    requestAnimationFrame(restore)
  }, [doc.text])

  React.useEffect(() => {
    window.__lassoFile = {
      changed: () => void changed(),
      assetsChanged: () => setV((n) => n + 1),
      setMode: (mode) => {
        const t = targetRef.current
        apply({ type: "mode", mode: modeFor(t.kind, mode, null) })
      },
      save: () => void save(),
      takeDisk: () => {
        const t = targetRef.current
        void (async () => {
          const seq = ++readSeq.current
          try {
            const r = await api.fileTextStamped(t.path, t.host)
            if (seq !== readSeq.current) return
            setReadOnly(r.tooLarge)
            apply({ type: "loaded", text: r.text, mtime: r.mtime })
          } catch (e) {
            if (seq !== readSeq.current) return
            if (e instanceof ApiError && e.status === 404)
              apply({ type: "disk-missing" })
          }
        })()
      },
      keepMine: () => apply({ type: "keep-mine" }),
      draft: () => docRef.current.draft ?? "",
      load: (req) => {
        const cur = targetRef.current
        const next: Target = {
          path: req.path || cur.path,
          host: req.host || cur.host,
          kind: req.kind ? kindOf(req.kind) : cur.kind,
        }
        const line = lineOf(req.line)
        const mode = modeFor(next.kind, req.mode, line)
        if (sameFile(next, cur)) {
          // The same file asked for again (a line jump): never a reload,
          // which would drop unsaved edits. A look at the disk is safe.
          if (next.kind !== cur.kind) setTarget(next)
          apply({ type: "mode", mode })
          setLineReq((r) => ({ line, seq: r.seq + 1 }))
          void changed()
          return
        }
        setTarget(next)
        targetRef.current = next
        changedAgain.current = false
        apply({ type: "reset", mode })
        setLineReq((r) => ({ line, seq: r.seq + 1 }))
        setV(0)
        void load(next)
      },
    }
    toApp({ op: "ready" })
    return () => {
      window.__lassoFile = undefined
    }
  }, [apply, changed, load, save, setReadOnly])

  // Tell the native bar whenever what it shows moves.
  const report = fileStateReport(doc)
  const posted = React.useRef<(FileStateReport & { readOnly: boolean }) | null>(
    null
  )
  React.useEffect(() => {
    if (
      posted.current &&
      sameReport(posted.current, report) &&
      posted.current.readOnly === readOnly
    )
      return
    posted.current = { ...report, readOnly }
    toApp({ op: "file-state", ...report, readOnly })
  })

  // ⌘S saves. The native window passes the key through to the page, the same
  // as ⌘Z and ⌘F reach CodeMirror.
  React.useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (
        (e.metaKey || e.ctrlKey) &&
        !e.shiftKey &&
        e.key.toLowerCase() === "s"
      ) {
        e.preventDefault()
        void save()
      }
    }
    document.addEventListener("keydown", onKey)
    return () => document.removeEventListener("keydown", onKey)
  }, [save])

  // Rebuilt only when the file, its host or the cache-buster moves: a new
  // resolver identity rebuilds the markdown's components map (and every
  // mermaid diagram in it).
  const resolveImage = React.useMemo(
    () => (src: string | undefined) => {
      const u = resolveMarkdownSrc(src, target.path, target.host)
      return u?.startsWith("/api/file?") ? `${u}&v=${v}` : u
    },
    [target.path, target.host, v]
  )

  const onEdit = React.useCallback(
    (draft: string) => apply({ type: "edit", draft }),
    [apply]
  )
  const onView = React.useCallback((view: EditorView | null) => {
    viewRef.current = view
  }, [])

  const raw = doc.mode === "raw" || target.kind === "text"
  const notShown =
    doc.loadError != null
      ? `Couldn't open this file: ${doc.loadError}`
      : doc.missing && doc.draft == null
        ? "This file isn't there."
        : !doc.loaded
          ? "loading…"
          : null

  return (
    <div className="flex h-screen flex-col bg-background">
      {notShown != null ? (
        <div className="vloading">{notShown}</div>
      ) : (
        doc.draft != null && (
          <>
            {!raw && target.kind === "markdown" && (
              <div ref={bodyRef} className="vbody">
                <div className="md-body">
                  <Markdown source={doc.draft} resolveImageSrc={resolveImage} />
                </div>
              </div>
            )}
            {!raw && target.kind === "svg" && (
              <div
                ref={bodyRef}
                className="vbody flex items-center justify-center p-4"
              >
                <img
                  className="max-h-full max-w-full"
                  src={`${api.fileURL(target.path, target.host)}&v=${v}`}
                  alt={target.path}
                />
              </div>
            )}
            {/* Mounted for every kind and only hidden in rich mode, so the
                buffer, its undo history and its scroll survive a mode switch. */}
            <div className={raw ? "vbody" : "hidden"}>
              <CodeEditor
                value={doc.draft}
                path={target.path}
                onChange={onEdit}
                readOnly={readOnly}
                changedLines={NO_LINES}
                line={lineReq.line}
                lineSeq={lineReq.seq}
                onView={onView}
              />
            </div>
          </>
        )
      )}
    </div>
  )
}

const NO_LINES: number[] = []

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <FileWindow />
  </React.StrictMode>
)

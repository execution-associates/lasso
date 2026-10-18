import { useQuery } from "@tanstack/react-query"
import { File } from "lucide-react"
import * as React from "react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import { api } from "@/lib/api"
import { type FuzzyResult, fuzzyFilter, splitLineSuffix } from "@/lib/fuzzy"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

const LIMIT = 60

// Files opened through quick open this session, newest first, so an empty
// query offers them like VS Code's "recently opened". Keyed by host because
// the same path on two machines is two files. In memory only: it is where
// you were this session, like the per-pane viewer state in FilesPanel.
const recent: { host: string; path: string }[] = []
function noteRecent(host: string, path: string) {
  const i = recent.findIndex((r) => r.host === host && r.path === path)
  if (i >= 0) recent.splice(i, 1)
  recent.unshift({ host, path })
  recent.length = Math.min(recent.length, 20)
}

// ⌘P in the file viewer: fuzzy-find a file under the Files tree's root and
// open it. The list is fetched once per open (and cached briefly per host and
// root), then filtered in the browser on every keystroke.
export function QuickOpen({
  open,
  onOpenChange,
  root,
  host,
  onPick,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  // The directory searched (the Files tree's root) and the host it is on.
  root: string | null
  host: string | null
  onPick: (path: string, line: number | null) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showCloseButton={false}
        // Pinned near the top like VS Code's picker, so the list grows down
        // instead of the input jumping as results arrive.
        className="top-[12%] translate-y-0 grid-cols-1 gap-0 overflow-hidden p-0 sm:max-w-xl"
      >
        <DialogTitle className="sr-only">Go to file</DialogTitle>
        <DialogDescription className="sr-only">
          Type part of a file's name or path; Enter opens it. A trailing :line
          jumps to that line.
        </DialogDescription>
        {/* Mounted only while open, so the query starts empty every time. */}
        {open && root && (
          <QuickOpenBody
            root={root}
            host={host}
            onPick={(path, line) => {
              noteRecent(host ?? "", path)
              onOpenChange(false)
              onPick(path, line)
            }}
          />
        )}
        {open && !root && (
          <div className="px-3 py-3 text-[12px] text-muted-foreground">
            No folder is open in Files yet.
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function QuickOpenBody({
  root,
  host,
  onPick,
}: {
  root: string
  host: string | null
  onPick: (path: string, line: number | null) => void
}) {
  const listing = useQuery({
    queryKey: qk.fileFind(host ?? "", root),
    queryFn: () => api.fileFind(root, host ?? undefined),
    // Re-listed when the picker reopens after this, so a file an agent just
    // wrote shows up; reopening within it reuses the list.
    staleTime: 10_000,
    gcTime: 5 * 60_000,
    retry: false,
  })
  const [query, setQuery] = React.useState("")
  const [active, setActive] = React.useState(0)
  const listRef = React.useRef<HTMLDivElement>(null)
  const navSource = React.useRef<"keyboard" | "pointer">("keyboard")

  const base = root.replace(/\/+$/, "")
  const { query: terms, line } = splitLineSuffix(query)

  const results: FuzzyResult[] = React.useMemo(() => {
    const files = listing.data?.files ?? []
    if (terms.trim()) return fuzzyFilter(files, terms, LIMIT)
    // Empty query: this session's recent files under this root, then the
    // shallowest files.
    const prefix = `${base}/`
    const mine = recent
      .filter((r) => r.host === (host ?? "") && r.path.startsWith(prefix))
      .map((r) => r.path.slice(prefix.length))
    const known = new Set(files)
    const recents = mine.filter((p) => known.has(p))
    const taken = new Set(recents)
    const rest = fuzzyFilter(files, "", LIMIT).filter((r) => !taken.has(r.path))
    return [
      ...recents.map((path) => ({ path, score: 0, positions: [] })),
      ...rest,
    ].slice(0, LIMIT)
  }, [listing.data, terms, base, host])

  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on filter change
  React.useEffect(() => {
    setActive(0)
  }, [terms])

  const clamped = Math.min(active, Math.max(results.length - 1, 0))

  React.useEffect(() => {
    if (navSource.current !== "keyboard") return
    listRef.current
      ?.querySelector<HTMLElement>(`[data-index="${clamped}"]`)
      ?.scrollIntoView({ block: "nearest" })
  }, [clamped])

  const choose = (r: FuzzyResult) => onPick(`${base}/${r.path}`, line)

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) {
      e.preventDefault()
      navSource.current = "keyboard"
      setActive(Math.min(clamped + 1, results.length - 1))
    } else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) {
      e.preventDefault()
      navSource.current = "keyboard"
      setActive(Math.max(clamped - 1, 0))
    } else if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      e.preventDefault()
      const r = results[clamped]
      if (r) choose(r)
    }
  }

  const total = listing.data?.files.length ?? 0

  return (
    <div className="flex min-w-0 flex-col">
      <input
        // biome-ignore lint/a11y/noAutofocus: a picker opened by a shortcut exists to be typed into
        autoFocus
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={`Go to file in ${baseName(base)}…`}
        aria-label="Find a file"
        {...NO_AUTOCORRECT}
        className="w-full border-border border-b bg-transparent px-3 py-2.5 text-sm outline-none placeholder:text-muted-foreground"
      />
      <div
        ref={listRef}
        role="listbox"
        aria-label="Files"
        className="max-h-[min(60vh,28rem)] overflow-y-auto p-1"
      >
        {listing.isLoading && (
          <div className="flex items-center justify-center gap-2 py-3 text-[12px] text-muted-foreground">
            <Orb state="working" px={16} />
            listing files…
          </div>
        )}
        {listing.error && (
          <div className="px-2 py-3 text-[11.5px] text-destructive">
            could not list files: {(listing.error as Error).message}
          </div>
        )}
        {listing.data && results.length === 0 && (
          <div className="px-2 py-3 text-[11.5px] text-muted-foreground">
            {total === 0 ? "No files here." : "No matching files."}
          </div>
        )}
        {results.map((r, i) => (
          <button
            key={r.path}
            type="button"
            role="option"
            aria-selected={i === clamped}
            data-index={i}
            onClick={() => choose(r)}
            onMouseMove={() => {
              navSource.current = "pointer"
              if (i !== clamped) setActive(i)
            }}
            className={cn(
              "flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1 text-left outline-none",
              i === clamped && "bg-accent ring-1 ring-primary/60"
            )}
          >
            <File className="size-3.5 flex-none text-muted-foreground" />
            <ResultLabel result={r} />
          </button>
        ))}
      </div>
      {listing.data?.truncated && (
        <div className="border-border border-t px-3 py-1.5 text-[11px] text-muted-foreground">
          Searched the first {total.toLocaleString()} files only.
        </div>
      )}
    </div>
  )
}

// The file's name, then its directory dimmed, with the matched characters
// highlighted in both.
function ResultLabel({ result }: { result: FuzzyResult }) {
  const { path, positions } = result
  const cut = path.lastIndexOf("/") + 1
  const hit = new Set(positions)
  const marked = (from: number, to: number) => {
    const out: React.ReactNode[] = []
    let run = ""
    let on = false
    const flush = (i: number) => {
      if (!run) return
      out.push(
        on ? (
          <span key={i} className="font-semibold text-primary">
            {run}
          </span>
        ) : (
          run
        )
      )
      run = ""
    }
    for (let i = from; i < to; i++) {
      const h = hit.has(i)
      if (h !== on) {
        flush(i)
        on = h
      }
      run += path[i]
    }
    flush(to)
    return out
  }
  return (
    <span className="flex min-w-0 items-baseline gap-2">
      <span className="flex-none text-[13px]">{marked(cut, path.length)}</span>
      {cut > 0 && (
        <span className="truncate text-[11.5px] text-muted-foreground">
          {marked(0, cut - 1)}
        </span>
      )}
    </span>
  )
}

function baseName(p: string) {
  return p.slice(p.lastIndexOf("/") + 1) || p
}

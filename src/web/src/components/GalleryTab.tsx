import { useQueries, useQuery } from "@tanstack/react-query"
import {
  ArrowUp,
  Film,
  Folder,
  Grid2x2,
  Grid3x3,
  ImageIcon,
  ImageOff,
  Music,
  Play,
  RefreshCw,
} from "lucide-react"
import * as React from "react"
import { Orb } from "@/components/ui/orb"
import { api, type FileEntry } from "@/lib/api"
import { fmtSize, isAudio, isImage, isVideo, tilde } from "@/lib/format"
import { cn } from "@/lib/utils"

type Kind = "image" | "video" | "audio"
type Filter = "all" | Kind

const kindOf = (name: string): Kind | null =>
  isImage(name)
    ? "image"
    : isVideo(name)
      ? "video"
      : isAudio(name)
        ? "audio"
        : null

const join = (dir: string, name: string) => `${dir.replace(/\/$/, "")}/${name}`

const countMedia = (entries: FileEntry[]) =>
  entries.reduce((n, e) => (!e.dir && kindOf(e.name) ? n + 1 : n), 0)

// Past this an image tile shows its size instead of a preview: /api/file
// serves media whole, so a folder of camera RAWs or huge PNGs would otherwise
// pull every byte just to draw a thumbnail.
const IMAGE_PREVIEW_MAX = 8 << 20

// Subfolders listed to count their media, at most. Only when the subfolder
// list is unfolded (or this folder has no media of its own): each one is a
// directory read, an SFTP round-trip on a remote host.
const SUBFOLDER_SCAN_MAX = 60

const COLS_KEY = "lasso-gallery-cols"
function readCols(): 2 | 3 {
  try {
    return localStorage.getItem(COLS_KEY) === "2" ? 2 : 3
  } catch {
    return 3
  }
}

// A few tiles load at a time, across the whole gallery. Every video and audio
// tile reads its file's metadata (Range requests), and a remote host serves
// them over one ssh connection, so a screenful starting at once would queue
// behind each other anyway and hold up the viewer's own fetch.
const MAX_LOADS = 6
let running = 0
const waiting: Array<() => void> = []
function pump() {
  while (running < MAX_LOADS && waiting.length > 0) waiting.shift()?.()
}
function acquire(onGrant: () => void): () => void {
  let state: "waiting" | "running" | "done" = "waiting"
  const grant = () => {
    if (state !== "waiting") return
    state = "running"
    running++
    onGrant()
  }
  waiting.push(grant)
  pump()
  return () => {
    if (state === "running") {
      running--
      state = "done"
      pump()
    } else if (state === "waiting") {
      state = "done"
      const i = waiting.indexOf(grant)
      if (i >= 0) waiting.splice(i, 1)
    }
  }
}

// Granted once the tile is near the viewport and a load slot is free. `done`
// hands the slot back (the element keeps its src); unmounting does too. A
// file that never reports back (a codec the browser won't touch) gives the
// slot up after a while rather than starving the rest.
function useLoadSlot(want: boolean): [boolean, () => void] {
  const [granted, setGranted] = React.useState(false)
  const release = React.useRef<(() => void) | null>(null)
  React.useEffect(() => {
    if (!want) return
    const r = acquire(() => setGranted(true))
    release.current = r
    const t = setTimeout(r, 15_000)
    return () => {
      clearTimeout(t)
      r()
      release.current = null
    }
  }, [want])
  const done = React.useCallback(() => release.current?.(), [])
  return [granted, done]
}

// True from the first time the element comes within a screen of the viewport.
function useNearView<T extends Element>(): [
  React.RefObject<T | null>,
  boolean,
] {
  const ref = React.useRef<T>(null)
  const [near, setNear] = React.useState(false)
  React.useEffect(() => {
    const el = ref.current
    if (!el || near) return
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((e) => e.isIntersecting)) setNear(true)
      },
      { rootMargin: "300px" }
    )
    io.observe(el)
    return () => io.disconnect()
  }, [near])
  return [ref, near]
}

// Keep the start and the extension: "screenshot-2026-10-31…12.png".
function midTrunc(s: string, max = 22) {
  if (s.length <= max) return s
  const tail = Math.min(8, Math.floor(max / 2))
  return `${s.slice(0, max - tail - 1)}…${s.slice(-tail)}`
}

function fmtDuration(sec: number) {
  if (!Number.isFinite(sec) || sec <= 0) return ""
  const s = Math.round(sec)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = String(s % 60).padStart(2, "0")
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${r}` : `${m}:${r}`
}

// The Gallery subtab: the images, video and audio in one folder as a grid.
// The folder is chosen by the parent (the Files tree's root, or one picked
// with "View in gallery"); every read goes to `host`, the machine it is on.
// Clicking a tile hands the whole visible list to the parent, which opens the
// file viewer with ‹ › stepping through it.
export function GalleryTab({
  dir,
  host,
  visible,
  onNavigate,
  onOpen,
}: {
  dir: string | null
  host: string | null
  // Whether this subtab is on screen: the listing is polled only then.
  visible: boolean
  onNavigate: (path: string) => void
  onOpen: (path: string, items: string[]) => void
}) {
  const [filter, setFilter] = React.useState<Filter>("all")
  const [cols, setCols] = React.useState<2 | 3>(readCols)
  const [foldersOpen, setFoldersOpen] = React.useState(false)

  // A new folder starts folded again; the filter carries over.
  // biome-ignore lint/correctness/useExhaustiveDependencies: dir/host are the trigger
  React.useEffect(() => setFoldersOpen(false), [dir, host])

  const listing = useQuery({
    queryKey: ["gallery-dir", host ?? "", dir],
    queryFn: () => api.files(dir as string, host ?? undefined),
    enabled: dir != null,
    staleTime: 0,
    refetchInterval: visible ? 5000 : false,
  })
  const data = listing.data
  const root = data?.path ?? dir

  const media = React.useMemo(() => {
    if (!data) return []
    return data.entries.flatMap((e) => {
      const kind = e.dir ? null : kindOf(e.name)
      return kind ? [{ entry: e, kind, full: join(data.path, e.name) }] : []
    })
  }, [data])
  const folders = React.useMemo(
    () => (data ? data.entries.filter((e) => e.dir) : []),
    [data]
  )
  const shown =
    filter === "all" ? media : media.filter((m) => m.kind === filter)
  const counts = React.useMemo(() => {
    const c = { image: 0, video: 0, audio: 0 }
    for (const m of media) c[m.kind]++
    return c
  }, [media])

  // Count each subfolder's media, only once someone could see the counts.
  const scan = data != null && (foldersOpen || media.length === 0)
  const scanned = folders
    .filter((f) => !f.name.startsWith("."))
    .slice(0, SUBFOLDER_SCAN_MAX)
  const subCounts = useQueries({
    queries: scanned.map((f) => {
      const full = join(data?.path ?? "", f.name)
      return {
        queryKey: ["gallery-dir", host ?? "", full],
        queryFn: () => api.files(full, host ?? undefined),
        enabled: scan,
        staleTime: 30_000,
        select: (d: { entries: FileEntry[] }) => countMedia(d.entries),
      }
    }),
  })
  const countFor = new Map<string, number | undefined>()
  for (const [i, f] of scanned.entries())
    countFor.set(f.name, subCounts[i]?.data)
  const withMedia = scanned.filter((f) => (countFor.get(f.name) ?? 0) > 0)

  const pickCols = (n: 2 | 3) => {
    setCols(n)
    try {
      localStorage.setItem(COLS_KEY, String(n))
    } catch {
      /* private window: the choice just isn't remembered */
    }
  }

  const items = shown.map((m) => m.full)

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <header className="flex flex-none flex-col gap-1.5 border-border border-b bg-background px-3 py-2">
        <div className="flex items-center gap-1">
          <span
            className="min-w-0 flex-1 truncate text-[13px] text-foreground"
            style={{ direction: "rtl", textAlign: "left" }}
            title={root ?? undefined}
          >
            {/* rtl keeps the tail of the path, the useful part, in view; the
                bdi stops it reordering the path's own characters. */}
            <bdi>{root ? tilde(root) : "waiting for herdr…"}</bdi>
          </span>
          <IconButton
            label="Up a folder"
            disabled={!data?.parent || data.parent === data.path}
            onClick={() => data?.parent && onNavigate(data.parent)}
          >
            <ArrowUp className="size-3.5" />
          </IconButton>
          <IconButton
            label="Refresh"
            disabled={dir == null}
            onClick={() => void listing.refetch()}
          >
            <RefreshCw
              className={cn("size-3.5", listing.isFetching && "animate-spin")}
            />
          </IconButton>
          <IconButton
            label={cols === 3 ? "Bigger tiles" : "Smaller tiles"}
            onClick={() => pickCols(cols === 3 ? 2 : 3)}
          >
            {cols === 3 ? (
              <Grid2x2 className="size-3.5" />
            ) : (
              <Grid3x3 className="size-3.5" />
            )}
          </IconButton>
        </div>
        <div className="flex items-center gap-1">
          <Chip active={filter === "all"} onClick={() => setFilter("all")}>
            All
          </Chip>
          <Chip
            active={filter === "image"}
            disabled={counts.image === 0}
            onClick={() => setFilter("image")}
          >
            Images
          </Chip>
          <Chip
            active={filter === "video"}
            disabled={counts.video === 0}
            onClick={() => setFilter("video")}
          >
            Video
          </Chip>
          <Chip
            active={filter === "audio"}
            disabled={counts.audio === 0}
            onClick={() => setFilter("audio")}
          >
            Audio
          </Chip>
          <span className="ml-auto text-[11px] text-muted-foreground tabular-nums">
            {data ? `${shown.length} item${shown.length === 1 ? "" : "s"}` : ""}
          </span>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto p-2">
        {dir == null ? (
          <Empty>waiting for herdr…</Empty>
        ) : listing.error && !data ? (
          <Empty>{(listing.error as Error).message}</Empty>
        ) : !data ? (
          <Empty>
            <Orb state="working" px={16} />
            loading…
          </Empty>
        ) : (
          <>
            {shown.length === 0 ? (
              <div className="flex flex-col items-center gap-1 px-4 py-8 text-center text-muted-foreground text-xs">
                <div>
                  {media.length === 0
                    ? "No images, video or audio in"
                    : `No ${filter === "image" ? "images" : filter} in`}
                </div>
                <div className="break-all font-mono">{tilde(data.path)}</div>
                {media.length === 0 && withMedia.length > 0 && (
                  <button
                    type="button"
                    className="mt-2 text-foreground underline-offset-2 hover:underline"
                    onClick={() => setFoldersOpen(true)}
                  >
                    {withMedia.length} subfolder
                    {withMedia.length === 1 ? " has" : "s have"} media ▸
                  </button>
                )}
              </div>
            ) : (
              <div
                className={cn(
                  "grid gap-2",
                  cols === 3 ? "grid-cols-3" : "grid-cols-2"
                )}
              >
                {shown.map((m, i) => (
                  <Tile
                    key={m.full}
                    url={api.fileURL(m.full, host ?? undefined)}
                    name={m.entry.name}
                    size={m.entry.size}
                    kind={m.kind}
                    onOpen={() => onOpen(items[i], items)}
                  />
                ))}
              </div>
            )}

            {folders.length > 0 && (
              <div className="mt-3">
                <button
                  type="button"
                  className="flex items-center gap-1 px-1 py-0.5 text-[12px] text-muted-foreground hover:text-foreground"
                  aria-expanded={foldersOpen}
                  onClick={() => setFoldersOpen((o) => !o)}
                >
                  <span className="w-3">{foldersOpen ? "▾" : "▸"}</span>
                  subfolders ({folders.length})
                </button>
                {foldersOpen && (
                  <div className="mt-1 flex flex-wrap gap-1.5 px-1">
                    {folders.map((f) => {
                      const n = countFor.get(f.name)
                      const empty = countFor.has(f.name) && n === 0
                      return (
                        <button
                          key={f.name}
                          type="button"
                          title={join(data.path, f.name)}
                          className={cn(
                            "flex max-w-full items-center gap-1 rounded-md border border-border px-2 py-0.5 text-[12px] hover:bg-accent",
                            empty && "text-muted-foreground opacity-60"
                          )}
                          onClick={() => onNavigate(join(data.path, f.name))}
                        >
                          <Folder className="size-3 shrink-0" />
                          <span className="truncate">{f.name}</span>
                          {n != null && n > 0 && (
                            <span className="text-muted-foreground tabular-nums">
                              {n}
                            </span>
                          )}
                        </button>
                      )
                    })}
                  </div>
                )}
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

function Tile({
  url,
  name,
  size,
  kind,
  onOpen,
}: {
  url: string
  name: string
  size?: number
  kind: Kind
  onOpen: () => void
}) {
  const [ref, near] = useNearView<HTMLButtonElement>()
  const tooBig = kind === "image" && (size ?? 0) > IMAGE_PREVIEW_MAX
  const [granted, done] = useLoadSlot(near && !tooBig)
  const [failed, setFailed] = React.useState(false)
  const [loaded, setLoaded] = React.useState(false)
  const [duration, setDuration] = React.useState<number | null>(null)
  const ok = () => {
    setLoaded(true)
    done()
  }
  const fail = () => {
    setFailed(true)
    done()
  }

  let body: React.ReactNode
  if (tooBig) {
    body = (
      <Placeholder icon={<ImageIcon className="size-5" />}>
        {fmtSize(size)}
      </Placeholder>
    )
  } else if (failed) {
    body = (
      <Placeholder icon={<ImageOff className="size-5" />}>
        no preview
      </Placeholder>
    )
  } else if (kind === "image") {
    body = granted && (
      <img
        src={url}
        alt={name}
        decoding="async"
        draggable={false}
        className="size-full object-cover"
        onLoad={ok}
        onError={fail}
      />
    )
  } else if (kind === "video") {
    // #t nudges past a black first frame; metadata only, so the browser reads
    // the container header and the frame it shows, not the whole file.
    body = granted && (
      <video
        src={`${url}#t=0.1`}
        preload="metadata"
        muted
        playsInline
        tabIndex={-1}
        className="pointer-events-none size-full object-cover"
        onLoadedMetadata={(e) => {
          setDuration(e.currentTarget.duration)
          ok()
        }}
        onError={fail}
      >
        <track kind="captions" />
      </video>
    )
  } else {
    body = (
      <>
        <Placeholder icon={<Music className="size-6" />} />
        {granted && (
          <audio
            src={url}
            preload="metadata"
            className="hidden"
            onLoadedMetadata={(e) => {
              setDuration(e.currentTarget.duration)
              ok()
            }}
            onError={done}
          >
            <track kind="captions" />
          </audio>
        )}
      </>
    )
  }

  const dur = duration != null ? fmtDuration(duration) : ""
  return (
    <button
      ref={ref}
      type="button"
      title={name}
      className="group flex min-w-0 flex-col gap-1 text-left"
      onClick={onOpen}
    >
      <div
        className={cn(
          "relative aspect-square w-full overflow-hidden rounded-md border border-border bg-muted transition-colors group-hover:border-foreground/40",
          kind !== "audio" && !loaded && !failed && !tooBig && "animate-pulse"
        )}
      >
        {body}
        {kind === "video" && !failed && (
          <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
            <span className="rounded-full bg-black/55 p-1.5 text-white">
              {loaded ? (
                <Play className="size-3.5 fill-current" />
              ) : (
                <Film className="size-3.5" />
              )}
            </span>
          </span>
        )}
        {dur && (
          <span className="pointer-events-none absolute right-1 bottom-1 rounded bg-black/60 px-1 text-[10px] text-white tabular-nums">
            {dur}
          </span>
        )}
      </div>
      <span className="truncate px-0.5 text-[11px] text-muted-foreground group-hover:text-foreground">
        {midTrunc(name)}
      </span>
    </button>
  )
}

function Placeholder({
  icon,
  children,
}: {
  icon: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <div className="flex size-full flex-col items-center justify-center gap-1 text-[10px] text-muted-foreground">
      {icon}
      {children}
    </div>
  )
}

function Empty({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-center gap-2 px-4 py-8 text-muted-foreground text-xs">
      {children}
    </div>
  )
}

function IconButton({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string
  disabled?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      disabled={disabled}
      className="flex items-center rounded p-1 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
      onClick={onClick}
    >
      {children}
    </button>
  )
}

function Chip({
  active,
  disabled,
  onClick,
  children,
}: {
  active: boolean
  disabled?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type="button"
      disabled={disabled && !active}
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "rounded-full border px-2 py-px text-[11px] transition-colors disabled:opacity-40",
        active
          ? "border-foreground/30 bg-accent text-foreground"
          : "border-border text-muted-foreground hover:text-foreground"
      )}
    >
      {children}
    </button>
  )
}

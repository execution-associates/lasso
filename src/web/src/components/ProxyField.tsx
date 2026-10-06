import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowDown,
  ArrowUp,
  Check,
  Loader2,
  Plus,
  RefreshCw,
  X,
} from "lucide-react"
import * as React from "react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { api, type ProxyCandidate } from "@/lib/api"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// ProxyField edits a browser profile's proxy setting as an ordered list of
// rows plus a "connect directly" fallback checkbox, instead of the raw
// comma-separated --proxy-server string the server stores. The string stays
// the value in and out (`socks5://a,socks5://b,direct://`), so the server,
// the MCP tools and existing profiles are untouched. The Add menu offers the
// SOCKS5 proxies lasso's machine found on loopback and online tailnet peers.

interface ParsedProxy {
  entries: string[]
  fallback: boolean
}

export function parseProxy(value: string): ParsedProxy {
  const parts = value
    .split(",")
    .map((p) => p.trim())
    .filter(Boolean)
  const isDirect = (p: string) => /^direct(:\/\/)?$/i.test(p)
  return {
    entries: parts.filter((p) => !isDirect(p)),
    fallback: parts.length > 0 && isDirect(parts[parts.length - 1] ?? ""),
  }
}

// The same canonical form the server writes (comma-joined, lowercase
// direct://), so an untouched list reads as unchanged, not dirty.
export function serializeProxy({ entries, fallback }: ParsedProxy): string {
  if (entries.length === 0) return ""
  return [...entries, ...(fallback ? ["direct://"] : [])].join(",")
}

// scheme://host[:port], nothing else: a comma would split the list, and the
// server refuses paths and credentials anyway. Its sentence covers the rest.
const customProxyRE = /^[a-z][a-z0-9+.-]*:\/\/[^\s,/@]+\/?$/i

function shortURL(u: string): string {
  return u.replace(/^socks5:\/\//i, "")
}

export function ProxyField({
  value,
  onValueChange,
}: {
  value: string
  onValueChange: (value: string) => void
}) {
  const queryClient = useQueryClient()
  const fallbackID = React.useId()
  const { entries, fallback } = parseProxy(value)
  // Remember the fallback choice while the list is empty (its checkbox is
  // hidden then), so removing the last proxy and adding one back keeps it.
  const [wantFallback, setWantFallback] = React.useState(fallback)
  React.useEffect(() => {
    if (entries.length > 0) setWantFallback(fallback)
  }, [fallback, entries.length])

  const commit = (
    next: string[],
    fb = entries.length ? fallback : wantFallback
  ) => onValueChange(serializeProxy({ entries: next, fallback: fb }))

  // The scan starts when the form appears (the dialog mounts this only while
  // open), so the Add menu is usually filled by the time it is opened.
  const scan = useQuery({
    queryKey: qk.browserProxies,
    queryFn: () => api.browserProxies(),
    staleTime: 30_000,
  })
  const found: ProxyCandidate[] = scan.data?.proxies ?? []
  const port = scan.data?.port ?? 1080
  const nameOf = (u: string) => found.find((c) => c.url === u)?.name
  const [rescanning, setRescanning] = React.useState(false)
  const rescan = async () => {
    setRescanning(true)
    try {
      queryClient.setQueryData(
        qk.browserProxies,
        await api.browserProxies(true)
      )
    } catch {
      // Keep the last list; a failed rescan is not worth an error banner.
    } finally {
      setRescanning(false)
    }
  }
  const busy = scan.isFetching || rescanning

  const [custom, setCustom] = React.useState<string | null>(null)
  const [customErr, setCustomErr] = React.useState("")
  const addCustom = () => {
    const v = (custom ?? "").trim().replace(/\/$/, "")
    if (!customProxyRE.test(v)) {
      setCustomErr("Use scheme://host:port, e.g. socks5://10.0.0.5:1080")
      return
    }
    if (!entries.includes(v)) commit([...entries, v])
    setCustom(null)
    setCustomErr("")
  }

  const move = (i: number, d: -1 | 1) => {
    const next = [...entries]
    const [it] = next.splice(i, 1)
    if (it === undefined) return
    next.splice(i + d, 0, it)
    commit(next)
  }

  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-col rounded-lg border border-border">
        {entries.length === 0 ? (
          <div className="px-2.5 py-1.5 text-[13px] text-muted-foreground">
            Direct, no proxy
          </div>
        ) : (
          entries.map((u, i) => {
            const name = nameOf(u)
            return (
              <div
                key={u}
                className={cn(
                  "flex items-center gap-2 py-1 pr-1 pl-2.5 text-[13px]",
                  i > 0 && "border-border border-t"
                )}
              >
                <span className="w-3 flex-shrink-0 text-muted-foreground text-xs tabular-nums">
                  {i + 1}
                </span>
                {name && <span className="flex-shrink-0">{name}</span>}
                <span
                  className="min-w-0 flex-1 truncate font-mono text-[12px] text-muted-foreground"
                  title={u}
                >
                  {shortURL(u)}
                </span>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label="Move up"
                  disabled={i === 0}
                  onClick={() => move(i, -1)}
                >
                  <ArrowUp className="size-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label="Move down"
                  disabled={i === entries.length - 1}
                  onClick={() => move(i, 1)}
                >
                  <ArrowDown className="size-3.5" />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  size="icon"
                  className="size-6"
                  aria-label={`Remove ${u}`}
                  onClick={() => commit(entries.filter((e) => e !== u))}
                >
                  <X className="size-3.5" />
                </Button>
              </div>
            )
          })
        )}
      </div>

      {custom === null ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="w-fit gap-1"
            >
              <Plus className="size-3.5" />
              Add proxy
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="w-80">
            <DropdownMenuLabel className="flex items-center text-[11px] text-muted-foreground">
              Found on :{port}
              <button
                type="button"
                className="ml-auto inline-flex items-center gap-1 rounded px-1 font-normal hover:text-foreground disabled:opacity-60"
                disabled={busy}
                onClick={() => void rescan()}
              >
                <RefreshCw className={cn("size-3", busy && "animate-spin")} />
                rescan
              </button>
            </DropdownMenuLabel>
            {scan.isPending ? (
              <div className="flex items-center gap-1.5 px-1.5 py-1 text-[12px] text-muted-foreground">
                <Loader2 className="size-3.5 animate-spin" />
                Checking this machine and the tailnet…
              </div>
            ) : scan.isError ? (
              <div className="px-1.5 py-1 text-[12px] text-destructive">
                Scan failed: {scan.error.message}
              </div>
            ) : found.length === 0 ? (
              <div className="px-1.5 py-1 text-[12px] text-muted-foreground">
                None found ({scan.data?.scanned ?? 0} hosts checked)
              </div>
            ) : (
              found.map((c) => {
                const added = entries.includes(c.url)
                return (
                  <DropdownMenuItem
                    key={c.url}
                    disabled={added}
                    onSelect={() => commit([...entries, c.url])}
                    className="text-[13px]"
                  >
                    <span className="truncate">{c.name}</span>
                    <span className="ml-auto truncate font-mono text-[12px] text-muted-foreground">
                      {c.addr}
                    </span>
                    {added && (
                      <span className="flex flex-shrink-0 items-center gap-0.5 text-[11px] text-muted-foreground">
                        <Check className="size-3" />
                        added
                      </span>
                    )}
                  </DropdownMenuItem>
                )
              })
            )}
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="text-[13px]"
              onSelect={() => {
                setCustom("")
                setCustomErr("")
              }}
            >
              Custom…
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-1.5">
            <Input
              autoFocus
              value={custom}
              placeholder="socks5://host:port"
              className="h-8 flex-1 font-mono text-[13px]"
              onChange={(e) => setCustom(e.target.value)}
              onKeyDown={(e) => {
                // Enter adds the row instead of submitting the whole form.
                if (e.key === "Enter") {
                  e.preventDefault()
                  addCustom()
                } else if (e.key === "Escape") {
                  e.preventDefault()
                  e.stopPropagation()
                  setCustom(null)
                }
              }}
            />
            <Button type="button" size="sm" onClick={addCustom}>
              Add
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="size-8"
              aria-label="Cancel"
              onClick={() => setCustom(null)}
            >
              <X className="size-3.5" />
            </Button>
          </div>
          {customErr && (
            <p className="text-[12px] text-destructive">{customErr}</p>
          )}
        </div>
      )}

      {entries.length > 0 && (
        <label
          htmlFor={fallbackID}
          className="flex items-center gap-2 text-[13px]"
        >
          <Checkbox
            id={fallbackID}
            checked={fallback}
            onCheckedChange={(c) => commit(entries, c === true)}
          />
          If no proxy answers, connect directly
        </label>
      )}
    </div>
  )
}

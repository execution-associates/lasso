import { useQueries } from "@tanstack/react-query"
import {
  ChevronDown,
  Plus,
  Smartphone,
  Trash2,
  TriangleAlert,
  UserRound,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { BotAvatar } from "@/components/BotParts"
import { ConnectPhoneDialog } from "@/components/ConnectPhone"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { labelClass } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Orb } from "@/components/ui/orb"
import { type AppUser, api, type BotDetail, type BotView } from "@/lib/api"
import { botKey, relativeTime, useBots, useMinuteClock } from "@/lib/bots"
import {
  activeDevices,
  botClientNote,
  invalidatePeople,
  lastSeen,
  personNameProblem,
  platformLabel,
  usePeople,
} from "@/lib/people"
import { cn } from "@/lib/utils"

// People: who has the phone app and which bots each of them reaches
// (clientapi_admin.go, docs/design/client-api.md). One list, two doors: the
// Bots view's People page (everyone) and a bot's settings tab (the people with
// that bot, where "Add a person" starts with the bot picked). Every action is
// an /api/app-users route that /mcp has a tool for.

// What the owner means by "owner", said once and reused wherever it is chosen.
const OWNER_HELP =
  "Owner means the bot treats this person as its human: it may follow their requests the way it follows yours. Leave it off and they are a guest who chats with the bot."

type Picks = Record<string, { owner: boolean }>

// The phone app's reach to each bot, for the note beside a bot's name. The
// detail is the Bot settings' own query, so the two screens share the answer.
function useClientNotes(names: string[]) {
  return useQueries({
    queries: names.map((name) => ({
      queryKey: botKey(name),
      queryFn: (): Promise<BotDetail> => api.bots.get(name),
      refetchInterval: 5000,
      staleTime: 2000,
    })),
    // The result is shared structurally, so an unchanged answer is the same
    // object and does not re-render the lists.
    combine: (results) => {
      const out: Record<string, string> = {}
      names.forEach((n, i) => {
        const note = botClientNote(results[i]?.data?.client)
        if (note) out[n] = note
      })
      return out
    },
  })
}

export function PeopleView({
  bots,
  lead,
  onOpenBot,
}: {
  bots: BotView[]
  lead?: React.ReactNode
  onOpenBot?: (name: string) => void
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-none items-center gap-2 border-border border-b px-2.5 py-1.5">
        {lead}
        <span className="font-medium text-[12.5px] text-foreground">
          People
        </span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className="mx-auto w-full max-w-3xl px-4 py-4">
          <People bots={bots} onOpenBot={onOpenBot} />
        </div>
      </div>
    </div>
  )
}

// A bot's settings tab.
export function PeopleTab({ bot }: { bot: BotView }) {
  const bots = useBots(false).data?.bots ?? []
  return <People bots={bots} only={bot.name} />
}

function People({
  bots,
  only,
  onOpenBot,
}: {
  bots: BotView[]
  // Show just the people with this bot.
  only?: string
  onOpenBot?: (name: string) => void
}) {
  const q = usePeople(true)
  const now = useMinuteClock()
  const [adding, setAdding] = React.useState(false)
  const [connecting, setConnecting] = React.useState<AppUser | null>(null)
  const names = React.useMemo(() => bots.map((b) => b.name), [bots])
  const notes = useClientNotes(names)

  const all = q.data?.users ?? []
  const users = only
    ? all.filter((u) => u.grants.some((g) => g.bot === only))
    : all

  // The dialog holds a snapshot; keep it pointed at the live row so a rename
  // or a disable shows. It closes if the person is deleted.
  const connectingUser = connecting
    ? (all.find((u) => u.id === connecting.id) ?? null)
    : null

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-start gap-3">
        <p className="min-w-0 flex-1 basis-60 text-[12.5px] text-muted-foreground leading-relaxed">
          {only
            ? `People who can talk to ${only} from the Lasso app on their phone. Connect a phone by showing them a QR code.`
            : "People who use the Lasso app on their phone. Each reaches only the bots you give them. Connect a phone by showing them a QR code."}
        </p>
        <Button onClick={() => setAdding(true)} disabled={bots.length === 0}>
          <Plus />
          {only ? "Add a person to this bot" : "Add a person"}
        </Button>
      </div>

      {q.isPending ? (
        <Orb state="working" px={18} />
      ) : q.error ? (
        <div className="flex flex-col items-start gap-2 text-[12.5px]">
          <p className="text-destructive">
            Could not load people: {(q.error as Error).message}
          </p>
          <Button size="sm" variant="outline" onClick={() => void q.refetch()}>
            Try again
          </Button>
        </div>
      ) : users.length === 0 ? (
        <div className="flex flex-col items-center gap-2 rounded-xl border border-border border-dashed px-4 py-8 text-center">
          <UserRound className="size-6 text-muted-foreground" />
          <p className="font-medium text-[13px] text-foreground">
            {only ? `Nobody has ${only} on their phone yet` : "No one yet"}
          </p>
          <p className="max-w-sm text-[12.5px] text-muted-foreground">
            {bots.length === 0
              ? "Create a bot first, then give someone access to it."
              : "Add a person, pick the bots they can reach, then show them a QR code to connect their phone."}
          </p>
        </div>
      ) : (
        <ul className="flex flex-col gap-2.5">
          {users.map((u) => (
            <PersonCard
              key={u.id}
              user={u}
              bots={bots}
              notes={notes}
              now={now}
              highlight={only}
              onConnect={() => setConnecting(u)}
              onOpenBot={onOpenBot}
            />
          ))}
        </ul>
      )}

      <AddPersonDialog
        open={adding}
        onOpenChange={setAdding}
        bots={bots}
        notes={notes}
        preselect={only}
        onCreated={(u) => setConnecting(u)}
      />
      <ConnectPhoneDialog
        user={connectingUser}
        onOpenChange={(o) => !o && setConnecting(null)}
      />
    </div>
  )
}

function PersonCard({
  user,
  bots,
  notes,
  now,
  highlight,
  onConnect,
  onOpenBot,
}: {
  user: AppUser
  bots: BotView[]
  notes: Record<string, string>
  now: number
  highlight?: string
  onConnect: () => void
  onOpenBot?: (name: string) => void
}) {
  const [open, setOpen] = React.useState(false)
  const devices = activeDevices(user)
  const seen = lastSeen(user)
  const byName = new Map(bots.map((b) => [b.name, b]))
  return (
    <li
      className={cn(
        "rounded-xl border border-border bg-card",
        user.disabled && "opacity-80"
      )}
    >
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2 p-3">
        <span className="flex size-9 shrink-0 items-center justify-center rounded-full bg-muted font-medium text-[14px] text-foreground">
          {([...user.name][0] ?? "?").toUpperCase()}
        </span>
        <div className="min-w-0 flex-1 basis-48">
          <div className="flex flex-wrap items-center gap-2">
            <span className="min-w-0 truncate font-medium text-[14px] text-foreground">
              {user.name}
            </span>
            {user.disabled && <Badge variant="destructive">Disabled</Badge>}
          </div>
          <p className="text-[12px] text-muted-foreground">
            {devices.length === 0
              ? "No phone connected"
              : `${devices.length} ${devices.length === 1 ? "phone" : "phones"}`}
            {devices.length > 0 &&
              ` · ${seen ? `last seen ${relativeTime(seen, now)}${relativeTime(seen, now) === "now" ? "" : " ago"}` : "not seen yet"}`}
          </p>
          <ul className="mt-2 flex flex-wrap gap-1.5">
            {user.grants.length === 0 && (
              <li className="text-[12px] text-muted-foreground">
                No bots yet. Open Manage to give them some.
              </li>
            )}
            {user.grants.map((g) => {
              const b = byName.get(g.bot)
              return (
                <li
                  key={g.bot}
                  className={cn(
                    "flex min-w-0 items-center gap-1.5 rounded-full bg-muted py-0.5 pr-2 pl-0.5 text-[12px]",
                    highlight === g.bot && "ring-1 ring-primary/50"
                  )}
                >
                  {b ? (
                    <BotAvatar bot={b} size={18} />
                  ) : (
                    <span className="size-[18px]" />
                  )}
                  {onOpenBot && b ? (
                    <button
                      type="button"
                      onClick={() => onOpenBot(g.bot)}
                      className="truncate font-medium text-foreground hover:underline"
                    >
                      {g.bot}
                    </button>
                  ) : (
                    <span className="truncate font-medium text-foreground">
                      {g.bot}
                    </span>
                  )}
                  {g.owner && (
                    <Badge
                      variant="outline"
                      className="h-4 px-1.5 text-[10.5px]"
                    >
                      owner
                    </Badge>
                  )}
                  {notes[g.bot] && (
                    <span
                      title={notes[g.bot]}
                      className="flex items-center gap-1 text-amber-500"
                    >
                      <TriangleAlert className="size-3" />
                      <span className="max-w-40 truncate text-[11px]">
                        {notes[g.bot]}
                      </span>
                    </span>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
        <div className="flex shrink-0 items-center gap-1.5 max-sm:w-full">
          <Button
            size="sm"
            onClick={onConnect}
            disabled={user.disabled}
            title={
              user.disabled
                ? "Enable this person to connect a phone"
                : undefined
            }
          >
            <Smartphone />
            Connect a phone
          </Button>
          <Button
            size="sm"
            variant="outline"
            aria-expanded={open}
            onClick={() => setOpen((o) => !o)}
          >
            Manage
            <ChevronDown
              className={cn("transition-transform", open && "rotate-180")}
            />
          </Button>
        </div>
      </div>
      {open && <ManagePerson user={user} bots={bots} notes={notes} now={now} />}
    </li>
  )
}

// What changed between the grants a person has and the picks on screen.
function picksOf(u: AppUser): Picks {
  const p: Picks = {}
  for (const g of u.grants) p[g.bot] = { owner: g.owner }
  return p
}

function picksDiffer(a: Picks, b: Picks): boolean {
  const ka = Object.keys(a)
  if (ka.length !== Object.keys(b).length) return true
  return ka.some((k) => !b[k] || b[k].owner !== a[k].owner)
}

function ManagePerson({
  user,
  bots,
  notes,
  now,
}: {
  user: AppUser
  bots: BotView[]
  notes: Record<string, string>
  now: number
}) {
  const [name, setName] = React.useState(user.name)
  const [picks, setPicks] = React.useState<Picks>(() => picksOf(user))
  const [busy, setBusy] = React.useState(false)
  const [confirm, setConfirm] = React.useState<"delete" | "revoke" | null>(null)
  const [revoking, setRevoking] = React.useState<{
    id: string
    label: string
  } | null>(null)

  const nameProblem = personNameProblem(name)
  const nameDirty = name.trim() !== user.name
  const stored = picksOf(user)
  const grantsDirty = picksDiffer(stored, picks)

  const run = async (fn: () => Promise<void>, done: string) => {
    setBusy(true)
    try {
      await fn()
      toast.success(done)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
      void invalidatePeople()
    }
  }

  const saveGrants = () =>
    run(async () => {
      for (const [bot, p] of Object.entries(picks)) {
        if (stored[bot]?.owner !== p.owner || !stored[bot])
          await api.appUsers.setGrant(user.id, bot, p.owner)
      }
      for (const bot of Object.keys(stored)) {
        if (!picks[bot]) await api.appUsers.revokeGrant(user.id, bot)
      }
    }, `${user.name}'s access saved`)

  // A grant to a bot that no longer exists cannot be edited from the picker
  // (it lists bots that exist), but it stays in the picks so saving does not
  // silently drop it; deleting the bot removes it server-side anyway.
  const live = user.devices
  return (
    <div className="flex flex-col gap-5 border-border border-t p-3">
      <section className="flex flex-col gap-1.5">
        <label className={labelClass} htmlFor={`name-${user.id}`}>
          Name
        </label>
        <div className="flex items-center gap-2">
          <Input
            id={`name-${user.id}`}
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={!!nameProblem}
            className="max-w-xs"
          />
          <Button
            size="sm"
            variant="outline"
            disabled={busy || !nameDirty || !!nameProblem}
            onClick={() =>
              void run(async () => {
                await api.appUsers.update(user.id, { name: name.trim() })
              }, "Name saved")
            }
          >
            Rename
          </Button>
        </div>
        {nameDirty && nameProblem && (
          <p className="text-[11px] text-destructive">{nameProblem}</p>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <span className={labelClass}>Bots they can reach</span>
        <GrantPicker
          bots={bots}
          picks={picks}
          onChange={setPicks}
          notes={notes}
        />
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={busy || !grantsDirty}
            onClick={() => void saveGrants()}
          >
            Save access
          </Button>
          {grantsDirty && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setPicks(picksOf(user))}
            >
              Undo
            </Button>
          )}
        </div>
      </section>

      <section className="flex flex-col gap-2">
        <span className={labelClass}>Phones</span>
        {live.length === 0 ? (
          <p className="text-[12.5px] text-muted-foreground">
            None connected. Use Connect a phone to pair one.
          </p>
        ) : (
          <ul className="flex flex-col divide-y divide-border/60 rounded-lg border border-border">
            {live.map((d) => (
              <li
                key={d.id}
                className={cn(
                  "flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2",
                  d.revoked_at && "opacity-60"
                )}
              >
                <Smartphone className="size-4 shrink-0 text-muted-foreground" />
                <div className="min-w-0 flex-1 basis-44">
                  <div className="flex items-center gap-2">
                    <span className="truncate font-medium text-[13px] text-foreground">
                      {d.label || "Phone"}
                    </span>
                    <span className="text-[11.5px] text-muted-foreground">
                      {platformLabel(d.platform)}
                    </span>
                    {d.revoked_at && <Badge variant="secondary">revoked</Badge>}
                  </div>
                  <p className="text-[11.5px] text-muted-foreground">
                    Connected {when(d.created_at, now)}
                    {d.paired_by ? ` by ${d.paired_by}` : ""} ·{" "}
                    {d.last_seen_at
                      ? `last seen ${when(d.last_seen_at, now)}`
                      : "not seen yet"}
                  </p>
                </div>
                {!d.revoked_at && (
                  <Button
                    size="xs"
                    variant="outline"
                    onClick={() => {
                      setRevoking({ id: d.id, label: d.label || "this phone" })
                      setConfirm("revoke")
                    }}
                  >
                    Revoke
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="flex flex-wrap items-center gap-2 border-border border-t pt-3">
        <Button
          size="sm"
          variant="outline"
          disabled={busy}
          onClick={() =>
            void run(
              async () => {
                await api.appUsers.update(user.id, { disabled: !user.disabled })
              },
              user.disabled ? `${user.name} enabled` : `${user.name} disabled`
            )
          }
        >
          {user.disabled ? "Enable" : "Disable"}
        </Button>
        <span className="min-w-0 flex-1 text-[11.5px] text-muted-foreground">
          {user.disabled
            ? "Their phones are shut out until you enable them."
            : "Disabling shuts their phones out at once; nothing is deleted."}
        </span>
        <Button
          size="sm"
          variant="ghost"
          className="text-muted-foreground hover:text-destructive"
          onClick={() => setConfirm("delete")}
        >
          <Trash2 />
          Delete…
        </Button>
      </section>

      <AlertDialog
        open={confirm === "delete"}
        onOpenChange={(o) => !o && setConfirm(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {user.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Their phones stop working at once, and lasso forgets their
              conversations with the bots, the messages and the files they sent.
              The bots keep their own memory. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep them</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() =>
                void run(async () => {
                  await api.appUsers.remove(user.id)
                }, `${user.name} deleted`)
              }
            >
              Delete {user.name}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={confirm === "revoke"}
        onOpenChange={(o) => !o && setConfirm(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Revoke {revoking?.label}?</AlertDialogTitle>
            <AlertDialogDescription>
              That phone is signed out at once and cannot come back without a
              new code. {user.name}'s other phones are not affected.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep it</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                const d = revoking
                if (d)
                  void run(async () => {
                    await api.appUsers.revokeDevice(user.id, d.id)
                  }, `${d.label} revoked`)
              }}
            >
              Revoke
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function when(iso: string, now: number): string {
  const r = relativeTime(iso, now)
  if (!r) return "never"
  if (r === "now") return "just now"
  return /^\d+[mhd]$/.test(r) ? `${r} ago` : `on ${r}`
}

// One row per bot: reach, and (when reached) owner.
export function GrantPicker({
  bots,
  picks,
  onChange,
  notes,
  idPrefix = "grant",
}: {
  bots: BotView[]
  picks: Picks
  onChange: (p: Picks) => void
  notes: Record<string, string>
  idPrefix?: string
}) {
  if (bots.length === 0)
    return (
      <p className="text-[12.5px] text-muted-foreground">
        There are no bots to give access to yet.
      </p>
    )
  const toggle = (bot: string, on: boolean) => {
    const next = { ...picks }
    if (on) next[bot] = next[bot] ?? { owner: false }
    else delete next[bot]
    onChange(next)
  }
  return (
    <div className="flex flex-col gap-2">
      <ul className="flex flex-col divide-y divide-border/60 rounded-lg border border-border">
        {bots.map((b) => {
          const p = picks[b.name]
          const id = `${idPrefix}-${b.name}`
          return (
            <li
              key={b.name}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2"
            >
              <label
                htmlFor={id}
                className="flex min-w-0 flex-1 basis-40 items-center gap-2.5"
              >
                <Checkbox
                  id={id}
                  checked={!!p}
                  onCheckedChange={(v) => toggle(b.name, v === true)}
                />
                <BotAvatar bot={b} size={22} />
                <span className="min-w-0">
                  <span className="block truncate font-medium text-[13px] text-foreground">
                    {b.name}
                  </span>
                  {notes[b.name] && (
                    <span className="flex items-center gap-1 text-[11px] text-amber-500">
                      <TriangleAlert className="size-3 shrink-0" />
                      <span className="min-w-0">{notes[b.name]}</span>
                    </span>
                  )}
                </span>
              </label>
              <label
                htmlFor={`${id}-owner`}
                className={cn(
                  "flex items-center gap-2 text-[12px]",
                  p ? "text-foreground" : "text-muted-foreground/60"
                )}
              >
                <Checkbox
                  id={`${id}-owner`}
                  checked={!!p?.owner}
                  disabled={!p}
                  onCheckedChange={(v) =>
                    onChange({ ...picks, [b.name]: { owner: v === true } })
                  }
                />
                Owner
              </label>
            </li>
          )
        })}
      </ul>
      <p className="text-[11.5px] text-muted-foreground leading-relaxed">
        {OWNER_HELP}
      </p>
    </div>
  )
}

function AddPersonDialog({
  open,
  onOpenChange,
  bots,
  notes,
  preselect,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  bots: BotView[]
  notes: Record<string, string>
  preselect?: string
  onCreated: (u: AppUser) => void
}) {
  const [name, setName] = React.useState("")
  const [touched, setTouched] = React.useState(false)
  const [picks, setPicks] = React.useState<Picks>({})
  const [busy, setBusy] = React.useState(false)

  // A fresh form every time it opens, with the bot it was opened from picked.
  React.useEffect(() => {
    if (!open) return
    setName("")
    setTouched(false)
    setBusy(false)
    setPicks(preselect ? { [preselect]: { owner: false } } : {})
  }, [open, preselect])

  const problem = personNameProblem(name)
  const ready = !problem && Object.keys(picks).length > 0

  const submit = async () => {
    setBusy(true)
    let created: AppUser | null = null
    try {
      const { user } = await api.appUsers.create({ name: name.trim() })
      created = user
      for (const [bot, p] of Object.entries(picks))
        await api.appUsers.setGrant(user.id, bot, p.owner)
      const full = (await api.appUsers.list()).users.find(
        (u) => u.id === user.id
      )
      onOpenChange(false)
      toast.success(`${user.name} added`)
      onCreated(full ?? user)
    } catch (e) {
      toast.error(
        created
          ? `${created.name} was added but not all of their access was set: ${(e as Error).message}`
          : (e as Error).message
      )
      if (created) onOpenChange(false)
    } finally {
      setBusy(false)
      void invalidatePeople()
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Add a person</DialogTitle>
          <DialogDescription>
            They get the Lasso app on their phone and reach only the bots you
            pick here. Next you show them a QR code to connect.
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (ready && !busy) void submit()
          }}
        >
          <div className="flex flex-col gap-1">
            <label className={labelClass} htmlFor="add-person-name">
              Name
            </label>
            <Input
              id="add-person-name"
              autoFocus
              value={name}
              placeholder="Ann"
              onChange={(e) => setName(e.target.value)}
              onBlur={() => setTouched(true)}
              aria-invalid={touched && !!problem}
            />
            {touched && problem && (
              <p className="text-[11px] text-destructive">{problem}</p>
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <span className={labelClass}>Bots they can reach</span>
            <GrantPicker
              bots={bots}
              picks={picks}
              onChange={setPicks}
              notes={notes}
              idPrefix="add"
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={() => onOpenChange(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={!ready || busy}>
              {busy ? <Orb state="working" px={14} on="accent" /> : <Plus />}
              Add and connect a phone
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

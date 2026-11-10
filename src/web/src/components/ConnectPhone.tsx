import {
  CircleCheck,
  Clock,
  Copy,
  QrCode,
  RefreshCw,
  TriangleAlert,
} from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { copyText } from "@/components/BotJobs"
import { QRCode } from "@/components/QRCode"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { fieldClass, labelClass } from "@/components/ui/field"
import { Orb } from "@/components/ui/orb"
import { type AppDevice, type AppUser, api } from "@/lib/api"
import {
  clock,
  invalidatePeople,
  normalizeBase,
  ownOrigin,
  pairingBaseProblem,
  pairingBaseWarning,
  platformLabel,
} from "@/lib/people"
import { cn } from "@/lib/utils"

// "Connect a phone": mint a one-time pairing for a person and show it as a QR
// code. The code is a credential, so it lives in this component's state and
// nowhere else: not a query cache, not storage, not the console, not a URL the
// page requests, and not a third-party QR service (the code is drawn here, from
// the link). Closing the dialog unmounts the body, which drops the code and
// cancels a pairing nobody used.

type Minted = {
  id: string
  link: string
  // performance.now() at which the code lapses: measured on this clock from
  // the moment it arrived, so a wrong clock on either side cannot skew it.
  deadline: number
}

type Phase = "minting" | "ready" | "expired" | "paired" | "gone" | "error"

const POLL_MS = 2000

export function ConnectPhoneDialog({
  user,
  onOpenChange,
}: {
  user: AppUser | null
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={!!user} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-md">
        {user && (
          <ConnectBody
            key={user.id}
            user={user}
            onClose={() => onOpenChange(false)}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function ConnectBody({
  user,
  onClose,
}: {
  user: AppUser
  onClose: () => void
}) {
  const [phase, setPhase] = React.useState<Phase>("minting")
  const [mint, setMint] = React.useState<Minted | null>(null)
  const [device, setDevice] = React.useState<AppDevice | null>(null)
  const [error, setError] = React.useState("")
  // The address the link points at: applied (used for the live code) and the
  // draft being typed.
  const [base, setBase] = React.useState(() => ownOrigin())
  const [baseDraft, setBaseDraft] = React.useState(base)
  const [now, setNow] = React.useState(() => performance.now())

  // The id of a pairing this dialog made and nobody has used, to cancel it
  // when the dialog goes away or a new code replaces it.
  const live = React.useRef<string | null>(null)
  const gen = React.useRef(0)
  const userID = user.id

  const dropLive = React.useCallback(() => {
    const id = live.current
    live.current = null
    if (id) void api.appUsers.cancelPairing(userID, id).catch(() => {})
  }, [userID])

  const issue = React.useCallback(
    async (baseURL: string) => {
      const g = ++gen.current
      dropLive()
      setMint(null)
      setDevice(null)
      setError("")
      setPhase("minting")
      try {
        const r = await api.appUsers.mintPairing(userID, baseURL)
        if (g !== gen.current) {
          // Closed or replaced while the request was out: do not leave it live.
          void api.appUsers.cancelPairing(userID, r.pairing.id).catch(() => {})
          return
        }
        const ttl =
          Date.parse(r.pairing.expires_at) - Date.parse(r.pairing.created_at)
        live.current = r.pairing.id
        setMint({
          id: r.pairing.id,
          link: r.link,
          deadline: performance.now() + (Number.isFinite(ttl) ? ttl : 600_000),
        })
        setNow(performance.now())
        setPhase("ready")
      } catch (e) {
        if (g !== gen.current) return
        setError((e as Error).message)
        setPhase("error")
      }
    },
    [userID, dropLive]
  )

  React.useEffect(() => {
    void issue(ownOrigin())
    return () => {
      gen.current++
      dropLive()
    }
  }, [issue, dropLive])

  // The countdown, and the moment the code lapses: the code leaves memory.
  const deadline = mint?.deadline
  React.useEffect(() => {
    if (phase !== "ready" || deadline === undefined) return
    const t = setInterval(() => {
      const n = performance.now()
      setNow(n)
      if (n >= deadline) {
        live.current = null
        setMint(null)
        setPhase("expired")
      }
    }, 500)
    return () => clearInterval(t)
  }, [phase, deadline])

  // Has the phone paired? The pairing list says when the code was used and by
  // which device (never the code itself).
  const mintID = mint?.id
  React.useEffect(() => {
    if (phase !== "ready" || !mintID) return
    let stop = false
    const t = setInterval(async () => {
      try {
        const { pairings } = await api.appUsers.pairings(userID)
        if (stop) return
        const p = pairings.find((x) => x.id === mintID)
        if (!p) {
          live.current = null
          setMint(null)
          setPhase("gone")
          return
        }
        if (!p.used_at) return
        live.current = null
        const { devices } = await api.appUsers.devices(userID)
        if (stop) return
        setDevice(devices.find((d) => d.id === p.device_id) ?? null)
        setMint(null)
        setPhase("paired")
        void invalidatePeople()
      } catch {
        // A blip: the next tick asks again.
      }
    }, POLL_MS)
    return () => {
      stop = true
      clearInterval(t)
    }
  }, [phase, mintID, userID])

  const baseProblem = pairingBaseProblem(baseDraft)
  const baseDirty = normalizeBase(baseDraft) !== base
  const warning = pairingBaseWarning(base)
  const applyBase = () => {
    if (baseProblem) return
    const b = normalizeBase(baseDraft)
    setBase(b)
    setBaseDraft(b)
    void issue(b)
  }

  const remaining = mint ? mint.deadline - now : 0

  return (
    <>
      <DialogHeader>
        <DialogTitle>Connect a phone for {user.name}</DialogTitle>
        <DialogDescription>
          Open the Lasso app on their phone and scan this code, or point the
          phone's camera at it. It works once.
        </DialogDescription>
      </DialogHeader>

      {warning && phase !== "paired" && (
        <p
          className={cn(
            "flex items-start gap-2 rounded-lg px-3 py-2 text-[12px] leading-relaxed",
            warning.level === "blocked"
              ? "bg-destructive/10 text-destructive"
              : "bg-muted text-amber-500"
          )}
        >
          <TriangleAlert className="mt-0.5 size-3.5 shrink-0" />
          <span>{warning.text}</span>
        </p>
      )}

      {phase === "minting" && (
        <Stage>
          <Orb state="working" px={22} />
          <span>Making a code…</span>
        </Stage>
      )}

      {phase === "ready" && mint && (
        <div className="flex flex-col items-center gap-2">
          <div className="w-full max-w-[17rem] overflow-hidden rounded-xl ring-1 ring-border">
            <QRCode
              value={mint.link}
              label="QR code of the pairing link"
              className="w-full"
            />
          </div>
          <p
            className="flex items-center gap-1.5 text-[12.5px] text-muted-foreground tabular-nums"
            aria-live="off"
          >
            <Clock className="size-3.5" />
            Expires in{" "}
            <span className="font-medium text-foreground">
              {clock(remaining)}
            </span>
          </p>
        </div>
      )}

      {phase === "expired" && (
        <Stage>
          <Clock className="size-6" />
          <span className="font-medium text-foreground">This code expired</span>
          <Button onClick={() => void issue(base)}>
            <RefreshCw />
            New code
          </Button>
        </Stage>
      )}

      {phase === "gone" && (
        <Stage>
          <Clock className="size-6" />
          <span className="font-medium text-foreground">
            This code is no longer valid
          </span>
          <Button onClick={() => void issue(base)}>
            <RefreshCw />
            New code
          </Button>
        </Stage>
      )}

      {phase === "error" && (
        <Stage>
          <TriangleAlert className="size-6 text-destructive" />
          <span className="text-center text-destructive">{error}</span>
          <Button onClick={() => void issue(base)}>
            <RefreshCw />
            Try again
          </Button>
        </Stage>
      )}

      {phase === "paired" && (
        <Stage tone="ok">
          <CircleCheck className="size-8 text-emerald-500" />
          <span className="text-center font-medium text-[14px] text-foreground">
            {device
              ? `${device.label || "A phone"} is connected`
              : "A phone is connected"}
          </span>
          <span className="text-center text-[12.5px]">
            {device ? `${platformLabel(device.platform)} · ` : ""}
            {user.name} can now talk to their bots.
          </span>
          <div className="mt-1 flex gap-2">
            <Button variant="outline" onClick={() => void issue(base)}>
              <QrCode />
              Connect another phone
            </Button>
            <Button onClick={onClose}>Done</Button>
          </div>
        </Stage>
      )}

      {phase === "ready" && mint && (
        <div className="flex flex-col gap-1.5">
          <span className={labelClass}>Link</span>
          <div className="flex items-start gap-2">
            <code
              data-testid="pairing-link"
              className="min-w-0 flex-1 select-all break-all rounded-md bg-muted px-2 py-1.5 font-mono text-[11px] text-foreground leading-snug"
            >
              {mint.link}
            </code>
            <Button
              size="sm"
              variant="outline"
              onClick={async () => {
                if (await copyText(mint.link)) toast.success("Link copied")
                else toast.error("Could not copy the link")
              }}
            >
              <Copy />
              Copy
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground">
            It opens a page on the phone that hands off to the app. Send it only
            to {user.name}: until it is used or expires it lets whoever has it
            pair a phone.
          </p>
        </div>
      )}

      {phase !== "paired" && (
        <form
          className="flex flex-col gap-1"
          onSubmit={(e) => {
            e.preventDefault()
            applyBase()
          }}
        >
          <label className={labelClass} htmlFor="pairing-base">
            Address the phone opens
          </label>
          <div className="flex items-center gap-2">
            <input
              id="pairing-base"
              value={baseDraft}
              onChange={(e) => setBaseDraft(e.target.value)}
              inputMode="url"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              aria-invalid={!!baseProblem}
              className={cn(fieldClass, "min-w-0 flex-1 font-mono text-[12px]")}
            />
            <Button
              type="submit"
              size="sm"
              variant="outline"
              disabled={!baseDirty || !!baseProblem}
            >
              Update
            </Button>
          </div>
          {baseProblem ? (
            <p className="text-[11px] text-destructive">{baseProblem}</p>
          ) : (
            <p className="text-[11px] text-muted-foreground">
              Starts as the address you are using now. Changing it makes a new
              code.
            </p>
          )}
        </form>
      )}
    </>
  )
}

function Stage({ children, tone }: { children: React.ReactNode; tone?: "ok" }) {
  return (
    <div
      className={cn(
        "mx-auto flex min-h-56 w-full max-w-[17rem] flex-col items-center justify-center gap-2.5 rounded-xl px-4 py-5 text-[13px] text-muted-foreground ring-1 ring-border",
        tone === "ok" && "max-w-none bg-emerald-500/5 ring-emerald-500/30"
      )}
    >
      {children}
    </div>
  )
}

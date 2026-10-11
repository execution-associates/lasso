import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronRight } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import { api } from "@/lib/api"
import { getMode, type Mode, setMode } from "@/lib/mode"
import {
  disablePush,
  enablePush,
  type PushState,
  readPushState,
} from "@/lib/push"
import { qk } from "@/lib/query"
import { useUIState } from "@/lib/ui-state"
import { cn } from "@/lib/utils"
import { Card, GroupHeader, Screen, ScrollBody } from "@/simple/parts"
import { switchUI } from "@/simple/route"

// The simple app's own settings. Everything else lasso can be told lives in
// the full UI, which the last row switches to on this device.

function Row({
  label,
  detail,
  children,
  divider,
  onClick,
}: {
  label: string
  detail?: React.ReactNode
  children?: React.ReactNode
  divider?: boolean
  onClick?: () => void
}) {
  const body = (
    <span
      className={cn(
        "flex min-h-[56px] w-full items-center gap-3 py-2.5 pr-4",
        divider && "border-border/60 border-t"
      )}
    >
      <span className="min-w-0 flex-1">
        <span className="block text-[16px] text-foreground">{label}</span>
        {detail && (
          <span className="mt-0.5 block text-[13px] text-muted-foreground leading-snug">
            {detail}
          </span>
        )}
      </span>
      {children}
    </span>
  )
  return onClick ? (
    <button
      type="button"
      onClick={onClick}
      className="block w-full pl-4 text-left active:bg-muted/60"
    >
      {body}
    </button>
  ) : (
    <div className="pl-4">{body}</div>
  )
}

function Switch({
  on,
  disabled,
  onChange,
  label,
}: {
  on: boolean
  disabled?: boolean
  onChange: (next: boolean) => void
  label: string
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!on)}
      className={cn(
        "relative h-[31px] w-[51px] shrink-0 rounded-full transition-colors disabled:opacity-40",
        on ? "bg-primary" : "bg-muted"
      )}
    >
      <span
        className={cn(
          "absolute top-[2px] left-[2px] size-[27px] rounded-full bg-background shadow transition-transform",
          on && "translate-x-5"
        )}
      />
    </button>
  )
}

function Notifications() {
  const queryClient = useQueryClient()
  const config = useQuery({
    queryKey: qk.push,
    queryFn: () => api.pushConfig(),
  })
  const [state, setState] = React.useState<PushState | null>(null)
  const [busy, setBusy] = React.useState(false)
  React.useEffect(() => {
    void readPushState().then((s) => {
      setState(s)
      void queryClient.invalidateQueries({ queryKey: qk.push })
    })
  }, [queryClient])
  const key = config.data?.public_key
  const on = state?.subscribed === true && state?.permission === "granted"

  const toggle = async (next: boolean) => {
    if (!key) return
    setBusy(true)
    try {
      // Straight from the tap: Safari honors requestPermission only from a
      // user gesture.
      setState(next ? await enablePush(key) : await disablePush())
      void queryClient.invalidateQueries({ queryKey: qk.push })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const detail =
    state?.support === "needs-home-screen"
      ? "On iPhone, add lasso to your Home Screen first, then turn this on from there."
      : state?.support === "unsupported"
        ? "This browser can't receive notifications."
        : state?.permission === "denied"
          ? "Notifications are blocked for lasso in this browser's settings."
          : "When an agent is waiting on you, or a bot has something new."
  return (
    <Row label="Notifications" detail={detail}>
      <Switch
        label="Notifications"
        on={on}
        disabled={!key || busy || state?.support !== "ok"}
        onChange={(n) => void toggle(n)}
      />
    </Row>
  )
}

const MODES: { id: Mode; label: string }[] = [
  { id: "system", label: "Auto" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
]

function Appearance() {
  // Re-render on a change from any browser: the mode is server state.
  useUIState()
  const mode = getMode()
  return (
    <Row label="Appearance">
      <span className="flex shrink-0 rounded-full bg-muted p-0.5">
        {MODES.map((m) => (
          <button
            key={m.id}
            type="button"
            aria-pressed={mode === m.id}
            onClick={() => setMode(m.id)}
            className={cn(
              "h-8 rounded-full px-3 text-[13.5px]",
              mode === m.id
                ? "bg-background font-medium text-foreground shadow-sm"
                : "text-muted-foreground"
            )}
          >
            {m.label}
          </button>
        ))}
      </span>
    </Row>
  )
}

export function SimpleSettings() {
  const version = useQuery({
    queryKey: ["simple", "version"],
    queryFn: () => api.version(),
    staleTime: 60_000,
  })
  return (
    <Screen title="Settings">
      <ScrollBody>
        <GroupHeader>This device</GroupHeader>
        <Card className="overflow-hidden">
          <Notifications />
          <div className="border-border/60 border-t" />
          <Appearance />
        </Card>

        <GroupHeader>lasso</GroupHeader>
        <Card className="overflow-hidden">
          <Row label="Connected to" detail={window.location.host}>
            <span className="shrink-0 text-[14px] text-muted-foreground">
              {version.data?.lasso_version ?? "…"}
            </span>
          </Row>
        </Card>

        <GroupHeader>Full lasso</GroupHeader>
        <Card className="overflow-hidden">
          <Row
            label="Switch to the advanced view"
            detail="Terminals, files, the browser and every setting. Remembered on this device; the full view has a way back."
            onClick={() => switchUI("advanced")}
          >
            <ChevronRight className="size-5 shrink-0 text-muted-foreground/70" />
          </Row>
        </Card>
      </ScrollBody>
    </Screen>
  )
}

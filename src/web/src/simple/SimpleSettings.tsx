import { useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronRight, Minus, Plus } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
import {
  ANALYTICS_LABEL,
  AnalyticsNote,
  useAnalyticsSetting,
} from "@/components/AnalyticsToggle"
import type { PhoneTextField } from "@/lib/api"
import { api } from "@/lib/api"
import { getMode, type Mode, setMode } from "@/lib/mode"
import { setNotifyKind, useNotifyChoices } from "@/lib/notify"
import {
  effectivePhoneText,
  formatPhoneText,
  PHONE_TEXT_CONTROLS,
  PHONE_TEXT_FIELDS,
  resetPhoneText,
  setPhoneText,
} from "@/lib/phone-text"
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

// Which kinds of notification may interrupt this device: a push, a toast. The
// Inbox lists everything waiting whatever is chosen here.
function NotifyKinds() {
  const q = useNotifyChoices()
  const kinds = q.data?.kinds ?? []
  return (
    <>
      <GroupHeader>Notify me about</GroupHeader>
      <Card className="overflow-hidden">
        {kinds.map((k, i) => (
          <Row
            key={k.id}
            label={k.label}
            detail={k.description}
            divider={i > 0}
          >
            <Switch
              label={k.label}
              on={q.data?.device.wants[k.id] ?? k.default}
              onChange={(on) =>
                void setNotifyKind(k.id, on).catch((e) =>
                  toast.error((e as Error).message)
                )
              }
            />
          </Row>
        ))}
        {q.isError && (
          <Row
            label="Couldn't load your choices"
            detail={(q.error as Error).message}
          />
        )}
      </Card>
      <p className="px-2 pt-2 text-[13px] text-muted-foreground leading-snug">
        For this device only. The Inbox lists everything waiting either way.
      </p>
    </>
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

// A value is stepped rather than dragged: a thumb-sized slider on a phone
// lands a pixel off as often as not, and each tap here is one exact step.
function Stepper({
  field,
  value,
  isSet,
}: {
  field: PhoneTextField
  value: number
  isSet: boolean
}) {
  const c = PHONE_TEXT_CONTROLS[field]
  const [lo, hi] = c.span
  // Rounded to the step, so 1.6 + 0.1 stores 1.7 and not 1.7000000000000002.
  const step = (dir: 1 | -1) => {
    const decimals = (String(c.step).split(".")[1] ?? "").length
    const next = Number((value + dir * c.step).toFixed(decimals))
    setPhoneText({ [field]: Math.min(hi, Math.max(lo, next)) })
  }
  const btn =
    "flex size-11 shrink-0 items-center justify-center rounded-full bg-muted text-foreground active:bg-accent disabled:opacity-30"
  return (
    <Row label={c.label} detail={isSet ? undefined : "default"}>
      <span className="flex shrink-0 items-center gap-1.5">
        <button
          type="button"
          aria-label={`Less ${c.label.toLowerCase()}`}
          disabled={value <= lo}
          onClick={() => step(-1)}
          className={btn}
        >
          <Minus className="size-5" />
        </button>
        <span
          aria-live="polite"
          className="min-w-[4.5em] text-center text-[15px] text-foreground tabular-nums"
        >
          {formatPhoneText(field, value)}
        </span>
        <button
          type="button"
          aria-label={`More ${c.label.toLowerCase()}`}
          disabled={value >= hi}
          onClick={() => step(1)}
          className={btn}
        >
          <Plus className="size-5" />
        </button>
      </span>
    </Row>
  )
}

// What the conversations and the Inbox read like at these settings: set
// inside .phone-chat on the reading panel, so the same rules and properties
// apply as in a real chat.
function TextPreview() {
  return (
    <div className="phone-chat px-4 pt-3 pb-4">
      <div className="chat-column border border-border/60 px-4 py-3">
        <div className="md-body md-chat">
          <p>
            The tests pass and the branch is pushed.{" "}
            <strong>Nothing is merged yet.</strong> Want me to open the pull
            request?
          </p>
        </div>
        <div className="mt-2 font-mono text-[11.5px] text-muted-foreground">
          Bash · go test ./... · 12s
        </div>
      </div>
    </div>
  )
}

function TextSettings() {
  const { values, set } = effectivePhoneText(useUIState().phone_text ?? {})
  const anySet = PHONE_TEXT_FIELDS.some((f) => set[f])
  return (
    <>
      <GroupHeader>Text</GroupHeader>
      <Card className="overflow-hidden">
        <TextPreview />
        {PHONE_TEXT_FIELDS.map((f) => (
          <div key={f} className="border-border/60 border-t">
            <Stepper field={f} value={values[f]} isSet={set[f]} />
          </div>
        ))}
        <div className="border-border/60 border-t">
          <button
            type="button"
            disabled={!anySet}
            onClick={resetPhoneText}
            className="flex min-h-[56px] w-full items-center px-4 text-left text-[16px] text-destructive active:bg-muted/60 disabled:text-muted-foreground disabled:opacity-60"
          >
            Reset to defaults
          </button>
        </div>
      </Card>
      <p className="px-2 pt-2 text-[13px] text-muted-foreground leading-snug">
        Conversations and Inbox cards in this app, on every phone. The full
        lasso's chat keeps its own, in Settings → Chat text. The reading panel
        shows behind a conversation when your theme has a backdrop.
      </p>
    </>
  )
}

function Analytics() {
  const a = useAnalyticsSetting()
  return (
    <>
      <GroupHeader>Privacy</GroupHeader>
      <Card className="overflow-hidden">
        <Row
          label={ANALYTICS_LABEL}
          detail={
            <AnalyticsNote heldBy={a.heldBy} url={a.config?.privacy_url} />
          }
        >
          <Switch
            on={a.on}
            disabled={a.busy || !!a.heldBy}
            onChange={a.set}
            label={ANALYTICS_LABEL}
          />
        </Row>
      </Card>
    </>
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

        <TextSettings />

        <NotifyKinds />

        <GroupHeader>lasso</GroupHeader>
        <Card className="overflow-hidden">
          <Row label="Connected to" detail={window.location.host}>
            <span className="shrink-0 text-[14px] text-muted-foreground">
              {version.data?.lasso_version ?? "…"}
            </span>
          </Row>
        </Card>

        <Analytics />

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

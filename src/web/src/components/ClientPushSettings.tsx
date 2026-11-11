import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"
import { toast } from "sonner"
import { Field, flatFieldClass } from "@/components/ui/field"
import {
  api,
  type ClientPushConfig,
  type ClientPushPatch,
  type ClientPushState,
} from "@/lib/api"
import { qk } from "@/lib/query"

// Push to the phone apps (clientpush.go, docs/design/client-threads.md): the
// APNs key and the Firebase service account are named by reference — a
// variable in lasso's environment or an op:// reference — and never stored or
// shown. Every field autosaves on blur, like Internal AI.

function stateText(s: ClientPushState | undefined, reason?: string): string {
  switch (s) {
    case "ready":
      return "ready"
    case "failing":
      return `failing${reason ? ` (${reason})` : ""}`
    case "unreadable":
      return reason || "unreadable"
    case "unconfigured":
      return "off"
  }
  return "reading…"
}

export function ClientPushSummary({ active }: { active: boolean }) {
  const q = useQuery({
    queryKey: qk.clientPush,
    queryFn: () => api.clientPush(),
    enabled: active,
  })
  if (!q.data) return null
  return (
    <>
      iPhone {stateText(q.data.apns.state)} · Android{" "}
      {stateText(q.data.fcm.state)}
    </>
  )
}

type Key = "team_id" | "key_id" | "topic" | "key_ref" | "credentials_ref"

export function ClientPushSettings({ active }: { active: boolean }) {
  const queryClient = useQueryClient()
  const q = useQuery({
    queryKey: qk.clientPush,
    queryFn: () => api.clientPush(),
    enabled: active,
  })
  const [vals, setVals] = React.useState<Record<Key, string>>({
    team_id: "",
    key_id: "",
    topic: "",
    key_ref: "",
    credentials_ref: "",
  })
  const seeded = React.useRef(false)
  React.useEffect(() => {
    if (!q.data || seeded.current) return
    seeded.current = true
    setVals({
      team_id: q.data.apns.team_id,
      key_id: q.data.apns.key_id,
      topic: q.data.apns.topic,
      key_ref: q.data.apns.key_ref,
      credentials_ref: q.data.fcm.credentials_ref,
    })
  }, [q.data])
  const save = useMutation({
    mutationFn: (patch: ClientPushPatch) => api.setClientPush(patch),
    onSuccess: (d: ClientPushConfig) =>
      queryClient.setQueryData(qk.clientPush, d),
    onError: (e: Error) => toast.error(`Couldn't save push: ${e.message}`),
  })
  const stored = q.data
  const storedOf = (k: Key) =>
    !stored
      ? ""
      : k === "credentials_ref"
        ? stored.fcm.credentials_ref
        : stored.apns[k]
  const saveIfChanged = (k: Key) => {
    const v = vals[k].trim()
    if (!stored || storedOf(k) === v) return
    save.mutate(
      k === "credentials_ref" ? { fcm: { [k]: v } } : { apns: { [k]: v } }
    )
  }
  const input = (k: Key, placeholder: string) => (
    <input
      id={`settings-push-${k}`}
      className={flatFieldClass}
      value={vals[k]}
      placeholder={placeholder}
      spellCheck={false}
      autoComplete="off"
      onChange={(e) => setVals((p) => ({ ...p, [k]: e.target.value }))}
      onBlur={() => saveIfChanged(k)}
    />
  )
  return (
    <div className="mb-4 flex flex-col gap-3">
      <p className="text-[12px] text-foreground">
        iPhone (APNs):{" "}
        <span className="text-muted-foreground">
          {stateText(stored?.apns.state, stored?.apns.reason)}
        </span>
      </p>
      <Field
        label="Team ID"
        hint="Your Apple developer team id (10 characters)."
        htmlFor="settings-push-team_id"
      >
        {input("team_id", "JF4UNR89NW")}
      </Field>
      <Field
        label="Key ID"
        hint="The id of the APNs auth key (.p8), 10 characters."
        htmlFor="settings-push-key_id"
      >
        {input("key_id", "ABCDE12345")}
      </Field>
      <Field
        label="Key"
        hint="Where the .p8 key's text comes from, never the key itself: a variable in lasso's environment, or op://vault/item/field."
        htmlFor="settings-push-key_ref"
      >
        {input("key_ref", "op://Lasso/APNs push key/p8")}
      </Field>
      <Field
        label="Topic"
        hint={`The iPhone app's bundle id. Empty uses ${stored?.apns.default_topic ?? "the default"}.`}
        htmlFor="settings-push-topic"
      >
        {input("topic", stored?.apns.default_topic ?? "")}
      </Field>
      <p className="text-[12px] text-foreground">
        Android (FCM):{" "}
        <span className="text-muted-foreground">
          {stateText(stored?.fcm.state, stored?.fcm.reason)}
          {stored?.fcm.project_id ? ` · project ${stored.fcm.project_id}` : ""}
        </span>
      </p>
      <Field
        label="Service account"
        hint="Where the Firebase service account JSON comes from: a variable in lasso's environment, or op://vault/item/field."
        htmlFor="settings-push-credentials_ref"
      >
        {input(
          "credentials_ref",
          "op://Lasso/Firebase lasso push/service-account.json"
        )}
      </Field>
      {save.isPending && (
        <span className="text-[11px] text-muted-foreground">Saving…</span>
      )}
      <p className="text-[11px] text-muted-foreground">
        The phone apps get a notification when a bot answers and the app is not
        open. Keys are read from this machine's environment or with{" "}
        <span className="font-mono">op read</span>; lasso never stores them.
        Send a test from a person's phone on the People page.
      </p>
    </div>
  )
}

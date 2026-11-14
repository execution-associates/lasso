import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"
import { toast } from "sonner"
import { Field, flatFieldClass } from "@/components/ui/field"
import {
  api,
  type ClientTTSConfig,
  type ClientTTSPatch,
  type ClientTTSTestResult,
} from "@/lib/api"
import { qk } from "@/lib/query"

// The phone apps' text-to-speech relay (clienttts.go, docs/design/
// client-tts.md): Inworld's key is named by reference — a variable in lasso's
// environment or an op:// reference — and never stored or shown. Every field
// autosaves on blur, like Phone app push.

function stateText(d: ClientTTSConfig | undefined): string {
  if (!d) return "reading…"
  const i = d.inworld
  switch (i.state) {
    case "ready":
      return i.enabled ? "ready" : "off"
    case "failing":
      return "failing"
    case "unreadable":
      return i.reason || "key unreadable"
    case "unconfigured":
      return "off"
  }
}

export function ClientTTSSummary({ active }: { active: boolean }) {
  const q = useQuery({
    queryKey: qk.clientTTS,
    queryFn: () => api.clientTTS(),
    enabled: active,
  })
  if (!q.data) return null
  return <>Inworld {stateText(q.data)}</>
}

type Key =
  | "key_ref"
  | "default_voice"
  | "monthly_character_limit"
  | "total_monthly_character_limit"

export function ClientTTSSettings({ active }: { active: boolean }) {
  const queryClient = useQueryClient()
  const q = useQuery({
    queryKey: qk.clientTTS,
    queryFn: () => api.clientTTS(),
    enabled: active,
  })
  const [vals, setVals] = React.useState<Record<Key, string>>({
    key_ref: "",
    default_voice: "",
    monthly_character_limit: "",
    total_monthly_character_limit: "",
  })
  const [test, setTest] = React.useState<ClientTTSTestResult | null>(null)
  const seeded = React.useRef(false)
  React.useEffect(() => {
    if (!q.data || seeded.current) return
    seeded.current = true
    const i = q.data.inworld
    setVals({
      key_ref: i.key_ref,
      default_voice: i.default_voice,
      monthly_character_limit: String(i.monthly_character_limit),
      total_monthly_character_limit: String(i.total_monthly_character_limit),
    })
  }, [q.data])
  const save = useMutation({
    mutationFn: (patch: ClientTTSPatch) => api.setClientTTS(patch),
    onSuccess: (d: ClientTTSConfig) =>
      queryClient.setQueryData(qk.clientTTS, d),
    onError: (e: Error) => toast.error(`Couldn't save speech: ${e.message}`),
  })
  const run = useMutation({
    mutationFn: () => api.testClientTTS(),
    onSuccess: (r) => {
      setTest(r)
      queryClient.invalidateQueries({ queryKey: qk.clientTTS })
    },
    onError: (e: Error) => toast.error(`Test failed: ${e.message}`),
  })
  const stored = q.data?.inworld
  const saveIfChanged = (k: Key) => {
    const v = vals[k].trim()
    if (!stored) return
    if (k === "key_ref" || k === "default_voice") {
      if (stored[k] !== v) save.mutate({ inworld: { [k]: v } })
      return
    }
    const n = Number(v)
    if (!Number.isInteger(n) || n < 0) {
      toast.error("A limit is a whole number of characters.")
      return
    }
    if (stored[k] !== n) save.mutate({ inworld: { [k]: n } })
  }
  const input = (k: Key, placeholder: string) => (
    <input
      id={`settings-tts-${k}`}
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
        Inworld:{" "}
        <span className="text-muted-foreground">{stateText(q.data)}</span>
      </p>
      <Field
        label="API key"
        hint="Where Inworld's key comes from, never the key itself: a variable in lasso's environment, or op://vault/item/field."
        htmlFor="settings-tts-key_ref"
      >
        {input("key_ref", "op://Lasso/Inworld/api key")}
      </Field>
      <Field
        label="Default voice"
        hint="The voice id the apps use unless they choose another."
        htmlFor="settings-tts-default_voice"
      >
        {input("default_voice", "Ashley")}
      </Field>
      <Field
        label="Monthly limit per person"
        hint={`Characters a person may have spoken each month (default ${stored?.default_monthly_character_limit ?? ""}).`}
        htmlFor="settings-tts-monthly_character_limit"
      >
        {input("monthly_character_limit", "")}
      </Field>
      <Field
        label="Monthly limit in total"
        hint={`Characters everyone together may have spoken each month: this is what caps the bill (default ${stored?.default_total_monthly_character_limit ?? ""}).`}
        htmlFor="settings-tts-total_monthly_character_limit"
      >
        {input("total_monthly_character_limit", "")}
      </Field>
      <label className="flex items-center gap-2 text-[12px] text-foreground">
        <input
          type="checkbox"
          checked={stored?.enabled ?? true}
          disabled={!stored}
          onChange={(e) =>
            save.mutate({ inworld: { enabled: e.target.checked } })
          }
        />
        Enabled
      </label>
      {q.data && (
        <p className="text-[12px] text-muted-foreground">
          {q.data.month}: {q.data.usage.characters_this_month.toLocaleString()}{" "}
          of {q.data.usage.limit_characters.toLocaleString()} characters
          {q.data.usage.people && q.data.usage.people.length > 0
            ? ` · ${q.data.usage.people
                .map(
                  (p) =>
                    `${p.name || p.user_id} ${p.characters.toLocaleString()}`
                )
                .join(", ")}`
            : ""}
        </p>
      )}
      <div className="flex items-center gap-3">
        <button
          type="button"
          className="rounded border border-border px-2 py-1 text-[12px] disabled:opacity-50"
          disabled={run.isPending || stored?.state === "unconfigured"}
          onClick={() => run.mutate()}
        >
          {run.isPending ? "Speaking…" : "Test"}
        </button>
        {test && (
          <span className="text-[12px] text-muted-foreground">
            {test.ok
              ? `ok · first audio ${test.first_audio_ms ?? "?"} ms · ${test.audio_ms ?? "?"} ms of speech`
              : `failed: ${test.code ?? "unknown"}${test.message ? ` (${test.message})` : ""}`}
          </span>
        )}
      </div>
      {save.isPending && (
        <span className="text-[11px] text-muted-foreground">Saving…</span>
      )}
      <p className="text-[11px] text-muted-foreground">
        The phone apps can speak a bot's replies with Inworld's voices without
        holding the key. What is spoken leaves this machine for Inworld, and
        counts against the limits above.
      </p>
    </div>
  )
}

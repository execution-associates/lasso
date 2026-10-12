import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import * as React from "react"
import { toast } from "sonner"
import { Checkbox } from "@/components/ui/checkbox"
import { api, type TelemetryConfig } from "@/lib/api"
import { qk } from "@/lib/query"
import { setTelemetryActive } from "@/lib/telemetry"

// Anonymous usage analytics, the one switch (telemetry.go). It is this
// lasso's setting, shared by every browser and phone on it: the server is
// what forwards events, so the server is what decides.

export const ANALYTICS_LABEL = "Anonymous usage analytics"

export const ANALYTICS_EXPLAINER =
  "Which views and features get used, and rough counts, under a random id. Never names, paths, hosts, prompts or anything you type."

// The config and a setter, for every place that shows the switch (Settings,
// the simple app's Settings, the first-run tour).
export function useAnalyticsSetting() {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: qk.telemetry,
    queryFn: () => api.telemetryConfig(),
    retry: false,
  })
  React.useEffect(() => {
    if (query.data) setTelemetryActive(query.data.active)
  }, [query.data])
  const mutation = useMutation({
    mutationFn: (on: boolean) => api.setTelemetry(on),
    onSuccess: (c: TelemetryConfig) => {
      setTelemetryActive(c.active)
      queryClient.setQueryData(qk.telemetry, c)
    },
    onError: (e: Error) => toast.error(`Couldn't save: ${e.message}`),
  })
  const c = query.data
  return {
    config: c,
    // The environment (DO_NOT_TRACK, LASSO_TELEMETRY=0) overrides the choice.
    heldBy: c?.env_off || "",
    on: c ? !c.env_off && c.enabled : true,
    busy: !c || mutation.isPending,
    set: (on: boolean) => mutation.mutate(on),
  }
}

export function AnalyticsNote({
  heldBy,
  url,
}: {
  heldBy: string
  url?: string
}) {
  return (
    <>
      {ANALYTICS_EXPLAINER}{" "}
      <a
        href={url || "https://www.executionassociates.com/privacy/"}
        target="_blank"
        rel="noreferrer"
        className="underline underline-offset-2 hover:text-foreground"
      >
        Privacy policy
      </a>
      .{heldBy && ` Off: ${heldBy} is set in lasso's environment.`}
    </>
  )
}

export function AnalyticsToggle({
  id = "settings-analytics",
}: {
  id?: string
}) {
  const a = useAnalyticsSetting()
  return (
    <div className="flex flex-col gap-1">
      <label
        className="flex cursor-pointer select-none items-center gap-2 text-[13px] text-foreground"
        htmlFor={id}
      >
        <Checkbox
          id={id}
          checked={a.on}
          disabled={a.busy || !!a.heldBy}
          onCheckedChange={(c) => a.set(c === true)}
        />
        {ANALYTICS_LABEL}
      </label>
      <p className="text-[11px] text-muted-foreground">
        <AnalyticsNote heldBy={a.heldBy} url={a.config?.privacy_url} />
      </p>
    </div>
  )
}

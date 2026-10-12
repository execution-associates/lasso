import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, ChevronDown, Search } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"

import {
  type AgentType,
  type CreatorDraft,
  clearDraftParams,
  createAgentRetrying,
  FALLBACK_HARNESSES,
  generateBranchName,
  hostUsable,
  PROMPT_PLACEHOLDERS,
  promptTitle,
  readDraft,
  readDraftHost,
  rememberCreatorHost,
  saveDraft,
  saveDraftHost,
} from "@/components/NewDialog"
import { NO_AUTOCORRECT } from "@/components/ui/input"
import { api, type CreateAgentPayload } from "@/lib/api"
import { tilde } from "@/lib/format"
import { qk } from "@/lib/query"
import { uiStateNow } from "@/lib/ui-state"
import { cn } from "@/lib/utils"
import { Card, Screen, ScrollBody } from "@/simple/parts"

// A new agent, from a phone. The full UI's creator (components/NewDialog.tsx)
// laid out for a thumb: the same API (createAgentRetrying), the same queries,
// and the same remembered choices — this browser's per-host draft and picked
// host, then the host's creator settings and last selections — so a phone and
// the full UI in one browser open on the same answers. What a phone does not
// need is left to the full UI: extra CLI args, plan mode, the advisor, file
// attachments and pasted screenshots.
//
// onCreated receives where the agent runs, to open its conversation.

const field =
  "h-12 w-full min-w-0 rounded-2xl border border-border bg-background/40 px-3.5 text-[16px] text-foreground outline-none focus:border-primary/60 disabled:opacity-50"

function Label({
  htmlFor,
  children,
}: {
  htmlFor?: string
  children: React.ReactNode
}) {
  return (
    <label
      htmlFor={htmlFor}
      className="font-medium text-[14px] text-muted-foreground"
    >
      {children}
    </label>
  )
}

// The repository, picked by typing part of its name: a list that narrows as
// you type, every row a full-width tap target. Collapsed to the chosen repo
// once one is picked.
function RepoPicker({
  repos,
  value,
  onChange,
  error,
  loading,
}: {
  repos: { path: string; name: string }[]
  value: string
  onChange: (path: string) => void
  error: string | null
  loading: boolean
}) {
  const [open, setOpen] = React.useState(false)
  const [filter, setFilter] = React.useState("")
  const current = repos.find((r) => r.path === value)
  const q = filter.trim().toLowerCase()
  const shown = q
    ? repos.filter(
        (r) =>
          r.name.toLowerCase().includes(q) || r.path.toLowerCase().includes(q)
      )
    : repos
  if (!open)
    return (
      <button
        type="button"
        id="new-repo"
        onClick={() => {
          setFilter("")
          setOpen(true)
        }}
        className={cn(field, "flex items-center gap-2 text-left")}
      >
        <span className="min-w-0 flex-1 truncate">
          {current?.name ??
            (loading
              ? "Loading repositories…"
              : error
                ? "Couldn't load repositories"
                : "Choose a repository")}
        </span>
        <ChevronDown className="size-5 shrink-0 text-muted-foreground" />
      </button>
    )
  return (
    <div className="flex flex-col gap-2">
      <div className="relative">
        <Search className="pointer-events-none absolute top-3.5 left-3.5 size-5 text-muted-foreground" />
        <input
          id="new-repo"
          // biome-ignore lint/a11y/noAutofocus: the picker was opened to type into.
          autoFocus
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") setOpen(false)
            if (e.key === "Enter") {
              e.preventDefault()
              if (shown[0]) {
                onChange(shown[0].path)
                setOpen(false)
              }
            }
          }}
          placeholder="Filter repositories…"
          enterKeyHint="go"
          className={cn(field, "pl-11")}
          {...NO_AUTOCORRECT}
        />
      </div>
      <Card className="max-h-[50vh] overflow-y-auto">
        {shown.length === 0 && (
          <div className="px-4 py-3 text-[15px] text-muted-foreground">
            {error ?? "No repositories match."}
          </div>
        )}
        {shown.map((r, i) => (
          <button
            key={r.path}
            type="button"
            onClick={() => {
              onChange(r.path)
              setOpen(false)
            }}
            className={cn(
              "flex min-h-[52px] w-full items-center gap-3 px-4 py-2 text-left active:bg-muted/60",
              i > 0 && "border-border/60 border-t"
            )}
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[16px] text-foreground">
                {r.name}
              </span>
              <span className="block truncate text-[12.5px] text-muted-foreground">
                {tilde(r.path)}
              </span>
            </span>
            {r.path === value && (
              <Check className="size-5 shrink-0 text-primary" />
            )}
          </button>
        ))}
      </Card>
    </div>
  )
}

export function NewAgent({
  onBack,
  onCreated,
}: {
  onBack: () => void
  onCreated: (host: string, pane: string | undefined) => void
}) {
  const queryClient = useQueryClient()
  const [placeholder] = React.useState(
    () =>
      PROMPT_PLACEHOLDERS[
        Math.floor(Math.random() * PROMPT_PLACEHOLDERS.length)
      ]
  )

  // The host, seeded as the full UI's creator seeds it: the one pinned in
  // Settings, else this browser's last pick, else where the last create ran.
  const [host, setHost] = React.useState(() => {
    const ui = uiStateNow()
    return (
      ui.creator_default_host ||
      readDraftHost() ||
      ui.creator_last_host ||
      "local"
    )
  })
  const hostsQuery = useQuery({
    queryKey: ["hosts"],
    queryFn: () => api.hosts(),
  })
  const usableHosts = React.useMemo(
    () => (hostsQuery.data?.hosts ?? []).filter(hostUsable),
    [hostsQuery.data]
  )
  const localLabel = hostsQuery.data?.local?.hostname || "this machine"
  // A remembered host this lasso can no longer create on falls back to the
  // local one once the probe has answered, as the full UI's does.
  React.useEffect(() => {
    if (!hostsQuery.isSuccess || host === "local") return
    if (!usableHosts.some((h) => h.alias === host)) {
      setHost("local")
      if (readDraftHost() === host) saveDraftHost("")
    }
  }, [hostsQuery.isSuccess, usableHosts, host])

  const configQuery = useQuery({
    queryKey: qk.agentConfig(host),
    queryFn: () => api.agentConfig(host),
  })
  const reposQuery = useQuery({
    queryKey: qk.repos(host),
    queryFn: () => api.repos(host),
  })
  const config = configQuery.data ?? null
  const repos = reposQuery.data?.repos ?? []
  const reposError = reposQuery.isError
    ? (reposQuery.error as Error).message
    : null
  const harnesses = config?.harnesses?.length
    ? config.harnesses
    : FALLBACK_HARNESSES

  const [type, setType] = React.useState<AgentType>("git")
  const [repo, setRepo] = React.useState("")
  const [baseBranch, setBaseBranch] = React.useState("")
  const [agent, setAgent] = React.useState("claude")
  const [model, setModel] = React.useState("")
  const [effort, setEffort] = React.useState("")
  const [prompt, setPrompt] = React.useState("")
  const harness =
    harnesses.find((h) => h.id === agent) ??
    harnesses[0] ??
    FALLBACK_HARNESSES[0]

  // Seed once per host, from this browser's draft for it, then the host's
  // remembered selections (the full UI's order, see NewDialog).
  const seededFor = React.useRef<string | null>(null)
  React.useEffect(() => {
    if (!config || !reposQuery.isSuccess || reposQuery.isFetching) return
    if (seededFor.current === host) return
    seededFor.current = host
    const draft = readDraft(host)
    setType(draft.type || config.last_agent_type || "git")
    const seededAgent =
      (draft.agent && harnesses.some((h) => h.id === draft.agent)
        ? draft.agent
        : "") ||
      config.default_agent ||
      config.last_agent ||
      "claude"
    setAgent(seededAgent)
    const same = draft.agent === seededAgent
    setModel(same ? (draft.model ?? "") : "")
    setEffort(same ? (draft.effort ?? "") : "")
    const remembered =
      draft.repo && repos.some((r) => r.path === draft.repo) ? draft.repo : ""
    const last = config.last_repo
    setRepo(
      remembered ||
        (last && repos.some((r) => r.path === last)
          ? last
          : (repos[0]?.path ?? ""))
    )
  }, [
    host,
    config,
    reposQuery.isSuccess,
    reposQuery.isFetching,
    repos,
    harnesses,
  ])

  // Remember what is picked, into the same per-host draft the full UI reads,
  // keeping the fields only the full UI edits as they were.
  React.useEffect(() => {
    if (seededFor.current !== host) return
    const prev = readDraft(host)
    const draft: CreatorDraft = {
      extraArgs: "",
      planMode: false,
      advisor: false,
      prefix: config?.branch_prefix ?? "",
      advanced: false,
      ...prev,
      type,
      repo,
      agent,
      model,
      effort,
    }
    saveDraft(host, draft)
  }, [host, type, repo, agent, model, effort, config])

  const branchesQuery = useQuery({
    queryKey: qk.repoBranches(host, repo),
    queryFn: () => api.repoBranches(repo, host),
    enabled: type === "git" && !!repo,
  })
  const branches = React.useMemo(() => {
    const b = branchesQuery.data
    return b ? [...(b.branches ?? []), ...(b.remoteBranches ?? [])] : []
  }, [branchesQuery.data])
  // The repo's remembered base branch while it still exists, else its default.
  React.useEffect(() => {
    if (type !== "git" || !repo) return
    const re = repos.find((r) => r.path === repo)
    const b = branchesQuery.data
    if (!b) {
      if (branchesQuery.isError) setBaseBranch(re?.last_base_branch || "main")
      return
    }
    const all = [...(b.branches ?? []), ...(b.remoteBranches ?? [])]
    const preferred = re?.last_base_branch
    const fallback =
      b.default ||
      (all.includes("main")
        ? "main"
        : all.includes("master")
          ? "master"
          : "") ||
      all[0] ||
      "main"
    setBaseBranch(preferred && all.includes(preferred) ? preferred : fallback)
  }, [repo, type, repos, branchesQuery.data, branchesQuery.isError])

  const create = useMutation({
    mutationFn: () => {
      const payload: CreateAgentPayload = {
        host,
        type,
        prompt: prompt.trim(),
        agent,
        model: model.trim() || undefined,
        effort: harness.effort_levels?.includes(effort) ? effort : undefined,
        plan_mode: false,
        advisor: false,
      }
      if (type === "git") {
        const draft = readDraft(host)
        payload.repo = repo
        payload.base_branch = baseBranch || undefined
        payload.branch_prefix =
          (draft.prefix ?? config?.branch_prefix ?? "").trim() || undefined
        payload.branch_name =
          generateBranchName(promptTitle(prompt)) || undefined
      }
      return createAgentRetrying(payload)
    },
    onSuccess: (rec) => {
      toast.success(`Created ${rec.title}`)
      clearDraftParams(host)
      rememberCreatorHost(host)
      void queryClient.invalidateQueries({ queryKey: ["agent-config"] })
      void queryClient.invalidateQueries({ queryKey: ["repos"] })
      void queryClient.invalidateQueries({ queryKey: ["simple"] })
      onCreated(host, rec.root_pane)
    },
    onError: (err) => {
      toast.error("Couldn't create the agent", {
        description: err instanceof Error ? err.message : String(err),
      })
    },
  })
  const canCreate = !create.isPending && (type === "scratch" || !!repo)
  const submit = () => {
    if (canCreate) create.mutate()
  }

  const segment = (t: AgentType, label: string) => (
    <button
      type="button"
      aria-pressed={type === t}
      onClick={() => setType(t)}
      className={cn(
        "h-11 flex-1 rounded-full text-[15px]",
        type === t
          ? "bg-background font-medium text-foreground shadow-sm"
          : "text-muted-foreground"
      )}
    >
      {label}
    </button>
  )

  return (
    <Screen title="New agent" onBack={onBack}>
      <ScrollBody className="pb-[calc(env(safe-area-inset-bottom)+24px)]">
        <form
          className="flex flex-col gap-4 pt-2"
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
        >
          <div className="flex rounded-full bg-muted p-1">
            {segment("git", "Git")}
            {segment("scratch", "Scratch")}
          </div>
          <p className="-mt-2 px-2 text-[13px] text-muted-foreground leading-snug">
            {type === "git"
              ? "Its own branch and worktree of a repository."
              : "A fresh folder, no repository."}
          </p>

          {type === "git" && (
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="new-repo">Repository</Label>
                <RepoPicker
                  repos={repos}
                  value={repo}
                  onChange={setRepo}
                  error={reposError}
                  loading={reposQuery.isLoading}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="new-branch">Base branch</Label>
                <select
                  id="new-branch"
                  value={baseBranch}
                  onChange={(e) => setBaseBranch(e.target.value)}
                  disabled={!repo}
                  className={field}
                >
                  {baseBranch && !branches.includes(baseBranch) && (
                    <option value={baseBranch}>{baseBranch}</option>
                  )}
                  {branches.map((b) => (
                    <option key={b} value={b}>
                      {b}
                    </option>
                  ))}
                </select>
              </div>
            </div>
          )}

          <div className="flex flex-col gap-3">
            {usableHosts.length > 0 && (
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="new-host">Host</Label>
                <select
                  id="new-host"
                  value={host}
                  onChange={(e) => {
                    setHost(e.target.value)
                    saveDraftHost(e.target.value)
                  }}
                  className={field}
                >
                  <option value="local">{localLabel}</option>
                  {usableHosts.map((h) => (
                    <option key={h.alias} value={h.alias}>
                      {h.alias}
                    </option>
                  ))}
                </select>
              </div>
            )}
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="new-harness">Harness</Label>
              <select
                id="new-harness"
                value={agent}
                onChange={(e) => {
                  setAgent(e.target.value)
                  // Model names and effort levels belong to one harness.
                  setModel("")
                  setEffort("")
                }}
                className={field}
              >
                {harnesses.map((h) => (
                  <option key={h.id} value={h.id}>
                    {h.label}
                  </option>
                ))}
              </select>
            </div>
            <div className="flex gap-3">
              <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                <Label htmlFor="new-model">Model</Label>
                <input
                  id="new-model"
                  list="new-model-suggestions"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="default"
                  className={field}
                  {...NO_AUTOCORRECT}
                />
                <datalist id="new-model-suggestions">
                  {(harness.model_suggestions ?? []).map((m) => (
                    <option key={m} value={m} />
                  ))}
                </datalist>
              </div>
              {!!harness.effort_levels?.length && (
                <div className="flex min-w-0 flex-1 flex-col gap-1.5">
                  <Label htmlFor="new-effort">Thinking</Label>
                  <select
                    id="new-effort"
                    value={effort}
                    onChange={(e) => setEffort(e.target.value)}
                    className={field}
                  >
                    <option value="">default</option>
                    {harness.effort_levels.map((l) => (
                      <option key={l} value={l}>
                        {l}
                      </option>
                    ))}
                  </select>
                </div>
              )}
            </div>
          </div>

          <div className="flex flex-col gap-1.5 pt-1">
            <Label htmlFor="new-prompt">Prompt</Label>
            <textarea
              id="new-prompt"
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                // A hardware keyboard's ⌘/Ctrl+Enter creates; Enter breaks the
                // line, as in the full UI's creator.
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault()
                  submit()
                }
              }}
              rows={7}
              placeholder={placeholder}
              className="min-h-[11rem] w-full resize-none rounded-[22px] border border-border bg-background/40 px-4 py-3 text-[16px] text-foreground leading-snug outline-none placeholder:text-muted-foreground focus:border-primary/60"
              {...NO_AUTOCORRECT}
            />
            <p className="px-2 text-[13px] text-muted-foreground leading-snug">
              The first line names the agent. Leave it empty to start the agent
              idle.
            </p>
          </div>

          <button
            type="submit"
            disabled={!canCreate}
            className="h-12 w-full rounded-full bg-primary font-semibold text-[16px] text-primary-foreground disabled:opacity-40"
          >
            {create.isPending ? "Creating…" : "Create agent"}
          </button>
        </form>
      </ScrollBody>
    </Screen>
  )
}

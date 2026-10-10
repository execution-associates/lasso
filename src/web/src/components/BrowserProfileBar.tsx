import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Settings2, Trash2 } from "lucide-react"
import * as React from "react"
import { toast } from "sonner"
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
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { api, type BrowserProfileStatus } from "@/lib/api"
import { qk } from "@/lib/query"
import { cn } from "@/lib/utils"

// BrowserProfileBar is the strip along the bottom of the Agent browser: which
// of lasso's browsers this tab is looking at, and (on a server that can) the
// way into creating, editing and deleting them. Each is a separate Chromium
// with its own cookies and logins, so switching is a reconnect, not a filter
// over one browser's pages. (The server stores a browser as a "profile".)

export function BrowserProfileBar({
  profiles,
  current,
  onPick,
  manageable,
}: {
  profiles: BrowserProfileStatus[]
  current: string
  onPick: (id: string) => void
  // False on an older server with no profile API: the selector shows just
  // the one default profile and there is nothing to manage.
  manageable: boolean
}) {
  const [open, setOpen] = React.useState(false)
  return (
    <div className="flex flex-shrink-0 items-center gap-1.5 border-border border-t bg-background px-2 py-1 text-[12px]">
      <span className="text-muted-foreground">Browser</span>
      <select
        aria-label="Browser"
        className="h-6 min-w-0 max-w-48 flex-shrink rounded-md border border-border bg-background px-1.5 text-[12px] text-foreground"
        value={current}
        disabled={profiles.length < 2}
        onChange={(e) => onPick(e.target.value)}
      >
        {profiles.map((p) => (
          <option key={p.id} value={p.id}>
            {p.running ? "● " : ""}
            {p.name}
          </option>
        ))}
      </select>
      {manageable && (
        <Button
          variant="ghost"
          size="icon"
          className="ml-auto size-6"
          title="Manage browsers"
          aria-label="Manage browsers"
          onClick={() => setOpen(true)}
        >
          <Settings2 className="size-3.5" />
        </Button>
      )}
      {manageable && (
        <ProfilesDialog
          open={open}
          onOpenChange={setOpen}
          profiles={profiles}
          current={current}
          onPick={onPick}
        />
      )}
    </div>
  )
}

const fieldLabel = "text-[12px] font-medium text-muted-foreground"

// The dialog has two tabs: Manage edits any existing profile (picked from the
// list inside the dialog, which does NOT switch the bar's profile), New
// creates one. It opens on Manage with the bar's profile selected.
function ProfilesDialog({
  open,
  onOpenChange,
  profiles,
  current,
  onPick,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  profiles: BrowserProfileStatus[]
  current: string
  onPick: (id: string) => void
}) {
  const queryClient = useQueryClient()
  const refresh = () => queryClient.invalidateQueries({ queryKey: qk.browser })

  const [tab, setTab] = React.useState<"manage" | "new">("manage")
  const [editID, setEditID] = React.useState(current)
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on open only, not when the bar's profile changes under an open dialog
  React.useEffect(() => {
    if (!open) return
    setTab("manage")
    setEditID(current)
  }, [open])
  const editing =
    profiles.find((p) => p.id === editID) ??
    profiles.find((p) => p.default) ??
    profiles[0]

  // The edit form is a draft of the profile being edited, reset whenever the
  // dialog opens or a different profile is picked.
  const [name, setName] = React.useState("")
  const [editErr, setEditErr] = React.useState("")
  const editingID = editing?.id
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on open and on a different profile only, not on every status poll
  React.useEffect(() => {
    if (!open) return
    setName(editing?.name ?? "")
    setEditErr("")
  }, [open, editingID])

  const [newName, setNewName] = React.useState("")
  const [createErr, setCreateErr] = React.useState("")
  const [confirmDelete, setConfirmDelete] = React.useState(false)

  const save = useMutation({
    mutationFn: () => {
      if (!editing) throw new Error("no browser selected")
      return api.updateBrowserProfile(editing.id, { name: name.trim() })
    },
    onSuccess: (p) => {
      setEditErr("")
      toast.success(`Saved ${p.name}`)
    },
    // A 400 is the validation sentence, which belongs under the field.
    onError: (e: Error) => setEditErr(e.message),
    onSettled: refresh,
  })

  const create = useMutation({
    mutationFn: () => api.createBrowserProfile({ name: newName.trim() }),
    onSuccess: (p) => {
      setCreateErr("")
      setNewName("")
      toast.success(`Created browser ${p.name}`)
      onPick(p.id)
      setEditID(p.id)
      setTab("manage")
    },
    onError: (e: Error) => setCreateErr(e.message),
    onSettled: refresh,
  })

  const remove = useMutation({
    mutationFn: (p: BrowserProfileStatus) => api.deleteBrowserProfile(p.id),
    onSuccess: (_, p) => {
      toast.success(`Deleted browser ${p.name}`)
      if (p.id === current) onPick("default")
      setEditID("default")
    },
    onError: (e: Error) => setEditErr(e.message),
    onSettled: refresh,
  })

  const dirty = !!editing && name.trim() !== editing.name

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Browsers</DialogTitle>
            <DialogDescription>
              Each is its own browser with its own cookies and logins, kept
              between restarts. Agents pick one by name.
            </DialogDescription>
          </DialogHeader>

          <Tabs
            value={tab}
            onValueChange={(v) => setTab(v as "manage" | "new")}
          >
            <TabsList>
              <TabsTrigger value="manage">Manage</TabsTrigger>
              <TabsTrigger value="new">New</TabsTrigger>
            </TabsList>

            <TabsContent value="manage" className="flex flex-col gap-3">
              <div
                role="listbox"
                aria-label="Browser to edit"
                className="flex max-h-44 flex-col overflow-y-auto rounded-lg border border-border p-1"
              >
                {profiles.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    role="option"
                    aria-selected={p.id === editing?.id}
                    onClick={() => setEditID(p.id)}
                    className={cn(
                      "flex items-center gap-1.5 rounded-md px-2 py-1 text-left text-[13px]",
                      p.id === editing?.id
                        ? "bg-accent text-accent-foreground"
                        : "hover:bg-muted"
                    )}
                  >
                    <span
                      className={cn(
                        "w-2 flex-shrink-0 text-[10px]",
                        p.running ? "text-emerald-500" : "invisible"
                      )}
                      title={p.running ? "Running" : undefined}
                    >
                      ●
                    </span>
                    <span className="min-w-0 truncate">{p.name}</span>
                    {p.id === current && (
                      <span className="flex-shrink-0 text-[11px] text-muted-foreground">
                        (showing)
                      </span>
                    )}
                  </button>
                ))}
              </div>

              {editing && (
                <form
                  className="flex flex-col gap-1.5"
                  onSubmit={(e) => {
                    e.preventDefault()
                    if (dirty) save.mutate()
                  }}
                >
                  <label className={fieldLabel} htmlFor="bp-name">
                    Name
                    {editing.default && (
                      <span className="ml-1 font-normal">
                        (default browser)
                      </span>
                    )}
                  </label>
                  <Input
                    id="bp-name"
                    value={name}
                    className="h-8 text-[13px]"
                    onChange={(e) => setName(e.target.value)}
                  />
                  {editErr && (
                    <p className="text-[12px] text-destructive [overflow-wrap:anywhere]">
                      {editErr}
                    </p>
                  )}
                  <div className="flex items-center gap-1.5">
                    <Button
                      type="submit"
                      size="sm"
                      disabled={!dirty || save.isPending}
                    >
                      {save.isPending ? "Saving…" : "Save"}
                    </Button>
                    {!editing.default && (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="ml-auto gap-1 text-destructive"
                        disabled={remove.isPending}
                        onClick={() => setConfirmDelete(true)}
                      >
                        <Trash2 className="size-3.5" />
                        Delete
                      </Button>
                    )}
                  </div>
                </form>
              )}
            </TabsContent>

            <TabsContent value="new">
              <form
                className="flex flex-col gap-1.5"
                onSubmit={(e) => {
                  e.preventDefault()
                  if (newName.trim()) create.mutate()
                }}
              >
                <label className={fieldLabel} htmlFor="bp-new-name">
                  Name
                </label>
                <Input
                  id="bp-new-name"
                  value={newName}
                  placeholder="e.g. Work"
                  className="h-8 text-[13px]"
                  onChange={(e) => setNewName(e.target.value)}
                />
                {createErr && (
                  <p className="text-[12px] text-destructive [overflow-wrap:anywhere]">
                    {createErr}
                  </p>
                )}
                <div>
                  <Button
                    type="submit"
                    size="sm"
                    disabled={!newName.trim() || create.isPending}
                  >
                    {create.isPending ? "Creating…" : "Create"}
                  </Button>
                </div>
              </form>
            </TabsContent>
          </Tabs>
        </DialogContent>
      </Dialog>

      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete {editing?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Its browser is stopped and its pages close. Its cookies, logins
              and everything else saved in it are deleted for good.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (editing) remove.mutate(editing)
              }}
            >
              Delete
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}

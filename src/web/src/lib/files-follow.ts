import * as React from "react"

// What the file viewer follows instead of the focused pane, while a view has
// a better answer: the Bots view, with a bot open, says that bot's folder. Only
// "follow" honours it, so a tree the human has steered stays where it is, and
// with no view setting one the viewer follows the focused pane as always.
export type FilesFollowTarget = { host: string; cwd: string }

let current: FilesFollowTarget | null = null
const listeners = new Set<() => void>()

export function setFilesFollowTarget(t: FilesFollowTarget | null) {
  if (t?.host === current?.host && t?.cwd === current?.cwd) return
  current = t
  for (const l of listeners) l()
}

export function useFilesFollowTarget(): FilesFollowTarget | null {
  return React.useSyncExternalStore(
    (cb) => {
      listeners.add(cb)
      return () => {
        listeners.delete(cb)
      }
    },
    () => current
  )
}

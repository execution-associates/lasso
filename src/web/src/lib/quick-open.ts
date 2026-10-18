// ⌘P (App's shortcut handler) asks the file viewer's quick open to show. App
// and FilesPanel never import each other, so this is the same tiny pub/sub as
// lib/open-file.ts, plus one parked request: the Files panel mounts on first
// reveal, which is AFTER the key press, so a request nobody heard is held
// until a panel subscribes.

type Listener = () => void
const listeners = new Set<Listener>()
let parked = false

export function onQuickOpenRequest(fn: Listener): () => void {
  listeners.add(fn)
  if (parked) {
    parked = false
    fn()
  }
  return () => {
    listeners.delete(fn)
  }
}

export function requestQuickOpen() {
  if (listeners.size === 0) {
    parked = true
    return
  }
  for (const fn of listeners) fn()
}

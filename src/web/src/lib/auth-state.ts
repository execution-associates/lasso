// Whether the network's sign-in has run out.
//
// lasso has no login of its own: behind Cloudflare Access the Access session
// is what lets a request through, and it expires (24h by default). A page that
// stays open past that keeps working until its next request, which Access
// answers with a 401 because every request says it is one (hostFetch sends
// X-Requested-With, Cloudflare's documented way to get a 401 rather than a
// redirect the page cannot follow). The event stream needs no handling of its
// own: every reconnect starts with api.active(), which goes through hostFetch.
//
// Signing in again is a full-page load, which runs the Access login in this
// window (inside an installed app too), so the bar only has to offer that.
import * as React from "react"

let signedOut = false
const listeners = new Set<() => void>()

export function noteResponseStatus(status: number) {
  if (status !== 401 || signedOut) return
  signedOut = true
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

export function useSignedOut(): boolean {
  return React.useSyncExternalStore(
    subscribe,
    () => signedOut,
    () => false
  )
}

export function signInAgain() {
  window.location.reload()
}

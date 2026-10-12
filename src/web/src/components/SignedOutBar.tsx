import { Button } from "@/components/ui/button"
import { signInAgain, useSignedOut } from "@/lib/auth-state"

// Shown once a request comes back 401: the network's sign-in (Cloudflare
// Access) has run out. Reloading runs that sign-in again in this window.
export function SignedOutBar() {
  const signedOut = useSignedOut()
  if (!signedOut) return null
  return (
    <div
      className="fixed inset-x-0 top-0 z-[100] flex items-center justify-center gap-3 border-b bg-background/95 px-4 pt-[max(0.5rem,env(safe-area-inset-top))] pb-2 text-sm shadow-sm backdrop-blur"
      role="alert"
    >
      <span>You're signed out. Sign in again to keep working.</span>
      <Button onClick={signInAgain} size="sm">
        Sign in again
      </Button>
    </div>
  )
}

import { useQuery } from "@tanstack/react-query"
import { toast } from "sonner"

import { api, type LiveNotification, type NotifyView } from "@/lib/api"
import { queryClient } from "@/lib/query"

// This browser as a notify device (notifydevices.go): which kinds of
// notification may interrupt it. The choices live on the server beside the
// device's push subscription, so Web Push filters there; the live channel hands
// every open tab every notification, and this module decides what this tab
// shows. A choice governs interruptions only; an Inbox lists everything.

const DEVICE_KEY = "lasso-device"

let cachedDevice: string | null = null

// Per browser, not per tab: two tabs on one laptop are one device with one set
// of choices. In localStorage, so it outlives the session.
export function notifyDeviceID(): string {
  if (cachedDevice) return cachedDevice
  let id: string | null = null
  try {
    id = localStorage.getItem(DEVICE_KEY)
  } catch {
    /* storage blocked: an id for this page's life */
  }
  if (!id || !/^[A-Za-z0-9_-]{8,64}$/.test(id)) {
    id = `web-${Math.random().toString(36).slice(2, 12)}${Date.now().toString(36)}`
    try {
      localStorage.setItem(DEVICE_KEY, id)
    } catch {
      /* ignore */
    }
  }
  cachedDevice = id
  return id
}

// A name for the device's row in settings: coarse on purpose, like the push
// device list's.
export function notifyDeviceLabel(): string {
  const ua = navigator.userAgent
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "browser"
  const os = /iPhone/.test(ua)
    ? "iPhone"
    : /iPad/.test(ua)
      ? "iPad"
      : /Android/.test(ua)
        ? "Android"
        : /Macintosh/.test(ua)
          ? "Mac"
          : /Windows/.test(ua)
            ? "Windows"
            : /Linux/.test(ua)
              ? "Linux"
              : "device"
  return `${browser} on ${os}`
}

const notifyKey = () => ["notify", notifyDeviceID()] as const

export function useNotifyChoices() {
  return useQuery({
    queryKey: notifyKey(),
    queryFn: () => api.notify(notifyDeviceID()),
    staleTime: 60_000,
  })
}

// Choose whether this device takes one kind; null returns it to its default.
// Optimistic, so the switch moves under the finger.
export async function setNotifyKind(kind: string, on: boolean | null) {
  const prev = queryClient.getQueryData<NotifyView>(notifyKey())
  if (prev && on !== null)
    queryClient.setQueryData<NotifyView>(notifyKey(), {
      ...prev,
      device: { ...prev.device, wants: { ...prev.device.wants, [kind]: on } },
    })
  try {
    const next = await api.notifyDevice({
      id: notifyDeviceID(),
      label: notifyDeviceLabel(),
      kinds: { [kind]: on },
    })
    queryClient.setQueryData(notifyKey(), next)
  } catch (e) {
    if (prev) queryClient.setQueryData(notifyKey(), prev)
    throw e
  }
}

// Every live notification is re-dispatched on window under this name
// (detail: LiveNotification), for a view that keeps them (the simple app's
// Inbox lists lasso's notices).
export const NOTIFICATION_EVENT = "lasso:notification"

// Whether this device takes a kind. Before its choices have loaded, only
// lasso's own notices get through, as they always did as toasts.
async function wants(kind: string): Promise<boolean> {
  let view = queryClient.getQueryData<NotifyView>(notifyKey())
  if (!view) {
    try {
      view = await queryClient.fetchQuery({
        queryKey: notifyKey(),
        queryFn: () => api.notify(notifyDeviceID()),
        staleTime: 60_000,
      })
    } catch {
      return kind === "lasso_notice"
    }
  }
  return view?.device.wants[kind] ?? false
}

// handleLiveNotification is the SSE `notification` handler (lib/app-store):
// hand it to whatever keeps them, then interrupt this tab only when its device
// takes the kind and the tab is in front of someone. A hidden tab shows
// nothing: a phone's push is the interruption there.
export async function handleLiveNotification(raw: string) {
  let n: LiveNotification
  try {
    n = JSON.parse(raw)
  } catch {
    return
  }
  if (!n?.title) return
  window.dispatchEvent(new CustomEvent(NOTIFICATION_EVENT, { detail: n }))
  if (document.visibilityState !== "visible") return
  if (!(await wants(n.kind))) return
  const opts = {
    description: n.body || undefined,
    action:
      n.url?.startsWith("/") && !n.url.startsWith("//")
        ? {
            label: "Open",
            onClick: () => window.location.assign(n.url as string),
          }
        : undefined,
  }
  if (n.level === "error") toast.error(n.title, opts)
  else if (n.level === "success") toast.success(n.title, opts)
  else toast.info(n.title, opts)
}

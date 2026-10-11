import { ChevronLeft, WifiOff } from "lucide-react"
import type * as React from "react"

import { Orb } from "@/components/ui/orb"
import { cn } from "@/lib/utils"

// The simple app's chrome: a centred title, an optional connection line,
// cards on the theme's own surfaces, and a floating tab bar. Every control is
// a tap target of at least 44px, and nothing depends on hover.

export function Screen({
  title,
  onBack,
  trailing,
  status,
  children,
  className,
}: {
  title: React.ReactNode
  onBack?: () => void
  trailing?: React.ReactNode
  status?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex min-h-0 flex-1 flex-col", className)}>
      <header className="grid flex-none grid-cols-[1fr_auto_1fr] items-center px-2 pt-2 pb-1">
        <div className="flex min-w-0 items-center">
          {onBack && (
            <button
              type="button"
              onClick={onBack}
              aria-label="Back"
              className="flex h-11 items-center pr-2 text-muted-foreground active:text-foreground"
            >
              <ChevronLeft className="size-6 shrink-0" />
            </button>
          )}
        </div>
        <h1 className="max-w-[60vw] truncate px-1 text-center font-semibold text-[19px] text-foreground tracking-tight">
          {title}
        </h1>
        <div className="flex items-center justify-end">{trailing}</div>
      </header>
      {status}
      {children}
    </div>
  )
}

// What a screen says when lasso, or some of the machines it drives, is not
// answering. A line, not a page: the last good listing stays on screen.
export function ConnectionLine({
  error,
  unlisted,
  onRetry,
}: {
  error: Error | null
  unlisted: string[]
  onRetry: () => void
}) {
  const offline = typeof navigator !== "undefined" && !navigator.onLine
  if (!error && !offline && unlisted.length === 0) return null
  const text =
    error || offline
      ? "lasso is not answering"
      : unlisted.length === 1
        ? `${unlisted[0]} is not answering`
        : `${unlisted.length} machines are not answering`
  return (
    <div className="flex flex-none items-center gap-2.5 px-5 py-2">
      <WifiOff className="size-5 shrink-0 text-muted-foreground" />
      <span className="min-w-0 flex-1 truncate text-[15px] text-muted-foreground">
        {text}
      </span>
      <button
        type="button"
        onClick={onRetry}
        className="h-9 shrink-0 px-1 font-semibold text-[15px] text-foreground"
      >
        Retry
      </button>
    </div>
  )
}

// The scrolling body of a screen, padded clear of the floating tab bar.
export function ScrollBody({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "min-h-0 flex-1 overflow-y-auto overscroll-contain px-4 pb-[calc(env(safe-area-inset-bottom)+104px)]",
        className
      )}
    >
      {children}
    </div>
  )
}

export function Card({
  children,
  className,
}: {
  children: React.ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        "rounded-[22px] border border-border/70 bg-card text-card-foreground",
        className
      )}
    >
      {children}
    </div>
  )
}

export function GroupHeader({ children }: { children: React.ReactNode }) {
  return (
    <div className="truncate px-2 pt-5 pb-2 font-medium text-[14px] text-muted-foreground">
      {children}
    </div>
  )
}

// One status, one dot: blocked is the destructive colour and pulses, working
// is the primary, done is good, the rest is quiet.
export function StatusDot({
  status,
  className,
}: {
  status?: string
  className?: string
}) {
  return (
    <span
      role="img"
      aria-label={status ?? "unknown"}
      className={cn(
        "size-2 shrink-0 rounded-full",
        status === "blocked"
          ? "fx-live bg-destructive"
          : status === "working"
            ? "bg-primary"
            : status === "done"
              ? "bg-good"
              : "bg-muted-foreground/50",
        className
      )}
    />
  )
}

export function EmptyState({
  icon,
  title,
  children,
}: {
  icon?: React.ReactNode
  title: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 px-10 pb-24 text-center">
      {icon && <div className="text-muted-foreground/70">{icon}</div>}
      <div className="font-semibold text-[20px] text-foreground">{title}</div>
      {children && (
        <div className="text-[15px] text-muted-foreground leading-snug">
          {children}
        </div>
      )}
    </div>
  )
}

export interface TabSpec<T extends string> {
  id: T
  label: string
  icon: React.ComponentType<{ className?: string }>
  badge?: boolean
}

export function TabBar<T extends string>({
  tabs,
  current,
  onSelect,
}: {
  tabs: TabSpec<T>[]
  current: T
  onSelect: (id: T) => void
}) {
  return (
    <nav
      aria-label="Views"
      className="fixed inset-x-0 bottom-0 z-30 flex justify-center px-4 pb-[calc(env(safe-area-inset-bottom)+10px)]"
    >
      <div className="flex w-full max-w-md items-stretch gap-1 rounded-full border border-border/70 bg-popover/85 p-1.5 shadow-[0_8px_32px_rgb(0_0_0/0.28)] backdrop-blur-xl">
        {tabs.map(({ id, label, icon: Icon, badge }) => {
          const on = id === current
          return (
            <button
              key={id}
              type="button"
              aria-current={on ? "page" : undefined}
              onClick={() => onSelect(id)}
              className={cn(
                "relative flex h-14 flex-1 flex-col items-center justify-center gap-0.5 rounded-full transition-colors",
                on ? "bg-muted text-foreground" : "text-muted-foreground"
              )}
            >
              <span className="relative">
                <Icon className="size-[22px]" />
                {badge && (
                  <span className="absolute -top-0.5 -right-1.5 size-2.5 rounded-full border-2 border-popover bg-primary" />
                )}
              </span>
              <span className="font-medium text-[11.5px]">{label}</span>
            </button>
          )
        })}
      </div>
    </nav>
  )
}

// The first listing can take a few seconds across many machines.
export function Loading() {
  return (
    <div className="flex flex-1 items-center justify-center pb-24">
      <Orb state="working" px={28} label="Loading" />
    </div>
  )
}

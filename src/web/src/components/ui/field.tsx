import type * as React from "react"

// Native textarea/select styled to match the shadcn <Input> (same border,
// radius, and background) so every field in a form reads as one set. Fields
// use bg-background (not transparent) so they contrast against a dialog's
// bg-popover surface. Keep mobile controls at 16px: iOS zooms the page for a
// smaller field.
export const fieldClass =
  "w-full rounded-lg border border-input bg-background px-2.5 py-1.5 text-base shadow-well outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 md:text-sm"

// The same field on a surface that is already the page's own (the Settings
// tab): transparent, so it sits on the panel rather than punching a hole in it.
export const flatFieldClass =
  "w-full rounded-lg border border-input bg-transparent px-2.5 py-1.5 text-sm shadow-well outline-none transition-colors placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-3 focus-visible:ring-ring/50 dark:bg-input/30"

export const labelClass = "font-medium text-muted-foreground text-xs"

// A label over its control, with an optional one-line hint under it.
export function Field({
  label,
  hint,
  htmlFor,
  children,
}: {
  label: string
  hint?: React.ReactNode
  htmlFor?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1">
      <label className={labelClass} htmlFor={htmlFor}>
        {label}
      </label>
      {children}
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  )
}

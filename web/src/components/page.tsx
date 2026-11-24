import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

/** A section's heading row: title, one line under it, actions on the right. */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
}) {
  return (
    <header className="flex flex-wrap items-end justify-between gap-4">
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="font-heading text-2xl font-semibold">{title}</h1>
        {description !== undefined && (
          <p className="text-sm text-muted-foreground">{description}</p>
        )}
      </div>
      {actions !== undefined && (
        <div className="flex flex-wrap items-stretch gap-2">{actions}</div>
      )}
    </header>
  )
}

/** Vertical rhythm every section page shares. */
export function Page({ children }: { children: ReactNode }) {
  return <div className="flex flex-col gap-5">{children}</div>
}

/** A titled surface. */
export function Panel({
  title,
  children,
  className,
}: {
  title?: string
  children: ReactNode
  className?: string
}) {
  return (
    <section
      className={cn(
        'flex flex-col gap-4 rounded-xl bg-card p-5 text-sm shadow-panel ring-1 ring-foreground/10',
        className,
      )}
    >
      {title !== undefined && (
        <h2 className="font-heading text-base font-semibold">{title}</h2>
      )}
      {children}
    </section>
  )
}

/** What a list shows when there is nothing in it yet. */
export function EmptyState({
  title,
  description,
  children,
  className,
}: {
  title: string
  description: string
  children?: ReactNode
  className?: string
}) {
  return (
    <div
      className={cn(
        'flex flex-col items-center gap-1.5 rounded-xl border border-dashed px-6 py-10 text-center text-sm',
        className,
      )}
    >
      <p className="font-heading text-base font-semibold">{title}</p>
      <p className="max-w-md text-muted-foreground">{description}</p>
      {children !== undefined && (
        <div className="mt-3 flex flex-wrap justify-center gap-2">{children}</div>
      )}
    </div>
  )
}

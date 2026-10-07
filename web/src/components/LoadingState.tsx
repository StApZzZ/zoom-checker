import { Skeleton } from '@/components/ui/skeleton'
import { cn } from '@/lib/utils'

export interface LoadingStateProps {
  /**
   * page  — header + tiles + table (whole page loading)
   * table — table rows
   * cards — grid of cards
   * list  — stacked lines
   * tiles — row of stat tiles
   */
  variant?: 'page' | 'table' | 'cards' | 'list' | 'tiles'
  rows?: number
  /** Screen reader text. */
  label?: string
  className?: string
}

const range = (n: number) => Array.from({ length: n }, (_, i) => i)

function TableSkeleton({ rows }: { rows: number }) {
  return (
    <div className="overflow-hidden rounded-xl ring-1 ring-foreground/10">
      <div className="flex gap-4 border-b bg-muted/40 px-4 py-3">
        <Skeleton className="h-4 w-1/3" />
        <Skeleton className="h-4 w-1/6" />
        <Skeleton className="ml-auto h-4 w-1/6" />
      </div>
      {range(rows).map((i) => (
        <div key={i} className="flex items-center gap-4 border-b px-4 py-3 last:border-b-0">
          <Skeleton className="h-4 w-2/5" />
          <Skeleton className="h-5 w-20 rounded-full" />
          <Skeleton className="ml-auto h-4 w-12" />
        </div>
      ))}
    </div>
  )
}

function TilesSkeleton({ count }: { count: number }) {
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
      {range(count).map((i) => (
        <div key={i} className="flex flex-col gap-3 rounded-xl p-4 ring-1 ring-foreground/10">
          <Skeleton className="h-4 w-24" />
          <Skeleton className="h-8 w-12" />
        </div>
      ))}
    </div>
  )
}

/** Skeleton placeholders with an accessible "Загрузка…" status. */
export function LoadingState({ variant = 'table', rows = 5, label = 'Загрузка…', className }: LoadingStateProps) {
  let body
  switch (variant) {
    case 'page':
      body = (
        <div className="flex flex-col gap-6">
          <div className="flex flex-col gap-2">
            <Skeleton className="h-8 w-64 max-w-full" />
            <Skeleton className="h-4 w-96 max-w-full" />
          </div>
          <TilesSkeleton count={5} />
          <TableSkeleton rows={rows} />
        </div>
      )
      break
    case 'cards':
      body = (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {range(rows).map((i) => (
            <div key={i} className="flex flex-col gap-3 rounded-xl p-4 ring-1 ring-foreground/10">
              <Skeleton className="h-5 w-2/3" />
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-4 w-1/3" />
            </div>
          ))}
        </div>
      )
      break
    case 'list':
      body = (
        <div className="flex flex-col gap-3">
          {range(rows).map((i) => (
            <Skeleton key={i} className="h-5" style={{ width: `${90 - (i % 3) * 15}%` }} />
          ))}
        </div>
      )
      break
    case 'tiles':
      body = <TilesSkeleton count={rows} />
      break
    default:
      body = <TableSkeleton rows={rows} />
  }
  return (
    <div role="status" aria-live="polite" aria-busy="true" className={cn('w-full', className)}>
      <span className="sr-only">{label}</span>
      <div aria-hidden="true">{body}</div>
    </div>
  )
}

/** Centered spinner for whole-screen waits (session check, lazy route loading). */
export function PageSpinner({ label = 'Загрузка…', className }: { label?: string; className?: string }) {
  return (
    <div
      role="status"
      aria-live="polite"
      className={cn('flex min-h-[50svh] w-full flex-col items-center justify-center gap-3 text-muted-foreground', className)}
    >
      <span
        aria-hidden="true"
        className="size-8 animate-spin rounded-full border-2 border-muted-foreground/25 border-t-foreground/70"
      />
      <span className="text-sm">{label}</span>
    </div>
  )
}

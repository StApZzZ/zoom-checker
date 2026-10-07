import type { ReactNode } from 'react'
import type { LucideIcon } from 'lucide-react'

import { Skeleton } from '@/components/ui/skeleton'
import { toneSolidClass, toneTextClass, type Tone } from '@/components/tone'
import { cn } from '@/lib/utils'

export interface StatTileProps {
  label: ReactNode
  value: ReactNode
  /** Colors the number and the accent bar. Default neutral. */
  tone?: Tone
  icon?: LucideIcon
  /** Small text under the label. */
  hint?: ReactNode
  loading?: boolean
  /** Makes the tile a button (e.g. to filter a table). */
  onClick?: () => void
  /** Pressed state when used as a filter toggle. */
  active?: boolean
  className?: string
}

/** Big number + label, used for lesson counters ("Сейчас в Zoom: 21"). */
export function StatTile({
  label,
  value,
  tone = 'neutral',
  icon: Icon,
  hint,
  loading = false,
  onClick,
  active,
  className,
}: StatTileProps) {
  const interactive = typeof onClick === 'function'
  const content = (
    <>
      <span aria-hidden="true" className={cn('absolute inset-y-3 left-0 w-1 rounded-r-full', toneSolidClass[tone])} />
      <span className="flex items-start gap-1.5 text-sm leading-tight text-muted-foreground">
        {Icon && <Icon className={cn('size-4 shrink-0', toneTextClass[tone])} aria-hidden="true" />}
        <span className="min-w-0 text-pretty break-words">{label}</span>
      </span>
      {loading ? (
        <Skeleton className="h-9 w-14" />
      ) : (
        <span className={cn('text-3xl leading-none font-semibold tracking-tight tabular-nums', toneTextClass[tone])}>
          {value}
        </span>
      )}
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </>
  )
  const base = cn(
    'relative flex min-w-0 flex-col gap-2 overflow-hidden rounded-xl bg-card py-4 pr-4 pl-5 text-left text-card-foreground ring-1 ring-foreground/10',
    interactive &&
      'cursor-pointer transition-colors outline-none hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50',
    active && 'bg-muted ring-2 ring-foreground/25',
    className,
  )
  if (interactive) {
    return (
      <button type="button" className={base} onClick={onClick} aria-pressed={active}>
        {content}
      </button>
    )
  }
  return <div className={base}>{content}</div>
}

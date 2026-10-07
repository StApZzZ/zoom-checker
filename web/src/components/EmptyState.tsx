import { isValidElement, type ReactNode } from 'react'
import { Inbox, type LucideIcon } from 'lucide-react'

import { cn } from '@/lib/utils'

export interface EmptyStateProps {
  /** Lucide icon component or any element. */
  icon?: LucideIcon | ReactNode
  title: ReactNode
  /** What to do next, in plain words. */
  text?: ReactNode
  /** Primary action (button / link). */
  action?: ReactNode
  /** compact = for use inside cards and tables. */
  size?: 'default' | 'compact'
  className?: string
}

/** Friendly placeholder for empty lists with a hint and an optional action. */
export function EmptyState({ icon = Inbox, title, text, action, size = 'default', className }: EmptyStateProps) {
  const compact = size === 'compact'
  let iconNode: ReactNode = null
  if (isValidElement(icon)) {
    iconNode = icon
  } else if (icon) {
    const Icon = icon as LucideIcon
    iconNode = <Icon className={compact ? 'size-5' : 'size-6'} aria-hidden="true" />
  }
  return (
    <div
      data-slot="empty-state"
      className={cn(
        'flex flex-col items-center justify-center text-center',
        compact ? 'gap-2 px-4 py-6' : 'gap-3 rounded-xl border border-dashed px-6 py-12',
        className,
      )}
    >
      {iconNode && (
        <div
          className={cn(
            'flex items-center justify-center rounded-full bg-muted text-muted-foreground',
            compact ? 'size-10' : 'size-12',
          )}
        >
          {iconNode}
        </div>
      )}
      <div className="flex max-w-md flex-col gap-1">
        <p className={cn('font-medium text-foreground', compact ? 'text-sm' : 'text-base')}>{title}</p>
        {text && <div className="text-sm text-pretty text-muted-foreground">{text}</div>}
      </div>
      {action && <div className="mt-1 flex flex-wrap items-center justify-center gap-2">{action}</div>}
    </div>
  )
}

import { Hand } from 'lucide-react'

import { finalStatusMeta } from '@/components/status-meta'
import { ToneBadge, type BadgeSize } from '@/components/StatusBadge'
import { cn } from '@/lib/utils'
import type { AttendanceStatus } from '@/lib/types'

export interface FinalStatusBadgeProps {
  status: AttendanceStatus
  /** The status was set by a teacher (manual_status !== null). */
  manual?: boolean
  size?: BadgeSize
  className?: string
}

/** Attendance verdict: "Был" / "Не был", plus a "вручную" marker for manual decisions. */
export function FinalStatusBadge({ status, manual = false, size, className }: FinalStatusBadgeProps) {
  const meta = Object.hasOwn(finalStatusMeta, status) ? finalStatusMeta[status] : undefined
  return (
    <span className={cn('inline-flex items-center gap-1.5', className)}>
      {meta ? (
        <ToneBadge tone={meta.tone} icon={meta.icon} size={size} title={meta.description}>
          {meta.label}
        </ToneBadge>
      ) : (
        // Unknown value from a newer server: show it rather than crash.
        <ToneBadge tone="neutral" size={size}>
          {String(status)}
        </ToneBadge>
      )}
      {manual && <ManualMarker />}
    </span>
  )
}

/** Small "вручную" note next to values a teacher changed by hand. */
export function ManualMarker({ className }: { className?: string }) {
  return (
    <span
      className={cn('inline-flex items-center gap-0.5 text-xs text-muted-foreground', className)}
      title="Отметка поставлена вручную"
    >
      <Hand className="size-3" aria-hidden="true" />
      вручную
    </span>
  )
}

import { useLiveSeconds } from '@/hooks/useLiveSeconds'
import { formatDuration, formatDurationClock, formatDurationLong } from '@/lib/format'
import { cn } from '@/lib/utils'

export interface LiveDurationProps {
  /** Server total at `asOf`. */
  seconds: number
  /** LessonView.as_of of the snapshot the total belongs to. */
  asOf: string | null | undefined
  /** Keeps counting while true (current_status === 'online'). */
  online: boolean
  /** clock = "01:14" (default), seconds = "01:14:05", long = "1 ч 14 мин". */
  format?: 'clock' | 'seconds' | 'long'
  className?: string
}

const formatters = {
  clock: formatDuration,
  seconds: formatDurationClock,
  long: formatDurationLong,
}

/** Presence time that ticks every second while the person is in Zoom. */
export function LiveDuration({ seconds, asOf, online, format = 'clock', className }: LiveDurationProps) {
  const total = useLiveSeconds(seconds, asOf, online)
  return (
    <span
      role="timer"
      aria-label={formatDurationLong(total)}
      className={cn('tabular-nums', online && 'font-medium', className)}
    >
      {formatters[format](total)}
    </span>
  )
}

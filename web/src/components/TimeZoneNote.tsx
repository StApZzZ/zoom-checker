// "Время — по вашему часовому поясу (UTC+5)…": shown only when the browser's zone
// differs from the server's TIMEZONE. The screen and downloaded files (exports get the
// browser's zone as `tz`) use the browser's zone; server messages and the audit log
// use TIMEZONE and name its offset, e.g. "18:00 (UTC+3)".

import { useState } from 'react'
import { Globe } from 'lucide-react'

import { browserTimeZone, utcOffsetLabel, zonesDiffer, type DateInput } from '@/lib/format'
import { useAuthConfig } from '@/lib/queries'
import { cn } from '@/lib/utils'

interface ZoneDifference {
  /** "UTC+5": the zone the screen and the date/time fields use. */
  browser: string
  /** "UTC+3": the server's TIMEZONE. */
  server: string
}

/**
 * The two offsets when the zones differ at `at` (default: when the note appeared; offsets
 * only change at daylight-saving switches); null when they agree or are unknown.
 */
function useZoneDifference(at?: DateInput): ZoneDifference | null {
  const serverZone = useAuthConfig().data?.timezone
  const [shownAt] = useState(() => Date.now())
  const when = at ?? shownAt
  if (!serverZone || !zonesDiffer(serverZone, when)) return null
  return { browser: utcOffsetLabel(browserTimeZone(), when), server: utcOffsetLabel(serverZone, when) }
}

export interface TimeZoneNoteProps {
  /** field: under date/time inputs; page: the app-wide line at the bottom of every page. */
  variant?: 'field' | 'page'
  /** The moment the times are about (the lesson date): daylight saving can change the offset. */
  at?: DateInput
  id?: string
  className?: string
}

export function TimeZoneNote({ variant = 'field', at, id, className }: TimeZoneNoteProps) {
  const diff = useZoneDifference(at)
  if (!diff) return null
  return (
    <p id={id} className={cn('flex items-start gap-1.5 text-xs text-muted-foreground', className)}>
      <Globe className="mt-px size-3.5 shrink-0" aria-hidden="true" />
      <span>
        {variant === 'field'
          ? `Время указывается по вашему часовому поясу (${diff.browser}), а не по времени сервиса (${diff.server}).`
          : `Время на экране и в скачанных файлах Excel — по вашему часовому поясу (${diff.browser}). Сервис работает по времени ${diff.server}: в его сообщениях время указано с поясом, например «(${diff.server})».`}
      </span>
    </p>
  )
}

// Helpers for the admin webhook journal ("Журнал событий").

import { zoomEventTypeLabels } from '@/components/status-meta'
import { EMPTY, formatDate, formatTimeWithSeconds } from '@/lib/format'
import { EVENT_PROCESSING_STATUSES, type EventProcessingStatus, type ISODateTime } from '@/lib/types'

/** "06.10.2026 18:02:07" — seconds matter in the journal. */
export function formatEventTime(value: ISODateTime | null | undefined): string {
  if (!value) return EMPTY
  return `${formatDate(value)} ${formatTimeWithSeconds(value)}`
}

/** Event types offered in the type filter (the ones the service knows). */
export const EVENT_TYPE_OPTIONS: readonly string[] = Object.keys(zoomEventTypeLabels)

/** Status from the URL (?status=failed); unknown values mean "all". */
export function parseStatusParam(value: string | null): EventProcessingStatus | undefined {
  return value && (EVENT_PROCESSING_STATUSES as readonly string[]).includes(value)
    ? (value as EventProcessingStatus)
    : undefined
}

/** Type from the URL (?type=meeting.ended); only known types are applied. */
export function parseTypeParam(value: string | null): string | undefined {
  return value && EVENT_TYPE_OPTIONS.includes(value) ? value : undefined
}

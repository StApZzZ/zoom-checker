import { useEffect, useState } from 'react'

import { ApiError, errorMessage } from '@/lib/api'
import { useZoomParse } from '@/lib/queries'

/** Value that follows `value` after it stopped changing for `delayMs`. */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(t)
  }, [value, delayMs])
  return debounced
}

export type ZoomCheck =
  | { state: 'empty' }
  | { state: 'checking' }
  | { state: 'ok'; meetingId: string; formatted: string }
  /** The server says there is no meeting number in the input (422). */
  | { state: 'invalid'; message: string }
  /** The check itself failed (network, 5xx): the server validates again on save. */
  | { state: 'unknown' }

/** How long the teacher has to stop typing before the link is checked. */
export const ZOOM_CHECK_DELAY_MS = 300

/**
 * Live "is this a Zoom link?" check: debounced call of GET /api/zoom/parse.
 * Results are cached per input, so pasting the same link again is instant.
 */
export function useZoomCheck(input: string, delayMs: number = ZOOM_CHECK_DELAY_MS): ZoomCheck {
  const trimmed = input.trim()
  const debounced = useDebouncedValue(trimmed, delayMs)
  const query = useZoomParse(debounced)
  if (!trimmed) return { state: 'empty' }
  if (debounced !== trimmed) return { state: 'checking' }
  if (query.data) {
    return {
      state: 'ok',
      meetingId: query.data.meeting_id,
      formatted: query.data.formatted || query.data.meeting_id,
    }
  }
  if (query.isError) {
    const err = query.error
    if (err instanceof ApiError && (err.isValidation || err.status === 400)) {
      return { state: 'invalid', message: errorMessage(err) }
    }
    return { state: 'unknown' }
  }
  return { state: 'checking' }
}

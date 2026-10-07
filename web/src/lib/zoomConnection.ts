// Whether Zoom is connected (GET /api/zoom/status), shared by the Zoom page and the
// warnings elsewhere (app shell, lesson list, waiting lesson) so they never disagree.

import { useNow } from '@/hooks/useNow'
import { toDate } from '@/lib/format'
import { useZoomStatus } from '@/lib/queries'
import type { ZoomStatus } from '@/lib/types'

export type ZoomConnectionState = 'ok' | 'waiting' | 'stale' | 'not_configured'

/** Events newer than this count as "Zoom присылает события". */
export const RECENT_EVENT_MS = 24 * 60 * 60 * 1000

/**
 * not_configured: no Secret Token, every webhook is refused; waiting: the secret is set
 * but no event ever arrived; stale: the last event is older than a day; ok otherwise.
 */
export function zoomConnectionState(
  status: Pick<ZoomStatus, 'secret_configured' | 'last_event_at'>,
  now: number,
): ZoomConnectionState {
  if (!status.secret_configured) return 'not_configured'
  const last = toDate(status.last_event_at)
  if (!last) return 'waiting'
  return now - last.getTime() <= RECENT_EVENT_MS ? 'ok' : 'stale'
}

/** Warnings outside the Zoom page re-check rarely: setup changes need a restart anyway. */
const WARNING_REFRESH_MS = 5 * 60_000

/**
 * The connection state for a warning banner, or null while unknown (loading, failed,
 * disabled): a banner must never guess, so unknown means "show nothing / keep the
 * usual wording". Shares the Zoom page's cache entry.
 */
export function useZoomConnection(enabled = true): ZoomConnectionState | null {
  const status = useZoomStatus({ enabled, refetchInterval: enabled ? WARNING_REFRESH_MS : false, retry: false })
  const now = useNow(enabled ? 60_000 : null)
  return enabled && status.data ? zoomConnectionState(status.data, now) : null
}

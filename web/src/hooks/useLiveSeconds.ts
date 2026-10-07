import { useEffect } from 'react'

import { useNow } from '@/hooks/useNow'
import { markReceived, receivedAt } from '@/lib/liveClock'

/**
 * Live total for a duration that keeps growing while the person is online.
 *
 * `totalSeconds` is the server value computed at `asOf` (LessonView.as_of).
 * While `isOnline`, the hook ticks every second and returns
 * `totalSeconds + (clientNow − receivedAt(asOf))`, where receivedAt is the
 * client time the snapshot arrived, moved back by the snapshot's age on
 * arrival when it was clearly computed earlier (see liveClock.ts). The receipt
 * is recorded by the query/SSE layer via markReceived(); for a snapshot that
 * bypassed it, the moment this hook first saw `asOf` is used. The client clock
 * is never compared with as_of directly, so clock skew cannot distort the count.
 */
export function useLiveSeconds(
  totalSeconds: number,
  asOf: string | null | undefined,
  isOnline: boolean,
): number {
  const live = isOnline && !!asOf
  const now = useNow(live ? 1000 : null)
  useEffect(() => {
    // No-op when the data layer already recorded this snapshot (first receipt wins).
    markReceived(asOf)
  }, [asOf])
  const safeTotal = Number.isFinite(totalSeconds) ? Math.max(0, totalSeconds) : 0
  if (!live) return safeTotal
  const base = receivedAt(asOf) ?? now
  return safeTotal + Math.max(0, Math.floor((now - base) / 1000))
}

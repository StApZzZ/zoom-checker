// Connection state shown in the "Подключение к Zoom" card (GET /api/zoom/status).

import { CircleCheck, Clock, Hourglass, KeyRound, type LucideIcon } from 'lucide-react'

import type { Tone } from '@/components/tone'
import type { ZoomStatus } from '@/lib/types'
import type { ZoomConnectionState } from '@/lib/zoomConnection'

export { RECENT_EVENT_MS, zoomConnectionState, type ZoomConnectionState } from '@/lib/zoomConnection'

export interface ZoomConnectionMeta {
  label: string
  /** Shorter wording for teachers (no configuration jargon). */
  teacherLabel: string
  tone: Tone
  icon: LucideIcon
}

export const zoomConnectionMeta: Record<ZoomConnectionState, ZoomConnectionMeta> = {
  ok: { label: 'Zoom присылает события', teacherLabel: 'Zoom присылает события', tone: 'success', icon: CircleCheck },
  waiting: { label: 'Событий пока не было', teacherLabel: 'Событий пока не было', tone: 'warning', icon: Hourglass },
  stale: {
    label: 'Событий не было больше суток',
    teacherLabel: 'Событий не было больше суток',
    tone: 'warning',
    icon: Clock,
  },
  not_configured: { label: 'Секрет не настроен', teacherLabel: 'Подключение не настроено', tone: 'danger', icon: KeyRound },
}

/** Clock drift (either way) above this is worth an admin's attention. */
export const CLOCK_SKEW_WARN_SECONDS = 120
/** A refused webhook stays on the card for this long. */
export const REJECTED_RECENT_MS = 24 * 60 * 60_000

export interface ZoomClockProblem {
  /** Whole minutes between the server clock and Zoom's; null when only a refusal is known. */
  skewMinutes: number | null
  /** True when the server clock is behind Zoom's. */
  serverBehind: boolean
  /** The last webhook refused for its time, when recent. */
  rejectedAt: string | null
}

/**
 * Server clock problems seen on Zoom's webhooks: a drift over 2 minutes skews live
 * durations, and a larger one makes the server refuse (and lose) Zoom's events.
 */
export function zoomClockProblem(
  status: Pick<ZoomStatus, 'clock_skew_seconds' | 'last_rejected_at'>,
  now: number,
): ZoomClockProblem | null {
  const skew = status.clock_skew_seconds
  const drift = typeof skew === 'number' && Number.isFinite(skew) && Math.abs(skew) > CLOCK_SKEW_WARN_SECONDS
  const rejected = status.last_rejected_at ? Date.parse(status.last_rejected_at) : NaN
  const recentRejection = !Number.isNaN(rejected) && now - rejected < REJECTED_RECENT_MS
  if (!drift && !recentRejection) return null
  return {
    skewMinutes: drift ? Math.round(Math.abs(skew) / 60) : null,
    serverBehind: drift && skew > 0,
    rejectedAt: recentRejection ? status.last_rejected_at! : null,
  }
}

/**
 * True for an address whose host is a public IP address: Zoom only accepts an
 * endpoint with a domain name (private and loopback IPs are isLocalWebhookUrl).
 */
export function isIpWebhookUrl(url: string): boolean {
  let host: string
  try {
    host = new URL(url.trim()).hostname
  } catch {
    return false
  }
  return host.startsWith('[') || /^\d{1,3}(\.\d{1,3}){3}$/.test(host)
}

/** Zoom only delivers webhooks to https:// endpoints. */
export function isInsecureWebhookUrl(url: string): boolean {
  return /^http:\/\//i.test(url.trim())
}

/**
 * True when the address only works inside the local network (localhost, a
 * private IP, a name without a dot or with an intranet suffix): Zoom calls the
 * webhook from the internet and can never reach it. Typical cause: PUBLIC_URL
 * was left at its default http://localhost:8080.
 */
export function isLocalWebhookUrl(url: string): boolean {
  let host: string
  try {
    host = new URL(url.trim()).hostname.toLowerCase()
  } catch {
    return false
  }
  if (!host) return false
  if (host.startsWith('[')) {
    // IPv6: loopback, unique local (fc00::/7) and link-local (fe80::/10).
    const v6 = host.slice(1, -1)
    return v6 === '::1' || /^f[cd][0-9a-f]{0,2}:/.test(v6) || /^fe[89ab][0-9a-f]?:/.test(v6)
  }
  const v4 = host.match(/^(\d{1,3})\.(\d{1,3})\.\d{1,3}\.\d{1,3}$/)
  if (v4) {
    const [a, b] = [Number(v4[1]), Number(v4[2])]
    return a === 10 || a === 127 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 169 && b === 254)
  }
  if (!host.includes('.')) return true
  return /\.(localhost|local|lan|internal|intranet|home|corp)$/.test(host)
}

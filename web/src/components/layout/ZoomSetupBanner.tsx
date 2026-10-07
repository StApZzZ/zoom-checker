// Admin-only warning above every page while Zoom cannot deliver attendance events:
// otherwise a fresh install looks ready and lessons silently record nobody.

import { ArrowRight, Hourglass, KeyRound, type LucideIcon } from 'lucide-react'
import { Link } from 'react-router'

import { toneBadgeClass, type Tone } from '@/components/tone'
import { cn } from '@/lib/utils'
import { useZoomConnection, type ZoomConnectionState } from '@/lib/zoomConnection'

interface BannerContent {
  tone: Tone
  icon: LucideIcon
  text: string
}

const CONTENT: Partial<Record<ZoomConnectionState, BannerContent>> = {
  not_configured: {
    tone: 'danger',
    icon: KeyRound,
    text: 'Zoom ещё не подключён — посещаемость не будет отмечаться.',
  },
  waiting: {
    tone: 'warning',
    icon: Hourglass,
    text: 'Из Zoom пока не пришло ни одного события. Проверьте подключение в разделе «Zoom».',
  },
}

/** Shown to admins outside /zoom while the secret is missing or no event ever arrived. */
export function ZoomSetupBanner({ enabled }: { enabled: boolean }) {
  const state = useZoomConnection(enabled)
  const content = state ? CONTENT[state] : undefined
  if (!enabled || !content) return null
  const Icon = content.icon
  return (
    <div className="mx-auto w-full max-w-6xl px-4 pt-4 sm:px-6">
      <section
        aria-label="Подключение к Zoom"
        data-tone={content.tone}
        className={cn(
          'flex flex-col gap-2 rounded-xl px-4 py-3 ring-1 ring-inset sm:flex-row sm:items-center sm:justify-between',
          toneBadgeClass[content.tone],
        )}
      >
        <p className="flex items-start gap-2.5 text-sm font-medium">
          <Icon className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          {content.text}
        </p>
        <Link
          to="/zoom"
          className="inline-flex shrink-0 items-center gap-1 self-start rounded-sm text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-3 focus-visible:ring-ring/50 sm:self-auto"
        >
          Открыть раздел «Zoom»
          <ArrowRight className="size-4" aria-hidden="true" />
        </Link>
      </section>
    </div>
  )
}

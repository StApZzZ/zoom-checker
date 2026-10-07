import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import type { LucideIcon } from 'lucide-react'

import {
  currentStatusMeta,
  eventStatusMeta,
  importRowStatusMeta,
  lessonStatusMeta,
  resolutionMeta,
  streamStateMeta,
  type StatusMeta,
} from '@/components/status-meta'
import { toneBadgeClass, toneSolidClass, type Tone } from '@/components/tone'
import type { StreamState } from '@/lib/sse'
import type { CurrentStatus, EventProcessingStatus, ImportRowStatus, LessonStatus, Resolution } from '@/lib/types'

export type BadgeSize = 'sm' | 'md' | 'lg'

const sizeClass: Record<BadgeSize, string> = {
  sm: 'h-5 gap-1 px-1.5 text-xs [&_svg]:size-3',
  md: 'h-6 gap-1.5 px-2 text-xs [&_svg]:size-3.5',
  lg: 'h-8 gap-2 px-3 text-sm [&_svg]:size-4',
}

const dotSize: Record<BadgeSize, string> = {
  sm: 'size-1.5',
  md: 'size-2',
  lg: 'size-2.5',
}

export interface ToneBadgeProps {
  tone: Tone
  icon?: LucideIcon
  /** Pulsing dot instead of the icon (live states). */
  pulse?: boolean
  size?: BadgeSize
  /** Tooltip / accessible description. */
  title?: string
  className?: string
  children: ReactNode
}

/** Icon + text + tone badge. Building block for all status badges. */
export function ToneBadge({ tone, icon: Icon, pulse, size = 'md', title, className, children }: ToneBadgeProps) {
  return (
    <span
      data-slot="status-badge"
      data-tone={tone}
      title={title}
      className={cn(
        'inline-flex w-fit shrink-0 items-center rounded-full font-medium whitespace-nowrap ring-1 ring-inset',
        sizeClass[size],
        toneBadgeClass[tone],
        className,
      )}
    >
      {pulse ? (
        <span className="relative flex shrink-0" aria-hidden="true">
          <span
            className={cn('absolute inline-flex size-full rounded-full opacity-60 motion-safe:animate-ping', toneSolidClass[tone])}
          />
          <span className={cn('relative inline-flex rounded-full', dotSize[size], toneSolidClass[tone])} />
        </span>
      ) : Icon ? (
        <Icon aria-hidden="true" />
      ) : null}
      <span>{children}</span>
    </span>
  )
}

type StatusBadgeVariant =
  | { kind: 'current'; status: CurrentStatus }
  | { kind: 'resolution'; status: Resolution }
  | { kind: 'lesson'; status: LessonStatus }
  | { kind: 'event'; status: EventProcessingStatus }
  | { kind: 'stream'; status: StreamState }
  | { kind: 'import'; status: ImportRowStatus }

export type StatusBadgeProps = StatusBadgeVariant & {
  size?: BadgeSize
  /** Overrides the default label (e.g. "Не был" in compact tables). */
  label?: string
  className?: string
}

/** Own-key lookup: an unexpected server value must never resolve to an Object.prototype member. */
function pick<K extends string>(table: Record<K, StatusMeta>, key: string): StatusMeta | undefined {
  return Object.hasOwn(table, key) ? table[key as K] : undefined
}

function metaFor(v: StatusBadgeVariant): StatusMeta | undefined {
  switch (v.kind) {
    case 'current':
      return pick(currentStatusMeta, v.status)
    case 'resolution':
      return pick(resolutionMeta, v.status)
    case 'lesson':
      return pick(lessonStatusMeta, v.status)
    case 'event':
      return pick(eventStatusMeta, v.status)
    case 'stream':
      return pick(streamStateMeta, v.status)
    case 'import':
      return pick(importRowStatusMeta, v.status)
  }
}

/**
 * Status of a student in Zoom (`current`), of a Zoom participant name
 * (`resolution`), of a lesson (`lesson`), of a webhook event (`event`) or of
 * the live update connection (`stream`, from useLiveLessonView().connection) or
 * of a row in the import preview (`import`).
 */
export function StatusBadge(props: StatusBadgeProps) {
  const { size, label, className } = props
  const meta = metaFor(props)
  if (!meta) {
    // Unknown value from a newer server: show it rather than crash.
    return (
      <ToneBadge tone="neutral" size={size} className={className}>
        {label ?? String(props.status)}
      </ToneBadge>
    )
  }
  return (
    <ToneBadge
      tone={meta.tone}
      icon={meta.icon}
      pulse={meta.pulse}
      size={size}
      title={meta.description}
      className={className}
    >
      {label ?? meta.label}
    </ToneBadge>
  )
}

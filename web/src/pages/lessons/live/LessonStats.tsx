import { CircleSlash, UserCheck, UserSearch, Users, Wifi } from 'lucide-react'

import { StatTile } from '@/components/StatTile'
import type { Counts, LessonStatus } from '@/lib/types'

export interface LessonStatsProps {
  counts: Counts
  lessonStatus: LessonStatus
  /** Scrolls to the "Не идентифицированы" block. */
  onShowUnidentified: () => void
}

/** The five live counters of TZ §14. */
export function LessonStats({ counts, lessonStatus, onShowUnidentified }: LessonStatsProps) {
  const started = lessonStatus !== 'planned'
  const unidentified = counts.unidentified > 0
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5" aria-label="Сводка по занятию" role="group">
      <StatTile label="В группе" value={counts.in_group} icon={Users} />
      <StatTile label="Сейчас в Zoom" value={counts.online} tone="success" icon={Wifi} />
      <StatTile label="Были хотя бы раз" value={counts.seen} tone="info" icon={UserCheck} />
      <StatTile
        label="Не подключались"
        value={counts.not_seen}
        tone={started && counts.not_seen > 0 ? 'danger' : 'neutral'}
        icon={CircleSlash}
      />
      <StatTile
        label="Не идентифицированы"
        value={counts.unidentified}
        tone={unidentified ? 'warning' : 'neutral'}
        icon={UserSearch}
        hint={unidentified ? 'Нажмите, чтобы разобраться' : undefined}
        onClick={unidentified ? onShowUnidentified : undefined}
        className={
          unidentified
            ? 'col-span-2 bg-amber-50/70 ring-2 ring-amber-500/50 sm:col-span-1 dark:bg-amber-400/10 dark:ring-amber-400/40'
            : 'col-span-2 sm:col-span-1'
        }
      />
    </div>
  )
}

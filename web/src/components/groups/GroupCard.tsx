import { Link } from 'react-router'
import { Archive, CalendarClock, CalendarX2, History, Users } from 'lucide-react'

import { ToneBadge } from '@/components/StatusBadge'
import { formatTime, formatWeekdayDate, pluralize, WORDS } from '@/lib/format'
import type { GroupSummary } from '@/lib/types'

function LessonLine({ group }: { group: GroupSummary }) {
  if (group.next_lesson_at)
    return (
      <span className="flex items-center gap-2">
        <CalendarClock className="size-4 shrink-0" aria-hidden="true" />
        <span>
          Следующее занятие:{' '}
          <span className="text-foreground">
            {formatWeekdayDate(group.next_lesson_at)}, {formatTime(group.next_lesson_at)}
          </span>
        </span>
      </span>
    )
  if (group.last_lesson_at)
    return (
      <span className="flex items-center gap-2">
        <History className="size-4 shrink-0" aria-hidden="true" />
        <span>
          Последнее занятие: <span className="text-foreground">{formatWeekdayDate(group.last_lesson_at)}</span>
        </span>
      </span>
    )
  return (
    <span className="flex items-center gap-2">
      <CalendarX2 className="size-4 shrink-0" aria-hidden="true" />
      Занятий пока нет
    </span>
  )
}

/** Clickable card of a group in the groups list. */
export function GroupCard({ group }: { group: GroupSummary }) {
  const archived = group.status === 'archived'
  return (
    <Link
      to={`/groups/${group.id}`}
      className="group flex h-full flex-col gap-3 rounded-xl bg-card p-5 text-card-foreground ring-1 ring-foreground/10 transition-colors outline-none hover:bg-muted/40 hover:ring-foreground/20 focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <div className="flex items-start justify-between gap-3">
        <h2 className="font-heading text-lg leading-snug font-semibold break-words group-hover:underline group-hover:underline-offset-4">
          {group.name}
        </h2>
        {archived && (
          <ToneBadge tone="muted" icon={Archive} title="Группа в архиве">
            В архиве
          </ToneBadge>
        )}
      </div>
      {group.description && <p className="line-clamp-2 text-sm text-pretty text-muted-foreground">{group.description}</p>}
      <div className="mt-auto flex flex-col gap-1.5 pt-1 text-sm text-muted-foreground">
        <span className="flex items-center gap-2">
          <Users className="size-4 shrink-0" aria-hidden="true" />
          <span className="font-medium text-foreground">{pluralize(group.student_count, WORDS.student)}</span>
        </span>
        <LessonLine group={group} />
      </div>
    </Link>
  )
}

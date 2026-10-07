import { lazy, Suspense, useMemo, useState } from 'react'
import { Link } from 'react-router'
import { CalendarPlus, CalendarX2, ChevronRight } from 'lucide-react'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { formatTimeRange, formatWeekdayDate } from '@/lib/format'
import { useLessons } from '@/lib/queries'
import { useGlobalStream } from '@/lib/sse'
import type { LessonSummary } from '@/lib/types'

// The dialog belongs to the lessons area; load it only when it is needed.
const CreateLessonDialog = lazy(() =>
  import('@/components/lessons/CreateLessonDialog').then((m) => ({ default: m.CreateLessonDialog })),
)

/** Newest first by planned start. */
function sortLessonsNewestFirst(lessons: readonly LessonSummary[]): LessonSummary[] {
  return lessons.slice().sort((a, b) => Date.parse(b.planned_start_at) - Date.parse(a.planned_start_at) || b.id - a.id)
}

function LessonCounts({ lesson }: { lesson: LessonSummary }) {
  const { counts } = lesson
  if (lesson.status === 'active')
    return (
      <span>
        Сейчас в Zoom: <span className="font-medium text-foreground tabular-nums">{counts.online}</span> из{' '}
        <span className="tabular-nums">{counts.in_group}</span>
      </span>
    )
  if (lesson.status === 'finished')
    return (
      <span>
        Были: <span className="font-medium text-foreground tabular-nums">{counts.present}</span> из{' '}
        <span className="tabular-nums">{counts.in_group}</span>
      </span>
    )
  return null
}

/** "Занятия группы": the group's lessons, newest first, with status and attendance. */
export function GroupLessonsPanel({ groupId }: { groupId: number }) {
  // No placeholder: another group's lessons must not flash here while this group's load.
  const lessons = useLessons({ group_id: groupId }, { placeholderData: undefined })
  // "Сейчас в Zoom" of a running lesson changes live (the tab is unmounted when hidden).
  useGlobalStream()
  const [createOpen, setCreateOpen] = useState(false)
  const [dialogLoaded, setDialogLoaded] = useState(false)
  const sorted = useMemo(() => (lessons.data ? sortLessonsNewestFirst(lessons.data) : null), [lessons.data])

  const openCreate = () => {
    setDialogLoaded(true)
    setCreateOpen(true)
  }

  const newLessonButton = (
    <Button type="button" size="lg" onClick={openCreate}>
      <CalendarPlus aria-hidden="true" />
      Новое занятие
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="text-sm text-muted-foreground">Все занятия этой группы — сначала самые новые.</p>
        {newLessonButton}
      </div>

      {sorted ? (
        sorted.length === 0 ? (
          <EmptyState
            icon={CalendarX2}
            title="Занятий пока нет"
            text="Создайте первое занятие: укажите дату, время и ссылку на Zoom. Во время урока посещаемость будет отмечаться сама."
            action={newLessonButton}
          />
        ) : (
          <ul className="flex flex-col divide-y overflow-hidden rounded-xl ring-1 ring-foreground/10" aria-label="Занятия группы">
            {sorted.map((lesson) => (
              <li key={lesson.id}>
                <Link
                  to={`/lessons/${lesson.id}`}
                  className="group flex flex-col gap-2 px-4 py-3 outline-none hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset sm:flex-row sm:items-center sm:gap-4"
                >
                  <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                    <span className="truncate font-medium">{lesson.title}</span>
                    <span className="text-sm text-muted-foreground first-letter:uppercase">
                      {formatWeekdayDate(lesson.planned_start_at)}, {formatTimeRange(lesson.planned_start_at, lesson.planned_end_at)}
                    </span>
                  </div>
                  <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
                    <LessonCounts lesson={lesson} />
                    <StatusBadge kind="lesson" status={lesson.status} />
                    <ChevronRight
                      className="hidden size-4 text-muted-foreground transition-transform group-hover:translate-x-0.5 sm:block"
                      aria-hidden="true"
                    />
                  </div>
                </Link>
              </li>
            ))}
          </ul>
        )
      ) : lessons.isError ? (
        <ErrorState error={lessons.error} onRetry={() => lessons.refetch()} retrying={lessons.isFetching} />
      ) : (
        <LoadingState variant="list" rows={4} />
      )}

      {dialogLoaded && (
        <Suspense fallback={null}>
          <CreateLessonDialog open={createOpen} onOpenChange={setCreateOpen} groupId={groupId} />
        </Suspense>
      )}
    </div>
  )
}

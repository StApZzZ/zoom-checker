// "/lessons/:id" — the live lesson screen (TZ §14, docs/architecture.md §7).

import { useCallback, useMemo, useRef, useState } from 'react'
import { SearchX } from 'lucide-react'
import { Link, useParams } from 'react-router'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { LoadingState } from '@/components/LoadingState'
import { Button } from '@/components/ui/button'
import { useNow } from '@/hooks/useNow'
import { ApiError } from '@/lib/api'
import { useLiveLessonView } from '@/lib/sse'
import { FinishedBanner, WaitingBanner } from '@/pages/lessons/live/LessonBanners'
import { LessonSessionsCard, OverdueBanner } from '@/pages/lessons/live/LessonSessions'
import { LessonHeader } from '@/pages/lessons/live/LessonHeader'
import { LessonStats } from '@/pages/lessons/live/LessonStats'
import { groupParticipants } from '@/pages/lessons/live/liveText'
import { HiddenParticipants, UnidentifiedBlock } from '@/pages/lessons/live/ParticipantsPanel'
import { StudentSheet } from '@/pages/lessons/live/StudentSheet'
import { StudentsTable } from '@/pages/lessons/live/StudentsTable'

function parseId(raw: string | undefined): number | undefined {
  if (!raw || !/^\d{1,15}$/.test(raw)) return undefined
  const n = Number(raw)
  return n > 0 ? n : undefined
}

function LessonNotFound({ deleted = false }: { deleted?: boolean }) {
  return (
    <Page>
      <PageHeader title={deleted ? 'Занятие удалено' : 'Занятие не найдено'} back={{ to: '/', label: 'Все занятия' }} />
      <EmptyState
        icon={SearchX}
        title={deleted ? 'Это занятие только что удалили' : 'Такого занятия нет'}
        text={
          deleted
            ? 'Возможно, его удалил коллега или вы сами в другой вкладке. Найдите нужное занятие в общем списке.'
            : 'Возможно, его удалили или ссылка неполная. Найдите занятие в общем списке.'
        }
        action={
          <Button asChild>
            <Link to="/">Ко всем занятиям</Link>
          </Button>
        }
      />
    </Page>
  )
}

export default function LessonPage() {
  const params = useParams()
  const lessonId = parseId(params.id)
  if (lessonId === undefined) return <LessonNotFound />
  // Another lesson (history back/forward reuses this route) starts with fresh
  // filters, search, open card and dialogs.
  return <LessonScreen key={lessonId} lessonId={lessonId} />
}

function LessonScreen({ lessonId }: { lessonId: number }) {
  // Once the server says the lesson does not exist (deleted while open, here or
  // elsewhere), stop streaming and polling it: the stale data must not stay on
  // screen with actions that can only fail.
  const [gone, setGone] = useState<'missing' | 'deleted' | null>(null)
  const { data, error, isError, isFetching, refetch, connection } = useLiveLessonView(gone ? undefined : lessonId)
  if (!gone && isError && error instanceof ApiError && error.isNotFound) setGone(data ? 'deleted' : 'missing')
  const [openStudentId, setOpenStudentId] = useState<number | null>(null)
  const unidentifiedRef = useRef<HTMLElement>(null)
  const status = data?.lesson.status
  // Only the waiting banner needs a clock (the "check Zoom" hint after the planned start).
  const now = useNow(status === 'planned' ? 30_000 : null)

  const participants = data?.participants
  const groups = useMemo(() => groupParticipants(participants ?? []), [participants])

  const showUnidentified = useCallback(() => {
    const el = unidentifiedRef.current
    if (!el) return
    const reduce = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
    el.scrollIntoView({ behavior: reduce ? 'auto' : 'smooth', block: 'start' })
    el.focus({ preventScroll: true })
  }, [])

  const closeSheet = useCallback(() => setOpenStudentId(null), [])

  if (gone) return <LessonNotFound deleted={gone === 'deleted'} />

  if (!data) {
    if (isError) {
      return (
        <Page>
          <PageHeader title="Занятие" back={{ to: '/', label: 'Все занятия' }} />
          <ErrorState error={error} onRetry={() => void refetch()} retrying={isFetching} />
        </Page>
      )
    }
    return (
      <Page width="wide">
        <LoadingState variant="page" rows={8} label="Загружаем занятие…" />
      </Page>
    )
  }

  const { lesson, counts, students } = data
  const finished = lesson.status === 'finished'
  const waiting = lesson.status === 'planned' && counts.seen === 0 && data.participants.length === 0

  return (
    <Page width="wide">
      <LessonHeader lesson={lesson} connection={connection} />

      {finished && <FinishedBanner lesson={lesson} />}
      {waiting && <WaitingBanner lesson={lesson} now={now} />}
      {lesson.status === 'active' && <OverdueBanner lesson={lesson} />}

      <LessonStats counts={counts} lessonStatus={lesson.status} onShowUnidentified={showUnidentified} />

      <div className="grid items-start gap-6 xl:grid-cols-[minmax(0,1fr)_24rem]">
        <section aria-labelledby="students-title" className="flex min-w-0 flex-col gap-3">
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
            <h2 id="students-title" className="text-lg font-semibold">
              Студенты
            </h2>
            {students.length > 0 && lesson.status !== 'planned' && (
              <p className="text-sm text-muted-foreground">
                Итог сейчас: были — <span className="font-medium text-foreground tabular-nums">{counts.present}</span>,
                не были — <span className="font-medium text-foreground tabular-nums">{counts.absent}</span>
              </p>
            )}
          </div>
          <StudentsTable
            lessonId={lesson.id}
            groupId={lesson.group.id}
            students={students}
            asOf={data.as_of}
            onOpenStudent={setOpenStudentId}
          />
        </section>

        <div className="flex min-w-0 flex-col gap-4">
          <UnidentifiedBlock
            ref={unidentifiedRef}
            lessonId={lesson.id}
            groupId={lesson.group.id}
            lessonFinished={finished}
            asOf={data.as_of}
            unresolved={groups.unresolved}
            anyParticipants={data.participants.length > 0}
            students={students}
          />
          <HiddenParticipants lessonId={lesson.id} asOf={data.as_of} hidden={groups.hidden} />
          <LessonSessionsCard lesson={lesson} />
        </div>
      </div>

      <StudentSheet lessonId={lesson.id} view={data} studentId={openStudentId} onClose={closeSheet} />
    </Page>
  )
}

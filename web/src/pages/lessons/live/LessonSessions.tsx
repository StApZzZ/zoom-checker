// The lesson's Zoom conferences: the "Zoom did not report the end" banner and the
// list of linked conferences with «Отвязать» (docs/architecture.md §5.5).

import { useState } from 'react'
import { Link2Off, Square, TriangleAlert, Video } from 'lucide-react'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useNow } from '@/hooks/useNow'
import { formatDate, formatMeetingId, formatTime, toDateInputValue } from '@/lib/format'
import { useLinkSession } from '@/lib/queries'
import type { Lesson, ZoomSession } from '@/lib/types'
import { isOverdue } from '@/pages/lessons/list/lessonSections'
import { FinishLessonDialog } from '@/pages/lessons/live/LessonHeader'
import { sessionTime } from '@/pages/zoom/linkCandidates'

/**
 * «Zoom не сообщил, что конференция закончилась»: a lesson still running an hour after
 * its planned end. Without a finish the minutes of everyone "in Zoom" keep growing.
 */
export function OverdueBanner({ lesson }: { lesson: Lesson }) {
  const now = useNow(60_000)
  const [finishOpen, setFinishOpen] = useState(false)
  if (!isOverdue(lesson, now) || !lesson.sessions.some((s) => s.ended_at === null)) return null
  const end = Date.parse(lesson.planned_end_at)
  const otherDay = toDateInputValue(end) !== toDateInputValue(now)
  return (
    <section
      aria-label="Zoom не сообщил об окончании"
      className="flex flex-col gap-3 rounded-xl bg-amber-50 p-4 ring-1 ring-amber-600/20 md:flex-row md:items-center md:justify-between dark:bg-amber-400/10 dark:ring-amber-400/25"
    >
      <div className="flex items-start gap-3">
        <TriangleAlert className="mt-0.5 size-5 shrink-0 text-amber-700 dark:text-amber-300" aria-hidden="true" />
        <div className="flex flex-col gap-1">
          <p className="font-medium">Zoom не сообщил, что конференция закончилась.</p>
          <p className="text-sm text-muted-foreground">
            По расписанию занятие закончилось {otherDay ? `${formatDate(end)} ` : ''}в {formatTime(end)}. Если оно уже
            прошло, нажмите «Завершить занятие» и укажите время окончания — иначе время тех, кто «В Zoom», будет
            расти. Если из Zoom 6 часов ничего не придёт, сервис завершит занятие сам.
          </p>
        </div>
      </div>
      <Button variant="outline" className="shrink-0 self-start md:self-auto" onClick={() => setFinishOpen(true)}>
        <Square aria-hidden="true" />
        Завершить занятие
      </Button>
      {/* Mounted per opening so the time defaults to "now" every time. */}
      {finishOpen && <FinishLessonDialog lesson={lesson} open onOpenChange={setFinishOpen} />}
    </section>
  )
}

function SessionWhen({ session }: { session: ZoomSession }) {
  const start = sessionTime(session)
  if (!start) return null
  return (
    <span className="tabular-nums">
      {formatTime(start)}
      {session.ended_at ? `–${formatTime(session.ended_at)}` : ' — идёт или конец неизвестен'}
    </span>
  )
}

/**
 * «Конференции Zoom»: what the attendance of this lesson is counted from. A wrong
 * conference (a colleague reused the same meeting number) is unlinked right here.
 */
export function LessonSessionsCard({ lesson }: { lesson: Lesson }) {
  const [unlinking, setUnlinking] = useState<ZoomSession | null>(null)
  const unlink = useLinkSession({ successMessage: 'Конференция отвязана от занятия' })
  if (lesson.sessions.length === 0) return null
  return (
    <Card size="sm">
      <CardHeader>
        <CardTitle>
          <h2 className="text-base font-semibold">Конференции Zoom</h2>
        </CardTitle>
        <CardDescription>По ним считается посещаемость этого занятия.</CardDescription>
      </CardHeader>
      <CardContent>
        <ul className="flex flex-col gap-2" aria-label="Конференции Zoom этого занятия">
          {lesson.sessions.map((s) => (
            <li
              key={s.id}
              className="flex flex-col gap-2 rounded-lg p-3 text-sm ring-1 ring-foreground/10 sm:flex-row sm:items-center sm:justify-between xl:flex-col xl:items-stretch"
            >
              <span className="flex min-w-0 items-start gap-2">
                <Video className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="flex min-w-0 flex-col">
                  <span className="font-medium tabular-nums">{formatMeetingId(s.meeting_id)}</span>
                  <span className="break-words text-muted-foreground">
                    «{s.topic.trim() || 'Без темы'}» · <SessionWhen session={s} />
                  </span>
                  <span className="text-xs text-muted-foreground">
                    {s.link_mode === 'manual' ? 'Привязана вручную' : 'Привязана автоматически'}
                  </span>
                </span>
              </span>
              <Button variant="outline" size="sm" className="shrink-0 self-start" onClick={() => setUnlinking(s)}>
                <Link2Off aria-hidden="true" />
                Это другая конференция
              </Button>
            </li>
          ))}
        </ul>
      </CardContent>
      <ConfirmDialog
        open={unlinking !== null}
        onOpenChange={(open) => !open && setUnlinking(null)}
        title="Отвязать конференцию от занятия?"
        description={
          unlinking
            ? `Посещаемость занятия пересчитается без участников конференции ${formatMeetingId(unlinking.meeting_id)} («${unlinking.topic.trim() || 'Без темы'}»). Сама она больше не привяжется; найти её можно в разделе «Zoom».`
            : undefined
        }
        confirmLabel="Отвязать"
        destructive
        onConfirm={() => (unlinking ? unlink.mutateAsync({ sessionId: unlinking.id, lessonId: null }) : undefined)}
      />
    </Card>
  )
}

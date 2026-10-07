import { useState } from 'react'
import { ArrowRight, CircleCheck, Hourglass, KeyRound, Link2, Video } from 'lucide-react'
import { Link } from 'react-router'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { NameReminder } from '@/components/lessons/NameReminder'
import { Button } from '@/components/ui/button'
import { useOptionalAuth } from '@/lib/auth'
import { formatDate, formatMeetingId, formatTime, toDateInputValue } from '@/lib/format'
import { useLessonAudit, useLinkSession, useZoomSessions } from '@/lib/queries'
import type { Lesson, ZoomSession } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useZoomConnection } from '@/lib/zoomConnection'
import { ExportButtons } from '@/pages/lessons/live/ExportButtons'
import { finishedBySystem } from '@/pages/lessons/live/liveText'
import { runningUnlinkedSessions, sessionTime } from '@/pages/zoom/linkCandidates'

/** Grace period after the planned start before the "check Zoom" hint appears. */
const LATE_START_HINT_MS = 5 * 60_000
/** Unlinked conferences checked for a lesson that has not started (newest first). */
const UNLINKED_LIMIT = 50
/** SSE does not announce conferences without a lesson: poll while the hint is shown. */
const UNLINKED_REFRESH_MS = 30_000
/** At most this many "maybe this is your lesson" conferences; the rest are on /zoom. */
const MAX_SUGGESTIONS = 3

const linkClass =
  'inline-flex w-fit items-center gap-1 rounded-sm font-medium text-foreground underline underline-offset-4 outline-none focus-visible:ring-3 focus-visible:ring-ring/50'

/** Shown before anybody joined: what happens next + the students' name reminder. */
export function WaitingBanner({ lesson, now }: { lesson: Lesson; now: number }) {
  const start = Date.parse(lesson.planned_start_at)
  const overdue = !Number.isNaN(start) && now - start > LATE_START_HINT_MS
  const zoom = useZoomConnection()
  const isAdmin = useOptionalAuth()?.isAdmin ?? false
  // Without the Secret Token Zoom's events are refused: nothing can ever be recorded,
  // and "check your Zoom meeting" would send the teacher after the wrong cause.
  const notConnected = zoom === 'not_configured'
  const unlinked = useZoomSessions(
    { unlinked: true, limit: UNLINKED_LIMIT },
    { enabled: overdue && !notConnected, refetchInterval: UNLINKED_REFRESH_MS },
  )
  const running = overdue && !notConnected ? runningUnlinkedSessions(unlinked.data ?? [], lesson.planned_start_at, now) : []

  if (notConnected) {
    return (
      <section
        aria-label="Ожидание начала занятия"
        className="flex flex-col gap-3 rounded-xl bg-red-50 p-4 ring-1 ring-red-600/20 dark:bg-red-400/10 dark:ring-red-400/25"
      >
        <div className="flex items-start gap-3">
          <KeyRound className="mt-0.5 size-5 shrink-0 text-red-700 dark:text-red-300" aria-hidden="true" />
          <div className="flex flex-col gap-1">
            <p className="font-medium">
              Сервис пока не подключён к Zoom, поэтому посещаемость не отмечается. Сообщите администратору.
            </p>
            {isAdmin && (
              <Link to="/zoom" className={cn(linkClass, 'text-sm')}>
                Подключить Zoom в разделе «Zoom»
                <ArrowRight className="size-4" aria-hidden="true" />
              </Link>
            )}
          </div>
        </div>
        <NameReminder />
      </section>
    )
  }

  return (
    <section
      aria-label="Ожидание начала занятия"
      className="flex flex-col gap-3 rounded-xl bg-sky-50 p-4 ring-1 ring-sky-600/20 dark:bg-sky-400/10 dark:ring-sky-400/25"
    >
      <div className="flex items-start gap-3">
        <Hourglass className="mt-0.5 size-5 shrink-0 text-sky-700 dark:text-sky-300" aria-hidden="true" />
        <div className="flex min-w-0 flex-col gap-1">
          <p className="font-medium">
            Ждём начала занятия в Zoom. Как только студенты начнут подключаться, список обновится сам.
          </p>
          {overdue ? (
            <>
              <p className="text-sm text-muted-foreground">
                Занятие должно было начаться в {formatTime(lesson.planned_start_at)}. Проверьте, что в Zoom запущена
                конференция {lesson.zoom_meeting_id_formatted || lesson.zoom_meeting_id}.
              </p>
              {zoom === 'waiting' && (
                <p className="text-sm text-muted-foreground">
                  Из Zoom ещё ни разу не приходили события — возможно, подключение не закончено. Сообщите
                  администратору.
                </p>
              )}
              {running.length > 0 ? (
                <ul className="mt-1 flex flex-col gap-2" aria-label="Конференции без занятия, которые идут сейчас">
                  {running.slice(0, MAX_SUGGESTIONS).map((s) => (
                    <RunningConference key={s.id} session={s} lesson={lesson} />
                  ))}
                  {running.length > MAX_SUGGESTIONS && (
                    <li className="text-sm">
                      <Link to="/zoom" className={linkClass}>
                        Все конференции без занятия
                        <ArrowRight className="size-4" aria-hidden="true" />
                      </Link>
                    </li>
                  )}
                </ul>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Если в Zoom другой номер — исправьте его в занятии: «Действия» → «Изменить». Все конференции без
                  занятия — в разделе{' '}
                  <Link to="/zoom" className={linkClass}>
                    «Zoom»
                  </Link>
                  .
                </p>
              )}
            </>
          ) : (
            <p className="text-sm text-muted-foreground">Обновлять страницу не нужно — просто держите её открытой.</p>
          )}
        </div>
      </div>
      <NameReminder />
    </section>
  )
}

/** "Похоже, конференция идёт под другим номером…" with a confirmed «Привязать». */
function RunningConference({ session, lesson }: { session: ZoomSession; lesson: Lesson }) {
  const [confirming, setConfirming] = useState(false)
  const link = useLinkSession({ successMessage: 'Конференция привязана к занятию' })
  const number = formatMeetingId(session.meeting_id)
  const topic = session.topic.trim() || 'Без темы'
  const started = sessionTime(session)
  const sameNumber = session.meeting_id !== '' && session.meeting_id === lesson.zoom_meeting_id
  return (
    <li className="flex flex-col gap-2 rounded-lg bg-background/70 p-3 ring-1 ring-foreground/10 sm:flex-row sm:items-center sm:justify-between">
      <p className="flex min-w-0 items-start gap-2 text-sm">
        <Video className="mt-0.5 size-4 shrink-0 text-sky-700 dark:text-sky-300" aria-hidden="true" />
        <span className="min-w-0 break-words">
          {sameNumber ? 'В Zoom идёт конференция с этим номером, но она не привязана к занятию: ' : 'Похоже, конференция идёт под другим номером: '}
          <span className="font-medium tabular-nums">{number}</span> · «{topic}»
          {started && <> · с {formatTime(started)}</>}
        </span>
      </p>
      <Button variant="outline" className="shrink-0 self-start sm:self-auto" onClick={() => setConfirming(true)}>
        <Link2 aria-hidden="true" />
        Привязать к этому занятию
      </Button>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Привязать конференцию к этому занятию?"
        description={
          <>
            Посещаемость занятия «{lesson.title}» будет считаться по участникам конференции {number} («{topic}»). Если
            это конференция коллеги, не привязывайте её. Чтобы следующие занятия находились сами, исправьте номер
            конференции в занятии: «Действия» → «Изменить».
          </>
        }
        confirmLabel="Привязать"
        onConfirm={() => link.mutateAsync({ sessionId: session.id, lessonId: lesson.id })}
      />
    </li>
  )
}

/** "Занятие завершено в 19:31…" with prominent export buttons. */
export function FinishedBanner({ lesson }: { lesson: Lesson }) {
  const end = lesson.actual_end_at ?? lesson.manual_finished_at ?? lesson.planned_end_at
  const otherDay = toDateInputValue(end) !== toDateInputValue(lesson.planned_start_at)
  // Only a finish without Zoom's "meeting ended" can be the service's own: ask the log then.
  const maybeAutomatic = lesson.manual_finished_at !== null && lesson.sessions.some((s) => s.ended_at === null)
  const audit = useLessonAudit(maybeAutomatic ? lesson.id : undefined)
  const automatic = maybeAutomatic && finishedBySystem(audit.data)
  return (
    <section
      aria-label="Занятие завершено"
      className="flex flex-col gap-3 rounded-xl bg-muted/60 p-4 ring-1 ring-foreground/10 md:flex-row md:items-center md:justify-between"
    >
      <div className="flex items-start gap-3">
        <CircleCheck className="mt-0.5 size-5 shrink-0 text-emerald-700 dark:text-emerald-400" aria-hidden="true" />
        <p>
          <span className="font-medium">
            Занятие завершено {otherDay ? `${formatDate(end)} ` : ''}в {formatTime(end)}.
          </span>{' '}
          {automatic
            ? 'Zoom не сообщил об окончании конференции, поэтому сервис завершил занятие сам — по последнему событию из Zoom. Если оно шло дольше: «Действия» → «Возобновить», затем «Завершить занятие» с нужным временем.'
            : 'Итоги зафиксированы; вы можете исправить отметки вручную.'}
        </p>
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        <ExportButtons lessonId={lesson.id} size="default" />
      </div>
    </section>
  )
}

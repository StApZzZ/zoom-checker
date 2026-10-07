import { useId, useMemo, useState } from 'react'
import { CalendarSearch, Link2, Loader2, Search } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { formatDateTime, formatMeetingId, formatTimeRange, formatWeekdayDate } from '@/lib/format'
import { useLessons, useLinkSession } from '@/lib/queries'
import type { LessonSummary, ZoomSession } from '@/lib/types'
import { cn } from '@/lib/utils'
import { candidateRange, linkCandidates, sessionTime } from '@/pages/zoom/linkCandidates'

export interface LinkSessionDialogProps {
  /** Session to link; null = closed. */
  session: ZoomSession | null
  onOpenChange: (open: boolean) => void
}

interface LessonOptionProps {
  lesson: LessonSummary
  id: string
  sameMeeting: boolean
  /** The session is linked to this lesson already: shown, but cannot be chosen again. */
  current: boolean
}

function LessonOption({ lesson, id, sameMeeting, current }: LessonOptionProps) {
  return (
    <Label
      htmlFor={id}
      className={cn(
        'flex items-start gap-3 rounded-lg p-3 font-normal ring-1 ring-foreground/10',
        current
          ? 'cursor-not-allowed bg-muted/30'
          : 'cursor-pointer hover:bg-muted/40 has-data-checked:bg-muted/60 has-data-checked:ring-foreground/30',
      )}
    >
      <RadioGroupItem id={id} value={String(lesson.id)} className="mt-0.5" disabled={current} />
      <span className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="font-medium break-words">
          {lesson.title}
          {current && (
            <span className="ml-2 inline-flex items-center gap-1 align-middle text-xs font-normal text-muted-foreground">
              <Link2 className="size-3.5" aria-hidden="true" />
              конференция сейчас привязана к нему
            </span>
          )}
        </span>
        <span className="text-muted-foreground">
          {lesson.group?.name} · {formatWeekdayDate(lesson.planned_start_at)},{' '}
          {formatTimeRange(lesson.planned_start_at, lesson.planned_end_at)}
        </span>
        <span className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
          <StatusBadge kind="lesson" status={lesson.status} size="sm" />
          <span className={sameMeeting ? 'font-medium text-foreground' : undefined}>
            Zoom {lesson.zoom_meeting_id_formatted || formatMeetingId(lesson.zoom_meeting_id)}
          </span>
        </span>
      </span>
    </Label>
  )
}

/** "Привязать к занятию": pick a lesson for a Zoom session (same Zoom ID first, then the same day). */
export function LinkSessionDialog({ session, onOpenChange }: LinkSessionDialogProps) {
  const open = session !== null
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex max-h-[calc(100svh-2rem)] flex-col sm:max-w-xl">
        {session && <LinkSessionForm key={session.id} session={session} onDone={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function LinkSessionForm({ session, onDone }: { session: ZoomSession; onDone: () => void }) {
  const id = useId()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState('')
  const range = candidateRange(session)
  const lessons = useLessons(range ?? {}, { enabled: range !== null })
  const link = useLinkSession({ successMessage: '' })

  const groups = useMemo(
    () => linkCandidates(session, lessons.data ?? [], query),
    [session, lessons.data, query],
  )
  const all = [...groups.sameMeeting, ...groups.sameDay]
  const chosen = all.find((l) => String(l.id) === selected && l.id !== session.lesson_id) ?? null
  const relink = session.lesson_id !== null
  const at = sessionTime(session)

  const submit = () => {
    if (!chosen) return
    link.mutate(
      { sessionId: session.id, lessonId: chosen.id },
      {
        onSuccess: () => {
          toast.success(`Конференция привязана к занятию «${chosen.title}»`, {
            action: { label: 'Открыть', onClick: () => navigate(`/lessons/${chosen.id}`) },
          })
          onDone()
        },
      },
    )
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>{relink ? 'Привязать к другому занятию' : 'Привязать к занятию'}</DialogTitle>
        <DialogDescription>
          Конференция Zoom {formatMeetingId(session.meeting_id)}
          {session.topic ? ` «${session.topic}»` : ''}
          {at ? `, ${formatDateTime(at)}` : ''}. Выберите занятие — посещаемость пересчитается по её участникам.
        </DialogDescription>
      </DialogHeader>

      <div className="relative">
        <Search
          className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
          aria-hidden="true"
        />
        <Input
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Найти по названию или группе"
          aria-label="Найти занятие по названию или группе"
          className="h-9 pl-8"
        />
      </div>

      <div className="-mx-4 min-h-0 flex-1 overflow-y-auto px-4">
        {lessons.data ? (
          all.length === 0 ? (
            <EmptyState
              size="compact"
              icon={CalendarSearch}
              title={query.trim() ? 'Ничего не нашлось' : 'Подходящих занятий нет'}
              text={
                query.trim() ? (
                  'Попробуйте другое слово или очистите поиск.'
                ) : (
                  <>
                    В этот день нет занятий. Создайте занятие на странице{' '}
                    <Link to="/" className="font-medium underline underline-offset-3" onClick={onDone}>
                      «Занятия»
                    </Link>{' '}
                    с номером конференции {formatMeetingId(session.meeting_id)} — конференция привяжется к нему сама.
                  </>
                )
              }
            />
          ) : (
            <RadioGroup value={selected} onValueChange={setSelected} aria-label="Занятие" className="gap-4">
              {groups.sameMeeting.length > 0 && (
                <div role="group" aria-labelledby={`${id}-same`} className="flex flex-col gap-2">
                  <p id={`${id}-same`} className="text-sm font-medium">
                    С тем же номером конференции
                  </p>
                  {groups.sameMeeting.map((l) => (
                    <LessonOption
                      key={l.id}
                      lesson={l}
                      id={`${id}-l${l.id}`}
                      sameMeeting
                      current={l.id === session.lesson_id}
                    />
                  ))}
                </div>
              )}
              {groups.sameDay.length > 0 && (
                <div role="group" aria-labelledby={`${id}-day`} className="flex flex-col gap-2">
                  <p id={`${id}-day`} className="text-sm font-medium">
                    Другие занятия в этот день
                  </p>
                  {groups.sameDay.map((l) => (
                    <LessonOption
                      key={l.id}
                      lesson={l}
                      id={`${id}-l${l.id}`}
                      sameMeeting={false}
                      current={l.id === session.lesson_id}
                    />
                  ))}
                </div>
              )}
            </RadioGroup>
          )
        ) : lessons.isError ? (
          <ErrorState size="compact" error={lessons.error} onRetry={() => lessons.refetch()} retrying={lessons.isFetching} />
        ) : (
          <LoadingState variant="list" rows={4} label="Загружаем занятия…" />
        )}
      </div>

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone} disabled={link.isPending}>
          Отмена
        </Button>
        <Button type="button" onClick={submit} disabled={!chosen || link.isPending}>
          {link.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Привязать
        </Button>
      </DialogFooter>
    </>
  )
}

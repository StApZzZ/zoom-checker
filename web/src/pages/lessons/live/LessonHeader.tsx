import { useId, useState } from 'react'
import {
  Ellipsis,
  ExternalLink,
  FileSpreadsheet,
  FileText,
  Pencil,
  RefreshCw,
  RotateCcw,
  Square,
  Trash2,
  Video,
} from 'lucide-react'
import { Link, useNavigate } from 'react-router'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { TimeZoneNote } from '@/components/TimeZoneNote'
import { CopyButton } from '@/components/CopyButton'
import { PageHeader } from '@/components/layout/PageHeader'
import { EditLessonDialog } from '@/components/lessons/EditLessonDialog'
import { FieldError } from '@/components/lessons/LessonFormFields'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useNow } from '@/hooks/useNow'
import {
  formatDate,
  formatTime,
  formatTimeRange,
  formatWeekdayDate,
  fromDateTimeInputs,
  toDateInputValue,
  toTimeInputValue,
} from '@/lib/format'
import { useDeleteLesson, useFinishLesson, useLessonExport, useRecomputeLesson, useReopenLesson } from '@/lib/queries'
import type { StreamState } from '@/lib/sse'
import type { Lesson } from '@/lib/types'
import { zoomJoinUrl, zoomMeetingEnded } from '@/pages/lessons/live/liveText'

function ZoomInfo({ lesson }: { lesson: Lesson }) {
  const joinUrl = zoomJoinUrl(lesson)
  if (!lesson.zoom_meeting_id) return null
  return (
    <span className="flex flex-wrap items-center gap-2">
      <span className="inline-flex h-8 items-center gap-1.5 rounded-full pr-1 pl-3 text-sm ring-1 ring-border">
        <Video className="size-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-muted-foreground">Zoom:</span>
        <span className="font-medium tabular-nums">{lesson.zoom_meeting_id_formatted || lesson.zoom_meeting_id}</span>
        <CopyButton
          value={lesson.zoom_meeting_id}
          variant="ghost"
          size="icon-sm"
          className="rounded-full"
          srLabel="Скопировать номер конференции"
          successMessage="Номер конференции скопирован"
        />
      </span>
      {joinUrl && (
        <Button asChild variant="outline" size="sm" className="h-8 rounded-full">
          <a href={joinUrl} target="_blank" rel="noopener noreferrer">
            <ExternalLink aria-hidden="true" />
            Открыть в Zoom
          </a>
        </Button>
      )}
    </span>
  )
}

/** Finishing later than this after the planned end suggests the planned end time instead of "now". */
const LATE_FINISH_HINT_MS = 30 * 60_000

/** «Завершить занятие» with the end time (also opened from the overdue banner). */
export function FinishLessonDialog({
  lesson,
  open,
  onOpenChange,
}: {
  lesson: Lesson
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const id = useId()
  const finish = useFinishLesson(lesson.id)
  const [openedAt] = useState(() => new Date())
  const nowMs = useNow(15_000)
  const today = toDateInputValue(openedAt)
  const [date, setDate] = useState(today)
  const [time, setTime] = useState(() => toTimeInputValue(openedAt))
  const at = fromDateTimeInputs(date, time)
  const plannedStart = Date.parse(lesson.planned_start_at)
  const plannedEnd = Date.parse(lesson.planned_end_at)
  const actualStart = lesson.actual_start_at ? Date.parse(lesson.actual_start_at) : null
  // Same rules as the server (POST /finish): not in the future, not before the start.
  const notStarted = actualStart === null && nowMs < plannedStart
  // The date is only asked for when the lesson is not "today" (or the teacher picked another day).
  const askDate = toDateInputValue(lesson.planned_start_at) !== today || date !== today
  // Finishing long after the planned end: "now" would credit everybody still in Zoom until now.
  const lateFinish = !notStarted && Number.isFinite(plannedEnd) && openedAt.getTime() - plannedEnd > LATE_FINISH_HINT_MS
  const plannedEndValues = { date: toDateInputValue(plannedEnd), time: toTimeInputValue(plannedEnd) }
  const atPlannedEnd = date === plannedEndValues.date && time === plannedEndValues.time

  let error: string | undefined
  if (notStarted) error = 'Занятие ещё не началось — завершить его пока нельзя.'
  else if (!at) error = 'Укажите время окончания'
  else if (Date.parse(at) > nowMs + 60_000) error = 'Это время ещё не наступило'
  else if (actualStart !== null && Date.parse(at) < actualStart) {
    error = `Занятие началось в ${formatTime(actualStart)} — укажите время позже`
  } else if (actualStart === null && Date.parse(at) < plannedStart) {
    error = `Занятие было назначено на ${formatTime(plannedStart)} — укажите время позже`
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Завершить занятие?"
      description={
        lesson.status === 'planned'
          ? 'Из Zoom по этому занятию ничего не приходило. После завершения все студенты будут отмечены как отсутствующие — отметки можно исправить вручную.'
          : 'Все, кто ещё в Zoom, будут отмечены как вышедшие в указанное время. Итоги зафиксируются, но отметки можно будет исправить вручную.'
      }
      confirmLabel="Завершить занятие"
      confirmDisabled={!!error}
      onConfirm={() => finish.mutateAsync(at ? { at } : {})}
    >
      {!notStarted && (
        <div className="flex flex-wrap gap-3">
          {askDate && (
            <div className="flex flex-col gap-2">
              <Label htmlFor={`${id}-date`}>Дата окончания</Label>
              <Input id={`${id}-date`} type="date" className="h-10" value={date} onChange={(e) => setDate(e.target.value)} />
            </div>
          )}
          <div className="flex flex-col gap-2">
            <Label htmlFor={`${id}-time`}>Время окончания</Label>
            <Input
              id={`${id}-time`}
              type="time"
              className="h-10 w-32"
              value={time}
              onChange={(e) => setTime(e.target.value)}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? `${id}-time-error` : lateFinish && !atPlannedEnd ? `${id}-time-hint` : undefined}
            />
          </div>
          <TimeZoneNote at={at ?? openedAt} className="basis-full" />
        </div>
      )}
      {lateFinish && !atPlannedEnd && (
        <div id={`${id}-time-hint`} className="flex flex-col items-start gap-2 rounded-md bg-amber-50 p-3 text-sm dark:bg-amber-400/10">
          <p>
            По расписанию занятие закончилось{' '}
            {plannedEndValues.date !== today ? `${formatDate(plannedEnd)} ` : ''}в {formatTime(plannedEnd)}. Укажите, когда
            оно закончилось на самом деле: тем, кто остался в Zoom, время засчитается до этой минуты.
          </p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => {
              setDate(plannedEndValues.date)
              setTime(plannedEndValues.time)
            }}
          >
            Поставить {formatTime(plannedEnd)}
          </Button>
        </div>
      )}
      <FieldError id={`${id}-time-error`}>{error}</FieldError>
    </ConfirmDialog>
  )
}

export interface LessonHeaderProps {
  lesson: Lesson
  connection: StreamState
}

/** Title, group, time, Zoom number, status, live indicator and the actions menu. */
export function LessonHeader({ lesson, connection }: LessonHeaderProps) {
  const navigate = useNavigate()
  const [editOpen, setEditOpen] = useState(false)
  const [finishOpen, setFinishOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const zoomEnded = zoomMeetingEnded(lesson)
  // When Zoom has ended too, clearing the manual finish keeps the lesson finished at the Zoom end time.
  const reopen = useReopenLesson(lesson.id, {
    successMessage: zoomEnded ? 'Время окончания теперь берётся из Zoom' : undefined,
  })
  const recompute = useRecomputeLesson(lesson.id)
  const exportFile = useLessonExport(lesson.id)
  const remove = useDeleteLesson()
  const finished = lesson.status === 'finished'

  const eyebrow = (
    <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
      <Link
        to={`/groups/${lesson.group.id}`}
        className="rounded-sm font-medium text-foreground underline-offset-4 outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
      >
        {lesson.group.name}
      </Link>
      <span aria-hidden="true">·</span>
      <span className="first-letter:uppercase">{formatWeekdayDate(lesson.planned_start_at)}</span>
      <span aria-hidden="true">·</span>
      <span className="tabular-nums">{formatTimeRange(lesson.planned_start_at, lesson.planned_end_at)}</span>
    </span>
  )

  // Finished lessons show the export buttons in the banner right below; here they stay in the menu.
  const actions = (
    <>
      <DropdownMenu modal={false}>
        <DropdownMenuTrigger asChild>
          <Button variant="outline" size="lg">
            <Ellipsis aria-hidden="true" />
            Действия
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="min-w-60">
          <DropdownMenuItem onSelect={() => setEditOpen(true)}>
            <Pencil aria-hidden="true" />
            Изменить
          </DropdownMenuItem>
          {finished ? (
            lesson.manual_finished_at === null ? (
              // Finished by Zoom: only a new Zoom session reopens it (the server answers 409).
              <DropdownMenuItem disabled className="flex-col items-start gap-0.5">
                <span className="flex items-center gap-2">
                  <RotateCcw aria-hidden="true" />
                  Возобновить
                </span>
                <span className="text-xs">Занятие закончилось вместе с конференцией в Zoom</span>
              </DropdownMenuItem>
            ) : (
              <DropdownMenuItem onSelect={() => reopen.mutate()} disabled={reopen.isPending}>
                <RotateCcw aria-hidden="true" />
                {zoomEnded ? 'Отменить ручное завершение' : 'Возобновить'}
              </DropdownMenuItem>
            )
          ) : (
            <DropdownMenuItem onSelect={() => setFinishOpen(true)}>
              <Square aria-hidden="true" />
              Завершить занятие
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => recompute.mutate()} disabled={recompute.isPending}>
            <RefreshCw aria-hidden="true" />
            Пересчитать посещаемость
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">Скачать посещаемость</DropdownMenuLabel>
          <DropdownMenuItem
            onSelect={() => exportFile.download('xlsx')}
            disabled={exportFile.pending.includes('xlsx')}
          >
            <FileSpreadsheet aria-hidden="true" />
            Excel (.xlsx)
          </DropdownMenuItem>
          <DropdownMenuItem onSelect={() => exportFile.download('csv')} disabled={exportFile.pending.includes('csv')}>
            <FileText aria-hidden="true" />
            CSV
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem variant="destructive" onSelect={() => setDeleteOpen(true)}>
            <Trash2 aria-hidden="true" />
            Удалить занятие
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </>
  )

  return (
    <>
      <PageHeader
        back={{ to: '/', label: 'Все занятия' }}
        eyebrow={eyebrow}
        title={lesson.title}
        documentTitle={lesson.title}
        actions={actions}
      >
        <StatusBadge kind="lesson" status={lesson.status} size="lg" />
        {!finished && <StatusBadge kind="stream" status={connection} />}
        <ZoomInfo lesson={lesson} />
      </PageHeader>

      <EditLessonDialog lesson={lesson} open={editOpen} onOpenChange={setEditOpen} />
      {/* Mounted per opening so the time defaults to "now" every time. */}
      {finishOpen && <FinishLessonDialog lesson={lesson} open onOpenChange={setFinishOpen} />}
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title="Удалить занятие?"
        description={`Занятие «${lesson.title}» и вся его посещаемость будут удалены. Это действие нельзя отменить.`}
        confirmLabel="Удалить"
        destructive
        onConfirm={() => remove.mutateAsync(lesson.id).then(() => navigate('/', { replace: true }))}
      />
    </>
  )
}

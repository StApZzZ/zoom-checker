import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Loader2, LogIn, Unlink, UserX } from 'lucide-react'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { FinalStatusBadge } from '@/components/FinalStatusBadge'
import { LiveDuration } from '@/components/LiveDuration'
import { LoadingState } from '@/components/LoadingState'
import { auditActionLabel, finalStatusMeta } from '@/components/status-meta'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Separator } from '@/components/ui/separator'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import {
  EMPTY,
  formatDateTime,
  formatDurationLong,
  formatEarlyLeave,
  formatLate,
  formatTime,
  pluralize,
  WORDS,
} from '@/lib/format'
import {
  queryKeys,
  useMapParticipant,
  useStudentHistory,
  useUnmapParticipant,
  useUpdateAttendance,
} from '@/lib/queries'
import type { AttendanceStatus, Interval, LessonView, Participant, StudentRow } from '@/lib/types'
import { normalizeSearch } from '@/pages/lessons/list/lessonSections'
import {
  auditText,
  auditValue,
  COMMENT_MAX,
  deviceNumbers,
  findParticipant,
  leaveReasonLabel,
  participantsOfStudent,
  REASON_MAX,
  unmatchOutcome,
} from '@/pages/lessons/live/liveText'

export interface StudentSheetProps {
  lessonId: number
  view: LessonView
  /** Student whose card is open; null = closed. */
  studentId: number | null
  onClose: () => void
}

/** Side sheet with everything about one student on this lesson. */
export function StudentSheet({ lessonId, view, studentId, onClose }: StudentSheetProps) {
  const row = studentId !== null ? view.students.find((s) => s.student_id === studentId) : undefined
  return (
    <Sheet open={studentId !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent
        side="right"
        className="w-full gap-0 overflow-y-auto data-[side=right]:w-full data-[side=right]:sm:max-w-lg"
      >
        {row ? (
          <StudentSheetBody key={row.student_id} lessonId={lessonId} view={view} row={row} />
        ) : (
          <>
            <SheetHeader>
              <SheetTitle>Студент не найден</SheetTitle>
              <SheetDescription>Возможно, его убрали из группы. Закройте эту панель.</SheetDescription>
            </SheetHeader>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

function Section({ title, children, id }: { title: string; children: ReactNode; id: string }) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-3 px-4 py-4">
      <h3 id={id} className="text-sm font-semibold">
        {title}
      </h3>
      {children}
    </section>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="text-sm font-medium">{children}</dd>
    </div>
  )
}

/** A short fingerprint of the row: when it changes, the interval history is refreshed. */
function rowSignature(row: StudentRow): string {
  return [
    row.current_status,
    row.devices_online,
    row.first_join_at,
    row.last_leave_at,
    row.manual_status,
    row.comment,
    row.zoom_names.join('\u0001'),
  ].join('|')
}

function StudentSheetBody({ lessonId, view, row }: { lessonId: number; view: LessonView; row: StudentRow }) {
  const id = useId()
  const qc = useQueryClient()
  const history = useStudentHistory(lessonId, row.student_id)
  const online = row.current_status === 'online'
  const lessonStarted = view.lesson.status !== 'planned'

  // Live: a join/leave/manual change of this student refreshes the history list.
  const signature = rowSignature(row)
  const firstRun = useRef(true)
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false
      return
    }
    void qc.invalidateQueries({ queryKey: queryKeys.lessons.history(lessonId, row.student_id) })
  }, [qc, lessonId, row.student_id, signature])

  const linked = useMemo(() => participantsOfStudent(view.participants, row.student_id), [view.participants, row.student_id])
  const seen = row.first_join_at !== null
  const late = formatLate(row.late_seconds)
  const early = formatEarlyLeave(row.early_leave_seconds)
  // Leaving early is only known once the lesson is over (the server computes it at the end).
  const lessonFinished = view.lesson.status === 'finished'

  return (
    <>
      <SheetHeader className="gap-2 border-b pr-12">
        <SheetTitle className="text-lg leading-snug font-semibold">{row.full_name}</SheetTitle>
        <SheetDescription>Посещаемость на занятии «{view.lesson.title}»</SheetDescription>
        <div className="flex flex-wrap items-center gap-2">
          <StatusBadge kind="current" status={row.current_status} />
          {row.devices_online > 1 && (
            <span className="text-xs text-muted-foreground">{pluralize(row.devices_online, WORDS.device)}</span>
          )}
          <FinalStatusBadge status={row.final_status} manual={row.manual_status !== null} />
        </div>
      </SheetHeader>

      <Section title="Сводка" id={`${id}-summary`}>
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
          <Fact label="Время на занятии">
            <LiveDuration seconds={row.total_seconds} asOf={view.as_of} online={online} format="long" />
          </Fact>
          <Fact label="Первый вход">{row.first_join_at ? formatTime(row.first_join_at) : EMPTY}</Fact>
          <Fact label="Последний выход">
            {online ? 'сейчас в Zoom' : row.last_leave_at ? formatTime(row.last_leave_at) : EMPTY}
          </Fact>
          {lessonStarted && <Fact label="Опоздание">{seen ? late || 'нет' : EMPTY}</Fact>}
          {lessonFinished && seen && <Fact label="Ушёл раньше">{early || 'нет'}</Fact>}
        </dl>
      </Section>
      <Separator />

      <FinalStatusEditor lessonId={lessonId} row={row} />
      <Separator />

      <CommentEditor lessonId={lessonId} row={row} />
      <Separator />

      <Section title="Имена в Zoom" id={`${id}-names`}>
        {linked.length === 0 && row.zoom_names.length === 0 ? (
          <p className="text-sm text-muted-foreground">Студент ещё не появлялся в Zoom на этом занятии.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {linked.length > 0
              ? linked.map((p) => <LinkedParticipant key={p.id} lessonId={lessonId} participant={p} row={row} />)
              : row.zoom_names.map((name) => (
                  <li key={name} className="rounded-md bg-muted/60 px-3 py-2 text-sm">
                    «{name}»
                  </li>
                ))}
          </ul>
        )}
      </Section>
      <Separator />

      <Section title="Входы и выходы" id={`${id}-intervals`}>
        {history.data ? (
          <IntervalList intervals={history.data.intervals} asOf={view.as_of} />
        ) : history.isError ? (
          <ErrorState
            size="compact"
            error={history.error}
            onRetry={() => void history.refetch()}
            retrying={history.isFetching}
            title="Не удалось загрузить историю"
          />
        ) : (
          <LoadingState variant="list" rows={3} />
        )}
      </Section>
      <Separator />

      <Section title="История изменений" id={`${id}-audit`}>
        {history.data ? (
          history.data.audit.length === 0 ? (
            <p className="text-sm text-muted-foreground">Вручную ничего не меняли.</p>
          ) : (
            <ol className="flex flex-col gap-3">
              {history.data.audit.map((a) => (
                <li key={a.id} className="flex flex-col gap-0.5 text-sm">
                  <span className="text-xs text-muted-foreground">
                    {formatDateTime(a.at)} · {a.user_name || 'система'}
                  </span>
                  <span className="font-medium">{auditActionLabel(a.action)}</span>
                  <AuditDetails action={a.action} previous={a.previous_value} next={a.new_value} />
                  {a.reason && <span className="text-muted-foreground">Причина: {a.reason}</span>}
                </li>
              ))}
            </ol>
          )
        ) : history.isError ? null : (
          <LoadingState variant="list" rows={2} />
        )}
      </Section>
    </>
  )
}

/** What changed, in plain words (no internal ids). */
function AuditDetails({ action, previous, next }: { action: string; previous: string; next: string }) {
  if (action === 'participant.unmap') {
    // "было" names the student the decision was about; "стало" is the automatic matching.
    const before = auditText(previous)
    const after = auditText(next)
    if (!before && !after) return null
    return (
      <span className="flex flex-col break-words text-muted-foreground">
        {before && <span>Было: {before}</span>}
        {after && <span>Стало: {after}</span>}
      </span>
    )
  }
  if (action === 'attendance.status') {
    if (!previous && !next) return null
    return (
      <span className="text-muted-foreground">
        {auditValue(previous)} → {auditValue(next)}
      </span>
    )
  }
  if (action === 'attendance.comment' && !next.trim()) {
    return <span className="text-muted-foreground">Комментарий удалён</span>
  }
  // A removed name (e.g. together with a cancelled match) is only in the previous value.
  const text = auditText(next) || (action === 'alias.delete' ? auditText(previous) : '')
  return text ? <span className="break-words text-muted-foreground">{text}</span> : null
}

type Choice = 'auto' | AttendanceStatus

/** Icon colours of the table's «Был / Не был» switch. */
const choiceIconClass: Record<AttendanceStatus, string> = {
  present: 'size-4 text-emerald-700 dark:text-emerald-400',
  absent: 'size-4 text-red-700 dark:text-red-400',
}

function FinalStatusEditor({ lessonId, row }: { lessonId: number; row: StudentRow }) {
  const id = useId()
  const update = useUpdateAttendance(lessonId)
  const current: Choice = row.manual_status ?? 'auto'
  const [choice, setChoice] = useState<Choice>(current)
  const [reason, setReason] = useState('')
  // The mark can change while the sheet is open (live update, another teacher):
  // follow it unless the teacher has picked something else and not saved yet.
  const [synced, setSynced] = useState<Choice>(current)
  if (synced !== current) {
    setSynced(current)
    if (choice === synced) setChoice(current)
  }
  const autoLabel = finalStatusMeta[row.auto_status]?.label ?? row.auto_status

  const save = () => {
    update.mutate(
      { studentId: row.student_id, input: { status: choice === 'auto' ? null : choice, reason: reason.trim() || undefined } },
      {
        onSuccess: () => {
          setReason('')
          toast.success('Отметка сохранена')
        },
      },
    )
  }

  const options: { value: Choice; label: ReactNode }[] = [
    {
      value: 'auto',
      label: (
        <>
          Автоматически <span className="text-muted-foreground">(сейчас: {autoLabel})</span>
        </>
      ),
    },
    // Same words and icons as the «Был / Не был» switch in the table.
    ...(['present', 'absent'] as const).map((status) => {
      const meta = finalStatusMeta[status]
      const Icon = meta.icon
      return {
        value: status,
        label: (
          <>
            <Icon className={choiceIconClass[status]} aria-hidden="true" />
            {meta.label}
          </>
        ),
      }
    }),
  ]
  // Unlike the switch in the table, picking here saves nothing until the button is pressed
  // (the reason needs a submit step): say so, or the choice is lost when the card is closed.
  const unsaved = choice !== current && !update.isPending

  return (
    <Section title="Итог" id={`${id}-final`}>
      <RadioGroup value={choice} onValueChange={(v) => setChoice(v as Choice)} aria-labelledby={`${id}-final`} className="gap-2.5">
        {options.map((o) => (
          <div key={o.value} className="flex items-center gap-2.5">
            <RadioGroupItem id={`${id}-${o.value}`} value={o.value} />
            <Label htmlFor={`${id}-${o.value}`} className="cursor-pointer font-normal">
              {o.label}
            </Label>
          </div>
        ))}
      </RadioGroup>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-reason`} className="font-normal text-muted-foreground">
          Причина (необязательно)
        </Label>
        <Textarea
          id={`${id}-reason`}
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          maxLength={REASON_MAX}
          placeholder="Например: был на занятии с телефона под другим именем"
          className="min-h-12"
        />
      </div>
      <div className="flex flex-col gap-2">
        <Button className="w-fit" onClick={save} disabled={update.isPending || choice === current}>
          {update.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Сохранить отметку
        </Button>
        <p role="status" className="text-sm empty:hidden">
          {unsaved && (
            <span className="flex items-start gap-1.5 text-amber-800 dark:text-amber-300">
              <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
              Отметка ещё не сохранена — нажмите «Сохранить отметку».
            </span>
          )}
        </p>
      </div>
    </Section>
  )
}

function CommentEditor({ lessonId, row }: { lessonId: number; row: StudentRow }) {
  const id = useId()
  const update = useUpdateAttendance(lessonId)
  const [text, setText] = useState(row.comment)
  // The text of a save that failed: when the optimistic comment disappears again,
  // the teacher's typing must stay in the field for another try.
  const [failed, setFailed] = useState<string | null>(null)
  // Follow a comment changed elsewhere (row popover, another teacher) unless there is an unsaved edit.
  const [synced, setSynced] = useState(row.comment)
  if (synced !== row.comment) {
    setSynced(row.comment)
    if (text.trim() === synced.trim() && text.trim() !== failed) setText(row.comment)
  }
  const changed = text.trim() !== row.comment.trim()
  const save = () => {
    const comment = text.trim()
    setFailed(null)
    update.mutate(
      { studentId: row.student_id, input: { comment } },
      {
        onSuccess: () => toast.success(comment ? 'Комментарий сохранён' : 'Комментарий удалён'),
        onError: () => setFailed(comment),
      },
    )
  }
  return (
    <Section title="Комментарий" id={`${id}-comment-title`}>
      <Textarea
        id={`${id}-comment`}
        aria-labelledby={`${id}-comment-title`}
        value={text}
        onChange={(e) => setText(e.target.value)}
        maxLength={COMMENT_MAX}
        placeholder="Заметка для себя: например, предупредил, что опоздает"
      />
      <Button
        variant="outline"
        className="w-fit"
        disabled={!changed || update.isPending}
        onClick={save}
      >
        {update.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
        Сохранить комментарий
      </Button>
    </Section>
  )
}

function LinkedParticipant({ lessonId, participant, row }: { lessonId: number; participant: Participant; row: StudentRow }) {
  const unmap = useUnmapParticipant(lessonId)
  const map = useMapParticipant(lessonId)
  const manual = participant.resolution === 'manual'
  const name = participant.display_name
  // Matched through a remembered name (not the student's own name): it stays remembered for later lessons.
  const viaAlias = !manual && normalizeSearch(participant.display_name) !== normalizeSearch(row.full_name)
  const markNotThisStudent = () => map.mutateAsync({ participantId: participant.id, input: { action: 'none' } })
  const undo = async () => {
    let view: unknown
    if (manual) {
      // Removes the decision and the name remembered with it (the server does both).
      view = await unmap.mutateAsync(participant.id)
      const after = findParticipant(view, participant)
      // The name can still match this student on its own (e.g. it is the student's full name):
      // keep the promise of the dialog and mark it "not this student" for this lesson.
      if (after && after.student_id === row.student_id && (after.resolution === 'matched' || after.resolution === 'manual')) {
        view = await markNotThisStudent()
      }
    } else {
      // Automatic match → tell the system "this name is not him" on this lesson.
      view = await markNotThisStudent()
    }
    toast.success(unmatchOutcome(name, findParticipant(view, participant)))
  }

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-md bg-muted/60 px-3 py-2">
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="text-sm font-medium break-words">«{participant.display_name}»</span>
        <span className="flex flex-wrap items-center gap-2">
          <StatusBadge kind="resolution" status={participant.resolution} size="sm" />
          {participant.devices > 1 && (
            <span className="text-xs text-muted-foreground">{pluralize(participant.devices, WORDS.connection)}</span>
          )}
        </span>
      </div>
      <ConfirmDialog
        trigger={
          <Button variant="ghost" size="sm" className="text-muted-foreground">
            <Unlink aria-hidden="true" />
            Отменить сопоставление
          </Button>
        }
        title="Отменить сопоставление?"
        description={
          manual ? (
            <>
              Время участника «{name}» перестанет засчитываться студенту {row.full_name}, а участник появится в списке
              «Не идентифицированы». Имя «{name}» больше не будет автоматически относиться к студенту {row.full_name}.
            </>
          ) : (
            <>
              Время участника «{name}» перестанет засчитываться студенту {row.full_name}, а участник появится в списке
              «Не идентифицированы». На этом занятии это имя больше не будет узнаваться автоматически.
              {viaAlias &&
                ' Чтобы оно не узнавалось и на следующих занятиях, удалите это имя у студента на странице группы.'}
            </>
          )
        }
        confirmLabel="Отменить сопоставление"
        cancelLabel="Не отменять"
        destructive
        onConfirm={undo}
      />
    </li>
  )
}

/** Join–leave intervals with duration, Zoom name / device and leave reason. */
function IntervalList({ intervals, asOf }: { intervals: Interval[]; asOf: string }) {
  // Numbered only when the student really used several devices at once (a reconnect is not a device).
  const devices = useMemo(() => deviceNumbers(intervals, Date.parse(asOf)), [intervals, asOf])
  if (intervals.length === 0) {
    return (
      <EmptyState
        size="compact"
        icon={UserX}
        title="Входов в Zoom не было"
        text="Как только студент подключится, здесь появится время входа и выхода."
      />
    )
  }
  const asOfMs = Date.parse(asOf)
  const sorted = [...intervals].sort((a, b) => Date.parse(a.join_at) - Date.parse(b.join_at) || a.id - b.id)
  return (
    <ol className="flex flex-col divide-y rounded-md ring-1 ring-foreground/10">
      {sorted.map((iv) => {
        const open = iv.leave_at === null
        const openSeconds = open && !Number.isNaN(asOfMs) ? Math.max(0, (asOfMs - Date.parse(iv.join_at)) / 1000) : 0
        const reason = leaveReasonLabel(iv.leave_reason)
        const device = devices.get(iv.id)
        return (
          <li key={iv.id} className="flex flex-col gap-1 px-3 py-2 text-sm">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="flex items-center gap-1.5 font-medium tabular-nums">
                <LogIn className="size-3.5 text-muted-foreground" aria-hidden="true" />
                {formatTime(iv.join_at)} – {open ? 'сейчас' : formatTime(iv.leave_at)}
              </span>
              <span className="text-muted-foreground tabular-nums">
                {open ? (
                  <LiveDuration seconds={openSeconds} asOf={asOf} online format="long" />
                ) : (
                  formatDurationLong(iv.duration_seconds ?? 0)
                )}
              </span>
            </div>
            <div className="flex flex-wrap gap-x-2 text-xs text-muted-foreground">
              {iv.zoom_user_name && <span className="break-words">«{iv.zoom_user_name}»</span>}
              {device !== undefined && <span>устройство {device}</span>}
              {open ? (
                <span className="font-medium text-emerald-700 dark:text-emerald-400">сейчас в Zoom</span>
              ) : (
                reason && <span title={iv.leave_reason}>{reason}</span>
              )}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

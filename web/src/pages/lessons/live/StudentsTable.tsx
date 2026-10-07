import { memo, useCallback, useId, useMemo, useState, type FormEvent, type MouseEvent } from 'react'
import {
  Archive,
  CircleCheck,
  CircleX,
  Loader2,
  MessageSquarePlus,
  MessageSquareText,
  Search,
  SearchX,
  UserPlus,
  Users,
} from 'lucide-react'
import { Link } from 'react-router'
import { toast } from 'sonner'

import { EmptyState } from '@/components/EmptyState'
import { ManualMarker } from '@/components/FinalStatusBadge'
import { LiveDuration } from '@/components/LiveDuration'
import { finalStatusMeta } from '@/components/status-meta'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Textarea } from '@/components/ui/textarea'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { EMPTY, formatDurationLong, formatTime, pluralize, WORDS } from '@/lib/format'
import { useUpdateAttendance } from '@/lib/queries'
import type { AttendanceStatus, StudentRow } from '@/lib/types'
import { cn } from '@/lib/utils'
import {
  EMPTY_FILTER_TITLES,
  filterCounts,
  filterStudents,
  COMMENT_MAX,
  isStudentFilter,
  STUDENT_FILTERS,
  type StudentFilter,
} from '@/pages/lessons/live/liveText'

// ---------------------------------------------------------------------------
// Итог toggle
// ---------------------------------------------------------------------------

const toggleOnClass: Record<AttendanceStatus, string> = {
  present:
    'data-[state=on]:bg-emerald-50 data-[state=on]:text-emerald-800 data-[state=on]:border-emerald-600/40 dark:data-[state=on]:bg-emerald-400/10 dark:data-[state=on]:text-emerald-300',
  absent:
    'data-[state=on]:bg-red-50 data-[state=on]:text-red-800 data-[state=on]:border-red-600/40 dark:data-[state=on]:bg-red-400/10 dark:data-[state=on]:text-red-300',
}

export interface AttendanceToggleProps {
  name: string
  value: AttendanceStatus
  onChange: (value: AttendanceStatus) => void
  disabled?: boolean
  className?: string
}

/** Two-way "Был / Не был" switch of the final attendance status. */
export function AttendanceToggle({ name, value, onChange, disabled, className }: AttendanceToggleProps) {
  return (
    <ToggleGroup
      type="single"
      variant="outline"
      size="sm"
      spacing={0}
      value={value}
      disabled={disabled}
      onValueChange={(v) => {
        if ((v === 'present' || v === 'absent') && v !== value) onChange(v)
      }}
      aria-label={`Итог: ${name}`}
      className={className}
    >
      <ToggleGroupItem value="present" title={finalStatusMeta.present.description} className={cn('gap-1', toggleOnClass.present)}>
        <CircleCheck aria-hidden="true" />
        {finalStatusMeta.present.label}
      </ToggleGroupItem>
      <ToggleGroupItem value="absent" title={finalStatusMeta.absent.description} className={cn('gap-1', toggleOnClass.absent)}>
        <CircleX aria-hidden="true" />
        {finalStatusMeta.absent.label}
      </ToggleGroupItem>
    </ToggleGroup>
  )
}

// ---------------------------------------------------------------------------
// Comment popover
// ---------------------------------------------------------------------------

function CommentButton({
  row,
  onSave,
}: {
  row: StudentRow
  onSave: (studentId: number, comment: string) => Promise<unknown>
}) {
  const id = useId()
  const [open, setOpen] = useState(false)
  const [text, setText] = useState(row.comment)
  const [saving, setSaving] = useState(false)
  const has = row.comment.trim() !== ''

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setSaving(true)
    try {
      await onSave(row.student_id, text.trim())
      setOpen(false)
    } catch {
      // The error toast comes from the mutation layer; keep the text for another try.
    } finally {
      setSaving(false)
    }
  }

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        if (saving) return
        setOpen(o)
        if (o) setText(row.comment)
      }}
    >
      <PopoverTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="icon-lg"
          aria-label={has ? `Комментарий: ${row.full_name}` : `Добавить комментарий: ${row.full_name}`}
          title={has ? row.comment : 'Добавить комментарий'}
        >
          {has ? (
            <MessageSquareText className="text-sky-700 dark:text-sky-400" aria-hidden="true" />
          ) : (
            <MessageSquarePlus className="text-muted-foreground" aria-hidden="true" />
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80">
        <form onSubmit={submit} className="flex flex-col gap-2">
          <Label htmlFor={`${id}-comment`}>Комментарий · {row.full_name}</Label>
          <Textarea
            id={`${id}-comment`}
            autoFocus
            value={text}
            onChange={(e) => setText(e.target.value)}
            maxLength={COMMENT_MAX}
            placeholder="Например: предупредил, что опоздает"
            disabled={saving}
          />
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)} disabled={saving}>
              Отмена
            </Button>
            <Button type="submit" disabled={saving || text.trim() === row.comment.trim()}>
              {saving && <Loader2 className="animate-spin" aria-hidden="true" />}
              Сохранить
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  )
}

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

interface RowProps {
  row: StudentRow
  asOf: string
  onOpen: (studentId: number) => void
  onSetStatus: (row: StudentRow, status: AttendanceStatus | null) => void
  onSaveComment: (studentId: number, comment: string) => Promise<unknown>
}

const INTERACTIVE = 'button, a, input, textarea, select, label, [role="radio"], [role="radiogroup"]'

const StudentTableRow = memo(function StudentTableRow({ row, asOf, onOpen, onSetStatus, onSaveComment }: RowProps) {
  const online = row.current_status === 'online'
  const manual = row.manual_status !== null
  const late = row.late_seconds !== null && row.late_seconds >= 60 ? row.late_seconds : 0

  const onRowClick = (e: MouseEvent<HTMLTableRowElement>) => {
    // Clicks inside popovers bubble through the React tree; only real row clicks open the sheet.
    const target = e.target as HTMLElement
    if (!e.currentTarget.contains(target) || target.closest(INTERACTIVE)) return
    onOpen(row.student_id)
  }

  return (
    <tr
      data-student-id={row.student_id}
      data-status={row.current_status}
      onClick={onRowClick}
      className={cn(
        'cursor-pointer border-b transition-colors last:border-b-0 hover:bg-muted/50',
        online && 'bg-emerald-50/40 dark:bg-emerald-400/5',
        row.archived && 'text-muted-foreground',
      )}
    >
      <td className="px-2 py-2.5 align-middle lg:px-3">
        <div className="flex min-w-0 flex-col gap-0.5">
          <button
            type="button"
            onClick={() => onOpen(row.student_id)}
            className="w-fit rounded-sm text-left font-medium break-words outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
          >
            {row.full_name}
          </button>
          {row.archived && (
            <span className="flex items-center gap-1 text-xs text-muted-foreground">
              <Archive className="size-3" aria-hidden="true" />в архиве
            </span>
          )}
          {row.comment && (
            <span className="line-clamp-1 text-xs text-muted-foreground italic" title={row.comment}>
              {row.comment}
            </span>
          )}
        </div>
      </td>
      <td className="px-2 py-2.5 align-middle lg:px-3">
        <div className="flex flex-col items-start gap-1">
          <StatusBadge kind="current" status={row.current_status} />
          {row.devices_online > 1 && (
            <span className="text-xs text-muted-foreground">{pluralize(row.devices_online, WORDS.device)}</span>
          )}
        </div>
      </td>
      <td className="px-2 py-2.5 align-middle lg:px-3">
        <LiveDuration seconds={row.total_seconds} asOf={asOf} online={online} />
      </td>
      <td className="px-2 py-2.5 align-middle lg:px-3">
        <div className="flex flex-col">
          <span className="tabular-nums">{row.first_join_at ? formatTime(row.first_join_at) : EMPTY}</span>
          {late > 0 && (
            // Wraps under the time below lg, where every pixel of width counts.
            <span className="text-xs text-muted-foreground lg:whitespace-nowrap">опоздание {formatDurationLong(late)}</span>
          )}
        </div>
      </td>
      <td className="px-2 py-2.5 align-middle lg:px-3">
        <div className="flex flex-col items-start gap-1">
          <AttendanceToggle name={row.full_name} value={row.final_status} onChange={(v) => onSetStatus(row, v)} />
          {manual && (
            <span className="flex flex-wrap items-center gap-x-2 text-xs">
              <ManualMarker />
              <button
                type="button"
                onClick={() => onSetStatus(row, null)}
                title={`Автоматически: ${finalStatusMeta[row.auto_status]?.label ?? row.auto_status}`}
                aria-label={`Вернуть автоматическую отметку: ${row.full_name}`}
                className="rounded-sm text-primary underline-offset-2 outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
              >
                вернуть авто
              </button>
            </span>
          )}
        </div>
      </td>
      <td className="px-2 py-2.5 text-right align-middle">
        <CommentButton row={row} onSave={onSaveComment} />
      </td>
    </tr>
  )
})

// ---------------------------------------------------------------------------
// Table
// ---------------------------------------------------------------------------

export interface StudentsTableProps {
  lessonId: number
  groupId: number
  students: StudentRow[]
  asOf: string
  onOpenStudent: (studentId: number) => void
}

// The header sticks under the top bar from lg on. Below lg the cells are narrower so the
// table, comment button included, fits a 768 px tablet in portrait (720 px of content);
// sideways scrolling stays only as a fallback for phones and very long names.
const thClass =
  'h-11 px-2 text-left align-middle text-sm font-medium whitespace-nowrap text-muted-foreground lg:sticky lg:top-14 lg:z-10 lg:bg-card lg:px-3'

/** Student list of the live screen: quick filters, search, live rows. */
export function StudentsTable({ lessonId, groupId, students, asOf, onOpenStudent }: StudentsTableProps) {
  const [filter, setFilter] = useState<StudentFilter>('all')
  const [query, setQuery] = useState('')
  const statusMutation = useUpdateAttendance(lessonId)
  const commentMutation = useUpdateAttendance(lessonId)
  const setStatus = statusMutation.mutate
  const saveComment = commentMutation.mutateAsync

  const counts = useMemo(() => filterCounts(students), [students])
  const rows = useMemo(() => filterStudents(students, filter, query), [students, filter, query])

  const onSetStatus = useCallback(
    (row: StudentRow, status: AttendanceStatus | null) => {
      setStatus(
        { studentId: row.student_id, input: { status } },
        status === null
          ? { onSuccess: () => toast.success(`${row.full_name}: отметка снова ставится автоматически`) }
          : undefined,
      )
    },
    [setStatus],
  )

  const onSaveComment = useCallback(
    (studentId: number, comment: string) =>
      saveComment({ studentId, input: { comment } }).then(() => {
        toast.success(comment ? 'Комментарий сохранён' : 'Комментарий удалён')
      }),
    [saveComment],
  )

  if (students.length === 0) {
    return (
      <EmptyState
        icon={Users}
        title="В группе пока нет студентов"
        text="Добавьте список студентов в группу — и они появятся здесь, а система начнёт их узнавать в Zoom."
        action={
          <Button asChild>
            <Link to={`/groups/${groupId}`}>
              <UserPlus aria-hidden="true" />
              Добавить студентов
            </Link>
          </Button>
        }
      />
    )
  }

  const resetFilters = () => {
    setFilter('all')
    setQuery('')
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <ToggleGroup
          type="single"
          variant="outline"
          spacing={2}
          value={filter}
          onValueChange={(v) => {
            if (isStudentFilter(v)) setFilter(v)
          }}
          aria-label="Показать студентов"
          className="flex-wrap"
        >
          {STUDENT_FILTERS.map((f) => (
            <ToggleGroupItem
              key={f.value}
              value={f.value}
              className="h-9 rounded-full px-3 data-[state=on]:border-foreground/30 data-[state=on]:bg-muted data-[state=on]:font-semibold"
            >
              {f.label}{' '}
              {/* The space keeps the accessible name readable ("В Zoom 2", not "В Zoom2"). */}
              <span className="rounded-full bg-background/80 px-1.5 text-xs tabular-nums ring-1 ring-border">
                {counts[f.value]}
              </span>
            </ToggleGroupItem>
          ))}
        </ToggleGroup>
        <div className="relative w-full lg:max-w-xs">
          <Search
            className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Найти студента"
            aria-label="Найти студента"
            className="h-10 pl-9"
          />
        </div>
      </div>

      {rows.length === 0 ? (
        <EmptyState
          size="compact"
          icon={query.trim() ? SearchX : Users}
          title={query.trim() ? 'Никого не нашли' : EMPTY_FILTER_TITLES[filter]}
          text={query.trim() ? 'Проверьте, правильно ли написана фамилия.' : undefined}
          action={
            <Button variant="outline" onClick={resetFilters}>
              Показать всех
            </Button>
          }
          className="rounded-xl border border-dashed"
        />
      ) : (
        // `relative`: the sr-only header text is absolutely positioned; without a positioned
        // scroll box it escapes the box and widens the whole page on a phone.
        <div className="relative rounded-xl bg-card ring-1 ring-foreground/10 max-lg:overflow-x-auto lg:overflow-clip">
          <table className="w-full min-w-[40rem] text-sm">
            <caption className="sr-only">Студенты группы и их посещаемость</caption>
            <thead className="border-b">
              <tr>
                <th scope="col" className={cn(thClass, 'w-[32%] rounded-tl-xl')}>
                  ФИО
                </th>
                <th scope="col" className={thClass}>
                  Статус
                </th>
                <th scope="col" className={thClass}>
                  Время
                </th>
                <th scope="col" className={thClass}>
                  Первый вход
                </th>
                <th scope="col" className={thClass}>
                  Итог
                </th>
                <th scope="col" className={cn(thClass, 'w-12 rounded-tr-xl')}>
                  <span className="sr-only">Комментарий</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <StudentTableRow
                  key={row.student_id}
                  row={row}
                  asOf={asOf}
                  onOpen={onOpenStudent}
                  onSetStatus={onSetStatus}
                  onSaveComment={onSaveComment}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

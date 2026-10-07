import { memo, useCallback, useMemo, useState, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { format as fnsFormat } from 'date-fns'
import { ru } from 'date-fns/locale/ru'
import { ArrowRight, Check, Pencil, VideoOff, X } from 'lucide-react'

import { toneTextClass } from '@/components/tone'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  cellDescription,
  cellsByLesson,
  isWithoutZoomData,
  lessonTotals,
  nextCell,
  NO_ZOOM_DATA_TEXT,
  orderReportStudents,
  percentTone,
  studentTotals,
  type Totals,
} from '@/components/groups/report-utils'
import { formatDateTime, formatTime, toDate } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { GroupReportCell, GroupReportLesson, GroupReportStudent } from '@/lib/types'

export interface ReportMatrixProps {
  students: readonly GroupReportStudent[]
  /** Lesson columns in display order. */
  lessons: readonly GroupReportLesson[]
  /** "Now" of the page: planned lessons before it are marked as having no Zoom data. */
  now?: Date
}


const NAME_COL = 'w-40 min-w-40 max-w-40 sm:w-56 sm:min-w-56 sm:max-w-56'
// Sticky next to the name column from tablets up (the name column is 14rem there).
const TOTAL_COL = 'w-24 min-w-24 md:sticky md:left-56'

function shortDate(value: string): { day: string; weekday: string } {
  const d = toDate(value)
  if (!d) return { day: '—', weekday: '' }
  return { day: fnsFormat(d, 'dd.MM', { locale: ru }), weekday: fnsFormat(d, 'EEEEEE', { locale: ru }) }
}

function TotalsText({ totals }: { totals: Totals }) {
  return (
    <span className="flex flex-col items-end leading-tight">
      <span className={cn('font-semibold tabular-nums', toneTextClass[percentTone(totals.percent)])}>
        {totals.percent === null ? '—' : `${totals.percent}%`}
      </span>
      <span className="text-xs text-muted-foreground tabular-nums">
        {totals.attended} из {totals.total}
      </span>
    </span>
  )
}

interface CellProps {
  cell: GroupReportCell | undefined
  lesson: GroupReportLesson
  student: string
  row: number
  col: number
  /** The matrix's single Tab stop (roving tabindex); arrow keys move it. */
  tabStop: boolean
  onFocusCell: (row: number, col: number) => void
}

/**
 * One student × lesson cell. A button opening a popover, not a hover tooltip: details
 * (minutes in Zoom, lateness) must open with a tap on a tablet and with Enter on a keyboard.
 */
const Cell = memo(function Cell({ cell, lesson, student, row, col, tabStop, onFocusCell }: CellProps) {
  const description = cellDescription(cell)
  const status = cell?.final_status ?? null
  const when = formatDateTime(lesson.planned_start_at)
  return (
    <td
      className={cn(
        'h-11 border-b border-l p-0 text-center',
        status === 'present' && 'bg-emerald-50 text-emerald-700 dark:bg-emerald-400/10 dark:text-emerald-300',
        status === 'absent' && 'bg-red-50 text-red-700 dark:bg-red-400/10 dark:text-red-300',
      )}
      data-status={status ?? 'none'}
    >
      <Popover>
        <PopoverTrigger asChild>
          <button
            type="button"
            data-cell={`${row}:${col}`}
            tabIndex={tabStop ? 0 : -1}
            onFocus={() => onFocusCell(row, col)}
            aria-label={`${student}, ${lesson.title}, ${when}: ${description}`}
            // Keeps a cell reached with the arrow keys clear of the sticky header, names and totals.
            className="relative flex h-11 w-full min-w-11 scroll-mt-20 scroll-mb-14 scroll-ml-40 items-center justify-center outline-none hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset sm:scroll-ml-56 md:scroll-ml-80"
          >
            {status === 'present' ? (
              <Check className="size-4" aria-hidden="true" />
            ) : status === 'absent' ? (
              <X className="size-4" aria-hidden="true" />
            ) : (
              <span aria-hidden="true" className="text-muted-foreground">
                —
              </span>
            )}
            {cell?.manual && (
              <Pencil className="absolute top-1 right-1 size-2.5 opacity-80" aria-hidden="true" data-manual="true" />
            )}
          </button>
        </PopoverTrigger>
        <PopoverContent className="w-64 gap-1.5" align="center">
          <p className="font-medium break-words">{student}</p>
          <p className="break-words text-muted-foreground">
            {lesson.title}, {when}
          </p>
          <p>{description}</p>
          <Link
            to={`/lessons/${lesson.id}`}
            className="mt-1 inline-flex w-fit items-center gap-1 rounded-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
          >
            Открыть занятие
            <ArrowRight className="size-4" aria-hidden="true" />
          </Link>
        </PopoverContent>
      </Popover>
    </td>
  )
})

/** Students × lessons attendance matrix with sticky names/header and totals. */
export function ReportMatrix({ students, lessons, now }: ReportMatrixProps) {
  const [fallbackNow] = useState(() => new Date())
  const at = now ?? fallbackNow
  const rows = useMemo(
    () => orderReportStudents(students).map((student) => ({ student, cells: cellsByLesson(student) })),
    [students],
  )
  const totals = useMemo(() => lessonTotals(rows.map((r) => r.cells), lessons), [rows, lessons])

  // Roving tabindex: a 30 × 30 matrix would otherwise be 900 Tab stops. Tab enters the
  // grid at the last focused cell, the arrow keys move inside it.
  const [focused, setFocused] = useState<[number, number]>([0, 0])
  const active: [number, number] =
    focused[0] < rows.length && focused[1] < lessons.length ? focused : [0, 0]
  const onFocusCell = useCallback(
    (row: number, col: number) => setFocused((prev) => (prev[0] === row && prev[1] === col ? prev : [row, col])),
    [],
  )
  const onKeyDown = (e: KeyboardEvent<HTMLTableSectionElement>) => {
    const pos = (e.target as HTMLElement).dataset?.cell
    if (!pos || e.altKey || e.shiftKey || e.metaKey) return
    const [row, col] = pos.split(':').map(Number)
    const next = nextCell(e.key, [row, col], rows.length, lessons.length, e.ctrlKey)
    if (!next) return
    e.preventDefault()
    e.currentTarget.querySelector<HTMLElement>(`[data-cell="${next[0]}:${next[1]}"]`)?.focus()
  }

  return (
    <div
      className="relative max-h-[70svh] overflow-auto rounded-xl ring-1 ring-foreground/10"
      tabIndex={0}
      aria-label="Таблица посещаемости, прокручивается"
    >
      <table className="w-max min-w-full border-separate border-spacing-0 text-sm">
        <caption className="sr-only">Посещаемость: строки — студенты, столбцы — занятия</caption>
        <thead>
          <tr>
            <th
              scope="col"
              className={cn('sticky top-0 left-0 z-30 border-b bg-muted px-3 py-2 text-left align-bottom font-medium', NAME_COL)}
            >
              Студент
            </th>
            <th
              scope="col"
              className={cn('sticky top-0 z-30 border-b border-l bg-muted px-3 py-2 text-right align-bottom font-medium', TOTAL_COL)}
            >
              Итого
            </th>
            {lessons.map((lesson) => {
              const { day, weekday } = shortDate(lesson.planned_start_at)
              const noZoom = isWithoutZoomData(lesson, at)
              const label = `${lesson.title}, ${formatDateTime(lesson.planned_start_at)}${noZoom ? `. ${NO_ZOOM_DATA_TEXT}` : ''}`
              return (
                <th key={lesson.id} scope="col" className="sticky top-0 z-20 border-b border-l bg-muted p-0 align-bottom font-normal">
                  <Tooltip>
                    <TooltipTrigger asChild>
                      <Link
                        to={`/lessons/${lesson.id}`}
                        className="flex min-w-11 flex-col items-center px-1.5 py-2 leading-tight outline-none hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
                        aria-label={label}
                        data-no-zoom={noZoom || undefined}
                      >
                        <span className="text-xs text-muted-foreground">{weekday}</span>
                        <span className="font-medium tabular-nums">{day}</span>
                        <span className="text-xs text-muted-foreground tabular-nums">{formatTime(lesson.planned_start_at)}</span>
                        {noZoom && <VideoOff className="mt-0.5 size-3 text-muted-foreground" aria-hidden="true" />}
                      </Link>
                    </TooltipTrigger>
                    <TooltipContent className="flex-col items-start">
                      <span>
                        {lesson.title} — {formatDateTime(lesson.planned_start_at)}
                      </span>
                      {noZoom && <span>{NO_ZOOM_DATA_TEXT}</span>}
                    </TooltipContent>
                  </Tooltip>
                </th>
              )
            })}
          </tr>
        </thead>
        <tbody onKeyDown={onKeyDown}>
          {rows.map(({ student, cells }, rowIndex) => {
            const t = studentTotals(cells, lessons)
            return (
              <tr key={student.student_id} className="group/row">
                <th
                  scope="row"
                  className={cn(
                    'sticky left-0 z-10 border-b bg-background px-3 py-1.5 text-left font-normal group-hover/row:bg-muted',
                    NAME_COL,
                  )}
                >
                  <span className={cn('block truncate font-medium', student.archived && 'text-muted-foreground')} title={student.full_name}>
                    {student.full_name}
                  </span>
                  {(student.external_id || student.archived) && (
                    <span className="block truncate text-xs text-muted-foreground">
                      {[student.external_id && `ID ${student.external_id}`, student.archived && 'в архиве'].filter(Boolean).join(' · ')}
                    </span>
                  )}
                </th>
                <td className={cn('z-10 border-b border-l bg-background px-3 py-1.5 group-hover/row:bg-muted', TOTAL_COL)}>
                  <TotalsText totals={t} />
                </td>
                {lessons.map((lesson, colIndex) => (
                  <Cell
                    key={lesson.id}
                    cell={cells.get(lesson.id)}
                    lesson={lesson}
                    student={student.full_name}
                    row={rowIndex}
                    col={colIndex}
                    tabStop={active[0] === rowIndex && active[1] === colIndex}
                    onFocusCell={onFocusCell}
                  />
                ))}
              </tr>
            )
          })}
        </tbody>
        <tfoot>
          <tr>
            <th
              scope="row"
              className={cn('sticky bottom-0 left-0 z-30 border-t bg-muted px-3 py-2 text-left font-medium', NAME_COL)}
            >
              Присутствовали
            </th>
            <td className={cn('sticky bottom-0 z-30 border-t border-l bg-muted px-3 py-2', TOTAL_COL)}>
              <TotalsText totals={totals.overall} />
            </td>
            {totals.perLesson.map((t, i) => (
              <td key={lessons[i].id} className="sticky bottom-0 z-20 border-t border-l bg-muted px-1.5 py-2 text-center leading-tight">
                <span className="block font-semibold tabular-nums">{t.attended}</span>
                <span className="block text-xs text-muted-foreground tabular-nums">из {t.total}</span>
              </td>
            ))}
          </tr>
        </tfoot>
      </table>
    </div>
  )
}

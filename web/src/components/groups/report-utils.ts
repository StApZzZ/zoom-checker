// Group attendance report: period presets and the matrix arithmetic.

import { endOfDay, endOfMonth, isValid, parseISO, startOfDay, startOfMonth, subMonths } from 'date-fns'

import { formatDate, formatDurationLong, formatLate } from '@/lib/format'
import type {
  DateRange,
  GroupReport,
  GroupReportCell,
  GroupReportLesson,
  GroupReportStudent,
} from '@/lib/types'
import type { Tone } from '@/components/tone'

export type ReportPeriod = 'this_month' | 'last_month' | '3_months' | 'all' | 'custom'

export const PERIOD_OPTIONS: readonly { value: Exclude<ReportPeriod, 'custom'>; label: string }[] = [
  { value: 'this_month', label: 'Этот месяц' },
  { value: 'last_month', label: 'Прошлый месяц' },
  { value: '3_months', label: '3 месяца' },
  { value: 'all', label: 'Всё время' },
]

export const DEFAULT_PERIOD: ReportPeriod = 'all'

export function parsePeriod(value: string | null): ReportPeriod {
  const all: readonly string[] = ['this_month', 'last_month', '3_months', 'all', 'custom']
  return value && all.includes(value) ? (value as ReportPeriod) : DEFAULT_PERIOD
}

/** Local calendar bounds of a period; null = open end. */
export interface PeriodBounds {
  from: Date | null
  to: Date | null
}

function parseDay(value: string | null | undefined): Date | null {
  if (!value || !/^\d{4}-\d{2}-\d{2}$/.test(value)) return null
  const d = parseISO(value)
  return isValid(d) ? d : null
}

/**
 * Bounds of a period in the browser's time zone. "3 месяца" is the current
 * month and the two before it. Custom days are inclusive ("YYYY-MM-DD"); a
 * start typed after the end is swapped rather than sent as an invalid range.
 */
export function periodBounds(period: ReportPeriod, now: Date, custom: { from?: string | null; to?: string | null } = {}): PeriodBounds {
  switch (period) {
    case 'this_month':
      return { from: startOfMonth(now), to: endOfMonth(now) }
    case 'last_month': {
      const prev = subMonths(now, 1)
      return { from: startOfMonth(prev), to: endOfMonth(prev) }
    }
    case '3_months':
      return { from: startOfMonth(subMonths(now, 2)), to: endOfMonth(now) }
    case 'custom': {
      let from = parseDay(custom.from)
      let to = parseDay(custom.to)
      if (from && to && from > to) [from, to] = [to, from]
      return { from: from && startOfDay(from), to: to && endOfDay(to) }
    }
    default:
      return { from: null, to: null }
  }
}

/**
 * Start sent for a period without one ("Всё время", or "Свой период" with
 * only "По"). The server's own default is just ~4 months back, so an open
 * start must be sent explicitly.
 */
export const ALL_TIME_FROM = new Date(2000, 0, 1)

/**
 * API range (RFC 3339 instants; the upper bound is the last millisecond of the
 * last day). Both bounds are always sent: without them the server falls back
 * to a window around today (120 days back, 60 ahead) and "Всё время" would
 * silently lose older lessons. An open end stops at the end of today (or of
 * the start day when it lies ahead), since later lessons have no attendance.
 */
export function boundsToRange(bounds: PeriodBounds, now: Date = new Date()): DateRange {
  const from = bounds.from ?? ALL_TIME_FROM
  let to = bounds.to
  if (!to) {
    const today = endOfDay(now)
    to = from > today ? endOfDay(from) : today
  }
  return { from: from.toISOString(), to: to.toISOString() }
}

/**
 * Range of a download. A period without a start begins at the day of its
 * first lesson instead of ALL_TIME_FROM, so the file name the server builds
 * from the range shows real dates; the content is the same.
 */
export function exportRange(
  bounds: PeriodBounds,
  range: DateRange,
  lessons: readonly Pick<GroupReportLesson, 'planned_start_at'>[],
): DateRange {
  if (bounds.from) return range
  let first: number | null = null
  for (const lesson of lessons) {
    const t = Date.parse(lesson.planned_start_at)
    if (!Number.isNaN(t) && (first === null || t < first)) first = t
  }
  return first === null ? range : { ...range, from: startOfDay(new Date(first)).toISOString() }
}

/** "с 01.08.2026 по 31.10.2026", "по 31.10.2026", "за всё время". */
export function boundsLabel(bounds: PeriodBounds): string {
  if (bounds.from && bounds.to) return `с ${formatDate(bounds.from)} по ${formatDate(bounds.to)}`
  if (bounds.from) return `с ${formatDate(bounds.from)}`
  if (bounds.to) return `по ${formatDate(bounds.to)}`
  return 'за всё время'
}

// ---------------------------------------------------------------------------
// Matrix
// ---------------------------------------------------------------------------

/** A planned lesson that has not started yet ("впереди"). */
export function isUpcoming(lesson: Pick<GroupReportLesson, 'status' | 'planned_start_at'>, now: Date): boolean {
  return lesson.status === 'planned' && Date.parse(lesson.planned_start_at) > now.getTime()
}

/**
 * A planned lesson whose time has passed: Zoom sent nothing for it (wrong
 * meeting, or the lesson did not take place), so the server counts everybody
 * absent unless a teacher marked them.
 */
export function isWithoutZoomData(lesson: Pick<GroupReportLesson, 'status' | 'planned_start_at'>, now: Date): boolean {
  return lesson.status === 'planned' && !isUpcoming(lesson, now)
}

/** Column hint for a lesson without Zoom data (isWithoutZoomData). */
export const NO_ZOOM_DATA_TEXT = 'Из Zoom данных не было — все считаются отсутствующими, кроме отмеченных вручную'

/**
 * Lessons shown as columns. Upcoming lessons have no attendance yet and would
 * only add empty columns (a weekly series can reach months ahead), unless a
 * teacher already marked someone for them. Planned lessons in the past stay:
 * the server and the export count them (everybody absent, or manual marks),
 * so hiding them would make the totals disagree and lose manual marks.
 */
export function reportColumns(report: Pick<GroupReport, 'lessons' | 'students'>, now: Date = new Date()): GroupReportLesson[] {
  const marked = new Set<number>()
  for (const student of report.students)
    for (const cell of student.cells ?? []) if (cell.final_status !== null) marked.add(cell.lesson_id)
  return report.lessons.filter((l) => !isUpcoming(l, now) || marked.has(l.id))
}

/** Cell of a student for a lesson, looked up by lesson id (not by position). */
export function cellsByLesson(student: Pick<GroupReportStudent, 'cells'>): Map<number, GroupReportCell> {
  const map = new Map<number, GroupReportCell>()
  for (const cell of student.cells ?? []) map.set(cell.lesson_id, cell)
  return map
}

export interface Totals {
  attended: number
  /** Lessons with an attendance record. */
  total: number
  /** Rounded percent, null when there is nothing to count. */
  percent: number | null
}

export function percentOf(attended: number, total: number): number | null {
  return total > 0 ? Math.round((attended / total) * 100) : null
}

export function studentTotals(cells: ReadonlyMap<number, GroupReportCell>, columns: readonly GroupReportLesson[]): Totals {
  let attended = 0
  let total = 0
  for (const lesson of columns) {
    const status = cells.get(lesson.id)?.final_status
    if (status === 'present') attended++
    if (status === 'present' || status === 'absent') total++
  }
  return { attended, total, percent: percentOf(attended, total) }
}

/** Present / recorded per column, and overall. */
export function lessonTotals(
  rows: readonly ReadonlyMap<number, GroupReportCell>[],
  columns: readonly GroupReportLesson[],
): { perLesson: Totals[]; overall: Totals } {
  let allAttended = 0
  let allTotal = 0
  const perLesson = columns.map((lesson) => {
    let attended = 0
    let total = 0
    for (const cells of rows) {
      const status = cells.get(lesson.id)?.final_status
      if (status === 'present') attended++
      if (status === 'present' || status === 'absent') total++
    }
    allAttended += attended
    allTotal += total
    return { attended, total, percent: percentOf(attended, total) }
  })
  return { perLesson, overall: { attended: allAttended, total: allTotal, percent: percentOf(allAttended, allTotal) } }
}

/** Tone of an attendance percent (always shown together with the number). */
export function percentTone(percent: number | null): Tone {
  if (percent === null) return 'muted'
  if (percent >= 80) return 'success'
  if (percent >= 50) return 'warning'
  return 'danger'
}

/** "Был · 1 ч 24 мин · опоздал на 2 мин · отмечено вручную". */
export function cellDescription(cell: GroupReportCell | undefined): string {
  if (!cell || cell.final_status === null) return 'Нет данных — студента ещё не было в группе'
  const parts = [cell.final_status === 'present' ? 'Был' : 'Не был']
  if (cell.total_seconds > 0) parts.push(`в Zoom ${formatDurationLong(cell.total_seconds)}`)
  const late = formatLate(cell.late_seconds)
  if (late && cell.final_status === 'present') parts.push(late)
  if (cell.manual) parts.push('отмечено вручную')
  return parts.join(' · ')
}

/** Active students first, archived ones last; otherwise the server's order. */
export function orderReportStudents(students: readonly GroupReportStudent[]): GroupReportStudent[] {
  return students.filter((s) => !s.archived).concat(students.filter((s) => s.archived))
}

/** Where an arrow/Home/End key moves the focus inside a rows × cols grid, or null for other keys. */
export function nextCell(
  key: string,
  [row, col]: readonly [number, number],
  rows: number,
  cols: number,
  ctrl = false,
): [number, number] | null {
  switch (key) {
    case 'ArrowRight':
      return [row, Math.min(col + 1, cols - 1)]
    case 'ArrowLeft':
      return [row, Math.max(col - 1, 0)]
    case 'ArrowDown':
      return [Math.min(row + 1, rows - 1), col]
    case 'ArrowUp':
      return [Math.max(row - 1, 0), col]
    case 'Home':
      return ctrl ? [0, 0] : [row, 0]
    case 'End':
      return ctrl ? [rows - 1, cols - 1] : [row, cols - 1]
    default:
      return null
  }
}

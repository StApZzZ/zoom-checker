// Splitting the lesson list into "Сегодня" / "Предстоящие" / "Прошедшие".

import { addDays, endOfDay, isSameDay, startOfDay } from 'date-fns'

import { toDate } from '@/lib/format'
import type { LessonSummary } from '@/lib/types'

/** How far the "Предстоящие" section looks ahead. */
export const UPCOMING_DAYS = 14
/** How far back the "Прошедшие" section looks. */
export const PAST_DAYS = 30

/** Date window of the lessons request (stable for a whole day). */
export function listWindow(now: Date): { from: string; to: string } {
  return {
    from: startOfDay(addDays(now, -PAST_DAYS)).toISOString(),
    to: endOfDay(addDays(now, UPCOMING_DAYS)).toISOString(),
  }
}

/** A running lesson this long past its planned end probably lost Zoom's "meeting ended". */
export const OVERDUE_AFTER_MS = 60 * 60_000

/**
 * True for a lesson still «Идёт» more than an hour after its planned end: Zoom most
 * likely never reported that the meeting ended (the server finishes such a lesson
 * itself only after 6 quiet hours), so the teacher should finish it by hand.
 */
export function isOverdue(lesson: Pick<LessonSummary, 'status' | 'planned_end_at'>, now: number): boolean {
  if (lesson.status !== 'active') return false
  const end = Date.parse(lesson.planned_end_at)
  return Number.isFinite(end) && now - end > OVERDUE_AFTER_MS
}

/** Lower-case, "ё" → "е", collapsed spaces: for forgiving search. */
export function normalizeSearch(s: string): string {
  return s.toLocaleLowerCase('ru').replaceAll('ё', 'е').replace(/\s+/g, ' ').trim()
}

export interface LessonFilterState {
  groupId: number | null
  search: string
}

export function matchesLesson(lesson: LessonSummary, filter: LessonFilterState): boolean {
  if (filter.groupId !== null && lesson.group.id !== filter.groupId) return false
  const q = normalizeSearch(filter.search)
  if (!q) return true
  const hay = normalizeSearch(`${lesson.title} ${lesson.group.name} ${lesson.zoom_meeting_id}`)
  return q.split(' ').every((word) => hay.includes(word))
}

export interface LessonSections {
  today: LessonSummary[]
  upcoming: LessonSummary[]
  past: LessonSummary[]
}

const startMs = (l: LessonSummary) => toDate(l.planned_start_at)?.getTime() ?? 0

/**
 * Today = planned today, plus any lesson that is going on right now (even one
 * that started yesterday and was never finished). Upcoming = later days within
 * the window; past = earlier days, newest first.
 */
export function splitLessons(lessons: readonly LessonSummary[], now: Date): LessonSections {
  const today: LessonSummary[] = []
  const upcoming: LessonSummary[] = []
  const past: LessonSummary[] = []
  const todayStart = startOfDay(now).getTime()
  for (const lesson of lessons) {
    const start = toDate(lesson.planned_start_at)
    if (lesson.status === 'active' || (start && isSameDay(start, now))) today.push(lesson)
    else if (start && start.getTime() >= todayStart) upcoming.push(lesson)
    else past.push(lesson)
  }
  today.sort((a, b) => startMs(a) - startMs(b) || a.id - b.id)
  upcoming.sort((a, b) => startMs(a) - startMs(b) || a.id - b.id)
  past.sort((a, b) => startMs(b) - startMs(a) || b.id - a.id)
  return { today, upcoming, past }
}

// Which lessons to offer when a teacher links a Zoom session by hand.

import { addDays, endOfDay, isSameDay, startOfDay } from 'date-fns'

import { toDate } from '@/lib/format'
import type { LessonSummary, ZoomSession } from '@/lib/types'

/** How many days around the session date lessons with the same Zoom ID are offered. */
export const SAME_MEETING_DAYS = 3

/** When the session happened: Zoom's start time, else the first event we received. */
export function sessionTime(s: Pick<ZoomSession, 'started_at' | 'first_event_at'>): Date | null {
  return toDate(s.started_at) ?? toDate(s.first_event_at)
}

/** A session without an end that started less than this ago is probably still going. */
export const ONGOING_MS = 12 * 60 * 60 * 1000

/** Not ended and started less than ONGOING_MS ago: shown as «Идёт». */
export function isRecent(s: Pick<ZoomSession, 'started_at' | 'first_event_at'>, now: number): boolean {
  const start = sessionTime(s)
  return !!start && now - start.getTime() < ONGOING_MS
}

/** A conference opened up to this long before the planned start may still be the lesson. */
export const EARLY_START_MS = 60 * 60 * 1000

/**
 * Conferences without a lesson that may be this lesson under another meeting number
 * (a mistyped number, an instant meeting instead of the scheduled one): not linked,
 * not ended, started between an hour before the planned start and now, and recent
 * enough to be still going (isRecent). Closest to the planned start first.
 */
export function runningUnlinkedSessions(
  sessions: readonly ZoomSession[],
  plannedStartAt: string,
  now: number,
): ZoomSession[] {
  const planned = toDate(plannedStartAt)?.getTime()
  if (planned === undefined) return []
  const startOf = (s: ZoomSession) => sessionTime(s)?.getTime() ?? Number.NaN
  return sessions
    .filter((s) => {
      const start = startOf(s)
      return (
        s.lesson_id === null &&
        !s.ended_at &&
        start >= planned - EARLY_START_MS &&
        start <= now &&
        isRecent(s, now)
      )
    })
    .sort((a, b) => Math.abs(startOf(a) - planned) - Math.abs(startOf(b) - planned) || b.id - a.id)
}

/** Lesson list range to load for the dialog: ±SAME_MEETING_DAYS local days around the session. */
export function candidateRange(s: Pick<ZoomSession, 'started_at' | 'first_event_at'>): { from: string; to: string } | null {
  const at = sessionTime(s)
  if (!at) return null
  return {
    from: startOfDay(addDays(at, -SAME_MEETING_DAYS)).toISOString(),
    to: endOfDay(addDays(at, SAME_MEETING_DAYS)).toISOString(),
  }
}

export interface LinkCandidates {
  /** Lessons with the same Zoom Meeting ID, closest planned start first. */
  sameMeeting: LessonSummary[]
  /** Other lessons planned on the same day, by start time. */
  sameDay: LessonSummary[]
}

const norm = (s: string) => s.toLocaleLowerCase('ru').replaceAll('ё', 'е').replace(/\s+/g, ' ').trim()

/** Splits lessons into "same Zoom ID" and "same day" groups; `query` filters by title or group name. */
export function linkCandidates(
  session: Pick<ZoomSession, 'meeting_id' | 'started_at' | 'first_event_at'>,
  lessons: readonly LessonSummary[],
  query = '',
): LinkCandidates {
  const at = sessionTime(session)
  const ref = at?.getTime() ?? 0
  const q = norm(query)
  const matches = (l: LessonSummary) => !q || norm(l.title).includes(q) || norm(l.group?.name ?? '').includes(q)
  const start = (l: LessonSummary) => toDate(l.planned_start_at)?.getTime() ?? 0

  const sameMeeting = lessons
    .filter((l) => session.meeting_id !== '' && l.zoom_meeting_id === session.meeting_id && matches(l))
    .sort((a, b) => Math.abs(start(a) - ref) - Math.abs(start(b) - ref) || a.id - b.id)
  const taken = new Set(sameMeeting.map((l) => l.id))
  const sameDay = at
    ? lessons
        .filter((l) => !taken.has(l.id) && matches(l) && isSameDay(toDate(l.planned_start_at) ?? 0, at))
        .sort((a, b) => start(a) - start(b) || a.id - b.id)
    : []
  return { sameMeeting, sameDay }
}

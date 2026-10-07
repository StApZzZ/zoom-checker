import { describe, expect, it } from 'vitest'

import { isOverdue, listWindow, matchesLesson, normalizeSearch, splitLessons } from '@/pages/lessons/list/lessonSections'
import { makeSummary } from '@/pages/lessons/testing'
import type { LessonSummary } from '@/lib/types'

const at = (month: number, day: number, h: number, mi = 0) => new Date(2026, month - 1, day, h, mi).toISOString()

function lesson(id: number, start: string, overrides: Partial<LessonSummary> = {}): LessonSummary {
  return makeSummary({ id, planned_start_at: start, planned_end_at: start, status: 'planned', ...overrides })
}

describe('listWindow', () => {
  it('covers 30 days back and 14 days ahead, whole days', () => {
    const w = listWindow(new Date(2026, 9, 6, 17, 5))
    expect(w.from).toBe(new Date(2026, 8, 6, 0, 0).toISOString())
    expect(w.to).toBe(new Date(2026, 9, 20, 23, 59, 59, 999).toISOString())
  })
})

describe('normalizeSearch / matchesLesson', () => {
  it('ignores case, "ё" and extra spaces', () => {
    expect(normalizeSearch('  Пётр   ИВАНОВ ')).toBe('петр иванов')
  })

  const l = lesson(1, at(10, 6, 18), { title: 'Linux. Занятие №4', group: { id: 3, name: 'Группа Ёлка' } })

  it.each([
    ['', null, true],
    ['linux', null, true],
    ['елка №4', null, true],
    ['1234567890', null, true],
    ['linux python', null, false],
    ['', 3, true],
    ['', 2, false],
  ])('search %j, group %j → %s', (search, groupId, expected) => {
    expect(matchesLesson(l, { search, groupId })).toBe(expected)
  })
})

describe('splitLessons', () => {
  const now = new Date(2026, 9, 6, 17, 5)

  it('puts today and running lessons first, then upcoming by date, then past newest first', () => {
    const lessons = [
      lesson(1, at(10, 8, 18)),
      lesson(2, at(10, 6, 19)),
      lesson(3, at(10, 6, 9)),
      lesson(4, at(10, 1, 18), { status: 'finished' }),
      lesson(5, at(10, 3, 18), { status: 'finished' }),
      // Started yesterday and still running: it belongs to "today".
      lesson(6, at(10, 5, 23, 30), { status: 'active' }),
      lesson(7, at(10, 7, 0, 0)),
    ]
    const { today, upcoming, past } = splitLessons(lessons, now)
    expect(today.map((l) => l.id)).toEqual([6, 3, 2])
    expect(upcoming.map((l) => l.id)).toEqual([7, 1])
    expect(past.map((l) => l.id)).toEqual([5, 4])
  })

  it('treats midnight of today as today', () => {
    const { today } = splitLessons([lesson(1, at(10, 6, 0))], now)
    expect(today.map((l) => l.id)).toEqual([1])
  })
})

describe('isOverdue', () => {
  const end = '2026-10-06T16:30:00Z'
  const at = (iso: string) => Date.parse(iso)
  it.each([
    ['active, an hour and a minute after the end', 'active', '2026-10-06T17:31:00Z', true],
    ['active, exactly an hour after the end', 'active', '2026-10-06T17:30:00Z', false],
    ['active, during the lesson', 'active', '2026-10-06T16:00:00Z', false],
    ['finished long ago', 'finished', '2026-10-07T12:00:00Z', false],
    ['planned and never held', 'planned', '2026-10-07T12:00:00Z', false],
  ] as const)('%s → %s', (_name, status, now, expected) => {
    expect(isOverdue({ status, planned_end_at: end }, at(now))).toBe(expected)
  })
})

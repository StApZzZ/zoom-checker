import { describe, expect, it } from 'vitest'

import {
  EMPTY,
  formatDate,
  formatDateTime,
  formatDuration,
  formatDurationClock,
  formatDurationLong,
  formatEarlyLeave,
  formatLate,
  formatRelative,
  formatTime,
  formatTimeRange,
  formatWeekdayDate,
  fromDateTimeInputs,
  browserTimeZone,
  utcOffsetLabel,
  utcOffsetMinutes,
  zonesDiffer,
  initials,
  plural,
  pluralize,
  toDate,
  toDateInputValue,
  toTimeInputValue,
  WORDS,
} from '@/lib/format'

// Local-time fixture: 6 Oct 2026 (Tuesday) 18:02:07.
const local = new Date(2026, 9, 6, 18, 2, 7)

describe('toDate', () => {
  it.each([
    [null, null],
    [undefined, null],
    ['', null],
    ['not a date', null],
  ])('returns null for %j', (input, expected) => {
    expect(toDate(input)).toBe(expected)
  })

  it('parses RFC 3339, unix ms and Date', () => {
    expect(toDate('2026-10-06T15:02:07Z')?.toISOString()).toBe('2026-10-06T15:02:07.000Z')
    expect(toDate(local.getTime())?.getTime()).toBe(local.getTime())
    expect(toDate(local)).toBe(local)
  })
})

describe('date formatting', () => {
  it('formats date, time and date-time', () => {
    expect(formatDate(local)).toBe('06.10.2026')
    expect(formatTime(local)).toBe('18:02')
    expect(formatDateTime(local)).toBe('06.10.2026 18:02')
  })

  it('formats API timestamps in local time', () => {
    const iso = local.toISOString()
    expect(formatTime(iso)).toBe('18:02')
    expect(formatDate(iso)).toBe('06.10.2026')
  })

  it('returns a dash for missing values', () => {
    expect(formatDate(null)).toBe(EMPTY)
    expect(formatTime(undefined)).toBe(EMPTY)
    expect(formatDateTime('garbage')).toBe(EMPTY)
    expect(formatWeekdayDate(null)).toBe(EMPTY)
  })

  it('formats weekday dates in Russian, adding the year only when it differs', () => {
    expect(formatWeekdayDate(local, new Date(2026, 0, 1))).toBe('вт, 6 октября')
    expect(formatWeekdayDate(new Date(2027, 0, 4), new Date(2026, 9, 6))).toBe('пн, 4 января 2027')
    expect(formatWeekdayDate(new Date(2026, 4, 1), new Date(2026, 9, 6))).toBe('пт, 1 мая')
  })

  it('formats time ranges', () => {
    expect(formatTimeRange(new Date(2026, 9, 6, 18, 0), new Date(2026, 9, 6, 19, 30))).toBe('18:00–19:30')
    expect(formatTimeRange(new Date(2026, 9, 6, 18, 0), null)).toBe('18:00')
    expect(formatTimeRange(null, null)).toBe(EMPTY)
  })
})

describe('durations', () => {
  it.each([
    [0, '00:00'],
    [59, '00:00'],
    [60, '00:01'],
    [4440, '01:14'],
    [4499, '01:14'],
    [36000, '10:00'],
    [360000, '100:00'],
    [-5, '00:00'],
    [null, '00:00'],
    [Number.NaN, '00:00'],
  ])('formatDuration(%s) = %s', (s, expected) => {
    expect(formatDuration(s)).toBe(expected)
  })

  it('formats clock durations with seconds', () => {
    expect(formatDurationClock(4445)).toBe('01:14:05')
    expect(formatDurationClock(0)).toBe('00:00:00')
  })

  it.each([
    [0, '0 мин'],
    [30, '0 мин'],
    [2700, '45 мин'],
    [3600, '1 ч'],
    [4440, '1 ч 14 мин'],
    [7260, '2 ч 1 мин'],
    [null, '0 мин'],
  ])('formatDurationLong(%s) = %s', (s, expected) => {
    expect(formatDurationLong(s)).toBe(expected)
  })

  it('formats lateness, ignoring less than a minute', () => {
    expect(formatLate(720)).toBe('опоздал на 12 мин')
    expect(formatLate(3900)).toBe('опоздал на 1 ч 5 мин')
    expect(formatLate(59)).toBe('')
    expect(formatLate(0)).toBe('')
    expect(formatLate(null)).toBe('')
  })

  it('formats early leave', () => {
    expect(formatEarlyLeave(600)).toBe('ушёл раньше на 10 мин')
    expect(formatEarlyLeave(null)).toBe('')
  })

  // Whole minutes are always rounded down, like a clock drops seconds. The Excel/CSV
  // export must use the same rule (internal/export Minutes, TestMinutes has the same
  // table), or the file and the screen disagree by a minute for about half the students.
  it.each([
    [0, '0 мин', '00:00'],
    [29, '0 мин', '00:00'],
    [30, '0 мин', '00:00'],
    [45, '0 мин', '00:00'],
    [49, '0 мин', '00:00'],
    [59, '0 мин', '00:00'],
    [60, '1 мин', '00:01'],
    [89, '1 мин', '00:01'],
    [90, '1 мин', '00:01'],
    [96, '1 мин', '00:01'],
    [5069, '1 ч 24 мин', '01:24'],
    [5070, '1 ч 24 мин', '01:24'],
    [-90, '0 мин', '00:00'],
  ])('rounds %s s down to whole minutes: %s / %s', (s, long, short) => {
    expect(formatDurationLong(s)).toBe(long)
    expect(formatDuration(s)).toBe(short)
  })

  it('treats lateness and early leave under a minute as none, and drops the seconds above it', () => {
    expect(formatLate(45)).toBe('')
    expect(formatLate(96)).toBe('опоздал на 1 мин')
    expect(formatEarlyLeave(59)).toBe('')
    expect(formatEarlyLeave(119)).toBe('ушёл раньше на 1 мин')
  })
})

describe('formatRelative', () => {
  const now = new Date(2026, 9, 6, 18, 0, 0)
  it('says "только что" for very recent times', () => {
    expect(formatRelative(new Date(2026, 9, 6, 17, 59, 40), now)).toBe('только что')
  })
  it('uses Russian relative phrases', () => {
    expect(formatRelative(new Date(2026, 9, 6, 17, 55), now)).toBe('5 минут назад')
    expect(formatRelative(new Date(2026, 9, 6, 20, 0), now)).toBe('через 2 часа')
  })
  it('returns a dash for missing values', () => {
    expect(formatRelative(null, now)).toBe(EMPTY)
  })
})

describe('plural', () => {
  it.each([
    [0, 'студентов'],
    [1, 'студент'],
    [2, 'студента'],
    [4, 'студента'],
    [5, 'студентов'],
    [11, 'студентов'],
    [12, 'студентов'],
    [14, 'студентов'],
    [21, 'студент'],
    [22, 'студента'],
    [25, 'студентов'],
    [101, 'студент'],
    [111, 'студентов'],
    [112, 'студентов'],
    [-1, 'студент'],
  ])('%i → %s', (n, expected) => {
    expect(plural(n, WORDS.student)).toBe(expected)
  })

  it('pluralize prefixes the number', () => {
    expect(pluralize(3, WORDS.lesson)).toBe('3 занятия')
    expect(pluralize(25, ['студент', 'студента', 'студентов'])).toBe('25 студентов')
  })
})

describe('input helpers', () => {
  it('produces input values in local time', () => {
    expect(toDateInputValue(local)).toBe('2026-10-06')
    expect(toTimeInputValue(local)).toBe('18:02')
    expect(toDateInputValue(null)).toBe('')
  })

  it('combines date and time inputs into an ISO string', () => {
    expect(fromDateTimeInputs('2026-10-06', '18:00')).toBe(new Date(2026, 9, 6, 18, 0).toISOString())
    expect(fromDateTimeInputs(' 2026-10-06 ', '9:05')).toBe(new Date(2026, 9, 6, 9, 5).toISOString())
  })

  it.each([
    ['', '18:00'],
    ['2026-10-06', ''],
    ['2026-02-30', '18:00'],
    ['2026-10-06', '24:00'],
    ['06.10.2026', '18:00'],
  ])('rejects invalid input %j %j', (d, t) => {
    expect(fromDateTimeInputs(d, t)).toBeNull()
  })
})

describe('initials', () => {
  it.each([
    ['Мария Иванова', 'МИ'],
    ['admin', 'AD'],
    ['  ', '?'],
    ['Иван Петрович Сидоров', 'ИП'],
  ])('%j → %s', (name, expected) => {
    expect(initials(name)).toBe(expected)
  })
})

describe('fromDateTimeInputs with seconds (review)', () => {
  it('accepts "HH:mm:ss" from time inputs with a small step', () => {
    expect(fromDateTimeInputs('2026-10-06', '18:00:30')).toBe(new Date(2026, 9, 6, 18, 0, 30).toISOString())
    expect(fromDateTimeInputs('2026-10-06', '18:00:30.250')).toBe(new Date(2026, 9, 6, 18, 0, 30).toISOString())
  })

  it('rejects out-of-range seconds', () => {
    expect(fromDateTimeInputs('2026-10-06', '18:00:60')).toBeNull()
  })
})

describe('time zones', () => {
  const at = '2026-10-06T23:59:00Z'

  it.each([
    ['Europe/Moscow', 180, 'UTC+3'],
    ['Asia/Yekaterinburg', 300, 'UTC+5'],
    ['Asia/Novosibirsk', 420, 'UTC+7'],
    ['Asia/Kolkata', 330, 'UTC+5:30'],
    ['America/St_Johns', -150, 'UTC−2:30'],
    ['America/Sao_Paulo', -180, 'UTC−3'],
    ['UTC', 0, 'UTC'],
  ])('%s is %s min (%s)', (zone, minutes, label) => {
    expect(utcOffsetMinutes(zone, at)).toBe(minutes)
    expect(utcOffsetLabel(zone, at)).toBe(label)
  })

  it('follows daylight saving time at the given moment', () => {
    expect(utcOffsetLabel('Europe/Berlin', '2026-07-01T12:00:00Z')).toBe('UTC+2')
    expect(utcOffsetLabel('Europe/Berlin', '2026-12-01T12:00:00Z')).toBe('UTC+1')
  })

  it('returns nothing for an unknown zone or date', () => {
    expect(utcOffsetMinutes('Mars/Olympus', at)).toBeNull()
    expect(utcOffsetLabel('Mars/Olympus', at)).toBe('')
    expect(utcOffsetLabel('', at)).toBe('')
    expect(utcOffsetMinutes('Europe/Moscow', 'не дата')).toBeNull()
  })

  it('compares offsets, not names', () => {
    expect(zonesDiffer('Europe/Moscow', at, 'Asia/Yekaterinburg')).toBe(true)
    expect(zonesDiffer('Europe/Moscow', at, 'Europe/Moscow')).toBe(false)
    expect(zonesDiffer('Europe/Moscow', at, 'Europe/Simferopol')).toBe(false)
    // Unknown on either side: nothing to say.
    expect(zonesDiffer(undefined, at, 'Asia/Yekaterinburg')).toBe(false)
    expect(zonesDiffer('Europe/Moscow', at, '')).toBe(false)
    expect(zonesDiffer('Mars/Olympus', at, 'Asia/Yekaterinburg')).toBe(false)
  })

  it('knows the browser zone', () => {
    expect(browserTimeZone()).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone)
    expect(utcOffsetLabel(browserTimeZone(), at)).not.toBe('')
  })
})

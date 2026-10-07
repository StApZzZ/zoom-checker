import { describe, expect, it } from 'vitest'

import {
  defaultSchedule,
  endTimeHint,
  firstInvalidField,
  nextHalfHour,
  parseWholeNumber,
  plannedRange,
  scheduleFromLesson,
  serverFieldErrors,
  seriesTitle,
  seriesTitlesPreview,
  suggestTitle,
  validateLessonForm,
  weeklyPreview,
  type LessonFormValues,
} from '@/components/lessons/lessonForm'

const local = (y: number, mo: number, d: number, h = 0, mi = 0) => new Date(y, mo - 1, d, h, mi)

describe('nextHalfHour', () => {
  it.each([
    [local(2026, 10, 6, 17, 5), local(2026, 10, 6, 17, 30)],
    [local(2026, 10, 6, 17, 29), local(2026, 10, 6, 17, 30)],
    [local(2026, 10, 6, 17, 30), local(2026, 10, 6, 18, 0)],
    [local(2026, 10, 6, 17, 59), local(2026, 10, 6, 18, 0)],
    [local(2026, 10, 6, 23, 45), local(2026, 10, 7, 0, 0)],
  ])('%s → %s', (now, expected) => {
    expect(nextHalfHour(now).getTime()).toBe(expected.getTime())
  })

  it('moves the default date to tomorrow late in the evening', () => {
    expect(defaultSchedule(local(2026, 10, 6, 23, 40), 60)).toEqual({ date: '2026-10-07', time: '00:00', duration: '60' })
    expect(defaultSchedule(local(2026, 10, 6, 17, 5))).toEqual({ date: '2026-10-06', time: '17:30', duration: '90' })
  })
})

describe('suggestTitle / parseWholeNumber', () => {
  it('numbers lessons from the count of existing ones', () => {
    expect(suggestTitle(0)).toBe('Занятие №1')
    expect(suggestTitle(4)).toBe('Занятие №5')
  })

  it('parses only whole numbers', () => {
    expect(parseWholeNumber(' 12 ')).toBe(12)
    expect(parseWholeNumber('12a')).toBeNull()
    expect(parseWholeNumber('1.5')).toBeNull()
    expect(parseWholeNumber('')).toBeNull()
    expect(parseWholeNumber('-3')).toBeNull()
  })
})

describe('plannedRange / endTimeHint / scheduleFromLesson', () => {
  it('builds UTC start and end from local inputs', () => {
    const range = plannedRange({ date: '2026-10-06', time: '18:00', duration: '90' })
    expect(range).toEqual({
      start: local(2026, 10, 6, 18, 0).toISOString(),
      end: local(2026, 10, 6, 19, 30).toISOString(),
    })
    expect(endTimeHint({ date: '2026-10-06', time: '18:00', duration: '90' })).toBe('Окончание в 19:30')
    expect(endTimeHint({ date: '2026-10-06', time: '23:30', duration: '60' })).toBe('Окончание в 00:30 07.10')
  })

  it('rejects incomplete or out-of-range schedules', () => {
    expect(plannedRange({ date: '', time: '18:00', duration: '90' })).toBeNull()
    expect(plannedRange({ date: '2026-10-06', time: '18:00', duration: '2' })).toBeNull()
    expect(plannedRange({ date: '2026-10-06', time: '18:00', duration: '9999' })).toBeNull()
    expect(endTimeHint({ date: '2026-10-06', time: '', duration: '90' })).toBe('')
  })

  it('reads the schedule of an existing lesson', () => {
    const start = local(2026, 10, 6, 18, 0).toISOString()
    const end = local(2026, 10, 6, 19, 30).toISOString()
    expect(scheduleFromLesson(start, end)).toEqual({ date: '2026-10-06', time: '18:00', duration: '90' })
  })
})

describe('weeklyPreview', () => {
  it('shows the number of lessons and the date range', () => {
    expect(weeklyPreview('2026-10-06', '18:00', '12')).toBe('Будет создано 12 занятий: 06.10 – 22.12')
    expect(weeklyPreview('2026-10-06', '18:00', '2')).toBe('Будет создано 2 занятия: 06.10 – 13.10')
  })

  it('adds the year when the series crosses New Year', () => {
    expect(weeklyPreview('2026-12-22', '18:00', '3')).toBe('Будет создано 3 занятия: 22.12.2026 – 05.01.2027')
  })

  it('is empty for invalid input', () => {
    expect(weeklyPreview('2026-10-06', '18:00', '1')).toBeNull()
    expect(weeklyPreview('2026-10-06', '18:00', '53')).toBeNull()
    expect(weeklyPreview('', '18:00', '12')).toBeNull()
  })
})

describe('validateLessonForm', () => {
  const valid: LessonFormValues = {
    groupId: '2',
    title: 'Занятие №1',
    zoom: 'https://zoom.us/j/1234567890',
    date: '2026-10-06',
    time: '18:00',
    duration: '90',
    repeat: false,
    weeks: '12',
  }

  it('accepts a complete form', () => {
    expect(validateLessonForm(valid, { requireGroup: true })).toEqual({})
  })

  it('explains every missing field in Russian', () => {
    const errors = validateLessonForm(
      { ...valid, groupId: '', title: ' ', zoom: '', date: '', time: '', duration: 'abc', repeat: true, weeks: '60' },
      { requireGroup: true },
    )
    expect(errors).toEqual({
      groupId: 'Выберите группу',
      title: 'Введите название занятия',
      zoom: 'Вставьте ссылку на Zoom или номер конференции',
      date: 'Укажите дату занятия',
      time: 'Укажите время начала',
      duration: 'Укажите длительность от 5 до 720 минут',
      weeks: 'Укажите от 2 до 52 недель',
    })
    expect(firstInvalidField(errors)).toBe('groupId')
  })

  it('uses the live Zoom check message and skips the group on edit', () => {
    const errors = validateLessonForm({ ...valid, groupId: '' }, { requireGroup: false, zoomError: 'Не нашли номер' })
    expect(errors).toEqual({ zoom: 'Не нашли номер' })
  })
})

describe('serverFieldErrors', () => {
  it('maps known server fields to form fields', () => {
    expect(serverFieldErrors({ field: 'zoom' }, 'Плохая ссылка')).toEqual({ zoom: 'Плохая ссылка' })
    expect(serverFieldErrors({ fields: { title: 'Пусто', planned_start_at: 'Неверно', other: 'x' } }, '')).toEqual({
      title: 'Пусто',
      date: 'Неверно',
    })
    expect(serverFieldErrors({ field: '__proto__' }, 'x')).toEqual({})
  })
})

describe('seriesTitle (mirrors the server numbering of a weekly series)', () => {
  it.each([
    ['Занятие №4', 0, 'Занятие №4'],
    ['Занятие №4', 1, 'Занятие №5'],
    ['Занятие №4', 11, 'Занятие №15'],
    ['Урок 09', 1, 'Урок 10'],
    ['Урок 09', 0, 'Урок 09'],
    ['Урок 007', 3, 'Урок 010'],
    ['Linux 9', 1, 'Linux 10'],
    ['Вводное', 5, 'Вводное'],
    ['  Занятие №1  ', 2, 'Занятие №3'],
  ])('%j, week %i → %j', (title, i, expected) => {
    expect(seriesTitle(title, i)).toBe(expected)
  })
})

describe('seriesTitlesPreview', () => {
  it('shows the first and the last title of a numbered series', () => {
    expect(seriesTitlesPreview('Занятие №5', '12')).toBe('Названия по порядку: «Занятие №5» … «Занятие №16»')
  })

  it('stays silent when the titles do not change or the input is invalid', () => {
    expect(seriesTitlesPreview('Вводное', '12')).toBeNull()
    expect(seriesTitlesPreview('Занятие №5', '1')).toBeNull()
    expect(seriesTitlesPreview('', '4')).toBeNull()
    expect(seriesTitlesPreview('Занятие №5', 'abc')).toBeNull()
  })
})

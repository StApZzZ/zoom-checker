// Pure helpers of the create/edit lesson forms: defaults, validation and the
// weekly-series preview. Kept free of React so they are easy to test.

import { addDays, addMinutes, differenceInMinutes, format } from 'date-fns'

import { fromDateTimeInputs, pluralize, toDate, toDateInputValue, toTimeInputValue, WORDS } from '@/lib/format'

/** Quick duration chips (minutes). */
export const DURATION_PRESETS = [45, 60, 90, 120] as const
/** Used when the group has no default duration. */
export const DEFAULT_DURATION_MINUTES = 90
export const MIN_DURATION_MINUTES = 5
export const MAX_DURATION_MINUTES = 720
export const MIN_WEEKS = 2
export const MAX_WEEKS = 52
export const DEFAULT_WEEKS = 12

/** Next round half hour strictly after `now`: 17:05 → 17:30, 17:30 → 18:00. */
export function nextHalfHour(now: Date): Date {
  const d = new Date(now)
  d.setSeconds(0, 0)
  d.setMinutes(d.getMinutes() < 30 ? 30 : 60)
  return d
}

/** "Занятие №5" for a group that already has 4 lessons. */
export function suggestTitle(existingLessons: number): string {
  return `Занятие №${Math.max(0, existingLessons) + 1}`
}

/** Strict positive integer parse of a text field ("12" → 12, "12a" / "" → null). */
export function parseWholeNumber(value: string): number | null {
  const s = value.trim()
  if (!/^\d{1,6}$/.test(s)) return null
  return Number(s)
}

/** Fields shared by the create and edit forms. */
export interface ScheduleValues {
  /** "YYYY-MM-DD" */
  date: string
  /** "HH:mm" */
  time: string
  /** Minutes as typed. */
  duration: string
}

export interface LessonFormValues extends ScheduleValues {
  groupId: string
  title: string
  zoom: string
  repeat: boolean
  weeks: string
}

export type LessonFormField = keyof LessonFormValues
export type LessonFormErrors = Partial<Record<LessonFormField, string>>

/** Default schedule for a new lesson: today (or tomorrow after 23:30), next half hour. */
export function defaultSchedule(now: Date, durationMinutes: number = DEFAULT_DURATION_MINUTES): ScheduleValues {
  const start = nextHalfHour(now)
  return { date: toDateInputValue(start), time: toTimeInputValue(start), duration: String(durationMinutes) }
}

/** Schedule fields of an existing lesson (edit form). */
export function scheduleFromLesson(startIso: string, endIso: string): ScheduleValues {
  const start = toDate(startIso)
  const end = toDate(endIso)
  const minutes = start && end ? differenceInMinutes(end, start) : DEFAULT_DURATION_MINUTES
  return {
    date: toDateInputValue(start),
    time: toTimeInputValue(start),
    duration: String(minutes > 0 ? minutes : DEFAULT_DURATION_MINUTES),
  }
}

/** Planned start/end (RFC 3339 UTC) of the schedule; null when it is incomplete or invalid. */
export function plannedRange(values: ScheduleValues): { start: string; end: string } | null {
  const start = fromDateTimeInputs(values.date, values.time)
  const minutes = parseWholeNumber(values.duration)
  if (!start || minutes === null || minutes < MIN_DURATION_MINUTES || minutes > MAX_DURATION_MINUTES) return null
  return { start, end: addMinutes(new Date(start), minutes).toISOString() }
}

/** "до 19:30" hint under the schedule; empty when the schedule is invalid. */
export function endTimeHint(values: ScheduleValues): string {
  const range = plannedRange(values)
  if (!range) return ''
  const start = new Date(range.start)
  const end = new Date(range.end)
  const sameDay = toDateInputValue(start) === toDateInputValue(end)
  return `Окончание в ${format(end, sameDay ? 'HH:mm' : 'HH:mm dd.MM')}`
}

/** Validates the date/time/duration part. */
export function validateSchedule(values: ScheduleValues): LessonFormErrors {
  const errors: LessonFormErrors = {}
  if (!values.date.trim()) errors.date = 'Укажите дату занятия'
  if (!values.time.trim()) errors.time = 'Укажите время начала'
  if (!errors.date && !errors.time && !fromDateTimeInputs(values.date, values.time)) {
    errors.date = 'Проверьте дату и время'
  }
  const minutes = parseWholeNumber(values.duration)
  if (minutes === null || minutes < MIN_DURATION_MINUTES || minutes > MAX_DURATION_MINUTES) {
    errors.duration = `Укажите длительность от ${MIN_DURATION_MINUTES} до ${MAX_DURATION_MINUTES} минут`
  }
  return errors
}

export interface ValidateOptions {
  /** Create form: a group must be chosen. */
  requireGroup: boolean
  /** Message of the live Zoom check when it rejected the input. */
  zoomError?: string | null
}

/** Validates the whole lesson form; an empty object means "ok". */
export function validateLessonForm(values: LessonFormValues, opts: ValidateOptions): LessonFormErrors {
  const errors: LessonFormErrors = {}
  if (opts.requireGroup && !values.groupId) errors.groupId = 'Выберите группу'
  if (!values.title.trim()) errors.title = 'Введите название занятия'
  else if (values.title.trim().length > 200) errors.title = 'Название слишком длинное — до 200 символов'
  if (!values.zoom.trim()) errors.zoom = 'Вставьте ссылку на Zoom или номер конференции'
  else if (opts.zoomError) errors.zoom = opts.zoomError
  Object.assign(errors, validateSchedule(values))
  if (values.repeat) {
    const weeks = parseWholeNumber(values.weeks)
    if (weeks === null || weeks < MIN_WEEKS || weeks > MAX_WEEKS) {
      errors.weeks = `Укажите от ${MIN_WEEKS} до ${MAX_WEEKS} недель`
    }
  }
  return errors
}

/** aria props linking a control to its hint / error text. */
export function describedBy(id: string, error: string | undefined, hasHint = false) {
  return {
    'aria-invalid': error ? true : undefined,
    'aria-describedby': error ? `${id}-error` : hasHint ? `${id}-hint` : undefined,
  } as const
}

/** Field order for focusing the first invalid field. */
export const FIELD_ORDER: readonly LessonFormField[] = [
  'groupId',
  'title',
  'zoom',
  'date',
  'time',
  'duration',
  'repeat',
  'weeks',
]

export function firstInvalidField(errors: LessonFormErrors): LessonFormField | undefined {
  return FIELD_ORDER.find((f) => errors[f])
}

/** Dates of a weekly series starting at `start`. */
export function weeklyDates(start: Date, weeks: number): Date[] {
  return Array.from({ length: Math.max(0, weeks) }, (_, i) => addDays(start, 7 * i))
}

/**
 * "Будет создано 12 занятий: 06.10 – 22.12" (the year is added when the series
 * crosses New Year). Null when the date or the number of weeks is invalid.
 */
export function weeklyPreview(date: string, time: string, weeksText: string): string | null {
  const weeks = parseWholeNumber(weeksText)
  if (weeks === null || weeks < MIN_WEEKS || weeks > MAX_WEEKS) return null
  const iso = fromDateTimeInputs(date, time || '12:00')
  if (!iso) return null
  const dates = weeklyDates(new Date(iso), weeks)
  const first = dates[0]
  const last = dates[dates.length - 1]
  const pattern = first.getFullYear() === last.getFullYear() ? 'dd.MM' : 'dd.MM.yyyy'
  return `Будет создано ${pluralize(weeks, WORDS.lesson)}: ${format(first, pattern)} – ${format(last, pattern)}`
}

/**
 * Title of occurrence `i` (0-based) of a weekly series, as the server names it
 * (internal/httpapi lessonSeriesTitle): a number at the end of the title grows by
 * one per week, keeping zero padding ("Занятие №4" → "Занятие №5"; "Урок 09" → "Урок 10").
 */
export function seriesTitle(title: string, i: number): string {
  const t = title.trim()
  if (i === 0) return t
  const m = /^(.*?)(\d{1,9})$/.exec(t)
  if (!m) return t
  const digits = m[2]
  let num = String(Number(digits) + i)
  if (digits.length > 1 && digits.startsWith('0') && num.length < digits.length) num = num.padStart(digits.length, '0')
  return m[1] + num
}

/**
 * "Названия по порядку: «Занятие №5» … «Занятие №16»" for a numbered title, so the
 * teacher is not surprised by the numbering; null when the titles stay the same
 * or the input is invalid.
 */
export function seriesTitlesPreview(title: string, weeksText: string): string | null {
  const weeks = parseWholeNumber(weeksText)
  const t = title.trim()
  if (!t || weeks === null || weeks < MIN_WEEKS || weeks > MAX_WEEKS) return null
  const first = seriesTitle(t, 0)
  const last = seriesTitle(t, weeks - 1)
  if (first === last) return null
  return `Названия по порядку: «${first}» … «${last}»`
}

/** Maps server field names (422 details) to form fields. */
const SERVER_FIELDS: Readonly<Record<string, LessonFormField>> = {
  group_id: 'groupId',
  title: 'title',
  zoom: 'zoom',
  zoom_meeting_id: 'zoom',
  planned_start_at: 'date',
  planned_end_at: 'duration',
  repeat_weekly: 'weeks',
}

/**
 * Field errors from a 422 envelope. Accepts `details.field` + message or
 * `details.fields: {name: message}`; anything else stays a form-level error.
 */
export function serverFieldErrors(details: Record<string, unknown>, message: string): LessonFormErrors {
  const out: LessonFormErrors = {}
  const field = details.field
  if (typeof field === 'string' && Object.hasOwn(SERVER_FIELDS, field)) out[SERVER_FIELDS[field]] = message
  const fields = details.fields
  if (fields && typeof fields === 'object') {
    for (const [name, text] of Object.entries(fields as Record<string, unknown>)) {
      if (Object.hasOwn(SERVER_FIELDS, name) && typeof text === 'string' && text.trim()) {
        out[SERVER_FIELDS[name]] = text
      }
    }
  }
  return out
}

// Russian formatting helpers. All dates are shown in the browser's local time zone.

import { format as fnsFormat, formatDistanceStrict, isValid, parseISO } from 'date-fns'
import { ru } from 'date-fns/locale/ru'

export type DateInput = string | number | Date | null | undefined

/** Placeholder shown instead of a missing value. */
export const EMPTY = '—'

/** Parses API timestamps (RFC 3339), unix ms or Date; returns null for missing/invalid input. */
export function toDate(value: DateInput): Date | null {
  if (value === null || value === undefined || value === '') return null
  const d = value instanceof Date ? value : typeof value === 'number' ? new Date(value) : parseISO(value)
  return isValid(d) ? d : null
}

function fmt(value: DateInput, pattern: string): string {
  const d = toDate(value)
  return d ? fnsFormat(d, pattern, { locale: ru }) : EMPTY
}

/** "06.10.2026" */
export const formatDate = (value: DateInput): string => fmt(value, 'dd.MM.yyyy')

/** "18:02" */
export const formatTime = (value: DateInput): string => fmt(value, 'HH:mm')

/** "06.10.2026 18:02" */
export const formatDateTime = (value: DateInput): string => fmt(value, 'dd.MM.yyyy HH:mm')

/** "18:02:07" — for event journals where seconds matter. */
export const formatTimeWithSeconds = (value: DateInput): string => fmt(value, 'HH:mm:ss')

/**
 * "вт, 6 октября". The year is appended when it differs from `now`'s year
 * ("пн, 5 января 2027") so old/future lessons are not ambiguous.
 */
export function formatWeekdayDate(value: DateInput, now: Date = new Date()): string {
  const d = toDate(value)
  if (!d) return EMPTY
  const pattern = d.getFullYear() === now.getFullYear() ? 'EEEEEE, d MMMM' : 'EEEEEE, d MMMM yyyy'
  return fnsFormat(d, pattern, { locale: ru })
}

/** "18:00–19:30" for a planned lesson slot. */
export function formatTimeRange(start: DateInput, end: DateInput): string {
  const s = toDate(start)
  const e = toDate(end)
  if (!s) return EMPTY
  if (!e) return formatTime(s)
  return `${formatTime(s)}–${formatTime(e)}`
}

const toWholeSeconds = (seconds: number | null | undefined): number =>
  seconds === null || seconds === undefined || !Number.isFinite(seconds) ? 0 : Math.max(0, Math.floor(seconds))

const pad2 = (n: number) => String(n).padStart(2, '0')

/** Duration as "hh:mm" (01:14). Hours are not capped: 100 h → "100:00". */
export function formatDuration(seconds: number | null | undefined): string {
  const total = toWholeSeconds(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return `${pad2(hours)}:${pad2(minutes)}`
}

/** Duration as "hh:mm:ss" (01:14:05) — for live counters that tick every second. */
export function formatDurationClock(seconds: number | null | undefined): string {
  const total = toWholeSeconds(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  return `${pad2(hours)}:${pad2(minutes)}:${pad2(total % 60)}`
}

/** "1 ч 14 мин", "2 ч", "45 мин", "0 мин". Seconds are dropped (rounded down). */
export function formatDurationLong(seconds: number | null | undefined): string {
  const total = toWholeSeconds(seconds)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (hours === 0) return `${minutes} мин`
  if (minutes === 0) return `${hours} ч`
  return `${hours} ч ${minutes} мин`
}

/** "опоздал на 12 мин"; empty string when not late (or less than a minute late). */
export function formatLate(seconds: number | null | undefined): string {
  if (toWholeSeconds(seconds) < 60) return ''
  return `опоздал на ${formatDurationLong(seconds)}`
}

/** "ушёл раньше на 10 мин"; empty string when the student stayed till the end. */
export function formatEarlyLeave(seconds: number | null | undefined): string {
  if (toWholeSeconds(seconds) < 60) return ''
  return `ушёл раньше на ${formatDurationLong(seconds)}`
}

/** "5 минут назад" / "через 2 часа"; `now` is injectable for live clocks and tests. */
export function formatRelative(value: DateInput, now: Date | number = Date.now()): string {
  const d = toDate(value)
  if (!d) return EMPTY
  const base = typeof now === 'number' ? new Date(now) : now
  if (Math.abs(d.getTime() - base.getTime()) < 45_000) return 'только что'
  return formatDistanceStrict(d, base, { locale: ru, addSuffix: true })
}

// ---------------------------------------------------------------------------
// Pluralization
// ---------------------------------------------------------------------------

/** Russian noun forms: [one, few, many] — e.g. ['студент', 'студента', 'студентов']. */
export type PluralForms = readonly [one: string, few: string, many: string]

/** Picks the Russian plural form for n: 1 студент, 2 студента, 5 студентов, 21 студент, 11 студентов. */
export function plural(n: number, forms: PluralForms): string {
  const abs = Math.abs(Math.trunc(n))
  const mod10 = abs % 10
  const mod100 = abs % 100
  if (mod10 === 1 && mod100 !== 11) return forms[0]
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return forms[1]
  return forms[2]
}

/** "21 студент", "3 занятия". */
export function pluralize(n: number, forms: PluralForms): string {
  return `${n} ${plural(n, forms)}`
}

/** Common noun forms used across the UI. */
export const WORDS = {
  student: ['студент', 'студента', 'студентов'],
  lesson: ['занятие', 'занятия', 'занятий'],
  group: ['группа', 'группы', 'групп'],
  participant: ['участник', 'участника', 'участников'],
  minute: ['минута', 'минуты', 'минут'],
  hour: ['час', 'часа', 'часов'],
  event: ['событие', 'события', 'событий'],
  row: ['строка', 'строки', 'строк'],
  device: ['устройство', 'устройства', 'устройств'],
  /** Joins to Zoom: a reconnect is a new connection, not a new device. */
  connection: ['подключение', 'подключения', 'подключений'],
  week: ['неделя', 'недели', 'недель'],
} as const satisfies Record<string, PluralForms>

// ---------------------------------------------------------------------------
// Time zones: the screen uses the browser's zone, the server (exports, its own
// messages) uses TIMEZONE. When they differ the UI says so.
// ---------------------------------------------------------------------------

/** IANA name of the browser's time zone ("Asia/Yekaterinburg"); '' when unknown. */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? ''
  } catch {
    return ''
  }
}

/** UTC offset of `zone` at `at` in minutes (+300 for UTC+5), or null for an unknown zone. */
export function utcOffsetMinutes(zone: string, at: DateInput = new Date()): number | null {
  const d = toDate(at)
  if (!zone || !d) return null
  let name: string | undefined
  try {
    name = new Intl.DateTimeFormat('en-US', { timeZone: zone, timeZoneName: 'longOffset' })
      .formatToParts(d)
      .find((p) => p.type === 'timeZoneName')?.value
  } catch {
    return null // RangeError: not a time zone this browser knows
  }
  if (!name) return null
  if (name === 'GMT' || name === 'UTC') return 0
  const m = /^(?:GMT|UTC)([+\-−])(\d{1,2}):?(\d{2})?$/.exec(name)
  if (!m) return null
  const minutes = Number(m[2]) * 60 + Number(m[3] ?? 0)
  return m[1] === '+' ? minutes : -minutes
}

/** "UTC+5", "UTC+5:30", "UTC−3", "UTC" for `zone` at `at`; '' for an unknown zone. */
export function utcOffsetLabel(zone: string, at: DateInput = new Date()): string {
  const offset = utcOffsetMinutes(zone, at)
  if (offset === null) return ''
  if (offset === 0) return 'UTC'
  const abs = Math.abs(offset)
  const hm = abs % 60 === 0 ? String(abs / 60) : `${Math.floor(abs / 60)}:${pad2(abs % 60)}`
  return `UTC${offset > 0 ? '+' : '−'}${hm}`
}

/**
 * True when clocks in `serverZone` and `browserZone` show different times at `at`
 * (offsets are compared, not names: Europe/Moscow and Europe/Simferopol agree).
 * False when either zone is unknown — then nothing can be said.
 */
export function zonesDiffer(serverZone: string | null | undefined, at: DateInput = new Date(), browserZone = browserTimeZone()): boolean {
  if (!serverZone || !browserZone) return false
  const server = utcOffsetMinutes(serverZone, at)
  const browser = utcOffsetMinutes(browserZone, at)
  return server !== null && browser !== null && server !== browser
}

// ---------------------------------------------------------------------------
// <input type="date|time"> helpers (local time)
// ---------------------------------------------------------------------------

/** "2026-10-06" for <input type="date">. */
export const toDateInputValue = (value: DateInput): string => {
  const d = toDate(value)
  return d ? fnsFormat(d, 'yyyy-MM-dd') : ''
}

/** "18:00" for <input type="time">. */
export const toTimeInputValue = (value: DateInput): string => {
  const d = toDate(value)
  return d ? fnsFormat(d, 'HH:mm') : ''
}

/**
 * Combines local "YYYY-MM-DD" and "HH:mm" input values into an RFC 3339 UTC
 * string for the API. Returns null when either part is missing or invalid.
 */
export function fromDateTimeInputs(date: string, time: string): string | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date.trim())
  // <input type="time"> yields "HH:mm", or "HH:mm:ss" when its step is below a minute.
  const t = /^(\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?$/.exec(time.trim())
  if (!m || !t) return null
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])]
  const [h, mi, sec] = [Number(t[1]), Number(t[2]), Number(t[3] ?? 0)]
  if (h > 23 || mi > 59 || sec > 59) return null
  const local = new Date(y, mo - 1, d, h, mi, sec, 0)
  if (!isValid(local) || local.getMonth() !== mo - 1 || local.getDate() !== d) return null
  return local.toISOString()
}

/**
 * Zoom Meeting ID grouped like Zoom shows it (mirrors names.FormatMeetingID):
 * 9 digits 3-3-3, 10 digits 3-3-4, 11 digits 3-4-4. Anything else is returned as is.
 */
export function formatMeetingId(id: string | null | undefined): string {
  const digits = (id ?? '').trim()
  if (!/^\d+$/.test(digits)) return digits || EMPTY
  switch (digits.length) {
    case 9:
    case 10:
      return `${digits.slice(0, 3)} ${digits.slice(3, 6)} ${digits.slice(6)}`
    case 11:
      return `${digits.slice(0, 3)} ${digits.slice(3, 7)} ${digits.slice(7)}`
    default:
      return digits
  }
}

/** Avatar initials: "Иван Петров" → "ИП", "admin" → "AD". */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[1][0]).toUpperCase()
}

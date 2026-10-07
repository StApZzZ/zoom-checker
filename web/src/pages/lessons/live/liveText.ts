// Wording and pure list logic of the live lesson screen.

import { normalizeSearch } from '@/pages/lessons/list/lessonSections'
import {
  isLessonView,
  type AuditEntry,
  type CurrentStatus,
  type IgnoreReason,
  type Interval,
  type Lesson,
  type Participant,
  type StudentRow,
} from '@/lib/types'

/** Max length of a teacher comment (the server's limit). */
export const COMMENT_MAX = 2000
/** Max length of the reason of a manual change (the server's limit). */
export const REASON_MAX = 500

// ---------------------------------------------------------------------------
// Student filters
// ---------------------------------------------------------------------------

export type StudentFilter = 'all' | CurrentStatus

export const STUDENT_FILTERS: readonly { value: StudentFilter; label: string }[] = [
  { value: 'all', label: 'Все' },
  { value: 'online', label: 'В Zoom' },
  { value: 'left', label: 'Вышли' },
  { value: 'not_seen', label: 'Не подключались' },
]

export function isStudentFilter(v: string): v is StudentFilter {
  return STUDENT_FILTERS.some((f) => f.value === v)
}

/** Number of rows per quick filter chip. */
export function filterCounts(rows: readonly StudentRow[]): Record<StudentFilter, number> {
  const counts: Record<StudentFilter, number> = { all: rows.length, online: 0, left: 0, not_seen: 0 }
  for (const r of rows) {
    if (Object.hasOwn(counts, r.current_status)) counts[r.current_status] += 1
  }
  return counts
}

/** Name / Zoom name search ("ё" = "е", every word must match). */
export function matchesStudent(row: StudentRow, query: string): boolean {
  const q = normalizeSearch(query)
  if (!q) return true
  const hay = normalizeSearch([row.full_name, row.external_id, ...row.zoom_names].join(' '))
  return q.split(' ').every((w) => hay.includes(w))
}

export function filterStudents(rows: readonly StudentRow[], filter: StudentFilter, query: string): StudentRow[] {
  return rows.filter((r) => (filter === 'all' || r.current_status === filter) && matchesStudent(r, query))
}

/** What an empty quick filter means, in plain words (no search text typed). */
export const EMPTY_FILTER_TITLES: Readonly<Record<StudentFilter, string>> = {
  all: 'В списке пока никого нет',
  online: 'Сейчас в Zoom никого нет',
  left: 'Пока никто не выходил из Zoom',
  not_seen: 'Все студенты уже подключались 👍',
}

// ---------------------------------------------------------------------------
// Participants
// ---------------------------------------------------------------------------

export interface ParticipantGroups {
  /** Need a teacher decision ("Не идентифицированы"). */
  unresolved: Participant[]
  /** Not counted ("Скрытые участники"). */
  hidden: Participant[]
}

/** Online first, then by first join, then by name: stable while the lesson goes on. */
function participantOrder(a: Participant, b: Participant): number {
  if (a.current_status !== b.current_status) return a.current_status === 'online' ? -1 : 1
  const ja = a.first_join_at ? Date.parse(a.first_join_at) : Infinity
  const jb = b.first_join_at ? Date.parse(b.first_join_at) : Infinity
  if (ja !== jb) return ja - jb
  return a.display_name.localeCompare(b.display_name, 'ru') || a.id - b.id
}

export function groupParticipants(participants: readonly Participant[]): ParticipantGroups {
  const unresolved: Participant[] = []
  const hidden: Participant[] = []
  for (const p of participants) {
    if (p.resolution === 'unidentified' || p.resolution === 'conflict') unresolved.push(p)
    else if (p.resolution === 'ignored') hidden.push(p)
  }
  unresolved.sort(participantOrder)
  hidden.sort(participantOrder)
  return { unresolved, hidden }
}

/** Zoom participants whose time counts for this student (auto or manual match). */
export function participantsOfStudent(participants: readonly Participant[], studentId: number): Participant[] {
  return participants
    .filter((p) => p.student_id === studentId && (p.resolution === 'matched' || p.resolution === 'manual'))
    .sort(participantOrder)
}

/** Teacher-facing reason of a hidden participant. */
export const hiddenReasonLabels: Readonly<Record<Exclude<IgnoreReason, ''>, string>> = {
  host: 'организатор',
  list: 'из списка исключений',
  manual: 'скрыт вручную',
}

export function hiddenReasonLabel(reason: IgnoreReason): string {
  return reason && Object.hasOwn(hiddenReasonLabels, reason) ? hiddenReasonLabels[reason as Exclude<IgnoreReason, ''>] : 'скрыт'
}

// ---------------------------------------------------------------------------
// Results of participant decisions (toasts)
// ---------------------------------------------------------------------------

/** The participant in a fresh LessonView: same id, else the same Zoom name (rows are per name). */
export function findParticipant(
  view: unknown,
  p: Pick<Participant, 'id' | 'normalized_name'>,
): Participant | undefined {
  if (!isLessonView(view)) return undefined
  return view.participants.find((x) => x.id === p.id) ?? view.participants.find((x) => x.normalized_name === p.normalized_name)
}

/** True when the participant's time counts for a student. */
function isCounted(p: Participant): boolean {
  return p.resolution === 'matched' || p.resolution === 'manual'
}

/** Why a participant stays hidden, as the end of a sentence. */
function stillHiddenReason(reason: IgnoreReason): string {
  switch (reason) {
    case 'list':
      return 'это имя есть в общем списке скрываемых имён. Убрать его оттуда может администратор в настройках.'
    case 'host':
      return 'это организатор конференции.'
    default:
      return 'попробуйте ещё раз.'
  }
}

export interface DecisionOutcome {
  /** false = the participant is still not where the teacher wanted it (error toast). */
  ok: boolean
  message: string
}

/** Toast after "Вернуть" on a hidden participant, from where the participant really ended up. */
export function restoreOutcome(name: string, after: Participant | undefined): DecisionOutcome {
  if (!after) return { ok: true, message: `«${name}» больше не скрыт` }
  if (after.resolution === 'ignored') return { ok: false, message: `«${name}» по-прежнему скрыт: ${stillHiddenReason(after.ignore_reason)}` }
  if (isCounted(after)) {
    return {
      ok: true,
      message: after.student_name
        ? `«${name}» снова учитывается: время засчитано студенту ${after.student_name}`
        : `«${name}» снова учитывается`,
    }
  }
  return { ok: true, message: `«${name}» больше не скрыт — укажите, кто это, в блоке «Не идентифицированы»` }
}

/** Toast after "Отменить сопоставление" in the student card. */
export function unmatchOutcome(name: string, after: Participant | undefined): string {
  if (!after) return `Сопоставление отменено: «${name}»`
  if (after.resolution === 'ignored') return `Сопоставление отменено: «${name}» скрыт (${hiddenReasonLabel(after.ignore_reason)})`
  if (isCounted(after) && after.student_name) {
    return `Сопоставление отменено: «${name}» теперь относится к студенту ${after.student_name}`
  }
  return `Сопоставление отменено: «${name}» теперь в блоке «Не идентифицированы»`
}

/** Toast after "Вернуть как было" on an unidentified name (the teacher's earlier decision is removed). */
export function undoOutcome(name: string, after: Participant | undefined): string {
  if (after && isCounted(after) && after.student_name) {
    return `«${name}» снова узнаётся автоматически: ${after.student_name}`
  }
  if (after?.resolution === 'ignored') return `«${name}» снова скрыт (${hiddenReasonLabel(after.ignore_reason)})`
  return `Решение по «${name}» отменено`
}

// ---------------------------------------------------------------------------
// Intervals & audit
// ---------------------------------------------------------------------------

/** Codes written by the attendance engine when it closes an interval itself. */
const ENGINE_LEAVE_CODES: Readonly<Record<string, string>> = {
  meeting_ended: 'конференция завершилась',
  lesson_finished: 'занятие завершено вручную',
  renamed: 'сменил имя',
}

/**
 * Zoom's leave texts, most specific first. The processor stores the part after
 * "Reason :" ("Host ended the meeting.", "leave breakout room"); older rows may
 * hold the whole text ("left the meeting. Reason : …").
 */
const ZOOM_LEAVE_REASONS: readonly [RegExp, string][] = [
  [/host (has )?(ended|closed) the meeting|meeting (has )?(ended|closed)/i, 'организатор завершил конференцию'],
  [/removed by (the )?host|host removed/i, 'удалён организатором'],
  [/disconnect|network|connection (was )?lost|lost connection|connection error/i, 'обрыв связи'],
  // "leave breakout room" closes the breakout part only: the student went back to the main room or left.
  [/\b(leave|left|leaving) (the )?breakout room/i, 'вышел из комнаты для групповой работы'],
  [/breakout room/i, 'перешёл в комнату для групповой работы'],
  [/\b(leave|left|leaving) (the )?waiting room/i, 'вышел из зала ожидания'],
  [/waiting room/i, 'перемещён в зал ожидания'],
  // Also "left the meeting with Unknown Failover Reason".
  [/\bunknown\b/i, 'причина неизвестна'],
  [/left the meeting|^left\b/i, 'вышел сам'],
]

/** "<Zoom reason>; renamed from: <old name>" — the processor's note that the device left under a new name. */
const RENAMED_FROM = /^(?:(.*?); )?renamed from: (.+)$/s

/** Longest old name shown in "сменил имя (было «…»)". */
const OLD_NAME_MAX = 60

function zoomLeaveLabel(reason: string): string {
  if (Object.hasOwn(ENGINE_LEAVE_CODES, reason)) return ENGINE_LEAVE_CODES[reason]
  // Only the part after "Reason :" says why ("left the meeting. Reason : Network connection error").
  const why = /reason\s*:\s*(.+)$/is.exec(reason)?.[1].trim() || reason
  for (const [re, label] of ZOOM_LEAVE_REASONS) if (re.test(why)) return label
  return 'вышел'
}

/**
 * Russian leave reason of an interval; '' when there is none. Understands the
 * engine codes, Zoom's English texts and the processor's "renamed from: <name>"
 * suffix. Unknown Zoom texts become a neutral "вышел".
 */
export function leaveReasonLabel(reason: string | null | undefined): string {
  const r = (reason ?? '').trim()
  if (!r) return ''
  const renamed = RENAMED_FROM.exec(r)
  if (renamed) {
    const old = renamed[2].trim()
    const shown = old.length > OLD_NAME_MAX ? `${old.slice(0, OLD_NAME_MAX - 1)}…` : old
    const label = `сменил имя (было «${shown}»)`
    const zoomPart = (renamed[1] ?? '').trim()
    return zoomPart ? `${label}, ${zoomLeaveLabel(zoomPart)}` : label
  }
  return zoomLeaveLabel(r)
}

const STATUS_VALUES: Readonly<Record<string, string>> = {
  present: 'Был',
  absent: 'Не был',
  auto: 'автоматически',
  null: 'автоматически',
}

/**
 * Audit texts reference students as "<ФИО> (id 12)" so the server can find
 * them; the database id means nothing to a teacher and is dropped for display.
 */
export function auditText(value: string): string {
  return value
    .replace(/\s*\(id \d+\)/g, '')
    .replace(/[ \t]{2,}/g, ' ')
    .trim()
}

/** Audit previous/new value in plain words (attendance statuses are translated). */
export function auditValue(value: string): string {
  const v = auditText(value)
  if (!v) return '—'
  return Object.hasOwn(STATUS_VALUES, v) ? STATUS_VALUES[v] : v
}

// ---------------------------------------------------------------------------
// Remembering Zoom names
// ---------------------------------------------------------------------------

/** Device brands and Zoom's default names: anybody can show up under them (TZ §12: "iPhone", "User123"). */
const DEVICE_WORDS: ReadonlySet<string> = new Set([
  'iphone', 'ipad', 'ipod', 'android', 'galaxy', 'samsung', 'xiaomi', 'redmi', 'poco', 'huawei', 'honor',
  'oneplus', 'pixel', 'nokia', 'realme', 'oppo', 'vivo', 'tecno', 'infinix', 'macbook', 'imac', 'windows',
  'pc', 'laptop', 'desktop', 'phone', 'tablet', 'mobile', 'айфон', 'айпад', 'андроид', 'самсунг', 'ноутбук',
  'ноут', 'компьютер', 'комп', 'телефон', 'планшет', 'смартфон', 'zoom', 'user', 'guest', 'participant',
  'пользователь', 'гость', 'участник',
])
/** Words that only qualify a device ("iPhone 13 Pro Max"); alone they may be a real name ("Max"). */
const DEVICE_MODIFIERS: ReadonlySet<string> = new Set([
  'pro', 'max', 'plus', 'mini', 'note', 'ultra', 'lite', 'se', 'air', 'my', 's', 'de', 'мой',
])

/**
 * True when a Zoom display name looks like a device or a default name rather
 * than a person ("iPhone", "User123", "Galaxy A52", a phone number). Remembering
 * such a name would credit the next stranger who joins under it to this student.
 */
export function looksLikeDeviceName(name: string): boolean {
  const words = name.normalize('NFKC').toLocaleLowerCase('ru').split(/[^\p{L}\p{N}]+/u).filter(Boolean)
  if (words.length === 0 || words.join('').length < 2) return true
  let device = false
  for (const w of words) {
    const base = w.replace(/\d+$/, '')
    if (/^\d+$/.test(w) || /^\p{L}{1,2}\d+\p{L}?$/u.test(w) || DEVICE_WORDS.has(w) || DEVICE_WORDS.has(base)) {
      device = true // number, model code (A52, S21) or a device / default word
    } else if (!DEVICE_MODIFIERS.has(w)) {
      return false // a word that may be a person's name
    }
  }
  return device
}

// ---------------------------------------------------------------------------
// Zoom link
// ---------------------------------------------------------------------------

/** zoom.us / zoom.com and their subdomains (us02web.zoom.us), like the server accepts for join links. */
function isZoomHost(host: string): boolean {
  const h = host.toLowerCase()
  return h === 'zoom.us' || h.endsWith('.zoom.us') || h === 'zoom.com' || h.endsWith('.zoom.com')
}

/**
 * "Открыть в Zoom" target: the stored join link when it is an https link to Zoom
 * (never another scheme or site — the link comes from user input), otherwise the
 * standard zoom.us/j/<id> link built from the meeting number.
 */
export function zoomJoinUrl(lesson: Pick<Lesson, 'zoom_join_url' | 'zoom_meeting_id'>): string {
  const url = (lesson.zoom_join_url ?? '').trim()
  if (/^https:\/\/[^\s]+$/i.test(url)) {
    try {
      if (isZoomHost(new URL(url).hostname)) return url
    } catch {
      // Not a parseable URL: fall back to the meeting number.
    }
  }
  const id = (lesson.zoom_meeting_id ?? '').replace(/\D/g, '')
  return id.length >= 9 && id.length <= 12 ? `https://zoom.us/j/${id}` : ''
}

/**
 * True when the newest «Занятие завершено» entry was written by the service itself
 * (no user): Zoom never reported the meeting end and the lesson was quiet for hours.
 */
export function finishedBySystem(audit: readonly AuditEntry[] | undefined): boolean {
  const finish = audit?.find((a) => a.action === 'lesson.finish')
  return finish !== undefined && finish.new_value.startsWith('Завершено автоматически')
}

/** True when every linked Zoom session has ended (the lesson then stays finished; docs §5.4 rule 5). */
export function zoomMeetingEnded(lesson: Pick<Lesson, 'sessions'>): boolean {
  return lesson.sessions.length > 0 && lesson.sessions.every((s) => s.ended_at !== null)
}

/**
 * "устройство N" for the join–leave intervals of one student, or an empty map when
 * the student never was in Zoom from two devices at the same time.
 *
 * Zoom gives every join its own participant_uuid (it stays the same only inside
 * breakout rooms), so a reconnect looks like a new device: numbering the uuids
 * would turn one laptop that lost Wi-Fi twice into "устройство 1, 2, 3". Instead
 * every connection (the intervals of one uuid) takes the lowest number that is
 * free when it joins; only connections that really overlap get different numbers.
 * Open intervals last until `nowMs`.
 */
export function deviceNumbers(
  intervals: readonly Pick<Interval, 'id' | 'join_at' | 'leave_at' | 'zoom_participant_uuid'>[],
  nowMs: number,
): Map<number, number> {
  const connections = new Map<string, { start: number; end: number; ids: number[] }>()
  for (const iv of intervals) {
    const start = Date.parse(iv.join_at)
    const end = iv.leave_at === null ? Math.max(start, nowMs) : Date.parse(iv.leave_at)
    if (!Number.isFinite(start) || !Number.isFinite(end)) continue
    const key = iv.zoom_participant_uuid ? `uuid:${iv.zoom_participant_uuid}` : `interval:${iv.id}`
    const c = connections.get(key)
    if (c) {
      c.start = Math.min(c.start, start)
      c.end = Math.max(c.end, end)
      c.ids.push(iv.id)
    } else connections.set(key, { start, end, ids: [iv.id] })
  }
  const ordered = [...connections.values()].sort((a, b) => a.start - b.start || a.end - b.end || a.ids[0] - b.ids[0])
  const laneEnds: number[] = [] // when the connection holding each number ends
  const numbers = new Map<number, number>()
  let simultaneous = false
  for (const c of ordered) {
    let lane = laneEnds.findIndex((end) => end <= c.start)
    if (lane === -1) lane = laneEnds.push(c.end) - 1
    else laneEnds[lane] = c.end
    if (lane > 0) simultaneous = true
    for (const id of c.ids) numbers.set(id, lane + 1)
  }
  return simultaneous ? numbers : new Map()
}

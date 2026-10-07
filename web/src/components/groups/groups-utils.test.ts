import { describe, expect, it } from 'vitest'

import { emptyGroupForm, groupInput, validateGroupForm } from '@/components/groups/group-form'
import {
  canCommit,
  commitRows,
  detectedSummary,
  duplicateGroups,
  duplicateGroupText,
  fileProblem,
  importResultSummary,
  isImportBlocked,
  originalNames,
  previewCountItems,
  previewFromError,
  removeDuplicateLines,
  withSourceLines,
} from '@/components/groups/import-utils'
import { buildMemoText, NAMES_HEADING, STUDENT_RESPONSIBILITY_TEXT } from '@/components/groups/memo'
import {
  ALL_TIME_FROM,
  boundsLabel,
  boundsToRange,
  cellDescription,
  cellsByLesson,
  exportRange,
  isWithoutZoomData,
  lessonTotals,
  nextCell,
  parsePeriod,
  percentTone,
  periodBounds,
  reportColumns,
  studentTotals,
} from '@/components/groups/report-utils'
import { activeNames, matchesStudent, normalizeName, rosterCounts } from '@/components/groups/students-utils'
import { ApiError } from '@/lib/api'
import type { GroupReport, GroupReportLesson, ImportPreview, ImportRow, Student } from '@/lib/types'

const row = (line: number, full_name: string, status: ImportRow['status'] = 'new', external_id = ''): ImportRow => ({
  line,
  external_id,
  full_name,
  normalized_name: full_name.toLowerCase(),
  status,
  message: '',
  student_id: null,
})

function preview(rows: ImportRow[], over: Partial<ImportPreview> = {}): ImportPreview {
  const counts = { new: 0, existing: 0, error: 0, duplicate: 0, archive: 0 }
  for (const r of rows) counts[r.status]++
  return {
    mode: 'add',
    rows,
    counts,
    detected: { format: 'text', encoding: 'utf-8', delimiter: '', name_column: '' },
    name_format: 'as_is',
    has_patronymics: false,
    ...over,
  }
}

describe('import helpers', () => {
  it('blocks saving when there are errors or duplicates', () => {
    expect(isImportBlocked(preview([row(1, 'Иванов Иван')]))).toBe(false)
    expect(isImportBlocked(preview([row(1, 'Иванов Иван', 'duplicate'), row(2, 'Иванов Иван', 'duplicate')]))).toBe(true)
    expect(isImportBlocked(preview([row(1, '', 'error')]))).toBe(true)
    expect(canCommit(preview([]))).toBe(false)
    // Only archive rows: the file itself was empty.
    expect(canCommit(preview([row(0, 'Сидоров Алексей', 'archive')]))).toBe(false)
    expect(canCommit(preview([row(1, 'Иванов Иван', 'existing'), row(0, 'Сидоров Алексей', 'archive')]))).toBe(true)
  })

  it('commits names as written in the file when patronymics are dropped', () => {
    const full = preview([row(1, 'Иванов Иван Иванович', 'new', '17'), row(2, 'Петров Пётр Петрович')], { has_patronymics: true })
    const short = preview([row(1, 'Иванов Иван', 'new', '17'), row(2, 'Петров Пётр'), row(0, 'Сидоров Алексей', 'archive')], {
      has_patronymics: true,
      name_format: 'last_first',
    })
    const originals = originalNames(full)
    expect(commitRows(short, originals)).toEqual([
      { external_id: '17', full_name: 'Иванов Иван Иванович', line: 1 },
      { external_id: '', full_name: 'Петров Пётр Петрович', line: 2 },
    ])
    expect(commitRows(full)).toEqual([
      { external_id: '17', full_name: 'Иванов Иван Иванович', line: 1 },
      { external_id: '', full_name: 'Петров Пётр Петрович', line: 2 },
    ])
    // A last_first preview never provides originals.
    expect(originalNames(short).size).toBe(0)
  })

  it('describes the detected format in plain words', () => {
    expect(
      detectedSummary({ format: 'csv', encoding: 'windows-1251', delimiter: ';', name_column: 'ФИО' }),
    ).toBe('Распознано: таблица CSV · кодировка WINDOWS-1251 · разделитель: точка с запятой · колонка с именами: «ФИО»')
    expect(detectedSummary({ format: 'text', encoding: 'utf-8', delimiter: '\t', name_column: '' })).toBe(
      'Распознано: простой текст · кодировка UTF-8 · разделитель: табуляция',
    )
    expect(detectedSummary(undefined)).toBe('')
  })

  it('summarizes the import result', () => {
    expect(importResultSummary({ created: 3, existing: 20, archived: 2, updated: 1 })).toBe(
      'Добавлено: 3 · обновлено: 1 · перенесено в архив: 2 · без изменений: 20',
    )
    expect(importResultSummary({ created: 0, existing: 0, archived: 0, updated: 0 })).toBe('Изменений нет')
  })

  it('takes the re-validated preview from a 422 of commit', () => {
    const p = preview([row(1, 'Иванов Иван', 'duplicate')])
    expect(previewFromError(new ApiError(422, 'validation', 'Ошибка', { preview: p }))).toEqual(p)
    expect(previewFromError(new ApiError(422, 'validation', 'Ошибка', { preview: 'nope' }))).toBeNull()
    expect(previewFromError(new ApiError(500, 'internal', 'Ошибка', { preview: p }))).toBeNull()
    expect(previewFromError(new Error('x'))).toBeNull()
  })

  it('keeps the file line numbers in a re-checked preview', () => {
    // A CSV with a header: the students are on lines 2..4.
    const sent = preview([row(2, 'Иванов Иван'), row(3, 'Петров Пётр'), row(4, 'Сидоров Алексей')], {
      detected: { format: 'csv', encoding: 'windows-1251', delimiter: ';', name_column: 'ФИО' },
    })
    expect(commitRows(sent).map((r) => r.line)).toEqual([2, 3, 4])

    // A server that numbers the request rows by position (1..n).
    const positional = preview([row(1, 'Иванов Иван'), row(2, 'Петров Пётр', 'error'), row(3, 'Сидоров Алексей'), row(0, 'Орлов Андрей', 'archive')])
    const fixed = withSourceLines(positional, sent)
    expect(fixed.rows.map((r) => r.line)).toEqual([2, 3, 4, 0])
    expect(fixed.detected).toEqual(sent.detected)

    // A server that echoes the lines sent with the rows: nothing to remap.
    const echoed = preview([row(2, 'Иванов Иван'), row(3, 'Петров Пётр', 'error'), row(4, 'Сидоров Алексей')])
    expect(withSourceLines(echoed, sent).rows.map((r) => r.line)).toEqual([2, 3, 4])
  })

  it('groups repeated names and counts names, not rows', () => {
    const dup = (line: number, name: string, id = '') => ({ ...row(line, name, 'duplicate', id), normalized_name: 'петрова анна' })
    const p = preview([
      row(1, 'Иванов Иван'),
      dup(2, 'Петрова Анна'),
      { ...row(3, 'Федоров Артем', 'duplicate'), normalized_name: 'федоров артем' },
      dup(4, '  петрова   АННА '),
      { ...row(9, 'Федоров Артём', 'duplicate'), normalized_name: 'федоров артем' },
      dup(31, 'ПЕТРОВА анна'),
    ])
    const groups = duplicateGroups(p)
    expect(groups).toEqual([
      { name: 'Петрова Анна', normalized: 'петрова анна', lines: [2, 4, 31], sameId: true },
      { name: 'Федоров Артем', normalized: 'федоров артем', lines: [3, 9], sameId: true },
    ])
    expect(duplicateGroupText(groups[0])).toBe('Строки 2, 4 и 31 — одно и то же имя «Петрова Анна»')
    expect(duplicateGroupText(groups[1])).toBe('Строки 3 и 9 — одно и то же имя «Федоров Артем»')
    expect(previewCountItems(p).find((i) => i.status === 'duplicate')).toEqual({
      status: 'duplicate',
      label: 'Повторяются имена',
      value: 2,
    })
    // Renames are counted among the existing rows: shown apart, never hidden there.
    const items = previewCountItems({ counts: { ...p.counts, existing: 4, rename: 1 } })
    expect(items.find((i) => i.status === 'existing')?.value).toBe(3)
    expect(items.find((i) => i.status === 'rename')).toEqual({ status: 'rename', label: 'Будут переименованы', value: 1 })
    // Older servers send no rename count.
    expect(previewCountItems({ counts: { ...p.counts, existing: 4 } }).find((i) => i.status === 'rename')?.value).toBe(0)
    // Without rows (older callers) the row count is labelled as such.
    expect(previewCountItems({ counts: p.counts }).find((i) => i.status === 'duplicate')).toEqual({
      status: 'duplicate',
      label: 'Строк с повторами',
      value: 5,
    })
  })

  it('removes the repeated lines of a pasted list only when it is safe', () => {
    const text = 'Иванов Иван\r\nПетрова Анна\r\n\r\n  петрова   АННА \r\nПетрова Анна'
    const group = { name: 'Петрова Анна', normalized: 'петрова анна', lines: [2, 4, 5], sameId: true }
    expect(removeDuplicateLines(text, group)).toBe('Иванов Иван\nПетрова Анна\n')
    // Different IDs: different students, never removed.
    expect(removeDuplicateLines(text, { ...group, sameId: false })).toBeNull()
    // The text no longer matches the preview: nothing is guessed.
    expect(removeDuplicateLines('Иванов Иван\nПетрова Анна\nСидоров', group)).toBeNull()
    expect(removeDuplicateLines(text, { ...group, lines: [2, 1] })).toBeNull()
    expect(removeDuplicateLines(text, { ...group, lines: [2] })).toBeNull()
    expect(removeDuplicateLines(text, { ...group, lines: [0, 4] })).toBeNull()
  })

  it('rejects empty, oversized and old Excel files before uploading', () => {
    expect(fileProblem({ name: 'list.csv', size: 10 })).toBeNull()
    expect(fileProblem({ name: 'list.csv', size: 0 })).toMatch(/пустой/)
    expect(fileProblem({ name: 'list.csv', size: 11 * 1024 * 1024 })).toMatch(/больше 10 МБ/)
    expect(fileProblem({ name: 'Список.XLS', size: 100 })).toMatch(/\.xlsx/)
  })
})

describe('group form', () => {
  it('validates name and duration', () => {
    expect(validateGroupForm(emptyGroupForm())).toEqual({ name: 'Введите название группы.' })
    // Same limits as the server: 15..600 minutes, a name up to 100 characters, a description up to 2000.
    expect(validateGroupForm({ name: 'A', description: '', duration: '0' }).duration).toMatch(/от 15 до 600/)
    expect(validateGroupForm({ name: 'A', description: '', duration: '14' }).duration).toMatch(/от 15 до 600/)
    expect(validateGroupForm({ name: 'A', description: '', duration: '15' })).toEqual({})
    expect(validateGroupForm({ name: 'A', description: '', duration: '601' }).duration).toBeDefined()
    expect(validateGroupForm({ name: 'A', description: '', duration: '1.5' }).duration).toBeDefined()
    expect(validateGroupForm({ name: 'Я'.repeat(100), description: '', duration: '90' })).toEqual({})
    expect(validateGroupForm({ name: 'Я'.repeat(101), description: '', duration: '90' }).name).toMatch(/больше 100/)
    // Runs of spaces count as one, as the server collapses them.
    expect(validateGroupForm({ name: `${'Я'.repeat(50)}      ${'Я'.repeat(49)}`, description: '', duration: '90' })).toEqual({})
    expect(validateGroupForm({ name: 'A', description: 'д'.repeat(2001), duration: '90' }).description).toMatch(/больше 2000/)
    expect(validateGroupForm({ name: ' DevOps ', description: '', duration: '90' })).toEqual({})
    expect(groupInput({ name: ' DevOps ', description: ' курс ', duration: ' 45 ' })).toEqual({
      name: 'DevOps',
      description: 'курс',
      default_duration_minutes: 45,
    })
  })
})

const student = (id: number, full_name: string, over: Partial<Student> = {}): Student => ({
  id,
  group_id: 1,
  external_id: '',
  full_name,
  normalized_name: full_name.toLowerCase(),
  status: 'active',
  aliases: [],
  ...over,
})

describe('roster helpers', () => {
  it('normalizes like the server (TZ §7)', () => {
    expect(normalizeName('  ФЁДОРОВ   Максим ')).toBe('федоров максим')
  })

  it('searches by name, alias and ID', () => {
    const s = student(1, 'Фёдоров Максим', { external_id: 'A-17', aliases: [{ id: 1, alias: 'Max F' }] })
    expect(matchesStudent(s, 'федоров')).toBe(true)
    expect(matchesStudent(s, 'max')).toBe(true)
    expect(matchesStudent(s, 'a-17')).toBe(true)
    expect(matchesStudent(s, 'петров')).toBe(false)
    expect(matchesStudent(s, '  ')).toBe(true)
  })

  it('counts and lists active students alphabetically', () => {
    const list = [student(1, 'Петров Пётр'), student(2, 'Иванов Иван'), student(3, 'Сидоров Алексей', { status: 'archived' })]
    expect(rosterCounts(list)).toEqual({ active: 2, archived: 1 })
    expect(activeNames(list)).toEqual(['Иванов Иван', 'Петров Пётр'])
  })

  it('builds the memo with the TZ §33 text and exact names', () => {
    const text = buildMemoText('DevOps-1', ['Иванов Иван', 'Петров Пётр'])
    expect(text).toContain('Памятка для студентов группы «DevOps-1»')
    expect(text).toContain(STUDENT_RESPONSIBILITY_TEXT)
    expect(text).toContain(`${NAMES_HEADING}\n1. Иванов Иван\n2. Петров Пётр`)
    expect(text).toContain('Переименовать')
    expect(buildMemoText('DevOps-1', [])).not.toContain(NAMES_HEADING)
  })
})

describe('report helpers', () => {
  it('moves the focus inside the matrix with arrows, Home and End, stopping at the edges', () => {
    expect(nextCell('ArrowRight', [1, 1], 3, 4)).toEqual([1, 2])
    expect(nextCell('ArrowRight', [1, 3], 3, 4)).toEqual([1, 3])
    expect(nextCell('ArrowLeft', [1, 0], 3, 4)).toEqual([1, 0])
    expect(nextCell('ArrowDown', [2, 1], 3, 4)).toEqual([2, 1])
    expect(nextCell('ArrowUp', [2, 1], 3, 4)).toEqual([1, 1])
    expect(nextCell('Home', [2, 3], 3, 4)).toEqual([2, 0])
    expect(nextCell('End', [2, 0], 3, 4)).toEqual([2, 3])
    expect(nextCell('Home', [2, 3], 3, 4, true)).toEqual([0, 0])
    expect(nextCell('End', [0, 0], 3, 4, true)).toEqual([2, 3])
    expect(nextCell('Enter', [0, 0], 3, 4)).toBeNull()
  })

  const now = new Date(2026, 9, 6, 15, 0) // 6 Oct 2026, local time

  it('computes period bounds in local time', () => {
    const month = periodBounds('this_month', now)
    expect(month.from).toEqual(new Date(2026, 9, 1))
    expect(month.to?.getDate()).toBe(31)
    const last = periodBounds('last_month', now)
    expect(last.from).toEqual(new Date(2026, 8, 1))
    expect(last.to?.getMonth()).toBe(8)
    expect(last.to?.getDate()).toBe(30)
    expect(periodBounds('3_months', now).from).toEqual(new Date(2026, 7, 1))
    expect(periodBounds('all', now)).toEqual({ from: null, to: null })
    const custom = periodBounds('custom', now, { from: '2026-09-01', to: 'bad' })
    expect(custom).toEqual({ from: new Date(2026, 8, 1), to: null })
    // A start typed after the end is swapped instead of being sent as an invalid range.
    expect(periodBounds('custom', now, { from: '2026-09-10', to: '2026-09-01' })).toEqual({
      from: new Date(2026, 8, 1),
      to: new Date(2026, 8, 10, 23, 59, 59, 999),
    })
    expect(boundsLabel(last)).toBe('с 01.09.2026 по 30.09.2026')
    expect(boundsLabel({ from: null, to: null })).toBe('за всё время')
    expect(parsePeriod('last_month')).toBe('last_month')
    expect(parsePeriod('__proto__')).toBe('all')
  })

  it('always sends both bounds: the server default is not "all time"', () => {
    const endOfToday = new Date(2026, 9, 6, 23, 59, 59, 999).toISOString()
    // "Всё время" starts long ago and ends today (later lessons have no attendance).
    expect(boundsToRange(periodBounds('all', now), now)).toEqual({ from: ALL_TIME_FROM.toISOString(), to: endOfToday })
    // Only "С": up to today; only "По": from the very beginning.
    expect(boundsToRange({ from: new Date(2026, 8, 1), to: null }, now)).toEqual({
      from: new Date(2026, 8, 1).toISOString(),
      to: endOfToday,
    })
    expect(boundsToRange({ from: null, to: new Date(2025, 0, 31, 23, 59, 59, 999) }, now)).toEqual({
      from: ALL_TIME_FROM.toISOString(),
      to: new Date(2025, 0, 31, 23, 59, 59, 999).toISOString(),
    })
    // A start in the future still gives a valid range (end of that day).
    expect(boundsToRange({ from: new Date(2026, 11, 1), to: null }, now)).toEqual({
      from: new Date(2026, 11, 1).toISOString(),
      to: new Date(2026, 11, 1, 23, 59, 59, 999).toISOString(),
    })
    const month = periodBounds('this_month', now)
    expect(boundsToRange(month, now)).toEqual({ from: month.from?.toISOString(), to: month.to?.toISOString() })
  })

  it('exports an open period from the day of its first lesson', () => {
    const range = boundsToRange({ from: null, to: null }, now)
    const lessons = [
      { planned_start_at: '2026-03-10T15:00:00Z' },
      { planned_start_at: new Date(2025, 8, 2, 18, 30).toISOString() },
    ]
    expect(exportRange({ from: null, to: null }, range, lessons)).toEqual({ ...range, from: new Date(2025, 8, 2).toISOString() })
    expect(exportRange({ from: null, to: null }, range, [])).toEqual(range)
    const month = periodBounds('this_month', now)
    const monthRange = boundsToRange(month, now)
    expect(exportRange(month, monthRange, lessons)).toBe(monthRange)
  })

  it('hides only upcoming lessons without marks', () => {
    const lessons: GroupReportLesson[] = [
      { id: 1, title: 'A', planned_start_at: '2026-10-01T15:00:00Z', status: 'finished' },
      // Planned, but its time has passed: Zoom sent nothing, everybody is absent.
      { id: 4, title: 'D', planned_start_at: '2026-10-02T15:00:00Z', status: 'planned' },
      { id: 3, title: 'C', planned_start_at: '2026-10-08T15:00:00Z', status: 'planned' },
      { id: 5, title: 'E', planned_start_at: '2026-10-09T15:00:00Z', status: 'planned' },
    ]
    const studentWith = (cells: { lesson_id: number; final_status: 'present' | 'absent' | null; manual?: boolean }[]) => ({
      cells: cells.map((c) => ({ total_seconds: 0, late_seconds: null, manual: false, ...c })),
    })
    const students = [
      studentWith([
        { lesson_id: 1, final_status: 'present' },
        { lesson_id: 4, final_status: 'absent' },
        { lesson_id: 3, final_status: null },
        // A teacher already excused this student for an upcoming lesson.
        { lesson_id: 5, final_status: 'present', manual: true },
      ]),
    ] as unknown as GroupReport['students']
    expect(reportColumns({ lessons, students }, now).map((l) => l.id)).toEqual([1, 4, 5])
    expect(isWithoutZoomData(lessons[1], now)).toBe(true)
    expect(isWithoutZoomData(lessons[0], now)).toBe(false)
    expect(isWithoutZoomData(lessons[2], now)).toBe(false)
  })

  it('counts totals only over shown lessons with a record', () => {
    const lessons: GroupReportLesson[] = [
      { id: 1, title: 'A', planned_start_at: '2026-10-01T15:00:00Z', status: 'finished' },
      { id: 2, title: 'B', planned_start_at: '2026-10-03T15:00:00Z', status: 'finished' },
      { id: 3, title: 'C', planned_start_at: '2026-10-08T15:00:00Z', status: 'planned' },
    ]
    const columns = reportColumns({ lessons, students: [] }, now)
    expect(columns.map((l) => l.id)).toEqual([1, 2])
    const cells = cellsByLesson({
      cells: [
        { lesson_id: 2, final_status: 'absent', manual: false, total_seconds: 0, late_seconds: null },
        { lesson_id: 1, final_status: 'present', manual: true, total_seconds: 4000, late_seconds: 120 },
        { lesson_id: 3, final_status: 'absent', manual: false, total_seconds: 0, late_seconds: null },
      ],
    })
    expect(studentTotals(cells, columns)).toEqual({ attended: 1, total: 2, percent: 50 })
    const empty = cellsByLesson({ cells: [{ lesson_id: 1, final_status: null, manual: false, total_seconds: 0, late_seconds: null }] })
    expect(studentTotals(empty, columns)).toEqual({ attended: 0, total: 0, percent: null })
    const t = lessonTotals([cells, empty], columns)
    expect(t.perLesson.map((x) => [x.attended, x.total])).toEqual([
      [1, 1],
      [0, 1],
    ])
    expect(t.overall).toEqual({ attended: 1, total: 2, percent: 50 })
  })

  it('describes a cell for the tooltip', () => {
    expect(cellDescription({ lesson_id: 1, final_status: 'present', manual: true, total_seconds: 5040, late_seconds: 120 })).toBe(
      'Был · в Zoom 1 ч 24 мин · опоздал на 2 мин · отмечено вручную',
    )
    expect(cellDescription({ lesson_id: 1, final_status: 'absent', manual: false, total_seconds: 0, late_seconds: null })).toBe('Не был')
    expect(cellDescription(undefined)).toMatch(/Нет данных/)
    expect(percentTone(85)).toBe('success')
    expect(percentTone(50)).toBe('warning')
    expect(percentTone(10)).toBe('danger')
    expect(percentTone(null)).toBe('muted')
  })
})

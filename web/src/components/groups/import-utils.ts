// Pure helpers of the student import flow (preview → commit). Kept apart from
// the components so they can be unit tested and shared by the import wizard
// and the "new group" dialog.

import { normalizeName } from '@/components/groups/students-utils'
import { ApiError } from '@/lib/api'
import { pluralize, WORDS } from '@/lib/format'
import type {
  ImportCommitRow,
  ImportDetected,
  ImportNameFormat,
  ImportPreview,
  ImportResult,
  ImportRow,
} from '@/lib/types'

/** Same limit as the server (internal/importer.MaxFileSize). */
export const MAX_IMPORT_FILE_BYTES = 10 * 1024 * 1024

/** File picker filter: CSV / TSV / plain text / Excel 2007+. */
export const IMPORT_FILE_ACCEPT = [
  '.csv',
  '.tsv',
  '.txt',
  '.xlsx',
  'text/csv',
  'text/plain',
  'text/tab-separated-values',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
].join(',')

/** Example shown in the paste box. */
export const PASTE_PLACEHOLDER = 'Иванов Иван\nПетров Пётр\nСидоров Алексей'

/** Russian problem with a chosen file that is obvious before uploading, or null. */
export function fileProblem(file: Pick<File, 'name' | 'size'>): string | null {
  if (file.size === 0) return 'Файл пустой. Выберите другой файл.'
  if (file.size > MAX_IMPORT_FILE_BYTES) return 'Файл слишком большой (больше 10 МБ). Оставьте в нём только список студентов.'
  const lower = file.name.toLowerCase()
  if (lower.endsWith('.xls'))
    return 'Старый формат Excel (.xls) не поддерживается. Откройте файл в Excel и сохраните как «Книга Excel (.xlsx)» или CSV.'
  return null
}

/** TZ §8, verbatim: the error shown when a list has names equal after normalization. */
export const DUPLICATE_NAMES_MESSAGE = 'Невозможно однозначно идентифицировать студентов по Zoom display name.'

/** Rows that stop the list from being saved (TZ §8: errors and duplicate names). */
export function isProblemRow(row: Pick<ImportRow, 'status'>): boolean {
  return row.status === 'error' || row.status === 'duplicate'
}

/** True when the preview has errors or duplicates: saving is not allowed. */
export function isImportBlocked(preview: Pick<ImportPreview, 'counts'>): boolean {
  return preview.counts.error > 0 || preview.counts.duplicate > 0
}

/** Rows of the file itself (archive rows are existing students missing from the file). */
export function fileRows(preview: Pick<ImportPreview, 'rows'>): ImportRow[] {
  return preview.rows.filter((r) => r.status !== 'archive')
}

/** True when there is anything to save: at least one student row and no blocking problems. */
export function canCommit(preview: ImportPreview): boolean {
  return !isImportBlocked(preview) && fileRows(preview).length > 0
}

/**
 * Names as written in the file, by line, taken from a preview built with
 * name_format "as_is". With "last_first" the preview rows carry the shortened
 * name, but the commit must send the original one: the server applies the
 * format again and matches existing students by the name as written.
 */
export type OriginalNames = ReadonlyMap<number, string>

export function originalNames(preview: ImportPreview): OriginalNames {
  const map = new Map<number, string>()
  if (preview.name_format === 'last_first') return map
  for (const row of fileRows(preview)) map.set(row.line, row.full_name)
  return map
}

/**
 * Rows of the commit request for a preview. Each row carries its file line so
 * a rejected commit's re-check still says "строка N" of the teacher's file.
 */
export function commitRows(preview: ImportPreview, originals: OriginalNames = new Map()): ImportCommitRow[] {
  const shortened = preview.name_format === 'last_first'
  return fileRows(preview).map((row) => ({
    external_id: row.external_id,
    full_name: shortened ? (originals.get(row.line) ?? row.full_name) : row.full_name,
    ...(row.line > 0 ? { line: row.line } : {}),
  }))
}

/** Effective name format of a preview (older servers omit the field). */
export function previewNameFormat(preview: Pick<ImportPreview, 'name_format'>): ImportNameFormat {
  return preview.name_format === 'last_first' ? 'last_first' : 'as_is'
}

const formatLabels: Record<string, string> = {
  csv: 'таблица CSV',
  xlsx: 'файл Excel',
  text: 'простой текст',
}

const delimiterLabels: Record<string, string> = {
  ',': 'запятая',
  ';': 'точка с запятой',
  '\t': 'табуляция',
  '|': 'вертикальная черта',
}

/**
 * Words for a delimiter. The server already sends the Russian word for the
 * usual ones ("точка с запятой"); a raw character is named or quoted.
 */
function delimiterLabel(delimiter: string): string {
  if (Object.hasOwn(delimiterLabels, delimiter)) return delimiterLabels[delimiter]
  if (Object.values(delimiterLabels).includes(delimiter)) return delimiter
  return `«${delimiter}»`
}

/** "Распознано: таблица CSV · кодировка UTF-8 · разделитель: точка с запятой · колонка с именами: «ФИО»". */
export function detectedSummary(detected: ImportDetected | null | undefined): string {
  if (!detected) return ''
  const parts: string[] = []
  if (detected.format) parts.push(Object.hasOwn(formatLabels, detected.format) ? formatLabels[detected.format] : detected.format)
  if (detected.encoding) parts.push(`кодировка ${detected.encoding.toUpperCase()}`)
  if (detected.delimiter) parts.push(`разделитель: ${delimiterLabel(detected.delimiter)}`)
  if (detected.name_column) parts.push(`колонка с именами: «${detected.name_column}»`)
  return parts.length ? `Распознано: ${parts.join(' · ')}` : ''
}

/** Toast text after a successful commit: "Добавлено: 3 · без изменений: 20 · в архив: 2". */
export function importResultSummary(result: Partial<ImportResult> | null | undefined): string {
  if (!result) return ''
  const parts: string[] = []
  if (result.created) parts.push(`добавлено: ${result.created}`)
  if (result.updated) parts.push(`обновлено: ${result.updated}`)
  if (result.restored) parts.push(`возвращено из архива: ${result.restored}`)
  if (result.archived) parts.push(`перенесено в архив: ${result.archived}`)
  if (result.existing) parts.push(`без изменений: ${result.existing}`)
  if (parts.length === 0) return 'Изменений нет'
  const text = parts.join(' · ')
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/** A "new" row that brings an archived student back instead of creating one. */
export function isRestoreRow(row: Pick<ImportRow, 'status' | 'restore'>): boolean {
  return row.status === 'new' && row.restore === true
}

/** An "existing" row that changes the student's stored name (shown apart, never hidden among «Уже в группе»). */
export function isRenameRow(row: Pick<ImportRow, 'status' | 'rename'>): boolean {
  return row.status === 'existing' && row.rename === true
}

/** Rows of the list that carry the same name (after normalization). */
export interface DuplicateGroup {
  /** The name as written in the first of these rows (not the messy spelling of a later one). */
  name: string
  /** Normalized name the server matched them by. */
  normalized: string
  /** Lines in the list, in order (0 when unknown). */
  lines: number[]
  /**
   * Every row has the same ID (or none): possibly one student entered twice. Rows with
   * different IDs are different people, so removing one of them is never offered.
   */
  sameId: boolean
}

/** Duplicate rows grouped by name, in list order: one entry per repeated name. */
export function duplicateGroups(preview: Pick<ImportPreview, 'rows'>): DuplicateGroup[] {
  const byName = new Map<string, ImportRow[]>()
  for (const row of preview.rows) {
    if (row.status !== 'duplicate') continue
    const key = row.normalized_name || normalizeName(row.full_name)
    const rows = byName.get(key)
    if (rows) rows.push(row)
    else byName.set(key, [row])
  }
  return [...byName.entries()].map(([normalized, rows]) => ({
    name: rows[0].full_name,
    normalized,
    lines: rows.map((r) => r.line),
    sameId: rows.every((r) => r.external_id === rows[0].external_id),
  }))
}

/** "2 и 31", "2, 9 и 31". */
export function joinLines(lines: readonly number[]): string {
  if (lines.length <= 1) return lines.join('')
  return `${lines.slice(0, -1).join(', ')} и ${lines[lines.length - 1]}`
}

/** "Строки 2 и 31 — одно и то же имя «Петрова Анна»". */
export function duplicateGroupText(group: Pick<DuplicateGroup, 'name' | 'lines'>): string {
  const known = group.lines.filter((l) => l > 0)
  if (known.length < 2) return `Имя «${group.name}» встречается в списке несколько раз`
  return `Строки ${joinLines(known)} — одно и то же имя «${group.name}»`
}

/**
 * The pasted text without the repeated lines of `group` (the first one stays), or null
 * when the lines cannot be found safely: unknown line numbers, different IDs, or a line
 * that does not hold the name (the preview no longer matches the text).
 */
export function removeDuplicateLines(text: string, group: DuplicateGroup): string | null {
  if (!group.sameId || group.lines.length < 2 || group.lines.some((l) => l < 1)) return null
  // Line numbers as the server counts them (internal/importer splitText).
  const lines = text.split(/\r\n|\r|\n/)
  const drop = new Set(group.lines.slice(1))
  for (const line of drop) {
    if (line > lines.length || !normalizeName(lines[line - 1]).includes(group.normalized)) return null
  }
  return lines.filter((_, i) => !drop.has(i + 1)).join('\n')
}

/** Count chips of the preview summary, in display order (zero counts are skipped by the caller). */
export function previewCountItems(preview: Pick<ImportPreview, 'counts'> & Partial<Pick<ImportPreview, 'rows'>>) {
  const c = preview.counts
  // The server counts restores among the new rows and renames among the existing ones;
  // show them apart.
  const restore = Math.min(Math.max(c.restore ?? 0, 0), c.new)
  const rename = Math.min(Math.max(c.rename ?? 0, 0), c.existing)
  // Names, not rows: one name written twice is one problem to fix, not two.
  const repeated = preview.rows ? duplicateGroups({ rows: preview.rows }).length : 0
  return [
    { status: 'new', label: 'Новых', value: c.new - restore },
    { status: 'restore', label: 'Вернутся из архива', value: restore },
    { status: 'existing', label: 'Уже в группе', value: c.existing - rename },
    { status: 'rename', label: 'Будут переименованы', value: rename },
    { status: 'error', label: 'С ошибкой', value: c.error },
    repeated > 0
      ? ({ status: 'duplicate', label: 'Повторяются имена', value: repeated } as const)
      : ({ status: 'duplicate', label: 'Строк с повторами', value: c.duplicate } as const),
    { status: 'archive', label: 'В архив', value: c.archive },
  ] as const
}

/** "25 строк" for the summary line. */
export function rowsLabel(n: number): string {
  return pluralize(n, WORDS.row)
}

function isImportPreview(value: unknown): value is ImportPreview {
  if (typeof value !== 'object' || value === null) return false
  const v = value as Partial<ImportPreview>
  return Array.isArray(v.rows) && typeof v.counts === 'object' && v.counts !== null
}

/** The re-validated preview the server attaches to a 422 of import/commit, or null. */
export function previewFromError(err: unknown): ImportPreview | null {
  if (!(err instanceof ApiError) || !err.isValidation) return null
  const preview = err.details.preview
  return isImportPreview(preview) ? preview : null
}

/**
 * The server re-checks a commit without the file: it echoes the `line` sent
 * with each row, or (servers that ignore it) numbers rows by their position in
 * the request (1..n). Puts back the file's line numbers in the second case and
 * the detected format of the preview the rows were sent from in both, so
 * "Строка" and the list of problem lines still point into the teacher's file.
 */
export function withSourceLines(recheck: ImportPreview, sent: ImportPreview): ImportPreview {
  const lines = fileRows(sent).map((r) => r.line)
  const rechecked = fileRows(recheck)
  const echoed = rechecked.length === lines.length && rechecked.every((row, i) => row.line === lines[i])
  const rows = echoed
    ? recheck.rows
    : recheck.rows.map((row) =>
        row.status !== 'archive' && row.line >= 1 && row.line <= lines.length ? { ...row, line: lines[row.line - 1] } : row,
      )
  return { ...recheck, rows, detected: sent.detected }
}

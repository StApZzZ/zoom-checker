// Roster helpers: search, ordering and counts of a group's students.

import type { Student } from '@/lib/types'

/** Same spirit as the server's names.Normalize (TZ §7): trim, lower case, ё→е, single spaces. */
export function normalizeName(value: string): string {
  return value.normalize('NFKC').toLowerCase().replaceAll('ё', 'е').replace(/\s+/g, ' ').trim()
}

const collator = new Intl.Collator('ru', { sensitivity: 'base', numeric: true })

/** Alphabetical order by full name (Russian collation). */
export function sortStudents(students: readonly Student[]): Student[] {
  return students.slice().sort((a, b) => collator.compare(a.full_name, b.full_name) || a.id - b.id)
}

/** Search by name, extra Zoom names or ID (substring, case and ё/е insensitive). */
export function matchesStudent(student: Student, query: string): boolean {
  const q = normalizeName(query)
  if (!q) return true
  if (normalizeName(student.full_name).includes(q)) return true
  if (student.external_id && normalizeName(student.external_id).includes(q)) return true
  return student.aliases.some((a) => normalizeName(a.alias).includes(q))
}

export interface RosterCounts {
  active: number
  archived: number
}

export function rosterCounts(students: readonly Student[]): RosterCounts {
  let active = 0
  let archived = 0
  for (const s of students) {
    if (s.status === 'archived') archived++
    else active++
  }
  return { active, archived }
}

/** Active students' names, alphabetically (for the memo). */
export function activeNames(students: readonly Student[]): string[] {
  return sortStudents(students.filter((s) => s.status !== 'archived')).map((s) => s.full_name)
}

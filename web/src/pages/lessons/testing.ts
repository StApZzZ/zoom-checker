// Test fixtures for the lesson screens (imported by *.test.tsx only).

import type { GroupSummary, Lesson, LessonSummary, LessonView, Participant, StudentRow } from '@/lib/types'

export function makeGroup(overrides: Partial<GroupSummary> = {}): GroupSummary {
  return {
    id: 2,
    name: 'DevOps-1',
    description: '',
    status: 'active',
    student_count: 25,
    default_duration_minutes: 90,
    created_at: '2026-09-01T10:00:00Z',
    last_lesson_at: null,
    next_lesson_at: null,
    ...overrides,
  }
}

export function makeLesson(overrides: Partial<Lesson> = {}): Lesson {
  return {
    id: 5,
    group: { id: 2, name: 'DevOps-1' },
    title: 'DevOps. Занятие №4',
    zoom_meeting_id: '1234567890',
    zoom_meeting_id_formatted: '123 456 7890',
    zoom_join_url: 'https://zoom.us/j/1234567890',
    planned_start_at: '2026-10-06T15:00:00Z',
    planned_end_at: '2026-10-06T16:30:00Z',
    actual_start_at: '2026-10-06T15:01:00Z',
    actual_end_at: null,
    status: 'active',
    manual_finished_at: null,
    sessions: [],
    ...overrides,
  }
}

export const zeroCounts = { in_group: 0, online: 0, seen: 0, not_seen: 0, unidentified: 0, present: 0, absent: 0 }

export function makeSummary(overrides: Partial<LessonSummary> = {}): LessonSummary {
  const { sessions: _sessions, ...lesson } = makeLesson()
  return { ...lesson, counts: { ...zeroCounts, in_group: 25 }, ...overrides }
}

export function makeStudent(overrides: Partial<StudentRow> = {}): StudentRow {
  return {
    student_id: 1,
    full_name: 'Иванов Иван',
    external_id: '',
    current_status: 'online',
    total_seconds: 600,
    first_join_at: '2026-10-06T15:02:00Z',
    last_leave_at: null,
    late_seconds: 60,
    early_leave_seconds: null,
    auto_status: 'present',
    manual_status: null,
    final_status: 'present',
    comment: '',
    zoom_names: ['Иванов Иван'],
    devices_online: 1,
    archived: false,
    ...overrides,
  }
}

export function makeParticipant(overrides: Partial<Participant> = {}): Participant {
  return {
    id: 100,
    display_name: 'Александр',
    normalized_name: 'александр',
    resolution: 'unidentified',
    ignore_reason: '',
    student_id: null,
    student_name: null,
    candidate_student_ids: [],
    current_status: 'online',
    first_join_at: '2026-10-06T15:05:00Z',
    last_leave_at: null,
    total_seconds: 300,
    devices: 1,
    manual: false,
    ...overrides,
  }
}

/** A running lesson: 4 students (online, left, late+manual, not seen), one unidentified, one host. */
export function makeView(overrides: Partial<LessonView> = {}): LessonView {
  const students = [
    makeStudent({ student_id: 1, full_name: 'Иванов Иван', zoom_names: ['Иванов Иван'] }),
    makeStudent({
      student_id: 2,
      full_name: 'Петров Пётр',
      current_status: 'left',
      total_seconds: 1500,
      last_leave_at: '2026-10-06T15:30:00Z',
      late_seconds: 0,
      zoom_names: ['Петров Пётр'],
    }),
    makeStudent({
      student_id: 3,
      full_name: 'Сидоров Алексей',
      total_seconds: 900,
      first_join_at: '2026-10-06T15:12:00Z',
      late_seconds: 720,
      devices_online: 2,
      auto_status: 'absent',
      manual_status: 'present',
      final_status: 'present',
      comment: 'Предупредил об опоздании',
      zoom_names: ['Сидоров Алексей', 'iPhone Алексея'],
    }),
    makeStudent({
      student_id: 4,
      full_name: 'Орлов Андрей',
      current_status: 'not_seen',
      total_seconds: 0,
      first_join_at: null,
      late_seconds: null,
      auto_status: 'absent',
      final_status: 'absent',
      zoom_names: [],
    }),
  ]
  const participants = [
    makeParticipant({ id: 100, display_name: 'Александр', normalized_name: 'александр' }),
    makeParticipant({
      id: 101,
      display_name: 'Мария Иванова',
      resolution: 'ignored',
      ignore_reason: 'host',
      total_seconds: 1800,
    }),
    makeParticipant({
      id: 102,
      display_name: 'Иванов Иван',
      resolution: 'matched',
      student_id: 1,
      student_name: 'Иванов Иван',
    }),
  ]
  return {
    lesson: makeLesson(),
    counts: { in_group: 4, online: 2, seen: 3, not_seen: 1, unidentified: 1, present: 3, absent: 1 },
    students,
    participants,
    as_of: '2026-10-06T15:40:00Z',
    ...overrides,
  }
}

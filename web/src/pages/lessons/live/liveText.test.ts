import { describe, expect, it } from 'vitest'

import {
  auditText,
  auditValue,
  COMMENT_MAX,
  deviceNumbers,
  EMPTY_FILTER_TITLES,
  filterCounts,
  filterStudents,
  findParticipant,
  groupParticipants,
  hiddenReasonLabel,
  isStudentFilter,
  leaveReasonLabel,
  looksLikeDeviceName,
  participantsOfStudent,
  REASON_MAX,
  restoreOutcome,
  STUDENT_FILTERS,
  undoOutcome,
  unmatchOutcome,
  zoomJoinUrl,
  zoomMeetingEnded,
} from '@/pages/lessons/live/liveText'
import { makeParticipant, makeStudent, makeView } from '@/pages/lessons/testing'

describe('student filters', () => {
  const rows = makeView().students

  it('counts every quick filter', () => {
    expect(filterCounts(rows)).toEqual({ all: 4, online: 2, left: 1, not_seen: 1 })
  })

  it('filters by status and by name or Zoom name', () => {
    expect(filterStudents(rows, 'online', '').map((r) => r.student_id)).toEqual([1, 3])
    expect(filterStudents(rows, 'all', 'ПЁТР').map((r) => r.student_id)).toEqual([2])
    expect(filterStudents(rows, 'all', 'iphone алексея').map((r) => r.student_id)).toEqual([3])
    expect(filterStudents(rows, 'left', 'сидоров')).toEqual([])
  })

  it('accepts only known filter values', () => {
    expect(isStudentFilter('not_seen')).toBe(true)
    expect(isStudentFilter('')).toBe(false)
    expect(isStudentFilter('toString')).toBe(false)
  })
})

describe('groupParticipants', () => {
  it('splits undecided and hidden participants, online first', () => {
    const participants = [
      makeParticipant({ id: 1, display_name: 'Б', current_status: 'left', first_join_at: '2026-10-06T15:00:00Z' }),
      makeParticipant({ id: 2, display_name: 'А', resolution: 'conflict', first_join_at: '2026-10-06T15:10:00Z' }),
      makeParticipant({ id: 3, display_name: 'В', first_join_at: '2026-10-06T15:05:00Z' }),
      makeParticipant({ id: 4, resolution: 'ignored', ignore_reason: 'host' }),
      makeParticipant({ id: 5, resolution: 'matched', student_id: 1 }),
      makeParticipant({ id: 6, resolution: 'manual', student_id: 1, current_status: 'left' }),
    ]
    const { unresolved, hidden } = groupParticipants(participants)
    expect(unresolved.map((p) => p.id)).toEqual([3, 2, 1])
    expect(hidden.map((p) => p.id)).toEqual([4])
    expect(participantsOfStudent(participants, 1).map((p) => p.id)).toEqual([5, 6])
    expect(participantsOfStudent(participants, 2)).toEqual([])
  })

  it('names the reason a participant is hidden', () => {
    expect(hiddenReasonLabel('host')).toBe('организатор')
    expect(hiddenReasonLabel('list')).toBe('из списка исключений')
    expect(hiddenReasonLabel('manual')).toBe('скрыт вручную')
    expect(hiddenReasonLabel('')).toBe('скрыт')
  })
})

describe('leaveReasonLabel', () => {
  it.each([
    ['', ''],
    ['   ', ''],
    // Engine codes.
    ['meeting_ended', 'конференция завершилась'],
    ['lesson_finished', 'занятие завершено вручную'],
    ['renamed', 'сменил имя'],
    // Zoom texts: the part after "Reason :" decides.
    ['left the meeting', 'вышел сам'],
    ['left the meeting. Reason : left the meeting', 'вышел сам'],
    ['left the meeting.Reason : Host ended the meeting.', 'организатор завершил конференцию'],
    ['left the meeting. Reason : Host closed the meeting', 'организатор завершил конференцию'],
    ['Host ended the meeting.', 'организатор завершил конференцию'],
    ['left the meeting. Reason : Removed by host', 'удалён организатором'],
    ['removed by host', 'удалён организатором'],
    ['left the meeting. Reason : got disconnected from Zoom server', 'обрыв связи'],
    ['Reason : Network connection error', 'обрыв связи'],
    ['left the meeting. Reason : lost connection', 'обрыв связи'],
    ['left the meeting. Reason : Unknown', 'причина неизвестна'],
    ['Unknown reason', 'причина неизвестна'],
    ['join breakout room', 'перешёл в комнату для групповой работы'],
    // What the processor really stores (the part after "Reason :"), see cmd/zoom-checker/simulate.go.
    ['leave breakout room', 'вышел из комнаты для групповой работы'],
    ['left the meeting. Reason : leave breakout room', 'вышел из комнаты для групповой работы'],
    ['Connection lost', 'обрыв связи'],
    ['Host closed the meeting.', 'организатор завершил конференцию'],
    ['Removed by host.', 'удалён организатором'],
    ['left the meeting with Unknown Failover Reason', 'причина неизвестна'],
    ['Leave waiting room', 'вышел из зала ожидания'],
    ['something new from Zoom', 'вышел'],
    // The processor's rename note, alone or after Zoom's reason.
    ['renamed from: iPhone', 'сменил имя (было «iPhone»)'],
    ['left the meeting. Reason : left the meeting; renamed from: iPhone Алексея', 'сменил имя (было «iPhone Алексея»), вышел сам'],
    ['Reason : Network connection error; renamed from: User 1', 'сменил имя (было «User 1»), обрыв связи'],
    ['meeting_ended; renamed from: Гость', 'сменил имя (было «Гость»), конференция завершилась'],
    // An old name with "; " inside stays whole.
    ['renamed from: Иванов; Петров', 'сменил имя (было «Иванов; Петров»)'],
  ])('%j → %j', (reason, label) => {
    expect(leaveReasonLabel(reason)).toBe(label)
  })

  it('shortens a very long old name', () => {
    const label = leaveReasonLabel(`renamed from: ${'Я'.repeat(100)}`)
    expect(label).toBe(`сменил имя (было «${'Я'.repeat(59)}…»)`)
  })
})

describe('participant decision outcomes', () => {
  const view = makeView()
  const name = 'Гость'

  it('finds the participant by id, else by the Zoom name', () => {
    expect(findParticipant(view, { id: 101, normalized_name: 'x' })?.display_name).toBe('Мария Иванова')
    expect(findParticipant(view, { id: 999, normalized_name: 'александр' })?.id).toBe(100)
    expect(findParticipant(view, { id: 999, normalized_name: 'нет' })).toBeUndefined()
    expect(findParticipant({}, { id: 100, normalized_name: 'александр' })).toBeUndefined()
    expect(findParticipant(undefined, { id: 100, normalized_name: 'александр' })).toBeUndefined()
  })

  it('says where a restored hidden participant ended up', () => {
    expect(restoreOutcome(name, makeParticipant({ resolution: 'unidentified' }))).toEqual({
      ok: true,
      message: '«Гость» больше не скрыт — укажите, кто это, в блоке «Не идентифицированы»',
    })
    expect(
      restoreOutcome(name, makeParticipant({ resolution: 'matched', student_id: 1, student_name: 'Иванов Иван' })),
    ).toEqual({ ok: true, message: '«Гость» снова учитывается: время засчитано студенту Иванов Иван' })
    expect(restoreOutcome(name, makeParticipant({ resolution: 'ignored', ignore_reason: 'list' }))).toEqual({
      ok: false,
      message:
        '«Гость» по-прежнему скрыт: это имя есть в общем списке скрываемых имён. Убрать его оттуда может администратор в настройках.',
    })
    expect(restoreOutcome(name, undefined)).toEqual({ ok: true, message: '«Гость» больше не скрыт' })
  })

  it('says what an undone match turned into', () => {
    expect(unmatchOutcome(name, makeParticipant({ resolution: 'unidentified' }))).toBe(
      'Сопоставление отменено: «Гость» теперь в блоке «Не идентифицированы»',
    )
    expect(unmatchOutcome(name, makeParticipant({ resolution: 'matched', student_id: 2, student_name: 'Петров Пётр' }))).toBe(
      'Сопоставление отменено: «Гость» теперь относится к студенту Петров Пётр',
    )
    expect(unmatchOutcome(name, makeParticipant({ resolution: 'ignored', ignore_reason: 'list' }))).toBe(
      'Сопоставление отменено: «Гость» скрыт (из списка исключений)',
    )
    expect(unmatchOutcome(name, undefined)).toBe('Сопоставление отменено: «Гость»')
  })

  it('says what an undone decision turned into', () => {
    expect(undoOutcome(name, makeParticipant({ resolution: 'matched', student_id: 1, student_name: 'Иванов Иван' }))).toBe(
      '«Гость» снова узнаётся автоматически: Иванов Иван',
    )
    expect(undoOutcome(name, makeParticipant({ resolution: 'ignored', ignore_reason: 'host' }))).toBe(
      '«Гость» снова скрыт (организатор)',
    )
    expect(undoOutcome(name, makeParticipant({ resolution: 'unidentified' }))).toBe('Решение по «Гость» отменено')
    expect(undoOutcome(name, undefined)).toBe('Решение по «Гость» отменено')
  })
})

describe('limits', () => {
  it('match the server (comment 2000, reason 500)', () => {
    expect(COMMENT_MAX).toBe(2000)
    expect(REASON_MAX).toBe(500)
  })
})

describe('auditValue', () => {
  it('translates attendance values and keeps server wording', () => {
    expect(auditValue('present')).toBe('Был')
    expect(auditValue('absent')).toBe('Не был')
    expect(auditValue('')).toBe('—')
    expect(auditValue('Присутствовал (вручную)')).toBe('Присутствовал (вручную)')
  })
})

describe('auditText', () => {
  it.each([
    ['«Иван»: студент Иванов Иван (id 12)', '«Иван»: студент Иванов Иван'],
    ['«iPhone» → Петров Пётр (id 3); имя запомнено для следующих занятий', '«iPhone» → Петров Пётр; имя запомнено для следующих занятий'],
    ['Студент №7 (id 7)', 'Студент №7'],
    ['Занятие (идёт)', 'Занятие (идёт)'],
    ['', ''],
  ])('%j → %j', (value, expected) => {
    expect(auditText(value)).toBe(expected)
  })

  it('is applied to audit values too', () => {
    expect(auditValue('студент Иванов Иван (id 12)')).toBe('студент Иванов Иван')
  })
})

describe('looksLikeDeviceName', () => {
  it.each([
    'iPhone',
    'iPad (2)',
    'Galaxy A52',
    'Redmi Note 8 Pro',
    'iPhone 13 Pro Max',
    'User123',
    'Zoom user',
    'Пользователь 5',
    'Гость',
    'телефон',
    '79161234567',
    '+7 916 123-45-67',
    '',
    'А',
  ])('%j looks like a device / default name', (name) => {
    expect(looksLikeDeviceName(name)).toBe(true)
  })

  it.each(['Александр', 'Иванов Иван', 'Max', 'iPhone Алексея', 'Alex’s iPhone', 'Мария 2'])(
    '%j may be a person',
    (name) => {
      expect(looksLikeDeviceName(name)).toBe(false)
    },
  )
})

describe('EMPTY_FILTER_TITLES', () => {
  it('has a plain-words title for every quick filter', () => {
    for (const f of STUDENT_FILTERS) expect(EMPTY_FILTER_TITLES[f.value]).toBeTruthy()
  })
})

describe('zoomJoinUrl', () => {
  it('prefers the stored https link', () => {
    expect(zoomJoinUrl({ zoom_join_url: 'https://us02web.zoom.us/j/123?pwd=x', zoom_meeting_id: '123' })).toBe(
      'https://us02web.zoom.us/j/123?pwd=x',
    )
  })

  it('never uses another scheme and falls back to the meeting number', () => {
    expect(zoomJoinUrl({ zoom_join_url: 'javascript:alert(1)', zoom_meeting_id: '1234567890' })).toBe(
      'https://zoom.us/j/1234567890',
    )
    expect(zoomJoinUrl({ zoom_join_url: '', zoom_meeting_id: '12' })).toBe('')
  })

  it('opens only Zoom sites', () => {
    expect(zoomJoinUrl({ zoom_join_url: 'https://zoom.com/j/1234567890', zoom_meeting_id: '1234567890' })).toBe(
      'https://zoom.com/j/1234567890',
    )
    for (const bad of ['https://evil.example/zoom.us/j/1', 'https://zoom.us.evil.example/j/1', 'https://notzoom.us/j/1']) {
      expect(zoomJoinUrl({ zoom_join_url: bad, zoom_meeting_id: '1234567890' })).toBe('https://zoom.us/j/1234567890')
    }
  })
})

describe('zoomMeetingEnded', () => {
  const session = (ended_at: string | null) => ({
    id: 1,
    meeting_uuid: 'u',
    meeting_id: '1',
    topic: '',
    lesson_id: 5,
    lesson: null,
    link_mode: 'auto' as const,
    started_at: null,
    ended_at,
    first_event_at: '2026-10-06T15:00:00Z',
  })

  it('is true only when every linked session has ended', () => {
    expect(zoomMeetingEnded({ sessions: [] })).toBe(false)
    expect(zoomMeetingEnded({ sessions: [session('2026-10-06T16:30:00Z')] })).toBe(true)
    expect(zoomMeetingEnded({ sessions: [session('2026-10-06T16:30:00Z'), session(null)] })).toBe(false)
  })
})

describe('fixtures', () => {
  it('make a consistent running lesson', () => {
    const v = makeView()
    expect(v.students.filter((s) => s.current_status === 'online')).toHaveLength(v.counts.online)
    expect(makeStudent().final_status).toBe('present')
  })
})

describe('deviceNumbers', () => {
  const at = (hhmm: string) => `2026-10-06T${hhmm}:00Z`
  const iv = (id: number, uuid: string, join: string, leave: string | null) => ({
    id,
    zoom_participant_uuid: uuid,
    join_at: at(join),
    leave_at: leave === null ? null : at(leave),
  })
  const now = Date.parse(at('19:40'))

  it('does not call reconnects devices: Zoom gives every join a new participant_uuid', () => {
    // 18:10–18:40, 18:50–19:30 (TZ scenario 4) and a third join: one laptop.
    const ivs = [iv(1, 'A', '18:10', '18:40'), iv(2, 'B', '18:50', '19:30'), iv(3, 'C', '19:31', null)]
    expect(deviceNumbers(ivs, now).size).toBe(0)
  })

  it('numbers devices that were in Zoom at the same time (PC 0–60, phone 30–90)', () => {
    const ivs = [iv(1, 'pc', '18:00', '19:00'), iv(2, 'phone', '18:30', '19:30')]
    expect([...deviceNumbers(ivs, now)]).toEqual([
      [1, 1],
      [2, 2],
    ])
  })

  it('reuses the free number for a later reconnect and keeps a breakout room on its device', () => {
    const ivs = [
      iv(1, 'pc', '18:00', '18:20'), // main room
      iv(2, 'pc', '18:20', '18:40'), // breakout room: same participant_uuid
      iv(3, 'phone', '18:10', '18:30'),
      iv(4, 'pc2', '18:45', null), // the PC again after a reconnect
    ]
    expect(Object.fromEntries(deviceNumbers(ivs, now))).toEqual({ 1: 1, 2: 1, 3: 2, 4: 1 })
  })

  it('counts open intervals until now and treats touching intervals as one device', () => {
    expect(deviceNumbers([iv(1, 'a', '18:00', null), iv(2, 'b', '18:30', null)], now).size).toBe(2)
    expect(deviceNumbers([iv(1, 'a', '18:00', '18:20'), iv(2, 'b', '18:20', null)], now).size).toBe(0)
    // Without a participant_uuid every interval stands on its own.
    expect(deviceNumbers([iv(1, '', '18:00', '18:30'), iv(2, '', '18:10', '18:20')], now).size).toBe(2)
  })
})

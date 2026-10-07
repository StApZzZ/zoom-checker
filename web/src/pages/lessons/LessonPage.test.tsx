import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), loading: vi.fn(), dismiss: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import type { QueryClient } from '@tanstack/react-query'

import { formatTime } from '@/lib/format'
import { applyAttendanceChange, createQueryClient } from '@/lib/queries'
import type { LessonView, Participant, StudentHistory, UpdateAttendanceInput, ZoomSession, ZoomStatus } from '@/lib/types'
import LessonPage from '@/pages/lessons/LessonPage'
import { makeLesson, makeParticipant, makeView } from '@/pages/lessons/testing'
import { FakeEventSource } from '@/test/fakeEventSource'
import { errorResponse, jsonResponse, mockFetch, renderRoutes, stubDownloads } from '@/test/utils'

type Handler = (url: URL, init: RequestInit) => Response | Promise<Response>

interface Sent {
  method: string
  path: string
  body: unknown
}

function setup(view: LessonView = makeView(), overrides: Record<string, Handler | unknown> = {}, client?: QueryClient) {
  const current = { view }
  const sent: Sent[] = []
  const record =
    (respond: Handler = () => jsonResponse({})): Handler =>
    (url, init) => {
      sent.push({ method: init.method ?? 'GET', path: url.pathname, body: init.body ? JSON.parse(String(init.body)) : undefined })
      return respond(url, init)
    }
  const handlers: Record<string, Handler | unknown> = {
    'GET /api/lessons/5/attendance': () => jsonResponse(current.view),
    'POST /api/lessons/5/finish': record(),
    'POST /api/lessons/5/reopen': record(),
    'POST /api/lessons/5/recompute': record(),
    'DELETE /api/lessons/5': record(() => new Response(null, { status: 204 })),
    'GET /api/lessons': [],
    'GET /api/groups': [],
    'POST /api/zoom/sessions/7/link': record(() => jsonResponse({})),
  }
  let tick = 0
  for (const s of view.students) {
    // Behaves like the server: the change is visible in the next attendance fetch.
    handlers[`PATCH /api/lessons/5/attendance/${s.student_id}`] = record((_url, init) => {
      const input = JSON.parse(String(init.body)) as UpdateAttendanceInput
      tick += 1
      current.view = {
        ...applyAttendanceChange(current.view, s.student_id, input),
        as_of: new Date(Date.parse(current.view.as_of) + tick * 1000).toISOString(),
      }
      return jsonResponse({})
    })
    handlers[`GET /api/lessons/5/students/${s.student_id}/history`] = { intervals: [], audit: [] } satisfies StudentHistory
  }
  for (const p of view.participants) {
    handlers[`POST /api/lessons/5/participants/${p.id}/map`] = record()
    handlers[`DELETE /api/lessons/5/participants/${p.id}/map`] = record()
  }
  Object.assign(handlers, overrides)
  const fetchMock = mockFetch(handlers)
  const user = userEvent.setup()
  const utils = renderRoutes(
    [
      { path: '/lessons/:id', element: <LessonPage /> },
      { path: '/', element: <p>Список занятий</p> },
    ],
    '/lessons/5',
    client,
  )
  return { ...utils, user, sent, fetchMock, current, record }
}

function zoomSession(overrides: Partial<ZoomSession> = {}): ZoomSession {
  return {
    id: 7,
    meeting_uuid: 'uuid-7',
    meeting_id: '1234567890',
    topic: 'DevOps',
    lesson_id: 5,
    lesson: null,
    link_mode: 'auto',
    started_at: '2026-10-06T15:00:00Z',
    ended_at: '2026-10-06T16:31:00Z',
    first_event_at: '2026-10-06T15:00:00Z',
    ...overrides,
  }
}

/** A copy of `view` whose participant `id` is replaced by `changes` (what the server answers after a decision). */
function withParticipant(view: LessonView, id: number, changes: Partial<Participant>): LessonView {
  return {
    ...view,
    participants: view.participants.map((p) => (p.id === id ? { ...p, ...changes } : p)),
    as_of: new Date(Date.parse(view.as_of) + 1000).toISOString(),
  }
}

/** A stat tile (the filter chips below reuse some labels, so look inside the summary only). */
function tile(label: string): HTMLElement {
  const summary = screen.getByRole('group', { name: 'Сводка по занятию' })
  const el = within(summary).getByText(label).closest<HTMLElement>('button, div.relative')
  if (!el) throw new Error(`no tile ${label}`)
  return el
}

const row = (name: string) => {
  const el = screen.getByRole('button', { name }).closest('tr')
  if (!el) throw new Error(`no row ${name}`)
  return el
}

beforeEach(() => {
  toastMock.success.mockReset()
  toastMock.error.mockReset()
  toastMock.loading.mockReset().mockReturnValue('toast-1')
  toastMock.dismiss.mockReset()
  FakeEventSource.reset()
  vi.stubGlobal('EventSource', FakeEventSource)
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('LessonPage', () => {
  it('renders the lesson snapshot: header, counters, students and participants', async () => {
    setup()
    expect(await screen.findByRole('heading', { level: 1, name: 'DevOps. Занятие №4' })).toBeInTheDocument()
    expect(document.title).toBe('DevOps. Занятие №4 · Zoom Checker')
    expect(screen.getByRole('link', { name: 'DevOps-1' })).toHaveAttribute('href', '/groups/2')
    expect(screen.getByText('123 456 7890')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Открыть в Zoom' })).toHaveAttribute('href', 'https://zoom.us/j/1234567890')
    expect(screen.getByText('Идёт')).toBeInTheDocument()
    expect(screen.getByText('Подключаемся…')).toBeInTheDocument()

    expect(within(tile('В группе')).getByText('4')).toBeInTheDocument()
    expect(within(tile('Сейчас в Zoom')).getByText('2')).toBeInTheDocument()
    expect(within(tile('Были хотя бы раз')).getByText('3')).toBeInTheDocument()
    expect(within(tile('Не подключались')).getByText('1')).toBeInTheDocument()
    expect(within(tile('Не идентифицированы')).getByText('1')).toBeInTheDocument()

    const ivanov = row('Иванов Иван')
    expect(within(ivanov).getByText('В Zoom')).toBeInTheDocument()
    expect(within(ivanov).getByText('00:10')).toBeInTheDocument()
    expect(within(ivanov).getByText('опоздание 1 мин')).toBeInTheDocument()
    expect(within(row('Петров Пётр')).getByText('Вышел')).toBeInTheDocument()
    const sidorov = row('Сидоров Алексей')
    expect(within(sidorov).getByText('2 устройства')).toBeInTheDocument()
    expect(within(sidorov).getByText('опоздание 12 мин')).toBeInTheDocument()
    expect(within(sidorov).getByText('вручную')).toBeInTheDocument()
    expect(within(sidorov).getByText('Предупредил об опоздании')).toBeInTheDocument()
    const orlov = row('Орлов Андрей')
    expect(within(orlov).getByText('Не подключался')).toBeInTheDocument()
    expect(within(orlov).getByRole('radio', { name: /Не был/ })).toHaveAttribute('aria-checked', 'true')

    const unidentified = screen.getByRole('region', { name: /Не идентифицированы/ })
    expect(within(unidentified).getByText('«Александр»')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Скрытые участники (1)' })).toHaveAttribute('aria-expanded', 'false')
  })

  it('filters students with the quick chips and the search box', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { level: 1 })
    const chips = screen.getByRole('radiogroup', { name: 'Показать студентов' })
    expect(within(chips).getByRole('radio', { name: 'Все 4' })).toHaveAttribute('aria-checked', 'true')
    expect(within(chips).getByRole('radio', { name: 'В Zoom 2' })).toBeInTheDocument()
    expect(within(chips).getByRole('radio', { name: 'Вышли 1' })).toBeInTheDocument()

    await user.click(within(chips).getByRole('radio', { name: 'Не подключались 1' }))
    expect(screen.getByRole('button', { name: 'Орлов Андрей' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Иванов Иван' })).toBeNull()

    await user.click(within(chips).getByRole('radio', { name: 'Все 4' }))
    await user.type(screen.getByRole('searchbox', { name: 'Найти студента' }), 'сидоров')
    expect(screen.getByRole('button', { name: 'Сидоров Алексей' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Орлов Андрей' })).toBeNull()

    // Zoom names are searchable too.
    await user.clear(screen.getByRole('searchbox', { name: 'Найти студента' }))
    await user.type(screen.getByRole('searchbox', { name: 'Найти студента' }), 'iphone')
    expect(screen.getByRole('button', { name: 'Сидоров Алексей' })).toBeInTheDocument()

    await user.type(screen.getByRole('searchbox', { name: 'Найти студента' }), 'zzz')
    expect(screen.getByText('Никого не нашли')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Показать всех' }))
    expect(screen.getAllByRole('row')).toHaveLength(5)
  })

  it('toggles the final status optimistically and sends it to the server', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const { user, sent, current } = setup(makeView(), {
      'PATCH /api/lessons/5/attendance/4': async (url: URL, init: RequestInit) => {
        sent.push({ method: 'PATCH', path: url.pathname, body: JSON.parse(String(init.body)) })
        await gate
        current.view = {
          ...current.view,
          students: current.view.students.map((s) =>
            s.student_id === 4 ? { ...s, manual_status: 'present', final_status: 'present' } : s,
          ),
          as_of: '2026-10-06T15:41:00Z',
        }
        return jsonResponse({})
      },
    })
    await screen.findByRole('heading', { level: 1 })
    const toggle = within(row('Орлов Андрей')).getByRole('radiogroup', { name: 'Итог: Орлов Андрей' })
    await user.click(within(toggle).getByRole('radio', { name: /^Был/ }))

    // Flipped before the server answered.
    await waitFor(() => expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true'))
    expect(within(row('Орлов Андрей')).getByText('вручную')).toBeInTheDocument()
    expect(sent).toEqual([{ method: 'PATCH', path: '/api/lessons/5/attendance/4', body: { status: 'present' } }])

    act(() => release())
    await waitFor(() => expect(screen.getByText(/Итог сейчас/)).toHaveTextContent('Итог сейчас: были — 4, не были — 0'))
    expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true')
  })

  it('rolls the toggle back when saving fails', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const { user } = setup(makeView(), {
      'PATCH /api/lessons/5/attendance/4': async () => {
        await gate
        return errorResponse(500, 'internal', 'Ошибка на сервере. Попробуйте ещё раз.')
      },
    })
    await screen.findByRole('heading', { level: 1 })
    const toggle = within(row('Орлов Андрей')).getByRole('radiogroup', { name: 'Итог: Орлов Андрей' })
    await user.click(within(toggle).getByRole('radio', { name: /^Был/ }))
    await waitFor(() => expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true'))

    act(() => release())
    await waitFor(() =>
      expect(within(toggle).getByRole('radio', { name: /Не был/ })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(within(row('Орлов Андрей')).queryByText('вручную')).toBeNull()
  })

  it('keeps a pending mark while live snapshots arrive, then shows the saved view without refetching', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let viewRequests = 0
    const saved = applyAttendanceChange(makeView({ as_of: '2026-10-06T15:42:00Z' }), 4, { status: 'present' })
    saved.students = saved.students.map((s) => (s.student_id === 1 ? { ...s, comment: 'Отметка сохранена сервером' } : s))
    const { user } = setup(makeView(), {
      'GET /api/lessons/5/attendance': () => {
        viewRequests += 1
        return jsonResponse(makeView())
      },
      'PATCH /api/lessons/5/attendance/4': async () => {
        await gate
        return jsonResponse(saved)
      },
    })
    await screen.findByRole('heading', { level: 1 })
    const es = FakeEventSource.latest()
    act(() => es.open())
    const toggle = within(row('Орлов Андрей')).getByRole('radiogroup', { name: 'Итог: Орлов Андрей' })
    await user.click(within(toggle).getByRole('radio', { name: /^Был/ }))
    await waitFor(() => expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true'))

    // A snapshot computed before the change was saved: newer, but still "Не был" for this student.
    const snapshot = makeView({ as_of: '2026-10-06T15:41:00Z' })
    snapshot.students = snapshot.students.map((s) =>
      s.student_id === 2 ? { ...s, current_status: 'online', last_leave_at: null } : s,
    )
    act(() => es.emit('snapshot', JSON.stringify(snapshot)))
    // The rest of the snapshot applies…
    await waitFor(() => expect(within(row('Петров Пётр')).getByText('В Zoom')).toBeInTheDocument())
    // …but the toggle being saved does not flip back.
    expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true')
    expect(within(row('Орлов Андрей')).getByText('вручную')).toBeInTheDocument()

    const requestsBefore = viewRequests
    act(() => release())
    // The view the server answered with is shown directly.
    expect(await screen.findByText('Отметка сохранена сервером')).toBeInTheDocument()
    expect(within(toggle).getByRole('radio', { name: /^Был/ })).toHaveAttribute('aria-checked', 'true')
    expect(screen.getByText(/Итог сейчас/)).toHaveTextContent('Итог сейчас: были — 4, не были — 0')
    await new Promise((r) => setTimeout(r, 20))
    expect(viewRequests).toBe(requestsBefore)
  })

  it('returns a manual mark back to the automatic one', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Вернуть автоматическую отметку: Сидоров Алексей' }))
    expect(sent).toContainEqual({ method: 'PATCH', path: '/api/lessons/5/attendance/3', body: { status: null } })
    const toggle = within(row('Сидоров Алексей')).getByRole('radiogroup')
    // auto_status of the fixture is "absent".
    await waitFor(() =>
      expect(within(toggle).getByRole('radio', { name: /Не был/ })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(within(row('Сидоров Алексей')).queryByText('вручную')).toBeNull()
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('Сидоров Алексей: отметка снова ставится автоматически'),
    )
  })

  it('saves a comment from the row popover', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Добавить комментарий: Иванов Иван' }))
    await user.type(screen.getByLabelText('Комментарий · Иванов Иван'), 'Сидел с телефона')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Комментарий сохранён'))
    expect(sent).toContainEqual({
      method: 'PATCH',
      path: '/api/lessons/5/attendance/1',
      body: { comment: 'Сидел с телефона' },
    })
  })

  it('maps an unidentified participant to a student', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Это студент…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Кто это — «Александр»?' })
    // Students who have not joined yet are offered first.
    expect(within(dialog).getByText('Ещё не подключались')).toBeInTheDocument()
    expect(within(dialog).getByText('Студент не выбран')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Сопоставить' })).toBeDisabled()
    await user.type(within(dialog).getByRole('combobox', { name: 'Найти студента' }), 'орл')
    await user.click(within(dialog).getByRole('option', { name: /Орлов Андрей/ }))
    expect(within(dialog).getByText('Выбран:')).toHaveTextContent('Выбран: Орлов Андрей')
    expect(within(dialog).getByRole('checkbox', { name: 'Запомнить это имя для следующих занятий' })).toHaveAttribute(
      'aria-checked',
      'true',
    )
    await user.click(within(dialog).getByRole('button', { name: 'Сопоставить' }))

    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('Время присутствия перенесено студенту Орлов Андрей'),
    )
    expect(sent).toContainEqual({
      method: 'POST',
      path: '/api/lessons/5/participants/100/map',
      body: { action: 'student', student_id: 4, remember_alias: true },
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('preselects a name candidate for a conflict and does not remember the ambiguous name', async () => {
    const view = makeView()
    view.participants = [
      makeParticipant({ id: 100, display_name: 'Иванов', resolution: 'conflict', candidate_student_ids: [4, 1] }),
    ]
    const { user, sent } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByText('Подходит нескольким студентам: Орлов Андрей, Иванов Иван')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Это студент…' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Подходят по имени')).toBeInTheDocument()
    expect(within(dialog).getByText('Выбран:')).toHaveTextContent('Выбран: Орлов Андрей')
    const remember = within(dialog).getByRole('checkbox', { name: 'Запомнить это имя для следующих занятий' })
    expect(remember).toHaveAttribute('aria-checked', 'false')
    await user.click(within(dialog).getByRole('button', { name: 'Сопоставить' }))
    await waitFor(() =>
      expect(sent).toContainEqual({
        method: 'POST',
        path: '/api/lessons/5/participants/100/map',
        body: { action: 'student', student_id: 4, remember_alias: false },
      }),
    )
  })

  it('hides a participant who is not a student', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Не студент' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Скрыть «Александр»?' })
    await user.click(within(dialog).getByRole('checkbox', { name: 'Всегда скрывать это имя' }))
    await user.click(within(dialog).getByRole('button', { name: 'Скрыть' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('«Александр» скрыт и будет скрываться на следующих занятиях'),
    )
    expect(sent).toContainEqual({
      method: 'POST',
      path: '/api/lessons/5/participants/100/map',
      body: { action: 'ignore', ignore_always: true },
    })
  })

  it('restores a participant hidden by a rule (organizer) with "none" for this lesson', async () => {
    const view = makeView()
    view.participants = [makeParticipant({ id: 101, display_name: 'Мария Иванова', resolution: 'ignored', ignore_reason: 'host' })]
    const { user, sent, record } = setup(view, {
      'POST /api/lessons/5/participants/101/map': (url: URL, init: RequestInit) =>
        record(() => jsonResponse(withParticipant(view, 101, { resolution: 'unidentified', ignore_reason: '', manual: true })))(
          url,
          init,
        ),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Скрытые участники (1)' }))
    expect(screen.getByText('«Мария Иванова»').nextElementSibling).toHaveTextContent(/^организатор ·/)
    await user.click(screen.getByRole('button', { name: 'Вернуть' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith(
        '«Мария Иванова» больше не скрыт — укажите, кто это, в блоке «Не идентифицированы»',
      ),
    )
    expect(sent).toEqual([{ method: 'POST', path: '/api/lessons/5/participants/101/map', body: { action: 'none' } }])
    // The answered view is shown right away: the participant moved to "Не идентифицированы".
    const unidentified = screen.getByRole('region', { name: /Не идентифицированы/ })
    expect(within(unidentified).getByText('«Мария Иванова»')).toBeInTheDocument()
  })

  it('restores a participant hidden by the teacher by removing that decision', async () => {
    const view = makeView()
    view.participants = [
      makeParticipant({ id: 103, display_name: 'Гость', normalized_name: 'гость', resolution: 'ignored', ignore_reason: 'manual', manual: true }),
    ]
    const after = withParticipant(view, 103, {
      resolution: 'matched',
      ignore_reason: '',
      manual: false,
      student_id: 2,
      student_name: 'Петров Пётр',
    })
    const { user, sent, record } = setup(view, {
      'DELETE /api/lessons/5/participants/103/map': (url: URL, init: RequestInit) => record(() => jsonResponse(after))(url, init),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Скрытые участники (1)' }))
    expect(screen.getByText('«Гость»').nextElementSibling).toHaveTextContent(/^скрыт вручную ·/)
    await user.click(screen.getByRole('button', { name: 'Вернуть' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('«Гость» снова учитывается: время засчитано студенту Петров Пётр'),
    )
    // Matched again on its own: no "none" override needed.
    expect(sent).toEqual([{ method: 'DELETE', path: '/api/lessons/5/participants/103/map', body: undefined }])
  })

  it('overrides the global hidden-names list when removing the decision is not enough', async () => {
    const view = makeView()
    view.participants = [
      makeParticipant({ id: 103, display_name: 'Гость', normalized_name: 'гость', resolution: 'ignored', ignore_reason: 'manual', manual: true }),
    ]
    // "Всегда скрывать это имя" put the name on the global list: without the decision it is hidden by the list.
    const byList = withParticipant(view, 103, { ignore_reason: 'list', manual: false })
    const counted = withParticipant(byList, 103, { resolution: 'unidentified', ignore_reason: '', manual: true })
    const { user, sent, record } = setup(view, {
      'DELETE /api/lessons/5/participants/103/map': (url: URL, init: RequestInit) => record(() => jsonResponse(byList))(url, init),
      'POST /api/lessons/5/participants/103/map': (url: URL, init: RequestInit) => record(() => jsonResponse(counted))(url, init),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Скрытые участники (1)' }))
    await user.click(screen.getByRole('button', { name: 'Вернуть' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('«Гость» больше не скрыт — укажите, кто это, в блоке «Не идентифицированы»'),
    )
    expect(sent).toEqual([
      { method: 'DELETE', path: '/api/lessons/5/participants/103/map', body: undefined },
      { method: 'POST', path: '/api/lessons/5/participants/103/map', body: { action: 'none' } },
    ])
    expect(toastMock.error).not.toHaveBeenCalled()
  })

  it('says so honestly when a restored participant is still hidden', async () => {
    const view = makeView()
    view.participants = [makeParticipant({ id: 104, display_name: 'Ассистент', resolution: 'ignored', ignore_reason: 'list' })]
    const { user } = setup(view, {
      // E.g. another teacher hid it again in between.
      'POST /api/lessons/5/participants/104/map': () => jsonResponse(withParticipant(view, 104, {})),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Скрытые участники (1)' }))
    await user.click(screen.getByRole('button', { name: 'Вернуть' }))
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith(expect.stringMatching(/^«Ассистент» по-прежнему скрыт: /)))
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('says so when every Zoom participant is identified', async () => {
    const view = makeView({ counts: { ...makeView().counts, unidentified: 0 } })
    view.participants = view.participants.filter((p) => p.resolution === 'matched')
    setup(view)
    expect(await screen.findByText('Все участники Zoom опознаны 👍')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Не идентифицированы/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Скрытые участники/ })).toBeNull()
  })

  it('scrolls to the unidentified block from its tile', async () => {
    const scroll = vi.spyOn(HTMLElement.prototype, 'scrollIntoView')
    const { user } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: /Не идентифицированы/ }))
    expect(scroll).toHaveBeenCalled()
    expect(screen.getByRole('region', { name: /Не идентифицированы/ })).toHaveFocus()
  })

  it('applies live snapshots in place and shows the live indicator', async () => {
    setup()
    await screen.findByRole('heading', { level: 1 })
    const es = FakeEventSource.latest()
    expect(es.url).toBe('/api/lessons/5/stream')
    act(() => es.open())
    expect(screen.getByText('Обновляется автоматически')).toBeInTheDocument()

    const next = makeView({ as_of: '2026-10-06T15:45:00Z' })
    next.students = next.students.map((s) =>
      s.student_id === 4 ? { ...s, current_status: 'online', first_join_at: '2026-10-06T15:44:00Z', total_seconds: 60 } : s,
    )
    next.counts = { ...next.counts, online: 3, seen: 4, not_seen: 0 }
    const before = row('Орлов Андрей')
    act(() => es.emit('snapshot', JSON.stringify(next)))
    // TanStack Query notifies observers on the next tick.
    await waitFor(() => expect(within(row('Орлов Андрей')).getByText('В Zoom')).toBeInTheDocument())
    // Same DOM row: updated in place, no reload flicker or loading state.
    expect(row('Орлов Андрей')).toBe(before)
    expect(screen.queryByRole('status', { name: /Загружаем/ })).toBeNull()
    expect(within(tile('Сейчас в Zoom')).getByText('3')).toBeInTheDocument()
    expect(within(tile('Не подключались')).getByText('0')).toBeInTheDocument()

    // A slightly older snapshot (e.g. a slow poll) must not roll the screen back. (Minutes
    // older would mean the server clock was set back: see isNewerView.)
    act(() => es.emit('snapshot', JSON.stringify(makeView({ as_of: '2026-10-06T15:44:50Z' }))))
    await new Promise((r) => setTimeout(r, 10))
    expect(within(row('Орлов Андрей')).getByText('В Zoom')).toBeInTheDocument()

    // Connection drops: the last data stays on screen, the indicator says we are reconnecting.
    act(() => es.drop())
    expect(await screen.findByText('Подключаемся…')).toBeInTheDocument()
    expect(within(row('Орлов Андрей')).getByText('В Zoom')).toBeInTheDocument()
  })

  it('shows the waiting banner with the name reminder before anyone joined', async () => {
    const view = makeView({
      lesson: makeLesson({ status: 'planned', actual_start_at: null, planned_start_at: '2099-10-06T15:00:00Z', planned_end_at: '2099-10-06T16:30:00Z' }),
      counts: { in_group: 4, online: 0, seen: 0, not_seen: 4, unidentified: 0, present: 0, absent: 4 },
      participants: [],
    })
    view.students = view.students.map((s) => ({ ...s, current_status: 'not_seen', total_seconds: 0, first_join_at: null }))
    const { user } = setup(view)
    expect(
      await screen.findByText(
        'Ждём начала занятия в Zoom. Как только студенты начнут подключаться, список обновится сам.',
      ),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Памятка для студентов: как подписаться в Zoom' }))
    expect(screen.getByText(/Ответственность за корректное отображаемое имя в Zoom лежит на участнике/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Скопировать текст' })).toBeInTheDocument()
    expect(screen.getByText('Пока никого')).toBeInTheDocument()
  })

  describe('waiting banner: why nobody shows up', () => {
    const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()
    const zoomStatus = (overrides: Partial<ZoomStatus> = {}): ZoomStatus => ({
      webhook_url: 'https://attendance.example.com/api/webhooks/zoom',
      secret_configured: true,
      last_event_at: minutesAgo(3),
      last_event_type: 'meeting.participant_joined',
      events_24h: 10,
      failed_count: 0,
      pending_count: 0,
      ...overrides,
    })
    const runningSession = (overrides: Partial<ZoomSession> = {}): ZoomSession => ({
      id: 7,
      meeting_uuid: 'uuid-7',
      meeting_id: '87022223333',
      topic: 'Мгновенная конференция',
      lesson_id: null,
      lesson: null,
      link_mode: 'auto',
      started_at: minutesAgo(9),
      ended_at: null,
      first_event_at: minutesAgo(9),
      ...overrides,
    })
    /** A lesson that should have started `startedMinutesAgo` ago and nobody has joined. */
    const waitingView = (startedMinutesAgo: number) => {
      const view = makeView({
        lesson: makeLesson({
          status: 'planned',
          actual_start_at: null,
          planned_start_at: minutesAgo(startedMinutesAgo),
          planned_end_at: minutesAgo(startedMinutesAgo - 90),
        }),
        counts: { in_group: 4, online: 0, seen: 0, not_seen: 4, unidentified: 0, present: 0, absent: 4 },
        participants: [],
      })
      view.students = view.students.map((st) => ({ ...st, current_status: 'not_seen', total_seconds: 0, first_join_at: null }))
      return view
    }
    const CHECK_ZOOM = /Проверьте, что в Zoom запущена конференция 123 456 7890/

    it('points to a conference that runs under another number and links it after confirmation', async () => {
      const { user, sent, fetchMock } = setup(
        waitingView(10),
        {
          'GET /api/zoom/status': zoomStatus(),
          'GET /api/zoom/sessions': [runningSession(), runningSession({ id: 8, meeting_uuid: 'old', started_at: minutesAgo(600) })],
        },
        // The app's client: its mutation cache shows the success toast.
        createQueryClient(),
      )
      const banner = await screen.findByRole('region', { name: 'Ожидание начала занятия' })
      expect(within(banner).getByText(CHECK_ZOOM)).toBeInTheDocument()
      const item = await within(banner).findByText(/Похоже, конференция идёт под другим номером:/)
      expect(item).toHaveTextContent(/870 2222 3333 · «Мгновенная конференция» · с \d\d:\d\d/)
      // Only the conference that is going on around the planned start.
      expect(within(banner).getAllByRole('button', { name: 'Привязать к этому занятию' })).toHaveLength(1)
      const sessionsCall = fetchMock.mock.calls
        .map(([u]) => new URL(String(u), 'http://localhost'))
        .find((u) => u.pathname === '/api/zoom/sessions')
      expect(sessionsCall?.searchParams.get('unlinked')).toBe('1')

      await user.click(within(banner).getByRole('button', { name: 'Привязать к этому занятию' }))
      const dialog = await screen.findByRole('alertdialog', { name: 'Привязать конференцию к этому занятию?' })
      expect(dialog).toHaveTextContent('Если это конференция коллеги, не привязывайте её.')
      await user.click(within(dialog).getByRole('button', { name: 'Привязать' }))
      await waitFor(() =>
        expect(sent).toContainEqual({ method: 'POST', path: '/api/zoom/sessions/7/link', body: { lesson_id: 5 } }),
      )
      expect(toastMock.success).toHaveBeenCalledWith('Конференция привязана к занятию')
    })

    it('says how to fix the number when no running conference fits', async () => {
      setup(waitingView(10), { 'GET /api/zoom/status': zoomStatus(), 'GET /api/zoom/sessions': [] })
      const banner = await screen.findByRole('region', { name: 'Ожидание начала занятия' })
      expect(
        await within(banner).findByText(/Если в Zoom другой номер — исправьте его в занятии: «Действия» → «Изменить»/),
      ).toBeInTheDocument()
      expect(within(banner).getByRole('link', { name: '«Zoom»' })).toHaveAttribute('href', '/zoom')
      expect(within(banner).queryByRole('button', { name: 'Привязать к этому занятию' })).toBeNull()
    })

    it('does not look for other conferences before the lesson is late', async () => {
      const { fetchMock } = setup(waitingView(1), { 'GET /api/zoom/status': zoomStatus(), 'GET /api/zoom/sessions': [runningSession()] })
      await screen.findByText('Обновлять страницу не нужно — просто держите её открытой.')
      expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/api/zoom/sessions'))).toBe(false)
      expect(screen.queryByText(/Похоже, конференция идёт под другим номером/)).toBeNull()
    })

    it('blames the missing Zoom connection, not the teacher\'s meeting, even before the start', async () => {
      setup(waitingView(-30), { 'GET /api/zoom/status': zoomStatus({ secret_configured: false, last_event_at: null }) })
      const banner = await screen.findByRole('region', { name: 'Ожидание начала занятия' })
      expect(
        await within(banner).findByText(
          'Сервис пока не подключён к Zoom, поэтому посещаемость не отмечается. Сообщите администратору.',
        ),
      ).toBeInTheDocument()
      expect(within(banner).queryByText(CHECK_ZOOM)).toBeNull()
      expect(within(banner).queryByText(/Ждём начала занятия в Zoom/)).toBeNull()
      // The name reminder for students still helps once the connection is fixed.
      expect(within(banner).getByRole('button', { name: 'Памятка для студентов: как подписаться в Zoom' })).toBeInTheDocument()
      // Outside the app shell there is no signed-in admin: no setup link for a teacher.
      expect(within(banner).queryByRole('link', { name: /Подключить Zoom/ })).toBeNull()
    })

    it('adds that Zoom has never sent anything when the connection is not finished', async () => {
      setup(waitingView(10), { 'GET /api/zoom/status': zoomStatus({ last_event_at: null }), 'GET /api/zoom/sessions': [] })
      const banner = await screen.findByRole('region', { name: 'Ожидание начала занятия' })
      expect(
        await within(banner).findByText(
          'Из Zoom ещё ни разу не приходили события — возможно, подключение не закончено. Сообщите администратору.',
        ),
      ).toBeInTheDocument()
      expect(within(banner).getByText(CHECK_ZOOM)).toBeInTheDocument()
    })

    it('keeps the usual wording when the Zoom status cannot be loaded', async () => {
      setup(waitingView(10))
      const banner = await screen.findByRole('region', { name: 'Ожидание начала занятия' })
      expect(within(banner).getByText(CHECK_ZOOM)).toBeInTheDocument()
      expect(within(banner).queryByText(/не подключён к Zoom/)).toBeNull()
    })
  })

  it('shows the finished banner with export buttons that download the file', async () => {
    const { click, anchors } = stubDownloads()
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const end = '2026-10-06T16:31:00Z'
    const { user, fetchMock } = setup(makeView({ lesson: makeLesson({ status: 'finished', actual_end_at: end }) }), {
      'GET /api/lessons/5/export': async (url: URL) => {
        await gate
        return new Response('xlsx-bytes', {
          headers: {
            'Content-Type': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
            'Content-Disposition': `attachment; filename="lesson.${url.searchParams.get('format')}"; filename*=UTF-8''${encodeURIComponent('Посещаемость DevOps 06.10.2026')}.${url.searchParams.get('format')}`,
          },
        })
      },
    })
    expect(await screen.findByText(`Занятие завершено в ${formatTime(end)}.`)).toBeInTheDocument()
    expect(screen.getByText('Итоги зафиксированы; вы можете исправить отметки вручную.')).toBeInTheDocument()
    expect(screen.queryByText('Подключаемся…')).toBeNull()
    const banner = screen.getByRole('region', { name: 'Занятие завершено' })
    // One prominent pair, not repeated in the header.
    expect(screen.getAllByRole('button', { name: 'Скачать Excel' })).toHaveLength(1)
    const excel = within(banner).getByRole('button', { name: 'Скачать Excel' })
    await user.click(excel)
    // Busy while the file is prepared: a second click cannot start a second download.
    await waitFor(() => expect(excel).toBeDisabled())
    expect(excel).toHaveAttribute('aria-busy', 'true')
    expect(toastMock.loading).toHaveBeenCalledWith('Готовим файл…')
    act(() => release())
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
    expect(anchors()[0].download).toBe('Посещаемость DevOps 06.10.2026.xlsx')
    expect(anchors()[0].getAttribute('href')).toBe('blob:zoom-checker/1')
    await waitFor(() => expect(excel).toBeEnabled())
    // The "Готовим файл…" toast turns into the success one.
    expect(toastMock.success).toHaveBeenCalledWith('Файл скачан', expect.objectContaining({ id: 'toast-1' }))
    expect(fetchMock.mock.calls.some(([u]) => String(u).startsWith('/api/lessons/5/export?format=xlsx&tz='))).toBe(true)

    await user.click(within(banner).getByRole('button', { name: 'Скачать CSV' }))
    await waitFor(() => expect(click).toHaveBeenCalledTimes(2))
    expect(anchors()[1].download).toBe('Посещаемость DevOps 06.10.2026.csv')
  })

  it('shows the server message when an export fails instead of saving the error', async () => {
    const { click } = stubDownloads()
    const end = '2026-10-06T16:31:00Z'
    const { user } = setup(
      makeView({ lesson: makeLesson({ status: 'finished', actual_end_at: end }) }),
      { 'GET /api/lessons/5/export': () => errorResponse(500, 'internal', 'Не удалось подготовить файл. Попробуйте ещё раз.') },
      createQueryClient(),
    )
    const banner = await screen.findByRole('region', { name: 'Занятие завершено' })
    await user.click(within(banner).getByRole('button', { name: 'Скачать CSV' }))
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('Не удалось подготовить файл. Попробуйте ещё раз.'))
    expect(click).not.toHaveBeenCalled()
    expect(toastMock.success).not.toHaveBeenCalled()
    expect(toastMock.dismiss).toHaveBeenCalledWith('toast-1')
    expect(within(banner).getByRole('button', { name: 'Скачать CSV' })).toBeEnabled()
  })

  it('downloads from the actions menu of a running lesson', async () => {
    const { click, anchors } = stubDownloads()
    const { user } = setup(makeView(), {
      'GET /api/lessons/5/export': () =>
        new Response('a;b', { headers: { 'Content-Disposition': 'attachment; filename="lesson-5.csv"' } }),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'CSV' }))
    await waitFor(() => expect(click).toHaveBeenCalledTimes(1))
    expect(anchors()[0].download).toBe('lesson-5.csv')
  })

  it('finishes the lesson from the actions menu', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Завершить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Завершить занятие?' })
    expect(within(dialog).getByLabelText('Время окончания')).toHaveValue(formatTime(new Date()))
    await user.click(within(dialog).getByRole('button', { name: 'Завершить занятие' }))
    await waitFor(() => expect(sent.some((s) => s.path === '/api/lessons/5/finish')).toBe(true))
    const body = sent.find((s) => s.path === '/api/lessons/5/finish')?.body as { at: string }
    expect(Math.abs(Date.parse(body.at) - Date.now())).toBeLessThan(120_000)
    // The success toast comes from the app QueryClient (mutation meta); here: the dialog closes.
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('does not accept a finish time in the future', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Завершить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Завершить занятие?' })
    const time = within(dialog).getByLabelText('Время окончания')
    const inTwoHours = new Date(Date.now() + 2 * 3600_000)
    // Only meaningful while "now + 2 h" is still today (the date field is hidden for today's lesson).
    if (inTwoHours.getDate() === new Date().getDate()) {
      await user.clear(time)
      await user.type(time, formatTime(inTwoHours))
      expect(within(dialog).getByText('Это время ещё не наступило')).toBeInTheDocument()
      expect(within(dialog).getByRole('button', { name: 'Завершить занятие' })).toBeDisabled()
    }
    await user.clear(time)
    expect(within(dialog).getByText('Укажите время окончания')).toBeInTheDocument()
    expect(sent.some((s) => s.path === '/api/lessons/5/finish')).toBe(false)
  })

  it('reopens a finished lesson and recomputes attendance from the menu', async () => {
    const end = '2026-10-06T16:31:00Z'
    const { user, sent } = setup(
      makeView({ lesson: makeLesson({ status: 'finished', actual_end_at: end, manual_finished_at: end }) }),
    )
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    expect(screen.queryByRole('menuitem', { name: 'Завершить занятие' })).toBeNull()
    await user.click(await screen.findByRole('menuitem', { name: 'Возобновить' }))
    await waitFor(() => expect(sent).toContainEqual({ method: 'POST', path: '/api/lessons/5/reopen', body: {} }))
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Пересчитать посещаемость' }))
    await waitFor(() => expect(sent).toContainEqual({ method: 'POST', path: '/api/lessons/5/recompute', body: {} }))
  })

  it('deletes the lesson after confirmation and goes back to the list', async () => {
    const { user, sent } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Удалить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Удалить занятие?' })
    await user.click(within(dialog).getByRole('button', { name: 'Удалить' }))
    expect(await screen.findByText('Список занятий')).toBeInTheDocument()
    expect(sent).toContainEqual({ method: 'DELETE', path: '/api/lessons/5', body: undefined })
  })

  it('opens the student sheet with intervals, Zoom names and the change history', async () => {
    const history: StudentHistory = {
      intervals: [
        {
          id: 1,
          join_at: '2026-10-06T15:12:00Z',
          leave_at: '2026-10-06T15:20:00Z',
          duration_seconds: 480,
          zoom_user_name: 'iPhone Алексея',
          zoom_participant_uuid: 'a',
          meeting_uuid: 'm',
          leave_reason: 'renamed',
        },
        {
          id: 2,
          join_at: '2026-10-06T15:20:00Z',
          leave_at: null,
          duration_seconds: null,
          zoom_user_name: 'Сидоров Алексей',
          zoom_participant_uuid: 'b',
          meeting_uuid: 'm',
          leave_reason: '',
        },
        {
          id: 3,
          join_at: '2026-10-06T15:02:00Z',
          leave_at: '2026-10-06T15:05:00Z',
          duration_seconds: 180,
          zoom_user_name: 'Сидоров Алексей',
          zoom_participant_uuid: 'c',
          meeting_uuid: 'm',
          leave_reason: 'left the meeting. Reason : left the meeting; renamed from: Гость',
        },
      ],
      audit: [
        {
          id: 9,
          at: '2026-10-06T15:30:00Z',
          user_name: 'Мария Иванова',
          action: 'attendance.status',
          entity_type: 'attendance',
          entity_id: '3',
          previous_value: 'absent',
          new_value: 'present',
          reason: 'Был с телефона',
        },
      ],
    }
    const view = makeView()
    view.participants = [
      ...view.participants,
      makeParticipant({ id: 104, display_name: 'iPhone Алексея', resolution: 'manual', student_id: 3, manual: true }),
    ]
    const { user, sent } = setup(view, { 'GET /api/lessons/5/students/3/history': history })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    expect(await within(sheet).findByText('сменил имя')).toBeInTheDocument()
    // The device left under a new name: the old one is shown in plain words.
    expect(within(sheet).getByText('сменил имя (было «Гость»), вышел сам')).toBeInTheDocument()
    // Same limits as the server.
    expect(within(sheet).getByRole('textbox', { name: 'Комментарий' })).toHaveAttribute('maxlength', '2000')
    expect(within(sheet).getByLabelText('Причина (необязательно)')).toHaveAttribute('maxlength', '500')
    expect(within(sheet).getByText(`${formatTime('2026-10-06T15:12:00Z')} – ${formatTime('2026-10-06T15:20:00Z')}`)).toBeInTheDocument()
    expect(within(sheet).getByText('8 мин')).toBeInTheDocument()
    // Three joins one after another (new participant_uuid each time) are reconnects, not three devices.
    expect(within(sheet).queryByText(/^устройство \d$/)).toBeNull()
    expect(within(sheet).getAllByText('сейчас в Zoom').length).toBeGreaterThan(0)
    expect(within(sheet).getByText('Изменена отметка о посещении')).toBeInTheDocument()
    expect(within(sheet).getByText('Не был → Был')).toBeInTheDocument()
    expect(within(sheet).getByText('Причина: Был с телефона')).toBeInTheDocument()
    expect(within(sheet).getByText('опоздал на 12 мин')).toBeInTheDocument()
    // Current manual decision is preselected.
    expect(within(sheet).getByRole('radio', { name: 'Был' })).toHaveAttribute('aria-checked', 'true')

    // The card uses the table's words for the mark, not a second vocabulary.
    expect(within(sheet).queryByText('Присутствовал')).toBeNull()
    expect(within(sheet).queryByText(/не сохранена/)).toBeNull()
    await user.click(within(sheet).getByRole('radio', { name: 'Не был' }))
    expect(within(sheet).getByText('Отметка ещё не сохранена — нажмите «Сохранить отметку».')).toBeInTheDocument()
    await user.type(within(sheet).getByLabelText('Причина (необязательно)'), 'Ушёл через 5 минут')
    await user.click(within(sheet).getByRole('button', { name: 'Сохранить отметку' }))
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Отметка сохранена'))
    expect(sent).toContainEqual({
      method: 'PATCH',
      path: '/api/lessons/5/attendance/3',
      body: { status: 'absent', reason: 'Ушёл через 5 минут' },
    })

    await user.click(within(sheet).getByRole('button', { name: 'Отменить сопоставление' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Отменить сопоставление?' })
    await user.click(within(confirm).getByRole('button', { name: 'Отменить сопоставление' }))
    await waitFor(() => expect(sent).toContainEqual({ method: 'DELETE', path: '/api/lessons/5/participants/104/map', body: undefined }))
  })

  it('names the remembered Zoom name that was removed with a cancelled match, without the technical id', async () => {
    const history: StudentHistory = {
      intervals: [],
      audit: [
        {
          id: 11,
          at: '2026-10-06T15:40:00Z',
          user_name: 'Мария Иванова',
          action: 'alias.delete',
          entity_type: 'student',
          entity_id: '3',
          previous_value: '«iPhone Алексея» → Сидоров Алексей (id 3)',
          new_value: '',
          reason: 'Отменено сопоставление участника на занятии',
        },
      ],
    }
    const { user } = setup(makeView(), { 'GET /api/lessons/5/students/3/history': history })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    expect(await within(sheet).findByText('Удалено имя в Zoom')).toBeInTheDocument()
    expect(within(sheet).getByText('«iPhone Алексея» → Сидоров Алексей')).toBeInTheDocument()
    expect(within(sheet).queryByText(/\(id \d+\)/)).toBeNull()
  })

  it('keeps a comment typed in the student card when saving it fails', async () => {
    let release: () => void = () => {}
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    const { user, sent } = setup(
      makeView(),
      {
        'PATCH /api/lessons/5/attendance/4': async (url: URL, init: RequestInit) => {
          sent.push({ method: 'PATCH', path: url.pathname, body: JSON.parse(String(init.body)) })
          await gate
          return errorResponse(500, 'internal', 'Ошибка на сервере. Попробуйте ещё раз.')
        },
      },
      createQueryClient(),
    )
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Орлов Андрей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Орлов Андрей' })
    const box = within(sheet).getByRole('textbox', { name: 'Комментарий' })
    await user.clear(box)
    await user.type(box, 'Предупредил, что опоздает')
    await user.click(within(sheet).getByRole('button', { name: 'Сохранить комментарий' }))
    // While saving, the comment is already shown in the student's row.
    await waitFor(() =>
      // (The table behind the open sheet is hidden from assistive technology.)
      expect(screen.getByRole('button', { name: 'Комментарий: Орлов Андрей', hidden: true })).toBeInTheDocument(),
    )
    act(() => release())
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('Ошибка на сервере. Попробуйте ещё раз.'))
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Добавить комментарий: Орлов Андрей', hidden: true })).toBeInTheDocument(),
    )
    expect(sent).toEqual([{ method: 'PATCH', path: '/api/lessons/5/attendance/4', body: { comment: 'Предупредил, что опоздает' } }])
    // The optimistic comment is gone from the view, but the teacher's text stays for another try.
    await waitFor(() => expect(within(sheet).getByRole('button', { name: 'Сохранить комментарий' })).toBeEnabled())
    expect(box).toHaveValue('Предупредил, что опоздает')
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('undoes a manual match together with the remembered name', async () => {
    const view = makeView()
    view.participants = [
      ...view.participants,
      makeParticipant({
        id: 104,
        display_name: 'iPhone Алексея',
        normalized_name: 'iphone алексея',
        resolution: 'manual',
        student_id: 3,
        student_name: 'Сидоров Алексей',
        manual: true,
      }),
    ]
    const after = withParticipant(view, 104, { resolution: 'unidentified', student_id: null, student_name: null, manual: false })
    const { user, sent, record } = setup(view, {
      'DELETE /api/lessons/5/participants/104/map': (url: URL, init: RequestInit) => record(() => jsonResponse(after))(url, init),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    await user.click(within(sheet).getByRole('button', { name: 'Отменить сопоставление' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Отменить сопоставление?' })
    expect(confirm).toHaveTextContent('Имя «iPhone Алексея» больше не будет автоматически относиться к студенту Сидоров Алексей.')
    await user.click(within(confirm).getByRole('button', { name: 'Отменить сопоставление' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith(
        'Сопоставление отменено: «iPhone Алексея» теперь в блоке «Не идентифицированы»',
      ),
    )
    expect(sent).toEqual([{ method: 'DELETE', path: '/api/lessons/5/participants/104/map', body: undefined }])
  })

  it('keeps the promise "Не идентифицированы" when the name still matches the student on its own', async () => {
    const view = makeView()
    view.participants = [
      ...view.participants.filter((p) => p.id !== 102),
      makeParticipant({
        id: 102,
        display_name: 'Иванов Иван',
        normalized_name: 'иванов иван',
        resolution: 'manual',
        student_id: 1,
        student_name: 'Иванов Иван',
        manual: true,
      }),
    ]
    // Without the decision the name is the student's own name: matched again automatically.
    const matched = withParticipant(view, 102, { resolution: 'matched', manual: false })
    const none = withParticipant(matched, 102, { resolution: 'unidentified', student_id: null, student_name: null, manual: true })
    const { user, sent, record } = setup(view, {
      'DELETE /api/lessons/5/participants/102/map': (url: URL, init: RequestInit) => record(() => jsonResponse(matched))(url, init),
      'POST /api/lessons/5/participants/102/map': (url: URL, init: RequestInit) => record(() => jsonResponse(none))(url, init),
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Иванов Иван' }))
    const sheet = await screen.findByRole('dialog', { name: 'Иванов Иван' })
    await user.click(within(sheet).getByRole('button', { name: 'Отменить сопоставление' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Отменить сопоставление?' })
    await user.click(within(confirm).getByRole('button', { name: 'Отменить сопоставление' }))
    await waitFor(() =>
      expect(toastMock.success).toHaveBeenCalledWith('Сопоставление отменено: «Иванов Иван» теперь в блоке «Не идентифицированы»'),
    )
    expect(sent).toEqual([
      { method: 'DELETE', path: '/api/lessons/5/participants/102/map', body: undefined },
      { method: 'POST', path: '/api/lessons/5/participants/102/map', body: { action: 'none' } },
    ])
  })

  it('explains that an automatically matched remembered name is only dropped for this lesson', async () => {
    const view = makeView()
    view.participants = [
      ...view.participants,
      makeParticipant({ id: 106, display_name: 'Лёша', normalized_name: 'леша', resolution: 'matched', student_id: 3, student_name: 'Сидоров Алексей' }),
    ]
    const { user, sent } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    await user.click(within(sheet).getByRole('button', { name: 'Отменить сопоставление' }))
    const confirm = await screen.findByRole('alertdialog', { name: 'Отменить сопоставление?' })
    expect(confirm).toHaveTextContent('На этом занятии это имя больше не будет узнаваться автоматически.')
    expect(confirm).toHaveTextContent('удалите это имя у студента на странице группы')
    await user.click(within(confirm).getByRole('button', { name: 'Отменить сопоставление' }))
    await waitFor(() =>
      expect(sent).toContainEqual({ method: 'POST', path: '/api/lessons/5/participants/106/map', body: { action: 'none' } }),
    )
    expect(toastMock.success).toHaveBeenCalledWith('Сопоставление отменено: «Лёша»')
  })

  it('undoes an earlier decision on a name that is unidentified again', async () => {
    const view = makeView()
    view.participants = [makeParticipant({ id: 105, display_name: 'Иванов', manual: true })]
    const { user, sent } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Вернуть как было' }))
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Решение по «Иванов» отменено'))
    expect(sent).toContainEqual({ method: 'DELETE', path: '/api/lessons/5/participants/105/map', body: undefined })
  })

  it('does not offer to undo when the teacher has not decided anything', async () => {
    setup()
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByRole('button', { name: 'Это студент…' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Вернуть как было' })).toBeNull()
  })

  it('keeps the open student card in step with live changes', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { level: 1 })
    const es = FakeEventSource.latest()
    act(() => es.open())
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    expect(within(sheet).getByRole('radio', { name: 'Был' })).toHaveAttribute('aria-checked', 'true')
    expect(within(sheet).getByRole('textbox', { name: 'Комментарий' })).toHaveValue('Предупредил об опоздании')

    // Another teacher returns the mark to automatic and clears the comment.
    const next = makeView({ as_of: '2026-10-06T15:45:00Z' })
    next.students = next.students.map((s) =>
      s.student_id === 3 ? { ...s, manual_status: null, final_status: 'absent', comment: '' } : s,
    )
    act(() => es.emit('snapshot', JSON.stringify(next)))
    await waitFor(() =>
      expect(within(sheet).getByRole('radio', { name: /^Автоматически/ })).toHaveAttribute('aria-checked', 'true'),
    )
    expect(within(sheet).getByRole('textbox', { name: 'Комментарий' })).toHaveValue('')
  })

  it('keeps an unsaved choice in the student card when other data changes', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { level: 1 })
    const es = FakeEventSource.latest()
    act(() => es.open())
    await user.click(screen.getByRole('button', { name: 'Сидоров Алексей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Сидоров Алексей' })
    await user.click(within(sheet).getByRole('radio', { name: 'Не был' }))

    const next = makeView({ as_of: '2026-10-06T15:45:00Z' })
    next.students = next.students.map((s) => (s.student_id === 3 ? { ...s, total_seconds: 1200 } : s))
    act(() => es.emit('snapshot', JSON.stringify(next)))
    await new Promise((r) => setTimeout(r, 10))
    expect(within(sheet).getByRole('radio', { name: 'Не был' })).toHaveAttribute('aria-checked', 'true')
    expect(within(sheet).getByRole('button', { name: 'Сохранить отметку' })).toBeEnabled()
    // Unlike the switch in the table, the card saves on the button: it says so.
    expect(within(sheet).getByText('Отметка ещё не сохранена — нажмите «Сохранить отметку».')).toBeInTheDocument()
  })

  it('notices that the open lesson was deleted elsewhere and stops updating it', async () => {
    let deleted = false
    const { client } = setup(makeView(), {
      'GET /api/lessons/5/attendance': () =>
        deleted
          ? errorResponse(404, 'not_found', 'Занятие не найдено. Возможно, его уже удалили.')
          : jsonResponse(makeView()),
    })
    await screen.findByRole('heading', { level: 1, name: 'DevOps. Занятие №4' })
    const es = FakeEventSource.latest()
    act(() => es.open())
    // The server ends the stream of a deleted lesson; the next refresh gets a 404.
    deleted = true
    await act(() => client.invalidateQueries())
    expect(await screen.findByRole('heading', { level: 1, name: 'Занятие удалено' })).toBeInTheDocument()
    expect(screen.getByText('Это занятие только что удалили')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Действия' })).toBeNull()
    // No more streaming or polling of a lesson that does not exist.
    expect(es.closed).toBe(true)
  })

  it('does not offer to reopen a lesson that ended together with the Zoom meeting', async () => {
    const end = '2026-10-06T16:31:00Z'
    const session = {
      id: 1,
      meeting_uuid: 'u1',
      meeting_id: '1234567890',
      topic: 'DevOps',
      lesson_id: 5,
      lesson: null,
      link_mode: 'auto' as const,
      started_at: '2026-10-06T15:00:00Z',
      ended_at: end,
      first_event_at: '2026-10-06T15:00:00Z',
    }
    const { user, sent } = setup(
      makeView({ lesson: makeLesson({ status: 'finished', actual_end_at: end, sessions: [session] }) }),
    )
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    const item = await screen.findByRole('menuitem', { name: /Возобновить/ })
    expect(item).toHaveAttribute('aria-disabled', 'true')
    expect(item).toHaveTextContent('Занятие закончилось вместе с конференцией в Zoom')
    await user.click(item)
    expect(sent.some((x) => x.path === '/api/lessons/5/reopen')).toBe(false)
  })

  it('offers to undo a manual finish when the Zoom meeting has ended too', async () => {
    const end = '2026-10-06T16:31:00Z'
    const lesson = makeLesson({
      status: 'finished',
      actual_end_at: '2026-10-06T16:00:00Z',
      manual_finished_at: '2026-10-06T16:00:00Z',
      sessions: [
        {
          id: 1,
          meeting_uuid: 'u1',
          meeting_id: '1234567890',
          topic: 'DevOps',
          lesson_id: 5,
          lesson: null,
          link_mode: 'auto',
          started_at: '2026-10-06T15:00:00Z',
          ended_at: end,
          first_event_at: '2026-10-06T15:00:00Z',
        },
      ],
    })
    const { user, sent } = setup(makeView({ lesson }))
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    expect(screen.queryByRole('menuitem', { name: 'Возобновить' })).toBeNull()
    await user.click(await screen.findByRole('menuitem', { name: 'Отменить ручное завершение' }))
    await waitFor(() => expect(sent).toContainEqual({ method: 'POST', path: '/api/lessons/5/reopen', body: {} }))
  })

  it('explains that a lesson which has not started cannot be finished yet', async () => {
    const view = makeView({
      lesson: makeLesson({
        status: 'planned',
        actual_start_at: null,
        planned_start_at: '2099-10-06T15:00:00Z',
        planned_end_at: '2099-10-06T16:30:00Z',
      }),
      participants: [],
    })
    const { user } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Завершить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Завершить занятие?' })
    expect(within(dialog).getByText('Занятие ещё не началось — завершить его пока нельзя.')).toBeInTheDocument()
    expect(within(dialog).getByText(/Из Zoom по этому занятию ничего не приходило/)).toBeInTheDocument()
    expect(within(dialog).queryByLabelText('Время окончания')).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Завершить занятие' })).toBeDisabled()
  })

  it('suggests the planned end when the lesson is finished long after it', async () => {
    const plannedEnd = new Date(Date.now() - 3.5 * 3600_000)
    plannedEnd.setSeconds(0, 0)
    const plannedStart = new Date(plannedEnd.getTime() - 90 * 60_000)
    const view = makeView({
      lesson: makeLesson({
        planned_start_at: plannedStart.toISOString(),
        planned_end_at: plannedEnd.toISOString(),
        actual_start_at: plannedStart.toISOString(),
      }),
    })
    const { user, sent } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Действия' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Завершить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Завершить занятие?' })
    // Defaults to now, but warns that everybody still in Zoom would be credited until now.
    expect(within(dialog).getByLabelText('Время окончания')).toHaveValue(formatTime(new Date()))
    expect(within(dialog).getByText(/По расписанию занятие закончилось/)).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: `Поставить ${formatTime(plannedEnd)}` }))
    expect(within(dialog).getByLabelText('Время окончания')).toHaveValue(formatTime(plannedEnd))
    expect(within(dialog).queryByText(/По расписанию занятие закончилось/)).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Завершить занятие' }))
    await waitFor(() =>
      expect(sent).toContainEqual({
        method: 'POST',
        path: '/api/lessons/5/finish',
        body: { at: plannedEnd.toISOString() },
      }),
    )
  })

  it('warns when Zoom never reported the end and finishes the lesson from the banner', async () => {
    const plannedEnd = new Date(Date.now() - 2 * 3600_000)
    plannedEnd.setSeconds(0, 0)
    const plannedStart = new Date(plannedEnd.getTime() - 90 * 60_000)
    const view = makeView({
      lesson: makeLesson({
        planned_start_at: plannedStart.toISOString(),
        planned_end_at: plannedEnd.toISOString(),
        actual_start_at: plannedStart.toISOString(),
        sessions: [zoomSession({ started_at: plannedStart.toISOString(), ended_at: null })],
      }),
    })
    const { user, sent } = setup(view)
    const banner = await screen.findByRole('region', { name: 'Zoom не сообщил об окончании' })
    expect(banner).toHaveTextContent('Zoom не сообщил, что конференция закончилась.')
    expect(banner).toHaveTextContent(`в ${formatTime(plannedEnd)}`)
    await user.click(within(banner).getByRole('button', { name: 'Завершить занятие' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Завершить занятие?' })
    await user.click(within(dialog).getByRole('button', { name: `Поставить ${formatTime(plannedEnd)}` }))
    await user.click(within(dialog).getByRole('button', { name: 'Завершить занятие' }))
    await waitFor(() =>
      expect(sent).toContainEqual({ method: 'POST', path: '/api/lessons/5/finish', body: { at: plannedEnd.toISOString() } }),
    )
  })

  it('does not show the overdue banner while the lesson is still within its planned time', async () => {
    const soon = new Date(Date.now() + 30 * 60_000).toISOString()
    setup(makeView({ lesson: makeLesson({ planned_end_at: soon, sessions: [zoomSession({ ended_at: null })] }) }))
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('region', { name: 'Zoom не сообщил об окончании' })).toBeNull()
  })

  it('lists the linked Zoom conferences and unlinks a wrong one', async () => {
    const view = makeView({ lesson: makeLesson({ sessions: [zoomSession({ topic: 'Совещание завуча' })] }) })
    const { user, sent } = setup(view)
    const list = await screen.findByRole('list', { name: 'Конференции Zoom этого занятия' })
    expect(list).toHaveTextContent('123 456 7890')
    expect(list).toHaveTextContent('«Совещание завуча»')
    expect(list).toHaveTextContent('Привязана автоматически')
    await user.click(within(list).getByRole('button', { name: 'Это другая конференция' }))
    const dialog = await screen.findByRole('alertdialog', { name: 'Отвязать конференцию от занятия?' })
    expect(dialog).toHaveTextContent('Совещание завуча')
    await user.click(within(dialog).getByRole('button', { name: 'Отвязать' }))
    await waitFor(() =>
      expect(sent).toContainEqual({ method: 'POST', path: '/api/zoom/sessions/7/link', body: { lesson_id: null } }),
    )
  })

  it('says when the service finished the lesson itself because Zoom never reported the end', async () => {
    const finishedAt = '2026-10-06T16:40:00Z'
    const lesson = makeLesson({
      status: 'finished',
      actual_end_at: finishedAt,
      manual_finished_at: finishedAt,
      sessions: [zoomSession({ ended_at: null })],
    })
    setup(makeView({ lesson }), {
      'GET /api/lessons/5/audit': [
        {
          id: 3,
          at: '2026-10-06T22:41:00Z',
          user_name: 'Система',
          action: 'lesson.finish',
          entity_type: 'lesson',
          entity_id: '5',
          previous_value: '',
          new_value: 'Завершено автоматически: 06.10.2026 19:40 (UTC+3)',
          reason: 'Zoom не сообщил, что конференция закончилась',
        },
      ],
    })
    const banner = await screen.findByRole('region', { name: 'Занятие завершено' })
    expect(await within(banner).findByText(/сервис завершил занятие сам/)).toBeInTheDocument()
  })

  it('shows a map error inside the dialog, next to the "remember" checkbox', async () => {
    const message =
      'Не удалось запомнить имя «Александр»: оно уже закреплено за студентом «Петров Пётр». Сопоставление не сохранено. Снимите отметку «Запомнить это имя» или выберите другого студента.'
    let attempts = 0
    const { user, sent } = setup(makeView(), {
      'POST /api/lessons/5/participants/100/map': (url: URL, init: RequestInit) => {
        const body = JSON.parse(String(init.body)) as { remember_alias?: boolean }
        sent.push({ method: 'POST', path: url.pathname, body })
        attempts += 1
        return body.remember_alias ? errorResponse(409, 'conflict', message) : jsonResponse({})
      },
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Это студент…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Кто это — «Александр»?' })
    await user.click(within(dialog).getByRole('option', { name: /Орлов Андрей/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Сопоставить' }))
    expect(await within(dialog).findByText(message)).toBeInTheDocument()
    expect(toastMock.success).not.toHaveBeenCalled()

    // Unticking "remember" clears the message; mapping without the alias succeeds.
    await user.click(within(dialog).getByRole('checkbox', { name: 'Запомнить это имя для следующих занятий' }))
    expect(within(dialog).queryByText(message)).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Сопоставить' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(attempts).toBe(2)
    expect(toastMock.success).toHaveBeenCalledWith('Время присутствия перенесено студенту Орлов Андрей')
  })

  it('does not remember a device name like "iPhone" by default', async () => {
    const view = makeView()
    view.participants = [makeParticipant({ id: 100, display_name: 'iPhone', normalized_name: 'iphone' })]
    const { user } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Это студент…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Кто это — «iPhone»?' })
    expect(within(dialog).getByRole('checkbox', { name: 'Запомнить это имя для следующих занятий' })).toHaveAttribute(
      'aria-checked',
      'false',
    )
    expect(within(dialog).getByText(/Похоже на название устройства/)).toBeInTheDocument()
  })

  it('offers group students who are missing from a finished lesson list', async () => {
    const end = '2026-10-06T16:31:00Z'
    const view = makeView({ lesson: makeLesson({ status: 'finished', actual_end_at: end, manual_finished_at: end }) })
    const groupStudent = (id: number, full_name: string, status: 'active' | 'archived' = 'active') => ({
      id,
      group_id: 2,
      external_id: '',
      full_name,
      normalized_name: full_name.toLowerCase(),
      status,
      aliases: [],
    })
    const { user, sent } = setup(view, {
      'GET /api/groups/2': {
        id: 2,
        name: 'DevOps-1',
        description: '',
        status: 'active',
        student_count: 6,
        default_duration_minutes: 90,
        created_at: '2026-09-01T10:00:00Z',
        last_lesson_at: null,
        next_lesson_at: null,
        students: [
          groupStudent(1, 'Иванов Иван'),
          groupStudent(9, 'Новиков Никита'),
          groupStudent(8, 'Старый Студент', 'archived'),
        ],
      },
    })
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Это студент…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Кто это — «Александр»?' })
    expect(await within(dialog).findByText('Нет в списке этого занятия')).toBeInTheDocument()
    expect(within(dialog).queryByRole('option', { name: /Старый Студент/ })).toBeNull()
    // Listed students are not repeated in the extra group.
    expect(within(dialog).getAllByRole('option', { name: /Иванов Иван/ })).toHaveLength(1)
    await user.click(within(dialog).getByRole('option', { name: /Новиков Никита/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Сопоставить' }))
    await waitFor(() =>
      expect(sent).toContainEqual({
        method: 'POST',
        path: '/api/lessons/5/participants/100/map',
        body: { action: 'student', student_id: 9, remember_alias: true },
      }),
    )
  })

  it('shows lateness and early leave only when they are known', async () => {
    const history: StudentHistory = {
      intervals: [],
      audit: [
        {
          id: 11,
          at: '2026-10-06T15:35:00Z',
          user_name: 'Мария Иванова',
          action: 'participant.map',
          entity_type: 'participant',
          entity_id: 'александр',
          previous_value: '«Александр»: автоматически: не идентифицирован',
          new_value: '«Александр»: студент Орлов Андрей (id 4); имя запомнено для следующих занятий',
          reason: '',
        },
        {
          id: 13,
          at: '2026-10-06T15:37:00Z',
          user_name: 'Мария Иванова',
          action: 'participant.unmap',
          entity_type: 'participant',
          entity_id: 'орлов',
          previous_value: '«Орлов»: студент Орлов Андрей (id 4)',
          new_value: '«Орлов»: сопоставляется автоматически; запомненное имя «Орлов» удалено',
          reason: '',
        },
        {
          id: 12,
          at: '2026-10-06T15:36:00Z',
          user_name: 'Мария Иванова',
          action: 'attendance.comment',
          entity_type: 'attendance',
          entity_id: '4',
          previous_value: 'Опоздает',
          new_value: '',
          reason: '',
        },
      ],
    }
    const { user } = setup(makeView(), { 'GET /api/lessons/5/students/4/history': history })
    await screen.findByRole('heading', { level: 1 })
    // Not seen yet: lateness is unknown, not "нет".
    await user.click(screen.getByRole('button', { name: 'Орлов Андрей' }))
    const sheet = await screen.findByRole('dialog', { name: 'Орлов Андрей' })
    expect(within(sheet).getByText('Опоздание').nextElementSibling).toHaveTextContent('—')
    expect(within(sheet).queryByText('Ушёл раньше')).toBeNull()
    // Audit texts without the internal "(id 4)" marker; an emptied comment is named as such.
    expect(
      await within(sheet).findByText('«Александр»: студент Орлов Андрей; имя запомнено для следующих занятий'),
    ).toBeInTheDocument()
    expect(within(sheet).getByText('Комментарий удалён')).toBeInTheDocument()
    // An undone match says which student it was about.
    expect(within(sheet).getByText('Было: «Орлов»: студент Орлов Андрей')).toBeInTheDocument()
    expect(
      within(sheet).getByText('Стало: «Орлов»: сопоставляется автоматически; запомненное имя «Орлов» удалено'),
    ).toBeInTheDocument()
    expect(within(sheet).queryByText(/\(id 4\)/)).toBeNull()
  })

  it('does not claim "left early: no" while the lesson is still going on', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { level: 1 })
    await user.click(screen.getByRole('button', { name: 'Петров Пётр' }))
    const sheet = await screen.findByRole('dialog', { name: 'Петров Пётр' })
    expect(within(sheet).getByText('Последний выход')).toBeInTheDocument()
    expect(within(sheet).queryByText('Ушёл раньше')).toBeNull()
  })

  it('says what an empty quick filter means', async () => {
    const view = makeView()
    view.students = view.students.filter((s) => s.current_status !== 'not_seen')
    const { user } = setup(view)
    await screen.findByRole('heading', { level: 1 })
    const chips = screen.getByRole('radiogroup', { name: 'Показать студентов' })
    await user.click(within(chips).getByRole('radio', { name: 'Не подключались 0' }))
    expect(screen.getByText('Все студенты уже подключались 👍')).toBeInTheDocument()
    expect(screen.queryByText('Никого не нашли')).toBeNull()
  })

  it('explains a missing lesson', async () => {
    setup(makeView(), { 'GET /api/lessons/5/attendance': () => errorResponse(404, 'not_found', 'Занятие не найдено') })
    expect(await screen.findByText('Такого занятия нет')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Ко всем занятиям' })).toHaveAttribute('href', '/')
  })
})

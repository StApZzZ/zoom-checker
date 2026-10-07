import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteObject } from 'react-router'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { STUDENT_RESPONSIBILITY_TEXT } from '@/components/groups/memo'
import type { GroupDetail, LessonSummary, Student } from '@/lib/types'
import GroupPage from '@/pages/groups/GroupPage'
import { FakeEventSource } from '@/test/fakeEventSource'
import { errorResponse, jsonResponse, mockFetch, renderRoutes } from '@/test/utils'

const routes: RouteObject[] = [
  { path: '/groups/:id', element: <GroupPage /> },
  { path: '/groups/:id/report', element: <h1>Отчёт</h1> },
]

function student(id: number, full_name: string, over: Partial<Student> = {}): Student {
  return {
    id,
    group_id: 3,
    external_id: '',
    full_name,
    normalized_name: full_name.toLowerCase().replaceAll('ё', 'е'),
    status: 'active',
    aliases: [],
    ...over,
  }
}

function makeGroup(): GroupDetail {
  return {
    id: 3,
    name: 'DevOps-1',
    description: 'Осенний поток',
    status: 'active',
    student_count: 2,
    default_duration_minutes: 90,
    created_at: '2026-09-01T10:00:00Z',
    last_lesson_at: null,
    next_lesson_at: null,
    students: [
      student(11, 'Петров Пётр', { external_id: '102' }),
      student(10, 'Иванов Иван', { external_id: '101', aliases: [{ id: 7, alias: 'Ваня' }] }),
      student(12, 'Сидоров Алексей', { status: 'archived' }),
    ],
  }
}

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

/** A tiny fake server that keeps the group in memory so refetches show changes. */
function setup(path = '/groups/3', extra: Record<string, unknown> = {}) {
  const group = makeGroup()
  const find = (id: number) => group.students.find((s) => s.id === id)!
  const fetchMock = mockFetch({
    'GET /api/groups/3': () => jsonResponse(group),
    'POST /api/students/11/aliases': (_url: URL, init: RequestInit) => {
      const { alias } = JSON.parse(String(init.body)) as { alias: string }
      const created = { id: 8, alias }
      find(11).aliases.push(created)
      return jsonResponse(created, 201)
    },
    'DELETE /api/students/10/aliases/7': () => {
      find(10).aliases = []
      return new Response(null, { status: 204 })
    },
    'PATCH /api/students/11': (_url: URL, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as Partial<Student>
      if (body.full_name === 'Иванов Иван')
        return errorResponse(409, 'conflict', 'В группе уже есть студент с таким именем или дополнительным именем.')
      Object.assign(find(11), body)
      return jsonResponse(find(11))
    },
    ...extra,
  })
  return { group, fetchMock, ...renderRoutes(routes, path) }
}

describe('GroupPage', () => {
  it('shows the header and the roster sorted by name with IDs, aliases and status', async () => {
    setup()
    expect(await screen.findByRole('heading', { level: 1, name: 'DevOps-1' })).toBeInTheDocument()
    expect(screen.getByText('Осенний поток')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Сводка посещаемости/ })).toHaveAttribute('href', '/groups/3/report')
    expect(screen.getByText(/В группе 2 студента/)).toBeInTheDocument()
    expect(screen.getByText(/в архиве 1/)).toBeInTheDocument()

    const table = screen.getByRole('table', { name: 'Студенты группы' })
    const rows = within(table).getAllByRole('row').slice(1)
    expect(rows.map((r) => within(r).getAllByRole('cell')[1].textContent)).toEqual(['Иванов Иван', 'Петров Пётр'])
    expect(within(rows[0]).getByText('101')).toBeInTheDocument()
    expect(within(rows[0]).getByText('Ваня')).toBeInTheDocument()
    expect(within(rows[0]).getByText('В группе')).toBeInTheDocument()
    expect(screen.queryByText('Сидоров Алексей')).toBeNull()

    const user = userEvent.setup()
    await user.click(screen.getByRole('radio', { name: 'В архиве (1)' }))
    expect(screen.getByText('Сидоров Алексей')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Вернуть в группу: Сидоров Алексей' })).toBeInTheDocument()
  })

  it('searches by name, alias and ID', async () => {
    setup()
    const user = userEvent.setup()
    const search = await screen.findByRole('searchbox', { name: 'Поиск студента' })
    await user.type(search, 'ваня')
    const table = screen.getByRole('table', { name: 'Студенты группы' })
    expect(within(table).getAllByRole('row')).toHaveLength(2)
    expect(within(table).getByText('Иванов Иван')).toBeInTheDocument()
    expect(screen.getByText(/найдено: 1/)).toBeInTheDocument()
    await user.clear(search)
    await user.type(search, 'нет такого')
    expect(screen.getByText('Никого не нашли')).toBeInTheDocument()
  })

  it('adds an extra Zoom name to a student', async () => {
    const { fetchMock } = setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Добавить имя в Zoom: Петров Пётр' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Как студент может называться в Zoom')).toBeInTheDocument()
    expect(within(dialog).getByText(/совпадает с записанным полностью/)).toBeInTheDocument()

    // The main name itself is not an extra name.
    await user.type(within(dialog).getByLabelText('Имя в Zoom'), '  ПЕТРОВ   пётр ')
    await user.click(within(dialog).getByRole('button', { name: 'Добавить имя' }))
    expect(within(dialog).getByText(/совпадает с основным именем/)).toBeInTheDocument()

    await user.clear(within(dialog).getByLabelText('Имя в Zoom'))
    await user.type(within(dialog).getByLabelText('Имя в Zoom'), 'Petrov Petr')
    await user.click(within(dialog).getByRole('button', { name: 'Добавить имя' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'POST')!
    expect(JSON.parse(String(call[1]?.body))).toEqual({ alias: 'Petrov Petr' })
    expect(await screen.findByText('Petrov Petr')).toBeInTheDocument()
  })

  it('removes an extra Zoom name after a confirmation', async () => {
    const { fetchMock } = setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Удалить имя «Ваня»' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText('Удалить имя «Ваня»?')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Удалить имя' }))
    await waitFor(() => expect(screen.queryByText('Ваня')).toBeNull())
    expect(fetchMock.mock.calls.some(([u, init]) => init?.method === 'DELETE' && String(u) === '/api/students/10/aliases/7')).toBe(true)
  })

  it('explains a duplicate name when renaming a student (409)', async () => {
    setup()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Изменить: Петров Пётр' }))
    const dialog = await screen.findByRole('dialog')
    const name = within(dialog).getByLabelText('Фамилия и имя')
    expect(name).toHaveValue('Петров Пётр')
    await user.clear(name)
    await user.type(name, 'Иванов Иван')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))
    expect(await within(dialog).findByText('Такое имя уже есть в группе')).toBeInTheDocument()
    expect(within(dialog).getByText('В группе уже есть студент с таким именем или дополнительным именем.')).toBeInTheDocument()
    expect(within(dialog).getByText(/например «Иванов Иван А\.»/)).toBeInTheDocument()

    await user.clear(name)
    await user.type(name, 'Петров Пётр С.')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(await screen.findByText('Петров Пётр С.')).toBeInTheDocument()
  })

  it('archives a student only after a confirmation', async () => {
    let patched: unknown = null
    setup('/groups/3', {
      'PATCH /api/students/10': (_url: URL, init: RequestInit) => {
        patched = JSON.parse(String(init.body))
        return jsonResponse({})
      },
    })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'В архив: Иванов Иван' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/посещаемость за прошлые занятия сохранится/)).toBeInTheDocument()
    expect(patched).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Перенести в архив' }))
    await waitFor(() => expect(patched).toEqual({ status: 'archived' }))
  })

  it('shows the memo with the TZ §33 text and the exact names', async () => {
    setup('/groups/3?tab=memo')
    expect(await screen.findByText(STUDENT_RESPONSIBILITY_TEXT)).toBeInTheDocument()
    expect(screen.getByText('Укажите в Zoom имя ровно так:')).toBeInTheDocument()
    const names = screen.getAllByRole('listitem').map((li) => li.textContent)
    expect(names).toContain('Иванов Иван')
    expect(names).toContain('Петров Пётр')
    expect(names).not.toContain('Сидоров Алексей')
    expect(screen.getByRole('button', { name: 'Скопировать памятку' })).toBeInTheDocument()
  })

  const lesson = (id: number, start: string, status: LessonSummary['status'], online = 1): LessonSummary => ({
      id,
      group: { id: 3, name: 'DevOps-1' },
      title: `Занятие ${id}`,
      zoom_meeting_id: '1234567890',
      zoom_meeting_id_formatted: '123 456 7890',
      zoom_join_url: '',
      planned_start_at: start,
      planned_end_at: start,
      actual_start_at: null,
      actual_end_at: null,
      status,
      manual_finished_at: null,
      counts: { in_group: 2, online, seen: 2, not_seen: 0, unidentified: 0, present: 2, absent: 0 },
    })

  it('lists the group lessons newest first', async () => {
    const { fetchMock } = setup('/groups/3?tab=lessons', {
      'GET /api/lessons': [lesson(1, '2026-09-01T15:00:00Z', 'finished'), lesson(2, '2026-10-06T15:00:00Z', 'active')],
    })
    const list = await screen.findByRole('list', { name: 'Занятия группы' })
    const links = within(list).getAllByRole('link')
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/lessons/2', '/lessons/1'])
    expect(within(links[0]).getByText(/Сейчас в Zoom/)).toBeInTheDocument()
    expect(within(links[1]).getByText(/Были/)).toBeInTheDocument()
    const lessonsCall = fetchMock.mock.calls.find(([u]) => String(u).startsWith('/api/lessons'))!
    expect(String(lessonsCall[0])).toBe('/api/lessons?group_id=3')
  })

  it('updates "Сейчас в Zoom" of a running lesson live', async () => {
    FakeEventSource.reset()
    vi.stubGlobal('EventSource', FakeEventSource)
    let online = 1
    setup('/groups/3?tab=lessons', {
      'GET /api/lessons': () => jsonResponse([lesson(2, '2026-10-06T15:00:00Z', 'active', online)]),
    })
    const list = await screen.findByRole('list', { name: 'Занятия группы' })
    expect(within(list).getByText(/Сейчас в Zoom/).textContent).toBe('Сейчас в Zoom: 1 из 2')
    const stream = FakeEventSource.instances.find((es) => es.url === '/api/stream')
    expect(stream).toBeDefined()
    stream!.open()
    online = 2
    stream!.emit('lessons', '{}')
    await waitFor(() => expect(within(list).getByText(/Сейчас в Zoom/).textContent).toBe('Сейчас в Zoom: 2 из 2'))
  })

  it('keeps a numbered title while the group cannot be loaded', async () => {
    mockFetch({ 'GET /api/groups/4': () => errorResponse(500, 'internal', 'Ошибка на сервере.') })
    renderRoutes(routes, '/groups/4')
    expect(await screen.findByRole('heading', { level: 1, name: 'Группа №4' })).toBeInTheDocument()
    expect(await screen.findByText('Ошибка на сервере.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Повторить/ })).toBeInTheDocument()
  })

  it('explains a group that does not exist instead of offering a retry', async () => {
    mockFetch({ 'GET /api/groups/4': () => errorResponse(404, 'not_found', 'Группа не найдена') })
    renderRoutes(routes, '/groups/4')
    expect(await screen.findByText('Такой группы нет')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'Группа №4' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'К списку групп' })).toHaveAttribute('href', '/groups')
    expect(screen.queryByRole('button', { name: /Повторить/ })).toBeNull()
  })
})

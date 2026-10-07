import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteObject } from 'react-router'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import type { GroupSummary, ImportPreview } from '@/lib/types'
import GroupsPage from '@/pages/groups/GroupsPage'
import { errorResponse, jsonResponse, mockFetch, renderRoutes } from '@/test/utils'

const routes: RouteObject[] = [
  { path: '/groups', element: <GroupsPage /> },
  { path: '/groups/:id', element: <h1>Страница группы</h1> },
]

function makeGroup(over: Partial<GroupSummary> = {}): GroupSummary {
  return {
    id: 1,
    name: 'DevOps-2026-01',
    description: 'Linux для начинающих',
    status: 'active',
    student_count: 25,
    default_duration_minutes: 90,
    created_at: '2026-09-01T10:00:00Z',
    last_lesson_at: null,
    next_lesson_at: null,
    ...over,
  }
}

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('GroupsPage', () => {
  it('explains what a group is when there are none', async () => {
    mockFetch({ 'GET /api/groups': [] })
    renderRoutes(routes, '/groups')
    expect(await screen.findByText('Групп пока нет')).toBeInTheDocument()
    expect(screen.getByText(/Группа — это список студентов одного курса/)).toBeInTheDocument()
    // Header button + empty state button.
    expect(screen.getAllByRole('button', { name: 'Новая группа' })).toHaveLength(2)
  })

  it('lists groups with student count and next lesson, and filters the archive', async () => {
    const calls: string[] = []
    mockFetch({
      'GET /api/groups': (url: URL) => {
        const status = url.searchParams.get('status') ?? ''
        calls.push(status)
        return jsonResponse(
          status === 'archived'
            ? []
            : [
                makeGroup({ next_lesson_at: '2026-10-08T15:00:00Z' }),
                makeGroup({ id: 2, name: 'Python-1', description: '', student_count: 1, last_lesson_at: '2026-09-30T15:00:00Z' }),
              ],
        )
      },
    })
    const user = userEvent.setup()
    const { router } = renderRoutes(routes, '/groups')

    const list = await screen.findByRole('list', { name: 'Список групп' })
    const cards = within(list).getAllByRole('link')
    expect(cards).toHaveLength(2)
    expect(cards[0]).toHaveAttribute('href', '/groups/1')
    expect(within(cards[0]).getByText('25 студентов')).toBeInTheDocument()
    expect(within(cards[0]).getByText('Linux для начинающих')).toBeInTheDocument()
    expect(within(cards[0]).getByText(/Следующее занятие/)).toBeInTheDocument()
    expect(within(cards[1]).getByText('1 студент')).toBeInTheDocument()
    expect(within(cards[1]).getByText(/Последнее занятие/)).toBeInTheDocument()
    expect(calls).toEqual(['active'])

    await user.click(screen.getByRole('radio', { name: 'Архив' }))
    expect(await screen.findByText('В архиве пусто')).toBeInTheDocument()
    expect(calls).toContain('archived')
    expect(router.state.location.search).toBe('?status=archived')
  })

  it('creates a group with a pasted list in one step', async () => {
    const created = makeGroup({ id: 9, name: 'Linux-2', description: '', student_count: 0 })
    const clean: ImportPreview = {
      mode: 'add',
      rows: [
        { line: 1, external_id: '', full_name: 'Иванов Иван', normalized_name: 'иванов иван', status: 'new', message: '', student_id: null },
        { line: 2, external_id: '', full_name: 'Петров Пётр', normalized_name: 'петров петр', status: 'new', message: '', student_id: null },
      ],
      counts: { new: 2, existing: 0, error: 0, duplicate: 0, archive: 0 },
      detected: { format: 'text', encoding: 'utf-8', delimiter: '', name_column: '' },
      name_format: 'as_is',
      has_patronymics: false,
    }
    let createBody: unknown = null
    let commitBody: unknown = null
    mockFetch({
      'GET /api/groups': [],
      'POST /api/groups': (_url: URL, init: RequestInit) => {
        createBody = JSON.parse(String(init.body))
        return jsonResponse(created, 201)
      },
      'POST /api/groups/9/import/preview': (_url: URL, init: RequestInit) => {
        expect(JSON.parse(String(init.body))).toEqual({ text: 'Иванов Иван\nПетров Пётр' })
        return jsonResponse(clean)
      },
      'POST /api/groups/9/import/commit': (_url: URL, init: RequestInit) => {
        commitBody = JSON.parse(String(init.body))
        return jsonResponse({ created: 2, existing: 0, archived: 0, updated: 0 })
      },
    })
    const user = userEvent.setup()
    const { router } = renderRoutes(routes, '/groups')
    await screen.findByText('Групп пока нет')
    await user.click(screen.getAllByRole('button', { name: 'Новая группа' })[0])

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Название группы'), 'Linux-2')
    const duration = within(dialog).getByLabelText('Обычная длительность занятия, минут')
    await user.clear(duration)
    await user.type(duration, '120')
    await user.click(within(dialog).getByLabelText(/Вставьте список студентов/))
    await user.paste('Иванов Иван\nПетров Пётр')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу и добавить студентов' }))

    expect(await screen.findByRole('heading', { name: 'Страница группы' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/groups/9')
    expect(createBody).toEqual({ name: 'Linux-2', description: '', default_duration_minutes: 120 })
    expect(commitBody).toEqual({
      mode: 'add',
      name_format: 'as_is',
      rows: [
        { external_id: '', full_name: 'Иванов Иван', line: 1 },
        { external_id: '', full_name: 'Петров Пётр', line: 2 },
      ],
    })
    expect(toastMock.success).toHaveBeenCalledWith('Группа «Linux-2» создана', { description: 'Добавлено: 2' })
  })

  it('keeps the dialog open with the preview when the pasted list has duplicates', async () => {
    const created = makeGroup({ id: 9, name: 'Linux-2', student_count: 0 })
    const dupMessage = 'Невозможно однозначно идентифицировать студентов по Zoom display name.'
    const blocked: ImportPreview = {
      mode: 'add',
      rows: [
        { line: 1, external_id: '', full_name: 'Иванов Иван', normalized_name: 'иванов иван', status: 'duplicate', message: dupMessage, student_id: null },
        { line: 2, external_id: '', full_name: 'Иванов Иван', normalized_name: 'иванов иван', status: 'duplicate', message: dupMessage, student_id: null },
      ],
      counts: { new: 0, existing: 0, error: 0, duplicate: 2, archive: 0 },
      detected: { format: 'text', encoding: 'utf-8', delimiter: '', name_column: '' },
      name_format: 'as_is',
      has_patronymics: false,
    }
    const fetchMock = mockFetch({
      'GET /api/groups': [],
      'POST /api/groups': () => jsonResponse(created, 201),
      'POST /api/groups/9/import/preview': blocked,
    })
    const user = userEvent.setup()
    const { router } = renderRoutes(routes, '/groups')
    await screen.findByText('Групп пока нет')
    await user.click(screen.getAllByRole('button', { name: 'Новая группа' })[0])
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Название группы'), 'Linux-2')
    await user.click(within(dialog).getByLabelText(/Вставьте список студентов/))
    await user.paste('Иванов Иван\nИванов Иван')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу и добавить студентов' }))

    expect(await within(dialog).findByText('Группа «Linux-2» создана')).toBeInTheDocument()
    expect(within(dialog).getByText('Исправьте список и проверьте снова')).toBeInTheDocument()
    // The TZ §8 error in the summary and next to both rows.
    expect(within(within(dialog).getByTestId('import-blocked')).getByText(dupMessage)).toBeInTheDocument()
    expect(within(dialog).getAllByText(dupMessage)).toHaveLength(3)
    expect(within(dialog).getByRole('button', { name: 'Сохранить список' })).toBeDisabled()
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/import/commit'))).toBe(false)

    // Back to the text box: the group is not created twice.
    await user.click(within(dialog).getByRole('button', { name: 'Исправить список' }))
    expect(within(dialog).queryByLabelText('Название группы')).toBeNull()
    expect(within(dialog).getByLabelText(/Вставьте список студентов/)).toHaveValue('Иванов Иван\nИванов Иван')

    // Skipping still opens the new group.
    await user.click(within(dialog).getByRole('button', { name: 'Пропустить' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/groups/9'))
    expect(toastMock.success).toHaveBeenCalledWith('Группа «Linux-2» создана', {
      description: 'Список студентов не сохранён — его можно загрузить на странице группы.',
    })
  })

  it('removes a repeated line of the pasted list and shows the fixed list before saving', async () => {
    const created = makeGroup({ id: 9, name: 'Linux-2', student_count: 0 })
    const dupMessage = 'Невозможно однозначно идентифицировать студентов по Zoom display name.'
    const dupRow = (line: number, full_name: string) => ({
      line,
      external_id: '',
      full_name,
      normalized_name: 'иванов иван',
      status: 'duplicate' as const,
      message: dupMessage,
      student_id: null,
    })
    const blocked: ImportPreview = {
      mode: 'add',
      rows: [dupRow(1, 'Иванов Иван'), dupRow(2, 'иванов  иван')],
      counts: { new: 0, existing: 0, error: 0, duplicate: 2, archive: 0 },
      detected: { format: 'text', encoding: 'utf-8', delimiter: '', name_column: '' },
      name_format: 'as_is',
      has_patronymics: false,
    }
    const fixed: ImportPreview = {
      ...blocked,
      rows: [{ ...dupRow(1, 'Иванов Иван'), status: 'new', message: '' }],
      counts: { new: 1, existing: 0, error: 0, duplicate: 0, archive: 0 },
    }
    const sent: string[] = []
    const fetchMock = mockFetch({
      'GET /api/groups': [],
      'POST /api/groups': () => jsonResponse(created, 201),
      'POST /api/groups/9/import/preview': (_url: URL, init: RequestInit) => {
        sent.push((JSON.parse(String(init.body)) as { text: string }).text)
        return jsonResponse(sent.length === 1 ? blocked : fixed)
      },
    })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups')
    await screen.findByText('Групп пока нет')
    await user.click(screen.getAllByRole('button', { name: 'Новая группа' })[0])
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Название группы'), 'Linux-2')
    await user.click(within(dialog).getByLabelText(/Вставьте список студентов/))
    await user.paste('Иванов Иван\nиванов  иван')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу и добавить студентов' }))

    const banner = await within(dialog).findByTestId('import-blocked')
    expect(within(banner).getByText('Строки 1 и 2 — одно и то же имя «Иванов Иван»')).toBeInTheDocument()
    await user.click(within(banner).getByRole('button', { name: /^Убрать повтор/ }))
    await waitFor(() => expect(sent).toEqual(['Иванов Иван\nиванов  иван', 'Иванов Иван']))
    // Re-checked, not saved behind the teacher's back.
    expect(await within(dialog).findByRole('button', { name: 'Сохранить список' })).toBeEnabled()
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/import/commit'))).toBe(false)
    await user.click(within(dialog).getByRole('button', { name: 'Исправить список' }))
    expect(within(dialog).getByLabelText(/Вставьте список студентов/)).toHaveValue('Иванов Иван')
  })

  it('checks the server limits next to the fields before sending', async () => {
    const fetchMock = mockFetch({ 'GET /api/groups': [] })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups')
    await screen.findByText('Групп пока нет')
    await user.click(screen.getAllByRole('button', { name: 'Новая группа' })[0])
    const dialog = await screen.findByRole('dialog')
    const name = within(dialog).getByLabelText('Название группы')
    await user.click(name)
    await user.paste('Я'.repeat(101))
    const duration = within(dialog).getByLabelText('Обычная длительность занятия, минут')
    await user.clear(duration)
    await user.type(duration, '10')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу' }))
    expect(within(dialog).getByText('Название слишком длинное (больше 100 символов).')).toBeInTheDocument()
    expect(within(dialog).getByText('Длительность — целое число минут от 15 до 600.')).toBeInTheDocument()
    expect(duration).toHaveAccessibleDescription(/от 15 до 600/)
    expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false)
  })

  it('shows the server message when the name is taken', async () => {
    mockFetch({
      'GET /api/groups': [],
      'POST /api/groups': () => errorResponse(409, 'conflict', 'Группа с таким названием уже есть.'),
    })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups')
    await screen.findByText('Групп пока нет')
    await user.click(screen.getAllByRole('button', { name: 'Новая группа' })[0])
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу' }))
    expect(within(dialog).getByText('Введите название группы.')).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('Название группы'), 'DevOps-2026-01')
    await user.click(within(dialog).getByRole('button', { name: 'Создать группу' }))
    expect(await within(dialog).findByText('Группа с таким названием уже есть.')).toBeInTheDocument()
  })
})

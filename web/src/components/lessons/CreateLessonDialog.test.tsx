import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useParams } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { CreateLessonDialog } from '@/components/lessons/CreateLessonDialog'
import type { CreateLessonInput, GroupSummary } from '@/lib/types'
import { makeGroup, makeLesson, makeSummary } from '@/pages/lessons/testing'
import { errorResponse, jsonResponse, mockFetch, renderRoutes } from '@/test/utils'

// Tuesday, 6 October 2026, 17:05 local time → default start 17:30.
const NOW = new Date(2026, 9, 6, 17, 5)
const local = (d: number, h: number, mi: number, month = 9) => new Date(2026, month, d, h, mi).toISOString()

const INVITATION = `Мария Иванова приглашает вас на запланированную конференцию: Zoom.
Тема: DevOps
Подключиться к конференции Zoom
https://us02web.zoom.us/j/1234567890?pwd=abc

Идентификатор конференции: 123 456 7890
Код доступа: 123456`

function LessonStub() {
  const { id } = useParams()
  return <p>Открыто занятие {id}</p>
}

interface SetupOptions {
  groups?: GroupSummary[]
  groupId?: number
}

function setup({ groups = [makeGroup({ default_duration_minutes: 120 })], groupId }: SetupOptions = {}) {
  const created: CreateLessonInput[] = []
  const onOpenChange = vi.fn()
  const fetchMock = mockFetch({
    'GET /api/groups': groups,
    'GET /api/lessons': (url: URL) =>
      jsonResponse(url.searchParams.get('group_id') === '2' ? [1, 2, 3, 4].map((id) => makeSummary({ id })) : []),
    'GET /api/zoom/parse': (url: URL) => {
      const input = url.searchParams.get('input') ?? ''
      return /1234567890|123 456 7890/.test(input)
        ? jsonResponse({ meeting_id: '1234567890', formatted: '123 456 7890' })
        : errorResponse(422, 'validation', 'В тексте нет номера конференции Zoom. Скопируйте ссылку из приглашения целиком.')
    },
    'POST /api/lessons': (_url: URL, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as CreateLessonInput
      created.push(body)
      const count = body.repeat_weekly ?? 1
      return jsonResponse(Array.from({ length: count }, (_, i) => makeLesson({ id: 77 + i, title: body.title })))
    },
  })
  const user = userEvent.setup()
  const utils = renderRoutes(
    [
      { path: '/', element: <CreateLessonDialog open onOpenChange={onOpenChange} groupId={groupId} /> },
      { path: '/lessons/:id', element: <LessonStub /> },
      { path: '/groups', element: <p>Страница групп</p> },
    ],
    '/',
  )
  return { ...utils, user, created, fetchMock, onOpenChange }
}

const parseCalls = (fetchMock: ReturnType<typeof mockFetch>) =>
  fetchMock.mock.calls
    .map(([input]) => new URL(String(input), 'http://localhost'))
    .filter((u) => u.pathname === '/api/zoom/parse')
    .map((u) => u.searchParams.get('input'))

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  toastMock.success.mockReset()
  toastMock.error.mockReset()
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('CreateLessonDialog', () => {
  it('prefills the group, the next lesson number, today, the next half hour and the group duration', async () => {
    setup()
    const dialog = await screen.findByRole('dialog', { name: 'Новое занятие' })
    // The only group is chosen automatically.
    await waitFor(() => expect(within(dialog).getByRole('combobox', { name: 'Группа' })).toHaveTextContent('DevOps-1'))
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №5'))
    expect(screen.getByLabelText('Дата')).toHaveValue('2026-10-06')
    expect(screen.getByLabelText('Время начала')).toHaveValue('17:30')
    expect(screen.getByLabelText('Длительность, минут')).toHaveValue(120)
    expect(screen.getByRole('button', { name: '120 мин' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByText('Окончание в 19:30')).toBeInTheDocument()
  })

  it('updates the suggested title when another group is chosen, until the teacher edits it', async () => {
    const { user } = setup({
      groups: [makeGroup({ id: 2, name: 'DevOps-1' }), makeGroup({ id: 3, name: 'Linux-2', default_duration_minutes: 45 })],
    })
    const group = await screen.findByRole('combobox', { name: 'Группа' })
    expect(screen.getByLabelText('Название')).toHaveValue('')
    await user.click(group)
    await user.click(await screen.findByRole('option', { name: /Linux-2/ }))
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №1'))
    expect(screen.getByLabelText('Длительность, минут')).toHaveValue(45)

    await user.click(group)
    await user.click(await screen.findByRole('option', { name: /DevOps-1/ }))
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №5'))
    expect(screen.getByLabelText('Длительность, минут')).toHaveValue(90)

    await user.clear(screen.getByLabelText('Название'))
    await user.type(screen.getByLabelText('Название'), 'Вводное')
    await user.click(screen.getByRole('button', { name: '60 мин' }))
    await user.click(group)
    await user.click(await screen.findByRole('option', { name: /Linux-2/ }))
    expect(screen.getByLabelText('Название')).toHaveValue('Вводное')
    expect(screen.getByLabelText('Длительность, минут')).toHaveValue(60)
  })

  /** Holds GET /api/lessons (the lesson count behind «Занятие №N») until `release()`. */
  function delayLessonCount(fetchMock: ReturnType<typeof mockFetch>) {
    let release: () => void = () => {}
    const loaded = new Promise<void>((resolve) => (release = resolve))
    const base = fetchMock.getMockImplementation()!
    fetchMock.mockImplementation(async (input, init) => {
      if (new URL(String(input), 'http://localhost').pathname === '/api/lessons') await loaded
      return base(input, init)
    })
    return () => release()
  }

  it('does not slip the suggested title under the cursor of a teacher who is about to type', async () => {
    const { fetchMock, user } = setup()
    const release = delayLessonCount(fetchMock)
    const title = await screen.findByLabelText('Название')
    await user.click(title)
    // The suggestion arrives while the cursor is already in the empty field…
    release()
    await waitFor(() => expect(title).toHaveAttribute('placeholder', 'Например, Занятие №1'))
    // …so what the teacher types is the whole title, not «Занятие №5Вводное».
    await user.type(title, 'Вводное')
    expect(title).toHaveValue('Вводное')
    await user.click(screen.getByLabelText('Ссылка Zoom или номер конференции'))
    expect(title).toHaveValue('Вводное')
  })

  it('still suggests the title for a field the teacher only passed through', async () => {
    const { fetchMock, user } = setup()
    const release = delayLessonCount(fetchMock)
    const title = await screen.findByLabelText('Название')
    await user.click(title)
    release()
    await waitFor(() => expect(title).toHaveAttribute('placeholder', 'Например, Занятие №1'))
    // Still empty while the cursor is in it…
    expect(title).toHaveValue('')
    await user.click(screen.getByLabelText('Ссылка Zoom или номер конференции'))
    // …and filled in once the teacher moved on without typing.
    await waitFor(() => expect(title).toHaveValue('Занятие №5'))
  })

  it('says the start time is in the teacher\'s zone when the server has another one', async () => {
    const { fetchMock } = setup()
    const base = fetchMock.getMockImplementation()!
    fetchMock.mockImplementation(async (input, init) =>
      new URL(String(input), 'http://localhost').pathname === '/api/auth/config'
        ? jsonResponse({ local_enabled: true, ldap_enabled: false, oidc_enabled: false, oidc_label: '', version: '1', timezone: 'Pacific/Kiritimati' })
        : base(input, init),
    )
    expect(await screen.findByText(/Время указывается по вашему часовому поясу \(UTC.*\), а не по времени сервиса \(UTC\+14\)\./)).toBeInTheDocument()
  })

  it('recognizes a whole pasted invitation after a short pause', async () => {
    const { user, fetchMock } = setup()
    const zoom = await screen.findByLabelText('Ссылка Zoom или номер конференции')
    expect(screen.getByText(/вставьте сюда целиком/)).toBeInTheDocument()
    await user.click(zoom)
    await user.paste(INVITATION)
    expect(screen.getByText('Проверяем ссылку…')).toBeInTheDocument()
    expect(await screen.findByText('Конференция 123 456 7890')).toBeInTheDocument()
    expect(parseCalls(fetchMock)).toEqual([INVITATION.trim()])
  })

  it('checks only the final text while the teacher is typing', async () => {
    const { user, fetchMock } = setup()
    const zoom = await screen.findByLabelText('Ссылка Zoom или номер конференции')
    await user.type(zoom, '12345')
    expect(
      await screen.findByText('В тексте нет номера конференции Zoom. Скопируйте ссылку из приглашения целиком.'),
    ).toBeInTheDocument()
    expect(zoom).toHaveAttribute('aria-invalid', 'true')
    expect(parseCalls(fetchMock)).toEqual(['12345'])
  })

  it('shows Russian validation messages and does not send an incomplete form', async () => {
    const { user, created } = setup()
    const title = await screen.findByLabelText('Название')
    await waitFor(() => expect(title).toHaveValue('Занятие №5'))
    await user.clear(title)
    await user.clear(screen.getByLabelText('Длительность, минут'))
    await user.click(screen.getByRole('button', { name: 'Создать занятие' }))
    expect(screen.getByText('Введите название занятия')).toBeInTheDocument()
    expect(screen.getByText('Вставьте ссылку на Zoom или номер конференции')).toBeInTheDocument()
    expect(screen.getByText('Укажите длительность от 5 до 720 минут')).toBeInTheDocument()
    expect(title).toHaveFocus()
    expect(created).toHaveLength(0)
  })

  it('blocks saving when the Zoom text has no meeting number', async () => {
    const { user, created } = setup()
    await user.type(await screen.findByLabelText('Ссылка Zoom или номер конференции'), 'привет')
    await screen.findByText(/В тексте нет номера конференции/)
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №5'))
    await user.click(screen.getByRole('button', { name: 'Создать занятие' }))
    expect(screen.getAllByText(/В тексте нет номера конференции/)).toHaveLength(1)
    expect(created).toHaveLength(0)
  })

  it('previews a weekly series', async () => {
    const { user } = setup()
    await user.click(await screen.findByRole('checkbox', { name: 'Повторять каждую неделю' }))
    expect(screen.getByText('Будет создано 12 занятий: 06.10 – 22.12')).toBeInTheDocument()
    const weeks = screen.getByLabelText('Сколько недель')
    await user.clear(weeks)
    await user.type(weeks, '4')
    expect(screen.getByText('Будет создано 4 занятия: 06.10 – 27.10')).toBeInTheDocument()
    await user.clear(weeks)
    await user.type(weeks, '1')
    await user.click(screen.getByRole('button', { name: 'Создать занятия' }))
    expect(screen.getByText('Укажите от 2 до 52 недель')).toBeInTheDocument()
  })

  it('tells how the titles of a weekly series will be numbered', async () => {
    const { user } = setup()
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №5'))
    await user.click(screen.getByRole('checkbox', { name: 'Повторять каждую неделю' }))
    expect(screen.getByText('Названия по порядку: «Занятие №5» … «Занятие №16»')).toBeInTheDocument()
    await user.clear(screen.getByLabelText('Название'))
    await user.type(screen.getByLabelText('Название'), 'Вводное')
    expect(screen.queryByText(/Названия по порядку/)).toBeNull()
  })

  it('ignores a preselected group that is not active any more', async () => {
    const { user, created } = setup({
      groups: [makeGroup({ id: 2, name: 'DevOps-1' }), makeGroup({ id: 3, name: 'Linux-2' })],
      groupId: 99,
    })
    const group = await screen.findByRole('combobox', { name: 'Группа' })
    expect(group).toHaveTextContent('Выберите группу')
    await user.click(screen.getByRole('button', { name: 'Создать занятие' }))
    expect(screen.getByText('Выберите группу', { selector: 'p' })).toBeInTheDocument()
    expect(created).toHaveLength(0)
  })

  it('creates a weekly series and opens the first lesson', async () => {
    const { user, created, onOpenChange } = setup()
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №5'))
    await user.click(screen.getByLabelText('Ссылка Zoom или номер конференции'))
    await user.paste('https://zoom.us/j/1234567890')
    await screen.findByText('Конференция 123 456 7890')
    await user.click(screen.getByRole('checkbox', { name: 'Повторять каждую неделю' }))
    const weeks = screen.getByLabelText('Сколько недель')
    await user.clear(weeks)
    await user.type(weeks, '3')
    await user.click(screen.getByRole('button', { name: 'Создать занятия' }))

    expect(await screen.findByText('Открыто занятие 77')).toBeInTheDocument()
    expect(created).toEqual([
      {
        group_id: 2,
        title: 'Занятие №5',
        zoom: 'https://zoom.us/j/1234567890',
        planned_start_at: local(6, 17, 30),
        planned_end_at: local(6, 19, 30),
        repeat_weekly: 3,
      },
    ])
    expect(toastMock.success).toHaveBeenCalledWith('Создано 3 занятия')
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('shows a server error inline and keeps the form', async () => {
    const { user, fetchMock } = setup()
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init: RequestInit = {}) => {
      const url = new URL(String(input), 'http://localhost')
      if (init.method === 'POST') return errorResponse(422, 'validation', 'Ссылка Zoom уже используется в другом занятии', { field: 'zoom' })
      if (url.pathname === '/api/zoom/parse') return jsonResponse({ meeting_id: '1234567890', formatted: '123 456 7890' })
      if (url.pathname === '/api/groups') return jsonResponse([makeGroup()])
      return jsonResponse([])
    })
    await user.click(await screen.findByLabelText('Ссылка Zoom или номер конференции'))
    await user.paste('1234567890')
    await screen.findByText('Конференция 123 456 7890')
    await waitFor(() => expect(screen.getByLabelText('Название')).toHaveValue('Занятие №1'))
    await user.click(screen.getByRole('button', { name: 'Создать занятие' }))
    expect(await screen.findByText('Ссылка Zoom уже используется в другом занятии')).toBeInTheDocument()
    expect(screen.getByLabelText('Ссылка Zoom или номер конференции')).toHaveFocus()
    expect(toastMock.error).not.toHaveBeenCalled()
  })

  it('asks to create a group first when there are none', async () => {
    const { user } = setup({ groups: [] })
    expect(await screen.findByText('Сначала создайте группу')).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Создать группу' }))
    expect(await screen.findByText('Страница групп')).toBeInTheDocument()
  })
})

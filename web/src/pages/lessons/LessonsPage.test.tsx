import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { GroupSummary, LessonSummary } from '@/lib/types'
import LessonsPage from '@/pages/lessons/LessonsPage'
import { makeGroup, makeSummary, zeroCounts } from '@/pages/lessons/testing'
import { FakeEventSource } from '@/test/fakeEventSource'
import { jsonResponse, mockFetch, renderRoutes } from '@/test/utils'

// Tuesday, 6 October 2026, 17:05 local time.
const NOW = new Date(2026, 9, 6, 17, 5)
const at = (day: number, h: number, mi = 0, month = 9) => new Date(2026, month, day, h, mi).toISOString()

function lesson(id: number, day: number, h: number, overrides: Partial<LessonSummary> = {}): LessonSummary {
  return makeSummary({
    id,
    title: `Занятие №${id}`,
    planned_start_at: at(day, h),
    planned_end_at: at(day, h + 1, 30),
    status: 'planned',
    actual_start_at: null,
    counts: { ...zeroCounts, in_group: 25, not_seen: 25, absent: 25 },
    ...overrides,
  })
}

const LINUX = { id: 3, name: 'Linux-2' }

/** Today: one running (live counts), one later; upcoming in 3 days; past: finished and a lesson without Zoom data. */
function sampleLessons(): LessonSummary[] {
  return [
    lesson(1, 6, 16, {
      title: 'DevOps. Занятие №4',
      status: 'active',
      actual_start_at: at(6, 16, 1),
      counts: { in_group: 25, online: 12, seen: 14, not_seen: 11, unidentified: 2, present: 13, absent: 12 },
    }),
    lesson(2, 6, 19, { title: 'Linux. Вводное', group: LINUX }),
    lesson(3, 9, 18, { title: 'DevOps. Занятие №5' }),
    lesson(4, 2, 18, {
      title: 'DevOps. Занятие №3',
      status: 'finished',
      counts: { in_group: 25, online: 0, seen: 21, not_seen: 4, unidentified: 0, present: 20, absent: 5 },
    }),
    lesson(5, 1, 18, { title: 'Linux. Пропущенное', group: LINUX }),
  ]
}

interface SetupOptions {
  lessons?: LessonSummary[]
  groups?: GroupSummary[]
  path?: string
}

function setup({
  lessons = sampleLessons(),
  groups = [makeGroup(), makeGroup({ id: 3, name: 'Linux-2' })],
  path = '/',
}: SetupOptions = {}) {
  const current = { lessons }
  const fetchMock = mockFetch({
    'GET /api/lessons': (url: URL) =>
      // The dialog asks for one group's lessons (title suggestion); the page asks for a date window.
      jsonResponse(url.searchParams.has('group_id') ? [] : current.lessons),
    'GET /api/groups': groups,
  })
  const user = userEvent.setup()
  const utils = renderRoutes(
    [
      { path: '/', element: <LessonsPage /> },
      { path: '/lessons/:id', element: <p>Экран занятия</p> },
      { path: '/groups', element: <p>Страница групп</p> },
    ],
    path,
  )
  return { ...utils, user, fetchMock, current }
}

const listCalls = (fetchMock: ReturnType<typeof mockFetch>) =>
  fetchMock.mock.calls
    .map(([input]) => new URL(String(input), 'http://localhost'))
    .filter((u) => u.pathname === '/api/lessons' && !u.searchParams.has('group_id'))

const section = (name: string) => screen.getByRole('region', { name: new RegExp(`^${name}`) })

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] })
  vi.setSystemTime(NOW)
  FakeEventSource.reset()
  vi.stubGlobal('EventSource', FakeEventSource)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('LessonsPage', () => {
  it('asks for the last 30 and the next 14 days', async () => {
    const { fetchMock } = setup()
    await screen.findByRole('heading', { name: /^Сегодня/ })
    const [call] = listCalls(fetchMock)
    expect(call.searchParams.get('from')).toBe(new Date(2026, 8, 6).toISOString())
    expect(Date.parse(call.searchParams.get('to') ?? '')).toBe(new Date(2026, 9, 20, 23, 59, 59, 999).getTime())
  })

  it('splits lessons into today, upcoming and past', async () => {
    setup()
    await screen.findByRole('heading', { name: /^Сегодня/ })

    const today = section('Сегодня')
    const running = within(today).getByRole('article', { name: 'DevOps. Занятие №4' })
    expect(within(running).getByText('Идёт')).toBeInTheDocument()
    expect(within(running).getByText('12')).toBeInTheDocument()
    expect(within(running).getByText('из 25 сейчас в Zoom')).toBeInTheDocument()
    expect(within(running).getByText('Были хотя бы раз: 14')).toBeInTheDocument()
    expect(within(running).getByText(/Не идентифицированы: 2/)).toBeInTheDocument()
    expect(within(running).getByRole('link', { name: 'Открыть занятие «DevOps. Занятие №4»' })).toHaveAttribute(
      'href',
      '/lessons/1',
    )

    const later = within(today).getByRole('article', { name: 'Linux. Вводное' })
    expect(within(later).getByText('Запланировано')).toBeInTheDocument()
    expect(within(later).getByText(/Начало в 19:00/)).toBeInTheDocument()
    expect(within(later).getByText('В группе 25 студентов')).toBeInTheDocument()

    const upcoming = section('Предстоящие')
    expect(within(upcoming).getByRole('link', { name: /DevOps\. Занятие №5/ })).toHaveAttribute('href', '/lessons/3')
    // Capitalized by CSS (first-letter:uppercase).
    expect(within(upcoming).getByText('пт, 9 октября')).toBeInTheDocument()

    const past = section('Прошедшие')
    const rows = within(past).getAllByRole('link')
    // Newest first.
    expect(rows.map((r) => r.getAttribute('href'))).toEqual(['/lessons/4', '/lessons/5'])
    expect(within(rows[0]).getByLabelText('Были 20 из 25')).toBeInTheDocument()
    expect(within(rows[1]).getByText('Нет данных из Zoom')).toBeInTheDocument()
  })

  it('refreshes live counts when the server says lessons changed', async () => {
    const { current } = setup()
    await screen.findByRole('heading', { name: /^Сегодня/ })
    const es = FakeEventSource.latest()
    expect(es.url).toBe('/api/stream')
    act(() => es.open())

    current.lessons = current.lessons.map((l) =>
      l.id === 1 ? { ...l, counts: { ...l.counts, online: 15, seen: 16, unidentified: 0 } } : l,
    )
    act(() => es.emit('lessons', ''))
    const running = within(section('Сегодня')).getByRole('article', { name: 'DevOps. Занятие №4' })
    await waitFor(() => expect(within(running).getByText('15')).toBeInTheDocument())
    expect(within(running).getByText('Были хотя бы раз: 16')).toBeInTheDocument()
    expect(within(running).queryByText(/Не идентифицированы/)).toBeNull()
  })

  it('dates a lesson that is still running since an earlier day', async () => {
    setup({ lessons: [lesson(6, 5, 18, { title: 'Вчерашнее', status: 'active', actual_start_at: at(5, 18, 2) })] })
    const card = await screen.findByRole('article', { name: 'Вчерашнее' })
    expect(within(card).getByText(/пн, 5 октября/)).toBeInTheDocument()
    expect(within(card).getByText(/18:00–19:30/)).toBeInTheDocument()
    // Zoom never reported the end: no stale "N из M сейчас в Zoom", a hint to finish it instead.
    expect(within(card).getByText('Zoom не сообщил об окончании — откройте занятие и завершите его')).toBeInTheDocument()
    expect(within(card).queryByText(/сейчас в Zoom/)).toBeNull()
  })

  it('says when a planned lesson got no data from Zoom', async () => {
    setup({ lessons: [lesson(7, 6, 14, { title: 'Утреннее' })] })
    const card = await screen.findByRole('article', { name: 'Утреннее' })
    expect(within(card).getByText(/Из Zoom никто не подключался/)).toBeInTheDocument()
  })

  it('filters by group and by search text', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { name: /^Сегодня/ })

    await user.click(screen.getByRole('combobox', { name: 'Группа' }))
    await user.click(await screen.findByRole('option', { name: 'Linux-2' }))
    await waitFor(() => expect(screen.queryByRole('article', { name: 'DevOps. Занятие №4' })).toBeNull())
    expect(screen.getByRole('article', { name: 'Linux. Вводное' })).toBeInTheDocument()
    expect(within(section('Прошедшие')).getAllByRole('link')).toHaveLength(1)

    await user.click(screen.getByRole('button', { name: 'Сбросить' }))
    expect(screen.getByRole('article', { name: 'DevOps. Занятие №4' })).toBeInTheDocument()

    // "ё" and case do not matter; every word must match.
    await user.type(screen.getByRole('searchbox', { name: 'Поиск по названию или группе' }), 'devops №5')
    expect(screen.queryByRole('article')).toBeNull()
    expect(within(section('Предстоящие')).getByRole('link', { name: /DevOps\. Занятие №5/ })).toBeInTheDocument()
    expect(screen.getByText('Сегодня занятий нет')).toBeInTheDocument()

    await user.type(screen.getByRole('searchbox', { name: 'Поиск по названию или группе' }), ' химия')
    expect(screen.getByText('Ничего не нашлось')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Сбросить фильтр' }))
    expect(screen.getByRole('searchbox', { name: 'Поиск по названию или группе' })).toHaveValue('')
    expect(screen.getByRole('article', { name: 'DevOps. Занятие №4' })).toBeInTheDocument()
  })

  it('points at Zoom names that are still not sorted out after a lesson', async () => {
    const counts = { in_group: 25, online: 0, seen: 21, not_seen: 4, unidentified: 2, present: 20, absent: 5 }
    setup({
      lessons: [
        lesson(8, 6, 9, { title: 'Утреннее', status: 'finished', counts }),
        lesson(9, 3, 18, { title: 'Прошлое', status: 'finished', counts }),
      ],
    })
    const card = await screen.findByRole('article', { name: 'Утреннее' })
    expect(within(card).getByText('Не идентифицированы: 2 — проверьте, не студенты ли это')).toBeInTheDocument()
    const row = within(section('Прошедшие')).getByRole('link', { name: /Прошлое/ })
    expect(within(row).getByText('Не опознаны: 2')).toBeInTheDocument()
  })

  it('keeps the group filter in the address', async () => {
    setup({ path: '/?group=3' })
    expect(await screen.findByRole('article', { name: 'Linux. Вводное' })).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: 'DevOps. Занятие №4' })).toBeNull()
    expect(screen.getByRole('combobox', { name: 'Группа' })).toHaveTextContent('Linux-2')
  })

  it('explains the three steps when there are no lessons and no groups yet', async () => {
    const { user } = setup({ lessons: [], groups: [] })
    expect(await screen.findByRole('heading', { name: 'Занятий пока нет — начните с трёх шагов' })).toBeInTheDocument()
    expect(screen.getByText('Создайте группу')).toBeInTheDocument()
    expect(screen.getByText('Создайте занятие со ссылкой Zoom')).toBeInTheDocument()
    expect(screen.getByText('Откройте его во время урока')).toBeInTheDocument()
    await user.click(screen.getByRole('link', { name: 'Создать группу' }))
    expect(await screen.findByText('Страница групп')).toBeInTheDocument()
  })

  it('marks the first step done and offers a new lesson when a group exists', async () => {
    const { user } = setup({ lessons: [], groups: [makeGroup()] })
    await screen.findByRole('heading', { name: 'Занятий пока нет — начните с трёх шагов' })
    expect(screen.getByText(/Шаг 1 \(готово\)/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Мои группы' })).toHaveAttribute('href', '/groups')
    const steps = screen.getByRole('list')
    await user.click(within(steps).getByRole('button', { name: 'Новое занятие' }))
    expect(await screen.findByRole('dialog', { name: 'Новое занятие' })).toBeInTheDocument()
  })

  it('opens the new lesson dialog from the big button', async () => {
    const { user } = setup()
    await screen.findByRole('heading', { name: /^Сегодня/ })
    const [headerButton] = screen.getAllByRole('button', { name: 'Новое занятие' })
    await user.click(headerButton)
    const dialog = await screen.findByRole('dialog', { name: 'Новое занятие' })
    expect(within(dialog).getByLabelText('Ссылка Zoom или номер конференции')).toBeInTheDocument()
  })
})

import { act, configure, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { formatEventTime, parseStatusParam, parseTypeParam } from '@/components/admin/events'
import { prettyJson } from '@/components/admin/json'
import type { WebhookEventDetail, WebhookEventSummary, WebhookEventsPage } from '@/lib/types'
import { routes } from '@/routes'
import { jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'

// Pages are lazy routes: warm the module so the first findBy* does not race a cold
// transform on a busy machine, and allow slow CI runners more time.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })
beforeAll(async () => {
  await import('@/pages/admin/EventsPage')
})

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
  toastMock.info.mockReset()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function makeEvent(overrides: Partial<WebhookEventSummary> = {}): WebhookEventSummary {
  return {
    id: 1,
    event_type: 'meeting.participant_joined',
    meeting_id: '1234567890',
    meeting_uuid: 'uuid-abc==',
    participant_name: 'Иванов Иван',
    occurred_at: '2026-10-06T15:00:00Z',
    received_at: '2026-10-06T15:00:01Z',
    processed_at: '2026-10-06T15:00:01Z',
    processing_status: 'processed',
    attempts: 1,
    error: '',
    ...overrides,
  }
}

const RAW_PAYLOAD =
  '{"event":"meeting.participant_joined","payload":{"account_id":"acc","object":{"id":"1234567890","participant":{"user_name":"Иванов Иван"}}},"event_ts":1759762800000}'

function makeDetail(summary: WebhookEventSummary, overrides: Partial<WebhookEventDetail> = {}): WebhookEventDetail {
  return {
    ...summary,
    event_ts: 1759762800000,
    dedupe_key: 'joined|uuid-abc==|p1|1759762800000',
    next_attempt_at: null,
    payload: RAW_PAYLOAD,
    ...overrides,
  }
}

type Handlers = Record<string, unknown>

function setup(path = '/admin/events', extra: Handlers = {}, pages?: (url: URL) => WebhookEventsPage) {
  const listPage =
    pages ??
    (() => ({
      items: [makeEvent()],
      next_before_id: null,
    }))
  const fetchMock = mockFetch({
    'GET /api/auth/me': makeUser({ role: 'admin' }),
    'GET /api/zoom/events': (url: URL) => jsonResponse(listPage(url)),
    ...extra,
  })
  return { fetchMock, ...renderRoutes(routes, path) }
}

/** Query parameters of every GET /api/zoom/events call so far. */
const listCalls = (fetchMock: ReturnType<typeof mockFetch>) =>
  fetchMock.mock.calls
    .map(([u, init]) => ({ url: new URL(String(u), 'http://localhost'), method: init?.method ?? 'GET' }))
    .filter(({ url, method }) => method === 'GET' && url.pathname === '/api/zoom/events')
    .map(({ url }) => url.searchParams)

describe('journal helpers', () => {
  it('pretty-prints valid JSON and keeps anything else as is', () => {
    expect(prettyJson('{"a":1,"b":{"c":"д"}}')).toEqual({ text: '{\n  "a": 1,\n  "b": {\n    "c": "д"\n  }\n}', valid: true })
    expect(prettyJson('not json')).toEqual({ text: 'not json', valid: false })
    expect(prettyJson('')).toEqual({ text: '', valid: false })
    expect(prettyJson(null)).toEqual({ text: '', valid: false })
    // 16+ digit numbers would be rounded by JSON.parse: show the original bytes.
    const big = '{"id":12345678901234567}'
    expect(prettyJson(big)).toEqual({ text: big, valid: false })
  })

  it('reads filters from the URL, ignoring unknown values', () => {
    expect(parseStatusParam('failed')).toBe('failed')
    expect(parseStatusParam('bogus')).toBeUndefined()
    expect(parseStatusParam(null)).toBeUndefined()
    expect(parseTypeParam('meeting.ended')).toBe('meeting.ended')
    expect(parseTypeParam('constructor')).toBeUndefined()
    expect(parseTypeParam('__proto__')).toBeUndefined()
  })

  it('formats journal times with seconds', () => {
    expect(formatEventTime(null)).toBe('—')
    expect(formatEventTime('2026-10-06T15:00:07Z')).toMatch(/^\d{2}\.\d{2}\.2026 \d{2}:00:07$/)
  })
})

describe('EventsPage', () => {
  it('lists events in plain Russian and pages with "Показать ещё"', async () => {
    const { fetchMock } = setup('/admin/events', {}, (url) =>
      url.searchParams.get('before_id') === '28'
        ? { items: [makeEvent({ id: 27 }), makeEvent({ id: 26, event_type: 'meeting.ended', participant_name: '' })], next_before_id: null }
        : {
            items: [
              makeEvent({ id: 30, event_type: 'meeting.participant_left', participant_name: 'Петров Пётр' }),
              makeEvent({ id: 29, processing_status: 'failed', attempts: 3, error: 'урок не найден' }),
              makeEvent({ id: 28, processing_status: 'duplicate' }),
            ],
            next_before_id: 28,
          },
    )
    const user = userEvent.setup()
    const table = await screen.findByRole('table')
    expect(within(table).getAllByRole('row')).toHaveLength(4)
    expect(within(table).getByText('Выход участника')).toBeInTheDocument()
    expect(within(table).getByText('Петров Пётр')).toBeInTheDocument()
    expect(within(table).getAllByText('123 456 7890').length).toBeGreaterThan(0)
    // Status is a badge with text, not only a color.
    expect(within(table).getByText('Ошибка')).toBeInTheDocument()
    expect(within(table).getByText('урок не найден')).toBeInTheDocument()
    expect(within(table).getByText('Повтор')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Показать ещё' }))
    await waitFor(() => expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(6))
    expect(screen.getByText('Конец конференции')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Показать ещё' })).toBeNull()
    expect(screen.getByText('Показаны все события: 5')).toBeInTheDocument()

    const calls = listCalls(fetchMock)
    expect(calls[0].get('before_id')).toBeNull()
    expect(calls.at(-1)!.get('before_id')).toBe('28')
  })

  it('applies the status from the link on the Zoom page and filters by type and search', async () => {
    const { fetchMock } = setup('/admin/events?status=failed')
    const user = userEvent.setup()
    await screen.findByRole('table')
    expect(listCalls(fetchMock)[0].get('status')).toBe('failed')
    expect(screen.getByRole('combobox', { name: 'Статус' })).toHaveTextContent('Ошибка')

    await user.click(screen.getByRole('combobox', { name: 'Тип события' }))
    await user.click(await screen.findByRole('option', { name: 'Вход участника' }))
    await waitFor(() => expect(listCalls(fetchMock).at(-1)!.get('type')).toBe('meeting.participant_joined'))
    expect(listCalls(fetchMock).at(-1)!.get('status')).toBe('failed')

    await user.type(screen.getByRole('searchbox', { name: 'Поиск' }), 'Иванов')
    await waitFor(() => expect(listCalls(fetchMock).at(-1)!.get('q')).toBe('Иванов'))

    await user.click(screen.getAllByRole('button', { name: 'Сбросить фильтры' })[0])
    await waitFor(() => {
      const last = listCalls(fetchMock).at(-1)!
      expect([last.get('status'), last.get('type'), last.get('q')]).toEqual([null, null, null])
    })
    expect(screen.getByRole('searchbox', { name: 'Поиск' })).toHaveValue('')
  })

  it('opens an event with every field and the formatted payload, and reprocesses it', async () => {
    const failed = makeEvent({ id: 29, processing_status: 'failed', attempts: 3, error: 'урок не найден' })
    const { fetchMock } = setup(
      '/admin/events',
      {
        'GET /api/zoom/events/29': makeDetail(failed),
        'POST /api/zoom/events/29/reprocess': makeDetail({ ...failed, processing_status: 'pending', error: '' }),
      },
      () => ({ items: [failed], next_before_id: null }),
    )
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Вход участника, №29: подробнее' }))

    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByRole('heading', { name: 'Вход участника' })).toBeInTheDocument()
    expect(within(sheet).getByText('Ошибка обработки')).toBeInTheDocument()
    expect(within(sheet).getByText('uuid-abc==')).toBeInTheDocument()
    const payload = await within(sheet).findByTestId('event-payload')
    expect(payload.textContent).toBe(JSON.stringify(JSON.parse(RAW_PAYLOAD), null, 2))
    expect(payload.textContent).toContain('\n  "event": "meeting.participant_joined"')
    expect(within(sheet).getByText('joined|uuid-abc==|p1|1759762800000')).toBeInTheDocument()
    expect(within(sheet).getByRole('button', { name: 'Скопировать' })).toBeInTheDocument()

    await user.click(within(sheet).getByRole('button', { name: 'Обработать заново' }))
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([u, init]) => String(u) === '/api/zoom/events/29/reprocess' && init?.method === 'POST'),
      ).toBe(true),
    )
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Событие отправлено на повторную обработку'))
  })

  it('shows a body that is not JSON as is', async () => {
    const ev = makeEvent({ id: 5 })
    setup('/admin/events', { 'GET /api/zoom/events/5': makeDetail(ev, { payload: 'garbage<>' }) }, () => ({
      items: [ev],
      next_before_id: null,
    }))
    const user = userEvent.setup()
    await screen.findByRole('table')
    // The whole row opens the details too.
    await user.click(screen.getByText('Иванов Иван'))
    const sheet = await screen.findByRole('dialog')
    expect(await within(sheet).findByTestId('event-payload')).toHaveTextContent('garbage<>')
    expect(within(sheet).getByText('Показано как есть, без форматирования.')).toBeInTheDocument()
  })

  it('retries every failed event after confirmation', async () => {
    const { fetchMock } = setup('/admin/events', { 'POST /api/zoom/events/reprocess-failed': { count: 3 } })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Повторить все ошибочные' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Обработать заново' }))
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Отправлено на повторную обработку: 3 события'))
    expect(
      fetchMock.mock.calls.some(([u, init]) => String(u) === '/api/zoom/events/reprocess-failed' && init?.method === 'POST'),
    ).toBe(true)
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('says so when there is nothing to retry', async () => {
    setup('/admin/events', { 'POST /api/zoom/events/reprocess-failed': { count: 0 } })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Повторить все ошибочные' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Обработать заново' }))
    await waitFor(() => expect(toastMock.info).toHaveBeenCalledWith('Событий с ошибкой нет — повторять нечего'))
  })

  it('explains an empty journal and links to the Zoom connection page', async () => {
    setup('/admin/events', {}, () => ({ items: [], next_before_id: null }))
    expect(await screen.findByText('Событий пока нет')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Подключение к Zoom' })).toHaveAttribute('href', '/zoom')
  })

  it('offers to reset filters when nothing matches', async () => {
    setup('/admin/events?status=failed', {}, () => ({ items: [], next_before_id: null }))
    expect(await screen.findByText('Ничего не нашлось')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Сбросить фильтры' }).length).toBeGreaterThan(0)
  })

  it('follows the URL when it changes from outside (nav link, back button)', async () => {
    const { fetchMock, router } = setup('/admin/events?q=Иванов')
    const search = await screen.findByRole('searchbox', { name: 'Поиск' })
    expect(search).toHaveValue('Иванов')
    await screen.findByRole('table')
    expect(listCalls(fetchMock).at(-1)!.get('q')).toBe('Иванов')

    // "Журнал событий" in the navigation drops the query.
    await act(() => router.navigate('/admin/events'))
    await waitFor(() => expect(search).toHaveValue(''))
    // The old text must not come back after the search debounce.
    await new Promise((r) => setTimeout(r, 500))
    expect(router.state.location.search).toBe('')
    expect(listCalls(fetchMock).at(-1)!.get('q')).toBeNull()
  })

  it('keeps the event on screen while the panel closes and highlights only an open one', async () => {
    const ev = makeEvent({ id: 31 })
    setup('/admin/events', { 'GET /api/zoom/events/31': makeDetail(ev) }, () => ({ items: [ev], next_before_id: null }))
    const user = userEvent.setup()
    const open = await screen.findByRole('button', { name: /Вход участника, №31/ })
    const row = open.closest('tr')!
    expect(row).not.toHaveAttribute('data-state')
    await user.click(open)
    const sheet = await screen.findByRole('dialog')
    expect(within(sheet).getByText(/Событие №31/)).toBeInTheDocument()
    expect(row).toHaveAttribute('data-state', 'selected')

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(row).not.toHaveAttribute('data-state')
  })
})

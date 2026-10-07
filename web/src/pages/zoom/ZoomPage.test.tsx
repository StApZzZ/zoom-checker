import { configure, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import type { LessonSummary, User, ZoomSession, ZoomSessionLesson, ZoomStatus } from '@/lib/types'
import { routes } from '@/routes'
import { jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'
import { linkCandidates, runningUnlinkedSessions } from '@/pages/zoom/linkCandidates'
import {
  isInsecureWebhookUrl,
  isIpWebhookUrl,
  isLocalWebhookUrl,
  zoomClockProblem,
  zoomConnectionState,
} from '@/pages/zoom/zoomStatus'

// Pages are lazy routes: warm the module so the first findBy* does not race a cold
// transform on a busy machine, and allow slow CI runners more time.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })
beforeAll(async () => {
  await import('@/pages/zoom/ZoomPage')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

function makeStatus(overrides: Partial<ZoomStatus> = {}): ZoomStatus {
  return {
    webhook_url: 'https://attendance.example.com/api/webhooks/zoom',
    secret_configured: true,
    last_event_at: minutesAgo(5),
    last_event_type: 'meeting.participant_joined',
    events_24h: 42,
    failed_count: 0,
    pending_count: 0,
    ...overrides,
  }
}

function makeSession(overrides: Partial<ZoomSession> = {}): ZoomSession {
  return {
    id: 7,
    meeting_uuid: 'uuid-7',
    meeting_id: '1234567890',
    topic: 'Linux, занятие 4',
    lesson_id: null,
    lesson: null,
    link_mode: 'auto',
    started_at: '2026-10-06T15:00:00Z',
    ended_at: '2026-10-06T16:30:00Z',
    first_event_at: '2026-10-06T15:00:00Z',
    ...overrides,
  }
}

/** The lesson summary a linked session carries. */
function linked(id: number, title: string): ZoomSessionLesson {
  return { id, title, group_name: 'DevOps-1', planned_start_at: '2026-10-06T15:00:00Z' }
}

function makeLesson(overrides: Partial<LessonSummary> = {}): LessonSummary {
  return {
    id: 1,
    group: { id: 1, name: 'DevOps-1' },
    title: 'Занятие',
    zoom_meeting_id: '1234567890',
    zoom_meeting_id_formatted: '123 456 7890',
    zoom_join_url: '',
    planned_start_at: '2026-10-06T15:00:00Z',
    planned_end_at: '2026-10-06T16:30:00Z',
    actual_start_at: null,
    actual_end_at: null,
    status: 'planned',
    manual_finished_at: null,
    counts: { in_group: 0, online: 0, seen: 0, not_seen: 0, unidentified: 0, present: 0, absent: 0 },
    ...overrides,
  }
}

function setup({
  me = makeUser({ role: 'admin' }),
  status = makeStatus(),
  sessions = [] as ZoomSession[],
  extra = {} as Record<string, unknown>,
}: { me?: User; status?: ZoomStatus; sessions?: ZoomSession[]; extra?: Record<string, unknown> } = {}) {
  const fetchMock = mockFetch({
    'GET /api/auth/me': me,
    'GET /api/zoom/status': status,
    'GET /api/zoom/sessions': (url: URL) =>
      jsonResponse(url.searchParams.get('unlinked') === '1' ? sessions.filter((s) => s.lesson_id === null) : sessions),
    ...extra,
  })
  return { fetchMock, ...renderRoutes(routes, '/zoom') }
}

describe('zoomConnectionState', () => {
  const now = Date.parse('2026-10-06T15:00:00Z')
  it.each([
    [{ secret_configured: false, last_event_at: '2026-10-06T14:59:00Z' }, 'not_configured'],
    [{ secret_configured: true, last_event_at: null }, 'waiting'],
    [{ secret_configured: true, last_event_at: '2026-10-06T10:00:00Z' }, 'ok'],
    [{ secret_configured: true, last_event_at: '2026-10-05T15:00:00Z' }, 'ok'],
    [{ secret_configured: true, last_event_at: '2026-10-05T14:59:59Z' }, 'stale'],
  ] as const)('%o → %s', (status, expected) => {
    expect(zoomConnectionState(status, now)).toBe(expected)
  })

  it('flags plain http webhook addresses', () => {
    expect(isInsecureWebhookUrl('http://10.0.0.5:8080/api/webhooks/zoom')).toBe(true)
    expect(isInsecureWebhookUrl('https://a.example.com/api/webhooks/zoom')).toBe(false)
  })

  it.each([
    ['http://localhost:8080/api/webhooks/zoom', true],
    ['https://127.0.0.1/api/webhooks/zoom', true],
    ['https://10.0.0.5/api/webhooks/zoom', true],
    ['https://172.20.1.2/api/webhooks/zoom', true],
    ['https://192.168.1.10:8443/api/webhooks/zoom', true],
    ['https://[::1]:8080/api/webhooks/zoom', true],
    ['https://[fd12:3456::1]/api/webhooks/zoom', true],
    ['https://attendance/api/webhooks/zoom', true],
    ['https://zoom-checker.corp/api/webhooks/zoom', true],
    ['https://dc1.local/api/webhooks/zoom', true],
    ['https://attendance.example.com/api/webhooks/zoom', false],
    ['https://172.32.0.1/api/webhooks/zoom', false],
    ['https://8.8.8.8/api/webhooks/zoom', false],
    ['https://[2001:db8::1]/api/webhooks/zoom', false],
    ['not a url', false],
    ['', false],
  ] as const)('local-only address %s → %s', (url, expected) => {
    expect(isLocalWebhookUrl(url)).toBe(expected)
  })
})

describe('runningUnlinkedSessions', () => {
  const planned = '2026-10-06T15:00:00Z'
  const now = Date.parse('2026-10-06T15:20:00Z')
  const running = (overrides: Partial<ZoomSession>) =>
    makeSession({ ended_at: null, started_at: '2026-10-06T15:02:00Z', first_event_at: '2026-10-06T15:02:00Z', ...overrides })

  it('offers conferences without a lesson that are going on around the planned start, closest first', () => {
    const sessions = [
      running({ id: 1, meeting_id: '87022223333', started_at: '2026-10-06T14:10:00Z' }),
      running({ id: 2, meeting_id: '87022223334', started_at: '2026-10-06T14:58:00Z' }),
      // No start time from Zoom: the first event counts.
      running({ id: 3, started_at: null, first_event_at: '2026-10-06T15:10:00Z' }),
    ]
    expect(runningUnlinkedSessions(sessions, planned, now).map((s) => s.id)).toEqual([2, 3, 1])
  })

  it.each([
    ['already ended', running({ ended_at: '2026-10-06T15:10:00Z' })],
    ['linked to a lesson', running({ lesson_id: 9, lesson: linked(9, 'Другое') })],
    ['started more than an hour before the planned start', running({ started_at: '2026-10-06T13:59:59Z' })],
    ['started after now (clock skew)', running({ started_at: '2026-10-06T15:21:00Z' })],
    ['started too long ago to be still going', running({ started_at: '2026-10-06T02:00:00Z' })],
    ['without any time', running({ started_at: null, first_event_at: '' })],
  ])('leaves out a conference %s', (_name, session) => {
    expect(runningUnlinkedSessions([session], planned, now)).toEqual([])
  })

  it('leaves out an old conference even for a lesson planned long ago', () => {
    const old = running({ started_at: '2026-10-05T14:30:00Z', first_event_at: '2026-10-05T14:30:00Z' })
    expect(runningUnlinkedSessions([old], '2026-10-05T15:00:00Z', now)).toEqual([])
  })
})

describe('linkCandidates', () => {
  it('puts lessons with the same Zoom ID first (closest first), then other lessons of that day', () => {
    const session = makeSession({ started_at: '2026-10-06T15:05:00Z' })
    const lessons = [
      makeLesson({ id: 1, title: 'Чужая, тот же день', zoom_meeting_id: '999999999', planned_start_at: '2026-10-06T09:00:00Z' }),
      makeLesson({ id: 2, title: 'Тот же ID, вчера', planned_start_at: '2026-10-05T15:00:00Z' }),
      makeLesson({ id: 3, title: 'Тот же ID, сегодня', planned_start_at: '2026-10-06T15:00:00Z' }),
      makeLesson({ id: 4, title: 'Чужая, другой день', zoom_meeting_id: '999999999', planned_start_at: '2026-10-08T09:00:00Z' }),
    ]
    const { sameMeeting, sameDay } = linkCandidates(session, lessons)
    expect(sameMeeting.map((l) => l.id)).toEqual([3, 2])
    expect(sameDay.map((l) => l.id)).toEqual([1])
    expect(linkCandidates(session, lessons, 'вчера').sameMeeting.map((l) => l.id)).toEqual([2])
    expect(linkCandidates(session, lessons, 'devops-1').sameDay.map((l) => l.id)).toEqual([1])
  })
})

describe('zoomClockProblem', () => {
  const now = Date.parse('2026-10-06T15:00:00Z')
  it.each([
    [{}, null],
    [{ clock_skew_seconds: null, last_rejected_at: null }, null],
    [{ clock_skew_seconds: 120 }, null],
    [{ clock_skew_seconds: -90 }, null],
    [{ clock_skew_seconds: 400 }, { skewMinutes: 7, serverBehind: true, rejectedAt: null }],
    [{ clock_skew_seconds: -3 * 3600 }, { skewMinutes: 180, serverBehind: false, rejectedAt: null }],
    [{ last_rejected_at: '2026-10-06T14:00:00Z' }, { skewMinutes: null, serverBehind: false, rejectedAt: '2026-10-06T14:00:00Z' }],
    [{ last_rejected_at: '2026-10-05T14:59:00Z' }, null],
  ] as const)('%o → %o', (status, expected) => {
    expect(zoomClockProblem(status, now)).toEqual(expected)
  })

  it.each([
    ['https://203.0.113.10/api/webhooks/zoom', true],
    ['https://[2001:db8::1]/api/webhooks/zoom', true],
    ['https://attendance.example.com/api/webhooks/zoom', false],
    ['not a url', false],
  ] as const)('IP address %s → %s', (url, expected) => {
    expect(isIpWebhookUrl(url)).toBe(expected)
  })
})

describe('ZoomPage: connection status', () => {
  it('warns the admin that the server clock differs from Zoom and how to fix it', async () => {
    setup({ status: makeStatus({ clock_skew_seconds: 400, last_rejected_at: minutesAgo(30) }) })
    const warning = await screen.findByTestId('zoom-clock-warning')
    expect(warning).toHaveTextContent('Часы сервера расходятся с Zoom')
    expect(warning).toHaveTextContent('отстают от Zoom примерно на 7 мин')
    expect(warning).toHaveTextContent('такие отметки теряются')
    expect(within(warning).getByText('sudo timedatectl set-ntp true')).toBeInTheDocument()
  })

  it('tells a teacher about lost events without server commands', async () => {
    setup({ me: makeUser(), status: makeStatus({ clock_skew_seconds: null, last_rejected_at: minutesAgo(10) }) })
    const warning = await screen.findByTestId('zoom-clock-warning')
    expect(warning).toHaveTextContent('Сообщите администратору')
    expect(warning).not.toHaveTextContent('timedatectl')
    expect(warning).not.toHaveTextContent('отстают')
  })

  it('stays quiet about a small clock difference', async () => {
    setup({ status: makeStatus({ clock_skew_seconds: -45, last_rejected_at: null }) })
    await screen.findByTestId('webhook-url')
    expect(screen.queryByTestId('zoom-clock-warning')).toBeNull()
  })

  it('warns that Zoom does not accept an IP address', async () => {
    setup({ status: makeStatus({ webhook_url: 'https://203.0.113.10/api/webhooks/zoom' }) })
    expect(await screen.findByText('Zoom не примет адрес с IP-адресом')).toBeInTheDocument()
    expect(screen.queryByText('Zoom не сможет достучаться до этого адреса')).toBeNull()
  })

  it('shows a healthy connection to an admin with the webhook address and a collapsed guide', async () => {
    setup()
    expect(await screen.findByText('Zoom присылает события')).toBeInTheDocument()
    expect(screen.getByText('Вход участника')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
    expect(screen.getByTestId('webhook-url')).toHaveTextContent('https://attendance.example.com/api/webhooks/zoom')
    expect(screen.getByRole('button', { name: 'Скопировать адрес' })).toBeInTheDocument()
    const guide = screen.getByRole('button', { name: /Как подключить Zoom/ })
    expect(guide).toHaveAttribute('aria-expanded', 'false')
    await userEvent.setup().click(guide)
    expect(guide).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('Participant/Host joined meeting')).toBeInTheDocument()
    expect(screen.getByText('meeting:read:participant:admin')).toBeInTheDocument()
  })

  it('tells the admin how to set the secret and opens the guide when it is missing', async () => {
    setup({ status: makeStatus({ secret_configured: false, last_event_at: null, last_event_type: '' }) })
    expect(await screen.findByText('Секрет не настроен')).toBeInTheDocument()
    const text = screen.getByTestId('zoom-state-text')
    expect(within(text).getByText('ZOOM_WEBHOOK_SECRET_TOKEN=…')).toBeInTheDocument()
    expect(within(text).getByText('sudo systemctl restart zoom-checker')).toBeInTheDocument()
    expect(screen.getByText('ещё не было')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Как подключить Zoom/ })).toHaveAttribute('aria-expanded', 'true')
  })

  it('warns that Zoom needs https', async () => {
    setup({ status: makeStatus({ webhook_url: 'http://10.0.0.5:8080/api/webhooks/zoom' }) })
    expect(await screen.findByText('Zoom принимает только адреса https://')).toBeInTheDocument()
  })

  it('warns that Zoom cannot reach an address inside the local network', async () => {
    setup({ status: makeStatus({ webhook_url: 'https://192.168.1.10/api/webhooks/zoom' }) })
    expect(await screen.findByText('Zoom не сможет достучаться до этого адреса')).toBeInTheDocument()
    expect(screen.queryByText('Zoom принимает только адреса https://')).toBeNull()
  })

  it('does not warn about a public https address', async () => {
    setup()
    await screen.findByTestId('webhook-url')
    expect(screen.queryByText('Zoom не сможет достучаться до этого адреса')).toBeNull()
    expect(screen.queryByText('Zoom принимает только адреса https://')).toBeNull()
  })

  it('links admins to failed events', async () => {
    setup({ status: makeStatus({ failed_count: 3 }) })
    const link = await screen.findByRole('link', { name: /Посмотреть ошибки в журнале/ })
    expect(link).toHaveAttribute('href', '/admin/events?status=failed')
  })

  it('shows teachers a plain status without configuration details', async () => {
    setup({ me: makeUser(), status: makeStatus({ last_event_at: null }) })
    expect(await screen.findByText('Событий пока не было')).toBeInTheDocument()
    expect(screen.getByTestId('zoom-state-text')).toHaveTextContent('сообщите администратору')
    expect(screen.queryByTestId('webhook-url')).toBeNull()
    expect(screen.queryByRole('button', { name: /Как подключить Zoom/ })).toBeNull()
  })

  it('does not show teachers the secret wording', async () => {
    setup({ me: makeUser(), status: makeStatus({ secret_configured: false }) })
    expect(await screen.findByText('Подключение не настроено')).toBeInTheDocument()
    expect(screen.queryByText('Секрет не настроен')).toBeNull()
    expect(screen.queryByText(/ZOOM_WEBHOOK_SECRET_TOKEN/)).toBeNull()
  })

  it('calls an event older than a day stale', async () => {
    setup({ status: makeStatus({ last_event_at: minutesAgo(60 * 30) }) })
    expect(await screen.findByText('Событий не было больше суток')).toBeInTheDocument()
  })
})

describe('ZoomPage: unlinked sessions', () => {
  it('says so when every session is linked, asking the server for up to 500 sessions', async () => {
    const { fetchMock } = setup({ sessions: [makeSession({ lesson_id: 5, lesson: linked(5, 'Linux #1') })] })
    expect(await screen.findByText('Все конференции привязаны к занятиям')).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([u]) => String(u).startsWith('/api/zoom/sessions'))
    expect(String(call![0])).toBe('/api/zoom/sessions?unlinked=1&limit=500')
  })

  it('shows the linked lesson from the session itself, without a request per row', async () => {
    const sessions = Array.from({ length: 3 }, (_, i) =>
      makeSession({ id: 20 + i, meeting_uuid: `u${i}`, lesson_id: 30 + i, lesson: linked(30 + i, `Linux #${i + 1}`) }),
    )
    const { fetchMock } = setup({ sessions })
    const user = userEvent.setup()
    await screen.findByText('Все конференции привязаны к занятиям')
    await user.click(screen.getByRole('radio', { name: 'Все' }))
    const link = await screen.findByRole('link', { name: /Linux #2/ })
    expect(link).toHaveAttribute('href', '/lessons/31')
    expect(link).toHaveTextContent('DevOps-1')
    expect(screen.getAllByRole('link', { name: /Linux #/ })).toHaveLength(3)
    expect(fetchMock.mock.calls.some(([u]) => /^\/api\/lessons\/\d+/.test(String(u)))).toBe(false)
    expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/zoom/sessions?limit=500')).toBe(true)
  })

  it('says when the list was cut at the newest 500 sessions', async () => {
    const sessions = Array.from({ length: 500 }, (_, i) => makeSession({ id: 1000 + i, meeting_uuid: `m${i}` }))
    setup({ sessions })
    expect(
      await screen.findByText('Здесь только последние 500 конференций — более старые не показываются.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Показать ещё' })).toBeInTheDocument()
  })

  it('does not mention the cut for a shorter list', async () => {
    setup({ sessions: [makeSession()] })
    expect(await screen.findByText('123 456 7890')).toBeInTheDocument()
    expect(screen.queryByText(/только последние/)).toBeNull()
  })

  it('links a session to a lesson chosen in the dialog', async () => {
    const lessons = [
      makeLesson({ id: 11, title: 'Другая группа', zoom_meeting_id: '555666777', planned_start_at: '2026-10-06T08:00:00Z', group: { id: 2, name: 'Python-2' } }),
      makeLesson({ id: 12, title: 'Linux #4', planned_start_at: '2026-10-06T15:00:00Z' }),
    ]
    const { fetchMock } = setup({
      sessions: [makeSession()],
      extra: {
        'GET /api/lessons': lessons,
        'POST /api/zoom/sessions/7/link': { ok: true },
      },
    })
    const user = userEvent.setup()
    expect(await screen.findByText('123 456 7890')).toBeInTheDocument()
    expect(screen.getByText('Linux, занятие 4')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Привязать к занятию' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: 'Привязать к занятию' })).toBeInTheDocument()
    const radios = await within(dialog).findAllByRole('radio')
    // Same Zoom ID first, then other lessons of the day.
    expect(radios[0]).toHaveAccessibleName(/Linux #4/)
    expect(radios[1]).toHaveAccessibleName(/Другая группа/)
    expect(within(dialog).getByText('С тем же номером конференции')).toBeInTheDocument()
    expect(within(dialog).getByText('Другие занятия в этот день')).toBeInTheDocument()

    const submit = within(dialog).getByRole('button', { name: 'Привязать' })
    expect(submit).toBeDisabled()
    await user.click(radios[0])
    await user.click(submit)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const call = fetchMock.mock.calls.find(([u, init]) => String(u) === '/api/zoom/sessions/7/link' && init?.method === 'POST')
    expect(JSON.parse(String(call![1]!.body))).toEqual({ lesson_id: 12 })
    // Lesson list was asked for a range around the session date.
    const lessonsCall = fetchMock.mock.calls.find(([u]) => String(u).startsWith('/api/lessons?'))
    const params = new URL(String(lessonsCall![0]), 'http://x').searchParams
    expect(params.get('from')).toBeTruthy()
    expect(params.get('to')).toBeTruthy()
  })

  it('marks the lesson a session is linked to and does not offer it again', async () => {
    const lessons = [
      makeLesson({ id: 12, title: 'Linux #4', planned_start_at: '2026-10-06T15:00:00Z' }),
      makeLesson({ id: 13, title: 'Linux #5', planned_start_at: '2026-10-07T15:00:00Z' }),
    ]
    const { fetchMock } = setup({
      sessions: [makeSession({ lesson_id: 12, lesson: linked(12, 'Linux #4') })],
      extra: {
        'GET /api/lessons': lessons,
        'POST /api/zoom/sessions/7/link': { ok: true },
      },
    })
    const user = userEvent.setup()
    await screen.findByText('Все конференции привязаны к занятиям')
    await user.click(screen.getByRole('radio', { name: 'Все' }))
    await user.click(await screen.findByRole('button', { name: 'Другое занятие' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: 'Привязать к другому занятию' })).toBeInTheDocument()
    const current = await within(dialog).findByRole('radio', { name: /Linux #4/ })
    expect(current).toBeDisabled()
    expect(current).toHaveAccessibleName(/конференция сейчас привязана к нему/)
    await user.click(within(dialog).getByRole('radio', { name: /Linux #5/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Привязать' }))
    await waitFor(() => {
      const call = fetchMock.mock.calls.find(([u, init]) => String(u) === '/api/zoom/sessions/7/link' && init?.method === 'POST')
      expect(JSON.parse(String(call?.[1]?.body))).toEqual({ lesson_id: 13 })
    })
  })

  it('suggests creating a lesson when nothing fits', async () => {
    setup({ sessions: [makeSession()], extra: { 'GET /api/lessons': [] } })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Привязать к занятию' }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('Подходящих занятий нет')).toBeInTheDocument()
    expect(within(dialog).getByRole('link', { name: '«Занятия»' })).toHaveAttribute('href', '/')
  })

  it('unlinks a linked session after confirmation in "Все" mode', async () => {
    const { fetchMock } = setup({
      sessions: [makeSession({ lesson_id: 12, lesson: linked(12, 'Linux #4'), link_mode: 'auto' })],
      extra: {
        'POST /api/zoom/sessions/7/link': { ok: true },
      },
    })
    const user = userEvent.setup()
    await screen.findByText('Все конференции привязаны к занятиям')
    await user.click(screen.getByRole('radio', { name: 'Все' }))
    expect(await screen.findByRole('link', { name: /Linux #4/ })).toHaveAttribute('href', '/lessons/12')
    await user.click(screen.getByRole('button', { name: 'Отвязать' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Отвязать' }))
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([u, init]) => String(u) === '/api/zoom/sessions/7/link' && JSON.parse(String(init?.body)).lesson_id === null,
        ),
      ).toBe(true),
    )
  })
})

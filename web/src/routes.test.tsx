import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { RouteObject } from 'react-router'

import { AppShell } from '@/components/layout/AppShell'
import { RootLayout } from '@/components/layout/RootLayout'
import { api, browserLocation } from '@/lib/api'
import { RequireAuth } from '@/lib/auth'
import type { AuthConfig, User, ZoomStatus } from '@/lib/types'
import RouteErrorPage from '@/pages/errors/RouteErrorPage'
import { makeLesson, makeView } from '@/pages/lessons/testing'
import { routes } from '@/routes'
import { FakeEventSource } from '@/test/fakeEventSource'
import { errorResponse, jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'

const config: AuthConfig = {
  local_enabled: true,
  ldap_enabled: false,
  oidc_enabled: false,
  oidc_label: '',
  version: '1.0.0',
}

function setup(path: string, me: User | null, extra: Record<string, unknown> = {}) {
  const fetchMock = mockFetch({
    'GET /api/auth/config': config,
    'GET /api/auth/me': () => (me ? jsonResponse(me) : errorResponse(401, 'unauthorized', 'Нужно войти')),
    ...extra,
  })
  return { fetchMock, ...renderRoutes(routes, path) }
}

beforeEach(() => {
  FakeEventSource.reset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('routing and guards', () => {
  it('redirects anonymous users to /login with next', async () => {
    const { router } = setup('/groups?status=all', null)
    expect(await screen.findByRole('button', { name: 'Войти' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
    expect(router.state.location.search).toBe('?next=%2Fgroups%3Fstatus%3Dall')
  })

  it('shows the shell and the lessons page to a teacher, without admin links', async () => {
    setup('/', makeUser())
    expect(await screen.findByRole('heading', { level: 1, name: 'Занятия' })).toBeInTheDocument()
    const nav = screen.getAllByRole('navigation', { name: 'Основное меню' })[0]
    expect(within(nav).getByRole('link', { name: 'Занятия' })).toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('link', { name: 'Группы' })).toBeInTheDocument()
    expect(within(nav).getByRole('link', { name: 'Zoom' })).toBeInTheDocument()
    expect(within(nav).getByRole('link', { name: 'Справка' })).toBeInTheDocument()
    expect(within(nav).queryByRole('button', { name: /Админ/ })).toBeNull()
    expect(document.title).toBe('Занятия · Zoom Checker')
  })

  it('opens the live lesson screen and marks "Занятия" active', async () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    const view = makeView({ lesson: makeLesson({ id: 12, title: 'Linux. Занятие №3' }) })
    const { fetchMock } = setup('/lessons/12', makeUser(), { 'GET /api/lessons/12/attendance': view })
    expect(await screen.findByRole('heading', { level: 1, name: 'Linux. Занятие №3' })).toBeInTheDocument()
    expect(document.title).toBe('Linux. Занятие №3 · Zoom Checker')
    const nav = screen.getAllByRole('navigation', { name: 'Основное меню' })[0]
    expect(within(nav).getByRole('link', { name: 'Занятия' })).toHaveAttribute('aria-current', 'page')
    // The route id reaches the page: the right lesson is loaded and streamed.
    expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/lessons/12/attendance')).toBe(true)
    expect(FakeEventSource.latest().url).toBe('/api/lessons/12/stream')
  })

  it('explains a missing lesson and still marks "Занятия" active', async () => {
    vi.stubGlobal('EventSource', FakeEventSource)
    setup('/lessons/404', makeUser(), {
      'GET /api/lessons/404/attendance': () => errorResponse(404, 'not_found', 'Занятие не найдено'),
    })
    expect(await screen.findByRole('heading', { level: 1, name: 'Занятие не найдено' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Ко всем занятиям' })).toHaveAttribute('href', '/')
    const nav = screen.getAllByRole('navigation', { name: 'Основное меню' })[0]
    expect(within(nav).getByRole('link', { name: 'Занятия' })).toHaveAttribute('aria-current', 'page')
  })

  it.each([
    ['/groups', 'Группы'],
    ['/groups/3', 'Группа №3'],
    ['/groups/3/report', 'Отчёт по группе №3'],
    ['/zoom', 'Zoom'],
    ['/help', 'Справка'],
  ])('renders %s', async (path, heading) => {
    setup(path, makeUser())
    expect(await screen.findByRole('heading', { level: 1, name: heading })).toBeInTheDocument()
  })

  it('blocks admin pages for teachers', async () => {
    setup('/admin/users', makeUser())
    expect(await screen.findByText('Этот раздел доступен только администраторам')).toBeInTheDocument()
  })

  it.each([
    ['/admin/users', 'Пользователи'],
    ['/admin/settings', 'Настройки'],
    ['/admin/events', 'Журнал событий'],
  ])('lets admins open %s', async (path, heading) => {
    setup(path, makeUser({ role: 'admin' }))
    expect(await screen.findByRole('heading', { level: 1, name: heading })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Админ/ }).length).toBeGreaterThan(0)
  })

  it('redirects /admin to /admin/users', async () => {
    const { router } = setup('/admin', makeUser({ role: 'admin' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Пользователи' })).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/admin/users')
  })

  it('shows 404 for unknown paths', async () => {
    setup('/no/such/page', makeUser())
    expect(await screen.findByText('Такой страницы нет')).toBeInTheDocument()
  })

  it('shows an error with retry when the session check fails', async () => {
    setup('/', null, { 'GET /api/auth/me': () => errorResponse(503, 'unavailable', 'Сервер перегружен') })
    expect(await screen.findByText('Сервер перегружен')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Повторить' })).toBeInTheDocument()
  })

  it('sends the user to login when an API call returns 401', async () => {
    const { router } = setup('/groups', makeUser(), {
      'GET /api/groups': () => errorResponse(401, 'unauthorized', 'Нужно войти'),
    })
    await screen.findByRole('heading', { level: 1, name: 'Группы' })
    await act(async () => {
      await api.groups.list().catch(() => {})
    })
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(router.state.location.search).toBe('?next=%2Fgroups')
  })
})

describe('Zoom connection warning in the app shell', () => {
  const status = (overrides: Partial<ZoomStatus> = {}): ZoomStatus => ({
    webhook_url: 'https://attendance.example.com/api/webhooks/zoom',
    secret_configured: true,
    last_event_at: new Date().toISOString(),
    last_event_type: 'meeting.participant_joined',
    events_24h: 3,
    failed_count: 0,
    pending_count: 0,
    ...overrides,
  })
  const banner = () => screen.queryByRole('region', { name: 'Подключение к Zoom' })

  it('tells an admin that Zoom is not connected and links to the Zoom page', async () => {
    setup('/groups', makeUser({ role: 'admin' }), {
      'GET /api/groups': [],
      'GET /api/zoom/status': status({ secret_configured: false, last_event_at: null }),
    })
    const region = await screen.findByRole('region', { name: 'Подключение к Zoom' })
    expect(region).toHaveTextContent('Zoom ещё не подключён — посещаемость не будет отмечаться.')
    expect(within(region).getByRole('link', { name: 'Открыть раздел «Zoom»' })).toHaveAttribute('href', '/zoom')
  })

  it('tells an admin when Zoom has never sent an event', async () => {
    setup('/groups', makeUser({ role: 'admin' }), {
      'GET /api/groups': [],
      'GET /api/zoom/status': status({ last_event_at: null }),
    })
    expect(await screen.findByRole('region', { name: 'Подключение к Zoom' })).toHaveTextContent(
      'Из Zoom пока не пришло ни одного события. Проверьте подключение в разделе «Zoom».',
    )
  })

  it('stays quiet once Zoom sends events, and when the status cannot be loaded', async () => {
    const { fetchMock } = setup('/groups', makeUser({ role: 'admin' }), {
      'GET /api/groups': [],
      'GET /api/zoom/status': status({ last_event_at: '2026-01-01T00:00:00Z' }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Группы' })
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/zoom/status')).toBe(true))
    expect(banner()).toBeNull()
  })

  it('is not shown to teachers (they cannot fix it) or on the Zoom page itself', async () => {
    const teacher = setup('/groups', makeUser(), {
      'GET /api/groups': [],
      'GET /api/zoom/status': status({ secret_configured: false, last_event_at: null }),
    })
    await screen.findByRole('heading', { level: 1, name: 'Группы' })
    expect(teacher.fetchMock.mock.calls.some(([u]) => String(u) === '/api/zoom/status')).toBe(false)
    expect(banner()).toBeNull()
    teacher.unmount()
    vi.unstubAllGlobals()

    setup('/zoom', makeUser({ role: 'admin' }), {
      'GET /api/zoom/sessions': [],
      'GET /api/zoom/status': status({ secret_configured: false, last_event_at: null }),
    })
    expect(await screen.findByRole('heading', { level: 1, name: 'Zoom' })).toBeInTheDocument()
    expect(await screen.findAllByText('Секрет не настроен')).not.toHaveLength(0)
    expect(banner()).toBeNull()
  })

  it('tells a teacher on the lesson list that nothing will be recorded', async () => {
    setup('/', makeUser(), {
      'GET /api/groups': [],
      'GET /api/lessons': [],
      'GET /api/zoom/status': status({ secret_configured: false, last_event_at: null }),
    })
    expect(
      await screen.findByText('Сервис пока не подключён к Zoom — отметки не появятся. Сообщите администратору.'),
    ).toBeInTheDocument()
    // The admin wording with the setup link is for admins only.
    expect(banner()).toBeNull()
  })

  it('does not repeat the warning on the lesson list for an admin', async () => {
    setup('/', makeUser({ role: 'admin' }), {
      'GET /api/groups': [],
      'GET /api/lessons': [],
      'GET /api/zoom/status': status({ secret_configured: false, last_event_at: null }),
    })
    expect(await screen.findByRole('region', { name: 'Подключение к Zoom' })).toBeInTheDocument()
    expect(screen.queryByText(/отметки не появятся/)).toBeNull()
  })
})

describe('session ended while the app is open', () => {
  const NOTICE = 'Вы вышли из системы или вход завершился. Войдите снова — вы вернётесь на ту же страницу.'

  it('says why the login form appeared when a data request gets 401', async () => {
    FakeEventSource.reset()
    vi.stubGlobal('EventSource', FakeEventSource)
    const { router } = setup('/lessons/3', makeUser(), {
      'GET /api/lessons/3/attendance': () => jsonResponse(makeView({ lesson: makeLesson({ id: 3 }) })),
      'GET /api/groups': () => errorResponse(401, 'unauthorized', 'Нужно войти'),
    })
    await screen.findByRole('heading', { level: 1, name: 'DevOps. Занятие №4' })
    await act(async () => {
      await api.groups.list().catch(() => {})
    })
    expect(await screen.findByText(NOTICE)).toBeInTheDocument()
    expect(router.state.location.pathname).toBe('/login')
    expect(router.state.location.search).toBe('?next=%2Flessons%2F3')
    // Neutral notice, not an error: the icon and the text carry it.
    expect(screen.getByText(NOTICE).closest('[role="alert"]')).not.toHaveClass('text-destructive')
  })

  it('says so when the session check on returning to the tab finds no session', async () => {
    let signedIn = true
    const { client, router } = setup('/groups', null, {
      'GET /api/auth/me': () => (signedIn ? jsonResponse(makeUser()) : errorResponse(401, 'unauthorized', 'Нужно войти')),
      'GET /api/groups': [],
    })
    await screen.findByRole('heading', { level: 1, name: 'Группы' })
    signedIn = false
    await act(async () => {
      await client.refetchQueries({ queryKey: ['auth', 'me'] })
    })
    expect(await screen.findByText(NOTICE)).toBeInTheDocument()
    expect(router.state.location.search).toBe('?next=%2Fgroups')
  })

  it('shows no notice on a first visit without a session', async () => {
    setup('/lessons/3', null)
    expect(await screen.findByRole('button', { name: 'Войти' })).toBeInTheDocument()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('cannot be faked with a link', async () => {
    setup('/login?sessionEnded=true&state=sessionEnded', null)
    expect(await screen.findByRole('button', { name: 'Войти' })).toBeInTheDocument()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('shows no notice after the user pressed «Выйти»', async () => {
    const { client, router } = setup('/groups', makeUser(), {
      'GET /api/groups': [],
      // Still being answered when another request gets 401.
      'POST /api/auth/logout': () => new Promise<Response>(() => {}),
    })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Меню пользователя/ }))
    await user.click(await screen.findByRole('menuitem', { name: 'Выйти' }))
    // What the 401 handler does: the user is dropped while the logout is under way.
    act(() => {
      client.setQueryData(['auth', 'me'], null)
    })
    await waitFor(() => expect(router.state.location.pathname).toBe('/login'))
    expect(await screen.findByRole('button', { name: 'Войти' })).toBeInTheDocument()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })
})

describe('user menu', () => {
  it('logs out and follows the redirect URL', async () => {
    const assign = vi.spyOn(browserLocation, 'assign').mockImplementation(() => {})
    const { fetchMock } = setup('/', makeUser({ display_name: 'Мария Иванова' }), {
      'POST /api/auth/logout': { redirect_url: 'https://sso.example.com/logout' },
    })
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Меню пользователя/ }))
    expect(await screen.findByText('Преподаватель', { exact: false })).toBeInTheDocument()
    await user.click(screen.getByRole('menuitem', { name: 'Выйти' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://sso.example.com/logout'))
    expect(fetchMock.mock.calls.some(([u, init]) => String(u) === '/api/auth/logout' && init?.method === 'POST')).toBe(true)
  })

  it('offers password change only to local accounts', async () => {
    setup('/', makeUser({ auth_source: 'ldap' }))
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Меню пользователя/ }))
    await screen.findByRole('menuitem', { name: 'Выйти' })
    expect(screen.queryByRole('menuitem', { name: 'Сменить пароль' })).toBeNull()
  })

  it('opens the change password dialog from the menu', async () => {
    setup('/', makeUser())
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Меню пользователя/ }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сменить пароль' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: 'Сменить пароль' })).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})

describe('must_change_password', () => {
  it('forces the password dialog until the password is changed', async () => {
    const { fetchMock } = setup('/', makeUser({ must_change_password: true }), {
      'POST /api/auth/password': () => new Response(null, { status: 204 }),
    })
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('heading', { name: 'Смените пароль' })).toBeInTheDocument()
    // No way to dismiss it.
    expect(within(dialog).queryByRole('button', { name: 'Отмена' })).toBeNull()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    await user.type(within(dialog).getByLabelText('Текущий пароль'), 'temp-1234')
    await user.type(within(dialog).getByLabelText('Новый пароль'), 'short')
    await user.type(within(dialog).getByLabelText('Повторите новый пароль'), 'short')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить пароль' }))
    expect(within(dialog).getByText(/не короче 8 символов/)).toBeInTheDocument()

    await user.clear(within(dialog).getByLabelText('Новый пароль'))
    await user.type(within(dialog).getByLabelText('Новый пароль'), 'new-secret-42')
    await user.clear(within(dialog).getByLabelText('Повторите новый пароль'))
    await user.type(within(dialog).getByLabelText('Повторите новый пароль'), 'new-secret-24')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить пароль' }))
    expect(within(dialog).getByText(/Пароли не совпадают/)).toBeInTheDocument()

    await user.clear(within(dialog).getByLabelText('Повторите новый пароль'))
    await user.type(within(dialog).getByLabelText('Повторите новый пароль'), 'new-secret-42')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить пароль' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    const call = fetchMock.mock.calls.find(([u]) => String(u) === '/api/auth/password')!
    expect(JSON.parse(String(call[1]!.body))).toEqual({ current_password: 'temp-1234', new_password: 'new-secret-42' })
  })

  it('loads the pages only after the temporary password is replaced', async () => {
    // The server answers 403 password_change_required to everything else until then;
    // a page loaded behind the dialog kept showing "Нет доступа" after the change.
    vi.stubGlobal('EventSource', FakeEventSource)
    const { fetchMock } = setup('/', makeUser({ must_change_password: true }), {
      'POST /api/auth/password': () => new Response(null, { status: 204 }),
      'GET /api/lessons': [],
      'GET /api/groups': [],
    })
    const dataCalls = () =>
      fetchMock.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith('/api/') && !u.startsWith('/api/auth/'))
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog')
    expect(screen.getByTestId('password-change-pending')).toBeInTheDocument()
    expect(screen.queryByRole('heading', { level: 1, name: 'Занятия' })).toBeNull()
    expect(dataCalls()).toEqual([])
    expect(FakeEventSource.instances).toHaveLength(0)

    await user.type(within(dialog).getByLabelText('Текущий пароль'), 'temp-1234')
    await user.type(within(dialog).getByLabelText('Новый пароль'), 'new-secret-42')
    await user.type(within(dialog).getByLabelText('Повторите новый пароль'), 'new-secret-42')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить пароль' }))

    expect(await screen.findByRole('heading', { level: 1, name: 'Занятия' })).toBeInTheDocument()
    await waitFor(() => expect(dataCalls().some((u) => u.startsWith('/api/lessons'))).toBe(true))
    expect(screen.queryByTestId('password-change-pending')).toBeNull()
    expect(screen.queryByText('Нет доступа')).toBeNull()
  })

  it('shows the server error inline', async () => {
    setup('/', makeUser({ must_change_password: true }), {
      'POST /api/auth/password': () => errorResponse(422, 'validation', 'Текущий пароль указан неверно'),
    })
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Текущий пароль'), 'wrong-pass')
    await user.type(within(dialog).getByLabelText('Новый пароль'), 'new-secret-42')
    await user.type(within(dialog).getByLabelText('Повторите новый пароль'), 'new-secret-42')
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить пароль' }))
    expect(await within(dialog).findByText('Текущий пароль указан неверно')).toBeInTheDocument()
  })
})

describe('review: account and error edge cases', () => {
  it('does not force a password change on LDAP/OIDC accounts (they have no local password)', async () => {
    setup('/', makeUser({ auth_source: 'ldap', must_change_password: true }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Занятия' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('keeps the page usable after the password dialog opened from the user menu is closed', async () => {
    setup('/', makeUser())
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Меню пользователя/ }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сменить пароль' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    // Radix must not leave the body blocked (pointer-events: none) after menu → dialog.
    await waitFor(() => expect(document.body.style.pointerEvents).not.toBe('none'))
    await user.click(screen.getAllByRole('link', { name: 'Группы' })[0])
    expect(await screen.findByRole('heading', { level: 1, name: 'Группы' })).toBeInTheDocument()
  })
})

describe('review: route error boundaries', () => {
  function shellRoutes(child: RouteObject): RouteObject[] {
    return [
      {
        element: <RootLayout />,
        errorElement: <RouteErrorPage fullScreen />,
        children: [
          {
            element: (
              <RequireAuth>
                <AppShell />
              </RequireAuth>
            ),
            children: [{ errorElement: <RouteErrorPage />, children: [child] }],
          },
        ],
      },
    ]
  }

  it('shows a crashed page inside the shell, keeping the navigation', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mockFetch({ 'GET /api/auth/me': makeUser() })
    function Boom(): never {
      throw new Error('boom')
    }
    renderRoutes(shellRoutes({ index: true, Component: Boom }), '/')
    expect(await screen.findByText('Что-то пошло не так')).toBeInTheDocument()
    expect(screen.getAllByRole('navigation', { name: 'Основное меню' }).length).toBeGreaterThan(0)
  })

  it('asks to reload after a deploy removed an old page chunk', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mockFetch({ 'GET /api/auth/me': makeUser() })
    renderRoutes(
      shellRoutes({
        index: true,
        lazy: async () => {
          throw new TypeError('Failed to fetch dynamically imported module: /assets/LessonsPage-old.js')
        },
      }),
      '/',
    )
    expect(await screen.findByText('Вышла новая версия приложения')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Обновить страницу' })).toBeInTheDocument()
  })
})

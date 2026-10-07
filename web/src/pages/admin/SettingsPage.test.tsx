import { act, configure, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import { addNames, isDirty, nameKey, rebaseDraft, splitNames, toDraft, validateSettings } from '@/components/admin/settings'
import { formatUptime, tlsModeLabel } from '@/components/admin/system'
import type { Settings, SystemInfo } from '@/lib/types'
import { routes } from '@/routes'
import { jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'

// Pages are lazy routes: warm the module so the first findBy* does not race a cold
// transform on a busy machine, and allow slow CI runners more time.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })
beforeAll(async () => {
  await import('@/pages/admin/SettingsPage')
})

const saved: Settings = {
  ignored_names: ['Zoom Recorder'],
  present_min_percent: 0,
  link_window_hours: 6,
  ignore_host: true,
}

const system: SystemInfo = {
  version: '1.4.0',
  commit: 'abcdef1234567',
  db_driver: 'postgres',
  tls_mode: 'auto',
  public_url: 'https://attendance.example.com',
  data_dir: '/var/lib/zoom-checker',
  timezone: 'Europe/Moscow',
  uptime_seconds: 3 * 86_400 + 4 * 3600 + 120,
  auth: {
    local: { enabled: true, detail: '' },
    ldap: { enabled: true, detail: 'ldaps://dc1.corp.local:636' },
    oidc: { enabled: false, detail: '' },
  },
}

afterEach(() => {
  vi.unstubAllGlobals()
})

function setup(extra: Record<string, unknown> = {}) {
  let current = saved
  /** Changes the settings on the "server" behind the page's back. */
  const serverChange = (next: Partial<Settings>) => {
    current = { ...current, ...next }
  }
  const fetchMock = mockFetch({
    'GET /api/auth/me': makeUser({ role: 'admin' }),
    'GET /api/settings': () => jsonResponse(current),
    'GET /api/system': system,
    'PUT /api/settings': (_u: URL, init: RequestInit) => {
      current = JSON.parse(String(init.body))
      return jsonResponse(current)
    },
    ...extra,
  })
  return { fetchMock, serverChange, ...renderRoutes(routes, '/admin/settings') }
}

const putBody = (fetchMock: ReturnType<typeof mockFetch>) => {
  const call = fetchMock.mock.calls.find(([u, init]) => String(u) === '/api/settings' && init?.method === 'PUT')
  return call ? JSON.parse(String(call[1]!.body)) : undefined
}

describe('settings helpers', () => {
  it('validates numbers', () => {
    const base = toDraft(saved)
    expect(validateSettings(base)).toEqual({ errors: {}, value: saved })
    expect(validateSettings({ ...base, present_min_percent: '101' }).errors.present_min_percent).toBe(
      'Введите целое число от 0 до 100.',
    )
    expect(validateSettings({ ...base, present_min_percent: '' }).value).toBeNull()
    expect(validateSettings({ ...base, present_min_percent: '50.5' }).value).toBeNull()
    expect(validateSettings({ ...base, link_window_hours: '0' }).errors.link_window_hours).toMatch(/от 1 до 72/)
    expect(validateSettings({ ...base, link_window_hours: '73' }).value).toBeNull()
    expect(validateSettings({ ...base, present_min_percent: ' 75 ', link_window_hours: '12' }).value).toEqual({
      ...saved,
      present_min_percent: 75,
      link_window_hours: 12,
    })
  })

  it('adds names without duplicates (case, spaces and ё ignored)', () => {
    expect(nameKey('  Фёдоров   ПЁТР ')).toBe('федоров петр')
    expect(addNames(['Преподаватель'], ['  преподаватель', 'Otter.ai', 'otter.AI', ' ', 'Zoom   Recorder'])).toEqual([
      'Преподаватель',
      'Otter.ai',
      'Zoom Recorder',
    ])
    expect(splitNames('Ассистент\nOtter.ai; Гость ,')).toEqual(['Ассистент', 'Otter.ai', 'Гость'])
  })

  it('moves unsaved edits onto newer server settings', () => {
    const base: Settings = { ignored_names: ['Zoom Recorder', 'Гость'], present_min_percent: 0, link_window_hours: 6, ignore_host: true }
    // Someone hid "Ассистент" from a lesson screen and switched the host flag.
    const latest: Settings = { ...base, ignored_names: ['Zoom Recorder', 'Гость', 'Ассистент'], ignore_host: false }
    // The admin removed "Гость", added "Otter.ai" and typed 50 %.
    const draft = { ...toDraft(base), ignored_names: ['Zoom Recorder', 'Otter.ai'], present_min_percent: '50' }
    expect(rebaseDraft(draft, base, latest)).toEqual({
      ignored_names: ['Zoom Recorder', 'Ассистент', 'Otter.ai'],
      present_min_percent: '50',
      link_window_hours: '6',
      ignore_host: false,
    })
    // An untouched form simply shows the newer settings.
    expect(rebaseDraft(toDraft(base), base, latest)).toEqual(toDraft(latest))
    // A name added on both sides (case / ё differences) is kept once.
    expect(
      rebaseDraft({ ...toDraft(base), ignored_names: [...base.ignored_names, 'ассистент'] }, base, latest).ignored_names,
    ).toEqual(['Zoom Recorder', 'Гость', 'Ассистент'])
  })

  it('tracks unsaved changes', () => {
    expect(isDirty(toDraft(saved), saved)).toBe(false)
    expect(isDirty({ ...toDraft(saved), ignore_host: false }, saved)).toBe(true)
    expect(isDirty({ ...toDraft(saved), ignored_names: [] }, saved)).toBe(true)
  })

  it('describes the system in plain words', () => {
    expect(formatUptime(3 * 86_400 + 4 * 3600)).toBe('3 дн 4 ч')
    expect(formatUptime(4 * 3600 + 12 * 60)).toBe('4 ч 12 мин')
    expect(formatUptime(30)).toBe('меньше минуты')
    expect(tlsModeLabel('auto')).toMatch(/Let’s Encrypt/)
  })
})

describe('SettingsPage', () => {
  it('edits the rules and saves exactly what the admin entered', async () => {
    const { fetchMock } = setup()
    const user = userEvent.setup()
    const save = await screen.findByRole('button', { name: 'Сохранить' })
    expect(save).toBeDisabled()
    expect(screen.getByText('Zoom Recorder')).toBeInTheDocument()

    // Tag editor: Enter adds, a duplicate is refused, examples add with one click, × removes.
    const names = screen.getByLabelText('Имена, которые не считаются студентами')
    await user.type(names, 'Ассистент{Enter}')
    expect(screen.getByText('Ассистент')).toBeInTheDocument()
    await user.type(names, 'zoom recorder{Enter}')
    expect(screen.getByText('Это имя уже есть в списке.')).toBeInTheDocument()
    await user.clear(names)
    await user.click(screen.getByRole('button', { name: 'Добавить «Otter.ai»' }))
    expect(screen.queryByRole('button', { name: 'Добавить «Otter.ai»' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Убрать «Zoom Recorder»' }))

    const percent = screen.getByLabelText(/Засчитывать присутствие/)
    await user.clear(percent)
    await user.type(percent, '50')
    const window = screen.getByLabelText('Окно привязки конференции к занятию, часов')
    await user.clear(window)
    await user.type(window, '8')
    await user.click(screen.getByRole('switch', { name: 'Не учитывать организатора конференции' }))

    expect(save).toBeEnabled()
    await user.click(save)
    await waitFor(() =>
      expect(putBody(fetchMock)).toEqual({
        ignored_names: ['Ассистент', 'Otter.ai'],
        present_min_percent: 50,
        link_window_hours: 8,
        ignore_host: false,
      }),
    )
    // Saved: nothing left to save.
    await waitFor(() => expect(save).toBeDisabled())
    expect(screen.getByLabelText(/Засчитывать присутствие/)).toHaveValue(50)
  })

  it('does not undo a name hidden elsewhere while the admin edits the form', async () => {
    const { fetchMock, serverChange, client } = setup()
    const user = userEvent.setup()
    const percent = await screen.findByLabelText(/Засчитывать присутствие/)
    await user.clear(percent)
    await user.type(percent, '40')

    // A teacher pressed "Всегда скрывать это имя" on a lesson; the page refetches (e.g. on focus).
    serverChange({ ignored_names: ['Zoom Recorder', 'Ассистент'] })
    await act(() => client.invalidateQueries({ queryKey: ['admin', 'settings'] }))
    expect(await screen.findByText('Ассистент')).toBeInTheDocument()
    expect(percent).toHaveValue(40)

    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() =>
      expect(putBody(fetchMock)).toEqual({
        ignored_names: ['Zoom Recorder', 'Ассистент'],
        present_min_percent: 40,
        link_window_hours: 6,
        ignore_host: true,
      }),
    )
  })

  it('shows newer settings in an untouched form without marking it changed', async () => {
    const { serverChange, client } = setup()
    const save = await screen.findByRole('button', { name: 'Сохранить' })
    serverChange({ present_min_percent: 30 })
    await act(() => client.invalidateQueries({ queryKey: ['admin', 'settings'] }))
    await waitFor(() => expect(screen.getByLabelText(/Засчитывать присутствие/)).toHaveValue(30))
    expect(save).toBeDisabled()
  })

  it('adds a typed name when the admin leaves the field without pressing Enter', async () => {
    const { fetchMock } = setup()
    const user = userEvent.setup()
    const names = await screen.findByLabelText('Имена, которые не считаются студентами')
    await user.type(names, 'Ассистент')
    await user.tab()
    expect(screen.getByText('Ассистент')).toBeInTheDocument()
    expect(names).toHaveValue('')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(putBody(fetchMock)?.ignored_names).toEqual(['Zoom Recorder', 'Ассистент']))
  })

  it('shows validation errors and does not save', async () => {
    const { fetchMock } = setup()
    const user = userEvent.setup()
    const percent = await screen.findByLabelText(/Засчитывать присутствие/)
    await user.clear(percent)
    await user.type(percent, '150')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(screen.getByText('Введите целое число от 0 до 100.')).toBeInTheDocument()
    expect(percent).toHaveAttribute('aria-invalid', 'true')
    expect(percent).toHaveFocus()
    expect(putBody(fetchMock)).toBeUndefined()

    // "Отменить" brings back the saved values.
    await user.click(screen.getByRole('button', { name: 'Отменить' }))
    expect(percent).toHaveValue(0)
    expect(screen.queryByText('Введите целое число от 0 до 100.')).toBeNull()
  })

  it('shows read-only system facts', async () => {
    setup()
    const card = (await screen.findByText('Версия')).closest('dl')!
    expect(within(card).getByText('1.4.0')).toBeInTheDocument()
    expect(within(card).getByText('(abcdef1)')).toBeInTheDocument()
    expect(within(card).getByText('PostgreSQL')).toBeInTheDocument()
    expect(within(card).getByText('Europe/Moscow')).toBeInTheDocument()
    expect(within(card).getByText('3 дн 4 ч')).toBeInTheDocument()
    expect(within(card).getByText('ldaps://dc1.corp.local:636')).toBeInTheDocument()
  })

  it('explains why LDAP or Keycloak is off, in the words of the server', async () => {
    setup({
      'GET /api/system': {
        ...system,
        auth: {
          ...system.auth,
          ldap: {
            enabled: false,
            detail: 'Настроен, но не запущен из-за ошибки в настройках — подробности в журнале сервера',
          },
          oidc: { enabled: false, detail: 'Не настроен (OIDC_ISSUER_URL не задан)' },
        },
      },
    })
    const card = (await screen.findByText('Версия')).closest('dl')!
    expect(within(card).getByText('Не настроен (OIDC_ISSUER_URL не задан)')).toBeInTheDocument()
    expect(within(card).getByText(/Настроен, но не запущен из-за ошибки/)).toBeInTheDocument()
    // The LDAP check card does not claim "not configured" when it is configured with an error.
    expect(screen.getByText('Вход через LDAP сейчас не работает')).toBeInTheDocument()
    expect(
      screen.getByText('Настроен, но не запущен из-за ошибки в настройках — подробности в журнале сервера.'),
    ).toBeInTheDocument()
  })

  it('runs the LDAP check and shows each step', async () => {
    const { fetchMock } = setup({
      'POST /api/system/test-ldap': {
        ok: false,
        steps: [
          { name: 'Подключение к серверу', ok: true, detail: 'ldaps://dc1.corp.local:636 отвечает' },
          { name: 'Вход служебной учётной записи', ok: false, detail: 'Неверный пароль служебной учётной записи' },
        ],
      },
    })
    const user = userEvent.setup()
    const login = await screen.findByLabelText('Логин сотрудника (необязательно)')
    const password = screen.getByLabelText('Пароль (необязательно)')
    expect(password).toBeDisabled()
    await user.type(login, 'ivanova')
    await user.type(password, 'pa55word')
    await user.click(screen.getByRole('button', { name: 'Проверить' }))

    const steps = await screen.findByRole('list', { name: 'Шаги проверки' })
    expect(within(steps).getAllByRole('listitem')).toHaveLength(2)
    expect(within(steps).getByText('Успешно:')).toBeInTheDocument()
    expect(within(steps).getByText('Неверный пароль служебной учётной записи')).toBeInTheDocument()
    expect(screen.getByText('Есть проблема')).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([u]) => String(u) === '/api/system/test-ldap')
    expect(JSON.parse(String(call![1]!.body))).toEqual({ username: 'ivanova', password: 'pa55word' })
    // The password is cleared after the check.
    expect(password).toHaveValue('')
  })
})

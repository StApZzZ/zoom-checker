import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  loginErrorMessage,
  parseRequiredRoles,
  publicAddress,
  publicLoginUrl,
  requiredRolesHint,
} from '@/pages/auth/loginErrors'
import type { AuthConfig } from '@/lib/types'
import { routes } from '@/routes'
import { errorResponse, jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'

const base: AuthConfig = {
  local_enabled: true,
  ldap_enabled: false,
  oidc_enabled: false,
  oidc_label: '',
  version: '1.2.3',
}

function setup(path: string, config: Partial<AuthConfig> = {}, extra: Record<string, unknown> = {}) {
  let loggedIn = false
  const user = makeUser()
  const fetchMock = mockFetch({
    'GET /api/auth/config': { ...base, ...config },
    'GET /api/auth/me': () => (loggedIn ? jsonResponse(user) : errorResponse(401, 'unauthorized', 'Нужно войти')),
    'POST /api/auth/login': (_u: URL, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as { username: string; password: string }
      if (body.password !== 'secret') return errorResponse(401, 'unauthorized', 'Неверный логин или пароль')
      loggedIn = true
      return jsonResponse({ user })
    },
    ...extra,
  })
  return { fetchMock, ...renderRoutes(routes, path) }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('LoginPage', () => {
  it('shows the username/password form for local accounts', async () => {
    setup('/login')
    expect(await screen.findByLabelText('Логин')).toBeInTheDocument()
    expect(screen.getByLabelText('Пароль')).toHaveAttribute('type', 'password')
    expect(screen.getByRole('button', { name: 'Войти' })).toBeInTheDocument()
    expect(screen.queryByText('Используйте корпоративную учётную запись')).toBeNull()
    expect(screen.getByText('Автоматический учёт посещаемости занятий в Zoom')).toBeInTheDocument()
    expect(screen.getByText('Версия 1.2.3')).toBeInTheDocument()
    expect(document.title).toBe('Вход · Zoom Checker')
  })

  it('shows the corporate account hint when LDAP is enabled', async () => {
    setup('/login', { local_enabled: false, ldap_enabled: true })
    expect(await screen.findByText('Используйте корпоративную учётную запись')).toBeInTheDocument()
    expect(screen.getByLabelText('Логин')).toBeInTheDocument()
  })

  it('shows only the OIDC button when only OIDC is enabled', async () => {
    setup('/login?next=%2Flessons%2F5', { local_enabled: false, oidc_enabled: true, oidc_label: 'Войти через СТАНКИН ID' })
    const link = await screen.findByRole('link', { name: 'Войти через СТАНКИН ID' })
    expect(link).toHaveAttribute('href', '/auth/oidc/login?next=%2Flessons%2F5')
    expect(screen.queryByLabelText('Логин')).toBeNull()
  })

  it('shows both the form and the OIDC button with a default label', async () => {
    setup('/login', { oidc_enabled: true, oidc_label: '  ' })
    const link = await screen.findByRole('link', { name: 'Войти через Keycloak' })
    expect(link).toHaveAttribute('href', '/auth/oidc/login')
    expect(screen.getByLabelText('Логин')).toBeInTheDocument()
    expect(screen.getByText('или')).toBeInTheDocument()
  })

  it('explains when no login method is enabled', async () => {
    setup('/login', { local_enabled: false })
    expect(await screen.findByText('Вход пока не настроен')).toBeInTheDocument()
  })

  it.each([
    ['access_denied', 'У вас нет доступа к системе. Обратитесь к администратору.'],
    ['oidc_failed', 'Не удалось войти через Keycloak. Попробуйте ещё раз.'],
    ['something_else', 'Не удалось войти. Попробуйте ещё раз.'],
  ])('maps ?error=%s to a friendly message', async (code, message) => {
    setup(`/login?error=${code}`)
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(loginErrorMessage(code)).toBe(message)
  })

  it('logs in and goes to the requested page', async () => {
    const { router, fetchMock } = setup('/login?next=%2Fgroups')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Логин'), '  ivanova ')
    await user.type(screen.getByLabelText('Пароль'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Войти' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/groups'))
    expect(await screen.findByRole('heading', { level: 1, name: 'Группы' })).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([u]) => String(u) === '/api/auth/login')!
    expect(JSON.parse(String(call[1]!.body))).toEqual({ username: 'ivanova', password: 'secret' })
  })

  it('never redirects outside the app after login', async () => {
    const { router } = setup('/login?next=%2F%2Fevil.example')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Логин'), 'ivanova')
    await user.type(screen.getByLabelText('Пароль'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Войти' }))
    await waitFor(() => expect(router.state.location.pathname).toBe('/'))
  })

  it('shows the server message for wrong credentials and clears the password', async () => {
    setup('/login')
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Логин'), 'ivanova')
    await user.type(screen.getByLabelText('Пароль'), 'wrong')
    await user.click(screen.getByRole('button', { name: 'Войти' }))
    expect(await screen.findByText('Неверный логин или пароль')).toBeInTheDocument()
    expect(screen.getByLabelText('Пароль')).toHaveValue('')
  })

  it('asks for both fields before submitting', async () => {
    const { fetchMock } = setup('/login')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Войти' }))
    expect(screen.getByText('Введите логин и пароль.')).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/auth/login')).toBe(false)
  })

  it('toggles password visibility', async () => {
    setup('/login')
    const user = userEvent.setup()
    const input = await screen.findByLabelText('Пароль')
    await user.click(screen.getByRole('button', { name: 'Показать пароль' }))
    expect(input).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: 'Скрыть пароль' }))
    expect(input).toHaveAttribute('type', 'password')
  })

  it('redirects an already logged-in user to next', async () => {
    const { router } = setup('/login?next=%2Fzoom', {}, { 'GET /api/auth/me': makeUser() })
    await waitFor(() => expect(router.state.location.pathname).toBe('/zoom'))
  })

  it('offers a retry when the config cannot be loaded', async () => {
    setup('/login', {}, { 'GET /api/auth/config': () => errorResponse(500, 'internal', 'Ошибка сервера') })
    expect(await screen.findByText('Не удалось открыть страницу входа')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Повторить' })).toBeInTheDocument()
  })
})

describe('LoginPage: access denied after Keycloak', () => {
  it('names the required roles and offers another account, keeping next', async () => {
    setup('/login?error=access_denied&required=zoom-teachers%2C%2Fadmins&next=%2Flessons%2F5', {
      local_enabled: false,
      oidc_enabled: true,
      oidc_label: 'Войти через СТАНКИН ID',
    })
    expect(
      await screen.findByText(
        'У вас нет доступа к системе. Попросите администратора выдать вам одну из ролей: «zoom-teachers», «/admins».',
      ),
    ).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Войти под другой учётной записью' })).toHaveAttribute(
      'href',
      '/auth/oidc/login?next=%2Flessons%2F5&prompt=login',
    )
    // The usual button stays (access may have been granted meanwhile).
    expect(screen.getByRole('link', { name: 'Войти через СТАНКИН ID' })).toHaveAttribute(
      'href',
      '/auth/oidc/login?next=%2Flessons%2F5',
    )
  })

  it('uses the singular for one role and the generic text without any', async () => {
    setup('/login?error=access_denied&required=teachers', { oidc_enabled: true })
    expect(
      await screen.findByText('У вас нет доступа к системе. Попросите администратора выдать вам роль «teachers».'),
    ).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Войти под другой учётной записью' })).toHaveAttribute(
      'href',
      '/auth/oidc/login?prompt=login',
    )
  })

  it('offers another account when Keycloak signed in an account disabled here', async () => {
    // Keycloak keeps that account signed in: without the switch every login comes back as it.
    setup('/login?error=disabled&next=%2Fgroups', { oidc_enabled: true })
    expect(
      await screen.findByText('Ваша учётная запись отключена. Обратитесь к администратору.'),
    ).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Войти под другой учётной записью' })).toHaveAttribute(
      'href',
      '/auth/oidc/login?next=%2Fgroups&prompt=login',
    )
  })

  it('offers another account only after a denial and only with Keycloak', async () => {
    setup('/login?error=oidc_failed', { oidc_enabled: true })
    await screen.findByRole('link', { name: 'Войти через Keycloak' })
    expect(screen.queryByRole('link', { name: 'Войти под другой учётной записью' })).toBeNull()
  })

  it('without Keycloak shows the hint but no account switch', async () => {
    setup('/login?error=access_denied&required=teachers')
    expect(await screen.findByText(/выдать вам роль «teachers»/)).toBeInTheDocument()
    await screen.findByLabelText('Логин')
    expect(screen.queryByRole('link', { name: 'Войти под другой учётной записью' })).toBeNull()
  })

  it('back from Keycloak after the switch: asks to sign in with the other account', async () => {
    // Keycloak's logout redirect: /login?state=switch_account (the old account's session has ended).
    setup('/login?state=switch_account', { local_enabled: false, oidc_enabled: true, oidc_label: 'Войти через СТАНКИН ID' })
    expect(
      await screen.findByText('Вы вышли из прежней учётной записи. Нажмите «Войти через СТАНКИН ID» и войдите под нужной.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Войти через СТАНКИН ID' })).toHaveAttribute('href', '/auth/oidc/login')
    // Not an error, and no second switch link.
    expect(screen.queryByText(/нет доступа/)).toBeNull()
    expect(screen.queryByRole('link', { name: 'Войти под другой учётной записью' })).toBeNull()
  })

  it('ignores the switch state when Keycloak is off', async () => {
    setup('/login?state=switch_account')
    await screen.findByLabelText('Логин')
    expect(screen.queryByText(/Вы вышли из прежней учётной записи/)).toBeNull()
  })
})

describe('parseRequiredRoles', () => {
  it.each([
    [null, []],
    ['', []],
    [' teachers , admins,teachers,, ', ['teachers', 'admins']],
    ['/Преподаватели/ИТ,zoom-checker:teacher', ['/Преподаватели/ИТ', 'zoom-checker:teacher']],
    // Crafted links must not put arbitrary text on the page.
    ['<b>x</b>,https://evil.example/?a=b,teachers', ['teachers']],
    [`${'a'.repeat(65)},ok`, ['ok']],
  ])('%j → %j', (raw, expected) => {
    expect(parseRequiredRoles(raw)).toEqual(expected)
  })

  it('keeps at most ten names', () => {
    const raw = Array.from({ length: 15 }, (_, i) => `role${i}`).join(',')
    expect(parseRequiredRoles(raw)).toHaveLength(10)
  })

  it('builds the hint', () => {
    expect(requiredRolesHint([])).toBe('')
    expect(requiredRolesHint(['a'])).toBe('Попросите администратора выдать вам роль «a».')
    expect(requiredRolesHint(['a', 'b'])).toBe('Попросите администратора выдать вам одну из ролей: «a», «b».')
  })
})

describe('LoginPage ?error= robustness (review)', () => {
  it.each(['__proto__', 'constructor', 'toString', 'hasOwnProperty'])(
    'shows the generic message for ?error=%s instead of crashing',
    async (code) => {
      setup(`/login?error=${code}`)
      expect(await screen.findByText('Не удалось войти. Попробуйте ещё раз.')).toBeInTheDocument()
      expect(await screen.findByLabelText('Логин')).toBeInTheDocument()
      expect(loginErrorMessage(code)).toBe('Не удалось войти. Попробуйте ещё раз.')
    },
  )
})

describe('LoginPage: Keycloak login opened on another address', () => {
  it('names and links the main address when the server reports it', async () => {
    setup('/login?error=oidc_host&next=%2Flessons%2F5', {
      local_enabled: false,
      oidc_enabled: true,
      public_url: 'https://zoom.example.com/',
    })
    const link = await screen.findByRole('link', { name: 'zoom.example.com' })
    expect(link).toHaveAttribute('href', 'https://zoom.example.com/login?next=%2Flessons%2F5')
    expect(link.closest('[role="alert"]')).toHaveTextContent(
      'Вход через Keycloak работает только по основному адресу сервиса: zoom.example.com. Откройте его и войдите снова.',
    )
  })

  it('keeps a path prefix of the main address', () => {
    const base = publicAddress('https://example.com/zoom/')!
    expect(publicLoginUrl(base, '/')).toBe('https://example.com/zoom/login')
    expect(publicLoginUrl(base, '/groups?status=all')).toBe('https://example.com/zoom/login?next=%2Fgroups%3Fstatus%3Dall')
  })

  it.each([undefined, '', 'javascript:alert(1)', 'ftp://zoom.example.com', 'https://user:pw@zoom.example.com', 'не адрес'])(
    'falls back to the plain hint without a usable address (%s)',
    async (publicUrl) => {
      setup('/login?error=oidc_host', { oidc_enabled: true, public_url: publicUrl })
      expect(
        await screen.findByText(
          'Вход через Keycloak работает только по основному адресу сервиса. Откройте сервис по его основному адресу и войдите снова.',
        ),
      ).toBeInTheDocument()
      expect(screen.queryByRole('link', { name: /zoom\.example\.com/ })).toBeNull()
    },
  )
})

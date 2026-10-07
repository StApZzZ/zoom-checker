import { configure, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { generatePassword } from '@/components/admin/password'
import { userStateMeta, validateNewUser } from '@/components/admin/users'
import { newPasswordProblem } from '@/components/auth/password'
import type { User } from '@/lib/types'
import { routes } from '@/routes'
import { errorResponse, jsonResponse, makeUser, mockFetch, renderRoutes } from '@/test/utils'

// Pages are lazy routes: warm the module so the first findBy* does not race a cold
// transform on a busy machine, and allow slow CI runners more time.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })
beforeAll(async () => {
  await import('@/pages/admin/UsersPage')
})

const admin = makeUser({ id: 1, username: 'admin', display_name: 'Админ Главный', role: 'admin' })
const teacher = makeUser({ id: 2, username: 'ivanova', display_name: 'Мария Иванова', last_login_at: '2026-10-05T10:00:00Z' })
const ldapUser = makeUser({ id: 3, username: 'petrov', display_name: 'Пётр Петров', auth_source: 'ldap' })
const oidcUser = makeUser({ id: 4, username: 'sidorova', display_name: 'Анна Сидорова', auth_source: 'oidc', disabled: true })

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function setup(extra: Record<string, unknown> = {}, users: User[] = [admin, teacher, ldapUser, oidcUser]) {
  const fetchMock = mockFetch({
    'GET /api/auth/me': admin,
    'GET /api/auth/config': { local_enabled: true, ldap_enabled: true, oidc_enabled: true, oidc_label: '', version: '1' },
    'GET /api/users': users,
    ...extra,
  })
  return { fetchMock, ...renderRoutes(routes, '/admin/users') }
}

const bodyOf = (fetchMock: ReturnType<typeof mockFetch>, method: string, path: string) => {
  const call = fetchMock.mock.calls.find(([u, init]) => String(u) === path && init?.method === method)
  return call ? JSON.parse(String(call[1]!.body)) : undefined
}

describe('generatePassword', () => {
  it('makes readable strong passwords that satisfy the policy', () => {
    for (let i = 0; i < 50; i++) {
      const pw = generatePassword()
      expect(pw).toMatch(/^[A-Za-z2-9]{4}-[A-Za-z2-9]{4}-[A-Za-z2-9]{4}$/)
      expect(pw).not.toMatch(/[01OIl]/)
      expect(pw).toMatch(/[a-z]/)
      expect(pw).toMatch(/[A-Z]/)
      expect(pw).toMatch(/\d/)
      expect(newPasswordProblem(pw)).toBeNull()
    }
  })

  it('retries until every character class is present', () => {
    // First 12 draws: all "a" (index 0) → rejected; then a mix.
    const seq = [...Array(12).fill(0), 0, 24, 48, ...Array(9).fill(1)]
    let i = 0
    const pw = generatePassword(3, (buf) => {
      buf[0] = seq[i++ % seq.length]
    })
    expect(pw).toBe('aA2b-bbbb-bbbb')
  })
})

describe('validateNewUser', () => {
  it('explains every problem in Russian', () => {
    const errors = validateNewUser({ username: 'iv anova', display_name: ' ', password: 'short', role: 'teacher' })
    expect(errors.username).toBe('Логин не должен содержать пробелов.')
    expect(errors.display_name).toMatch(/Введите имя/)
    expect(errors.password).toBe('Пароль должен быть не короче 8 символов.')
    expect(validateNewUser({ username: 'ivanova', display_name: 'Мария', password: 'xK7p-m2Qa-9Rtw', role: 'admin' })).toEqual({})
  })

  it('labels account states', () => {
    expect(userStateMeta({ disabled: true, must_change_password: true, auth_source: 'local' }).label).toBe('Отключён')
    expect(userStateMeta({ disabled: false, must_change_password: true, auth_source: 'local' }).label).toBe('Ждёт смены пароля')
    expect(userStateMeta({ disabled: false, must_change_password: true, auth_source: 'ldap' }).label).toBe('Активен')
  })
})

describe('UsersPage', () => {
  it('lists users with role, source, last login and state', async () => {
    setup()
    const table = await screen.findByRole('table')
    const rows = within(table).getAllByRole('row')
    expect(rows).toHaveLength(5)
    expect(within(table).getByText('Это вы')).toBeInTheDocument()
    expect(within(table).getAllByText('Локальный', { selector: 'td' })).toHaveLength(2)
    expect(within(table).getByText('LDAP')).toBeInTheDocument()
    expect(within(table).getByText('Keycloak')).toBeInTheDocument()
    expect(within(table).getAllByText('Ещё не входил').length).toBeGreaterThan(0)
    expect(within(table).getByText('Отключён')).toBeInTheDocument()
    expect(screen.getByText(/появляются в этом списке сами после\s+первого входа/)).toBeInTheDocument()
    // No actions on your own account.
    expect(screen.queryByRole('button', { name: 'Действия: Админ Главный' })).toBeNull()
  })

  it('filters by name or login', async () => {
    setup()
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.type(screen.getByRole('searchbox', { name: 'Поиск по имени или логину' }), 'петр')
    const rows = within(screen.getByRole('table')).getAllByRole('row')
    expect(rows).toHaveLength(2)
    expect(screen.getByText('Найдено: 1 из 4')).toBeInTheDocument()
  })

  it('creates a local user and shows the password once', async () => {
    const { fetchMock } = setup({
      'POST /api/users': (_u: URL, init: RequestInit) => {
        const body = JSON.parse(String(init.body))
        return jsonResponse(makeUser({ id: 9, username: body.username, display_name: body.display_name, role: body.role, must_change_password: true }), 201)
      },
    })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Новый пользователь' }))
    const dialog = await screen.findByRole('dialog')

    // A strong password is suggested right away.
    const password = within(dialog).getByLabelText('Пароль') as HTMLInputElement
    expect(newPasswordProblem(password.value)).toBeNull()

    await user.click(within(dialog).getByRole('button', { name: 'Создать' }))
    expect(within(dialog).getByText(/Введите логин/)).toBeInTheDocument()
    expect(within(dialog).getByText(/Введите имя/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([u, init]) => String(u) === '/api/users' && init?.method === 'POST')).toBe(false)

    await user.type(within(dialog).getByLabelText('Логин'), '  smirnov ')
    await user.type(within(dialog).getByLabelText('Имя и фамилия'), 'Олег Смирнов')
    await user.click(within(dialog).getByRole('radio', { name: /Администратор/ }))
    await user.clear(password)
    await user.type(password, 'Secret-Pass-42')
    await user.click(within(dialog).getByRole('button', { name: 'Создать' }))

    expect(await within(dialog).findByRole('heading', { name: 'Пользователь создан' })).toBeInTheDocument()
    expect(bodyOf(fetchMock, 'POST', '/api/users')).toEqual({
      username: 'smirnov',
      display_name: 'Олег Смирнов',
      password: 'Secret-Pass-42',
      role: 'admin',
    })
    expect(within(dialog).getByTestId('credentials-username')).toHaveTextContent('smirnov')
    expect(within(dialog).getByTestId('credentials-password')).toHaveTextContent('Secret-Pass-42')
    expect(within(dialog).getByText(/При первом входе система попросит придумать новый пароль/)).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Скопировать логин и пароль' })).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Готово' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('shows a taken login next to the field', async () => {
    setup({ 'POST /api/users': () => errorResponse(409, 'conflict', 'Пользователь с таким логином уже есть') })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Новый пользователь' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Логин'), 'ivanova')
    await user.type(within(dialog).getByLabelText('Имя и фамилия'), 'Мария')
    await user.click(within(dialog).getByRole('button', { name: 'Создать' }))
    const login = within(dialog).getByLabelText('Логин')
    await waitFor(() => expect(login).toHaveAttribute('aria-invalid', 'true'))
    expect(within(dialog).getByText('Пользователь с таким логином уже есть')).toBeInTheDocument()
    expect(toastMock.error).not.toHaveBeenCalled()
  })

  it('resets a local password and shows the new temporary password', async () => {
    const { fetchMock } = setup({
      'PATCH /api/users/2': () => jsonResponse({ ...teacher, must_change_password: true }),
    })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Мария Иванова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сбросить пароль' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(within(confirm).getByText('Сбросить пароль пользователя «Мария Иванова»?')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Сбросить пароль' }))

    const dialog = await screen.findByRole('dialog', { name: 'Новый временный пароль' })
    const sent = bodyOf(fetchMock, 'PATCH', '/api/users/2')
    expect(Object.keys(sent)).toEqual(['password'])
    expect(newPasswordProblem(sent.password)).toBeNull()
    expect(within(dialog).getByTestId('credentials-password')).toHaveTextContent(sent.password)
    expect(within(dialog).getByTestId('credentials-username')).toHaveTextContent('ivanova')
    expect(toastMock.success).toHaveBeenCalledWith('Пароль сброшен')
  })

  it('does not offer password reset or role change to LDAP users', async () => {
    setup()
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Пётр Петров' }))
    expect(await screen.findByRole('menuitem', { name: 'Роль задаётся группами LDAP' })).toHaveAttribute('aria-disabled', 'true')
    expect(screen.queryByRole('menuitem', { name: 'Сбросить пароль' })).toBeNull()
    expect(screen.getByRole('menuitem', { name: 'Отключить' })).toBeInTheDocument()
  })

  it('changes the role of a local user only after confirmation', async () => {
    const { fetchMock } = setup({ 'PATCH /api/users/2': () => jsonResponse({ ...teacher, role: 'admin' }) })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Мария Иванова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сделать администратором' }))

    // Granting admin rights is never a single mis-click: nothing is sent before the confirmation.
    let confirm = await screen.findByRole('alertdialog')
    expect(within(confirm).getByText('Сделать «Мария Иванова» администратором?')).toBeInTheDocument()
    expect(bodyOf(fetchMock, 'PATCH', '/api/users/2')).toBeUndefined()
    await user.click(within(confirm).getByRole('button', { name: 'Отмена' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(bodyOf(fetchMock, 'PATCH', '/api/users/2')).toBeUndefined()

    await user.click(screen.getByRole('button', { name: 'Действия: Мария Иванова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сделать администратором' }))
    confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Сделать администратором' }))
    await waitFor(() => expect(bodyOf(fetchMock, 'PATCH', '/api/users/2')).toEqual({ role: 'admin' }))
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Мария Иванова теперь администратор'))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('demotes an administrator and keeps the dialog open when the server refuses', async () => {
    const otherAdmin = makeUser({ id: 5, username: 'boss', display_name: 'Ольга Смирнова', role: 'admin' })
    const { fetchMock } = setup(
      {
        'PATCH /api/users/5': () => errorResponse(422, 'validation', 'Нельзя убрать последнего администратора'),
      },
      [admin, otherAdmin],
    )
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Ольга Смирнова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Сделать преподавателем' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(within(confirm).getByText('Сделать «Ольга Смирнова» преподавателем?')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Сделать преподавателем' }))
    await waitFor(() => expect(bodyOf(fetchMock, 'PATCH', '/api/users/5')).toEqual({ role: 'teacher' }))
    // The error is toasted by the mutation layer (not in this test's QueryClient); the dialog stays.
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('re-enables a disabled user and disables after confirmation', async () => {
    const { fetchMock } = setup({
      'PATCH /api/users/4': () => jsonResponse({ ...oidcUser, disabled: false }),
      'PATCH /api/users/2': () => jsonResponse({ ...teacher, disabled: true }),
    })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Анна Сидорова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Включить' }))
    await waitFor(() => expect(bodyOf(fetchMock, 'PATCH', '/api/users/4')).toEqual({ disabled: false }))

    await user.click(screen.getByRole('button', { name: 'Действия: Мария Иванова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Отключить' }))
    const confirm = await screen.findByRole('alertdialog')
    await user.click(within(confirm).getByRole('button', { name: 'Отключить' }))
    await waitFor(() => expect(bodyOf(fetchMock, 'PATCH', '/api/users/2')).toEqual({ disabled: true }))
  })

  it('deletes a user after confirmation', async () => {
    const { fetchMock } = setup({ 'DELETE /api/users/2': () => new Response(null, { status: 204 }) })
    const user = userEvent.setup()
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Действия: Мария Иванова' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Удалить' }))
    const confirm = await screen.findByRole('alertdialog')
    expect(within(confirm).getByText('Удалить пользователя «Мария Иванова»?')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Удалить' }))
    await waitFor(() =>
      expect(fetchMock.mock.calls.some(([u, init]) => String(u) === '/api/users/2' && init?.method === 'DELETE')).toBe(true),
    )
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })
})

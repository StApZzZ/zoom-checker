// Corporate login, part 2: the instance restarted with Samba AD (preset ad,
// ldaps) instead of OpenLDAP, against deploy/dev/auth-stand. Keycloak and the
// local admin stay enabled next to it.

import { expect, test } from '@playwright/test'

import {
  AD,
  AD_DOMAIN,
  expectSignedIn,
  KC,
  LOCAL_ADMIN,
  loginFormError,
  newContext,
  oidcLogin,
  sessionRole,
  STAND_ENABLED,
  STAND_SKIP_REASON,
  startAuthServer,
  stopAuthServer,
  submitLoginForm,
  userRow,
  type AuthServer,
} from './stand'

test.skip(!STAND_ENABLED, STAND_SKIP_REASON)

let server: AuthServer | undefined

test.beforeAll(async () => {
  server = await startAuthServer('ad')
})

test.afterAll(async () => {
  await stopAuthServer(server)
})

/** The account id the server reports for the current session. */
async function sessionUserId(page: import('@playwright/test').Page): Promise<number> {
  const res = await page.request.get('/api/auth/me')
  expect(res.status()).toBe(200)
  return ((await res.json()) as { user: { id: number } }).user.id
}

test('a teacher through a nested group signs in as CORP\\login and as login@domain: one account', async ({
  page,
  browser,
}) => {
  // DOMAIN\login, the way Windows users type it.
  await submitLoginForm(page, `CORP\\${AD.teacher.username}`, AD.teacher.password)
  await expectSignedIn(page, AD.teacher.name, 'teacher')
  expect(await sessionRole(page)).toBe('ldap:t.ivanova:teacher')
  const id = await sessionUserId(page)

  // The UPN (login@domain) in another browser.
  const ctx = await newContext(browser)
  try {
    const other = await ctx.newPage()
    await submitLoginForm(other, `${AD.teacher.username}@${AD_DOMAIN}`, AD.teacher.password)
    await expectSignedIn(other, AD.teacher.name, 'teacher')
    expect(await sessionRole(other)).toBe('ldap:t.ivanova:teacher')
    expect(await sessionUserId(other)).toBe(id)
  } finally {
    await ctx.close()
  }
})

test('a disabled account and a user without a group cannot sign in', async ({ page }) => {
  // carol is in ZoomChecker-Teachers, but disabled in AD: no hint that the account exists.
  await submitLoginForm(page, AD.disabled.username, AD.disabled.password)
  await expect(loginFormError(page)).toHaveText('Неверный логин или пароль')
  await submitLoginForm(page, `CORP\\${AD.disabled.username}`, AD.disabled.password)
  await expect(loginFormError(page)).toHaveText('Неверный логин или пароль')
  expect((await page.request.get('/api/auth/me')).status()).toBe(401)

  await submitLoginForm(page, AD.student.username, AD.student.password)
  await expect(loginFormError(page)).toHaveText(
    'У вас нет доступа к Zoom Checker. Попросите администратора добавить вас в одну из групп: ZoomChecker-Teachers, ZoomChecker-Admins.',
  )

  await submitLoginForm(page, AD.teacher.username, 'Wrong-Passw0rd!')
  await expect(loginFormError(page)).toHaveText('Неверный логин или пароль')
  expect((await page.request.get('/api/auth/me')).status()).toBe(401)
})

test('an AD admin gets the admin pages; Keycloak and the local admin keep working; /admin/users shows the sources', async ({
  page,
  browser,
}) => {
  await submitLoginForm(page, AD.admin.username, AD.admin.password)
  await expectSignedIn(page, AD.admin.name, 'admin')
  expect(await sessionRole(page)).toBe('ldap:a.petrov:admin')
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Настройки' })).toBeVisible()

  // Other kinds of accounts, each in its own browser.
  for (const signIn of [
    (p: typeof page) => submitLoginForm(p, AD.teacher.username, AD.teacher.password),
    (p: typeof page) => oidcLogin(p, KC.teacher),
  ]) {
    const ctx = await newContext(browser)
    const p = await ctx.newPage()
    await signIn(p)
    await expect(p.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
    await ctx.close()
  }

  const ctx = await newContext(browser)
  try {
    const admin = await ctx.newPage()
    await submitLoginForm(admin, LOCAL_ADMIN.username, LOCAL_ADMIN.password)
    await expectSignedIn(admin, 'Администратор', 'admin')
    expect(await sessionRole(admin)).toBe('local:admin:admin')

    await admin.goto('/admin/users')
    await expect(admin.getByRole('heading', { level: 1, name: 'Пользователи' })).toBeVisible()
    const expected: Array<[string, string, string]> = [
      [LOCAL_ADMIN.username, 'Администратор', 'Локальный'],
      [AD.teacher.username, 'Преподаватель', 'LDAP'],
      [AD.admin.username, 'Администратор', 'LDAP'],
      [KC.teacher.username, 'Преподаватель', 'Keycloak'],
    ]
    for (const [username, role, source] of expected) {
      const cells = userRow(admin, username).getByRole('cell')
      await expect(cells.nth(1), `role of ${username}`).toHaveText(role)
      await expect(cells.nth(2), `source of ${username}`).toHaveText(source)
    }
    // Both spellings of t.ivanova are one account, with the AD display name and mail.
    await expect(userRow(admin, AD.teacher.username)).toHaveCount(1)
    await expect(userRow(admin, AD.teacher.username)).toContainText(AD.teacher.name)
    await expect(userRow(admin, AD.teacher.username)).toContainText(`t.ivanova@${AD_DOMAIN}`)
    // Refused logins never create an account.
    await expect(userRow(admin, AD.disabled.username)).toHaveCount(0)
    await expect(userRow(admin, AD.student.username)).toHaveCount(0)
  } finally {
    await ctx.close()
  }
})

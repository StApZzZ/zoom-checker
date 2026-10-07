// Corporate login, part 1: Keycloak (realm edu) and OpenLDAP next to the local
// admin, against deploy/dev/auth-stand. One instance on localhost:8080 for the
// whole file; every test uses fresh browser contexts, so neither the app's nor
// Keycloak's session leaks from one test into another.

import { expect, test } from '@playwright/test'

import {
  expectKeycloakLoginForm,
  expectSignedIn,
  KC,
  keycloakButton,
  keycloakSignIn,
  LOCAL_ADMIN,
  loginFormError,
  logoutFromApp,
  newContext,
  OIDC_REQUIRED_ROLES,
  oidcLogin,
  OPENLDAP,
  sessionRole,
  STAND_ENABLED,
  STAND_SKIP_REASON,
  startAuthServer,
  stopAuthServer,
  submitLoginForm,
  userRow,
  APP_URL,
  type AuthServer,
} from './stand'

test.skip(!STAND_ENABLED, STAND_SKIP_REASON)

let server: AuthServer | undefined

test.beforeAll(async () => {
  server = await startAuthServer('openldap')
})

test.afterAll(async () => {
  await stopAuthServer(server)
})

test.describe('Keycloak (OIDC)', () => {
  test('a teacher signs in through the Keycloak form and lands where they wanted to go, as a teacher', async ({
    page,
  }) => {
    // A protected page sends an anonymous visitor to /login?next=…
    await page.goto('/groups')
    await expect(page).toHaveURL(/\/login\?next=%2Fgroups$/)
    await expect(page.getByText('Используйте корпоративную учётную запись')).toBeVisible()
    await keycloakButton(page).click()
    await keycloakSignIn(page, KC.teacher)

    await expect(page).toHaveURL(`${APP_URL}/groups`)
    await expect(page.getByRole('heading', { level: 1, name: 'Группы' })).toBeVisible()
    await page.goto('/')
    await expectSignedIn(page, KC.teacher.name, 'teacher')
    expect(await sessionRole(page)).toBe('oidc:kc-teacher:teacher')

    // No admin pages for a teacher: neither in the UI nor through the API.
    await page.goto('/admin/users')
    await expect(page.getByText('Этот раздел доступен только администраторам')).toBeVisible()
    expect((await page.request.get('/api/users')).status()).toBe(403)
  })

  test('an administrator gets the admin pages', async ({ page }) => {
    await oidcLogin(page, KC.admin)
    await expectSignedIn(page, KC.admin.name, 'admin')
    expect(await sessionRole(page)).toBe('oidc:kc-admin:admin')

    await page.getByRole('navigation').getByRole('button', { name: 'Админ' }).click()
    await page.getByRole('menuitem', { name: 'Пользователи' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Пользователи' })).toBeVisible()
    await expect(userRow(page, KC.admin.username).getByRole('cell').nth(2)).toHaveText('Keycloak')
    await page.goto('/admin/settings')
    await expect(page.getByRole('heading', { level: 1, name: 'Настройки' })).toBeVisible()
  })

  test('a student is refused with the roles that would grant access, and can sign in as somebody else', async ({
    page,
  }) => {
    await oidcLogin(page, KC.student)
    await expect(page).toHaveURL(/\/login\?error=access_denied&required=/)
    const alert = page.getByRole('alert').filter({ hasText: 'нет доступа' })
    await expect(alert).toHaveText(
      `У вас нет доступа к системе. Попросите администратора выдать вам одну из ролей: ${OIDC_REQUIRED_ROLES.map((r) => `«${r}»`).join(', ')}.`,
    )
    expect((await page.request.get('/api/auth/me')).status()).toBe(401)

    // Keycloak still remembers the student: the plain button sends the same
    // account straight back, without asking for a password.
    await keycloakButton(page).click()
    await expect(page).toHaveURL(/\/login\?error=access_denied/)
    await expect(alert).toBeVisible()

    // «Войти под другой учётной записью» ends the student's Keycloak session
    // and comes back with a hint; Keycloak then shows its full, empty form
    // (not a password prompt locked to the student).
    await page.getByRole('link', { name: 'Войти под другой учётной записью' }).click()
    await expect(page).toHaveURL(`${APP_URL}/login?state=switch_account`)
    await expect(
      page.getByText('Вы вышли из прежней учётной записи. Нажмите «Войти через Keycloak» и войдите под нужной.'),
    ).toBeVisible()
    await expect(page.getByText(/нет доступа/)).toHaveCount(0)
    await keycloakButton(page).click()
    await expectKeycloakLoginForm(page)
    await expect(page.locator('#username')).toHaveValue('')
    await keycloakSignIn(page, KC.teacher)
    await expectSignedIn(page, KC.teacher.name, 'teacher')
    expect(await sessionRole(page)).toBe('oidc:kc-teacher:teacher')
  })

  test('signing out of the app ends the Keycloak session too', async ({ page }) => {
    await oidcLogin(page, KC.teacher)
    await expectSignedIn(page, KC.teacher.name, 'teacher')

    await logoutFromApp(page)
    // Through Keycloak's end-session endpoint back to the app's login page.
    await expect(page).toHaveURL(`${APP_URL}/login`)
    await expect(keycloakButton(page)).toBeVisible()
    expect((await page.request.get('/api/auth/me')).status()).toBe(401)

    // Keycloak asks for credentials again instead of signing the user in silently.
    await keycloakButton(page).click()
    await expectKeycloakLoginForm(page)
    await keycloakSignIn(page, KC.admin)
    await expectSignedIn(page, KC.admin.name, 'admin')
  })
})

test.describe('OpenLDAP', () => {
  test('a teacher signs in with the directory login and password', async ({ page }) => {
    await submitLoginForm(page, OPENLDAP.teacher.username, OPENLDAP.teacher.password)
    await expectSignedIn(page, OPENLDAP.teacher.name, 'teacher')
    expect(await sessionRole(page)).toBe('ldap:teacher1:teacher')
    // Directory accounts change their password in the directory, not here.
    await page.getByRole('button', { name: /^Меню пользователя/ }).click()
    await expect(page.getByRole('menuitem', { name: 'Сменить пароль' })).toHaveCount(0)
    await page.keyboard.press('Escape')
  })

  test('a wrong password, an unknown login and a user without a group are refused with clear messages', async ({
    page,
  }) => {
    // One wrong password only: OpenLDAP locks an account after 3 in a row
    // (the successful login below resets the count).
    await submitLoginForm(page, OPENLDAP.admin.username, 'Wrong-Passw0rd!')
    await expect(loginFormError(page)).toHaveText('Неверный логин или пароль')
    await expect(page).toHaveURL(/\/login/)
    await expect(page.getByLabel('Пароль', { exact: true })).toHaveValue('')

    await submitLoginForm(page, 'nobody-here', 'Whatever-Passw0rd!')
    await expect(loginFormError(page)).toHaveText('Неверный логин или пароль')

    await submitLoginForm(page, OPENLDAP.student.username, OPENLDAP.student.password)
    await expect(loginFormError(page)).toHaveText(
      'У вас нет доступа к Zoom Checker. Попросите администратора добавить вас в одну из групп: zoom-checker-teachers, zoom-checker-admins.',
    )
    expect((await page.request.get('/api/auth/me')).status()).toBe(401)

    await submitLoginForm(page, OPENLDAP.admin.username, OPENLDAP.admin.password)
    await expectSignedIn(page, OPENLDAP.admin.name, 'admin')
    expect(await sessionRole(page)).toBe('ldap:admin1:admin')
  })
})

test('the local admin still signs in, and directory users appear in /admin/users with their source', async ({
  page,
  browser,
}) => {
  // Make sure both kinds of external accounts exist, whatever ran before.
  for (const signIn of [
    (p: typeof page) => submitLoginForm(p, OPENLDAP.teacher.username, OPENLDAP.teacher.password),
    (p: typeof page) => oidcLogin(p, KC.teacher),
  ]) {
    const ctx = await newContext(browser)
    const p = await ctx.newPage()
    await signIn(p)
    await expect(p.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
    await ctx.close()
  }

  await submitLoginForm(page, LOCAL_ADMIN.username, LOCAL_ADMIN.password)
  await expectSignedIn(page, 'Администратор', 'admin')
  expect(await sessionRole(page)).toBe('local:admin:admin')

  await page.goto('/admin/users')
  await expect(page.getByRole('heading', { level: 1, name: 'Пользователи' })).toBeVisible()
  const expected: Array<[string, string, string]> = [
    [LOCAL_ADMIN.username, 'Администратор', 'Локальный'],
    [OPENLDAP.teacher.username, 'Преподаватель', 'LDAP'],
    [KC.teacher.username, 'Преподаватель', 'Keycloak'],
  ]
  for (const [username, role, source] of expected) {
    const cells = userRow(page, username).getByRole('cell')
    await expect(cells.nth(1), `role of ${username}`).toHaveText(role)
    await expect(cells.nth(2), `source of ${username}`).toHaveText(source)
  }
  await expect(userRow(page, OPENLDAP.teacher.username)).toContainText(OPENLDAP.teacher.name)
  await expect(userRow(page, KC.teacher.username)).toContainText(KC.teacher.name)
  // Refused logins never create an account.
  await expect(userRow(page, KC.student.username)).toHaveCount(0)
  await expect(userRow(page, OPENLDAP.student.username)).toHaveCount(0)
})

test('an admin blocks directory accounts: they are signed out and refused until enabled again', async ({
  page,
  browser,
}) => {
  const accounts = [OPENLDAP.teacher, KC.teacher]
  // Both teachers are signed in, each in their own browser.
  const ldapCtx = await newContext(browser)
  const kcCtx = await newContext(browser)
  const ldapPage = await ldapCtx.newPage()
  const kcPage = await kcCtx.newPage()
  await submitLoginForm(ldapPage, OPENLDAP.teacher.username, OPENLDAP.teacher.password)
  await expectSignedIn(ldapPage, OPENLDAP.teacher.name, 'teacher')
  await oidcLogin(kcPage, KC.teacher)
  await expectSignedIn(kcPage, KC.teacher.name, 'teacher')

  await submitLoginForm(page, LOCAL_ADMIN.username, LOCAL_ADMIN.password)
  await expectSignedIn(page, 'Администратор', 'admin')
  await page.goto('/admin/users')
  const setDisabled = async (name: string, username: string, disable: boolean) => {
    await userRow(page, username).getByRole('button', { name: `Действия: ${name}` }).click()
    await page.getByRole('menuitem', { name: disable ? 'Отключить' : 'Включить' }).click()
    if (disable) {
      const dialog = page.getByRole('alertdialog', { name: `Отключить пользователя «${name}»?` })
      await dialog.getByRole('button', { name: 'Отключить' }).click()
    }
    await expect(userRow(page, username).getByRole('cell').nth(4)).toHaveText(disable ? 'Отключён' : 'Активен')
  }
  try {
    for (const a of accounts) await setDisabled(a.name, a.username, true)

    // Open sessions end at once.
    expect((await ldapPage.request.get('/api/auth/me')).status()).toBe(401)
    expect((await kcPage.request.get('/api/auth/me')).status()).toBe(401)

    // The directory still accepts them, the app does not, and says why.
    await submitLoginForm(ldapPage, OPENLDAP.teacher.username, OPENLDAP.teacher.password)
    await expect(loginFormError(ldapPage)).toHaveText('Ваша учётная запись отключена. Обратитесь к администратору.')
    // Keycloak signs kc-teacher in silently (its session is alive); the app refuses.
    await kcPage.goto('/login')
    await keycloakButton(kcPage).click()
    await expect(kcPage).toHaveURL(`${APP_URL}/login?error=disabled`)
    await expect(kcPage.getByRole('alert')).toHaveText('Ваша учётная запись отключена. Обратитесь к администратору.')
    // Without a way out every Keycloak login on this browser would come back as
    // kc-teacher: «Войти под другой учётной записью» ends that Keycloak session.
    await kcPage.getByRole('link', { name: 'Войти под другой учётной записью' }).click()
    await expect(kcPage).toHaveURL(`${APP_URL}/login?state=switch_account`)
    await keycloakButton(kcPage).click()
    await expectKeycloakLoginForm(kcPage)
    await expect(kcPage.locator('#username')).toHaveValue('')
  } finally {
    for (const a of accounts) await setDisabled(a.name, a.username, false)
  }

  // Enabled again: both sign in as before.
  await submitLoginForm(ldapPage, OPENLDAP.teacher.username, OPENLDAP.teacher.password)
  await expectSignedIn(ldapPage, OPENLDAP.teacher.name, 'teacher')
  // Its Keycloak session was ended by the switch above: Keycloak asks again.
  await oidcLogin(kcPage, KC.teacher)
  await expectSignedIn(kcPage, KC.teacher.name, 'teacher')
  await ldapCtx.close()
  await kcCtx.close()
})

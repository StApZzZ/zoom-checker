// The identity-provider stand (deploy/dev/auth-stand: Keycloak, OpenLDAP,
// Samba AD) and a zoom-checker instance wired to it.
//
// The realm `edu` registers http://localhost:8080/auth/oidc/callback only, so
// the instance always listens on 127.0.0.1:8080 with PUBLIC_URL
// http://localhost:8080, and the auth project runs one test at a time.

import crypto from 'node:crypto'
import fs from 'node:fs'
import net from 'node:net'
import path from 'node:path'

import { expect, type Browser, type BrowserContext, type Page } from '@playwright/test'

import { REPO_DIR, runDir, TIMEZONE, WEB_DIR } from '../helpers/env'
import { startServer, type RunningServer } from '../helpers/server'
import { escapeRegExp } from '../helpers/ui'

/** Set E2E_AUTH_STAND=1 once `deploy/dev/auth-stand/stand.sh up` is done. */
export const STAND_ENABLED = !!process.env.E2E_AUTH_STAND
export const STAND_SKIP_REASON =
  'E2E_AUTH_STAND is not set: run deploy/dev/auth-stand/stand.sh up, then E2E_AUTH_STAND=1 npm run e2e -- --project=auth'

export const APP_PORT = 8080
/** PUBLIC_URL of the instance: the redirect URI registered in the realm. */
export const APP_URL = `http://localhost:${APP_PORT}`

export const KEYCLOAK_URL = 'http://localhost:18080'
export const KEYCLOAK_ISSUER = `${KEYCLOAK_URL}/realms/edu`
const STATE_DIR = path.join(REPO_DIR, 'deploy', 'dev', 'auth-stand', '.state')

export interface Account {
  username: string
  password: string
  /** Display name the provider sends. */
  name: string
}

/** Local bootstrap admin of the instance (it keeps working next to LDAP/OIDC). */
export const LOCAL_ADMIN: Account = { username: 'admin', password: 'Auth-e2e-Admin-Pa55', name: 'admin' }

export const KC = {
  admin: { username: 'kc-admin', password: 'KcAdmin-Passw0rd!', name: 'Анна Админова' },
  teacher: { username: 'kc-teacher', password: 'KcTeacher-Passw0rd!', name: 'Ольга Преподаватель' },
  student: { username: 'kc-student', password: 'KcStudent-Passw0rd!', name: 'Иван Студентов' },
} satisfies Record<string, Account>

export const OPENLDAP = {
  teacher: { username: 'teacher1', password: 'Teacher1-Passw0rd!', name: 'Мария Иванова' },
  admin: { username: 'admin1', password: 'Admin1-Passw0rd!', name: 'Пётр Админов' },
  student: { username: 'student1', password: 'Student1-Passw0rd!', name: 'Семён Студентов' },
} satisfies Record<string, Account>

export const AD_DOMAIN = 'corp.example.test'
export const AD = {
  /** Teacher only through a nested group: helpdesk ∈ ZoomChecker-Teachers. */
  teacher: { username: 't.ivanova', password: 'Ivanova-Passw0rd!', name: 'Татьяна Иванова' },
  admin: { username: 'a.petrov', password: 'Petrov-Passw0rd!', name: 'Алексей Петров' },
  student: { username: 's.sidorov', password: 'Sidorov-Passw0rd!', name: 'Сергей Сидоров' },
  /** In ZoomChecker-Teachers, but the account is disabled in AD. */
  disabled: { username: 'carol', password: 'Carol-Passw0rd!', name: 'Carol Disabled' },
} satisfies Record<string, Account>

export const OIDC_REQUIRED_ROLES = ['zoom-checker-teacher', 'zoom-checker-admin']

export type Directory = 'openldap' | 'ad'

/** OIDC_* for the realm `edu`. */
function oidcEnv(): Record<string, string> {
  return {
    OIDC_ISSUER_URL: KEYCLOAK_ISSUER,
    OIDC_CLIENT_ID: 'zoom-checker',
    OIDC_CLIENT_SECRET: process.env.ZC_OIDC_CLIENT_SECRET || 'zc-dev-oidc-secret',
    OIDC_ADMIN_ROLES: 'zoom-checker-admin',
    OIDC_TEACHER_ROLES: 'zoom-checker-teacher',
  }
}

/** LDAP_* for one of the stand's directories (README of the stand). */
export function ldapEnv(dir: Directory): Record<string, string> {
  if (dir === 'openldap') {
    return {
      LDAP_URL: 'ldap://localhost:13890',
      LDAP_PRESET: 'openldap',
      LDAP_BASE_DN: 'dc=example,dc=test',
      LDAP_BIND_DN: 'uid=zc-svc,ou=TechnicalAccounts,ou=Users,dc=example,dc=test',
      LDAP_BIND_PASSWORD: 'Svc-Passw0rd!',
      LDAP_ADMIN_GROUPS: 'zoom-checker-admins',
      LDAP_TEACHER_GROUPS: 'zoom-checker-teachers',
    }
  }
  return {
    LDAP_URL: 'ldaps://localhost:13636',
    // Samba's certificate is issued to dc1.corp.example.test, not localhost.
    LDAP_INSECURE_SKIP_VERIFY: 'true',
    LDAP_PRESET: 'ad',
    LDAP_DOMAIN: AD_DOMAIN,
    LDAP_BASE_DN: 'DC=corp,DC=example,DC=test',
    LDAP_BIND_DN: `svc-zoom@${AD_DOMAIN}`,
    LDAP_BIND_PASSWORD: 'Svc-Passw0rd!',
    LDAP_ADMIN_GROUPS: 'ZoomChecker-Admins',
    LDAP_TEACHER_GROUPS: 'ZoomChecker-Teachers',
  }
}

/** Whether something already listens on 127.0.0.1:port. */
function portBusy(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.connect({ host: '127.0.0.1', port })
    socket.setTimeout(1_000)
    socket.once('connect', () => {
      socket.destroy()
      resolve(true)
    })
    socket.once('timeout', () => {
      socket.destroy()
      resolve(false)
    })
    socket.once('error', () => resolve(false))
  })
}

/** Fails early, with the fix in the message, when the stand is not up. */
async function checkStand(dir: Directory): Promise<void> {
  const res = await fetch(`${KEYCLOAK_ISSUER}/.well-known/openid-configuration`, {
    signal: AbortSignal.timeout(5_000),
  }).catch((err: unknown) => {
    throw new Error(`Keycloak is not reachable at ${KEYCLOAK_URL} (${String(err)}): run deploy/dev/auth-stand/stand.sh up`)
  })
  if (!res.ok) throw new Error(`Keycloak realm edu answered ${res.status}: run deploy/dev/auth-stand/stand.sh up`)
  if (dir === 'ad' && !fs.existsSync(path.join(STATE_DIR, 'samba-ca.pem'))) {
    throw new Error('Samba AD is not set up (no .state/samba-ca.pem): run deploy/dev/auth-stand/stand.sh up samba')
  }
}

export interface AuthServer extends RunningServer {
  dir: Directory
}

/**
 * Starts zoom-checker on 127.0.0.1:8080 with local accounts, Keycloak (OIDC)
 * and the given directory (LDAP) enabled, on SQLite in a fresh data
 * directory. `dataDir` reuses an earlier instance's data (a restart with
 * another directory).
 */
export async function startAuthServer(dir: Directory, dataDir?: string): Promise<AuthServer> {
  await checkStand(dir)
  if (await portBusy(APP_PORT)) {
    throw new Error(`port ${APP_PORT} is busy: the realm only accepts ${APP_URL}, stop whatever listens there`)
  }
  const data = dataDir ?? path.join(runDir(), `auth-${dir}-${crypto.randomBytes(4).toString('hex')}`)
  const server = await startServer({
    dataDir: data,
    port: APP_PORT,
    adminPassword: LOCAL_ADMIN.password,
    logFile: path.join(data, `server-${dir}.log`),
    env: { PUBLIC_URL: APP_URL, ...oidcEnv(), ...ldapEnv(dir) },
  })
  return { ...server, dir }
}

/** Stops the instance and keeps its log next to the other test results. */
export async function stopAuthServer(server: AuthServer | undefined): Promise<void> {
  if (!server) return
  await server.stop()
  const out = path.join(WEB_DIR, 'test-results')
  fs.mkdirSync(out, { recursive: true })
  if (fs.existsSync(server.logFile)) fs.copyFileSync(server.logFile, path.join(out, `auth-${server.dir}-server.log`))
}

/** A browser context without any session: neither the app's nor Keycloak's. */
export function newContext(browser: Browser): Promise<BrowserContext> {
  return browser.newContext({
    baseURL: APP_URL,
    storageState: { cookies: [], origins: [] },
    locale: 'ru-RU',
    timezoneId: TIMEZONE,
    viewport: { width: 1366, height: 900 },
  })
}

/** Keycloak's own login form (a page on the Keycloak host). */
export async function expectKeycloakLoginForm(page: Page): Promise<void> {
  await expect(page).toHaveURL(new RegExp(`^${KEYCLOAK_ISSUER}/`))
  await expect(page.locator('#username')).toBeVisible()
  await expect(page.locator('#username')).toBeEditable()
  await expect(page.locator('#password')).toBeVisible()
}

/** Fills Keycloak's login form and submits it. */
export async function keycloakSignIn(page: Page, account: Account): Promise<void> {
  await expectKeycloakLoginForm(page)
  await page.locator('#username').fill(account.username)
  await page.locator('#password').fill(account.password)
  await page.locator('#kc-login').click()
}

/** The Keycloak button on the app's login page. */
export function keycloakButton(page: Page) {
  return page.getByRole('link', { name: 'Войти через Keycloak' })
}

/** From the app's login page through Keycloak's form back into the app. */
export async function oidcLogin(page: Page, account: Account): Promise<void> {
  await page.goto('/login')
  await keycloakButton(page).click()
  await keycloakSignIn(page, account)
}

/** The login form of the app (local or LDAP account). */
export async function submitLoginForm(page: Page, username: string, password: string): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('Логин').fill(username)
  await page.getByLabel('Пароль', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Войти', exact: true }).click()
}

/** The error shown under the login form. */
export function loginFormError(page: Page) {
  return page.locator('form').getByRole('alert')
}

/**
 * Signed in to the app as `name` with `role`: the user menu says who, and the
 * admin menu is there for administrators only.
 */
export async function expectSignedIn(page: Page, name: string, role: 'admin' | 'teacher'): Promise<void> {
  await expect(page).toHaveURL(new RegExp(`^${APP_URL}/(?!login)`))
  await expect(page.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
  const menu = page.getByRole('button', { name: `Меню пользователя: ${name}` })
  await expect(menu).toBeVisible()
  await menu.click()
  await expect(page.getByRole('menu')).toContainText(role === 'admin' ? 'Администратор' : 'Преподаватель')
  await page.keyboard.press('Escape')
  const adminMenu = page.getByRole('navigation').getByRole('button', { name: 'Админ' })
  if (role === 'admin') await expect(adminMenu).toBeVisible()
  else await expect(adminMenu).toHaveCount(0)
}

/** "Меню пользователя → Выйти". */
export async function logoutFromApp(page: Page): Promise<void> {
  await page.getByRole('button', { name: /^Меню пользователя/ }).click()
  await page.getByRole('menuitem', { name: 'Выйти' }).click()
}

/** The role the server reports for the current session. */
export async function sessionRole(page: Page): Promise<string> {
  const res = await page.request.get('/api/auth/me')
  expect(res.status()).toBe(200)
  const body = (await res.json()) as { user: { role: string; auth_source: string; username: string } }
  return `${body.user.auth_source}:${body.user.username}:${body.user.role}`
}

/** /admin/users → the row of the account `username` (its login line is "username · email"). */
export function userRow(page: Page, username: string) {
  return page
    .getByRole('row')
    .filter({ has: page.getByText(new RegExp(`^${escapeRegExp(username)}( · |$)`)) })
}

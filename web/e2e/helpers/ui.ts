// UI helpers: everything here goes through the real screens, the way a teacher
// clicks through them.

import crypto from 'node:crypto'

import { expect, type Browser, type BrowserContext, type Locator, type Page } from '@playwright/test'

import { ADMIN_PASSWORD, ADMIN_USERNAME, TIMEZONE } from './env'

export interface GroupRef {
  id: number
  name: string
}

export interface LessonRef {
  id: number
  title: string
}

/** Unique, readable suffix for names of groups/users created by a test. */
export function uniq(prefix: string): string {
  return `${prefix} ${crypto.randomBytes(3).toString('hex')}`
}

export function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** A browser context without the saved admin session (optionally on another server). */
export async function freshContext(browser: Browser, baseURL: string): Promise<BrowserContext> {
  return browser.newContext({
    baseURL,
    storageState: { cookies: [], origins: [] },
    locale: 'ru-RU',
    timezoneId: TIMEZONE,
    viewport: { width: 1366, height: 900 },
  })
}

/** Logs in through the login form (local account). */
export async function login(page: Page, username = ADMIN_USERNAME, password = ADMIN_PASSWORD): Promise<void> {
  await page.goto('/login')
  await page.getByLabel('Логин').fill(username)
  await page.getByLabel('Пароль', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Войти', exact: true }).click()
  await expect(page).not.toHaveURL(/\/login/)
}

function idFromUrl(page: Page, re: RegExp): number {
  const m = re.exec(new URL(page.url()).pathname)
  if (!m) throw new Error(`unexpected URL ${page.url()}`)
  return Number(m[1])
}

/**
 * "Группы → Новая группа": name + pasted list, saved in the same dialog.
 * Returns once the group page is open.
 */
export async function createGroupByPaste(page: Page, name: string, names: string[]): Promise<GroupRef> {
  await page.goto('/groups')
  await page.getByRole('button', { name: 'Новая группа' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Новая группа' })
  await dialog.getByLabel('Название группы').fill(name)
  await dialog.getByLabel(/Вставьте список студентов/).fill(names.join('\n'))
  await dialog
    .getByRole('button', { name: names.length > 0 ? 'Создать группу и добавить студентов' : 'Создать группу', exact: true })
    .click()
  // Saved automatically when there is nothing to decide; otherwise press "Сохранить список".
  const save = page.getByRole('dialog').getByRole('button', { name: 'Сохранить список' })
  const onGroupPage = () => /\/groups\/\d+$/.test(new URL(page.url()).pathname)
  await expect(async () => {
    // isVisible() does not wait; isEnabled() would wait for a button that is gone.
    if (!onGroupPage() && (await save.isVisible()) && (await save.isEnabled({ timeout: 500 }))) {
      await save.click({ timeout: 2_000 })
    }
    expect(onGroupPage()).toBe(true)
  }).toPass({ timeout: 15_000 })
  const id = idFromUrl(page, /\/groups\/(\d+)$/)
  await expect(page.getByRole('heading', { level: 1, name, exact: true })).toBeVisible()
  if (names.length > 0) {
    await expect(page.getByRole('tab', { name: /Студенты/ })).toContainText(String(names.length))
  }
  return { id, name }
}

/** Date and time inputs ("YYYY-MM-DD", "HH:mm") of `d` in the server/browser timezone. */
export function localInputs(d: Date): { date: string; time: string } {
  const parts = Object.fromEntries(
    new Intl.DateTimeFormat('en-CA', {
      timeZone: TIMEZONE,
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hourCycle: 'h23',
    })
      .formatToParts(d)
      .map((p) => [p.type, p.value]),
  )
  return { date: `${parts.year}-${parts.month}-${parts.day}`, time: `${parts.hour}:${parts.minute}` }
}

/** "HH:mm" of `d` in the server timezone (how the UI shows times). */
export function localTime(d: Date): string {
  return localInputs(d).time
}

/** Start of the current minute minus `n` minutes. */
export function minutesAgo(n: number): Date {
  const now = Date.now()
  return new Date(Math.floor(now / 60_000) * 60_000 - n * 60_000)
}

export interface CreateLessonOptions {
  title?: string
  durationMinutes?: number
}

/**
 * "Занятия → Новое занятие": group, title, Zoom link (or a whole invitation),
 * date, time and duration. Returns once the live lesson page is open.
 */
export async function createLesson(
  page: Page,
  group: GroupRef,
  zoomInput: string,
  start: Date,
  opts: CreateLessonOptions = {},
): Promise<LessonRef> {
  const title = opts.title ?? `Занятие ${localInputs(start).time}`
  await page.goto('/')
  await page.getByRole('button', { name: 'Новое занятие' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Новое занятие' })
  await dialog.getByRole('combobox', { name: 'Группа' }).click()
  await page.getByRole('option', { name: new RegExp(`^${escapeRegExp(group.name)}\\s*·`) }).click()
  // The title is suggested ("Занятие №N") once the group's lessons are loaded:
  // wait for it, or typing could land in front of the suggestion.
  const titleInput = dialog.getByLabel('Название', { exact: true })
  await expect(titleInput).toHaveValue(/^Занятие №\d+$/)
  await titleInput.fill(title)
  await dialog.getByLabel('Ссылка Zoom или номер конференции').fill(zoomInput)
  await expect(dialog.getByText(/^Конференция \d/)).toBeVisible()
  const { date, time } = localInputs(start)
  await dialog.getByLabel('Дата', { exact: true }).fill(date)
  await dialog.getByLabel('Время начала').fill(time)
  await dialog.getByLabel('Длительность, минут').fill(String(opts.durationMinutes ?? 90))
  await dialog.getByRole('button', { name: 'Создать занятие' }).click()
  await page.waitForURL(/\/lessons\/\d+$/)
  const id = idFromUrl(page, /\/lessons\/(\d+)$/)
  await expect(page.getByRole('heading', { level: 1, name: title, exact: true })).toBeVisible()
  return { id, title }
}

/** Opens the live lesson screen and waits until live updates are connected. */
export async function openLesson(page: Page, lesson: LessonRef | number): Promise<void> {
  const id = typeof lesson === 'number' ? lesson : lesson.id
  await page.goto(`/lessons/${id}`)
  await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
  await expectStreamLive(page)
}

/** "Обновляется автоматически" — the live stream is open. */
export async function expectStreamLive(page: Page, timeout = 10_000): Promise<void> {
  await expect(page.getByText('Обновляется автоматически', { exact: true })).toBeVisible({ timeout })
}

/** The row of a student in the live table. */
export function studentRow(page: Page, fullName: string): Locator {
  return page
    .locator('tbody tr[data-student-id]')
    .filter({ has: page.getByRole('button', { name: fullName, exact: true }) })
}

/** The "Время" cell (hh:mm) of a student row. */
export function durationCell(row: Locator): Locator {
  return row.getByRole('timer')
}

/** A live counter tile ("Сейчас в Zoom", …) — its number. */
export function statValue(page: Page, label: string): Locator {
  return page
    .getByRole('group', { name: 'Сводка по занятию' })
    .locator(':scope > *')
    .filter({ hasText: label })
    .locator('span.text-3xl')
}

/** The "Не идентифицированы" block of the live screen. */
export function unidentifiedBlock(page: Page): Locator {
  return page.locator('section#unidentified')
}

/** An entry of the "Не идентифицированы" list by Zoom name. */
export function unidentifiedItem(page: Page, displayName: string): Locator {
  return unidentifiedBlock(page).locator('li[data-participant-id]').filter({ hasText: `«${displayName}»` })
}

/** Lesson status badge in the header ("Идёт", "Завершено", …). */
export function lessonStatusBadge(page: Page, label: string): Locator {
  return page.locator('[data-slot="status-badge"]').filter({ hasText: new RegExp(`^${escapeRegExp(label)}$`) })
}

/** Opens "Действия" in the lesson header and picks an item. */
export async function lessonAction(page: Page, item: string | RegExp): Promise<void> {
  await page.getByRole('button', { name: 'Действия' }).click()
  await page.getByRole('menuitem', { name: item }).click()
}

/** Downloads a file by clicking `trigger`; returns its bytes and suggested name. */
export async function download(page: Page, click: () => Promise<void>): Promise<{ name: string; body: Buffer }> {
  const [dl] = await Promise.all([page.waitForEvent('download'), click()])
  const stream = await dl.createReadStream()
  const chunks: Buffer[] = []
  for await (const c of stream) chunks.push(c as Buffer)
  return { name: dl.suggestedFilename(), body: Buffer.concat(chunks) }
}

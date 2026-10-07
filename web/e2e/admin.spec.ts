// Administration: the very first login with a generated password (own server
// instance without ADMIN_PASSWORD), local teacher accounts, settings.

import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'

import type { BrowserContext, Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { runDir } from './helpers/env'
import { startServer } from './helpers/server'
import { freshContext, login } from './helpers/ui'

/** The mandatory "Смените пароль" dialog of a temporary password. */
async function changeForcedPassword(page: Page, current: string, next: string): Promise<void> {
  const dialog = page.getByRole('dialog', { name: 'Смените пароль' })
  await expect(dialog).toBeVisible()
  await expect(dialog).toContainText('Вы вошли с временным паролем')
  await dialog.getByLabel('Текущий пароль').fill(current)
  await dialog.getByLabel('Новый пароль', { exact: true }).fill(next)
  await dialog.getByLabel('Повторите новый пароль').fill(next)
  await dialog.getByRole('button', { name: 'Сохранить пароль' }).click()
  await expect(dialog).toBeHidden()
}

async function logout(page: Page): Promise<void> {
  await page.getByRole('button', { name: /^Меню пользователя/ }).click()
  await page.getByRole('menuitem', { name: 'Выйти' }).click()
  await expect(page).toHaveURL(/\/login/)
}

test('first start without ADMIN_PASSWORD: generated password, forced change, then normal access', async ({
  browser,
}) => {
  const dataDir = path.join(runDir(), `first-login-${crypto.randomBytes(4).toString('hex')}`)
  const server = await startServer({ dataDir, adminPassword: null })
  let context: BrowserContext | undefined
  try {
    const file = path.join(dataDir, 'initial-admin-password.txt')
    expect(fs.existsSync(file)).toBe(true)
    expect(fs.statSync(file).mode & 0o777).toBe(0o600)
    const generated = fs.readFileSync(file, 'utf8').trim()
    expect(generated.length).toBeGreaterThanOrEqual(12)
    // The log says where the password is, never the password itself.
    const log = fs.readFileSync(server.logFile, 'utf8')
    expect(log).toContain('initial-admin-password.txt')
    expect(log).not.toContain(generated)

    context = await freshContext(browser, server.url)
    const page = await context.newPage()
    await login(page, 'admin', generated)

    const dialog = page.getByRole('dialog', { name: 'Смените пароль' })
    await expect(dialog).toBeVisible()
    // It cannot be dismissed, and the API refuses everything else meanwhile.
    await page.keyboard.press('Escape')
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Отмена' })).toHaveCount(0)
    // Nothing behind the dialog tries to load (the server would refuse it).
    await expect(page.getByRole('heading', { level: 1 })).toHaveCount(0)
    const blocked = await page.request.get('/api/groups')
    expect(blocked.status()).toBe(403)
    expect((await blocked.json()).error.code).toBe('password_change_required')

    // A too short password is explained in Russian.
    await dialog.getByLabel('Текущий пароль').fill(generated)
    await dialog.getByLabel('Новый пароль', { exact: true }).fill('123')
    await dialog.getByLabel('Повторите новый пароль').fill('123')
    await dialog.getByRole('button', { name: 'Сохранить пароль' }).click()
    await expect(dialog.getByRole('alert')).toContainText(/не короче/)

    const mine = `Мой-новый-пароль-${crypto.randomBytes(3).toString('hex')}`
    await changeForcedPassword(page, generated, mine)
    await expect(page.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
    // The page behind the dialog loads normally once the password is changed
    // (it used to stay on «Нет доступа — Сначала смените временный пароль»).
    await expect(page.getByText('Занятий пока нет — начните с трёх шагов')).toBeVisible()
    await expect(page.getByText('Нет доступа')).toHaveCount(0)
    // The file with the old password is gone once it is no longer valid.
    await expect.poll(() => fs.existsSync(file)).toBe(false)

    await page.goto('/admin/users')
    await expect(page.getByRole('heading', { level: 1, name: 'Пользователи' })).toBeVisible()
    await expect(page.getByRole('row').filter({ hasText: 'Это вы' })).toContainText('Администратор')

    // Signing in again: the old password no longer works, the new one does, without the dialog.
    await logout(page)
    await page.getByLabel('Логин').fill('admin')
    await page.getByLabel('Пароль', { exact: true }).fill(generated)
    await page.getByRole('button', { name: 'Войти', exact: true }).click()
    await expect(page.getByText('Неверный логин или пароль')).toBeVisible()
    await login(page, 'admin', mine)
    await expect(page.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
    await expect(page.getByRole('dialog', { name: 'Смените пароль' })).toHaveCount(0)
  } finally {
    await context?.close()
    await server.stop()
  }
})

test('an admin creates a local teacher who signs in and has no admin pages', async ({ page, browser, baseURL }) => {
  const username = `teacher_${crypto.randomBytes(3).toString('hex')}`
  const password = `Tmp-${crypto.randomBytes(6).toString('hex')}`

  await page.goto('/admin/users')
  await page.getByRole('button', { name: 'Новый пользователь' }).click()
  const dialog = page.getByRole('dialog', { name: 'Новый пользователь' })
  await dialog.getByLabel('Логин').fill(username)
  await dialog.getByLabel('Имя и фамилия').fill('Мария Учительница')
  await dialog.getByRole('radio', { name: /^Преподаватель/ }).click()
  await dialog.getByLabel('Пароль', { exact: true }).fill(password)
  await dialog.getByRole('button', { name: 'Создать', exact: true }).click()

  const created = page.getByRole('dialog', { name: 'Пользователь создан' })
  await expect(created).toContainText(username)
  await expect(created).toContainText(password)
  await expect(created).toContainText('При первом входе система попросит придумать новый пароль.')
  await created.getByRole('button', { name: 'Готово' }).click()
  const row = page.getByRole('row').filter({ hasText: username })
  await expect(row).toContainText('Мария Учительница')
  await expect(row).toContainText('Преподаватель')
  await expect(row).toContainText('Локальный')
  await expect(row).toContainText('Ещё не входил')

  // The teacher signs in; a password set by an admin is temporary.
  const context = await freshContext(browser, baseURL!)
  try {
    const tp = await context.newPage()
    await login(tp, username, password)
    await changeForcedPassword(tp, password, `${password}-own`)
    await expect(tp.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
    await expect(tp.getByRole('button', { name: 'Новое занятие' }).first()).toBeVisible()
    await expect(tp.getByText('Нет доступа')).toHaveCount(0)
    await expect(tp.getByRole('navigation', { name: 'Основное меню' }).getByRole('button', { name: 'Админ' })).toHaveCount(0)
    await tp.goto('/admin/users')
    await expect(tp.getByText('Этот раздел доступен только администраторам')).toBeVisible()
    expect((await tp.request.get('/api/users')).status()).toBe(403)
  } finally {
    await context.close()
  }

  await page.reload()
  await expect(page.getByRole('row').filter({ hasText: username })).not.toContainText('Ещё не входил')
})

test('the settings page saves (and keeps) a changed value', async ({ page }) => {
  await page.goto('/admin/settings')
  await expect(page.getByRole('heading', { level: 1, name: 'Настройки' })).toBeVisible()
  const windowHours = page.getByLabel('Окно привязки конференции к занятию, часов')
  const save = page.getByRole('button', { name: 'Сохранить', exact: true })
  // Default: a meeting may start at most 1 hour before its lesson (docs/architecture.md §5.5).
  await expect(windowHours).toHaveValue('1')
  await expect(save).toBeDisabled()

  // Only the link window changes, and only a little: other rules would alter lessons of tests
  // running in parallel.
  await windowHours.fill('2')
  await expect(save).toBeEnabled()
  await save.click()
  await expect(page.getByText('Настройки сохранены. Посещаемость недавних занятий пересчитывается.')).toBeVisible()
  await expect(save).toBeDisabled()

  await page.reload()
  await expect(windowHours).toHaveValue('2')
  const api = await (await page.request.get('/api/settings')).json()
  expect(api.link_window_hours).toBe(2)

  // A wrong value is explained and not saved.
  await windowHours.fill('100')
  await save.click()
  await expect(page.getByText(/от 1 до 72/)).toBeVisible()

  await windowHours.fill('1')
  await save.click()
  await expect(page.getByText('Настройки сохранены. Посещаемость недавних занятий пересчитывается.').first()).toBeVisible()
  await page.reload()
  await expect(windowHours).toHaveValue('1')
})

// Logs in as the admin once through the login form; the specs reuse the session.

import fs from 'node:fs'
import path from 'node:path'

import { expect, test } from './fixtures'
import { AUTH_FILE } from './helpers/env'
import { login } from './helpers/ui'

test('admin logs in through the login form', async ({ page }) => {
  await login(page)
  await expect(page.getByRole('heading', { level: 1, name: 'Занятия' })).toBeVisible()
  fs.mkdirSync(path.dirname(AUTH_FILE), { recursive: true })
  await page.context().storageState({ path: AUTH_FILE })
})

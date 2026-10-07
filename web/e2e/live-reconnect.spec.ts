// Live updates survive a server restart: the open lesson page notices the
// broken stream, keeps its data, falls back to polling and goes live again by
// itself. Uses its own server instance (the shared one keeps serving the
// other specs).

import crypto from 'node:crypto'
import path from 'node:path'

import { expect, test } from './fixtures'
import { ADMIN_PASSWORD, runDir } from './helpers/env'
import { recordMetric } from './helpers/metrics'
import { liveLesson } from './helpers/scenario'
import { startServer, type RunningServer } from './helpers/server'
import { expectStreamLive, freshContext, login, studentRow } from './helpers/ui'
import { participant, participantJoined, sendEvent } from './helpers/zoom'

test('kill -9 and restart of the server: the live page shows «Подключаемся…» and recovers by itself', async ({
  browser,
}, testInfo) => {
  test.setTimeout(120_000)
  const dataDir = path.join(runDir(), `restart-${crypto.randomBytes(4).toString('hex')}`)
  let server: RunningServer = await startServer({ dataDir, adminPassword: ADMIN_PASSWORD })
  const port = server.port
  const context = await freshContext(browser, server.url)
  try {
    const page = await context.newPage()
    await login(page)
    const { meeting } = await liveLesson(page, 'Перезапуск', ['Иванов Иван', 'Петрова Анна'], { baseUrl: server.url })
    await sendEvent(participantJoined(meeting, participant('Иванов Иван'), new Date()), { baseUrl: server.url })
    await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')

    await server.stop('SIGKILL')
    await expect(page.getByText('Подключаемся…', { exact: true })).toBeVisible({ timeout: 10_000 })
    // What was on screen stays there while the server is away.
    await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')
    await expect(page.getByText('Обновление раз в 5 секунд', { exact: true })).toBeVisible({ timeout: 20_000 })

    server = await startServer({ dataDir, port, adminPassword: ADMIN_PASSWORD })
    const upAt = Date.now()
    await expectStreamLive(page, 30_000)
    recordMetric(testInfo, 'live page back to «Обновляется автоматически» after restart', Date.now() - upAt)

    // Really live again: a new join shows up without a reload.
    await sendEvent(participantJoined(meeting, participant('Петрова Анна'), new Date()), { baseUrl: server.url })
    await expect(studentRow(page, 'Петрова Анна')).toHaveAttribute('data-status', 'online', { timeout: 5_000 })
    await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')
  } finally {
    await context.close()
    await server.stop()
  }
})

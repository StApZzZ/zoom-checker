// `zoom-checker simulate` (the demo/test webhook player) against the real server:
// a 30-minute lesson played at ×120 (15 s) while the live screen is open.

import { spawn } from 'node:child_process'
import crypto from 'node:crypto'

import { expect, test } from './fixtures'
import { BIN, WEBHOOK_SECRET } from './helpers/env'
import {
  createGroupByPaste,
  createLesson,
  lessonStatusBadge,
  minutesAgo,
  openLesson,
  statValue,
  studentRow,
  uniq,
  unidentifiedItem,
} from './helpers/ui'
import { randomMeetingId } from './helpers/zoom'

/** cmd/zoom-checker/simulate.go defaultSimNames, in role order. */
const SIM_NAMES = [
  'Иванов Иван', // joins as "  ИВАНОВ   Иван "
  'Петрова Анна', // leaves and rejoins twice
  'Сидоров Алексей', // two devices
  'Смирнова Мария', // breakout room and back
  'Кузнецов Дмитрий', // never joins
  'Попова Екатерина', // late
  'Васильев Михаил', // late
  'Новикова Ольга', // leaves early
  'Фёдоров Артём',
  'Морозова Дарья',
  'Волков Никита',
  'Соколова Алёна',
]

interface SimRun {
  done: Promise<number>
  output: () => string
}

function runSimulator(args: string[]): SimRun {
  let out = ''
  const child = spawn(BIN, ['simulate', ...args], {
    env: { PATH: process.env.PATH ?? '/usr/bin:/bin', HOME: process.env.HOME ?? '/tmp' },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child.stdout.on('data', (d: Buffer) => (out += d.toString()))
  child.stderr.on('data', (d: Buffer) => (out += d.toString()))
  const done = new Promise<number>((resolve, reject) => {
    child.on('error', reject)
    child.on('exit', (code) => resolve(code ?? -1))
  })
  return { done, output: () => out }
}

test('simulate --speed 120 --duration 30m: the live screen follows the lesson to its end', async ({
  page,
  baseURL,
}, testInfo) => {
  test.setTimeout(120_000)
  const group = await createGroupByPaste(page, uniq('Симулятор'), SIM_NAMES)
  const meetingId = randomMeetingId()
  const lesson = await createLesson(page, group, meetingId, minutesAgo(0), { durationMinutes: 30 })
  await openLesson(page, lesson)
  await expect(statValue(page, 'В группе')).toHaveText('12')

  const seed = process.env.E2E_SIM_SEED || String(crypto.randomInt(1, 2 ** 31))
  testInfo.annotations.push({ type: 'simulate seed', description: seed })
  const sim = runSimulator([
    '--url',
    `${baseURL}/api/webhooks/zoom`,
    '--secret',
    WEBHOOK_SECRET,
    '--meeting',
    meetingId,
    '--speed',
    '120',
    '--duration',
    '30m',
    '--seed',
    seed,
  ])
  let exitCode: number | null = null
  void sim.done.then((c) => (exitCode = c))

  try {
    // During the lesson: people are in Zoom, the unidentified ones are listed.
    await expect
      .poll(async () => Number(await statValue(page, 'Сейчас в Zoom').textContent()), { timeout: 10_000 })
      .toBeGreaterThan(0)
    await expect(lessonStatusBadge(page, 'Идёт')).toBeVisible()
    await expect(unidentifiedItem(page, 'iPhone')).toBeVisible()
    await expect(unidentifiedItem(page, 'Ваня')).toBeVisible()
    await expect(studentRow(page, 'Иванов Иван')).not.toHaveAttribute('data-status', 'not_seen')
    expect(exitCode, 'the checks above must happen while the simulation is still running').toBeNull()

    const code = await sim.done
    expect(code, sim.output()).toBe(0)
    expect(sim.output()).toMatch(/с ошибкой: 0\./)
  } finally {
    await testInfo.attach('simulate output', { body: sim.output(), contentType: 'text/plain' })
  }

  // After meeting.ended: finished, nobody online, every interval closed.
  await expect(lessonStatusBadge(page, 'Завершено')).toBeVisible({ timeout: 10_000 })
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('0')
  await expect(page.locator('tbody tr[data-status="online"]')).toHaveCount(0)
  await expect(statValue(page, 'Были хотя бы раз')).toHaveText('11')
  await expect(statValue(page, 'Не подключались')).toHaveText('1')
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('2')
  const never = studentRow(page, 'Кузнецов Дмитрий')
  await expect(never).toHaveAttribute('data-status', 'not_seen')
  await expect(never.getByRole('radio', { name: 'Не был', exact: true })).toBeChecked()
  for (const name of ['iPhone', 'Ваня']) {
    await expect(unidentifiedItem(page, name)).toContainText('Вышел')
  }
  // The host is hidden by the default "не учитывать организатора" rule.
  await expect(page.getByRole('button', { name: 'Скрытые участники (1)' })).toBeVisible()

  const view = await (await page.request.get(`/api/lessons/${lesson.id}/attendance`)).json()
  expect(view.lesson.status).toBe('finished')
  for (const s of view.students as { student_id: number; full_name: string; current_status: string; first_join_at: string | null }[]) {
    expect(s.current_status, s.full_name).not.toBe('online')
    if (!s.first_join_at) continue
    const hist = await (await page.request.get(`/api/lessons/${lesson.id}/students/${s.student_id}/history`)).json()
    for (const iv of hist.intervals as { leave_at: string | null }[]) expect(iv.leave_at, s.full_name).not.toBeNull()
  }
  const participants = (await (await page.request.get(`/api/lessons/${lesson.id}/participants`)).json()) as {
    display_name: string
    current_status: string
  }[]
  for (const p of participants) expect(p.current_status, p.display_name).toBe('left')

  // The simulator delivers one event twice: the journal marks exactly one repeat.
  const events = await (await page.request.get(`/api/zoom/events?q=${meetingId}&status=duplicate&limit=50`)).json()
  expect(events.items).toHaveLength(1)
})

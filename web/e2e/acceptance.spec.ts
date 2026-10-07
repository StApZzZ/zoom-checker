// TZ §36 acceptance scenarios, one test each, against the real binary. Zoom is
// played by signed webhooks; everything the TZ describes as UI is checked in the UI.

import type { Page } from '@playwright/test'

import { expect, test } from './fixtures'
import { exportCell, parseExportCsv, readXlsxSheet, readZipEntry } from './helpers/files'
import { recordMetric } from './helpers/metrics'
import { liveLesson } from './helpers/scenario'
import {
  download,
  durationCell,
  lessonAction,
  lessonStatusBadge,
  localTime,
  statValue,
  studentRow,
  unidentifiedBlock,
  unidentifiedItem,
} from './helpers/ui'
import {
  addMinutes,
  meetingEnded,
  participant,
  participantJoined,
  participantLeft,
  postWebhookBody,
  sendEvent,
  sendEvents,
  sendWebhook,
  webhookBody,
} from './helpers/zoom'

declare global {
  interface Window {
    __shownAt?: number
  }
}

/** Records (in the page) the moment the student's row shows "В Zoom". */
async function watchOnline(page: Page, fullName: string): Promise<void> {
  await page.evaluate((name) => {
    delete window.__shownAt
    const check = () => {
      for (const tr of document.querySelectorAll('tbody tr[data-student-id]')) {
        const btn = tr.querySelector('td button')
        if (btn?.textContent?.trim() !== name) continue
        if (tr.getAttribute('data-status') === 'online' && tr.textContent?.includes('В Zoom')) {
          window.__shownAt = Date.now()
          observer.disconnect()
        }
      }
    }
    const observer = new MutationObserver(check)
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true })
    check()
  }, fullName)
}

/** Opens the student's side sheet. */
async function openStudentSheet(page: Page, fullName: string) {
  await studentRow(page, fullName).getByRole('button', { name: fullName, exact: true }).click()
  const sheet = page.getByRole('dialog', { name: fullName })
  await expect(sheet).toBeVisible()
  return sheet
}

/** The "Входы и выходы" list of an open student sheet. */
function intervalItems(sheet: ReturnType<Page['getByRole']>) {
  return sheet.getByRole('region', { name: 'Входы и выходы' }).getByRole('listitem')
}

test('S1: «Иванов Иван» joins → the live screen shows him «В Zoom» within 5 s', async ({ page }, testInfo) => {
  const { meeting } = await liveLesson(page, 'S1', ['Иванов Иван', 'Петрова Анна'])
  const row = studentRow(page, 'Иванов Иван')
  await expect(row).toHaveAttribute('data-status', 'not_seen')
  await expect(row.getByText('Не подключался')).toBeVisible()

  await watchOnline(page, 'Иванов Иван')
  const sentAt = Date.now()
  await sendEvent(participantJoined(meeting, participant('Иванов Иван'), new Date()))
  await page.waitForFunction(() => window.__shownAt !== undefined, undefined, { timeout: 5_000 })
  const latency = (await page.evaluate(() => window.__shownAt!)) - sentAt
  recordMetric(testInfo, 'S1 webhook POST → «В Zoom» badge', latency)
  expect(latency).toBeLessThan(5_000)

  await expect(row.getByText('В Zoom')).toBeVisible()
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('1')
  await expect(studentRow(page, 'Петрова Анна')).toHaveAttribute('data-status', 'not_seen')
})

test('S2: «  ИВАНОВ   ИВАН » is identified as «Иванов Иван»', async ({ page }) => {
  const { meeting } = await liveLesson(page, 'S2', ['Иванов Иван', 'Петрова Анна'])
  await sendEvent(participantJoined(meeting, participant('  ИВАНОВ   ИВАН '), new Date()))

  const row = studentRow(page, 'Иванов Иван')
  await expect(row).toHaveAttribute('data-status', 'online')
  await expect(row.getByText('В Zoom')).toBeVisible()
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('0')
  await expect(unidentifiedBlock(page).getByText('Все участники Zoom опознаны')).toBeVisible()

  const sheet = await openStudentSheet(page, 'Иванов Иван')
  await expect(sheet.getByText('ИВАНОВ   ИВАН', { exact: false }).first()).toBeVisible()
})

test('S3: «Ваня» lands in «Не идентифицированы» and nobody is counted present', async ({ page }) => {
  const { meeting } = await liveLesson(page, 'S3', ['Иванов Иван', 'Петрова Анна'])
  await sendEvent(participantJoined(meeting, participant('Ваня'), new Date()))

  const item = unidentifiedItem(page, 'Ваня')
  await expect(item).toBeVisible()
  await expect(item.getByText('В Zoom')).toBeVisible()
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('1')
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('0')
  await expect(statValue(page, 'Были хотя бы раз')).toHaveText('0')
  for (const name of ['Иванов Иван', 'Петрова Анна']) {
    const row = studentRow(page, name)
    await expect(row).toHaveAttribute('data-status', 'not_seen')
    await expect(row.getByRole('radio', { name: 'Не был', exact: true })).toBeChecked()
  }
  await expect(page.getByText(/Итог сейчас: были — 0/)).toBeVisible()
})

test('S4: joins 18:10, leaves 18:40, joins 18:50, leaves 19:30 → 70 minutes on screen and in the export', async ({
  page,
}) => {
  // Lesson planned now-3h … now+1h; T0 plays 18:00.
  const { meeting, start: t0 } = await liveLesson(page, 'S4', ['Иванов Иван', 'Петрова Анна'], {
    startedMinutesAgo: 180,
    durationMinutes: 240,
  })
  const first = participant('Иванов Иван')
  const second = participant('Иванов Иван') // a rejoin gets a new participant_uuid
  await sendEvents([
    participantJoined(meeting, first, addMinutes(t0, 10)),
    participantLeft(meeting, first, addMinutes(t0, 40)),
    participantJoined(meeting, second, addMinutes(t0, 50)),
    participantLeft(meeting, second, addMinutes(t0, 90)),
  ])

  const row = studentRow(page, 'Иванов Иван')
  await expect(row).toHaveAttribute('data-status', 'left')
  await expect(row.getByText('Вышел')).toBeVisible()
  await expect(durationCell(row)).toHaveText('01:10')
  await expect(durationCell(row)).toHaveAttribute('aria-label', '1 ч 10 мин')

  const sheet = await openStudentSheet(page, 'Иванов Иван')
  const items = intervalItems(sheet)
  await expect(items).toHaveCount(2)
  await expect(items.nth(0)).toContainText(`${localTime(addMinutes(t0, 10))} – ${localTime(addMinutes(t0, 40))}`)
  await expect(items.nth(0)).toContainText('30 мин')
  await expect(items.nth(1)).toContainText(`${localTime(addMinutes(t0, 50))} – ${localTime(addMinutes(t0, 90))}`)
  await expect(items.nth(1)).toContainText('40 мин')
  // A rejoin gets a new participant_uuid from Zoom, but it is not a second device.
  await expect(sheet.getByText(/^устройство \d$/)).toHaveCount(0)
  await page.keyboard.press('Escape')

  const csv = await download(page, () => lessonAction(page, 'CSV'))
  const { rows } = parseExportCsv(csv.body)
  const ivanov = rows.find((r) => r['ФИО'] === 'Иванов Иван')
  expect(ivanov?.['Суммарное время, мин']).toBe('70')
  expect(exportCell(ivanov, 'Первый вход')).toBe(localTime(addMinutes(t0, 10)))
  expect(exportCell(ivanov, 'Последний выход')).toBe(localTime(addMinutes(t0, 90)))
})

test('S5: two devices with overlap (PC 0–60, phone 30–90) → 90 minutes, not 120', async ({ page }) => {
  const { meeting, start: t0 } = await liveLesson(page, 'S5', ['Сидоров Алексей', 'Петрова Анна'], {
    startedMinutesAgo: 180,
    durationMinutes: 240,
  })
  const pc = participant('Сидоров Алексей')
  const phone = participant('Сидоров Алексей')
  await sendEvents([
    participantJoined(meeting, pc, t0),
    participantJoined(meeting, phone, addMinutes(t0, 30)),
  ])
  const row = studentRow(page, 'Сидоров Алексей')
  await expect(row.getByText('2 устройства')).toBeVisible()

  await sendEvents([participantLeft(meeting, pc, addMinutes(t0, 60)), participantLeft(meeting, phone, addMinutes(t0, 90))])
  await expect(row).toHaveAttribute('data-status', 'left')
  await expect(durationCell(row)).toHaveText('01:30')

  const sheet = await openStudentSheet(page, 'Сидоров Алексей')
  await expect(intervalItems(sheet)).toHaveCount(2)
  await expect(intervalItems(sheet).nth(0)).toContainText('устройство 1')
  await expect(intervalItems(sheet).nth(1)).toContainText('устройство 2')
  await expect(sheet.getByRole('timer').first()).toHaveText('1 ч 30 мин')
  await page.keyboard.press('Escape')

  const csv = await download(page, () => lessonAction(page, 'CSV'))
  const sidorov = parseExportCsv(csv.body).rows.find((r) => r['ФИО'] === 'Сидоров Алексей')
  expect(sidorov?.['Суммарное время, мин']).toBe('90')
})

test('S6: the same webhook delivered again (also with a new event_ts) → one interval, one presence', async ({
  page,
}) => {
  const { meeting, start: t0, lesson } = await liveLesson(page, 'S6', ['Иванов Иван', 'Петрова Анна'], {
    startedMinutesAgo: 60,
  })
  const p = participant('Иванов Иван')
  const join = participantJoined(meeting, p, addMinutes(t0, 5))
  const body = webhookBody(join.type, join.payload, join.eventTs)
  await postWebhookBody(body)
  await postWebhookBody(body) // Zoom retry: identical bytes
  await sendWebhook(join.type, join.payload, { eventTs: join.eventTs + 61_000 }) // redelivery, new event_ts
  await sendEvent(participantLeft(meeting, p, addMinutes(t0, 35)))
  const row = studentRow(page, 'Иванов Иван')
  await expect(row).toHaveAttribute('data-status', 'left')
  await expect(durationCell(row)).toHaveText('00:30')

  // A retry can also arrive late, after the leave (Zoom retries when its first
  // delivery timed out): it must not reopen the interval. Wait until the server
  // has processed it before looking at the screen again.
  await postWebhookBody(body)
  const joinStatuses = async () => {
    const res = await page.request.get(`/api/zoom/events?q=${meeting.id}&type=meeting.participant_joined&limit=50`)
    expect(res.ok()).toBeTruthy()
    const { items } = (await res.json()) as { items: { processing_status: string }[] }
    return items.map((e) => e.processing_status).sort()
  }
  // The raw journal keeps every delivery; all but the first are marked as repeats.
  await expect.poll(joinStatuses).toEqual(['duplicate', 'duplicate', 'duplicate', 'processed'])
  const view = await (await page.request.get(`/api/lessons/${lesson.id}/attendance`)).json()
  const ivanov = (view.students as { student_id: number; full_name: string; current_status: string; total_seconds: number }[]).find(
    (s) => s.full_name === 'Иванов Иван',
  )!
  expect(ivanov.current_status).toBe('left')
  expect(ivanov.total_seconds).toBe(30 * 60)
  const hist = await (await page.request.get(`/api/lessons/${lesson.id}/students/${ivanov.student_id}/history`)).json()
  expect(hist.intervals).toHaveLength(1)

  await expect(row).toHaveAttribute('data-status', 'left')
  await expect(durationCell(row)).toHaveText('00:30')
  await expect(statValue(page, 'Были хотя бы раз')).toHaveText('1')
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('0')

  const sheet = await openStudentSheet(page, 'Иванов Иван')
  await expect(intervalItems(sheet)).toHaveCount(1)
  await expect(intervalItems(sheet).first()).toContainText('30 мин')
})

test('S7: meeting.ended closes every open interval at the end time; students «Вышел», lesson «Завершено»', async ({
  page,
}) => {
  const names = ['Иванов Иван', 'Петрова Анна', 'Сидоров Алексей']
  const { meeting, start: t0, lesson } = await liveLesson(page, 'S7', names, { startedMinutesAgo: 120, durationMinutes: 180 })
  await sendEvents([
    participantJoined(meeting, participant('Иванов Иван'), addMinutes(t0, 2)),
    participantJoined(meeting, participant('Петрова Анна'), addMinutes(t0, 5)),
  ])
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('2')

  const end = addMinutes(t0, 62)
  await sendEvent(meetingEnded(meeting, end))

  await expect(lessonStatusBadge(page, 'Завершено')).toBeVisible()
  // The date is added when the end falls on another day than the planned start.
  await expect(
    page.getByText(new RegExp(`Занятие завершено (\\d\\d\\.\\d\\d\\.\\d{4} )?в ${localTime(end)}\\.`)),
  ).toBeVisible()
  await expect(statValue(page, 'Сейчас в Zoom')).toHaveText('0')
  for (const [name, minutes] of [
    ['Иванов Иван', '01:00'],
    ['Петрова Анна', '00:57'],
  ] as const) {
    const row = studentRow(page, name)
    await expect(row).toHaveAttribute('data-status', 'left')
    await expect(row.getByText('Вышел')).toBeVisible()
    await expect(durationCell(row)).toHaveText(minutes)
  }
  await expect(studentRow(page, 'Сидоров Алексей')).toHaveAttribute('data-status', 'not_seen')

  // Every interval is closed exactly at meeting.ended.
  const view = await (await page.request.get(`/api/lessons/${lesson.id}/attendance`)).json()
  for (const s of view.students as { student_id: number; full_name: string; first_join_at: string | null }[]) {
    if (!s.first_join_at) continue
    const hist = await (await page.request.get(`/api/lessons/${lesson.id}/students/${s.student_id}/history`)).json()
    expect(hist.intervals.length, s.full_name).toBeGreaterThan(0)
    for (const iv of hist.intervals as { leave_at: string | null; leave_reason: string }[]) {
      expect(iv.leave_at, s.full_name).not.toBeNull()
      expect(Date.parse(iv.leave_at!), s.full_name).toBe(end.getTime())
      expect(iv.leave_reason).toBe('meeting_ended')
    }
  }
})

test('S8: the teacher maps «Александр» to «Александров Александр» → his time moves to the student', async ({
  page,
}) => {
  const { meeting, start: t0 } = await liveLesson(page, 'S8', ['Александров Александр', 'Петрова Анна'], {
    startedMinutesAgo: 90,
  })
  const p = participant('Александр')
  await sendEvents([participantJoined(meeting, p, addMinutes(t0, 3)), participantLeft(meeting, p, addMinutes(t0, 48))])

  const item = unidentifiedItem(page, 'Александр')
  await expect(item).toBeVisible()
  await expect(item.getByRole('timer')).toHaveText('00:45')
  const student = studentRow(page, 'Александров Александр')
  await expect(student).toHaveAttribute('data-status', 'not_seen')

  await item.getByRole('button', { name: 'Это студент…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Кто это — «Александр»?' })
  await dialog.getByPlaceholder('Начните вводить фамилию').fill('Алекс')
  await dialog.getByRole('option', { name: /Александров Александр/ }).click()
  await expect(dialog.getByText('Выбран: Александров Александр')).toBeVisible()
  await dialog.getByRole('button', { name: 'Сопоставить' }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText('Время присутствия перенесено студенту Александров Александр')).toBeVisible()

  await expect(student).toHaveAttribute('data-status', 'left')
  await expect(durationCell(student)).toHaveText('00:45')
  await expect(student.getByRole('radio', { name: 'Был', exact: true })).toBeChecked()
  await expect(unidentifiedItem(page, 'Александр')).toHaveCount(0)
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('0')
})

test('S9: after the lesson ends, CSV and XLSX exports list every student, also those who never joined', async ({
  page,
}) => {
  const names = ['Иванов Иван', 'Петрова Анна', 'Сидоров Алексей', 'Фёдоров Артём']
  const { meeting, start: t0, group, lesson } = await liveLesson(page, 'S9', names, { startedMinutesAgo: 60 })
  const p = participant('Петрова Анна')
  await sendEvents([participantJoined(meeting, p, addMinutes(t0, 1)), participantLeft(meeting, p, addMinutes(t0, 31))])
  await sendEvent(meetingEnded(meeting, addMinutes(t0, 40)))
  await expect(lessonStatusBadge(page, 'Завершено')).toBeVisible()

  const banner = page.getByRole('region', { name: 'Занятие завершено' })
  const csv = await download(page, () => banner.getByRole('button', { name: 'Скачать CSV' }).click())
  expect(csv.name).toMatch(/\.csv$/)
  const parsed = parseExportCsv(csv.body)
  expect(parsed.bom).toBe(true)
  expect(parsed.header.slice(0, 2)).toEqual(['ФИО', 'ID студента'])
  expect(parsed.header).toContain('Суммарное время, мин')
  expect(parsed.rows.map((r) => r['ФИО']).sort()).toEqual([...names].sort())
  for (const r of parsed.rows) {
    expect(r['Группа']).toBe(group.name)
    expect(r['Занятие']).toBe(lesson.title)
    if (r['ФИО'] === 'Петрова Анна') {
      expect(r['Суммарное время, мин']).toBe('30')
      expect(r['Статус']).toBe('присутствовал')
      expect(r['Код статуса']).toBe('present')
    } else {
      expect(r['Суммарное время, мин'], r['ФИО']).toBe('0')
      expect(r['Статус'], r['ФИО']).toBe('отсутствовал')
      expect(r['Код статуса'], r['ФИО']).toBe('absent')
      expect(exportCell(r, 'Первый вход'), r['ФИО']).toBe('')
    }
  }

  const xlsx = await download(page, () => banner.getByRole('button', { name: 'Скачать Excel' }).click())
  expect(xlsx.name).toMatch(/\.xlsx$/)
  expect(xlsx.body.subarray(0, 2).toString('latin1')).toBe('PK')
  expect(xlsx.body.length).toBeGreaterThan(4_000)
  const shared = readZipEntry(xlsx.body, 'xl/sharedStrings.xml')?.toString('utf8') ?? ''
  for (const n of names) expect(shared, n).toContain(n)
  // The «Посещаемость» sheet itself: one row per student of the group, the same
  // numbers as the CSV (names alone could also come from another sheet).
  const [head = [], ...sheetRows] = readXlsxSheet(xlsx.body, 'Посещаемость')
  expect(head.slice(0, 2)).toEqual(['ФИО', 'ID студента'])
  const minutesCol = head.indexOf('Суммарное время, мин')
  const codeCol = head.indexOf('Код статуса')
  expect(minutesCol).toBeGreaterThan(0)
  expect(codeCol).toBeGreaterThan(0)
  expect(sheetRows.map((r) => r[0]).sort()).toEqual([...names].sort())
  for (const r of sheetRows) {
    const present = r[0] === 'Петрова Анна'
    expect(r[minutesCol], r[0]).toBe(present ? '30' : '0')
    expect(r[codeCol], r[0]).toBe(present ? 'present' : 'absent')
  }
})

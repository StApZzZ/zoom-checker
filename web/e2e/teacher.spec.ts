// What a teacher does around a lesson: groups and imports, lessons from a Zoom
// invitation, manual marks, comments, hiding participants, finishing, reports,
// and the Zoom status / raw event journal.

import { expect, test } from './fixtures'
import { encodeCp1251 } from './helpers/files'
import { liveLesson } from './helpers/scenario'
import {
  createGroupByPaste,
  createLesson,
  lessonAction,
  lessonStatusBadge,
  minutesAgo,
  openLesson,
  statValue,
  studentRow,
  uniq,
  unidentifiedBlock,
  unidentifiedItem,
} from './helpers/ui'
import {
  addMinutes,
  formatMeetingId,
  newMeeting,
  participant,
  participantJoined,
  participantLeft,
  sendEvent,
  sendEvents,
} from './helpers/zoom'

const TZ8_MESSAGE = 'Невозможно однозначно идентифицировать студентов по Zoom display name.'

test('a pasted list with a repeated name shows the TZ §8 error and cannot be saved', async ({ page }) => {
  const name = uniq('Дубли')
  await page.goto('/groups')
  await page.getByRole('button', { name: 'Новая группа' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Новая группа' })
  await dialog.getByLabel('Название группы').fill(name)
  await dialog.getByLabel(/Вставьте список студентов/).fill('Иванов Иван\nПетрова Анна\n  ИВАНОВ   иван ')
  await dialog.getByRole('button', { name: 'Создать группу и добавить студентов' }).click()

  const check = page.getByRole('dialog', { name: 'Проверьте список студентов' })
  const blocked = check.getByTestId('import-blocked')
  await expect(blocked).toBeVisible()
  await expect(blocked).toContainText(TZ8_MESSAGE)
  await expect(blocked).toContainText('Строки с проблемами: 1, 3')
  await expect(check.getByRole('button', { name: 'Сохранить список' })).toBeDisabled()
  await expect(check.locator('tr[data-problem]')).toHaveCount(2)

  // The group exists, the list was not saved.
  await check.getByRole('button', { name: 'Пропустить' }).click()
  await page.waitForURL(/\/groups\/\d+$/)
  await expect(page.getByRole('heading', { level: 1, name, exact: true })).toBeVisible()
  await expect(page.getByRole('tab', { name: /Студенты/ })).toHaveText(/Студенты\s*0/)
})

test('a line pasted twice by mistake is removed with «Убрать повтор», then the list saves', async ({ page }) => {
  const name = uniq('Повтор')
  await page.goto('/groups')
  await page.getByRole('button', { name: 'Новая группа' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Новая группа' })
  await dialog.getByLabel('Название группы').fill(name)
  // A blank line and messy spelling: line numbers and the name must still match the server's.
  await dialog.getByLabel(/Вставьте список студентов/).fill('Петрова Анна\nИванов Иван\n\n  петрова   АННА \nСидоров Алексей')
  await dialog.getByRole('button', { name: 'Создать группу и добавить студентов' }).click()

  const check = page.getByRole('dialog', { name: 'Проверьте список студентов' })
  const blocked = check.getByTestId('import-blocked')
  await expect(blocked).toContainText('Строки 1 и 4 — одно и то же имя «Петрова Анна»')
  await expect(blocked).toContainText('Если это один и тот же студент — удалите лишнюю строку.')
  await blocked.getByRole('button', { name: 'Убрать повтор «Петрова Анна»: удалить строку 4' }).click()

  await expect(blocked).toBeHidden()
  await expect(check.locator('tr[data-problem]')).toHaveCount(0)
  await check.getByRole('button', { name: 'Сохранить список' }).click()
  await page.waitForURL(/\/groups\/\d+$/)
  await expect(page.getByRole('tab', { name: /Студенты/ })).toHaveText(/Студенты\s*3/)
})

test('the import wizard reads a Windows-1251 CSV with ";" from Excel', async ({ page }) => {
  const group = await createGroupByPaste(page, uniq('Импорт'), [])
  const csv = encodeCp1251(
    ['№ зачётки;ФИО;Телефон', '2026-001;Иванов Иван;+7 900 000-00-01', '2026-002;Фёдоров Артём;', '2026-003;Соколова Алёна;'].join(
      '\r\n',
    ) + '\r\n',
  )
  expect(csv.includes(Buffer.from('Фёдоров'))).toBe(false) // really not UTF-8

  await page.getByRole('tab', { name: 'Импорт списка' }).click()
  await page.getByLabel('Файл со списком студентов').setInputFiles({
    name: 'Список группы.csv',
    mimeType: 'text/csv',
    buffer: csv,
  })
  await expect(page.getByText('Файл «Список группы.csv»')).toBeVisible()
  await expect(page.getByText(/Распознано: .*кодировка WINDOWS-1251 · разделитель: точка с запятой/)).toBeVisible()
  const preview = page.getByRole('table', { name: 'Результат проверки списка' })
  for (const [id, fio] of [
    ['2026-001', 'Иванов Иван'],
    ['2026-002', 'Фёдоров Артём'],
    ['2026-003', 'Соколова Алёна'],
  ]) {
    const row = preview.getByRole('row').filter({ hasText: fio })
    await expect(row).toContainText(id)
    await expect(row).toContainText('Новый')
  }
  await page.getByRole('button', { name: 'Сохранить список' }).click()

  await expect(page.getByRole('tab', { name: /Студенты/, selected: true })).toHaveText(/Студенты\s*3/)
  const table = page.getByRole('table').filter({ hasText: 'Фёдоров Артём' })
  await expect(table).toContainText('Иванов Иван')
  await expect(table).toContainText('Соколова Алёна')
  await expect(table).toContainText('2026-002')

  // The lesson screen lists them too (names exactly as in the file).
  const start = minutesAgo(5)
  const lesson = await createLesson(page, group, `https://zoom.us/j/${newMeeting(start).id}`, start)
  await openLesson(page, lesson)
  await expect(studentRow(page, 'Фёдоров Артём')).toBeVisible()
  await expect(statValue(page, 'В группе')).toHaveText('3')
})

test('a lesson is created from a whole Zoom invitation text', async ({ page }) => {
  const group = await createGroupByPaste(page, uniq('Приглашение'), ['Иванов Иван'])
  const start = minutesAgo(-60)
  const meeting = newMeeting(start)
  const formatted = formatMeetingId(meeting.id)
  const invitation = [
    'Мария Иванова приглашает вас на запланированную конференцию: Zoom.',
    '',
    'Тема: Математика — 10А',
    'Время: 7 окт. 2026 18:00 Москва',
    '',
    'Подключиться к конференции Zoom',
    `https://us05web.zoom.us/j/${meeting.id}?pwd=QWxhZGRpbjpvcGVuIHNlc2FtZQ.1`,
    '',
    `Идентификатор конференции: ${formatted}`,
    'Код доступа: 482913',
  ].join('\n')

  await page.goto('/')
  await page.getByRole('button', { name: 'Новое занятие' }).first().click()
  const dialog = page.getByRole('dialog', { name: 'Новое занятие' })
  await dialog.getByLabel('Ссылка Zoom или номер конференции').fill(invitation)
  await expect(dialog.getByText(`Конференция ${formatted}`)).toBeVisible()
  await page.keyboard.press('Escape')

  const lesson = await createLesson(page, group, invitation, start, { title: 'Математика по приглашению' })
  await expect(page.getByText('Zoom:').locator('..')).toContainText(formatted)
  await expect(page.getByRole('link', { name: 'Открыть в Zoom' })).toHaveAttribute(
    'href',
    new RegExp(`zoom\\.us/j/${meeting.id}`),
  )
  expect(lesson.title).toBe('Математика по приглашению')
  await expect(lessonStatusBadge(page, 'Запланировано')).toBeVisible()
})

test('manual «Был»/«Не был» with the «вручную» marker, back to automatic, and a comment', async ({ page }) => {
  const { meeting, start } = await liveLesson(page, 'Отметки', ['Иванов Иван', 'Петрова Анна'])
  await sendEvent(participantJoined(meeting, participant('Иванов Иван'), addMinutes(start, 1)))
  await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')

  const anna = studentRow(page, 'Петрова Анна')
  await expect(anna.getByRole('radio', { name: 'Не был', exact: true })).toBeChecked()
  await expect(anna.getByText('вручную')).toHaveCount(0)

  await anna.getByRole('radio', { name: 'Был', exact: true }).click()
  await expect(anna.getByRole('radio', { name: 'Был', exact: true })).toBeChecked()
  await expect(anna.getByText('вручную')).toBeVisible()
  await expect(page.getByText(/Итог сейчас: были — 2/)).toBeVisible()

  // Survives a reload: it is stored on the server.
  await page.reload()
  await expect(anna.getByRole('radio', { name: 'Был', exact: true })).toBeChecked()
  await expect(anna.getByText('вручную')).toBeVisible()

  await anna.getByRole('button', { name: 'Вернуть автоматическую отметку: Петрова Анна' }).click()
  await expect(page.getByText('Петрова Анна: отметка снова ставится автоматически')).toBeVisible()
  await expect(anna.getByRole('radio', { name: 'Не был', exact: true })).toBeChecked()
  await expect(anna.getByText('вручную')).toHaveCount(0)

  await anna.getByRole('button', { name: 'Добавить комментарий: Петрова Анна' }).click()
  const comment = page.getByRole('dialog').getByLabel('Комментарий · Петрова Анна')
  await comment.fill('Предупредила, что заболела')
  await page.getByRole('dialog').getByRole('button', { name: 'Сохранить' }).click()
  await expect(page.getByText('Комментарий сохранён')).toBeVisible()
  await expect(anna).toContainText('Предупредила, что заболела')
  await expect(anna.getByRole('button', { name: 'Комментарий: Петрова Анна' })).toBeVisible()

  // The student card keeps the history of these changes.
  await anna.getByRole('button', { name: 'Петрова Анна', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Петрова Анна' })
  const history = sheet.getByRole('region', { name: 'История изменений' })
  await expect(history.getByText('Изменена отметка о посещении')).toHaveCount(2)
  await expect(history.getByText('Изменён комментарий')).toBeVisible()
  await expect(sheet.getByRole('region', { name: 'Входы и выходы' })).toContainText('Входов в Zoom не было')
})

test('the student card shows join/leave intervals with reasons', async ({ page }) => {
  const { meeting, start } = await liveLesson(page, 'Карточка', ['Смирнова Мария', 'Петрова Анна'], {
    startedMinutesAgo: 60,
  })
  const first = participant('Смирнова Мария')
  const second = participant('Смирнова Мария')
  await sendEvents([
    participantJoined(meeting, first, addMinutes(start, 2)),
    participantLeft(meeting, first, addMinutes(start, 20), 'left the meeting. Reason : Connection lost'),
    participantJoined(meeting, second, addMinutes(start, 22)),
  ])
  const row = studentRow(page, 'Смирнова Мария')
  await expect(row).toHaveAttribute('data-status', 'online')
  await row.getByRole('button', { name: 'Смирнова Мария', exact: true }).click()
  const sheet = page.getByRole('dialog', { name: 'Смирнова Мария' })
  const intervals = sheet.getByRole('region', { name: 'Входы и выходы' }).getByRole('listitem')
  await expect(intervals).toHaveCount(2)
  await expect(intervals.nth(0)).toContainText('18 мин')
  await expect(intervals.nth(0)).toContainText('обрыв связи')
  await expect(intervals.nth(1)).toContainText('сейчас в Zoom')
  await expect(sheet.getByRole('region', { name: 'Имена в Zoom' })).toContainText('Смирнова Мария')
})

test('«Не студент» hides a participant; «Вернуть» brings him back', async ({ page }) => {
  const { meeting } = await liveLesson(page, 'Скрыть', ['Иванов Иван'])
  const guest = `Гость ${Date.now().toString(36)}`
  await sendEvent(participantJoined(meeting, participant(guest), new Date()))
  const item = unidentifiedItem(page, guest)
  await expect(item).toBeVisible()

  await item.getByRole('button', { name: 'Не студент' }).click()
  const confirm = page.getByRole('alertdialog', { name: `Скрыть «${guest}»?` })
  await expect(confirm.getByLabel('Всегда скрывать это имя')).not.toBeChecked()
  await confirm.getByRole('button', { name: 'Скрыть', exact: true }).click()
  await expect(page.getByText(`«${guest}» скрыт`, { exact: true })).toBeVisible()
  await expect(unidentifiedItem(page, guest)).toHaveCount(0)
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('0')

  const hidden = page.getByRole('button', { name: 'Скрытые участники (1)' })
  await hidden.click()
  const hiddenRow = page
    .getByRole('listitem')
    .filter({ hasText: `«${guest}»` })
    .filter({ has: page.getByRole('button', { name: 'Вернуть' }) })
  await expect(hiddenRow).toContainText('скрыт вручную')
  await hiddenRow.getByRole('button', { name: 'Вернуть' }).click()

  await expect(unidentifiedItem(page, guest)).toBeVisible()
  await expect(statValue(page, 'Не идентифицированы')).toHaveText('1')
  await expect(page.getByRole('button', { name: /Скрытые участники/ })).toHaveCount(0)
})

test('finish a lesson by hand, then reopen it', async ({ page }) => {
  const { meeting, start } = await liveLesson(page, 'Завершение', ['Иванов Иван', 'Петрова Анна'])
  await sendEvent(participantJoined(meeting, participant('Иванов Иван'), addMinutes(start, 1)))
  await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')

  await lessonAction(page, 'Завершить занятие')
  const confirm = page.getByRole('alertdialog', { name: 'Завершить занятие?' })
  await expect(confirm.getByLabel('Время окончания')).toHaveValue(/^\d\d:\d\d$/)
  await confirm.getByRole('button', { name: 'Завершить занятие' }).click()

  await expect(lessonStatusBadge(page, 'Завершено')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Занятие завершено' })).toBeVisible()
  const ivanov = studentRow(page, 'Иванов Иван')
  await expect(ivanov).toHaveAttribute('data-status', 'left')
  await expect(ivanov.getByRole('radio', { name: 'Был', exact: true })).toBeChecked()
  await expect(studentRow(page, 'Петрова Анна').getByRole('radio', { name: 'Не был', exact: true })).toBeChecked()
  await expect(page.getByText('Обновляется автоматически')).toHaveCount(0)

  await lessonAction(page, 'Возобновить')
  await expect(lessonStatusBadge(page, 'Идёт')).toBeVisible()
  await expect(page.getByRole('region', { name: 'Занятие завершено' })).toHaveCount(0)
  // Nobody left Zoom: reopening brings the open interval back.
  await expect(ivanov).toHaveAttribute('data-status', 'online')
})

test('the group report shows students × lessons', async ({ page }) => {
  const { meeting, start, group, lesson } = await liveLesson(page, 'Сводка', ['Иванов Иван', 'Петрова Анна'], {
    startedMinutesAgo: 60,
  })
  const p = participant('Иванов Иван')
  await sendEvents([participantJoined(meeting, p, addMinutes(start, 1)), participantLeft(meeting, p, addMinutes(start, 40))])
  await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'left')

  await page.goto(`/groups/${group.id}`)
  await page.getByRole('link', { name: 'Сводка посещаемости' }).click()
  await expect(page).toHaveURL(new RegExp(`/groups/${group.id}/report`))
  const allTime = page.getByRole('radiogroup', { name: 'Период отчёта' }).getByRole('radio', { name: 'Всё время' })
  await allTime.click()
  await expect(allTime).toBeChecked()
  const matrix = page.getByRole('table', { name: /Посещаемость: строки — студенты/ })
  await expect(matrix.getByRole('link', { name: new RegExp(`^${lesson.title},`) })).toBeVisible()
  const ivanov = matrix.getByRole('row').filter({ has: page.getByRole('rowheader', { name: 'Иванов Иван' }) })
  const anna = matrix.getByRole('row').filter({ has: page.getByRole('rowheader', { name: 'Петрова Анна' }) })
  await expect(ivanov.locator('td[data-status]')).toHaveAttribute('data-status', 'present')
  const ivanovCell = ivanov.getByRole('button', { name: new RegExp(`^Иванов Иван, ${lesson.title}, .*: Был · в Zoom 39 мин`) })
  await expect(ivanovCell).toBeVisible()
  await expect(anna.locator('td[data-status]')).toHaveAttribute('data-status', 'absent')
  await expect(anna.getByRole('button', { name: /: Не был$/ })).toBeVisible()
  await expect(ivanov).toContainText('1 из 1')
  await expect(anna).toContainText('0 из 1')

  // The details open with a click (a tap on a tablet), not only on mouse hover.
  await ivanovCell.click()
  const details = page.getByRole('dialog').filter({ hasText: 'Был · в Zoom 39 мин' })
  await expect(details).toContainText('Иванов Иван')
  await details.getByRole('link', { name: 'Открыть занятие' }).click()
  await expect(page).toHaveURL(new RegExp(`/lessons/${lesson.id}$`))
})

test('the Zoom page says «Zoom присылает события» once webhooks arrive', async ({ page }) => {
  const { meeting } = await liveLesson(page, 'Zoom', ['Иванов Иван'])
  await sendEvent(participantJoined(meeting, participant('Иванов Иван'), new Date()))
  await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')

  await page.getByRole('navigation', { name: 'Основное меню' }).getByRole('link', { name: 'Zoom' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'Zoom' })).toBeVisible()
  const card = page.locator('[data-slot="card"]').filter({ has: page.getByRole('heading', { name: 'Подключение к Zoom' }) })
  await expect(card.getByText('Zoom присылает события', { exact: true })).toBeVisible()
  await expect(card).toContainText(/Последнее событие: \S+/)
  await expect(page.getByTestId('webhook-url')).toContainText('/api/webhooks/zoom')
})

test('the admin event journal lists raw events and shows the payload', async ({ page }) => {
  const { meeting, start } = await liveLesson(page, 'Журнал', ['Иванов Иван'])
  const name = `Журнал Тест ${Date.now().toString(36)}`
  const p = participant(name)
  await sendEvents([participantJoined(meeting, p, addMinutes(start, 1)), participantLeft(meeting, p, addMinutes(start, 3))])
  await expect(unidentifiedItem(page, name)).toBeVisible()
  await expect(unidentifiedBlock(page)).toContainText('Вышел')

  await page.goto('/admin/events')
  await expect(page.getByRole('heading', { level: 1, name: 'Журнал событий' })).toBeVisible()
  await page.getByLabel('Поиск').fill(meeting.id)
  const events = page.getByRole('region', { name: 'События' })
  await expect(events.getByRole('row')).toHaveCount(4) // header + started + joined + left
  await expect(events.getByRole('row').filter({ hasText: name })).toHaveCount(2)
  await expect(events.getByRole('row').getByText('Обработано', { exact: true })).toHaveCount(3)

  await events.getByRole('button', { name: /^Вход участника, №\d+: подробнее$/ }).click()
  const sheet = page.getByRole('dialog', { name: 'Вход участника' })
  const payload = sheet.getByTestId('event-payload')
  await expect(payload).toContainText(`"user_name": "${name}"`)
  await expect(payload).toContainText(`"participant_uuid": "${p.uuid}"`)
  await expect(payload).toContainText('"event": "meeting.participant_joined"')
  await expect(sheet).toContainText(formatMeetingId(meeting.id))
})

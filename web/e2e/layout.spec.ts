// Teachers use laptops and tablets: nothing may be cut off or need
// sideways scrolling of the whole page at tablet sizes, nor on a small laptop
// (1280 px is the narrowest width with the side column next to the table).

import { expect, test } from './fixtures'
import { liveLesson } from './helpers/scenario'
import { studentRow } from './helpers/ui'
import { addMinutes, participant, participantJoined, sendEvent } from './helpers/zoom'

const TABLETS = [
  { name: 'iPad portrait', width: 768, height: 1024 },
  { name: 'iPad Air portrait', width: 820, height: 1180 },
  { name: 'iPad landscape', width: 1024, height: 768 },
  { name: 'small laptop', width: 1280, height: 800 },
]

for (const vp of TABLETS) {
  test(`${vp.name} (${vp.width}×${vp.height}): the live screen and the main pages fit`, async ({ page }) => {
    await page.setViewportSize({ width: vp.width, height: vp.height })
    const names = ['Иванов Иван', 'Константинопольская-Щедрина Анастасия-Мария', 'Петрова Анна']
    const { meeting, start, group } = await liveLesson(page, 'Планшет', names)
    await sendEvent(participantJoined(meeting, participant('Иванов Иван'), addMinutes(start, 1)))
    await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')

    // Every control of a row can be reached completely (the table may scroll sideways
    // inside its box, but must not cut columns off).
    for (const name of names) {
      const comment = studentRow(page, name).getByRole('button', { name: `Добавить комментарий: ${name}` })
      await comment.scrollIntoViewIfNeeded()
      await expect(comment).toBeInViewport({ ratio: 1 })
      await expect(studentRow(page, name).getByRole('radio', { name: 'Не был', exact: true })).toBeInViewport({ ratio: 1 })
    }
    if (vp.width < 1024) {
      // Below lg the table still fits its box at tablet width: a cut-off comment button
      // would need a sideways swipe nobody knows about (touch screens hide the scrollbar).
      const box = page.locator('table').filter({ has: page.getByRole('columnheader', { name: 'ФИО' }) }).locator('..')
      const { scrollWidth, clientWidth } = await box.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
      expect(scrollWidth, 'the student table scrolls sideways').toBeLessThanOrEqual(clientWidth)
    }
    await studentRow(page, 'Петрова Анна').getByRole('button', { name: 'Добавить комментарий: Петрова Анна' }).click()
    await expect(page.getByRole('dialog').getByLabel('Комментарий · Петрова Анна')).toBeVisible()
    await page.keyboard.press('Escape')

    for (const path of [null, '/', '/groups', `/groups/${group.id}`, `/groups/${group.id}/report`, '/zoom', '/admin/users', '/admin/events', '/help']) {
      if (path) await page.goto(path)
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
      const { scrollWidth, innerWidth } = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        innerWidth: window.innerWidth,
      }))
      expect(scrollWidth, `${path ?? 'lesson'}: the page scrolls sideways`).toBeLessThanOrEqual(innerWidth)
    }
  })
}

// Not a target device, but a teacher may glance at a phone: the page itself must not
// slide sideways (wide tables scroll inside their own box).
test('phone (390×844): no page scrolls sideways', async ({ page }) => {
  // Set up at the default size (the helpers use the desktop menu), then look at it on a phone.
  const { meeting, start, group } = await liveLesson(page, 'Телефон', ['Иванов Иван', 'Петрова Анна', 'Сидоров Алексей'])
  await sendEvent(participantJoined(meeting, participant('Иванов Иван'), addMinutes(start, 1)))
  await expect(studentRow(page, 'Иванов Иван')).toHaveAttribute('data-status', 'online')
  await page.setViewportSize({ width: 390, height: 844 })
  for (const path of [null, '/', `/groups/${group.id}`, `/groups/${group.id}/report`, '/zoom', '/help']) {
    if (path) await page.goto(path)
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    const { scrollWidth, innerWidth } = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      innerWidth: window.innerWidth,
    }))
    expect(scrollWidth, `${path ?? 'lesson'}: the page scrolls sideways`).toBeLessThanOrEqual(innerWidth)
  }
})

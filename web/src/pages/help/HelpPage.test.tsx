import { configure, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), info: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))
const copyMock = vi.hoisted(() => vi.fn(async (_text: string) => true))
vi.mock('@/lib/clipboard', () => ({ copyToClipboard: copyMock }))

import { HELP_SECTIONS, STUDENT_MEMO, STUDENT_RESPONSIBILITY_TEXT } from '@/pages/help/helpContent'
import { routes } from '@/routes'
import { makeUser, mockFetch, renderRoutes } from '@/test/utils'

// Pages are lazy routes: warm the module so the first findBy* does not race a cold transform.
configure({ asyncUtilTimeout: 5000 })
vi.setConfig({ testTimeout: 20_000 })
beforeAll(async () => {
  await import('@/pages/help/HelpPage')
})

beforeEach(() => {
  toastMock.success.mockReset()
  toastMock.error.mockReset()
  copyMock.mockClear()
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** TZ §33, copied here on purpose: the memo must not drift from the requirement. */
const TZ_33 =
  'Для автоматического учёта посещаемости необходимо указывать в Zoom имя и фамилию, соответствующие данным в списке учебной группы. Если имя участника не позволяет однозначно определить студента, система может не учитывать его присутствие. Ответственность за корректное отображаемое имя в Zoom лежит на участнике.'

function setup(path = '/help') {
  mockFetch({ 'GET /api/auth/me': makeUser({ role: 'teacher' }) })
  return renderRoutes(routes, path)
}

describe('help content', () => {
  it('keeps the student memo word for word as in TZ §33', () => {
    expect(STUDENT_RESPONSIBILITY_TEXT).toBe(TZ_33)
    expect(STUDENT_MEMO).toContain(TZ_33)
    expect(STUDENT_MEMO.startsWith('Памятка для студентов\n')).toBe(true)
  })

  it('has unique section anchors', () => {
    const ids = HELP_SECTIONS.map((s) => s.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})

describe('HelpPage', () => {
  it('renders a table of contents that points at every section', async () => {
    setup()
    const toc = await screen.findByRole('navigation', { name: 'Содержание' })
    const links = within(toc).getAllByRole('link')
    expect(links.map((a) => a.textContent)).toEqual(HELP_SECTIONS.map((s, i) => `${i + 1}.${s.title}`))

    for (const [i, s] of HELP_SECTIONS.entries()) {
      expect(links[i]).toHaveAttribute('href', `#${s.id}`)
      const section = document.getElementById(s.id)
      expect(section, s.id).not.toBeNull()
      // Each section is labelled by its own heading, so screen readers announce the same title.
      expect(within(section!).getByRole('heading', { level: 2, name: s.title })).toBeInTheDocument()
      expect(screen.getByRole('region', { name: s.title })).toBe(section)
    }
  })

  it('is available to teachers and sets the tab title', async () => {
    setup()
    expect(await screen.findByRole('heading', { level: 1, name: 'Справка' })).toBeInTheDocument()
    await waitFor(() => expect(document.title).toContain('Справка'))
  })

  it('illustrates statuses with the same badges and tiles as the lesson screen', async () => {
    setup()
    const during = await screen.findByRole('region', { name: 'Во время занятия' })
    const tiles = within(during).getByRole('group', { name: 'Пример счётчиков' })
    for (const label of ['В группе', 'Сейчас в Zoom', 'Были хотя бы раз', 'Не подключались', 'Не идентифицированы'])
      expect(within(tiles).getByText(label)).toBeInTheDocument()
    // Status is text + icon, never color alone.
    expect(within(during).getByText('В Zoom')).toBeInTheDocument()
    expect(within(during).getByText('Вышел')).toBeInTheDocument()
    expect(within(during).getByText('Не подключался')).toBeInTheDocument()
    expect(within(during).getByText(/Запомнить это имя для следующих занятий/)).toBeInTheDocument()
  })

  it('sends teachers to the Zoom page when events do not arrive', async () => {
    const user = userEvent.setup()
    setup()
    const faq = await screen.findByRole('region', { name: 'Частые вопросы' })
    await user.click(within(faq).getByText('Экран занятия не меняется, хотя студенты уже в Zoom'))
    expect(within(faq).getByRole('link', { name: 'Zoom' })).toHaveAttribute('href', '/zoom')
    expect(within(faq).getByText(/обратитесь к администратору/)).toBeInTheDocument()
  })

  it('copies the student memo', async () => {
    const user = userEvent.setup()
    setup()
    const memo = await screen.findByRole('region', { name: 'Памятка для студентов' })
    expect(within(memo).getByText(TZ_33)).toBeInTheDocument()
    await user.click(within(memo).getByRole('button', { name: 'Скопировать памятку' }))
    expect(copyMock).toHaveBeenCalledWith(STUDENT_MEMO)
    await waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Памятка скопирована'))
  })

  it('scrolls to the section from the link (/help#students)', async () => {
    const scrolled: string[] = []
    vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(function (this: HTMLElement) {
      scrolled.push(this.id)
    })
    setup('/help#students')
    await screen.findByRole('region', { name: 'Памятка для студентов' })
    await waitFor(() => expect(scrolled).toContain('students'))
  })
})

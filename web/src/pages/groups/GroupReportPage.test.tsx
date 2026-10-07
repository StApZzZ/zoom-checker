import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { RouteObject } from 'react-router'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), loading: vi.fn(), dismiss: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { ALL_TIME_FROM } from '@/components/groups/report-utils'
import { api } from '@/lib/api'
import { createQueryClient } from '@/lib/queries'
import type { GroupReport, GroupReportCell } from '@/lib/types'
import GroupReportPage from '@/pages/groups/GroupReportPage'
import { errorResponse, jsonResponse, mockFetch, renderRoutes, stubDownloads } from '@/test/utils'

const routes: RouteObject[] = [{ path: '/groups/:id/report', element: <GroupReportPage /> }]

/** A week from the real "now": the page decides what is upcoming by the clock. */
const NEXT_WEEK = new Date(Date.now() + 7 * 24 * 3600_000).toISOString()

const cell = (lesson_id: number, final_status: GroupReportCell['final_status'], over: Partial<GroupReportCell> = {}): GroupReportCell => ({
  lesson_id,
  final_status,
  manual: false,
  total_seconds: final_status === 'present' ? 5040 : 0,
  late_seconds: null,
  ...over,
})

function makeReport(): GroupReport {
  return {
    group: { id: 3, name: 'DevOps-1' },
    from: null,
    to: null,
    lessons: [
      { id: 1, title: 'Linux #1', planned_start_at: '2026-09-29T15:00:00Z', status: 'finished' },
      { id: 2, title: 'Linux #2', planned_start_at: '2026-10-06T15:00:00Z', status: 'finished' },
      { id: 3, title: 'Linux #3', planned_start_at: NEXT_WEEK, status: 'planned' },
    ],
    students: [
      {
        student_id: 10,
        full_name: 'Иванов Иван',
        external_id: '101',
        archived: false,
        cells: [cell(1, 'present', { late_seconds: 120 }), cell(2, 'present', { manual: true }), cell(3, null)],
        present_count: 2,
        absent_count: 0,
      },
      {
        student_id: 11,
        full_name: 'Петров Пётр',
        external_id: '',
        archived: false,
        cells: [cell(1, 'absent'), cell(2, 'present')],
        present_count: 1,
        absent_count: 1,
      },
      {
        student_id: 12,
        full_name: 'Новиков Олег',
        external_id: '',
        archived: false,
        // Joined after the first lesson.
        cells: [cell(1, null), cell(2, 'absent')],
        present_count: 0,
        absent_count: 1,
      },
    ],
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

function rowOf(name: string): HTMLElement {
  return screen.getByRole('rowheader', { name: new RegExp(name) }).closest('tr') as HTMLElement
}

describe('GroupReportPage', () => {
  it('renders the students × lessons matrix with totals', async () => {
    mockFetch({ 'GET /api/groups/3/report': makeReport() })
    renderRoutes(routes, '/groups/3/report')

    expect(await screen.findByRole('heading', { level: 1, name: 'Сводка посещаемости' })).toBeInTheDocument()
    expect(screen.getByText('Группа «DevOps-1»')).toBeInTheDocument()
    const table = screen.getByRole('table')
    // Planned lessons have no data yet and are not shown as columns.
    const headers = within(table).getAllByRole('columnheader')
    expect(headers.map((h) => h.textContent)).toEqual(['Студент', 'Итого', expect.stringContaining('29.09'), expect.stringContaining('06.10')])
    expect(within(table).getByRole('link', { name: /Linux #1/ })).toHaveAttribute('href', '/lessons/1')
    expect(screen.getByText(/ещё 1 занятие впереди/)).toBeInTheDocument()
    expect(screen.getByText(/2 занятия/)).toBeInTheDocument()

    const ivanov = rowOf('Иванов Иван')
    expect(within(ivanov).getByText('100%')).toBeInTheDocument()
    expect(within(ivanov).getByText('2 из 2')).toBeInTheDocument()
    expect(within(ivanov).getByText('ID 101')).toBeInTheDocument()
    expect(ivanov.querySelectorAll('td[data-status="present"]')).toHaveLength(2)
    expect(ivanov.querySelectorAll('[data-manual]')).toHaveLength(1)
    expect(within(ivanov).getByRole('button', { name: /^Иванов Иван, Linux #1, .*: Был · в Zoom 1 ч 24 мин · опоздал на 2 мин$/ })).toBeInTheDocument()
    expect(within(ivanov).getByRole('button', { name: /^Иванов Иван, Linux #2, .*: Был · в Zoom 1 ч 24 мин · отмечено вручную$/ })).toBeInTheDocument()

    const petrov = rowOf('Петров Пётр')
    expect(within(petrov).getByText('50%')).toBeInTheDocument()
    expect(within(petrov).getByText('1 из 2')).toBeInTheDocument()
    expect(petrov.querySelectorAll('td[data-status="absent"]')).toHaveLength(1)

    const novikov = rowOf('Новиков Олег')
    expect(within(novikov).getByText('0%')).toBeInTheDocument()
    expect(within(novikov).getByText('0 из 1')).toBeInTheDocument()
    expect(novikov.querySelectorAll('td[data-status="none"]')).toHaveLength(1)
    expect(within(novikov).getByRole('button', { name: /Нет данных/ })).toBeInTheDocument()

    // Footer: present per lesson and overall.
    const footer = screen.getByRole('rowheader', { name: 'Присутствовали' }).closest('tr') as HTMLElement
    const footerCells = within(footer).getAllByRole('cell')
    expect(footerCells.map((c) => c.textContent)).toEqual(['60%3 из 5', '1из 2', '2из 3'])
  })

  it('opens the details of a cell with a tap or Enter, not only with a mouse hover', async () => {
    mockFetch({ 'GET /api/groups/3/report': makeReport() })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report')
    await screen.findByRole('table')
    expect(screen.getByText('Нажмите на клетку, чтобы увидеть время в Zoom и опоздание.')).toBeInTheDocument()

    await user.click(within(rowOf('Иванов Иван')).getByRole('button', { name: /Linux #1/ }))
    const details = await screen.findByRole('dialog')
    expect(within(details).getByText('Иванов Иван')).toBeInTheDocument()
    expect(within(details).getByText('Был · в Zoom 1 ч 24 мин · опоздал на 2 мин')).toBeInTheDocument()
    // Straight to the lesson to correct the mark.
    expect(within(details).getByRole('link', { name: 'Открыть занятие' })).toHaveAttribute('href', '/lessons/1')
    await user.keyboard('{Escape}')
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('is one Tab stop; the arrow keys move between cells', async () => {
    mockFetch({ 'GET /api/groups/3/report': makeReport() })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report')
    const table = await screen.findByRole('table')
    const cells = table.querySelectorAll<HTMLElement>('tbody button[data-cell]')
    expect(cells).toHaveLength(6)
    expect([...cells].filter((c) => c.tabIndex === 0)).toHaveLength(1)

    const first = within(rowOf('Иванов Иван')).getByRole('button', { name: /Linux #1/ })
    first.focus()
    await user.keyboard('{ArrowRight}')
    expect(within(rowOf('Иванов Иван')).getByRole('button', { name: /Linux #2/ })).toHaveFocus()
    await user.keyboard('{ArrowDown}')
    const petrov2 = within(rowOf('Петров Пётр')).getByRole('button', { name: /Linux #2/ })
    expect(petrov2).toHaveFocus()
    // Tab leaves and comes back to the same cell.
    expect([...cells].filter((c) => c.tabIndex === 0)).toEqual([petrov2])
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('dialog')).toHaveTextContent('Был · в Zoom 1 ч 24 мин')
  })

  it('switches the period and passes it to the report and the export', async () => {
    const urls: URL[] = []
    mockFetch({
      'GET /api/groups/3/report': (url: URL) => {
        urls.push(url)
        return jsonResponse(makeReport())
      },
    })
    const download = vi.spyOn(api.groups, 'downloadReport').mockResolvedValue('report.xlsx')
    const user = userEvent.setup()
    const { router } = renderRoutes(routes, '/groups/3/report')
    await screen.findByRole('table')
    // "Всё время" sends explicit bounds: without them the server would only look ~4 months back.
    expect(urls[0].searchParams.get('from')).toBe(ALL_TIME_FROM.toISOString())
    expect(urls[0].searchParams.get('to')).toBeTruthy()
    expect(screen.getByText(/Период:/).textContent).toContain('за всё время')

    await user.click(screen.getByRole('radio', { name: 'Прошлый месяц' }))
    await vi.waitFor(() => expect(urls.length).toBe(2))
    expect(urls[1].searchParams.get('from')).toBeTruthy()
    expect(urls[1].searchParams.get('to')).toBeTruthy()
    expect(router.state.location.search).toBe('?period=last_month')
    expect(screen.getByText(/Период:/).textContent).toMatch(/с \d\d\.\d\d\.\d{4} по \d\d\.\d\d\.\d{4}/)

    await user.click(screen.getByRole('button', { name: 'Скачать Excel' }))
    expect(download).toHaveBeenCalledWith(3, 'xlsx', {
      from: urls[1].searchParams.get('from'),
      to: urls[1].searchParams.get('to'),
    })
    await vi.waitFor(() => expect(toastMock.success).toHaveBeenCalledWith('Файл скачан', expect.anything()))
    await user.click(screen.getByRole('button', { name: 'Скачать CSV' }))
    expect(download).toHaveBeenLastCalledWith(3, 'csv', expect.any(Object))
  })

  it('downloads the file itself and shows a failure instead of saving it', async () => {
    const { anchors } = stubDownloads()
    let fail = false
    mockFetch({
      'GET /api/groups/3/report': makeReport(),
      'GET /api/groups/3/report/export': () =>
        fail
          ? errorResponse(422, 'validation', 'Слишком длинный период: выберите не больше двух лет.')
          : new Response('xlsx', {
              headers: { 'Content-Disposition': `attachment; filename*=UTF-8''${encodeURIComponent('Посещаемость DevOps-1.xlsx')}` },
            }),
    })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report', createQueryClient())
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Скачать Excel' }))
    await vi.waitFor(() => expect(anchors()).toHaveLength(1))
    expect(anchors()[0].download).toBe('Посещаемость DevOps-1.xlsx')

    fail = true
    await user.click(screen.getByRole('button', { name: 'Скачать CSV' }))
    await vi.waitFor(() =>
      expect(toastMock.error).toHaveBeenCalledWith('Слишком длинный период: выберите не больше двух лет.'),
    )
    expect(anchors()).toHaveLength(1)
    expect(screen.getByRole('button', { name: 'Скачать CSV' })).toBeEnabled()
  })

  it('exports "Всё время" from the day of the first lesson', async () => {
    mockFetch({ 'GET /api/groups/3/report': makeReport() })
    const download = vi.spyOn(api.groups, 'downloadReport').mockResolvedValue('report.csv')
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report')
    await screen.findByRole('table')
    await user.click(screen.getByRole('button', { name: 'Скачать CSV' }))
    const first = new Date('2026-09-29T15:00:00Z')
    first.setHours(0, 0, 0, 0)
    expect(download).toHaveBeenCalledWith(3, 'csv', { from: first.toISOString(), to: expect.any(String) })
  })

  it('shows a past lesson Zoom sent nothing for, with its manual marks', async () => {
    const report = makeReport()
    report.lessons.push({ id: 4, title: 'Linux #0', planned_start_at: '2026-09-22T15:00:00Z', status: 'planned' })
    report.students[0].cells.push(cell(4, 'present', { manual: true, total_seconds: 0 }))
    report.students[1].cells.push(cell(4, 'absent'))
    mockFetch({ 'GET /api/groups/3/report': report })
    renderRoutes(routes, '/groups/3/report')

    const table = await screen.findByRole('table')
    const link = within(table).getByRole('link', { name: /Linux #0/ })
    expect(link).toHaveAttribute('data-no-zoom', 'true')
    expect(link).toHaveAccessibleName(/Из Zoom данных не было/)
    expect(screen.getByText('из Zoom данных не было')).toBeInTheDocument()
    // The manual mark counts: Иванов was at 3 of 3 lessons.
    expect(within(rowOf('Иванов Иван')).getByText('3 из 3')).toBeInTheDocument()
    expect(within(rowOf('Петров Пётр')).getByText('1 из 3')).toBeInTheDocument()
    // Only the lesson that is really ahead is left out.
    expect(screen.getByText(/ещё 1 занятие впереди/)).toBeInTheDocument()
  })

  it('does not export while the new period is still loading', async () => {
    let release: (() => void) | null = null
    mockFetch({
      'GET /api/groups/3/report': (url: URL) =>
        url.searchParams.get('from') === ALL_TIME_FROM.toISOString()
          ? jsonResponse(makeReport())
          : new Promise<Response>((resolve) => {
              release = () => resolve(jsonResponse(makeReport()))
            }),
    })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report')
    await screen.findByRole('table')
    expect(screen.getByRole('button', { name: 'Скачать Excel' })).toBeEnabled()
    await user.click(screen.getByRole('radio', { name: 'Этот месяц' }))
    // The previous period's table stays on screen, but cannot be exported as "this month".
    expect(await screen.findByText('обновляем…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Скачать Excel' })).toBeDisabled()
    release!()
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Скачать Excel' })).toBeEnabled())
  })

  it('explains a group that does not exist instead of offering a retry', async () => {
    mockFetch({ 'GET /api/groups/9/report': () => errorResponse(404, 'not_found', 'Группа не найдена') })
    renderRoutes(routes, '/groups/9/report')
    expect(await screen.findByText('Такой группы нет')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'К списку групп' })).toHaveAttribute('href', '/groups')
    expect(screen.queryByRole('button', { name: /Повторить/ })).toBeNull()
  })

  it('shows an empty state when there are no lessons in the period', async () => {
    mockFetch({ 'GET /api/groups/3/report': { ...makeReport(), lessons: [], students: [] } })
    const user = userEvent.setup()
    renderRoutes(routes, '/groups/3/report?period=this_month')
    expect(await screen.findByText('За этот период занятий нет')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Показать всё время' }))
    expect(await screen.findByText('У группы ещё не было занятий. Создайте занятие на главной странице.')).toBeInTheDocument()
  })
})

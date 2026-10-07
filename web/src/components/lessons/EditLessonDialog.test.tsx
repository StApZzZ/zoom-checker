import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { EditLessonDialog } from '@/components/lessons/EditLessonDialog'
import type { Lesson, UpdateLessonInput } from '@/lib/types'
import { makeLesson } from '@/pages/lessons/testing'
import { errorResponse, jsonResponse, mockFetch, renderWithClient } from '@/test/utils'

const local = (d: number, h: number, mi: number) => new Date(2026, 9, d, h, mi).toISOString()

function setup(lesson: Lesson = makeLesson({ planned_start_at: local(6, 18, 0), planned_end_at: local(6, 19, 30) })) {
  const sent: UpdateLessonInput[] = []
  const onOpenChange = vi.fn()
  const fetchMock = mockFetch({
    'PATCH /api/lessons/5': (_url: URL, init: RequestInit) => {
      const body = JSON.parse(String(init.body)) as UpdateLessonInput
      sent.push(body)
      return jsonResponse({ ...lesson, ...body })
    },
    'GET /api/zoom/parse': (url: URL) =>
      /\d{9,11}/.test(url.searchParams.get('input') ?? '')
        ? jsonResponse({ meeting_id: '9876543210', formatted: '987 654 3210' })
        : errorResponse(422, 'validation', 'В тексте нет номера конференции Zoom.'),
  })
  const user = userEvent.setup()
  const utils = renderWithClient(<EditLessonDialog lesson={lesson} open onOpenChange={onOpenChange} />)
  return { ...utils, user, sent, onOpenChange, fetchMock }
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('EditLessonDialog', () => {
  it('starts from the lesson as it is', async () => {
    setup()
    expect(await screen.findByRole('dialog', { name: 'Изменить занятие' })).toBeInTheDocument()
    expect(screen.getByLabelText('Название')).toHaveValue('DevOps. Занятие №4')
    expect(screen.getByLabelText('Ссылка Zoom или номер конференции')).toHaveValue('https://zoom.us/j/1234567890')
    expect(screen.getByLabelText('Дата')).toHaveValue('2026-10-06')
    expect(screen.getByLabelText('Время начала')).toHaveValue('18:00')
    expect(screen.getByLabelText('Длительность, минут')).toHaveValue(90)
    expect(screen.getByRole('button', { name: '90 мин' })).toHaveAttribute('aria-pressed', 'true')
  })

  it('sends only what was changed', async () => {
    const { user, sent, onOpenChange } = setup()
    const title = await screen.findByLabelText('Название')
    await user.clear(title)
    await user.type(title, 'Вводное занятие')
    await user.click(screen.getByRole('button', { name: '120 мин' }))
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    expect(sent).toEqual([
      { title: 'Вводное занятие', planned_start_at: local(6, 18, 0), planned_end_at: local(6, 20, 0) },
    ])
  })

  it('sends a new Zoom link once it is recognized', async () => {
    const { user, sent } = setup()
    const zoom = await screen.findByLabelText('Ссылка Zoom или номер конференции')
    await user.clear(zoom)
    await user.click(zoom)
    await user.paste('https://zoom.us/j/9876543210')
    expect(await screen.findByText('Конференция 987 654 3210')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    await waitFor(() => expect(sent).toEqual([{ zoom: 'https://zoom.us/j/9876543210' }]))
  })

  it('closes without a request when nothing changed', async () => {
    const { user, onOpenChange, fetchMock } = setup()
    await user.click(await screen.findByRole('button', { name: 'Сохранить' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PATCH')).toHaveLength(0)
  })

  it('shows Russian validation and server errors next to the field', async () => {
    const { user, fetchMock } = setup()
    const title = await screen.findByLabelText('Название')
    await user.clear(title)
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(screen.getByText('Введите название занятия')).toBeInTheDocument()
    expect(title).toHaveFocus()

    fetchMock.mockImplementation(async () =>
      errorResponse(422, 'validation', 'Время окончания должно быть позже начала', { field: 'planned_start_at' }),
    )
    await user.type(title, 'Новое')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(await screen.findByText('Время окончания должно быть позже начала')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText('Дата')).toHaveFocus())
  })

  it('warns that a finished lesson will be recalculated', async () => {
    setup(makeLesson({ status: 'finished' }))
    expect(
      await screen.findByText('После изменения времени или ссылки посещаемость будет пересчитана.'),
    ).toBeInTheDocument()
  })
})

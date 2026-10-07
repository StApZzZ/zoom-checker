import type { ReactNode } from 'react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn(), loading: vi.fn(), dismiss: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { ApiError, setUnauthorizedHandler } from '@/lib/api'
import { receivedAt } from '@/lib/liveClock'
import {
  applyAttendanceChange,
  applyPendingAttendance,
  createQueryClient,
  isNewerView,
  queryKeys,
  shouldRetry,
  storeLessonView,
  useCreateGroup,
  useCreateLesson,
  useDeleteLesson,
  useFinishLesson,
  useGroupReportExport,
  useLessonExport,
  useLessonView,
  useMapParticipant,
  useMe,
  useRecomputeLesson,
  useReprocessFailed,
  useUnmapParticipant,
  useUpdateAttendance,
  useUpdateSettings,
} from '@/lib/queries'
import type { LessonView, StudentRow } from '@/lib/types'
import { createTestQueryClient, errorResponse, jsonResponse, makeUser, mockFetch, stubDownloads } from '@/test/utils'

function row(id: number, overrides: Partial<StudentRow> = {}): StudentRow {
  return {
    student_id: id,
    full_name: `Студент ${id}`,
    external_id: '',
    current_status: 'left',
    total_seconds: 600,
    first_join_at: '2026-10-06T15:01:00Z',
    last_leave_at: '2026-10-06T15:11:00Z',
    late_seconds: 60,
    early_leave_seconds: null,
    auto_status: 'present',
    manual_status: null,
    final_status: 'present',
    comment: '',
    zoom_names: [],
    devices_online: 0,
    archived: false,
    ...overrides,
  }
}

function view(asOf = '2026-10-06T15:30:00Z'): LessonView {
  return {
    lesson: {
      id: 9,
      group: { id: 2, name: 'DevOps-1' },
      title: 'Занятие',
      zoom_meeting_id: '1234567890',
      zoom_meeting_id_formatted: '123 456 7890',
      zoom_join_url: '',
      planned_start_at: '2026-10-06T15:00:00Z',
      planned_end_at: '2026-10-06T16:30:00Z',
      actual_start_at: null,
      actual_end_at: null,
      status: 'active',
      manual_finished_at: null,
      sessions: [],
    },
    counts: { in_group: 2, online: 0, seen: 1, not_seen: 1, unidentified: 0, present: 1, absent: 1 },
    students: [row(1), row(2, { current_status: 'not_seen', auto_status: 'absent', final_status: 'absent', total_seconds: 0 })],
    participants: [],
    as_of: asOf,
  }
}

let client: QueryClient
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={client}>{children}</QueryClientProvider>
)

beforeEach(() => {
  client = createTestQueryClient()
  toastMock.error.mockReset()
  toastMock.success.mockReset()
  toastMock.loading.mockReset()
  toastMock.dismiss.mockReset()
})
afterEach(() => {
  client.clear()
  vi.unstubAllGlobals()
})

describe('queryKeys', () => {
  it('nests everything about a lesson under byId(id)', () => {
    const prefix = queryKeys.lessons.byId(9)
    for (const key of [
      queryKeys.lessons.detail(9),
      queryKeys.lessons.view(9),
      queryKeys.lessons.participants(9),
      queryKeys.lessons.audit(9),
      queryKeys.lessons.history(9, 3),
    ]) {
      expect(key.slice(0, prefix.length)).toEqual(prefix)
    }
    expect(queryKeys.lessons.list({}).slice(0, 2)).toEqual(queryKeys.lessons.lists())
  })

  it('normalizes filters so equal filters share a cache entry', () => {
    expect(queryKeys.lessons.list({})).toEqual(queryKeys.lessons.list({ from: undefined }))
    expect(queryKeys.groups.report(1)).toEqual(queryKeys.groups.report(1, {}))
    expect(queryKeys.zoom.events({ status: 'failed' })).not.toEqual(queryKeys.zoom.events({}))
  })
})

describe('shouldRetry', () => {
  it('retries once for server/network errors and never for 4xx', () => {
    expect(shouldRetry(0, new ApiError(500, 'internal', 'x'))).toBe(true)
    expect(shouldRetry(1, new ApiError(500, 'internal', 'x'))).toBe(false)
    expect(shouldRetry(0, new ApiError(0, 'network', 'x'))).toBe(true)
    expect(shouldRetry(0, new ApiError(404, 'not_found', 'x'))).toBe(false)
    expect(shouldRetry(0, new ApiError(422, 'validation', 'x'))).toBe(false)
  })
})

describe('createQueryClient', () => {
  it('uses the project defaults', () => {
    const qc = createQueryClient()
    const q = qc.getDefaultOptions().queries!
    expect(q.staleTime).toBe(10_000)
    expect(q.refetchOnWindowFocus).toBe(true)
    expect(q.retry).toBe(shouldRetry)
  })

  it('toasts mutation errors unless the mutation opts out, and skips 401', async () => {
    client = createQueryClient()
    mockFetch({ 'POST /api/groups': () => errorResponse(409, 'conflict', 'Такая группа уже есть') })
    const { result } = renderHook(() => useCreateGroup(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({ name: 'A' }).catch(() => {})
    })
    expect(toastMock.error).toHaveBeenCalledWith('Такая группа уже есть')

    toastMock.error.mockReset()
    const quiet = renderHook(() => useCreateGroup({ errorToast: false }), { wrapper })
    await act(async () => {
      await quiet.result.current.mutateAsync({ name: 'A' }).catch(() => {})
    })
    expect(toastMock.error).not.toHaveBeenCalled()

    const onUnauthorized = vi.fn()
    const restore = setUnauthorizedHandler(onUnauthorized)
    mockFetch({ 'POST /api/groups': () => errorResponse(401, 'unauthorized', 'Нужно войти') })
    const anon = renderHook(() => useCreateGroup(), { wrapper })
    await act(async () => {
      await anon.result.current.mutateAsync({ name: 'A' }).catch(() => {})
    })
    restore()
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
    expect(toastMock.error).not.toHaveBeenCalled()
  })

  it('shows the success toast and invalidates group lists', async () => {
    client = createQueryClient()
    client.setQueryData(queryKeys.groups.list('active'), [])
    mockFetch({ 'POST /api/groups': () => jsonResponse({ id: 1, name: 'A' }, 201) })
    const { result } = renderHook(() => useCreateGroup(), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({ name: 'A' })
    })
    expect(toastMock.success).toHaveBeenCalledWith('Группа создана')
    expect(client.getQueryState(queryKeys.groups.list('active'))?.isInvalidated).toBe(true)
  })
})

describe('useMe', () => {
  it('returns the user', async () => {
    const user = makeUser()
    mockFetch({ 'GET /api/auth/me': user })
    const { result } = renderHook(() => useMe(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toEqual(user)
  })

  it('returns null (not an error) when not logged in', async () => {
    mockFetch({ 'GET /api/auth/me': () => errorResponse(401, 'unauthorized', 'Нужно войти') })
    const { result } = renderHook(() => useMe(), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data).toBeNull()
  })

  it('reports other failures as errors', async () => {
    mockFetch({ 'GET /api/auth/me': () => errorResponse(500, 'internal', 'Ошибка') })
    const { result } = renderHook(() => useMe(), { wrapper })
    await waitFor(() => expect(result.current.isError).toBe(true))
  })
})

describe('useLessonView', () => {
  it('records the receive time of each snapshot', async () => {
    mockFetch({ 'GET /api/lessons/9/attendance': view('2026-10-06T15:31:00Z') })
    const { result } = renderHook(() => useLessonView(9), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(receivedAt('2026-10-06T15:31:00Z')).toBeTypeOf('number')
  })

  it('keeps a newer cached snapshot over an older response', async () => {
    let release: (r: Response) => void = () => {}
    mockFetch({ 'GET /api/lessons/9/attendance': () => new Promise<Response>((r) => (release = r)) })
    const { result } = renderHook(() => useLessonView(9), { wrapper })
    // An SSE snapshot lands while the request is in flight.
    act(() => {
      client.setQueryData(queryKeys.lessons.view(9), view('2026-10-06T15:30:05Z'))
    })
    await act(async () => {
      release(jsonResponse(view('2026-10-06T15:30:00Z')))
    })
    await waitFor(() => expect(result.current.isFetching).toBe(false))
    expect(result.current.data?.as_of).toBe('2026-10-06T15:30:05Z')
  })

  it('is disabled without a valid id', () => {
    const fetchMock = mockFetch({})
    renderHook(() => useLessonView(undefined), { wrapper })
    renderHook(() => useLessonView(Number.NaN), { wrapper })
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('isNewerView', () => {
  it('compares as_of timestamps', () => {
    expect(isNewerView({ as_of: '2026-10-06T15:00:01Z' }, { as_of: '2026-10-06T15:00:00Z' })).toBe(true)
    expect(isNewerView({ as_of: '2026-10-06T15:00:00Z' }, { as_of: '2026-10-06T15:00:01Z' })).toBe(false)
    expect(isNewerView({ as_of: '2026-10-06T15:00:00Z' }, undefined)).toBe(true)
  })

  it.each([
    ['a late response a few seconds older', '2026-10-06T14:59:50Z', false],
    ['exactly at the clock-reset bound', '2026-10-06T14:58:00Z', false],
    ['the server clock was set back by more than 2 minutes', '2026-10-06T14:57:59Z', true],
    ['the server clock was set back by an hour', '2026-10-06T14:00:00Z', true],
  ])('%s → %s', (_name, asOf, newer) => {
    expect(isNewerView({ as_of: asOf }, { as_of: '2026-10-06T15:00:00Z' })).toBe(newer)
  })
})

describe('applyAttendanceChange', () => {
  it('sets a manual status and recounts present/absent', () => {
    const next = applyAttendanceChange(view(), 2, { status: 'present' })
    expect(next.students[1]).toMatchObject({ manual_status: 'present', final_status: 'present' })
    expect(next.counts).toMatchObject({ present: 2, absent: 0 })
  })

  it('null returns to the automatic status', () => {
    const manual = applyAttendanceChange(view(), 1, { status: 'absent' })
    expect(manual.students[0].final_status).toBe('absent')
    const back = applyAttendanceChange(manual, 1, { status: null })
    expect(back.students[0]).toMatchObject({ manual_status: null, final_status: 'present' })
  })

  it('updates only the comment when status is omitted', () => {
    const next = applyAttendanceChange(view(), 1, { comment: 'болел' })
    expect(next.students[0]).toMatchObject({ comment: 'болел', manual_status: null })
  })

  it('returns the same object for unknown students', () => {
    const v = view()
    expect(applyAttendanceChange(v, 99, { status: 'present' })).toBe(v)
  })
})

describe('useUpdateAttendance', () => {
  /** The live view as the screen sees it, plus the mutation. */
  function renderLive() {
    return renderHook(() => ({ view: useLessonView(9), update: useUpdateAttendance(9) }), { wrapper })
  }

  it('shows the change at once without touching the cached server view', async () => {
    let release: (r: Response) => void = () => {}
    mockFetch({
      'PATCH /api/lessons/9/attendance/2': () => new Promise<Response>((r) => (release = r)),
      'GET /api/lessons/9/attendance': view('2026-10-06T15:40:00Z'),
    })
    client.setQueryData(queryKeys.lessons.view(9), view())
    const { result } = renderLive()
    act(() => {
      result.current.update.mutate({ studentId: 2, input: { status: 'present' } })
    })
    await waitFor(() => expect(result.current.view.data?.students[1].final_status).toBe('present'))
    expect(result.current.view.data?.counts).toMatchObject({ present: 2, absent: 0 })
    // The cache keeps what the server said; the change is an overlay until it is saved.
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(9))?.students[1].final_status).toBe('absent')
    await act(async () => release(jsonResponse({})))
    await waitFor(() => expect(result.current.update.isSuccess).toBe(true))
  })

  it('keeps the pending change over a newer live snapshot (the Был/Не был race)', async () => {
    let release: (r: Response) => void = () => {}
    mockFetch({ 'PATCH /api/lessons/9/attendance/2': () => new Promise<Response>((r) => (release = r)) })
    client.setQueryData(queryKeys.lessons.view(9), view())
    const { result } = renderLive()
    act(() => {
      result.current.update.mutate({ studentId: 2, input: { status: 'present', comment: 'был с телефона' } })
    })
    await waitFor(() => expect(result.current.view.data?.students[1].final_status).toBe('present'))

    // An SSE snapshot computed just before the PATCH was saved: newer as_of, old value.
    const snapshot = view('2026-10-06T15:35:00Z')
    snapshot.students[0] = { ...snapshot.students[0], current_status: 'online' }
    act(() => {
      client.setQueryData(queryKeys.lessons.view(9), snapshot)
    })
    await waitFor(() => expect(result.current.view.data?.students[0].current_status).toBe('online'))
    expect(result.current.view.data?.students[1]).toMatchObject({
      final_status: 'present',
      manual_status: 'present',
      comment: 'был с телефона',
    })

    // The server answers with the saved view: it replaces the snapshot and the overlay goes away.
    const saved = applyAttendanceChange(view('2026-10-06T15:36:00Z'), 2, { status: 'present', comment: 'был с телефона' })
    await act(async () => release(jsonResponse(saved)))
    await waitFor(() => expect(result.current.update.isSuccess).toBe(true))
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(9))).toEqual(saved)
    expect(result.current.view.data).toEqual(saved)
  })

  it('applies several pending changes in order', async () => {
    const releases: ((r: Response) => void)[] = []
    mockFetch({ 'PATCH /api/lessons/9/attendance/2': () => new Promise<Response>((r) => releases.push(r)) })
    client.setQueryData(queryKeys.lessons.view(9), view())
    const { result } = renderLive()
    act(() => {
      result.current.update.mutate({ studentId: 2, input: { status: 'present' } })
    })
    act(() => {
      result.current.update.mutate({ studentId: 2, input: { status: null } })
    })
    // The last click wins: back to the automatic "absent".
    await waitFor(() => expect(releases).toHaveLength(2))
    expect(result.current.view.data?.students[1]).toMatchObject({ manual_status: null, final_status: 'absent' })
    await act(async () => {
      releases[0](jsonResponse({}))
      releases[1](jsonResponse({}))
    })
  })

  it('drops the change when the server rejects it', async () => {
    mockFetch({ 'PATCH /api/lessons/9/attendance/2': () => errorResponse(403, 'forbidden', 'Нет прав') })
    client.setQueryData(queryKeys.lessons.view(9), view())
    const { result } = renderHook(
      () => ({ view: useLessonView(9), update: useUpdateAttendance(9, { errorToast: false }) }),
      { wrapper },
    )
    await act(async () => {
      await result.current.update.mutateAsync({ studentId: 2, input: { status: 'present' } }).catch(() => {})
    })
    await waitFor(() => expect(result.current.view.data?.students[1].final_status).toBe('absent'))
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(9))?.students[1].final_status).toBe('absent')
  })
})

describe('lesson actions answered with a LessonView', () => {
  it('store the answered view instead of refetching it, and refresh the rest', async () => {
    const answered = view('2026-10-06T15:45:00Z')
    answered.lesson = { ...answered.lesson, status: 'finished', manual_finished_at: '2026-10-06T15:45:00Z' }
    const fetchMock = mockFetch({
      'POST /api/lessons/9/finish': answered,
      'GET /api/lessons/9/attendance': view('2026-10-06T15:30:00Z'),
    })
    client.setQueryData(queryKeys.lessons.view(9), view())
    client.setQueryData(queryKeys.lessons.participants(9), [])
    const { result } = renderHook(() => useFinishLesson(9), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({})
    })
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(9))).toEqual(answered)
    expect(client.getQueryState(queryKeys.lessons.view(9))?.isInvalidated).toBe(false)
    expect(client.getQueryState(queryKeys.lessons.participants(9))?.isInvalidated).toBe(true)
    expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/lessons/9/attendance')).toBe(false)
    expect(receivedAt(answered.as_of)).toBeTypeOf('number')
  })

  it('do not replace a newer live snapshot with an older answer', async () => {
    mockFetch({ 'POST /api/lessons/9/participants/7/map': view('2026-10-06T15:31:00Z') })
    const newer = view('2026-10-06T15:32:00Z')
    client.setQueryData(queryKeys.lessons.view(9), newer)
    const { result } = renderHook(() => useMapParticipant(9), { wrapper })
    await act(async () => {
      await result.current.mutateAsync({ participantId: 7, input: { action: 'none' } })
    })
    expect(client.getQueryData(queryKeys.lessons.view(9))).toBe(newer)
    expect(client.getQueryState(queryKeys.lessons.view(9))?.isInvalidated).toBe(false)
  })

  it('refetch the view when the answer is not a view of this lesson', async () => {
    const other = view('2026-10-06T15:45:00Z')
    other.lesson = { ...other.lesson, id: 10 }
    mockFetch({ 'DELETE /api/lessons/9/participants/7/map': other, 'POST /api/lessons/9/recompute': {} })
    client.setQueryData(queryKeys.lessons.view(9), view())
    const unmap = renderHook(() => useUnmapParticipant(9), { wrapper })
    await act(async () => {
      await unmap.result.current.mutateAsync(7)
    })
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(9))?.as_of).toBe('2026-10-06T15:30:00Z')
    expect(client.getQueryState(queryKeys.lessons.view(9))?.isInvalidated).toBe(true)

    client.setQueryData(queryKeys.lessons.view(9), view())
    const recompute = renderHook(() => useRecomputeLesson(9), { wrapper })
    await act(async () => {
      await recompute.result.current.mutateAsync()
    })
    expect(client.getQueryState(queryKeys.lessons.view(9))?.isInvalidated).toBe(true)
  })

  it('unmapping refreshes the group too (a remembered name may be removed with the decision)', async () => {
    mockFetch({ 'DELETE /api/lessons/9/participants/7/map': view('2026-10-06T15:45:00Z') })
    client.setQueryData(queryKeys.lessons.view(9), view())
    client.setQueryData(queryKeys.groups.detail(2), { id: 2 })
    const { result } = renderHook(() => useUnmapParticipant(9), { wrapper })
    await act(async () => {
      await result.current.mutateAsync(7)
    })
    expect(client.getQueryState(queryKeys.groups.detail(2))?.isInvalidated).toBe(true)
  })
})

describe('storeLessonView', () => {
  it('accepts only views of the lesson', () => {
    expect(storeLessonView(client, 9, undefined)).toBe(false)
    expect(storeLessonView(client, 9, { lesson: { id: 9 } })).toBe(false)
    expect(storeLessonView(client, 9, view())).toBe(true)
    expect(client.getQueryData(queryKeys.lessons.view(9))).toEqual(view())
  })
})

describe('applyPendingAttendance', () => {
  it('applies changes in order and leaves the view alone without any', () => {
    const v = view()
    expect(applyPendingAttendance(v, [])).toBe(v)
    const next = applyPendingAttendance(v, [
      { studentId: 2, input: { status: 'present' } },
      { studentId: 2, input: { comment: 'болел' } },
    ])
    expect(next.students[1]).toMatchObject({ final_status: 'present', comment: 'болел' })
  })
})

describe('export downloads', () => {
  it('report the pending format and toast the result', async () => {
    const { anchors } = stubDownloads()
    let release: (r: Response) => void = () => {}
    mockFetch({ 'GET /api/lessons/9/export': () => new Promise<Response>((r) => (release = r)) })
    const { result } = renderHook(() => ({ a: useLessonExport(9), b: useLessonExport(9) }), { wrapper })
    act(() => result.current.a.download('csv'))
    // Every button for the same lesson file sees it.
    await waitFor(() => expect(result.current.b.pending).toEqual(['csv']))
    expect(toastMock.loading).toHaveBeenCalledWith('Готовим файл…')
    await act(async () =>
      release(
        new Response('a;b', {
          headers: { 'Content-Disposition': `attachment; filename="x.csv"; filename*=UTF-8''${encodeURIComponent('Занятие.csv')}` },
        }),
      ),
    )
    await waitFor(() => expect(result.current.a.pending).toEqual([]))
    expect(anchors().map((a) => a.download)).toEqual(['Занятие.csv'])
    expect(toastMock.success).toHaveBeenCalledWith('Файл скачан', expect.anything())
  })

  it('downloads a group report for the period', async () => {
    const { anchors } = stubDownloads()
    const fetchMock = mockFetch({ 'GET /api/groups/3/report/export': () => new Response('x', { headers: {} }) })
    const { result } = renderHook(() => useGroupReportExport(3), { wrapper })
    act(() => result.current.download({ format: 'xlsx', range: { from: '2026-09-01', to: '2026-10-01' } }))
    await waitFor(() => expect(anchors()).toHaveLength(1))
    // No Content-Disposition: a readable fallback name.
    expect(anchors()[0].download).toBe('group-3-report.xlsx')
    const url = new URL(String(fetchMock.mock.calls[0][0]), 'http://localhost')
    expect(url.pathname).toBe('/api/groups/3/report/export')
    expect(Object.fromEntries(url.searchParams)).toMatchObject({ format: 'xlsx', from: '2026-09-01', to: '2026-10-01' })
  })
})

describe('applyAttendanceChange counts (review)', () => {
  it('adjusts counts by the changed row instead of recounting the rows', () => {
    // The server counted 25 students; only two rows are loaded here.
    const v = { ...view(), counts: { ...view().counts, in_group: 25, present: 20, absent: 5 } }
    const next = applyAttendanceChange(v, 2, { status: 'present' })
    expect(next.counts).toMatchObject({ in_group: 25, present: 21, absent: 4 })
    const back = applyAttendanceChange(next, 2, { status: null })
    expect(back.counts).toMatchObject({ present: 20, absent: 5 })
  })

  it('leaves counts alone when the final status does not change', () => {
    const v = view()
    expect(applyAttendanceChange(v, 1, { status: 'present' }).counts).toEqual(v.counts)
    expect(applyAttendanceChange(v, 1, { comment: 'x' }).counts).toEqual(v.counts)
  })

  it('does not count archived students (the server leaves them out of the tiles)', () => {
    const v = view()
    v.students[1] = { ...v.students[1], archived: true }
    const next = applyAttendanceChange(v, 2, { status: 'present' })
    expect(next.students[1].final_status).toBe('present')
    expect(next.counts).toEqual(v.counts)
  })

  it('does not mutate the cached view', () => {
    const v = view()
    const snapshot = JSON.stringify(v)
    applyAttendanceChange(v, 2, { status: 'present', comment: 'болел' })
    expect(JSON.stringify(v)).toBe(snapshot)
  })
})

describe('invalidation map (review)', () => {
  function seed() {
    client.setQueryData(queryKeys.groups.report(2, { from: '2026-10-01' }), { lessons: [] })
    client.setQueryData(queryKeys.groups.detail(2), { id: 2 })
    client.setQueryData(queryKeys.lessons.list({}), [])
    client.setQueryData(queryKeys.lessons.view(9), view())
    client.setQueryData(queryKeys.zoom.sessions(true), [])
  }
  const invalidated = (key: readonly unknown[]) => client.getQueryState(key)?.isInvalidated

  it('creating and deleting lessons refreshes lists and group reports', async () => {
    seed()
    mockFetch({ 'POST /api/lessons': [], 'DELETE /api/lessons/9': () => new Response(null, { status: 204 }) })
    const create = renderHook(() => useCreateLesson(), { wrapper })
    await act(async () => {
      await create.result.current.mutateAsync({
        group_id: 2,
        title: 'Занятие',
        zoom: '123 456 7890',
        planned_start_at: '2026-10-06T15:00:00Z',
        planned_end_at: '2026-10-06T16:30:00Z',
      })
    })
    expect(invalidated(queryKeys.lessons.list({}))).toBe(true)
    expect(invalidated(queryKeys.groups.report(2, { from: '2026-10-01' }))).toBe(true)
    expect(invalidated(queryKeys.groups.detail(2))).toBe(false)

    seed()
    const del = renderHook(() => useDeleteLesson(), { wrapper })
    await act(async () => {
      await del.result.current.mutateAsync(9)
    })
    expect(client.getQueryData(queryKeys.lessons.view(9))).toBeUndefined()
    expect(invalidated(queryKeys.groups.report(2, { from: '2026-10-01' }))).toBe(true)
  })

  it('settings and reprocessing refresh lessons and reports', async () => {
    seed()
    const settings = { ignored_names: [], present_min_percent: 50, link_window_hours: 2, ignore_host: true }
    mockFetch({ 'PUT /api/settings': settings, 'POST /api/zoom/events/reprocess-failed': { count: 3 } })
    const upd = renderHook(() => useUpdateSettings(), { wrapper })
    await act(async () => {
      await upd.result.current.mutateAsync(settings)
    })
    expect(invalidated(queryKeys.lessons.view(9))).toBe(true)
    expect(invalidated(queryKeys.groups.report(2, { from: '2026-10-01' }))).toBe(true)

    seed()
    const re = renderHook(() => useReprocessFailed(), { wrapper })
    await act(async () => {
      await re.result.current.mutateAsync()
    })
    expect(invalidated(queryKeys.lessons.view(9))).toBe(true)
    expect(invalidated(queryKeys.zoom.sessions(true))).toBe(true)
    expect(invalidated(queryKeys.groups.report(2, { from: '2026-10-01' }))).toBe(true)
  })
})

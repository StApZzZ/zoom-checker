import type { ReactNode } from 'react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { receivedAt } from '@/lib/liveClock'
import { queryKeys, useLessons } from '@/lib/queries'
import {
  STREAM_TIMINGS,
  createRefresher,
  parseSnapshot,
  useGlobalStream,
  useLessonStream,
  useLiveLessonView,
} from '@/lib/sse'
import type { LessonView } from '@/lib/types'
import { FakeEventSource } from '@/test/fakeEventSource'
import { createTestQueryClient, mockFetch } from '@/test/utils'

function makeView(asOf: string, online = 1): LessonView {
  return {
    lesson: {
      id: 5,
      group: { id: 2, name: 'DevOps-1' },
      title: 'DevOps. Занятие №4',
      zoom_meeting_id: '1234567890',
      zoom_meeting_id_formatted: '123 456 7890',
      zoom_join_url: '',
      planned_start_at: '2026-10-06T15:00:00Z',
      planned_end_at: '2026-10-06T16:30:00Z',
      actual_start_at: '2026-10-06T15:01:00Z',
      actual_end_at: null,
      status: 'active',
      manual_finished_at: null,
      sessions: [],
    },
    counts: { in_group: 2, online, seen: 1, not_seen: 1, unidentified: 0, present: 1, absent: 1 },
    students: [],
    participants: [],
    as_of: asOf,
  }
}

let client: QueryClient
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={client}>{children}</QueryClientProvider>
)

const tick = async (ms: number) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-06T15:30:00Z'))
  FakeEventSource.reset()
  vi.stubGlobal('EventSource', FakeEventSource)
  client = createTestQueryClient()
})

afterEach(() => {
  client.clear()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  Object.defineProperty(document, 'hidden', { configurable: true, value: false })
})

describe('useLessonStream', () => {
  it('opens the lesson stream and stores snapshots in the view cache', async () => {
    const { result } = renderHook(() => useLessonStream(5), { wrapper })
    const es = FakeEventSource.latest()
    expect(es.url).toBe('/api/lessons/5/stream')
    expect(result.current).toBe('reconnecting')

    act(() => es.open())
    expect(result.current).toBe('live')

    const view = makeView('2026-10-06T15:30:00Z')
    act(() => es.emit('snapshot', JSON.stringify(view)))
    expect(client.getQueryData(queryKeys.lessons.view(5))).toEqual(view)
    expect(receivedAt(view.as_of)).toBe(Date.now())
  })

  it('ignores malformed and older snapshots', async () => {
    renderHook(() => useLessonStream(5), { wrapper })
    const es = FakeEventSource.latest()
    act(() => es.open())
    const newer = makeView('2026-10-06T15:30:10Z', 3)
    act(() => es.emit('snapshot', JSON.stringify(newer)))
    act(() => es.emit('snapshot', '{not json'))
    act(() => es.emit('snapshot', JSON.stringify(makeView('2026-10-06T15:30:00Z', 1))))
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(5))?.counts.online).toBe(3)
  })

  it('keeps updating after the server clock was set back', async () => {
    renderHook(() => useLessonStream(5), { wrapper })
    const es = FakeEventSource.latest()
    act(() => es.open())
    act(() => es.emit('snapshot', JSON.stringify(makeView('2026-10-06T15:30:10Z', 3))))
    // The server restarted with its clock an hour behind: every new snapshot is "older".
    act(() => es.emit('snapshot', JSON.stringify(makeView('2026-10-06T14:30:00Z', 4))))
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(5))?.counts.online).toBe(4)
    act(() => es.emit('snapshot', JSON.stringify(makeView('2026-10-06T14:30:05Z', 5))))
    expect(client.getQueryData<LessonView>(queryKeys.lessons.view(5))?.counts.online).toBe(5)
  })

  it('goes reconnecting → polling while broken and polls the view every 5 s', async () => {
    let calls = 0
    mockFetch({
      'GET /api/lessons/5/attendance': () => {
        calls += 1
        return new Response(JSON.stringify(makeView(new Date().toISOString())), {
          headers: { 'Content-Type': 'application/json' },
        })
      },
    })
    const { result } = renderHook(() => useLiveLessonView(5), { wrapper })
    await tick(0)
    expect(calls).toBe(1) // initial fetch
    const es = FakeEventSource.latest()
    act(() => es.open())
    expect(result.current.connection).toBe('live')

    // While live there is no polling.
    await tick(STREAM_TIMINGS.pollMs * 3)
    expect(calls).toBe(1)

    act(() => es.drop())
    expect(result.current.connection).toBe('reconnecting')
    await tick(STREAM_TIMINGS.pollMs)
    expect(calls).toBe(2)
    await tick(STREAM_TIMINGS.graceMs - STREAM_TIMINGS.pollMs)
    expect(result.current.connection).toBe('polling')
    expect(calls).toBe(3)

    // The browser reconnects on its own: back to live, polling stops.
    act(() => es.open())
    expect(result.current.connection).toBe('live')
    await tick(STREAM_TIMINGS.pollMs * 2)
    expect(calls).toBe(3)
  })

  it('reconnects with backoff after a fatal error', async () => {
    const { result } = renderHook(() => useLessonStream(5), { wrapper })
    const first = FakeEventSource.latest()
    act(() => first.open())
    act(() => first.fail())
    expect(result.current).toBe('polling')
    expect(first.closed).toBe(true)
    expect(FakeEventSource.instances).toHaveLength(1)

    await tick(STREAM_TIMINGS.reconnectBaseMs)
    expect(FakeEventSource.instances).toHaveLength(2)
    const second = FakeEventSource.latest()
    act(() => second.fail())
    // Second attempt waits twice as long.
    await tick(STREAM_TIMINGS.reconnectBaseMs)
    expect(FakeEventSource.instances).toHaveLength(2)
    await tick(STREAM_TIMINGS.reconnectBaseMs)
    expect(FakeEventSource.instances).toHaveLength(3)
    act(() => FakeEventSource.latest().open())
    expect(result.current).toBe('live')
  })

  it('closes the stream and stops timers on unmount', async () => {
    const { unmount } = renderHook(() => useLessonStream(5), { wrapper })
    const es = FakeEventSource.latest()
    act(() => es.drop())
    unmount()
    expect(es.closed).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('reconnects when the lesson id changes and does nothing without an id', () => {
    const { rerender } = renderHook(({ id }: { id: number | undefined }) => useLessonStream(id), {
      wrapper,
      initialProps: { id: undefined as number | undefined },
    })
    expect(FakeEventSource.instances).toHaveLength(0)
    rerender({ id: 5 })
    rerender({ id: 6 })
    expect(FakeEventSource.instances.map((e) => e.url)).toEqual(['/api/lessons/5/stream', '/api/lessons/6/stream'])
    expect(FakeEventSource.instances[0].closed).toBe(true)
  })

  it('closes the stream in a long-hidden tab and reconnects when shown', async () => {
    renderHook(() => useLessonStream(5), { wrapper })
    const first = FakeEventSource.latest()
    act(() => first.open())

    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await tick(STREAM_TIMINGS.hiddenCloseMs)
    expect(first.closed).toBe(true)

    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(FakeEventSource.instances).toHaveLength(2)
    expect(FakeEventSource.latest().closed).toBe(false)
  })

  it('falls back to polling when EventSource is unavailable', () => {
    vi.stubGlobal('EventSource', undefined)
    const { result } = renderHook(() => useLessonStream(5), { wrapper })
    expect(result.current).toBe('polling')
  })
})

describe('useGlobalStream', () => {
  it('invalidates lesson lists on "lessons" events and on reconnect', async () => {
    client.setQueryData(queryKeys.lessons.list({}), [])
    client.setQueryData(queryKeys.lessons.view(5), makeView('2026-10-06T15:30:00Z'))
    const { result } = renderHook(() => useGlobalStream(), { wrapper })
    const es = FakeEventSource.latest()
    expect(es.url).toBe('/api/stream')
    act(() => es.open())
    expect(result.current).toBe('live')
    expect(client.getQueryState(queryKeys.lessons.list({}))?.isInvalidated).toBe(false)

    act(() => es.emit('lessons', ''))
    expect(client.getQueryState(queryKeys.lessons.list({}))?.isInvalidated).toBe(true)
    // Lesson views have their own stream and are left alone.
    expect(client.getQueryState(queryKeys.lessons.view(5))?.isInvalidated).toBe(false)

    client.setQueryData(queryKeys.lessons.list({}), [])
    act(() => es.drop())
    act(() => es.open())
    expect(client.getQueryState(queryKeys.lessons.list({}))?.isInvalidated).toBe(true)
  })
})

describe('parseSnapshot', () => {
  it('accepts views and rejects junk', () => {
    expect(parseSnapshot(JSON.stringify(makeView('2026-10-06T15:30:00Z')))).not.toBeNull()
    expect(parseSnapshot('null')).toBeNull()
    expect(parseSnapshot('{"a":1}')).toBeNull()
    expect(parseSnapshot('')).toBeNull()
  })
})

describe('stream edge cases (review)', () => {
  it('frees the connection of a tab that was opened in the background', async () => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    renderHook(() => useLessonStream(5), { wrapper })
    const first = FakeEventSource.latest()
    act(() => first.open())
    await tick(STREAM_TIMINGS.hiddenCloseMs)
    expect(first.closed).toBe(true)

    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(FakeEventSource.instances).toHaveLength(2)
  })

  it('reconnects right away when the browser comes back online', async () => {
    const { result } = renderHook(() => useLessonStream(5), { wrapper })
    act(() => FakeEventSource.latest().fail())
    expect(FakeEventSource.instances).toHaveLength(1)
    act(() => {
      window.dispatchEvent(new Event('online'))
    })
    expect(FakeEventSource.instances).toHaveLength(2)
    act(() => FakeEventSource.latest().open())
    expect(result.current).toBe('live')
    // The cancelled backoff timer must not open a third stream later.
    await tick(STREAM_TIMINGS.reconnectMaxMs)
    expect(FakeEventSource.instances).toHaveLength(2)
  })

  it('ignores "online" while the stream is open', () => {
    renderHook(() => useLessonStream(5), { wrapper })
    act(() => FakeEventSource.latest().open())
    act(() => {
      window.dispatchEvent(new Event('online'))
    })
    expect(FakeEventSource.instances).toHaveLength(1)
  })

  it('polling does not abort a slow request (server slower than the poll interval)', async () => {
    const signals: AbortSignal[] = []
    let calls = 0
    mockFetch({
      'GET /api/lessons/5/attendance': (_url: URL, init: RequestInit) => {
        calls += 1
        signals.push(init.signal!)
        const asOf = new Date().toISOString()
        const delay = calls === 1 ? 0 : 7_000
        return new Promise<Response>((resolve) =>
          setTimeout(
            () => resolve(new Response(JSON.stringify(makeView(asOf, calls)), { headers: { 'Content-Type': 'application/json' } })),
            delay,
          ),
        )
      },
    })
    const { result } = renderHook(() => useLiveLessonView(5), { wrapper })
    await tick(10)
    expect(result.current.data?.counts.online).toBe(1)
    act(() => FakeEventSource.latest().fail()) // stream gone: poll every 5 s

    await tick(STREAM_TIMINGS.pollMs) // poll #1 starts a 7 s request
    expect(calls).toBe(2)
    await tick(STREAM_TIMINGS.pollMs) // poll #2 while it is still running
    expect(signals[1].aborted).toBe(false)
    await tick(2_500)
    expect(result.current.data?.counts.online).toBe(2)
  })
})

describe('createRefresher (review)', () => {
  it('invalidates right away when nothing is loading', () => {
    client.setQueryData(queryKeys.lessons.list({}), [])
    createRefresher(client, queryKeys.lessons.lists())()
    expect(client.getQueryState(queryKeys.lessons.list({}))?.isInvalidated).toBe(true)
  })

  it('never aborts a running request and refetches once after it', async () => {
    const signals: AbortSignal[] = []
    let calls = 0
    mockFetch({
      'GET /api/lessons': (_url: URL, init: RequestInit) => {
        calls += 1
        signals.push(init.signal!)
        return new Promise<Response>((resolve) =>
          setTimeout(() => resolve(new Response('[]', { headers: { 'Content-Type': 'application/json' } })), 1_000),
        )
      },
    })
    const { result } = renderHook(() => useLessons({}), { wrapper })
    await tick(0)
    expect(calls).toBe(1)
    const refresh = createRefresher(client, queryKeys.lessons.lists())
    // A burst of "lessons" events while the first request is still loading.
    refresh()
    refresh()
    refresh()
    expect(signals[0].aborted).toBe(false)
    await tick(1_000) // first request finishes → exactly one more
    expect(calls).toBe(2)
    await tick(1_000)
    expect(calls).toBe(2)
    expect(result.current.isSuccess).toBe(true)
    expect(signals.some((s) => s.aborted)).toBe(false)
  })
})

describe('no EventSource support (review)', () => {
  it('keeps polling through a long hidden period without throwing', async () => {
    vi.stubGlobal('EventSource', undefined)
    let calls = 0
    mockFetch({
      'GET /api/lessons/5/attendance': () => {
        calls += 1
        return new Response(JSON.stringify(makeView(new Date().toISOString())), {
          headers: { 'Content-Type': 'application/json' },
        })
      },
    })
    const { result } = renderHook(() => useLiveLessonView(5), { wrapper })
    await tick(0)
    expect(result.current.connection).toBe('polling')

    Object.defineProperty(document, 'hidden', { configurable: true, value: true })
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    await tick(STREAM_TIMINGS.hiddenCloseMs)
    Object.defineProperty(document, 'hidden', { configurable: true, value: false })
    expect(() =>
      act(() => {
        document.dispatchEvent(new Event('visibilitychange'))
      }),
    ).not.toThrow()
    expect(result.current.connection).toBe('polling')
    const before = calls
    await tick(STREAM_TIMINGS.pollMs)
    expect(calls).toBe(before + 1)
  })
})

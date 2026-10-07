import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useLiveSeconds } from '@/hooks/useLiveSeconds'
import { request } from '@/lib/api'
import { markReceived, noteServerDate, receivedAt } from '@/lib/liveClock'

const T0 = new Date('2026-10-06T15:00:00Z').getTime()

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(T0)
})
afterEach(() => {
  vi.useRealTimers()
})

const tick = (ms: number) =>
  act(() => {
    vi.advanceTimersByTime(ms)
  })

describe('useLiveSeconds', () => {
  it('adds client time elapsed since the snapshot was received while online', () => {
    // The server clock is 10 minutes ahead of the client: must not matter.
    const asOf = '2026-10-06T15:10:00Z'
    markReceived(asOf, T0)
    const { result } = renderHook(() => useLiveSeconds(100, asOf, true))
    expect(result.current).toBe(100)
    tick(1000)
    expect(result.current).toBe(101)
    tick(59_000)
    expect(result.current).toBe(160)
  })

  it('accounts for time between receipt and render', () => {
    const asOf = '2026-10-06T14:00:00Z'
    markReceived(asOf, T0 - 5000)
    const { result } = renderHook(() => useLiveSeconds(10, asOf, true))
    expect(result.current).toBe(15)
  })

  it('is static when the person is not online', () => {
    const asOf = '2026-10-06T15:00:00Z'
    markReceived(asOf, T0)
    const { result } = renderHook(() => useLiveSeconds(42, asOf, false))
    tick(10_000)
    expect(result.current).toBe(42)
  })

  it('restarts from the new total when a new snapshot arrives', () => {
    const first = '2026-10-06T15:00:00Z'
    markReceived(first, T0)
    const { result, rerender } = renderHook(({ total, asOf }) => useLiveSeconds(total, asOf, true), {
      initialProps: { total: 100, asOf: first },
    })
    tick(30_000)
    expect(result.current).toBe(130)
    const second = '2026-10-06T15:00:30Z'
    markReceived(second, Date.now())
    rerender({ total: 131, asOf: second })
    expect(result.current).toBe(131)
    tick(2000)
    expect(result.current).toBe(133)
  })

  it('uses the first time it saw an unknown snapshot', () => {
    const asOf = '2026-10-06T16:00:00Z'
    const { result } = renderHook(() => useLiveSeconds(5, asOf, true))
    expect(result.current).toBe(5)
    expect(receivedAt(asOf)).toBe(T0)
    tick(3000)
    expect(result.current).toBe(8)
  })

  it('never goes below the server total and sanitizes bad input', () => {
    const asOf = '2026-10-06T15:00:00Z'
    markReceived(asOf, T0 + 60_000) // receipt "in the future" relative to the clock
    const { result } = renderHook(() => useLiveSeconds(20, asOf, true))
    expect(result.current).toBe(20)
    const bad = renderHook(() => useLiveSeconds(Number.NaN, null, true))
    expect(bad.result.current).toBe(0)
  })
})

describe('markReceived', () => {
  it('keeps the first receipt time', () => {
    markReceived('a', 1)
    markReceived('a', 2)
    expect(receivedAt('a')).toBe(1)
    markReceived(null)
    expect(receivedAt(null)).toBeUndefined()
  })
})

describe('snapshot age on arrival (review)', () => {
  // Client clock T0 = 15:00:00Z; the server clock is 10 minutes ahead (15:10:00).
  const serverDate = 'Tue, 06 Oct 2026 15:10:00 GMT'

  it('adds the age of a snapshot that was computed long before it was served', () => {
    noteServerDate(serverDate, T0)
    // Stored projection computed 5 minutes ago on the server clock, nobody joined/left since.
    const asOf = '2026-10-06T15:05:00Z'
    markReceived(asOf, T0)
    const { result } = renderHook(() => useLiveSeconds(600, asOf, true))
    expect(result.current).toBe(600 + 300)
    tick(60_000)
    expect(result.current).toBe(600 + 360)
  })

  it('ignores latency-sized ages and the client clock skew for fresh snapshots', () => {
    noteServerDate(serverDate, T0)
    const asOf = '2026-10-06T15:09:59.000Z' // ~1.5 s old: just latency
    markReceived(asOf, T0)
    expect(receivedAt(asOf)).toBe(T0)
    const { result } = renderHook(() => useLiveSeconds(100, asOf, true))
    expect(result.current).toBe(100)
  })

  it('does not use a server clock reading that is too old', () => {
    noteServerDate(serverDate, T0 - 11 * 60_000)
    const asOf = '2026-10-06T15:05:00Z'
    markReceived(asOf, T0)
    expect(receivedAt(asOf)).toBe(T0)
  })

  it('ignores missing or malformed Date headers', () => {
    noteServerDate(null, T0)
    noteServerDate('not a date', T0)
    markReceived('2026-10-06T14:00:00Z', T0)
    expect(receivedAt('2026-10-06T14:00:00Z')).toBe(T0)
  })

  it('reads the Date header of every API response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('{}', { headers: { 'Content-Type': 'application/json', Date: serverDate } })),
    )
    await request('/api/zoom/status')
    vi.unstubAllGlobals()
    markReceived('2026-10-06T15:05:00Z', T0)
    expect(receivedAt('2026-10-06T15:05:00Z')).toBe(T0 - 300_500)
  })
})

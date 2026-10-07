import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { vi } from 'vitest'

import { activeTickerCount, useNow } from '@/hooks/useNow'

beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-10-06T15:00:00Z'))
})
afterEach(() => {
  vi.useRealTimers()
})

describe('useNow', () => {
  it('ticks every second by default', () => {
    const { result } = renderHook(() => useNow())
    const start = result.current
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(result.current).toBe(start + 1000)
    act(() => {
      vi.advanceTimersByTime(3000)
    })
    expect(result.current).toBe(start + 4000)
  })

  it('respects a custom interval', () => {
    const { result } = renderHook(() => useNow(5000))
    const start = result.current
    act(() => {
      vi.advanceTimersByTime(4999)
    })
    expect(result.current).toBe(start)
    act(() => {
      vi.advanceTimersByTime(1)
    })
    expect(result.current).toBe(start + 5000)
  })

  it('does not tick when paused with null', () => {
    const { result } = renderHook(() => useNow(null))
    const start = result.current
    act(() => {
      vi.advanceTimersByTime(10_000)
    })
    expect(result.current).toBe(start)
    expect(activeTickerCount()).toBe(0)
  })

  it('shares one timer between hooks and stops it after the last unmount', () => {
    const a = renderHook(() => useNow())
    const b = renderHook(() => useNow())
    expect(activeTickerCount()).toBe(1)
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(a.result.current).toBe(b.result.current)
    a.unmount()
    expect(activeTickerCount()).toBe(1)
    b.unmount()
    expect(activeTickerCount()).toBe(0)
  })
})

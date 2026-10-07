import { useEffect, useState } from 'react'

// One shared timer per interval keeps 50 live counters on a page from creating
// 50 intervals and makes every counter tick in the same task (React batches the
// resulting state updates into one render).

type Listener = (now: number) => void

interface Ticker {
  listeners: Set<Listener>
  timer: ReturnType<typeof setInterval>
}

const tickers = new Map<number, Ticker>()

/** Subscribes to a shared clock ticking every `intervalMs`. Exported for tests and non-React code. */
export function subscribeNow(intervalMs: number, listener: Listener): () => void {
  let t = tickers.get(intervalMs)
  if (!t) {
    const listeners = new Set<Listener>()
    const timer = setInterval(() => {
      const now = Date.now()
      for (const l of listeners) l(now)
    }, intervalMs)
    t = { listeners, timer }
    tickers.set(intervalMs, t)
  }
  const ticker = t
  ticker.listeners.add(listener)
  return () => {
    ticker.listeners.delete(listener)
    if (ticker.listeners.size === 0) {
      clearInterval(ticker.timer)
      tickers.delete(intervalMs)
    }
  }
}

/** Number of live shared timers (for tests). */
export function activeTickerCount(): number {
  return tickers.size
}

/**
 * Current time in ms (Date.now()), re-rendering every `intervalMs` (default 1 s).
 * Pass `null` or 0 to pause ticking; the hook then keeps returning the last value,
 * which lets callers keep hook order while not needing a clock.
 */
export function useNow(intervalMs: number | null = 1000): number {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (intervalMs === null || intervalMs <= 0) return
    return subscribeNow(intervalMs, setNow)
  }, [intervalMs])
  return now
}

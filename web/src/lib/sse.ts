// Server-Sent Events (docs/architecture.md §5.8): live lesson snapshots and the
// global "lessons changed" signal, with a polling fallback while the stream is broken.

import { useQueryClient, type QueryClient, type QueryKey } from '@tanstack/react-query'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'

import { api, GLOBAL_STREAM_URL } from '@/lib/api'
import { markReceived } from '@/lib/liveClock'
import { isNewerView, queryKeys, useLessonView } from '@/lib/queries'
import { isLessonView, type LessonView } from '@/lib/types'

/**
 * - `live`: the stream is open; data arrives as it changes.
 * - `reconnecting`: connecting for the first time or after a short drop
 *   (data is polled every few seconds meanwhile).
 * - `polling`: the stream has been broken for a while (or is unsupported);
 *   data is refreshed by polling until it comes back.
 */
export type StreamState = 'live' | 'reconnecting' | 'polling'

export const STREAM_TIMINGS = {
  /** Polling interval while the stream is not live. */
  pollMs: 5_000,
  /** How long a non-live stream counts as "reconnecting" before it is shown as "polling". */
  graceMs: 10_000,
  /** Manual reconnect backoff after the browser gave up (readyState CLOSED). */
  reconnectBaseMs: 2_000,
  reconnectMaxMs: 30_000,
  /** A tab hidden this long closes its stream to free the connection; it reconnects when shown. */
  hiddenCloseMs: 60_000,
}

export interface EventSourceOptions {
  /** Named SSE events to listen for (`event: <name>`). */
  events: readonly string[]
  onEvent: (name: string, event: MessageEvent<string>) => void
  /** Called on every successful open; `reconnected` is false for the first one. */
  onOpen?: (info: { reconnected: boolean }) => void
  /** Called every `pollMs` while the stream is not live and the tab is visible. */
  onPoll?: () => void
  pollMs?: number
}

/**
 * Low-level EventSource hook with reconnect, polling fallback and background-tab
 * pausing. `url = null` disables it. Handlers may change between renders without
 * reconnecting; changing `url` or `events` reconnects.
 */
export function useEventSource(url: string | null, options: EventSourceOptions): StreamState {
  const optsRef = useRef(options)
  useLayoutEffect(() => {
    optsRef.current = options
  })
  const [state, setState] = useState<StreamState>('reconnecting')
  const eventsKey = options.events.join('\n')

  useEffect(() => {
    if (!url) return
    const eventNames = eventsKey ? eventsKey.split('\n') : []
    const T = STREAM_TIMINGS
    const pollMs = optsRef.current.pollMs ?? T.pollMs

    let es: EventSource | null = null
    let disposed = false
    let everOpened = false
    let paused = false
    let attempt = 0
    let current: StreamState = 'reconnecting'
    let graceTimer: ReturnType<typeof setTimeout> | null = null
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let hiddenTimer: ReturnType<typeof setTimeout> | null = null
    let pollTimer: ReturnType<typeof setInterval> | null = null

    const updatePolling = () => {
      const shouldPoll = current !== 'live' && !paused && !document.hidden && !disposed
      if (shouldPoll && pollTimer === null) {
        pollTimer = setInterval(() => optsRef.current.onPoll?.(), pollMs)
      } else if (!shouldPoll && pollTimer !== null) {
        clearInterval(pollTimer)
        pollTimer = null
      }
    }

    const set = (next: StreamState) => {
      current = next
      if (!disposed) setState(next)
      updatePolling()
    }

    const clearGrace = () => {
      if (graceTimer !== null) clearTimeout(graceTimer)
      graceTimer = null
    }

    const startGrace = () => {
      if (graceTimer !== null) return
      graceTimer = setTimeout(() => {
        graceTimer = null
        if (current !== 'live') set('polling')
      }, T.graceMs)
    }

    const closeSource = () => {
      if (es) {
        es.onopen = null
        es.onerror = null
        es.close()
        es = null
      }
    }

    const connect = () => {
      if (disposed || paused) return
      reconnectTimer = null
      if (typeof EventSource === 'undefined') {
        // No SSE support (old browser / embedded webview): polling only.
        set('polling')
        return
      }
      closeSource()
      const source = new EventSource(url)
      es = source
      source.onopen = () => {
        if (es !== source) return
        attempt = 0
        clearGrace()
        const reconnected = everOpened
        everOpened = true
        set('live')
        optsRef.current.onOpen?.({ reconnected })
      }
      source.onerror = () => {
        if (es !== source) return
        if (source.readyState === EventSource.CLOSED) {
          // The browser gave up (HTTP error, wrong content type): retry ourselves.
          closeSource()
          clearGrace()
          set('polling')
          const delay = Math.min(T.reconnectMaxMs, T.reconnectBaseMs * 2 ** attempt)
          attempt += 1
          reconnectTimer = setTimeout(connect, delay)
        } else {
          // CONNECTING: the browser retries on its own.
          if (current === 'live') set('reconnecting')
          startGrace()
        }
      }
      for (const name of eventNames) {
        source.addEventListener(name, (ev) => {
          if (es !== source) return
          optsRef.current.onEvent(name, ev as MessageEvent<string>)
        })
      }
      if (current !== 'live') startGrace()
    }

    const onVisibility = () => {
      if (document.hidden) {
        if (hiddenTimer === null && !paused) {
          hiddenTimer = setTimeout(() => {
            hiddenTimer = null
            paused = true
            closeSource()
            clearGrace()
            if (reconnectTimer !== null) clearTimeout(reconnectTimer)
            reconnectTimer = null
            updatePolling()
          }, T.hiddenCloseMs)
        }
      } else {
        if (hiddenTimer !== null) clearTimeout(hiddenTimer)
        hiddenTimer = null
        if (paused) {
          paused = false
          attempt = 0
          if (typeof EventSource !== 'undefined') set('reconnecting')
          connect()
        }
      }
      updatePolling()
    }

    // Back from offline (laptop woke up, Wi-Fi returned): skip the remaining backoff.
    const onOnline = () => {
      if (disposed || paused || es !== null || reconnectTimer === null) return
      clearTimeout(reconnectTimer)
      reconnectTimer = null
      attempt = 0
      connect()
    }

    if (typeof EventSource === 'undefined') {
      set('polling')
    } else {
      set('reconnecting')
      connect()
    }
    window.addEventListener('online', onOnline)
    document.addEventListener('visibilitychange', onVisibility)
    // Opened in a background tab: start the countdown that frees the connection.
    if (document.hidden) onVisibility()

    return () => {
      disposed = true
      document.removeEventListener('visibilitychange', onVisibility)
      window.removeEventListener('online', onOnline)
      closeSource()
      clearGrace()
      if (reconnectTimer !== null) clearTimeout(reconnectTimer)
      if (hiddenTimer !== null) clearTimeout(hiddenTimer)
      if (pollTimer !== null) clearInterval(pollTimer)
    }
  }, [url, eventsKey])

  return url ? state : 'reconnecting'
}

// ---------------------------------------------------------------------------
// Lesson stream
// ---------------------------------------------------------------------------

const LESSON_EVENTS = ['snapshot'] as const
const GLOBAL_EVENTS = ['lessons'] as const

/** Parses an SSE snapshot payload; null when malformed. */
export function parseSnapshot(data: string): LessonView | null {
  try {
    const view: unknown = JSON.parse(data)
    return isLessonView(view) ? view : null
  } catch {
    return null
  }
}

/**
 * Subscribes to `/api/lessons/{id}/stream`. Every `snapshot` event replaces the
 * cached LessonView (queryKeys.lessons.view(id)); while the stream is broken the
 * view is refetched every 5 s. Closes the stream on unmount. Attendance changes
 * still being saved stay visible on top of snapshots (useLessonView overlays them).
 */
export function useLessonStream(lessonId: number | undefined): StreamState {
  const qc = useQueryClient()
  const enabled = typeof lessonId === 'number' && lessonId > 0
  return useEventSource(enabled ? api.lessons.streamUrl(lessonId) : null, {
    events: LESSON_EVENTS,
    onEvent: (_name, ev) => {
      if (!enabled) return
      const view = parseSnapshot(ev.data)
      if (!view) return
      markReceived(view.as_of)
      const key = queryKeys.lessons.view(lessonId)
      // A slower poll/refetch must not overwrite this fresher snapshot.
      void qc.cancelQueries({ queryKey: key })
      const current = qc.getQueryData<LessonView>(key)
      if (isNewerView(view, current)) qc.setQueryData(key, view)
    },
    onPoll: () => {
      // cancelRefetch: false — a slow server (response > poll interval) must not have every
      // request aborted by the next poll, or the view would never update.
      if (enabled) void qc.invalidateQueries({ queryKey: queryKeys.lessons.view(lessonId) }, { cancelRefetch: false })
    },
  })
}

/**
 * LessonView query + live stream in one hook — what the live lesson screen needs.
 * Returns the query result plus `connection` (StreamState).
 */
export function useLiveLessonView(lessonId: number | undefined) {
  const connection = useLessonStream(lessonId)
  const query = useLessonView(lessonId)
  return { ...query, connection }
}

// ---------------------------------------------------------------------------
// Global stream
// ---------------------------------------------------------------------------

/**
 * Returns a function that refreshes the queries under `queryKey` after a change
 * notification without starving them: a request that is already running is not
 * aborted (with frequent events and a slow server an abort-and-restart policy
 * would never let one finish), and because that response may predate the
 * change, exactly one more refetch is queued for when it settles.
 */
export function createRefresher(qc: QueryClient, queryKey: QueryKey): () => void {
  let queued = false
  const refresh = (): void => {
    const running = qc
      .getQueryCache()
      .findAll({ queryKey, fetchStatus: 'fetching' })
      .map((q) => q.promise)
      .filter((p) => p !== undefined)
    if (running.length === 0) {
      void qc.invalidateQueries({ queryKey })
      return
    }
    if (queued) return
    queued = true
    void Promise.allSettled(running).then(() => {
      queued = false
      refresh()
    })
  }
  return refresh
}

/**
 * Subscribes to `/api/stream`; each `lessons` event (and every reconnect)
 * refreshes the lesson lists so dashboards show live counts. Mount it only on
 * pages that show lesson lists — every stream holds one HTTP connection.
 */
export function useGlobalStream(): StreamState {
  const qc = useQueryClient()
  const refresh = useMemo(() => createRefresher(qc, queryKeys.lessons.lists()), [qc])
  return useEventSource(GLOBAL_STREAM_URL, {
    events: GLOBAL_EVENTS,
    onEvent: refresh,
    onOpen: ({ reconnected }) => {
      if (reconnected) refresh()
    },
    onPoll: refresh,
    pollMs: 15_000,
  })
}

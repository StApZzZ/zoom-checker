import type { ReactElement, ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import { createMemoryRouter, RouterProvider, type RouteObject } from 'react-router'
import { vi } from 'vitest'

import { TooltipProvider } from '@/components/ui/tooltip'
import type { User } from '@/lib/types'

/** QueryClient for tests: no retries, no refetch on focus, no cache GC timers. */
export function createTestQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchOnWindowFocus: false, gcTime: Infinity, staleTime: Infinity },
      mutations: { retry: false },
    },
  })
}

export function Providers({ client, children }: { client: QueryClient; children: ReactNode }) {
  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>{children}</TooltipProvider>
    </QueryClientProvider>
  )
}

export function renderWithClient(ui: ReactElement, client: QueryClient = createTestQueryClient()) {
  return { client, ...render(<Providers client={client}>{ui}</Providers>) }
}

/** Renders a route table in a memory router at `path`. */
export function renderRoutes(routes: RouteObject[], path: string, client: QueryClient = createTestQueryClient()) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  const utils = render(
    <Providers client={client}>
      <RouterProvider router={router} />
    </Providers>,
  )
  return { client, router, ...utils }
}

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json; charset=utf-8' },
  })
}

export function errorResponse(status: number, code: string, message: string, details?: unknown): Response {
  return jsonResponse({ error: { code, message, ...(details ? { details } : {}) } }, status)
}

type Handler = (url: URL, init: RequestInit) => Response | Promise<Response>

/**
 * Stubs global fetch with a route table: keys are "METHOD /path" (query string
 * ignored). Unmatched requests return 404 with an error envelope.
 */
export function mockFetch(handlers: Record<string, Handler | unknown>) {
  const fn = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://localhost')
    const method = (init.method ?? 'GET').toUpperCase()
    const handler = handlers[`${method} ${url.pathname}`]
    if (handler === undefined) return errorResponse(404, 'not_found', `no mock for ${method} ${url.pathname}`)
    if (typeof handler === 'function') return (handler as Handler)(url, init)
    return jsonResponse(handler)
  })
  vi.stubGlobal('fetch', fn)
  return fn
}

export function makeUser(overrides: Partial<User> = {}): User {
  return {
    id: 1,
    username: 'ivanova',
    display_name: 'Мария Иванова',
    email: 'ivanova@example.com',
    role: 'teacher',
    auth_source: 'local',
    must_change_password: false,
    disabled: false,
    last_login_at: null,
    ...overrides,
  }
}

/**
 * Stubs what saving a download needs (object URLs, the hidden anchor's click).
 * `anchors()` returns the clicked anchors: their `download` is the file name.
 */
export function stubDownloads() {
  const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:zoom-checker/1')
  const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  return { createObjectURL, revokeObjectURL, click, anchors: () => click.mock.contexts as HTMLAnchorElement[] }
}

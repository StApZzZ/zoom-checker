import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  api,
  ApiError,
  buildUrl,
  downloadFile,
  errorFromResponse,
  errorMessage,
  filenameFromContentDisposition,
  loginUrl,
  request,
  safeNextPath,
  setUnauthorizedHandler,
} from '@/lib/api'
import { browserTimeZone } from '@/lib/format'
import { errorResponse, jsonResponse, makeUser, mockFetch, stubDownloads } from '@/test/utils'

afterEach(() => {
  vi.unstubAllGlobals()
})

function lastCall(fetchMock: ReturnType<typeof mockFetch>) {
  const [input, init] = fetchMock.mock.calls.at(-1)!
  return { url: String(input), init: init as RequestInit }
}

describe('buildUrl', () => {
  it('skips empty values and encodes the rest', () => {
    expect(buildUrl('/api/lessons', { from: '2026-10-01', to: undefined, group_id: 3, status: null, q: '' })).toBe(
      '/api/lessons?from=2026-10-01&group_id=3',
    )
    expect(buildUrl('/api/zoom/parse', { input: 'https://zoom.us/j/123?pwd=a&b' })).toBe(
      '/api/zoom/parse?input=https%3A%2F%2Fzoom.us%2Fj%2F123%3Fpwd%3Da%26b',
    )
    expect(buildUrl('/x')).toBe('/x')
    expect(buildUrl('/x?a=1', { b: true })).toBe('/x?a=1&b=true')
  })
})

describe('request', () => {
  it('sends same-origin credentials and JSON bodies', async () => {
    const fetchMock = mockFetch({ 'POST /api/groups': (_u: URL, init: RequestInit) => jsonResponse({ echoed: JSON.parse(String(init.body)) }, 201) })
    const res = await request<{ echoed: unknown }>('/api/groups', { method: 'POST', body: { name: 'DevOps' } })
    expect(res.echoed).toEqual({ name: 'DevOps' })
    const { init } = lastCall(fetchMock)
    expect(init.credentials).toBe('same-origin')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect((init.headers as Record<string, string>).Accept).toBe('application/json')
  })

  it('does not set Content-Type for GET requests', async () => {
    const fetchMock = mockFetch({ 'GET /api/groups': [] })
    await api.groups.list('all')
    const { url, init } = lastCall(fetchMock)
    expect(url).toBe('/api/groups?status=all')
    expect((init.headers as Record<string, string>)['Content-Type']).toBeUndefined()
    expect(init.body).toBeUndefined()
  })

  it('returns undefined for 204 and empty bodies', async () => {
    mockFetch({ 'DELETE /api/users/7': () => new Response(null, { status: 204 }) })
    await expect(api.admin.users.remove(7)).resolves.toBeUndefined()
  })

  it('returns text for non-JSON responses', async () => {
    mockFetch({ 'GET /healthz': () => new Response('ok', { headers: { 'Content-Type': 'text/plain' } }) })
    await expect(request<string>('/healthz')).resolves.toBe('ok')
  })

  it('parses the error envelope into ApiError', async () => {
    mockFetch({
      'POST /api/groups': () =>
        errorResponse(422, 'validation', 'Группа с таким названием уже есть', { field: 'name' }),
    })
    const err = await api.groups.create({ name: 'DevOps' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    const e = err as ApiError
    expect(e.status).toBe(422)
    expect(e.code).toBe('validation')
    expect(e.message).toBe('Группа с таким названием уже есть')
    expect(e.details).toEqual({ field: 'name' })
    expect(e.isValidation).toBe(true)
  })

  it('falls back to a Russian message when there is no envelope', async () => {
    mockFetch({ 'GET /api/groups': () => new Response('<html>Bad gateway</html>', { status: 502 }) })
    const e = (await api.groups.list().catch((x: unknown) => x)) as ApiError
    expect(e.status).toBe(502)
    expect(e.code).toBe('internal')
    expect(e.message).toMatch(/Сервер временно недоступен/)
  })

  it('turns network failures into ApiError(0, network)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    const e = (await api.groups.list().catch((x: unknown) => x)) as ApiError
    expect(e).toBeInstanceOf(ApiError)
    expect(e.isNetwork).toBe(true)
    expect(e.code).toBe('network')
    expect(e.message).toMatch(/Нет связи с сервером/)
  })

  it('rethrows aborts untouched', async () => {
    const abort = new DOMException('aborted', 'AbortError')
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(abort))
    await expect(api.groups.list()).rejects.toBe(abort)
  })

  it('rejects malformed JSON with an ApiError', async () => {
    mockFetch({ 'GET /api/groups': () => new Response('{oops', { headers: { 'Content-Type': 'application/json' } }) })
    await expect(api.groups.list()).rejects.toBeInstanceOf(ApiError)
  })
})

describe('401 handling', () => {
  let handler: ReturnType<typeof vi.fn<(e: ApiError) => void>>
  let restore: () => void
  beforeEach(() => {
    handler = vi.fn<(e: ApiError) => void>()
    restore = setUnauthorizedHandler(handler)
  })
  afterEach(() => restore())

  it('calls the unauthorized handler for regular requests', async () => {
    mockFetch({ 'GET /api/groups': () => errorResponse(401, 'unauthorized', 'Нужно войти в систему') })
    await expect(api.groups.list()).rejects.toMatchObject({ status: 401 })
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler.mock.calls[0][0]).toBeInstanceOf(ApiError)
  })

  it('does not redirect for login, me and password change', async () => {
    mockFetch({
      'POST /api/auth/login': () => errorResponse(401, 'unauthorized', 'Неверный логин или пароль'),
      'GET /api/auth/me': () => errorResponse(401, 'unauthorized', 'Нужно войти в систему'),
      'POST /api/auth/password': () => errorResponse(401, 'unauthorized', 'Неверный текущий пароль'),
    })
    await expect(api.auth.login({ username: 'a', password: 'b' })).rejects.toMatchObject({
      message: 'Неверный логин или пароль',
    })
    await expect(api.auth.me()).rejects.toMatchObject({ status: 401 })
    await expect(api.auth.changePassword({ current_password: 'a', new_password: 'b' })).rejects.toMatchObject({
      status: 401,
    })
    expect(handler).not.toHaveBeenCalled()
  })

  it('restores the previous handler', async () => {
    const inner = vi.fn<(e: ApiError) => void>()
    const undo = setUnauthorizedHandler(inner)
    undo()
    mockFetch({ 'GET /api/groups': () => errorResponse(401, 'unauthorized', 'x') })
    await api.groups.list().catch(() => {})
    expect(inner).not.toHaveBeenCalled()
    expect(handler).toHaveBeenCalledTimes(1)
  })
})

describe('endpoints', () => {
  it('auth.me accepts both User and {user}', async () => {
    const user = makeUser()
    mockFetch({ 'GET /api/auth/me': user })
    await expect(api.auth.me()).resolves.toEqual(user)
    mockFetch({ 'GET /api/auth/me': { user } })
    await expect(api.auth.me()).resolves.toEqual(user)
  })

  it('builds the OIDC login URL', () => {
    expect(api.auth.oidcLoginUrl('/lessons/5?tab=a')).toBe('/auth/oidc/login?next=%2Flessons%2F5%3Ftab%3Da')
    expect(api.auth.oidcLoginUrl('/')).toBe('/auth/oidc/login')
    expect(api.auth.oidcLoginUrl()).toBe('/auth/oidc/login')
    // Another account: the identity provider asks for credentials again.
    expect(api.auth.oidcLoginUrl('/zoom', { prompt: 'login' })).toBe('/auth/oidc/login?next=%2Fzoom&prompt=login')
    expect(api.auth.oidcLoginUrl(undefined, { prompt: 'select_account' })).toBe('/auth/oidc/login?prompt=select_account')
  })

  it('import preview sends pasted text as JSON with the mode', async () => {
    const fetchMock = mockFetch({ 'POST /api/groups/4/import/preview': { mode: 'replace', rows: [] } })
    await api.groups.importPreview(4, { text: 'Иванов Иван\nПетров Пётр' }, 'replace')
    const { url, init } = lastCall(fetchMock)
    expect(url).toBe('/api/groups/4/import/preview?mode=replace')
    expect(JSON.parse(String(init.body))).toEqual({ text: 'Иванов Иван\nПетров Пётр' })
  })

  it('import preview uploads a file as multipart without a JSON content type', async () => {
    const fetchMock = mockFetch({ 'POST /api/groups/4/import/preview': { mode: 'add', rows: [] } })
    const file = new File(['ФИО\nИванов Иван'], 'list.csv', { type: 'text/csv' })
    await api.groups.importPreview(4, { file })
    const { url, init } = lastCall(fetchMock)
    expect(url).toBe('/api/groups/4/import/preview?mode=add')
    expect(init.body).toBeInstanceOf(FormData)
    const sent = (init.body as FormData).get('file') as File
    expect(sent.name).toBe('list.csv')
    expect((init.headers as Record<string, string>)['Content-Type']).toBeUndefined()
  })

  it('lessons.list passes the filter as query parameters', async () => {
    const fetchMock = mockFetch({ 'GET /api/lessons': [] })
    await api.lessons.list({ from: '2026-10-01T00:00:00Z', group_id: 2, status: 'active' })
    expect(lastCall(fetchMock).url).toBe('/api/lessons?from=2026-10-01T00%3A00%3A00Z&group_id=2&status=active')
  })

  it('attendance update sends null to return to the automatic status', async () => {
    const fetchMock = mockFetch({ 'PATCH /api/lessons/9/attendance/31': {} })
    await api.lessons.updateAttendance(9, 31, { status: null, reason: 'ошибка' })
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ status: null, reason: 'ошибка' })
  })

  it('zoom.linkSession sends lesson_id (null unlinks)', async () => {
    const fetchMock = mockFetch({ 'POST /api/zoom/sessions/3/link': {} })
    await api.zoom.linkSession(3, null)
    expect(JSON.parse(String(lastCall(fetchMock).init.body))).toEqual({ lesson_id: null })
  })

  it('events.list passes paging and filters', async () => {
    const fetchMock = mockFetch({ 'GET /api/zoom/events': { items: [], next_before_id: null } })
    await api.admin.events.list({ status: 'failed', before_id: 120, limit: 50 })
    expect(lastCall(fetchMock).url).toBe('/api/zoom/events?status=failed&before_id=120&limit=50')
  })

  it('zoom.sessions only sends unlinked=1 and limit when asked', async () => {
    const fetchMock = mockFetch({ 'GET /api/zoom/sessions': [] })
    await api.zoom.sessions()
    expect(lastCall(fetchMock).url).toBe('/api/zoom/sessions')
    await api.zoom.sessions({ unlinked: true })
    expect(lastCall(fetchMock).url).toBe('/api/zoom/sessions?unlinked=1')
    await api.zoom.sessions({ unlinked: true, limit: 500 })
    expect(lastCall(fetchMock).url).toBe('/api/zoom/sessions?unlinked=1&limit=500')
    await api.zoom.sessions({ limit: 200 })
    expect(lastCall(fetchMock).url).toBe('/api/zoom/sessions?limit=200')
  })

  it('builds export URLs that ask for the times in the browser zone', () => {
    const tz = new URLSearchParams({ tz: browserTimeZone() }).toString()
    expect(browserTimeZone()).not.toBe('')
    expect(api.lessons.exportUrl(5, 'xlsx')).toBe(`/api/lessons/5/export?format=xlsx&${tz}`)
    expect(api.groups.reportExportUrl(2, 'csv', { from: '2026-09-01', to: '2026-10-01' })).toBe(
      `/api/groups/2/report/export?format=csv&from=2026-09-01&to=2026-10-01&${tz}`,
    )
    expect(api.lessons.streamUrl(5)).toBe('/api/lessons/5/stream')
  })
})

describe('downloadFile', () => {
  const disposition = `attachment; filename="lesson-5.xlsx"; filename*=UTF-8''${encodeURIComponent('Посещаемость Linux 06.10.2026.xlsx')}`

  it('fetches the file and saves it under the server file name', async () => {
    const { anchors, createObjectURL, revokeObjectURL } = stubDownloads()
    vi.useFakeTimers()
    try {
      const fetchMock = mockFetch({
        'GET /api/lessons/5/export': () => new Response('bytes', { headers: { 'Content-Disposition': disposition } }),
      })
      await expect(downloadFile('/api/lessons/5/export?format=xlsx', 'lesson-5.xlsx')).resolves.toBe(
        'Посещаемость Linux 06.10.2026.xlsx',
      )
      const { url, init } = lastCall(fetchMock)
      expect(url).toBe('/api/lessons/5/export?format=xlsx')
      expect(init.credentials).toBe('same-origin')
      expect(createObjectURL).toHaveBeenCalledTimes(1)
      expect(anchors()).toHaveLength(1)
      expect(anchors()[0].download).toBe('Посещаемость Linux 06.10.2026.xlsx')
      expect(anchors()[0].getAttribute('href')).toBe('blob:zoom-checker/1')
      // The temporary anchor is gone; the object URL is freed a bit later.
      expect(document.querySelector('a[download]')).toBeNull()
      expect(revokeObjectURL).not.toHaveBeenCalled()
      vi.advanceTimersByTime(60_000)
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:zoom-checker/1')
    } finally {
      vi.useRealTimers()
    }
  })

  it('falls back to the given name without Content-Disposition', async () => {
    const { anchors } = stubDownloads()
    mockFetch({ 'GET /api/lessons/5/export': () => new Response('bytes') })
    await expect(downloadFile('/api/lessons/5/export?format=csv', 'lesson-5.csv')).resolves.toBe('lesson-5.csv')
    expect(anchors()[0].download).toBe('lesson-5.csv')
  })

  it('rejects with the server message and saves nothing', async () => {
    const { click } = stubDownloads()
    mockFetch({ 'GET /api/lessons/5/export': () => errorResponse(404, 'not_found', 'Занятие не найдено') })
    const err = await downloadFile('/api/lessons/5/export?format=csv').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).message).toBe('Занятие не найдено')
    expect(click).not.toHaveBeenCalled()
  })

  it('sends an expired session to the login page', async () => {
    const { click } = stubDownloads()
    const handler = vi.fn()
    const restore = setUnauthorizedHandler(handler)
    try {
      mockFetch({ 'GET /api/groups/2/report/export': () => errorResponse(401, 'unauthorized', 'Нужно войти') })
      await expect(api.groups.downloadReport(2, 'xlsx')).rejects.toMatchObject({ status: 401 })
      expect(handler).toHaveBeenCalledTimes(1)
      expect(click).not.toHaveBeenCalled()
    } finally {
      restore()
    }
  })

  it('reports a network failure in plain words', async () => {
    stubDownloads()
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )
    await expect(api.lessons.downloadExport(5, 'csv')).rejects.toMatchObject({ status: 0, code: 'network' })
  })
})

describe('filenameFromContentDisposition', () => {
  it.each([
    [null, null],
    ['', null],
    ['attachment', null],
    ['attachment; filename="report.csv"', 'report.csv'],
    ['attachment; filename=report.csv', 'report.csv'],
    ['attachment; filename="a \\"b\\".csv"', 'a "b".csv'],
    ['attachment; filename="a;b.csv"', 'a;b.csv'],
    // filename* (RFC 5987) wins, whatever the order.
    [`attachment; filename*=UTF-8''%D0%9E%D1%82%D1%87%D1%91%D1%82.xlsx; filename="report.xlsx"`, 'Отчёт.xlsx'],
    [`attachment; filename="report.xlsx"; filename*=utf-8'ru'%D0%9E%D1%82%D1%87%D1%91%D1%82.xlsx`, 'Отчёт.xlsx'],
    [`attachment; filename*=iso-8859-1'en'caf%E9.csv`, 'café.csv'],
    // Broken encodings fall back to the plain name.
    [`attachment; filename="report.csv"; filename*=UTF-8''%E0%A4%A.csv`, 'report.csv'],
    [`attachment; filename="report.csv"; filename*=koi8-r''%F0.csv`, 'report.csv'],
    // No directories, no control characters.
    ['attachment; filename="../../etc/passwd"', '.._.._etc_passwd'],
    [`attachment; filename*=UTF-8''a%0Ab.csv`, 'ab.csv'],
    ['attachment; filename=".."', null],
  ])('%j → %j', (header, expected) => {
    expect(filenameFromContentDisposition(header)).toBe(expected)
  })
})

describe('helpers', () => {
  it('errorMessage handles any thrown value', () => {
    expect(errorMessage(new ApiError(404, 'not_found', 'Занятие не найдено'))).toBe('Занятие не найдено')
    expect(errorMessage(new Error('boom'))).toMatch(/Что-то пошло не так/)
    expect(errorMessage('x')).toMatch(/Что-то пошло не так/)
  })

  it('errorFromResponse maps statuses to codes', async () => {
    expect((await errorFromResponse(new Response('', { status: 403 }))).code).toBe('forbidden')
    expect((await errorFromResponse(new Response('', { status: 404 }))).code).toBe('not_found')
    expect((await errorFromResponse(new Response('', { status: 429 }))).code).toBe('rate_limited')
  })

  it('loginUrl carries the current path as next', () => {
    expect(loginUrl('/lessons/5?x=1')).toBe('/login?next=%2Flessons%2F5%3Fx%3D1')
    expect(loginUrl('/')).toBe('/login')
    expect(loginUrl('/login?next=/a')).toBe('/login')
  })

  it.each([
    [null, '/'],
    ['', '/'],
    ['/lessons/5', '/lessons/5'],
    ['/groups?status=all#x', '/groups?status=all#x'],
    ['//evil.example', '/'],
    ['/\\evil.example', '/'],
    ['https://evil.example', '/'],
    ['javascript:alert(1)', '/'],
    ['/login', '/'],
    ['/login?next=/x', '/'],
    ['/a\nb', '/'],
  ])('safeNextPath(%j) = %j', (input, expected) => {
    expect(safeNextPath(input)).toBe(expected)
  })
})

describe('api.admin.system.testLdap (review)', () => {
  const steps = [
    { name: 'Подключение', ok: true, detail: 'ldaps://dc1:636' },
    { name: 'Поиск пользователя', ok: false, detail: 'Пользователь не найден' },
  ]

  it('passes { ok, steps } through', async () => {
    mockFetch({ 'POST /api/system/test-ldap': { ok: false, steps } })
    await expect(api.admin.system.testLdap({ username: 'ivanova' })).resolves.toEqual({ ok: false, steps })
  })

  it('accepts a bare DiagStep[] body (Go auth.DiagStep) and derives ok', async () => {
    mockFetch({ 'POST /api/system/test-ldap': steps })
    await expect(api.admin.system.testLdap()).resolves.toEqual({ ok: false, steps })
    mockFetch({ 'POST /api/system/test-ldap': [steps[0]] })
    await expect(api.admin.system.testLdap()).resolves.toEqual({ ok: true, steps: [steps[0]] })
  })

  it('derives ok when the server omits it and survives an empty body', async () => {
    mockFetch({ 'POST /api/system/test-ldap': { steps: [steps[0]] } })
    await expect(api.admin.system.testLdap()).resolves.toEqual({ ok: true, steps: [steps[0]] })
    mockFetch({ 'POST /api/system/test-ldap': () => new Response(null, { status: 204 }) })
    await expect(api.admin.system.testLdap()).resolves.toEqual({ ok: false, steps: [] })
  })
})

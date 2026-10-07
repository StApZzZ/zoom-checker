// Thin fetch wrapper around the Zoom Checker HTTP API (docs/architecture.md §6).
// All server calls of the SPA go through this module.

import type {
  ApiErrorCode,
  ApiErrorEnvelope,
  AuditEntry,
  AuthConfig,
  ChangePasswordInput,
  CreateGroupInput,
  CreateLessonInput,
  CreateStudentInput,
  CreateUserInput,
  DateRange,
  DiagnosticStep,
  EventsFilter,
  ExportFormat,
  FinishLessonInput,
  GroupDetail,
  GroupReport,
  GroupStatusFilter,
  GroupSummary,
  ImportCommitInput,
  ImportMode,
  ImportNameFormat,
  ImportPreview,
  ImportResult,
  ImportSource,
  Lesson,
  LessonFilter,
  LessonSummary,
  LessonView,
  LoginInput,
  LoginResponse,
  LogoutResponse,
  MapParticipantInput,
  OidcPrompt,
  Participant,
  ReprocessResult,
  Settings,
  Student,
  StudentHistory,
  SystemInfo,
  TestLdapInput,
  TestLdapResult,
  UpdateAttendanceInput,
  UpdateGroupInput,
  UpdateLessonInput,
  UpdateStudentInput,
  UpdateUserInput,
  User,
  WebhookEventDetail,
  WebhookEventsPage,
  ZoomParseResult,
  ZoomSession,
  ZoomSessionsFilter,
  ZoomStatus,
} from '@/lib/types'
import { browserTimeZone } from '@/lib/format'
import { noteServerDate } from '@/lib/liveClock'

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

/** Error thrown for every failed API call (HTTP error or network failure). */
export class ApiError extends Error {
  /** HTTP status; 0 for network failures. */
  readonly status: number
  /** Machine code from the error envelope (validation, not_found, ...). */
  readonly code: ApiErrorCode
  /** Extra data from the envelope, e.g. field errors or an import preview. */
  readonly details: Record<string, unknown>

  constructor(status: number, code: ApiErrorCode, message: string, details?: Record<string, unknown> | null) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details ?? {}
  }

  get isUnauthorized(): boolean {
    return this.status === 401
  }

  get isForbidden(): boolean {
    return this.status === 403
  }

  get isNotFound(): boolean {
    return this.status === 404
  }

  get isValidation(): boolean {
    return this.status === 422 || this.code === 'validation'
  }

  get isNetwork(): boolean {
    return this.status === 0
  }
}

export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

const NETWORK_MESSAGE = 'Нет связи с сервером. Проверьте подключение к сети и попробуйте ещё раз.'
const FALLBACK_MESSAGE = 'Что-то пошло не так. Попробуйте ещё раз.'

/** Russian fallback messages used when the server did not send an envelope. */
function statusMessage(status: number): string {
  switch (status) {
    case 400:
      return 'Запрос не удалось обработать. Проверьте введённые данные.'
    case 401:
      return 'Сессия истекла. Войдите снова.'
    case 403:
      return 'Недостаточно прав для этого действия.'
    case 404:
      return 'Не найдено. Возможно, запись уже удалена.'
    case 409:
      return 'Данные изменились. Обновите страницу и попробуйте ещё раз.'
    case 413:
      return 'Файл слишком большой.'
    case 422:
      return 'Проверьте введённые данные.'
    case 429:
      return 'Слишком много попыток. Подождите минуту и попробуйте снова.'
    case 502:
    case 503:
    case 504:
      return 'Сервер временно недоступен. Попробуйте через минуту.'
    default:
      return status >= 500 ? 'Ошибка на сервере. Попробуйте ещё раз чуть позже.' : FALLBACK_MESSAGE
  }
}

function statusCode(status: number): ApiErrorCode {
  switch (status) {
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    case 422:
      return 'validation'
    case 429:
      return 'rate_limited'
    default:
      return status >= 500 ? 'internal' : `http_${status}`
  }
}

function isEnvelope(body: unknown): body is ApiErrorEnvelope {
  if (typeof body !== 'object' || body === null || !('error' in body)) return false
  const e = (body as { error: unknown }).error
  return typeof e === 'object' && e !== null && typeof (e as { message?: unknown }).message === 'string'
}

/** Converts any failed Response into an ApiError, reading the error envelope when present. */
export async function errorFromResponse(res: Response): Promise<ApiError> {
  let body: unknown = null
  try {
    const text = await res.text()
    body = text ? JSON.parse(text) : null
  } catch {
    body = null
  }
  if (isEnvelope(body)) {
    const { code, message, details } = body.error
    return new ApiError(
      res.status,
      code || statusCode(res.status),
      message.trim() || statusMessage(res.status),
      details && typeof details === 'object' ? details : null,
    )
  }
  return new ApiError(res.status, statusCode(res.status), statusMessage(res.status))
}

/** Human readable Russian message for any thrown value (for toasts and error states). */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof Error && err.name === 'AbortError') return 'Запрос отменён.'
  return FALLBACK_MESSAGE
}

// ---------------------------------------------------------------------------
// Unauthorized handling
// ---------------------------------------------------------------------------

export type UnauthorizedHandler = (error: ApiError) => void

/** Path + query + hash of the current page, used as `next` after login. */
export function currentPath(): string {
  const { pathname, search, hash } = window.location
  return `${pathname}${search}${hash}`
}

/** "/login?next=<path>"; `next` is omitted for the root page and for /login itself. */
export function loginUrl(next: string = currentPath()): string {
  if (!next || next === '/' || next.startsWith('/login')) return '/login'
  return `/login?next=${encodeURIComponent(next)}`
}

/**
 * Sanitizes a `next` redirect target: only same-origin absolute paths are
 * allowed (no "//host", no "/\\host", no scheme), and never /login itself.
 */
export function safeNextPath(raw: string | null | undefined): string {
  if (!raw) return '/'
  const next = raw.trim()
  if (!next.startsWith('/') || next.startsWith('//') || next.startsWith('/\\')) return '/'
  // Control characters (e.g. tab/newline) could smuggle a different target past the checks.
  for (let i = 0; i < next.length; i++) if (next.charCodeAt(i) < 0x20) return '/'
  if (next === '/login' || next.startsWith('/login?') || next.startsWith('/login/')) return '/'
  return next
}

/** Full-page navigation (indirection so tests can observe it; jsdom cannot navigate). */
export const browserLocation = {
  assign(url: string): void {
    window.location.assign(url)
  },
}

const defaultUnauthorizedHandler: UnauthorizedHandler = () => {
  if (window.location.pathname === '/login') return
  browserLocation.assign(loginUrl())
}

let unauthorizedHandler: UnauthorizedHandler = defaultUnauthorizedHandler

/**
 * Replaces the global 401 handler (AuthProvider installs a router-aware one).
 * Returns a function restoring the previous handler.
 */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): () => void {
  const previous = unauthorizedHandler
  unauthorizedHandler = handler ?? defaultUnauthorizedHandler
  return () => {
    unauthorizedHandler = previous
  }
}

// ---------------------------------------------------------------------------
// Core request
// ---------------------------------------------------------------------------

export type QueryValue = string | number | boolean | null | undefined
export type QueryParams = Record<string, QueryValue>

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  query?: QueryParams
  /** Serialized as JSON. Ignored when `form` is set. */
  body?: unknown
  /** Sent as multipart/form-data (browser sets the boundary header). */
  form?: FormData
  signal?: AbortSignal
  /** Do not trigger the global unauthorized handler on 401 (login form, /me probe). */
  skipAuthRedirect?: boolean
}

/** Builds a same-origin URL with query parameters; null/undefined/'' values are skipped. */
export function buildUrl(path: string, query?: QueryParams): string {
  if (!query) return path
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue
    params.append(key, String(value))
  }
  const qs = params.toString()
  if (!qs) return path
  return `${path}${path.includes('?') ? '&' : '?'}${qs}`
}

/**
 * fetch() with the shared error handling: network failures and HTTP errors
 * become ApiError, and a 401 triggers the global unauthorized handler.
 */
async function send(
  url: string,
  init: Omit<RequestInit, 'credentials'>,
  opts: { skipAuthRedirect?: boolean } = {},
): Promise<Response> {
  let res: Response
  try {
    res = await fetch(url, { ...init, credentials: 'same-origin' })
  } catch (err) {
    // Aborts are not network failures: let TanStack Query see the AbortError.
    if (err instanceof DOMException && err.name === 'AbortError') throw err
    if (init.signal?.aborted) throw err
    throw new ApiError(0, 'network', NETWORK_MESSAGE)
  }

  // Server clock for live counters (see liveClock.ts); every response carries `Date`.
  noteServerDate(res.headers.get('Date'))

  if (!res.ok) {
    const error = await errorFromResponse(res)
    if (res.status === 401 && !opts.skipAuthRedirect) unauthorizedHandler(error)
    throw error
  }
  return res
}

/** Low-level request. Resolves with the parsed JSON body (undefined for empty bodies). */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', query, body, form, signal, skipAuthRedirect } = opts
  const headers: Record<string, string> = { Accept: 'application/json' }
  let payload: BodyInit | undefined
  if (form) {
    payload = form
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }

  const res = await send(buildUrl(path, query), { method, headers, body: payload, signal }, { skipAuthRedirect })

  if (res.status === 204 || res.status === 205) return undefined as T
  const text = await res.text()
  if (!text) return undefined as T
  const contentType = res.headers.get('Content-Type') ?? ''
  if (contentType.includes('json')) {
    try {
      return JSON.parse(text) as T
    } catch {
      throw new ApiError(res.status, 'internal', 'Сервер прислал непонятный ответ. Попробуйте ещё раз.')
    }
  }
  return text as T
}

const get = <T>(path: string, query?: QueryParams, signal?: AbortSignal) =>
  request<T>(path, { query, signal })
const post = <T>(path: string, body?: unknown, extra?: Partial<RequestOptions>) =>
  request<T>(path, { ...extra, method: 'POST', body: body ?? {} })
const put = <T>(path: string, body: unknown) => request<T>(path, { method: 'PUT', body })
const patch = <T>(path: string, body: unknown) => request<T>(path, { method: 'PATCH', body })
const del = <T = void>(path: string) => request<T>(path, { method: 'DELETE' })

/** Encodes one path segment (ids are numbers, but aliases or uuids could be strings). */
const seg = (v: string | number) => encodeURIComponent(String(v))

// ---------------------------------------------------------------------------
// Downloads
// ---------------------------------------------------------------------------

/** Drops control characters and path separators; null when nothing usable is left. */
function cleanFilename(name: string): string | null {
  const cleaned = name
    .replace(/\p{Cc}/gu, '')
    .replace(/[/\\]/g, '_')
    .trim()
  return cleaned && cleaned !== '.' && cleaned !== '..' ? cleaned : null
}

/** Decodes an RFC 5987 ext-value (`UTF-8''%D0%9F...`); null when it cannot be read. */
function decodeExtValue(raw: string): string | null {
  const m = /^([^']*)'[^']*'(.*)$/.exec(raw.trim().replace(/^"(.*)"$/, '$1'))
  if (!m) return null
  const charset = m[1].toLowerCase()
  try {
    if (charset === 'utf-8') return decodeURIComponent(m[2])
    if (charset === 'iso-8859-1') {
      return m[2].replace(/%([0-9a-f]{2})/gi, (_, hex: string) => String.fromCharCode(parseInt(hex, 16)))
    }
  } catch {
    // Broken percent-encoding: fall back to the plain filename parameter.
  }
  return null
}

/**
 * File name from a Content-Disposition header: the RFC 5987 `filename*`
 * parameter first (it carries Cyrillic names), then the plain `filename`.
 */
export function filenameFromContentDisposition(header: string | null | undefined): string | null {
  if (!header) return null
  const ext = /(?:^|;)\s*filename\*\s*=\s*([^;]+)/i.exec(header)
  if (ext) {
    const decoded = decodeExtValue(ext[1])
    const name = decoded === null ? null : cleanFilename(decoded)
    if (name) return name
  }
  const plain = /(?:^|;)\s*filename\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;]*))/i.exec(header)
  if (plain) {
    const value = plain[1] !== undefined ? plain[1].replace(/\\(.)/g, '$1') : (plain[2] ?? '')
    return cleanFilename(value)
  }
  return null
}

/** How long an object URL stays valid after the click (revoking at once can cancel the download). */
const OBJECT_URL_TTL_MS = 40_000

/** Saves a Blob under `filename` through a temporary object URL. */
export function saveBlob(blob: Blob, filename: string): void {
  const href = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = href
  a.download = filename
  a.rel = 'noopener'
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(href), OBJECT_URL_TTL_MS)
}

/**
 * `tz` for export downloads: the browser's zone, so the times in the file are the ones
 * on the screen. Servers that do not know the parameter ignore it (their TIMEZONE is used).
 */
function exportZone(): string | undefined {
  return browserTimeZone() || undefined
}

/**
 * Downloads a GET endpoint (exports) and saves the file. Unlike a plain
 * `<a download>`, failures are visible: a 401 goes through the normal login
 * redirect and any other error rejects with an ApiError carrying the server's
 * Russian message (instead of the browser saving the JSON error as a file).
 * Resolves with the saved file name (from Content-Disposition, else `fallbackName`).
 */
export async function downloadFile(url: string, fallbackName = 'download'): Promise<string> {
  const res = await send(url, { method: 'GET', headers: { Accept: '*/*' } })
  let blob: Blob
  try {
    blob = await res.blob()
  } catch {
    // The connection dropped while the file was arriving.
    throw new ApiError(0, 'network', NETWORK_MESSAGE)
  }
  const filename = filenameFromContentDisposition(res.headers.get('Content-Disposition')) ?? fallbackName
  saveBlob(blob, filename)
  return filename
}

// ---------------------------------------------------------------------------
// Endpoints
// ---------------------------------------------------------------------------

const auth = {
  config: (signal?: AbortSignal) => request<AuthConfig>('/api/auth/config', { signal, skipAuthRedirect: true }),
  /** Current user, or throws ApiError(401) when not logged in (never redirects). */
  me: async (signal?: AbortSignal): Promise<User> => {
    // Tolerates both `User` and `{ user: User }` response shapes.
    const data = await request<User | LoginResponse>('/api/auth/me', { signal, skipAuthRedirect: true })
    if (data && typeof data === 'object' && 'user' in data && data.user && typeof data.user === 'object') return data.user
    if (!data || typeof data !== 'object') throw new ApiError(500, 'internal', 'Сервер прислал непонятный ответ. Попробуйте ещё раз.')
    return data as User
  },
  login: (input: LoginInput) =>
    request<LoginResponse>('/api/auth/login', { method: 'POST', body: input, skipAuthRedirect: true }),
  logout: () => request<LogoutResponse | undefined>('/api/auth/logout', { method: 'POST', body: {}, skipAuthRedirect: true }),
  changePassword: (input: ChangePasswordInput) =>
    request<void>('/api/auth/password', { method: 'POST', body: input, skipAuthRedirect: true }),
  /**
   * Browser URL that starts the OIDC (Keycloak) login. `prompt: 'login'` makes
   * the identity provider ask for credentials again, so another account can be used.
   */
  oidcLoginUrl: (next?: string, opts: { prompt?: OidcPrompt } = {}) =>
    buildUrl('/auth/oidc/login', { next: next && next !== '/' ? next : undefined, prompt: opts.prompt }),
}

const groups = {
  list: (status: GroupStatusFilter = 'active', signal?: AbortSignal) =>
    get<GroupSummary[]>('/api/groups', { status }, signal),
  get: (id: number, signal?: AbortSignal) => get<GroupDetail>(`/api/groups/${seg(id)}`, undefined, signal),
  create: (input: CreateGroupInput) => post<GroupSummary>('/api/groups', input),
  update: (id: number, input: UpdateGroupInput) => patch<GroupSummary>(`/api/groups/${seg(id)}`, input),
  addStudent: (groupId: number, input: CreateStudentInput) =>
    post<Student>(`/api/groups/${seg(groupId)}/students`, input),
  /**
   * Parses an uploaded file (multipart field `file`) or pasted text (JSON `{text}`)
   * without saving anything.
   */
  importPreview: (groupId: number, source: ImportSource, mode: ImportMode = 'add', nameFormat?: ImportNameFormat) => {
    const path = `/api/groups/${seg(groupId)}/import/preview`
    // name_format is omitted unless chosen, so the server default ('as_is') applies.
    const query = { mode, name_format: nameFormat }
    if ('text' in source) return request<ImportPreview>(path, { method: 'POST', query, body: { text: source.text } })
    const form = new FormData()
    const name = source.filename ?? (source.file instanceof File ? source.file.name : 'students.txt')
    form.append('file', source.file, name)
    return request<ImportPreview>(path, { method: 'POST', query, form })
  },
  /** On 422 the ApiError details carry the re-validated preview (`details.preview`). */
  importCommit: (groupId: number, input: ImportCommitInput) =>
    post<ImportResult>(`/api/groups/${seg(groupId)}/import/commit`, input),
  report: (groupId: number, range: DateRange = {}, signal?: AbortSignal) =>
    get<GroupReport>(`/api/groups/${seg(groupId)}/report`, { from: range.from, to: range.to }, signal),
  reportExportUrl: (groupId: number, format: ExportFormat, range: DateRange = {}) =>
    buildUrl(`/api/groups/${seg(groupId)}/report/export`, { format, from: range.from, to: range.to, tz: exportZone() }),
  /** Downloads the report file; resolves with the saved file name. */
  downloadReport: (groupId: number, format: ExportFormat, range: DateRange = {}) =>
    downloadFile(groups.reportExportUrl(groupId, format, range), `group-${groupId}-report.${format}`),
}

const students = {
  update: (id: number, input: UpdateStudentInput) => patch<Student>(`/api/students/${seg(id)}`, input),
  addAlias: (studentId: number, alias: string) =>
    post<{ id: number; alias: string }>(`/api/students/${seg(studentId)}/aliases`, { alias }),
  deleteAlias: (studentId: number, aliasId: number) =>
    del(`/api/students/${seg(studentId)}/aliases/${seg(aliasId)}`),
}

const lessonPath = (id: number) => `/api/lessons/${seg(id)}`

const lessons = {
  list: (filter: LessonFilter = {}, signal?: AbortSignal) =>
    get<LessonSummary[]>(
      '/api/lessons',
      { from: filter.from, to: filter.to, group_id: filter.group_id, status: filter.status },
      signal,
    ),
  /** Creates one lesson or a weekly series; returns all created lessons. */
  create: (input: CreateLessonInput) => post<Lesson[]>('/api/lessons', input),
  get: (id: number, signal?: AbortSignal) => get<Lesson>(lessonPath(id), undefined, signal),
  update: (id: number, input: UpdateLessonInput) => patch<Lesson>(lessonPath(id), input),
  remove: (id: number) => del(lessonPath(id)),
  // Manual actions answer with the fresh LessonView.
  finish: (id: number, input: FinishLessonInput = {}) => post<LessonView>(`${lessonPath(id)}/finish`, input),
  reopen: (id: number) => post<LessonView>(`${lessonPath(id)}/reopen`),
  recompute: (id: number) => post<LessonView>(`${lessonPath(id)}/recompute`),
  view: (id: number, signal?: AbortSignal) => get<LessonView>(`${lessonPath(id)}/attendance`, undefined, signal),
  unidentified: (id: number, signal?: AbortSignal) =>
    get<Participant[]>(`${lessonPath(id)}/unidentified`, undefined, signal),
  participants: (id: number, signal?: AbortSignal) =>
    get<Participant[]>(`${lessonPath(id)}/participants`, undefined, signal),
  mapParticipant: (id: number, participantId: number, input: MapParticipantInput) =>
    post<LessonView>(`${lessonPath(id)}/participants/${seg(participantId)}/map`, input),
  /** Removes the teacher's decision (and a name remembered with it, docs C3). */
  unmapParticipant: (id: number, participantId: number) =>
    del<LessonView>(`${lessonPath(id)}/participants/${seg(participantId)}/map`),
  updateAttendance: (id: number, studentId: number, input: UpdateAttendanceInput) =>
    patch<LessonView>(`${lessonPath(id)}/attendance/${seg(studentId)}`, input),
  studentHistory: (id: number, studentId: number, signal?: AbortSignal) =>
    get<StudentHistory>(`${lessonPath(id)}/students/${seg(studentId)}/history`, undefined, signal),
  audit: (id: number, signal?: AbortSignal) => get<AuditEntry[]>(`${lessonPath(id)}/audit`, undefined, signal),
  exportUrl: (id: number, format: ExportFormat) => buildUrl(`${lessonPath(id)}/export`, { format, tz: exportZone() }),
  /** Downloads the attendance file; resolves with the saved file name. */
  downloadExport: (id: number, format: ExportFormat) => downloadFile(lessons.exportUrl(id, format), `lesson-${id}.${format}`),
  streamUrl: (id: number) => `${lessonPath(id)}/stream`,
}

const zoom = {
  /** Extracts the Meeting ID from a link / invitation text; 422 when there is none. */
  parse: (input: string, signal?: AbortSignal) => get<ZoomParseResult>('/api/zoom/parse', { input }, signal),
  status: (signal?: AbortSignal) => get<ZoomStatus>('/api/zoom/status', undefined, signal),
  /** Newest first; `limit` defaults to 100 on the server (at most 500). */
  sessions: (filter: ZoomSessionsFilter = {}, signal?: AbortSignal) =>
    get<ZoomSession[]>('/api/zoom/sessions', { unlinked: filter.unlinked ? 1 : undefined, limit: filter.limit }, signal),
  /** lesson_id null unlinks the session (and stops auto-linking it). */
  linkSession: (sessionId: number, lessonId: number | null) =>
    post<unknown>(`/api/zoom/sessions/${seg(sessionId)}/link`, { lesson_id: lessonId }),
}

const admin = {
  settings: {
    get: (signal?: AbortSignal) => get<Settings>('/api/settings', undefined, signal),
    update: (input: Settings) => put<Settings>('/api/settings', input),
  },
  users: {
    list: (signal?: AbortSignal) => get<User[]>('/api/users', undefined, signal),
    create: (input: CreateUserInput) => post<User>('/api/users', input),
    update: (id: number, input: UpdateUserInput) => patch<User>(`/api/users/${seg(id)}`, input),
    remove: (id: number) => del(`/api/users/${seg(id)}`),
  },
  events: {
    list: (filter: EventsFilter = {}, signal?: AbortSignal) =>
      get<WebhookEventsPage>(
        '/api/zoom/events',
        {
          status: filter.status,
          type: filter.type,
          q: filter.q,
          before_id: filter.before_id,
          limit: filter.limit,
        },
        signal,
      ),
    get: (id: number, signal?: AbortSignal) => get<WebhookEventDetail>(`/api/zoom/events/${seg(id)}`, undefined, signal),
    reprocess: (id: number) => post<unknown>(`/api/zoom/events/${seg(id)}/reprocess`),
    reprocessFailed: () => post<ReprocessResult>('/api/zoom/events/reprocess-failed'),
  },
  system: {
    get: (signal?: AbortSignal) => get<SystemInfo>('/api/system', undefined, signal),
    /** Step-by-step LDAP check; normalizes a bare `DiagStep[]` body to `{ ok, steps }`. */
    testLdap: async (input: TestLdapInput = {}): Promise<TestLdapResult> => {
      const data = await post<Partial<TestLdapResult> | DiagnosticStep[] | undefined>('/api/system/test-ldap', input)
      const steps = Array.isArray(data) ? data : Array.isArray(data?.steps) ? data.steps : []
      const allPassed = steps.length > 0 && steps.every((s) => s.ok)
      const ok = !Array.isArray(data) && typeof data?.ok === 'boolean' ? data.ok : allPassed
      return { ok, steps }
    },
  },
}

/** URL of the global SSE stream (`event: lessons`). */
export const GLOBAL_STREAM_URL = '/api/stream'

/** Typed client for every endpoint of docs/architecture.md §6. */
export const api = { auth, groups, students, lessons, zoom, admin }

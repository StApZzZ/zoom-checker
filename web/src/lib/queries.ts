// TanStack Query keys, read hooks and mutation hooks. Pages use these instead
// of calling `api` directly so cache invalidation stays consistent.

import { useMemo } from 'react'
import {
  keepPreviousData,
  QueryClient,
  QueryCache,
  MutationCache,
  useInfiniteQuery,
  useMutation,
  useMutationState,
  useQuery,
  useQueryClient,
  type MutationKey,
  type QueryKey,
  type UseQueryOptions,
  type UseQueryResult,
} from '@tanstack/react-query'
import { toast } from 'sonner'

import { api, ApiError, errorMessage } from '@/lib/api'
import { markReceived } from '@/lib/liveClock'
import type {
  CreateGroupInput,
  CreateLessonInput,
  CreateStudentInput,
  CreateUserInput,
  DateRange,
  EventsFilter,
  ExportFormat,
  FinishLessonInput,
  GroupStatusFilter,
  ImportCommitInput,
  ImportMode,
  ImportNameFormat,
  ImportSource,
  LessonFilter,
  LessonView,
  MapParticipantInput,
  Settings,
  StudentRow,
  TestLdapInput,
  UpdateAttendanceInput,
  UpdateGroupInput,
  UpdateLessonInput,
  UpdateStudentInput,
  UpdateUserInput,
  User,
  ZoomSessionsFilter,
} from '@/lib/types'
import { isLessonView } from '@/lib/types'

// ---------------------------------------------------------------------------
// QueryClient
// ---------------------------------------------------------------------------

declare module '@tanstack/react-query' {
  interface Register {
    mutationMeta: {
      /** Set to false when the page shows the error itself (inline form errors). */
      errorToast?: boolean
      /** Toast shown on success. */
      successMessage?: string
    }
  }
}

/** Retry once, but never for client errors (4xx): retrying a 404/403/422 cannot help. */
export function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status >= 400 && error.status < 500) return false
  return failureCount < 1
}

/** Creates the app QueryClient with the project defaults. */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    queryCache: new QueryCache(),
    mutationCache: new MutationCache({
      onError: (error, _vars, _ctx, mutation) => {
        if (mutation.meta?.errorToast === false) return
        // 401 already redirects to the login page.
        if (error instanceof ApiError && error.isUnauthorized) return
        toast.error(errorMessage(error))
      },
      onSuccess: (_data, _vars, _ctx, mutation) => {
        if (mutation.meta?.successMessage) toast.success(mutation.meta.successMessage)
      },
    }),
    defaultOptions: {
      queries: {
        staleTime: 10_000,
        retry: shouldRetry,
        refetchOnWindowFocus: true,
      },
      mutations: {
        retry: false,
      },
    },
  })
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

/**
 * Hierarchical query keys. Invalidate a prefix to refresh everything below it,
 * e.g. `queryKeys.lessons.byId(5)` covers the lesson, its view, participants,
 * audit and student histories.
 */
export const queryKeys = {
  auth: {
    all: ['auth'] as const,
    me: () => ['auth', 'me'] as const,
    config: () => ['auth', 'config'] as const,
  },
  groups: {
    all: ['groups'] as const,
    lists: () => ['groups', 'list'] as const,
    list: (status: GroupStatusFilter) => ['groups', 'list', status] as const,
    byId: (id: number) => ['groups', 'byId', id] as const,
    detail: (id: number) => ['groups', 'byId', id, 'detail'] as const,
    report: (id: number, range: DateRange = {}) =>
      ['groups', 'byId', id, 'report', { from: range.from ?? null, to: range.to ?? null }] as const,
  },
  lessons: {
    all: ['lessons'] as const,
    lists: () => ['lessons', 'list'] as const,
    list: (filter: LessonFilter = {}) =>
      [
        'lessons',
        'list',
        {
          from: filter.from ?? null,
          to: filter.to ?? null,
          group_id: filter.group_id ?? null,
          status: filter.status ?? null,
        },
      ] as const,
    byId: (id: number) => ['lessons', 'byId', id] as const,
    detail: (id: number) => ['lessons', 'byId', id, 'detail'] as const,
    view: (id: number) => ['lessons', 'byId', id, 'view'] as const,
    participants: (id: number) => ['lessons', 'byId', id, 'participants'] as const,
    unidentified: (id: number) => ['lessons', 'byId', id, 'unidentified'] as const,
    audit: (id: number) => ['lessons', 'byId', id, 'audit'] as const,
    history: (id: number, studentId: number) => ['lessons', 'byId', id, 'history', studentId] as const,
  },
  zoom: {
    all: ['zoom'] as const,
    status: () => ['zoom', 'status'] as const,
    sessions: (unlinked = false, limit?: number) => ['zoom', 'sessions', { unlinked, limit: limit ?? null }] as const,
    parse: (input: string) => ['zoom', 'parse', input] as const,
    events: (filter: EventsFilter = {}) =>
      [
        'zoom',
        'events',
        {
          status: filter.status ?? null,
          type: filter.type ?? null,
          q: filter.q ?? null,
          before_id: filter.before_id ?? null,
          limit: filter.limit ?? null,
        },
      ] as const,
    eventsAll: () => ['zoom', 'events'] as const,
    /** Journal with "Показать ещё" paging (useEventsInfinite); before_id is the page param. */
    eventsInfinite: (filter: Omit<EventsFilter, 'before_id'> = {}) =>
      [
        'zoom',
        'events',
        'infinite',
        { status: filter.status ?? null, type: filter.type ?? null, q: filter.q ?? null, limit: filter.limit ?? null },
      ] as const,
    event: (id: number) => ['zoom', 'event', id] as const,
  },
  admin: {
    settings: () => ['admin', 'settings'] as const,
    users: () => ['admin', 'users'] as const,
    system: () => ['admin', 'system'] as const,
  },
}

/** Keys of mutations other code looks up while they run (useMutationState). */
export const mutationKeys = {
  /** useUpdateAttendance: pending inputs are overlaid on the live view. */
  attendance: (lessonId: number) => ['lessons', lessonId, 'attendance'] as const,
  /** Export downloads: the buttons show which format is being prepared. */
  lessonExport: (lessonId: number) => ['export', 'lesson', lessonId] as const,
  reportExport: (groupId: number) => ['export', 'report', groupId] as const,
}

// ---------------------------------------------------------------------------
// Read hooks
// ---------------------------------------------------------------------------

type ExtraQueryOptions<T> = Omit<UseQueryOptions<T, Error, T, QueryKey>, 'queryKey' | 'queryFn'>

/** Current user; `null` when not logged in (401 is not an error here). */
export function useMe(options: ExtraQueryOptions<User | null> = {}) {
  return useQuery({
    queryKey: queryKeys.auth.me(),
    queryFn: async ({ signal }) => {
      try {
        return await api.auth.me(signal)
      } catch (err) {
        if (err instanceof ApiError && err.isUnauthorized) return null
        throw err
      }
    },
    staleTime: 60_000,
    ...options,
  })
}

export function useAuthConfig() {
  return useQuery({
    queryKey: queryKeys.auth.config(),
    queryFn: ({ signal }) => api.auth.config(signal),
    staleTime: 5 * 60_000,
  })
}

export function useGroups(status: GroupStatusFilter = 'active') {
  return useQuery({
    queryKey: queryKeys.groups.list(status),
    queryFn: ({ signal }) => api.groups.list(status, signal),
  })
}

export function useGroup(id: number | undefined) {
  return useQuery({
    queryKey: queryKeys.groups.detail(id ?? 0),
    queryFn: ({ signal }) => api.groups.get(id!, signal),
    enabled: isValidId(id),
  })
}

export function useGroupReport(id: number | undefined, range: DateRange = {}) {
  return useQuery({
    queryKey: queryKeys.groups.report(id ?? 0, range),
    queryFn: ({ signal }) => api.groups.report(id!, range, signal),
    enabled: isValidId(id),
    placeholderData: keepPreviousData,
  })
}

export function useLessons(filter: LessonFilter = {}, options: ExtraQueryOptions<Awaited<ReturnType<typeof api.lessons.list>>> = {}) {
  return useQuery({
    queryKey: queryKeys.lessons.list(filter),
    queryFn: ({ signal }) => api.lessons.list(filter, signal),
    placeholderData: keepPreviousData,
    ...options,
  })
}

export function useLesson(id: number | undefined) {
  return useQuery({
    queryKey: queryKeys.lessons.detail(id ?? 0),
    queryFn: ({ signal }) => api.lessons.get(id!, signal),
    enabled: isValidId(id),
  })
}

/** Fetches a LessonView and records its client receive time for live counters. */
export async function fetchLessonView(id: number, signal?: AbortSignal): Promise<LessonView> {
  const view = await api.lessons.view(id, signal)
  markReceived(view.as_of)
  return view
}

/**
 * A snapshot older than the cached one by more than this is not a late
 * response but a server whose clock was set back (restart after an NTP fix,
 * a VM resumed from a snapshot). Real reordering of responses is a matter of
 * seconds, so treating such a view as current is safe.
 */
export const CLOCK_RESET_MS = 2 * 60_000

/**
 * True when `a` was computed at or after `b` (as_of comparison; both from the
 * server clock). A view more than CLOCK_RESET_MS older also counts as newer:
 * otherwise, after the server clock jumped back, every new snapshot would be
 * dropped and the screen would freeze until the clock caught up.
 */
export function isNewerView(a: Pick<LessonView, 'as_of'>, b: Pick<LessonView, 'as_of'> | undefined): boolean {
  if (!b) return true
  const ta = Date.parse(a.as_of)
  const tb = Date.parse(b.as_of)
  if (Number.isNaN(ta) || Number.isNaN(tb)) return true
  return ta >= tb || tb - ta > CLOCK_RESET_MS
}

/**
 * Stores a LessonView that a mutation answered with, unless the cache already
 * holds a newer one (e.g. an SSE snapshot computed after the change). Returns
 * true when `data` was a view of this lesson, i.e. no refetch of the view is needed.
 */
export function storeLessonView(qc: QueryClient, lessonId: number, data: unknown): boolean {
  if (!isLessonView(data) || data.lesson.id !== lessonId) return false
  markReceived(data.as_of)
  const key = queryKeys.lessons.view(lessonId)
  if (isNewerView(data, qc.getQueryData<LessonView>(key))) qc.setQueryData(key, data)
  return true
}

/** Variables of useUpdateAttendance. */
export interface AttendanceChange {
  studentId: number
  input: UpdateAttendanceInput
}

/** Attendance changes of this lesson that the server has not confirmed yet, oldest first. */
export function usePendingAttendance(lessonId: number | undefined): AttendanceChange[] {
  return useMutationState({
    filters: { mutationKey: mutationKeys.attendance(lessonId ?? 0), status: 'pending' },
    select: (m) => m.state.variables as AttendanceChange,
  })
}

/** Applies not yet confirmed attendance changes on top of a server view (optimistic UI). */
export function applyPendingAttendance(view: LessonView, pending: readonly AttendanceChange[]): LessonView {
  return pending.reduce((v, p) => (p ? applyAttendanceChange(v, p.studentId, p.input) : v), view)
}

/**
 * Live lesson data (`GET /api/lessons/{id}/attendance`). Pair with
 * useLessonStream(id) from `@/lib/sse` (or use useLiveLessonView) for realtime updates.
 *
 * The cache always holds the server's view; Был/Не был and comment changes that
 * are still being saved are overlaid on `data`. That way a live snapshot computed
 * before the change was saved cannot flip the toggle back, and a failed save
 * simply disappears instead of rolling back over newer data.
 */
export function useLessonView(
  id: number | undefined,
  options: ExtraQueryOptions<LessonView> = {},
): UseQueryResult<LessonView, Error> {
  const qc = useQueryClient()
  const query = useQuery({
    queryKey: queryKeys.lessons.view(id ?? 0),
    queryFn: async ({ signal }) => {
      const view = await fetchLessonView(id!, signal)
      // Keep a newer SSE snapshot that arrived while this request was in flight.
      const cached = qc.getQueryData<LessonView>(queryKeys.lessons.view(id!))
      return cached && !isNewerView(view, cached) ? cached : view
    },
    enabled: isValidId(id),
    ...options,
  })
  const pending = usePendingAttendance(id)
  const server = query.data
  const data = useMemo(
    () => (server && pending.length > 0 ? applyPendingAttendance(server, pending) : server),
    [server, pending],
  )
  return data === server ? query : ({ ...query, data } as UseQueryResult<LessonView, Error>)
}

export function useLessonParticipants(id: number | undefined) {
  return useQuery({
    queryKey: queryKeys.lessons.participants(id ?? 0),
    queryFn: ({ signal }) => api.lessons.participants(id!, signal),
    enabled: isValidId(id),
  })
}

export function useLessonAudit(id: number | undefined) {
  return useQuery({
    queryKey: queryKeys.lessons.audit(id ?? 0),
    queryFn: ({ signal }) => api.lessons.audit(id!, signal),
    enabled: isValidId(id),
  })
}

export function useStudentHistory(lessonId: number | undefined, studentId: number | undefined) {
  return useQuery({
    queryKey: queryKeys.lessons.history(lessonId ?? 0, studentId ?? 0),
    queryFn: ({ signal }) => api.lessons.studentHistory(lessonId!, studentId!, signal),
    enabled: isValidId(lessonId) && isValidId(studentId),
  })
}

export function useSettings() {
  return useQuery({
    queryKey: queryKeys.admin.settings(),
    queryFn: ({ signal }) => api.admin.settings.get(signal),
  })
}

export function useUsers() {
  return useQuery({
    queryKey: queryKeys.admin.users(),
    queryFn: ({ signal }) => api.admin.users.list(signal),
  })
}

export function useSystemInfo() {
  return useQuery({
    queryKey: queryKeys.admin.system(),
    queryFn: ({ signal }) => api.admin.system.get(signal),
  })
}

/**
 * Zoom webhook connection status; refreshed every 30 s while visible by default.
 * Banners elsewhere pass a slower refetchInterval; the cache entry is shared.
 */
export function useZoomStatus(options: ExtraQueryOptions<Awaited<ReturnType<typeof api.zoom.status>>> = {}) {
  return useQuery({
    queryKey: queryKeys.zoom.status(),
    queryFn: ({ signal }) => api.zoom.status(signal),
    refetchInterval: 30_000,
    ...options,
  })
}

export function useZoomSessions(
  filter: ZoomSessionsFilter = {},
  options: ExtraQueryOptions<Awaited<ReturnType<typeof api.zoom.sessions>>> = {},
) {
  const unlinked = filter.unlinked ?? false
  const limit = filter.limit
  return useQuery({
    queryKey: queryKeys.zoom.sessions(unlinked, limit),
    queryFn: ({ signal }) => api.zoom.sessions({ unlinked, limit }, signal),
    ...options,
  })
}

/** Meeting ID extraction preview while the teacher types a Zoom link. */
export function useZoomParse(input: string) {
  const trimmed = input.trim()
  return useQuery({
    queryKey: queryKeys.zoom.parse(trimmed),
    queryFn: ({ signal }) => api.zoom.parse(trimmed, signal),
    enabled: trimmed.length > 0,
    staleTime: Infinity,
    retry: false,
  })
}

/** One page of the webhook journal (admin). Use `before_id` = previous page's next_before_id. */
export function useEvents(filter: EventsFilter = {}) {
  return useQuery({
    queryKey: queryKeys.zoom.events(filter),
    queryFn: ({ signal }) => api.admin.events.list(filter, signal),
    placeholderData: keepPreviousData,
  })
}

/**
 * Webhook journal (admin) with "Показать ещё": pages are chained by
 * next_before_id; `hasNextPage` is false once the server returns null.
 */
export function useEventsInfinite(filter: Omit<EventsFilter, 'before_id'> = {}) {
  return useInfiniteQuery({
    queryKey: queryKeys.zoom.eventsInfinite(filter),
    queryFn: ({ pageParam, signal }) => api.admin.events.list({ ...filter, before_id: pageParam }, signal),
    initialPageParam: undefined as number | undefined,
    getNextPageParam: (last) => last.next_before_id ?? undefined,
    placeholderData: keepPreviousData,
  })
}

export function useEvent(id: number | undefined) {
  return useQuery({
    queryKey: queryKeys.zoom.event(id ?? 0),
    queryFn: ({ signal }) => api.admin.events.get(id!, signal),
    enabled: isValidId(id),
  })
}

function isValidId(id: number | undefined): id is number {
  return typeof id === 'number' && Number.isInteger(id) && id > 0
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

/** Options every mutation hook accepts. */
export interface MutationHookOptions {
  /** false = do not show the global error toast (page renders the error inline). */
  errorToast?: boolean
  /** Toast shown on success (omit for none). */
  successMessage?: string
}

const meta = (o: MutationHookOptions | undefined, defaultSuccess?: string) => ({
  errorToast: o?.errorToast,
  successMessage: o?.successMessage ?? defaultSuccess,
})

// --- auth ---

export function useChangePassword(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: api.auth.changePassword,
    meta: meta(o, 'Пароль изменён'),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.auth.me() }),
  })
}

// --- groups & students ---

export function useCreateGroup(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateGroupInput) => api.groups.create(input),
    meta: meta(o, 'Группа создана'),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.groups.lists() }),
  })
}

export function useUpdateGroup(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: UpdateGroupInput) => api.groups.update(groupId, input),
    meta: meta(o, 'Изменения сохранены'),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: queryKeys.groups.all }),
        // Lessons embed the group name.
        qc.invalidateQueries({ queryKey: queryKeys.lessons.all }),
      ]),
  })
}

/** Invalidates everything that shows a group's roster. */
function invalidateRoster(qc: QueryClient, groupId: number) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: queryKeys.groups.byId(groupId) }),
    qc.invalidateQueries({ queryKey: queryKeys.groups.lists() }),
    // Roster changes re-resolve participants of not finished lessons.
    qc.invalidateQueries({ queryKey: queryKeys.lessons.all }),
  ])
}

export function useAddStudent(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateStudentInput) => api.groups.addStudent(groupId, input),
    meta: meta(o, 'Студент добавлен'),
    onSuccess: () => invalidateRoster(qc, groupId),
  })
}

export function useUpdateStudent(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ studentId, input }: { studentId: number; input: UpdateStudentInput }) =>
      api.students.update(studentId, input),
    meta: meta(o, 'Изменения сохранены'),
    onSuccess: () => invalidateRoster(qc, groupId),
  })
}

export function useAddAlias(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ studentId, alias }: { studentId: number; alias: string }) => api.students.addAlias(studentId, alias),
    meta: meta(o, 'Имя добавлено'),
    onSuccess: () => invalidateRoster(qc, groupId),
  })
}

export function useDeleteAlias(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ studentId, aliasId }: { studentId: number; aliasId: number }) =>
      api.students.deleteAlias(studentId, aliasId),
    meta: meta(o, 'Имя удалено'),
    onSuccess: () => invalidateRoster(qc, groupId),
  })
}

/** Preview never changes data, so it invalidates nothing. */
export function useImportPreview(groupId: number, o?: MutationHookOptions) {
  return useMutation({
    mutationFn: ({ source, mode, nameFormat }: { source: ImportSource; mode: ImportMode; nameFormat?: ImportNameFormat }) =>
      api.groups.importPreview(groupId, source, mode, nameFormat),
    meta: meta(o),
  })
}

export function useImportCommit(groupId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: ImportCommitInput) => api.groups.importCommit(groupId, input),
    meta: meta(o, 'Список студентов сохранён'),
    onSuccess: () => invalidateRoster(qc, groupId),
  })
}

// --- lessons ---

/** Every group attendance report (lesson columns and cells). */
function invalidateReports(qc: QueryClient) {
  return qc.invalidateQueries({ queryKey: queryKeys.groups.all, predicate: (q) => q.queryKey[3] === 'report' })
}

/** Lesson lists + group summaries (last/next lesson dates) + group reports (lesson columns). */
function invalidateLessonLists(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: queryKeys.lessons.lists() }),
    qc.invalidateQueries({ queryKey: queryKeys.groups.lists() }),
    invalidateReports(qc),
  ])
}

/** One lesson (detail, view, participants, audit, histories) + lists + group reports. */
function invalidateLesson(qc: QueryClient, lessonId: number) {
  return Promise.all([qc.invalidateQueries({ queryKey: queryKeys.lessons.byId(lessonId) }), invalidateLessonLists(qc)])
}

/**
 * After a lesson action that answers with a LessonView: the view goes straight
 * into the cache (no round trip), everything else about the lesson is refreshed.
 * Without a view in the answer the view is refetched too, and the returned
 * promise waits for that, so a mutation stays pending until the screen is current.
 */
function afterLessonChange(qc: QueryClient, lessonId: number, data: unknown): Promise<unknown> | undefined {
  const stored = storeLessonView(qc, lessonId, data)
  const refresh = Promise.all([
    qc.invalidateQueries({
      queryKey: queryKeys.lessons.byId(lessonId),
      predicate: (q) => !stored || q.queryKey[3] !== 'view',
    }),
    invalidateLessonLists(qc),
  ])
  return stored ? undefined : refresh
}

export function useCreateLesson(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateLessonInput) => api.lessons.create(input),
    meta: meta(o),
    onSuccess: () =>
      Promise.all([invalidateLessonLists(qc), qc.invalidateQueries({ queryKey: queryKeys.zoom.all })]),
  })
}

export function useUpdateLesson(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: UpdateLessonInput) => api.lessons.update(lessonId, input),
    meta: meta(o, 'Изменения сохранены'),
    onSuccess: () =>
      Promise.all([invalidateLesson(qc, lessonId), qc.invalidateQueries({ queryKey: queryKeys.zoom.all })]),
  })
}

export function useDeleteLesson(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (lessonId: number) => api.lessons.remove(lessonId),
    meta: meta(o, 'Занятие удалено'),
    onSuccess: (_d, lessonId) => {
      qc.removeQueries({ queryKey: queryKeys.lessons.byId(lessonId) })
      return Promise.all([invalidateLessonLists(qc), qc.invalidateQueries({ queryKey: queryKeys.zoom.all })])
    },
  })
}

export function useFinishLesson(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: FinishLessonInput = {}) => api.lessons.finish(lessonId, input),
    meta: meta(o, 'Занятие завершено'),
    onSuccess: (data) => afterLessonChange(qc, lessonId, data),
  })
}

export function useReopenLesson(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.lessons.reopen(lessonId),
    meta: meta(o, 'Занятие снова открыто'),
    onSuccess: (data) => afterLessonChange(qc, lessonId, data),
  })
}

export function useRecomputeLesson(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.lessons.recompute(lessonId),
    meta: meta(o, 'Посещаемость пересчитана'),
    onSuccess: (data) => afterLessonChange(qc, lessonId, data),
  })
}

export function useMapParticipant(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ participantId, input }: { participantId: number; input: MapParticipantInput }) =>
      api.lessons.mapParticipant(lessonId, participantId, input),
    meta: meta(o),
    onSuccess: (data, { input }) =>
      Promise.all([
        afterLessonChange(qc, lessonId, data),
        // "Remember this name" adds an alias to the group.
        input.remember_alias ? qc.invalidateQueries({ queryKey: queryKeys.groups.all }) : undefined,
        // "Always hide this name" changes the global ignore list.
        input.ignore_always ? qc.invalidateQueries({ queryKey: queryKeys.admin.settings() }) : undefined,
      ]),
  })
}

/** Removes the teacher's decision on a participant; a name remembered with it is removed too. */
export function useUnmapParticipant(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (participantId: number) => api.lessons.unmapParticipant(lessonId, participantId),
    meta: meta(o),
    onSuccess: (data) =>
      Promise.all([
        afterLessonChange(qc, lessonId, data),
        // The remembered name (alias) of the student may have been removed.
        qc.invalidateQueries({ queryKey: queryKeys.groups.all }),
      ]),
  })
}

/**
 * Applies an attendance change to a LessonView (the optimistic overlay of useLessonView).
 * Counts are adjusted by the change of this one row instead of being recounted
 * from `students`: the server decides which rows count (e.g. archived students
 * may be listed but not counted), and a recount would make the tiles jump.
 */
export function applyAttendanceChange(view: LessonView, studentId: number, input: UpdateAttendanceInput): LessonView {
  const index = view.students.findIndex((row) => row.student_id === studentId)
  if (index < 0) return view
  const before = view.students[index]
  const after: StudentRow = { ...before }
  if (input.status !== undefined) {
    after.manual_status = input.status
    after.final_status = input.status ?? before.auto_status
  }
  if (input.comment !== undefined) after.comment = input.comment
  const students = view.students.slice()
  students[index] = after
  let counts = view.counts
  if (!before.archived && before.final_status !== after.final_status) {
    const delta = after.final_status === 'present' ? 1 : -1
    counts = {
      ...counts,
      present: Math.max(0, counts.present + delta),
      absent: Math.max(0, counts.absent - delta),
    }
  }
  return { ...view, students, counts }
}

/**
 * Sets the final status (or comment) of a student. While the request runs,
 * useLessonView overlays the change on the live view (see usePendingAttendance),
 * so the toggle flips at once and live snapshots cannot flip it back; the
 * answered LessonView then goes into the cache.
 */
export function useUpdateAttendance(lessonId: number, o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationKey: mutationKeys.attendance(lessonId),
    mutationFn: ({ studentId, input }: AttendanceChange) => api.lessons.updateAttendance(lessonId, studentId, input),
    meta: meta(o),
    onSettled: (data, error) => {
      if (error) {
        // Nothing was saved: drop the overlay right away, refresh in the background.
        void invalidateLesson(qc, lessonId)
        return
      }
      return afterLessonChange(qc, lessonId, data)
    },
  })
}

// --- exports ---

export interface ExportDownload<V> {
  /** Starts the download; errors are shown as toasts by the QueryClient. */
  download: (variables: V) => void
  /** Formats being prepared right now (from any button for the same file). */
  pending: readonly ExportFormat[]
}

function useExportDownload<V>(
  mutationKey: MutationKey,
  mutationFn: (variables: V) => Promise<string>,
  formatOf: (variables: V) => ExportFormat,
  o?: MutationHookOptions,
): ExportDownload<V> {
  const mutation = useMutation({
    mutationKey,
    mutationFn,
    // The success toast below has a description; the generic meta toast would duplicate it.
    meta: { errorToast: o?.errorToast },
    // Feedback also when started from a menu that closes at once; a big report can take a few seconds.
    onMutate: () => ({ toastId: toast.loading('Готовим файл…') }),
    onSuccess: (_name, _vars, ctx) => {
      toast.success(o?.successMessage || 'Файл скачан', {
        id: ctx?.toastId,
        description: 'Обычно он сохраняется в папку «Загрузки».',
      })
    },
    // The error itself is toasted by the QueryClient (or shown by the page).
    onError: (_err, _vars, ctx) => {
      if (ctx?.toastId !== undefined) toast.dismiss(ctx.toastId)
    },
  })
  const pending = useMutationState({
    filters: { mutationKey, status: 'pending' },
    select: (m) => formatOf(m.state.variables as V),
  })
  return { download: mutation.mutate, pending }
}

/** Excel/CSV file of one lesson (fetched, so failures and expired sessions are handled). */
export function useLessonExport(lessonId: number, o?: MutationHookOptions): ExportDownload<ExportFormat> {
  return useExportDownload(
    mutationKeys.lessonExport(lessonId),
    (format: ExportFormat) => api.lessons.downloadExport(lessonId, format),
    (format) => format,
    o,
  )
}

/** Excel/CSV file of a group report for a period. */
export function useGroupReportExport(
  groupId: number,
  o?: MutationHookOptions,
): ExportDownload<{ format: ExportFormat; range: DateRange }> {
  return useExportDownload(
    mutationKeys.reportExport(groupId),
    ({ format, range }: { format: ExportFormat; range: DateRange }) => api.groups.downloadReport(groupId, format, range),
    (v) => v.format,
    o,
  )
}

// --- zoom ---

export function useLinkSession(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ sessionId, lessonId }: { sessionId: number; lessonId: number | null }) =>
      api.zoom.linkSession(sessionId, lessonId),
    meta: meta(o, 'Привязка сохранена'),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: queryKeys.zoom.all }),
        // Both the old and the new lesson change; ids are unknown here.
        qc.invalidateQueries({ queryKey: queryKeys.lessons.all }),
        invalidateReports(qc),
      ]),
  })
}

// --- admin ---

export function useUpdateSettings(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: Settings) => api.admin.settings.update(input),
    meta: meta(o, 'Настройки сохранены'),
    onSuccess: (data) => {
      if (data) qc.setQueryData(queryKeys.admin.settings(), data)
      return Promise.all([
        qc.invalidateQueries({ queryKey: queryKeys.admin.settings() }),
        // Ignore list / percent rule change every lesson's projection and the reports built from it.
        qc.invalidateQueries({ queryKey: queryKeys.lessons.all }),
        invalidateReports(qc),
      ])
    },
  })
}

export function useCreateUser(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateUserInput) => api.admin.users.create(input),
    meta: meta(o, 'Пользователь создан'),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.admin.users() }),
  })
}

export function useUpdateUser(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ userId, input }: { userId: number; input: UpdateUserInput }) =>
      api.admin.users.update(userId, input),
    meta: meta(o, 'Изменения сохранены'),
    onSuccess: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: queryKeys.admin.users() }),
        qc.invalidateQueries({ queryKey: queryKeys.auth.me() }),
      ]),
  })
}

export function useDeleteUser(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (userId: number) => api.admin.users.remove(userId),
    meta: meta(o, 'Пользователь удалён'),
    onSuccess: () => qc.invalidateQueries({ queryKey: queryKeys.admin.users() }),
  })
}

function invalidateAfterReprocess(qc: QueryClient) {
  return Promise.all([
    qc.invalidateQueries({ queryKey: queryKeys.zoom.all }),
    qc.invalidateQueries({ queryKey: queryKeys.lessons.all }),
    invalidateReports(qc),
  ])
}

export function useReprocessEvent(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (eventId: number) => api.admin.events.reprocess(eventId),
    meta: meta(o, 'Событие отправлено на повторную обработку'),
    // Reprocessing can change any lesson's attendance.
    onSuccess: () => invalidateAfterReprocess(qc),
  })
}

export function useReprocessFailed(o?: MutationHookOptions) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: () => api.admin.events.reprocessFailed(),
    meta: meta(o),
    onSuccess: () => invalidateAfterReprocess(qc),
  })
}

export function useTestLdap(o?: MutationHookOptions) {
  return useMutation({
    mutationFn: (input: TestLdapInput) => api.admin.system.testLdap(input),
    meta: meta(o),
  })
}

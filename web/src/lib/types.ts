// API shapes. Everything in the "§6" block mirrors docs/architecture.md §6
// field-for-field (snake_case, RFC 3339 UTC strings, null for missing values).
// Shapes marked "not specified in §6" are this client's expectation of the
// server DTOs and must be kept in sync with internal/httpapi/dto.go.

/** RFC 3339 UTC timestamp as sent by the API, e.g. "2026-10-06T15:00:00Z". */
export type ISODateTime = string

// ---------------------------------------------------------------------------
// Enumerations (string unions used by the §6 interfaces)
// ---------------------------------------------------------------------------

export type Role = 'admin' | 'teacher'
export type AuthSource = 'local' | 'oidc' | 'ldap'
export type EntityStatus = 'active' | 'archived'
export type LessonStatus = 'planned' | 'active' | 'finished'
export type CurrentStatus = 'online' | 'left' | 'not_seen'
export type ParticipantStatus = 'online' | 'left'
export type AttendanceStatus = 'present' | 'absent'
export type Resolution = 'matched' | 'manual' | 'unidentified' | 'conflict' | 'ignored'
export type IgnoreReason = '' | 'host' | 'list' | 'manual'
export type LinkMode = 'auto' | 'manual'
export type ImportMode = 'add' | 'replace'
export type ImportRowStatus = 'new' | 'existing' | 'error' | 'duplicate' | 'archive'
export type ImportFormat = 'csv' | 'xlsx' | 'text'
/** How imported names are stored: as written, or "Фамилия Имя" without the patronymic. */
export type ImportNameFormat = 'as_is' | 'last_first'
export type MappingAction = 'student' | 'ignore' | 'none'
export type ExportFormat = 'csv' | 'xlsx'
export type EventProcessingStatus = 'pending' | 'processed' | 'duplicate' | 'ignored' | 'failed'
export type GroupStatusFilter = EntityStatus | 'all'

export const LESSON_STATUSES: readonly LessonStatus[] = ['planned', 'active', 'finished']
export const CURRENT_STATUSES: readonly CurrentStatus[] = ['online', 'left', 'not_seen']
export const RESOLUTIONS: readonly Resolution[] = ['matched', 'manual', 'unidentified', 'conflict', 'ignored']
export const EVENT_PROCESSING_STATUSES: readonly EventProcessingStatus[] = [
  'pending',
  'processed',
  'duplicate',
  'ignored',
  'failed',
]

// ---------------------------------------------------------------------------
// §6 shapes
// ---------------------------------------------------------------------------

export interface User {
  id: number
  username: string
  display_name: string
  email: string
  role: Role
  auth_source: AuthSource
  must_change_password: boolean
  disabled: boolean
  last_login_at: ISODateTime | null
}

export interface GroupSummary {
  id: number
  name: string
  description: string
  status: EntityStatus
  student_count: number
  default_duration_minutes: number
  created_at: ISODateTime
  last_lesson_at: ISODateTime | null
  next_lesson_at: ISODateTime | null
}

export interface StudentAlias {
  id: number
  alias: string
}

export interface Student {
  id: number
  group_id: number
  external_id: string
  full_name: string
  normalized_name: string
  status: EntityStatus
  aliases: StudentAlias[]
}

export interface GroupDetail extends GroupSummary {
  students: Student[]
}

export interface ImportRow {
  line: number
  external_id: string
  full_name: string
  normalized_name: string
  status: ImportRowStatus
  message: string
  student_id: number | null
  /** A "new" row that brings back the archived student `student_id` (not specified in §6). */
  restore?: boolean
  /** An "existing" row that changes the stored name of student `student_id`. Optional: older servers. */
  rename?: boolean
}

export interface ImportCounts {
  new: number
  existing: number
  error: number
  duplicate: number
  archive: number
  /** "new" rows that restore an archived student, also counted in `new` (not specified in §6). */
  restore?: number
  /** "existing" rows that rename a student, also counted in `existing`. Optional: older servers. */
  rename?: number
}

export interface ImportDetected {
  format: ImportFormat
  encoding: string
  delimiter: string
  name_column: string
}

export interface ImportPreview {
  mode: ImportMode
  rows: ImportRow[]
  counts: ImportCounts
  detected: ImportDetected
  /** Name format the rows were built with (not specified in §6; mirrors importer.NameFormat). */
  name_format: ImportNameFormat
  /** Most names have 3+ words: offer name_format=last_first (not specified in §6). */
  has_patronymics: boolean
}

/** Short reference to a group embedded in a lesson. */
export interface GroupRef {
  id: number
  name: string
}

export interface Lesson {
  id: number
  group: GroupRef
  title: string
  zoom_meeting_id: string
  zoom_meeting_id_formatted: string
  zoom_join_url: string
  planned_start_at: ISODateTime
  planned_end_at: ISODateTime
  actual_start_at: ISODateTime | null
  actual_end_at: ISODateTime | null
  status: LessonStatus
  manual_finished_at: ISODateTime | null
  sessions: ZoomSession[]
}

export interface Counts {
  in_group: number
  online: number
  seen: number
  not_seen: number
  unidentified: number
  present: number
  absent: number
}

export interface LessonSummary extends Omit<Lesson, 'sessions'> {
  counts: Counts
}

export interface StudentRow {
  student_id: number
  full_name: string
  external_id: string
  current_status: CurrentStatus
  total_seconds: number
  first_join_at: ISODateTime | null
  last_leave_at: ISODateTime | null
  late_seconds: number | null
  early_leave_seconds: number | null
  auto_status: AttendanceStatus
  manual_status: AttendanceStatus | null
  final_status: AttendanceStatus
  comment: string
  zoom_names: string[]
  devices_online: number
  archived: boolean
}

export interface Participant {
  id: number
  display_name: string
  normalized_name: string
  resolution: Resolution
  ignore_reason: IgnoreReason
  student_id: number | null
  student_name: string | null
  candidate_student_ids: number[]
  current_status: ParticipantStatus
  first_join_at: ISODateTime | null
  last_leave_at: ISODateTime | null
  total_seconds: number
  devices: number
  manual: boolean
}

export interface LessonView {
  lesson: Lesson
  counts: Counts
  students: StudentRow[]
  participants: Participant[]
  /** Server time the totals were computed at; live clients add elapsed time for online rows. */
  as_of: ISODateTime
}

export interface Interval {
  id: number
  join_at: ISODateTime
  leave_at: ISODateTime | null
  duration_seconds: number | null
  zoom_user_name: string
  zoom_participant_uuid: string
  meeting_uuid: string
  leave_reason: string
}

export interface AuditEntry {
  id: number
  at: ISODateTime
  user_name: string
  action: string
  entity_type: string
  entity_id: string
  previous_value: string
  new_value: string
  reason: string
}

/** Short summary of the lesson a Zoom session is linked to (ZoomSession.lesson). */
export interface ZoomSessionLesson {
  id: number
  title: string
  group_name: string
  planned_start_at: ISODateTime
}

export interface ZoomSession {
  id: number
  meeting_uuid: string
  meeting_id: string
  topic: string
  lesson_id: number | null
  /** The linked lesson; null when the session is not linked. */
  lesson: ZoomSessionLesson | null
  link_mode: LinkMode
  started_at: ISODateTime | null
  ended_at: ISODateTime | null
  first_event_at: ISODateTime
}

export interface Settings {
  ignored_names: string[]
  present_min_percent: number
  link_window_hours: number
  ignore_host: boolean
}

// ---------------------------------------------------------------------------
// Shapes spelled out inline in §6 (endpoint descriptions)
// ---------------------------------------------------------------------------

/** GET /api/auth/config */
export interface AuthConfig {
  local_enabled: boolean
  ldap_enabled: boolean
  oidc_enabled: boolean
  oidc_label: string
  version: string
  /**
   * The service's main address (PUBLIC_URL). Optional: older servers do not send it;
   * the login page then names no address in the «откройте основной адрес» hint.
   */
  public_url?: string
  /**
   * IANA name of the server time zone (TIMEZONE) that exports and server messages use.
   * Optional: without it the app cannot tell whether the browser's zone differs.
   */
  timezone?: string
}

/** POST /api/auth/login → { user } */
export interface LoginResponse {
  user: User
}

/** POST /api/auth/logout → { redirect_url } */
export interface LogoutResponse {
  redirect_url: string
}

/** GET /api/zoom/parse?input= */
export interface ZoomParseResult {
  meeting_id: string
  formatted: string
}

/** GET /api/zoom/status */
export interface ZoomStatus {
  webhook_url: string
  secret_configured: boolean
  last_event_at: ISODateTime | null
  last_event_type: string
  events_24h: number
  failed_count: number
  pending_count: number
  /**
   * Zoom's request time minus the server clock, in seconds, measured on recent signed
   * webhooks (positive: the server clock is behind); null before the first one since the
   * server started. Optional: older servers do not send it.
   */
  clock_skew_seconds?: number | null
  /** Last signed webhook refused because its time was too far from the server clock (the event was lost). */
  last_rejected_at?: ISODateTime | null
}

/** GET /api/lessons/{id}/students/{student_id}/history */
export interface StudentHistory {
  intervals: Interval[]
  audit: AuditEntry[]
}

/** GET /api/zoom/events → { items, next_before_id } */
export interface WebhookEventsPage {
  items: WebhookEventSummary[]
  next_before_id: number | null
}

// ---------------------------------------------------------------------------
// Request bodies (from §6 endpoint descriptions)
// ---------------------------------------------------------------------------

export interface LoginInput {
  username: string
  password: string
}

export interface ChangePasswordInput {
  current_password: string
  new_password: string
}

export interface CreateGroupInput {
  name: string
  description?: string
  default_duration_minutes?: number
}

export interface UpdateGroupInput {
  name?: string
  description?: string
  status?: EntityStatus
  default_duration_minutes?: number
}

export interface CreateStudentInput {
  full_name: string
  external_id?: string
}

export interface UpdateStudentInput {
  full_name?: string
  external_id?: string
  status?: EntityStatus
}

/** Source for POST /api/groups/{id}/import/preview: an uploaded file or pasted text. */
export type ImportSource = { file: File | Blob; filename?: string } | { text: string }

export interface ImportCommitRow {
  external_id: string
  full_name: string
  /**
   * Line of the row in the previewed file (not specified in §6). The server
   * echoes it in details.preview of a rejected commit; without it rows are
   * numbered by position (1, 2, ...).
   */
  line?: number
}

export interface ImportCommitInput {
  mode: ImportMode
  rows: ImportCommitRow[]
  /** Same value as the preview the rows came from (default 'as_is'). */
  name_format?: ImportNameFormat
}

export interface DateRange {
  /** Inclusive lower bound (RFC 3339 or YYYY-MM-DD, passed through as-is). */
  from?: string
  /** Upper bound (RFC 3339 or YYYY-MM-DD, passed through as-is). */
  to?: string
}

export interface LessonFilter extends DateRange {
  group_id?: number
  status?: LessonStatus
}

export interface CreateLessonInput {
  group_id: number
  title: string
  /** Zoom link, Meeting ID or the whole invitation text; the server extracts the ID. */
  zoom: string
  planned_start_at: ISODateTime
  planned_end_at: ISODateTime
  /** Create this many weekly occurrences (1..52). */
  repeat_weekly?: number
}

export interface UpdateLessonInput {
  title?: string
  zoom?: string
  planned_start_at?: ISODateTime
  planned_end_at?: ISODateTime
}

export interface FinishLessonInput {
  at?: ISODateTime
}

export interface MapParticipantInput {
  student_id?: number
  action?: MappingAction
  remember_alias?: boolean
  ignore_always?: boolean
  reason?: string
}

export interface UpdateAttendanceInput {
  /** null = back to the automatic status. Omit to leave unchanged. */
  status?: AttendanceStatus | null
  comment?: string
  reason?: string
}

export interface CreateUserInput {
  username: string
  display_name: string
  password: string
  role: Role
}

export interface UpdateUserInput {
  role?: Role
  disabled?: boolean
  display_name?: string
  password?: string
}

/** GET /api/zoom/sessions query. */
export interface ZoomSessionsFilter {
  /** Only sessions without a lesson. */
  unlinked?: boolean
  /** Newest first; the server default is 100, at most 500. */
  limit?: number
}

/** `prompt` of GET /auth/oidc/login, passed on to the identity provider. */
export type OidcPrompt = 'login' | 'select_account'

export interface EventsFilter {
  status?: EventProcessingStatus
  type?: string
  q?: string
  before_id?: number
  limit?: number
}

export interface TestLdapInput {
  username?: string
  password?: string
}

// ---------------------------------------------------------------------------
// Response shapes NOT specified in §6 — client expectations, keep in sync
// with internal/httpapi/dto.go.
// ---------------------------------------------------------------------------

/** POST /api/groups/{id}/import/commit (not specified in §6). */
export interface ImportResult {
  created: number
  existing: number
  archived: number
  restored?: number
  /** Existing students whose name or ID was changed by the import. */
  updated: number
}

/** One cell of the group report matrix (not specified in §6). */
export interface GroupReportCell {
  lesson_id: number
  /** null = no attendance row (student added later / archived before the lesson). */
  final_status: AttendanceStatus | null
  manual: boolean
  total_seconds: number
  late_seconds: number | null
}

export interface GroupReportLesson {
  id: number
  title: string
  planned_start_at: ISODateTime
  status: LessonStatus
}

export interface GroupReportStudent {
  student_id: number
  full_name: string
  external_id: string
  archived: boolean
  /** Same order as GroupReport.lessons. */
  cells: GroupReportCell[]
  present_count: number
  absent_count: number
}

/** GET /api/groups/{id}/report (not specified in §6). */
export interface GroupReport {
  group: GroupRef
  from: ISODateTime | null
  to: ISODateTime | null
  lessons: GroupReportLesson[]
  students: GroupReportStudent[]
}

/** Item of GET /api/zoom/events (not specified in §6; mirrors zoom_webhook_events). */
export interface WebhookEventSummary {
  id: number
  event_type: string
  meeting_id: string
  meeting_uuid: string
  participant_name: string
  occurred_at: ISODateTime | null
  received_at: ISODateTime
  processed_at: ISODateTime | null
  processing_status: EventProcessingStatus
  attempts: number
  error: string
}

/** GET /api/zoom/events/{id} (not specified in §6). */
export interface WebhookEventDetail extends WebhookEventSummary {
  event_ts: number
  dedupe_key: string
  next_attempt_at: ISODateTime | null
  /** Raw webhook body, byte-for-byte. */
  payload: string
}

/** POST /api/zoom/events/reprocess-failed (not specified in §6). */
export interface ReprocessResult {
  count: number
}

export interface AuthProviderStatus {
  enabled: boolean
  /** Human readable state, e.g. issuer URL or LDAP URL (no secrets). */
  detail: string
}

/** GET /api/system (not specified in §6). */
export interface SystemInfo {
  version: string
  commit: string
  db_driver: 'sqlite' | 'postgres' | string
  tls_mode: 'off' | 'auto' | 'file' | string
  public_url: string
  data_dir: string
  timezone: string
  uptime_seconds: number
  auth: {
    local: AuthProviderStatus
    ldap: AuthProviderStatus
    oidc: AuthProviderStatus
  }
}

/** One diagnostic step; mirrors Go `auth.DiagStep` (Russian name/detail, never secrets). */
export interface DiagnosticStep {
  name: string
  ok: boolean
  detail: string
}

/**
 * POST /api/system/test-ldap (not specified in §6). `ok` = every step passed.
 * The client also accepts a bare `DiagStep[]` body (see api.admin.system.testLdap).
 */
export interface TestLdapResult {
  ok: boolean
  steps: DiagnosticStep[]
}

// ---------------------------------------------------------------------------
// Error envelope
// ---------------------------------------------------------------------------

export type ApiErrorCode =
  | 'validation'
  | 'not_found'
  | 'conflict'
  | 'unauthorized'
  | 'forbidden'
  | 'internal'
  | 'rate_limited'
  | 'network'
  | (string & {})

export interface ApiErrorEnvelope {
  error: {
    code: ApiErrorCode
    message: string
    details?: Record<string, unknown> | null
  }
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

export function isAdmin(user: Pick<User, 'role'> | null | undefined): boolean {
  return user?.role === 'admin'
}

/** Best human name of a user: display name, falling back to the login. */
export function userDisplayName(user: Pick<User, 'display_name' | 'username'>): string {
  return user.display_name.trim() || user.username
}

/** True when the student's final status was set by a teacher, not computed. */
export function isManualStatus(row: Pick<StudentRow, 'manual_status'>): boolean {
  return row.manual_status !== null
}

/**
 * True for a LessonView-shaped value (SSE snapshot or a mutation response);
 * guards the cache against empty or unexpected bodies.
 */
export function isLessonView(value: unknown): value is LessonView {
  if (typeof value !== 'object' || value === null) return false
  const v = value as Partial<Record<keyof LessonView, unknown>>
  return (
    typeof v.lesson === 'object' &&
    v.lesson !== null &&
    typeof v.as_of === 'string' &&
    typeof v.counts === 'object' &&
    v.counts !== null &&
    Array.isArray(v.students) &&
    Array.isArray(v.participants)
  )
}

/** Participants that need a teacher decision (shown in "Не идентифицированы"). */
export function needsResolution(p: Pick<Participant, 'resolution'>): boolean {
  return p.resolution === 'unidentified' || p.resolution === 'conflict'
}

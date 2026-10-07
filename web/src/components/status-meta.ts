// Russian labels, tones and icons for every status the UI shows.
// Exported separately from the badge components so filters, selects and
// tables can reuse the same wording.

import {
  Archive,
  CalendarClock,
  CircleCheck,
  CircleHelp,
  CircleSlash,
  CircleX,
  Copy,
  EyeOff,
  LogOut,
  Radio,
  RefreshCw,
  Timer,
  TriangleAlert,
  UserCheck,
  UserPen,
  UserPlus,
  Wifi,
  type LucideIcon,
} from 'lucide-react'

import type { Tone } from '@/components/tone'
import type { StreamState } from '@/lib/sse'
import type {
  AttendanceStatus,
  AuthSource,
  CurrentStatus,
  EventProcessingStatus,
  IgnoreReason,
  ImportRowStatus,
  LessonStatus,
  Resolution,
  Role,
} from '@/lib/types'

export interface StatusMeta {
  label: string
  /** One sentence for tooltips / screen readers. */
  description: string
  tone: Tone
  icon: LucideIcon
  /** Shows a pulsing "live" dot instead of the icon. */
  pulse?: boolean
}

export const currentStatusMeta: Record<CurrentStatus, StatusMeta> = {
  online: { label: 'В Zoom', description: 'Сейчас на занятии в Zoom', tone: 'success', icon: Wifi, pulse: true },
  left: { label: 'Вышел', description: 'Был на занятии, сейчас вышел из Zoom', tone: 'warning', icon: LogOut },
  not_seen: { label: 'Не подключался', description: 'Ещё ни разу не подключался к занятию', tone: 'danger', icon: CircleSlash },
}

export const resolutionMeta: Record<Resolution, StatusMeta> = {
  matched: { label: 'Опознан', description: 'Имя в Zoom совпало со студентом группы', tone: 'success', icon: UserCheck },
  manual: { label: 'Сопоставлен вручную', description: 'Преподаватель указал, какой это студент', tone: 'info', icon: UserPen },
  unidentified: {
    label: 'Не опознан',
    description: 'Имя в Zoom не совпало ни с одним студентом группы',
    tone: 'warning',
    icon: CircleHelp,
  },
  conflict: {
    label: 'Конфликт имён',
    description: 'Имя в Zoom подходит сразу нескольким студентам',
    tone: 'danger',
    icon: TriangleAlert,
  },
  ignored: { label: 'Скрыт', description: 'Участник не учитывается в посещаемости', tone: 'muted', icon: EyeOff },
}

export const lessonStatusMeta: Record<LessonStatus, StatusMeta> = {
  planned: { label: 'Запланировано', description: 'Занятие ещё не началось', tone: 'info', icon: CalendarClock },
  active: { label: 'Идёт', description: 'Занятие идёт прямо сейчас', tone: 'success', icon: Radio, pulse: true },
  finished: { label: 'Завершено', description: 'Занятие завершено', tone: 'muted', icon: CircleCheck },
}

export const finalStatusMeta: Record<AttendanceStatus, StatusMeta> = {
  present: { label: 'Был', description: 'Присутствовал на занятии', tone: 'success', icon: CircleCheck },
  absent: { label: 'Не был', description: 'Отсутствовал на занятии', tone: 'danger', icon: CircleX },
}

export const ignoreReasonLabels: Record<Exclude<IgnoreReason, ''>, string> = {
  host: 'Организатор конференции',
  list: 'Из списка скрытых имён',
  manual: 'Скрыт преподавателем',
}

export const eventStatusMeta: Record<EventProcessingStatus, StatusMeta> = {
  pending: { label: 'В очереди', description: 'Событие ждёт обработки', tone: 'info', icon: CalendarClock },
  processed: { label: 'Обработано', description: 'Событие учтено', tone: 'success', icon: CircleCheck },
  duplicate: { label: 'Повтор', description: 'Zoom прислал это событие повторно; учтено один раз', tone: 'muted', icon: EyeOff },
  ignored: { label: 'Пропущено', description: 'Событие не влияет на посещаемость', tone: 'muted', icon: EyeOff },
  failed: { label: 'Ошибка', description: 'Событие не удалось обработать', tone: 'danger', icon: TriangleAlert },
}

export const roleLabels: Record<Role, string> = {
  admin: 'Администратор',
  teacher: 'Преподаватель',
}

/** Live update connection of a page (StreamState from @/lib/sse). */
export const streamStateMeta: Record<StreamState, StatusMeta> = {
  live: {
    label: 'Обновляется автоматически',
    description: 'Изменения из Zoom появляются на экране сразу',
    tone: 'success',
    icon: Radio,
    pulse: true,
  },
  // Also the state of the very first connection, so the wording is neutral.
  reconnecting: {
    label: 'Подключаемся…',
    description: 'Подключаемся к серверу для мгновенных обновлений. Пока данные обновляются каждые 5 секунд',
    tone: 'info',
    icon: RefreshCw,
  },
  polling: {
    label: 'Обновление раз в 5 секунд',
    description: 'Мгновенные обновления сейчас недоступны, данные обновляются каждые 5 секунд',
    tone: 'warning',
    icon: Timer,
  },
}

/** Rows of the student import preview (ImportRow.status). */
export const importRowStatusMeta: Record<ImportRowStatus, StatusMeta> = {
  new: { label: 'Новый', description: 'Студент будет добавлен в группу', tone: 'success', icon: UserPlus },
  existing: { label: 'Уже в группе', description: 'Такой студент уже есть, изменений нет', tone: 'muted', icon: UserCheck },
  error: { label: 'Ошибка', description: 'Строку не удалось разобрать, она будет пропущена', tone: 'danger', icon: TriangleAlert },
  duplicate: {
    label: 'Повтор',
    // The server marks every occurrence of a repeated name, so "выше в списке" would be wrong for the first one.
    description: 'Это имя встречается в списке несколько раз — сервис не сможет отличить этих студентов',
    tone: 'warning',
    icon: Copy,
  },
  archive: {
    label: 'В архив',
    description: 'Студента нет в новом списке, он будет перенесён в архив',
    tone: 'warning',
    icon: Archive,
  },
}

/** Russian wording for AuditEntry.action (constants of internal/model). */
export const auditActionLabels: Readonly<Record<string, string>> = {
  'attendance.status': 'Изменена отметка о посещении',
  'attendance.comment': 'Изменён комментарий',
  'participant.map': 'Участник Zoom сопоставлен',
  'participant.unmap': 'Сопоставление отменено',
  'lesson.create': 'Занятие создано',
  'lesson.update': 'Занятие изменено',
  'lesson.delete': 'Занятие удалено',
  'lesson.finish': 'Занятие завершено',
  'lesson.reopen': 'Занятие возобновлено',
  'session.link': 'Изменена привязка конференции Zoom',
  'group.create': 'Группа создана',
  'group.update': 'Группа изменена',
  'group.import': 'Загружен список студентов',
  'student.create': 'Студент добавлен',
  'student.update': 'Данные студента изменены',
  'alias.add': 'Добавлено имя в Zoom',
  'alias.delete': 'Удалено имя в Zoom',
  'settings.update': 'Изменены настройки',
  'user.create': 'Пользователь создан',
  'user.update': 'Пользователь изменён',
  'user.delete': 'Пользователь удалён',
  'event.reprocess': 'Событие Zoom обработано повторно',
}

/** Label of an audit action; unknown actions (newer server) are shown as is. */
export function auditActionLabel(action: string): string {
  return Object.hasOwn(auditActionLabels, action) ? auditActionLabels[action] : action
}

/** Russian wording for Zoom webhook event types (zoom_webhook_events.event_type). */
export const zoomEventTypeLabels: Readonly<Record<string, string>> = {
  'meeting.started': 'Начало конференции',
  'meeting.ended': 'Конец конференции',
  'meeting.participant_joined': 'Вход участника',
  'meeting.participant_left': 'Выход участника',
  'meeting.participant_joined_breakout_room': 'Вход в сессионный зал',
  'meeting.participant_left_breakout_room': 'Выход из сессионного зала',
  'endpoint.url_validation': 'Проверка адреса (Validate)',
}

/** Label of a Zoom event type; unknown types are shown as is. */
export function zoomEventTypeLabel(type: string): string {
  return Object.hasOwn(zoomEventTypeLabels, type) ? zoomEventTypeLabels[type] : type
}

/** Where a user account comes from (User.auth_source). */
export const authSourceLabels: Record<AuthSource, string> = {
  local: 'Локальный',
  ldap: 'LDAP',
  oidc: 'Keycloak',
}

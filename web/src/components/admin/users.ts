// Validation and labels for the admin "Пользователи" page.

import { CircleCheck, CircleSlash, KeyRound, type LucideIcon } from 'lucide-react'

import { newPasswordProblem } from '@/components/auth/password'
import type { Tone } from '@/components/tone'
import type { Role, User } from '@/lib/types'

export interface NewUserDraft {
  username: string
  display_name: string
  password: string
  role: Role
}

export type NewUserErrors = Partial<Record<keyof NewUserDraft, string>>

export const MIN_USERNAME_LENGTH = 2
export const MAX_USERNAME_LENGTH = 64
export const MAX_DISPLAY_NAME_LENGTH = 200

/** Characters the server accepts in a login (internal/httpapi usernamePattern; it lower-cases first). */
const USERNAME_CHARS = /^[a-z0-9._@-]+$/i

/** Russian problems of the "Новый пользователь" form; empty object when it can be sent. */
export function validateNewUser(d: NewUserDraft): NewUserErrors {
  const errors: NewUserErrors = {}
  const username = d.username.trim()
  if (!username) errors.username = 'Введите логин — с ним пользователь будет входить в систему.'
  else if (/\s/.test(username)) errors.username = 'Логин не должен содержать пробелов.'
  else if (!USERNAME_CHARS.test(username))
    errors.username = 'В логине можно использовать только латинские буквы, цифры и знаки . _ @ -'
  else if (username.length < MIN_USERNAME_LENGTH)
    errors.username = `Логин слишком короткий: хотя бы ${MIN_USERNAME_LENGTH} символа.`
  else if (username.length > MAX_USERNAME_LENGTH)
    errors.username = `Логин слишком длинный: не больше ${MAX_USERNAME_LENGTH} символов.`

  const name = d.display_name.trim()
  if (!name) errors.display_name = 'Введите имя — его видно в истории изменений.'
  else if (Array.from(name).length > MAX_DISPLAY_NAME_LENGTH)
    errors.display_name = `Имя слишком длинное: не больше ${MAX_DISPLAY_NAME_LENGTH} символов.`

  const pw = newPasswordProblem(d.password)
  if (pw) errors.password = pw.replace('Новый пароль', 'Пароль')
  return errors
}

const FORM_FIELDS: readonly (keyof NewUserDraft)[] = ['username', 'display_name', 'password', 'role']

/**
 * Form field a server error belongs to: `details.field` of the error envelope,
 * or the login for a 409 (taken login). null = show the error above the buttons.
 */
export function serverErrorField(err: unknown): keyof NewUserDraft | null {
  if (typeof err !== 'object' || err === null) return null
  const { details, status } = err as { details?: Record<string, unknown>; status?: number }
  const field = details?.field
  if (typeof field === 'string' && (FORM_FIELDS as readonly string[]).includes(field)) return field as keyof NewUserDraft
  return status === 409 ? 'username' : null
}

export interface UserStateMeta {
  label: string
  description: string
  tone: Tone
  icon: LucideIcon
}

/** "Активен" / "Отключён" / "Ждёт смены пароля" for the status column. */
export function userStateMeta(user: Pick<User, 'disabled' | 'must_change_password' | 'auth_source'>): UserStateMeta {
  if (user.disabled)
    return { label: 'Отключён', description: 'Не может войти в систему', tone: 'danger', icon: CircleSlash }
  if (user.must_change_password && user.auth_source === 'local')
    return {
      label: 'Ждёт смены пароля',
      description: 'При следующем входе пользователь придумает новый пароль',
      tone: 'warning',
      icon: KeyRound,
    }
  return { label: 'Активен', description: 'Может входить в систему', tone: 'success', icon: CircleCheck }
}

/** Search across login, name and email (case-insensitive, ё = е). */
export function matchesUser(user: Pick<User, 'username' | 'display_name' | 'email'>, query: string): boolean {
  const norm = (s: string) => s.toLocaleLowerCase('ru').replaceAll('ё', 'е')
  const q = norm(query.trim())
  if (!q) return true
  return [user.username, user.display_name, user.email].some((v) => norm(v).includes(q))
}

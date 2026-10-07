// Mirrors the server policy (internal/auth.ValidateNewPassword) so teachers see
// the problem before submitting; the server re-validates anyway.

/** Minimum length of a new local password, in characters (Unicode code points). */
export const MIN_PASSWORD_LENGTH = 8
/** Maximum length of a new local password, in characters (Unicode code points). */
export const MAX_PASSWORD_LENGTH = 256

/** Russian problem with a new password, or null when it satisfies the policy. */
export function newPasswordProblem(next: string): string | null {
  // Code points, like Go's utf8.RuneCountInString: an emoji counts as one character.
  const chars = Array.from(next)
  if (chars.length < MIN_PASSWORD_LENGTH) return `Новый пароль должен быть не короче ${MIN_PASSWORD_LENGTH} символов.`
  if (chars.length > MAX_PASSWORD_LENGTH) return `Пароль слишком длинный: не больше ${MAX_PASSWORD_LENGTH} символов.`
  if (chars.every((c) => c === chars[0])) return 'Пароль не должен состоять из одного повторяющегося символа.'
  return null
}

/** Returns a Russian validation message for the change-password form, or null when valid. */
export function validatePasswordForm(current: string, next: string, repeat: string): string | null {
  if (!current) return 'Введите текущий пароль.'
  const problem = newPasswordProblem(next)
  if (problem) return problem
  if (next !== repeat) return 'Пароли не совпадают. Введите новый пароль ещё раз.'
  if (next === current) return 'Новый пароль должен отличаться от текущего.'
  return null
}

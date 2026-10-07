/** Messages for /login?error=<code> set by the OIDC callback and the server. */
export const LOGIN_ERROR_MESSAGES: Readonly<Record<string, string>> = {
  access_denied: 'У вас нет доступа к системе. Обратитесь к администратору.',
  oidc_failed: 'Не удалось войти через Keycloak. Попробуйте ещё раз.',
  // The Keycloak login was started on another address than the service's main one
  // (PUBLIC_URL): its return address and cookies belong to the main address only.
  oidc_host: 'Вход через Keycloak работает только по основному адресу сервиса. Откройте сервис по его основному адресу и войдите снова.',
  session_expired: 'Сессия истекла. Войдите снова.',
  disabled: 'Ваша учётная запись отключена. Обратитесь к администратору.',
}

/**
 * The service's main address for the oidc_host hint, or null when it is missing or not
 * a plain http(s) address (the value is shown as a link, so nothing else may get through).
 */
export function publicAddress(raw: string | null | undefined): URL | null {
  if (!raw) return null
  try {
    const url = new URL(raw.trim())
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return null
    if (url.username || url.password) return null
    return url
  } catch {
    return null
  }
}

/**
 * Login page of the main address, keeping where the user was going (`next`, already
 * sanitized). The address's own path is kept: the service may live under a prefix.
 */
export function publicLoginUrl(base: URL, next: string): string {
  const url = new URL(base.href)
  url.pathname = `${url.pathname.replace(/\/+$/, '')}/login`
  url.search = next && next !== '/' ? `?next=${encodeURIComponent(next)}` : ''
  url.hash = ''
  return url.href
}

/** Human message for a login error code; unknown codes get a generic text. */
export function loginErrorMessage(code: string): string {
  // Own keys only: "?error=__proto__" or "?error=toString" must not reach
  // Object.prototype (an object/function rendered as a React child crashes the page).
  return Object.hasOwn(LOGIN_ERROR_MESSAGES, code) ? LOGIN_ERROR_MESSAGES[code] : 'Не удалось войти. Попробуйте ещё раз.'
}

/** At most this many required roles are shown (the link could be crafted). */
const MAX_REQUIRED = 10
const MAX_REQUIRED_LENGTH = 64
/** Role and group names as identity providers write them: letters, digits, "_-.:/@" and spaces. */
const ROLE_NAME = /^[\p{L}\p{N}_\-.:/@ ]+$/u

/**
 * Roles or groups from `?required=` (comma separated) that grant access.
 * The parameter comes from a link anybody can craft, so only short plain names
 * are kept: it must not be usable to show arbitrary text on the login page.
 */
export function parseRequiredRoles(raw: string | null | undefined): string[] {
  if (!raw) return []
  const out: string[] = []
  for (const part of raw.split(',')) {
    const name = part.trim().replace(/\s+/g, ' ')
    if (!name || name.length > MAX_REQUIRED_LENGTH || !ROLE_NAME.test(name) || out.includes(name)) continue
    out.push(name)
    if (out.length === MAX_REQUIRED) break
  }
  return out
}

/** "Попросите администратора выдать вам одну из ролей: «a», «b»." ('' without roles). */
export function requiredRolesHint(roles: readonly string[]): string {
  if (roles.length === 0) return ''
  const list = roles.map((r) => `«${r}»`).join(', ')
  return roles.length === 1
    ? `Попросите администратора выдать вам роль ${list}.`
    : `Попросите администратора выдать вам одну из ролей: ${list}.`
}

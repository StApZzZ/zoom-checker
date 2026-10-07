// Plain Russian wording for the read-only "Система" card (GET /api/system).

/** "3 дн 4 ч", "4 ч 12 мин", "12 мин", "меньше минуты". */
export function formatUptime(seconds: number | null | undefined): string {
  const total = typeof seconds === 'number' && Number.isFinite(seconds) ? Math.max(0, Math.floor(seconds)) : 0
  const days = Math.floor(total / 86_400)
  const hours = Math.floor((total % 86_400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days > 0) return hours > 0 ? `${days} дн ${hours} ч` : `${days} дн`
  if (hours > 0) return minutes > 0 ? `${hours} ч ${minutes} мин` : `${hours} ч`
  if (minutes > 0) return `${minutes} мин`
  return 'меньше минуты'
}

export function dbDriverLabel(driver: string): string {
  switch (driver) {
    case 'sqlite':
      return 'SQLite (файл на сервере)'
    case 'postgres':
    case 'postgresql':
    case 'pgx':
      return 'PostgreSQL'
    default:
      return driver || '—'
  }
}

export function tlsModeLabel(mode: string): string {
  switch (mode) {
    case 'off':
      return 'Выключен в сервисе — HTTP (обычно HTTPS включён на прокси перед ним)'
    case 'auto':
      return 'Автоматический сертификат Let’s Encrypt'
    case 'file':
      return 'Сертификат из файла'
    default:
      return mode || '—'
  }
}

/** First 7 characters of a git commit, or '' for unknown builds. */
export function shortCommit(commit: string | null | undefined): string {
  const c = (commit ?? '').trim()
  if (!c || c === 'unknown' || c === 'none') return ''
  return c.slice(0, 7)
}

// Semantic color tones shared by badges, stat tiles and indicators.
// Color is always paired with an icon and text (never the only signal).

export type Tone = 'success' | 'warning' | 'danger' | 'info' | 'neutral' | 'muted'

/** Soft background + strong text + subtle ring, readable in light and dark themes. */
export const toneBadgeClass: Record<Tone, string> = {
  success:
    'bg-emerald-50 text-emerald-800 ring-emerald-600/25 dark:bg-emerald-400/10 dark:text-emerald-300 dark:ring-emerald-400/25',
  warning:
    'bg-amber-50 text-amber-900 ring-amber-600/30 dark:bg-amber-400/10 dark:text-amber-200 dark:ring-amber-400/25',
  danger: 'bg-red-50 text-red-800 ring-red-600/20 dark:bg-red-400/10 dark:text-red-300 dark:ring-red-400/25',
  info: 'bg-sky-50 text-sky-800 ring-sky-600/20 dark:bg-sky-400/10 dark:text-sky-300 dark:ring-sky-400/25',
  neutral: 'bg-muted text-foreground ring-foreground/10',
  muted: 'bg-transparent text-muted-foreground ring-border',
}

/** Text color only (numbers in stat tiles, inline icons). */
export const toneTextClass: Record<Tone, string> = {
  success: 'text-emerald-700 dark:text-emerald-400',
  warning: 'text-amber-700 dark:text-amber-300',
  danger: 'text-red-700 dark:text-red-400',
  info: 'text-sky-700 dark:text-sky-400',
  neutral: 'text-foreground',
  muted: 'text-muted-foreground',
}

/** Solid fill for small dots / accent bars. */
export const toneSolidClass: Record<Tone, string> = {
  success: 'bg-emerald-500',
  warning: 'bg-amber-500',
  danger: 'bg-red-500',
  info: 'bg-sky-500',
  neutral: 'bg-foreground/60',
  muted: 'bg-muted-foreground/50',
}

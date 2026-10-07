import { cn } from '@/lib/utils'

/** Product mark: a check in a rounded square (same as the favicon). */
export function LogoMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 32 32" aria-hidden="true" className={cn('size-8 shrink-0', className)}>
      <rect width="32" height="32" rx="8" className="fill-sky-600 dark:fill-sky-500" />
      <path
        d="M9 16.5l4.5 4.5L23 11.5"
        fill="none"
        stroke="white"
        strokeWidth="3.2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  )
}

/** Mark + "Zoom Checker" + "Посещаемость". */
export function Brand({ className, compact = false }: { className?: string; compact?: boolean }) {
  return (
    <span className={cn('flex items-center gap-2.5', className)}>
      <LogoMark />
      <span className="flex flex-col leading-tight">
        <span className="font-heading text-[0.95rem] font-semibold tracking-tight">Zoom Checker</span>
        {!compact && <span className="text-xs text-muted-foreground">Посещаемость</span>}
      </span>
    </span>
  )
}

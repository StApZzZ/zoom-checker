import { RefreshCw, SearchX, ServerCrash, ShieldAlert, WifiOff, type LucideIcon } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { ApiError, errorMessage } from '@/lib/api'
import { cn } from '@/lib/utils'

export interface ErrorStateProps {
  error: unknown
  /** Shows a "Повторить" button. */
  onRetry?: () => void
  /** Spins the retry icon while a refetch is in flight. */
  retrying?: boolean
  /** Overrides the default title derived from the error. */
  title?: string
  /** compact = inline block for cards / table bodies. */
  size?: 'default' | 'compact'
  className?: string
}

function describe(error: unknown): { title: string; icon: LucideIcon; text?: string } {
  if (error instanceof ApiError) {
    // The network message itself starts with "Нет связи с сервером"; avoid repeating it.
    if (error.isNetwork)
      return { title: 'Нет связи с сервером', icon: WifiOff, text: 'Проверьте подключение к сети и попробуйте ещё раз.' }
    if (error.isForbidden) return { title: 'Нет доступа', icon: ShieldAlert }
    if (error.isNotFound) return { title: 'Не найдено', icon: SearchX }
  }
  return { title: 'Не удалось загрузить данные', icon: ServerCrash }
}

/** Error block with the server's Russian message and a retry button. */
export function ErrorState({ error, onRetry, retrying = false, title, size = 'default', className }: ErrorStateProps) {
  const d = describe(error)
  const Icon = d.icon
  const compact = size === 'compact'
  return (
    <div
      role="alert"
      data-slot="error-state"
      className={cn(
        'flex flex-col items-center justify-center gap-3 text-center',
        compact ? 'px-4 py-6' : 'rounded-xl border border-destructive/30 bg-destructive/5 px-6 py-10',
        className,
      )}
    >
      <div className="flex size-11 items-center justify-center rounded-full bg-destructive/10 text-destructive">
        <Icon className="size-5" aria-hidden="true" />
      </div>
      <div className="flex max-w-md flex-col gap-1">
        <p className="font-medium text-foreground">{title ?? d.title}</p>
        <p className="text-sm text-pretty text-muted-foreground">{d.text ?? errorMessage(error)}</p>
      </div>
      {onRetry && (
        <Button type="button" variant="outline" onClick={onRetry} disabled={retrying}>
          <RefreshCw aria-hidden="true" className={cn(retrying && 'animate-spin')} />
          Повторить
        </Button>
      )}
    </div>
  )
}

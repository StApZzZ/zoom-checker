import type { ReactNode } from 'react'
import { CircleAlert } from 'lucide-react'

import { Label } from '@/components/ui/label'
import { cn } from '@/lib/utils'

export interface FormFieldProps {
  /** id of the control; the hint gets `${id}-hint`, the error `${id}-error`. */
  id: string
  label: ReactNode
  hint?: ReactNode
  error?: string | null
  className?: string
  children: ReactNode
}

/** Label + control + hint + inline error, stacked. */
export function FormField({ id, label, hint, error, className, children }: FormFieldProps) {
  return (
    <div className={cn('flex flex-col gap-2', className)}>
      <Label htmlFor={id} className="leading-snug">
        {label}
      </Label>
      {children}
      {hint && (
        <p id={`${id}-hint`} className="text-sm text-pretty text-muted-foreground">
          {hint}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} className="flex items-start gap-1.5 text-sm text-destructive">
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span>{error}</span>
        </p>
      )}
    </div>
  )
}

import { CircleCheck, CircleX } from 'lucide-react'

import type { DiagnosticStep } from '@/lib/types'
import { cn } from '@/lib/utils'

/** Numbered list of check steps with ✔ / ✘, the step name and the server's Russian explanation. */
export function DiagnosticSteps({ steps, className }: { steps: DiagnosticStep[]; className?: string }) {
  return (
    <ol className={cn('flex flex-col gap-2', className)} aria-label="Шаги проверки">
      {steps.map((step, i) => {
        const Icon = step.ok ? CircleCheck : CircleX
        return (
          <li
            key={`${i}-${step.name}`}
            className={cn(
              'flex gap-3 rounded-lg p-3 ring-1',
              step.ok
                ? 'bg-emerald-50/60 ring-emerald-600/20 dark:bg-emerald-400/5 dark:ring-emerald-400/20'
                : 'bg-red-50/60 ring-red-600/20 dark:bg-red-400/5 dark:ring-red-400/20',
            )}
          >
            <Icon
              className={cn(
                'mt-0.5 size-5 shrink-0',
                step.ok ? 'text-emerald-700 dark:text-emerald-400' : 'text-red-700 dark:text-red-400',
              )}
              aria-hidden="true"
            />
            <div className="flex min-w-0 flex-col gap-0.5">
              <p className="font-medium">
                <span className="sr-only">{step.ok ? 'Успешно: ' : 'Ошибка: '}</span>
                {step.name}
              </p>
              {step.detail && <p className="text-sm break-words text-muted-foreground">{step.detail}</p>}
            </div>
          </li>
        )
      })}
    </ol>
  )
}

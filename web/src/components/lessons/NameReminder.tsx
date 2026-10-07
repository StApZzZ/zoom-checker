import { useState } from 'react'
import { ChevronDown, MessageSquareText } from 'lucide-react'

import { CopyButton } from '@/components/CopyButton'
import { NAME_REMINDER_TEXT } from '@/components/lessons/nameReminder'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

export interface NameReminderProps {
  defaultOpen?: boolean
  className?: string
}

/** Collapsible "how to name yourself in Zoom" note for students, with a copy button. */
export function NameReminder({ defaultOpen = false, className }: NameReminderProps) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <Collapsible open={open} onOpenChange={setOpen} className={cn('rounded-lg border bg-background', className)}>
      <CollapsibleTrigger
        className={cn(
          'flex w-full items-center gap-2 rounded-lg px-3 py-2.5 text-left text-sm font-medium outline-none',
          'hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50',
        )}
      >
        <MessageSquareText className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="flex-1">Памятка для студентов: как подписаться в Zoom</span>
        <ChevronDown
          className={cn('size-4 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')}
          aria-hidden="true"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-3 px-3 pb-3">
        <p className="text-sm text-muted-foreground">
          Отправьте этот текст студентам в чат группы до занятия — тогда система узнает их автоматически.
        </p>
        <blockquote className="rounded-md border-l-4 border-sky-500/60 bg-muted/50 px-3 py-2 text-sm text-pretty">
          {NAME_REMINDER_TEXT}
        </blockquote>
        <CopyButton
          value={NAME_REMINDER_TEXT}
          label="Скопировать текст"
          successMessage="Текст памятки скопирован"
          className="w-fit"
        />
      </CollapsibleContent>
    </Collapsible>
  )
}

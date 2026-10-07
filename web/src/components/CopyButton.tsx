import { useEffect, useRef, useState, type ComponentProps } from 'react'
import { Check, Copy } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { copyToClipboard } from '@/lib/clipboard'
import { cn } from '@/lib/utils'

export interface CopyButtonProps extends Omit<ComponentProps<typeof Button>, 'onClick' | 'value' | 'children'> {
  /** Text put on the clipboard. */
  value: string
  /** Visible text; omit for an icon-only button. */
  label?: string
  /** Accessible name for the icon-only variant. */
  srLabel?: string
  successMessage?: string
}

/** Copies `value` and confirms with a "Скопировано" toast and a check mark. */
export function CopyButton({
  value,
  label,
  srLabel = 'Скопировать',
  successMessage = 'Скопировано',
  variant = 'outline',
  size,
  className,
  ...rest
}: CopyButtonProps) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current)
    },
    [],
  )

  const onClick = async () => {
    const ok = await copyToClipboard(value)
    if (!ok) {
      toast.error('Не удалось скопировать. Выделите текст и нажмите Ctrl+C.')
      return
    }
    toast.success(successMessage)
    setCopied(true)
    if (timer.current) clearTimeout(timer.current)
    timer.current = setTimeout(() => setCopied(false), 2000)
  }

  const Icon = copied ? Check : Copy
  return (
    <Button
      type="button"
      variant={variant}
      size={size ?? (label ? 'default' : 'icon')}
      className={cn(className)}
      onClick={onClick}
      aria-label={label ? undefined : srLabel}
      title={label ? undefined : srLabel}
      {...rest}
    >
      <Icon aria-hidden="true" className={cn(copied && 'text-emerald-600 dark:text-emerald-400')} />
      {label}
    </Button>
  )
}

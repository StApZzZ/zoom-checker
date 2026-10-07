import { useState, type MouseEvent, type ReactNode } from 'react'
import { Loader2 } from 'lucide-react'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

export interface ConfirmDialogProps {
  /** Element that opens the dialog (rendered via asChild). Omit when controlled. */
  trigger?: ReactNode
  open?: boolean
  onOpenChange?: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  /** Extra content between the description and the buttons (e.g. a reason field). */
  children?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  /** Red confirm button for irreversible actions. */
  destructive?: boolean
  /**
   * Called on confirm. When it returns a promise the dialog shows a spinner and
   * stays open until it settles; it closes on success and stays open on error
   * (the error toast comes from the mutation layer).
   */
  onConfirm: () => void | Promise<unknown>
  confirmDisabled?: boolean
}

/** "Are you sure?" dialog with async-aware confirm button. */
export function ConfirmDialog({
  trigger,
  open: openProp,
  onOpenChange,
  title,
  description,
  children,
  confirmLabel = 'Подтвердить',
  cancelLabel = 'Отмена',
  destructive = false,
  onConfirm,
  confirmDisabled = false,
}: ConfirmDialogProps) {
  const [openState, setOpenState] = useState(false)
  const [pending, setPending] = useState(false)
  const controlled = openProp !== undefined
  const open = controlled ? openProp : openState

  const setOpen = (next: boolean) => {
    if (pending && !next) return
    if (!controlled) setOpenState(next)
    onOpenChange?.(next)
  }

  const handleConfirm = async (e: MouseEvent) => {
    // Keep the dialog open until the action finishes.
    e.preventDefault()
    try {
      const result = onConfirm()
      if (result && typeof (result as Promise<unknown>).then === 'function') {
        setPending(true)
        await result
      }
      setPending(false)
      if (!controlled) setOpenState(false)
      onOpenChange?.(false)
    } catch {
      setPending(false)
    }
  }

  return (
    <AlertDialog open={open} onOpenChange={setOpen}>
      {trigger && <AlertDialogTrigger asChild>{trigger}</AlertDialogTrigger>}
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description && <AlertDialogDescription>{description}</AlertDialogDescription>}
        </AlertDialogHeader>
        {children}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={pending}>{cancelLabel}</AlertDialogCancel>
          <AlertDialogAction
            variant={destructive ? 'destructive' : 'default'}
            disabled={pending || confirmDisabled}
            onClick={handleConfirm}
          >
            {pending && <Loader2 className="animate-spin" aria-hidden="true" />}
            {confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

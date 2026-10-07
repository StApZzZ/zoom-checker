import { useId, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Loader2 } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { MIN_PASSWORD_LENGTH, validatePasswordForm } from '@/components/auth/password'
import { errorMessage } from '@/lib/api'
import { queryKeys, useChangePassword } from '@/lib/queries'
import type { User } from '@/lib/types'

export interface ChangePasswordDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** must_change_password: the dialog cannot be dismissed, only completed or logged out of. */
  forced?: boolean
  onLogout?: () => void
}

/** "Сменить пароль" form for local accounts; forced mode for must_change_password. */
export function ChangePasswordDialog({ open, onOpenChange, forced = false, onLogout }: ChangePasswordDialogProps) {
  const qc = useQueryClient()
  const mutation = useChangePassword({ errorToast: false })
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const id = useId()

  const reset = () => {
    setCurrent('')
    setNext('')
    setRepeat('')
    setFormError(null)
    mutation.reset()
  }

  const handleOpenChange = (value: boolean) => {
    if (forced || mutation.isPending) return
    if (!value) reset()
    onOpenChange(value)
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const problem = validatePasswordForm(current, next, repeat)
    setFormError(problem)
    if (problem) return
    mutation.mutate(
      { current_password: current, new_password: next },
      {
        onSuccess: () => {
          qc.setQueryData<User | null>(queryKeys.auth.me(), (me) => (me ? { ...me, must_change_password: false } : me))
          reset()
          onOpenChange(false)
        },
      },
    )
  }

  const shownError = formError ?? (mutation.isError ? errorMessage(mutation.error) : null)
  const preventWhenForced = (e: Event) => {
    if (forced) e.preventDefault()
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent
        showCloseButton={!forced}
        onEscapeKeyDown={preventWhenForced}
        onInteractOutside={preventWhenForced}
        className="sm:max-w-md"
      >
        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          <DialogHeader>
            <DialogTitle>{forced ? 'Смените пароль' : 'Сменить пароль'}</DialogTitle>
            <DialogDescription>
              {forced
                ? 'Вы вошли с временным паролем. Придумайте свой — так никто другой не сможет войти под вашим именем.'
                : `Новый пароль — не короче ${MIN_PASSWORD_LENGTH} символов.`}
            </DialogDescription>
          </DialogHeader>

          <div className="flex flex-col gap-2">
            <Label htmlFor={`${id}-current`}>Текущий пароль</Label>
            <Input
              id={`${id}-current`}
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
              autoFocus
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={`${id}-new`}>Новый пароль</Label>
            <Input
              id={`${id}-new`}
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
              minLength={MIN_PASSWORD_LENGTH}
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={`${id}-repeat`}>Повторите новый пароль</Label>
            <Input
              id={`${id}-repeat`}
              type="password"
              autoComplete="new-password"
              value={repeat}
              onChange={(e) => setRepeat(e.target.value)}
              required
            />
          </div>

          {shownError && (
            <Alert variant="destructive">
              <CircleAlert aria-hidden="true" />
              <AlertDescription>{shownError}</AlertDescription>
            </Alert>
          )}

          <DialogFooter>
            {forced ? (
              onLogout && (
                <Button type="button" variant="ghost" onClick={onLogout} disabled={mutation.isPending}>
                  Выйти
                </Button>
              )
            ) : (
              <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={mutation.isPending}>
                Отмена
              </Button>
            )}
            <Button type="submit" disabled={mutation.isPending}>
              {mutation.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
              Сохранить пароль
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

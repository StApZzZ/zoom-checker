import { useId, useState, type FormEvent } from 'react'
import { CircleAlert, Info, Loader2 } from 'lucide-react'

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
import { normalizeName } from '@/components/groups/students-utils'
import { errorMessage } from '@/lib/api'
import { useAddAlias } from '@/lib/queries'
import type { Student } from '@/lib/types'

export interface AddAliasDialogProps {
  groupId: number
  /** Student to add a Zoom name to; null closes the dialog. */
  student: Student | null
  onClose: () => void
}

/** "Как студент может называться в Zoom": adds an extra exact-match name. */
export function AddAliasDialog({ groupId, student, onClose }: AddAliasDialogProps) {
  return (
    <Dialog open={student !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        {student && <AddAliasForm key={student.id} groupId={groupId} student={student} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function AddAliasForm({ groupId, student, onClose }: { groupId: number; student: Student; onClose: () => void }) {
  const id = useId()
  const add = useAddAlias(groupId, { errorToast: false, successMessage: 'Имя в Zoom добавлено' })
  const [alias, setAlias] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const value = alias.trim()
    if (!value) {
      setLocalError('Введите имя, как оно написано в Zoom.')
      return
    }
    const norm = normalizeName(value)
    if (norm === student.normalized_name || norm === normalizeName(student.full_name)) {
      setLocalError('Это имя совпадает с основным именем студента — его добавлять не нужно.')
      return
    }
    if (student.aliases.some((a) => normalizeName(a.alias) === norm)) {
      setLocalError('Такое имя у студента уже есть.')
      return
    }
    setLocalError(null)
    add.mutate({ studentId: student.id, alias: value }, { onSuccess: onClose })
  }

  const shownError = localError ?? (add.isError ? errorMessage(add.error) : null)
  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
      <DialogHeader>
        <DialogTitle>Как студент может называться в Zoom</DialogTitle>
        <DialogDescription>
          Студент: <span className="font-medium text-foreground">{student.full_name}</span>
        </DialogDescription>
      </DialogHeader>
      <Alert>
        <Info aria-hidden="true" />
        <AlertDescription>
          Сервис узнаёт студента, только если имя в Zoom совпадает с записанным полностью. Большие и маленькие буквы,
          лишние пробелы и «ё» вместо «е» не важны, а сокращения, опечатки и латиница — важны. Добавьте имя ровно так,
          как оно видно в списке участников Zoom.
        </AlertDescription>
      </Alert>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-alias`}>Имя в Zoom</Label>
        <Input
          id={`${id}-alias`}
          value={alias}
          onChange={(e) => setAlias(e.target.value)}
          placeholder="Например, Ivanov Ivan или Ваня Иванов"
          autoComplete="off"
          autoFocus
          className="h-10"
          aria-invalid={shownError ? true : undefined}
        />
      </div>
      {shownError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{shownError}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose} disabled={add.isPending}>
          Отмена
        </Button>
        <Button type="submit" disabled={add.isPending}>
          {add.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Добавить имя
        </Button>
      </DialogFooter>
    </form>
  )
}

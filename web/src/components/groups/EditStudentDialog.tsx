import { useId, useState, type FormEvent } from 'react'
import { CircleAlert, Loader2 } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
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
import { ApiError, errorMessage } from '@/lib/api'
import { useUpdateStudent } from '@/lib/queries'
import type { Student } from '@/lib/types'

export interface EditStudentDialogProps {
  groupId: number
  /** Student being edited; null closes the dialog. */
  student: Student | null
  onClose: () => void
}

/** Edit the name / ID of a student. A name taken in the group (409) is explained inline. */
export function EditStudentDialog({ groupId, student, onClose }: EditStudentDialogProps) {
  return (
    <Dialog open={student !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        {/* Remount per student so the fields start from its current values. */}
        {student && <EditStudentForm key={student.id} groupId={groupId} student={student} onClose={onClose} />}
      </DialogContent>
    </Dialog>
  )
}

function EditStudentForm({ groupId, student, onClose }: { groupId: number; student: Student; onClose: () => void }) {
  const id = useId()
  const update = useUpdateStudent(groupId, { errorToast: false })
  const [fullName, setFullName] = useState(student.full_name)
  const [externalId, setExternalId] = useState(student.external_id)
  const [nameError, setNameError] = useState<string | null>(null)

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const name = fullName.trim()
    if (!name) {
      setNameError('Введите фамилию и имя.')
      return
    }
    setNameError(null)
    const input: { full_name?: string; external_id?: string } = {}
    if (name !== student.full_name) input.full_name = name
    if (externalId.trim() !== student.external_id) input.external_id = externalId.trim()
    if (Object.keys(input).length === 0) {
      onClose()
      return
    }
    update.mutate({ studentId: student.id, input }, { onSuccess: onClose })
  }

  const conflict = update.error instanceof ApiError && update.error.status === 409
  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
      <DialogHeader>
        <DialogTitle>Изменить данные студента</DialogTitle>
        <DialogDescription>
          Имя должно совпадать с тем, как студент подписан в Zoom. Если студент подписан иначе — добавьте ему
          дополнительное имя.
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-name`}>Фамилия и имя</Label>
        <Input
          id={`${id}-name`}
          value={fullName}
          onChange={(e) => setFullName(e.target.value)}
          autoComplete="off"
          autoFocus
          className="h-10"
          aria-invalid={nameError || conflict ? true : undefined}
          aria-describedby={nameError ? `${id}-name-error` : undefined}
        />
        {nameError && (
          <p id={`${id}-name-error`} className="text-sm text-destructive">
            {nameError}
          </p>
        )}
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-ext`}>
          ID студента <span className="font-normal text-muted-foreground">(необязательно)</span>
        </Label>
        <Input
          id={`${id}-ext`}
          value={externalId}
          onChange={(e) => setExternalId(e.target.value)}
          autoComplete="off"
          className="h-10 w-48"
        />
      </div>

      {update.isError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          {conflict && <AlertTitle>Такое имя уже есть в группе</AlertTitle>}
          <AlertDescription className="flex flex-col gap-1">
            <p>{errorMessage(update.error)}</p>
            {conflict && (
              <p>
                Имена в группе не должны повторяться, иначе сервис не отличит студентов в Zoom. Добавьте уточнение,
                например «{fullName.trim() || 'Иванов Иван'} А.».
              </p>
            )}
          </AlertDescription>
        </Alert>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose} disabled={update.isPending}>
          Отмена
        </Button>
        <Button type="submit" disabled={update.isPending}>
          {update.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Сохранить
        </Button>
      </DialogFooter>
    </form>
  )
}

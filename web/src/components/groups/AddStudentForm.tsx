import { useId, useRef, useState, type FormEvent } from 'react'
import { CircleAlert, Loader2, UserPlus } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ApiError, errorMessage } from '@/lib/api'
import { useAddStudent } from '@/lib/queries'

/** Inline "add one student" form above the roster. */
export function AddStudentForm({ groupId }: { groupId: number }) {
  const id = useId()
  const add = useAddStudent(groupId, { errorToast: false })
  const [fullName, setFullName] = useState('')
  const [externalId, setExternalId] = useState('')
  const [localError, setLocalError] = useState<string | null>(null)
  const nameRef = useRef<HTMLInputElement>(null)

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const name = fullName.trim()
    if (!name) {
      setLocalError('Введите фамилию и имя студента.')
      nameRef.current?.focus()
      return
    }
    setLocalError(null)
    add.mutate(
      { full_name: name, external_id: externalId.trim() || undefined },
      {
        onSuccess: () => {
          setFullName('')
          setExternalId('')
          nameRef.current?.focus()
        },
      },
    )
  }

  const conflict = add.error instanceof ApiError && add.error.status === 409
  const error = localError ?? (add.isError ? errorMessage(add.error) : null)

  return (
    <form
      onSubmit={onSubmit}
      noValidate
      aria-labelledby={`${id}-title`}
      className="flex flex-col gap-3 rounded-xl bg-muted/40 p-4 ring-1 ring-foreground/5"
    >
      <h3 id={`${id}-title`} className="text-sm font-medium">
        Добавить студента
      </h3>
      <div className="flex flex-col gap-3 md:flex-row md:items-end">
        <div className="flex flex-1 flex-col gap-1.5">
          <Label htmlFor={`${id}-name`} className="text-xs text-muted-foreground">
            Фамилия и имя — как в Zoom
          </Label>
          <Input
            ref={nameRef}
            id={`${id}-name`}
            value={fullName}
            onChange={(e) => setFullName(e.target.value)}
            placeholder="Иванов Иван"
            autoComplete="off"
            className="h-10 bg-background"
            disabled={add.isPending}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? `${id}-error` : undefined}
          />
        </div>
        <div className="flex flex-col gap-1.5 md:w-44">
          <Label htmlFor={`${id}-ext`} className="text-xs text-muted-foreground">
            ID (необязательно)
          </Label>
          <Input
            id={`${id}-ext`}
            value={externalId}
            onChange={(e) => setExternalId(e.target.value)}
            autoComplete="off"
            className="h-10 bg-background"
            disabled={add.isPending}
          />
        </div>
        <Button type="submit" size="lg" className="h-10" disabled={add.isPending}>
          {add.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <UserPlus aria-hidden="true" />}
          Добавить
        </Button>
      </div>
      {error && (
        <div id={`${id}-error`} role="alert" className="flex items-start gap-2 text-sm text-destructive">
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <div className="flex flex-col gap-0.5">
            <span>{error}</span>
            {conflict && (
              <span className="text-muted-foreground">
                Если это другой человек, добавьте уточнение, например «{fullName.trim() || 'Иванов Иван'} А.».
              </span>
            )}
          </div>
        </div>
      )}
    </form>
  )
}

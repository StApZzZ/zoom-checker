import { useState, type FormEvent } from 'react'
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
import { GroupFormFields } from '@/components/groups/GroupFormFields'
import {
  groupFormFrom,
  groupInput,
  hasErrors,
  validateGroupForm,
  type GroupFormErrors,
  type GroupFormValues,
} from '@/components/groups/group-form'
import { errorMessage } from '@/lib/api'
import { useUpdateGroup } from '@/lib/queries'
import type { GroupSummary } from '@/lib/types'

export interface EditGroupDialogProps {
  group: GroupSummary
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Rename a group, change its description or default lesson duration. */
export function EditGroupDialog({ group, open, onOpenChange }: EditGroupDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        {open && <EditGroupForm group={group} onClose={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function EditGroupForm({ group, onClose }: { group: GroupSummary; onClose: () => void }) {
  const update = useUpdateGroup(group.id, { errorToast: false })
  const [values, setValues] = useState<GroupFormValues>(() => groupFormFrom(group))
  const [errors, setErrors] = useState<GroupFormErrors>({})

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const problems = validateGroupForm(values)
    setErrors(problems)
    if (hasErrors(problems)) return
    update.mutate(groupInput(values), { onSuccess: onClose })
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-5" noValidate>
      <DialogHeader>
        <DialogTitle>Изменить группу</DialogTitle>
        <DialogDescription>Студенты и занятия группы не изменятся.</DialogDescription>
      </DialogHeader>
      <GroupFormFields values={values} onChange={setValues} errors={errors} disabled={update.isPending} autoFocus />
      {update.isError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{errorMessage(update.error)}</AlertDescription>
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

import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { ArrowLeft, CircleAlert, CircleCheck, Loader2, Save } from 'lucide-react'
import { toast } from 'sonner'

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
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { GroupFormFields } from '@/components/groups/GroupFormFields'
import {
  emptyGroupForm,
  groupInput,
  hasErrors,
  validateGroupForm,
  type GroupFormErrors,
  type GroupFormValues,
} from '@/components/groups/group-form'
import { ImportPreviewView } from '@/components/groups/ImportPreviewView'
import { canCommit, importResultSummary, PASTE_PLACEHOLDER } from '@/components/groups/import-utils'
import { useImportFlow } from '@/components/groups/useImportFlow'
import { errorMessage } from '@/lib/api'
import { useCreateGroup } from '@/lib/queries'
import type { GroupSummary, ImportResult } from '@/lib/types'

export interface CreateGroupDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

type Stage = 'form' | 'import'

/**
 * "Новая группа": name, description, default duration and an optional pasted
 * student list. With a list, the group is created and the list is checked and
 * saved in the same dialog (automatically when the check finds nothing to ask).
 */
export function CreateGroupDialog({ open, onOpenChange }: CreateGroupDialogProps) {
  const id = useId()
  const navigate = useNavigate()
  const createGroup = useCreateGroup({ errorToast: false, successMessage: '' })
  const [values, setValues] = useState<GroupFormValues>(emptyGroupForm)
  const [errors, setErrors] = useState<GroupFormErrors>({})
  const [studentsText, setStudentsText] = useState('')
  const [stage, setStage] = useState<Stage>('form')
  const [created, setCreated] = useState<GroupSummary | null>(null)
  const [attempt, setAttempt] = useState(0)
  const [importBusy, setImportBusy] = useState(false)

  const reset = () => {
    setValues(emptyGroupForm())
    setErrors({})
    setStudentsText('')
    setStage('form')
    setCreated(null)
    setAttempt(0)
    setImportBusy(false)
    createGroup.reset()
  }

  const finish = (group: GroupSummary, result: ImportResult | null) => {
    const description = result
      ? importResultSummary(result)
      : studentsText.trim()
        ? 'Список студентов не сохранён — его можно загрузить на странице группы.'
        : 'Теперь добавьте студентов.'
    toast.success(`Группа «${group.name}» создана`, { description })
    reset()
    onOpenChange(false)
    navigate(`/groups/${group.id}`)
  }

  const handleOpenChange = (next: boolean) => {
    if (next) {
      onOpenChange(true)
      return
    }
    if (createGroup.isPending || importBusy) return
    // The group already exists: go to it even if the list was not saved.
    if (created) finish(created, null)
    else {
      reset()
      onOpenChange(false)
    }
  }

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    const hasList = studentsText.trim().length > 0
    if (created) {
      // Fixing the list of a group created a moment ago.
      if (!hasList) {
        finish(created, null)
        return
      }
      setAttempt((n) => n + 1)
      setStage('import')
      return
    }
    const problems = validateGroupForm(values)
    setErrors(problems)
    if (hasErrors(problems)) return
    let group: GroupSummary
    try {
      group = await createGroup.mutateAsync(groupInput(values))
    } catch {
      return // shown inline from createGroup.error
    }
    if (!hasList) {
      finish(group, null)
      return
    }
    setCreated(group)
    setAttempt((n) => n + 1)
    setStage('import')
  }

  const wide = stage === 'import'
  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className={wide ? 'max-h-[92svh] overflow-y-auto sm:max-w-4xl' : 'max-h-[92svh] overflow-y-auto sm:max-w-2xl'}>
        {stage === 'import' && created ? (
          <NewGroupImport
            key={attempt}
            group={created}
            text={studentsText}
            onTextChange={setStudentsText}
            onBusyChange={setImportBusy}
            onBack={() => setStage('form')}
            onDone={(result) => finish(created, result)}
            onSkip={() => finish(created, null)}
          />
        ) : (
          <form onSubmit={onSubmit} className="flex flex-col gap-5" noValidate>
            <DialogHeader>
              <DialogTitle>{created ? 'Исправьте список студентов' : 'Новая группа'}</DialogTitle>
              <DialogDescription>
                {created
                  ? `Группа «${created.name}» уже создана. Исправьте список и проверьте его снова.`
                  : 'Группа — это список студентов одного курса. По нему сервис узнаёт студентов в Zoom.'}
              </DialogDescription>
            </DialogHeader>

            {!created && (
              <GroupFormFields
                values={values}
                onChange={setValues}
                errors={errors}
                disabled={createGroup.isPending}
                autoFocus
              />
            )}

            <div className="flex flex-col gap-2">
              <Label htmlFor={`${id}-students`}>
                Вставьте список студентов (по одному в строке)
                {!created && <span className="font-normal text-muted-foreground"> — необязательно</span>}
              </Label>
              <Textarea
                id={`${id}-students`}
                value={studentsText}
                onChange={(e) => setStudentsText(e.target.value)}
                placeholder={PASTE_PLACEHOLDER}
                rows={created ? 12 : 7}
                className="font-mono text-sm"
                disabled={createGroup.isPending}
                aria-describedby={`${id}-students-hint`}
                autoFocus={!!created}
              />
              <p id={`${id}-students-hint`} className="text-xs text-muted-foreground">
                Можно скопировать столбец из Excel. Список можно добавить и позже — на странице группы.
              </p>
            </div>

            {createGroup.isError && (
              <Alert variant="destructive">
                <CircleAlert aria-hidden="true" />
                <AlertDescription>{errorMessage(createGroup.error)}</AlertDescription>
              </Alert>
            )}

            <DialogFooter>
              {created ? (
                <Button type="button" variant="outline" onClick={() => finish(created, null)}>
                  Пропустить
                </Button>
              ) : (
                <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={createGroup.isPending}>
                  Отмена
                </Button>
              )}
              <Button type="submit" disabled={createGroup.isPending}>
                {createGroup.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
                {created ? 'Проверить список' : studentsText.trim() ? 'Создать группу и добавить студентов' : 'Создать группу'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

interface NewGroupImportProps {
  group: GroupSummary
  text: string
  /** «Убрать повтор» changed the list: keep the box in step for «Исправить список». */
  onTextChange: (text: string) => void
  onBusyChange: (busy: boolean) => void
  onBack: () => void
  onDone: (result: ImportResult) => void
  onSkip: () => void
}

/** Checks the pasted list of a just created group; saves it right away when there is nothing to decide. */
function NewGroupImport({ group, text, onTextChange, onBusyChange, onBack, onDone, onSkip }: NewGroupImportProps) {
  const flow = useImportFlow(group.id, { toastOnSuccess: false })
  const started = useRef(false)
  const [autoSaving, setAutoSaving] = useState(false)
  const { runPreview, commit } = flow
  const busy = flow.previewing || flow.committing

  useEffect(() => {
    onBusyChange(busy)
  }, [busy, onBusyChange])

  // Runs once per mounted attempt (the parent remounts it with a new key);
  // the ref also keeps StrictMode's second effect run from checking twice.
  useEffect(() => {
    if (started.current) return
    started.current = true
    void (async () => {
      const preview = await runPreview({ source: { text }, kind: 'text', label: 'Вставленный список' }, 'add')
      // Ask only when there is a choice to make (patronymics) or a problem to fix.
      if (!preview || !canCommit(preview) || preview.has_patronymics) return
      setAutoSaving(true)
      const result = await commit()
      setAutoSaving(false)
      if (result) onDone(result)
    })()
  }, [runPreview, commit, text, onDone])

  const save = async () => {
    const result = await commit()
    if (result) onDone(result)
  }

  // Re-checks without saving right away: the teacher sees the fixed list first.
  const replaceText = (next: string) => {
    onTextChange(next)
    void runPreview({ source: { text: next }, kind: 'text', label: 'Вставленный список' }, 'add')
  }

  const { preview } = flow
  return (
    <div className="flex flex-col gap-5">
      <DialogHeader>
        <DialogTitle>Проверьте список студентов</DialogTitle>
        <DialogDescription>Посмотрите, всё ли сервис понял правильно, и сохраните список.</DialogDescription>
      </DialogHeader>

      <Alert>
        <CircleCheck aria-hidden="true" className="text-emerald-600 dark:text-emerald-400" />
        <AlertTitle>Группа «{group.name}» создана</AlertTitle>
        <AlertDescription>Осталось сохранить список студентов.</AlertDescription>
      </Alert>

      {!preview || autoSaving ? (
        flow.previewError && !flow.previewing ? (
          <Alert variant="destructive">
            <CircleAlert aria-hidden="true" />
            <AlertDescription>{flow.previewError}</AlertDescription>
          </Alert>
        ) : (
          <p role="status" className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" aria-hidden="true" />
            {autoSaving ? 'Сохраняем список…' : 'Проверяем список…'}
          </p>
        )
      ) : (
        <ImportPreviewView
          preview={preview}
          sourceInfo={flow.sourceInfo}
          onNameFormatChange={(f) => void flow.changeNameFormat(f)}
          updating={flow.previewing}
          commitError={flow.commitError}
          previewError={flow.previewError}
          onPastedTextChange={replaceText}
        />
      )}

      <DialogFooter className="sm:justify-between">
        <div className="flex flex-col-reverse gap-2 sm:flex-row">
          <Button type="button" variant="ghost" onClick={onSkip} disabled={busy}>
            Пропустить
          </Button>
          <Button type="button" variant="outline" onClick={onBack} disabled={busy}>
            <ArrowLeft aria-hidden="true" />
            Исправить список
          </Button>
        </div>
        <Button
          type="button"
          onClick={() => void save()}
          disabled={!preview || !canCommit(preview) || busy || autoSaving}
        >
          {flow.committing ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Save aria-hidden="true" />}
          Сохранить список
        </Button>
      </DialogFooter>
    </div>
  )
}

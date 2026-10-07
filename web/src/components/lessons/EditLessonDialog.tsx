import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { CircleAlert, Loader2, Pencil } from 'lucide-react'

import { Field, ScheduleFields, ZoomField } from '@/components/lessons/LessonFormFields'
import {
  describedBy,
  firstInvalidField,
  plannedRange,
  scheduleFromLesson,
  serverFieldErrors,
  validateLessonForm,
  type LessonFormErrors,
  type LessonFormValues,
  type ScheduleValues,
} from '@/components/lessons/lessonForm'
import { useZoomCheck } from '@/components/lessons/useZoomCheck'
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
import { ApiError, errorMessage } from '@/lib/api'
import { useUpdateLesson } from '@/lib/queries'
import type { Lesson, LessonSummary, UpdateLessonInput } from '@/lib/types'

type EditableLesson = Pick<
  Lesson | LessonSummary,
  'id' | 'title' | 'zoom_join_url' | 'zoom_meeting_id' | 'planned_start_at' | 'planned_end_at' | 'status'
>

export interface EditLessonDialogProps {
  lesson: EditableLesson
  open: boolean
  onOpenChange: (open: boolean) => void
}

/** Initial Zoom field text: the join link when known, otherwise the meeting number. */
function initialZoom(lesson: EditableLesson): string {
  return lesson.zoom_join_url || lesson.zoom_meeting_id || ''
}

/** "Изменить занятие": title, Zoom link, date, start time and duration. */
export function EditLessonDialog({ lesson, open, onOpenChange }: EditLessonDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Pencil className="size-5 text-muted-foreground" aria-hidden="true" />
            Изменить занятие
          </DialogTitle>
          <DialogDescription>
            {lesson.status === 'finished'
              ? 'После изменения времени или ссылки посещаемость будет пересчитана.'
              : 'Изменения сразу появятся на экране занятия.'}
          </DialogDescription>
        </DialogHeader>
        <EditLessonForm lesson={lesson} onDone={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function EditLessonForm({ lesson, onDone }: { lesson: EditableLesson; onDone: () => void }) {
  const id = useId()
  const update = useUpdateLesson(lesson.id, { errorToast: false })
  const [initial] = useState(() => ({
    title: lesson.title,
    zoom: initialZoom(lesson),
    ...scheduleFromLesson(lesson.planned_start_at, lesson.planned_end_at),
  }))
  const [title, setTitle] = useState(initial.title)
  const [zoom, setZoom] = useState(initial.zoom)
  const [schedule, setSchedule] = useState<ScheduleValues>({
    date: initial.date,
    time: initial.time,
    duration: initial.duration,
  })
  const [errors, setErrors] = useState<LessonFormErrors>({})
  const [formError, setFormError] = useState<string | null>(null)
  const zoomCheck = useZoomCheck(zoom)
  const pending = update.isPending

  const values: LessonFormValues = { groupId: '', title, zoom, ...schedule, repeat: false, weeks: '' }

  const clearError = (...fields: (keyof LessonFormValues)[]) => {
    if (fields.some((f) => errors[f])) {
      setErrors((prev) => {
        const next = { ...prev }
        for (const f of fields) delete next[f]
        return next
      })
    }
  }

  const focusField = (field: keyof LessonFormValues | undefined) => {
    if (field) document.getElementById(`${id}-${field}`)?.focus()
  }

  // Fields are disabled while saving: a server-side field error is focused once they are enabled again.
  const focusAfterSave = useRef<keyof LessonFormValues | undefined>(undefined)
  useEffect(() => {
    if (!pending && focusAfterSave.current) {
      document.getElementById(`${id}-${focusAfterSave.current}`)?.focus()
      focusAfterSave.current = undefined
    }
  })

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (pending) return
    setFormError(null)
    const zoomChanged = zoom.trim() !== initial.zoom.trim()
    const found = validateLessonForm(values, {
      requireGroup: false,
      zoomError: zoomChanged && zoomCheck.state === 'invalid' ? zoomCheck.message : null,
    })
    setErrors(found)
    if (Object.keys(found).length > 0) {
      focusField(firstInvalidField(found))
      return
    }
    const range = plannedRange(schedule)
    if (!range) return
    const input: UpdateLessonInput = {}
    if (title.trim() !== initial.title) input.title = title.trim()
    // Only a changed link is sent: re-sending the same one must not relink Zoom sessions.
    if (zoomChanged) input.zoom = zoom.trim()
    const initialRange = plannedRange(initial)
    if (!initialRange || range.start !== initialRange.start || range.end !== initialRange.end) {
      input.planned_start_at = range.start
      input.planned_end_at = range.end
    }
    if (Object.keys(input).length === 0) {
      onDone()
      return
    }
    update.mutate(input, {
      onSuccess: onDone,
      onError: (err) => {
        if (err instanceof ApiError && err.isValidation) {
          const fieldErrors = serverFieldErrors(err.details, err.message)
          if (Object.keys(fieldErrors).length > 0) {
            setErrors(fieldErrors)
            focusAfterSave.current = firstInvalidField(fieldErrors)
            return
          }
        }
        setFormError(errorMessage(err))
      },
    })
  }

  const titleId = `${id}-title`
  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
      <Field id={titleId} label="Название" error={errors.title}>
        <Input
          id={titleId}
          name="title"
          className="h-10"
          value={title}
          onChange={(e) => {
            setTitle(e.target.value)
            clearError('title')
          }}
          maxLength={200}
          disabled={pending}
          {...describedBy(titleId, errors.title)}
        />
      </Field>
      <ZoomField
        id={`${id}-zoom`}
        value={zoom}
        onChange={(v) => {
          setZoom(v)
          clearError('zoom')
        }}
        check={zoomCheck}
        error={errors.zoom}
        disabled={pending}
      />
      <ScheduleFields
        idPrefix={id}
        values={schedule}
        errors={errors}
        disabled={pending}
        onChange={(patch) => {
          setSchedule((prev) => ({ ...prev, ...patch }))
          clearError(...(Object.keys(patch) as (keyof ScheduleValues)[]))
        }}
      />
      {formError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" size="lg" onClick={onDone} disabled={pending}>
          Отмена
        </Button>
        <Button type="submit" size="lg" disabled={pending}>
          {pending && <Loader2 className="animate-spin" aria-hidden="true" />}
          Сохранить
        </Button>
      </DialogFooter>
    </form>
  )
}

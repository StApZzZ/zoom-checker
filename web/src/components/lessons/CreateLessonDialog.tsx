import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { CalendarPlus, CircleAlert, Loader2, Plus, Users } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import { toast } from 'sonner'

import { Field, ScheduleFields, ZoomField } from '@/components/lessons/LessonFormFields'
import {
  DEFAULT_DURATION_MINUTES,
  DEFAULT_WEEKS,
  defaultSchedule,
  describedBy,
  firstInvalidField,
  MAX_WEEKS,
  MIN_WEEKS,
  plannedRange,
  serverFieldErrors,
  seriesTitlesPreview,
  suggestTitle,
  validateLessonForm,
  weeklyPreview,
  type LessonFormErrors,
  type LessonFormValues,
  type ScheduleValues,
} from '@/components/lessons/lessonForm'
import { useZoomCheck } from '@/components/lessons/useZoomCheck'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { ApiError, errorMessage } from '@/lib/api'
import { pluralize, WORDS } from '@/lib/format'
import { useCreateLesson, useGroups, useLessons } from '@/lib/queries'
import type { Lesson } from '@/lib/types'

export interface CreateLessonDialogProps {
  /** Controlled open state; omit together with onOpenChange to use `trigger`. */
  open?: boolean
  onOpenChange?: (open: boolean) => void
  /** Element that opens the dialog (rendered via asChild). */
  trigger?: ReactNode
  /** Preselected group (e.g. when opened from the group page). */
  groupId?: number
  /**
   * Called with all created lessons (one, or the whole weekly series). By default
   * the dialog opens the first created lesson.
   */
  onCreated?: (lessons: Lesson[]) => void
}

/** "Новое занятие" dialog: group, title, Zoom link, date/time/duration, weekly repeat. */
export function CreateLessonDialog({ open: openProp, onOpenChange, trigger, groupId, onCreated }: CreateLessonDialogProps) {
  const [openState, setOpenState] = useState(false)
  const open = openProp ?? openState
  const setOpen = (next: boolean) => {
    if (openProp === undefined) setOpenState(next)
    onOpenChange?.(next)
  }
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      {trigger && <DialogTrigger asChild>{trigger}</DialogTrigger>}
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <CalendarPlus className="size-5 text-muted-foreground" aria-hidden="true" />
            Новое занятие
          </DialogTitle>
          <DialogDescription>
            Укажите группу и ссылку на Zoom. Во время урока откройте занятие — посещаемость будет отмечаться сама.
          </DialogDescription>
        </DialogHeader>
        {/* Mounted only while open, so every opening starts with fresh defaults. */}
        <CreateLessonForm
          initialGroupId={groupId}
          onCancel={() => setOpen(false)}
          onCreated={(lessons) => {
            setOpen(false)
            onCreated?.(lessons)
          }}
          navigateOnCreate={!onCreated}
        />
      </DialogContent>
    </Dialog>
  )
}

interface CreateLessonFormProps {
  initialGroupId?: number
  onCancel: () => void
  onCreated: (lessons: Lesson[]) => void
  navigateOnCreate: boolean
}

function CreateLessonForm({ initialGroupId, onCancel, onCreated, navigateOnCreate }: CreateLessonFormProps) {
  const id = useId()
  const navigate = useNavigate()
  const groups = useGroups('active')
  const create = useCreateLesson({ errorToast: false })

  const [groupChoice, setGroupChoice] = useState(initialGroupId ? String(initialGroupId) : '')
  const [titleInput, setTitleInput] = useState<string | null>(null)
  const [zoom, setZoom] = useState('')
  const [schedule, setSchedule] = useState<Omit<ScheduleValues, 'duration'>>(() => defaultSchedule(new Date()))
  const [durationInput, setDurationInput] = useState<string | null>(null)
  const [repeat, setRepeat] = useState(false)
  const [weeks, setWeeks] = useState(String(DEFAULT_WEEKS))
  const [errors, setErrors] = useState<LessonFormErrors>({})
  const [formError, setFormError] = useState<string | null>(null)

  const groupList = groups.data ?? []
  // A preselected group that is not active (archived, deleted, a stale ?group= link)
  // cannot get lessons: drop it instead of sending it and failing on save.
  const choiceValid = groupChoice !== '' && (groups.isError || groupList.some((g) => String(g.id) === groupChoice))
  // A teacher with exactly one group does not have to pick it.
  const groupId = choiceValid ? groupChoice : groupList.length === 1 ? String(groupList[0].id) : ''
  const group = groupList.find((g) => String(g.id) === groupId)
  const numericGroupId = groupId ? Number(groupId) : undefined

  const groupLessons = useLessons({ group_id: numericGroupId }, { enabled: !!numericGroupId })
  const lessonCountKnown = !!numericGroupId && !!groupLessons.data && !groupLessons.isPlaceholderData
  // Until the teacher edits them, the title and duration follow the chosen group.
  const title = titleInput ?? (lessonCountKnown ? suggestTitle(groupLessons.data!.length) : '')
  // The teacher put the cursor into the still empty title field: the suggestion must not
  // appear under the cursor, or what they type would end up after «Занятие №N». Released
  // on blur when nothing was typed, so the suggestion still comes for a field just tabbed through.
  const titleClaimed = useRef(false)
  const claimTitle = () => {
    if (titleInput === null && title === '') {
      titleClaimed.current = true
      setTitleInput('')
    }
  }
  const releaseTitle = () => {
    if (titleClaimed.current && titleInput === '') setTitleInput(null)
    titleClaimed.current = false
  }
  const duration = durationInput ?? String(group?.default_duration_minutes || DEFAULT_DURATION_MINUTES)

  const zoomCheck = useZoomCheck(zoom)

  const values: LessonFormValues = { groupId, title, zoom, ...schedule, duration, repeat, weeks }
  const preview = repeat ? weeklyPreview(schedule.date, schedule.time, weeks) : null
  const titlesPreview = preview ? seriesTitlesPreview(title, weeks) : null
  const pending = create.isPending

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
    if (!field) return
    const el = document.getElementById(`${id}-${field}`)
    el?.focus()
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
    const found = validateLessonForm(values, {
      requireGroup: true,
      zoomError: zoomCheck.state === 'invalid' ? zoomCheck.message : null,
    })
    setErrors(found)
    if (Object.keys(found).length > 0) {
      focusField(firstInvalidField(found))
      return
    }
    const range = plannedRange(values)
    if (!range) return
    create.mutate(
      {
        group_id: Number(groupId),
        title: title.trim(),
        zoom: zoom.trim(),
        planned_start_at: range.start,
        planned_end_at: range.end,
        ...(repeat ? { repeat_weekly: Number(weeks) } : {}),
      },
      {
        onSuccess: (created) => {
          const list = Array.isArray(created) ? created : []
          toast.success(list.length > 1 ? `Создано ${pluralize(list.length, WORDS.lesson)}` : 'Занятие создано')
          onCreated(list)
          if (navigateOnCreate && list[0]) void navigate(`/lessons/${list[0].id}`)
        },
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
      },
    )
  }

  if (groups.isPending) {
    return (
      <div role="status" aria-busy="true" className="flex flex-col gap-4">
        <span className="sr-only">Загрузка…</span>
        <Skeleton className="h-10" />
        <Skeleton className="h-10" />
        <Skeleton className="h-16" />
      </div>
    )
  }

  if (!groups.isError && groupList.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 rounded-lg border border-dashed px-4 py-8 text-center">
        <div className="flex size-11 items-center justify-center rounded-full bg-muted text-muted-foreground">
          <Users className="size-5" aria-hidden="true" />
        </div>
        <div className="flex flex-col gap-1">
          <p className="font-medium">Сначала создайте группу</p>
          <p className="text-sm text-muted-foreground">
            Занятие проводится для группы студентов. Создайте группу и добавьте список студентов — это займёт пару минут.
          </p>
        </div>
        <Button asChild size="lg" onClick={onCancel}>
          <Link to="/groups">
            <Plus aria-hidden="true" />
            Создать группу
          </Link>
        </Button>
      </div>
    )
  }

  const groupFieldId = `${id}-groupId`
  const titleId = `${id}-title`
  const weeksId = `${id}-weeks`

  return (
    <form onSubmit={onSubmit} noValidate className="flex flex-col gap-5">
      <div className="flex flex-col gap-2">
        <div className="flex items-center justify-between gap-2">
          <Label htmlFor={groupFieldId}>Группа</Label>
          <Link
            to="/groups"
            onClick={onCancel}
            className="rounded-sm text-sm text-primary underline-offset-4 outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
          >
            + создать группу
          </Link>
        </div>
        <Select
          value={groupId}
          onValueChange={(v) => {
            setGroupChoice(v)
            clearError('groupId')
          }}
          disabled={pending || groups.isError}
        >
          <SelectTrigger id={groupFieldId} className="h-10 w-full" {...describedBy(groupFieldId, errors.groupId)}>
            <SelectValue placeholder={groups.isError ? 'Не удалось загрузить группы' : 'Выберите группу'} />
          </SelectTrigger>
          <SelectContent position="popper">
            {groupList.map((g) => (
              <SelectItem key={g.id} value={String(g.id)}>
                {g.name}
                <span className="text-muted-foreground">· {pluralize(g.student_count, WORDS.student)}</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {errors.groupId && (
          <p id={`${groupFieldId}-error`} className="flex items-start gap-1.5 text-sm text-destructive">
            <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            {errors.groupId}
          </p>
        )}
      </div>

      <Field id={titleId} label="Название" error={errors.title}>
        <Input
          id={titleId}
          name="title"
          className="h-10"
          value={title}
          placeholder={numericGroupId && !lessonCountKnown ? 'Занятие №…' : 'Например, Занятие №1'}
          onFocus={claimTitle}
          onBlur={releaseTitle}
          onChange={(e) => {
            titleClaimed.current = false
            setTitleInput(e.target.value)
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
        values={{ ...schedule, duration }}
        errors={errors}
        disabled={pending}
        onChange={(patch) => {
          const { duration: d, ...rest } = patch
          if (d !== undefined) setDurationInput(d)
          if (Object.keys(rest).length > 0) setSchedule((prev) => ({ ...prev, ...rest }))
          clearError(...(Object.keys(patch) as (keyof ScheduleValues)[]))
        }}
      />

      <div className="flex flex-col gap-3 rounded-lg bg-muted/50 p-3">
        <div className="flex items-center gap-2.5">
          <Checkbox
            id={`${id}-repeat`}
            checked={repeat}
            onCheckedChange={(v) => {
              setRepeat(v === true)
              clearError('weeks')
            }}
            disabled={pending}
          />
          <Label htmlFor={`${id}-repeat`} className="cursor-pointer">
            Повторять каждую неделю
          </Label>
        </div>
        {repeat && (
          <div className="flex flex-col gap-2 pl-6.5">
            <div className="flex flex-wrap items-center gap-2">
              <Label htmlFor={weeksId} className="font-normal">
                Сколько недель
              </Label>
              <Input
                id={weeksId}
                name="weeks"
                type="number"
                inputMode="numeric"
                min={MIN_WEEKS}
                max={MAX_WEEKS}
                className="h-9 w-20"
                value={weeks}
                onChange={(e) => {
                  setWeeks(e.target.value)
                  clearError('weeks')
                }}
                disabled={pending}
                {...describedBy(weeksId, errors.weeks, !!preview)}
              />
            </div>
            {errors.weeks ? (
              <p id={`${weeksId}-error`} className="flex items-start gap-1.5 text-sm text-destructive">
                <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
                {errors.weeks}
              </p>
            ) : (
              preview && (
                <div id={`${weeksId}-hint`} className="flex flex-col gap-0.5" aria-live="polite">
                  <p className="text-sm font-medium">{preview}</p>
                  {titlesPreview && <p className="text-sm break-words text-muted-foreground">{titlesPreview}</p>}
                </div>
              )
            )}
          </div>
        )}
      </div>

      {formError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{formError}</AlertDescription>
        </Alert>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" size="lg" onClick={onCancel} disabled={pending}>
          Отмена
        </Button>
        <Button type="submit" size="lg" disabled={pending}>
          {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Plus aria-hidden="true" />}
          {repeat ? 'Создать занятия' : 'Создать занятие'}
        </Button>
      </DialogFooter>
    </form>
  )
}

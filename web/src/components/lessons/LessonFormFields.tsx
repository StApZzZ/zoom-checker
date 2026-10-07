// Form pieces shared by CreateLessonDialog and EditLessonDialog.

import type { ReactNode } from 'react'
import { CircleAlert, CircleCheck, Info, Loader2 } from 'lucide-react'

import { TimeZoneNote } from '@/components/TimeZoneNote'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  describedBy,
  DURATION_PRESETS,
  endTimeHint,
  MAX_DURATION_MINUTES,
  MIN_DURATION_MINUTES,
  type LessonFormErrors,
  type ScheduleValues,
} from '@/components/lessons/lessonForm'
import type { ZoomCheck } from '@/components/lessons/useZoomCheck'
import { fromDateTimeInputs } from '@/lib/format'
import { cn } from '@/lib/utils'

/** Inline validation message under a field (announced by screen readers via aria-describedby). */
export function FieldError({ id, children }: { id: string; children?: ReactNode }) {
  if (!children) return null
  return (
    <p id={id} className="flex items-start gap-1.5 text-sm text-destructive">
      <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      <span>{children}</span>
    </p>
  )
}

/** Label + control + hint + error with consistent spacing. */
export function Field({
  id,
  label,
  error,
  hint,
  children,
  className,
}: {
  id: string
  label: ReactNode
  error?: string
  hint?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex min-w-0 flex-col gap-2', className)}>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && !error && (
        <div id={`${id}-hint`} className="text-xs text-muted-foreground">
          {hint}
        </div>
      )}
      <FieldError id={`${id}-error`}>{error}</FieldError>
    </div>
  )
}

/** Result line of the live Zoom link check. */
function ZoomCheckStatus({ id, check }: { id: string; check: ZoomCheck }) {
  let body: ReactNode
  switch (check.state) {
    case 'empty':
      body = (
        <span className="flex items-start gap-1.5 text-muted-foreground">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          Скопируйте приглашение или ссылку из Zoom и вставьте сюда целиком — номер мы найдём сами.
        </span>
      )
      break
    case 'checking':
      body = (
        <span className="flex items-center gap-1.5 text-muted-foreground">
          <Loader2 className="size-4 shrink-0 animate-spin" aria-hidden="true" />
          Проверяем ссылку…
        </span>
      )
      break
    case 'ok':
      body = (
        <span className="flex items-center gap-1.5 font-medium text-emerald-700 dark:text-emerald-400">
          <CircleCheck className="size-4 shrink-0" aria-hidden="true" />
          Конференция {check.formatted}
        </span>
      )
      break
    case 'invalid':
      body = (
        <span className="flex items-start gap-1.5 text-destructive">
          <CircleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          {check.message}
        </span>
      )
      break
    case 'unknown':
      body = (
        <span className="flex items-start gap-1.5 text-muted-foreground">
          <Info className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          Не удалось проверить ссылку сейчас — проверим при сохранении.
        </span>
      )
      break
  }
  return (
    <div id={`${id}-status`} role="status" aria-live="polite" className="text-sm">
      {body}
    </div>
  )
}

export interface ZoomFieldProps {
  id: string
  value: string
  onChange: (value: string) => void
  check: ZoomCheck
  /** Submit-time error (empty field / server rejection). */
  error?: string
  disabled?: boolean
}

/** "Ссылка Zoom или номер конференции" with live recognition feedback. */
export function ZoomField({ id, value, onChange, check, error, disabled }: ZoomFieldProps) {
  // The live check already shows the server's message; do not repeat it as the field error.
  const showError = error && !(check.state === 'invalid' && check.message === error) ? error : undefined
  const invalid = !!error || check.state === 'invalid'
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Label htmlFor={id}>Ссылка Zoom или номер конференции</Label>
      <Textarea
        id={id}
        name="zoom"
        rows={1}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="https://zoom.us/j/1234567890 или 123 456 7890"
        autoComplete="off"
        spellCheck={false}
        className="max-h-40 min-h-10 resize-none"
        disabled={disabled}
        aria-invalid={invalid || undefined}
        aria-describedby={showError ? `${id}-status ${id}-error` : `${id}-status`}
      />
      <ZoomCheckStatus id={id} check={check} />
      <FieldError id={`${id}-error`}>{showError}</FieldError>
    </div>
  )
}

export interface ScheduleFieldsProps {
  idPrefix: string
  values: ScheduleValues
  onChange: (patch: Partial<ScheduleValues>) => void
  errors: LessonFormErrors
  disabled?: boolean
}

/** Дата · Время начала · Длительность (with 45/60/90/120 chips). */
export function ScheduleFields({ idPrefix, values, onChange, errors, disabled }: ScheduleFieldsProps) {
  const dateId = `${idPrefix}-date`
  const timeId = `${idPrefix}-time`
  const durationId = `${idPrefix}-duration`
  const hint = endTimeHint(values)
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 min-[420px]:grid-cols-2">
        <Field id={dateId} label="Дата" error={errors.date}>
          <Input
            id={dateId}
            name="date"
            type="date"
            className="h-10"
            value={values.date}
            onChange={(e) => onChange({ date: e.target.value })}
            disabled={disabled}
            {...describedBy(dateId, errors.date)}
          />
        </Field>
        <Field id={timeId} label="Время начала" error={errors.time}>
          <Input
            id={timeId}
            name="time"
            type="time"
            step={300}
            className="h-10"
            value={values.time}
            onChange={(e) => onChange({ time: e.target.value })}
            disabled={disabled}
            {...describedBy(timeId, errors.time)}
          />
        </Field>
        <TimeZoneNote at={fromDateTimeInputs(values.date, values.time) ?? undefined} className="min-[420px]:col-span-2 -mt-2" />
      </div>
      <Field id={durationId} label="Длительность, минут" error={errors.duration} hint={hint || undefined}>
        <div className="flex flex-wrap items-center gap-2">
          <Input
            id={durationId}
            name="duration"
            type="number"
            inputMode="numeric"
            min={MIN_DURATION_MINUTES}
            max={MAX_DURATION_MINUTES}
            step={5}
            className="h-10 w-24"
            value={values.duration}
            onChange={(e) => onChange({ duration: e.target.value })}
            disabled={disabled}
            {...describedBy(durationId, errors.duration, !!hint)}
          />
          <div role="group" aria-label="Быстрый выбор длительности" className="flex flex-wrap gap-1.5">
            {DURATION_PRESETS.map((m) => {
              const active = values.duration.trim() === String(m)
              return (
                <button
                  key={m}
                  type="button"
                  aria-pressed={active}
                  disabled={disabled}
                  onClick={() => onChange({ duration: String(m) })}
                  className={cn(
                    'inline-flex h-9 items-center rounded-full border px-3 text-sm font-medium transition-colors outline-none',
                    'focus-visible:ring-3 focus-visible:ring-ring/50 disabled:opacity-50',
                    active
                      ? 'border-primary bg-primary text-primary-foreground'
                      : 'border-border bg-background hover:bg-muted',
                  )}
                >
                  {m} мин
                </button>
              )
            })}
          </div>
        </div>
      </Field>
    </div>
  )
}

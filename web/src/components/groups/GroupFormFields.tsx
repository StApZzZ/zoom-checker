import { useId } from 'react'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  MAX_DURATION_MINUTES,
  MIN_DURATION_MINUTES,
  type GroupFormErrors,
  type GroupFormValues,
} from '@/components/groups/group-form'

export interface GroupFormFieldsProps {
  values: GroupFormValues
  onChange: (values: GroupFormValues) => void
  errors?: GroupFormErrors
  disabled?: boolean
  autoFocus?: boolean
}

function FieldError({ id, children }: { id: string; children?: string }) {
  if (!children) return null
  return (
    <p id={id} className="text-sm text-destructive">
      {children}
    </p>
  )
}

/** Name, description and default lesson duration of a group. */
export function GroupFormFields({ values, onChange, errors = {}, disabled, autoFocus }: GroupFormFieldsProps) {
  const id = useId()
  const set = (patch: Partial<GroupFormValues>) => onChange({ ...values, ...patch })
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-name`}>Название группы</Label>
        <Input
          id={`${id}-name`}
          value={values.name}
          onChange={(e) => set({ name: e.target.value })}
          placeholder="Например, DevOps-2026-01"
          autoComplete="off"
          autoFocus={autoFocus}
          disabled={disabled}
          aria-invalid={errors.name ? true : undefined}
          aria-describedby={errors.name ? `${id}-name-error` : undefined}
          className="h-10"
        />
        <FieldError id={`${id}-name-error`}>{errors.name}</FieldError>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-description`}>
          Описание <span className="font-normal text-muted-foreground">(необязательно)</span>
        </Label>
        <Textarea
          id={`${id}-description`}
          value={values.description}
          onChange={(e) => set({ description: e.target.value })}
          placeholder="Например, курс «Linux для начинающих», осенний поток"
          rows={2}
          disabled={disabled}
          aria-invalid={errors.description ? true : undefined}
          aria-describedby={errors.description ? `${id}-description-error` : undefined}
        />
        <FieldError id={`${id}-description-error`}>{errors.description}</FieldError>
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-duration`}>Обычная длительность занятия, минут</Label>
        <Input
          id={`${id}-duration`}
          type="number"
          inputMode="numeric"
          min={MIN_DURATION_MINUTES}
          max={MAX_DURATION_MINUTES}
          step={1}
          value={values.duration}
          onChange={(e) => set({ duration: e.target.value })}
          disabled={disabled}
          aria-invalid={errors.duration ? true : undefined}
          aria-describedby={`${id}-duration-hint${errors.duration ? ` ${id}-duration-error` : ''}`}
          className="h-10 w-32"
        />
        <p id={`${id}-duration-hint`} className="text-xs text-muted-foreground">
          Когда вы создаёте занятие, время окончания подставится само.
        </p>
        <FieldError id={`${id}-duration-error`}>{errors.duration}</FieldError>
      </div>
    </div>
  )
}

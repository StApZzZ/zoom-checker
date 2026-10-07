// Values and validation of the group form (create / edit dialogs).

import type { CreateGroupInput, GroupSummary } from '@/lib/types'

// Same limits as the server (internal/httpapi/routes_groups.go): checking them
// here keeps the message next to the field instead of a generic error box.
export const DEFAULT_DURATION_MINUTES = 90
export const MIN_DURATION_MINUTES = 15
export const MAX_DURATION_MINUTES = 600
export const MAX_GROUP_NAME_LENGTH = 100
export const MAX_DESCRIPTION_LENGTH = 2000

/** Length in characters as the server counts it: code points of the trimmed text. */
function charCount(value: string, collapseSpaces = false): number {
  const text = value.trim()
  return Array.from(collapseSpaces ? text.replace(/\s+/g, ' ') : text).length
}

export interface GroupFormValues {
  name: string
  description: string
  /** Raw text of the number input. */
  duration: string
}

export type GroupFormErrors = Partial<Record<keyof GroupFormValues, string>>

export function emptyGroupForm(): GroupFormValues {
  return { name: '', description: '', duration: String(DEFAULT_DURATION_MINUTES) }
}

export function groupFormFrom(group: Pick<GroupSummary, 'name' | 'description' | 'default_duration_minutes'>): GroupFormValues {
  return {
    name: group.name,
    description: group.description,
    duration: String(group.default_duration_minutes || DEFAULT_DURATION_MINUTES),
  }
}

/** Russian problems per field; empty object when the form is valid. */
export function validateGroupForm(values: GroupFormValues): GroupFormErrors {
  const errors: GroupFormErrors = {}
  const name = values.name.trim()
  if (!name) errors.name = 'Введите название группы.'
  else if (charCount(name, true) > MAX_GROUP_NAME_LENGTH) errors.name = `Название слишком длинное (больше ${MAX_GROUP_NAME_LENGTH} символов).`
  if (charCount(values.description) > MAX_DESCRIPTION_LENGTH)
    errors.description = `Описание слишком длинное (больше ${MAX_DESCRIPTION_LENGTH} символов).`
  const raw = values.duration.trim()
  const minutes = Number(raw)
  if (!raw) errors.duration = 'Укажите длительность занятия в минутах.'
  else if (!Number.isInteger(minutes) || minutes < MIN_DURATION_MINUTES || minutes > MAX_DURATION_MINUTES)
    errors.duration = `Длительность — целое число минут от ${MIN_DURATION_MINUTES} до ${MAX_DURATION_MINUTES}.`
  return errors
}

export function hasErrors(errors: GroupFormErrors): boolean {
  return Object.keys(errors).length > 0
}

/** Request body for a valid form. */
export function groupInput(values: GroupFormValues): Required<CreateGroupInput> {
  return {
    name: values.name.trim(),
    description: values.description.trim(),
    default_duration_minutes: Number(values.duration.trim()),
  }
}

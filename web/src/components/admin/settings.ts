// Form model and validation for the admin "Настройки" page (docs/architecture.md §6 Settings).

import type { Settings } from '@/lib/types'

// Same bounds as the server (internal/httpapi/admin_settings.go).
export const MIN_LINK_WINDOW_HOURS = 1
export const MAX_LINK_WINDOW_HOURS = 72
export const MAX_IGNORED_NAME_LENGTH = 200

/** Quick-add examples for "Имена, которые не считаются студентами". */
export const IGNORED_NAME_EXAMPLES = ['Преподаватель', 'Zoom Recorder', 'Otter.ai']

/** Form state: numbers are kept as typed so an empty field can be shown and validated. */
export interface SettingsDraft {
  ignored_names: string[]
  present_min_percent: string
  link_window_hours: string
  ignore_host: boolean
}

export type SettingsErrors = Partial<Record<'present_min_percent' | 'link_window_hours', string>>

export function toDraft(s: Settings): SettingsDraft {
  return {
    ignored_names: [...s.ignored_names],
    present_min_percent: String(s.present_min_percent),
    link_window_hours: String(s.link_window_hours),
    ignore_host: s.ignore_host,
  }
}

/**
 * Same comparison key the server uses for names (§5.1, without NFKC corner
 * cases): lower case, ё = е, whitespace collapsed. Used to skip duplicates.
 */
export function nameKey(name: string): string {
  return name
    .normalize('NFKC')
    .replace(/[​-‍⁠﻿­‎‏]/g, '')
    .toLocaleLowerCase('ru')
    .replaceAll('ё', 'е')
    .replace(/\s+/g, ' ')
    .trim()
}

/** Adds names (trimmed, inner spaces collapsed) that are not in the list yet; keeps the order. */
export function addNames(list: readonly string[], input: readonly string[]): string[] {
  const seen = new Set(list.map(nameKey))
  const out = [...list]
  for (const raw of input) {
    const name = raw.replace(/\s+/g, ' ').trim()
    const key = nameKey(name)
    if (!key || seen.has(key)) continue
    seen.add(key)
    out.push(name)
  }
  return out
}

/** Splits pasted text into names: one per line, or separated by commas / semicolons. */
export function splitNames(text: string): string[] {
  return text
    .split(/[\n\r;,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

function parseWholeNumber(raw: string): number | null {
  const t = raw.trim()
  if (!/^\d+$/.test(t)) return null
  return Number(t)
}

/** Validates the draft; `value` is the payload for PUT /api/settings when there are no errors. */
export function validateSettings(d: SettingsDraft): { errors: SettingsErrors; value: Settings | null } {
  const errors: SettingsErrors = {}
  const percent = parseWholeNumber(d.present_min_percent)
  if (percent === null || percent > 100) errors.present_min_percent = 'Введите целое число от 0 до 100.'
  const hours = parseWholeNumber(d.link_window_hours)
  if (hours === null || hours < MIN_LINK_WINDOW_HOURS || hours > MAX_LINK_WINDOW_HOURS)
    errors.link_window_hours = `Введите целое число часов от ${MIN_LINK_WINDOW_HOURS} до ${MAX_LINK_WINDOW_HOURS}.`
  if (errors.present_min_percent || errors.link_window_hours) return { errors, value: null }
  return {
    errors,
    value: {
      ignored_names: d.ignored_names,
      present_min_percent: percent!,
      link_window_hours: hours!,
      ignore_host: d.ignore_host,
    },
  }
}

/**
 * Moves the admin's unsaved edits onto newer server settings (three-way merge).
 * Settings change behind the form's back: "Всегда скрывать это имя" on a lesson
 * screen adds to ignored_names, another admin may save too. Fields the admin
 * did not touch take the newer value; touched ones keep the edit; names keep
 * the admin's additions and removals on top of the newer list. Without this a
 * save would silently drop names added in the meantime.
 */
export function rebaseDraft(draft: SettingsDraft, base: Settings, latest: Settings): SettingsDraft {
  const b = toDraft(base)
  const l = toDraft(latest)
  const baseKeys = new Set(b.ignored_names.map(nameKey))
  const draftKeys = new Set(draft.ignored_names.map(nameKey))
  const removed = new Set([...baseKeys].filter((k) => !draftKeys.has(k)))
  const added = draft.ignored_names.filter((n) => !baseKeys.has(nameKey(n)))
  return {
    ignored_names: addNames(
      l.ignored_names.filter((n) => !removed.has(nameKey(n))),
      added,
    ),
    present_min_percent:
      draft.present_min_percent.trim() !== b.present_min_percent ? draft.present_min_percent : l.present_min_percent,
    link_window_hours:
      draft.link_window_hours.trim() !== b.link_window_hours ? draft.link_window_hours : l.link_window_hours,
    ignore_host: draft.ignore_host !== b.ignore_host ? draft.ignore_host : l.ignore_host,
  }
}

/** True when the draft differs from the saved settings (numbers compared as typed, trimmed). */
export function isDirty(d: SettingsDraft, saved: Settings): boolean {
  const base = toDraft(saved)
  return (
    d.ignore_host !== base.ignore_host ||
    d.present_min_percent.trim() !== base.present_min_percent ||
    d.link_window_hours.trim() !== base.link_window_hours ||
    d.ignored_names.length !== base.ignored_names.length ||
    d.ignored_names.some((n, i) => n !== base.ignored_names[i])
  )
}

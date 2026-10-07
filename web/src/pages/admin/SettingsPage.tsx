import { useId, useState, type FormEvent } from 'react'
import { Info, Loader2, RotateCcw, Save, SlidersHorizontal } from 'lucide-react'

import { describedBy } from '@/components/admin/form'
import { FormField } from '@/components/admin/FormField'
import { LdapCheckCard } from '@/components/admin/LdapCheckCard'
import {
  IGNORED_NAME_EXAMPLES,
  isDirty,
  MAX_LINK_WINDOW_HOURS,
  MIN_LINK_WINDOW_HOURS,
  rebaseDraft,
  toDraft,
  validateSettings,
  type SettingsDraft,
  type SettingsErrors,
} from '@/components/admin/settings'
import { SystemInfoCard } from '@/components/admin/SystemInfoCard'
import { TagListEditor } from '@/components/admin/TagListEditor'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { LoadingState } from '@/components/LoadingState'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { useSettings, useUpdateSettings } from '@/lib/queries'
import type { Settings } from '@/lib/types'

export default function SettingsPage() {
  const settings = useSettings()
  return (
    <Page width="narrow">
      <PageHeader
        title="Настройки"
        description="Правила учёта посещаемости. Действуют для всех групп и занятий."
      />
      {settings.data ? (
        <RulesForm saved={settings.data} />
      ) : settings.isError ? (
        <ErrorState error={settings.error} onRetry={() => settings.refetch()} retrying={settings.isFetching} />
      ) : (
        <LoadingState variant="list" rows={8} />
      )}
      <SystemInfoCard />
      <LdapCheckCard />
    </Page>
  )
}

function RulesForm({ saved }: { saved: Settings }) {
  const id = useId()
  const update = useUpdateSettings({
    successMessage: 'Настройки сохранены. Посещаемость недавних занятий пересчитывается.',
  })
  // `base` is the server version the draft was started from.
  const [base, setBase] = useState<Settings>(saved)
  const [draft, setDraft] = useState<SettingsDraft>(() => toDraft(saved))
  const [errors, setErrors] = useState<SettingsErrors>({})

  // Newer settings arrived (refetch on focus, a name hidden from a lesson screen,
  // another admin): show them, keeping this admin's unsaved edits on top, so a
  // later save does not undo the other change. Query results keep their identity
  // while the content is unchanged, so this runs only on a real change.
  if (saved !== base) {
    setDraft(rebaseDraft(draft, base, saved))
    setBase(saved)
  }

  const set = <K extends keyof SettingsDraft>(key: K, value: SettingsDraft[K]) => {
    setDraft((d) => ({ ...d, [key]: value }))
    if (key in errors) setErrors((e) => ({ ...e, [key]: undefined }))
  }

  const dirty = isDirty(draft, base)

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const { errors: problems, value } = validateSettings(draft)
    setErrors(problems)
    if (!value) {
      const first = problems.present_min_percent ? `${id}-percent` : `${id}-window`
      document.getElementById(first)?.focus()
      return
    }
    update.mutate(value, {
      onSuccess: (data) => {
        const next = data ?? value
        setBase(next)
        setDraft(toDraft(next))
      },
    })
  }

  const f = {
    names: `${id}-names`,
    percent: `${id}-percent`,
    window: `${id}-window`,
    host: `${id}-host`,
  }
  const percentHint =
    'Например, 50 — студент должен пробыть в Zoom хотя бы половину занятия. 0 — достаточно одного подключения.'
  const windowHint =
    'Конференция Zoom с тем же номером привязывается к занятию, если началась не раньше чем за столько часов до его начала (и не позже чем через 30 минут после окончания). Обычно хватает 1.'

  return (
    <form onSubmit={onSubmit} noValidate>
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-lg">
            <SlidersHorizontal className="size-5 text-muted-foreground" aria-hidden="true" />
            Правила учёта
          </CardTitle>
          <CardDescription>Изменения применяются к будущим занятиям и к занятиям за последние два месяца.</CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-6">
          <FormField
            id={f.names}
            label="Имена, которые не считаются студентами"
            hint="Участники Zoom с такими именами не попадут в «Не идентифицированы» и не будут учитываться. Например, ассистенты, запись конференции или бот-стенографист. Регистр букв не важен."
          >
            <TagListEditor
              id={f.names}
              value={draft.ignored_names}
              onChange={(v) => set('ignored_names', v)}
              suggestions={IGNORED_NAME_EXAMPLES}
              placeholder="Введите имя и нажмите Enter"
              describedBy={describedBy(f.names, true)}
            />
          </FormField>

          <FormField
            id={f.percent}
            label="Засчитывать присутствие, если студент был не меньше … % занятия"
            hint={percentHint}
            error={errors.present_min_percent}
          >
            <div className="flex items-center gap-2">
              <Input
                id={f.percent}
                type="number"
                inputMode="numeric"
                min={0}
                max={100}
                step={1}
                value={draft.present_min_percent}
                onChange={(e) => set('present_min_percent', e.target.value)}
                aria-invalid={!!errors.present_min_percent || undefined}
                aria-describedby={describedBy(f.percent, true, errors.present_min_percent)}
                className="h-9 w-28"
              />
              <span className="text-sm text-muted-foreground" aria-hidden="true">
                %
              </span>
            </div>
          </FormField>

          <FormField
            id={f.window}
            label="Окно привязки конференции к занятию, часов"
            hint={windowHint}
            error={errors.link_window_hours}
          >
            <Input
              id={f.window}
              type="number"
              inputMode="numeric"
              min={MIN_LINK_WINDOW_HOURS}
              max={MAX_LINK_WINDOW_HOURS}
              step={1}
              value={draft.link_window_hours}
              onChange={(e) => set('link_window_hours', e.target.value)}
              aria-invalid={!!errors.link_window_hours || undefined}
              aria-describedby={describedBy(f.window, true, errors.link_window_hours)}
              className="h-9 w-28"
            />
          </FormField>

          <div className="flex items-start justify-between gap-4 rounded-lg p-3 ring-1 ring-foreground/10">
            <div className="flex flex-col gap-1">
              <Label htmlFor={f.host} className="leading-snug">
                Не учитывать организатора конференции
              </Label>
              <p id={`${f.host}-hint`} className="text-sm text-muted-foreground">
                Тот, кто запустил конференцию в Zoom (обычно преподаватель), не попадёт в «Не идентифицированы» и не
                будет учитываться.
              </p>
            </div>
            <Switch
              id={f.host}
              checked={draft.ignore_host}
              onCheckedChange={(v) => set('ignore_host', v)}
              aria-describedby={`${f.host}-hint`}
              className="mt-1"
            />
          </div>
        </CardContent>
        <CardFooter className="flex flex-col items-stretch gap-3 sm:flex-row sm:items-center sm:justify-between">
          <p className="flex items-start gap-2 text-sm text-muted-foreground">
            <Info className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
            После сохранения посещаемость занятий за последние два месяца пересчитается. Ручные отметки преподавателей
            не меняются.
          </p>
          <div className="flex shrink-0 gap-2">
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setDraft(toDraft(base))
                setErrors({})
              }}
              disabled={!dirty || update.isPending}
            >
              <RotateCcw aria-hidden="true" />
              Отменить
            </Button>
            <Button type="submit" size="lg" disabled={!dirty || update.isPending}>
              {update.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Save aria-hidden="true" />}
              Сохранить
            </Button>
          </div>
        </CardFooter>
      </Card>
    </form>
  )
}

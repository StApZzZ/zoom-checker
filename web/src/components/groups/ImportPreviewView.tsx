import { useId, useState } from 'react'
import { ArchiveRestore, CircleAlert, Eraser, FileText, Info, Loader2, PencilLine, TriangleAlert } from 'lucide-react'

import { StatusBadge, ToneBadge } from '@/components/StatusBadge'
import { importRowStatusMeta, type StatusMeta } from '@/components/status-meta'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import {
  detectedSummary,
  duplicateGroups,
  duplicateGroupText,
  DUPLICATE_NAMES_MESSAGE,
  isImportBlocked,
  isProblemRow,
  isRenameRow,
  isRestoreRow,
  joinLines,
  previewCountItems,
  previewNameFormat,
  removeDuplicateLines,
  rowsLabel,
} from '@/components/groups/import-utils'
import type { ImportSourceInfo } from '@/components/groups/useImportFlow'
import { cn } from '@/lib/utils'
import type { ImportNameFormat, ImportPreview } from '@/lib/types'

export interface ImportPreviewViewProps {
  preview: ImportPreview
  sourceInfo: ImportSourceInfo | null
  onNameFormatChange: (format: ImportNameFormat) => void
  /** A new preview is being loaded (name format switch). */
  updating?: boolean
  commitError?: string | null
  previewError?: string | null
  /**
   * Pasted lists only: replaces the pasted text (and re-checks it). Enables «Убрать повтор»,
   * which deletes the repeated lines of one name; a file has to be fixed by the teacher.
   */
  onPastedTextChange?: (text: string) => void
  className?: string
}

const MAX_LISTED_LINES = 10
/** Repeated names listed one by one in the summary; the rest are in the table. */
const MAX_LISTED_GROUPS = 5

/** A new row that brings back an archived student (not a row status of its own). */
const restoreMeta: StatusMeta = {
  label: 'Из архива',
  description: 'Студент был в архиве группы и вернётся в список, история посещений сохранится',
  tone: 'info',
  icon: ArchiveRestore,
}

/** An existing student whose stored name the import changes (not a row status of its own). */
const renameMeta: StatusMeta = {
  label: 'Переименование',
  description: 'Студент уже в группе; его имя в сервисе изменится на имя из списка',
  tone: 'warning',
  icon: PencilLine,
}

const chipMeta = (status: ReturnType<typeof previewCountItems>[number]['status']): StatusMeta =>
  status === 'restore' ? restoreMeta : status === 'rename' ? renameMeta : importRowStatusMeta[status]

/** Step 2 of the import: what will happen to every row, with blocking problems explained. */
export function ImportPreviewView({
  preview,
  sourceInfo,
  onNameFormatChange,
  updating = false,
  commitError,
  previewError,
  onPastedTextChange,
  className,
}: ImportPreviewViewProps) {
  const id = useId()
  const [onlyProblems, setOnlyProblems] = useState(false)
  const blocked = isImportBlocked(preview)
  const format = previewNameFormat(preview)
  const offerShortNames = preview.has_patronymics || format === 'last_first'
  const fileRowCount = preview.rows.filter((r) => r.status !== 'archive').length
  const problemLines = preview.rows.filter(isProblemRow).map((r) => r.line)
  const rows = onlyProblems && blocked ? preview.rows.filter(isProblemRow) : preview.rows
  const showIds = preview.rows.some((r) => r.external_id)
  const detected = detectedSummary(preview.detected)
  const fromFile = sourceInfo?.kind === 'file'
  const pastedText = sourceInfo && 'text' in sourceInfo.source ? sourceInfo.source.text : null
  const duplicates = duplicateGroups(preview)
  const example = duplicates[0]?.name ?? 'Иванов Иван'

  return (
    <div className={cn('flex flex-col gap-4', className)}>
      <div className="flex flex-col gap-1">
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <FileText className="size-4 text-muted-foreground" aria-hidden="true" />
          <span className="font-medium">{fromFile ? `Файл «${sourceInfo?.label}»` : 'Вставленный список'}</span>
          <span className="text-muted-foreground">· {rowsLabel(fileRowCount)}</span>
        </p>
        {detected && <p className="text-xs text-muted-foreground">{detected}</p>}
      </div>

      <ul aria-label="Итоги проверки" className="flex flex-wrap gap-2">
        {previewCountItems(preview)
          .filter((item) => item.value > 0)
          .map((item) => {
            const meta = chipMeta(item.status)
            return (
              <li key={item.status}>
                <ToneBadge tone={meta.tone} icon={meta.icon} size="lg" title={meta.description}>
                  {item.label}: {item.value}
                </ToneBadge>
              </li>
            )
          })}
      </ul>

      {offerShortNames && (
        <Alert>
          <Info aria-hidden="true" />
          <AlertTitle>В списке есть отчества</AlertTitle>
          <AlertDescription className="flex flex-col gap-3">
            <p>
              В Zoom студенты обычно подписываются фамилией и именем, без отчества. Сервис узнаёт студента только по
              точному совпадению имени, поэтому лучше хранить имена так же, как в Zoom.
            </p>
            <div className="flex items-center gap-3">
              <Switch
                id={`${id}-short`}
                checked={format === 'last_first'}
                onCheckedChange={(checked) => onNameFormatChange(checked ? 'last_first' : 'as_is')}
                disabled={updating}
              />
              <Label htmlFor={`${id}-short`} className="font-medium text-foreground">
                Оставить только Фамилию и Имя
              </Label>
              {updating && <Loader2 className="size-4 animate-spin text-muted-foreground" aria-label="Обновляем список" />}
            </div>
          </AlertDescription>
        </Alert>
      )}

      {blocked && (
        <Alert variant="destructive" data-testid="import-blocked">
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>{fromFile ? 'Исправьте файл и загрузите снова' : 'Исправьте список и проверьте снова'}</AlertTitle>
          <AlertDescription className="flex flex-col gap-2">
            {preview.counts.duplicate > 0 && (
              <>
                <p className="font-medium">{DUPLICATE_NAMES_MESSAGE}</p>
                {duplicates.length > 0 && (
                  <ul aria-label="Повторяющиеся имена" className="flex flex-col gap-1.5">
                    {duplicates.slice(0, MAX_LISTED_GROUPS).map((group) => {
                      const fixed = pastedText !== null && onPastedTextChange ? removeDuplicateLines(pastedText, group) : null
                      const extra = group.lines.slice(1)
                      return (
                        <li key={group.normalized} className="flex flex-wrap items-center gap-x-3 gap-y-1">
                          <span>
                            {duplicateGroupText(group)}
                            {!group.sameId && ' (у строк разные ID — это разные студенты)'}
                          </span>
                          {fixed !== null && (
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              className="text-foreground"
                              disabled={updating}
                              onClick={() => onPastedTextChange?.(fixed)}
                              aria-label={`Убрать повтор «${group.name}»: удалить ${extra.length > 1 ? 'строки' : 'строку'} ${joinLines(extra)}`}
                            >
                              <Eraser aria-hidden="true" />
                              Убрать повтор
                            </Button>
                          )}
                        </li>
                      )
                    })}
                    {duplicates.length > MAX_LISTED_GROUPS && (
                      <li>…и ещё {duplicates.length - MAX_LISTED_GROUPS} — см. таблицу ниже.</li>
                    )}
                  </ul>
                )}
                <p>
                  Если это один и тот же студент — удалите лишнюю строку
                  {fromFile ? ' в файле и загрузите его снова' : ''}. Если это разные люди — добавьте уточнение, например
                  «{example} А.» и «{example} Б.», и попросите студентов подписаться в Zoom так же.
                </p>
              </>
            )}
            {preview.counts.error > 0 && <p>Некоторые строки не удалось разобрать — пояснения есть в таблице.</p>}
            <p>
              Сохранить такой список нельзя. Строки с проблемами:{' '}
              {problemLines.slice(0, MAX_LISTED_LINES).join(', ')}
              {problemLines.length > MAX_LISTED_LINES ? ` и ещё ${problemLines.length - MAX_LISTED_LINES}` : ''}.
            </p>
          </AlertDescription>
        </Alert>
      )}

      {(commitError || previewError) && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{commitError ?? previewError}</AlertDescription>
        </Alert>
      )}

      {blocked && preview.rows.length > MAX_LISTED_LINES && (
        <div className="flex items-center gap-3">
          <Switch id={`${id}-problems`} checked={onlyProblems} onCheckedChange={setOnlyProblems} />
          <Label htmlFor={`${id}-problems`} className="font-normal">
            Показать только строки с проблемами
          </Label>
        </div>
      )}

      <div
        className={cn(
          'relative max-h-[28rem] overflow-auto rounded-xl ring-1 ring-foreground/10 transition-opacity',
          updating && 'opacity-60',
        )}
        aria-busy={updating || undefined}
      >
        <table className="w-full text-sm">
          <caption className="sr-only">Результат проверки списка</caption>
          <thead className="sticky top-0 z-10 bg-muted/90 backdrop-blur">
            <tr className="border-b text-left">
              <th scope="col" className="w-16 px-3 py-2 font-medium whitespace-nowrap">
                Строка
              </th>
              {showIds && (
                <th scope="col" className="px-3 py-2 font-medium">
                  ID
                </th>
              )}
              <th scope="col" className="px-3 py-2 font-medium">
                ФИО
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Что будет
              </th>
              <th scope="col" className="px-3 py-2 font-medium">
                Пояснение
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row, index) => {
              const problem = isProblemRow(row)
              const rename = isRenameRow(row)
              return (
                <tr
                  key={`${row.status}-${row.line}-${index}`}
                  data-problem={problem || undefined}
                  className={cn(
                    'border-b align-top last:border-b-0',
                    problem && 'bg-red-50/70 dark:bg-red-950/25',
                    row.status === 'archive' && 'bg-amber-50/50 dark:bg-amber-950/15',
                  )}
                >
                  <td className="px-3 py-2 text-muted-foreground tabular-nums">{row.line > 0 ? row.line : '—'}</td>
                  {showIds && <td className="px-3 py-2 text-muted-foreground">{row.external_id || '—'}</td>}
                  <td className={cn('px-3 py-2 font-medium break-words', !row.full_name && 'text-muted-foreground italic')}>
                    {row.full_name || 'пусто'}
                  </td>
                  <td className="px-3 py-2">
                    {isRestoreRow(row) ? (
                      <ToneBadge tone={restoreMeta.tone} icon={restoreMeta.icon} title={restoreMeta.description}>
                        {restoreMeta.label}
                      </ToneBadge>
                    ) : rename ? (
                      <ToneBadge tone={renameMeta.tone} icon={renameMeta.icon} title={renameMeta.description}>
                        {renameMeta.label}
                      </ToneBadge>
                    ) : (
                      <StatusBadge kind="import" status={row.status} />
                    )}
                  </td>
                  <td
                    className={cn('min-w-48 px-3 py-2 text-pretty', problem || rename ? 'text-foreground' : 'text-muted-foreground')}
                  >
                    {row.message}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

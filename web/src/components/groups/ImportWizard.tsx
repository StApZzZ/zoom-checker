import { useState } from 'react'
import { ArrowLeft, Check, Loader2, Save } from 'lucide-react'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { ImportPreviewView } from '@/components/groups/ImportPreviewView'
import { ImportSourceStep } from '@/components/groups/ImportSourceStep'
import { canCommit, isImportBlocked } from '@/components/groups/import-utils'
import { useImportFlow } from '@/components/groups/useImportFlow'
import { plural, type PluralForms } from '@/lib/format'
import { cn } from '@/lib/utils'
import type { ImportResult } from '@/lib/types'

export interface ImportWizardProps {
  groupId: number
  /** The group already has active students (otherwise add/replace makes no difference). */
  hasStudents: boolean
  /** Called after the list was saved. */
  onDone?: (result: ImportResult) => void
}

const STEPS = ['Список', 'Проверка', 'Готово'] as const

/** "перенести 1 студента / 2 студентов / 5 студентов" (animate accusative). */
const STUDENT_ACCUSATIVE: PluralForms = ['студента', 'студентов', 'студентов']

function StepIndicator({ current }: { current: 0 | 1 | 2 }) {
  return (
    <ol className="flex flex-wrap items-center gap-2 text-sm" aria-label="Шаги загрузки">
      {STEPS.map((label, i) => {
        const done = i < current
        const active = i === current
        return (
          <li key={label} className="flex items-center gap-2" aria-current={active ? 'step' : undefined}>
            {i > 0 && <span aria-hidden="true" className="h-px w-6 bg-border" />}
            <span
              aria-hidden="true"
              className={cn(
                'flex size-6 items-center justify-center rounded-full text-xs font-semibold ring-1',
                active && 'bg-primary text-primary-foreground ring-primary',
                done && 'bg-emerald-600 text-white ring-emerald-600 dark:bg-emerald-500',
                !active && !done && 'text-muted-foreground ring-border',
              )}
            >
              {done ? <Check className="size-3.5" /> : i + 1}
            </span>
            <span className={cn(active ? 'font-medium text-foreground' : 'text-muted-foreground')}>
              {label}
              {done && <span className="sr-only"> (готово)</span>}
            </span>
          </li>
        )
      })}
    </ol>
  )
}

/** "Импорт списка": choose a file or paste text → check the preview → save. */
export function ImportWizard({ groupId, hasStudents, onDone }: ImportWizardProps) {
  const flow = useImportFlow(groupId)
  const [text, setText] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const { preview } = flow

  const save = async () => {
    const result = await flow.commit()
    if (!result) return
    setText('')
    flow.reset()
    onDone?.(result)
  }

  // «Убрать повтор» in a pasted list: the box gets the fixed text and the list is checked again.
  const replacePastedText = (next: string) => {
    setText(next)
    void flow.runPreview({ source: { text: next }, kind: 'text', label: 'Вставленный список' })
  }
  const fromFile = flow.sourceInfo?.kind === 'file'

  const archiveCount = preview?.mode === 'replace' ? preview.counts.archive : 0
  const onSaveClick = () => {
    if (archiveCount > 0) setConfirmOpen(true)
    else void save()
  }

  return (
    <div className="flex flex-col gap-6">
      <StepIndicator current={preview ? (flow.committing ? 2 : 1) : 0} />

      {!preview ? (
        <ImportSourceStep
          mode={flow.mode}
          onModeChange={flow.setMode}
          showMode={hasStudents}
          text={text}
          onTextChange={setText}
          onSubmitText={(value) =>
            void flow.runPreview({ source: { text: value }, kind: 'text', label: 'Вставленный список' })
          }
          onFile={(file) => void flow.runPreview({ source: { file }, kind: 'file', label: file.name })}
          busy={flow.previewing}
          error={flow.previewError}
        />
      ) : (
        <>
          <ImportPreviewView
            preview={preview}
            sourceInfo={flow.sourceInfo}
            onNameFormatChange={(f) => void flow.changeNameFormat(f)}
            updating={flow.previewing}
            commitError={flow.commitError}
            previewError={flow.previewError}
            onPastedTextChange={fromFile ? undefined : replacePastedText}
          />
          <div className="flex flex-col-reverse gap-2 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
            <Button type="button" variant="outline" size="lg" onClick={flow.backToSource} disabled={flow.committing}>
              <ArrowLeft aria-hidden="true" />
              {/* Back to the paste box (text kept) or to the file picker. */}
              {!isImportBlocked(preview) ? 'Назад' : fromFile ? 'Загрузить исправленный файл' : 'Исправить список'}
            </Button>
            <div className="flex flex-col gap-1 sm:items-end">
              <Button
                type="button"
                size="lg"
                onClick={onSaveClick}
                disabled={!canCommit(preview) || flow.committing || flow.previewing}
              >
                {flow.committing ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Save aria-hidden="true" />}
                Сохранить список
              </Button>
              {isImportBlocked(preview) && (
                <p className="text-xs text-muted-foreground">Сохранить можно, когда в списке не останется ошибок.</p>
              )}
            </div>
          </div>
        </>
      )}

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={`Перенести в архив ${archiveCount} ${plural(archiveCount, STUDENT_ACCUSATIVE)}?`}
        description="Этих студентов нет в новом списке. Они не будут удалены: их посещаемость сохранится, а вернуть студента можно в любой момент на вкладке «Студенты»."
        confirmLabel="Сохранить и перенести в архив"
        // save() never throws: errors are shown next to the preview once the dialog closes.
        onConfirm={save}
      />
    </div>
  )
}

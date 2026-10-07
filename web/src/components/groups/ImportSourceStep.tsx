import { useId, useRef, useState, type DragEvent, type FormEvent } from 'react'
import { CircleAlert, FileUp, Loader2, ScanSearch } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'
import { fileProblem, IMPORT_FILE_ACCEPT, PASTE_PLACEHOLDER } from '@/components/groups/import-utils'
import { cn } from '@/lib/utils'
import type { ImportMode } from '@/lib/types'

export interface ImportSourceStepProps {
  mode: ImportMode
  onModeChange: (mode: ImportMode) => void
  /** Hide the add/replace choice (e.g. the group has no students yet). */
  showMode?: boolean
  text: string
  onTextChange: (text: string) => void
  onSubmitText: (text: string) => void
  onFile: (file: File) => void
  busy?: boolean
  /** Server error of the last preview attempt. */
  error?: string | null
}

const MODES: { value: ImportMode; title: string; text: string }[] = [
  {
    value: 'add',
    title: 'Добавить к списку',
    text: 'Новые студенты добавятся в группу, все остальные останутся.',
  },
  {
    value: 'replace',
    title: 'Заменить список',
    text: 'Список станет полным составом группы. Студенты, которых в нём нет, будут перенесены в архив — не удалены: их посещаемость сохранится, и их можно вернуть.',
  },
]

/** Step 1 of the import: drag & drop / pick a file, or paste the list. */
export function ImportSourceStep({
  mode,
  onModeChange,
  showMode = true,
  text,
  onTextChange,
  onSubmitText,
  onFile,
  busy = false,
  error,
}: ImportSourceStepProps) {
  const id = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)
  const [localError, setLocalError] = useState<string | null>(null)

  const takeFile = (file: File | undefined) => {
    if (!file || busy) return
    const problem = fileProblem(file)
    setLocalError(problem)
    if (!problem) onFile(file)
  }

  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    setDragging(false)
    takeFile(e.dataTransfer.files?.[0])
  }

  const onDragOver = (e: DragEvent) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
    if (!dragging) setDragging(true)
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    if (!text.trim() || busy) return
    setLocalError(null)
    onSubmitText(text)
  }

  const shownError = localError ?? error ?? null

  return (
    <div className="flex flex-col gap-6">
      {showMode && (
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-2 text-sm font-medium">Что сделать со списком</legend>
          <RadioGroup
            value={mode}
            onValueChange={(v) => onModeChange(v as ImportMode)}
            className="grid gap-3 md:grid-cols-2"
            disabled={busy}
          >
            {MODES.map((m) => (
              <Label
                key={m.value}
                htmlFor={`${id}-${m.value}`}
                className={cn(
                  'flex cursor-pointer items-start gap-3 rounded-xl p-4 font-normal ring-1 ring-foreground/10 transition-colors hover:bg-muted/50',
                  mode === m.value && 'bg-muted/60 ring-2 ring-primary/60',
                )}
              >
                <RadioGroupItem id={`${id}-${m.value}`} value={m.value} className="mt-0.5" aria-describedby={`${id}-${m.value}-text`} />
                <span className="flex flex-col gap-1">
                  <span className="font-medium">{m.title}</span>
                  <span id={`${id}-${m.value}-text`} className="text-sm leading-snug text-muted-foreground">
                    {m.text}
                  </span>
                </span>
              </Label>
            ))}
          </RadioGroup>
        </fieldset>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        <section aria-labelledby={`${id}-file-title`} className="flex flex-col gap-2">
          <h3 id={`${id}-file-title`} className="text-sm font-medium">
            Загрузите файл
          </h3>
          <div
            onDragOver={onDragOver}
            onDragEnter={onDragOver}
            onDragLeave={() => setDragging(false)}
            onDrop={onDrop}
            data-dragging={dragging || undefined}
            className={cn(
              'flex min-h-48 flex-1 flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-8 text-center transition-colors',
              dragging ? 'border-primary bg-primary/5' : 'border-border',
            )}
          >
            <div className="flex size-12 items-center justify-center rounded-full bg-muted text-muted-foreground">
              <FileUp className="size-6" aria-hidden="true" />
            </div>
            <p className="font-medium">{dragging ? 'Отпустите файл здесь' : 'Перетащите файл сюда'}</p>
            <p className="text-sm text-muted-foreground">или</p>
            <Button type="button" variant="outline" size="lg" onClick={() => inputRef.current?.click()} disabled={busy}>
              Выбрать файл
            </Button>
            <p className="max-w-xs text-xs text-pretty text-muted-foreground">
              CSV, Excel (.xlsx) или TXT, до 10 МБ. Нужна колонка с ФИО, колонка с ID — по желанию.
            </p>
            <input
              ref={inputRef}
              type="file"
              accept={IMPORT_FILE_ACCEPT}
              className="sr-only"
              tabIndex={-1}
              aria-label="Файл со списком студентов"
              onChange={(e) => {
                takeFile(e.target.files?.[0])
                // Allow choosing the same (fixed) file again.
                e.target.value = ''
              }}
            />
          </div>
        </section>

        <form onSubmit={onSubmit} className="flex flex-col gap-2">
          <Label htmlFor={`${id}-paste`} className="text-sm font-medium">
            Или вставьте список (по одному студенту в строке)
          </Label>
          <Textarea
            id={`${id}-paste`}
            value={text}
            onChange={(e) => onTextChange(e.target.value)}
            placeholder={PASTE_PLACEHOLDER}
            rows={8}
            className="min-h-48 flex-1 font-mono text-sm"
            aria-describedby={`${id}-paste-hint`}
            disabled={busy}
          />
          <p id={`${id}-paste-hint`} className="text-xs text-muted-foreground">
            Можно скопировать столбцы из Excel — вместе с колонкой ID, если она есть.
          </p>
          <Button type="submit" size="lg" className="self-start" disabled={busy || !text.trim()}>
            {busy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <ScanSearch aria-hidden="true" />}
            Проверить список
          </Button>
        </form>
      </div>

      {busy && (
        <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          Читаем список…
        </p>
      )}

      {shownError && !busy && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{shownError}</AlertDescription>
        </Alert>
      )}
    </div>
  )
}

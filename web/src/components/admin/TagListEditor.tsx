import { useState, type ClipboardEvent, type KeyboardEvent } from 'react'
import { Plus, X } from 'lucide-react'

import { addNames, MAX_IGNORED_NAME_LENGTH, nameKey, splitNames } from '@/components/admin/settings'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

const DUPLICATE = 'Это имя уже есть в списке.'

export interface TagListEditorProps {
  id: string
  value: string[]
  onChange: (value: string[]) => void
  /** Quick-add examples shown while they are not in the list. */
  suggestions?: string[]
  placeholder?: string
  describedBy?: string
  disabled?: boolean
}

/** Editable list of names shown as chips: type and press Enter, paste several lines, or pick an example. */
export function TagListEditor({
  id,
  value,
  onChange,
  suggestions = [],
  placeholder,
  describedBy,
  disabled,
}: TagListEditorProps) {
  const [text, setText] = useState('')
  const [notice, setNotice] = useState('')

  const add = (names: string[]) => {
    const next = addNames(value, names)
    const added = next.length - value.length
    if (added === 0 && names.some((n) => n.trim())) setNotice(DUPLICATE)
    else setNotice(added > 0 ? `Добавлено: ${next.slice(value.length).join(', ')}` : '')
    if (added > 0) onChange(next)
  }

  const commit = () => {
    if (!text.trim()) return
    add(splitNames(text))
    setText('')
  }

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter' || e.key === ',' || e.key === ';') {
      e.preventDefault()
      commit()
    }
  }

  const onPaste = (e: ClipboardEvent<HTMLInputElement>) => {
    const pasted = e.clipboardData.getData('text')
    if (!/[\n\r;,]/.test(pasted)) return
    e.preventDefault()
    add(splitNames(text + pasted))
    setText('')
  }

  const remove = (name: string) => {
    onChange(value.filter((n) => n !== name))
    setNotice(`Убрано: ${name}`)
  }

  const present = new Set(value.map(nameKey))
  const missingSuggestions = suggestions.filter((s) => !present.has(nameKey(s)))

  return (
    <div className="flex flex-col gap-3">
      {value.length > 0 ? (
        <ul className="flex flex-wrap gap-2" aria-label="Имена в списке">
          {value.map((name) => (
            <li
              key={name}
              className="inline-flex max-w-full items-center gap-1 rounded-full bg-muted py-1 pr-1 pl-3 text-sm ring-1 ring-foreground/10"
            >
              <span className="min-w-0 truncate" title={name}>
                {name}
              </span>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                className="rounded-full"
                onClick={() => remove(name)}
                aria-label={`Убрать «${name}»`}
                title="Убрать"
                disabled={disabled}
              >
                <X aria-hidden="true" />
              </Button>
            </li>
          ))}
        </ul>
      ) : (
        <p className="rounded-lg border border-dashed px-3 py-2 text-sm text-muted-foreground">
          Список пуст — все участники Zoom считаются возможными студентами.
        </p>
      )}

      <div className="flex gap-2">
        <Input
          id={id}
          value={text}
          onChange={(e) => {
            setText(e.target.value)
            if (notice) setNotice('')
          }}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          // A typed name the admin did not confirm with Enter would otherwise be
          // lost on "Сохранить" (which stays disabled while only text is typed).
          onBlur={commit}
          placeholder={placeholder}
          maxLength={MAX_IGNORED_NAME_LENGTH}
          aria-describedby={describedBy}
          disabled={disabled}
          className="h-9"
          autoComplete="off"
        />
        <Button type="button" variant="outline" className="h-9" onClick={commit} disabled={disabled || !text.trim()}>
          <Plus aria-hidden="true" />
          Добавить
        </Button>
      </div>

      {missingSuggestions.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">Например:</span>
          {missingSuggestions.map((s) => (
            <Button
              key={s}
              type="button"
              variant="secondary"
              size="sm"
              onClick={() => add([s])}
              disabled={disabled}
              aria-label={`Добавить «${s}»`}
            >
              <Plus aria-hidden="true" />
              {s}
            </Button>
          ))}
        </div>
      )}

      <p
        className={notice === DUPLICATE ? 'text-sm text-amber-700 dark:text-amber-300' : 'sr-only'}
        aria-live="polite"
      >
        {notice}
      </p>
    </div>
  )
}

import { useState } from 'react'
import { Eye, EyeOff, Wand2 } from 'lucide-react'

import { generatePassword } from '@/components/admin/password'
import { CopyButton } from '@/components/CopyButton'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

export interface PasswordFieldProps {
  id: string
  value: string
  onChange: (value: string) => void
  invalid?: boolean
  /** id of the hint / error element. */
  describedBy?: string
  disabled?: boolean
}

/** Password input with "show", "generate" and "copy" buttons (admin creates accounts for others). */
export function PasswordField({ id, value, onChange, invalid, describedBy, disabled }: PasswordFieldProps) {
  const [visible, setVisible] = useState(true)
  return (
    <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
      <div className="relative min-w-0 flex-1">
        <Input
          id={id}
          type={visible ? 'text' : 'password'}
          autoComplete="new-password"
          spellCheck={false}
          autoCapitalize="off"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-invalid={invalid || undefined}
          aria-describedby={describedBy}
          disabled={disabled}
          className="h-9 pr-10 font-mono"
        />
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          className="absolute top-1/2 right-1 -translate-y-1/2"
          onClick={() => setVisible((v) => !v)}
          aria-label={visible ? 'Скрыть пароль' : 'Показать пароль'}
          aria-pressed={visible}
          title={visible ? 'Скрыть пароль' : 'Показать пароль'}
          disabled={disabled}
        >
          {visible ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
        </Button>
      </div>
      <div className="flex gap-2">
        <Button
          type="button"
          variant="outline"
          className="h-9"
          onClick={() => {
            onChange(generatePassword())
            setVisible(true)
          }}
          disabled={disabled}
        >
          <Wand2 aria-hidden="true" />
          Придумать
        </Button>
        <CopyButton
          value={value}
          srLabel="Скопировать пароль"
          successMessage="Пароль скопирован"
          className="size-9"
          disabled={disabled || value === ''}
        />
      </div>
    </div>
  )
}

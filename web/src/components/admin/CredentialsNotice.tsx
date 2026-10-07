import { KeyRound } from 'lucide-react'

import { CopyButton } from '@/components/CopyButton'

export interface CredentialsNoticeProps {
  username: string
  password: string
  /** Server set must_change_password: the user picks a new password at first login. */
  mustChange: boolean
}

/** Login + one-time password with copy buttons, shown right after creating an account or resetting a password. */
export function CredentialsNotice({ username, password, mustChange }: CredentialsNoticeProps) {
  const both = `Адрес: ${window.location.origin}\nЛогин: ${username}\nПароль: ${password}`
  return (
    <div className="flex flex-col gap-3">
      <dl className="grid grid-cols-[auto_1fr_auto] items-center gap-x-3 gap-y-2 rounded-lg bg-muted/60 p-3">
        <dt className="text-sm text-muted-foreground">Логин</dt>
        <dd className="min-w-0 font-mono text-base break-all" data-testid="credentials-username">
          {username}
        </dd>
        <dd>
          <CopyButton value={username} srLabel="Скопировать логин" successMessage="Логин скопирован" size="icon-sm" />
        </dd>
        <dt className="text-sm text-muted-foreground">Пароль</dt>
        <dd className="min-w-0 font-mono text-base break-all" data-testid="credentials-password">
          {password}
        </dd>
        <dd>
          <CopyButton value={password} srLabel="Скопировать пароль" successMessage="Пароль скопирован" size="icon-sm" />
        </dd>
      </dl>
      <CopyButton value={both} label="Скопировать логин и пароль" successMessage="Логин и пароль скопированы" />
      <p className="flex gap-2 text-sm text-muted-foreground">
        <KeyRound className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
        <span>
          Пароль показан только сейчас — сохраните или сразу передайте его пользователю.{' '}
          {mustChange
            ? 'При первом входе система попросит придумать новый пароль.'
            : 'Пользователь сможет сменить пароль сам: меню пользователя → «Сменить пароль».'}
        </span>
      </p>
    </div>
  )
}

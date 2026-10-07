import { useId, useState, type FormEvent } from 'react'
import { CircleAlert, Loader2 } from 'lucide-react'

import { CredentialsNotice } from '@/components/admin/CredentialsNotice'
import { describedBy } from '@/components/admin/form'
import { FormField } from '@/components/admin/FormField'
import { generatePassword } from '@/components/admin/password'
import { PasswordField } from '@/components/admin/PasswordField'
import { serverErrorField, validateNewUser, type NewUserDraft, type NewUserErrors } from '@/components/admin/users'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { errorMessage } from '@/lib/api'
import { useCreateUser } from '@/lib/queries'
import type { Role, User } from '@/lib/types'

export interface CreateUserDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const roleOptions: { value: Role; title: string; text: string }[] = [
  {
    value: 'teacher',
    title: 'Преподаватель',
    text: 'Группы, занятия и посещаемость.',
  },
  {
    value: 'admin',
    title: 'Администратор',
    text: 'Всё то же, плюс пользователи, настройки и журнал событий Zoom.',
  },
]

interface Created {
  username: string
  name: string
  mustChange: boolean
  password: string
}

const emptyDraft = (): NewUserDraft => ({ username: '', display_name: '', password: generatePassword(), role: 'teacher' })

/** "Новый пользователь": local account form, then the one-time login + password. */
export function CreateUserDialog({ open, onOpenChange }: CreateUserDialogProps) {
  const id = useId()
  const mutation = useCreateUser({ errorToast: false })
  const [draft, setDraft] = useState<NewUserDraft>(emptyDraft)
  const [errors, setErrors] = useState<NewUserErrors>({})
  const [created, setCreated] = useState<Created | null>(null)
  const serverError = mutation.isError ? mutation.error : null
  const serverField = serverErrorField(serverError)

  const set = <K extends keyof NewUserDraft>(key: K, value: NewUserDraft[K]) => {
    setDraft((d) => ({ ...d, [key]: value }))
    if (errors[key]) setErrors((e) => ({ ...e, [key]: undefined }))
    // The server's complaint is about the old value.
    if (mutation.isError && serverField === key) mutation.reset()
  }

  const reset = () => {
    setDraft(emptyDraft())
    setErrors({})
    setCreated(null)
    mutation.reset()
  }

  const handleOpenChange = (value: boolean) => {
    if (mutation.isPending) return
    if (!value) reset()
    onOpenChange(value)
  }

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const problems = validateNewUser(draft)
    setErrors(problems)
    if (Object.values(problems).some(Boolean)) return
    const input = {
      username: draft.username.trim(),
      display_name: draft.display_name.trim(),
      password: draft.password,
      role: draft.role,
    }
    mutation.mutate(input, {
      onSuccess: (user?: User) =>
        setCreated({
          username: user?.username || input.username,
          name: user?.display_name || input.display_name,
          mustChange: user?.must_change_password ?? false,
          password: input.password,
        }),
    })
  }

  // Server problems with one field (taken login, bad password) are shown next to it.
  const fieldError = (key: keyof NewUserDraft) =>
    errors[key] ?? (serverError && serverField === key ? errorMessage(serverError) : undefined)
  const usernameError = fieldError('username')
  const nameError = fieldError('display_name')
  const passwordError = fieldError('password')
  const generalError =
    serverError && (serverField === null || serverField === 'role') ? errorMessage(serverError) : null

  const f = {
    username: `${id}-username`,
    name: `${id}-name`,
    password: `${id}-password`,
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-lg">
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>Пользователь создан</DialogTitle>
              <DialogDescription>
                {created.name} может входить в систему с этими данными.
              </DialogDescription>
            </DialogHeader>
            <CredentialsNotice username={created.username} password={created.password} mustChange={created.mustChange} />
            <DialogFooter>
              <Button type="button" onClick={() => handleOpenChange(false)}>
                Готово
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
            <DialogHeader>
              <DialogTitle>Новый пользователь</DialogTitle>
              <DialogDescription>
                Учётная запись с паролем в этой системе. Сотрудникам с корпоративной учётной записью (LDAP или Keycloak)
                создавать пользователя не нужно — они просто входят.
              </DialogDescription>
            </DialogHeader>

            <FormField id={f.username} label="Логин" hint="Латинскими буквами, без пробелов, например ivanova." error={usernameError}>
              <Input
                id={f.username}
                value={draft.username}
                onChange={(e) => set('username', e.target.value)}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                autoFocus
                aria-invalid={!!usernameError || undefined}
                aria-describedby={describedBy(f.username, true, usernameError)}
                className="h-9"
              />
            </FormField>

            <FormField id={f.name} label="Имя и фамилия" error={nameError}>
              <Input
                id={f.name}
                value={draft.display_name}
                onChange={(e) => set('display_name', e.target.value)}
                autoComplete="off"
                aria-invalid={!!nameError || undefined}
                aria-describedby={describedBy(f.name, false, nameError)}
                className="h-9"
              />
            </FormField>

            <fieldset className="flex flex-col gap-2">
              <legend className="mb-2 text-sm font-medium">Роль</legend>
              <RadioGroup value={draft.role} onValueChange={(v) => set('role', v as Role)} className="gap-2">
                {roleOptions.map((o) => (
                  <Label
                    key={o.value}
                    htmlFor={`${id}-role-${o.value}`}
                    className="flex cursor-pointer items-start gap-3 rounded-lg p-3 font-normal ring-1 ring-foreground/10 has-data-checked:bg-muted/60 has-data-checked:ring-foreground/25"
                  >
                    <RadioGroupItem id={`${id}-role-${o.value}`} value={o.value} className="mt-0.5" />
                    <span className="flex flex-col gap-1">
                      <span className="font-medium">{o.title}</span>
                      <span className="text-muted-foreground">{o.text}</span>
                    </span>
                  </Label>
                ))}
              </RadioGroup>
            </fieldset>

            <FormField
              id={f.password}
              label="Пароль"
              hint="Мы придумали надёжный пароль. Можно ввести свой — не короче 8 символов."
              error={passwordError}
            >
              <PasswordField
                id={f.password}
                value={draft.password}
                onChange={(v) => set('password', v)}
                invalid={!!passwordError}
                describedBy={describedBy(f.password, true, passwordError)}
              />
            </FormField>

            {generalError && (
              <Alert variant="destructive">
                <CircleAlert aria-hidden="true" />
                <AlertDescription>{generalError}</AlertDescription>
              </Alert>
            )}

            <DialogFooter>
              <Button type="button" variant="outline" onClick={() => handleOpenChange(false)} disabled={mutation.isPending}>
                Отмена
              </Button>
              <Button type="submit" disabled={mutation.isPending}>
                {mutation.isPending && <Loader2 className="animate-spin" aria-hidden="true" />}
                Создать
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}

import { useId, useState, type FormEvent } from 'react'
import { CircleAlert, CircleCheck, Loader2, Network, TriangleAlert } from 'lucide-react'

import { DiagnosticSteps } from '@/components/admin/DiagnosticSteps'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { errorMessage } from '@/lib/api'
import { useSystemInfo, useTestLdap } from '@/lib/queries'

/** "Проверка LDAP": runs the server-side diagnostics, optionally with a real login. */
export function LdapCheckCard() {
  const id = useId()
  const system = useSystemInfo()
  const test = useTestLdap({ errorToast: false })
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')

  const ldap = system.data?.auth?.ldap
  const ldapDisabled = !!ldap && !ldap.enabled

  const onSubmit = (e: FormEvent) => {
    e.preventDefault()
    const input = username.trim() ? { username: username.trim(), password: password || undefined } : {}
    test.mutate(input, {
      // The password is not needed after the check; do not keep it on screen.
      onSettled: () => setPassword(''),
    })
  }

  const result = test.data
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-lg">
          <Network className="size-5 text-muted-foreground" aria-hidden="true" />
          Проверка LDAP
        </CardTitle>
        <CardDescription>
          Проверяет связь с каталогом (Active Directory, OpenLDAP, FreeIPA), служебную учётную запись и поиск. Укажите
          логин сотрудника, чтобы проверить его поиск и группы, а с паролем — ещё и вход. Пароль никуда не сохраняется.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {ldapDisabled && (
          <Alert>
            <TriangleAlert aria-hidden="true" />
            <AlertTitle>Вход через LDAP сейчас не работает</AlertTitle>
            <AlertDescription>
              {/* The server says whether LDAP is not configured or configured with an error. */}
              {ldap.detail && <p>{ldap.detail.trim().replace(/[.!…]?$/, '.')}</p>}
              <p>
                Чтобы сотрудники входили с корпоративной учётной записью, укажите LDAP_URL и остальные параметры LDAP в
                файле настроек сервера и перезапустите сервис.
              </p>
            </AlertDescription>
          </Alert>
        )}
        <form onSubmit={onSubmit} className="flex flex-col gap-3" noValidate>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor={`${id}-user`}>Логин сотрудника (необязательно)</Label>
              <Input
                id={`${id}-user`}
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="off"
                autoCapitalize="off"
                spellCheck={false}
                placeholder="ivanova"
                className="h-9"
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor={`${id}-password`}>Пароль (необязательно)</Label>
              <Input
                id={`${id}-password`}
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="off"
                disabled={!username.trim()}
                className="h-9"
              />
            </div>
          </div>
          <div>
            <Button type="submit" variant="outline" size="lg" disabled={test.isPending}>
              {test.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Network aria-hidden="true" />}
              {test.isPending ? 'Проверяем…' : 'Проверить'}
            </Button>
          </div>
        </form>

        <div aria-live="polite" className="flex flex-col gap-3">
          {test.isError && (
            <Alert variant="destructive">
              <CircleAlert aria-hidden="true" />
              <AlertTitle>Проверку не удалось запустить</AlertTitle>
              <AlertDescription>{errorMessage(test.error)}</AlertDescription>
            </Alert>
          )}
          {result && !test.isPending && (
            <>
              {result.ok ? (
                <Alert>
                  <CircleCheck aria-hidden="true" className="text-emerald-700 dark:text-emerald-400" />
                  <AlertTitle>Всё в порядке</AlertTitle>
                  <AlertDescription>Все шаги проверки пройдены.</AlertDescription>
                </Alert>
              ) : (
                <Alert variant="destructive">
                  <CircleAlert aria-hidden="true" />
                  <AlertTitle>Есть проблема</AlertTitle>
                  <AlertDescription>Посмотрите шаг с ошибкой ниже — там написано, что исправить.</AlertDescription>
                </Alert>
              )}
              {result.steps.length > 0 && <DiagnosticSteps steps={result.steps} />}
            </>
          )}
        </div>
      </CardContent>
    </Card>
  )
}

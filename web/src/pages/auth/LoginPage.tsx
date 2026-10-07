import { useId, useState, type FormEvent, type KeyboardEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Eye, EyeOff, KeyRound, Loader2, LogIn, LogOut, UserRoundCog } from 'lucide-react'
import { Navigate, useLocation, useNavigate, useSearchParams } from 'react-router'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LogoMark } from '@/components/layout/Logo'
import { PageSpinner } from '@/components/LoadingState'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { useDocumentTitle } from '@/hooks/useDocumentTitle'
import { api, errorMessage, safeNextPath } from '@/lib/api'
import { useAuth, type LoginLocationState } from '@/lib/auth'
import {
  loginErrorMessage,
  parseRequiredRoles,
  publicAddress,
  publicLoginUrl,
  requiredRolesHint,
} from '@/pages/auth/loginErrors'
import { queryKeys, useAuthConfig } from '@/lib/queries'
import type { AuthConfig } from '@/lib/types'
import { cn } from '@/lib/utils'

function PasswordForm({ config, next }: { config: AuthConfig; next: string }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const id = useId()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [capsLock, setCapsLock] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault()
    if (!username.trim() || !password) {
      setError('Введите логин и пароль.')
      return
    }
    setPending(true)
    setError(null)
    try {
      const { user } = await api.auth.login({ username: username.trim(), password })
      // Drop anything cached for a previous session before showing the app.
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'auth' })
      qc.setQueryData(queryKeys.auth.me(), user)
      navigate(next, { replace: true })
    } catch (err) {
      setError(errorMessage(err))
      setPassword('')
      setPending(false)
    }
  }

  const trackCapsLock = (e: KeyboardEvent<HTMLInputElement>) => {
    if (typeof e.getModifierState === 'function') setCapsLock(e.getModifierState('CapsLock'))
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate aria-describedby={error ? `${id}-error` : undefined}>
      {config.ldap_enabled && (
        <p className="flex items-start gap-2 rounded-lg bg-muted/60 px-3 py-2 text-sm text-muted-foreground">
          <KeyRound className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          Используйте корпоративную учётную запись
        </p>
      )}
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-username`}>Логин</Label>
        <Input
          id={`${id}-username`}
          name="username"
          autoComplete="username"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          autoFocus
          className="h-10"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          aria-invalid={!!error || undefined}
          disabled={pending}
        />
      </div>
      <div className="flex flex-col gap-2">
        <Label htmlFor={`${id}-password`}>Пароль</Label>
        <div className="relative">
          <Input
            id={`${id}-password`}
            name="password"
            type={showPassword ? 'text' : 'password'}
            autoComplete="current-password"
            className="h-10 pr-10"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            onKeyUp={trackCapsLock}
            onKeyDown={trackCapsLock}
            aria-invalid={!!error || undefined}
            disabled={pending}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            className="absolute top-1/2 right-1.5 -translate-y-1/2 text-muted-foreground"
            onClick={() => setShowPassword((v) => !v)}
            aria-label={showPassword ? 'Скрыть пароль' : 'Показать пароль'}
            aria-pressed={showPassword}
          >
            {showPassword ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
          </Button>
        </div>
        {capsLock && <p className="text-xs text-amber-700 dark:text-amber-300">Включён Caps Lock</p>}
      </div>
      {error && (
        <Alert variant="destructive" id={`${id}-error`}>
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      <Button type="submit" size="lg" className="h-10 w-full" disabled={pending}>
        {pending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <LogIn aria-hidden="true" />}
        Войти
      </Button>
    </form>
  )
}

interface LoginOptionsProps {
  config: AuthConfig
  next: string
  /**
   * Keycloak signed in an account that cannot use the app (no role, or disabled
   * here): offer «Войти под другой учётной записью».
   */
  refused: boolean
  /** Back from Keycloak after «Войти под другой учётной записью» ended the old account's session. */
  switched: boolean
}

function LoginOptions({ config, next, refused, switched }: LoginOptionsProps) {
  const hasForm = config.local_enabled || config.ldap_enabled
  const oidcLabel = config.oidc_label.trim() || 'Войти через Keycloak'
  if (!hasForm && !config.oidc_enabled) {
    return (
      <EmptyState
        size="compact"
        icon={KeyRound}
        title="Вход пока не настроен"
        text="Ни один способ входа не включён. Обратитесь к администратору системы."
      />
    )
  }
  return (
    <div className="flex flex-col gap-5">
      {config.oidc_enabled && switched && (
        <Alert>
          <UserRoundCog aria-hidden="true" />
          <AlertDescription>
            Вы вышли из прежней учётной записи. Нажмите «{oidcLabel}» и войдите под нужной.
          </AlertDescription>
        </Alert>
      )}
      {hasForm && <PasswordForm config={config} next={next} />}
      {hasForm && config.oidc_enabled && (
        <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-hidden="true">
          <Separator className="flex-1" />
          или
          <Separator className="flex-1" />
        </div>
      )}
      {config.oidc_enabled && (
        <Button asChild variant={hasForm ? 'outline' : 'default'} size="lg" className="h-10 w-full">
          <a href={api.auth.oidcLoginUrl(next)}>
            <KeyRound aria-hidden="true" />
            {oidcLabel}
          </a>
        </Button>
      )}
      {config.oidc_enabled && refused && (
        // Keycloak still remembers the account without access: the server ends that
        // session first (back here with ?state=switch_account), or sends prompt=login.
        <Button asChild variant="ghost" size="lg" className="-mt-2 h-10 w-full">
          <a href={api.auth.oidcLoginUrl(next, { prompt: 'login' })}>
            <UserRoundCog aria-hidden="true" />
            Войти под другой учётной записью
          </a>
        </Button>
      )}
    </div>
  )
}

/** /login — username/password (local or LDAP) and/or the OIDC button, per /api/auth/config. */
export default function LoginPage() {
  useDocumentTitle('Вход')
  const [params] = useSearchParams()
  const next = safeNextPath(params.get('next'))
  const errorCode = params.get('error')
  const denied = errorCode === 'access_denied'
  // Only the Keycloak callback sends error=disabled (the form shows its own errors).
  const refused = denied || errorCode === 'disabled'
  // Set by Keycloak's logout redirect (RP-initiated logout `state`).
  const switched = params.get('state') === 'switch_account'
  const requiredHint = denied ? requiredRolesHint(parseRequiredRoles(params.get('required'))) : ''
  // Set by RequireAuth when a signed-in user lost the session (not by a link anybody can craft).
  const sessionEnded = (useLocation().state as LoginLocationState | null)?.sessionEnded === true
  const { me, isLoading: meLoading } = useAuth()
  const config = useAuthConfig()

  if (me) return <Navigate to={next} replace />

  const callbackError = errorCode ? loginErrorMessage(errorCode) : null
  // Keycloak login opened on another address than the main one: name and link the main one.
  const mainAddress = errorCode === 'oidc_host' ? publicAddress(config.data?.public_url) : null

  return (
    <div className="flex min-h-svh flex-col items-center justify-center bg-muted/40 px-4 py-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <div className="flex flex-col items-center gap-3 text-center">
          <LogoMark className="size-12" />
          <div className="flex flex-col gap-1">
            <h1 className="font-heading text-2xl font-semibold tracking-tight">Zoom Checker</h1>
            <p className="text-sm text-balance text-muted-foreground">
              Автоматический учёт посещаемости занятий в Zoom
            </p>
          </div>
        </div>

        <Card className="shadow-sm">
          <CardContent className="flex flex-col gap-4 pt-2">
            <h2 className="text-base font-medium">Вход в систему</h2>
            {callbackError && (
              <Alert variant="destructive">
                <CircleAlert aria-hidden="true" />
                <AlertDescription>
                  {mainAddress ? (
                    <p>
                      Вход через Keycloak работает только по основному адресу сервиса:{' '}
                      <a
                        href={publicLoginUrl(mainAddress, next)}
                        className="font-medium break-all underline underline-offset-4"
                      >
                        {mainAddress.host + mainAddress.pathname.replace(/\/+$/, '')}
                      </a>
                      . Откройте его и войдите снова.
                    </p>
                  ) : requiredHint ? (
                    `У вас нет доступа к системе. ${requiredHint}`
                  ) : (
                    callbackError
                  )}
                </AlertDescription>
              </Alert>
            )}
            {sessionEnded && !callbackError && (
              <Alert>
                <LogOut aria-hidden="true" />
                <AlertDescription>
                  Вы вышли из системы или вход завершился. Войдите снова — вы вернётесь на ту же страницу.
                </AlertDescription>
              </Alert>
            )}
            {config.isPending || meLoading ? (
              <PageSpinner className="min-h-48" />
            ) : config.isError ? (
              <ErrorState
                size="compact"
                error={config.error}
                onRetry={() => void config.refetch()}
                retrying={config.isFetching}
                title="Не удалось открыть страницу входа"
              />
            ) : (
              <LoginOptions config={config.data} next={next} refused={refused} switched={switched} />
            )}
          </CardContent>
          {config.data?.version && (
            <CardFooter className={cn('justify-center text-xs text-muted-foreground')}>
              Версия {config.data.version}
            </CardFooter>
          )}
        </Card>
      </div>
    </div>
  )
}

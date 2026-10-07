import { useMemo, useState } from 'react'
import {
  Info,
  KeyRound,
  MoreHorizontal,
  Search,
  ShieldCheck,
  Trash2,
  UserCheck,
  UserPlus,
  UserRound,
  UserX,
  UsersRound,
} from 'lucide-react'
import { toast } from 'sonner'

import { CreateUserDialog } from '@/components/admin/CreateUserDialog'
import { CredentialsNotice } from '@/components/admin/CredentialsNotice'
import { generatePassword } from '@/components/admin/password'
import { matchesUser, userStateMeta } from '@/components/admin/users'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { LoadingState } from '@/components/LoadingState'
import { authSourceLabels, roleLabels } from '@/components/status-meta'
import { ToneBadge } from '@/components/StatusBadge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useAuth } from '@/lib/auth'
import { formatDateTime, formatRelative, pluralize } from '@/lib/format'
import { useAuthConfig, useDeleteUser, useUpdateUser, useUsers } from '@/lib/queries'
import { userDisplayName, type UpdateUserInput, type User } from '@/lib/types'

type PendingAction = { kind: 'reset' | 'delete' | 'disable' | 'role'; user: User }

interface ResetResult {
  username: string
  password: string
  mustChange: boolean
}

const USER_WORDS = ['пользователь', 'пользователя', 'пользователей'] as const

export default function UsersPage() {
  const users = useUsers()
  const authConfig = useAuthConfig()
  const { me } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [query, setQuery] = useState('')
  // The last chosen action stays set while its dialog slides out (title does not blank).
  const [action, setAction] = useState<PendingAction | null>(null)
  const [actionOpen, setActionOpen] = useState(false)
  const openAction = (next: PendingAction) => {
    setAction(next)
    setActionOpen(true)
  }
  const dialogProps = (kind: PendingAction['kind']) => ({
    open: actionOpen && action?.kind === kind,
    onOpenChange: setActionOpen,
  })
  const name = action ? userDisplayName(action.user) : ''
  const newRole = action?.user.role === 'admin' ? 'teacher' : 'admin'
  const [resetResult, setResetResult] = useState<ResetResult | null>(null)

  // Each action shows its own success message.
  const updateUser = useUpdateUser({ successMessage: '' })
  const deleteUser = useDeleteUser()

  const update = async (user: User, input: UpdateUserInput, message: string) => {
    const updated = await updateUser.mutateAsync({ userId: user.id, input })
    toast.success(message)
    return updated
  }
  // Errors of fire-and-forget actions are already toasted by the mutation layer.
  const run = (p: Promise<unknown>) => void p.catch(() => {})

  const sorted = useMemo(
    () =>
      (users.data ?? [])
        .slice()
        .sort((a, b) => userDisplayName(a).localeCompare(userDisplayName(b), 'ru', { sensitivity: 'base' })),
    [users.data],
  )
  const visible = sorted.filter((u) => matchesUser(u, query))

  return (
    <Page>
      <PageHeader
        title="Пользователи"
        description="Кто может входить в систему: преподаватели и администраторы."
        actions={
          <Button size="lg" onClick={() => setCreateOpen(true)}>
            <UserPlus aria-hidden="true" />
            Новый пользователь
          </Button>
        }
      />

      <Alert>
        <Info aria-hidden="true" />
        <AlertTitle>Корпоративные учётные записи</AlertTitle>
        <AlertDescription>
          Сотрудники, которые входят через LDAP (Active Directory) или Keycloak, появляются в этом списке сами после
          первого входа. Их роль берётся из групп в каталоге и обновляется при каждом входе, поэтому здесь её не
          поменять. Чтобы закрыть доступ такому сотруднику, отключите его.
        </AlertDescription>
      </Alert>

      {authConfig.data && !authConfig.data.local_enabled && (
        <Alert>
          <KeyRound aria-hidden="true" />
          <AlertTitle>Вход по паролю выключен</AlertTitle>
          <AlertDescription>
            Локальные пользователи сейчас не могут войти: в настройках сервера AUTH_LOCAL_ENABLED=false.
          </AlertDescription>
        </Alert>
      )}

      {users.data ? (
        <section aria-label="Список пользователей" className="flex flex-col gap-3">
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
            <div className="relative w-full sm:max-w-xs">
              <Search
                className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
                aria-hidden="true"
              />
              <Input
                type="search"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="Поиск по имени или логину"
                aria-label="Поиск по имени или логину"
                className="h-9 pl-8"
              />
            </div>
            <p className="text-sm text-muted-foreground" aria-live="polite">
              {query.trim()
                ? `Найдено: ${visible.length} из ${sorted.length}`
                : pluralize(sorted.length, USER_WORDS)}
            </p>
          </div>

          {visible.length === 0 ? (
            <EmptyState
              icon={UsersRound}
              title={sorted.length === 0 ? 'Пользователей пока нет' : 'Никого не нашли'}
              text={
                sorted.length === 0
                  ? 'Создайте учётную запись для преподавателя кнопкой «Новый пользователь».'
                  : 'Проверьте написание или очистите поиск.'
              }
            />
          ) : (
            <div className="overflow-hidden rounded-xl ring-1 ring-foreground/10">
              <Table>
                <TableHeader className="bg-muted/40">
                  <TableRow>
                    <TableHead className="pl-4">Пользователь</TableHead>
                    <TableHead>Роль</TableHead>
                    <TableHead>Вход через</TableHead>
                    <TableHead>Последний вход</TableHead>
                    <TableHead>Состояние</TableHead>
                    <TableHead className="pr-4 text-right">
                      <span className="sr-only">Действия</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {visible.map((user) => (
                    <UserRow
                      key={user.id}
                      user={user}
                      isSelf={me?.id === user.id}
                      busy={updateUser.isPending && updateUser.variables?.userId === user.id}
                      onEnable={() => run(update(user, { disabled: false }, 'Пользователь снова может входить'))}
                      onAction={(kind) => openAction({ kind, user })}
                    />
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </section>
      ) : users.isError ? (
        <ErrorState error={users.error} onRetry={() => users.refetch()} retrying={users.isFetching} />
      ) : (
        <LoadingState variant="table" />
      )}

      <CreateUserDialog open={createOpen} onOpenChange={setCreateOpen} />

      <ConfirmDialog
        {...dialogProps('role')}
        title={newRole === 'admin' ? `Сделать «${name}» администратором?` : `Сделать «${name}» преподавателем?`}
        description={
          newRole === 'admin'
            ? 'Администратор может добавлять и отключать пользователей, менять правила учёта и смотреть журнал событий Zoom.'
            : 'Пользователь продолжит работать с группами и занятиями, но разделы для администраторов станут ему недоступны.'
        }
        confirmLabel={newRole === 'admin' ? 'Сделать администратором' : 'Сделать преподавателем'}
        onConfirm={() =>
          action ? update(action.user, { role: newRole }, `${name} теперь ${roleLabels[newRole].toLowerCase()}`) : undefined
        }
      />

      <ConfirmDialog
        {...dialogProps('reset')}
        title={`Сбросить пароль пользователя «${name}»?`}
        description="Старый пароль перестанет работать. Мы покажем новый временный пароль — передайте его пользователю."
        confirmLabel="Сбросить пароль"
        onConfirm={async () => {
          if (!action) return
          const password = generatePassword()
          const updated = await update(action.user, { password }, 'Пароль сброшен')
          setResetResult({
            username: updated?.username || action.user.username,
            password,
            mustChange: updated?.must_change_password ?? false,
          })
        }}
      />

      <ConfirmDialog
        {...dialogProps('disable')}
        title={`Отключить пользователя «${name}»?`}
        description="Пользователь не сможет войти, пока вы его снова не включите. Все его данные сохранятся."
        confirmLabel="Отключить"
        destructive
        onConfirm={() => (action ? update(action.user, { disabled: true }, 'Пользователь отключён') : undefined)}
      />

      <ConfirmDialog
        {...dialogProps('delete')}
        title={`Удалить пользователя «${name}»?`}
        description={
          action?.user.auth_source === 'local'
            ? 'Пользователь больше не сможет войти. Отметки и история изменений, которые он сделал, сохранятся. Это нельзя отменить.'
            : 'Если сотрудник снова войдёт через корпоративную учётную запись, он появится в списке заново. Чтобы закрыть доступ, лучше отключите его.'
        }
        confirmLabel="Удалить"
        destructive
        onConfirm={() => (action ? deleteUser.mutateAsync(action.user.id) : undefined)}
      />

      <Dialog open={resetResult !== null} onOpenChange={(open) => !open && setResetResult(null)}>
        <DialogContent className="sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Новый временный пароль</DialogTitle>
            <DialogDescription>Сообщите эти данные пользователю. Больше пароль показан не будет.</DialogDescription>
          </DialogHeader>
          {resetResult && (
            <CredentialsNotice
              username={resetResult.username}
              password={resetResult.password}
              mustChange={resetResult.mustChange}
            />
          )}
          <DialogFooter>
            <Button type="button" onClick={() => setResetResult(null)}>
              Готово
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

interface UserRowProps {
  user: User
  isSelf: boolean
  busy: boolean
  onEnable: () => void
  onAction: (kind: PendingAction['kind']) => void
}

function UserRow({ user, isSelf, busy, onEnable, onAction }: UserRowProps) {
  const name = userDisplayName(user)
  const state = userStateMeta(user)
  const local = user.auth_source === 'local'
  const sourceLabel = Object.hasOwn(authSourceLabels, user.auth_source)
    ? authSourceLabels[user.auth_source]
    : String(user.auth_source)
  const roleLabel = Object.hasOwn(roleLabels, user.role) ? roleLabels[user.role] : String(user.role)

  return (
    <TableRow className={user.disabled ? 'text-muted-foreground' : undefined}>
      <TableCell className="pl-4">
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="flex items-center gap-2 font-medium text-foreground">
            {name}
            {isSelf && (
              <Badge variant="secondary" className="font-normal">
                Это вы
              </Badge>
            )}
          </span>
          <span className="text-xs text-muted-foreground">
            {user.username}
            {user.email && user.email !== user.username ? ` · ${user.email}` : ''}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <span className="inline-flex items-center gap-1.5">
          {user.role === 'admin' ? (
            <ShieldCheck className="size-4 text-sky-700 dark:text-sky-400" aria-hidden="true" />
          ) : (
            <UserRound className="size-4 text-muted-foreground" aria-hidden="true" />
          )}
          {roleLabel}
        </span>
      </TableCell>
      <TableCell>{sourceLabel}</TableCell>
      <TableCell>
        {user.last_login_at ? (
          <span title={formatRelative(user.last_login_at)}>{formatDateTime(user.last_login_at)}</span>
        ) : (
          <span className="text-muted-foreground">Ещё не входил</span>
        )}
      </TableCell>
      <TableCell>
        <ToneBadge tone={state.tone} icon={state.icon} title={state.description}>
          {state.label}
        </ToneBadge>
      </TableCell>
      <TableCell className="pr-4 text-right">
        {isSelf ? (
          <span className="text-xs text-muted-foreground" title="Свой пароль меняйте в меню пользователя справа вверху">
            —
          </span>
        ) : (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" size="icon" aria-label={`Действия: ${name}`} disabled={busy}>
                <MoreHorizontal aria-hidden="true" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-60">
              <DropdownMenuLabel className="truncate">{name}</DropdownMenuLabel>
              {local ? (
                <DropdownMenuItem onSelect={() => onAction('role')}>
                  {user.role === 'admin' ? <UserRound aria-hidden="true" /> : <ShieldCheck aria-hidden="true" />}
                  {user.role === 'admin' ? 'Сделать преподавателем' : 'Сделать администратором'}
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem disabled>
                  <ShieldCheck aria-hidden="true" />
                  Роль задаётся группами {sourceLabel}
                </DropdownMenuItem>
              )}
              {local && (
                <DropdownMenuItem onSelect={() => onAction('reset')}>
                  <KeyRound aria-hidden="true" />
                  Сбросить пароль
                </DropdownMenuItem>
              )}
              {user.disabled ? (
                <DropdownMenuItem onSelect={onEnable}>
                  <UserCheck aria-hidden="true" />
                  Включить
                </DropdownMenuItem>
              ) : (
                <DropdownMenuItem onSelect={() => onAction('disable')}>
                  <UserX aria-hidden="true" />
                  Отключить
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              <DropdownMenuItem variant="destructive" onSelect={() => onAction('delete')}>
                <Trash2 aria-hidden="true" />
                Удалить
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </TableCell>
    </TableRow>
  )
}

// Session state for the SPA: who is logged in, logout, and route guards.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ShieldAlert } from 'lucide-react'
import { Link, Navigate, Outlet, useLocation } from 'react-router'

import { ChangePasswordDialog } from '@/components/auth/ChangePasswordDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageSpinner } from '@/components/LoadingState'
import { Button } from '@/components/ui/button'
import { api, browserLocation, loginUrl, setUnauthorizedHandler } from '@/lib/api'
import { queryKeys, useMe } from '@/lib/queries'
import type { Role, User } from '@/lib/types'

export interface AuthContextValue {
  /** Logged-in user, null when anonymous (or not yet known). */
  me: User | null
  role: Role | null
  isAdmin: boolean
  /** Local accounts can change their password in the app; LDAP/OIDC cannot. */
  canChangePassword: boolean
  /** True until the first session check finished. */
  isLoading: boolean
  /** Session check failed for a reason other than "not logged in" (e.g. server down). */
  error: unknown
  /**
   * The user was signed in and the session ended without them pressing «Выйти» here
   * (logout in another tab, session expired, account disabled): the login page says so.
   */
  sessionEnded: boolean
  refetch: () => void
  /** Ends the session and leaves the app (to /login or the IdP's logout page). */
  logout: () => Promise<void>
  openChangePassword: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

/**
 * Loads the current user (GET /api/auth/me), installs the global 401 handler
 * (drops the cached user so RequireAuth redirects to /login?next=…) and shows
 * the mandatory "Смените пароль" dialog when must_change_password is set.
 * Must be rendered inside the router and the QueryClientProvider.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const meQuery = useMe()
  const [passwordOpen, setPasswordOpen] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const me = meQuery.data ?? null

  // Derived during render, not in an effect: RequireAuth redirects in this same render,
  // and its <Navigate> runs before an effect of this provider would.
  const [prevMe, setPrevMe] = useState<User | null>(me)
  const [sessionEnded, setSessionEnded] = useState(false)
  if (me !== prevMe) {
    setPrevMe(me)
    if (me) setSessionEnded(false)
    else if (prevMe && !loggingOut) setSessionEnded(true)
  }

  useEffect(
    () =>
      setUnauthorizedHandler(() => {
        qc.setQueryData(queryKeys.auth.me(), null)
      }),
    [qc],
  )

  const logout = useCallback(async () => {
    // Leaving on purpose: not a session that "ended by itself".
    setLoggingOut(true)
    let target = '/login'
    try {
      const res = await api.auth.logout()
      if (res?.redirect_url) target = res.redirect_url
    } catch {
      // The session may already be gone; leaving is still the right outcome.
    }
    qc.clear()
    browserLocation.assign(target)
  }, [qc])

  const { refetch } = meQuery
  const value = useMemo<AuthContextValue>(
    () => ({
      me,
      role: me?.role ?? null,
      isAdmin: me?.role === 'admin',
      canChangePassword: me?.auth_source === 'local',
      isLoading: meQuery.isPending,
      error: meQuery.error,
      sessionEnded,
      refetch: () => void refetch(),
      logout,
      openChangePassword: () => setPasswordOpen(true),
    }),
    [me, meQuery.isPending, meQuery.error, sessionEnded, refetch, logout],
  )

  const forced = passwordChangeForced(me)
  return (
    <AuthContext.Provider value={value}>
      {children}
      {me && (forced || passwordOpen) && (
        <ChangePasswordDialog
          open
          forced={forced}
          onOpenChange={setPasswordOpen}
          onLogout={() => void logout()}
        />
      )}
    </AuthContext.Provider>
  )
}

/**
 * The user signed in with a temporary password and must replace it first.
 * Only local accounts have a password here; for LDAP/OIDC the flag cannot be satisfied.
 */
function passwordChangeForced(me: User | null): boolean {
  return !!me?.must_change_password && me.auth_source === 'local'
}

/** Session state; must be used under AuthProvider. */
// eslint-disable-next-line react/only-export-components -- the hook belongs with its provider
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside <AuthProvider>')
  return ctx
}

/** Router state RequireAuth hands to /login. */
export interface LoginLocationState {
  /** A signed-in user lost the session while using the app (see AuthContextValue.sessionEnded). */
  sessionEnded?: boolean
}

/**
 * Session state when available, null outside AuthProvider. For small role-dependent
 * extras in widgets that are also rendered on their own (tests, previews).
 */
// eslint-disable-next-line react/only-export-components -- the hook belongs with its provider
export function useOptionalAuth(): AuthContextValue | null {
  return useContext(AuthContext)
}

/** Renders children (or the nested route) only for logged-in users; otherwise redirects to /login?next=…. */
export function RequireAuth({ children }: { children?: ReactNode }) {
  const { me, isLoading, error, refetch, sessionEnded } = useAuth()
  const location = useLocation()
  if (isLoading) return <PageSpinner label="Проверяем вход…" className="min-h-svh" />
  if (!me) {
    if (error) {
      return (
        <Page width="narrow" className="min-h-svh justify-center">
          <ErrorState error={error} onRetry={refetch} />
        </Page>
      )
    }
    // Router state, not a query parameter: a crafted link cannot show the notice.
    const state: LoginLocationState | undefined = sessionEnded ? { sessionEnded: true } : undefined
    return <Navigate to={loginUrl(`${location.pathname}${location.search}${location.hash}`)} replace state={state} />
  }
  // Until the temporary password is replaced (AuthProvider shows the dialog) the server
  // refuses every other request: pages mounted now would only load into "Нет доступа"
  // errors and broken live streams that stay after the change. Mount them afterwards.
  if (passwordChangeForced(me)) return <div className="min-h-svh bg-muted/40" data-testid="password-change-pending" />
  return <>{children ?? <Outlet />}</>
}

/** Renders children (or the nested route) only for admins; teachers see a friendly "no access" page. */
export function RequireAdmin({ children }: { children?: ReactNode }) {
  const { isAdmin } = useAuth()
  if (!isAdmin) {
    return (
      <Page width="narrow">
        <EmptyState
          icon={ShieldAlert}
          title="Этот раздел доступен только администраторам"
          text="Если вам нужен доступ, обратитесь к администратору системы."
          action={
            <Button asChild variant="outline">
              <Link to="/">К занятиям</Link>
            </Button>
          }
        />
      </Page>
    )
  }
  return <>{children ?? <Outlet />}</>
}

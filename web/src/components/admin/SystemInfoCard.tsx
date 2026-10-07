import type { ReactNode } from 'react'
import { CircleCheck, CircleMinus, Server } from 'lucide-react'

import { dbDriverLabel, formatUptime, shortCommit, tlsModeLabel } from '@/components/admin/system'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useSystemInfo } from '@/lib/queries'
import type { AuthProviderStatus } from '@/lib/types'

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 py-2.5 sm:grid-cols-[12rem_1fr] sm:gap-4">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-sm break-words">{children}</dd>
    </div>
  )
}

function Provider({ name, status }: { name: string; status: AuthProviderStatus | undefined }) {
  const enabled = !!status?.enabled
  const Icon = enabled ? CircleCheck : CircleMinus
  return (
    <li className="flex items-start gap-2">
      <Icon
        className={enabled ? 'mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-400' : 'mt-0.5 size-4 shrink-0 text-muted-foreground'}
        aria-hidden="true"
      />
      <span className="min-w-0">
        <span className={enabled ? 'font-medium' : 'text-muted-foreground'}>{name}</span>
        <span className="text-muted-foreground"> — {enabled ? 'включён' : 'выключен'}</span>
        {/* For a switched-off provider the detail says why (not configured / configuration error). */}
        {status?.detail && <span className="block text-xs break-all text-muted-foreground">{status.detail}</span>}
      </span>
    </li>
  )
}

/** Read-only facts about the running server (GET /api/system). */
export function SystemInfoCard() {
  const system = useSystemInfo()
  const info = system.data
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-lg">
          <Server className="size-5 text-muted-foreground" aria-hidden="true" />
          Система
        </CardTitle>
        <CardDescription>
          Эти параметры задаются в файле настроек сервера. Чтобы их поменять, отредактируйте файл и перезапустите сервис.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {info ? (
          <dl className="divide-y">
            <Row label="Версия">
              {info.version || '—'}
              {shortCommit(info.commit) && <span className="text-muted-foreground"> ({shortCommit(info.commit)})</span>}
            </Row>
            <Row label="Адрес сервиса">{info.public_url || '—'}</Row>
            <Row label="База данных">{dbDriverLabel(info.db_driver)}</Row>
            <Row label="HTTPS">{tlsModeLabel(info.tls_mode)}</Row>
            <Row label="Часовой пояс">{info.timezone || '—'}</Row>
            <Row label="Работает без перезапуска">{formatUptime(info.uptime_seconds)}</Row>
            <Row label="Папка с данными">
              <span className="font-mono text-xs">{info.data_dir || '—'}</span>
            </Row>
            <Row label="Способы входа">
              <ul className="flex flex-col gap-1.5">
                <Provider name="По логину и паролю" status={info.auth?.local} />
                <Provider name="LDAP / Active Directory" status={info.auth?.ldap} />
                <Provider name="Keycloak (OIDC)" status={info.auth?.oidc} />
              </ul>
            </Row>
          </dl>
        ) : system.isError ? (
          <ErrorState size="compact" error={system.error} onRetry={() => system.refetch()} retrying={system.isFetching} />
        ) : (
          <LoadingState variant="list" rows={6} />
        )}
      </CardContent>
    </Card>
  )
}

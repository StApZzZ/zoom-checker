import type { ReactNode } from 'react'
import { Activity, ArrowRight, CircleAlert, Clock, Inbox, Link2, Timer, TriangleAlert } from 'lucide-react'
import { Link } from 'react-router'

import { CopyButton } from '@/components/CopyButton'
import { ToneBadge } from '@/components/StatusBadge'
import { StatTile } from '@/components/StatTile'
import { zoomEventTypeLabel } from '@/components/status-meta'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { formatDateTime, formatRelative } from '@/lib/format'
import type { ZoomStatus } from '@/lib/types'
import {
  isInsecureWebhookUrl,
  isIpWebhookUrl,
  isLocalWebhookUrl,
  zoomClockProblem,
  zoomConnectionMeta,
  type ZoomClockProblem,
  type ZoomConnectionState,
} from '@/pages/zoom/zoomStatus'

export interface ZoomStatusCardProps {
  status: ZoomStatus
  state: ZoomConnectionState
  isAdmin: boolean
  now: number
}

function Code({ children }: { children: ReactNode }) {
  return <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em] break-all">{children}</code>
}

function StateText({ state, isAdmin }: { state: ZoomConnectionState; isAdmin: boolean }) {
  switch (state) {
    case 'ok':
      return <p>Всё в порядке: Zoom сообщает системе о входах и выходах участников, отметки ставятся сами.</p>
    case 'waiting':
      return isAdmin ? (
        <p>
          Секрет указан, но Zoom ещё ничего не присылал. Проверьте, что приложение в Zoom активировано и адрес ниже
          указан в нём верно (инструкция ниже). События появятся, когда начнётся первая конференция.
        </p>
      ) : (
        <p>
          Zoom ещё не присылал событий. Они появятся, когда начнётся первое занятие в Zoom. Если занятие уже идёт, а
          отметки не появляются — сообщите администратору.
        </p>
      )
    case 'stale':
      return isAdmin ? (
        <p>
          Это нормально, если занятий не было. Если сейчас идёт занятие, а событий нет — проверьте, что приложение в
          Zoom активно и адрес в нём не менялся.
        </p>
      ) : (
        <p>Это нормально, если занятий не было. Если занятие идёт, а отметки не появляются — сообщите администратору.</p>
      )
    case 'not_configured':
      return isAdmin ? (
        <div className="flex flex-col gap-2">
          <p>Пока секрет не указан, сервис не принимает события от Zoom и посещаемость не отмечается.</p>
          <ol className="list-decimal space-y-1 pl-5">
            <li>
              Скопируйте <strong>Secret Token</strong> из приложения в Zoom (раздел Feature, см. инструкцию ниже).
            </li>
            <li>
              Впишите его в файл настроек сервера (обычно <Code>/etc/zoom-checker/zoom-checker.env</Code>, для Docker —
              файл <Code>.env</Code>): <Code>ZOOM_WEBHOOK_SECRET_TOKEN=…</Code>
            </li>
            <li>
              Перезапустите сервис: <Code>sudo systemctl restart zoom-checker</Code> (или{' '}
              <Code>docker compose up -d</Code>)
            </li>
          </ol>
        </div>
      ) : (
        <p>
          Подключение к Zoom ещё не настроено, поэтому посещаемость пока не отмечается автоматически. Сообщите
          администратору системы.
        </p>
      )
  }
}

/** "Часы сервера расходятся с Zoom": events may be refused and lost; the fix is NTP on the server. */
function ClockWarning({ problem, isAdmin }: { problem: ZoomClockProblem; isAdmin: boolean }) {
  return (
    <Alert data-testid="zoom-clock-warning">
      <Clock aria-hidden="true" className="text-amber-700 dark:text-amber-300" />
      <AlertTitle>Часы сервера расходятся с Zoom</AlertTitle>
      <AlertDescription>
        {problem.skewMinutes !== null && (
          <p>
            Часы сервера {problem.serverBehind ? 'отстают от Zoom' : 'спешат относительно Zoom'} примерно на{' '}
            {problem.skewMinutes} мин. Из-за этого время в отметках может сбиваться.
          </p>
        )}
        {problem.rejectedAt && (
          <p>
            <time dateTime={problem.rejectedAt}>{formatDateTime(problem.rejectedAt)}</time> Zoom прислал событие, но
            сервис не смог его принять из-за разницы во времени — такие отметки теряются.
          </p>
        )}
        {isAdmin ? (
          <p>
            Включите синхронизацию времени на сервере: <Code>sudo timedatectl set-ntp true</Code> (для Docker — на
            самом сервере, контейнер берёт время у него).
          </p>
        ) : (
          <p>Сообщите администратору.</p>
        )}
      </AlertDescription>
    </Alert>
  )
}

/** "Подключение к Zoom": state badge, explanation, last event, counters and (for admins) the webhook address. */
export function ZoomStatusCard({ status, state, isAdmin, now }: ZoomStatusCardProps) {
  const meta = zoomConnectionMeta[state]
  const insecure = isInsecureWebhookUrl(status.webhook_url)
  const local = isLocalWebhookUrl(status.webhook_url)
  const ipAddress = !local && isIpWebhookUrl(status.webhook_url)
  const clock = zoomClockProblem(status, now)
  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle className="text-lg">
              <h2>Подключение к Zoom</h2>
            </CardTitle>
            <CardDescription>Zoom сообщает системе, кто вошёл в конференцию и кто вышел.</CardDescription>
          </div>
          <ToneBadge tone={meta.tone} icon={meta.icon} size="lg" className="max-w-full">
            {isAdmin ? meta.label : meta.teacherLabel}
          </ToneBadge>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-5">
        <div className="text-sm text-pretty sm:text-base" data-testid="zoom-state-text">
          <StateText state={state} isAdmin={isAdmin} />
        </div>

        {clock && <ClockWarning problem={clock} isAdmin={isAdmin} />}

        <p className="text-sm">
          <span className="text-muted-foreground">Последнее событие: </span>
          {status.last_event_at ? (
            <>
              <span className="font-medium">{zoomEventTypeLabel(status.last_event_type) || 'событие'}</span>
              {' — '}
              <time dateTime={status.last_event_at}>{formatDateTime(status.last_event_at)}</time>
              <span className="text-muted-foreground"> ({formatRelative(status.last_event_at, now)})</span>
            </>
          ) : (
            <span className="font-medium">ещё не было</span>
          )}
        </p>

        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <StatTile label="Событий за 24 часа" value={status.events_24h} icon={Activity} tone="info" />
          <StatTile
            label="Не удалось обработать"
            value={status.failed_count}
            icon={status.failed_count > 0 ? CircleAlert : Inbox}
            tone={status.failed_count > 0 ? 'danger' : 'muted'}
            hint={status.failed_count > 0 ? (isAdmin ? 'Подробности — в журнале событий' : 'Администратор разберётся') : 'Ошибок нет'}
          />
          <StatTile
            label="Ждут обработки"
            value={status.pending_count}
            icon={Timer}
            tone={status.pending_count > 0 ? 'warning' : 'muted'}
            hint={status.pending_count > 0 ? 'Обычно обрабатываются за секунды' : 'Очередь пуста'}
          />
        </div>

        {isAdmin && status.failed_count > 0 && (
          <div>
            <Button asChild variant="outline">
              <Link to="/admin/events?status=failed">
                Посмотреть ошибки в журнале
                <ArrowRight aria-hidden="true" />
              </Link>
            </Button>
          </div>
        )}

        {isAdmin && (
          <div className="flex flex-col gap-2 rounded-lg bg-muted/50 p-3 ring-1 ring-foreground/10">
            <p className="flex items-center gap-1.5 text-sm font-medium">
              <Link2 className="size-4 text-muted-foreground" aria-hidden="true" />
              Адрес для Zoom (Event notification endpoint URL)
            </p>
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <code
                className="min-w-0 flex-1 rounded-md bg-background px-3 py-2 font-mono text-sm break-all ring-1 ring-foreground/10"
                data-testid="webhook-url"
              >
                {status.webhook_url || '—'}
              </code>
              {status.webhook_url && (
                <CopyButton
                  value={status.webhook_url}
                  label="Скопировать адрес"
                  successMessage="Адрес скопирован"
                  className="h-9"
                />
              )}
            </div>
            {insecure && (
              <Alert className="mt-1">
                <TriangleAlert aria-hidden="true" className="text-amber-700 dark:text-amber-300" />
                <AlertTitle>Zoom принимает только адреса https://</AlertTitle>
                <AlertDescription>
                  Включите HTTPS (TLS_MODE=auto или прокси с сертификатом) и укажите PUBLIC_URL, начинающийся с
                  https://, затем перезапустите сервис.
                </AlertDescription>
              </Alert>
            )}
            {ipAddress && (
              <Alert className="mt-1">
                <TriangleAlert aria-hidden="true" className="text-amber-700 dark:text-amber-300" />
                <AlertTitle>Zoom не примет адрес с IP-адресом</AlertTitle>
                <AlertDescription>
                  Zoom принимает адрес для событий только с доменным именем. Заведите DNS-запись (например,
                  attendance.example.com) на этот сервер, укажите её в PUBLIC_URL и перезапустите сервис.
                </AlertDescription>
              </Alert>
            )}
            {local && (
              <Alert className="mt-1">
                <TriangleAlert aria-hidden="true" className="text-amber-700 dark:text-amber-300" />
                <AlertTitle>Zoom не сможет достучаться до этого адреса</AlertTitle>
                <AlertDescription>
                  Адрес открывается только внутри вашей сети, а Zoom присылает события из интернета. Укажите в PUBLIC_URL
                  адрес сервиса, доступный из интернета (например, https://attendance.example.com), и перезапустите
                  сервис.
                </AlertDescription>
              </Alert>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  )
}

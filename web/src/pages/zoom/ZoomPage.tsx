import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { LoadingState } from '@/components/LoadingState'
import { useNow } from '@/hooks/useNow'
import { useAuth } from '@/lib/auth'
import { useZoomStatus } from '@/lib/queries'
import { SetupGuide } from '@/pages/zoom/SetupGuide'
import { ZoomSessionsSection } from '@/pages/zoom/ZoomSessionsSection'
import { ZoomStatusCard } from '@/pages/zoom/ZoomStatusCard'
import { zoomConnectionState } from '@/pages/zoom/zoomStatus'

export default function ZoomPage() {
  const { isAdmin } = useAuth()
  const status = useZoomStatus()
  // "within 24 h" and "5 минут назад" only need a coarse clock.
  const now = useNow(30_000)
  const state = status.data ? zoomConnectionState(status.data, now) : null

  return (
    <Page>
      <PageHeader
        title="Zoom"
        description={
          isAdmin
            ? 'Подключение к Zoom, инструкция по настройке и конференции, которые не удалось сопоставить с занятиями.'
            : 'Работает ли подключение к Zoom и какие конференции не удалось сопоставить с занятиями.'
        }
      />

      {status.data && state ? (
        <>
          <ZoomStatusCard status={status.data} state={state} isAdmin={isAdmin} now={now} />
          {/* Open on arrival until events flow; it does not collapse by itself while the admin follows the steps. */}
          {isAdmin && <SetupGuide webhookUrl={status.data.webhook_url} defaultOpen={state !== 'ok'} />}
        </>
      ) : status.isError ? (
        <ErrorState
          error={status.error}
          title="Не удалось узнать состояние подключения"
          onRetry={() => status.refetch()}
          retrying={status.isFetching}
        />
      ) : (
        <LoadingState variant="list" rows={5} label="Проверяем подключение к Zoom…" />
      )}

      <ZoomSessionsSection />
    </Page>
  )
}

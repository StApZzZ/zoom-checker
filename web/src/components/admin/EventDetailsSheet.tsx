import type { ReactNode } from 'react'
import { CircleAlert, Loader2, RotateCw } from 'lucide-react'
import { toast } from 'sonner'

import { prettyJson } from '@/components/admin/json'
import { CopyButton } from '@/components/CopyButton'
import { ErrorState } from '@/components/ErrorState'
import { zoomEventTypeLabel } from '@/components/status-meta'
import { StatusBadge } from '@/components/StatusBadge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { formatEventTime } from '@/components/admin/events'
import { EMPTY, formatMeetingId } from '@/lib/format'
import { useEvent, useReprocessEvent } from '@/lib/queries'
import type { WebhookEventSummary } from '@/lib/types'

function Field({ label, children, mono }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <div className="grid gap-0.5 py-2 sm:grid-cols-[11rem_1fr] sm:gap-3">
      <dt className="text-sm text-muted-foreground">{label}</dt>
      <dd className={mono ? 'min-w-0 font-mono text-xs break-all' : 'min-w-0 text-sm break-words'}>{children}</dd>
    </div>
  )
}

export interface EventDetailsSheetProps {
  /** Row the admin clicked (shown immediately; the payload is loaded separately). */
  event: WebhookEventSummary | null
  /** Defaults to `event !== null`; pass it to keep the content while the sheet closes. */
  open?: boolean
  onOpenChange: (open: boolean) => void
}

/** Side sheet with every stored field of a webhook event, the pretty-printed body and "Обработать заново". */
export function EventDetailsSheet({ event, open = event !== null, onOpenChange }: EventDetailsSheetProps) {
  const detail = useEvent(event?.id)
  // The sheet toasts itself, so the result is visible with any QueryClient setup.
  const reprocess = useReprocessEvent({ successMessage: '' })
  // Fresh detail (after a reprocess) wins over the row from the list.
  const e = detail.data && detail.data.id === event?.id ? detail.data : event
  const full = detail.data && detail.data.id === event?.id ? detail.data : null
  const payload = prettyJson(full?.payload)

  return (
    <Sheet open={open && event !== null} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 sm:max-w-2xl">
        {e && (
          <>
            <SheetHeader className="border-b pr-12">
              <SheetTitle className="text-lg">{zoomEventTypeLabel(e.event_type)}</SheetTitle>
              <SheetDescription>
                Событие №{e.id} · <span className="font-mono text-xs">{e.event_type || EMPTY}</span>
              </SheetDescription>
            </SheetHeader>

            <div className="flex flex-1 flex-col gap-4 overflow-y-auto p-4">
              <div className="flex flex-wrap items-center gap-2">
                <StatusBadge kind="event" status={e.processing_status} size="lg" />
              </div>

              {e.error && (
                <Alert variant="destructive">
                  <CircleAlert aria-hidden="true" />
                  <AlertTitle>Ошибка обработки</AlertTitle>
                  <AlertDescription className="break-words whitespace-pre-wrap">{e.error}</AlertDescription>
                </Alert>
              )}

              <dl className="divide-y">
                <Field label="Получено">{formatEventTime(e.received_at)}</Field>
                <Field label="Произошло в Zoom">{formatEventTime(e.occurred_at)}</Field>
                <Field label="Обработано">{formatEventTime(e.processed_at)}</Field>
                <Field label="Попыток обработки">{e.attempts}</Field>
                {full?.next_attempt_at && <Field label="Следующая попытка">{formatEventTime(full.next_attempt_at)}</Field>}
                <Field label="Участник">{e.participant_name || EMPTY}</Field>
                <Field label="Конференция (Meeting ID)">{e.meeting_id ? formatMeetingId(e.meeting_id) : EMPTY}</Field>
                <Field label="Meeting UUID" mono>
                  {e.meeting_uuid || EMPTY}
                </Field>
                {full && (
                  <>
                    <Field label="Метка времени Zoom (event_ts)" mono>
                      {full.event_ts || EMPTY}
                    </Field>
                    <Field label="Ключ повтора (dedupe)" mono>
                      {full.dedupe_key || EMPTY}
                    </Field>
                  </>
                )}
              </dl>

              <section aria-labelledby="event-payload-title" className="flex flex-col gap-2">
                <div className="flex items-center justify-between gap-2">
                  <h3 id="event-payload-title" className="font-medium">
                    Данные от Zoom (JSON)
                  </h3>
                  {full && (
                    <CopyButton
                      value={payload.text || full.payload}
                      label="Скопировать"
                      size="sm"
                      successMessage="JSON скопирован"
                    />
                  )}
                </div>
                {full ? (
                  <>
                    {!payload.valid && full.payload && (
                      <p className="text-xs text-muted-foreground">Показано как есть, без форматирования.</p>
                    )}
                    <pre
                      className="max-h-[50svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-relaxed whitespace-pre"
                      data-testid="event-payload"
                      tabIndex={0}
                      aria-label="Данные события в формате JSON"
                    >
                      {payload.text || 'Пусто'}
                    </pre>
                  </>
                ) : detail.isError ? (
                  <ErrorState size="compact" error={detail.error} onRetry={() => detail.refetch()} retrying={detail.isFetching} />
                ) : (
                  <div role="status" className="flex flex-col gap-2">
                    <span className="sr-only">Загрузка…</span>
                    <Skeleton className="h-4 w-3/4" />
                    <Skeleton className="h-4 w-2/3" />
                    <Skeleton className="h-4 w-1/2" />
                  </div>
                )}
              </section>
            </div>

            <SheetFooter className="border-t sm:flex-row sm:justify-end">
              <Button variant="outline" onClick={() => onOpenChange(false)}>
                Закрыть
              </Button>
              <Button
                onClick={() =>
                  reprocess.mutate(e.id, {
                    onSuccess: () => toast.success('Событие отправлено на повторную обработку'),
                  })
                }
                disabled={reprocess.isPending}
              >
                {reprocess.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCw aria-hidden="true" />}
                Обработать заново
              </Button>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

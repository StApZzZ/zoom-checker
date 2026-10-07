import { useEffect, useMemo, useState } from 'react'
import { Loader2, RefreshCw, RotateCw, ScrollText, Search, SearchX } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import { toast } from 'sonner'

import { EventDetailsSheet } from '@/components/admin/EventDetailsSheet'
import { EVENT_TYPE_OPTIONS, formatEventTime, parseStatusParam, parseTypeParam } from '@/components/admin/events'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { LoadingState } from '@/components/LoadingState'
import { eventStatusMeta, zoomEventTypeLabel } from '@/components/status-meta'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { EMPTY, formatMeetingId, pluralize, WORDS } from '@/lib/format'
import { useEventsInfinite, useReprocessFailed } from '@/lib/queries'
import { EVENT_PROCESSING_STATUSES, type WebhookEventSummary } from '@/lib/types'
import { cn } from '@/lib/utils'

const ALL = 'all'
const SEARCH_DEBOUNCE_MS = 350

export default function EventsPage() {
  const [params, setParams] = useSearchParams()
  const status = parseStatusParam(params.get('status'))
  const type = parseTypeParam(params.get('type'))
  const q = (params.get('q') ?? '').trim()
  const [text, setText] = useState(q)
  // The URL changed by itself (nav link, back button): show its query in the box,
  // otherwise the debounce below would put the old text back into the URL.
  const [urlQ, setUrlQ] = useState(q)
  if (q !== urlQ) {
    setUrlQ(q)
    if (text.trim() !== q) setText(q)
  }
  // Kept while the sheet slides out, so it does not go blank mid-animation.
  const [selected, setSelected] = useState<WebhookEventSummary | null>(null)
  const [sheetOpen, setSheetOpen] = useState(false)
  const openEvent = (ev: WebhookEventSummary) => {
    setSelected(ev)
    setSheetOpen(true)
  }

  const setParam = (key: string, value: string | undefined) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true },
    )

  // Search is applied after the admin stops typing.
  useEffect(() => {
    const value = text.trim()
    if (value === q) return
    const t = setTimeout(
      () =>
        setParams(
          (prev) => {
            const next = new URLSearchParams(prev)
            if (value) next.set('q', value)
            else next.delete('q')
            return next
          },
          { replace: true },
        ),
      SEARCH_DEBOUNCE_MS,
    )
    return () => clearTimeout(t)
  }, [text, q, setParams])

  const events = useEventsInfinite({ status, type, q: q || undefined })
  const reprocessFailed = useReprocessFailed()

  const items = useMemo(() => {
    const seen = new Set<number>()
    const out: WebhookEventSummary[] = []
    for (const page of events.data?.pages ?? [])
      for (const item of page.items ?? []) {
        if (seen.has(item.id)) continue
        seen.add(item.id)
        out.push(item)
      }
    return out
  }, [events.data])

  const filtered = !!(status || type || q)
  const resetFilters = () => {
    setText('')
    setParams(new URLSearchParams(), { replace: true })
  }

  return (
    <Page width="wide">
      <PageHeader
        title="Журнал событий"
        description="Всё, что присылает Zoom: входы и выходы участников, начало и конец конференций. Здесь видно ошибки и можно обработать события заново."
        actions={
          <>
            <Button
              variant="outline"
              size="lg"
              onClick={() => events.refetch()}
              disabled={events.isFetching}
              aria-label="Обновить список"
              title="Обновить список"
            >
              <RefreshCw className={cn(events.isFetching && 'animate-spin')} aria-hidden="true" />
              <span className="hidden sm:inline">Обновить</span>
            </Button>
            <ConfirmDialog
              trigger={
                <Button size="lg">
                  <RotateCw aria-hidden="true" />
                  Повторить все ошибочные
                </Button>
              }
              title="Обработать заново все события с ошибкой?"
              description="Система ещё раз попробует учесть каждое событие со статусом «Ошибка». Посещаемость затронутых занятий пересчитается."
              confirmLabel="Обработать заново"
              onConfirm={async () => {
                const res = await reprocessFailed.mutateAsync()
                const count = res?.count ?? 0
                if (count > 0) toast.success(`Отправлено на повторную обработку: ${pluralize(count, WORDS.event)}`)
                else toast.info('Событий с ошибкой нет — повторять нечего')
              }}
            />
          </>
        }
      />

      <div className="flex flex-col gap-3 md:flex-row md:items-end">
        <div className="flex flex-col gap-1.5 md:w-52">
          <Label htmlFor="events-status">Статус</Label>
          <Select value={status ?? ALL} onValueChange={(v) => setParam('status', v === ALL ? undefined : v)}>
            <SelectTrigger id="events-status" className="h-9 w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent position="popper">
              <SelectItem value={ALL}>Все статусы</SelectItem>
              {EVENT_PROCESSING_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {eventStatusMeta[s].label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex flex-col gap-1.5 md:w-64">
          <Label htmlFor="events-type">Тип события</Label>
          <Select value={type ?? ALL} onValueChange={(v) => setParam('type', v === ALL ? undefined : v)}>
            <SelectTrigger id="events-type" className="h-9 w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent position="popper">
              <SelectItem value={ALL}>Все типы</SelectItem>
              {EVENT_TYPE_OPTIONS.map((t) => (
                <SelectItem key={t} value={t}>
                  {zoomEventTypeLabel(t)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <Label htmlFor="events-search">Поиск</Label>
          <div className="relative">
            <Search
              className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <Input
              id="events-search"
              type="search"
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder="Имя участника, номер или UUID конференции"
              className="h-9 pl-8"
            />
          </div>
        </div>
        {filtered && (
          <Button variant="ghost" className="h-9 self-start md:self-auto" onClick={resetFilters}>
            Сбросить фильтры
          </Button>
        )}
      </div>

      {events.data ? (
        items.length === 0 ? (
          filtered ? (
            <EmptyState
              icon={SearchX}
              title="Ничего не нашлось"
              text="Попробуйте другой статус, тип или запрос."
              action={
                <Button variant="outline" onClick={resetFilters}>
                  Сбросить фильтры
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={ScrollText}
              title="Событий пока нет"
              text="Когда Zoom пришлёт первое событие, оно появится здесь. Если занятие уже идёт, проверьте настройку подключения."
              action={
                <Button asChild variant="outline">
                  <Link to="/zoom">Подключение к Zoom</Link>
                </Button>
              }
            />
          )
        ) : (
          <section aria-label="События" className="flex flex-col gap-3">
            <div
              className={cn(
                'overflow-hidden rounded-xl ring-1 ring-foreground/10 transition-opacity',
                events.isPlaceholderData && 'opacity-60',
              )}
              aria-busy={events.isPlaceholderData || undefined}
            >
              <Table>
                <TableHeader className="bg-muted/40">
                  <TableRow>
                    <TableHead className="pl-4">Получено</TableHead>
                    <TableHead>Событие</TableHead>
                    <TableHead>Участник</TableHead>
                    <TableHead>Конференция</TableHead>
                    <TableHead>Статус</TableHead>
                    <TableHead className="pr-4 text-right">Попытки</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {items.map((ev) => (
                    <TableRow
                      key={ev.id}
                      className="cursor-pointer"
                      onClick={() => openEvent(ev)}
                      data-state={sheetOpen && selected?.id === ev.id ? 'selected' : undefined}
                    >
                      <TableCell className="pl-4 tabular-nums">{formatEventTime(ev.received_at)}</TableCell>
                      <TableCell>
                        <button
                          type="button"
                          className="rounded-sm text-left font-medium outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
                          onClick={(e) => {
                            e.stopPropagation()
                            openEvent(ev)
                          }}
                          aria-label={`${zoomEventTypeLabel(ev.event_type)}, №${ev.id}: подробнее`}
                        >
                          {zoomEventTypeLabel(ev.event_type)}
                        </button>
                        <span className="block font-mono text-xs text-muted-foreground">{ev.event_type}</span>
                      </TableCell>
                      <TableCell className="max-w-56 truncate" title={ev.participant_name || undefined}>
                        {ev.participant_name || <span className="text-muted-foreground">{EMPTY}</span>}
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {ev.meeting_id ? formatMeetingId(ev.meeting_id) : <span className="text-muted-foreground">{EMPTY}</span>}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <StatusBadge kind="event" status={ev.processing_status} />
                          {ev.error && (
                            <span className="max-w-64 truncate text-xs text-destructive" title={ev.error}>
                              {ev.error}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell className="pr-4 text-right tabular-nums">{ev.attempts}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>

            <div className="flex flex-col items-center gap-2">
              {events.hasNextPage ? (
                <Button
                  variant="outline"
                  size="lg"
                  onClick={() => events.fetchNextPage()}
                  // While another filter loads, the rows on screen belong to the old one.
                  disabled={events.isFetchingNextPage || events.isPlaceholderData}
                >
                  {events.isFetchingNextPage && <Loader2 className="animate-spin" aria-hidden="true" />}
                  Показать ещё
                </Button>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Показаны все события{filtered ? ' по этим условиям' : ''}: {items.length}
                </p>
              )}
              {events.isFetchNextPageError && (
                <p role="alert" className="text-sm text-destructive">
                  Не удалось загрузить следующие события. Нажмите «Показать ещё», чтобы попробовать снова.
                </p>
              )}
            </div>
          </section>
        )
      ) : events.isError ? (
        <ErrorState error={events.error} onRetry={() => events.refetch()} retrying={events.isFetching} />
      ) : (
        <LoadingState variant="table" rows={8} />
      )}

      <EventDetailsSheet event={selected} open={sheetOpen} onOpenChange={setSheetOpen} />
    </Page>
  )
}

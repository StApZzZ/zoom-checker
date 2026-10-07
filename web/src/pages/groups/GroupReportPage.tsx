import { useId, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { CalendarRange, CalendarX2, Check, Download, FileSpreadsheet, Loader2, Pencil, SearchX, Users, VideoOff, X } from 'lucide-react'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { ReportMatrix } from '@/components/groups/ReportMatrix'
import {
  boundsLabel,
  boundsToRange,
  DEFAULT_PERIOD,
  exportRange,
  isWithoutZoomData,
  parsePeriod,
  PERIOD_OPTIONS,
  periodBounds,
  reportColumns,
  type ReportPeriod,
} from '@/components/groups/report-utils'
import { ApiError } from '@/lib/api'
import { pluralize, WORDS } from '@/lib/format'
import { useGroupReport, useGroupReportExport } from '@/lib/queries'
import type { ExportFormat } from '@/lib/types'

function parseId(value: string | undefined): number | undefined {
  return value && /^\d+$/.test(value) && Number(value) > 0 ? Number(value) : undefined
}

function Legend({ noZoom }: { noZoom: boolean }) {
  return (
    <ul className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground" aria-label="Обозначения">
      <li className="flex items-center gap-1.5">
        <span className="flex size-5 items-center justify-center rounded bg-emerald-50 text-emerald-700 dark:bg-emerald-400/10 dark:text-emerald-300">
          <Check className="size-3.5" aria-hidden="true" />
        </span>
        был
      </li>
      <li className="flex items-center gap-1.5">
        <span className="flex size-5 items-center justify-center rounded bg-red-50 text-red-700 dark:bg-red-400/10 dark:text-red-300">
          <X className="size-3.5" aria-hidden="true" />
        </span>
        не был
      </li>
      <li className="flex items-center gap-1.5">
        <Pencil className="size-3" aria-hidden="true" />
        отмечено вручную
      </li>
      <li className="flex items-center gap-1.5">
        <span aria-hidden="true">—</span>
        нет данных (студента ещё не было в группе)
      </li>
      {noZoom && (
        <li className="flex items-center gap-1.5">
          <VideoOff className="size-3" aria-hidden="true" />
          из Zoom данных не было
        </li>
      )}
      <li>Нажмите на клетку, чтобы увидеть время в Zoom и опоздание.</li>
    </ul>
  )
}

export default function GroupReportPage() {
  const id = useId()
  const { id: idParam } = useParams()
  const groupId = parseId(idParam)
  const [params, setParams] = useSearchParams()
  const period = parsePeriod(params.get('period'))
  const customFrom = params.get('from') ?? ''
  const customTo = params.get('to') ?? ''
  // "This month" is computed once per visit; a report open over midnight keeps its range.
  const [now] = useState(() => new Date())
  const bounds = useMemo(
    () => periodBounds(period, now, { from: customFrom, to: customTo }),
    [period, now, customFrom, customTo],
  )
  const range = useMemo(() => boundsToRange(bounds, now), [bounds, now])
  const report = useGroupReport(groupId, range)
  const exportFile = useGroupReportExport(groupId ?? 0)

  const updateParams = (patch: Record<string, string | null>) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        for (const [key, value] of Object.entries(patch)) {
          if (value) next.set(key, value)
          else next.delete(key)
        }
        return next
      },
      { replace: true },
    )
  const setPeriod = (value: ReportPeriod) =>
    updateParams({ period: value === DEFAULT_PERIOD ? null : value, ...(value === 'custom' ? {} : { from: null, to: null }) })

  const data = report.data
  const groupName = data?.group.name
  const columns = data ? reportColumns(data, now) : []
  const hiddenPlanned = data ? data.lessons.length - columns.length : 0
  // Placeholder data belongs to the previous period: do not export it under the new one.
  const canExport = !!data && !report.isPlaceholderData

  const download = (format: ExportFormat) => {
    if (!groupId || !data) return
    exportFile.download({ format, range: exportRange(bounds, range, data.lessons) })
  }
  const exporting = (format: ExportFormat) => exportFile.pending.includes(format)

  if (groupId === undefined) {
    return (
      <Page>
        <PageHeader title="Группа не найдена" back={{ to: '/groups', label: 'Все группы' }} />
        <EmptyState
          icon={SearchX}
          title="Такой группы нет"
          text="Возможно, ссылка устарела. Откройте группу из списка."
          action={
            <Button asChild variant="outline">
              <Link to="/groups">К списку групп</Link>
            </Button>
          }
        />
      </Page>
    )
  }

  return (
    <Page width="wide">
      <PageHeader
        title={groupName ? 'Сводка посещаемости' : `Отчёт по группе №${groupId}`}
        documentTitle={groupName ? `Посещаемость · ${groupName}` : 'Отчёт по группе'}
        eyebrow={groupName ? `Группа «${groupName}»` : undefined}
        description="Кто был на занятиях группы за выбранный период."
        back={{ to: `/groups/${groupId}`, label: groupName ?? 'К группе' }}
        actions={
          <>
            <Button
              size="lg"
              onClick={() => download('xlsx')}
              disabled={!canExport || exporting('xlsx')}
              aria-busy={exporting('xlsx') || undefined}
            >
              {exporting('xlsx') ? (
                <Loader2 className="animate-spin" aria-hidden="true" />
              ) : (
                <FileSpreadsheet aria-hidden="true" />
              )}
              Скачать Excel
            </Button>
            <Button
              size="lg"
              variant="outline"
              onClick={() => download('csv')}
              disabled={!canExport || exporting('csv')}
              aria-busy={exporting('csv') || undefined}
            >
              {exporting('csv') ? <Loader2 className="animate-spin" aria-hidden="true" /> : <Download aria-hidden="true" />}
              Скачать CSV
            </Button>
          </>
        }
      />

      <section className="flex flex-col gap-3" aria-label="Период">
        <div className="flex flex-col gap-3 lg:flex-row lg:items-end lg:justify-between">
          <div className="-mx-4 overflow-x-auto px-4 sm:mx-0 sm:px-0">
            <ToggleGroup
              type="single"
              variant="outline"
              spacing={0}
              value={period}
              onValueChange={(v) => v && setPeriod(v as ReportPeriod)}
              aria-label="Период отчёта"
            >
              {PERIOD_OPTIONS.map((o) => (
                <ToggleGroupItem key={o.value} value={o.value} className="px-3">
                  {o.label}
                </ToggleGroupItem>
              ))}
              <ToggleGroupItem value="custom" className="px-3">
                <CalendarRange aria-hidden="true" />
                Свой период
              </ToggleGroupItem>
            </ToggleGroup>
          </div>
          {period === 'custom' && (
            <div className="flex flex-wrap items-end gap-3">
              <div className="flex flex-col gap-1">
                <Label htmlFor={`${id}-from`} className="text-xs text-muted-foreground">
                  С
                </Label>
                <Input
                  id={`${id}-from`}
                  type="date"
                  value={customFrom}
                  max={customTo || undefined}
                  onChange={(e) => updateParams({ from: e.target.value })}
                  className="h-9 w-40"
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor={`${id}-to`} className="text-xs text-muted-foreground">
                  По
                </Label>
                <Input
                  id={`${id}-to`}
                  type="date"
                  value={customTo}
                  min={customFrom || undefined}
                  onChange={(e) => updateParams({ to: e.target.value })}
                  className="h-9 w-40"
                />
              </div>
            </div>
          )}
        </div>
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground" aria-live="polite">
          <span>
            Период: <span className="text-foreground">{boundsLabel(bounds)}</span>
          </span>
          {data && columns.length > 0 && <span>· {pluralize(columns.length, WORDS.lesson)}</span>}
          {hiddenPlanned > 0 && columns.length > 0 && (
            <span>· ещё {pluralize(hiddenPlanned, WORDS.lesson)} впереди — по ним пока нет данных</span>
          )}
          {report.isFetching && report.isPlaceholderData && (
            <span className="flex items-center gap-1">
              <Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
              обновляем…
            </span>
          )}
        </p>
      </section>

      {data ? (
        columns.length === 0 ? (
          <EmptyState
            icon={CalendarX2}
            title="За этот период занятий нет"
            text={
              hiddenPlanned > 0
                ? 'Занятия ещё не прошли — посещаемость появится после первого занятия.'
                : period === 'all'
                  ? 'У группы ещё не было занятий. Создайте занятие на главной странице.'
                  : 'Выберите другой период — например, «Всё время».'
            }
            action={
              period !== 'all' ? (
                <Button variant="outline" onClick={() => setPeriod('all')}>
                  Показать всё время
                </Button>
              ) : undefined
            }
          />
        ) : data.students.length === 0 ? (
          <EmptyState
            icon={Users}
            title="В группе нет студентов"
            text="Добавьте студентов на странице группы — и здесь появится их посещаемость."
            action={
              <Button asChild variant="outline">
                <Link to={`/groups/${groupId}`}>К группе</Link>
              </Button>
            }
          />
        ) : (
          <div className="flex flex-col gap-3">
            <Legend noZoom={columns.some((l) => isWithoutZoomData(l, now))} />
            <ReportMatrix students={data.students} lessons={columns} now={now} />
          </div>
        )
      ) : report.error instanceof ApiError && report.error.isNotFound ? (
        <EmptyState
          icon={SearchX}
          title="Такой группы нет"
          text="Возможно, ссылка устарела. Откройте группу из списка."
          action={
            <Button asChild variant="outline">
              <Link to="/groups">К списку групп</Link>
            </Button>
          }
        />
      ) : report.isError ? (
        <ErrorState error={report.error} onRetry={() => report.refetch()} retrying={report.isFetching} />
      ) : (
        <LoadingState variant="table" rows={8} label="Загружаем отчёт…" />
      )}
    </Page>
  )
}

import { useMemo, useState } from 'react'
import { CircleCheck, Link2, Link2Off, Radio, Video } from 'lucide-react'
import { Link } from 'react-router'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { ToneBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { useNow } from '@/hooks/useNow'
import { formatDate, formatMeetingId, formatTime, formatWeekdayDate } from '@/lib/format'
import { useLinkSession, useZoomSessions } from '@/lib/queries'
import type { ZoomSession, ZoomSessionLesson } from '@/lib/types'
import { LinkSessionDialog } from '@/pages/zoom/LinkSessionDialog'
import { isRecent, sessionTime } from '@/pages/zoom/linkCandidates'

type Mode = 'unlinked' | 'all'

const PAGE = 50
/** Sessions loaded per mode (the server's maximum); the table pages through them locally. */
const SESSIONS_LIMIT = 500

/** The linked lesson, from the summary the sessions list carries (no request per row). */
function LinkedLesson({ lessonId, lesson }: { lessonId: number; lesson: ZoomSessionLesson | null | undefined }) {
  return (
    <Link
      to={`/lessons/${lessonId}`}
      className="flex min-w-0 flex-col rounded-sm outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <span className="truncate font-medium">{lesson?.title || 'Открыть занятие'}</span>
      {lesson && (
        <span className="truncate text-xs text-muted-foreground">
          {lesson.group_name} · {formatDate(lesson.planned_start_at)}
        </span>
      )}
    </Link>
  )
}

function When({ session }: { session: ZoomSession }) {
  const start = sessionTime(session)
  if (!start) return <span className="text-muted-foreground">—</span>
  return (
    <span className="flex flex-col">
      <span>{formatWeekdayDate(start)}</span>
      <span className="text-xs text-muted-foreground tabular-nums">
        {formatTime(start)}
        {session.ended_at ? `–${formatTime(session.ended_at)}` : ''}
      </span>
    </span>
  )
}

/** "Несвязанные сессии Zoom": sessions without a lesson, with link / unlink actions. */
export function ZoomSessionsSection() {
  const now = useNow(60_000)
  const [mode, setMode] = useState<Mode>('unlinked')
  const [limit, setLimit] = useState(PAGE)
  const sessions = useZoomSessions({ unlinked: mode === 'unlinked', limit: SESSIONS_LIMIT })
  const [linking, setLinking] = useState<ZoomSession | null>(null)
  const [unlinking, setUnlinking] = useState<ZoomSession | null>(null)
  const unlink = useLinkSession({ successMessage: 'Конференция отвязана от занятия' })

  const sorted = useMemo(
    () =>
      (sessions.data ?? [])
        .slice()
        .sort((a, b) => (sessionTime(b)?.getTime() ?? 0) - (sessionTime(a)?.getTime() ?? 0) || b.id - a.id),
    [sessions.data],
  )
  const shown = sorted.slice(0, limit)

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle className="text-lg">
              <h2>Несвязанные сессии Zoom</h2>
            </CardTitle>
            <CardDescription className="max-w-2xl text-pretty">
              Конференции, которые система не смогла сопоставить с занятием: например, занятие не создано или в нём
              указан другой номер конференции. Привяжите конференцию к занятию — посещаемость пересчитается.
            </CardDescription>
          </div>
          <ToggleGroup
            type="single"
            variant="outline"
            value={mode}
            onValueChange={(v) => {
              if (!v) return
              setMode(v as Mode)
              setLimit(PAGE)
            }}
            aria-label="Какие конференции показать"
          >
            <ToggleGroupItem value="unlinked">Без занятия</ToggleGroupItem>
            <ToggleGroupItem value="all">Все</ToggleGroupItem>
          </ToggleGroup>
        </div>
      </CardHeader>
      <CardContent>
        {sessions.data ? (
          sorted.length === 0 ? (
            mode === 'unlinked' ? (
              <EmptyState
                icon={CircleCheck}
                title="Все конференции привязаны к занятиям"
                text="Если Zoom пришлёт события конференции, для которой нет занятия, она появится здесь."
              />
            ) : (
              <EmptyState
                icon={Video}
                title="Конференций пока не было"
                text="Они появятся, когда Zoom пришлёт первые события."
              />
            )
          ) : (
            <div className="flex flex-col gap-3">
              <div className="overflow-hidden rounded-xl ring-1 ring-foreground/10">
                <Table>
                  <TableHeader className="bg-muted/40">
                    <TableRow>
                      <TableHead className="pl-4">Конференция</TableHead>
                      <TableHead>Когда</TableHead>
                      <TableHead>Занятие</TableHead>
                      <TableHead className="pr-4 text-right">
                        <span className="sr-only">Действия</span>
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {shown.map((s) => (
                      <TableRow key={s.id}>
                        <TableCell className="pl-4">
                          <span className="flex min-w-0 flex-col gap-0.5">
                            <span className="font-medium tabular-nums">{formatMeetingId(s.meeting_id)}</span>
                            <span className="max-w-72 truncate text-xs text-muted-foreground" title={s.topic || undefined}>
                              {s.topic || 'Без темы'}
                            </span>
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col items-start gap-1">
                            <When session={s} />
                            {!s.ended_at &&
                              (isRecent(s, now) ? (
                                <ToneBadge tone="success" icon={Radio} size="sm">
                                  Идёт
                                </ToneBadge>
                              ) : (
                                <span className="text-xs text-muted-foreground">конец неизвестен</span>
                              ))}
                          </div>
                        </TableCell>
                        <TableCell className="max-w-64">
                          {s.lesson_id !== null ? (
                            <LinkedLesson lessonId={s.lesson_id} lesson={s.lesson} />
                          ) : (
                            <span className="flex flex-col">
                              <span className="text-muted-foreground">Не привязана</span>
                              {s.link_mode === 'manual' && (
                                <span className="text-xs text-muted-foreground">отвязана вручную</span>
                              )}
                            </span>
                          )}
                        </TableCell>
                        <TableCell className="pr-4 text-right">
                          <div className="flex justify-end gap-2">
                            {s.lesson_id !== null ? (
                              <>
                                <Button variant="ghost" onClick={() => setLinking(s)}>
                                  Другое занятие
                                </Button>
                                <Button variant="outline" onClick={() => setUnlinking(s)}>
                                  <Link2Off aria-hidden="true" />
                                  Отвязать
                                </Button>
                              </>
                            ) : (
                              <Button onClick={() => setLinking(s)}>
                                <Link2 aria-hidden="true" />
                                Привязать к занятию
                              </Button>
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
              {sorted.length > shown.length && (
                <div className="flex justify-center">
                  <Button variant="outline" onClick={() => setLimit((n) => n + PAGE)}>
                    Показать ещё
                  </Button>
                </div>
              )}
              {sorted.length >= SESSIONS_LIMIT && (
                <p className="text-center text-sm text-muted-foreground">
                  Здесь только последние {SESSIONS_LIMIT} конференций — более старые не показываются.
                </p>
              )}
            </div>
          )
        ) : sessions.isError ? (
          <ErrorState
            size="compact"
            error={sessions.error}
            onRetry={() => sessions.refetch()}
            retrying={sessions.isFetching}
          />
        ) : (
          <LoadingState variant="table" rows={3} />
        )}
      </CardContent>

      <LinkSessionDialog session={linking} onOpenChange={(open) => !open && setLinking(null)} />

      <ConfirmDialog
        open={unlinking !== null}
        onOpenChange={(open) => !open && setUnlinking(null)}
        title="Отвязать конференцию от занятия?"
        description="Посещаемость занятия пересчитается без участников этой конференции. Сама она больше не привяжется — при необходимости привяжите её снова вручную."
        confirmLabel="Отвязать"
        destructive
        onConfirm={() => (unlinking ? unlink.mutateAsync({ sessionId: unlinking.id, lessonId: null }) : undefined)}
      />
    </Card>
  )
}

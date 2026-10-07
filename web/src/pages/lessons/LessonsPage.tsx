// "/" — Занятия: today's lessons with live counts, upcoming (14 days) and past (30 days).

import { useMemo, useState } from 'react'
import { CalendarPlus, CalendarX2, KeyRound, Search, SearchX, X } from 'lucide-react'
import { useSearchParams } from 'react-router'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { CreateLessonDialog } from '@/components/lessons/CreateLessonDialog'
import { LoadingState } from '@/components/LoadingState'
import { toneBadgeClass } from '@/components/tone'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { useNow } from '@/hooks/useNow'
import { useOptionalAuth } from '@/lib/auth'
import { formatWeekdayDate, pluralize, toDateInputValue, WORDS } from '@/lib/format'
import { useGroups, useLessons } from '@/lib/queries'
import { useGlobalStream } from '@/lib/sse'
import { cn } from '@/lib/utils'
import { useZoomConnection } from '@/lib/zoomConnection'
import { GettingStarted, PastLessonRow, TodayLessonCard, UpcomingLessonRow } from '@/pages/lessons/list/LessonCards'
import { listWindow, matchesLesson, splitLessons } from '@/pages/lessons/list/lessonSections'

const ALL_GROUPS = 'all'
/** Past lessons shown before "Показать все". */
const PAST_PREVIEW = 10

function SectionTitle({ id, children, extra }: { id: string; children: string; extra?: string }) {
  return (
    <h2 id={id} className="flex flex-wrap items-baseline gap-x-2 text-lg font-semibold">
      {children}
      {extra && <span className="text-sm font-normal text-muted-foreground first-letter:uppercase">{extra}</span>}
    </h2>
  )
}

/** Teachers cannot fix the Zoom connection, but must know nothing will be recorded. */
function ZoomNotConnectedNotice() {
  return (
    <p
      role="status"
      className={cn('flex items-start gap-2.5 rounded-xl px-4 py-3 text-sm font-medium ring-1 ring-inset', toneBadgeClass.danger)}
    >
      <KeyRound className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
      Сервис пока не подключён к Zoom — отметки не появятся. Сообщите администратору.
    </p>
  )
}

export default function LessonsPage() {
  // Live counts on the cards: every change of any lesson refreshes the list.
  useGlobalStream()
  // Admins get the same warning (with the way to fix it) above every page from the app shell.
  const isAdmin = useOptionalAuth()?.isAdmin ?? false
  const zoom = useZoomConnection(!isAdmin)
  const now = useNow(60_000)
  const dayKey = toDateInputValue(now)
  // The request window moves once a day, not every minute.
  const range = useMemo(() => listWindow(new Date(`${dayKey}T12:00:00`)), [dayKey])

  const [params, setParams] = useSearchParams()
  const groupParam = params.get('group') ?? ''
  const groupId = /^\d+$/.test(groupParam) ? Number(groupParam) : null
  const search = params.get('q') ?? ''
  const filtersActive = groupId !== null || search.trim() !== ''

  const setParam = (key: string, value: string) => {
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true },
    )
  }
  const resetFilters = () => setParams({}, { replace: true })

  const groups = useGroups('active')
  const lessons = useLessons(range)
  const [createOpen, setCreateOpen] = useState(false)
  const [showAllPast, setShowAllPast] = useState(false)

  const sections = useMemo(() => {
    if (!lessons.data) return null
    const visible = lessons.data.filter((l) => matchesLesson(l, { groupId, search }))
    return splitLessons(visible, new Date(now))
  }, [lessons.data, groupId, search, now])

  const hasGroups = (groups.data?.length ?? 0) > 0
  const total = lessons.data?.length ?? 0
  const nothingFound = sections && sections.today.length + sections.upcoming.length + sections.past.length === 0
  const pastShown = sections ? (showAllPast ? sections.past : sections.past.slice(0, PAST_PREVIEW)) : []

  const newLessonButton = (
    <Button size="lg" className="h-11 px-4 text-base" onClick={() => setCreateOpen(true)}>
      <CalendarPlus aria-hidden="true" />
      Новое занятие
    </Button>
  )

  return (
    <Page>
      <PageHeader
        title="Занятия"
        description="Откройте занятие во время урока — посещаемость будет отмечаться сама."
        actions={newLessonButton}
      />
      <CreateLessonDialog open={createOpen} onOpenChange={setCreateOpen} groupId={groupId ?? undefined} />
      {zoom === 'not_configured' && <ZoomNotConnectedNotice />}

      {lessons.data ? (
        total === 0 && !filtersActive ? (
          // Wait for the groups: the first step's state (done / to do) depends on them.
          groups.isPending ? (
            <LoadingState variant="cards" rows={3} label="Загружаем занятия…" />
          ) : (
            <GettingStarted hasGroups={hasGroups} onCreate={() => setCreateOpen(true)} />
          )
        ) : (
          <>
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center" role="search" aria-label="Поиск занятий">
              <Select value={groupId !== null ? String(groupId) : ALL_GROUPS} onValueChange={(v) => setParam('group', v === ALL_GROUPS ? '' : v)}>
                <SelectTrigger className="h-10 w-full sm:w-64" aria-label="Группа">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent position="popper">
                  <SelectItem value={ALL_GROUPS}>Все группы</SelectItem>
                  {groups.data?.map((g) => (
                    <SelectItem key={g.id} value={String(g.id)}>
                      {g.name}
                    </SelectItem>
                  ))}
                  {groupId !== null && groups.data && !groups.data.some((g) => g.id === groupId) && (
                    <SelectItem value={String(groupId)}>Выбранная группа</SelectItem>
                  )}
                </SelectContent>
              </Select>
              <div className="relative w-full sm:max-w-sm">
                <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
                <Input
                  type="search"
                  value={search}
                  onChange={(e) => setParam('q', e.target.value)}
                  placeholder="Поиск по названию или группе"
                  aria-label="Поиск по названию или группе"
                  className="h-10 pl-9"
                />
              </div>
              {filtersActive && (
                <Button variant="ghost" onClick={resetFilters} className="w-fit">
                  <X aria-hidden="true" />
                  Сбросить
                </Button>
              )}
            </div>

            {nothingFound ? (
              <EmptyState
                icon={SearchX}
                title="Ничего не нашлось"
                text="Попробуйте другое название или выберите «Все группы»."
                action={
                  <Button variant="outline" onClick={resetFilters}>
                    Сбросить фильтр
                  </Button>
                }
              />
            ) : (
              sections && (
                <>
                  <section aria-labelledby="today" className="flex flex-col gap-3">
                    <SectionTitle id="today" extra={formatWeekdayDate(now, new Date(now))}>
                      Сегодня
                    </SectionTitle>
                    {sections.today.length > 0 ? (
                      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                        {sections.today.map((lesson) => (
                          <TodayLessonCard key={lesson.id} lesson={lesson} now={now} />
                        ))}
                      </div>
                    ) : (
                      <EmptyState
                        size="compact"
                        icon={CalendarX2}
                        title="Сегодня занятий нет"
                        text="Если урок всё-таки есть, создайте занятие — это быстро."
                        action={
                          <Button variant="outline" onClick={() => setCreateOpen(true)}>
                            <CalendarPlus aria-hidden="true" />
                            Новое занятие
                          </Button>
                        }
                        className="rounded-xl border border-dashed"
                      />
                    )}
                  </section>

                  <section aria-labelledby="upcoming" className="flex flex-col gap-2">
                    <SectionTitle id="upcoming" extra="на две недели вперёд">
                      Предстоящие
                    </SectionTitle>
                    {sections.upcoming.length > 0 ? (
                      <ul className="divide-y rounded-xl bg-card ring-1 ring-foreground/10">
                        {sections.upcoming.map((lesson) => (
                          <UpcomingLessonRow key={lesson.id} lesson={lesson} now={now} />
                        ))}
                      </ul>
                    ) : (
                      <p className="text-sm text-muted-foreground">На ближайшие две недели занятий нет.</p>
                    )}
                  </section>

                  {sections.past.length > 0 && (
                    <section aria-labelledby="past" className="flex flex-col gap-2">
                      <SectionTitle id="past" extra="за последние 30 дней">
                        Прошедшие
                      </SectionTitle>
                      <ul className="divide-y rounded-xl bg-card ring-1 ring-foreground/10">
                        {pastShown.map((lesson) => (
                          <PastLessonRow key={lesson.id} lesson={lesson} now={now} />
                        ))}
                      </ul>
                      {sections.past.length > PAST_PREVIEW && (
                        <Button variant="ghost" className="w-fit" onClick={() => setShowAllPast((v) => !v)}>
                          {showAllPast
                            ? 'Свернуть'
                            : `Показать все (${pluralize(sections.past.length, WORDS.lesson)})`}
                        </Button>
                      )}
                    </section>
                  )}
                </>
              )
            )}
          </>
        )
      ) : lessons.isError ? (
        <ErrorState error={lessons.error} onRetry={() => void lessons.refetch()} retrying={lessons.isFetching} />
      ) : (
        <LoadingState variant="cards" rows={3} label="Загружаем занятия…" />
      )}
    </Page>
  )
}

import type { ReactNode } from 'react'
import {
  CalendarPlus,
  ChevronRight,
  CircleCheck,
  CircleHelp,
  Clock,
  Radio,
  TriangleAlert,
  UserCheck,
  Users,
  Video,
  type LucideIcon,
} from 'lucide-react'
import { isSameDay } from 'date-fns'
import { Link } from 'react-router'

import { StatusBadge, ToneBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { formatRelative, formatTime, formatTimeRange, formatWeekdayDate, pluralize, toDate, WORDS } from '@/lib/format'
import type { LessonSummary } from '@/lib/types'
import { cn } from '@/lib/utils'
import { isOverdue } from '@/pages/lessons/list/lessonSections'

const lessonUrl = (lesson: Pick<LessonSummary, 'id'>) => `/lessons/${lesson.id}`

function Stat({ icon: Icon, children, className }: { icon: LucideIcon; children: ReactNode; className?: string }) {
  return (
    <p className={cn('flex items-center gap-1.5 text-sm', className)}>
      <Icon className="size-4 shrink-0" aria-hidden="true" />
      <span>{children}</span>
    </p>
  )
}

/** Live numbers / start time in a "Сегодня" card. */
function TodayCardBody({ lesson, now }: { lesson: LessonSummary; now: number }) {
  const { counts } = lesson
  if (lesson.status === 'active' && isOverdue(lesson, now)) {
    // "N из M сейчас в Zoom" would be stale numbers from a meeting whose end never arrived.
    return (
      <div className="flex flex-col gap-2">
        <Stat icon={TriangleAlert} className="font-medium text-amber-800 dark:text-amber-200">
          Zoom не сообщил об окончании — откройте занятие и завершите его
        </Stat>
        <Stat icon={UserCheck} className="text-muted-foreground">
          Были хотя бы раз: {counts.seen}
        </Stat>
      </div>
    )
  }
  if (lesson.status === 'active') {
    return (
      <div className="flex flex-col gap-2">
        <p className="flex items-baseline gap-1.5">
          <span className="text-3xl leading-none font-semibold tracking-tight text-emerald-700 tabular-nums dark:text-emerald-400">
            {counts.online}
          </span>
          <span className="text-sm text-muted-foreground">
            из {counts.in_group} сейчас в Zoom
          </span>
        </p>
        <Stat icon={UserCheck} className="text-muted-foreground">
          Были хотя бы раз: {counts.seen}
        </Stat>
        {counts.unidentified > 0 && (
          <Stat icon={TriangleAlert} className="font-medium text-amber-800 dark:text-amber-200">
            Не идентифицированы: {counts.unidentified} — откройте занятие, чтобы разобраться
          </Stat>
        )}
      </div>
    )
  }
  if (lesson.status === 'finished') {
    return (
      <div className="flex flex-col gap-2">
        <Stat icon={CircleCheck} className="text-muted-foreground">
          Были на занятии:{' '}
          <span className="font-medium text-foreground tabular-nums">
            {counts.present} из {counts.in_group}
          </span>
        </Stat>
        {counts.unidentified > 0 && (
          <Stat icon={TriangleAlert} className="font-medium text-amber-800 dark:text-amber-200">
            Не идентифицированы: {counts.unidentified} — проверьте, не студенты ли это
          </Stat>
        )}
      </div>
    )
  }
  const start = toDate(lesson.planned_start_at)
  const end = toDate(lesson.planned_end_at)
  const startsInFuture = start ? start.getTime() > now : false
  // Planned slot is over and Zoom never reported the meeting: no point in "waiting".
  const slotOver = end ? end.getTime() <= now : false
  return (
    <div className="flex flex-col gap-2">
      <Stat icon={Clock} className="text-muted-foreground">
        {startsInFuture ? (
          <>
            Начало в {formatTime(start)} · <span className="text-foreground">{formatRelative(start, now)}</span>
          </>
        ) : slotOver ? (
          <>Из Zoom никто не подключался. Если урок был, проверьте ссылку на конференцию.</>
        ) : (
          <>Должно было начаться в {formatTime(start)} — ждём подключений в Zoom</>
        )}
      </Stat>
      <Stat icon={Users} className="text-muted-foreground">
        В группе {pluralize(counts.in_group, WORDS.student)}
      </Stat>
    </div>
  )
}

/** Big card of a today's lesson; active lessons are highlighted. */
export function TodayLessonCard({ lesson, now }: { lesson: LessonSummary; now: number }) {
  // An overdue lesson is not highlighted as live: it most likely ended long ago.
  const active = lesson.status === 'active' && !isOverdue(lesson, now)
  const titleId = `lesson-card-${lesson.id}`
  const start = toDate(lesson.planned_start_at)
  const startedOtherDay = start !== null && !isSameDay(start, now)
  return (
    <article
      aria-labelledby={titleId}
      className={cn(
        'flex min-w-0 flex-col gap-4 rounded-xl bg-card p-4 text-card-foreground ring-1 ring-foreground/10',
        active && 'bg-emerald-50/60 ring-2 ring-emerald-600/40 dark:bg-emerald-400/5 dark:ring-emerald-400/40',
      )}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <StatusBadge kind="lesson" status={lesson.status} size={active ? 'lg' : 'md'} />
        <span className="text-sm font-medium text-muted-foreground tabular-nums">
          {/* A lesson still running since an earlier day (nobody finished it) says which day. */}
          {startedOtherDay && (
            <>
              <span className="inline-block first-letter:uppercase">
                {formatWeekdayDate(lesson.planned_start_at, new Date(now))},
              </span>{' '}
            </>
          )}
          {formatTimeRange(lesson.planned_start_at, lesson.planned_end_at)}
        </span>
      </div>
      <div className="flex min-w-0 flex-col gap-0.5">
        <h3 id={titleId} className="text-lg leading-snug font-semibold break-words">
          {lesson.title}
        </h3>
        <p className="truncate text-sm text-muted-foreground">{lesson.group.name}</p>
      </div>
      <TodayCardBody lesson={lesson} now={now} />
      <Button
        asChild
        size="lg"
        variant={active ? 'default' : 'outline'}
        className="mt-auto h-11 w-full text-base"
      >
        <Link to={lessonUrl(lesson)} aria-label={`Открыть занятие «${lesson.title}»`}>
          {active && <Radio aria-hidden="true" />}
          Открыть
        </Link>
      </Button>
    </article>
  )
}

const rowLinkClass =
  'flex min-h-14 items-center gap-3 rounded-lg px-3 py-2.5 outline-none transition-colors hover:bg-muted/60 focus-visible:ring-3 focus-visible:ring-ring/50'

function WhenCell({ lesson, now }: { lesson: LessonSummary; now: number }) {
  return (
    <div className="flex w-32 shrink-0 flex-col sm:w-40">
      <span className="text-sm font-medium first-letter:uppercase">
        {formatWeekdayDate(lesson.planned_start_at, new Date(now))}
      </span>
      <span className="text-xs text-muted-foreground tabular-nums">
        {formatTimeRange(lesson.planned_start_at, lesson.planned_end_at)}
      </span>
    </div>
  )
}

function TitleCell({ lesson }: { lesson: LessonSummary }) {
  return (
    <div className="flex min-w-0 flex-1 flex-col">
      <span className="truncate font-medium">{lesson.title}</span>
      <span className="truncate text-sm text-muted-foreground">{lesson.group.name}</span>
    </div>
  )
}

/** One line of "Предстоящие". */
export function UpcomingLessonRow({ lesson, now }: { lesson: LessonSummary; now: number }) {
  return (
    <li>
      <Link to={lessonUrl(lesson)} className={rowLinkClass}>
        <WhenCell lesson={lesson} now={now} />
        <TitleCell lesson={lesson} />
        <ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      </Link>
    </li>
  )
}

/** One line of "Прошедшие": date, title, how many attended. */
export function PastLessonRow({ lesson, now }: { lesson: LessonSummary; now: number }) {
  const { counts } = lesson
  const noData = lesson.status === 'planned'
  return (
    <li>
      <Link to={lessonUrl(lesson)} className={rowLinkClass}>
        <WhenCell lesson={lesson} now={now} />
        <TitleCell lesson={lesson} />
        {noData ? (
          <ToneBadge tone="muted" icon={CircleHelp} title="Zoom не прислал данных об этой конференции" className="hidden sm:inline-flex">
            Нет данных из Zoom
          </ToneBadge>
        ) : (
          <>
            {counts.unidentified > 0 && (
              <ToneBadge
                tone="warning"
                icon={TriangleAlert}
                title="Участники Zoom, которых система не узнала: откройте занятие и укажите, кто это"
                className="hidden sm:inline-flex"
              >
                Не опознаны: {counts.unidentified}
              </ToneBadge>
            )}
            <span
              className="flex shrink-0 items-center gap-1 text-sm tabular-nums"
              aria-label={`Были ${counts.present} из ${counts.in_group}`}
            >
              <UserCheck className="size-4 text-muted-foreground" aria-hidden="true" />
              <span className="font-medium">{counts.present}</span>
              <span className="text-muted-foreground">из {counts.in_group}</span>
            </span>
          </>
        )}
        <ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
      </Link>
    </li>
  )
}

function Step({
  n,
  icon: Icon,
  title,
  text,
  done,
  action,
}: {
  n: number
  icon: LucideIcon
  title: string
  text: string
  done?: boolean
  action?: ReactNode
}) {
  return (
    <li className="flex flex-col gap-3 rounded-xl bg-card p-4 ring-1 ring-foreground/10">
      <div className="flex items-center gap-3">
        <span
          className={cn(
            'flex size-9 shrink-0 items-center justify-center rounded-full text-sm font-semibold',
            done
              ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-400/15 dark:text-emerald-300'
              : 'bg-muted text-foreground',
          )}
          aria-hidden="true"
        >
          {done ? <CircleCheck className="size-5" /> : n}
        </span>
        <Icon className="size-5 text-muted-foreground" aria-hidden="true" />
      </div>
      <div className="flex flex-col gap-1">
        <p className="font-medium">
          <span className="sr-only">Шаг {n}{done ? ' (готово)' : ''}: </span>
          {title}
        </p>
        <p className="text-sm text-pretty text-muted-foreground">{text}</p>
      </div>
      {action && <div className="mt-auto pt-1">{action}</div>}
    </li>
  )
}

/** Empty state of the lessons page: the three steps to the first live lesson. */
export function GettingStarted({ hasGroups, onCreate }: { hasGroups: boolean; onCreate: () => void }) {
  return (
    <section aria-labelledby="getting-started" className="flex flex-col gap-4 rounded-xl border border-dashed p-4 sm:p-6">
      <div className="flex flex-col gap-1">
        <h2 id="getting-started" className="text-lg font-semibold">
          Занятий пока нет — начните с трёх шагов
        </h2>
        <p className="text-sm text-muted-foreground">Это займёт несколько минут. Дальше всё будет отмечаться само.</p>
      </div>
      <ol className="grid gap-3 md:grid-cols-3">
        <Step
          n={1}
          icon={Users}
          title="Создайте группу"
          text="Добавьте студентов: можно вставить список из Excel или таблицы."
          done={hasGroups}
          action={
            <Button asChild variant={hasGroups ? 'outline' : 'default'} size="lg">
              <Link to="/groups">{hasGroups ? 'Мои группы' : 'Создать группу'}</Link>
            </Button>
          }
        />
        <Step
          n={2}
          icon={Video}
          title="Создайте занятие со ссылкой Zoom"
          text="Вставьте ссылку или приглашение из Zoom — номер конференции найдётся сам."
          action={
            hasGroups ? (
              <Button size="lg" onClick={onCreate}>
                <CalendarPlus aria-hidden="true" />
                Новое занятие
              </Button>
            ) : undefined
          }
        />
        <Step
          n={3}
          icon={Radio}
          title="Откройте его во время урока"
          text="Вы увидите, кто сейчас в Zoom, кто вышел и кто не подключался. Обновлять страницу не нужно."
        />
      </ol>
    </section>
  )
}

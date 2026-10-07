import { useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { Archive, ArchiveRestore, CalendarDays, Clock, FileUp, Loader2, NotebookText, Pencil, SearchX, Sheet, Users } from 'lucide-react'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { ToneBadge } from '@/components/StatusBadge'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EditGroupDialog } from '@/components/groups/EditGroupDialog'
import { GroupLessonsPanel } from '@/components/groups/GroupLessonsPanel'
import { ImportWizard } from '@/components/groups/ImportWizard'
import { StudentMemo } from '@/components/groups/StudentMemo'
import { StudentsPanel } from '@/components/groups/StudentsPanel'
import { rosterCounts } from '@/components/groups/students-utils'
import { ApiError } from '@/lib/api'
import { pluralize, WORDS } from '@/lib/format'
import { useGroup, useUpdateGroup } from '@/lib/queries'
import type { GroupDetail } from '@/lib/types'

const TABS = ['students', 'import', 'lessons', 'memo'] as const
type Tab = (typeof TABS)[number]

function parseTab(value: string | null): Tab {
  return TABS.find((t) => t === value) ?? 'students'
}

function parseId(value: string | undefined): number | undefined {
  return value && /^\d+$/.test(value) && Number(value) > 0 ? Number(value) : undefined
}

function GroupActions({ group }: { group: GroupDetail }) {
  const [editOpen, setEditOpen] = useState(false)
  const archive = useUpdateGroup(group.id, { successMessage: 'Группа перенесена в архив' })
  const restore = useUpdateGroup(group.id, { successMessage: 'Группа снова активна' })
  const archived = group.status === 'archived'
  return (
    <>
      <Button asChild size="lg">
        <Link to={`/groups/${group.id}/report`}>
          <Sheet aria-hidden="true" />
          Сводка посещаемости
        </Link>
      </Button>
      <Button variant="outline" size="lg" onClick={() => setEditOpen(true)}>
        <Pencil aria-hidden="true" />
        Изменить
      </Button>
      {archived ? (
        <Button variant="outline" size="lg" onClick={() => restore.mutate({ status: 'active' })} disabled={restore.isPending}>
          {restore.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <ArchiveRestore aria-hidden="true" />}
          Вернуть из архива
        </Button>
      ) : (
        <ConfirmDialog
          trigger={
            <Button variant="outline" size="lg">
              <Archive aria-hidden="true" />В архив
            </Button>
          }
          title={`Перенести группу «${group.name}» в архив?`}
          description="Группа пропадёт из списка активных. Студенты, занятия и посещаемость сохранятся — группу можно вернуть в любой момент."
          confirmLabel="Перенести в архив"
          destructive
          onConfirm={() => archive.mutateAsync({ status: 'archived' })}
        />
      )}
      <EditGroupDialog group={group} open={editOpen} onOpenChange={setEditOpen} />
    </>
  )
}

function ArchivedNotice() {
  return (
    <Alert className="border-amber-300 bg-amber-50/60 dark:border-amber-500/30 dark:bg-amber-400/5">
      <Archive aria-hidden="true" />
      <AlertTitle>Группа в архиве</AlertTitle>
      <AlertDescription>
        Её нет в списке активных групп. Студенты, занятия и посещаемость сохранены — нажмите «Вернуть из архива», чтобы
        снова работать с группой.
      </AlertDescription>
    </Alert>
  )
}

function GroupNotFound() {
  return (
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
  )
}

function GroupTabs({ group }: { group: GroupDetail }) {
  const [params, setParams] = useSearchParams()
  const tab = parseTab(params.get('tab'))
  const setTab = (next: Tab) =>
    setParams(
      (prev) => {
        const p = new URLSearchParams(prev)
        if (next === 'students') p.delete('tab')
        else p.set('tab', next)
        return p
      },
      { replace: true },
    )
  const counts = rosterCounts(group.students)

  return (
    <Tabs value={tab} onValueChange={(v) => setTab(parseTab(v))} className="gap-5">
      <div className="-mx-4 overflow-x-auto px-4 pb-1 sm:mx-0 sm:px-0">
        <TabsList aria-label="Разделы группы">
          <TabsTrigger value="students" className="px-3">
            <Users aria-hidden="true" />
            Студенты <span className="text-muted-foreground tabular-nums">{counts.active}</span>
          </TabsTrigger>
          <TabsTrigger value="import" className="px-3">
            <FileUp aria-hidden="true" />
            Импорт списка
          </TabsTrigger>
          <TabsTrigger value="lessons" className="px-3">
            <CalendarDays aria-hidden="true" />
            Занятия группы
          </TabsTrigger>
          <TabsTrigger value="memo" className="px-3">
            <NotebookText aria-hidden="true" />
            Памятка для студентов
          </TabsTrigger>
        </TabsList>
      </div>
      <TabsContent value="students">
        <StudentsPanel group={group} onOpenImport={() => setTab('import')} />
      </TabsContent>
      <TabsContent value="import">
        <ImportWizard groupId={group.id} hasStudents={counts.active > 0} onDone={() => setTab('students')} />
      </TabsContent>
      <TabsContent value="lessons">
        <GroupLessonsPanel groupId={group.id} />
      </TabsContent>
      <TabsContent value="memo">
        <StudentMemo group={group} onOpenStudents={() => setTab('students')} />
      </TabsContent>
    </Tabs>
  )
}

export default function GroupPage() {
  const { id } = useParams()
  const groupId = parseId(id)
  const group = useGroup(groupId)
  const back = { to: '/groups', label: 'Все группы' }

  if (groupId === undefined) {
    return (
      <Page>
        <PageHeader title="Группа не найдена" back={back} />
        <GroupNotFound />
      </Page>
    )
  }

  const data = group.data
  // Retrying cannot bring back a group that does not exist.
  const notFound = !data && group.error instanceof ApiError && group.error.isNotFound
  return (
    <Page>
      <PageHeader
        title={data?.name ?? `Группа №${groupId}`}
        documentTitle={data?.name ?? 'Группа'}
        description={data?.description || undefined}
        back={back}
        actions={data && <GroupActions group={data} />}
      >
        {data && (
          <>
            {data.status === 'archived' && (
              <ToneBadge tone="muted" icon={Archive}>
                В архиве
              </ToneBadge>
            )}
            <ToneBadge tone="neutral" icon={Users}>
              {pluralize(rosterCounts(data.students).active, WORDS.student)}
            </ToneBadge>
            <ToneBadge tone="neutral" icon={Clock} title="Обычная длительность занятия">
              Занятие {data.default_duration_minutes} мин
            </ToneBadge>
          </>
        )}
      </PageHeader>

      {data ? (
        <>
          {data.status === 'archived' && <ArchivedNotice />}
          <GroupTabs group={data} />
        </>
      ) : notFound ? (
        <GroupNotFound />
      ) : group.isError ? (
        <ErrorState error={group.error} onRetry={() => group.refetch()} retrying={group.isFetching} />
      ) : (
        <LoadingState variant="table" rows={6} label="Загружаем группу…" />
      )}
    </Page>
  )
}

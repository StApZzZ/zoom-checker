import { useState } from 'react'
import { useSearchParams } from 'react-router'
import { Archive, Plus, Search, SearchX, Users } from 'lucide-react'

import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { LoadingState } from '@/components/LoadingState'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { CreateGroupDialog } from '@/components/groups/CreateGroupDialog'
import { GroupCard } from '@/components/groups/GroupCard'
import { normalizeName } from '@/components/groups/students-utils'
import { useGroups } from '@/lib/queries'
import type { EntityStatus } from '@/lib/types'

/** Show the search box from this many groups on. */
const SEARCH_FROM = 7

export default function GroupsPage() {
  const [params, setParams] = useSearchParams()
  const status: EntityStatus = params.get('status') === 'archived' ? 'archived' : 'active'
  const groups = useGroups(status)
  const [query, setQuery] = useState('')
  const [createOpen, setCreateOpen] = useState(false)

  const setStatus = (next: EntityStatus) => {
    setQuery('')
    setParams(next === 'archived' ? { status: 'archived' } : {}, { replace: true })
  }

  const q = normalizeName(query)
  const shown = groups.data?.filter((g) => !q || normalizeName(`${g.name} ${g.description}`).includes(q))

  const newGroupButton = (
    <Button size="lg" className="h-11 px-4 text-base" onClick={() => setCreateOpen(true)}>
      <Plus aria-hidden="true" />
      Новая группа
    </Button>
  )

  return (
    <Page>
      <PageHeader
        title="Группы"
        description="Списки студентов. По ним сервис узнаёт студентов в Zoom и отмечает посещаемость."
        actions={newGroupButton}
      />

      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <ToggleGroup
          type="single"
          variant="outline"
          spacing={0}
          value={status}
          onValueChange={(v) => v && setStatus(v as EntityStatus)}
          aria-label="Какие группы показать"
        >
          <ToggleGroupItem value="active" className="px-4">
            <Users aria-hidden="true" />
            Активные
          </ToggleGroupItem>
          <ToggleGroupItem value="archived" className="px-4">
            <Archive aria-hidden="true" />
            Архив
          </ToggleGroupItem>
        </ToggleGroup>
        {((groups.data?.length ?? 0) >= SEARCH_FROM || query) && (
          <div className="relative sm:w-72">
            <Search
              className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
              aria-hidden="true"
            />
            <Input
              type="search"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Найти группу"
              aria-label="Найти группу"
              className="h-9 pl-8"
            />
          </div>
        )}
      </div>

      {groups.data && shown ? (
        groups.data.length === 0 ? (
          status === 'active' ? (
            <EmptyState
              icon={Users}
              title="Групп пока нет"
              text={
                <>
                  Группа — это список студентов одного курса. Сервис сравнивает имена участников Zoom с этим списком и
                  сам отмечает, кто был на занятии. Создайте группу и вставьте список студентов — это займёт минуту.
                </>
              }
              action={newGroupButton}
            />
          ) : (
            <EmptyState
              icon={Archive}
              title="В архиве пусто"
              text="Сюда попадают группы, которые вы перенесли в архив, например после окончания курса. Их можно вернуть в любой момент."
            />
          )
        ) : shown.length === 0 ? (
          <EmptyState
            icon={SearchX}
            title="Ничего не нашли"
            text="Проверьте название или очистите поиск."
            action={
              <Button variant="outline" onClick={() => setQuery('')}>
                Очистить поиск
              </Button>
            }
          />
        ) : (
          <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label="Список групп">
            {shown.map((group) => (
              <li key={group.id}>
                <GroupCard group={group} />
              </li>
            ))}
          </ul>
        )
      ) : groups.isError ? (
        <ErrorState error={groups.error} onRetry={() => groups.refetch()} retrying={groups.isFetching} />
      ) : (
        <LoadingState variant="cards" rows={3} label="Загружаем группы…" />
      )}

      <CreateGroupDialog open={createOpen} onOpenChange={setCreateOpen} />
    </Page>
  )
}

import { useId, useMemo, useState, type ReactNode } from 'react'
import { Archive, ArchiveRestore, CircleCheck, Pencil, Plus, Search, SearchX, Upload, Users, X } from 'lucide-react'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { ToneBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { AddAliasDialog } from '@/components/groups/AddAliasDialog'
import { AddStudentForm } from '@/components/groups/AddStudentForm'
import { EditStudentDialog } from '@/components/groups/EditStudentDialog'
import { matchesStudent, rosterCounts, sortStudents } from '@/components/groups/students-utils'
import { pluralize, WORDS } from '@/lib/format'
import { useDeleteAlias, useUpdateStudent } from '@/lib/queries'
import type { GroupDetail, Student, StudentAlias } from '@/lib/types'

export interface StudentsPanelProps {
  group: GroupDetail
  /** Switches to the "Импорт списка" tab. */
  onOpenImport: () => void
}

type View = 'active' | 'archived'

function IconAction({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button type="button" variant="ghost" size="icon" onClick={onClick} aria-label={label}>
          {children}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

/** "Студенты" tab: search, add form, roster table with Zoom names and archive actions. */
export function StudentsPanel({ group, onOpenImport }: StudentsPanelProps) {
  const id = useId()
  const [query, setQuery] = useState('')
  const [view, setView] = useState<View>('active')
  const [editing, setEditing] = useState<Student | null>(null)
  const [aliasFor, setAliasFor] = useState<Student | null>(null)
  // The subject of a confirmation stays set while the dialog animates closed,
  // so its title does not flash empty; `…Open` drives the dialog.
  const [archiveTarget, setArchiveTarget] = useState<Student | null>(null)
  const [archiveOpen, setArchiveOpen] = useState(false)
  const [aliasToRemove, setAliasToRemove] = useState<{ student: Student; alias: StudentAlias } | null>(null)
  const [aliasRemoveOpen, setAliasRemoveOpen] = useState(false)

  const archiveStudent = useUpdateStudent(group.id, { successMessage: 'Студент перенесён в архив' })
  const restoreStudent = useUpdateStudent(group.id, { successMessage: 'Студент снова в группе' })
  const deleteAlias = useDeleteAlias(group.id)

  const counts = rosterCounts(group.students)
  const sorted = useMemo(() => sortStudents(group.students), [group.students])
  const inView = sorted.filter((s) => (view === 'archived' ? s.status === 'archived' : s.status !== 'archived'))
  const shown = inView.filter((s) => matchesStudent(s, query))
  const showIds = group.students.some((s) => s.external_id)
  const searching = query.trim().length > 0

  const restore = (student: Student) => restoreStudent.mutate({ studentId: student.id, input: { status: 'active' } })

  return (
    <div className="flex flex-col gap-5">
      <AddStudentForm groupId={group.id} />

      {group.students.length === 0 ? (
        <EmptyState
          icon={Users}
          title="В группе пока нет студентов"
          text="Добавьте студентов по одному в форме выше или загрузите весь список сразу — из Excel, CSV или простым текстом."
          action={
            <Button type="button" size="lg" onClick={onOpenImport}>
              <Upload aria-hidden="true" />
              Загрузить список
            </Button>
          }
        />
      ) : (
        <>
          <div className="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
            <p className="text-sm text-muted-foreground" aria-live="polite">
              <span className="font-medium text-foreground">В группе {pluralize(counts.active, WORDS.student)}</span>
              {counts.archived > 0 && <> · в архиве {counts.archived}</>}
              {searching && <> · найдено: {shown.length}</>}
            </p>
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              {counts.archived > 0 && (
                <ToggleGroup
                  type="single"
                  variant="outline"
                  spacing={0}
                  value={view}
                  onValueChange={(v) => v && setView(v as View)}
                  aria-label="Какие студенты показаны"
                >
                  <ToggleGroupItem value="active">В группе ({counts.active})</ToggleGroupItem>
                  <ToggleGroupItem value="archived">В архиве ({counts.archived})</ToggleGroupItem>
                </ToggleGroup>
              )}
              <div className="relative sm:w-72">
                <Search
                  className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
                  aria-hidden="true"
                />
                <Input
                  id={`${id}-search`}
                  type="search"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Поиск по имени или ID"
                  aria-label="Поиск студента"
                  className="h-9 pl-8"
                />
              </div>
            </div>
          </div>

          {shown.length === 0 ? (
            searching ? (
              <EmptyState
                icon={SearchX}
                size="compact"
                title="Никого не нашли"
                text="Проверьте написание или очистите поиск."
                action={
                  <Button type="button" variant="outline" onClick={() => setQuery('')}>
                    Очистить поиск
                  </Button>
                }
              />
            ) : (
              <EmptyState
                icon={view === 'archived' ? Archive : Users}
                size="compact"
                title={view === 'archived' ? 'В архиве никого нет' : 'Все студенты в архиве'}
                text={
                  view === 'archived'
                    ? 'Сюда попадают студенты, которых убрали из группы.'
                    : 'Верните нужных студентов из архива или загрузите новый список.'
                }
              />
            )
          ) : (
            // `relative` keeps the absolutely positioned sr-only texts inside the scroll box
            // (otherwise they widen the whole page on a phone).
            <div className="relative overflow-x-auto rounded-xl ring-1 ring-foreground/10">
              <table className="w-full text-sm">
                <caption className="sr-only">
                  {view === 'archived' ? 'Студенты в архиве' : 'Студенты группы'}
                </caption>
                <thead className="bg-muted/50">
                  <tr className="border-b text-left">
                    <th scope="col" className="w-10 px-3 py-2.5 font-medium text-muted-foreground">
                      №
                    </th>
                    <th scope="col" className="px-3 py-2.5 font-medium">
                      ФИО
                    </th>
                    {showIds && (
                      <th scope="col" className="px-3 py-2.5 font-medium">
                        ID
                      </th>
                    )}
                    <th scope="col" className="px-3 py-2.5 font-medium">
                      Дополнительные имена в Zoom
                    </th>
                    <th scope="col" className="px-3 py-2.5 font-medium">
                      Статус
                    </th>
                    <th scope="col" className="px-3 py-2.5 text-right font-medium">
                      <span className="sr-only">Действия</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {shown.map((student, index) => {
                    const archived = student.status === 'archived'
                    return (
                      <tr key={student.id} className="border-b align-middle last:border-b-0 hover:bg-muted/30">
                        <td className="px-3 py-2 text-muted-foreground tabular-nums">{index + 1}</td>
                        <td className="px-3 py-2 font-medium break-words">{student.full_name}</td>
                        {showIds && <td className="px-3 py-2 text-muted-foreground">{student.external_id || '—'}</td>}
                        <td className="px-3 py-2">
                          <ul className="flex flex-wrap items-center gap-1.5" aria-label={`Дополнительные имена в Zoom: ${student.full_name}`}>
                            {student.aliases.map((alias) => (
                              <li
                                key={alias.id}
                                className="inline-flex items-center gap-0.5 rounded-full bg-secondary py-0.5 pr-0.5 pl-2.5 text-xs text-secondary-foreground ring-1 ring-foreground/10"
                              >
                                {alias.alias}
                                <button
                                  type="button"
                                  onClick={() => {
                                    setAliasToRemove({ student, alias })
                                    setAliasRemoveOpen(true)
                                  }}
                                  aria-label={`Удалить имя «${alias.alias}»`}
                                  className="inline-flex size-6 items-center justify-center rounded-full text-muted-foreground outline-none hover:bg-foreground/10 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring"
                                >
                                  <X className="size-3" aria-hidden="true" />
                                </button>
                              </li>
                            ))}
                            {!archived && (
                              <li>
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="xs"
                                  onClick={() => setAliasFor(student)}
                                  aria-label={`Добавить имя в Zoom: ${student.full_name}`}
                                  className="text-muted-foreground"
                                >
                                  <Plus aria-hidden="true" />
                                  Имя
                                </Button>
                              </li>
                            )}
                          </ul>
                        </td>
                        <td className="px-3 py-2">
                          {archived ? (
                            <ToneBadge tone="muted" icon={Archive} title="Студент убран из группы, история посещаемости сохранена">
                              В архиве
                            </ToneBadge>
                          ) : (
                            <ToneBadge tone="success" icon={CircleCheck} title="Студент учитывается на занятиях">
                              В группе
                            </ToneBadge>
                          )}
                        </td>
                        <td className="px-2 py-1 text-right whitespace-nowrap">
                          <IconAction label={`Изменить: ${student.full_name}`} onClick={() => setEditing(student)}>
                            <Pencil aria-hidden="true" />
                          </IconAction>
                          {archived ? (
                            <IconAction label={`Вернуть в группу: ${student.full_name}`} onClick={() => restore(student)}>
                              <ArchiveRestore aria-hidden="true" />
                            </IconAction>
                          ) : (
                            <IconAction
                              label={`В архив: ${student.full_name}`}
                              onClick={() => {
                                setArchiveTarget(student)
                                setArchiveOpen(true)
                              }}
                            >
                              <Archive aria-hidden="true" />
                            </IconAction>
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}

      <EditStudentDialog groupId={group.id} student={editing} onClose={() => setEditing(null)} />
      <AddAliasDialog groupId={group.id} student={aliasFor} onClose={() => setAliasFor(null)} />

      <ConfirmDialog
        open={archiveOpen}
        onOpenChange={setArchiveOpen}
        title={`Перенести в архив: ${archiveTarget?.full_name ?? ''}?`}
        description="Студент не будет отмечаться на новых занятиях. Его посещаемость за прошлые занятия сохранится, а вернуть студента можно в любой момент."
        confirmLabel="Перенести в архив"
        destructive
        onConfirm={() =>
          archiveTarget
            ? archiveStudent.mutateAsync({ studentId: archiveTarget.id, input: { status: 'archived' } })
            : undefined
        }
      />

      <ConfirmDialog
        open={aliasRemoveOpen}
        onOpenChange={setAliasRemoveOpen}
        title={`Удалить имя «${aliasToRemove?.alias.alias ?? ''}»?`}
        description={`Если студент ${aliasToRemove?.student.full_name ?? ''} зайдёт в Zoom под этим именем, сервис его не узнает. Завершённые занятия не изменятся.`}
        confirmLabel="Удалить имя"
        destructive
        onConfirm={() =>
          aliasToRemove
            ? deleteAlias.mutateAsync({ studentId: aliasToRemove.student.id, aliasId: aliasToRemove.alias.id })
            : undefined
        }
      />
    </div>
  )
}

import { useId, useMemo, useState } from 'react'
import { CircleAlert, Loader2, UserCheck } from 'lucide-react'
import { toast } from 'sonner'

import { currentStatusMeta } from '@/components/status-meta'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { errorMessage } from '@/lib/api'
import { useGroup, useMapParticipant } from '@/lib/queries'
import type { Participant, Student, StudentRow } from '@/lib/types'
import { looksLikeDeviceName } from '@/pages/lessons/live/liveText'

export interface MapParticipantDialogProps {
  lessonId: number
  /** Group of the lesson: a finished lesson also offers students missing from its list. */
  groupId: number
  lessonFinished: boolean
  open: boolean
  /** Participant being mapped; kept while the dialog animates closed. */
  participant: Participant | null
  /** Students of the lesson (archived ones are not offered). */
  students: readonly StudentRow[]
  onClose: () => void
}

/** "Это студент…": pick the student a Zoom participant really is. */
export function MapParticipantDialog({
  lessonId,
  groupId,
  lessonFinished,
  open,
  participant,
  students,
  onClose,
}: MapParticipantDialogProps) {
  return (
    <Dialog open={open && participant !== null} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[calc(100svh-2rem)] overflow-y-auto sm:max-w-md">
        {participant && (
          <MapParticipantForm
            key={participant.id}
            lessonId={lessonId}
            groupId={groupId}
            lessonFinished={lessonFinished}
            participant={participant}
            students={students}
            onDone={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

/** One choice of the list: a student row of the lesson or a group student not on it. */
interface Choice {
  student_id: number
  full_name: string
  /** Second line: the student's state on this lesson. */
  note: string
  keywords: string[]
}

interface Groups {
  candidates: Choice[]
  notSeen: Choice[]
  others: Choice[]
  /** Active group students without a row on this (finished, frozen) lesson. */
  notListed: Choice[]
}

const rowChoice = (s: StudentRow): Choice => ({
  student_id: s.student_id,
  full_name: s.full_name,
  note: Object.hasOwn(currentStatusMeta, s.current_status) ? currentStatusMeta[s.current_status].label : '',
  keywords: [s.full_name, ...s.zoom_names],
})

const groupStudentChoice = (s: Student): Choice => ({
  student_id: s.id,
  full_name: s.full_name,
  note: 'не было в списке занятия',
  keywords: [s.full_name, ...s.aliases.map((a) => a.alias)],
})

/**
 * Likely matches first: name candidates, then students who have not joined yet,
 * then everyone else, then group students that are not on this lesson's list.
 */
function groupChoices(
  students: readonly StudentRow[],
  groupStudents: readonly Student[] | undefined,
  candidateIds: readonly number[],
): Groups {
  const active = students.filter((s) => !s.archived)
  const listed = new Set(students.map((s) => s.student_id))
  const extra = (groupStudents ?? [])
    .filter((s) => s.status === 'active' && !listed.has(s.id))
    .sort((a, b) => a.full_name.localeCompare(b.full_name, 'ru'))
    .map(groupStudentChoice)
  const byId = new Map<number, Choice>([...active.map((s) => [s.student_id, rowChoice(s)] as const), ...extra.map((c) => [c.student_id, c] as const)])
  const candidates = candidateIds.map((id) => byId.get(id)).filter((c): c is Choice => c !== undefined)
  const isCandidate = new Set(candidates.map((c) => c.student_id))
  const rest = active.filter((s) => !isCandidate.has(s.student_id))
  return {
    candidates,
    notSeen: rest.filter((s) => s.current_status === 'not_seen').map(rowChoice),
    others: rest.filter((s) => s.current_status !== 'not_seen').map(rowChoice),
    notListed: extra.filter((c) => !isCandidate.has(c.student_id)),
  }
}

function MapParticipantForm({
  lessonId,
  groupId,
  lessonFinished,
  participant,
  students,
  onDone,
}: {
  lessonId: number
  groupId: number
  lessonFinished: boolean
  participant: Participant
  students: readonly StudentRow[]
  onDone: () => void
}) {
  const id = useId()
  // The error is shown here, next to the checkbox it usually talks about (409: the name is taken).
  const map = useMapParticipant(lessonId, { errorToast: false })
  // A finished lesson keeps its student list as it was; a student added to the group
  // afterwards (forgotten in the list) can still be chosen — the server adds the row.
  const group = useGroup(lessonFinished ? groupId : undefined)
  const groups = useMemo(
    () => groupChoices(students, group.data?.students, participant.candidate_student_ids),
    [students, group.data?.students, participant.candidate_student_ids],
  )
  const conflict = participant.resolution === 'conflict'
  const deviceName = looksLikeDeviceName(participant.display_name)
  const [selected, setSelected] = useState<number | null>(groups.candidates[0]?.student_id ?? null)
  // An ambiguous name, or one that anybody could use ("iPhone"), should not be remembered.
  const [remember, setRemember] = useState(!conflict && !deviceName)
  const all = [...groups.candidates, ...groups.notSeen, ...groups.others, ...groups.notListed]
  const selectedChoice = all.find((c) => c.student_id === selected)

  const submit = () => {
    if (!selectedChoice) return
    map.mutate(
      {
        participantId: participant.id,
        input: { action: 'student', student_id: selectedChoice.student_id, remember_alias: remember },
      },
      {
        onSuccess: () => {
          toast.success(`Время присутствия перенесено студенту ${selectedChoice.full_name}`)
          onDone()
        },
      },
    )
  }

  const renderItem = (c: Choice) => {
    const checked = c.student_id === selected
    return (
      <CommandItem
        key={c.student_id}
        value={`${c.full_name} #${c.student_id}`}
        keywords={c.keywords}
        onSelect={() => {
          setSelected(c.student_id)
          map.reset()
        }}
        data-checked={checked}
        // The keyboard cursor also tints rows; the chosen one gets a ring + check mark so they are not confused.
        className="min-h-10 data-[checked=true]:font-medium data-[checked=true]:ring-2 data-[checked=true]:ring-primary/50 data-[checked=true]:ring-inset"
      >
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="truncate font-medium">{c.full_name}</span>
          <span className="truncate text-xs text-muted-foreground">{c.note}</span>
        </span>
        {checked && <span className="sr-only">(выбран)</span>}
      </CommandItem>
    )
  }

  let rememberHint: string
  if (conflict) rememberHint = 'Это имя подходит нескольким студентам — запоминать его лучше не надо.'
  else if (deviceName) rememberHint = 'Похоже на название устройства: под ним может зайти кто угодно, поэтому запоминать его не стоит.'
  else rememberHint = `В следующий раз «${participant.display_name}» будет узнан автоматически.`

  return (
    <>
      <DialogHeader>
        <DialogTitle>Кто это — «{participant.display_name}»?</DialogTitle>
        <DialogDescription>
          Выберите студента. Всё время этого участника на занятии будет засчитано выбранному студенту.
        </DialogDescription>
      </DialogHeader>

      {/* cmdk names the search input after `label` (aria-labelledby) and the list after its own `label`. */}
      <Command className="rounded-lg! ring-1 ring-foreground/10" label="Найти студента">
        <CommandInput placeholder="Начните вводить фамилию" />
        <CommandList className="max-h-64" label="Студенты группы">
          <CommandEmpty>Такого студента в группе нет.</CommandEmpty>
          {groups.candidates.length > 0 && (
            <CommandGroup heading={conflict ? 'Подходят по имени' : 'Подходит по имени'}>
              {groups.candidates.map(renderItem)}
            </CommandGroup>
          )}
          {groups.notSeen.length > 0 && (
            <CommandGroup heading="Ещё не подключались">{groups.notSeen.map(renderItem)}</CommandGroup>
          )}
          {groups.others.length > 0 && (
            <CommandGroup heading="Остальные студенты">{groups.others.map(renderItem)}</CommandGroup>
          )}
          {groups.notListed.length > 0 && (
            <CommandGroup heading="Нет в списке этого занятия">{groups.notListed.map(renderItem)}</CommandGroup>
          )}
        </CommandList>
      </Command>

      <p className="text-sm" aria-live="polite">
        {selectedChoice ? (
          <>
            Выбран: <span className="font-medium">{selectedChoice.full_name}</span>
          </>
        ) : (
          <span className="text-muted-foreground">Студент не выбран</span>
        )}
      </p>

      <div className="flex items-start gap-2.5">
        <Checkbox
          id={`${id}-remember`}
          checked={remember}
          onCheckedChange={(v) => {
            setRemember(v === true)
            map.reset()
          }}
          className="mt-0.5"
          aria-describedby={`${id}-remember-hint`}
        />
        <div className="flex flex-col gap-1">
          <Label htmlFor={`${id}-remember`} className="cursor-pointer">
            Запомнить это имя для следующих занятий
          </Label>
          <p id={`${id}-remember-hint`} className="text-xs text-muted-foreground">
            {rememberHint}
          </p>
        </div>
      </div>

      {map.isError && (
        <Alert variant="destructive">
          <CircleAlert aria-hidden="true" />
          <AlertDescription>{errorMessage(map.error)}</AlertDescription>
        </Alert>
      )}

      <DialogFooter>
        <Button type="button" variant="outline" size="lg" onClick={onDone} disabled={map.isPending}>
          Отмена
        </Button>
        <Button type="button" size="lg" onClick={submit} disabled={!selectedChoice || map.isPending}>
          {map.isPending ? <Loader2 className="animate-spin" aria-hidden="true" /> : <UserCheck aria-hidden="true" />}
          Сопоставить
        </Button>
      </DialogFooter>
    </>
  )
}

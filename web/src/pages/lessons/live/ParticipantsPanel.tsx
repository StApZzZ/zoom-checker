import { useId, useState, type Ref } from 'react'
import { ChevronDown, EyeOff, Loader2, RotateCcw, TriangleAlert, UserCheck, UserSearch } from 'lucide-react'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/ConfirmDialog'
import { EmptyState } from '@/components/EmptyState'
import { LiveDuration } from '@/components/LiveDuration'
import { StatusBadge } from '@/components/StatusBadge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Label } from '@/components/ui/label'
import { EMPTY, formatTime, pluralize, WORDS } from '@/lib/format'
import { useMapParticipant, useUnmapParticipant } from '@/lib/queries'
import type { Participant, StudentRow } from '@/lib/types'
import { cn } from '@/lib/utils'
import { findParticipant, hiddenReasonLabel, restoreOutcome, undoOutcome } from '@/pages/lessons/live/liveText'
import { MapParticipantDialog } from '@/pages/lessons/live/MapParticipantDialog'

// ---------------------------------------------------------------------------
// Не идентифицированы
// ---------------------------------------------------------------------------

function UnresolvedRow({
  participant,
  asOf,
  studentNames,
  onMap,
  onIgnore,
  onUndo,
  undoPending,
}: {
  participant: Participant
  asOf: string
  studentNames: ReadonlyMap<number, string>
  onMap: (p: Participant) => void
  onIgnore: (p: Participant) => void
  /** Removes the teacher's earlier decision (e.g. a cancelled automatic match). */
  onUndo: (p: Participant) => void
  undoPending: boolean
}) {
  const nameId = useId()
  const conflict = participant.resolution === 'conflict'
  const candidates = participant.candidate_student_ids
    .map((id) => studentNames.get(id))
    .filter((n): n is string => !!n)
  return (
    <li
      data-participant-id={participant.id}
      className={cn(
        'flex flex-col gap-2.5 rounded-lg p-3 ring-1 ring-foreground/10',
        participant.current_status === 'online' && 'bg-amber-50/50 dark:bg-amber-400/5',
      )}
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span id={nameId} className="font-medium break-words">
          «{participant.display_name}»
        </span>
        <StatusBadge kind="current" status={participant.current_status} size="sm" />
      </div>
      <dl className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-muted-foreground">
        <div className="flex gap-1">
          <dt>Время:</dt>
          <dd className="text-foreground">
            <LiveDuration
              seconds={participant.total_seconds}
              asOf={asOf}
              online={participant.current_status === 'online'}
            />
          </dd>
        </div>
        <div className="flex gap-1">
          <dt>Первый вход:</dt>
          <dd className="text-foreground tabular-nums">
            {participant.first_join_at ? formatTime(participant.first_join_at) : EMPTY}
          </dd>
        </div>
        {participant.devices > 1 && <div>{pluralize(participant.devices, WORDS.connection)}</div>}
      </dl>
      {conflict && (
        <p className="flex items-start gap-1.5 text-sm text-amber-900 dark:text-amber-200">
          <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          <span>
            Подходит нескольким студентам
            {candidates.length > 0 && <>: {candidates.join(', ')}</>}
          </span>
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <Button size="lg" onClick={() => onMap(participant)} aria-describedby={nameId}>
          <UserCheck aria-hidden="true" />
          Это студент…
        </Button>
        <Button size="lg" variant="outline" onClick={() => onIgnore(participant)} aria-describedby={nameId}>
          <EyeOff aria-hidden="true" />
          Не студент
        </Button>
        {participant.manual && (
          <Button
            size="lg"
            variant="ghost"
            onClick={() => onUndo(participant)}
            disabled={undoPending}
            aria-describedby={nameId}
            title="Отменить ваше решение по этому имени: система снова попробует узнать участника сама"
          >
            <RotateCcw aria-hidden="true" />
            Вернуть как было
          </Button>
        )}
      </div>
    </li>
  )
}

export interface UnidentifiedBlockProps {
  lessonId: number
  groupId: number
  lessonFinished: boolean
  asOf: string
  unresolved: Participant[]
  /** Whether anybody has joined the Zoom meeting yet (any participant at all). */
  anyParticipants: boolean
  students: readonly StudentRow[]
  /** The tile "Не идентифицированы" scrolls here. */
  ref?: Ref<HTMLElement>
}

/** "Не идентифицированы": Zoom names that matched nobody (or several students). */
export function UnidentifiedBlock({
  lessonId,
  groupId,
  lessonFinished,
  asOf,
  unresolved,
  anyParticipants,
  students,
  ref,
}: UnidentifiedBlockProps) {
  const map = useMapParticipant(lessonId)
  const unmap = useUnmapParticipant(lessonId)
  // Targets outlive the "open" flags so the dialogs keep their text while animating closed.
  const [mapTarget, setMapTarget] = useState<Participant | null>(null)
  const [mapOpen, setMapOpen] = useState(false)
  const [ignoreTarget, setIgnoreTarget] = useState<Participant | null>(null)
  const [ignoreOpen, setIgnoreOpen] = useState(false)
  const [ignoreAlways, setIgnoreAlways] = useState(false)
  const alwaysId = useId()
  const studentNames = new Map(students.map((s) => [s.student_id, s.full_name]))

  const openMap = (p: Participant) => {
    setMapTarget(p)
    setMapOpen(true)
  }

  const openIgnore = (p: Participant) => {
    setIgnoreAlways(false)
    setIgnoreTarget(p)
    setIgnoreOpen(true)
  }

  const undo = (p: Participant) =>
    unmap.mutate(p.id, { onSuccess: (view) => toast.success(undoOutcome(p.display_name, findParticipant(view, p))) })

  const confirmIgnore = () => {
    const target = ignoreTarget
    if (!target) return
    const always = ignoreAlways
    return map
      .mutateAsync({ participantId: target.id, input: { action: 'ignore', ignore_always: always } })
      .then(() =>
        toast.success(
          always
            ? `«${target.display_name}» скрыт и будет скрываться на следующих занятиях`
            : `«${target.display_name}» скрыт`,
        ),
      )
  }

  return (
    <section
      ref={ref}
      id="unidentified"
      tabIndex={-1}
      aria-labelledby="unidentified-title"
      className="flex scroll-mt-20 flex-col gap-3 rounded-xl bg-card p-4 ring-1 ring-foreground/10 outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
    >
      <div className="flex flex-col gap-1">
        <h2 id="unidentified-title" className="flex items-center gap-2 text-lg font-semibold">
          <UserSearch className="size-5 text-muted-foreground" aria-hidden="true" />
          Не идентифицированы
          {unresolved.length > 0 && (
            <span className="rounded-full bg-amber-100 px-2 text-sm text-amber-900 tabular-nums dark:bg-amber-400/15 dark:text-amber-200">
              {unresolved.length}
            </span>
          )}
        </h2>
        {unresolved.length > 0 && (
          <p className="text-sm text-muted-foreground">
            Эти участники Zoom не совпали ни с одним студентом группы. Укажите, кто это, — время присутствия перенесётся
            студенту.
          </p>
        )}
      </div>
      {unresolved.length === 0 ? (
        anyParticipants ? (
          <EmptyState size="compact" icon={UserCheck} title="Все участники Zoom опознаны 👍" />
        ) : (
          <EmptyState
            size="compact"
            icon={UserSearch}
            title="Пока никого"
            text="Если кто-то подключится под непонятным именем, он появится здесь — и вы сможете указать, кто это."
          />
        )
      ) : (
        <ul className="flex flex-col gap-2">
          {unresolved.map((p) => (
            <UnresolvedRow
              key={p.id}
              participant={p}
              asOf={asOf}
              studentNames={studentNames}
              onMap={openMap}
              onIgnore={openIgnore}
              onUndo={undo}
              undoPending={unmap.isPending}
            />
          ))}
        </ul>
      )}

      <MapParticipantDialog
        lessonId={lessonId}
        groupId={groupId}
        lessonFinished={lessonFinished}
        open={mapOpen}
        participant={mapTarget}
        students={students}
        onClose={() => setMapOpen(false)}
      />

      <ConfirmDialog
        open={ignoreOpen && ignoreTarget !== null}
        onOpenChange={setIgnoreOpen}
        title={`Скрыть «${ignoreTarget?.display_name ?? ''}»?`}
        description="Например, это ассистент, гость или запись. Время этого участника не будет учитываться в посещаемости."
        confirmLabel="Скрыть"
        onConfirm={confirmIgnore}
      >
        <div className="flex items-start gap-2.5">
          <Checkbox
            id={alwaysId}
            checked={ignoreAlways}
            onCheckedChange={(v) => setIgnoreAlways(v === true)}
            className="mt-0.5"
          />
          <div className="flex flex-col gap-1">
            <Label htmlFor={alwaysId} className="cursor-pointer">
              Всегда скрывать это имя
            </Label>
            <p className="text-xs text-muted-foreground">На следующих занятиях участник с таким именем тоже будет скрыт.</p>
          </div>
        </div>
      </ConfirmDialog>
    </section>
  )
}

// ---------------------------------------------------------------------------
// Скрытые участники
// ---------------------------------------------------------------------------

function HiddenRow({ lessonId, participant, asOf }: { lessonId: number; participant: Participant; asOf: string }) {
  const unmap = useUnmapParticipant(lessonId)
  const map = useMapParticipant(lessonId)
  const [busy, setBusy] = useState(false)
  const restore = async () => {
    setBusy(true)
    try {
      let view: unknown
      // Hidden by the teacher → remove that decision first.
      const decided = participant.manual || participant.ignore_reason === 'manual'
      if (decided) view = await unmap.mutateAsync(participant.id)
      // Still hidden by a rule (organizer, or "always hide" put the name on the global list):
      // override it for this lesson so the participant is counted again.
      if (!decided || findParticipant(view, participant)?.resolution === 'ignored') {
        view = await map.mutateAsync({ participantId: participant.id, input: { action: 'none' } })
      }
      const outcome = restoreOutcome(participant.display_name, findParticipant(view, participant))
      if (outcome.ok) toast.success(outcome.message)
      else toast.error(outcome.message)
    } catch {
      // The mutation layer shows the error.
    } finally {
      setBusy(false)
    }
  }
  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1.5 py-2">
      <div className="flex min-w-0 flex-1 flex-col">
        <span className="text-sm font-medium break-words">«{participant.display_name}»</span>
        <span className="text-xs text-muted-foreground">
          {hiddenReasonLabel(participant.ignore_reason)} ·{' '}
          <LiveDuration seconds={participant.total_seconds} asOf={asOf} online={participant.current_status === 'online'} />
        </span>
      </div>
      <Button variant="ghost" size="sm" onClick={() => void restore()} disabled={busy}>
        {busy ? <Loader2 className="animate-spin" aria-hidden="true" /> : <RotateCcw aria-hidden="true" />}
        Вернуть
      </Button>
    </li>
  )
}

export function HiddenParticipants({
  lessonId,
  asOf,
  hidden,
}: {
  lessonId: number
  asOf: string
  hidden: Participant[]
}) {
  const [open, setOpen] = useState(false)
  if (hidden.length === 0) return null
  return (
    <Collapsible open={open} onOpenChange={setOpen} className="rounded-xl bg-card ring-1 ring-foreground/10">
      <CollapsibleTrigger className="flex w-full items-center gap-2 rounded-xl px-4 py-3 text-left text-sm font-medium outline-none hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50">
        <EyeOff className="size-4 text-muted-foreground" aria-hidden="true" />
        <span className="flex-1">Скрытые участники ({hidden.length})</span>
        <ChevronDown className={cn('size-4 text-muted-foreground transition-transform', open && 'rotate-180')} aria-hidden="true" />
      </CollapsibleTrigger>
      <CollapsibleContent className="px-4 pb-3">
        <p className="text-sm text-muted-foreground">
          Не учитываются в посещаемости: организатор конференции, имена из списка исключений и те, кого вы скрыли.
        </p>
        <ul className="divide-y">
          {hidden.map((p) => (
            <HiddenRow key={p.id} lessonId={lessonId} participant={p} asOf={asOf} />
          ))}
        </ul>
      </CollapsibleContent>
    </Collapsible>
  )
}

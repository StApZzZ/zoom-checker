import { Lightbulb, Users } from 'lucide-react'

import { CopyButton } from '@/components/CopyButton'
import { EmptyState } from '@/components/EmptyState'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  buildMemoText,
  memoTitle,
  NAMES_HEADING,
  RENAME_HEADING,
  RENAME_TIPS,
  STUDENT_RESPONSIBILITY_TEXT,
} from '@/components/groups/memo'
import { activeNames } from '@/components/groups/students-utils'
import type { GroupDetail } from '@/lib/types'

/** "Памятка для студентов" tab: TZ §33 text + the exact names to use in Zoom, ready to copy. */
export function StudentMemo({ group, onOpenStudents }: { group: GroupDetail; onOpenStudents: () => void }) {
  const names = activeNames(group.students)
  const text = buildMemoText(group.name, names)

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Отправьте эту памятку студентам перед началом курса — например, в чат группы. Тогда сервис узнает каждого в
          Zoom и отметит посещаемость сам.
        </p>
        <CopyButton value={text} label="Скопировать памятку" size="lg" successMessage="Памятка скопирована" />
      </div>

      <article
        aria-label="Текст памятки"
        className="flex flex-col gap-4 rounded-xl bg-card p-5 text-sm leading-relaxed ring-1 ring-foreground/10 sm:p-6"
      >
        <h3 className="font-heading text-base font-semibold">{memoTitle(group.name)}</h3>
        <p>{STUDENT_RESPONSIBILITY_TEXT}</p>
        {names.length > 0 ? (
          <section className="flex flex-col gap-2">
            <h4 className="font-medium">{NAMES_HEADING}</h4>
            <ol className="list-decimal gap-x-10 pl-6 sm:columns-2 lg:columns-3">
              {names.map((name, i) => (
                <li key={`${i}-${name}`} className="break-inside-avoid py-0.5 font-medium">
                  {name}
                </li>
              ))}
            </ol>
          </section>
        ) : (
          <EmptyState
            icon={Users}
            size="compact"
            title="Список имён появится, когда в группе будут студенты"
            action={
              <button
                type="button"
                onClick={onOpenStudents}
                className="text-sm font-medium text-primary underline-offset-4 hover:underline"
              >
                Добавить студентов
              </button>
            }
          />
        )}
        <section className="flex flex-col gap-2">
          <h4 className="font-medium">{RENAME_HEADING}</h4>
          <ul className="flex flex-col gap-1.5">
            {RENAME_TIPS.map((tip) => (
              <li key={tip} className="flex gap-2">
                <span aria-hidden="true">—</span>
                <span>{tip}</span>
              </li>
            ))}
          </ul>
        </section>
      </article>

      <Alert>
        <Lightbulb aria-hidden="true" />
        <AlertTitle>Совет</AlertTitle>
        <AlertDescription>
          Если студент всё равно подписывается по-своему (например, латиницей), добавьте ему дополнительное имя на вкладке
          «Студенты» — кнопка «+ Имя» рядом с его фамилией.
        </AlertDescription>
      </Alert>
    </div>
  )
}

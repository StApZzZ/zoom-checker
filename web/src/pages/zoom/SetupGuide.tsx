import { useState, type ReactNode } from 'react'
import { BookOpen, ChevronDown, ExternalLink, Lightbulb } from 'lucide-react'
import { Link } from 'react-router'

import { CopyButton } from '@/components/CopyButton'
import { Card, CardContent, CardDescription, CardHeader } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

/** Name of a button / field as it is written in the Zoom interface (English). */
function Ui({ children }: { children: ReactNode }) {
  return <strong className="font-semibold text-foreground">{children}</strong>
}

function Code({ children }: { children: ReactNode }) {
  return <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.85em] break-all">{children}</code>
}

function Step({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="relative flex gap-4 pb-6 last:pb-0">
      <span
        aria-hidden="true"
        className="relative z-10 flex size-8 shrink-0 items-center justify-center rounded-full bg-sky-600 text-sm font-semibold text-white dark:bg-sky-500"
      >
        {n}
      </span>
      <div className="flex min-w-0 flex-1 flex-col gap-1.5 pt-1">
        <h3 className="font-medium">
          <span className="sr-only">Шаг {n}. </span>
          {title}
        </h3>
        <div className="flex flex-col gap-2 text-sm text-pretty text-muted-foreground">{children}</div>
      </div>
    </li>
  )
}

const ZOOM_EVENTS = [
  'Start Meeting',
  'End Meeting',
  'Participant/Host joined meeting',
  'Participant/Host left meeting',
  'Meeting participant has joined a breakout room',
  'Meeting participant has left a breakout room',
] as const

export interface SetupGuideProps {
  webhookUrl: string
  defaultOpen?: boolean
}

/** Numbered, collapsible "how to connect Zoom" guide for administrators. */
export function SetupGuide({ webhookUrl, defaultOpen = false }: SetupGuideProps) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <Card>
      <Collapsible open={open} onOpenChange={setOpen}>
        <CardHeader>
          <h2 className="font-heading text-lg leading-snug font-medium">
            <CollapsibleTrigger className="-m-2 flex w-[calc(100%+1rem)] items-center gap-3 rounded-lg p-2 text-left outline-none hover:bg-muted/50 focus-visible:ring-3 focus-visible:ring-ring/50">
              <BookOpen className="size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="min-w-0 flex-1">Как подключить Zoom</span>
              <ChevronDown
                className={cn('size-5 shrink-0 text-muted-foreground transition-transform', open && 'rotate-180')}
                aria-hidden="true"
              />
            </CollapsibleTrigger>
          </h2>
          <CardDescription>
            Пошаговая инструкция для администратора, около 15 минут. Нужен доступ администратора к аккаунту Zoom.
          </CardDescription>
        </CardHeader>
        <CollapsibleContent>
          <CardContent className="flex flex-col gap-6 pt-4">
            <ol className="relative before:absolute before:top-2 before:bottom-2 before:left-4 before:w-px before:bg-border">
              <Step n={1} title="Откройте Zoom App Marketplace">
                <p>
                  Зайдите на{' '}
                  <a
                    href="https://marketplace.zoom.us/"
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 font-medium text-foreground underline underline-offset-3"
                  >
                    marketplace.zoom.us
                    <ExternalLink className="size-3.5" aria-hidden="true" />
                    <span className="sr-only">(откроется в новой вкладке)</span>
                  </a>{' '}
                  под учётной записью администратора Zoom — того аккаунта, в котором преподаватели проводят занятия.
                </p>
              </Step>
              <Step n={2} title="Создайте приложение">
                <p>
                  Вверху справа нажмите <Ui>Develop</Ui> → <Ui>Build App</Ui>. Выберите тип{' '}
                  <Ui>Server-to-Server OAuth App</Ui> (или <Ui>Webhook Only App</Ui>, если он есть в списке) и нажмите{' '}
                  <Ui>Create</Ui>. Название — любое, например «Zoom Checker».
                </p>
              </Step>
              <Step n={3} title="Заполните раздел Information">
                <p>
                  Укажите название приложения, компанию (<Ui>Company Name</Ui>) и контакты (
                  <Ui>Developer Contact Information</Ui>: имя и почта). Нажмите <Ui>Continue</Ui>.
                </p>
              </Step>
              <Step n={4} title="Скопируйте Secret Token в настройки сервера">
                <p>
                  Откройте раздел <Ui>Feature</Ui> и скопируйте <Ui>Secret Token</Ui>. Впишите его в файл настроек
                  сервера (обычно <Code>/etc/zoom-checker/zoom-checker.env</Code>, для Docker — файл <Code>.env</Code>
                  ):
                </p>
                <pre className="overflow-x-auto rounded-lg bg-muted p-3 font-mono text-xs text-foreground">
                  ZOOM_WEBHOOK_SECRET_TOKEN=скопированный_Secret_Token
                </pre>
                <p>
                  Затем перезапустите сервис: <Code>sudo systemctl restart zoom-checker</Code> (или{' '}
                  <Code>docker compose up -d</Code>).
                </p>
              </Step>
              <Step n={5} title="Включите подписку на события">
                <p>
                  В том же разделе включите <Ui>Event Subscriptions</Ui> и нажмите <Ui>Add Event Subscription</Ui>.
                  Название подписки — любое, например «Посещаемость».
                </p>
              </Step>
              <Step n={6} title="Вставьте адрес сервиса">
                <p>
                  В поле <Ui>Event notification endpoint URL</Ui> вставьте адрес:
                </p>
                <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                  <code className="min-w-0 flex-1 rounded-md bg-muted px-3 py-2 font-mono text-xs break-all text-foreground">
                    {webhookUrl || '—'}
                  </code>
                  {webhookUrl && (
                    <CopyButton value={webhookUrl} label="Скопировать" size="sm" successMessage="Адрес скопирован" />
                  )}
                </div>
                <p>
                  Рядом с полем есть кнопка <Ui>Validate</Ui> (подробнее — в шаге 9). Если Zoom не даёт сохранить
                  подписку без проверки, нажмите её сразу.
                </p>
              </Step>
              <Step n={7} title="Выберите, чьи конференции отслеживать">
                <p>
                  Выберите <Ui>All users in your account</Ui> — тогда события будут приходить о конференциях всех
                  преподавателей аккаунта.
                </p>
              </Step>
              <Step n={8} title="Добавьте события">
                <p>
                  Нажмите <Ui>Add Events</Ui>, откройте раздел <Ui>Meeting</Ui> и отметьте:
                </p>
                <ul className="flex flex-col gap-1 pl-1">
                  {ZOOM_EVENTS.map((e) => (
                    <li key={e} className="flex items-start gap-2">
                      <span aria-hidden="true" className="mt-2 size-1.5 shrink-0 rounded-full bg-sky-600" />
                      <Ui>{e}</Ui>
                    </li>
                  ))}
                </ul>
                <p>
                  Нажмите <Ui>Done</Ui>, затем <Ui>Save</Ui>.
                </p>
              </Step>
              <Step n={9} title="Проверьте адрес кнопкой Validate">
                <p>
                  Нажмите <Ui>Validate</Ui> рядом с адресом. Проверка пройдёт, только если сервис запущен и Secret Token
                  уже записан в настройки (шаг 4). Если Zoom пишет об ошибке — проверьте эти два пункта и что адрес
                  открывается из интернета.
                </p>
              </Step>
              <Step n={10} title="Добавьте права (Scopes)">
                <p>
                  В разделе <Ui>Scopes</Ui> нажмите <Ui>Add Scopes</Ui> и добавьте{' '}
                  <Code>meeting:read:participant:admin</Code> и <Code>meeting:read:meeting:admin</Code>.
                </p>
              </Step>
              <Step n={11} title="Активируйте приложение">
                <p>
                  В разделе <Ui>Activation</Ui> нажмите <Ui>Activate your app</Ui>.
                </p>
              </Step>
              <Step n={12} title="Проверьте, что всё работает">
                <p>
                  Начните тестовую конференцию в Zoom и зайдите в неё. Через несколько секунд статус вверху этой
                  страницы сменится на «Zoom присылает события».
                </p>
              </Step>
            </ol>

            <aside
              aria-labelledby="zoom-tips-title"
              className="flex flex-col gap-2 rounded-lg bg-amber-50 p-4 text-sm ring-1 ring-amber-600/20 dark:bg-amber-400/10 dark:ring-amber-400/20"
            >
              <h3 id="zoom-tips-title" className="flex items-center gap-2 font-medium">
                <Lightbulb className="size-4 text-amber-700 dark:text-amber-300" aria-hidden="true" />
                Полезно знать
              </h3>
              <ul className="flex list-disc flex-col gap-1.5 pl-5 text-pretty">
                <li>
                  События приходят только о конференциях, которые проводит пользователь этого аккаунта Zoom. Если
                  преподаватель запускает занятие с личного аккаунта, система его не увидит.
                </li>
                <li>Адрес сервиса должен открываться из интернета и начинаться с https://.</li>
                <li>
                  Попросите студентов заранее, до входа в конференцию, указать в Zoom имя и фамилию так же, как в списке
                  группы. Готовую памятку можно скопировать в{' '}
                  <Link to="/help#students" className="font-medium underline underline-offset-3">
                    справке
                  </Link>
                  .
                </li>
              </ul>
            </aside>
          </CardContent>
        </CollapsibleContent>
      </Collapsible>
    </Card>
  )
}

import { useEffect, type ReactNode } from 'react'
import {
  ArrowDown,
  ArrowRight,
  CircleCheck,
  CircleHelp,
  CircleX,
  FileSpreadsheet,
  History,
  Lightbulb,
  ListChecks,
  MessageSquareText,
  MonitorCheck,
  Server,
  UserCheck,
  UserMinus,
  Users,
  Video,
  Wifi,
  type LucideIcon,
} from 'lucide-react'
import { Link, useLocation } from 'react-router'

import { CopyButton } from '@/components/CopyButton'
import { FinalStatusBadge } from '@/components/FinalStatusBadge'
import { Page } from '@/components/layout/Page'
import { PageHeader } from '@/components/layout/PageHeader'
import { StatTile } from '@/components/StatTile'
import { StatusBadge } from '@/components/StatusBadge'
import { cn } from '@/lib/utils'
import {
  HELP_SECTIONS,
  STUDENT_HOWTO_LINES,
  STUDENT_MEMO,
  STUDENT_RESPONSIBILITY_TEXT,
  type HelpSectionId,
} from '@/pages/help/helpContent'

const sectionTitle = (id: HelpSectionId) => HELP_SECTIONS.find((s) => s.id === id)!.title

function Section({ id, children }: { id: HelpSectionId; children: ReactNode }) {
  return (
    <section id={id} aria-labelledby={`${id}-title`} className="flex scroll-mt-20 flex-col gap-4">
      <h2 id={`${id}-title`} className="font-heading text-xl font-semibold tracking-tight sm:text-2xl">
        {sectionTitle(id)}
      </h2>
      {children}
    </section>
  )
}

function P({ children, className }: { children: ReactNode; className?: string }) {
  return <p className={cn('text-pretty text-foreground/90', className)}>{children}</p>
}

/** Button / section name as it appears in the app. */
function Ui({ children }: { children: ReactNode }) {
  return <strong className="font-semibold">«{children}»</strong>
}

function Tip({ children }: { children: ReactNode }) {
  return (
    <div className="flex gap-3 rounded-lg bg-sky-50 p-4 text-sm ring-1 ring-sky-600/15 dark:bg-sky-400/10 dark:ring-sky-400/20">
      <Lightbulb className="mt-0.5 size-4 shrink-0 text-sky-700 dark:text-sky-400" aria-hidden="true" />
      <div className="flex flex-col gap-1 text-pretty">{children}</div>
    </div>
  )
}

function FlowBox({ icon: Icon, title, text }: { icon: LucideIcon; title: string; text: string }) {
  return (
    <li className="flex flex-1 flex-col items-center gap-2 rounded-xl bg-card p-4 text-center ring-1 ring-foreground/10">
      <span className="flex size-11 items-center justify-center rounded-full bg-sky-100 text-sky-700 dark:bg-sky-400/15 dark:text-sky-300">
        <Icon className="size-5" aria-hidden="true" />
      </span>
      <span className="font-medium">{title}</span>
      <span className="text-sm text-muted-foreground">{text}</span>
    </li>
  )
}

function FlowArrow() {
  return (
    <li aria-hidden="true" className="flex items-center justify-center text-muted-foreground">
      <ArrowRight className="hidden size-5 md:block" />
      <ArrowDown className="size-5 md:hidden" />
    </li>
  )
}

function Steps({ children }: { children: ReactNode }) {
  return <ol className="flex flex-col gap-4">{children}</ol>
}

function StepItem({ n, title, children }: { n: number; title: string; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span
        aria-hidden="true"
        className="flex size-7 shrink-0 items-center justify-center rounded-full bg-sky-600 text-sm font-semibold text-white dark:bg-sky-500"
      >
        {n}
      </span>
      <div className="flex min-w-0 flex-col gap-1 pt-0.5">
        <h3 className="font-medium">{title}</h3>
        <div className="text-sm text-pretty text-muted-foreground">{children}</div>
      </div>
    </li>
  )
}

function Explained({ visual, children }: { visual: ReactNode; children: ReactNode }) {
  return (
    <div className="grid items-start gap-2 py-3 sm:grid-cols-[13rem_1fr] sm:gap-4">
      <dt className="flex flex-wrap items-center gap-2">{visual}</dt>
      <dd className="text-sm text-pretty text-muted-foreground">{children}</dd>
    </div>
  )
}

function NameExample({ ok, zoom, list, note }: { ok: boolean; zoom: string; list: string; note: string }) {
  const Icon = ok ? CircleCheck : CircleX
  return (
    <li className="flex items-start gap-2 text-sm">
      <Icon
        className={cn('mt-0.5 size-4 shrink-0', ok ? 'text-emerald-700 dark:text-emerald-400' : 'text-red-700 dark:text-red-400')}
        aria-hidden="true"
      />
      <span>
        <span className="sr-only">{ok ? 'Совпадёт: ' : 'Не совпадёт: '}</span>в Zoom «{zoom}», в списке «{list}» —{' '}
        <span className="text-muted-foreground">{note}</span>
      </span>
    </li>
  )
}

function Faq({ q, children }: { q: string; children: ReactNode }) {
  return (
    <details className="group rounded-lg ring-1 ring-foreground/10 open:bg-muted/30">
      <summary className="flex cursor-pointer list-none items-start gap-2 rounded-lg p-4 font-medium outline-none select-none focus-visible:ring-3 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
        <CircleHelp className="mt-0.5 size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="flex-1">{q}</span>
        <ArrowDown
          className="mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180"
          aria-hidden="true"
        />
      </summary>
      <div className="flex flex-col gap-2 px-4 pb-4 pl-10 text-sm text-pretty text-muted-foreground">{children}</div>
    </details>
  )
}

function Toc({ className }: { className?: string }) {
  return (
    <nav aria-label="Содержание" className={className}>
      <p className="mb-2 text-sm font-medium text-muted-foreground">Содержание</p>
      <ol className="flex flex-col gap-0.5">
        {HELP_SECTIONS.map((s, i) => (
          <li key={s.id}>
            <a
              href={`#${s.id}`}
              className="flex gap-2 rounded-md px-2 py-1.5 text-sm outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50"
            >
              <span className="text-muted-foreground tabular-nums" aria-hidden="true">
                {i + 1}.
              </span>
              {s.title}
            </a>
          </li>
        ))}
      </ol>
    </nav>
  )
}

export default function HelpPage() {
  const { hash } = useLocation()

  // Links like /help#students: React Router does not scroll to the anchor by itself.
  useEffect(() => {
    const id = decodeURIComponent(hash.replace(/^#/, ''))
    if (!id) return
    document.getElementById(id)?.scrollIntoView({ block: 'start' })
  }, [hash])

  return (
    <Page>
      <PageHeader
        title="Справка"
        description="Как пользоваться Zoom Checker: от подготовки группы до выгрузки посещаемости."
      />

      <div className="grid gap-8 lg:grid-cols-[14rem_1fr]">
        <aside className="lg:sticky lg:top-20 lg:self-start">
          <Toc className="rounded-xl p-3 ring-1 ring-foreground/10" />
        </aside>

        <div className="flex min-w-0 flex-col gap-12 text-base">
          <Section id="how">
            <P>
              Вы проводите занятие в Zoom как обычно. Zoom сам сообщает системе, кто вошёл в конференцию и кто вышел.
              Система находит студента в списке группы по имени и отмечает присутствие. Вы видите всё на экране
              занятия — без обновления страницы.
            </P>
            <ol aria-label="Схема работы" className="flex flex-col items-stretch gap-2 md:flex-row">
              <FlowBox icon={Video} title="Zoom" text="Студент входит в конференцию или выходит из неё" />
              <FlowArrow />
              <FlowBox icon={Server} title="Система" text="Находит студента по имени и считает время" />
              <FlowArrow />
              <FlowBox icon={MonitorCheck} title="Экран преподавателя" text="Статус меняется через пару секунд" />
            </ol>
            <P>
              Главное — <strong>имя в Zoom должно совпадать с именем в списке группы</strong>. Большие и маленькие
              буквы, лишние пробелы и «ё/е» не важны, а вот порядок слов и сокращения — важны:
            </P>
            <ul className="flex flex-col gap-1.5">
              <NameExample ok zoom="иванов  иван" list="Иванов Иван" note="совпадёт" />
              <NameExample ok zoom="Фёдоров Максим" list="Федоров Максим" note="совпадёт" />
              <NameExample ok={false} zoom="Ваня" list="Иванов Иван" note="не совпадёт, нужно сопоставить вручную" />
              <NameExample ok={false} zoom="Иван Иванов" list="Иванов Иван" note="другой порядок слов — не совпадёт" />
            </ul>
          </Section>

          <Section id="prepare">
            <Steps>
              <StepItem n={1} title="Создайте группу">
                Раздел <Ui>Группы</Ui> → <Ui>Новая группа</Ui>. Достаточно названия, например «DevOps-1».
              </StepItem>
              <StepItem n={2} title="Загрузите список студентов">
                Скопируйте фамилии и имена из Excel и вставьте — по одному студенту в строке. Это можно сделать сразу
                в окне новой группы или позже на странице группы, вкладка <Ui>Импорт списка</Ui>: там же можно
                загрузить файл CSV или Excel. Перед сохранением система покажет ошибки и повторы.
              </StepItem>
              <StepItem n={3} title="Создайте занятие со ссылкой Zoom">
                Раздел <Ui>Занятия</Ui> → <Ui>Новое занятие</Ui>. Выберите группу и время, вставьте ссылку на
                конференцию или текст приглашения целиком — номер конференции система найдёт сама. Для курса можно
                сразу создать занятия на несколько недель вперёд.
              </StepItem>
              <StepItem n={4} title="Отправьте студентам памятку">
                Попросите студентов указывать в Zoom фамилию и имя как в списке. Готовый текст — в разделе{' '}
                <a href="#students" className="font-medium text-foreground underline underline-offset-3">
                  «Памятка для студентов»
                </a>
                .
              </StepItem>
            </Steps>
            <Tip>
              <p>
                Личная ссылка вида <span className="font-mono">zoom.us/my/…</span> не подходит — в ней нет номера
                конференции. Используйте ссылку с номером или сам номер (Meeting ID).
              </p>
            </Tip>
          </Section>

          <Section id="during">
            <P>
              Откройте занятие в разделе <Ui>Занятия</Ui>. Экран обновляется сам. Вверху — счётчики:
            </P>
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-5" aria-label="Пример счётчиков" role="group">
              <StatTile label="В группе" value={25} icon={Users} />
              <StatTile label="Сейчас в Zoom" value={21} icon={Wifi} tone="success" />
              <StatTile label="Были хотя бы раз" value={23} icon={UserCheck} tone="info" />
              <StatTile label="Не подключались" value={2} icon={UserMinus} tone="danger" />
              <StatTile label="Не идентифицированы" value={1} icon={CircleHelp} tone="warning" />
            </div>
            <P>Возле каждого студента — его состояние и время в Zoom:</P>
            <dl className="divide-y rounded-xl px-4 ring-1 ring-foreground/10">
              <Explained visual={<StatusBadge kind="current" status="online" />}>
                Студент сейчас на занятии. Время присутствия растёт на глазах.
              </Explained>
              <Explained visual={<StatusBadge kind="current" status="left" />}>
                Был на занятии, сейчас вышел. Если вернётся, время продолжит считаться — перерыв не засчитывается.
              </Explained>
              <Explained visual={<StatusBadge kind="current" status="not_seen" />}>
                Ещё ни разу не подключался — или вошёл под другим именем (посмотрите «Не идентифицированы»).
              </Explained>
            </dl>

            <h3 className="mt-2 text-lg font-semibold">Что делать с «Не идентифицированы»</h3>
            <P>
              Здесь участники Zoom, чьё имя не нашлось в списке группы, например «iPhone» или «Саша». Для каждого
              видно, когда вошёл и сколько пробыл.
            </P>
            <dl className="divide-y rounded-xl px-4 ring-1 ring-foreground/10">
              <Explained visual={<StatusBadge kind="resolution" status="unidentified" />}>
                Нажмите <Ui>Это студент…</Ui> и выберите, кто это. Всё накопленное время перейдёт студенту. Отметьте{' '}
                <Ui>Запомнить это имя для следующих занятий</Ui> — тогда в следующий раз студент найдётся сам.
              </Explained>
              <Explained visual={<StatusBadge kind="resolution" status="conflict" />}>
                Имя подходит сразу нескольким студентам (например, двум «Ивановым Иванам»). Выберите нужного через{' '}
                <Ui>Это студент…</Ui>.
              </Explained>
              <Explained visual={<StatusBadge kind="resolution" status="ignored" />}>
                Нажмите <Ui>Не студент</Ui> для гостя, ассистента или записи — участник уйдёт в{' '}
                <Ui>Скрытые участники</Ui>. Отметьте <Ui>Всегда скрывать это имя</Ui>, чтобы не делать этого каждый
                раз. Ошиблись — нажмите <Ui>Вернуть</Ui>.
              </Explained>
            </dl>
            <Tip>
              <p>
                Занятие завершается само, когда организатор завершает конференцию в Zoom. Можно завершить и вручную
                кнопкой <Ui>Завершить занятие</Ui>; завершённое вручную занятие можно снова открыть кнопкой{' '}
                <Ui>Возобновить</Ui>.
              </p>
            </Tip>
          </Section>

          <Section id="after">
            <P>После занятия у каждого студента стоит итог — система считает его по времени в Zoom:</P>
            <dl className="divide-y rounded-xl px-4 ring-1 ring-foreground/10">
              <Explained visual={<FinalStatusBadge status="present" />}>
                Студент был на занятии. Сколько времени для этого нужно пробыть в Zoom, задаёт администратор в
                настройках.
              </Explained>
              <Explained visual={<FinalStatusBadge status="absent" />}>Студента на занятии не было.</Explained>
              <Explained visual={<FinalStatusBadge status="present" manual />}>
                Отметку поставил преподаватель. Ручная отметка важнее автоматической и не меняется при пересчёте.
              </Explained>
            </dl>
            <ul className="flex flex-col gap-3">
              <li className="flex gap-3">
                <ListChecks className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="text-sm text-pretty">
                  <strong>Исправить отметку.</strong> В колонке <Ui>Итог</Ui> нажмите <Ui>Был</Ui> или{' '}
                  <Ui>Не был</Ui>. Чтобы снова доверить отметку системе, нажмите <Ui>вернуть авто</Ui>. Каждое
                  изменение сохраняется в истории: видно, кто и когда его сделал.
                </span>
              </li>
              <li className="flex gap-3">
                <MessageSquareText className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="text-sm text-pretty">
                  <strong>Комментарий.</strong> Например: «был с телефона без звука» или «отпущен к врачу».
                </span>
              </li>
              <li className="flex gap-3">
                <History className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="text-sm text-pretty">
                  <strong>История.</strong> Нажмите на студента — откроются все его входы и выходы, имена в Zoom и кто
                  что менял.
                </span>
              </li>
              <li className="flex gap-3">
                <FileSpreadsheet className="mt-0.5 size-5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span className="text-sm text-pretty">
                  <strong>Выгрузка в Excel.</strong> На странице занятия — кнопки <Ui>Скачать Excel</Ui> и{' '}
                  <Ui>Скачать CSV</Ui>. Посещаемость за месяц или семестр — в разделе <Ui>Группы</Ui> → нужная группа
                  → <Ui>Сводка посещаемости</Ui>.
                </span>
              </li>
            </ul>
          </Section>

          <Section id="faq">
            <div className="flex flex-col gap-2">
              <Faq q="Студент зашёл с телефона и с компьютера одновременно">
                <p>
                  Ничего делать не нужно. Это одно присутствие: время не удваивается, а в истории студента видны оба
                  устройства.
                </p>
              </Faq>
              <Faq q="Студент несколько раз выходил и заходил снова">
                <p>Время складывается из всех подключений, перерывы не засчитываются. Статус показывает, где он сейчас.</p>
              </Faq>
              <Faq q="Студент сменил имя в Zoom во время занятия">
                <p>
                  Zoom сообщает новое имя, только когда участник выходит. Чтобы студент сразу появился в списке,
                  попросите его выйти и зайти снова по той же ссылке (если включён зал ожидания — впустите его). Если
                  новое имя не совпадает со списком, участник появится в «Не идентифицированы» — сопоставьте его со
                  студентом и, если нужно, запомните имя.
                </p>
              </Faq>
              <Faq q="Одна ссылка Zoom на два занятия подряд или на несколько групп">
                <p>
                  Одна конференция Zoom засчитывается только одному занятию. Сдвоенную пару одной группы создайте
                  одним занятием на всё время. Если два занятия идут подряд по одной ссылке, между ними завершите
                  конференцию для всех и запустите её снова. Для совместной лекции нескольких групп создайте общую
                  группу (поток) со студентами всех групп и одно занятие для неё.
                </p>
                <p>
                  Если конференция привязалась не к тому занятию, на странице занятия в блоке{' '}
                  <Ui>Конференции Zoom</Ui> нажмите <Ui>Это другая конференция</Ui>, а затем привяжите её к нужному
                  занятию{' '}
                  <Link to="/zoom" className="font-medium text-foreground underline underline-offset-3">
                    на странице Zoom
                  </Link>
                  .
                </p>
              </Faq>
              <Faq q="Занятие давно закончилось, а на экране всё ещё «Идёт»">
                <p>
                  Значит, Zoom не сообщил, что конференция закончилась. Нажмите <Ui>Завершить занятие</Ui> в жёлтом
                  блоке или в меню <Ui>Действия</Ui> и укажите, когда урок закончился на самом деле. Если из Zoom 6
                  часов ничего не приходит, сервис завершает такое занятие сам.
                </p>
              </Faq>
              <Faq q="Я, преподаватель, вижу себя в «Не идентифицированы»">
                <p>
                  Нажмите <Ui>Не студент</Ui> и отметьте <Ui>Всегда скрывать это имя</Ui>. Администратор также может
                  включить настройку «Не учитывать организатора конференции».
                </p>
              </Faq>
              <Faq q="Студента нет в списке группы, но он был на занятии">
                <p>
                  Добавьте его в группу (раздел <Ui>Группы</Ui>). Если он не нашёлся на занятии сам, сопоставьте его
                  вручную в «Не идентифицированы» кнопкой <Ui>Это студент…</Ui>.
                </p>
              </Faq>
              <Faq q="Экран занятия не меняется, хотя студенты уже в Zoom">
                <p>
                  Проверьте, что в занятии указан тот же номер конференции, что и в Zoom, и что занятие проводится с
                  рабочего аккаунта Zoom. Затем откройте страницу{' '}
                  <Link to="/zoom" className="font-medium text-foreground underline underline-offset-3">
                    Zoom
                  </Link>
                  : если там красный или жёлтый статус — Zoom не присылает события, обратитесь к администратору.
                </p>
                <p>
                  Если конференция идёт под другим номером, на странице занятия появится «Похоже, конференция идёт под
                  другим номером» — нажмите <Ui>Привязать к этому занятию</Ui>. Если конференция прошла, но не
                  привязалась к занятию, она будет на странице Zoom в списке «Несвязанные сессии Zoom» — нажмите{' '}
                  <Ui>Привязать к занятию</Ui>.
                </p>
              </Faq>
            </div>
          </Section>

          <Section id="students">
            <P>Отправьте этот текст студентам перед началом курса — например, в чат группы или по почте.</P>
            <Tip>
              <p>
                На странице группы, вкладка <Ui>Памятка для студентов</Ui>, есть такая же памятка вместе со списком
                имён группы — каждый студент увидит, как именно ему подписаться в Zoom.
              </p>
            </Tip>
            <figure className="flex flex-col gap-4 rounded-xl bg-card p-5 ring-1 ring-foreground/10">
              <blockquote className="border-l-4 border-sky-500 pl-4 text-pretty">{STUDENT_RESPONSIBILITY_TEXT}</blockquote>
              <ul className="flex list-disc flex-col gap-1.5 pl-5 text-sm text-pretty text-muted-foreground">
                {STUDENT_HOWTO_LINES.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
              <div>
                <CopyButton value={STUDENT_MEMO} label="Скопировать памятку" successMessage="Памятка скопирована" />
              </div>
            </figure>
          </Section>
        </div>
      </div>
    </Page>
  )
}

import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Users } from 'lucide-react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { newPasswordProblem, validatePasswordForm } from '@/components/auth/password'
import { ConfirmDialog } from '@/components/ConfirmDialog'
import { CopyButton } from '@/components/CopyButton'
import { EmptyState } from '@/components/EmptyState'
import { ErrorState } from '@/components/ErrorState'
import { FinalStatusBadge } from '@/components/FinalStatusBadge'
import { LiveDuration } from '@/components/LiveDuration'
import { LoadingState, PageSpinner } from '@/components/LoadingState'
import { StatTile } from '@/components/StatTile'
import { auditActionLabel } from '@/components/status-meta'
import { StatusBadge } from '@/components/StatusBadge'
import { ApiError } from '@/lib/api'
import { markReceived } from '@/lib/liveClock'

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
})

describe('StatusBadge', () => {
  it.each([
    ['current', 'online', 'В Zoom', 'success'],
    ['current', 'left', 'Вышел', 'warning'],
    ['current', 'not_seen', 'Не подключался', 'danger'],
    ['resolution', 'unidentified', 'Не опознан', 'warning'],
    ['resolution', 'conflict', 'Конфликт имён', 'danger'],
    ['resolution', 'ignored', 'Скрыт', 'muted'],
    ['lesson', 'planned', 'Запланировано', 'info'],
    ['lesson', 'active', 'Идёт', 'success'],
    ['lesson', 'finished', 'Завершено', 'muted'],
    ['event', 'failed', 'Ошибка', 'danger'],
  ] as const)('%s/%s → "%s" (%s)', (kind, status, label, tone) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    render(<StatusBadge kind={kind} status={status as any} />)
    const badge = screen.getByText(label).closest('[data-slot="status-badge"]')
    expect(badge).toHaveAttribute('data-tone', tone)
    // Never color alone: text is present and the icon is hidden from screen readers.
    expect(badge?.querySelector('[aria-hidden="true"]')).not.toBeNull()
    expect(badge).toHaveAttribute('title')
  })

  it('allows a custom label', () => {
    render(<StatusBadge kind="current" status="not_seen" label="Не был" />)
    expect(screen.getByText('Не был')).toBeInTheDocument()
  })

  it('renders unknown values instead of crashing', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    render(<StatusBadge kind="lesson" status={'paused' as any} />)
    expect(screen.getByText('paused')).toBeInTheDocument()
  })
})

describe('FinalStatusBadge', () => {
  it('shows present/absent', () => {
    render(
      <>
        <FinalStatusBadge status="present" />
        <FinalStatusBadge status="absent" />
      </>,
    )
    expect(screen.getByText('Был')).toBeInTheDocument()
    expect(screen.getByText('Не был')).toBeInTheDocument()
    expect(screen.queryByText('вручную')).toBeNull()
  })

  it('marks manual decisions', () => {
    render(<FinalStatusBadge status="absent" manual />)
    expect(screen.getByText('вручную')).toHaveAttribute('title', 'Отметка поставлена вручную')
  })
})

describe('LiveDuration', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-06T15:00:00Z'))
  })
  afterEach(() => vi.useRealTimers())

  it('ticks while online', () => {
    markReceived('2026-10-06T15:00:00Z', Date.now())
    render(<LiveDuration seconds={4439} asOf="2026-10-06T15:00:00Z" online />)
    const timer = screen.getByRole('timer')
    expect(timer).toHaveTextContent('01:13')
    act(() => {
      vi.advanceTimersByTime(1000)
    })
    expect(timer).toHaveTextContent('01:14')
    expect(timer).toHaveAttribute('aria-label', '1 ч 14 мин')
  })

  it('stays still while offline and supports other formats', () => {
    render(<LiveDuration seconds={2700} asOf="2026-10-06T15:00:00Z" online={false} format="long" />)
    act(() => {
      vi.advanceTimersByTime(5000)
    })
    expect(screen.getByRole('timer')).toHaveTextContent('45 мин')
  })
})

describe('EmptyState', () => {
  it('renders icon, title, text and action', () => {
    render(<EmptyState icon={Users} title="Групп пока нет" text="Создайте первую группу." action={<button>Создать</button>} />)
    expect(screen.getByText('Групп пока нет')).toBeInTheDocument()
    expect(screen.getByText('Создайте первую группу.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Создать' })).toBeInTheDocument()
  })
})

describe('ErrorState', () => {
  it('shows the API message and retries', async () => {
    const onRetry = vi.fn()
    render(<ErrorState error={new ApiError(404, 'not_found', 'Группа не найдена')} onRetry={onRetry} />)
    expect(screen.getByRole('alert')).toHaveTextContent('Не найдено')
    expect(screen.getByText('Группа не найдена')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Повторить' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it.each([
    [new ApiError(0, 'network', 'Нет связи'), 'Нет связи с сервером'],
    [new ApiError(403, 'forbidden', 'Нельзя'), 'Нет доступа'],
    [new Error('x'), 'Не удалось загрузить данные'],
  ])('titles %s', (error, title) => {
    render(<ErrorState error={error} />)
    expect(screen.getByText(title)).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })
})

describe('LoadingState', () => {
  it.each(['page', 'table', 'cards', 'list', 'tiles'] as const)('%s variant is an accessible status', (variant) => {
    render(<LoadingState variant={variant} />)
    expect(screen.getByRole('status')).toHaveTextContent('Загрузка…')
  })

  it('PageSpinner has a label', () => {
    render(<PageSpinner label="Проверяем вход…" />)
    expect(screen.getByRole('status')).toHaveTextContent('Проверяем вход…')
  })
})

describe('StatTile', () => {
  it('shows the number and label', () => {
    render(<StatTile label="Сейчас в Zoom" value={21} tone="success" />)
    expect(screen.getByText('21')).toBeInTheDocument()
    expect(screen.getByText('Сейчас в Zoom')).toBeInTheDocument()
    expect(screen.queryByRole('button')).toBeNull()
  })

  it('becomes a toggle button when clickable', async () => {
    const onClick = vi.fn()
    render(<StatTile label="Не подключались" value={2} onClick={onClick} active />)
    const button = screen.getByRole('button', { name: /Не подключались/ })
    expect(button).toHaveAttribute('aria-pressed', 'true')
    await userEvent.click(button)
    expect(onClick).toHaveBeenCalled()
  })

  it('shows a skeleton while loading', () => {
    render(<StatTile label="В группе" value={25} loading />)
    expect(screen.queryByText('25')).toBeNull()
  })
})

describe('CopyButton', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('copies with the Clipboard API and confirms', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: true })
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    render(<CopyButton value="123 456 7890" srLabel="Скопировать Zoom ID" />)
    await userEvent.click(screen.getByRole('button', { name: 'Скопировать Zoom ID' }))
    expect(writeText).toHaveBeenCalledWith('123 456 7890')
    expect(toastMock.success).toHaveBeenCalledWith('Скопировано')
  })

  it('falls back to execCommand outside secure contexts', async () => {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: false })
    const exec = vi.fn().mockReturnValue(true)
    Object.defineProperty(document, 'execCommand', { configurable: true, value: exec })
    render(<CopyButton value="https://example.com/hook" label="Скопировать адрес" />)
    await userEvent.click(screen.getByRole('button', { name: 'Скопировать адрес' }))
    expect(exec).toHaveBeenCalledWith('copy')
    expect(toastMock.success).toHaveBeenCalled()
  })

  it('reports failures', async () => {
    Object.defineProperty(window, 'isSecureContext', { configurable: true, value: false })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn().mockReturnValue(false) })
    render(<CopyButton value="x" />)
    await userEvent.click(screen.getByRole('button', { name: 'Скопировать' }))
    expect(toastMock.error).toHaveBeenCalled()
  })
})

describe('ConfirmDialog', () => {
  it('waits for an async confirm and then closes', async () => {
    let resolve: () => void = () => {}
    const onConfirm = vi.fn(() => new Promise<void>((r) => (resolve = r)))
    const user = userEvent.setup()
    render(
      <ConfirmDialog
        trigger={<button>Удалить</button>}
        title="Удалить занятие?"
        description="Это действие нельзя отменить."
        confirmLabel="Удалить"
        destructive
        onConfirm={onConfirm}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Удалить' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent('Это действие нельзя отменить.')
    const confirm = screen.getAllByRole('button', { name: 'Удалить' }).at(-1)!
    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(confirm).toBeDisabled()
    await act(async () => resolve())
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
  })

  it('stays open when the action fails', async () => {
    const user = userEvent.setup()
    render(
      <ConfirmDialog
        trigger={<button>Завершить</button>}
        title="Завершить занятие?"
        confirmLabel="Завершить занятие"
        onConfirm={() => Promise.reject(new Error('nope'))}
      />,
    )
    await user.click(screen.getByRole('button', { name: 'Завершить' }))
    await user.click(await screen.findByRole('button', { name: 'Завершить занятие' }))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Завершить занятие' })).toBeEnabled()
  })

  it('supports controlled mode and cancel', async () => {
    const onOpenChange = vi.fn()
    const user = userEvent.setup()
    render(<ConfirmDialog open title="Точно?" onOpenChange={onOpenChange} onConfirm={() => {}} />)
    await user.click(screen.getByRole('button', { name: 'Отмена' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})

describe('status labels (review)', () => {
  it.each([
    ['stream', 'live', 'Обновляется автоматически', 'success'],
    ['stream', 'reconnecting', 'Подключаемся…', 'info'],
    ['stream', 'polling', 'Обновление раз в 5 секунд', 'warning'],
    ['import', 'new', 'Новый', 'success'],
    ['import', 'existing', 'Уже в группе', 'muted'],
    ['import', 'error', 'Ошибка', 'danger'],
    ['import', 'duplicate', 'Повтор', 'warning'],
    ['import', 'archive', 'В архив', 'warning'],
  ] as const)('%s/%s → "%s" (%s)', (kind, status, label, tone) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    render(<StatusBadge kind={kind} status={status as any} />)
    const badge = screen.getByText(label).closest('[data-slot="status-badge"]')
    expect(badge).toHaveAttribute('data-tone', tone)
    expect(badge).toHaveAttribute('title')
  })

  it.each(['__proto__', 'constructor', 'toString'])('never resolves %j to Object.prototype', (value) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    render(<StatusBadge kind="current" status={value as any} />)
    expect(screen.getByText(value).closest('[data-slot="status-badge"]')).toHaveAttribute('data-tone', 'neutral')
  })

  it('FinalStatusBadge shows an unknown value instead of crashing', () => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    render(<FinalStatusBadge status={'excused' as any} manual />)
    expect(screen.getByText('excused')).toBeInTheDocument()
    expect(screen.getByText('вручную')).toBeInTheDocument()
  })

  it('labels audit actions in Russian and passes unknown ones through', () => {
    expect(auditActionLabel('attendance.status')).toBe('Изменена отметка о посещении')
    expect(auditActionLabel('participant.map')).toBe('Участник Zoom сопоставлен')
    expect(auditActionLabel('lesson.finish')).toBe('Занятие завершено')
    expect(auditActionLabel('something.new')).toBe('something.new')
    expect(auditActionLabel('__proto__')).toBe('__proto__')
  })
})

describe('password policy (review)', () => {
  it('counts characters like the server (code points, not UTF-16 units)', () => {
    // 4 emoji = 8 UTF-16 units but 4 characters: the server rejects it as too short.
    expect(newPasswordProblem('🔑🔒🔑🔒')).toMatch(/не короче 8/)
    expect(newPasswordProblem('пароль12')).toBeNull()
  })

  it('rejects one repeated character and overly long passwords', () => {
    expect(newPasswordProblem('aaaaaaaa')).toMatch(/одного повторяющегося символа/)
    expect(newPasswordProblem('a'.repeat(200) + 'b'.repeat(57))).toMatch(/слишком длинный/)
    expect(newPasswordProblem('ab'.repeat(128))).toBeNull()
  })

  it('validates the whole change-password form', () => {
    expect(validatePasswordForm('', 'new-secret-42', 'new-secret-42')).toBe('Введите текущий пароль.')
    expect(validatePasswordForm('old', 'new-secret-42', 'new-secret-24')).toMatch(/не совпадают/)
    expect(validatePasswordForm('new-secret-42', 'new-secret-42', 'new-secret-42')).toMatch(/отличаться/)
    expect(validatePasswordForm('old', 'new-secret-42', 'new-secret-42')).toBeNull()
  })
})

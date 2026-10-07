import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const toastMock = vi.hoisted(() => ({ error: vi.fn(), success: vi.fn() }))
vi.mock('sonner', () => ({ toast: toastMock }))

import { ImportWizard } from '@/components/groups/ImportWizard'
import type { ImportPreview, ImportRow } from '@/lib/types'
import { errorResponse, jsonResponse, mockFetch, renderWithClient } from '@/test/utils'

const TZ8 = 'Невозможно однозначно идентифицировать студентов по Zoom display name.'

const row = (line: number, full_name: string, status: ImportRow['status'] = 'new', message = ''): ImportRow => ({
  line,
  external_id: '',
  full_name,
  normalized_name: full_name.toLowerCase(),
  status,
  message,
  student_id: null,
})

function preview(rows: ImportRow[], over: Partial<ImportPreview> = {}): ImportPreview {
  const counts = { new: 0, existing: 0, error: 0, duplicate: 0, archive: 0 }
  for (const r of rows) counts[r.status]++
  return {
    mode: 'add',
    rows,
    counts,
    detected: { format: 'text', encoding: 'utf-8', delimiter: '', name_column: '' },
    name_format: 'as_is',
    has_patronymics: false,
    ...over,
  }
}

beforeEach(() => {
  toastMock.error.mockReset()
  toastMock.success.mockReset()
})

afterEach(() => {
  vi.unstubAllGlobals()
})

async function pasteList(user: ReturnType<typeof userEvent.setup>, text: string) {
  const box = screen.getByLabelText(/Или вставьте список/)
  await user.click(box)
  await user.paste(text)
  await user.click(screen.getByRole('button', { name: 'Проверить список' }))
}

describe('ImportWizard', () => {
  it('pastes a list, drops patronymics and saves the names as written', async () => {
    const full = preview([row(1, 'Иванов Иван Иванович'), row(2, 'Петров Пётр Петрович')], { has_patronymics: true })
    const short = preview([row(1, 'Иванов Иван'), row(2, 'Петров Пётр')], { has_patronymics: true, name_format: 'last_first' })
    const previewCalls: URL[] = []
    let commitBody: unknown = null
    mockFetch({
      'POST /api/groups/5/import/preview': (url: URL) => {
        previewCalls.push(url)
        return jsonResponse(url.searchParams.get('name_format') === 'last_first' ? short : full)
      },
      'POST /api/groups/5/import/commit': (_url: URL, init: RequestInit) => {
        commitBody = JSON.parse(String(init.body))
        return jsonResponse({ created: 2, existing: 0, archived: 0, updated: 0 })
      },
    })
    const onDone = vi.fn()
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} onDone={onDone} />)

    // A group without students does not need the add/replace choice.
    expect(screen.queryByText('Заменить список')).toBeNull()
    await pasteList(user, 'Иванов Иван Иванович\nПетров Пётр Петрович')

    expect(await screen.findByText('Иванов Иван Иванович')).toBeInTheDocument()
    expect(previewCalls[0].searchParams.get('mode')).toBe('add')
    expect(previewCalls[0].searchParams.get('name_format')).toBe('as_is')
    expect(screen.getByText('В списке есть отчества')).toBeInTheDocument()
    expect(screen.getByText('Новых: 2')).toBeInTheDocument()
    expect(screen.getByText('Распознано: простой текст · кодировка UTF-8')).toBeInTheDocument()

    const toggle = screen.getByRole('switch', { name: 'Оставить только Фамилию и Имя' })
    expect(toggle).not.toBeChecked()
    await user.click(toggle)
    expect(await screen.findByText('Иванов Иван')).toBeInTheDocument()
    expect(screen.queryByText('Иванов Иван Иванович')).toBeNull()
    expect(screen.getByRole('switch', { name: 'Оставить только Фамилию и Имя' })).toBeChecked()
    expect(previewCalls.at(-1)?.searchParams.get('name_format')).toBe('last_first')

    await user.click(screen.getByRole('button', { name: 'Сохранить список' }))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith({ created: 2, existing: 0, archived: 0, updated: 0 }))
    // The server re-applies the format to the names as written in the list.
    expect(commitBody).toEqual({
      mode: 'add',
      name_format: 'last_first',
      rows: [
        { external_id: '', full_name: 'Иванов Иван Иванович', line: 1 },
        { external_id: '', full_name: 'Петров Пётр Петрович', line: 2 },
      ],
    })
    expect(toastMock.success).toHaveBeenCalledWith('Список студентов сохранён', { description: 'Добавлено: 2' })
    // Back to step 1 with an empty box.
    expect(screen.getByLabelText(/Или вставьте список/)).toHaveValue('')
  })

  it('blocks saving a file with duplicate names and explains why (TZ §8)', async () => {
    // As the server words it (internal/importer markDuplicates): the other line and a letter per row.
    const advice = 'Если это один и тот же студент — удалите лишнюю строку. Если это разные люди — добавьте уточнение, например'
    const dup2 = `${TZ8} Такое же имя в строке 3. ${advice} «Иванов Иван А.».`
    const dup3 = `${TZ8} Такое же имя в строке 2. ${advice} «Иванов Иван Б.».`
    const blocked = preview([row(2, 'Иванов Иван', 'duplicate', dup2), row(3, 'Иванов Иван', 'duplicate', dup3), row(4, 'Петров Пётр')], {
      detected: { format: 'csv', encoding: 'utf-8', delimiter: ';', name_column: 'ФИО' },
    })
    const fetchMock = mockFetch({ 'POST /api/groups/5/import/preview': blocked })
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents />)

    const file = new File(['ФИО\nИванов Иван\nИванов Иван\nПетров Пётр'], 'list.csv', { type: 'text/csv' })
    await user.upload(screen.getByLabelText('Файл со списком студентов'), file)

    expect(await screen.findByText('Исправьте файл и загрузите снова')).toBeInTheDocument()
    // The TZ §8 error itself, not only per-row hints.
    expect(within(screen.getByTestId('import-blocked')).getByText(TZ8)).toBeInTheDocument()
    expect(screen.getByText(dup2)).toBeInTheDocument()
    expect(screen.getByText(dup3)).toBeInTheDocument()
    expect(screen.getByText(/Строки с проблемами:\s*2, 3/)).toBeInTheDocument()
    // One repeated name, not "2 problems".
    expect(screen.getByText('Повторяются имена: 1')).toBeInTheDocument()
    const banner = screen.getByTestId('import-blocked')
    expect(within(banner).getByText('Строки 2 и 3 — одно и то же имя «Иванов Иван»')).toBeInTheDocument()
    // The case of one student entered twice is covered, not only real namesakes.
    expect(banner).toHaveTextContent(
      'Если это один и тот же студент — удалите лишнюю строку в файле и загрузите его снова. Если это разные люди — добавьте уточнение, например «Иванов Иван А.» и «Иванов Иван Б.»',
    )
    // A file cannot be edited here.
    expect(within(banner).queryByRole('button', { name: /Убрать повтор/ })).toBeNull()
    expect(screen.getByText(/разделитель: точка с запятой · колонка с именами: «ФИО»/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить список' })).toBeDisabled()
    const problemRows = document.querySelectorAll('tr[data-problem]')
    expect(problemRows).toHaveLength(2)
    expect(within(problemRows[0] as HTMLElement).getByText('Повтор')).toBeInTheDocument()
    // The file went as multipart, with the chosen mode.
    const [, init] = fetchMock.mock.calls[0]
    expect(init?.body).toBeInstanceOf(FormData)

    // "Fix and reload" goes back to step 1.
    await user.click(screen.getByRole('button', { name: 'Загрузить исправленный файл' }))
    expect(screen.getByRole('button', { name: 'Выбрать файл' })).toBeInTheDocument()
  })

  it('removes an accidentally repeated line of a pasted list with one click', async () => {
    const pasted = 'Иванов Иван\nПетрова Анна\nФедоров Артём\n  петрова   АННА '
    const dup = `${TZ8} Такое же имя в строке 2.`
    const blocked = preview([
      row(1, 'Иванов Иван'),
      { ...row(2, 'Петрова Анна', 'duplicate', dup), normalized_name: 'петрова анна' },
      row(3, 'Федоров Артём'),
      { ...row(4, 'петрова АННА', 'duplicate', dup), normalized_name: 'петрова анна' },
    ])
    const fixed = preview([row(1, 'Иванов Иван'), row(2, 'Петрова Анна'), row(3, 'Федоров Артём')])
    const sentTexts: string[] = []
    mockFetch({
      'POST /api/groups/5/import/preview': (_url: URL, init: RequestInit) => {
        const { text } = JSON.parse(String(init.body)) as { text: string }
        sentTexts.push(text)
        return jsonResponse(sentTexts.length === 1 ? blocked : fixed)
      },
    })
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} />)
    await pasteList(user, pasted)

    const banner = await screen.findByTestId('import-blocked')
    // The name as written first, not the messy later spelling.
    expect(within(banner).getByText('Строки 2 и 4 — одно и то же имя «Петрова Анна»')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Исправить список' })).toBeInTheDocument()

    await user.click(within(banner).getByRole('button', { name: 'Убрать повтор «Петрова Анна»: удалить строку 4' }))
    await waitFor(() => expect(sentTexts).toHaveLength(2))
    expect(sentTexts[1]).toBe('Иванов Иван\nПетрова Анна\nФедоров Артём')
    expect(await screen.findByRole('button', { name: 'Сохранить список' })).toBeEnabled()
    expect(screen.queryByTestId('import-blocked')).toBeNull()

    // The box holds the fixed list too.
    await user.click(screen.getByRole('button', { name: 'Назад' }))
    expect(screen.getByLabelText(/Или вставьте список/)).toHaveValue('Иванов Иван\nПетрова Анна\nФедоров Артём')
  })

  it('shows the re-checked preview when the server rejects the commit (422)', async () => {
    const ok = preview([row(1, 'Иванов Иван'), row(2, 'Петров Пётр')])
    const recheck = preview([row(1, 'Иванов Иван', 'error', 'Это имя уже записано как дополнительное имя студента «Петров Пётр».'), row(2, 'Петров Пётр')])
    mockFetch({
      'POST /api/groups/5/import/preview': ok,
      'POST /api/groups/5/import/commit': () =>
        errorResponse(422, 'validation', 'Список изменился с момента проверки.', { preview: recheck }),
    })
    const onDone = vi.fn()
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} onDone={onDone} />)
    await pasteList(user, 'Иванов Иван\nПетров Пётр')
    await user.click(await screen.findByRole('button', { name: 'Сохранить список' }))

    expect(await screen.findByText(/Список изменился с момента проверки\./)).toBeInTheDocument()
    expect(screen.getByText('Это имя уже записано как дополнительное имя студента «Петров Пётр».')).toBeInTheDocument()
    expect(screen.getByText('Исправьте список и проверьте снова')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Сохранить список' })).toBeDisabled()
    expect(onDone).not.toHaveBeenCalled()
    expect(toastMock.success).not.toHaveBeenCalled()
  })

  it('asks before archiving students missing from a replacing list', async () => {
    const replacing = preview([row(1, 'Иванов Иван', 'existing'), row(0, 'Сидоров Алексей', 'archive')], { mode: 'replace' })
    let commitBody: { mode?: string } = {}
    const fetchMock = mockFetch({
      'POST /api/groups/5/import/preview': replacing,
      'POST /api/groups/5/import/commit': (_url: URL, init: RequestInit) => {
        commitBody = JSON.parse(String(init.body))
        return jsonResponse({ created: 0, existing: 1, archived: 1, updated: 0 })
      },
    })
    const onDone = vi.fn()
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents onDone={onDone} />)

    await user.click(screen.getByRole('radio', { name: /Заменить список/ }))
    await pasteList(user, 'Иванов Иван')
    expect(await screen.findByText('Сидоров Алексей')).toBeInTheDocument()
    expect(new URL(String(fetchMock.mock.calls[0][0]), 'http://x').searchParams.get('mode')).toBe('replace')

    await user.click(screen.getByRole('button', { name: 'Сохранить список' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText('Перенести в архив 1 студента?')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Сохранить и перенести в архив' }))

    await waitFor(() => expect(onDone).toHaveBeenCalled())
    expect(commitBody.mode).toBe('replace')
    // Archive rows are not part of the list being saved.
    expect(commitBody).toMatchObject({ rows: [{ external_id: '', full_name: 'Иванов Иван', line: 1 }] })
    expect(toastMock.success).toHaveBeenCalledWith('Список студентов сохранён', {
      description: 'Перенесено в архив: 1 · без изменений: 1',
    })
  })

  it('keeps the file line numbers when the server rejects the commit (422)', async () => {
    // CSV with a header row: the students are on lines 2 and 3 of the file.
    const ok = preview([row(2, 'Иванов Иван'), row(3, 'Петров Пётр')], {
      detected: { format: 'csv', encoding: 'utf-8', delimiter: ';', name_column: 'ФИО' },
    })
    let commitBody: { rows?: { line?: number }[] } = {}
    mockFetch({
      'POST /api/groups/5/import/preview': ok,
      'POST /api/groups/5/import/commit': (_url: URL, init: RequestInit) => {
        commitBody = JSON.parse(String(init.body))
        // The re-check knows nothing about the file: format "text", lines as sent.
        const recheck = preview([row(2, 'Иванов Иван', 'error', 'Имя уже занято.'), row(3, 'Петров Пётр')])
        return errorResponse(422, 'validation', 'В списке есть ошибки.', { preview: recheck })
      },
    })
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} />)
    await user.upload(screen.getByLabelText('Файл со списком студентов'), new File(['ФИО\nИванов Иван\nПетров Пётр'], 'list.csv'))
    await user.click(await screen.findByRole('button', { name: 'Сохранить список' }))

    // Line 2 of the file — not "line 3", the second row of the request.
    expect(await screen.findByText(/Строки с проблемами:\s*2\./)).toBeInTheDocument()
    expect(commitBody.rows?.map((r) => r.line)).toEqual([2, 3])
    // Still described as the uploaded file.
    expect(screen.getByText(/таблица CSV · кодировка UTF-8 · разделитель: точка с запятой/)).toBeInTheDocument()
    expect(screen.getByText('Исправьте файл и загрузите снова')).toBeInTheDocument()
  })

  it('checks the list again by itself when a name got taken meanwhile (409)', async () => {
    const before = preview([row(1, 'Иванов Иван'), row(2, 'Петров Пётр')])
    // Meanwhile a colleague added Петров Пётр to the group.
    const after = preview([row(1, 'Иванов Иван'), row(2, 'Петров Пётр', 'existing', 'Уже есть в группе.')])
    let previews = 0
    let commits = 0
    mockFetch({
      'POST /api/groups/5/import/preview': () => jsonResponse(previews++ === 0 ? before : after),
      'POST /api/groups/5/import/commit': () =>
        commits++ === 0
          ? errorResponse(409, 'conflict', 'Пока вы проверяли список, в группе что-то изменилось. Проверьте список ещё раз.')
          : jsonResponse({ created: 1, existing: 1, archived: 0, updated: 0 }),
    })
    const onDone = vi.fn()
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} onDone={onDone} />)
    await pasteList(user, 'Иванов Иван\nПетров Пётр')
    await user.click(await screen.findByRole('button', { name: 'Сохранить список' }))

    expect(await screen.findByText(/Мы проверили список заново/)).toBeInTheDocument()
    expect(previews).toBe(2)
    expect(screen.getByText('Уже в группе: 1')).toBeInTheDocument()
    expect(onDone).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Сохранить список' }))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith({ created: 1, existing: 1, archived: 0, updated: 0 }))
  })

  it('shows renamed students apart from «Уже в группе», with their own badge', async () => {
    const renamed = {
      ...row(1, 'Петрова Мария', 'existing', 'Уже есть в группе. Имя будет изменено: «Иванова Мария» → «Петрова Мария».'),
      student_id: 10,
      rename: true,
    }
    const p = preview([renamed, { ...row(2, 'Сидоров Алексей', 'existing', 'Уже есть в группе.'), student_id: 11 }])
    p.counts.rename = 1
    mockFetch({ 'POST /api/groups/5/import/preview': p })
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents />)
    await pasteList(user, 'Петрова Мария\nСидоров Алексей')

    const chips = await screen.findByRole('list', { name: 'Итоги проверки' })
    expect(within(chips).getByText('Уже в группе: 1')).toBeInTheDocument()
    expect(within(chips).getByText('Будут переименованы: 1')).toBeInTheDocument()
    const renamedRow = screen.getByText('Петрова Мария').closest('tr')!
    expect(within(renamedRow).getByText('Переименование')).toBeInTheDocument()
    expect(within(renamedRow).getByText(/«Иванова Мария» → «Петрова Мария»/)).toBeInTheDocument()
  })

  it('shows a readable error when the file cannot be read', async () => {
    mockFetch({
      'POST /api/groups/5/import/preview': () =>
        errorResponse(422, 'validation', 'Это файл Word. Сохраните список как Excel или CSV.'),
    })
    const user = userEvent.setup()
    renderWithClient(<ImportWizard groupId={5} hasStudents={false} />)
    await user.upload(screen.getByLabelText('Файл со списком студентов'), new File(['x'], 'list.txt', { type: 'text/plain' }))
    expect(await screen.findByText('Это файл Word. Сохраните список как Excel или CSV.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Проверить список' })).toBeInTheDocument()
  })
})

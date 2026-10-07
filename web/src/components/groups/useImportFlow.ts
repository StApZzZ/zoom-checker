// State machine of the student import: choose a source → preview (optionally
// re-run without patronymics) → commit. Shared by ImportWizard and the
// "new group" dialog.

import { useCallback, useRef, useState } from 'react'
import { toast } from 'sonner'

import {
  canCommit,
  commitRows,
  importResultSummary,
  originalNames,
  previewFromError,
  previewNameFormat,
  withSourceLines,
  type OriginalNames,
} from '@/components/groups/import-utils'
import { ApiError, errorMessage } from '@/lib/api'
import { useImportCommit, useImportPreview } from '@/lib/queries'
import type { ImportMode, ImportNameFormat, ImportPreview, ImportResult, ImportSource } from '@/lib/types'

export type ImportSourceKind = 'file' | 'text'

export interface ImportSourceInfo {
  source: ImportSource
  kind: ImportSourceKind
  /** File name or "Вставленный список". */
  label: string
}

export interface ImportFlow {
  mode: ImportMode
  setMode: (mode: ImportMode) => void
  /** Name format the user asked for (the preview's own format is `preview.name_format`). */
  nameFormat: ImportNameFormat
  sourceInfo: ImportSourceInfo | null
  preview: ImportPreview | null
  previewing: boolean
  previewError: string | null
  committing: boolean
  commitError: string | null
  /** Parses a file or pasted text and shows the preview. Resolves with the preview or null on error. */
  runPreview: (info: ImportSourceInfo, mode?: ImportMode) => Promise<ImportPreview | null>
  /** Re-runs the preview of the current source with another name format. */
  changeNameFormat: (format: ImportNameFormat) => Promise<ImportPreview | null>
  /** Saves the latest preview. Resolves with the result, or null when it failed (see commitError). */
  commit: () => Promise<ImportResult | null>
  /** Back to choosing a source (keeps mode and name format). */
  backToSource: () => void
  reset: () => void
}

export interface ImportFlowOptions {
  initialMode?: ImportMode
  /** Show a success toast with the result summary (default true). */
  toastOnSuccess?: boolean
  /** Title of the success toast. */
  successTitle?: string
}

interface Snapshot {
  sourceInfo: ImportSourceInfo | null
  preview: ImportPreview | null
  originals: OriginalNames
}

const EMPTY: Snapshot = { sourceInfo: null, preview: null, originals: new Map() }

export function useImportFlow(groupId: number, options: ImportFlowOptions = {}): ImportFlow {
  const { initialMode = 'add', toastOnSuccess = true, successTitle = 'Список студентов сохранён' } = options
  const { mutateAsync: previewAsync } = useImportPreview(groupId, { errorToast: false })
  // The summary toast below replaces the default one.
  const commitMutation = useImportCommit(groupId, { errorToast: false, successMessage: '' })
  const { mutateAsync: commitAsync } = commitMutation

  const [mode, setMode] = useState<ImportMode>(initialMode)
  const [nameFormat, setNameFormat] = useState<ImportNameFormat>('as_is')
  const [snapshot, setSnapshotState] = useState<Snapshot>(EMPTY)
  const [previewing, setPreviewing] = useState(false)
  const [previewError, setPreviewError] = useState<string | null>(null)
  const [commitError, setCommitError] = useState<string | null>(null)
  // Latest values for async callbacks (auto-commit right after a preview).
  const latest = useRef<Snapshot>(EMPTY)
  // Only the latest preview request may update the screen (fast toggling, re-uploads).
  const requestSeq = useRef(0)

  const setSnapshot = useCallback((next: Snapshot) => {
    latest.current = next
    setSnapshotState(next)
  }, [])

  const load = useCallback(
    async (info: ImportSourceInfo, wantedMode: ImportMode, format: ImportNameFormat, sameSource: boolean) => {
      const seq = ++requestSeq.current
      setPreviewing(true)
      setPreviewError(null)
      setCommitError(null)
      try {
        let originals = sameSource ? latest.current.originals : new Map<number, string>()
        let result: ImportPreview | null = null
        if (!sameSource || format === 'as_is') {
          // The names as written in the file come from an as_is preview (see commitRows).
          result = await previewAsync({ source: info.source, mode: wantedMode, nameFormat: 'as_is' })
          originals = originalNames(result)
        }
        if (format === 'last_first' && (!result || result.has_patronymics)) {
          result = await previewAsync({ source: info.source, mode: wantedMode, nameFormat: 'last_first' })
        }
        if (seq !== requestSeq.current || !result) return null
        setSnapshot({ sourceInfo: info, preview: result, originals })
        return result
      } catch (err) {
        if (seq === requestSeq.current) setPreviewError(errorMessage(err))
        return null
      } finally {
        if (seq === requestSeq.current) setPreviewing(false)
      }
    },
    [previewAsync, setSnapshot],
  )

  const runPreview = useCallback(
    (info: ImportSourceInfo, wantedMode: ImportMode = mode) => {
      setMode(wantedMode)
      return load(info, wantedMode, nameFormat, false)
    },
    [load, mode, nameFormat],
  )

  const changeNameFormat = useCallback(
    async (format: ImportNameFormat) => {
      setNameFormat(format)
      const { sourceInfo, preview } = latest.current
      if (!sourceInfo) return null
      return load(sourceInfo, preview?.mode ?? mode, format, true)
    },
    [load, mode],
  )

  const commit = useCallback(async () => {
    const { preview, originals } = latest.current
    if (!preview || !canCommit(preview)) return null
    setCommitError(null)
    try {
      const result = await commitAsync({
        mode: preview.mode ?? mode,
        rows: commitRows(preview, originals),
        name_format: previewNameFormat(preview),
      })
      if (toastOnSuccess) toast.success(successTitle, { description: importResultSummary(result) })
      return result ?? { created: 0, existing: 0, archived: 0, updated: 0 }
    } catch (err) {
      const recheck = previewFromError(err)
      const { sourceInfo } = latest.current
      if (recheck) {
        // The roster changed since the preview: show the server's re-check.
        const fresh = withSourceLines(recheck, preview)
        const originalsNow = previewNameFormat(fresh) === 'as_is' ? originalNames(fresh) : originals
        setSnapshot({ ...latest.current, preview: fresh, originals: originalsNow })
        setCommitError(`${errorMessage(err)} Список проверен заново — посмотрите строки с пояснениями ниже.`)
      } else if (err instanceof ApiError && err.status === 409 && sourceInfo) {
        // A name got taken while the teacher was looking at the preview
        // (another tab, a colleague): check the same list again right away.
        const again = await load(sourceInfo, preview.mode ?? mode, previewNameFormat(preview), true)
        setCommitError(
          again
            ? 'Пока вы смотрели список, в группе что-то изменилось. Мы проверили список заново — посмотрите результат и сохраните ещё раз.'
            : errorMessage(err),
        )
      } else {
        setCommitError(errorMessage(err))
      }
      return null
    }
  }, [commitAsync, load, mode, toastOnSuccess, successTitle, setSnapshot])

  const backToSource = useCallback(() => {
    requestSeq.current++
    setSnapshot({ ...latest.current, preview: null })
    setPreviewing(false)
    setPreviewError(null)
    setCommitError(null)
  }, [setSnapshot])

  const reset = useCallback(() => {
    requestSeq.current++
    setSnapshot(EMPTY)
    setPreviewing(false)
    setPreviewError(null)
    setCommitError(null)
    setNameFormat('as_is')
    setMode(initialMode)
  }, [initialMode, setSnapshot])

  return {
    mode,
    setMode,
    nameFormat,
    sourceInfo: snapshot.sourceInfo,
    preview: snapshot.preview,
    previewing,
    previewError,
    committing: commitMutation.isPending,
    commitError,
    runPreview,
    changeNameFormat,
    commit,
    backToSource,
    reset,
  }
}

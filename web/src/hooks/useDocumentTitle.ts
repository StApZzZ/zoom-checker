import { useEffect } from 'react'

export const APP_NAME = 'Zoom Checker'

/** Sets document.title to "<title> · Zoom Checker" (or just the app name). */
export function useDocumentTitle(title: string | null | undefined): void {
  useEffect(() => {
    const previous = document.title
    document.title = title ? `${title} · ${APP_NAME}` : `${APP_NAME} — посещаемость`
    return () => {
      document.title = previous
    }
  }, [title])
}

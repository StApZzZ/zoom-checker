import { RefreshCw, TriangleAlert } from 'lucide-react'
import { isRouteErrorResponse, Link, useRouteError } from 'react-router'

import { EmptyState } from '@/components/EmptyState'
import { Page } from '@/components/layout/Page'
import { Button } from '@/components/ui/button'
import { useDocumentTitle } from '@/hooks/useDocumentTitle'
import { isChunkLoadError } from '@/pages/errors/chunkError'

/**
 * Router error boundary: render crash, failed lazy chunk (after a deploy), 404 responses.
 * `fullScreen` for the root boundary (no app shell around it); inside the shell
 * the page only fills the area under the top bar.
 */
export default function RouteErrorPage({ fullScreen = false }: { fullScreen?: boolean }) {
  const error = useRouteError()
  useDocumentTitle('Ошибка')
  if (import.meta.env.DEV) console.error(error)

  const chunk = isChunkLoadError(error)
  const notFound = isRouteErrorResponse(error) && error.status === 404
  const title = chunk
    ? 'Вышла новая версия приложения'
    : notFound
      ? 'Такой страницы нет'
      : 'Что-то пошло не так'
  const text = chunk
    ? 'Обновите страницу, чтобы продолжить работу.'
    : notFound
      ? 'Возможно, ссылка устарела или в ней опечатка.'
      : 'Попробуйте обновить страницу. Если ошибка повторяется, сообщите администратору.'

  return (
    <Page width="narrow" className={fullScreen ? 'min-h-svh justify-center' : 'flex-1 justify-center'}>
      <EmptyState
        icon={TriangleAlert}
        title={title}
        text={text}
        action={
          <>
            <Button type="button" onClick={() => window.location.reload()}>
              <RefreshCw aria-hidden="true" />
              Обновить страницу
            </Button>
            <Button asChild variant="outline">
              <Link to="/" reloadDocument>
                На главную
              </Link>
            </Button>
          </>
        }
      />
    </Page>
  )
}

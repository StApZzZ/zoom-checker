import { SearchX } from 'lucide-react'
import { Link } from 'react-router'

import { EmptyState } from '@/components/EmptyState'
import { Page } from '@/components/layout/Page'
import { Button } from '@/components/ui/button'
import { useDocumentTitle } from '@/hooks/useDocumentTitle'

/** 404 for unknown client routes. */
export default function NotFoundPage() {
  useDocumentTitle('Страница не найдена')
  return (
    <Page width="narrow" className="flex-1 justify-center">
      <EmptyState
        icon={SearchX}
        title="Такой страницы нет"
        text="Возможно, ссылка устарела или в ней опечатка. Вернитесь к списку занятий."
        action={
          <Button asChild>
            <Link to="/">К занятиям</Link>
          </Button>
        }
      />
    </Page>
  )
}

import type { ReactNode } from 'react'
import { ArrowLeft } from 'lucide-react'
import { Link } from 'react-router'

import { useDocumentTitle } from '@/hooks/useDocumentTitle'
import { cn } from '@/lib/utils'

export interface PageHeaderProps {
  title: ReactNode
  /** One-line explanation under the title. */
  description?: ReactNode
  /** Buttons on the right (wrap below the title on narrow screens). */
  actions?: ReactNode
  /** "← Назад" link above the title. */
  back?: { to: string; label: string }
  /** Small line above the title (e.g. group name, date). */
  eyebrow?: ReactNode
  /** Extra content under the description (badges, meta). */
  children?: ReactNode
  /** Browser tab title; defaults to `title` when it is a string. */
  documentTitle?: string
  className?: string
}

/** Title + description + actions block at the top of every page. Also sets the tab title. */
export function PageHeader({
  title,
  description,
  actions,
  back,
  eyebrow,
  children,
  documentTitle,
  className,
}: PageHeaderProps) {
  useDocumentTitle(documentTitle ?? (typeof title === 'string' ? title : undefined))
  return (
    <header className={cn('flex flex-col gap-3', className)}>
      {back && (
        <Link
          to={back.to}
          className="inline-flex w-fit items-center gap-1 rounded-md text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-3 focus-visible:ring-ring/50"
        >
          <ArrowLeft className="size-4" aria-hidden="true" />
          {back.label}
        </Link>
      )}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 flex-col gap-1">
          {eyebrow && <div className="text-sm text-muted-foreground">{eyebrow}</div>}
          <h1 className="font-heading text-2xl font-semibold tracking-tight text-balance break-words sm:text-3xl">
            {title}
          </h1>
          {description && <div className="max-w-3xl text-sm text-pretty text-muted-foreground sm:text-base">{description}</div>}
          {children && <div className="mt-1 flex flex-wrap items-center gap-2">{children}</div>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </header>
  )
}

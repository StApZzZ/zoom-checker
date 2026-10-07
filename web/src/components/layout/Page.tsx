import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export interface PageProps {
  children: ReactNode
  /** narrow = forms/settings, default = most pages, wide = big tables, full = edge to edge. */
  width?: 'narrow' | 'default' | 'wide' | 'full'
  className?: string
}

const widthClass: Record<NonNullable<PageProps['width']>, string> = {
  narrow: 'max-w-3xl',
  default: 'max-w-6xl',
  wide: 'max-w-[96rem]',
  full: 'max-w-none',
}

/** Page container: centered column with responsive side padding. */
export function Page({ children, width = 'default', className }: PageProps) {
  return (
    <div className={cn('mx-auto flex w-full flex-col gap-6 px-4 py-6 sm:px-6 lg:py-8', widthClass[width], className)}>
      {children}
    </div>
  )
}

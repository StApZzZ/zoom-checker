import { CalendarDays, LifeBuoy, ScrollText, Settings, Users, UsersRound, Video, type LucideIcon } from 'lucide-react'

export interface NavItem {
  to: string
  label: string
  icon: LucideIcon
  /** Extra path prefixes that mark the item active (e.g. /lessons/5 → "Занятия"). */
  alsoActive?: string[]
}

export const primaryNav: NavItem[] = [
  { to: '/', label: 'Занятия', icon: CalendarDays, alsoActive: ['/lessons'] },
  { to: '/groups', label: 'Группы', icon: Users },
  { to: '/zoom', label: 'Zoom', icon: Video },
]

export const adminNav: NavItem[] = [
  { to: '/admin/users', label: 'Пользователи', icon: UsersRound },
  { to: '/admin/settings', label: 'Настройки', icon: Settings },
  { to: '/admin/events', label: 'Журнал событий', icon: ScrollText },
]

export const helpNav: NavItem = { to: '/help', label: 'Справка', icon: LifeBuoy }

/** Whether `item` should be highlighted for `pathname`. */
export function isNavActive(item: NavItem, pathname: string): boolean {
  const prefixes = [item.to, ...(item.alsoActive ?? [])]
  return prefixes.some((p) =>
    p === '/' ? pathname === '/' : pathname === p || pathname.startsWith(p.endsWith('/') ? p : `${p}/`),
  )
}

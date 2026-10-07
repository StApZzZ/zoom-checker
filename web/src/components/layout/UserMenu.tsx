import { ChevronDown, KeyRound, LogOut } from 'lucide-react'

import { roleLabels } from '@/components/status-meta'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useAuth } from '@/lib/auth'
import { initials } from '@/lib/format'
import { userDisplayName } from '@/lib/types'

/** Avatar button with name, role, "Сменить пароль" and "Выйти". */
export function UserMenu() {
  const { me, canChangePassword, openChangePassword, logout } = useAuth()
  if (!me) return null
  const name = userDisplayName(me)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" className="h-9 gap-2 px-1.5 sm:px-2" aria-label={`Меню пользователя: ${name}`}>
          <Avatar className="size-7">
            <AvatarFallback className="text-xs">{initials(name)}</AvatarFallback>
          </Avatar>
          <span className="hidden max-w-40 truncate text-sm lg:inline">{name}</span>
          <ChevronDown className="hidden text-muted-foreground lg:block" aria-hidden="true" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-56">
        <DropdownMenuLabel className="flex flex-col gap-0.5 font-normal">
          <span className="truncate font-medium text-foreground">{name}</span>
          <span className="truncate text-xs text-muted-foreground">
            {me.username} · {roleLabels[me.role]}
          </span>
        </DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuGroup>
          {canChangePassword && (
            <DropdownMenuItem onSelect={openChangePassword}>
              <KeyRound aria-hidden="true" />
              Сменить пароль
            </DropdownMenuItem>
          )}
          <DropdownMenuItem onSelect={() => void logout()}>
            <LogOut aria-hidden="true" />
            Выйти
          </DropdownMenuItem>
        </DropdownMenuGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

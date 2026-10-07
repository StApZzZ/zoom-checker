import { Suspense, useState } from 'react'
import { ChevronDown, Menu, ShieldCheck } from 'lucide-react'
import { Link, Outlet, useLocation, useNavigation } from 'react-router'

import { Brand } from '@/components/layout/Logo'
import { adminNav, helpNav, isNavActive, primaryNav, type NavItem } from '@/components/layout/nav'
import { ThemeToggle } from '@/components/layout/ThemeToggle'
import { UserMenu } from '@/components/layout/UserMenu'
import { ZoomSetupBanner } from '@/components/layout/ZoomSetupBanner'
import { LoadingState } from '@/components/LoadingState'
import { TimeZoneNote } from '@/components/TimeZoneNote'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle, SheetTrigger } from '@/components/ui/sheet'
import { useAuth } from '@/lib/auth'
import { cn } from '@/lib/utils'

const desktopLinkClass = (active: boolean) =>
  cn(
    'inline-flex h-9 items-center gap-1.5 rounded-lg px-3 text-sm font-medium transition-colors outline-none',
    'focus-visible:ring-3 focus-visible:ring-ring/50',
    active ? 'bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
  )

function DesktopLink({ item, pathname }: { item: NavItem; pathname: string }) {
  const active = isNavActive(item, pathname)
  return (
    <Link to={item.to} className={desktopLinkClass(active)} aria-current={active ? 'page' : undefined}>
      {item.label}
    </Link>
  )
}

function AdminMenu({ pathname }: { pathname: string }) {
  const active = adminNav.some((item) => isNavActive(item, pathname))
  return (
    <DropdownMenu>
      <DropdownMenuTrigger className={desktopLinkClass(active)}>
        Админ
        <ChevronDown className="size-4" aria-hidden="true" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-52">
        {adminNav.map((item) => {
          const Icon = item.icon
          const itemActive = isNavActive(item, pathname)
          return (
            <DropdownMenuItem key={item.to} asChild className={cn(itemActive && 'bg-muted font-medium')}>
              <Link to={item.to} aria-current={itemActive ? 'page' : undefined}>
                <Icon aria-hidden="true" />
                {item.label}
              </Link>
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function MobileLink({ item, pathname, onNavigate }: { item: NavItem; pathname: string; onNavigate: () => void }) {
  const active = isNavActive(item, pathname)
  const Icon = item.icon
  return (
    <Link
      to={item.to}
      onClick={onNavigate}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'flex h-11 items-center gap-3 rounded-lg px-3 text-base font-medium transition-colors outline-none',
        'focus-visible:ring-3 focus-visible:ring-ring/50',
        active ? 'bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground',
      )}
    >
      <Icon className="size-5" aria-hidden="true" />
      {item.label}
    </Link>
  )
}

function MobileNav({ pathname, isAdmin }: { pathname: string; isAdmin: boolean }) {
  const [open, setOpen] = useState(false)
  const close = () => setOpen(false)
  return (
    <Sheet open={open} onOpenChange={setOpen}>
      <SheetTrigger asChild>
        <Button variant="ghost" size="icon" className="md:hidden" aria-label="Открыть меню">
          <Menu aria-hidden="true" />
        </Button>
      </SheetTrigger>
      <SheetContent side="left" className="w-72 gap-0 p-0">
        <SheetHeader className="border-b p-4">
          <SheetTitle asChild>
            <div>
              <Brand />
            </div>
          </SheetTitle>
          <SheetDescription className="sr-only">Разделы приложения</SheetDescription>
        </SheetHeader>
        <nav aria-label="Основное меню" className="flex flex-col gap-1 overflow-y-auto p-3">
          {primaryNav.map((item) => (
            <MobileLink key={item.to} item={item} pathname={pathname} onNavigate={close} />
          ))}
          {isAdmin && (
            <>
              <p className="mt-4 mb-1 flex items-center gap-1.5 px-3 text-xs font-medium tracking-wide text-muted-foreground uppercase">
                <ShieldCheck className="size-3.5" aria-hidden="true" />
                Администрирование
              </p>
              {adminNav.map((item) => (
                <MobileLink key={item.to} item={item} pathname={pathname} onNavigate={close} />
              ))}
            </>
          )}
          <div className="mt-4 border-t pt-3">
            <MobileLink item={helpNav} pathname={pathname} onNavigate={close} />
          </div>
        </nav>
      </SheetContent>
    </Sheet>
  )
}

/** Thin bar at the top while a lazy page chunk is loading. */
function NavigationProgress() {
  const navigation = useNavigation()
  if (navigation.state === 'idle') return null
  return (
    <div role="progressbar" aria-label="Загрузка страницы" className="absolute inset-x-0 bottom-0 h-0.5 overflow-hidden">
      <div className="h-full w-1/3 animate-pulse bg-sky-500 motion-safe:animate-[nav-progress_1.2s_ease-in-out_infinite]" />
    </div>
  )
}

/**
 * Authenticated layout: sticky top bar (brand, navigation, theme, user menu)
 * and the routed page below. Pages render their own <Page> container.
 */
export function AppShell() {
  const { isAdmin } = useAuth()
  const { pathname } = useLocation()
  return (
    <div className="flex min-h-svh flex-col bg-background">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-background px-3 py-2 text-sm font-medium ring-2 ring-ring focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Перейти к содержимому
      </a>
      <header className="sticky top-0 z-40 border-b bg-background/85 backdrop-blur supports-backdrop-filter:bg-background/70">
        <div className="mx-auto flex h-14 w-full max-w-[96rem] items-center gap-2 px-3 sm:px-6">
          <MobileNav pathname={pathname} isAdmin={isAdmin} />
          <Link
            to="/"
            className="mr-2 rounded-lg outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            aria-label="Zoom Checker — на главную"
          >
            <Brand />
          </Link>
          <nav aria-label="Основное меню" className="hidden items-center gap-1 md:flex">
            {primaryNav.map((item) => (
              <DesktopLink key={item.to} item={item} pathname={pathname} />
            ))}
            {isAdmin && <AdminMenu pathname={pathname} />}
            <DesktopLink item={helpNav} pathname={pathname} />
          </nav>
          <div className="ml-auto flex items-center gap-1">
            <ThemeToggle />
            <UserMenu />
          </div>
        </div>
        <NavigationProgress />
      </header>
      <main id="main" tabIndex={-1} className="flex flex-1 flex-col outline-none">
        {/* The Zoom page shows the same state in full itself. */}
        <ZoomSetupBanner enabled={isAdmin && pathname !== '/zoom' && !pathname.startsWith('/zoom/')} />
        <Suspense fallback={<LoadingState variant="page" className="mx-auto max-w-6xl px-4 py-6 sm:px-6" />}>
          <Outlet />
        </Suspense>
      </main>
      <footer className="mx-auto w-full max-w-6xl px-4 sm:px-6 empty:hidden">
        <TimeZoneNote variant="page" className="border-t py-3" />
      </footer>
    </div>
  )
}

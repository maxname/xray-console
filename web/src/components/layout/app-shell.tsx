import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, Outlet, useRouter, useRouterState } from '@tanstack/react-router'
import {
  ChartNoAxesColumn,
  Copy,
  GitBranch,
  Laptop,
  LayoutDashboard,
  Link2,
  LogOut,
  Menu,
  Server,
} from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { BrandMark } from '@/components/brand-mark'
import { LanguageMenu, ThemeMenu } from '@/components/preference-menus'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { Toaster } from '@/components/ui/toaster'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { useMe } from '@/lib/use-me'
import { cn } from '@/lib/utils'

const NAV_ITEMS = [
  { to: '/', labelKey: 'nav.overview', icon: LayoutDashboard, exact: true },
  { to: '/servers', labelKey: 'nav.servers', icon: Server, exact: false },
  { to: '/templates', labelKey: 'nav.templates', icon: Copy, exact: false },
  { to: '/chains', labelKey: 'nav.chains', icon: Link2, exact: false },
  { to: '/routing', labelKey: 'nav.routing', icon: GitBranch, exact: false },
  { to: '/clients', labelKey: 'nav.clients', icon: Laptop, exact: false },
  { to: '/stats', labelKey: 'nav.stats', icon: ChartNoAxesColumn, exact: false },
] as const

export function AppShell() {
  const { t } = useTranslation()
  const [menuOpen, setMenuOpen] = useState(false)

  return (
    <div className="flex min-h-svh flex-col bg-background md:flex-row">
      <a
        href="#main-content"
        className="sr-only focus-visible:absolute focus-visible:top-2 focus-visible:left-2 focus-visible:z-50 focus-visible:rounded-md focus-visible:bg-background focus-visible:px-3 focus-visible:py-2 focus-visible:text-sm focus-visible:font-medium focus-visible:ring-2 focus-visible:ring-ring focus-visible:not-sr-only"
      >
        {t('nav.skipToContent')}
      </a>

      {/* Desktop: the sidebar stays put while the page scrolls. */}
      <aside className="sticky top-0 hidden h-svh w-58 shrink-0 border-r border-sidebar-border bg-sidebar md:block">
        <SidebarBody />
      </aside>

      {/* Phone: the same sidebar, behind a button. */}
      <div className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-sidebar-border bg-sidebar px-3 md:hidden">
        <Sheet open={menuOpen} onOpenChange={setMenuOpen}>
          <SheetTrigger asChild>
            <Button variant="ghost" size="icon" aria-label={t('nav.openMenu')}>
              <Menu aria-hidden="true" />
            </Button>
          </SheetTrigger>
          <SheetContent
            side="left"
            className="w-64 bg-sidebar p-0"
            aria-describedby={undefined}
            // Radix skips links when it picks what to focus on open, so it
            // lands on the theme button and pops its tooltip over the menu.
            // Focus the panel itself; Tab then starts from the top.
            onOpenAutoFocus={(event) => {
              event.preventDefault()
              ;(event.currentTarget as HTMLElement).focus()
            }}
          >
            <SheetTitle className="sr-only">{t('nav.label')}</SheetTitle>
            <SheetDescription className="sr-only">{t('app.name')}</SheetDescription>
            <SidebarBody onNavigate={() => setMenuOpen(false)} />
          </SheetContent>
        </Sheet>
        <BrandMark />
        <span className="font-heading text-[15px] font-semibold" translate="no">
          {t('app.name')}
        </span>
      </div>

      <main
        id="main-content"
        className="min-w-0 flex-1 px-4 py-6 md:px-8 md:pt-7 md:pb-10"
      >
        <div className="mx-auto w-full max-w-6xl">
          <Outlet />
        </div>
      </main>

      <Toaster />
    </div>
  )
}

function SidebarBody({ onNavigate }: { onNavigate?: () => void }) {
  const { t } = useTranslation()
  const pathname = useRouterState({ select: (s) => s.location.pathname })

  const isActive = (to: string, exact: boolean) =>
    exact ? pathname === to : pathname === to || pathname.startsWith(`${to}/`)

  return (
    <div className="flex h-full flex-col gap-5 px-3 py-5 text-sm text-sidebar-foreground">
      <Link
        to="/"
        onClick={onNavigate}
        className="flex items-center gap-2.5 rounded-lg px-2 outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring"
      >
        <BrandMark />
        <span className="flex min-w-0 flex-col">
          <span className="font-heading text-[15px] leading-5 font-semibold" translate="no">
            {t('app.name')}
          </span>
          <span className="font-mono text-[11px] leading-4 text-muted-foreground" translate="no">
            {t('app.tagline')}
          </span>
        </span>
      </Link>

      <nav aria-label={t('nav.label')} className="flex flex-col gap-0.5">
        {NAV_ITEMS.map((item) => {
          const active = isActive(item.to, item.exact)
          return (
            <Link
              key={item.to}
              to={item.to}
              onClick={onNavigate}
              aria-current={active ? 'page' : undefined}
              className={cn(
                'flex min-h-8 items-center gap-2.5 rounded-lg px-2.5 outline-none transition-colors focus-visible:ring-2 focus-visible:ring-sidebar-ring',
                active
                  ? 'bg-sidebar-primary font-semibold text-sidebar-primary-foreground'
                  : 'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
              )}
            >
              <item.icon className="size-4.5 shrink-0" strokeWidth={1.8} aria-hidden="true" />
              <span className="truncate">{t(item.labelKey)}</span>
            </Link>
          )
        })}
      </nav>

      <SidebarFooter />
    </div>
  )
}

/** Who is signed in, and the three things that belong to them: language,
 *  theme, signing out. */
function SidebarFooter() {
  const { t } = useTranslation()
  const { data: me } = useMe()
  const logout = useLogout()

  return (
    <div className="mt-auto flex items-center gap-1 border-t border-sidebar-border pt-3 pl-2">
      <Tooltip>
        <TooltipTrigger asChild>
          <Link
            to="/security"
            className="min-w-0 flex-1 truncate rounded-md font-medium outline-none hover:underline focus-visible:ring-3 focus-visible:ring-ring/50"
            aria-label={t('security.open', { name: me?.username ?? '' })}
          >
            <span translate="no">{me?.username ?? ''}</span>
          </Link>
        </TooltipTrigger>
        <TooltipContent>{t('security.title')}</TooltipContent>
      </Tooltip>
      <LanguageMenu />
      <ThemeMenu />
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
            aria-label={t('header.logout')}
          >
            <LogOut aria-hidden="true" />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{t('header.logout')}</TooltipContent>
      </Tooltip>
    </div>
  )
}

function useLogout() {
  const router = useRouter()
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: () => api.logout(),
    // Leave even if the request failed: staying on a page that looks signed
    // in after pressing "sign out" is the worse outcome.
    onSettled: () => {
      queryClient.clear()
      void router.navigate({ to: '/login' })
    },
  })
}

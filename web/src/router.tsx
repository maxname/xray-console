import {
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  redirect,
} from '@tanstack/react-router'

import { AppShell } from '@/components/layout/app-shell'
import { RouterError, RouterPending } from '@/components/router-fallbacks'
import { api } from '@/lib/api'
// Login and setup stay eager: they are the first paint for anyone without a
// session, and they are small. Every page behind the auth guard is split out.
import { LoginPage } from '@/pages/login'
import { SetupPage } from '@/pages/setup'

const rootRoute = createRootRoute({
  component: Outlet,
})

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  beforeLoad: async () => {
    const state = await api.getAuthState()
    if (!state.setup_required) {
      throw redirect({ to: state.authenticated ? '/' : '/login' })
    }
  },
  component: SetupPage,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  beforeLoad: async () => {
    const state = await api.getAuthState()
    if (state.setup_required) {
      throw redirect({ to: '/setup' })
    }
    if (state.authenticated) {
      throw redirect({ to: '/' })
    }
  },
  component: LoginPage,
})

// Pathless layout route guarding everything that requires a session.
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: async () => {
    const state = await api.getAuthState()
    if (state.setup_required) {
      throw redirect({ to: '/setup' })
    }
    if (!state.authenticated) {
      throw redirect({ to: '/login' })
    }
  },
  component: AppShell,
})

const overviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  component: lazyRouteComponent(() => import('@/pages/overview'), 'OverviewPage'),
})

const serversRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/servers',
  component: lazyRouteComponent(() => import('@/pages/servers'), 'ServersPage'),
})

const serverNewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/servers/new',
  component: lazyRouteComponent(() => import('@/pages/server-new'), 'ServerNewPage'),
})

const serverRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/servers/$serverId',
  component: lazyRouteComponent(() => import('@/pages/server-page'), 'ServerPage'),
})

const templatesRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/templates',
  // ?id= picks the template shown, so a link can point at one.
  validateSearch: (search: Record<string, unknown>): { id?: number } => {
    const id = Number(search.id)
    return Number.isInteger(id) && id > 0 ? { id } : {}
  },
  component: lazyRouteComponent(() => import('@/pages/templates'), 'TemplatesPage'),
})

const chainsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/chains',
  component: lazyRouteComponent(() => import('@/pages/chains'), 'ChainsPage'),
})

const routingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/routing',
  component: lazyRouteComponent(() => import('@/pages/routing'), 'RoutingPage'),
})

const clientsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/clients',
  component: lazyRouteComponent(() => import('@/pages/clients'), 'ClientsPage'),
})

const statsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/stats',
  component: lazyRouteComponent(() => import('@/pages/stats'), 'StatsPage'),
})

const routeTree = rootRoute.addChildren([
  setupRoute,
  loginRoute,
  appRoute.addChildren([
    overviewRoute,
    serversRoute,
    serverNewRoute,
    serverRoute,
    templatesRoute,
    chainsRoute,
    routingRoute,
    clientsRoute,
    statsRoute,
  ]),
])

export const router = createRouter({
  routeTree,
  defaultPendingComponent: RouterPending,
  defaultErrorComponent: RouterError,
})

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}

import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { api, ApiError } from '@/lib/api'
import { FreshView, InstallView } from '@/pages/server'
import { UninstallView } from '@/pages/server-lifecycle'
import { ReadyView } from '@/pages/server-ready'

export function ServerPage() {
  const { t } = useTranslation()
  const params = useParams({ strict: false })
  const id = Number(params.serverId)

  const serverQuery = useQuery({
    queryKey: ['servers', id],
    queryFn: () => api.getServer(id),
    retry: false,
    // While a job runs the server's own record changes under us.
    refetchInterval: (q) =>
      // A server that has gone — removed with its Xray — is not asked again.
      q.state.status !== 'error' &&
      (q.state.data?.status === 'installing' || q.state.data?.status === 'uninstalling')
        ? 1500
        : false,
  })
  const server = serverQuery.data
  const operationId = server?.operation_id ?? null

  const feedQuery = useQuery({
    queryKey: ['operations', operationId],
    queryFn: () => api.getOperation(operationId as number),
    enabled: operationId !== null,
    refetchInterval: (q) =>
      q.state.data?.operation.status === 'running' ? 1000 : false,
  })

  if (serverQuery.isError) {
    const missing =
      serverQuery.error instanceof ApiError && serverQuery.error.status === 404
    // The last thing known about a server that has just vanished is that
    // Xray was being removed from it: that is the removal finishing, not a
    // wrong address.
    if (missing && server?.status === 'uninstalling') {
      return (
        <Page>
          <PageHeader title={t('uninstall.done.title', { name: server.name })} />
          <EmptyState
            title={t('uninstall.done.heading')}
            description={t('uninstall.done.text', { name: server.name })}
          >
            <Button asChild variant="outline">
              <Link to="/servers">{t('server.toList')}</Link>
            </Button>
          </EmptyState>
        </Page>
      )
    }
    return (
      <Page>
        <PageHeader title={t('servers.title')} />
        <EmptyState
          title={missing ? t('server.notFoundTitle') : t('servers.loadFailedTitle')}
          description={missing ? t('server.notFound') : t('servers.loadFailed')}
        >
          <Button asChild variant="outline">
            <Link to="/servers">{t('server.toList')}</Link>
          </Button>
        </EmptyState>
      </Page>
    )
  }

  if (!server || (operationId !== null && !feedQuery.data)) {
    return (
      <Page>
        <PageHeader title={server?.name ?? '…'} />
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-5 w-full" />
          <Skeleton className="h-5 w-2/3" />
        </Panel>
      </Page>
    )
  }

  const feed = feedQuery.data ?? null

  if (server.status === 'ready') {
    return <ReadyView server={server} feed={feed} />
  }
  if (server.status === 'uninstalling' && feed?.operation.kind === 'uninstall') {
    return <UninstallView server={server} feed={feed} />
  }
  if (server.status === 'new' || feed === null) {
    return <FreshView server={server} lastOperation={feed?.operation ?? null} />
  }
  return <InstallView server={server} feed={feed} />
}

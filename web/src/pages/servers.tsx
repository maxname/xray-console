import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { ServerStatusBadge } from '@/components/server-status'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { api, type Server } from '@/lib/api'
import { useRelease } from '@/lib/use-release'
import { ReleaseBanner } from '@/pages/server-lifecycle'

const STEPS = ['connect', 'template', 'install'] as const
const REQUIREMENTS = ['server', 'access', 'ports', 'domain'] as const

export function ServersPage() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.listServers(),
    // An installation changes a row's state without anyone touching this page.
    refetchInterval: (q) =>
      q.state.data?.servers.some(
        (server) => server.status === 'installing' || server.status === 'uninstalling',
      )
        ? 2000
        : // Otherwise only the availability checks move, once a minute.
          30_000,
  })

  if (query.isError) {
    return (
      <Page>
        <PageHeader title={t('servers.title')} />
        <EmptyState
          title={t('servers.loadFailedTitle')}
          description={t('servers.loadFailed')}
        >
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  if (!query.data) {
    return (
      <Page>
        <PageHeader title={t('servers.title')} />
        <Panel>
          <Skeleton className="h-5 w-48" />
          <Skeleton className="h-5 w-full" />
          <Skeleton className="h-5 w-2/3" />
        </Panel>
      </Page>
    )
  }

  const servers = query.data.servers
  if (servers.length === 0) {
    return <FirstRun />
  }

  return (
    <Page>
      <PageHeader
        title={t('servers.title')}
        description={t('servers.subtitle')}
        actions={
          <Button asChild>
            <Link to="/servers/new">{t('servers.add')}</Link>
          </Button>
        }
      />
      <ReleaseBanner />
      <Panel className="gap-3">
        <p className="text-muted-foreground">
          {t('servers.count', { count: servers.length })}
        </p>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('servers.columns.server')}</TableHead>
              <TableHead>{t('servers.columns.status')}</TableHead>
              <TableHead>{t('servers.columns.xray')}</TableHead>
              <TableHead>{t('servers.columns.template')}</TableHead>
              <TableHead>{t('servers.columns.mask')}</TableHead>
              <TableHead className="text-right">{t('servers.columns.action')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {servers.map((server) => (
              <ServerRow key={server.id} server={server} />
            ))}
          </TableBody>
        </Table>
      </Panel>
    </Page>
  )
}

function ServerRow({ server }: { server: Server }) {
  const { t } = useTranslation()
  const place = [server.host, server.location].filter(Boolean).join(' · ')
  const release = useRelease()
  const latest = release.data?.latest ?? ''
  const behind = release.data?.outdated.some((item) => item.id === server.id) ?? false

  return (
    <TableRow>
      <TableCell>
        <Link
          to="/servers/$serverId"
          params={{ serverId: String(server.id) }}
          className="font-medium text-primary underline-offset-4 hover:underline"
        >
          {server.name}
        </Link>
        <div className="font-mono text-xs text-muted-foreground">{place}</div>
      </TableCell>
      <TableCell>
        <ServerStatusBadge server={server} />
      </TableCell>
      <TableCell className="font-mono">
        {server.xray_version || '—'}
        {behind && (
          <div className="font-sans text-xs text-primary">
            {t('update.availableShort', { version: latest })}
          </div>
        )}
      </TableCell>
      <TableCell>
        <TemplateCell server={server} />
      </TableCell>
      <TableCell className="font-mono">{server.inbound?.mask_host ?? '—'}</TableCell>
      <TableCell className="text-right">
        <Button asChild variant="outline" size="sm">
          <Link to="/servers/$serverId" params={{ serverId: String(server.id) }}>
            {server.status === 'new' ? t('servers.install') : t('servers.open')}
          </Link>
        </Button>
      </TableCell>
    </TableRow>
  )
}

/** The server's template and whether it runs what the template says. */
function TemplateCell({ server }: { server: Server }) {
  const { t } = useTranslation()
  const template = server.template
  if (!template) {
    return <>—</>
  }
  const installed = server.status === 'ready'
  const note = !installed
    ? null
    : template.changed > 0
      ? t('servers.sync.changed', { count: template.changed })
      : template.overridden > 0
        ? t('servers.sync.overridden', { count: template.overridden })
        : t('servers.sync.synced', { version: template.version })

  return (
    <>
      <span translate="no">{template.name}</span>
      {note !== null && (
        <div
          className={
            template.changed > 0 ? 'text-xs text-warning' : 'text-xs text-muted-foreground'
          }
        >
          {note}
        </div>
      )}
    </>
  )
}

/** What the section shows before the first server exists. */
function FirstRun() {
  const { t } = useTranslation()

  return (
    <Page>
      <PageHeader title={t('servers.title')} description={t('servers.subtitleEmpty')} />

      <Panel className="gap-6 p-6 sm:p-8">
        <div className="flex flex-col gap-1.5">
          <h2 className="font-heading text-xl font-semibold">{t('servers.emptyTitle')}</h2>
          <p className="max-w-2xl text-muted-foreground">{t('servers.emptyDescription')}</p>
        </div>
        <Button asChild className="self-start">
          <Link to="/servers/new">{t('servers.addFirst')}</Link>
        </Button>

        <ol className="grid gap-4 sm:grid-cols-3">
          {STEPS.map((step, index) => (
            <li key={step} className="flex gap-3">
              <span
                aria-hidden="true"
                className="flex size-6 shrink-0 items-center justify-center rounded-full bg-muted font-heading text-xs font-semibold"
              >
                {index + 1}
              </span>
              <div className="flex min-w-0 flex-col gap-0.5">
                <span className="font-medium">{t(`servers.steps.${step}.title`)}</span>
                <span className="text-muted-foreground">
                  {t(`servers.steps.${step}.description`)}
                </span>
              </div>
            </li>
          ))}
        </ol>
      </Panel>

      <Panel title={t('servers.requirements.title')}>
        <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5">
          {REQUIREMENTS.map((item) => (
            <div key={item} className="contents">
              <dt className="text-muted-foreground">{t(`servers.requirements.${item}`)}</dt>
              <dd>{t(`servers.requirements.${item}Value`)}</dd>
            </div>
          ))}
        </dl>
      </Panel>
    </Page>
  )
}

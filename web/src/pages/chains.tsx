import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Fragment, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  api,
  type Chain,
  type ChainHop,
  type ChainServer,
} from '@/lib/api'
import { formatDayTime } from '@/lib/format'
import { cn } from '@/lib/utils'

import { explain, chainsKey } from '@/lib/chains'
import { ChainEditor, DeleteChainDialog } from '@/pages/chain-editor'

export function ChainsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // null: closed; 'new': creating; a chain: editing it.
  const [editing, setEditing] = useState<Chain | 'new' | null>(null)

  const query = useQuery({
    queryKey: chainsKey,
    queryFn: () => api.listChains(),
    // Faster while a batch is going through a chain's servers; otherwise the
    // checks move once a minute.
    refetchInterval: (q) =>
      q.state.data?.chains.some((chain) => chain.batch?.running) ? 1500 : 30_000,
  })
  const servers = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.listServers(),
  })

  const replace = (next: Chain) => {
    queryClient.setQueryData<{ chains: Chain[] }>(chainsKey, (current) =>
      current
        ? { chains: current.chains.map((chain) => (chain.id === next.id ? next : chain)) }
        : current,
    )
  }
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: chainsKey })
    void queryClient.invalidateQueries({ queryKey: ['servers'] })
  }

  const checkAll = useMutation({
    mutationFn: async (chains: Chain[]) => {
      // One at a time: each check is a real request through real servers.
      for (const chain of chains) {
        try {
          replace(await api.checkChain(chain.id))
        } catch {
          // A chain that cannot be checked yet says so on its own card.
        }
      }
    },
    onSettled: refresh,
  })

  if (query.isError) {
    return (
      <Page>
        <PageHeader title={t('chains.title')} />
        <EmptyState
          title={t('chains.loadFailedTitle')}
          description={t('servers.loadFailed')}
        >
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  const chains = query.data?.chains ?? []
  const installed = (servers.data?.servers ?? []).filter(
    (server) => server.status === 'ready',
  )

  return (
    <Page>
      <PageHeader
        title={t('chains.title')}
        description={t('chains.subtitle')}
        actions={
          <>
            {chains.length > 0 && (
              <Button
                variant="outline"
                disabled={checkAll.isPending}
                onClick={() => checkAll.mutate(chains)}
              >
                {checkAll.isPending ? t('chains.checking') : t('chains.checkAll')}
              </Button>
            )}
            <Button disabled={installed.length === 0} onClick={() => setEditing('new')}>
              {t('chains.create')}
            </Button>
          </>
        }
      />

      {!query.data ? (
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-20 w-full" />
        </Panel>
      ) : chains.length === 0 ? (
        <EmptyState
          title={t('chains.emptyTitle')}
          description={
            installed.length === 0 && servers.data
              ? t('chains.needServers')
              : t('chains.emptyDescription')
          }
        >
          {installed.length === 0 && servers.data ? (
            <Button asChild variant="outline">
              <Link to="/servers">{t('overview.firstRun.action')}</Link>
            </Button>
          ) : (
            <Button onClick={() => setEditing('new')}>{t('chains.create')}</Button>
          )}
        </EmptyState>
      ) : (
        <>
          {chains.map((chain) => (
            <ChainCard
              key={chain.id}
              chain={chain}
              onEdit={() => setEditing(chain)}
              onChanged={replace}
              onRefresh={refresh}
            />
          ))}
          <Panel title={t('chains.howItWorks.title')}>
            <p className="max-w-3xl text-muted-foreground">{t('chains.howItWorks.text')}</p>
          </Panel>
        </>
      )}

      {editing !== null && (
        <ChainEditor
          // A different chain is a different form.
          key={editing === 'new' ? 'new' : editing.id}
          chain={editing === 'new' ? null : editing}
          servers={installed}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            refresh()
          }}
        />
      )}
    </Page>
  )
}

function ChainCard({
  chain,
  onEdit,
  onChanged,
  onRefresh,
}: {
  chain: Chain
  onEdit: () => void
  onChanged: (chain: Chain) => void
  onRefresh: () => void
}) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)

  const check = useMutation({
    mutationFn: () => api.checkChain(chain.id),
    onSuccess: onChanged,
  })
  const apply = useMutation({
    mutationFn: () => api.applyChain(chain.id),
    onSuccess: (next) => {
      onChanged(next)
      onRefresh()
    },
  })
  const running = chain.batch?.running ?? false
  const pending = chain.pending.length > 0

  return (
    <Panel>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="font-heading text-lg font-semibold">{chain.name}</h2>
            <ChainStateBadge chain={chain} />
          </div>
          {chain.description !== '' && (
            <p className="text-muted-foreground">{chain.description}</p>
          )}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={check.isPending || running}
            onClick={() => check.mutate()}
          >
            {check.isPending ? t('chains.checking') : t('chains.actions.check')}
          </Button>
          <Button variant="outline" size="sm" disabled={running} onClick={onEdit}>
            {t('chains.actions.edit')}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            disabled={running}
            onClick={() => setDeleting(true)}
          >
            {t('chains.actions.delete')}
          </Button>
        </div>
      </div>

      <ChainDiagram chain={chain} />

      {chain.batch && (
        <div
          role="status"
          className={cn(
            'flex flex-col gap-0.5 rounded-lg px-3 py-2',
            chain.batch.failed.length > 0 ? 'bg-destructive/8' : 'bg-muted',
          )}
        >
          <span>
            {chain.batch.running
              ? t('chains.batch.running', {
                  done: chain.batch.done,
                  total: chain.batch.total,
                  current: chain.batch.current,
                })
              : t('chains.batch.done', { done: chain.batch.done, total: chain.batch.total })}
          </span>
          {chain.batch.failed.length > 0 && (
            <span className="text-destructive">
              {t('chains.batch.failed', { servers: chain.batch.failed.join(', ') })}
            </span>
          )}
        </div>
      )}

      {pending && !running && (
        <div
          role="status"
          className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2.5"
        >
          <span className="min-w-0 flex-1 basis-72">
            {t('chains.pending.text', {
              count: chain.pending.length,
              names: chain.pending.join(', '),
            })}
          </span>
          <Button size="sm" disabled={apply.isPending} onClick={() => apply.mutate()}>
            {apply.isPending ? t('chains.pending.applying') : t('chains.pending.apply')}
          </Button>
        </div>
      )}

      {!pending && chain.check && !chain.check.ok && (
        <div className="flex flex-col gap-0.5 rounded-lg border border-destructive/40 bg-destructive/8 px-3 py-2.5">
          <span className="font-medium text-destructive">{t('chains.failedNote')}</span>
          {chain.check.error !== '' && (
            <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
              {chain.check.error}
            </code>
          )}
        </div>
      )}

      {(check.isError || apply.isError) && (
        <Notice title={explain(t, check.error ?? apply.error)} />
      )}

      <DeleteChainDialog
        chain={chain}
        open={deleting}
        onOpenChange={setDeleting}
        onDeleted={onRefresh}
      />
    </Panel>
  )
}

function ChainStateBadge({ chain }: { chain: Chain }) {
  const { t } = useTranslation()
  // Until every server has the chain applied, a check tests something else.
  if (chain.pending.length > 0) {
    return <Badge variant="warning">{t('chains.state.pending')}</Badge>
  }
  if (!chain.check) {
    return <Badge variant="secondary">{t('chains.state.unchecked')}</Badge>
  }
  const when = t('chains.checkedAt', { when: formatDayTime(chain.check.at) })
  return (
    <>
      {chain.check.ok ? (
        <Badge variant="success">{t('chains.state.okMs', { ms: chain.check.ms })}</Badge>
      ) : (
        <Badge variant="destructive">{t('chains.state.failed')}</Badge>
      )}
      <span className="text-xs text-muted-foreground">{when}</span>
    </>
  )
}

/** The route as boxes joined by lines: client, each link, the Internet. */
function ChainDiagram({ chain }: { chain: Chain }) {
  const { t } = useTranslation()
  const last = chain.hops.length - 1
  const role = (index: number): string => {
    if (chain.hops.length === 1) return t('chains.role.only')
    if (index === 0) return t('chains.role.entry')
    if (index === last) return t('chains.role.exit')
    return t('chains.role.middle', { number: index + 1 })
  }

  return (
    // A column on a phone, where a row would wrap mid-route and leave the
    // connectors pointing at nothing.
    <div className="flex flex-col items-start sm:flex-row sm:flex-wrap sm:items-stretch sm:gap-y-3">
      <EndBox title={t('chains.client')} />
      {chain.hops.map((hop, index) => (
        <Fragment key={index}>
          <Connector />
          <HopBox hop={hop} role={role(index)} />
        </Fragment>
      ))}
      <Connector />
      <EndBox title={t('chains.internet')} note={t('chains.destination')} />
    </div>
  )
}

function Connector() {
  return (
    <span
      aria-hidden="true"
      className="flex h-4 w-12 justify-center sm:h-auto sm:w-auto sm:max-w-24 sm:min-w-6 sm:flex-1 sm:items-center"
    >
      <span className="h-full w-px bg-border sm:h-px sm:w-full" />
    </span>
  )
}

function EndBox({ title, note }: { title: string; note?: string }) {
  return (
    <div className="flex min-h-14 flex-col justify-center rounded-lg bg-muted px-3 py-2">
      <span className="font-medium">{title}</span>
      {note !== undefined && <span className="text-xs text-muted-foreground">{note}</span>}
    </div>
  )
}

function HopBox({ hop, role }: { hop: ChainHop; role: string }) {
  const { t } = useTranslation()
  const group = hop.servers.length > 1
  return (
    <div
      className={cn(
        'flex min-h-14 min-w-36 flex-col justify-center gap-1 rounded-lg border bg-card px-3 py-2',
        group && 'border-primary',
      )}
    >
      <span className="text-xs text-muted-foreground">
        {group
          ? `${role} · ${t('chains.group', {
              strategy: t(`chains.strategyShort.${hop.strategy}`),
            })}`
          : role}
      </span>
      {hop.servers.map((server) => (
        <MemberLine key={server.id} server={server} />
      ))}
      {hop.reserve && (
        <span className="text-xs text-warning">
          {t('chains.reserve', { name: hop.reserve.name })}
        </span>
      )}
    </div>
  )
}

function MemberLine({ server }: { server: ChainServer }) {
  const { t } = useTranslation()
  const bad = server.state === 'down' || server.state === 'missing'
  return (
    <span className="flex items-baseline justify-between gap-3">
      {server.state === 'missing' ? (
        <span className="font-medium text-destructive">#{server.id}</span>
      ) : (
        <Link
          to="/servers/$serverId"
          params={{ serverId: String(server.id) }}
          className="font-medium underline-offset-4 hover:underline"
        >
          {server.name}
        </Link>
      )}
      <span
        className={cn(
          'text-xs tabular-nums',
          bad ? 'text-destructive' : 'text-muted-foreground',
        )}
      >
        {server.state === 'up'
          ? server.tcp_ms !== null
            ? t('overview.state.up', { ms: server.tcp_ms })
            : ''
          : t(`chains.serverState.${server.state}`)}
      </span>
    </span>
  )
}

// --- editing ---------------------------------------------------------------------


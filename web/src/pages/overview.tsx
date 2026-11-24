import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { StatTile, TrafficDays, UptimeBar, UptimeLegend } from '@/components/charts'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  api,
  type Overview,
  type OverviewServer,
  type PanelEvent,
} from '@/lib/api'
import { formatBytes, formatDayTime, formatDuration, formatPercent } from '@/lib/format'
import { useMe } from '@/lib/use-me'
import { useNow } from '@/lib/use-now'
import { cn } from '@/lib/utils'

type Translate = (key: string, options?: Record<string, unknown>) => string

export function OverviewPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['overview'],
    queryFn: () => api.getOverview(),
    // Checks run once a minute; half that keeps the page at most half a
    // minute behind without asking for the same answer over and over.
    refetchInterval: 30_000,
  })
  const check = useMutation({
    mutationFn: () => api.runChecks(),
    onSuccess: (overview) => {
      queryClient.setQueryData(['overview'], overview)
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    },
  })

  if (query.isError) {
    return (
      <Page>
        <PageHeader title={t('overview.title')} />
        <EmptyState
          title={t('overview.loadFailedTitle')}
          description={t('servers.loadFailed')}
        >
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }
  const overview = query.data
  if (!overview) {
    return (
      <Page>
        <PageHeader title={t('overview.title')} />
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-16 w-full" />
        </Panel>
      </Page>
    )
  }

  const { counts } = overview
  const down = overview.servers.filter((server) => server.check?.state === 'down')
  const checked = counts.up + counts.down + counts.unknown

  return (
    <Page>
      <PageHeader
        title={t('overview.title')}
        description={summary(t, overview)}
        actions={
          <>
            {checked > 0 && (
              <Button
                variant="outline"
                onClick={() => check.mutate()}
                disabled={check.isPending}
              >
                {check.isPending ? t('overview.checking') : t('overview.checkNow')}
              </Button>
            )}
            <Button asChild>
              <Link to="/servers/new">{t('overview.addServer')}</Link>
            </Button>
          </>
        }
      />
      {check.isError && <Notice title={t('overview.checkFailed')} />}

      {down.map((server) => (
        <DownAlert key={server.id} server={server} />
      ))}

      {counts.servers === 0 ? (
        <Panel className="sm:flex-row sm:items-center sm:justify-between">
          <div className="flex min-w-0 flex-col gap-1">
            <h2 className="font-heading text-base font-semibold">
              {t('overview.firstRun.title')}
            </h2>
            <p className="max-w-xl text-muted-foreground">
              {t('overview.firstRun.description')}
            </p>
          </div>
          <Button asChild variant="outline" className="self-start sm:self-center">
            <Link to="/servers">{t('overview.firstRun.action')}</Link>
          </Button>
        </Panel>
      ) : (
        <>
          <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
            <OnlineTile overview={overview} down={down} />
            <TrafficTile overview={overview} />
            <LatencyTile overview={overview} />
          </div>
          <Panel>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="font-heading text-base font-semibold">
                {t('overview.availability.title')}
              </h2>
              <Link to="/servers" className="text-primary underline-offset-4 hover:underline">
                {t('overview.availability.all')}
              </Link>
            </div>
            <ul className="flex flex-col divide-y">
              {overview.servers.map((server) => (
                <AvailabilityRow key={server.id} server={server} />
              ))}
            </ul>
            <UptimeLegend />
          </Panel>
        </>
      )}

      {overview.counting > 0 && (
        <Panel>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-heading text-base font-semibold">{t('overview.traffic.title')}</h2>
            <Link to="/stats" className="text-primary underline-offset-4 hover:underline">
              {t('overview.traffic.more')}
            </Link>
          </div>
          <TrafficDays days={overview.traffic} label={t('overview.traffic.title')} />
        </Panel>
      )}

      <div className="grid gap-5 lg:grid-cols-2">
        <Panel title={t('overview.events.title')}>
          {overview.events.length === 0 ? (
            <EmptyState
              title={t('overview.events.emptyTitle')}
              description={t('overview.events.emptyDescription')}
              className="flex-1 justify-center"
            />
          ) : (
            <ul className="flex flex-col gap-2.5">
              {overview.events.map((event) => (
                <EventLine key={event.id} event={event} />
              ))}
            </ul>
          )}
        </Panel>
        <PanelInfo />
      </div>
    </Page>
  )
}

function summary(t: Translate, overview: Overview): string {
  const { counts } = overview
  if (counts.servers === 0) {
    return t('overview.noServers')
  }
  const parts = [t('overview.servers', { count: counts.servers })]
  if (counts.up > 0) {
    parts.push(t('overview.online', { count: counts.up }))
  }
  if (counts.down > 0) {
    parts.push(t('overview.down', { count: counts.down }))
  }
  return parts.join(' · ')
}

/** A server that stopped answering: what, for how long, and the likeliest why. */
function DownAlert({ server }: { server: OverviewServer }) {
  const { t } = useTranslation()
  const now = useNow()
  const check = server.check!
  const seconds = Math.max(0, now - check.since)
  return (
    <div
      role="status"
      className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-xl border border-destructive/40 bg-destructive/8 px-4 py-3 text-sm"
    >
      <div className="flex min-w-0 flex-1 basis-80 flex-col gap-0.5">
        <span className="font-semibold text-destructive">
          {t('overview.alert.title', {
            name: server.name,
            duration: formatDuration(seconds),
          })}
        </span>
        <span>{t(check.stage === 'tcp' ? 'overview.alert.tcp' : 'overview.alert.request')}</span>
        {check.error !== '' && (
          <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
            {check.error}
          </code>
        )}
      </div>
      <Link
        to="/servers/$serverId"
        params={{ serverId: String(server.id) }}
        className="font-medium text-primary underline-offset-4 hover:underline"
      >
        {t('overview.alert.open')}
      </Link>
    </div>
  )
}

function OnlineTile({ overview, down }: { overview: Overview; down: OverviewServer[] }) {
  const { t } = useTranslation()
  const { counts } = overview
  // "Out of" everything checks apply to; a server with no Xray is not offline.
  const total = counts.up + counts.down + counts.unknown
  const note =
    down.length > 0
      ? t('overview.tiles.someDown', { names: down.map((server) => server.name).join(', ') })
      : counts.unknown > 0 || total === 0
        ? t('overview.tiles.waiting')
        : t('overview.tiles.allUp')
  return (
    <StatTile
      label={t('overview.tiles.online')}
      value={t('overview.tiles.onlineValue', { up: counts.up, total })}
      note={note}
    />
  )
}

function TrafficTile({ overview }: { overview: Overview }) {
  const { t } = useTranslation()
  const sum = (day: { up: number; down: number } | undefined) => (day ? day.up + day.down : 0)
  const today = sum(overview.traffic[overview.traffic.length - 1])
  const yesterday = sum(overview.traffic[overview.traffic.length - 2])
  if (overview.counting === 0) {
    return (
      <StatTile
        label={t('overview.tiles.traffic')}
        value="—"
        note={t('overview.tiles.trafficOff')}
      />
    )
  }
  return (
    <StatTile
      label={t('overview.tiles.traffic')}
      value={formatBytes(today)}
      note={t('overview.tiles.trafficNote', { amount: formatBytes(yesterday) })}
    />
  )
}

function LatencyTile({ overview }: { overview: Overview }) {
  const { t } = useTranslation()
  const { latency } = overview
  if (latency.avg_ms === null) {
    return (
      <StatTile
        label={t('overview.tiles.latency')}
        value={t('overview.tiles.latencyEmpty')}
        note={t('overview.tiles.latencyEmptyNote')}
      />
    )
  }
  return (
    <StatTile
      label={t('overview.tiles.latency')}
      value={t('overview.tiles.latencyValue', { ms: latency.avg_ms })}
      note={t('overview.tiles.latencyNote', {
        count: latency.servers,
        seconds: latency.interval_seconds,
      })}
    />
  )
}

function AvailabilityRow({ server }: { server: OverviewServer }) {
  const { t } = useTranslation()
  const percent = server.uptime_percent
  const state = server.status !== 'ready' ? 'not_installed' : (server.check?.state ?? 'unknown')

  const badge =
    state === 'up' ? (
      <Badge variant="success">
        {server.check?.tcp_ms != null
          ? t('overview.state.up', { ms: server.check.tcp_ms })
          : t('overview.state.upNoLatency')}
      </Badge>
    ) : state === 'down' ? (
      <Badge variant="destructive">{t('overview.state.down')}</Badge>
    ) : (
      <Badge variant="secondary">{t(`overview.state.${state}`)}</Badge>
    )

  return (
    <li className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 py-3 first:pt-0 last:pb-0 md:grid-cols-[minmax(0,12rem)_6.5rem_minmax(0,1fr)_4rem]">
      <div className="flex min-w-0 flex-col">
        <Link
          to="/servers/$serverId"
          params={{ serverId: String(server.id) }}
          className="truncate font-medium underline-offset-4 hover:underline"
        >
          {server.name}
        </Link>
        {server.location !== '' && (
          <span className="truncate text-xs text-muted-foreground">{server.location}</span>
        )}
      </div>
      <div className="justify-self-end md:justify-self-start">{badge}</div>
      <UptimeBar
        days={server.days}
        label={
          percent === null
            ? t('overview.availability.ariaEmpty', { name: server.name })
            : t('overview.availability.aria', {
                name: server.name,
                percent: formatPercent(percent),
              })
        }
      />
      <span className="justify-self-end font-heading font-semibold tabular-nums">
        {percent === null ? '—' : formatPercent(percent)}
      </span>
    </li>
  )
}

function describeEvent(t: Translate, event: PanelEvent): string {
  const name = event.server_name
  const data = event.data
  switch (event.kind) {
    case 'server_installed':
      return t('overview.events.server_installed', {
        name,
        version: typeof data.version === 'string' ? data.version : '',
      })
    case 'config_applied': {
      const changed = Array.isArray(data.changed)
        ? data.changed.filter((key): key is string => typeof key === 'string')
        : []
      return changed.length > 0
        ? t('overview.events.config_applied', {
            name,
            fields: changed.map((key) => t(`fields.${key}`)).join(', '),
          })
        : t('overview.events.config_applied_same', { name })
    }
    case 'apply_failed':
      return data.restored === false
        ? t('overview.events.apply_failed_broken', { name })
        : t('overview.events.apply_failed', { name })
    case 'server_up':
      return t('overview.events.server_up', {
        name,
        duration: formatDuration(
          typeof data.down_seconds === 'number' ? data.down_seconds : 0,
        ),
      })
    case 'xray_updated':
    case 'update_failed': {
      const versions = {
        name,
        from: typeof data.from === 'string' ? data.from : '',
        to: typeof data.to === 'string' ? data.to : '',
      }
      if (event.kind === 'xray_updated') {
        return t('overview.events.xray_updated', versions)
      }
      return data.restored === false
        ? t('overview.events.update_failed_broken', versions)
        : t('overview.events.update_failed', versions)
    }
    default:
      return t(`overview.events.${event.kind}`, { name })
  }
}

function EventLine({ event }: { event: PanelEvent }) {
  const { t } = useTranslation()
  const bad =
    event.kind === 'server_down' ||
    event.kind === 'apply_failed' ||
    event.kind === 'update_failed'
  return (
    <li className="grid grid-cols-[auto_1fr] gap-x-4">
      <span className="font-mono text-xs leading-5 text-muted-foreground">
        {formatDayTime(event.at)}
      </span>
      <span className={cn(bad && 'text-destructive')}>{describeEvent(t, event)}</span>
    </li>
  )
}

function PanelInfo() {
  const { t } = useTranslation()
  const { data: me, isError, refetch } = useMe()

  if (isError) {
    return (
      <Panel title={t('overview.panel.title')}>
        <p className="text-muted-foreground">{t('common.loadFailed')}</p>
        <Button variant="outline" className="self-start" onClick={() => void refetch()}>
          {t('common.retry')}
        </Button>
      </Panel>
    )
  }

  const totp = me
    ? me.totp_enabled
      ? t('overview.panel.totpOn')
      : me.totp_pending
        ? t('overview.panel.totpPending')
        : t('overview.panel.totpOff')
    : null

  return (
    <Panel title={t('overview.panel.title')}>
      <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5">
        <Row label={t('overview.panel.address')}>
          {me ? <span className="font-mono break-all">{me.panel_url}</span> : null}
        </Row>
        <Row label={t('overview.panel.admin')}>{me?.username ?? null}</Row>
        <Row label={t('overview.panel.totp')}>{totp}</Row>
        <Row label={t('overview.panel.lastLogin')}>
          {me
            ? me.last_login_at !== null
              ? t('overview.panel.lastLoginValue', {
                  when: formatDayTime(me.last_login_at),
                  ip: me.last_login_ip,
                })
              : '—'
            : null}
        </Row>
        <Row label={t('overview.panel.version')}>
          {me ? <span className="font-mono">{me.version}</span> : null}
        </Row>
      </dl>
    </Panel>
  )
}

/** One label–value pair; a null value is still loading. */
function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0">{children ?? <Skeleton className="h-5 w-32" />}</dd>
    </>
  )
}

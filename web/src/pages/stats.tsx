import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import {
  StatTile,
  TrafficDays,
  TrafficLegend,
  UptimeBar,
  UptimeLegend,
} from '@/components/charts'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Segmented } from '@/components/segmented'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import {
  api,
  ApiError,
  type AlertSettings,
  type AlertTestResult,
  type Stats,
  type StatsPeriod,
  type StatsServer,
} from '@/lib/api'
import { type Translate } from '@/lib/errors'
import {
  formatBytes,
  formatDay,
  formatDayTime,
  formatDuration,
  formatPercent,
} from '@/lib/format'
import { cn } from '@/lib/utils'

const PERIODS: StatsPeriod[] = ['24h', '7d', '30d']
const PERIOD_DAYS: Record<StatsPeriod, number> = { '24h': 1, '7d': 7, '30d': 30 }

export function StatsPage() {
  const { t } = useTranslation()
  const [period, setPeriod] = useState<StatsPeriod>('7d')
  const [alerts, setAlerts] = useState(false)

  const query = useQuery({
    queryKey: ['stats', period],
    queryFn: () => api.getStats(period),
    // Checks move once a minute, traffic every few.
    refetchInterval: 60_000,
    // Switching the period keeps the page on screen instead of blanking it.
    placeholderData: (previous) => previous,
  })

  const actions = (
    <>
      <Segmented
        label={t('stats.period.label')}
        options={PERIODS.map((option) => ({ value: option, label: t(`stats.period.${option}`) }))}
        value={period}
        onChange={setPeriod}
      />
      <Button variant="outline" onClick={() => setAlerts(true)}>
        {t('alerts.open')}
      </Button>
    </>
  )

  if (query.isError && !query.data) {
    return (
      <Page>
        <PageHeader title={t('stats.title')} />
        <EmptyState title={t('stats.loadFailedTitle')} description={t('servers.loadFailed')}>
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  const stats = query.data
  return (
    <Page>
      <PageHeader title={t('stats.title')} description={t('stats.subtitle')} actions={actions} />

      {!stats ? (
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-24 w-full" />
        </Panel>
      ) : stats.servers.length === 0 ? (
        <EmptyState title={t('stats.emptyTitle')} description={t('stats.emptyDescription')}>
          <Button asChild variant="outline">
            <Link to="/servers">{t('overview.firstRun.action')}</Link>
          </Button>
        </EmptyState>
      ) : (
        <>
          <Tiles stats={stats} />
          <TrafficPanel stats={stats} />
          <LatencyPanel stats={stats} />
          <AvailabilityPanel stats={stats} />
        </>
      )}

      {alerts && <AlertsDialog onClose={() => setAlerts(false)} />}
    </Page>
  )
}

function Tiles({ stats }: { stats: Stats }) {
  const { t } = useTranslation()
  const total = stats.traffic.up + stats.traffic.down
  const days = PERIOD_DAYS[stats.period]
  const counting = stats.servers.some((server) => server.counting)
  const { latency, incidents } = stats
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <StatTile
        label={t(`stats.tiles.traffic.${stats.period}`)}
        value={counting ? formatBytes(total) : '—'}
        note={
          !counting
            ? t('stats.tiles.trafficOff')
            : days > 1
              ? t('stats.tiles.trafficPerDay', { amount: formatBytes(total / days) })
              : t('stats.tiles.trafficSplit', {
                  down: formatBytes(stats.traffic.down),
                  up: formatBytes(stats.traffic.up),
                })
        }
      />
      <StatTile
        label={t('stats.tiles.availability')}
        value={stats.availability_percent === null ? '—' : formatPercent(stats.availability_percent)}
        note={
          stats.availability_percent === null
            ? t('stats.tiles.noChecks')
            : t('stats.tiles.availabilityNote', { count: latency.checks })
        }
      />
      <StatTile
        label={t('stats.tiles.latency')}
        value={latency.request_median === null ? '—' : t('stats.ms', { ms: latency.request_median })}
        note={
          latency.request_p95 === null
            ? t('stats.tiles.noChecks')
            : t('stats.tiles.latencyNote', { ms: latency.request_p95 })
        }
      />
      <StatTile
        label={t('stats.tiles.incidents')}
        value={String(incidents.count)}
        note={
          incidents.count === 0
            ? t('stats.tiles.noIncidents')
            : t('stats.tiles.incidentsNote', { duration: formatDuration(incidents.seconds) })
        }
      />
    </div>
  )
}

function TrafficPanel({ stats }: { stats: Stats }) {
  const { t } = useTranslation()
  const installed = stats.servers.filter((server) => server.status === 'ready')
  // Bars share one scale: the busiest server is the full width.
  const peak = Math.max(1, ...installed.map((server) => server.traffic.up + server.traffic.down))
  const sorted = [...installed].sort(
    (a, b) => b.traffic.up + b.traffic.down - (a.traffic.up + a.traffic.down),
  )
  return (
    <div className="grid gap-5 xl:grid-cols-2">
      <Panel>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-heading text-base font-semibold">
            {t(`stats.traffic.byServer.${stats.period}`)}
          </h2>
          <TrafficLegend />
        </div>
        {sorted.length === 0 ? (
          <p className="text-muted-foreground">{t('stats.traffic.noServers')}</p>
        ) : (
          <ul className="flex flex-col gap-3">
            {sorted.map((server) => (
              <TrafficRow key={server.id} server={server} peak={peak} />
            ))}
          </ul>
        )}
        <p className="text-xs text-muted-foreground">{t('stats.traffic.note')}</p>
      </Panel>
      <Panel>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-heading text-base font-semibold">{t('stats.traffic.byDay')}</h2>
          <TrafficLegend />
        </div>
        <TrafficDays days={stats.traffic_days} label={t('stats.traffic.byDay')} />
        <div className="flex justify-between text-xs text-muted-foreground tabular-nums">
          <span>{formatDay(stats.traffic_days[0].day)}</span>
          <span>{formatDay(stats.traffic_days[stats.traffic_days.length - 1].day)}</span>
        </div>
      </Panel>
    </div>
  )
}

function TrafficRow({ server, peak }: { server: StatsServer; peak: number }) {
  const { t } = useTranslation()
  const { up, down } = server.traffic
  const total = up + down
  const share = (value: number) => `${(value / peak) * 100}%`
  return (
    <li className="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_auto] items-center gap-x-3">
      <Link
        to="/servers/$serverId"
        params={{ serverId: String(server.id) }}
        className="truncate font-medium underline-offset-4 hover:underline"
      >
        {server.name}
      </Link>
      {!server.counting ? (
        <span className="col-span-2 text-xs text-warning">{t('stats.traffic.notCounting')}</span>
      ) : (
        <>
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                role="img"
                tabIndex={-1}
                aria-label={t('stats.traffic.aria', {
                  name: server.name,
                  down: formatBytes(down),
                  up: formatBytes(up),
                })}
                className="flex h-5 items-center gap-0.5"
              >
                {total === 0 ? (
                  <span className="h-0.5 w-full rounded-sm bg-border" />
                ) : (
                  <>
                    {down > 0 && (
                      <span
                        className={cn('h-3 bg-traffic-down', up === 0 && 'rounded-r-sm')}
                        style={{ width: share(down), minWidth: 2 }}
                      />
                    )}
                    {up > 0 && (
                      <span
                        className="h-3 rounded-r-sm bg-traffic-up"
                        style={{ width: share(up), minWidth: 2 }}
                      />
                    )}
                  </>
                )}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {t('stats.traffic.tooltip', { down: formatBytes(down), up: formatBytes(up) })}
            </TooltipContent>
          </Tooltip>
          <span className="justify-self-end font-heading font-semibold tabular-nums">
            {formatBytes(total)}
          </span>
        </>
      )}
    </li>
  )
}

function LatencyPanel({ stats }: { stats: Stats }) {
  const { t } = useTranslation()
  const ms = (value: number | null) => (value === null ? '—' : t('stats.ms', { ms: value }))
  const checked = stats.servers.filter((server) => server.status === 'ready')
  return (
    <Panel title={t('stats.latency.title')} className="gap-3">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('stats.latency.server')}</TableHead>
            <TableHead className="text-right">{t('stats.latency.tcp')}</TableHead>
            <TableHead className="text-right">{t('stats.latency.request')}</TableHead>
            <TableHead className="hidden text-right sm:table-cell">{t('stats.latency.p95')}</TableHead>
            <TableHead className="text-right">{t('stats.latency.loss')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {checked.map((server) => {
            const loss = server.loss_percent
            return (
              <TableRow key={server.id}>
                <TableCell>
                  <Link
                    to="/servers/$serverId"
                    params={{ serverId: String(server.id) }}
                    className="font-medium underline-offset-4 hover:underline"
                  >
                    {server.name}
                  </Link>
                </TableCell>
                <TableCell className="text-right tabular-nums">{ms(server.latency.tcp_median)}</TableCell>
                <TableCell className="text-right tabular-nums">
                  {ms(server.latency.request_median)}
                </TableCell>
                <TableCell className="hidden text-right tabular-nums sm:table-cell">
                  {ms(server.latency.request_p95)}
                </TableCell>
                <TableCell
                  className={cn(
                    'text-right tabular-nums',
                    loss !== null && loss > 0 && 'font-medium text-destructive',
                  )}
                >
                  {loss === null ? '—' : formatPercent(loss)}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
      <p className="text-xs text-muted-foreground">{t('stats.latency.note')}</p>
    </Panel>
  )
}

function AvailabilityPanel({ stats }: { stats: Stats }) {
  const { t } = useTranslation()
  const checked = stats.servers.filter((server) => server.status === 'ready')
  return (
    <Panel>
      <h2 className="font-heading text-base font-semibold">{t('stats.availability.title')}</h2>
      <ul className="flex flex-col divide-y">
        {checked.map((server) => {
          const percent = server.uptime_percent
          const outage = server.last_outage
          return (
            <li
              key={server.id}
              className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-1.5 py-2.5 md:grid-cols-[minmax(0,8rem)_minmax(0,1fr)_4.5rem]"
            >
              <span className="truncate font-medium">{server.name}</span>
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
              <span
                className={cn(
                  'col-span-2 text-xs md:col-span-3',
                  outage?.ongoing ? 'text-destructive' : 'text-muted-foreground',
                )}
              >
                {outage === null
                  ? t(`stats.availability.none.${stats.period}`)
                  : outage.ongoing
                    ? t('stats.availability.ongoing', { when: formatDayTime(outage.start) })
                    : t('stats.availability.last', {
                        count: server.outages,
                        when: formatDayTime(outage.start),
                        duration: formatDuration(outage.seconds),
                      })}
              </span>
            </li>
          )
        })}
      </ul>
      <UptimeLegend />
    </Panel>
  )
}

// --- alerts ----------------------------------------------------------------------

function explainAlert(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_alert') {
      return t(`alerts.errors.${String(error.details.reason ?? '')}`, {
        defaultValue: t('common.error'),
      })
    }
    if (error.code === 'no_channel') {
      return t('alerts.errors.no_channel')
    }
  }
  return t('common.error')
}

function AlertsDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['alerts'], queryFn: () => api.getAlerts(), staleTime: 0 })

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        {query.data ? (
          <AlertsForm settings={query.data} onClose={onClose} />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('alerts.title')}</DialogTitle>
              <DialogDescription>{t('alerts.description')}</DialogDescription>
            </DialogHeader>
            {query.isError ? (
              <Notice title={t('common.loadFailed')} />
            ) : (
              <Skeleton className="h-40 w-full" />
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function AlertsForm({ settings, onClose }: { settings: AlertSettings; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [form, setForm] = useState(settings)
  // The token is never sent back, so the field starts empty: left empty it
  // keeps what is saved, unless removal was asked for.
  const [token, setToken] = useState('')
  const [removeToken, setRemoveToken] = useState(false)
  const [tested, setTested] = useState<AlertTestResult | null>(null)

  const set = (change: Partial<AlertSettings>) => setForm((current) => ({ ...current, ...change }))
  const body = () => ({
    enabled: form.enabled,
    telegram_token: removeToken ? '' : token.trim() !== '' ? token.trim() : null,
    telegram_chat: removeToken ? '' : form.telegram_chat,
    webhook_url: form.webhook_url,
    on_down: form.on_down,
    on_up: form.on_up,
    on_update: form.on_update,
    down_after_minutes: form.down_after_minutes,
    repeat_hours: form.repeat_hours,
    quiet_hours: form.quiet_hours,
  })
  const saved = (next: AlertSettings) => {
    queryClient.setQueryData(['alerts'], next)
    setForm(next)
    setToken('')
    setRemoveToken(false)
  }

  const save = useMutation({
    mutationFn: () => api.updateAlerts(body()),
    onSuccess: (next) => {
      saved(next)
      onClose()
    },
  })
  // A test is of what is on screen: save first, then send.
  const test = useMutation({
    mutationFn: async () => {
      saved(await api.updateAlerts(body()))
      return api.testAlerts()
    },
    onMutate: () => setTested(null),
    onSuccess: setTested,
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }
  const busy = save.isPending || test.isPending
  const hasToken = (form.telegram_token_set && !removeToken) || token.trim() !== ''
  const failure = save.error ?? test.error

  return (
    <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
      <DialogHeader>
        <DialogTitle>{t('alerts.title')}</DialogTitle>
        <DialogDescription>{t('alerts.description')}</DialogDescription>
      </DialogHeader>

      <fieldset className="grid gap-3 rounded-lg border p-3">
        <legend className="px-1 font-medium">{t('alerts.telegram.title')}</legend>
        <Field
          hint={
            form.telegram_token_set && !removeToken
              ? t('alerts.telegram.tokenSaved')
              : t('alerts.telegram.tokenHint')
          }
        >
          <Label htmlFor="alerts-token">{t('alerts.telegram.token')}</Label>
          <div className="flex gap-2">
            <Input
              id="alerts-token"
              className="min-w-0 flex-1 font-mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              disabled={removeToken}
              placeholder={form.telegram_token_set && !removeToken ? '••••••••' : '123456789:AA…'}
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
            {form.telegram_token_set && (
              <Button type="button" variant="outline" onClick={() => setRemoveToken((value) => !value)}>
                {removeToken ? t('alerts.telegram.keep') : t('alerts.telegram.remove')}
              </Button>
            )}
          </div>
        </Field>
        <Field hint={t('alerts.telegram.chatHint')}>
          <Label htmlFor="alerts-chat">{t('alerts.telegram.chat')}</Label>
          <Input
            id="alerts-chat"
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
            disabled={removeToken}
            placeholder="-1001234567890"
            value={removeToken ? '' : form.telegram_chat}
            onChange={(event) => set({ telegram_chat: event.target.value })}
          />
        </Field>
      </fieldset>

      <Field hint={t('alerts.webhook.hint')}>
        <Label htmlFor="alerts-webhook">{t('alerts.webhook.url')}</Label>
        <Input
          id="alerts-webhook"
          className="font-mono"
          type="url"
          autoComplete="off"
          spellCheck={false}
          placeholder="https://hooks.example.net/xray"
          value={form.webhook_url}
          onChange={(event) => set({ webhook_url: event.target.value })}
        />
      </Field>

      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1.5 font-medium">{t('alerts.about')}</legend>
        {(
          [
            ['on_down', 'down'],
            ['on_up', 'up'],
            ['on_update', 'update'],
          ] as const
        ).map(([key, label]) => (
          <label key={key} className="inline-flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={form[key]}
              onChange={(event) => set({ [key]: event.target.checked })}
            />
            <span>{t(`alerts.events.${label}`)}</span>
          </label>
        ))}
      </fieldset>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field hint={t('alerts.afterHint')}>
          <Label htmlFor="alerts-after">{t('alerts.after')}</Label>
          <Input
            id="alerts-after"
            type="number"
            min={1}
            max={1440}
            className="w-28 tabular-nums"
            value={form.down_after_minutes}
            onChange={(event) => set({ down_after_minutes: Number(event.target.value) })}
          />
        </Field>
        <Field>
          <Label htmlFor="alerts-repeat">{t('alerts.repeat.label')}</Label>
          <Select
            value={String(form.repeat_hours)}
            onValueChange={(value) => set({ repeat_hours: Number(value) })}
          >
            <SelectTrigger id="alerts-repeat" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="0">{t('alerts.repeat.0')}</SelectItem>
              <SelectItem value="1">{t('alerts.repeat.1')}</SelectItem>
              <SelectItem value="6">{t('alerts.repeat.6')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('alerts.quiet.title')}</span>
          <span className="text-xs text-muted-foreground">{t('alerts.quiet.hint')}</span>
        </span>
        <Switch
          checked={form.quiet_hours}
          aria-label={t('alerts.quiet.title')}
          onCheckedChange={(quiet_hours) => set({ quiet_hours })}
        />
      </label>
      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('alerts.enabled.title')}</span>
          <span className="text-xs text-muted-foreground">{t('alerts.enabled.hint')}</span>
        </span>
        <Switch
          checked={form.enabled}
          aria-label={t('alerts.enabled.title')}
          onCheckedChange={(enabled) => set({ enabled })}
        />
      </label>

      {tested !== null && (
        <div role="status" className="flex flex-col gap-1 rounded-lg bg-muted px-3 py-2.5">
          {(['telegram', 'webhook'] as const)
            .filter((channel) => tested[channel] !== '')
            .map((channel) => (
              <span key={channel} className={cn(tested[channel] !== 'ok' && 'text-destructive')}>
                {tested[channel] === 'ok'
                  ? t(`alerts.test.${channel}Ok`)
                  : t(`alerts.test.${channel}Failed`)}
                {tested[channel] !== 'ok' && (
                  <code className="block font-mono text-xs break-all" translate="no">
                    {tested[channel]}
                  </code>
                )}
              </span>
            ))}
        </div>
      )}
      {failure !== null && <Notice title={explainAlert(t, failure)} />}

      <DialogFooter className="sm:justify-between">
        <Button
          type="button"
          variant="outline"
          disabled={busy || (!hasToken && form.webhook_url.trim() === '')}
          onClick={() => test.mutate()}
        >
          {test.isPending ? t('alerts.test.sending') : t('alerts.test.send')}
        </Button>
        <div className="flex flex-col-reverse gap-2 sm:flex-row">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={busy}>
            {save.isPending ? t('routing.saving') : t('routing.save')}
          </Button>
        </div>
      </DialogFooter>
    </form>
  )
}

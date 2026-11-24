import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { Panel } from '@/components/page'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  api,
  type Device,
  type LocalClient,
  type RuleSet,
} from '@/lib/api'
import { formatBytes, formatDayTime, formatDuration } from '@/lib/format'
import { useNow } from '@/lib/use-now'

import { explain, useDevicesCache } from '@/lib/devices'
import { NewDeviceDialog, DeviceDialog } from '@/pages/device-dialogs'

/** The client inside the panel: local proxies on the computer it runs on. */
export function LocalClientPanel({ local, sets }: { local: LocalClient; sets: RuleSet[] }) {
  const { t } = useTranslation()
  const update = useDevicesCache()
  const [ruleSet, setRuleSet] = useState(local.rule_set_id || sets[0]?.id || 0)
  const [socks, setSocks] = useState(String(local.socks_port))
  const [http, setHttp] = useState(String(local.http_port))

  const save = useMutation({
    mutationFn: (enabled: boolean) =>
      api.updateLocalClient({
        enabled,
        rule_set_id: ruleSet,
        socks_port: Number(socks),
        http_port: Number(http),
      }),
    onSuccess: update,
  })
  // The switch turns the client on and off and nothing else: it goes with the
  // settings already saved, not with whatever is being typed into the form.
  // Before the first save there are none, so the rule set shown is the one.
  const toggle = useMutation({
    mutationFn: (enabled: boolean) =>
      api.updateLocalClient({
        enabled,
        rule_set_id: local.rule_set_id || ruleSet,
        socks_port: local.socks_port,
        http_port: local.http_port,
      }),
    onSuccess: update,
  })
  // Before the first save there is no rule set to differ from.
  const dirty =
    (local.rule_set_id !== 0 && ruleSet !== local.rule_set_id) ||
    Number(socks) !== local.socks_port ||
    Number(http) !== local.http_port

  return (
    <Panel>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="font-heading text-base font-semibold">{t('devices.local.title')}</h2>
            <LocalBadge local={local} />
          </div>
          <p className="max-w-2xl text-muted-foreground">{t('devices.local.text')}</p>
        </div>
        <Switch
          checked={local.enabled}
          disabled={save.isPending || toggle.isPending || (sets.length === 0 && !local.enabled)}
          aria-label={t('devices.local.switch')}
          onCheckedChange={(enabled) => toggle.mutate(enabled)}
        />
      </div>

      {sets.length === 0 && !local.enabled ? (
        <p className="text-muted-foreground">{t('devices.needRuleSet')}</p>
      ) : (
        <form
          className="flex flex-wrap items-end gap-3"
          onSubmit={(event) => {
            event.preventDefault()
            save.mutate(local.enabled)
          }}
        >
          <Field>
            <Label htmlFor="local-rule-set">{t('devices.fields.ruleSet')}</Label>
            <Select value={String(ruleSet)} onValueChange={(value) => setRuleSet(Number(value))}>
              <SelectTrigger id="local-rule-set" className="w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sets.map((set) => (
                  <SelectItem key={set.id} value={String(set.id)}>
                    {set.name} · {set.entry.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <Label htmlFor="local-socks">{t('devices.fields.socks')}</Label>
            <Input
              id="local-socks"
              type="number"
              className="w-28 tabular-nums"
              value={socks}
              onChange={(event) => setSocks(event.target.value)}
            />
          </Field>
          <Field>
            <Label htmlFor="local-http">{t('devices.fields.http')}</Label>
            <Input
              id="local-http"
              type="number"
              className="w-28 tabular-nums"
              value={http}
              onChange={(event) => setHttp(event.target.value)}
            />
          </Field>
          {dirty && (
            <Button type="submit" disabled={save.isPending}>
              {t('routing.save')}
            </Button>
          )}
        </form>
      )}

      {local.state === 'running' && (
        <p className="font-mono text-xs text-muted-foreground" translate="no">
          socks5://127.0.0.1:{local.socks_port} · http://127.0.0.1:{local.http_port}
        </p>
      )}
      {local.state === 'running' && local.checked_at !== null && !local.check_ok && (
        <Notice tone="warning" title={t('devices.local.checkFailed')}>
          {local.entry?.pending ? t('devices.local.checkPending') : local.check_error}
        </Notice>
      )}
      {local.state === 'failed' && (
        <Notice title={t(`devices.local.errors.${local.error}.title`)}>
          {t(`devices.local.errors.${local.error}.text`, {
            socks: local.socks_port,
            http: local.http_port,
          })}
        </Notice>
      )}
      {(save.isError || toggle.isError) && (
        <Notice title={explain(t, save.error ?? toggle.error)} />
      )}
    </Panel>
  )
}

function LocalBadge({ local }: { local: LocalClient }) {
  const { t } = useTranslation()
  if (local.state === 'failed') {
    return <Badge variant="destructive">{t('devices.local.state.failed')}</Badge>
  }
  if (local.state === 'off') {
    return <Badge variant="secondary">{t('devices.local.state.off')}</Badge>
  }
  if (local.checked_at !== null && local.check_ok) {
    return (
      <>
        <Badge variant="success">{t('devices.local.state.working')}</Badge>
        <span className="text-xs text-muted-foreground">
          {t('devices.local.checkedAt', { when: formatDayTime(local.checked_at) })}
        </span>
      </>
    )
  }
  return <Badge variant="secondary">{t('devices.local.state.running')}</Badge>
}

// --- remote devices ---------------------------------------------------------------

export function DevicesPanel({ devices, sets }: { devices: Device[]; sets: RuleSet[] }) {
  const { t } = useTranslation()
  // null: closed; 'new': adding; a number: the device with that id.
  const [open, setOpen] = useState<number | 'new' | null>(null)
  const update = useDevicesCache()
  const push = useMutation({
    mutationFn: (id: number) => api.pushDevice(id),
    onSuccess: update,
  })
  const current = typeof open === 'number' ? devices.find((device) => device.id === open) : undefined

  return (
    <Panel className="gap-3">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 flex-col gap-1">
          <h2 className="font-heading text-base font-semibold">{t('devices.title')}</h2>
          <p className="max-w-2xl text-muted-foreground">{t('devices.text')}</p>
        </div>
        <Button size="sm" disabled={sets.length === 0} onClick={() => setOpen('new')}>
          {t('devices.add')}
        </Button>
      </div>

      {devices.length === 0 ? (
        <p className="text-muted-foreground">
          {sets.length === 0 ? t('devices.needRuleSet') : t('devices.empty')}
        </p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('devices.columns.device')}</TableHead>
              <TableHead>{t('devices.columns.state')}</TableHead>
              <TableHead className="hidden md:table-cell">{t('devices.columns.ruleSet')}</TableHead>
              <TableHead className="hidden text-right lg:table-cell">
                {t('clients.columns.traffic')}
              </TableHead>
              <TableHead className="text-right">{t('devices.columns.action')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {devices.map((device) => (
              <TableRow key={device.id}>
                <TableCell>
                  <button
                    type="button"
                    className="font-medium text-primary underline-offset-4 hover:underline"
                    onClick={() => setOpen(device.id)}
                  >
                    {device.name}
                  </button>
                  <div className="font-mono text-xs text-muted-foreground" translate="no">
                    {[device.os_name, device.host].filter(Boolean).join(' · ')}
                  </div>
                </TableCell>
                <TableCell>
                  <DeviceState device={device} />
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  {device.rule_set_name}
                  <div className="text-xs text-muted-foreground">
                    {device.status !== 'ready'
                      ? ''
                      : device.pending
                        ? t('devices.sync.pending')
                        : t('devices.sync.synced')}
                  </div>
                </TableCell>
                <TableCell className="hidden text-right tabular-nums lg:table-cell">
                  {device.traffic.up + device.traffic.down === 0
                    ? '—'
                    : formatBytes(device.traffic.up + device.traffic.down)}
                </TableCell>
                <TableCell className="text-right">
                  {device.status === 'ready' && device.pending ? (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={push.isPending}
                      onClick={() => push.mutate(device.id)}
                    >
                      {t('devices.push')}
                    </Button>
                  ) : (
                    <Button variant="outline" size="sm" onClick={() => setOpen(device.id)}>
                      {t('servers.open')}
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {push.isError && <Notice title={explain(t, push.error)} />}

      {open === 'new' && <NewDeviceDialog sets={sets} onClose={() => setOpen(null)} />}
      {current && (
        <DeviceDialog key={current.id} device={current} sets={sets} onClose={() => setOpen(null)} />
      )}
    </Panel>
  )
}

function DeviceState({ device }: { device: Device }) {
  const { t } = useTranslation()
  const now = useNow()
  if (device.status === 'working') {
    return (
      <>
        <Badge variant="secondary">{t('devices.state.working')}</Badge>
        {device.step !== '' && (
          <div className="mt-1 text-xs text-muted-foreground">{t(`devices.steps.${device.step}`)}</div>
        )}
      </>
    )
  }
  if (device.status === 'failed' || device.status === 'new') {
    return <Badge variant="warning">{t(`devices.state.${device.status}`)}</Badge>
  }
  // Set up: what the last look at it found.
  const failed = device.failure !== null
  if (!device.online) {
    return (
      <>
        <Badge variant="destructive">{t('devices.state.offline')}</Badge>
        {device.seen_at !== null && (
          <div className="mt-1 text-xs text-destructive">
            {t('devices.offlineFor', {
              duration: formatDuration(Math.max(0, now - device.seen_at)),
            })}
          </div>
        )}
      </>
    )
  }
  return (
    <>
      {device.service === 'active' ? (
        <Badge variant="success">{t('devices.state.online')}</Badge>
      ) : (
        <Badge variant="destructive">{t('devices.state.stopped')}</Badge>
      )}
      {failed && <div className="mt-1 text-xs text-warning">{t('devices.pushFailed')}</div>}
    </>
  )
}

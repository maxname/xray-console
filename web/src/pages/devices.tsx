import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { Panel } from '@/components/page'
import { Badge } from '@/components/ui/badge'
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
  ApiError,
  type Device,
  type DeviceSettings,
  type DevicesResponse,
  type HostKeyProbe,
  type LocalClient,
  type RuleSet,
} from '@/lib/api'
import { devicesKey } from '@/lib/rules'
import { explained } from '@/lib/failures'
import { formatBytes, formatDayTime, formatDuration } from '@/lib/format'
import { toast } from '@/lib/toast'
import { useNow } from '@/lib/use-now'

type Translate = (key: string, options?: Record<string, unknown>) => string

const REFUSALS = [
  'name_taken',
  'invalid_name',
  'invalid_ports',
  'invalid_host',
  'invalid_port',
  'invalid_user',
  'bad_rule_set',
  'host_key_required',
  'busy',
] as const

function explain(t: Translate, error: unknown): string {
  if (error instanceof ApiError && (REFUSALS as readonly string[]).includes(error.code)) {
    return t(`devices.errors.${error.code}`)
  }
  return t('common.error')
}

function useDevicesCache() {
  const queryClient = useQueryClient()
  return (next: DevicesResponse) => {
    queryClient.setQueryData(devicesKey, next)
    // Devices and the local client are users of entry servers: what those
    // have waiting to be applied has just changed.
    void queryClient.invalidateQueries({ queryKey: ['rule-sets'] })
    void queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
  }
}

// --- the panel's own computer -----------------------------------------------------

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
          disabled={save.isPending || (sets.length === 0 && !local.enabled)}
          aria-label={t('devices.local.switch')}
          onCheckedChange={(enabled) => save.mutate(enabled)}
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
      {save.isError && <Notice title={explain(t, save.error)} />}
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

function SettingsFields({
  value,
  onChange,
  sets,
  prefix,
}: {
  value: DeviceSettings
  onChange: (change: Partial<DeviceSettings>) => void
  sets: RuleSet[]
  prefix: string
}) {
  const { t } = useTranslation()
  return (
    <>
      <Field hint={t('devices.fields.ruleSetHint')}>
        <Label htmlFor={`${prefix}-rule-set`}>{t('devices.fields.ruleSet')}</Label>
        <Select
          value={String(value.rule_set_id)}
          onValueChange={(next) => onChange({ rule_set_id: Number(next) })}
        >
          <SelectTrigger id={`${prefix}-rule-set`} className="w-full">
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
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <Label htmlFor={`${prefix}-socks`}>{t('devices.fields.socks')}</Label>
          <Input
            id={`${prefix}-socks`}
            type="number"
            className="tabular-nums"
            value={value.socks_port}
            onChange={(event) => onChange({ socks_port: Number(event.target.value) })}
          />
        </Field>
        <Field>
          <Label htmlFor={`${prefix}-http`}>{t('devices.fields.http')}</Label>
          <Input
            id={`${prefix}-http`}
            type="number"
            className="tabular-nums"
            value={value.http_port}
            onChange={(event) => onChange({ http_port: Number(event.target.value) })}
          />
        </Field>
      </div>
      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('devices.fields.lan')}</span>
          <span className="text-xs text-muted-foreground">{t('devices.fields.lanHint')}</span>
        </span>
        <Switch
          checked={value.lan}
          aria-label={t('devices.fields.lan')}
          onCheckedChange={(lan) => onChange({ lan })}
        />
      </label>
      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('devices.fields.autoPush')}</span>
          <span className="text-xs text-muted-foreground">{t('devices.fields.autoPushHint')}</span>
        </span>
        <Switch
          checked={value.auto_push}
          aria-label={t('devices.fields.autoPush')}
          onCheckedChange={(auto_push) => onChange({ auto_push })}
        />
      </label>
    </>
  )
}

function NewDeviceDialog({ sets, onClose }: { sets: RuleSet[]; onClose: () => void }) {
  const { t } = useTranslation()
  const update = useDevicesCache()
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [user, setUser] = useState('root')
  const [password, setPassword] = useState('')
  const [settings, setSettings] = useState<DeviceSettings>({
    name: '',
    rule_set_id: sets[0]?.id ?? 0,
    socks_port: 10808,
    http_port: 10809,
    lan: false,
    auto_push: true,
  })
  const [probe, setProbe] = useState<HostKeyProbe | null>(null)
  const [confirmed, setConfirmed] = useState(false)

  const fetchKey = useMutation({
    mutationFn: () => api.probeServer(host.trim(), Number(port)),
    onSuccess: (result) => {
      setProbe(result)
      setConfirmed(false)
    },
  })
  const create = useMutation({
    mutationFn: () =>
      api.createDevice({
        ...settings,
        name: settings.name.trim(),
        host: host.trim(),
        ssh_port: Number(port),
        ssh_user: user.trim(),
        host_key: probe?.host_key ?? '',
        password,
      }),
    onSuccess: (next) => {
      update(next)
      onClose()
    },
  })
  // A different address is a different machine: its key has to be seen anew.
  const retarget = (change: () => void) => {
    change()
    setProbe(null)
    setConfirmed(false)
    fetchKey.reset()
  }
  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    create.mutate()
  }
  const probeFailed =
    fetchKey.error instanceof ApiError && fetchKey.error.code === 'ssh_unreachable'
      ? t('devices.new.unreachable')
      : fetchKey.isError
        ? explain(t, fetchKey.error)
        : null

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>{t('devices.new.title')}</DialogTitle>
            <DialogDescription>{t('devices.new.description')}</DialogDescription>
          </DialogHeader>

          <Field>
            <Label htmlFor="device-name">{t('devices.fields.name')}</Label>
            <Input
              id="device-name"
              autoComplete="off"
              required
              placeholder={t('devices.fields.namePlaceholder')}
              value={settings.name}
              onChange={(event) => setSettings((current) => ({ ...current, name: event.target.value }))}
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_6rem_8rem]">
            <Field>
              <Label htmlFor="device-host">{t('devices.fields.host')}</Label>
              <Input
                id="device-host"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                required
                placeholder="192.168.1.1"
                value={host}
                onChange={(event) => retarget(() => setHost(event.target.value))}
              />
            </Field>
            <Field>
              <Label htmlFor="device-port">{t('devices.fields.sshPort')}</Label>
              <Input
                id="device-port"
                type="number"
                className="tabular-nums"
                required
                value={port}
                onChange={(event) => retarget(() => setPort(event.target.value))}
              />
            </Field>
            <Field>
              <Label htmlFor="device-user">{t('devices.fields.user')}</Label>
              <Input
                id="device-user"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                required
                value={user}
                onChange={(event) => setUser(event.target.value)}
              />
            </Field>
          </div>

          <div className="flex flex-col gap-2 rounded-lg border p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{t('devices.new.fingerprint')}</span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={host.trim() === '' || port.trim() === '' || fetchKey.isPending}
                onClick={() => fetchKey.mutate()}
              >
                {fetchKey.isPending ? t('serverNew.fingerprint.fetching') : t('serverNew.fingerprint.fetch')}
              </Button>
            </div>
            {probe === null ? (
              <span className="text-xs text-muted-foreground">{t('devices.new.fingerprintPrompt')}</span>
            ) : (
              <>
                <code className="font-mono text-xs break-all" translate="no">
                  {probe.fingerprint} · {probe.type}
                </code>
                <label className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={confirmed}
                    onChange={(event) => setConfirmed(event.target.checked)}
                  />
                  <span>{t('serverNew.fingerprint.confirm')}</span>
                </label>
              </>
            )}
            {probeFailed !== null && <Notice title={probeFailed} />}
          </div>

          <Field hint={t('devices.new.passwordHint')}>
            <Label htmlFor="device-password">{t('devices.new.password', { user })}</Label>
            <Input
              id="device-password"
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </Field>

          <SettingsFields
            value={settings}
            sets={sets}
            prefix="device-new"
            onChange={(change) => setSettings((current) => ({ ...current, ...change }))}
          />

          {create.isError && <Notice title={explain(t, create.error)} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button type="submit" disabled={probe === null || !confirmed || create.isPending}>
              {create.isPending ? t('devices.new.starting') : t('devices.new.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** Failure codes a device has its own words for; the rest are a server's. */
const OWN_ERRORS = [
  'port_in_use',
  'no_entry',
  'entry_pending',
  'entry_failed',
  'unsupported_os',
  'ssh_password_required',
  'ssh_unreachable',
] as const

function DeviceFailure({ device }: { device: Device }) {
  const { t } = useTranslation()
  const failure = device.failure
  if (failure === null) {
    return null
  }
  const details = failure.details ?? {}
  const own = (OWN_ERRORS as readonly string[]).includes(failure.code)
  const prefix = own ? `devices.failures.${failure.code}` : `server.errors.${explained(failure.code)}`
  const output = typeof details.output === 'string' ? details.output : ''
  // A device that already worked went back to what it had.
  const restored = device.status === 'ready' && details.restored === true
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2.5">
      <span className="font-semibold text-warning">
        {restored
          ? t('devices.failed.restored')
          : device.status === 'ready'
            ? t('devices.failed.push')
            : t('devices.failed.setup', { step: t(`devices.steps.${device.step}`, { defaultValue: '' }) })}
      </span>
      <span className="font-medium">{t(`${prefix}.title`, details)}</span>
      <span>{t(`${prefix}.text`, details)}</span>
      {(!own && explained(failure.code) === 'internal') || failure.code === 'ssh_unreachable' ? (
        <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
          {failure.message}
        </code>
      ) : null}
      {output !== '' && (
        <pre
          className="max-h-40 overflow-auto rounded-lg bg-muted p-2 font-mono text-xs whitespace-pre-wrap"
          translate="no"
          tabIndex={0}
        >
          {output}
        </pre>
      )}
    </div>
  )
}

function DeviceDialog({
  device,
  sets,
  onClose,
}: {
  device: Device
  sets: RuleSet[]
  onClose: () => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const update = useDevicesCache()
  const [settings, setSettings] = useState<DeviceSettings>({
    name: device.name,
    rule_set_id: device.rule_set_id,
    socks_port: device.socks_port,
    http_port: device.http_port,
    lan: device.lan,
    auto_push: device.auto_push,
  })
  const [password, setPassword] = useState('')
  const [deleting, setDeleting] = useState(false)
  const working = device.status === 'working'

  const save = useMutation({
    mutationFn: () => api.updateDevice(device.id, { ...settings, name: settings.name.trim() }),
    onSuccess: (next) => {
      update(next)
      onClose()
    },
  })
  const setup = useMutation({
    mutationFn: () => api.setupDevice(device.id, password),
    onSuccess: (next) => {
      setPassword('')
      update(next)
    },
  })
  const push = useMutation({ mutationFn: () => api.pushDevice(device.id), onSuccess: update })
  const remove = useMutation({
    mutationFn: () => api.deleteDevice(device.id),
    onSuccess: (result) => {
      toast(
        result.cleaned
          ? { title: t('devices.deleted', { name: device.name }), variant: 'success' }
          : {
              title: t('devices.deleted', { name: device.name }),
              description: t('devices.deletedNotCleaned'),
              duration: 0,
            },
      )
      void queryClient.invalidateQueries({ queryKey: devicesKey })
      void queryClient.invalidateQueries({ queryKey: ['rule-sets'] })
      onClose()
    },
  })
  const failure = save.error ?? setup.error ?? push.error ?? remove.error
  const needsPassword =
    device.failure?.code === 'ssh_password_required' || device.failure?.code === 'ssh_key_rejected'

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        <form
          className="grid gap-4 text-sm"
          onSubmit={(event) => {
            event.preventDefault()
            save.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t('devices.edit.title', { name: device.name })}</DialogTitle>
            <DialogDescription>
              <span className="font-mono" translate="no">
                {device.ssh_user}@{device.host}:{device.ssh_port}
              </span>
              {device.os_name !== '' && ` · ${device.os_name}`}
              {device.xray_version !== '' && ` · Xray ${device.xray_version}`}
            </DialogDescription>
          </DialogHeader>

          {working ? (
            <div role="status" className="rounded-lg bg-muted px-3 py-2.5">
              {t('devices.state.working')}
              {device.step !== '' && `: ${t(`devices.steps.${device.step}`)}`}…
            </div>
          ) : (
            <DeviceFailure device={device} />
          )}

          <Field>
            <Label htmlFor="device-edit-name">{t('devices.fields.name')}</Label>
            <Input
              id="device-edit-name"
              autoComplete="off"
              required
              value={settings.name}
              onChange={(event) => setSettings((current) => ({ ...current, name: event.target.value }))}
            />
          </Field>
          <SettingsFields
            value={settings}
            sets={sets}
            prefix="device-edit"
            onChange={(change) => setSettings((current) => ({ ...current, ...change }))}
          />

          {device.status !== 'ready' && !working && (
            <div className="flex flex-col gap-2 rounded-lg border p-3">
              {needsPassword && (
                <Field hint={t('devices.new.passwordHint')}>
                  <Label htmlFor="device-retry-password">
                    {t('devices.new.password', { user: device.ssh_user })}
                  </Label>
                  <Input
                    id="device-retry-password"
                    type="password"
                    autoComplete="new-password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                  />
                </Field>
              )}
              <Button
                type="button"
                className="self-start"
                disabled={setup.isPending}
                onClick={() => setup.mutate()}
              >
                {t('devices.edit.retry')}
              </Button>
              <span className="text-xs text-muted-foreground">{t('devices.edit.retryHint')}</span>
            </div>
          )}

          {device.log !== '' && (
            <details>
              <summary className="cursor-pointer text-primary">{t('devices.edit.log')}</summary>
              <pre
                className="mt-2 max-h-56 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
                translate="no"
                tabIndex={0}
              >
                {device.log}
              </pre>
            </details>
          )}

          {deleting && (
            <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2.5">
              <span>{t('devices.edit.deleteConfirm')}</span>
              <div className="flex gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant="destructive"
                  disabled={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  {remove.isPending ? t('devices.edit.deleting') : t('clients.editor.deleteYes')}
                </Button>
                <Button type="button" size="sm" variant="outline" onClick={() => setDeleting(false)}>
                  {t('common.cancel')}
                </Button>
              </div>
            </div>
          )}
          {failure !== null && <Notice title={explain(t, failure)} />}

          <DialogFooter className="sm:justify-between">
            <Button
              type="button"
              variant="destructive"
              disabled={working}
              onClick={() => setDeleting(true)}
            >
              {t('devices.edit.delete')}
            </Button>
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              {device.status === 'ready' && (
                <Button
                  type="button"
                  variant="outline"
                  disabled={working || push.isPending}
                  onClick={() => push.mutate()}
                >
                  {t('devices.push')}
                </Button>
              )}
              <Button type="submit" disabled={working || save.isPending}>
                {save.isPending ? t('routing.saving') : t('routing.save')}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

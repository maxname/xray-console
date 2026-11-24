import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { ConfirmStrip } from '@/components/confirm-strip'
import { FailureExplained } from '@/components/failure-explained'
import { CopyBlock } from '@/components/install-fields'
import { LogView, StepList } from '@/components/operation-view'
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
  api,
  ApiError,
  type Device,
  type DeviceSettings,
  type HostKeyProbe,
  type OperationFeed,
  type RuleSet,
} from '@/lib/api'
import { devicesKey } from '@/lib/rules'
import { explained } from '@/lib/failures'
import { toast } from '@/lib/toast'

import { explain, useDevicesCache, OWN_ERRORS } from '@/lib/devices'

export function SettingsFields({
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
      {value.lan && (
        <label className="flex items-center justify-between gap-4">
          <span className="flex flex-col">
            <span className="font-medium">{t('devices.fields.proxyAuth')}</span>
            <span className="text-xs text-muted-foreground">
              {t('devices.fields.proxyAuthHint')}
            </span>
          </span>
          <Switch
            checked={value.proxy_auth}
            aria-label={t('devices.fields.proxyAuth')}
            onCheckedChange={(proxy_auth) => onChange({ proxy_auth })}
          />
        </label>
      )}
      {/* Open to a network and asking nothing: said plainly, with what to do. */}
      {value.lan && !value.proxy_auth && (
        <Notice tone="warning" title={t('devices.fields.lanWarning')} />
      )}
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

export function NewDeviceDialog({ sets, onClose }: { sets: RuleSet[]; onClose: () => void }) {
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
    proxy_auth: false,
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

/**
 * The device's latest job, as the engine keeps it: its steps and what it
 * printed. Asked again every second for as long as the job runs.
 */
function useDeviceJob(device: Device): OperationFeed | null {
  const working = device.status === 'working'
  const query = useQuery({
    queryKey: ['operations', device.operation_id],
    queryFn: () => api.getOperation(device.operation_id as number),
    enabled: device.operation_id !== null,
    // Until the record itself says the job is over: the device's own state
    // may be a poll ahead of it, or behind.
    refetchInterval: (q) =>
      working || q.state.data?.operation.status === 'running' ? 1000 : false,
  })
  return device.operation_id === null ? null : (query.data ?? null)
}

/** The job's steps while it runs; afterwards steps and log, on request. */
function DeviceJob({ device, job }: { device: Device; job: OperationFeed | null }) {
  const { t } = useTranslation()
  const working = device.status === 'working'
  if (job === null) {
    return working ? (
      <div role="status" className="rounded-lg bg-muted px-3 py-2.5">
        {t('devices.state.working')}…
      </div>
    ) : null
  }
  const title = t(`devices.jobs.${job.operation.kind}`, { defaultValue: t('devices.state.working') })
  const steps = <StepList steps={job.operation.steps} label={title} names="devices.steps" />
  if (working) {
    return (
      <div role="status" className="flex flex-col gap-3 rounded-lg bg-muted px-3 py-2.5">
        <span className="font-medium">{title}</span>
        {steps}
      </div>
    )
  }
  return (
    <details>
      <summary className="cursor-pointer text-primary">{t('devices.edit.job')}</summary>
      <div className="mt-2 flex flex-col gap-3">
        <span className="font-medium">{title}</span>
        {steps}
        <LogView lines={job.log} follow={false} />
      </div>
    </details>
  )
}

export function DeviceFailure({ device }: { device: Device }) {
  const { t } = useTranslation()
  const failure = device.failure
  if (failure === null) {
    return null
  }
  const own = (OWN_ERRORS as readonly string[]).includes(failure.code)
  const prefix = own ? `devices.failures.${failure.code}` : `server.errors.${explained(failure.code)}`
  // A device that already worked went back to what it had.
  const restored = device.status === 'ready' && failure.details?.restored === true
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/5 px-3 py-2.5">
      <span className="font-semibold text-warning">
        {restored
          ? t('devices.failed.restored')
          : device.status === 'ready'
            ? t('devices.failed.push')
            : t('devices.failed.setup', { step: t(`devices.steps.${device.step}`, { defaultValue: '' }) })}
      </span>
      <FailureExplained
        failure={failure}
        prefix={prefix}
        tellMessage={
          (!own && explained(failure.code) === 'internal') || failure.code === 'ssh_unreachable'
        }
        compact
      />
    </div>
  )
}

export function DeviceDialog({
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
    proxy_auth: device.proxy_auth,
    auto_push: device.auto_push,
  })
  const [password, setPassword] = useState('')
  const [deleting, setDeleting] = useState(false)
  const [undoing, setUndoing] = useState(false)
  const working = device.status === 'working'
  const job = useDeviceJob(device)

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
  const retry = useMutation({
    mutationFn: () => api.retryDevice(device.id, password),
    onSuccess: (next) => {
      setPassword('')
      update(next)
    },
  })
  const rollback = useMutation({
    mutationFn: () => api.rollbackDevice(device.id),
    onSuccess: (next) => {
      setUndoing(false)
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
  const failure =
    save.error ?? setup.error ?? retry.error ?? rollback.error ?? push.error ?? remove.error
  const starting = setup.isPending || retry.isPending || rollback.isPending
  // Only a set-up puts things on the device that can be taken off again; a
  // push that fails puts the previous settings back by itself.
  const canUndo = device.failure !== null && job?.operation.kind === 'device_setup'
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

          {!working && <DeviceFailure device={device} />}
          <DeviceJob device={device} job={job} />

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

          {/* What the saved settings ask for — not what is being typed above. */}
          {device.proxy_user !== undefined && device.proxy_password !== undefined && (
            <div className="flex flex-col gap-2 rounded-lg border p-3">
              <span className="font-medium">{t('devices.fields.proxyAccess')}</span>
              <span className="text-xs text-muted-foreground">
                {t('devices.fields.proxyAccessHint')}
              </span>
              <div className="grid gap-2 sm:grid-cols-2">
                <Field>
                  <span className="text-xs text-muted-foreground">
                    {t('devices.fields.proxyUser')}
                  </span>
                  <CopyBlock text={device.proxy_user} />
                </Field>
                <Field>
                  <span className="text-xs text-muted-foreground">
                    {t('devices.fields.proxyPassword')}
                  </span>
                  <CopyBlock text={device.proxy_password} />
                </Field>
              </div>
              {device.pending && (
                <span className="text-xs text-muted-foreground">
                  {t('devices.fields.proxyAccessPending')}
                </span>
              )}
            </div>
          )}

          {!working && (device.failure !== null || device.status !== 'ready') && (
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
              <div className="flex flex-wrap gap-2">
                {device.failure !== null && (
                  <Button type="button" disabled={starting} onClick={() => retry.mutate()}>
                    {t('devices.edit.retryStep', {
                      step: t(`devices.steps.${device.step}`, { defaultValue: '' }),
                    })}
                  </Button>
                )}
                {device.status !== 'ready' && (
                  <Button
                    type="button"
                    variant={device.failure === null ? 'default' : 'outline'}
                    disabled={starting}
                    onClick={() => setup.mutate()}
                  >
                    {device.failure === null ? t('devices.edit.setup') : t('devices.edit.retry')}
                  </Button>
                )}
                {canUndo && (
                  <Button
                    type="button"
                    variant="outline"
                    disabled={starting}
                    onClick={() => setUndoing(true)}
                  >
                    {t('devices.edit.rollback')}
                  </Button>
                )}
              </div>
              <span className="text-xs text-muted-foreground">
                {device.failure !== null
                  ? t('devices.edit.retryStepHint')
                  : t('devices.edit.retryHint')}
              </span>
              {undoing && (
                <ConfirmStrip
                  text={t('devices.edit.rollbackConfirm')}
                  confirm={t('devices.edit.rollbackYes')}
                  pending={rollback.isPending}
                  onConfirm={() => rollback.mutate()}
                  onCancel={() => setUndoing(false)}
                />
              )}
            </div>
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

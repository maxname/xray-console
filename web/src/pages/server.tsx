import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Check, Loader2, Undo2, X } from 'lucide-react'
import { useState, type FormEvent, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import {
  AccessFields,
  CopyBlock,
  MaskField,
  TemplateField,
} from '@/components/install-fields'
import { Page, PageHeader, Panel } from '@/components/page'
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
  api,
  ApiError,
  type LogLine,
  type Operation,
  type OperationFeed,
  type Server,
  type ServerChange,
  type StepState,
} from '@/lib/api'
import {
  initialInstallForm,
  isKnownFormError,
  toInstallOptions,
} from '@/lib/install-form'
import { explained } from '@/lib/failures'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

export function Breadcrumb({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  return (
    <>
      <Link to="/servers" className="text-primary underline-offset-4 hover:underline">
        {t('server.breadcrumb')}
      </Link>
      {' / '}
      {children}
    </>
  )
}

/** Refetch everything a job on this server can have changed. */
function useRefreshServer(server: Server) {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: ['servers'] })
    void queryClient.invalidateQueries({ queryKey: ['operations', server.operation_id] })
  }
}

// --- installation in progress, or stopped on a failure ----------------------------

export function InstallView({ server, feed }: { server: Server; feed: OperationFeed }) {
  const { t } = useTranslation()
  const refresh = useRefreshServer(server)
  const { operation, log } = feed
  const running = operation.status === 'running'
  const failed = operation.status === 'failed' && operation.error !== null

  const cancel = useMutation({
    mutationFn: () => api.cancelOperation(operation.id),
    onSettled: refresh,
  })

  const current = operation.steps.findIndex(
    (step) => step.status === 'running' || step.status === 'failed',
  )
  const position =
    current >= 0
      ? current + 1
      : operation.steps.filter((step) => step.status === 'done').length

  return (
    <Page>
      <PageHeader
        title={failed ? t('server.failed.title') : server.name}
        description={
          <Breadcrumb>
            {server.name}
            {failed && operation.error?.step
              ? ` · ${t('server.failed.subtitle', {
                  step: t(`server.steps.${operation.error.step}`),
                })}`
              : ''}
          </Breadcrumb>
        }
        actions={
          <>
            {running && (
              <Button
                variant="outline"
                onClick={() => cancel.mutate()}
                disabled={cancel.isPending}
              >
                {cancel.isPending
                  ? t('server.installing.stopping')
                  : t('server.installing.stop')}
              </Button>
            )}
            <Button asChild variant="outline">
              <Link to="/servers">{t('server.toList')}</Link>
            </Button>
          </>
        }
      />

      {failed && <FailurePanel server={server} operation={operation} />}

      <div className="grid gap-5 lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
        <Panel title={t('server.installing.title')}>
          <p className="text-muted-foreground">
            {t('server.installing.progress', {
              current: Math.max(position, 1),
              total: operation.steps.length,
            })}
          </p>
          <StepList steps={operation.steps} />
        </Panel>
        <Panel title={t('server.log.title')}>
          <LogView lines={log} follow={running} />
        </Panel>
      </div>
    </Page>
  )
}

export function StepList({ steps, label }: { steps: StepState[]; label?: string }) {
  const { t } = useTranslation()
  return (
    <ol className="flex flex-col gap-3" aria-label={label ?? t('server.installing.title')}>
      {steps.map((step) => (
        <li key={step.id} className="flex items-start gap-3">
          <StepMark status={step.status} />
          <div className="flex min-w-0 flex-col">
            <span
              className={cn(
                step.status === 'running' && 'font-semibold',
                (step.status === 'pending' || step.status === 'undone') &&
                  'text-muted-foreground',
              )}
            >
              {t(`server.steps.${step.id}`)}
              <span className="sr-only"> — {t(`server.stepStatus.${step.status}`)}</span>
            </span>
            {step.detail !== '' && (
              <span className="font-mono text-xs break-all text-muted-foreground">
                {step.detail}
              </span>
            )}
          </div>
        </li>
      ))}
    </ol>
  )
}

function StepMark({ status }: { status: StepState['status'] }) {
  const base =
    'mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border [&_svg]:size-3'
  switch (status) {
    case 'done':
      return (
        <span className={cn(base, 'border-primary bg-primary text-primary-foreground')}>
          <Check aria-hidden="true" strokeWidth={3} />
        </span>
      )
    case 'running':
      return (
        <span className={cn(base, 'border-primary text-primary')}>
          <Loader2 aria-hidden="true" className="animate-spin" />
        </span>
      )
    case 'failed':
      return (
        <span className={cn(base, 'border-destructive bg-destructive/10 text-destructive')}>
          <X aria-hidden="true" strokeWidth={3} />
        </span>
      )
    case 'undone':
      return (
        <span className={cn(base, 'border-input text-muted-foreground')}>
          <Undo2 aria-hidden="true" />
        </span>
      )
    default:
      return <span className={cn(base, 'border-input')} />
  }
}

export function LogView({ lines, follow }: { lines: LogLine[]; follow: boolean }) {
  const { t } = useTranslation()

  if (lines.length === 0) {
    return <p className="text-muted-foreground">{t('server.log.empty')}</p>
  }
  const text = lines
    .map((line) => (line.kind === 'cmd' ? `$ ${line.text}` : line.text))
    .join('\n')

  return (
    <>
      <pre
        // While the job runs, keep the newest line in view: the callback ref
        // re-runs on every render that adds lines.
        ref={(node) => {
          if (node && follow) {
            node.scrollTop = node.scrollHeight
          }
        }}
        className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5 whitespace-pre-wrap"
        aria-label={t('server.log.title')}
        translate="no"
        tabIndex={0}
      >
        {lines.map((line) => (
          <span
            key={line.seq}
            className={cn(
              'block break-all',
              line.kind === 'cmd' && 'text-foreground',
              line.kind === 'out' && 'text-muted-foreground',
              line.kind === 'info' && 'text-info',
              line.kind === 'error' && 'text-destructive',
            )}
          >
            {line.kind === 'cmd' ? `$ ${line.text}` : line.text}
          </span>
        ))}
      </pre>
      <CopyLogButton text={text} />
    </>
  )
}

function CopyLogButton({ text }: { text: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="self-start"
      onClick={() => {
        void navigator.clipboard.writeText(text).then(() => {
          setCopied(true)
          setTimeout(() => setCopied(false), 2000)
        })
      }}
    >
      {copied ? t('common.copied') : t('server.log.copy')}
    </Button>
  )
}

function detail(details: Record<string, unknown>, key: string): string {
  const value = details[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}

function FailurePanel({ server, operation }: { server: Server; operation: Operation }) {
  const { t } = useTranslation()
  const refresh = useRefreshServer(server)
  const [password, setPassword] = useState('')

  const failure = operation.error!
  const code = explained(failure.code)
  const details = failure.details ?? {}
  // Any step already undone means this failure happened on the way back.
  const duringRollback = operation.steps.some((step) => step.status === 'undone')
  const stepNumber = operation.steps.findIndex((step) => step.id === failure.step) + 1
  const needsPassword =
    code === 'ssh_password_rejected' || code === 'ssh_password_required'
  const hostKey = code === 'ssh_host_key_changed' ? detail(details, 'host_key') : ''
  const output = detail(details, 'output')

  const retry = useMutation({
    mutationFn: async () => {
      if (hostKey !== '') {
        await api.acceptHostKey(server.id, hostKey)
      }
      await api.retryOperation(operation.id, needsPassword ? password : undefined)
    },
    onSettled: refresh,
  })
  const rollback = useMutation({
    mutationFn: () => api.rollbackOperation(operation.id),
    onSettled: refresh,
  })
  const busy = retry.isPending || rollback.isPending

  const handleRetry = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    retry.mutate()
  }

  return (
    <Panel className="border border-warning/40 bg-warning/5">
      <div className="flex flex-col gap-1">
        <h2 className="font-heading text-base font-semibold text-warning">
          {duringRollback
            ? t('server.failed.rollbackFailedTitle')
            : t('server.failed.pausedTitle', { number: stepNumber })}
        </h2>
        <p>
          {duringRollback
            ? t('server.failed.rollbackFailed', {
                step: failure.step ? t(`server.steps.${failure.step}`) : '',
              })
            : t('server.failed.paused')}
        </p>
      </div>

      <div className="flex flex-col gap-1.5">
        <h3 className="font-medium">{t('server.failed.whatHappened')}</h3>
        <p className="font-semibold">{t(`server.errors.${code}.title`, details)}</p>
        <p>{t(`server.errors.${code}.text`, details)}</p>
        {code === 'port_in_use' && detail(details, 'process') !== '' && (
          <p>{t('server.errors.port_in_use.process', details)}</p>
        )}
        {code === 'ssh_host_key_changed' && (
          <code className="font-mono text-xs break-all" translate="no">
            {t('server.errors.ssh_host_key_changed.was', details)}
          </code>
        )}
        {code === 'ssh_key_rejected' && (
          <CopyBlock text={detail(details, 'public_key')} />
        )}
        {(code === 'internal' || code === 'ssh_unreachable') && failure.message !== '' && (
          <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
            {failure.message}
          </code>
        )}
        {output !== '' && (
          <details>
            <summary className="cursor-pointer text-primary">
              {t('server.failed.output')}
            </summary>
            <pre
              className="mt-2 max-h-60 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
              translate="no"
            >
              {output}
            </pre>
          </details>
        )}
      </div>

      <form className="flex flex-col gap-3" onSubmit={handleRetry}>
        <h3 className="font-medium">{t('server.failed.whatToDo')}</h3>
        {needsPassword && !duringRollback && (
          <Field>
            <Label htmlFor="retry-password">
              {t('server.failed.password', { user: server.ssh_user })}
            </Label>
            <Input
              id="retry-password"
              className="max-w-sm"
              type="password"
              autoComplete="new-password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
          </Field>
        )}
        <div className="flex flex-wrap gap-x-6 gap-y-3">
          {!duringRollback && (
            <div className="flex max-w-xs flex-col gap-1">
              <Button type="submit" className="self-start" disabled={busy}>
                {retry.isPending
                  ? t('server.failed.retrying')
                  : hostKey !== ''
                    ? t('server.failed.acceptHostKey')
                    : t('server.failed.retry')}
              </Button>
              <span className="text-xs text-muted-foreground">
                {t('server.failed.retryHint')}
              </span>
            </div>
          )}
          <div className="flex max-w-xs flex-col gap-1">
            <Button
              type="button"
              variant={duringRollback ? 'default' : 'outline'}
              className="self-start"
              disabled={busy}
              onClick={() => rollback.mutate()}
            >
              {t('server.failed.rollback')}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t('server.failed.rollbackHint')}
            </span>
          </div>
        </div>
        {(retry.isError || rollback.isError) && (
          <Notice title={t('server.failed.actionFailed')} />
        )}
      </form>
    </Panel>
  )
}

// --- nothing installed: a server added earlier, or rolled back ---------------------

export function FreshView({
  server,
  lastOperation,
}: {
  server: Server
  lastOperation: Operation | null
}) {
  const { t } = useTranslation()
  const refresh = useRefreshServer(server)
  const [install, setInstall] = useState(() => ({
    ...initialInstallForm,
    maskHost: lastOperation?.params.mask_host ?? '',
  }))
  const [deleting, setDeleting] = useState(false)

  const start = useMutation({
    mutationFn: () => api.installServer(server.id, toInstallOptions(install)),
    onSuccess: refresh,
  })

  const error = (() => {
    if (!start.isError) {
      return null
    }
    if (start.error instanceof ApiError && isKnownFormError(start.error.code)) {
      return t(`serverNew.errors.${start.error.code}`)
    }
    return t('common.error')
  })()

  return (
    <Page>
      <PageHeader
        title={server.name}
        description={<Breadcrumb>{t('server.fresh.title')}</Breadcrumb>}
        actions={
          <Button variant="outline" onClick={() => setDeleting(true)}>
            {t('server.delete')}
          </Button>
        }
      />
      <p className="max-w-3xl rounded-lg bg-muted px-3 py-2">
        {lastOperation?.status === 'rolled_back'
          ? t('server.fresh.rolledBack')
          : t('server.fresh.description')}
      </p>
      <form
        className="flex max-w-3xl flex-col gap-5"
        onSubmit={(event) => {
          event.preventDefault()
          start.mutate()
        }}
      >
        <Panel title={`${server.ssh_user}@${server.host}:${server.ssh_port}`}>
          <AccessFields value={install} onChange={setInstall} user={server.ssh_user} />
        </Panel>
        <Panel title={t('installForm.maskTitle')}>
          <TemplateField value={install} onChange={setInstall} />
          <MaskField value={install} onChange={setInstall} />
        </Panel>
        {error !== null && <Notice title={error} />}
        <Button type="submit" className="self-start" disabled={start.isPending}>
          {start.isPending ? t('serverNew.submitting') : t('server.fresh.submit')}
        </Button>
      </form>
      <DeleteDialog server={server} open={deleting} onOpenChange={setDeleting} />
    </Page>
  )
}

// --- installed ---------------------------------------------------------------------

/** What the installation did: its checks, what it put on the server, its log. */
export function InstallSummary({
  server,
  feed,
}: {
  server: Server
  feed: OperationFeed | null
}) {
  const { t } = useTranslation()
  const [deleting, setDeleting] = useState(false)
  const [showLog, setShowLog] = useState(false)
  const result = feed?.operation.result ?? {}
  const inbound = server.inbound

  const handshake = result.handshake_ms
  const httpStatus = result.http_status
  const duration = feed ? formatDuration(feed.operation, t) : null

  return (
    <>
      <Panel>
        <div className="flex flex-col gap-1">
          <h2 className="font-heading text-lg font-semibold">
            {duration
              ? t('server.done.headingTimed', { name: server.name, duration })
              : t('server.done.heading', { name: server.name })}
          </h2>
          <p className="text-muted-foreground">{t('server.done.description')}</p>
        </div>
        <div className="grid gap-4 sm:grid-cols-3">
          {typeof handshake === 'number' && (
            <Tile
              label={t('server.done.handshake')}
              value={t('server.done.handshakeValue', { ms: handshake })}
              note={t('server.done.handshakeNote')}
            />
          )}
          {typeof httpStatus === 'number' && inbound && (
            <Tile
              label={t('server.done.request')}
              value={t('server.done.requestValue', { status: httpStatus })}
              note={t('server.done.requestNote', {
                mode: inbound.xhttp_mode,
                path: inbound.xhttp_path,
              })}
            />
          )}
          <Tile
            label={t('server.done.autostart')}
            value={t('server.done.autostartValue')}
            note={t('server.done.autostartNote')}
          />
        </div>
      </Panel>

      <div className="grid gap-5 lg:grid-cols-2">
        <Panel title={t('server.done.installed')}>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5">
            <Fact label={t('server.done.xray')} mono>
              {server.xray_version}
            </Fact>
            <Fact label={t('server.done.system')}>
              {[server.os_name, server.arch].filter(Boolean).join(' · ')}
            </Fact>
            {inbound && (
              <>
                <Fact label={t('server.done.maskHost')} mono>
                  {inbound.mask_host}
                </Fact>
                <Fact label={t('server.done.port')} mono>
                  {inbound.port}
                </Fact>
                <Fact label={t('server.done.publicKey')} mono>
                  {inbound.public_key}
                </Fact>
                <Fact label={t('server.done.shortIds')}>
                  {t('server.done.shortIdsValue', { count: inbound.short_ids })}
                </Fact>
              </>
            )}
            <Fact label={t('server.done.passwordLogin')}>
              {server.manifest.some((change) => change.kind === 'ssh_password')
                ? t('server.done.passwordLoginOff')
                : t('server.done.passwordLoginOn')}
            </Fact>
            <Fact label={t('server.done.hostKey')} mono>
              {server.host_key_fingerprint}
            </Fact>
          </dl>
        </Panel>

        <Panel title={t('server.done.changed')}>
          <p className="text-muted-foreground">{t('server.done.changedHint')}</p>
          <ChangeList changes={server.manifest} />
        </Panel>
      </div>

      {feed && (
        <Panel>
          <Button
            variant="outline"
            size="sm"
            className="self-start"
            onClick={() => setShowLog((shown) => !shown)}
            aria-expanded={showLog}
          >
            {showLog ? t('server.log.hide') : t('server.log.show')}
          </Button>
          {showLog && <LogView lines={feed.log} follow={false} />}
        </Panel>
      )}

      <Button
        variant="destructive"
        className="self-start"
        onClick={() => setDeleting(true)}
      >
        {t('server.delete')}
      </Button>
      <DeleteDialog server={server} open={deleting} onOpenChange={setDeleting} />
    </>
  )
}

function formatDuration(
  operation: Operation,
  t: (key: string, options?: Record<string, unknown>) => string,
): string | null {
  if (operation.finished_at === null) {
    return null
  }
  const total = operation.finished_at - operation.started_at
  // A retried installation spans the pause too; its "duration" would mislead.
  if (total <= 0 || total > 3600) {
    return null
  }
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  return minutes > 0
    ? t('server.done.minutes', { m: minutes, s: seconds })
    : t('server.done.seconds', { s: seconds })
}

function Tile({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-lg border p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="font-heading text-xl font-semibold">{value}</span>
      <span className="text-xs break-all text-muted-foreground">{note}</span>
    </div>
  )
}

function Fact({
  label,
  mono = false,
  children,
}: {
  label: string
  mono?: boolean
  children: ReactNode
}) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className={cn('min-w-0 break-all', mono && 'font-mono')}>{children}</dd>
    </>
  )
}

export function ChangeList({ changes }: { changes: ServerChange[] }) {
  const { t } = useTranslation()
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5">
      {changes.map((change) => (
        <div key={`${change.kind}:${change.text}`} className="contents">
          <dt className="text-muted-foreground">
            {t(`server.done.changeKinds.${change.kind}`)}
          </dt>
          <dd className="min-w-0 font-mono break-all">{change.text}</dd>
        </div>
      ))}
    </dl>
  )
}

// --- deleting ----------------------------------------------------------------------

export function DeleteDialog({
  server,
  open,
  onOpenChange,
}: {
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [typed, setTyped] = useState('')

  const hasAccess = server.manifest.some(
    (change) => change.kind === 'ssh_key' || change.kind === 'ssh_password',
  )

  const remove = useMutation({
    mutationFn: () => api.deleteServer(server.id, typed),
    onSuccess: (response) => {
      toast(
        response.access_removed
          ? {
              title: t('server.deleteDialog.done', { name: server.name }),
              variant: 'success',
            }
          : {
              title: t('server.deleteDialog.done', { name: server.name }),
              description: t('server.deleteDialog.accessLeft'),
              duration: 0,
            },
      )
      queryClient.removeQueries({ queryKey: ['servers', server.id] })
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
      void navigate({ to: '/servers' })
    },
  })

  const error = (() => {
    if (!remove.isError) {
      return null
    }
    if (remove.error instanceof ApiError && remove.error.code === 'busy') {
      return t('server.deleteDialog.busy')
    }
    if (remove.error instanceof ApiError && remove.error.code === 'server_in_chains') {
      const chains = remove.error.details.chains
      return t('server.deleteDialog.inChains', {
        chains: Array.isArray(chains) ? chains.join(', ') : '',
      })
    }
    if (remove.error instanceof ApiError && remove.error.code === 'server_in_rule_sets') {
      const sets = remove.error.details.rule_sets
      return t('server.deleteDialog.inRuleSets', {
        sets: Array.isArray(sets) ? sets.join(', ') : '',
      })
    }
    return t('common.error')
  })()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            remove.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t('server.deleteDialog.title', { name: server.name })}</DialogTitle>
            <DialogDescription>
              {server.status === 'ready'
                ? t('server.deleteDialog.description')
                : t('server.deleteDialog.descriptionFresh')}
              {hasAccess && ` ${t('server.deleteDialog.access')}`}
            </DialogDescription>
          </DialogHeader>
          <Field>
            <Label htmlFor="delete-server-name">{t('server.deleteDialog.confirm')}</Label>
            <Input
              id="delete-server-name"
              className="font-mono"
              placeholder={server.name}
              autoComplete="off"
              spellCheck={false}
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
            />
          </Field>
          {error !== null && <Notice title={error} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              variant="destructive"
              disabled={typed !== server.name || remove.isPending}
            >
              {remove.isPending
                ? t('server.deleteDialog.submitting')
                : t('server.deleteDialog.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

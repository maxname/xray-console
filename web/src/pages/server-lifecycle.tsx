import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { FailureExplained } from '@/components/failure-explained'
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
  type OnError,
  type Operation,
  type OperationFeed,
  type Server,
} from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'
import { aftermath, explained } from '@/lib/failures'
import { toast } from '@/lib/toast'
import { releaseKey, useRelease } from '@/lib/use-release'
import { cn } from '@/lib/utils'
import { Breadcrumb, ChangeList } from '@/components/server-parts'
import { DeleteDialog } from '@/pages/server-delete'
import { LogView, StepList } from '@/components/operation-view'

/** What a failed job says went wrong, in the words the installation uses. */
function FailureText({ operation }: { operation: Operation }) {
  const failure = operation.error
  if (failure === null) {
    return null
  }
  const code = explained(failure.code)
  return (
    <FailureExplained
      failure={failure}
      prefix={`server.errors.${code}`}
      tellMessage={code === 'internal' || code === 'ssh_unreachable'}
    />
  )
}

// --- updating Xray ----------------------------------------------------------------

const UPDATE_REFUSALS = ['busy', 'up_to_date', 'release_unknown', 'not_installed'] as const

function explainUpdate(t: Translate, error: unknown): string {
  return explainKnown(t, error, 'update', UPDATE_REFUSALS)
}

/**
 * Offers the newest release when the server is behind it; nothing otherwise,
 * and nothing while a job is already running there.
 */
export function UpdateButton({ server, busy }: { server: Server; busy: boolean }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const release = useRelease()
  const [open, setOpen] = useState(false)

  const update = useMutation({
    mutationFn: () => api.updateXray(server.id),
    onSuccess: () => {
      setOpen(false)
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    },
  })

  const latest = release.data?.latest ?? ''
  if (busy || latest === '' || !release.data?.outdated.some((item) => item.id === server.id)) {
    return null
  }
  return (
    <>
      <Button onClick={() => setOpen(true)}>{t('update.button', { version: latest })}</Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="text-sm">
          <DialogHeader>
            <DialogTitle>{t('update.dialog.title', { name: server.name })}</DialogTitle>
            <DialogDescription>
              {t('update.dialog.versions', { from: server.xray_version, to: latest })}
            </DialogDescription>
          </DialogHeader>
          <p>{t('update.dialog.text')}</p>
          {update.isError && <Notice title={explainUpdate(t, update.error)} />}
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button disabled={update.isPending} onClick={() => update.mutate()}>
              {update.isPending ? t('update.dialog.starting') : t('update.dialog.submit')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

/** An update of this server: its steps while it runs, what went wrong if it failed. */
export function UpdateStatus({ server, feed }: { server: Server; feed: OperationFeed | null }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [dismissed, setDismissed] = useState<number | null>(null)
  const operation = feed?.operation.kind === 'update' ? feed.operation : null

  // When an update ends, the server's version and who is behind have changed.
  const lastStatus = useRef(operation?.status)
  useEffect(() => {
    const previous = lastStatus.current
    lastStatus.current = operation?.status
    if (previous === operation?.status || previous !== 'running') {
      return
    }
    void queryClient.invalidateQueries({ queryKey: ['servers'] })
    void queryClient.invalidateQueries({ queryKey: releaseKey })
    if (operation?.status === 'succeeded') {
      toast({
        title: t('update.done', { name: server.name, version: operation.params.to ?? '' }),
        variant: 'success',
      })
    }
  }, [operation?.status, operation?.params.to, queryClient, server.name, t])

  if (operation === null) {
    return null
  }
  const versions = { from: operation.params.from ?? '', to: operation.params.to ?? '' }

  if (operation.status === 'running') {
    return (
      <Panel title={t('update.running', versions)}>
        <StepList steps={operation.steps} label={t('update.running', versions)} />
      </Panel>
    )
  }
  if (operation.status !== 'failed' || operation.error === null || dismissed === operation.id) {
    return null
  }
  // Only what the job itself reported: see aftermath.
  const left = aftermath(operation.error.details)
  return (
    <Panel className="border border-destructive/40 bg-destructive/5">
      <div className="flex flex-col gap-1">
        <h2 className="font-heading text-base font-semibold text-destructive">
          {t(`update.failed.title.${left}`)}
        </h2>
        <p>{t(`update.failed.${left}`, { name: server.name, ...versions })}</p>
      </div>
      <FailureText operation={operation} />
      <Button variant="outline" className="self-start" onClick={() => setDismissed(operation.id)}>
        {t('update.failed.dismiss')}
      </Button>
    </Panel>
  )
}

/** On the servers list: the newest release, who is behind, and "update all". */
export function ReleaseBanner() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const release = useRelease()
  const [open, setOpen] = useState(false)
  const [onError, setOnError] = useState<OnError>('stop')

  const updateAll = useMutation({
    mutationFn: () => api.updateAllXray(onError),
    onSuccess: (next) => {
      queryClient.setQueryData(releaseKey, next)
      setOpen(false)
    },
  })

  // When the batch ends the list's versions are stale.
  const running = release.data?.batch?.running ?? false
  const wasRunning = useRef(running)
  useEffect(() => {
    if (wasRunning.current && !running) {
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    }
    wasRunning.current = running
  }, [running, queryClient])

  const data = release.data
  if (!data) {
    return null
  }
  const batch = data.batch
  if (batch?.running) {
    return (
      <div role="status" className="rounded-xl bg-muted px-4 py-3 text-sm">
        {t('update.batch.running', {
          done: batch.done,
          total: batch.total,
          current: batch.current,
        })}
      </div>
    )
  }
  const failed = batch?.failed ?? []
  if (data.outdated.length === 0 && failed.length === 0) {
    return null
  }
  return (
    <>
      <div
        role="status"
        className={cn(
          'flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-xl border px-4 py-3 text-sm',
          failed.length > 0 ? 'border-destructive/40 bg-destructive/5' : 'border-primary/30 bg-primary/5',
        )}
      >
        <span className="flex min-w-0 flex-1 basis-80 flex-col gap-0.5">
          {failed.length > 0 && (
            <span className="font-medium text-destructive">
              {t('update.batch.failed', { servers: failed.join(', ') })}
            </span>
          )}
          {data.outdated.length > 0 && (
            <span>
              {t('update.available', { version: data.latest, count: data.outdated.length })}
            </span>
          )}
        </span>
        {data.outdated.length > 0 && (
          <Button size="sm" onClick={() => setOpen(true)}>
            {t('update.all.button')}
          </Button>
        )}
      </div>

      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="text-sm">
          <DialogHeader>
            <DialogTitle>{t('update.all.title', { version: data.latest })}</DialogTitle>
            <DialogDescription>{t('update.all.text')}</DialogDescription>
          </DialogHeader>
          <ul className="flex flex-col gap-1">
            {data.outdated.map((item) => (
              <li key={item.id} className="flex justify-between gap-4">
                <span className="font-medium">{item.name}</span>
                <span className="font-mono text-xs text-muted-foreground" translate="no">
                  {item.version} → {data.latest}
                </span>
              </li>
            ))}
          </ul>
          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-2 font-medium">{t('update.all.onError')}</legend>
            {(['stop', 'continue'] as const).map((choice) => (
              <label key={choice} className="flex cursor-pointer items-start gap-2">
                <input
                  type="radio"
                  name="update-on-error"
                  className="mt-1 size-4 accent-primary"
                  checked={onError === choice}
                  onChange={() => setOnError(choice)}
                />
                <span className="flex flex-col">
                  <span>{t(`update.all.${choice}`)}</span>
                  <span className="text-xs text-muted-foreground">
                    {t(`update.all.${choice}Hint`)}
                  </span>
                </span>
              </label>
            ))}
          </fieldset>
          {updateAll.isError && <Notice title={explainUpdate(t, updateAll.error)} />}
          <DialogFooter>
            <Button variant="outline" onClick={() => setOpen(false)}>
              {t('common.cancel')}
            </Button>
            <Button disabled={updateAll.isPending} onClick={() => updateAll.mutate()}>
              {updateAll.isPending ? t('update.dialog.starting') : t('update.all.submit')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}

// --- removing Xray ----------------------------------------------------------------

/** Where the copies of earlier configurations are kept on a server. */
const BACKUP_DIR = '/root/xray-console-backup'

export function UninstallDialog({
  server,
  open,
  onOpenChange,
}: {
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        {/* The form lives only while the dialog is open: a name typed and then
            cancelled must not be waiting there, button armed, the next time. */}
        <UninstallForm server={server} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function UninstallForm({ server, onClose }: { server: Server; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [typed, setTyped] = useState('')
  const [removeBackups, setRemoveBackups] = useState(false)

  const start = useMutation({
    mutationFn: () => api.uninstallServer(server.id, { name: typed, remove_backups: removeBackups }),
    onSuccess: () => {
      onClose()
      // The server's page becomes the removal's page.
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    },
  })

  const error = (() => {
    if (!start.isError) {
      return null
    }
    if (start.error instanceof ApiError) {
      const details = start.error.details
      const names = (key: string) => {
        const value = details[key]
        return Array.isArray(value) ? value.join(', ') : ''
      }
      switch (start.error.code) {
        case 'busy':
          return t('server.deleteDialog.busy')
        case 'server_in_chains':
          return t('server.deleteDialog.inChains', { chains: names('chains') })
        case 'server_in_rule_sets':
          return t('server.deleteDialog.inRuleSets', { sets: names('rule_sets') })
        case 'wrong_state':
          return t('uninstall.dialog.wrongState')
      }
    }
    return t('common.error')
  })()

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    start.mutate()
  }

  return (
    <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
      <DialogHeader>
        <DialogTitle>{t('uninstall.dialog.title', { name: server.name })}</DialogTitle>
        <DialogDescription>{t('uninstall.dialog.description')}</DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-2">
        <span className="font-medium">{t('uninstall.dialog.removed')}</span>
        <ChangeList changes={server.manifest} />
        <span className="text-xs text-muted-foreground">{t('uninstall.dialog.onlyOwn')}</span>
      </div>

      <label className="flex cursor-pointer items-start gap-2">
        <input
          type="checkbox"
          className="mt-0.5 size-4 accent-primary"
          checked={removeBackups}
          onChange={(event) => setRemoveBackups(event.target.checked)}
        />
        <span className="flex flex-col">
          <span>{t('uninstall.dialog.backups')}</span>
          <span className="text-xs text-muted-foreground">
            {t('uninstall.dialog.backupsHint', { path: BACKUP_DIR })}
          </span>
        </span>
      </label>

      <Field>
        <Label htmlFor="uninstall-server-name">{t('server.deleteDialog.confirm')}</Label>
        <Input
          id="uninstall-server-name"
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
        <Button type="button" variant="outline" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button
          type="submit"
          variant="destructive"
          disabled={typed !== server.name || start.isPending}
        >
          {start.isPending ? t('uninstall.dialog.starting') : t('uninstall.dialog.submit')}
        </Button>
      </DialogFooter>
    </form>
  )
}

/** A removal of Xray in progress, or stopped partway and waiting. */
export function UninstallView({ server, feed }: { server: Server; feed: OperationFeed }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [deleting, setDeleting] = useState(false)
  const { operation, log } = feed
  const running = operation.status === 'running'
  const failed = operation.status === 'failed' && operation.error !== null

  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['servers'] })
    void queryClient.invalidateQueries({ queryKey: ['operations', operation.id] })
  }
  const cancel = useMutation({
    mutationFn: () => api.cancelOperation(operation.id),
    onSettled: refresh,
  })
  const retry = useMutation({
    mutationFn: () => api.retryOperation(operation.id),
    onSettled: refresh,
  })

  const current = operation.steps.findIndex(
    (step) => step.status === 'running' || step.status === 'failed',
  )
  const position =
    current >= 0 ? current + 1 : operation.steps.filter((step) => step.status === 'done').length

  return (
    <Page>
      <PageHeader
        title={t('uninstall.title', { name: server.name })}
        description={<Breadcrumb>{t('uninstall.subtitle', { name: server.name })}</Breadcrumb>}
        actions={
          <>
            {running && (
              <Button variant="outline" disabled={cancel.isPending} onClick={() => cancel.mutate()}>
                {cancel.isPending ? t('server.installing.stopping') : t('server.installing.stop')}
              </Button>
            )}
            <Button asChild variant="outline">
              <Link to="/servers">{t('server.toList')}</Link>
            </Button>
          </>
        }
      />

      {failed && (
        <Panel className="border border-warning/40 bg-warning/5">
          <div className="flex flex-col gap-1">
            <h2 className="font-heading text-base font-semibold text-warning">
              {t('uninstall.paused.title', { number: Math.max(position, 1) })}
            </h2>
            <p>{t('uninstall.paused.text')}</p>
          </div>
          <FailureText operation={operation} />
          <div className="flex flex-wrap gap-x-6 gap-y-3">
            <div className="flex max-w-xs flex-col gap-1">
              <Button className="self-start" disabled={retry.isPending} onClick={() => retry.mutate()}>
                {retry.isPending ? t('server.failed.retrying') : t('uninstall.paused.retry')}
              </Button>
              <span className="text-xs text-muted-foreground">{t('uninstall.paused.retryHint')}</span>
            </div>
            <div className="flex max-w-xs flex-col gap-1">
              <Button variant="outline" className="self-start" onClick={() => setDeleting(true)}>
                {t('server.delete')}
              </Button>
              <span className="text-xs text-muted-foreground">{t('uninstall.paused.forgetHint')}</span>
            </div>
          </div>
          {retry.isError && <Notice title={t('server.failed.actionFailed')} />}
        </Panel>
      )}

      <div className="grid gap-5 lg:grid-cols-[minmax(0,5fr)_minmax(0,6fr)]">
        <div className="flex flex-col gap-5">
          <Panel title={t('uninstall.progress.title')}>
            <p className="text-muted-foreground">
              {t('server.installing.progress', {
                current: Math.max(position, 1),
                total: operation.steps.length,
              })}
            </p>
            <StepList steps={operation.steps} label={t('uninstall.progress.title')} />
          </Panel>
          <Panel title={t('uninstall.notes.title')}>
            <ul className="flex list-disc flex-col gap-1.5 pl-5 text-muted-foreground">
              <li>{t('uninstall.notes.onlyOwn')}</li>
              <li>
                {operation.params.remove_backups
                  ? t('uninstall.notes.backupsRemoved', { path: BACKUP_DIR })
                  : t('uninstall.notes.backupsKept', { path: BACKUP_DIR })}
              </li>
              <li>{t('uninstall.notes.noUndo')}</li>
              <li>{t('uninstall.notes.leaves')}</li>
            </ul>
          </Panel>
        </div>
        <Panel title={t('uninstall.log')}>
          <LogView lines={log} follow={running} />
        </Panel>
      </div>

      <DeleteDialog server={server} open={deleting} onOpenChange={setDeleting} />
    </Page>
  )
}

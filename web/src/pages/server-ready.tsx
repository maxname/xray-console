import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { Page, PageHeader, Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  api,
  ApiError,
  type OperationFeed,
  type Server,
  type ServerConfig,
  type UpdateConfigRequest,
} from '@/lib/api'
import { toast } from '@/lib/toast'
import { InstallSummary } from '@/pages/server'
import { Breadcrumb } from '@/components/server-parts'
import { UninstallDialog, UpdateButton, UpdateStatus } from '@/pages/server-lifecycle'

import { configKey, useServerConfig, fieldLabel, explain } from '@/lib/server-config'
import { Labelled, TextControl, FieldRow, TemplatePanel } from '@/pages/server-settings-fields'
import { ApplyProgress, ApplyFailed, RotateDialog } from '@/pages/server-apply'
import { DiffView, HistoryList, ConfigTab } from '@/pages/server-config-tab'

type TabId = 'settings' | 'config' | 'install'

/** An installed server: its settings, its config.json, how it was installed. */
export function ReadyView({ server, feed }: { server: Server; feed: OperationFeed | null }) {
  const { t } = useTranslation()
  // Straight after installing, open on what the installation did; once
  // anything has been applied since, settings are what one comes here for.
  const [tab, setTab] = useState<TabId>(
    feed?.operation.kind === 'install' ? 'install' : 'settings',
  )
  const place = [server.location, server.host].filter(Boolean).join(' · ')
  const [uninstalling, setUninstalling] = useState(false)

  return (
    <Page>
      <PageHeader
        title={server.name}
        description={
          <Breadcrumb>
            {t('config.subtitle', { place, version: server.xray_version })}
          </Breadcrumb>
        }
        actions={
          <>
            <UpdateButton server={server} busy={feed?.operation.status === 'running'} />
            <RestartButton server={server} />
            <JournalButton server={server} />
            <Button variant="outline" onClick={() => setUninstalling(true)}>
              {t('uninstall.button')}
            </Button>
          </>
        }
      />
      <UpdateStatus server={server} feed={feed} />
      <UninstallDialog server={server} open={uninstalling} onOpenChange={setUninstalling} />
      <Tabs value={tab} onValueChange={(value) => setTab(value as TabId)}>
        <TabsList>
          <TabsTrigger value="settings">{t('config.tabs.settings')}</TabsTrigger>
          <TabsTrigger value="config">{t('config.tabs.config')}</TabsTrigger>
          <TabsTrigger value="install">{t('config.tabs.install')}</TabsTrigger>
        </TabsList>
        <TabsContent value="settings" className="mt-4 flex flex-col gap-5">
          <SettingsTab server={server} feed={feed} />
        </TabsContent>
        <TabsContent value="config" className="mt-4 flex flex-col gap-5">
          <ConfigTab server={server} />
        </TabsContent>
        <TabsContent value="install" className="mt-4 flex flex-col gap-5">
          <InstallSummary server={server} feed={installFeed(feed)} />
        </TabsContent>
      </Tabs>
    </Page>
  )
}

/** The installation's own feed, or nothing once a later job has replaced it. */
function installFeed(feed: OperationFeed | null): OperationFeed | null {
  return feed?.operation.kind === 'install' ? feed : null
}

function RestartButton({ server }: { server: Server }) {
  const { t } = useTranslation()
  const restart = useMutation({
    mutationFn: () => api.restartXray(server.id),
    onSuccess: ({ state }) =>
      toast({
        title: t('config.restarted'),
        description: t('config.restartedState', { state }),
        variant: state === 'active' ? 'success' : 'destructive',
      }),
    onError: (error) =>
      toast({
        title: t('config.restartFailed'),
        description:
          error instanceof ApiError && error.code === 'busy'
            ? t('fieldErrors.busy')
            : undefined,
        variant: 'destructive',
      }),
  })
  return (
    <Button variant="outline" onClick={() => restart.mutate()} disabled={restart.isPending}>
      {restart.isPending ? t('config.restarting') : t('config.restart')}
    </Button>
  )
}

function JournalButton({ server }: { server: Server }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const journal = useQuery({
    queryKey: ['servers', server.id, 'journal'],
    queryFn: () => api.getJournal(server.id),
    enabled: open,
    // A log is read fresh every time it is opened.
    gcTime: 0,
    retry: false,
  })

  return (
    <>
      <Button variant="outline" onClick={() => setOpen(true)}>
        {t('config.journal')}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>{t('config.journalTitle', { name: server.name })}</DialogTitle>
            <DialogDescription>{t('config.journalDescription')}</DialogDescription>
          </DialogHeader>
          {journal.isPending && <Skeleton className="h-40 w-full" />}
          {journal.isError && <Notice title={t('config.journalFailed')} />}
          {journal.data &&
            (journal.data.text === '' ? (
              <p className="text-sm text-muted-foreground">{t('config.journalEmpty')}</p>
            ) : (
              <pre
                className="max-h-[60svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5 whitespace-pre-wrap"
                translate="no"
                tabIndex={0}
              >
                {journal.data.text}
              </pre>
            ))}
        </DialogContent>
      </Dialog>
    </>
  )
}

// --- settings --------------------------------------------------------------------

function SettingsTab({ server, feed }: { server: Server; feed: OperationFeed | null }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useServerConfig(server)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [showDiff, setShowDiff] = useState(false)
  const [rotating, setRotating] = useState(false)
  const [dismissed, setDismissed] = useState<number | null>(null)

  const applyOperation = feed?.operation.kind === 'apply' ? feed.operation : null

  // When an apply ends, what counts as "changed" has changed with it.
  const lastStatus = useRef(applyOperation?.status)
  useEffect(() => {
    const previous = lastStatus.current
    lastStatus.current = applyOperation?.status
    if (previous === applyOperation?.status) {
      return
    }
    void queryClient.invalidateQueries({ queryKey: configKey(server.id) })
    if (previous === 'running' && applyOperation?.status === 'succeeded') {
      toast({ title: t('config.applied', { name: server.name }), variant: 'success' })
    }
  }, [applyOperation?.status, queryClient, server.id, server.name, t])

  const setConfig = (next: ServerConfig) =>
    queryClient.setQueryData(configKey(server.id), next)

  /** Saves one edit; the error, if any, shows under the control named by errorKey. */
  const save = async (
    errorKey: string,
    action: () => Promise<ServerConfig>,
  ): Promise<boolean> => {
    try {
      setConfig(await action())
      setErrors((current) => ({ ...current, [errorKey]: '' }))
      // The servers list shows the same counts.
      void queryClient.invalidateQueries({ queryKey: ['servers'], exact: true })
      return true
    } catch (error) {
      setErrors((current) => ({ ...current, [errorKey]: explain(t, error) }))
      return false
    }
  }
  const update = (errorKey: string, body: UpdateConfigRequest) =>
    save(errorKey, () => api.updateServerConfig(server.id, body))

  const apply = useMutation({
    mutationFn: () => api.applyServerConfig(server.id),
    onSettled: () => {
      setDismissed(null)
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    },
  })

  if (query.isError) {
    return (
      <Panel>
        <p className="text-muted-foreground">{t('common.loadFailed')}</p>
        <Button variant="outline" className="self-start" onClick={() => void query.refetch()}>
          {t('common.retry')}
        </Button>
      </Panel>
    )
  }
  const config = query.data
  if (!config) {
    return (
      <Panel>
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-5 w-full" />
        <Skeleton className="h-5 w-2/3" />
      </Panel>
    )
  }

  const busy = config.busy || applyOperation?.status === 'running' || apply.isPending
  const field = (key: string) => config.fields.find((item) => item.key === key)!
  const overridden = config.fields.filter((item) => item.source === 'override')
  const pending = config.changed.length > 0

  const renderField = (key: string, placeholder?: string) => (
    <FieldRow
      field={field(key)}
      disabled={busy}
      placeholder={placeholder}
      error={errors[key]}
      onChange={(value) => update(key, { overrides: { [key]: value } })}
    />
  )

  return (
    <>
      {applyOperation?.status === 'running' && <ApplyProgress operation={applyOperation} />}
      {applyOperation?.status === 'failed' &&
        applyOperation.error !== null &&
        dismissed !== applyOperation.id && (
          <ApplyFailed
            server={server}
            operation={applyOperation}
            onDismiss={() => setDismissed(applyOperation.id)}
          />
        )}

      {pending && !busy && (
        <div
          role="status"
          className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-xl border border-warning/40 bg-warning/8 px-4 py-3 text-sm"
        >
          <span className="min-w-0 flex-1 basis-80 font-semibold text-warning">
            {config.applied_at === null
              ? t('config.pending.neverApplied')
              : t('config.pending.title', {
                  count: config.changed.length,
                  fields: config.changed.map((key) => fieldLabel(t, key)).join(', '),
                })}
          </span>
          <div className="flex flex-wrap gap-2">
            {config.diff.length > 0 && (
              <Button variant="outline" onClick={() => setShowDiff((shown) => !shown)}>
                {showDiff ? t('config.pending.hideDiff') : t('config.pending.showDiff')}
              </Button>
            )}
            {config.applied_at !== null && (
              <Button
                variant="outline"
                onClick={() => void save('reset', () => api.resetServerConfig(server.id))}
              >
                {t('config.pending.reset')}
              </Button>
            )}
            <Button onClick={() => apply.mutate()}>{t('config.pending.apply')}</Button>
          </div>
        </div>
      )}
      {apply.isError && <Notice title={explain(t, apply.error)} />}
      {errors.reset && <Notice title={errors.reset} />}

      {pending && showDiff && config.diff.length > 0 && (
        <Panel title={t('config.diff.title')}>
          <DiffView lines={config.diff} />
          <p className="text-xs text-muted-foreground">{t('config.pending.note')}</p>
        </Panel>
      )}

      <div className="grid gap-5 xl:grid-cols-2">
        <Panel title={t('config.reality.title')}>
          {renderField('inbound.port')}
          <Labelled
            label={t('fields.reality.maskHost')}
            htmlFor="config-mask-host"
            hint={t('config.reality.maskHostHint')}
            error={errors.maskHost}
          >
            <TextControl
              id="config-mask-host"
              value={config.mask_host}
              disabled={busy}
              onCommit={(value) => update('maskHost', { mask_host: value })}
            />
          </Labelled>
          <Labelled
            label={t('fields.reality.publicKey')}
            hint={t('config.reality.publicKeyHint', { name: server.name })}
          >
            <code className="rounded-lg bg-muted px-2.5 py-1.5 font-mono text-[13px] break-all" translate="no">
              {config.public_key}
            </code>
          </Labelled>
          <Labelled label={t('fields.reality.shortIds')} error={errors.shortIds}>
            <ul className="flex flex-wrap gap-2">
              {config.short_ids.map((shortId) => (
                <li
                  key={shortId}
                  className="flex items-center gap-1 rounded-lg bg-muted py-1 pr-1 pl-2.5 font-mono text-[13px]"
                  translate="no"
                >
                  {shortId}
                  <Button
                    variant="ghost"
                    size="icon-xs"
                    disabled={busy || config.short_ids.length === 1}
                    aria-label={t('config.reality.shortIdRemove', { id: shortId })}
                    onClick={() =>
                      void save('shortIds', () => api.removeShortId(server.id, shortId))
                    }
                  >
                    <X aria-hidden="true" />
                  </Button>
                </li>
              ))}
            </ul>
          </Labelled>
          <div className="flex flex-wrap gap-2">
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => void save('shortIds', () => api.addShortId(server.id))}
            >
              {t('config.reality.shortIdAdd')}
            </Button>
            <Button variant="outline" size="sm" disabled={busy} onClick={() => setRotating(true)}>
              {t('config.reality.rotate')}
            </Button>
          </div>
        </Panel>

        <Panel title={t('config.xhttp.title')}>
          {renderField('xhttp.mode')}
          {renderField('xhttp.path')}
          {renderField('xhttp.xPaddingBytes')}
          {renderField('xhttp.host', t('config.xhttp.hostPlaceholder'))}
        </Panel>

        <TemplatePanel
          server={server}
          config={config}
          overridden={overridden}
          disabled={busy}
          error={errors.template}
          onUpdate={(body) => update('template', body)}
        />

        <Panel title={t('config.history.title')}>
          <HistoryList entries={config.history} />
        </Panel>
      </div>

      <RotateDialog
        server={server}
        open={rotating}
        onOpenChange={setRotating}
        onConfirm={() => save('shortIds', () => api.rotateKeys(server.id))}
      />
    </>
  )
}

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Check, Loader2, X } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  api,
  ApiError,
  type ConfigField,
  type DiffLine,
  type HistoryEntry,
  type Operation,
  type OperationFeed,
  type Server,
  type ServerConfig,
  type UpdateConfigRequest,
} from '@/lib/api'
import { useCopy } from '@/lib/clipboard'
import { aftermath } from '@/lib/failures'
import { formatDayTime } from '@/lib/format'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'
import { Breadcrumb, InstallSummary } from '@/pages/server'
import { UninstallDialog, UpdateButton, UpdateStatus } from '@/pages/server-lifecycle'

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

function configKey(serverId: number) {
  return ['servers', serverId, 'config'] as const
}

function useServerConfig(server: Server) {
  return useQuery({
    queryKey: configKey(server.id),
    queryFn: () => api.getServerConfig(server.id),
    refetchInterval: (q) => (q.state.data?.busy ? 1500 : false),
  })
}

// --- header actions ------------------------------------------------------------

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

/** A field's name as the administrator knows it. */
function fieldLabel(t: (key: string) => string, key: string): string {
  return t(`fields.${key}`)
}

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

/** Turns a failed save into a sentence. */
/** The servers an error names, as a list to read. */
function serverNames(value: unknown): string {
  return Array.isArray(value) ? value.filter((name) => typeof name === 'string').join(', ') : ''
}

function explain(
  t: (key: string, options?: Record<string, unknown>) => string,
  error: unknown,
): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_field') {
      const reason = String(error.details.reason ?? 'generic')
      return t(`fieldErrors.${reason}`, { defaultValue: t('fieldErrors.generic') })
    }
    if (error.code === 'invalid_mask_host' || error.code === 'busy') {
      return t(`fieldErrors.${error.code}`)
    }
    if (error.code === 'upstream_pending') {
      return t('fieldErrors.upstream_pending', { servers: serverNames(error.details.servers) })
    }
  }
  return t('fieldErrors.generic')
}

function Labelled({
  label,
  htmlFor,
  tag,
  hint,
  error,
  children,
}: {
  label: string
  htmlFor?: string
  tag?: ReactNode
  hint?: string
  error?: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="font-medium">
            {label}
          </label>
        ) : (
          <span className="font-medium">{label}</span>
        )}
        {tag !== undefined && <span className="text-xs text-muted-foreground">{tag}</span>}
      </div>
      {children}
      {hint !== undefined && <span className="text-xs text-muted-foreground">{hint}</span>}
      {error ? (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      ) : null}
    </div>
  )
}

/** A text input that saves when the administrator is done with it — on leaving
 *  the field or pressing Enter — not on every keystroke. */
function TextControl({
  id,
  value,
  disabled,
  placeholder,
  onCommit,
}: {
  id: string
  value: string
  disabled: boolean
  placeholder?: string
  onCommit: (value: string) => Promise<boolean>
}) {
  const [draft, setDraft] = useState(value)
  // Follow the saved value when it changes underneath: a reset, a new template.
  const [seen, setSeen] = useState(value)
  if (seen !== value) {
    setSeen(value)
    setDraft(value)
  }

  const commit = () => {
    if (draft.trim() !== value) {
      void onCommit(draft)
    }
  }

  return (
    <Input
      id={id}
      className="font-mono"
      value={draft}
      disabled={disabled}
      placeholder={placeholder}
      autoComplete="off"
      spellCheck={false}
      autoCapitalize="off"
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault()
          commit()
        }
      }}
    />
  )
}

/** One template field on a server: inherited, overridden, or locked. */
function FieldRow({
  field,
  disabled,
  placeholder,
  error,
  onChange,
}: {
  field: ConfigField
  disabled: boolean
  placeholder?: string
  error?: string
  /** null goes back to the template's value. */
  onChange: (value: string | null) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const id = `config-${field.key}`
  const locked = field.fixed || !field.overridable

  const tag = field.fixed ? (
    t('config.source.fixed')
  ) : !field.overridable ? (
    t('config.source.locked')
  ) : field.source === 'override' ? (
    <>
      {t('config.source.override', {
        value: field.template_value || t('config.source.empty'),
      })}{' '}
      <button
        type="button"
        className="text-primary underline-offset-4 hover:underline disabled:opacity-50"
        disabled={disabled}
        onClick={() => void onChange(null)}
      >
        {t('config.source.revert')}
      </button>
    </>
  ) : (
    t('config.source.template')
  )

  return (
    <Labelled label={t(`fields.${field.key}`)} htmlFor={id} tag={tag} error={error}>
      {field.type === 'enum' ? (
        <div id={id} role="radiogroup" className="flex flex-wrap gap-1.5">
          {(field.options ?? []).map((option) => (
            <button
              key={option}
              type="button"
              role="radio"
              aria-checked={field.value === option}
              disabled={disabled || locked}
              onClick={() => void onChange(option)}
              className={cn(
                'rounded-lg border px-2.5 py-1 font-mono text-[13px] transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60',
                field.value === option
                  ? 'border-primary bg-primary/10 text-foreground'
                  : 'hover:bg-muted',
              )}
            >
              {option}
            </button>
          ))}
        </div>
      ) : (
        <TextControl
          id={id}
          value={field.value}
          disabled={disabled || locked}
          placeholder={placeholder}
          onCommit={(value) => onChange(value)}
        />
      )}
    </Labelled>
  )
}

function TemplatePanel({
  server,
  config,
  overridden,
  disabled,
  error,
  onUpdate,
}: {
  server: Server
  config: ServerConfig
  overridden: ConfigField[]
  disabled: boolean
  error?: string
  onUpdate: (body: UpdateConfigRequest) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const templates = useQuery({
    queryKey: ['templates'],
    queryFn: () => api.listTemplates(),
  })
  const choices = templates.data?.templates ?? []
  const behind =
    config.applied_template_version !== null &&
    config.applied_template_version < config.template.version

  return (
    <Panel title={t('config.template.title')}>
      <p className="font-heading text-base font-semibold" translate="no">
        {config.template.name} v{config.template.version}
      </p>
      <p className="text-muted-foreground">
        {t('config.template.summary', {
          total: config.fields.length,
          inherited: config.fields.length - overridden.length,
          overridden: overridden.length,
        })}
      </p>
      {behind && (
        <p className="text-warning">
          {t('config.template.behind', {
            applied: config.applied_template_version,
            latest: config.template.version,
          })}
        </p>
      )}
      {choices.length > 1 && (
        <Labelled label={t('config.template.change')} htmlFor={`template-${server.id}`}>
          <Select
            value={String(config.template.id)}
            disabled={disabled}
            onValueChange={(value) => void onUpdate({ template_id: Number(value) })}
          >
            <SelectTrigger id={`template-${server.id}`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {choices.map((template) => (
                <SelectItem key={template.id} value={String(template.id)}>
                  {template.name} v{template.version}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Labelled>
      )}
      {error ? <Notice title={error} /> : null}
      <div className="flex flex-wrap gap-2">
        {overridden.length > 0 && (
          <Button
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() =>
              void onUpdate({
                overrides: Object.fromEntries(overridden.map((item) => [item.key, null])),
              })
            }
          >
            {t('config.template.revertAll')}
          </Button>
        )}
        <Button asChild variant="outline" size="sm">
          <Link to="/templates" search={{ id: config.template.id }}>
            {t('config.template.open')}
          </Link>
        </Button>
      </div>
    </Panel>
  )
}

export function DiffView({ lines }: { lines: DiffLine[] }) {
  return (
    <pre
      className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5"
      translate="no"
      tabIndex={0}
    >
      {lines.map((line, index) => (
        <span
          key={index}
          className={cn(
            'block whitespace-pre-wrap',
            line.op === '+' && 'bg-success/10 text-success',
            line.op === '-' && 'bg-destructive/10 text-destructive',
            line.op === '@' && 'text-muted-foreground',
          )}
        >
          {line.op === '@' ? '⋯' : `${line.op} ${line.text}`}
        </span>
      ))}
    </pre>
  )
}

function HistoryList({ entries }: { entries: HistoryEntry[] }) {
  const { t } = useTranslation()
  if (entries.length === 0) {
    return <p className="text-muted-foreground">{t('config.history.empty')}</p>
  }

  const describe = (entry: HistoryEntry): string => {
    if (entry.status === 'running') {
      return t('config.history.running')
    }
    if (entry.kind === 'install') {
      return entry.status === 'succeeded'
        ? t('config.history.install')
        : t('config.history.installFailed')
    }
    if (entry.status !== 'succeeded') {
      return t('config.history.failed')
    }
    return entry.changed.length > 0
      ? t('config.history.applied', {
          fields: entry.changed.map((key) => t(`fields.${key}`)).join(', '),
        })
      : t('config.history.appliedSame')
  }

  return (
    <ul className="flex flex-col gap-2.5">
      {entries.map((entry) => (
        <li key={entry.id} className="grid grid-cols-[auto_1fr] gap-x-4">
          <span className="font-mono text-xs leading-5 text-muted-foreground">
            {formatDayTime(entry.finished_at ?? entry.started_at)}
          </span>
          <span className={cn(entry.status === 'failed' && 'text-destructive')}>
            {describe(entry)}
          </span>
        </li>
      ))}
    </ul>
  )
}

// --- applying --------------------------------------------------------------------

function ApplyProgress({ operation }: { operation: Operation }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('config.applying.title')}>
      <ol className="flex flex-col gap-2">
        {operation.steps.map((step) => (
          <li key={step.id} className="flex items-center gap-3">
            <span
              className={cn(
                'flex size-5 shrink-0 items-center justify-center rounded-full border [&_svg]:size-3',
                step.status === 'done' && 'border-primary bg-primary text-primary-foreground',
                step.status === 'running' && 'border-primary text-primary',
                step.status === 'pending' && 'border-input',
              )}
            >
              {step.status === 'done' && <Check aria-hidden="true" strokeWidth={3} />}
              {step.status === 'running' && (
                <Loader2 aria-hidden="true" className="animate-spin" />
              )}
            </span>
            <span
              className={cn(
                step.status === 'running' && 'font-semibold',
                step.status === 'pending' && 'text-muted-foreground',
              )}
            >
              {t(`config.applying.steps.${step.id}`)}
            </span>
          </li>
        ))}
      </ol>
    </Panel>
  )
}

/** Failure codes of an apply that have their own explanation; the wording is
 *  shared with the installation's. */
const APPLY_ERRORS = [
  'config_rejected',
  'service_failed',
  'check_failed',
  'port_unreachable',
  'mask_unreachable',
  'ssh_unreachable',
  'ssh_key_rejected',
  'ssh_host_key_changed',
  'cancelled',
  'interrupted',
  'timeout',
] as const

function ApplyFailed({
  server,
  operation,
  onDismiss,
}: {
  server: Server
  operation: Operation
  onDismiss: () => void
}) {
  const { t } = useTranslation()
  const failure = operation.error!
  const details = failure.details ?? {}
  const code = (APPLY_ERRORS as readonly string[]).includes(failure.code)
    ? failure.code
    : 'internal'
  // Only what the job itself reported: see aftermath.
  const left = aftermath(details)
  const output = typeof details.output === 'string' ? details.output : ''

  return (
    <Panel className="border border-destructive/40 bg-destructive/5">
      <div className="flex flex-col gap-1">
        <h2 className="font-heading text-base font-semibold text-destructive">
          {t(`config.applyFailed.title.${left}`)}
        </h2>
        <p>{t(`config.applyFailed.${left}`, { name: server.name })}</p>
      </div>
      <div className="flex flex-col gap-1">
        <p className="font-semibold">{t(`server.errors.${code}.title`, details)}</p>
        <p>{t(`server.errors.${code}.text`, details)}</p>
        {code === 'internal' && failure.message !== '' && (
          <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
            {failure.message}
          </code>
        )}
      </div>
      {output !== '' && (
        <pre
          className="max-h-60 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs whitespace-pre-wrap"
          translate="no"
          tabIndex={0}
        >
          {output}
        </pre>
      )}
      <Button variant="outline" className="self-start" onClick={onDismiss}>
        {t('config.applyFailed.dismiss')}
      </Button>
    </Panel>
  )
}

function RotateDialog({
  server,
  open,
  onOpenChange,
  onConfirm,
}: {
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [pending, setPending] = useState(false)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('config.rotateDialog.title', { name: server.name })}</DialogTitle>
          <DialogDescription>{t('config.rotateDialog.description')}</DialogDescription>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">{t('config.rotateDialog.note')}</p>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="destructive"
            disabled={pending}
            onClick={() => {
              setPending(true)
              void onConfirm().finally(() => {
                setPending(false)
                onOpenChange(false)
              })
            }}
          >
            {t('config.rotateDialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- config.json -----------------------------------------------------------------

function ConfigTab({ server }: { server: Server }) {
  const { t } = useTranslation()
  const query = useServerConfig(server)
  const { copied, copy } = useCopy()

  if (!query.data) {
    return (
      <Panel>
        <Skeleton className="h-40 w-full" />
      </Panel>
    )
  }
  const text = query.data.config
  return (
    <Panel>
      <p className="text-muted-foreground">{t('config.configNote')}</p>
      <pre
        className="max-h-[70svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5"
        translate="no"
        tabIndex={0}
      >
        {text}
      </pre>
      <Button
        variant="outline"
        size="sm"
        className="self-start"
        onClick={() => copy(text)}
      >
        {copied ? t('common.copied') : t('config.copyConfig')}
      </Button>
    </Panel>
  )
}

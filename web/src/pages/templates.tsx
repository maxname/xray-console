import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
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
  type ConfigField,
  type TemplateDetail,
  type TemplateServer,
  type TemplateSummary,
} from '@/lib/api'
import { type Translate } from '@/lib/errors'
import { formatDayTime } from '@/lib/format'
import { toast } from '@/lib/toast'
import { cn } from '@/lib/utils'

import { explain, templateKey } from '@/lib/templates'
import { ApplyDialog, PropertiesDialog, CreateDialog } from '@/pages/template-dialogs'

export function TemplatesPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const search = useSearch({ strict: false })
  const [creating, setCreating] = useState<'blank' | 'server' | null>(null)

  const list = useQuery({
    queryKey: ['templates'],
    queryFn: () => api.listTemplates(),
  })

  if (list.isError) {
    return (
      <Page>
        <PageHeader title={t('templates.title')} />
        <EmptyState
          title={t('templates.loadFailedTitle')}
          description={t('servers.loadFailed')}
        >
          <Button variant="outline" onClick={() => void list.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  const templates = list.data?.templates ?? []
  const requested = typeof search.id === 'number' ? search.id : undefined
  // The one asked for in the address, else the first: a panel always has one.
  const selected = templates.find((item) => item.id === requested) ?? templates[0]
  const select = (id: number) => void navigate({ to: '/templates', search: { id } })

  return (
    <Page>
      <PageHeader
        title={t('templates.title')}
        description={t('templates.subtitle')}
        actions={
          <>
            <Button variant="outline" onClick={() => setCreating('server')}>
              {t('templates.createFromServer')}
            </Button>
            <Button onClick={() => setCreating('blank')}>{t('templates.create')}</Button>
          </>
        }
      />

      {!list.data ? (
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-5 w-full" />
        </Panel>
      ) : (
        <>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
            {templates.map((item) => (
              <TemplateCard
                key={item.id}
                template={item}
                selected={item.id === selected?.id}
                onSelect={() => select(item.id)}
              />
            ))}
          </div>
          {selected && (
            // Keyed by id: a different template is a different form, not the
            // same one with new props — its draft must not carry over.
            <TemplateView key={selected.id} id={selected.id} only={templates.length === 1} />
          )}
        </>
      )}

      <CreateDialog
        mode={creating}
        onClose={() => setCreating(null)}
        onCreated={(id) => {
          setCreating(null)
          select(id)
        }}
      />
    </Page>
  )
}

function usage(t: Translate, template: TemplateSummary): string {
  if (template.server_count === 0) {
    return t('templates.usageNone')
  }
  const parts = [t('templates.usage', { count: template.server_count })]
  if (template.changed > 0) {
    parts.push(t('templates.usageChanged', { count: template.changed }))
  } else {
    parts.push(t('templates.usageSynced'))
  }
  if (template.overridden > 0) {
    parts.push(t('templates.usageOverridden', { count: template.overridden }))
  }
  return parts.join(' · ')
}

function TemplateCard({
  template,
  selected,
  onSelect,
}: {
  template: TemplateSummary
  selected: boolean
  onSelect: () => void
}) {
  const { t } = useTranslation()
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      className={cn(
        'flex flex-col gap-1 rounded-xl border bg-card p-4 text-left text-sm shadow-panel transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
        selected ? 'border-primary' : 'hover:bg-muted/60',
      )}
    >
      <span className="flex items-baseline justify-between gap-2">
        <span className="truncate font-heading text-base font-semibold" translate="no">
          {template.name}
        </span>
        <span className="font-mono text-xs text-muted-foreground">v{template.version}</span>
      </span>
      {template.description !== '' && (
        <span className="text-muted-foreground">{template.description}</span>
      )}
      <span className={cn('text-xs', template.changed > 0 ? 'text-warning' : 'text-muted-foreground')}>
        {usage(t, template)}
      </span>
    </button>
  )
}

function TemplateView({ id, only }: { id: number; only: boolean }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [properties, setProperties] = useState(false)
  const [applying, setApplying] = useState(false)

  const query = useQuery({
    queryKey: templateKey(id),
    queryFn: () => api.getTemplate(id),
    // While a batch runs, the servers' states change one after another.
    refetchInterval: (q) => (q.state.data?.batch?.running ? 1500 : false),
  })

  const accept = (detail: TemplateDetail) => {
    queryClient.setQueryData(templateKey(id), detail)
    void queryClient.invalidateQueries({ queryKey: ['templates'], exact: true })
    void queryClient.invalidateQueries({ queryKey: ['servers'] })
  }

  // A batch that ends has changed the counts on the cards above, which come
  // from another query.
  const batchRunning = query.data?.batch?.running ?? false
  const wasRunning = useRef(batchRunning)
  useEffect(() => {
    if (wasRunning.current && !batchRunning) {
      void queryClient.invalidateQueries({ queryKey: ['templates'], exact: true })
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
    }
    wasRunning.current = batchRunning
  }, [batchRunning, queryClient])

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
  const detail = query.data
  if (!detail) {
    return (
      <Panel>
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-24 w-full" />
      </Panel>
    )
  }

  return (
    <>
      <Panel>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-heading text-base font-semibold">
            {t('templates.variables', { name: detail.template.name })}
          </h2>
          <Button variant="outline" size="sm" onClick={() => setProperties(true)}>
            {t('templates.properties')}
          </Button>
        </div>
        {/* Keyed by version: saving makes the saved values the new baseline. */}
        <VariablesForm key={detail.template.version} detail={detail} onSaved={accept} />
      </Panel>

      <div className="grid gap-5 xl:grid-cols-2">
        <Panel>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-heading text-base font-semibold">
              {t('templates.servers.title')}
            </h2>
            {detail.template.changed > 0 && !detail.batch?.running && (
              <Button size="sm" onClick={() => setApplying(true)}>
                {t('templates.servers.applyAll', { version: detail.template.version })}
              </Button>
            )}
          </div>
          <BatchLine detail={detail} />
          {detail.servers.length === 0 ? (
            <p className="text-muted-foreground">{t('templates.servers.empty')}</p>
          ) : (
            <ul className="flex flex-col divide-y">
              {detail.servers.map((server) => (
                <ServerLine key={server.id} server={server} />
              ))}
            </ul>
          )}
        </Panel>

        <Panel title={t('templates.versions.title')}>
          <VersionList detail={detail} onReverted={accept} />
        </Panel>
      </div>

      <PropertiesDialog
        detail={detail}
        only={only}
        open={properties}
        onOpenChange={setProperties}
        onSaved={accept}
      />
      <ApplyDialog
        detail={detail}
        open={applying}
        onOpenChange={setApplying}
        onStarted={accept}
      />
    </>
  )
}

type Scope = 'common' | 'overridable'

/** The table of a template's values and who may change them. */
function VariablesForm({
  detail,
  onSaved,
}: {
  detail: TemplateDetail
  onSaved: (detail: TemplateDetail) => void
}) {
  const { t } = useTranslation()
  const initialValues = Object.fromEntries(
    detail.fields.map((field) => [field.key, field.value]),
  )
  const initialScopes = Object.fromEntries(
    detail.fields.map((field) => [
      field.key,
      (field.overridable ? 'overridable' : 'common') as Scope,
    ]),
  )
  const [values, setValues] = useState<Record<string, string>>(initialValues)
  const [scopes, setScopes] = useState<Record<string, Scope>>(initialScopes)

  const dirty = detail.fields.some(
    (field) =>
      values[field.key].trim() !== initialValues[field.key] ||
      scopes[field.key] !== initialScopes[field.key],
  )

  const save = useMutation({
    mutationFn: () =>
      api.updateTemplate(detail.template.id, {
        values,
        overridable: detail.fields
          .filter((field) => scopes[field.key] === 'overridable')
          .map((field) => field.key),
      }),
    onSuccess: (next) => {
      if (next.template.version !== detail.template.version) {
        toast({
          title: t('templates.saved', {
            name: next.template.name,
            version: next.template.version,
          }),
          variant: 'success',
        })
      }
      onSaved(next)
    },
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t('templates.columns.field')}</TableHead>
            <TableHead>{t('templates.columns.value')}</TableHead>
            <TableHead>{t('templates.columns.scope')}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {detail.fields.map((field) => (
            <TableRow key={field.key}>
              <TableCell className="font-mono">
                <label htmlFor={`template-${field.key}`}>{field.key}</label>
              </TableCell>
              <TableCell className="min-w-48">
                <ValueControl
                  field={field}
                  value={values[field.key]}
                  onChange={(value) =>
                    setValues((current) => ({ ...current, [field.key]: value }))
                  }
                />
              </TableCell>
              <TableCell className="min-w-52">
                {field.fixed ? (
                  <span className="text-muted-foreground">
                    {t('templates.scope.common')} · {t('templates.fixedNote')}
                  </span>
                ) : (
                  <Select
                    value={scopes[field.key]}
                    onValueChange={(value) =>
                      setScopes((current) => ({ ...current, [field.key]: value as Scope }))
                    }
                  >
                    <SelectTrigger
                      className="w-full"
                      aria-label={`${field.key}: ${t('templates.columns.scope')}`}
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="common">{t('templates.scope.common')}</SelectItem>
                      <SelectItem value="overridable">
                        {t('templates.scope.overridable')}
                      </SelectItem>
                    </SelectContent>
                  </Select>
                )}
              </TableCell>
            </TableRow>
          ))}
          {/* What never comes from a template: shown so the table is the whole
              picture of where a server's settings come from. */}
          {(
            [
              ['reality.maskHost', 'maskHost'],
              ['reality.privateKey', 'privateKey'],
              ['reality.shortIds', 'shortIds'],
            ] as const
          ).map(([key, note]) => (
            <TableRow key={key}>
              <TableCell className="font-mono">{key}</TableCell>
              <TableCell className="text-muted-foreground">
                {t(`templates.serverValues.${note}`)}
              </TableCell>
              <TableCell className="text-muted-foreground">
                {t('templates.scope.server')}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      {save.isError && <Notice title={explain(t, save.error)} />}
      {dirty && (
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={save.isPending}>
            {save.isPending
              ? t('templates.saving')
              : t('templates.save', { version: detail.template.version + 1 })}
          </Button>
          <Button
            type="button"
            variant="outline"
            onClick={() => {
              setValues(initialValues)
              setScopes(initialScopes)
              save.reset()
            }}
          >
            {t('templates.discard')}
          </Button>
        </div>
      )}
    </form>
  )
}

function ValueControl({
  field,
  value,
  onChange,
}: {
  field: ConfigField
  value: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const id = `template-${field.key}`
  if (field.type === 'enum') {
    return (
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger id={id} className="w-full font-mono">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {(field.options ?? []).map((option) => (
            <SelectItem key={option} value={option} className="font-mono">
              {option}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    )
  }
  return (
    <Input
      id={id}
      className="font-mono"
      value={value}
      disabled={field.fixed}
      placeholder={t('templates.emptyValue')}
      autoComplete="off"
      spellCheck={false}
      autoCapitalize="off"
      onChange={(event) => onChange(event.target.value)}
    />
  )
}

function BatchLine({ detail }: { detail: TemplateDetail }) {
  const { t } = useTranslation()
  const batch = detail.batch
  if (!batch) {
    return null
  }
  return (
    <div
      role="status"
      className={cn(
        'flex flex-col gap-0.5 rounded-lg px-3 py-2',
        batch.failed.length > 0 ? 'bg-destructive/8' : 'bg-muted',
      )}
    >
      <span>
        {batch.running
          ? t('templates.batch.running', {
              done: batch.done,
              total: batch.total,
              current: batch.current,
            })
          : t('templates.batch.done', { done: batch.done, total: batch.total })}
      </span>
      {batch.failed.length > 0 && (
        <span className="text-destructive">
          {t('templates.batch.failed', { servers: batch.failed.join(', ') })}
        </span>
      )}
    </div>
  )
}

function ServerLine({ server }: { server: TemplateServer }) {
  const { t } = useTranslation()
  const variant =
    server.state === 'synced' ? 'success' : server.state === 'changed' ? 'warning' : 'secondary'

  const note =
    server.state === 'changed'
      ? t('templates.servers.pending', { fields: server.changed.join(', ') })
      : server.overrides.length > 0
        ? t('templates.servers.overrides', { fields: server.overrides.join(', ') })
        : server.applied_at !== null
          ? t('templates.servers.appliedAt', { when: formatDayTime(server.applied_at) })
          : server.state === 'not_installed'
            ? ''
            : t('templates.servers.neverApplied')

  return (
    <li className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 first:pt-0 last:pb-0">
      <span className="font-medium" translate="no">
        {server.name}
      </span>
      <Badge variant={variant}>{t(`templates.servers.state.${server.state}`)}</Badge>
      <span className="min-w-0 flex-1 basis-40 text-xs break-words text-muted-foreground">
        {note}
      </span>
      <Button asChild variant="outline" size="sm">
        <Link to="/servers/$serverId" params={{ serverId: String(server.id) }}>
          {t('templates.servers.open')}
        </Link>
      </Button>
    </li>
  )
}

function VersionList({
  detail,
  onReverted,
}: {
  detail: TemplateDetail
  onReverted: (detail: TemplateDetail) => void
}) {
  const { t } = useTranslation()
  const revert = useMutation({
    mutationFn: (version: number) => api.revertTemplate(detail.template.id, version),
    onSuccess: onReverted,
  })

  return (
    <>
      <ul className="flex flex-col divide-y">
        {detail.versions.map((version) => {
          const current = version.version === detail.template.version
          return (
            <li
              key={version.version}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2.5 first:pt-0 last:pb-0"
            >
              <span className="font-mono font-medium">v{version.version}</span>
              <span className="font-mono text-xs text-muted-foreground">
                {formatDayTime(version.created_at)}
              </span>
              <span className="min-w-0 flex-1 basis-40 font-mono text-xs break-words">
                {version.note === '' ? t('templates.versions.initial') : version.note}
              </span>
              {current ? (
                <span className="text-xs text-muted-foreground">
                  {t('templates.versions.current')}
                </span>
              ) : (
                <Button
                  variant="outline"
                  size="sm"
                  disabled={revert.isPending}
                  onClick={() => revert.mutate(version.version)}
                >
                  {t('templates.versions.revert')}
                </Button>
              )}
            </li>
          )
        })}
      </ul>
      {revert.isError && <Notice title={explain(t, revert.error)} />}
    </>
  )
}

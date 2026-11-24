import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowDown, ArrowUp } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { EntryApply } from '@/components/entry-apply'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Segmented } from '@/components/segmented'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import {
  api,
  type Chain,
  type Rule,
  type RuleAction,
  type RuleSet,
} from '@/lib/api'
import {
  chainsFrom,
  clientsPending,
  describeMatch,
  PRESETS,
  type Preset,
} from '@/lib/rules'
import { cn } from '@/lib/utils'

import { explain, toRequest } from '@/lib/routing'
import { RuleEditor, RuleSetEditor } from '@/pages/routing-editors'

const ruleSetsKey = ['rule-sets'] as const

export function RoutingPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [selected, setSelected] = useState<number | null>(null)
  // null: closed; 'new': creating; a set: editing it.
  const [editing, setEditing] = useState<RuleSet | 'new' | null>(null)

  const query = useQuery({
    queryKey: ruleSetsKey,
    queryFn: () => api.listRuleSets(),
    // Faster while a server is being applied, so the banner follows it.
    refetchInterval: (q) =>
      q.state.data?.rule_sets.some((set) => set.entry.busy) ? 1500 : 30_000,
  })
  const chains = useQuery({ queryKey: ['chains'], queryFn: () => api.listChains() })
  const servers = useQuery({ queryKey: ['servers'], queryFn: () => api.listServers() })

  const refresh = () => queryClient.invalidateQueries({ queryKey: ruleSetsKey })

  if (query.isError) {
    return (
      <Page>
        <PageHeader title={t('routing.title')} />
        <EmptyState title={t('routing.loadFailedTitle')} description={t('servers.loadFailed')}>
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  const sets = query.data?.rule_sets ?? []
  const current = sets.find((set) => set.id === selected) ?? sets[0] ?? null
  const installed = (servers.data?.servers ?? []).filter((server) => server.status === 'ready')
  const allChains = chains.data?.chains ?? []

  return (
    <Page>
      <PageHeader
        title={t('routing.title')}
        description={t('routing.subtitle')}
        actions={
          <Button disabled={installed.length === 0} onClick={() => setEditing('new')}>
            {t('routing.createSet')}
          </Button>
        }
      />

      {!query.data ? (
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-24 w-full" />
        </Panel>
      ) : current === null ? (
        <EmptyState
          title={t('routing.emptyTitle')}
          description={
            installed.length === 0 && servers.data
              ? t('routing.needServers')
              : t('routing.emptyDescription')
          }
        >
          {installed.length === 0 && servers.data ? (
            <Button asChild variant="outline">
              <Link to="/servers">{t('overview.firstRun.action')}</Link>
            </Button>
          ) : (
            <Button onClick={() => setEditing('new')}>{t('routing.createSet')}</Button>
          )}
        </EmptyState>
      ) : (
        <>
          {sets.length > 1 && (
            <Segmented
              label={t('routing.sets')}
              className="self-start"
              options={sets.map((set) => ({ value: set.id, label: set.name }))}
              value={current.id}
              onChange={setSelected}
            />
          )}
          <RuleSetView
            // A different set is a different list with its own open dialogs.
            key={current.id}
            set={current}
            chains={allChains}
            protocols={query.data.protocols}
            onEdit={() => setEditing(current)}
            onRefresh={refresh}
          />
        </>
      )}

      {editing !== null && (
        <RuleSetEditor
          key={editing === 'new' ? 'new' : editing.id}
          set={editing === 'new' ? null : editing}
          servers={installed}
          chains={allChains}
          onClose={() => setEditing(null)}
          onSaved={(saved) => {
            setEditing(null)
            setSelected(saved?.id ?? null)
            void refresh()
          }}
        />
      )}
    </Page>
  )
}

function TargetBadge({
  action,
  chainId,
  chains,
}: {
  action: RuleAction
  chainId: number
  chains: Chain[]
}) {
  const { t } = useTranslation()
  if (action === 'block') {
    return <Badge variant="destructive">{t('routing.target.block')}</Badge>
  }
  if (action === 'direct') {
    return <Badge variant="secondary">{t('routing.target.direct')}</Badge>
  }
  const chain = chains.find((candidate) => candidate.id === chainId)
  return chain ? (
    <Badge>{t('routing.target.chain', { name: chain.name })}</Badge>
  ) : (
    <Badge variant="destructive">{t('routing.target.chainGone')}</Badge>
  )
}

function RuleSetView({
  set,
  chains,
  protocols,
  onEdit,
  onRefresh,
}: {
  set: RuleSet
  chains: Chain[]
  protocols: string[]
  onEdit: () => void
  onRefresh: () => Promise<unknown>
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // null: closed; 'new': adding; otherwise the rule being edited — by its id,
  // not its place: the list may be reordered or shortened elsewhere while the
  // editor is open, and a place would then point at another rule.
  const [editing, setEditing] = useState<{ id: string } | 'new' | null>(null)
  const available = chainsFrom(chains, set.entry.id)

  const save = useMutation({
    mutationFn: (rules: Rule[]) => api.updateRuleSet(set.id, toRequest(set, { rules })),
    onSuccess: (next) => {
      queryClient.setQueryData<{ rule_sets: RuleSet[]; protocols: string[] }>(
        ruleSetsKey,
        (data) =>
          data && {
            ...data,
            rule_sets: data.rule_sets.map((item) => (item.id === next.id ? next : item)),
          },
      )
    },
  })

  const move = (index: number, by: number) => {
    const rules = [...set.rules]
    const [rule] = rules.splice(index, 1)
    rules.splice(index + by, 0, rule)
    save.mutate(rules)
  }
  const toggle = (index: number, enabled: boolean) =>
    save.mutate(set.rules.map((rule, at) => (at === index ? { ...rule, enabled } : rule)))
  const addPreset = (preset: Preset) =>
    save.mutate([
      ...set.rules,
      { ...preset.rule, id: '', name: t(`routing.presets.${preset.key}.name`) },
    ])

  return (
    <>
      <Panel>
        <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
          <div className="flex min-w-0 flex-col gap-1">
            <h2 className="font-heading text-lg font-semibold">{set.name}</h2>
            <p className="text-muted-foreground">
              {t('routing.runsOn')}{' '}
              <Link
                to="/servers/$serverId"
                params={{ serverId: String(set.entry.id) }}
                className="font-medium text-foreground underline-offset-4 hover:underline"
              >
                {set.entry.name}
              </Link>
              {' · '}
              {set.subscriptions === 0
                ? t('routing.noSubscriptions')
                : t('routing.usedBy', { count: set.subscriptions })}
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={onEdit}>
            {t('routing.editSet')}
          </Button>
        </div>

        {set.subscriptions === 0 && (
          <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg bg-muted px-3 py-2.5">
            <span className="min-w-0 flex-1 basis-72">{t('routing.noSubscriptionsHint')}</span>
            <Button asChild variant="outline" size="sm">
              <Link to="/clients">{t('routing.toClients')}</Link>
            </Button>
          </div>
        )}
        {clientsPending(set.entry) && (
          <EntryApply entry={set.entry} text={t('routing.pending')} onRefresh={onRefresh} />
        )}
      </Panel>

      <Panel className="gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="font-heading text-base font-semibold">{t('routing.rules.title')}</h2>
          <Button size="sm" onClick={() => setEditing('new')}>
            {t('routing.rules.add')}
          </Button>
        </div>
        <p className="text-muted-foreground">{t('routing.rules.order')}</p>
        {save.isError && <Notice title={explain(t, save.error)} />}

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-10">{t('routing.rules.number')}</TableHead>
              <TableHead className="w-full">{t('routing.rules.condition')}</TableHead>
              <TableHead className="hidden lg:table-cell">{t('routing.rules.target')}</TableHead>
              <TableHead className="text-right">{t('routing.rules.actions')}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {set.rules.map((rule, index) => (
              <TableRow key={rule.id} className={cn(!rule.enabled && 'text-muted-foreground')}>
                <TableCell className="tabular-nums">{index + 1}</TableCell>
                <TableCell className="whitespace-normal">
                  <span className="font-medium">
                    {rule.name !== '' ? rule.name : t('routing.rules.unnamed')}
                  </span>
                  {!rule.enabled && (
                    <Badge variant="outline" className="ml-2">
                      {t('routing.rules.off')}
                    </Badge>
                  )}
                  <div
                    className="font-mono text-xs [overflow-wrap:anywhere] text-muted-foreground"
                    translate="no"
                  >
                    {describeMatch(rule)}
                  </div>
                  {/* On a narrow screen the destination has no column of its own. */}
                  <div className="mt-1.5 lg:hidden">
                    <TargetBadge action={rule.action} chainId={rule.chain_id} chains={chains} />
                  </div>
                </TableCell>
                <TableCell className="hidden lg:table-cell">
                  <TargetBadge action={rule.action} chainId={rule.chain_id} chains={chains} />
                </TableCell>
                <TableCell>
                  <div className="flex items-center justify-end gap-1.5">
                    <Switch
                      checked={rule.enabled}
                      disabled={save.isPending}
                      aria-label={t('routing.rules.enabled', { number: index + 1 })}
                      onCheckedChange={(enabled) => toggle(index, enabled)}
                    />
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      disabled={index === 0 || save.isPending}
                      aria-label={t('routing.rules.up', { number: index + 1 })}
                      onClick={() => move(index, -1)}
                    >
                      <ArrowUp />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon-sm"
                      disabled={index === set.rules.length - 1 || save.isPending}
                      aria-label={t('routing.rules.down', { number: index + 1 })}
                      onClick={() => move(index, 1)}
                    >
                      <ArrowDown />
                    </Button>
                    <Button variant="outline" size="sm" onClick={() => setEditing({ id: rule.id })}>
                      {t('routing.rules.edit')}
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            ))}
            <TableRow>
              <TableCell className="tabular-nums">{set.rules.length + 1}</TableCell>
              <TableCell className="whitespace-normal">
                <span className="font-medium">{t('routing.rules.rest')}</span>
                <div className="text-xs text-muted-foreground">{t('routing.rules.restHint')}</div>
                <div className="mt-1.5 lg:hidden">
                  <TargetBadge
                    action={set.default_action}
                    chainId={set.default_chain_id}
                    chains={chains}
                  />
                </div>
              </TableCell>
              <TableCell className="hidden lg:table-cell">
                <TargetBadge
                  action={set.default_action}
                  chainId={set.default_chain_id}
                  chains={chains}
                />
              </TableCell>
              <TableCell className="text-right">
                <Button variant="outline" size="sm" onClick={onEdit}>
                  {t('routing.rules.edit')}
                </Button>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </Panel>

      <Panel title={t('routing.presets.title')}>
        <p className="max-w-3xl text-muted-foreground">{t('routing.presets.text')}</p>
        <div className="flex flex-wrap gap-2">
          {PRESETS.map((preset) => (
            <Button
              key={preset.key}
              variant="outline"
              size="sm"
              disabled={save.isPending}
              onClick={() => addPreset(preset)}
            >
              {t(`routing.presets.${preset.key}.add`)}
            </Button>
          ))}
        </div>
      </Panel>

      <Panel title={t('routing.howItWorks.title')}>
        <p className="max-w-3xl text-muted-foreground">{t('routing.howItWorks.text')}</p>
      </Panel>

      {editing !== null && (
        <RuleEditor
          key={editing === 'new' ? 'new' : editing.id}
          set={set}
          ruleId={editing === 'new' ? null : editing.id}
          chains={available}
          protocols={protocols}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void onRefresh()
          }}
        />
      )}
    </>
  )
}

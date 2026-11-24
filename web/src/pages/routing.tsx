import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowDown, ArrowUp } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { EntryApply } from '@/components/entry-apply'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
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
import { Textarea } from '@/components/ui/textarea'
import {
  api,
  ApiError,
  type Chain,
  type Rule,
  type RuleAction,
  type RuleCondition,
  type RuleConditionType,
  type RuleSet,
  type RuleSetRequest,
  type Server,
} from '@/lib/api'
import {
  chainsFrom,
  clientsPending,
  CONDITION_TYPES,
  describeMatch,
  guessCondition,
  parseTarget,
  PRESETS,
  targetValue,
  type Preset,
} from '@/lib/rules'
import { cn } from '@/lib/utils'

type Translate = (key: string, options?: Record<string, unknown>) => string

const KNOWN_ERRORS = ['name_taken', 'invalid_name', 'bad_entry', 'rule_set_in_use'] as const

function explain(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_rule') {
      const reason = String(error.details.reason ?? '')
      const index = Number(error.details.rule ?? 0)
      return t(`routing.errors.rule.${reason}`, {
        // -1 is the default route, which has no number.
        where: index < 0 ? t('routing.errors.inDefault') : t('routing.errors.inRule', { number: index + 1 }),
        value: String(error.details.value ?? ''),
        defaultValue: t('common.error'),
      })
    }
    if ((KNOWN_ERRORS as readonly string[]).includes(error.code)) {
      return t(`routing.errors.${error.code}`)
    }
  }
  return t('common.error')
}

const ruleSetsKey = ['rule-sets'] as const

function toRequest(set: RuleSet, change: Partial<RuleSetRequest> = {}): RuleSetRequest {
  return {
    name: set.name,
    entry_server_id: set.entry.id,
    rules: set.rules,
    default_action: set.default_action,
    default_chain_id: set.default_chain_id,
    ...change,
  }
}

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
            <div
              role="tablist"
              aria-label={t('routing.sets')}
              className="flex flex-wrap gap-1 self-start rounded-lg bg-muted p-1"
            >
              {sets.map((set) => (
                <button
                  key={set.id}
                  type="button"
                  role="tab"
                  aria-selected={set.id === current.id}
                  className={cn(
                    'rounded-md px-3 py-1.5 text-sm font-medium outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
                    set.id === current.id
                      ? 'bg-card shadow-sm'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                  onClick={() => setSelected(set.id)}
                >
                  {set.name}
                </button>
              ))}
            </div>
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
  // null: closed; 'new': adding; a number: editing the rule at that place.
  const [editing, setEditing] = useState<number | 'new' | null>(null)
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
                    <Button variant="outline" size="sm" onClick={() => setEditing(index)}>
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
          key={editing}
          set={set}
          index={editing === 'new' ? null : editing}
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

/** One select for "where to": the chains entered on this server, direct, block. */
function TargetSelect({
  id,
  value,
  chains,
  onChange,
}: {
  id: string
  value: string
  chains: Chain[]
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {chains.map((chain) => (
          <SelectItem key={chain.id} value={`chain:${chain.id}`}>
            {t('routing.target.chain', { name: chain.name })}
          </SelectItem>
        ))}
        <SelectItem value="direct">{t('routing.target.direct')}</SelectItem>
        <SelectItem value="block">{t('routing.target.block')}</SelectItem>
      </SelectContent>
    </Select>
  )
}

function RuleEditor({
  set,
  index,
  chains,
  protocols,
  onClose,
  onSaved,
}: {
  set: RuleSet
  /** The rule's place in the set; null for a new one. */
  index: number | null
  chains: Chain[]
  protocols: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const original = index === null ? null : set.rules[index]
  const [name, setName] = useState(original?.name ?? '')
  const [conditions, setConditions] = useState<RuleCondition[]>(
    original?.conditions ?? [{ type: 'domain', value: '' }],
  )
  const [ports, setPorts] = useState(original?.ports ?? '')
  const [protocol, setProtocol] = useState(original?.protocol ?? '')
  const [target, setTarget] = useState(
    original ? targetValue(original.action, original.chain_id) : 'direct',
  )
  const [enabled, setEnabled] = useState(original?.enabled ?? true)
  // The list being pasted; null while the box is closed.
  const [pasted, setPasted] = useState<string | null>(null)

  const save = useMutation({
    mutationFn: (rules: Rule[]) => api.updateRuleSet(set.id, toRequest(set, { rules })),
    onSuccess: onSaved,
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const rule: Rule = {
      id: original?.id ?? '',
      name: name.trim(),
      // A row left blank is a row not filled in, not a condition.
      conditions: conditions.filter((condition) => condition.value.trim() !== ''),
      ports,
      protocol,
      ...parseTarget(target),
      enabled,
    }
    save.mutate(
      index === null
        ? [...set.rules, rule]
        : set.rules.map((existing, at) => (at === index ? rule : existing)),
    )
  }
  const setCondition = (at: number, change: Partial<RuleCondition>) =>
    setConditions((current) =>
      current.map((condition, i) => (i === at ? { ...condition, ...change } : condition)),
    )
  const addPasted = () => {
    const added = (pasted ?? '')
      .split(/[\n,;]+/)
      .map(guessCondition)
      .filter((condition): condition is RuleCondition => condition !== null)
    setConditions((current) => [
      ...current.filter((condition) => condition.value.trim() !== ''),
      ...added,
    ])
    setPasted(null)
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-2xl">
        <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>
              {index === null
                ? t('routing.rule.titleNew')
                : t('routing.rule.titleEdit', { number: index + 1 })}
            </DialogTitle>
            <DialogDescription>{t('routing.rule.description')}</DialogDescription>
          </DialogHeader>

          <Field>
            <Label htmlFor="rule-name">{t('routing.rule.name')}</Label>
            <Input
              id="rule-name"
              autoComplete="off"
              placeholder={t('routing.rule.namePlaceholder')}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>

          <fieldset className="flex flex-col gap-2">
            <legend className="mb-2 font-medium">{t('routing.rule.conditions')}</legend>
            {conditions.map((condition, at) => (
              <div key={at} className="flex flex-wrap gap-2 sm:flex-nowrap">
                <Select
                  value={condition.type}
                  onValueChange={(type) => setCondition(at, { type: type as RuleConditionType })}
                >
                  <SelectTrigger
                    className="w-full sm:w-56"
                    aria-label={t('routing.rule.conditionType', { number: at + 1 })}
                  >
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {CONDITION_TYPES.map((type) => (
                      <SelectItem key={type} value={type}>
                        {t(`routing.conditionTypes.${type}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Input
                  className="min-w-0 flex-1 font-mono"
                  autoComplete="off"
                  spellCheck={false}
                  aria-label={t('routing.rule.conditionValue', { number: at + 1 })}
                  placeholder={t(`routing.conditionPlaceholders.${condition.type}`)}
                  value={condition.value}
                  onChange={(event) => setCondition(at, { value: event.target.value })}
                />
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => setConditions((current) => current.filter((_, i) => i !== at))}
                >
                  {t('routing.rule.removeCondition')}
                </Button>
              </div>
            ))}
            <div className="flex flex-wrap gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() =>
                  setConditions((current) => [...current, { type: 'domain', value: '' }])
                }
              >
                {t('routing.rule.addCondition')}
              </Button>
              {pasted === null && (
                <Button type="button" variant="outline" size="sm" onClick={() => setPasted('')}>
                  {t('routing.rule.paste')}
                </Button>
              )}
            </div>
            {pasted !== null && (
              <div className="flex flex-col gap-2 rounded-lg border p-3">
                <Label htmlFor="rule-paste">{t('routing.rule.pasteLabel')}</Label>
                <Textarea
                  id="rule-paste"
                  className="font-mono"
                  spellCheck={false}
                  value={pasted}
                  onChange={(event) => setPasted(event.target.value)}
                />
                <div className="flex gap-2">
                  <Button type="button" size="sm" onClick={addPasted}>
                    {t('routing.rule.pasteAdd')}
                  </Button>
                  <Button type="button" variant="outline" size="sm" onClick={() => setPasted(null)}>
                    {t('common.cancel')}
                  </Button>
                </div>
              </div>
            )}
            <span className="text-xs text-muted-foreground">{t('routing.rule.conditionsHint')}</span>
          </fieldset>

          <div className="grid gap-4 sm:grid-cols-2">
            <Field hint={t('routing.rule.portsHint')}>
              <Label htmlFor="rule-ports">{t('routing.rule.ports')}</Label>
              <Input
                id="rule-ports"
                className="font-mono"
                autoComplete="off"
                placeholder="443, 8000-8100"
                value={ports}
                onChange={(event) => setPorts(event.target.value)}
              />
            </Field>
            <Field hint={t('routing.rule.protocolHint')}>
              <Label htmlFor="rule-protocol">{t('routing.rule.protocol')}</Label>
              <Select
                value={protocol === '' ? 'any' : protocol}
                onValueChange={(value) => setProtocol(value === 'any' ? '' : value)}
              >
                <SelectTrigger id="rule-protocol" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="any">{t('routing.rule.protocolAny')}</SelectItem>
                  {protocols.map((item) => (
                    <SelectItem key={item} value={item}>
                      {item}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>

          <Field
            hint={chains.length === 0 ? t('routing.rule.noChains', { server: set.entry.name }) : undefined}
          >
            <Label htmlFor="rule-target">{t('routing.rule.target')}</Label>
            <TargetSelect id="rule-target" value={target} chains={chains} onChange={setTarget} />
          </Field>

          <label className="flex items-center justify-between gap-4">
            <span className="flex flex-col">
              <span className="font-medium">{t('routing.rule.enabled')}</span>
              <span className="text-xs text-muted-foreground">{t('routing.rule.enabledHint')}</span>
            </span>
            <Switch
              checked={enabled}
              aria-label={t('routing.rule.enabled')}
              onCheckedChange={setEnabled}
            />
          </label>

          {save.isError && <Notice title={explain(t, save.error)} />}
          <DialogFooter className="sm:justify-between">
            {index !== null ? (
              <Button
                type="button"
                variant="destructive"
                disabled={save.isPending}
                onClick={() => save.mutate(set.rules.filter((_, at) => at !== index))}
              >
                {t('routing.rule.delete')}
              </Button>
            ) : (
              <span />
            )}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={save.isPending}>
                {save.isPending ? t('routing.saving') : t('routing.save')}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function RuleSetEditor({
  set,
  servers,
  chains,
  onClose,
  onSaved,
}: {
  set: RuleSet | null
  servers: Server[]
  chains: Chain[]
  onClose: () => void
  /** The saved set, or null when it was deleted. */
  onSaved: (set: RuleSet | null) => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(set?.name ?? '')
  const [entry, setEntry] = useState(set?.entry.id ?? servers[0]?.id ?? 0)
  const [target, setTarget] = useState(
    set ? targetValue(set.default_action, set.default_chain_id) : 'direct',
  )
  const [presets, setPresets] = useState<Preset['key'][]>([])
  const available = chainsFrom(chains, entry)

  const save = useMutation({
    mutationFn: () => {
      const body: RuleSetRequest = {
        name: name.trim(),
        entry_server_id: entry,
        rules: set
          ? set.rules
          : PRESETS.filter((preset) => presets.includes(preset.key)).map((preset) => ({
              ...preset.rule,
              id: '',
              name: t(`routing.presets.${preset.key}.name`),
            })),
        ...(() => {
          const parsed = parseTarget(target)
          return { default_action: parsed.action, default_chain_id: parsed.chain_id }
        })(),
      }
      return set ? api.updateRuleSet(set.id, body) : api.createRuleSet(body)
    },
    onSuccess: onSaved,
  })
  const remove = useMutation({
    mutationFn: () => api.deleteRuleSet(set?.id ?? 0),
    onSuccess: () => onSaved(null),
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }
  const changeEntry = (id: number) => {
    setEntry(id)
    // A chain entered on the old server cannot be the default on the new one.
    if (target.startsWith('chain:') && !chainsFrom(chains, id).some((chain) => `chain:${chain.id}` === target)) {
      setTarget('direct')
    }
  }

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>
              {set ? t('routing.set.titleEdit', { name: set.name }) : t('routing.set.titleNew')}
            </DialogTitle>
            <DialogDescription>{t('routing.set.description')}</DialogDescription>
          </DialogHeader>

          <Field>
            <Label htmlFor="set-name">{t('routing.set.name')}</Label>
            <Input
              id="set-name"
              autoComplete="off"
              required
              placeholder={t('routing.set.namePlaceholder')}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>

          <Field hint={t('routing.set.entryHint')}>
            <Label htmlFor="set-entry">{t('routing.set.entry')}</Label>
            <Select value={String(entry)} onValueChange={(value) => changeEntry(Number(value))}>
              <SelectTrigger id="set-entry" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {servers.map((server) => (
                  <SelectItem key={server.id} value={String(server.id)}>
                    {server.name}
                    {server.location !== '' && ` · ${server.location}`}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          <Field
            hint={
              available.length === 0
                ? t('routing.set.noChains')
                : t('routing.set.defaultHint')
            }
          >
            <Label htmlFor="set-default">{t('routing.set.default')}</Label>
            <TargetSelect id="set-default" value={target} chains={available} onChange={setTarget} />
          </Field>

          {!set && (
            <fieldset className="flex flex-col gap-1.5">
              <legend className="mb-1.5 font-medium">{t('routing.set.presets')}</legend>
              {PRESETS.map((preset) => (
                <label key={preset.key} className="inline-flex cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={presets.includes(preset.key)}
                    onChange={(event) =>
                      setPresets((current) =>
                        event.target.checked
                          ? [...current, preset.key]
                          : current.filter((key) => key !== preset.key),
                      )
                    }
                  />
                  <span>{t(`routing.presets.${preset.key}.add`)}</span>
                </label>
              ))}
            </fieldset>
          )}

          {(save.isError || remove.isError) && (
            <Notice title={explain(t, save.error ?? remove.error)} />
          )}
          <DialogFooter className="sm:justify-between">
            {set ? (
              <Button
                type="button"
                variant="destructive"
                disabled={remove.isPending || set.subscriptions > 0}
                title={set.subscriptions > 0 ? t('routing.errors.rule_set_in_use') : undefined}
                onClick={() => remove.mutate()}
              >
                {t('routing.set.delete')}
              </Button>
            ) : (
              <span />
            )}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={save.isPending || entry === 0}>
                {save.isPending ? t('routing.saving') : t('routing.save')}
              </Button>
            </div>
          </DialogFooter>
          {set && set.subscriptions > 0 && (
            <span className="text-xs text-muted-foreground">
              {t('routing.set.deleteBlocked', { count: set.subscriptions })}
            </span>
          )}
        </form>
      </DialogContent>
    </Dialog>
  )
}

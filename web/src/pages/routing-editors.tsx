import { useMutation } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { ConfirmStrip } from '@/components/confirm-strip'
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
import { Textarea } from '@/components/ui/textarea'
import {
  api,
  type Chain,
  type Rule,
  type RuleCondition,
  type RuleConditionType,
  type RuleSet,
  type RuleSetRequest,
  type Server,
} from '@/lib/api'
import {
  chainsFrom,
  CONDITION_TYPES,
  guessCondition,
  parseTarget,
  PRESETS,
  targetValue,
  type Preset,
} from '@/lib/rules'

import { explain, toRequest } from '@/lib/routing'

/** One select for "where to": the chains entered on this server, direct, block. */
export function TargetSelect({
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

export function RuleEditor({
  set,
  ruleId,
  chains,
  protocols,
  onClose,
  onSaved,
}: {
  set: RuleSet
  /** The rule's id; null for a new one. */
  ruleId: string | null
  chains: Chain[]
  protocols: string[]
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const index = ruleId === null ? -1 : set.rules.findIndex((rule) => rule.id === ruleId)
  const original = index === -1 ? null : set.rules[index]
  // Deleted while this editor was open — in another tab, say.
  const gone = ruleId !== null && index === -1
  const [deleting, setDeleting] = useState(false)
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
      id: ruleId ?? '',
      name: name.trim(),
      // A row left blank is a row not filled in, not a condition.
      conditions: conditions.filter((condition) => condition.value.trim() !== ''),
      ports,
      protocol,
      ...parseTarget(target),
      enabled,
    }
    save.mutate(
      ruleId === null
        ? [...set.rules, rule]
        : set.rules.map((existing) => (existing.id === ruleId ? rule : existing)),
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
              {ruleId === null
                ? t('routing.rule.titleNew')
                : gone
                  ? t('routing.rule.titleGone')
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

          {gone && <Notice title={t('routing.rule.gone')} />}
          {deleting && (
            <ConfirmStrip
              text={t('routing.rule.deleteConfirm')}
              pending={save.isPending}
              onConfirm={() => save.mutate(set.rules.filter((rule) => rule.id !== ruleId))}
              onCancel={() => setDeleting(false)}
            />
          )}
          {save.isError && <Notice title={explain(t, save.error)} />}
          <DialogFooter className="sm:justify-between">
            {ruleId !== null && !gone ? (
              <Button
                type="button"
                variant="destructive"
                disabled={save.isPending}
                onClick={() => setDeleting(true)}
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
              <Button type="submit" disabled={save.isPending || gone}>
                {save.isPending ? t('routing.saving') : t('routing.save')}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function RuleSetEditor({
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
  const [deleting, setDeleting] = useState(false)

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

          {set && deleting && (
            <ConfirmStrip
              text={t('routing.set.deleteConfirm', { name: set.name })}
              pending={remove.isPending}
              onConfirm={() => remove.mutate()}
              onCancel={() => setDeleting(false)}
            />
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
                onClick={() => setDeleting(true)}
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

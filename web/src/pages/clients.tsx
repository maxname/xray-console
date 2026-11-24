import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { QRCodeSVG } from 'qrcode.react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { EntryApply } from '@/components/entry-apply'
import { EmptyState, Page, PageHeader, Panel } from '@/components/page'
import { Segmented } from '@/components/segmented'
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
import {
  api,
  type ChainServer,
  type RuleSet,
  type Subscription,
  type SubscriptionRequest,
} from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'
import { copyText } from '@/lib/clipboard'
import { formatBytes, formatDate, formatDayTime } from '@/lib/format'
import { clientsPending, devicesKey } from '@/lib/rules'
import { toast } from '@/lib/toast'
import { useMe } from '@/lib/use-me'
import { DevicesPanel, LocalClientPanel } from '@/pages/devices'

const KNOWN_ERRORS = ['invalid_name', 'bad_rule_set'] as const

function explain(t: Translate, error: unknown): string {
  return explainKnown(t, error, 'clients', KNOWN_ERRORS)
}

const subscriptionsKey = ['subscriptions'] as const

/** A Unix time as the value of a date input, and back: through the end of that day. */
function toDateInput(unixSeconds: number | null): string {
  if (unixSeconds === null) {
    return ''
  }
  const date = new Date(unixSeconds * 1000)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
}

function fromDateInput(value: string): number | null {
  if (value === '') {
    return null
  }
  const [year, month, day] = value.split('-').map(Number)
  // "Until the 31st" includes the 31st.
  return Math.floor(new Date(year, month - 1, day, 23, 59, 59).getTime() / 1000)
}

function copy(t: Translate, text: string) {
  void copyText(text).then((ok) => {
    toast(
      ok
        ? { title: t('common.copied'), variant: 'success' }
        : { title: t('common.copyFailed'), variant: 'destructive' },
    )
  })
}

export function ClientsPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const me = useMe()
  // null: closed; 'new': creating; a subscription: editing it.
  const [editing, setEditing] = useState<Subscription | 'new' | null>(null)
  const [showing, setShowing] = useState<Subscription | null>(null)

  const query = useQuery({
    queryKey: subscriptionsKey,
    queryFn: () => api.listSubscriptions(),
    refetchInterval: (q) =>
      q.state.data?.subscriptions.some((sub) => sub.entry?.busy) ? 1500 : 30_000,
  })
  const ruleSets = useQuery({ queryKey: ['rule-sets'], queryFn: () => api.listRuleSets() })
  const devices = useQuery({
    queryKey: devicesKey,
    queryFn: () => api.listDevices(),
    // A job on a device moves through its steps in seconds; otherwise the
    // panel looks at devices once a minute.
    refetchInterval: (q) => {
      const data = q.state.data
      const moving =
        data?.devices.some((device) => device.status === 'working' || device.entry?.busy) ||
        data?.local.entry?.busy
      return moving ? 1500 : 20_000
    },
  })

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: subscriptionsKey }),
      queryClient.invalidateQueries({ queryKey: devicesKey }),
    ])
  // The address apps reach the panel by is the one given at setup; the
  // address in this browser may be a tunnel to it.
  const origin = (me.data?.panel_url || window.location.origin).replace(/\/+$/, '')
  const urlOf = (sub: Subscription) => origin + sub.path

  if (query.isError) {
    return (
      <Page>
        <PageHeader title={t('clients.title')} />
        <EmptyState title={t('clients.loadFailedTitle')} description={t('servers.loadFailed')}>
          <Button variant="outline" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </EmptyState>
      </Page>
    )
  }

  const subs = query.data?.subscriptions ?? []
  const sets = ruleSets.data?.rule_sets ?? []
  // One banner per entry server that does not yet match its clients, of
  // whatever kind.
  const waiting = new Map<number, ChainServer>()
  const entries = [
    ...subs.map((sub) => sub.entry),
    ...(devices.data?.devices.map((device) => device.entry) ?? []),
    devices.data?.local.entry ?? null,
  ]
  for (const entry of entries) {
    if (entry && clientsPending(entry)) {
      waiting.set(entry.id, entry)
    }
  }

  return (
    <Page>
      <PageHeader
        title={t('clients.title')}
        description={t('clients.subtitle')}
        actions={
          <Button disabled={sets.length === 0} onClick={() => setEditing('new')}>
            {t('clients.create')}
          </Button>
        }
      />

      {[...waiting.values()].map((entry) => (
        <EntryApply key={entry.id} entry={entry} text={t('clients.pending')} onRefresh={refresh} />
      ))}

      {devices.data && ruleSets.data && (
        <>
          <LocalClientPanel
            // The form starts from what is saved; saved anew, it starts again.
            key={`${devices.data.local.rule_set_id}:${devices.data.local.socks_port}:${devices.data.local.http_port}:${sets.length}`}
            local={devices.data.local}
            sets={sets}
          />
          <DevicesPanel devices={devices.data.devices} sets={sets} />
        </>
      )}

      {!query.data ? (
        <Panel>
          <Skeleton className="h-5 w-56" />
          <Skeleton className="h-20 w-full" />
        </Panel>
      ) : subs.length === 0 ? (
        <EmptyState
          title={t('clients.subscriptions.emptyTitle')}
          description={
            sets.length === 0 && ruleSets.data
              ? t('clients.needRuleSet')
              : t('clients.subscriptions.emptyDescription')
          }
        >
          {sets.length === 0 && ruleSets.data ? (
            <Button asChild variant="outline">
              <Link to="/routing">{t('clients.toRouting')}</Link>
            </Button>
          ) : (
            <Button onClick={() => setEditing('new')}>{t('clients.create')}</Button>
          )}
        </EmptyState>
      ) : (
        <Panel className="gap-3">
          <div className="flex flex-col gap-1">
            <h2 className="font-heading text-base font-semibold">
              {t('clients.subscriptions.title')}
            </h2>
            <p className="text-muted-foreground">{t('clients.subscriptions.text')}</p>
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t('clients.columns.who')}</TableHead>
                <TableHead>{t('clients.columns.state')}</TableHead>
                <TableHead>{t('clients.columns.until')}</TableHead>
                <TableHead className="hidden text-right md:table-cell">
                  {t('clients.columns.traffic')}
                </TableHead>
                <TableHead className="hidden lg:table-cell">
                  {t('clients.columns.fetched')}
                </TableHead>
                <TableHead className="text-right">{t('clients.columns.link')}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {subs.map((sub) => (
                <TableRow key={sub.id}>
                  <TableCell>
                    <button
                      type="button"
                      className="font-medium text-primary underline-offset-4 hover:underline"
                      onClick={() => setEditing(sub)}
                    >
                      {sub.name}
                    </button>
                    <div className="text-xs text-muted-foreground">
                      {sub.rule_set_name}
                      {sub.entry && ` · ${sub.entry.name}`}
                    </div>
                  </TableCell>
                  <TableCell>
                    <StateCell sub={sub} />
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {sub.expires_at === null ? t('clients.forever') : formatDate(sub.expires_at)}
                  </TableCell>
                  <TableCell className="hidden text-right tabular-nums md:table-cell">
                    {sub.traffic.up + sub.traffic.down === 0
                      ? '—'
                      : formatBytes(sub.traffic.up + sub.traffic.down)}
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground tabular-nums lg:table-cell">
                    {sub.fetched_at === null ? t('clients.neverFetched') : formatDayTime(sub.fetched_at)}
                  </TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-1.5">
                      <Button variant="outline" size="sm" onClick={() => setShowing(sub)}>
                        {t('clients.qr')}
                      </Button>
                      <Button variant="outline" size="sm" onClick={() => copy(t, urlOf(sub))}>
                        {t('common.copy')}
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Panel>
      )}

      {query.data && subs.length > 0 && (
        <Panel title={t('clients.howItWorks.title')}>
          <p className="max-w-3xl text-muted-foreground">{t('clients.howItWorks.text')}</p>
        </Panel>
      )}

      {editing !== null && (
        <SubscriptionEditor
          key={editing === 'new' ? 'new' : editing.id}
          sub={editing === 'new' ? null : editing}
          sets={sets}
          url={editing === 'new' ? '' : urlOf(editing)}
          onClose={() => setEditing(null)}
          onChanged={(next) => {
            // A reissue keeps the dialog open on the new link.
            setEditing(next)
            void refresh()
          }}
          onSaved={() => {
            setEditing(null)
            void refresh()
          }}
        />
      )}
      {showing !== null && (
        <LinkDialog sub={showing} url={urlOf(showing)} onClose={() => setShowing(null)} />
      )}
    </Page>
  )
}

function StateCell({ sub }: { sub: Subscription }) {
  const { t } = useTranslation()
  const variants = { active: 'success', disabled: 'secondary', expired: 'warning' } as const
  // What the server does and what the subscription says differ until an apply.
  const note =
    sub.entry === null
      ? null
      : sub.state === 'active' && !sub.on_server
        ? t('clients.notOnServer')
        : sub.state !== 'active' && sub.on_server
          ? t('clients.stillOnServer')
          : null
  return (
    <>
      <Badge variant={variants[sub.state]}>{t(`clients.state.${sub.state}`)}</Badge>
      {note !== null && <div className="mt-1 text-xs text-warning">{note}</div>}
    </>
  )
}

/** The link as a QR code for a phone, with the direct connection behind a tab. */
function LinkDialog({
  sub,
  url,
  onClose,
}: {
  sub: Subscription
  url: string
  onClose: () => void
}) {
  const { t } = useTranslation()
  const [kind, setKind] = useState<'subscription' | 'direct'>('subscription')
  const value = kind === 'subscription' ? url : sub.link

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="text-sm">
        <DialogHeader>
          <DialogTitle>{t('clients.link.title', { name: sub.name })}</DialogTitle>
          <DialogDescription>{t(`clients.link.${kind}Text`)}</DialogDescription>
        </DialogHeader>

        {sub.link !== '' && (
          <Segmented
            label={t('clients.link.kind')}
            className="justify-self-start"
            options={(['subscription', 'direct'] as const).map((option) => ({
              value: option,
              label: t(`clients.link.${option}`),
            }))}
            value={kind}
            onChange={setKind}
          />
        )}

        {/* White behind the code in both themes: scanners want dark-on-light. */}
        <div className="justify-self-center rounded-lg bg-white p-3">
          <QRCodeSVG value={value} size={224} role="img" aria-label={t('clients.link.qrLabel')} />
        </div>
        <code
          className="max-h-24 overflow-y-auto rounded-lg bg-muted px-2.5 py-1.5 font-mono text-xs break-all select-all"
          translate="no"
        >
          {value}
        </code>
        {sub.state !== 'active' && <Notice tone="warning" title={t('clients.link.inactive')} />}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t('common.close')}
          </Button>
          <Button onClick={() => copy(t, value)}>{t('common.copy')}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function SubscriptionEditor({
  sub,
  sets,
  url,
  onClose,
  onChanged,
  onSaved,
}: {
  sub: Subscription | null
  sets: RuleSet[]
  url: string
  onClose: () => void
  onChanged: (sub: Subscription) => void
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const [name, setName] = useState(sub?.name ?? '')
  const [ruleSet, setRuleSet] = useState(sub?.rule_set_id ?? sets[0]?.id ?? 0)
  const [until, setUntil] = useState(toDateInput(sub?.expires_at ?? null))
  const [enabled, setEnabled] = useState(sub?.enabled ?? true)
  const [confirming, setConfirming] = useState<'reissue' | 'delete' | null>(null)

  const save = useMutation({
    mutationFn: () => {
      const body: SubscriptionRequest = {
        name: name.trim(),
        rule_set_id: ruleSet,
        enabled,
        expires_at: fromDateInput(until),
      }
      return sub ? api.updateSubscription(sub.id, body) : api.createSubscription(body)
    },
    onSuccess: onSaved,
  })
  const reissue = useMutation({
    mutationFn: () => api.reissueSubscription(sub?.id ?? 0),
    onSuccess: (next) => {
      setConfirming(null)
      onChanged(next)
    },
  })
  const remove = useMutation({
    mutationFn: () => api.deleteSubscription(sub?.id ?? 0),
    onSuccess: onSaved,
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }
  const failure = save.error ?? reissue.error ?? remove.error

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>
              {sub ? t('clients.editor.titleEdit', { name: sub.name }) : t('clients.editor.titleNew')}
            </DialogTitle>
            <DialogDescription>{t('clients.editor.description')}</DialogDescription>
          </DialogHeader>

          <Field>
            <Label htmlFor="sub-name">{t('clients.editor.name')}</Label>
            <Input
              id="sub-name"
              autoComplete="off"
              required
              placeholder={t('clients.editor.namePlaceholder')}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>

          <Field hint={t('clients.editor.ruleSetHint')}>
            <Label htmlFor="sub-rule-set">{t('clients.editor.ruleSet')}</Label>
            <Select value={String(ruleSet)} onValueChange={(value) => setRuleSet(Number(value))}>
              <SelectTrigger id="sub-rule-set" className="w-full">
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

          <Field hint={t('clients.editor.untilHint')}>
            <Label htmlFor="sub-until">{t('clients.editor.until')}</Label>
            <Input
              id="sub-until"
              type="date"
              className="w-48"
              value={until}
              onChange={(event) => setUntil(event.target.value)}
            />
          </Field>

          {sub && (
            <>
              <Field hint={t('clients.editor.reissueHint')}>
                <span className="font-medium">{t('clients.editor.link')}</span>
                <div className="flex flex-wrap items-center gap-2">
                  <code
                    className="min-w-0 flex-1 basis-56 rounded-lg bg-muted px-2.5 py-1.5 font-mono text-xs break-all select-all"
                    translate="no"
                  >
                    {url}
                  </code>
                  <Button type="button" variant="outline" size="sm" onClick={() => copy(t, url)}>
                    {t('common.copy')}
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setConfirming('reissue')}
                  >
                    {t('clients.editor.reissue')}
                  </Button>
                </div>
              </Field>

              <label className="flex items-center justify-between gap-4">
                <span className="flex flex-col">
                  <span className="font-medium">{t('clients.editor.enabled')}</span>
                  <span className="text-xs text-muted-foreground">
                    {t('clients.editor.enabledHint')}
                  </span>
                </span>
                <Switch
                  checked={enabled}
                  aria-label={t('clients.editor.enabled')}
                  onCheckedChange={setEnabled}
                />
              </label>
            </>
          )}

          {confirming !== null && (
            <div className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2.5">
              <span>{t(`clients.editor.${confirming}Confirm`)}</span>
              <div className="flex gap-2">
                <Button
                  type="button"
                  size="sm"
                  variant={confirming === 'delete' ? 'destructive' : 'default'}
                  disabled={reissue.isPending || remove.isPending}
                  onClick={() => (confirming === 'delete' ? remove.mutate() : reissue.mutate())}
                >
                  {t(`clients.editor.${confirming}Yes`)}
                </Button>
                <Button type="button" size="sm" variant="outline" onClick={() => setConfirming(null)}>
                  {t('common.cancel')}
                </Button>
              </div>
            </div>
          )}

          {failure !== null && <Notice title={explain(t, failure)} />}
          <DialogFooter className="sm:justify-between">
            {sub ? (
              <Button type="button" variant="destructive" onClick={() => setConfirming('delete')}>
                {t('clients.editor.delete')}
              </Button>
            ) : (
              <span />
            )}
            <div className="flex flex-col-reverse gap-2 sm:flex-row">
              <Button type="button" variant="outline" onClick={onClose}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={save.isPending || ruleSet === 0}>
                {save.isPending ? t('routing.saving') : t('routing.save')}
              </Button>
            </div>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

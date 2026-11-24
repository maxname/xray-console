import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
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
  api,
  type AlertSettings,
  type AlertTestResult,
} from '@/lib/api'
import { formatPanelClock } from '@/lib/format'
import { useMe } from '@/lib/use-me'
import { cn } from '@/lib/utils'

import { explainAlert } from '@/lib/alerts'

export function AlertsDialog({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['alerts'], queryFn: () => api.getAlerts(), staleTime: 0 })

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-xl">
        {query.data ? (
          <AlertsForm settings={query.data} onClose={onClose} />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>{t('alerts.title')}</DialogTitle>
              <DialogDescription>{t('alerts.description')}</DialogDescription>
            </DialogHeader>
            {query.isError ? (
              <Notice title={t('common.loadFailed')} />
            ) : (
              <Skeleton className="h-40 w-full" />
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

export function AlertsForm({ settings, onClose }: { settings: AlertSettings; onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data: me } = useMe()
  const [form, setForm] = useState(settings)
  // The token is never sent back, so the field starts empty: left empty it
  // keeps what is saved, unless removal was asked for.
  const [token, setToken] = useState('')
  const [removeToken, setRemoveToken] = useState(false)
  const [tested, setTested] = useState<AlertTestResult | null>(null)

  const set = (change: Partial<AlertSettings>) => setForm((current) => ({ ...current, ...change }))
  const body = () => ({
    enabled: form.enabled,
    telegram_token: removeToken ? '' : token.trim() !== '' ? token.trim() : null,
    telegram_chat: removeToken ? '' : form.telegram_chat,
    webhook_url: form.webhook_url,
    on_down: form.on_down,
    on_up: form.on_up,
    on_update: form.on_update,
    on_cert: form.on_cert,
    down_after_minutes: form.down_after_minutes,
    repeat_hours: form.repeat_hours,
    quiet_hours: form.quiet_hours,
  })
  const saved = (next: AlertSettings) => {
    queryClient.setQueryData(['alerts'], next)
    setForm(next)
    setToken('')
    setRemoveToken(false)
  }

  const save = useMutation({
    mutationFn: () => api.updateAlerts(body()),
    onSuccess: (next) => {
      saved(next)
      onClose()
    },
  })
  // A test is of what is on screen: save first, then send.
  const test = useMutation({
    mutationFn: async () => {
      saved(await api.updateAlerts(body()))
      return api.testAlerts()
    },
    onMutate: () => setTested(null),
    onSuccess: setTested,
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    save.mutate()
  }
  const busy = save.isPending || test.isPending
  const hasToken = (form.telegram_token_set && !removeToken) || token.trim() !== ''
  const failure = save.error ?? test.error

  return (
    <form className="grid gap-4 text-sm" onSubmit={handleSubmit}>
      <DialogHeader>
        <DialogTitle>{t('alerts.title')}</DialogTitle>
        <DialogDescription>{t('alerts.description')}</DialogDescription>
      </DialogHeader>

      <fieldset className="grid gap-3 rounded-lg border p-3">
        <legend className="px-1 font-medium">{t('alerts.telegram.title')}</legend>
        <Field
          hint={
            form.telegram_token_set && !removeToken
              ? t('alerts.telegram.tokenSaved')
              : t('alerts.telegram.tokenHint')
          }
        >
          <Label htmlFor="alerts-token">{t('alerts.telegram.token')}</Label>
          <div className="flex gap-2">
            <Input
              id="alerts-token"
              className="min-w-0 flex-1 font-mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              disabled={removeToken}
              placeholder={form.telegram_token_set && !removeToken ? '••••••••' : '123456789:AA…'}
              value={token}
              onChange={(event) => setToken(event.target.value)}
            />
            {form.telegram_token_set && (
              <Button type="button" variant="outline" onClick={() => setRemoveToken((value) => !value)}>
                {removeToken ? t('alerts.telegram.keep') : t('alerts.telegram.remove')}
              </Button>
            )}
          </div>
        </Field>
        <Field hint={t('alerts.telegram.chatHint')}>
          <Label htmlFor="alerts-chat">{t('alerts.telegram.chat')}</Label>
          <Input
            id="alerts-chat"
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
            disabled={removeToken}
            placeholder="-1001234567890"
            value={removeToken ? '' : form.telegram_chat}
            onChange={(event) => set({ telegram_chat: event.target.value })}
          />
        </Field>
      </fieldset>

      <Field hint={t('alerts.webhook.hint')}>
        <Label htmlFor="alerts-webhook">{t('alerts.webhook.url')}</Label>
        <Input
          id="alerts-webhook"
          className="font-mono"
          type="url"
          autoComplete="off"
          spellCheck={false}
          placeholder="https://hooks.example.net/xray"
          value={form.webhook_url}
          onChange={(event) => set({ webhook_url: event.target.value })}
        />
      </Field>

      <fieldset className="flex flex-col gap-1.5">
        <legend className="mb-1.5 font-medium">{t('alerts.about')}</legend>
        {(
          [
            ['on_down', 'down'],
            ['on_up', 'up'],
            ['on_update', 'update'],
            ['on_cert', 'cert'],
          ] as const
        ).map(([key, label]) => (
          <label key={key} className="inline-flex cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={form[key]}
              onChange={(event) => set({ [key]: event.target.checked })}
            />
            <span>{t(`alerts.events.${label}`)}</span>
          </label>
        ))}
      </fieldset>

      <div className="grid gap-4 sm:grid-cols-2">
        <Field hint={t('alerts.afterHint')}>
          <Label htmlFor="alerts-after">{t('alerts.after')}</Label>
          <Input
            id="alerts-after"
            type="number"
            min={1}
            max={1440}
            className="w-28 tabular-nums"
            value={form.down_after_minutes}
            onChange={(event) => set({ down_after_minutes: Number(event.target.value) })}
          />
        </Field>
        <Field>
          <Label htmlFor="alerts-repeat">{t('alerts.repeat.label')}</Label>
          <Select
            value={String(form.repeat_hours)}
            onValueChange={(value) => set({ repeat_hours: Number(value) })}
          >
            <SelectTrigger id="alerts-repeat" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="0">{t('alerts.repeat.0')}</SelectItem>
              <SelectItem value="1">{t('alerts.repeat.1')}</SelectItem>
              <SelectItem value="6">{t('alerts.repeat.6')}</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>

      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('alerts.quiet.title')}</span>
          <span className="text-xs text-muted-foreground">
            {t('alerts.quiet.hint', {
              clock: me ? formatPanelClock(me.timezone, me.utc_offset_minutes) : '…',
            })}
          </span>
        </span>
        <Switch
          checked={form.quiet_hours}
          aria-label={t('alerts.quiet.title')}
          onCheckedChange={(quiet_hours) => set({ quiet_hours })}
        />
      </label>
      <label className="flex items-center justify-between gap-4">
        <span className="flex flex-col">
          <span className="font-medium">{t('alerts.enabled.title')}</span>
          <span className="text-xs text-muted-foreground">{t('alerts.enabled.hint')}</span>
        </span>
        <Switch
          checked={form.enabled}
          aria-label={t('alerts.enabled.title')}
          onCheckedChange={(enabled) => set({ enabled })}
        />
      </label>

      {tested !== null && (
        <div role="status" className="flex flex-col gap-1 rounded-lg bg-muted px-3 py-2.5">
          {(['telegram', 'webhook'] as const)
            .filter((channel) => tested[channel] !== '')
            .map((channel) => (
              <span key={channel} className={cn(tested[channel] !== 'ok' && 'text-destructive')}>
                {tested[channel] === 'ok'
                  ? t(`alerts.test.${channel}Ok`)
                  : t(`alerts.test.${channel}Failed`)}
                {tested[channel] !== 'ok' && (
                  <code className="block font-mono text-xs break-all" translate="no">
                    {tested[channel]}
                  </code>
                )}
              </span>
            ))}
        </div>
      )}
      {failure !== null && <Notice title={explainAlert(t, failure)} />}

      <DialogFooter className="sm:justify-between">
        <Button
          type="button"
          variant="outline"
          disabled={busy || (!hasToken && form.webhook_url.trim() === '')}
          onClick={() => test.mutate()}
        >
          {test.isPending ? t('alerts.test.sending') : t('alerts.test.send')}
        </Button>
        <div className="flex flex-col-reverse gap-2 sm:flex-row">
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
          <Button type="submit" disabled={busy}>
            {save.isPending ? t('routing.saving') : t('routing.save')}
          </Button>
        </div>
      </DialogFooter>
    </form>
  )
}

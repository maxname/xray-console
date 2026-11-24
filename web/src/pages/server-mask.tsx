import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { MaskField, type MaskValue } from '@/components/mask-field'
import { Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  api,
  ApiError,
  type Server,
  type ServerCert,
  type ServerConfig,
  type UpdateConfigRequest,
} from '@/lib/api'
import { daysLeft } from '@/lib/domain'
import { explained } from '@/lib/failures'
import { formatDate } from '@/lib/format'
import { toast } from '@/lib/toast'
import { useNow } from '@/lib/use-now'

/**
 * What the server passes itself off as: its own domain — with the certificate
 * strangers are shown for it — or someone else's site. Changing it is an edit
 * like any other: nothing reaches the server until it is applied, and the
 * apply is what sets the stub site up or takes it away.
 */
export function MaskPanel({
  server,
  config,
  disabled,
  onUpdate,
}: {
  server: Server
  config: ServerConfig
  disabled: boolean
  /** Saves the edit; resolves to the sentence for a refusal, or to null. */
  onUpdate: (body: UpdateConfigRequest) => Promise<string | null>
}) {
  const { t } = useTranslation()
  const [editing, setEditing] = useState(false)
  const own = config.domain !== ''
  // The certificate belongs to the domain the server runs; one that is still
  // waiting to be applied has none yet.
  const applied = own && config.domain === config.running_domain
  const cert = applied && server.cert?.domain === config.domain ? server.cert : null

  return (
    <Panel title={t('mask.title')}>
      <div className="flex flex-col gap-0.5">
        <span className="text-xs text-muted-foreground">
          {own ? t('mask.ownDomain') : t('mask.otherSite')}
        </span>
        <span className="font-mono text-base break-all" translate="no">
          {config.mask_host}
        </span>
      </div>

      {own && !applied && <p className="text-warning">{t('mask.pendingDomain')}</p>}
      {!own && config.running_domain !== '' && (
        <p className="text-warning">{t('mask.pendingRemoval', { domain: config.running_domain })}</p>
      )}
      {!own && config.running_domain === '' && (
        <p className="text-muted-foreground">{t('mask.otherSiteHint')}</p>
      )}
      {applied && <CertStatus cert={cert} />}

      <div className="flex flex-wrap gap-2">
        <Button variant="outline" size="sm" disabled={disabled} onClick={() => setEditing(true)}>
          {t('mask.change')}
        </Button>
        {applied && <RenewButton server={server} disabled={disabled} />}
      </div>
      {applied && <p className="text-xs text-muted-foreground">{t('mask.renewHint')}</p>}

      <Dialog open={editing} onOpenChange={setEditing}>
        <DialogContent className="max-w-xl">
          <DialogHeader>
            <DialogTitle>{t('mask.dialog.title', { name: server.name })}</DialogTitle>
            <DialogDescription>{t('mask.dialog.description')}</DialogDescription>
          </DialogHeader>
          <MaskForm
            server={server}
            config={config}
            onUpdate={onUpdate}
            onDone={() => setEditing(false)}
          />
        </DialogContent>
      </Dialog>
    </Panel>
  )
}

/** When the certificate strangers are shown runs out, as the panel last saw it. */
function CertStatus({ cert }: { cert: ServerCert | null }) {
  const { t } = useTranslation()
  const now = useNow()
  if (cert === null) {
    return <p className="text-muted-foreground">{t('mask.cert.unseen')}</p>
  }
  const left = daysLeft(cert, now)
  if (cert.not_after === null || left === null) {
    return <p className="text-warning">{t('mask.cert.unread', { error: cert.error })}</p>
  }
  const date = formatDate(cert.not_after)
  return (
    <div className="flex flex-col gap-1">
      {left < 0 ? (
        <p className="font-semibold text-destructive">{t('mask.cert.expired', { date })}</p>
      ) : (
        <p className={cert.overdue ? 'font-semibold text-warning' : undefined}>
          {t('mask.cert.until', { date, count: left })}
          {cert.issuer !== '' && (
            <span className="text-muted-foreground"> · {cert.issuer}</span>
          )}
        </p>
      )}
      {cert.overdue && left >= 0 && <p className="text-warning">{t('mask.cert.overdue')}</p>}
      {cert.error !== '' && (
        <p className="text-xs text-muted-foreground">{t('mask.cert.problem', { error: cert.error })}</p>
      )}
    </div>
  )
}

/** Codes a renewal by hand can fail with that the catalogue explains. */
const RENEW_ERRORS = ['cert_failed', 'cert_rate_limited', 'ssh_unreachable', 'ssh_key_rejected'] as const

function RenewButton({ server, disabled }: { server: Server; disabled: boolean }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const renew = useMutation({
    mutationFn: () => api.renewCert(server.id),
    onSuccess: ({ not_after: until }) => {
      toast({
        title: t('mask.renewed'),
        description: until > 0 ? t('mask.renewedUntil', { date: formatDate(until) }) : undefined,
        variant: 'success',
      })
      // The panel looks at the server again by itself; give it a moment.
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
      window.setTimeout(() => void queryClient.invalidateQueries({ queryKey: ['servers'] }), 4000)
    },
    onError: (error) => {
      const code = error instanceof ApiError ? error.code : ''
      toast({
        title: t('mask.renewFailed'),
        description: (RENEW_ERRORS as readonly string[]).includes(code)
          ? t(`server.errors.${explained(code)}.title`)
          : code === 'busy'
            ? t('fieldErrors.busy')
            : undefined,
        variant: 'destructive',
      })
    },
  })
  return (
    <Button
      variant="outline"
      size="sm"
      disabled={disabled || renew.isPending}
      onClick={() => renew.mutate()}
    >
      {renew.isPending ? t('mask.renewing') : t('mask.renew')}
    </Button>
  )
}

/** The dialog's form; its own component so that closing forgets what was typed. */
function MaskForm({
  server,
  config,
  onUpdate,
  onDone,
}: {
  server: Server
  config: ServerConfig
  onUpdate: (body: UpdateConfigRequest) => Promise<string | null>
  onDone: () => void
}) {
  const { t } = useTranslation()
  const [value, setValue] = useState<MaskValue>({
    maskMode: config.domain !== '' ? 'domain' : 'site',
    domain: config.domain,
    maskHost: config.domain !== '' ? '' : config.mask_host,
  })
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    setSaving(true)
    const body =
      value.maskMode === 'domain'
        ? { domain: value.domain.trim() }
        : { mask_host: value.maskHost.trim() }
    void onUpdate(body)
      .then((refusal) => {
        setError(refusal)
        if (refusal === null) {
          onDone()
        }
      })
      .finally(() => setSaving(false))
  }

  return (
    <form className="flex flex-col gap-4" onSubmit={handleSubmit}>
      <MaskField value={value} onChange={setValue} host={server.host} name="server-mask" />
      <p className="text-xs text-muted-foreground">
        {value.maskMode === 'domain' ? t('mask.dialog.domainNote') : t('mask.dialog.siteNote')}
      </p>
      {error !== null && (
        <span role="alert" className="text-sm text-destructive">
          {error}
        </span>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onDone}>
          {t('common.cancel')}
        </Button>
        <Button type="submit" disabled={saving}>
          {t('mask.dialog.save')}
        </Button>
      </DialogFooter>
    </form>
  )
}

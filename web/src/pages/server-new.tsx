import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { AccessFields, MaskField, TemplateField } from '@/components/install-fields'
import { Page, PageHeader, Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, ApiError, type HostKeyProbe } from '@/lib/api'
import {
  initialInstallForm,
  isKnownFormError,
  toInstallOptions,
} from '@/lib/install-form'

export function ServerNewPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()

  const [name, setName] = useState('')
  const [location, setLocation] = useState('')
  const [host, setHost] = useState('')
  const [port, setPort] = useState('22')
  const [user, setUser] = useState('root')
  const [install, setInstall] = useState(initialInstallForm)
  // The key the server presented, and whether the administrator vouched for it.
  const [probe, setProbe] = useState<HostKeyProbe | null>(null)
  const [confirmed, setConfirmed] = useState(false)

  const probeMutation = useMutation({
    mutationFn: () => api.probeServer(host.trim(), Number(port)),
    onSuccess: (result) => {
      setProbe(result)
      setConfirmed(false)
    },
  })

  // A fingerprint belongs to one address: change the address and it is void.
  const resetProbe = () => {
    setProbe(null)
    setConfirmed(false)
    probeMutation.reset()
  }

  const createMutation = useMutation({
    mutationFn: () =>
      api.createServer({
        name: name.trim(),
        location: location.trim(),
        host: host.trim(),
        ssh_port: Number(port),
        ssh_user: user.trim(),
        host_key: probe?.host_key ?? '',
        ...toInstallOptions(install),
      }),
    onSuccess: ({ server }) => {
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
      void navigate({
        to: '/servers/$serverId',
        params: { serverId: String(server.id) },
      })
    },
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    createMutation.mutate()
  }

  const formError = (() => {
    if (!createMutation.isError) {
      return null
    }
    const error = createMutation.error
    if (error instanceof ApiError && isKnownFormError(error.code)) {
      return t(`serverNew.errors.${error.code}`)
    }
    return t('common.error')
  })()

  const probeError = (() => {
    if (!probeMutation.isError) {
      return null
    }
    const error = probeMutation.error
    if (error instanceof ApiError && error.code === 'ssh_unreachable') {
      return {
        title: t('serverNew.fingerprint.unreachableTitle'),
        text: t('serverNew.fingerprint.unreachable'),
      }
    }
    if (error instanceof ApiError && isKnownFormError(error.code)) {
      return { title: t(`serverNew.errors.${error.code}`), text: undefined }
    }
    return { title: t('common.error'), text: undefined }
  })()

  const canProbe = host.trim() !== '' && port.trim() !== ''
  const canSubmit = probe !== null && confirmed && !createMutation.isPending

  return (
    <Page>
      <PageHeader
        title={t('serverNew.title')}
        description={
          <>
            <Link to="/servers" className="text-primary underline-offset-4 hover:underline">
              {t('serverNew.breadcrumb')}
            </Link>
            {' / '}
            {t('serverNew.subtitle')}
          </>
        }
      />

      <form className="flex max-w-3xl flex-col gap-5" onSubmit={handleSubmit}>
        <Panel title={t('serverNew.ssh.title')}>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <Label htmlFor="server-name">{t('serverNew.ssh.name')}</Label>
              <Input
                id="server-name"
                placeholder={t('serverNew.ssh.namePlaceholder')}
                autoComplete="off"
                spellCheck={false}
                autoCapitalize="off"
                required
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field>
              <Label htmlFor="server-location">{t('serverNew.ssh.location')}</Label>
              <Input
                id="server-location"
                placeholder={t('serverNew.ssh.locationPlaceholder')}
                autoComplete="off"
                value={location}
                onChange={(event) => setLocation(event.target.value)}
              />
            </Field>
          </div>
          <div className="grid gap-4 sm:grid-cols-[1fr_7rem_10rem]">
            <Field>
              <Label htmlFor="server-host">{t('serverNew.ssh.host')}</Label>
              <Input
                id="server-host"
                className="font-mono"
                placeholder={t('serverNew.ssh.hostPlaceholder')}
                autoComplete="off"
                spellCheck={false}
                autoCapitalize="off"
                required
                value={host}
                onChange={(event) => {
                  setHost(event.target.value)
                  resetProbe()
                }}
              />
            </Field>
            <Field>
              <Label htmlFor="server-port">{t('serverNew.ssh.port')}</Label>
              <Input
                id="server-port"
                className="font-mono"
                inputMode="numeric"
                required
                value={port}
                onChange={(event) => {
                  setPort(event.target.value)
                  resetProbe()
                }}
              />
            </Field>
            <Field>
              <Label htmlFor="server-user">{t('serverNew.ssh.user')}</Label>
              <Input
                id="server-user"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                autoCapitalize="off"
                required
                value={user}
                onChange={(event) => setUser(event.target.value)}
              />
            </Field>
          </div>

          {/* Before any password: which machine is this, really? */}
          <div className="flex flex-col gap-2 rounded-lg border p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="font-medium">{t('serverNew.fingerprint.title')}</span>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={!canProbe || probeMutation.isPending}
                onClick={() => probeMutation.mutate()}
              >
                {probeMutation.isPending
                  ? t('serverNew.fingerprint.fetching')
                  : t('serverNew.fingerprint.fetch')}
              </Button>
            </div>
            {probe === null ? (
              <span className="text-xs text-muted-foreground">
                {t('serverNew.fingerprint.prompt')}
              </span>
            ) : (
              <>
                <code className="font-mono text-[13px] break-all" translate="no">
                  {probe.fingerprint} · {probe.type}
                </code>
                <span className="text-xs text-muted-foreground">
                  {t('serverNew.fingerprint.hint')}
                </span>
                <label className="inline-flex min-h-8 cursor-pointer items-center gap-2">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={confirmed}
                    onChange={(event) => setConfirmed(event.target.checked)}
                  />
                  <span>{t('serverNew.fingerprint.confirm')}</span>
                </label>
              </>
            )}
            {probeError !== null && (
              <Notice title={probeError.title}>{probeError.text}</Notice>
            )}
          </div>

          <AccessFields value={install} onChange={setInstall} user={user.trim()} />
        </Panel>

        <Panel title={t('installForm.maskTitle')}>
          <TemplateField value={install} onChange={setInstall} />
          <MaskField value={install} onChange={setInstall} />
        </Panel>

        {formError !== null && <Notice title={formError} />}

        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={!canSubmit}>
            {createMutation.isPending ? t('serverNew.submitting') : t('serverNew.submit')}
          </Button>
          <Button asChild variant="outline">
            <Link to="/servers">{t('common.cancel')}</Link>
          </Button>
        </div>
      </form>
    </Page>
  )
}

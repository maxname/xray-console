import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { HostKeyCheck } from '@/components/host-key-check'
import { AccessFields, TemplateField } from '@/components/install-fields'
import { MaskField } from '@/components/mask-field'
import { Page, PageHeader, Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { api, ApiError } from '@/lib/api'
import { useHostKeyCheck } from '@/lib/host-key'
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
  const hostKey = useHostKeyCheck(host, port)

  const createMutation = useMutation({
    mutationFn: () =>
      api.createServer({
        name: name.trim(),
        location: location.trim(),
        host: host.trim(),
        ssh_port: Number(port),
        ssh_user: user.trim(),
        host_key: hostKey.hostKey,
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

  const probeFailure = (() => {
    const error = hostKey.error
    if (error === null) {
      return null
    }
    if (error instanceof ApiError && error.code === 'ssh_unreachable') {
      return {
        title: t('serverNew.fingerprint.unreachableTitle'),
        text: t('serverNew.fingerprint.unreachable'),
      }
    }
    if (error instanceof ApiError && isKnownFormError(error.code)) {
      return { title: t(`serverNew.errors.${error.code}`) }
    }
    return { title: t('common.error') }
  })()

  const canSubmit = hostKey.ready && !createMutation.isPending

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
                onChange={(event) => setHost(event.target.value)}
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
                onChange={(event) => setPort(event.target.value)}
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

          <HostKeyCheck
            check={hostKey}
            title={t('serverNew.fingerprint.title')}
            prompt={t('serverNew.fingerprint.prompt')}
            hint={t('serverNew.fingerprint.hint')}
            failure={probeFailure}
          />

          <AccessFields value={install} onChange={setInstall} user={user.trim()} />
        </Panel>

        <Panel title={t('installForm.maskTitle')}>
          <TemplateField value={install} onChange={setInstall} />
          <MaskField value={install} onChange={setInstall} host={host} />
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

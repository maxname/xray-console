import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
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
import {
  api,
  type OnError,
  type TemplateDetail,
} from '@/lib/api'

import { explain, templateKey } from '@/lib/templates'

export function ApplyDialog({
  detail,
  open,
  onOpenChange,
  onStarted,
}: {
  detail: TemplateDetail
  open: boolean
  onOpenChange: (open: boolean) => void
  onStarted: (detail: TemplateDetail) => void
}) {
  const { t } = useTranslation()
  const [onError, setOnError] = useState<OnError>('stop')
  const start = useMutation({
    mutationFn: () => api.applyTemplate(detail.template.id, onError),
    onSuccess: (next) => {
      onStarted(next)
      onOpenChange(false)
    },
  })
  const targets = detail.servers.filter((server) => server.state === 'changed')

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t('templates.applyDialog.title', {
              count: targets.length,
              version: detail.template.version,
            })}
          </DialogTitle>
          <DialogDescription>{t('templates.applyDialog.description')}</DialogDescription>
        </DialogHeader>
        <p className="font-mono text-sm" translate="no">
          {targets.map((server) => server.name).join(', ')}
        </p>
        <fieldset className="flex flex-col gap-2 text-sm">
          <legend className="mb-2 font-medium">{t('templates.applyDialog.onError')}</legend>
          {(['stop', 'continue'] as const).map((choice) => (
            <label key={choice} className="flex cursor-pointer items-start gap-3">
              <input
                type="radio"
                name="apply-on-error"
                className="mt-1 size-4 accent-primary"
                checked={onError === choice}
                onChange={() => setOnError(choice)}
              />
              <span className="flex flex-col">
                <span>{t(`templates.applyDialog.${choice}`)}</span>
                <span className="text-xs text-muted-foreground">
                  {t(`templates.applyDialog.${choice}Hint`)}
                </span>
              </span>
            </label>
          ))}
        </fieldset>
        {start.isError && <Notice title={explain(t, start.error)} />}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button disabled={start.isPending} onClick={() => start.mutate()}>
            {t('templates.applyDialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function PropertiesDialog({
  detail,
  only,
  open,
  onOpenChange,
  onSaved,
}: {
  detail: TemplateDetail
  only: boolean
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved: (detail: TemplateDetail) => void
}) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [name, setName] = useState(detail.template.name)
  const [description, setDescription] = useState(detail.template.description)

  const save = useMutation({
    mutationFn: () => api.updateTemplate(detail.template.id, { name, description }),
    onSuccess: (next) => {
      onSaved(next)
      onOpenChange(false)
    },
  })
  const remove = useMutation({
    mutationFn: () => api.deleteTemplate(detail.template.id),
    onSuccess: () => {
      onOpenChange(false)
      queryClient.removeQueries({ queryKey: templateKey(detail.template.id) })
      void queryClient.invalidateQueries({ queryKey: ['templates'], exact: true })
      void navigate({ to: '/templates', search: {} })
    },
  })
  const used = detail.template.server_count
  const [deleting, setDeleting] = useState(false)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            save.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {t('templates.propertiesDialog.title', { name: detail.template.name })}
            </DialogTitle>
            <DialogDescription>{t('templates.propertiesDialog.description')}</DialogDescription>
          </DialogHeader>
          <Field>
            <Label htmlFor="template-name">{t('templates.createDialog.name')}</Label>
            <Input
              id="template-name"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
              required
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field>
            <Label htmlFor="template-description">{t('templates.createDialog.about')}</Label>
            <Input
              id="template-description"
              autoComplete="off"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Field>
          <p className="text-xs text-muted-foreground">
            {used > 0
              ? t('templates.propertiesDialog.inUse', { count: used })
              : only
                ? t('templates.propertiesDialog.lastOne')
                : null}
          </p>
          {deleting && (
            <ConfirmStrip
              text={t('templates.propertiesDialog.deleteConfirm', { name: detail.template.name })}
              pending={remove.isPending}
              onConfirm={() => remove.mutate()}
              onCancel={() => setDeleting(false)}
            />
          )}
          {(save.isError || remove.isError) && (
            <Notice title={explain(t, save.error ?? remove.error)} />
          )}
          <DialogFooter className="sm:justify-between">
            <Button
              type="button"
              variant="destructive"
              disabled={used > 0 || only || remove.isPending}
              onClick={() => setDeleting(true)}
            >
              {t('templates.propertiesDialog.delete')}
            </Button>
            <span className="flex gap-2">
              <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
                {t('common.cancel')}
              </Button>
              <Button type="submit" disabled={save.isPending}>
                {t('templates.propertiesDialog.save')}
              </Button>
            </span>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function CreateDialog({
  mode,
  onClose,
  onCreated,
}: {
  mode: 'blank' | 'server' | null
  onClose: () => void
  onCreated: (id: number) => void
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [serverId, setServerId] = useState('')
  const fromServer = mode === 'server'

  const servers = useQuery({
    queryKey: ['servers'],
    queryFn: () => api.listServers(),
    enabled: fromServer,
  })
  const installed = (servers.data?.servers ?? []).filter(
    (server) => server.status === 'ready',
  )

  const create = useMutation({
    mutationFn: () =>
      api.createTemplate({
        name: name.trim(),
        description: description.trim(),
        from_server_id: fromServer ? Number(serverId) : undefined,
      }),
    onSuccess: (detail) => {
      void queryClient.invalidateQueries({ queryKey: ['templates'], exact: true })
      setName('')
      setDescription('')
      setServerId('')
      onCreated(detail.template.id)
    },
  })

  return (
    <Dialog
      open={mode !== null}
      onOpenChange={(open) => {
        if (!open) {
          create.reset()
          onClose()
        }
      }}
    >
      <DialogContent>
        <form
          className="grid gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>
              {fromServer
                ? t('templates.createDialog.titleFromServer')
                : t('templates.createDialog.title')}
            </DialogTitle>
            <DialogDescription>
              {fromServer
                ? t('templates.createDialog.descriptionFromServer')
                : t('templates.createDialog.description')}
            </DialogDescription>
          </DialogHeader>
          {fromServer &&
            (servers.data && installed.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                {t('templates.createDialog.noServers')}
              </p>
            ) : (
              <Field>
                <Label htmlFor="new-template-server">
                  {t('templates.createDialog.server')}
                </Label>
                <Select value={serverId} onValueChange={setServerId}>
                  <SelectTrigger id="new-template-server" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {installed.map((server) => (
                      <SelectItem key={server.id} value={String(server.id)}>
                        {server.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            ))}
          <Field>
            <Label htmlFor="new-template-name">{t('templates.createDialog.name')}</Label>
            <Input
              id="new-template-name"
              className="font-mono"
              placeholder={t('templates.createDialog.namePlaceholder')}
              autoComplete="off"
              spellCheck={false}
              autoCapitalize="off"
              required
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </Field>
          <Field>
            <Label htmlFor="new-template-description">
              {t('templates.createDialog.about')}
            </Label>
            <Input
              id="new-template-description"
              autoComplete="off"
              value={description}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Field>
          {create.isError && <Notice title={explain(t, create.error)} />}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t('common.cancel')}
            </Button>
            <Button
              type="submit"
              disabled={create.isPending || (fromServer && serverId === '')}
            >
              {t('templates.createDialog.submit')}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

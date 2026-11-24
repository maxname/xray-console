import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
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
  api,
  ApiError,
  type Server,
} from '@/lib/api'
import { toast } from '@/lib/toast'

export function DeleteDialog({
  server,
  open,
  onOpenChange,
}: {
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {/* The form lives only while the dialog is open: a name typed and then
            cancelled must not be waiting there, button armed, the next time. */}
        <DeleteForm server={server} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

export function DeleteForm({ server, onClose }: { server: Server; onClose: () => void }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [typed, setTyped] = useState('')

  const hasAccess = server.manifest.some(
    (change) => change.kind === 'ssh_key' || change.kind === 'ssh_password',
  )

  const remove = useMutation({
    mutationFn: () => api.deleteServer(server.id, typed),
    onSuccess: (response) => {
      toast(
        response.access_removed
          ? {
              title: t('server.deleteDialog.done', { name: server.name }),
              variant: 'success',
            }
          : {
              title: t('server.deleteDialog.done', { name: server.name }),
              description: t('server.deleteDialog.accessLeft'),
              duration: 0,
            },
      )
      queryClient.removeQueries({ queryKey: ['servers', server.id] })
      void queryClient.invalidateQueries({ queryKey: ['servers'] })
      void navigate({ to: '/servers' })
    },
  })

  const error = (() => {
    if (!remove.isError) {
      return null
    }
    if (remove.error instanceof ApiError && remove.error.code === 'busy') {
      return t('server.deleteDialog.busy')
    }
    if (remove.error instanceof ApiError && remove.error.code === 'server_in_chains') {
      const chains = remove.error.details.chains
      return t('server.deleteDialog.inChains', {
        chains: Array.isArray(chains) ? chains.join(', ') : '',
      })
    }
    if (remove.error instanceof ApiError && remove.error.code === 'server_in_rule_sets') {
      const sets = remove.error.details.rule_sets
      return t('server.deleteDialog.inRuleSets', {
        sets: Array.isArray(sets) ? sets.join(', ') : '',
      })
    }
    return t('common.error')
  })()

  return (
    <form
      className="grid gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        remove.mutate()
      }}
    >
      <DialogHeader>
        <DialogTitle>{t('server.deleteDialog.title', { name: server.name })}</DialogTitle>
        <DialogDescription>
          {server.status === 'ready'
            ? t('server.deleteDialog.description')
            : t('server.deleteDialog.descriptionFresh')}
          {hasAccess && ` ${t('server.deleteDialog.access')}`}
        </DialogDescription>
      </DialogHeader>
      <Field>
        <Label htmlFor="delete-server-name">{t('server.deleteDialog.confirm')}</Label>
        <Input
          id="delete-server-name"
          className="font-mono"
          placeholder={server.name}
          autoComplete="off"
          spellCheck={false}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
        />
      </Field>
      {error !== null && <Notice title={error} />}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          {t('common.cancel')}
        </Button>
        <Button
          type="submit"
          variant="destructive"
          disabled={typed !== server.name || remove.isPending}
        >
          {remove.isPending
            ? t('server.deleteDialog.submitting')
            : t('server.deleteDialog.submit')}
        </Button>
      </DialogFooter>
    </form>
  )
}

import { Check, Loader2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Panel } from '@/components/page'
import { FailureExplained } from '@/components/failure-explained'
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
  type Operation,
  type Server,
} from '@/lib/api'
import { aftermath } from '@/lib/failures'
import { cn } from '@/lib/utils'

import { APPLY_ERRORS } from '@/lib/server-config'

export function ApplyProgress({ operation }: { operation: Operation }) {
  const { t } = useTranslation()
  return (
    <Panel title={t('config.applying.title')}>
      <ol className="flex flex-col gap-2">
        {operation.steps.map((step) => (
          <li key={step.id} className="flex items-center gap-3">
            <span
              className={cn(
                'flex size-5 shrink-0 items-center justify-center rounded-full border [&_svg]:size-3',
                step.status === 'done' && 'border-primary bg-primary text-primary-foreground',
                step.status === 'running' && 'border-primary text-primary',
                step.status === 'pending' && 'border-input',
              )}
            >
              {step.status === 'done' && <Check aria-hidden="true" strokeWidth={3} />}
              {step.status === 'running' && (
                <Loader2 aria-hidden="true" className="animate-spin" />
              )}
            </span>
            <span
              className={cn(
                step.status === 'running' && 'font-semibold',
                step.status === 'pending' && 'text-muted-foreground',
              )}
            >
              {t(`config.applying.steps.${step.id}`)}
            </span>
          </li>
        ))}
      </ol>
    </Panel>
  )
}

export function ApplyFailed({
  server,
  operation,
  onDismiss,
}: {
  server: Server
  operation: Operation
  onDismiss: () => void
}) {
  const { t } = useTranslation()
  const failure = operation.error!
  const details = failure.details ?? {}
  const code = (APPLY_ERRORS as readonly string[]).includes(failure.code)
    ? failure.code
    : 'internal'
  // Only what the job itself reported: see aftermath.
  const left = aftermath(details)

  return (
    <Panel className="border border-destructive/40 bg-destructive/5">
      <div className="flex flex-col gap-1">
        <h2 className="font-heading text-base font-semibold text-destructive">
          {t(`config.applyFailed.title.${left}`)}
        </h2>
        <p>{t(`config.applyFailed.${left}`, { name: server.name })}</p>
      </div>
      <FailureExplained
        failure={failure}
        prefix={`server.errors.${code}`}
        tellMessage={code === 'internal'}
      />
      <Button variant="outline" className="self-start" onClick={onDismiss}>
        {t('config.applyFailed.dismiss')}
      </Button>
    </Panel>
  )
}

export function RotateDialog({
  server,
  open,
  onOpenChange,
  onConfirm,
}: {
  server: Server
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => Promise<boolean>
}) {
  const { t } = useTranslation()
  const [pending, setPending] = useState(false)
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('config.rotateDialog.title', { name: server.name })}</DialogTitle>
          <DialogDescription>{t('config.rotateDialog.description')}</DialogDescription>
        </DialogHeader>
        <p className="text-sm text-muted-foreground">{t('config.rotateDialog.note')}</p>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            {t('common.cancel')}
          </Button>
          <Button
            variant="destructive"
            disabled={pending}
            onClick={() => {
              setPending(true)
              void onConfirm().finally(() => {
                setPending(false)
                onOpenChange(false)
              })
            }}
          >
            {t('config.rotateDialog.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// --- config.json -----------------------------------------------------------------

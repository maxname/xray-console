import { Check, Loader2, Undo2, X } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  type LogLine,
  type StepState,
} from '@/lib/api'
import { useCopy } from '@/lib/clipboard'
import { cn } from '@/lib/utils'

export function StepList({ steps, label }: { steps: StepState[]; label?: string }) {
  const { t } = useTranslation()
  return (
    <ol className="flex flex-col gap-3" aria-label={label ?? t('server.installing.title')}>
      {steps.map((step) => (
        <li key={step.id} className="flex items-start gap-3">
          <StepMark status={step.status} />
          <div className="flex min-w-0 flex-col">
            <span
              className={cn(
                step.status === 'running' && 'font-semibold',
                (step.status === 'pending' || step.status === 'undone') &&
                  'text-muted-foreground',
              )}
            >
              {t(`server.steps.${step.id}`)}
              <span className="sr-only"> — {t(`server.stepStatus.${step.status}`)}</span>
            </span>
            {step.detail !== '' && (
              <span className="font-mono text-xs break-all text-muted-foreground">
                {step.detail}
              </span>
            )}
          </div>
        </li>
      ))}
    </ol>
  )
}

export function StepMark({ status }: { status: StepState['status'] }) {
  const base =
    'mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full border [&_svg]:size-3'
  switch (status) {
    case 'done':
      return (
        <span className={cn(base, 'border-primary bg-primary text-primary-foreground')}>
          <Check aria-hidden="true" strokeWidth={3} />
        </span>
      )
    case 'running':
      return (
        <span className={cn(base, 'border-primary text-primary')}>
          <Loader2 aria-hidden="true" className="animate-spin" />
        </span>
      )
    case 'failed':
      return (
        <span className={cn(base, 'border-destructive bg-destructive/10 text-destructive')}>
          <X aria-hidden="true" strokeWidth={3} />
        </span>
      )
    case 'undone':
      return (
        <span className={cn(base, 'border-input text-muted-foreground')}>
          <Undo2 aria-hidden="true" />
        </span>
      )
    default:
      return <span className={cn(base, 'border-input')} />
  }
}

export function LogView({ lines, follow }: { lines: LogLine[]; follow: boolean }) {
  const { t } = useTranslation()

  if (lines.length === 0) {
    return <p className="text-muted-foreground">{t('server.log.empty')}</p>
  }
  const text = lines
    .map((line) => (line.kind === 'cmd' ? `$ ${line.text}` : line.text))
    .join('\n')

  return (
    <>
      <pre
        // While the job runs, keep the newest line in view: the callback ref
        // re-runs on every render that adds lines.
        ref={(node) => {
          if (node && follow) {
            node.scrollTop = node.scrollHeight
          }
        }}
        className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5 whitespace-pre-wrap"
        aria-label={t('server.log.title')}
        translate="no"
        tabIndex={0}
      >
        {lines.map((line) => (
          <span
            key={line.seq}
            className={cn(
              'block break-all',
              line.kind === 'cmd' && 'text-foreground',
              line.kind === 'out' && 'text-muted-foreground',
              line.kind === 'info' && 'text-info',
              line.kind === 'error' && 'text-destructive',
            )}
          >
            {line.kind === 'cmd' ? `$ ${line.text}` : line.text}
          </span>
        ))}
      </pre>
      <CopyLogButton text={text} />
    </>
  )
}

export function CopyLogButton({ text }: { text: string }) {
  const { t } = useTranslation()
  const { copied, copy } = useCopy()
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="self-start"
      onClick={() => copy(text)}
    >
      {copied ? t('common.copied') : t('server.log.copy')}
    </Button>
  )
}

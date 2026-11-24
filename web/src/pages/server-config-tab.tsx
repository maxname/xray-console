import { useTranslation } from 'react-i18next'

import { Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import {
  type DiffLine,
  type HistoryEntry,
  type Server,
} from '@/lib/api'
import { useCopy } from '@/lib/clipboard'
import { formatDayTime } from '@/lib/format'
import { cn } from '@/lib/utils'

import { useServerConfig } from '@/lib/server-config'

export function DiffView({ lines }: { lines: DiffLine[] }) {
  return (
    <pre
      className="max-h-96 overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5"
      translate="no"
      tabIndex={0}
    >
      {lines.map((line, index) => (
        <span
          key={index}
          className={cn(
            'block whitespace-pre-wrap',
            line.op === '+' && 'bg-success/10 text-success',
            line.op === '-' && 'bg-destructive/10 text-destructive',
            line.op === '@' && 'text-muted-foreground',
          )}
        >
          {line.op === '@' ? '⋯' : `${line.op} ${line.text}`}
        </span>
      ))}
    </pre>
  )
}

export function HistoryList({ entries }: { entries: HistoryEntry[] }) {
  const { t } = useTranslation()
  if (entries.length === 0) {
    return <p className="text-muted-foreground">{t('config.history.empty')}</p>
  }

  const describe = (entry: HistoryEntry): string => {
    if (entry.status === 'running') {
      return t('config.history.running')
    }
    if (entry.kind === 'install') {
      return entry.status === 'succeeded'
        ? t('config.history.install')
        : t('config.history.installFailed')
    }
    if (entry.status !== 'succeeded') {
      return t('config.history.failed')
    }
    return entry.changed.length > 0
      ? t('config.history.applied', {
          fields: entry.changed.map((key) => t(`fields.${key}`)).join(', '),
        })
      : t('config.history.appliedSame')
  }

  return (
    <ul className="flex flex-col gap-2.5">
      {entries.map((entry) => (
        <li key={entry.id} className="grid grid-cols-[auto_1fr] gap-x-4">
          <span className="font-mono text-xs leading-5 text-muted-foreground">
            {formatDayTime(entry.finished_at ?? entry.started_at)}
          </span>
          <span className={cn(entry.status === 'failed' && 'text-destructive')}>
            {describe(entry)}
          </span>
        </li>
      ))}
    </ul>
  )
}

// --- applying --------------------------------------------------------------------

export function ConfigTab({ server }: { server: Server }) {
  const { t } = useTranslation()
  const query = useServerConfig(server)
  const { copied, copy } = useCopy()

  if (!query.data) {
    return (
      <Panel>
        <Skeleton className="h-40 w-full" />
      </Panel>
    )
  }
  const text = query.data.config
  return (
    <Panel>
      <p className="text-muted-foreground">{t('config.configNote')}</p>
      <pre
        className="max-h-[70svh] overflow-auto rounded-lg bg-muted p-3 font-mono text-xs leading-5"
        translate="no"
        tabIndex={0}
      >
        {text}
      </pre>
      <Button
        variant="outline"
        size="sm"
        className="self-start"
        onClick={() => copy(text)}
      >
        {copied ? t('common.copied') : t('config.copyConfig')}
      </Button>
    </Panel>
  )
}

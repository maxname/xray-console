import { useMutation } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import { api, ApiError, type ChainServer } from '@/lib/api'

/**
 * Rules and subscriptions are edited in the panel and run on the entry server,
 * so between an edit and an apply the two differ. This says so and offers the
 * apply. Render it only while clientsPending: unmounting is what clears a
 * finished attempt.
 */
export function EntryApply({
  entry,
  text,
  onRefresh,
}: {
  entry: ChainServer
  /** What is waiting, in the words of the page showing it. */
  text: string
  /** Reloads the page's data; resolves once it is fresh. */
  onRefresh: () => Promise<unknown>
}) {
  const { t } = useTranslation()
  // Set once an apply started from here has been seen by the server, so that
  // "still pending and no longer busy" can be read as "it failed".
  const [started, setStarted] = useState(false)

  const apply = useMutation({
    mutationFn: () => api.applyServerConfig(entry.id),
    onMutate: () => setStarted(false),
    onSuccess: async () => {
      await onRefresh()
      setStarted(true)
    },
  })

  const serverLink = (
    <Link
      to="/servers/$serverId"
      params={{ serverId: String(entry.id) }}
      className="font-medium underline underline-offset-4"
    >
      {entry.name}
    </Link>
  )

  if (entry.busy || apply.isPending) {
    return (
      <div role="status" className="rounded-lg bg-muted px-3 py-2.5 text-sm">
        {t('entryApply.running')} {serverLink}…
      </div>
    )
  }

  const failed = started && entry.pending
  // An apply puts the whole desired configuration on the server, not only
  // the part this page is about.
  const others = entry.changed.filter((key) => key !== 'policies')
  const refused = apply.error instanceof ApiError ? apply.error.code : null
  const first =
    apply.error instanceof ApiError && Array.isArray(apply.error.details.servers)
      ? apply.error.details.servers.filter((name) => typeof name === 'string').join(', ')
      : ''

  return (
    <div className="flex flex-col gap-2 text-sm">
      <div
        role="status"
        className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2.5"
      >
        <span className="min-w-0 flex-1 basis-72">
          {text} {t('entryApply.where')} {serverLink}.
          {others.length > 0 && (
            <span className="block text-xs text-muted-foreground">
              {t('entryApply.others', {
                fields: others.map((key) => t(`fields.${key}`, { defaultValue: key })).join(', '),
              })}
            </span>
          )}
        </span>
        <Button size="sm" onClick={() => apply.mutate()}>
          {failed ? t('entryApply.retry') : t('entryApply.apply')}
        </Button>
      </div>
      {failed && (
        <Notice title={t('entryApply.failedTitle')}>
          {t('entryApply.failedText')} {serverLink}.
        </Notice>
      )}
      {apply.isError && (
        <Notice
          title={
            refused === 'busy' || refused === 'not_installed'
              ? t(`entryApply.errors.${refused}`)
              : refused === 'upstream_pending'
                ? t('entryApply.errors.upstream_pending', { servers: first })
                : t('common.error')
          }
        />
      )}
    </div>
  )
}

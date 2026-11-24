import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import type { Server } from '@/lib/api'
import { formatDuration } from '@/lib/format'
import { useNow } from '@/lib/use-now'

/**
 * A server's state in one badge. Until Xray is installed that is where the
 * installation stands; after, it is what the availability checks say.
 */
export function ServerStatusBadge({ server }: { server: Server }) {
  const { t } = useTranslation()
  const now = useNow()

  if (server.status !== 'ready') {
    return (
      <Badge
        variant={
          server.status === 'failed' || server.status === 'uninstalling' ? 'warning' : 'secondary'
        }
      >
        {t(`servers.status.${server.status}`)}
      </Badge>
    )
  }
  const check = server.check
  if (check === null || check.state === 'unknown') {
    return <Badge variant="secondary">{t('servers.status.unknown')}</Badge>
  }
  if (check.state === 'up') {
    return <Badge variant="success">{t('servers.status.up')}</Badge>
  }
  const seconds = Math.max(0, now - check.since)
  return (
    <>
      <Badge variant="destructive">{t('servers.status.down')}</Badge>
      <div className="text-xs text-destructive">
        {t('servers.downFor', { duration: formatDuration(seconds) })}
      </div>
    </>
  )
}

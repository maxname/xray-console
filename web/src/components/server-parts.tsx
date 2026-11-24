import { Link } from '@tanstack/react-router'
import { type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { type ServerChange } from '@/lib/api'

export function Breadcrumb({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  return (
    <>
      <Link to="/servers" className="text-primary underline-offset-4 hover:underline">
        {t('server.breadcrumb')}
      </Link>
      {' / '}
      {children}
    </>
  )
}

export function ChangeList({ changes }: { changes: ServerChange[] }) {
  const { t } = useTranslation()
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2.5">
      {changes.map((change) => (
        <div key={`${change.kind}:${change.text}`} className="contents">
          <dt className="text-muted-foreground">
            {t(`server.done.changeKinds.${change.kind}`)}
          </dt>
          <dd className="min-w-0 font-mono break-all">{change.text}</dd>
        </div>
      ))}
    </dl>
  )
}

// --- deleting ----------------------------------------------------------------------

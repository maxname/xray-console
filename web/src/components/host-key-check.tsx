import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import type { HostKeyCheck as Check } from '@/lib/host-key'

// Before any password: which machine is this, really? The block that fetches
// a machine's SSH key, shows its fingerprint and asks for a confirmation —
// the same for a server and for a device, which differ only in their words.
export function HostKeyCheck({
  check,
  title,
  prompt,
  hint,
  failure,
}: {
  check: Check
  title: string
  prompt: string
  hint?: string
  failure: { title: string; text?: string } | null
}) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col gap-2 rounded-lg border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-medium">{title}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={!check.canFetch || check.pending}
          onClick={check.fetch}
        >
          {check.pending ? t('hostKey.fetching') : t('hostKey.fetch')}
        </Button>
      </div>
      {check.probe === null ? (
        <span className="text-xs text-muted-foreground">{prompt}</span>
      ) : (
        <>
          <code className="font-mono text-[13px] break-all" translate="no">
            {check.probe.fingerprint} · {check.probe.type}
          </code>
          {hint !== undefined && <span className="text-xs text-muted-foreground">{hint}</span>}
          <label className="inline-flex min-h-8 cursor-pointer items-center gap-2">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={check.confirmed}
              onChange={(event) => check.setConfirmed(event.target.checked)}
            />
            <span>{t('hostKey.confirm')}</span>
          </label>
        </>
      )}
      {failure !== null && <Notice title={failure.title}>{failure.text}</Notice>}
    </div>
  )
}

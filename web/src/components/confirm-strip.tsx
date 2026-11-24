import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

/**
 * The second step of deleting something from inside its dialog: the question,
 * "yes, delete" and a way back. Deleting takes two clicks everywhere, so a
 * slip on the first one costs nothing.
 */
export function ConfirmStrip({
  text,
  confirm,
  pending,
  onConfirm,
  onCancel,
}: {
  text: string
  /** What the button that goes ahead says; "yes, delete" unless given. */
  confirm?: string
  pending: boolean
  onConfirm: () => void
  onCancel: () => void
}) {
  const { t } = useTranslation()
  return (
    <div
      role="alert"
      className="flex flex-col gap-2 rounded-lg border border-warning/40 bg-warning/8 px-3 py-2.5"
    >
      <span>{text}</span>
      <div className="flex gap-2">
        <Button type="button" size="sm" variant="destructive" disabled={pending} onClick={onConfirm}>
          {confirm ?? t('common.deleteYes')}
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onCancel}>
          {t('common.cancel')}
        </Button>
      </div>
    </div>
  )
}

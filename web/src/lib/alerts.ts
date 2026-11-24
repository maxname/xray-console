
import { ApiError } from '@/lib/api'
import { type Translate } from '@/lib/errors'

export function explainAlert(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_alert') {
      return t(`alerts.errors.${String(error.details.reason ?? '')}`, {
        defaultValue: t('common.error'),
      })
    }
    if (error.code === 'no_channel') {
      return t('alerts.errors.no_channel')
    }
  }
  return t('common.error')
}


import { ApiError } from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'

/** Errors with their own sentence, by API code. */
export const KNOWN_ERRORS = [
  'name_taken',
  'invalid_name',
  'template_in_use',
  'last_template',
  'nothing_to_apply',
  'busy',
  'not_installed',
] as const

export function explain(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_field') {
      const reason = String(error.details.reason ?? 'generic')
      const key = String(error.details.key ?? '')
      const text = t(`fieldErrors.${reason}`, { defaultValue: t('fieldErrors.generic') })
      return key ? `${key}: ${text}` : text
    }
  }
  return explainKnown(t, error, 'templates', KNOWN_ERRORS)
}

export function templateKey(id: number) {
  return ['templates', id] as const
}

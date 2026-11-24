import { ApiError } from '@/lib/api'

/** The translate function as helpers outside components take it. */
export type Translate = (key: string, options?: Record<string, unknown>) => string

/**
 * The text for a request that was refused.
 *
 * A page names the codes its own part of the catalogue explains
 * (`<namespace>.errors.<code>`). A code it does not name is still said as
 * plainly as it can be: something deleted elsewhere meanwhile, or too many
 * requests, are not "something went wrong".
 */
export function explainKnown(
  t: Translate,
  error: unknown,
  namespace: string,
  known: readonly string[],
): string {
  if (error instanceof ApiError) {
    if (known.includes(error.code)) {
      return t(`${namespace}.errors.${error.code}`)
    }
    if (error.code === 'not_found' || error.code === 'rate_limited') {
      return t(`common.errors.${error.code}`)
    }
  }
  return t('common.error')
}

/** The names an error lists under a key of its details, as text to read. */
export function listed(error: ApiError, key: string): string {
  const value = error.details[key]
  return Array.isArray(value) ? value.filter((item) => typeof item === 'string').join(', ') : ''
}

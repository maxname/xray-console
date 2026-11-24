
import {
  ApiError,
  type RuleSet,
  type RuleSetRequest,
} from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'

export const KNOWN_ERRORS = [
  'name_taken',
  'invalid_name',
  'bad_entry',
  'rule_set_in_use',
  'rule_set_local_client',
] as const

export function explain(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_rule') {
      const reason = String(error.details.reason ?? '')
      const index = Number(error.details.rule ?? 0)
      return t(`routing.errors.rule.${reason}`, {
        // -1 is the default route, which has no number.
        where: index < 0 ? t('routing.errors.inDefault') : t('routing.errors.inRule', { number: index + 1 }),
        value: String(error.details.value ?? ''),
        defaultValue: t('common.error'),
      })
    }
  }
  return explainKnown(t, error, 'routing', KNOWN_ERRORS)
}

export function toRequest(set: RuleSet, change: Partial<RuleSetRequest> = {}): RuleSetRequest {
  return {
    name: set.name,
    entry_server_id: set.entry.id,
    rules: set.rules,
    default_action: set.default_action,
    default_chain_id: set.default_chain_id,
    ...change,
  }
}

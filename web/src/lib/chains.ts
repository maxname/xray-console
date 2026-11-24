
import { ApiError, type ChainStrategy } from '@/lib/api'
import { explainKnown, listed, type Translate } from '@/lib/errors'

export const KNOWN_ERRORS = [
  'name_taken',
  'invalid_name',
  'nothing_to_apply',
  'busy',
  'chain_not_ready',
] as const

export function explain(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_hop') {
      const reason = String(error.details.reason ?? '')
      const number = Number(error.details.hop ?? 0) + 1
      return t(`chains.errors.hop.${reason}`, { number, defaultValue: t('common.error') })
    }
    if (error.code === 'chain_in_rule_sets') {
      return t('chains.errors.chain_in_rule_sets', { sets: listed(error, 'rule_sets') })
    }
  }
  return explainKnown(t, error, 'chains', KNOWN_ERRORS)
}

export const chainsKey = ['chains'] as const

export const STRATEGIES: ChainStrategy[] = ['leastPing', 'roundRobin', 'random']

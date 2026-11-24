import type {
  Chain,
  ChainServer,
  Rule,
  RuleAction,
  RuleCondition,
  RuleConditionType,
} from '@/lib/api'

export const CONDITION_TYPES: RuleConditionType[] = [
  'domain',
  'full',
  'keyword',
  'regexp',
  'geosite',
  'ip',
  'geoip',
]

/** The query key of devices and the panel's own client. */
export const devicesKey = ['devices'] as const

/**
 * Whether an entry server's rules and subscriptions differ from the panel's,
 * or are being brought in line right now.
 */
export function clientsPending(entry: ChainServer): boolean {
  return entry.busy || entry.changed.includes('policies')
}

/** A destination as one select value: "direct", "block" or "chain:<id>". */
export function targetValue(action: RuleAction, chainId: number): string {
  return action === 'chain' ? `chain:${chainId}` : action
}

export function parseTarget(value: string): { action: RuleAction; chain_id: number } {
  if (value.startsWith('chain:')) {
    return { action: 'chain', chain_id: Number(value.slice('chain:'.length)) }
  }
  return { action: value === 'block' ? 'block' : 'direct', chain_id: 0 }
}

/** The chains a rule on this server can send traffic into: those entered here. */
export function chainsFrom(chains: Chain[], entryServerId: number): Chain[] {
  return chains.filter((chain) => chain.hops[0]?.servers[0]?.id === entryServerId)
}

/**
 * What a rule matches, in Xray's own notation — the one its documentation and
 * every guide use: "geosite:category-ru, domain:example.com, port:443".
 */
export function describeMatch(rule: Rule): string {
  const parts = rule.conditions.map((condition) =>
    condition.type === 'ip' ? condition.value : `${condition.type}:${condition.value}`,
  )
  if (rule.ports !== '') {
    parts.push(`port:${rule.ports}`)
  }
  if (rule.protocol !== '') {
    parts.push(`protocol:${rule.protocol}`)
  }
  return parts.join(', ')
}

/** Guesses what a pasted line is: an address or subnet, or else a domain. */
export function guessCondition(line: string): RuleCondition | null {
  const value = line.trim()
  if (value === '') {
    return null
  }
  // "geosite:category-ru" pasted in Xray's notation keeps its kind.
  const colon = value.indexOf(':')
  const kind = colon > 0 ? value.slice(0, colon) : ''
  if (kind !== 'ip' && (CONDITION_TYPES as string[]).includes(kind)) {
    return { type: kind as RuleConditionType, value: value.slice(colon + 1) }
  }
  const v4 = /^(\d{1,3}\.){3}\d{1,3}(\/\d{1,2})?$/
  const v6 = /^[0-9a-f]*:[0-9a-f:]*(\/\d{1,3})?$/i
  return { type: v4.test(value) || v6.test(value) ? 'ip' : 'domain', value }
}

export interface Preset {
  key: 'ads' | 'ru' | 'torrents'
  rule: Omit<Rule, 'id' | 'name'>
}

/**
 * Ready-made rules. The lists they name ship with Xray (geosite.dat,
 * geoip.dat), so they work on a server as installed.
 */
export const PRESETS: Preset[] = [
  {
    key: 'ads',
    rule: {
      conditions: [{ type: 'geosite', value: 'category-ads-all' }],
      ports: '',
      protocol: '',
      action: 'block',
      chain_id: 0,
      enabled: true,
    },
  },
  {
    key: 'ru',
    rule: {
      conditions: [
        { type: 'geosite', value: 'category-ru' },
        { type: 'geoip', value: 'ru' },
      ],
      ports: '',
      protocol: '',
      action: 'direct',
      chain_id: 0,
      enabled: true,
    },
  },
  {
    key: 'torrents',
    rule: {
      conditions: [],
      ports: '',
      protocol: 'bittorrent',
      action: 'block',
      chain_id: 0,
      enabled: true,
    },
  },
]

import { describe, expect, it } from 'vitest'

import type { Chain, Rule } from '@/lib/api'
import { chainsFrom, describeMatch, guessCondition, parseTarget, targetValue } from '@/lib/rules'

const rule = (change: Partial<Rule>): Rule => ({
  id: 'a',
  name: '',
  conditions: [],
  ports: '',
  protocol: '',
  action: 'direct',
  chain_id: 0,
  enabled: true,
  ...change,
})

describe('describeMatch', () => {
  it('writes a rule the way Xray does', () => {
    expect(
      describeMatch(
        rule({
          conditions: [
            { type: 'geosite', value: 'category-ru' },
            { type: 'domain', value: 'example.com' },
            { type: 'ip', value: '198.51.100.0/24' },
          ],
          ports: '22,5432',
          protocol: 'tls',
        }),
      ),
    ).toBe('geosite:category-ru, domain:example.com, 198.51.100.0/24, port:22,5432, protocol:tls')
  })

  it('shows a rule with only a protocol', () => {
    expect(describeMatch(rule({ protocol: 'bittorrent' }))).toBe('protocol:bittorrent')
  })
})

describe('guessCondition', () => {
  it('tells addresses from names', () => {
    expect(guessCondition(' video.example.com ')).toEqual({ type: 'domain', value: 'video.example.com' })
    expect(guessCondition('198.51.100.7')).toEqual({ type: 'ip', value: '198.51.100.7' })
    expect(guessCondition('10.0.0.0/8')).toEqual({ type: 'ip', value: '10.0.0.0/8' })
    expect(guessCondition('2001:db8::/32')).toEqual({ type: 'ip', value: '2001:db8::/32' })
  })

  it('keeps the kind of a line already in Xray notation', () => {
    expect(guessCondition('geosite:category-ads-all')).toEqual({
      type: 'geosite',
      value: 'category-ads-all',
    })
    expect(guessCondition('regexp:^a:b$')).toEqual({ type: 'regexp', value: '^a:b$' })
  })

  it('skips blank lines', () => {
    expect(guessCondition('   ')).toBeNull()
  })
})

describe('targets', () => {
  it('round-trips through one select value', () => {
    expect(parseTarget(targetValue('chain', 7))).toEqual({ action: 'chain', chain_id: 7 })
    expect(parseTarget(targetValue('block', 7))).toEqual({ action: 'block', chain_id: 0 })
    expect(parseTarget(targetValue('direct', 0))).toEqual({ action: 'direct', chain_id: 0 })
  })

  it('offers only the chains entered on the server', () => {
    const chain = (id: number, entry: number) =>
      ({ id, hops: [{ servers: [{ id: entry }] }, { servers: [{ id: 99 }] }] }) as unknown as Chain
    expect(chainsFrom([chain(1, 10), chain(2, 20), chain(3, 10)], 10).map((c) => c.id)).toEqual([1, 3])
  })
})

import { describe, expect, it } from 'vitest'

import { aftermath, explained, failureValues } from '@/lib/failures'

describe('aftermath', () => {
  it('reports only what the job itself said', () => {
    expect(aftermath({ restored: true })).toBe('restored')
    expect(aftermath({ restored: false })).toBe('notRestored')
  })

  it('assumes nothing when the job never said', () => {
    // A job that broke off before reporting has not put anything back as far
    // as anyone knows; "restored" here would send the administrator away from
    // a server that may be down.
    expect(aftermath({})).toBe('unknown')
    expect(aftermath({ output: 'boom' })).toBe('unknown')
    expect(aftermath(null)).toBe('unknown')
    expect(aftermath(undefined)).toBe('unknown')
    expect(aftermath({ restored: 'true' })).toBe('unknown')
  })
})

describe('explained', () => {
  it('reads a code the catalogue does not explain as internal', () => {
    expect(explained('port_in_use')).toBe('port_in_use')
    expect(explained('something_new')).toBe('internal')
  })
})

describe('failureValues', () => {
  it('lets plain values through to fill the blanks of a sentence', () => {
    expect(failureValues({ user: 'deploy', port: 443, path: '/usr/local/bin/xray' })).toEqual({
      user: 'deploy',
      port: 443,
      path: '/usr/local/bin/xray',
    })
  })

  it('keeps out what the translator would take for its own instructions', () => {
    // A server's output, or a detail of a new kind, must not be able to pick
    // another sentence, another language or a plural form.
    expect(
      failureValues({ count: 5, context: 'male', defaultValue: 'x', lng: 'en', ns: 'other', user: 'root' }),
    ).toEqual({ user: 'root' })
  })

  it('keeps out what is not a plain value', () => {
    expect(failureValues({ restored: true, servers: ['a'], nested: { a: 1 }, none: null })).toEqual({})
    expect(failureValues(undefined)).toEqual({})
    expect(failureValues(null)).toEqual({})
  })
})

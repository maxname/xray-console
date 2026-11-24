import { describe, expect, it } from 'vitest'

import { aftermath, explained } from '@/lib/failures'

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

import { describe, expect, it } from 'vitest'

import { ApiError } from '@/lib/api'
import { explainKnown, listed } from '@/lib/errors'

const t = (key: string) => key

function refusal(code: string, details: Record<string, unknown> = {}) {
  return new ApiError(409, code, 'refused', details)
}

describe('explainKnown', () => {
  it("uses the page's own text for a code the page names", () => {
    expect(explainKnown(t, refusal('name_taken'), 'chains', ['name_taken'])).toBe(
      'chains.errors.name_taken',
    )
  })

  it('says plainly what any page can meet', () => {
    // Deleted in another tab meanwhile; asked too often.
    expect(explainKnown(t, refusal('not_found'), 'chains', [])).toBe('common.errors.not_found')
    expect(explainKnown(t, refusal('rate_limited'), 'chains', [])).toBe(
      'common.errors.rate_limited',
    )
  })

  it('falls back to the general text for everything else', () => {
    expect(explainKnown(t, refusal('something_new'), 'chains', ['name_taken'])).toBe('common.error')
    expect(explainKnown(t, new Error('network'), 'chains', ['name_taken'])).toBe('common.error')
    expect(explainKnown(t, null, 'chains', [])).toBe('common.error')
  })
})

describe('listed', () => {
  it('reads the names an error lists, and nothing else', () => {
    expect(listed(refusal('x', { servers: ['ams-1', 'fra-2'] }), 'servers')).toBe('ams-1, fra-2')
    expect(listed(refusal('x', { servers: ['ams-1', 7, null] }), 'servers')).toBe('ams-1')
    expect(listed(refusal('x', { servers: 'ams-1' }), 'servers')).toBe('')
    expect(listed(refusal('x'), 'servers')).toBe('')
  })
})

import { describe, expect, it } from 'vitest'

import '@/i18n'
import { formatBytes, formatPercent } from '@/lib/format'

// The space between a number and its unit does not break.
const nbsp = '\u00a0'

describe('formatBytes', () => {
  it('picks the unit that fits and keeps three significant digits', () => {
    expect(formatBytes(0)).toBe(`0${nbsp}Б`)
    expect(formatBytes(900)).toBe(`900${nbsp}Б`)
    expect(formatBytes(1024)).toBe(`1${nbsp}КБ`)
    expect(formatBytes(1536)).toBe(`1,5${nbsp}КБ`)
    expect(formatBytes(37.5 * 1024 ** 2)).toBe(`37,5${nbsp}МБ`)
    expect(formatBytes(412 * 1024 ** 3)).toBe(`412${nbsp}ГБ`)
    expect(formatBytes(2.84 * 1024 ** 4)).toBe(`2,84${nbsp}ТБ`)
  })

  it('does not run out of units', () => {
    expect(formatBytes(5000 * 1024 ** 4)).toContain('ТБ')
  })
})

describe('formatPercent', () => {
  it('uses the decimal comma and leaves whole numbers whole', () => {
    expect(formatPercent(100)).toBe('100%')
    expect(formatPercent(99.9)).toBe('99,9%')
    expect(formatPercent(57.14)).toBe('57,14%')
    expect(formatPercent(0)).toBe('0%')
  })
})

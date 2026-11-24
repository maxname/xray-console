import { describe, expect, it } from 'vitest'

import { ru } from '@/i18n/ru'

// Every source file, as text.
const sources = import.meta.glob<string>('../**/*.{ts,tsx}', {
  query: '?raw',
  import: 'default',
  eager: true,
})

type Node = string | { [key: string]: Node }

/** Whether the catalogue has a text for the key — as it is, or in plural forms. */
function has(key: string): boolean {
  const parts = key.split('.')
  const last = parts.pop() as string
  let node: Node = ru as Node
  for (const part of parts) {
    if (typeof node === 'string' || !(part in node)) {
      return false
    }
    node = node[part]
  }
  if (typeof node === 'string') {
    return false
  }
  if (typeof node[last] === 'string') {
    return true
  }
  // Russian plurals: "servers_one", "servers_few", "servers_many".
  return ['one', 'few', 'many'].every((form) => typeof node[`${last}_${form}`] === 'string')
}

describe('the Russian catalogue', () => {
  it('has a text for every key the code asks for by name', () => {
    const missing: string[] = []
    let checked = 0
    for (const [file, text] of Object.entries(sources)) {
      if (file.includes('.test.')) {
        continue
      }
      // t('a.b.c') and t("a.b.c"); keys put together at run time are not
      // something a test can read.
      for (const match of text.matchAll(/\bt\(\s*['"]([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)+)['"]/g)) {
        checked++
        if (!has(match[1])) {
          missing.push(`${file}: ${match[1]}`)
        }
      }
    }
    // The search itself still finds the code's keys: a test that reads no
    // files would pass just as well.
    expect(checked).toBeGreaterThan(400)
    // A key with no text is shown to the administrator as the key itself.
    expect(missing).toEqual([])
  })

  it('has all three forms wherever it has a plural', () => {
    const incomplete: string[] = []
    const walk = (node: Node, path: string) => {
      if (typeof node === 'string') {
        return
      }
      const stems = new Set(
        Object.keys(node)
          .filter((key) => /_(one|few|many)$/.test(key))
          .map((key) => key.replace(/_(one|few|many)$/, '')),
      )
      for (const stem of stems) {
        for (const form of ['one', 'few', 'many']) {
          if (typeof node[`${stem}_${form}`] !== 'string') {
            incomplete.push(`${path}${stem}_${form}`)
          }
        }
      }
      for (const [key, child] of Object.entries(node)) {
        walk(child, `${path}${key}.`)
      }
    }
    walk(ru as Node, '')
    expect(incomplete).toEqual([])
  })
})

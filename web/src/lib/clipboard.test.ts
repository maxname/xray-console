import { afterEach, describe, expect, it, vi } from 'vitest'

import { copyText } from '@/lib/clipboard'

function secureContext(value: boolean) {
  Object.defineProperty(window, 'isSecureContext', { value, configurable: true })
}

function clipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
}

describe('copyText', () => {
  afterEach(() => {
    secureContext(false)
    clipboard(undefined)
    vi.restoreAllMocks()
  })

  it('uses the clipboard API where there is one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    secureContext(true)
    clipboard({ writeText })
    await expect(copyText('vless://link')).resolves.toBe(true)
    expect(writeText).toHaveBeenCalledWith('vless://link')
  })

  it('copies the old way on a panel opened over plain HTTP', async () => {
    // No secure context: navigator.clipboard is simply not there.
    secureContext(false)
    clipboard(undefined)
    let selected = ''
    document.execCommand = vi.fn(() => {
      selected = (document.activeElement as HTMLTextAreaElement).value
      return true
    })
    await expect(copyText('ssh-ed25519 AAAA panel')).resolves.toBe(true)
    expect(selected).toBe('ssh-ed25519 AAAA panel')
    // The field it copied from is gone again.
    expect(document.querySelector('textarea')).toBeNull()
  })

  it('falls back when the clipboard API refuses, and says so when nothing works', async () => {
    secureContext(true)
    clipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) })
    document.execCommand = vi.fn(() => false)
    await expect(copyText('text')).resolves.toBe(false)
    expect(document.execCommand).toHaveBeenCalledWith('copy')
  })
})

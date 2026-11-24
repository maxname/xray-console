import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { toast } from '@/lib/toast'

/**
 * Puts text on the clipboard and says whether it worked.
 *
 * The clipboard API exists only on a page opened over HTTPS or on localhost.
 * A panel reached by its address over plain HTTP — through a tunnel, on a home
 * network — has none, so there the text is selected in a hidden field and
 * copied the old way.
 */
export async function copyText(text: string): Promise<boolean> {
  if (window.isSecureContext && navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Refused — a permission, a lost focus. The old way may still work.
    }
  }
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  // Inside an open dialog focus cannot leave it, and a field that cannot be
  // focused cannot be copied from: put the field where the focus is allowed.
  const host = document.activeElement?.closest('[role="dialog"]') ?? document.body
  host.appendChild(area)
  area.focus()
  area.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    area.remove()
  }
}

/**
 * A copy button's behaviour: `copied` is true for two seconds after a copy
 * that worked; one that did not says so instead of looking like it did.
 */
export function useCopy(): { copied: boolean; copy: (text: string) => void } {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)
  const copy = useCallback(
    (text: string) => {
      void copyText(text).then((ok) => {
        if (!ok) {
          toast({ title: t('common.copyFailed'), variant: 'destructive' })
          return
        }
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      })
    },
    [t],
  )
  return { copied, copy }
}

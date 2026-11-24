import { useMutation } from '@tanstack/react-query'

import { api, ApiError, type DomainCheck, type ServerCert } from '@/lib/api'
import { type Translate } from '@/lib/errors'

export interface DomainCheckState {
  /** Where the domain leads, once asked. */
  result: DomainCheck | null
  pending: boolean
  /** Why asking failed, or null. */
  error: unknown
  canRun: boolean
  run: () => void
}

// Where a domain leads, asked of the panel before an installation finds out
// the hard way. An answer belongs to the domain and the address it was asked
// for: it is remembered with both, so editing either voids it without anyone
// having to remember to — the way a host key's fingerprint is kept.
export function useDomainCheck(domain: string, host: string): DomainCheckState {
  const target = `${domain.trim().toLowerCase()} ${host.trim()}`
  const mutation = useMutation({
    mutationFn: (to: { domain: string; host: string; target: string }) =>
      api.checkDomain(to.domain, to.host),
  })
  const current = mutation.variables?.target === target
  const canRun = domain.trim() !== '' && host.trim() !== ''
  return {
    result: current && mutation.isSuccess ? mutation.data : null,
    pending: current && mutation.isPending,
    error: current && mutation.isError ? mutation.error : null,
    canRun,
    run: () => {
      if (canRun) {
        mutation.mutate({ domain: domain.trim(), host: host.trim(), target })
      }
    },
  }
}

/** The DNS record to make at the registrar: its type for this address, or
 *  null when the server is known by a name, which has no single record. */
export function recordType(host: string): 'A' | 'AAAA' | null {
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) {
    return 'A'
  }
  return host.includes(':') ? 'AAAA' : null
}

/** What a failed check of a domain is called. */
export function explainDomainCheck(t: Translate, error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_domain' || error.code === 'invalid_host' || error.code === 'dns_failed') {
      return t(`domain.errors.${error.code}`)
    }
  }
  return t('common.error')
}

/** Whole days until a certificate runs out, by a clock in Unix seconds;
 *  negative once it has. */
export function daysLeft(cert: ServerCert, now: number): number | null {
  if (cert.not_after === null) {
    return null
  }
  return Math.floor((cert.not_after - now) / 86400)
}

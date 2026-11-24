import { useMutation } from '@tanstack/react-query'
import { useState } from 'react'

import { api, type HostKeyProbe } from '@/lib/api'

export interface HostKeyCheck {
  /** The key the machine at this address presented, once asked. */
  probe: HostKeyProbe | null
  /** Whether the administrator vouched for that key. */
  confirmed: boolean
  setConfirmed: (confirmed: boolean) => void
  fetch: () => void
  canFetch: boolean
  pending: boolean
  /** Why asking this address failed, or null. */
  error: unknown
  /** What to send as `host_key`: empty until a key is seen. */
  hostKey: string
  /** A key was seen and vouched for: the form may go. */
  ready: boolean
}

// The key a machine presents, shown to the administrator before any password
// is typed. A fingerprint belongs to one address: what was fetched and what
// was confirmed are both remembered with the address they were for, so
// changing the address voids them without anyone having to remember to.
// Typing the same address back brings them back: it is the address the key
// was seen at.
// Typing the same address back brings them back: it is the address the key
// was seen at.
export function useHostKeyCheck(host: string, port: string): HostKeyCheck {
  const target = `${host.trim()}:${port.trim()}`
  const [confirmedFor, setConfirmedFor] = useState<string | null>(null)
  const mutation = useMutation({
    mutationFn: (to: { host: string; port: number; target: string }) =>
      api.probeServer(to.host, to.port),
    // A key fetched again is vouched for again.
    onSuccess: () => setConfirmedFor(null),
  })
  // An answer about another address is not an answer about this one.
  const current = mutation.variables?.target === target
  const probe = current && mutation.isSuccess ? mutation.data : null
  const confirmed = probe !== null && confirmedFor === target
  return {
    probe,
    confirmed,
    setConfirmed: (next) => setConfirmedFor(next ? target : null),
    fetch: () => mutation.mutate({ host: host.trim(), port: Number(port), target }),
    canFetch: host.trim() !== '' && port.trim() !== '',
    pending: current && mutation.isPending,
    error: current && mutation.isError ? mutation.error : null,
    hostKey: probe?.host_key ?? '',
    ready: confirmed,
  }
}

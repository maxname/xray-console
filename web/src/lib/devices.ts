import { useQueryClient } from '@tanstack/react-query'

import { type DevicesResponse } from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'
import { devicesKey } from '@/lib/rules'

export const REFUSALS = [
  'name_taken',
  'invalid_name',
  'invalid_ports',
  'invalid_host',
  'invalid_port',
  'invalid_user',
  'bad_rule_set',
  'host_key_required',
  'busy',
  'nothing_to_retry',
] as const

export function explain(t: Translate, error: unknown): string {
  return explainKnown(t, error, 'devices', REFUSALS)
}

export function useDevicesCache() {
  const queryClient = useQueryClient()
  return (next: DevicesResponse) => {
    queryClient.setQueryData(devicesKey, next)
    // Devices and the local client are users of entry servers: what those
    // have waiting to be applied has just changed.
    void queryClient.invalidateQueries({ queryKey: ['rule-sets'] })
    void queryClient.invalidateQueries({ queryKey: ['subscriptions'] })
  }
}

// --- the panel's own computer -----------------------------------------------------

/** Failure codes a device has its own words for; the rest are a server's. */
export const OWN_ERRORS = [
  'port_in_use',
  'no_entry',
  'entry_pending',
  'entry_failed',
  'unsupported_os',
  'ssh_password_required',
  'ssh_unreachable',
] as const

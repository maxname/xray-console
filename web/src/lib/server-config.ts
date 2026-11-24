import { useQuery } from '@tanstack/react-query'

import {
  api,
  ApiError,
  type Server,
} from '@/lib/api'

export function configKey(serverId: number) {
  return ['servers', serverId, 'config'] as const
}

export function useServerConfig(server: Server) {
  return useQuery({
    queryKey: configKey(server.id),
    queryFn: () => api.getServerConfig(server.id),
    refetchInterval: (q) => (q.state.data?.busy ? 1500 : false),
  })
}

// --- header actions ------------------------------------------------------------

/** A field's name as the administrator knows it. */
export function fieldLabel(t: (key: string) => string, key: string): string {
  return t(`fields.${key}`)
}

/** Turns a failed save into a sentence. */
/** The servers an error names, as a list to read. */
export function serverNames(value: unknown): string {
  return Array.isArray(value) ? value.filter((name) => typeof name === 'string').join(', ') : ''
}

export function explain(
  t: (key: string, options?: Record<string, unknown>) => string,
  error: unknown,
): string {
  if (error instanceof ApiError) {
    if (error.code === 'invalid_field') {
      const reason = String(error.details.reason ?? 'generic')
      return t(`fieldErrors.${reason}`, { defaultValue: t('fieldErrors.generic') })
    }
    if (error.code === 'invalid_mask_host' || error.code === 'busy') {
      return t(`fieldErrors.${error.code}`)
    }
    if (error.code === 'upstream_pending') {
      return t('fieldErrors.upstream_pending', { servers: serverNames(error.details.servers) })
    }
  }
  return t('fieldErrors.generic')
}

/** Failure codes of an apply that have their own explanation; the wording is
 *  shared with the installation's. */
export const APPLY_ERRORS = [
  'config_rejected',
  'service_failed',
  'check_failed',
  'port_unreachable',
  'mask_unreachable',
  'ssh_unreachable',
  'ssh_key_rejected',
  'ssh_host_key_changed',
  'cancelled',
  'interrupted',
  'timeout',
] as const

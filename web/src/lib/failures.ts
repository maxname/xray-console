/** Failure codes the catalogue explains. Anything else reads as "internal". */
const EXPLAINED = [
  'ssh_password_rejected',
  'ssh_password_required',
  'ssh_key_rejected',
  'ssh_unreachable',
  'ssh_host_key_changed',
  'no_root',
  'unsupported_os',
  'unsupported_arch',
  'clock_skew',
  'disk_space',
  'xray_exists',
  'port_in_use',
  'download_failed',
  'upload_corrupted',
  'config_rejected',
  'update_rejected',
  'service_failed',
  'check_failed',
  'port_unreachable',
  'mask_unreachable',
  'domain_unresolved',
  'domain_mismatch',
  'domain_ipv6',
  'nginx_exists',
  'packages_failed',
  'acme_download_failed',
  'cert_failed',
  'cert_rate_limited',
  'stub_failed',
  'cert_invalid',
  'cancelled',
  'interrupted',
  'timeout',
  'internal',
] as const

export type ExplainedCode = (typeof EXPLAINED)[number]

export function explained(code: string): ExplainedCode {
  return (EXPLAINED as readonly string[]).includes(code)
    ? (code as ExplainedCode)
    : 'internal'
}

/**
 * What a failed apply or update left on the server, as far as the job said:
 * the previous state is back, it is not, or the job never got to say. Nothing
 * is assumed — "restored" shown without the server's word for it would send
 * the administrator away from a server that may be down.
 */
export type Aftermath = 'restored' | 'notRestored' | 'unknown'

export function aftermath(details: Record<string, unknown> | null | undefined): Aftermath {
  if (details?.restored === true) {
    return 'restored'
  }
  return details?.restored === false ? 'notRestored' : 'unknown'
}

/** Names the translator reads as its own instructions, not as values. */
const TRANSLATOR_OPTIONS = new Set([
  'count',
  'context',
  'defaultValue',
  'lng',
  'lngs',
  'ns',
  'replace',
  'returnObjects',
  'returnDetails',
  'joinArrays',
  'postProcess',
  'interpolation',
  'ordinal',
  'keySeparator',
  'nsSeparator',
  'fallbackLng',
  'skipInterpolation',
])

/**
 * A failure's details as values for a sentence of the catalogue.
 *
 * The details come from the panel's backend, and some of them from a server's
 * own output. Handed to the translator as they are, a detail that happened to
 * be called "count" or "defaultValue" would change which sentence is shown
 * instead of filling a blank in it. Only plain values under ordinary names go
 * through.
 */
export function failureValues(
  details: Record<string, unknown> | null | undefined,
): Record<string, string | number> {
  const values: Record<string, string | number> = {}
  for (const [key, value] of Object.entries(details ?? {})) {
    if (!TRANSLATOR_OPTIONS.has(key) && (typeof value === 'string' || typeof value === 'number')) {
      values[key] = value
    }
  }
  return values
}

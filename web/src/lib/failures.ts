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
  'cancelled',
  'interrupted',
  'internal',
] as const

export type ExplainedCode = (typeof EXPLAINED)[number]

export function explained(code: string): ExplainedCode {
  return (EXPLAINED as readonly string[]).includes(code)
    ? (code as ExplainedCode)
    : 'internal'
}

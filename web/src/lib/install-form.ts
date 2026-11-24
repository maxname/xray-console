import type { AuthMethod, InstallOptions } from '@/lib/api'

export type MaskMode = 'domain' | 'site'

/** What the administrator chooses before an installation starts. */
export interface InstallFormState {
  auth: AuthMethod
  password: string
  disablePasswordLogin: boolean
  /** What the server passes itself off as: its own domain, or someone else's site. */
  maskMode: MaskMode
  domain: string
  maskHost: string
  /** 0 lets the panel pick its default template. */
  templateId: number
}

export const initialInstallForm: InstallFormState = {
  auth: 'password',
  password: '',
  // On by default, as designed: once the key works, a password is only an
  // attack surface. Deleting the server from the panel turns it back on.
  disablePasswordLogin: true,
  // Someone else's site needs nothing prepared; a domain of one's own does.
  maskMode: 'site',
  domain: '',
  maskHost: '',
  templateId: 0,
}

export function toInstallOptions(state: InstallFormState): InstallOptions {
  return {
    auth: state.auth,
    password: state.auth === 'password' ? state.password : undefined,
    disable_password_login: state.disablePasswordLogin,
    ...(state.maskMode === 'domain'
      ? { domain: state.domain.trim() }
      : { mask_host: state.maskHost.trim() }),
    template_id: state.templateId || undefined,
  }
}

/** Errors the catalogue has its own sentence for, keyed by API code. */
const KNOWN_ERRORS = [
  'name_taken',
  'invalid_name',
  'invalid_host',
  'invalid_port',
  'invalid_user',
  'invalid_mask_host',
  'invalid_domain',
  'password_required',
  'host_key_required',
  'wrong_state',
  'busy',
] as const

type KnownError = (typeof KNOWN_ERRORS)[number]

export function isKnownFormError(code: string): code is KnownError {
  return (KNOWN_ERRORS as readonly string[]).includes(code)
}

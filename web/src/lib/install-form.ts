import type { AuthMethod, InstallOptions } from '@/lib/api'

/** What the administrator chooses before an installation starts. */
export interface InstallFormState {
  auth: AuthMethod
  password: string
  disablePasswordLogin: boolean
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
  maskHost: '',
  templateId: 0,
}

export function toInstallOptions(state: InstallFormState): InstallOptions {
  return {
    auth: state.auth,
    password: state.auth === 'password' ? state.password : undefined,
    disable_password_login: state.disablePasswordLogin,
    mask_host: state.maskHost.trim(),
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
  'password_required',
  'host_key_required',
  'wrong_state',
  'busy',
] as const

type KnownError = (typeof KNOWN_ERRORS)[number]

export function isKnownFormError(code: string): code is KnownError {
  return (KNOWN_ERRORS as readonly string[]).includes(code)
}

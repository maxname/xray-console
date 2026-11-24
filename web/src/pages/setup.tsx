import { useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { QRCodeSVG } from 'qrcode.react'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { AuthLayout, Field, Notice } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { api, ApiError, type TotpEnrollment } from '@/lib/api'

const MIN_PASSWORD_LENGTH = 12

/** Setup errors the catalogue has its own sentence for, keyed by API code. */
const KNOWN_ERRORS = [
  'password_too_short',
  'password_too_long',
  'invalid_username',
  'invalid_panel_url',
  'bad_setup_token',
  'already_set_up',
] as const

type KnownError = (typeof KNOWN_ERRORS)[number]

function isKnownError(code: string): code is KnownError {
  return (KNOWN_ERRORS as readonly string[]).includes(code)
}

/** The one-time code arrives in the link the panel prints at start-up. */
function tokenFromUrl(): string {
  // After the "#": that part of an address is never sent to a server, so the
  // code does not end up in the reverse proxy's log. Before it, for a link
  // printed by an older panel.
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, '')).get('token')
  return fragment ?? new URLSearchParams(window.location.search).get('token') ?? ''
}

export function SetupPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  // Set once the account exists and only the authenticator step is left.
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null)

  if (enrollment !== null) {
    return (
      <AuthLayout
        title={t('setup.totpStep.title')}
        description={t('setup.totpStep.description')}
      >
        <TotpStep enrollment={enrollment} onDone={() => void navigate({ to: '/' })} />
      </AuthLayout>
    )
  }

  return (
    <AuthLayout title={t('setup.title')} description={t('setup.description')}>
      <AccountStep
        onCreated={(totp) => {
          if (totp) {
            setEnrollment(totp)
          } else {
            void navigate({ to: '/' })
          }
        }}
      />
    </AuthLayout>
  )
}

function AccountStep({
  onCreated,
}: {
  onCreated: (totp: TotpEnrollment | undefined) => void
}) {
  const { t } = useTranslation()
  const [linkToken] = useState(tokenFromUrl)
  const [token, setToken] = useState(linkToken)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [confirmPassword, setConfirmPassword] = useState('')
  const [panelUrl, setPanelUrl] = useState(window.location.origin)
  const [enableTotp, setEnableTotp] = useState(false)
  const [mismatch, setMismatch] = useState(false)

  const mutation = useMutation({
    mutationFn: () =>
      api.setup({
        token: token.trim(),
        username: username.trim(),
        password,
        panel_url: panelUrl.trim(),
        enable_totp: enableTotp,
      }),
    onSuccess: (response) => onCreated(response.totp),
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (password !== confirmPassword) {
      setMismatch(true)
      return
    }
    setMismatch(false)
    mutation.mutate()
  }

  const errorMessage = (() => {
    if (mismatch) {
      return t('setup.errors.passwordMismatch')
    }
    if (!mutation.isError) {
      return null
    }
    const error = mutation.error
    if (error instanceof ApiError && isKnownError(error.code)) {
      return t(`setup.errors.${error.code}`, { count: MIN_PASSWORD_LENGTH })
    }
    return t('common.error')
  })()

  return (
    <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
      {/* With the link from the start-up output the code is already here and
          the form is exactly four fields. Asked for only when someone typed
          the address by hand. */}
      {linkToken === '' && (
        <Field hint={t('setup.tokenHint')}>
          <Label htmlFor="setup-token">{t('setup.token')}</Label>
          <Input
            id="setup-token"
            className="font-mono"
            autoComplete="off"
            spellCheck={false}
            required
            value={token}
            onChange={(event) => setToken(event.target.value)}
          />
        </Field>
      )}
      <Field>
        <Label htmlFor="setup-username">{t('setup.username')}</Label>
        <Input
          id="setup-username"
          autoComplete="username"
          spellCheck={false}
          autoCapitalize="off"
          required
          value={username}
          onChange={(event) => setUsername(event.target.value)}
        />
      </Field>
      <Field>
        <Label htmlFor="setup-password">{t('setup.password')}</Label>
        <Input
          id="setup-password"
          type="password"
          autoComplete="new-password"
          placeholder={t('setup.passwordPlaceholder', { count: MIN_PASSWORD_LENGTH })}
          minLength={MIN_PASSWORD_LENGTH}
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Field>
      <Field>
        <Label htmlFor="setup-confirm-password">{t('setup.confirmPassword')}</Label>
        <Input
          id="setup-confirm-password"
          type="password"
          autoComplete="new-password"
          required
          aria-invalid={mismatch || undefined}
          value={confirmPassword}
          onChange={(event) => setConfirmPassword(event.target.value)}
        />
      </Field>
      <Field hint={t('setup.panelUrlHint')}>
        <Label htmlFor="setup-panel-url">{t('setup.panelUrl')}</Label>
        <Input
          id="setup-panel-url"
          className="font-mono"
          type="url"
          autoComplete="off"
          spellCheck={false}
          required
          value={panelUrl}
          onChange={(event) => setPanelUrl(event.target.value)}
        />
      </Field>
      <div className="flex items-center justify-between gap-3">
        <span className="flex flex-col">
          <span id="setup-totp-label">{t('setup.totp')}</span>
          <span className="text-xs text-muted-foreground">{t('setup.totpHint')}</span>
        </span>
        <Switch
          aria-labelledby="setup-totp-label"
          checked={enableTotp}
          onCheckedChange={setEnableTotp}
        />
      </div>
      {errorMessage !== null && <Notice title={errorMessage} />}
      <Button type="submit" disabled={mutation.isPending}>
        {mutation.isPending ? t('setup.submitting') : t('setup.submit')}
      </Button>
    </form>
  )
}

function TotpStep({
  enrollment,
  onDone,
}: {
  enrollment: TotpEnrollment
  onDone: () => void
}) {
  const { t } = useTranslation()
  const [code, setCode] = useState('')

  const mutation = useMutation({
    mutationFn: () => api.confirmTotp(code),
    onSuccess: onDone,
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    mutation.mutate()
  }

  const errorMessage = (() => {
    if (!mutation.isError) {
      return null
    }
    const error = mutation.error
    if (error instanceof ApiError && error.code === 'totp_invalid') {
      return t('setup.totpStep.invalid')
    }
    return t('common.error')
  })()

  return (
    <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
      {/* White behind the code in both themes: scanners want dark-on-light,
          and a QR drawn in the dark theme's colours reads badly or not at all. */}
      <div className="self-center rounded-lg bg-white p-3">
        <QRCodeSVG
          value={enrollment.url}
          size={176}
          role="img"
          aria-label={t('setup.totpStep.qrLabel')}
        />
      </div>
      <Field>
        <span className="font-medium">{t('setup.totpStep.secret')}</span>
        <code
          className="rounded-lg bg-muted px-2.5 py-1.5 font-mono text-[13px] break-all select-all"
          translate="no"
        >
          {enrollment.secret.replace(/(.{4})(?=.)/g, '$1 ')}
        </code>
      </Field>
      <Field>
        <Label htmlFor="setup-totp-code">{t('setup.totpStep.code')}</Label>
        <Input
          id="setup-totp-code"
          className="font-mono tracking-widest"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={7}
          autoFocus
          required
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
      </Field>
      {errorMessage !== null && <Notice title={errorMessage} />}
      <Button type="submit" disabled={mutation.isPending}>
        {mutation.isPending
          ? t('setup.totpStep.confirming')
          : t('setup.totpStep.confirm')}
      </Button>
      <Button type="button" variant="ghost" onClick={onDone}>
        {t('setup.totpStep.skip')}
      </Button>
      <span className="text-xs text-muted-foreground">{t('setup.totpStep.skipNote')}</span>
    </form>
  )
}

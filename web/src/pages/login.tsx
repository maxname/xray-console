import { useMutation } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { AuthLayout, Field, Notice } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  api,
  ApiError,
  type InvalidCredentialsDetails,
  type LockedDetails,
  type LoginRequest,
} from '@/lib/api'
import { formatDayTime } from '@/lib/format'

export function LoginPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [remember, setRemember] = useState(false)
  // Flips once the server says the password was right and a code is the only
  // thing missing; the field is not shown to accounts without 2FA.
  const [needsCode, setNeedsCode] = useState(false)

  const mutation = useMutation({
    mutationFn: (body: LoginRequest) => api.login(body),
    onSuccess: () => void navigate({ to: '/' }),
    onError: (error) => {
      if (error instanceof ApiError && error.code === 'totp_required') {
        setNeedsCode(true)
      }
    },
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    mutation.mutate({
      username,
      password,
      code: needsCode ? code : undefined,
      remember,
    })
  }

  const error = mutation.isError ? mutation.error : null
  const apiError = error instanceof ApiError ? error : null
  const invalid =
    apiError?.code === 'invalid_credentials'
      ? (apiError.details as unknown as InvalidCredentialsDetails)
      : null
  const locked =
    apiError?.code === 'locked'
      ? (apiError.details as unknown as LockedDetails)
      : null
  // The first "a code is required" answer is a prompt, not a failure.
  const otherError = error !== null && invalid === null && locked === null &&
    apiError?.code !== 'totp_required'

  const clientIp = invalid?.client_ip ?? locked?.client_ip

  return (
    <AuthLayout title={t('login.title')} description={t('login.description')}>
      <form className="flex flex-col gap-5" onSubmit={handleSubmit}>
        {invalid !== null && (
          <Notice title={t(needsCode ? 'login.invalidCodeTitle' : 'login.invalidTitle')}>
            {t('login.attemptsLeft', {
              count: invalid.attempts_left,
              minutes: invalid.lock_minutes,
            })}
          </Notice>
        )}
        {locked !== null && (
          <Notice title={t('login.lockedTitle')}>
            {t('login.lockedRetry', {
              count: Math.max(1, Math.ceil(locked.retry_after_seconds / 60)),
            })}
          </Notice>
        )}
        {otherError && (
          <Notice title={t('login.unavailableTitle')}>{t('login.unavailable')}</Notice>
        )}

        <Field>
          <Label htmlFor="login-username">{t('login.username')}</Label>
          <Input
            id="login-username"
            autoComplete="username"
            spellCheck={false}
            autoCapitalize="off"
            required
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </Field>
        <Field>
          <Label htmlFor="login-password">{t('login.password')}</Label>
          <Input
            id="login-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        {needsCode && (
          <Field hint={t('login.codeHint')}>
            <Label htmlFor="login-code">{t('login.code')}</Label>
            <Input
              id="login-code"
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
        )}

        <label className="inline-flex min-h-8 cursor-pointer items-center gap-2">
          <input
            type="checkbox"
            className="size-4 accent-primary"
            checked={remember}
            onChange={(event) => setRemember(event.target.checked)}
          />
          <span>{t('login.remember')}</span>
        </label>

        <Button type="submit" disabled={mutation.isPending}>
          {mutation.isPending ? t('login.submitting') : t('login.submit')}
        </Button>

        {clientIp !== undefined && (
          <span className="text-xs text-muted-foreground">
            {t('login.address', { ip: clientIp })}
            {invalid?.previous_login_at !== undefined &&
              ` · ${t('login.previousLogin', {
                when: formatDayTime(invalid.previous_login_at),
              })}`}
            .
          </span>
        )}
        <span className="text-xs text-muted-foreground">
          {t('login.forgot')}{' '}
          <code className="font-mono" translate="no">
            xray-console reset-password
          </code>
          .
        </span>
      </form>
    </AuthLayout>
  )
}

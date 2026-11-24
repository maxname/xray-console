import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { useTranslation } from 'react-i18next'

import { Field, Notice } from '@/components/auth-layout'
import { Page, PageHeader, Panel } from '@/components/page'
import { TotpKey } from '@/components/totp-key'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { api, type AccountSession, type TotpEnrollment } from '@/lib/api'
import { explainKnown, type Translate } from '@/lib/errors'
import { formatDayTime } from '@/lib/format'
import { toast } from '@/lib/toast'
import { useMe } from '@/lib/use-me'

const MIN_PASSWORD_LENGTH = 12

const KNOWN_ERRORS = [
  'wrong_password',
  'password_too_short',
  'password_too_long',
  'totp_invalid',
  'totp_already_on',
  'totp_off',
] as const

function explain(t: Translate, error: unknown): string {
  return explainKnown(t, error, 'security', KNOWN_ERRORS)
}

const sessionsKey = ['sessions'] as const

/** The account: its password, two-factor sign-in, and where it is signed in. */
export function SecurityPage() {
  const { t } = useTranslation()
  return (
    <Page>
      <PageHeader title={t('security.title')} description={t('security.subtitle')} />
      <PasswordPanel />
      <TotpPanel />
      <SessionsPanel />
    </Page>
  )
}

// --- password -------------------------------------------------------------------

function PasswordPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [repeat, setRepeat] = useState('')
  const [mismatch, setMismatch] = useState(false)

  const change = useMutation({
    mutationFn: () => api.changePassword(current, next),
    onSuccess: () => {
      setCurrent('')
      setNext('')
      setRepeat('')
      toast({ title: t('security.password.done'), variant: 'success' })
      // Every other browser has just been signed out.
      void queryClient.invalidateQueries({ queryKey: sessionsKey })
    },
  })

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (next !== repeat) {
      setMismatch(true)
      return
    }
    setMismatch(false)
    change.mutate()
  }

  return (
    <Panel title={t('security.password.title')}>
      <p className="max-w-2xl text-muted-foreground">{t('security.password.text')}</p>
      <form className="flex max-w-sm flex-col gap-4" onSubmit={handleSubmit}>
        <Field>
          <Label htmlFor="security-current">{t('security.password.current')}</Label>
          <Input
            id="security-current"
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(event) => setCurrent(event.target.value)}
          />
        </Field>
        <Field hint={t('security.password.hint', { count: MIN_PASSWORD_LENGTH })}>
          <Label htmlFor="security-new">{t('security.password.next')}</Label>
          <Input
            id="security-new"
            type="password"
            autoComplete="new-password"
            minLength={MIN_PASSWORD_LENGTH}
            required
            value={next}
            onChange={(event) => setNext(event.target.value)}
          />
        </Field>
        <Field>
          <Label htmlFor="security-repeat">{t('security.password.repeat')}</Label>
          <Input
            id="security-repeat"
            type="password"
            autoComplete="new-password"
            required
            value={repeat}
            onChange={(event) => setRepeat(event.target.value)}
          />
        </Field>
        {mismatch && <Notice title={t('security.password.mismatch')} />}
        {change.isError && <Notice title={explain(t, change.error)} />}
        <Button type="submit" className="self-start" disabled={change.isPending}>
          {change.isPending ? t('security.saving') : t('security.password.submit')}
        </Button>
      </form>
    </Panel>
  )
}

// --- two-factor sign-in -----------------------------------------------------------

function TotpPanel() {
  const { t } = useTranslation()
  const { data: me } = useMe()
  // What the panel is in the middle of, if anything.
  const [step, setStep] = useState<'idle' | 'enabling' | 'disabling'>('idle')

  if (!me) {
    return (
      <Panel title={t('security.totp.title')}>
        <Skeleton className="h-5 w-64" />
      </Panel>
    )
  }

  return (
    <Panel title={t('security.totp.title')}>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={me.totp_enabled ? 'default' : 'secondary'}>
          {me.totp_enabled ? t('security.totp.on') : t('security.totp.off')}
        </Badge>
        <span className="max-w-2xl text-muted-foreground">
          {me.totp_enabled ? t('security.totp.onText') : t('security.totp.offText')}
        </span>
      </div>
      {step === 'idle' &&
        (me.totp_enabled ? (
          <Button variant="outline" className="self-start" onClick={() => setStep('disabling')}>
            {t('security.totp.disable')}
          </Button>
        ) : (
          <Button className="self-start" onClick={() => setStep('enabling')}>
            {t('security.totp.enable')}
          </Button>
        ))}
      {step === 'enabling' && <EnableTotp onClose={() => setStep('idle')} />}
      {step === 'disabling' && <DisableTotp onClose={() => setStep('idle')} />}
    </Panel>
  )
}

/** Password first, then the key to scan and a code to prove it was. */
function EnableTotp({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const [enrollment, setEnrollment] = useState<TotpEnrollment | null>(null)
  const [code, setCode] = useState('')

  const begin = useMutation({
    mutationFn: () => api.beginTotp(password),
    onSuccess: (issued) => {
      setEnrollment(issued)
      setPassword('')
    },
  })
  const confirm = useMutation({
    mutationFn: () => api.confirmTotp(code),
    onSuccess: () => {
      toast({ title: t('security.totp.enabled'), variant: 'success' })
      void queryClient.invalidateQueries({ queryKey: ['me'] })
      onClose()
    },
  })

  if (enrollment === null) {
    return (
      <form
        className="flex max-w-sm flex-col gap-4"
        onSubmit={(event) => {
          event.preventDefault()
          begin.mutate()
        }}
      >
        <Field hint={t('security.totp.passwordHint')}>
          <Label htmlFor="security-totp-password">{t('security.password.current')}</Label>
          <Input
            id="security-totp-password"
            type="password"
            autoComplete="current-password"
            autoFocus
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        {begin.isError && <Notice title={explain(t, begin.error)} />}
        <div className="flex gap-2">
          <Button type="submit" disabled={begin.isPending}>
            {t('security.totp.next')}
          </Button>
          <Button type="button" variant="outline" onClick={onClose}>
            {t('common.cancel')}
          </Button>
        </div>
      </form>
    )
  }

  return (
    <form
      className="flex max-w-sm flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        confirm.mutate()
      }}
    >
      <p className="text-muted-foreground">{t('setup.totpStep.description')}</p>
      <TotpKey enrollment={enrollment} />
      <Field>
        <Label htmlFor="security-totp-code">{t('setup.totpStep.code')}</Label>
        <Input
          id="security-totp-code"
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
      {confirm.isError && <Notice title={explain(t, confirm.error)} />}
      <div className="flex gap-2">
        <Button type="submit" disabled={confirm.isPending}>
          {confirm.isPending ? t('setup.totpStep.confirming') : t('security.totp.confirm')}
        </Button>
        <Button type="button" variant="outline" onClick={onClose}>
          {t('common.cancel')}
        </Button>
      </div>
      {/* Until a code is confirmed nothing has changed: sign-in is still by
          password alone, so leaving here is safe. */}
      <span className="text-xs text-muted-foreground">{t('security.totp.notYet')}</span>
    </form>
  )
}

function DisableTotp({ onClose }: { onClose: () => void }) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')

  const disable = useMutation({
    mutationFn: () => api.disableTotp(password, code),
    onSuccess: () => {
      toast({ title: t('security.totp.disabled') })
      void queryClient.invalidateQueries({ queryKey: ['me'] })
      onClose()
    },
  })

  return (
    <form
      className="flex max-w-sm flex-col gap-4"
      onSubmit={(event) => {
        event.preventDefault()
        disable.mutate()
      }}
    >
      <Notice tone="warning" title={t('security.totp.disableWarning')} />
      <Field>
        <Label htmlFor="security-off-password">{t('security.password.current')}</Label>
        <Input
          id="security-off-password"
          type="password"
          autoComplete="current-password"
          autoFocus
          required
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Field>
      <Field hint={t('security.totp.lostHint')}>
        <Label htmlFor="security-off-code">{t('setup.totpStep.code')}</Label>
        <Input
          id="security-off-code"
          className="font-mono tracking-widest"
          inputMode="numeric"
          autoComplete="one-time-code"
          maxLength={7}
          required
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
      </Field>
      {disable.isError && <Notice title={explain(t, disable.error)} />}
      <div className="flex gap-2">
        <Button type="submit" variant="destructive" disabled={disable.isPending}>
          {t('security.totp.disable')}
        </Button>
        <Button type="button" variant="outline" onClick={onClose}>
          {t('common.cancel')}
        </Button>
      </div>
    </form>
  )
}

// --- sessions -------------------------------------------------------------------

/** A browser and a system out of a User-Agent, for a row a person reads. */
function describeAgent(agent: string): string {
  const browser = /Edg\//.test(agent)
    ? 'Edge'
    : /OPR\//.test(agent)
      ? 'Opera'
      : /Firefox\//.test(agent)
        ? 'Firefox'
        : /Chrome\//.test(agent)
          ? 'Chrome'
          : /Safari\//.test(agent)
            ? 'Safari'
            : ''
  const system = /Android/.test(agent)
    ? 'Android'
    : /iPhone|iPad/.test(agent)
      ? 'iOS'
      : /Mac OS X/.test(agent)
        ? 'macOS'
        : /Windows/.test(agent)
          ? 'Windows'
          : /Linux/.test(agent)
            ? 'Linux'
            : ''
  if (browser === '') {
    // A script, a command-line client: its own name says more than a guess.
    return agent === '' ? '—' : agent.slice(0, 60)
  }
  return system === '' ? browser : `${browser} · ${system}`
}

function SessionsPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery({ queryKey: sessionsKey, queryFn: () => api.getSessions() })
  const refresh = () => queryClient.invalidateQueries({ queryKey: sessionsKey })

  const revoke = useMutation({
    mutationFn: (id: string) => api.revokeSession(id),
    onSettled: refresh,
  })
  const revokeOthers = useMutation({
    mutationFn: () => api.revokeOtherSessions(),
    onSuccess: (result) => {
      toast({ title: t('security.sessions.ended', { count: result.ended }), variant: 'success' })
    },
    onSettled: refresh,
  })

  const sessions = query.data?.sessions ?? []
  const others = sessions.filter((session) => !session.current).length

  return (
    <Panel title={t('security.sessions.title')}>
      <p className="max-w-2xl text-muted-foreground">{t('security.sessions.text')}</p>
      {query.isError && (
        <Notice title={t('common.loadFailed')}>
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            {t('common.retry')}
          </Button>
        </Notice>
      )}
      {query.isPending && <Skeleton className="h-5 w-full" />}
      {sessions.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t('security.sessions.browser')}</TableHead>
              <TableHead>{t('security.sessions.address')}</TableHead>
              <TableHead>{t('security.sessions.seen')}</TableHead>
              <TableHead>{t('security.sessions.expires')}</TableHead>
              <TableHead className="w-0" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {sessions.map((session) => (
              <SessionRow
                key={session.id}
                session={session}
                pending={revoke.isPending}
                onRevoke={() => revoke.mutate(session.id)}
              />
            ))}
          </TableBody>
        </Table>
      )}
      {(revoke.isError || revokeOthers.isError) && (
        <Notice title={explain(t, revoke.error ?? revokeOthers.error)} />
      )}
      {others > 0 && (
        <Button
          variant="outline"
          className="self-start"
          disabled={revokeOthers.isPending}
          onClick={() => revokeOthers.mutate()}
        >
          {t('security.sessions.endOthers')}
        </Button>
      )}
    </Panel>
  )
}

function SessionRow({
  session,
  pending,
  onRevoke,
}: {
  session: AccountSession
  pending: boolean
  onRevoke: () => void
}) {
  const { t } = useTranslation()
  return (
    <TableRow>
      <TableCell>
        <span className="flex flex-wrap items-center gap-2">
          <span title={session.user_agent}>{describeAgent(session.user_agent)}</span>
          {session.current && <Badge variant="secondary">{t('security.sessions.current')}</Badge>}
          {session.remember && <Badge variant="outline">{t('security.sessions.remembered')}</Badge>}
        </span>
      </TableCell>
      <TableCell className="font-mono text-[13px]" translate="no">
        {session.ip}
      </TableCell>
      <TableCell>{formatDayTime(session.last_seen_at)}</TableCell>
      <TableCell>{formatDayTime(session.expires_at)}</TableCell>
      <TableCell>
        {/* This browser is signed out with "sign out", where everything that
            goes with it happens too. */}
        {!session.current && (
          <Button variant="outline" size="sm" disabled={pending} onClick={onRevoke}>
            {t('security.sessions.end')}
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}

import { useTranslation } from 'react-i18next'

import { Field } from '@/components/auth-layout'
import { CopyBlock } from '@/components/install-fields'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { explainDomainCheck, recordType, useDomainCheck } from '@/lib/domain'

/**
 * A server's own domain: the name, the record to make for it at the
 * registrar, and what the panel sees when it asks DNS where the name leads.
 * The check is a courtesy — the installation asks again, with the server's
 * own word for its addresses — so nothing here stops a form from being sent.
 */
export function DomainField({
  id,
  value,
  onChange,
  host,
  disabled = false,
}: {
  id: string
  value: string
  onChange: (value: string) => void
  /** The server's address: what the domain has to lead to. */
  host: string
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const check = useDomainCheck(value, host)
  const domain = value.trim().toLowerCase()
  const address = host.trim()
  const record = recordType(address)

  return (
    <>
      <Field hint={t('domain.hint')}>
        <Label htmlFor={id}>{t('domain.name')}</Label>
        <Input
          id={id}
          className="font-mono"
          placeholder={t('domain.placeholder')}
          autoComplete="off"
          spellCheck={false}
          autoCapitalize="off"
          required
          disabled={disabled}
          value={value}
          onChange={(event) => onChange(event.target.value)}
          // Asked when the name is finished, not at every letter of it.
          onBlur={check.run}
        />
      </Field>

      <div className="flex flex-col gap-2 rounded-lg border p-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="font-medium">{t('domain.record.title')}</span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={!check.canRun || check.pending || disabled}
            onClick={check.run}
          >
            {check.pending
              ? t('domain.record.checking')
              : check.result !== null || check.error !== null
                ? t('domain.record.again')
                : t('domain.record.check')}
          </Button>
        </div>
        {domain !== '' && address !== '' && record !== null ? (
          <CopyBlock text={`${domain}  ${record}  ${address}`} />
        ) : (
          <span className="text-xs text-muted-foreground">
            {address === '' ? t('domain.record.needHost') : t('domain.record.byName', { host: address })}
          </span>
        )}
        <span className="text-xs text-muted-foreground">{t('domain.record.hint')}</span>
        <DomainVerdict check={check} host={address} />
      </div>
    </>
  )
}

function DomainVerdict({
  check,
  host,
}: {
  check: ReturnType<typeof useDomainCheck>
  host: string
}) {
  const { t } = useTranslation()
  if (check.pending) {
    return (
      <span role="status" className="text-muted-foreground">
        {t('domain.verdict.pending')}
      </span>
    )
  }
  if (check.error !== null) {
    return (
      <span role="alert" className="text-destructive">
        {explainDomainCheck(t, check.error)}
      </span>
    )
  }
  const result = check.result
  if (result === null) {
    return null
  }
  const found = result.addresses.join(', ')
  if (result.addresses.length === 0) {
    return (
      <span role="status" className="text-warning">
        {t('domain.verdict.none')}
      </span>
    )
  }
  if (!result.match) {
    return (
      <span role="status" className="text-destructive">
        {t('domain.verdict.elsewhere', { found, host })}
      </span>
    )
  }
  // The server's own or not: nothing answers for the domain there on 443.
  if (result.ipv6.length > 0) {
    return (
      <span role="status" className="text-destructive">
        {t('domain.verdict.ipv6', { ipv6: result.ipv6.join(', ') })}
      </span>
    )
  }
  return (
    <>
      <span role="status" className="text-success">
        {t('domain.verdict.match', { found })}
      </span>
      {result.other.length > 0 && (
        <span className="text-warning">
          {t('domain.verdict.other', { other: result.other.join(', ') })}
        </span>
      )}
    </>
  )
}

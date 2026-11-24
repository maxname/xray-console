import { useTranslation } from 'react-i18next'

import { Field } from '@/components/auth-layout'
import { DomainField } from '@/components/domain-field'
import { RadioCard } from '@/components/install-fields'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { MaskMode } from '@/lib/install-form'

export interface MaskValue {
  maskMode: MaskMode
  domain: string
  maskHost: string
}

/**
 * What REALITY passes the server off as: its own domain, with a stub site
 * and a certificate the panel sets up on the server, or someone else's site.
 * The same choice at installation and in an installed server's settings.
 */
export function MaskField<Value extends MaskValue>({
  value,
  onChange,
  host,
  name = 'install-mask',
}: {
  value: Value
  onChange: (next: Value) => void
  /** The server's address, for the domain's DNS record. */
  host: string
  /** Names the radio group; distinct where two of these can be on a page. */
  name?: string
}) {
  const { t } = useTranslation()
  return (
    <>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-2 font-medium">{t('installForm.maskMode')}</legend>
        <RadioCard
          name={name}
          checked={value.maskMode === 'domain'}
          onSelect={() => onChange({ ...value, maskMode: 'domain' })}
          title={t('installForm.ownDomain')}
          hint={t('installForm.ownDomainHint')}
        />
        <RadioCard
          name={name}
          checked={value.maskMode === 'site'}
          onSelect={() => onChange({ ...value, maskMode: 'site' })}
          title={t('installForm.otherSite')}
          hint={t('installForm.otherSiteHint')}
        />
      </fieldset>

      {value.maskMode === 'domain' ? (
        <>
          <DomainField
            id={`${name}-domain`}
            value={value.domain}
            onChange={(domain) => onChange({ ...value, domain })}
            host={host}
          />
          <p className="text-xs text-muted-foreground">{t('installForm.ownDomainNeeds')}</p>
        </>
      ) : (
        <Field hint={t('installForm.maskHostHint')}>
          <Label htmlFor={`${name}-host`}>{t('installForm.maskHost')}</Label>
          <Input
            id={`${name}-host`}
            className="font-mono"
            placeholder={t('installForm.maskHostPlaceholder')}
            autoComplete="off"
            spellCheck={false}
            autoCapitalize="off"
            required
            value={value.maskHost}
            onChange={(event) => onChange({ ...value, maskHost: event.target.value })}
          />
        </Field>
      )}
    </>
  )
}

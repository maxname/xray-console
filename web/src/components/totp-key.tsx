import { QRCodeSVG } from 'qrcode.react'
import { useTranslation } from 'react-i18next'

import { Field } from '@/components/auth-layout'
import type { TotpEnrollment } from '@/lib/api'

/** A two-factor secret as an authenticator app takes it: a code to scan, and
 *  the same key to type where there is no camera. */
export function TotpKey({ enrollment }: { enrollment: TotpEnrollment }) {
  const { t } = useTranslation()
  return (
    <>
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
    </>
  )
}

import { useTranslation } from 'react-i18next'

import type { OperationFailure } from '@/lib/api'
import { failureValues } from '@/lib/failures'
import { cn } from '@/lib/utils'

/**
 * What went wrong, in the catalogue's words: a title, a paragraph and, when
 * there is any, what the server itself printed.
 *
 * `prefix` names the catalogue entry (`<prefix>.title`, `<prefix>.text`).
 * `tellMessage` adds the raw message — for failures the catalogue can only
 * call "something else", where the message is all there is to go on.
 */
export function FailureExplained({
  failure,
  prefix,
  tellMessage,
  compact = false,
}: {
  failure: OperationFailure
  prefix: string
  tellMessage: boolean
  /** Inside a dialog: less room for the output. */
  compact?: boolean
}) {
  const { t } = useTranslation()
  const values = failureValues(failure.details)
  const output = typeof failure.details?.output === 'string' ? failure.details.output : ''
  return (
    <>
      <div className="flex flex-col gap-1">
        <p className="font-semibold">{t(`${prefix}.title`, values)}</p>
        <p>{t(`${prefix}.text`, values)}</p>
        {tellMessage && failure.message !== '' && (
          <code className="font-mono text-xs break-all text-muted-foreground" translate="no">
            {failure.message}
          </code>
        )}
      </div>
      {output !== '' && (
        <pre
          className={cn(
            'overflow-auto rounded-lg bg-muted font-mono text-xs whitespace-pre-wrap',
            compact ? 'max-h-40 p-2' : 'max-h-60 p-3',
          )}
          translate="no"
          tabIndex={0}
        >
          {output}
        </pre>
      )}
    </>
  )
}

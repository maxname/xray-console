import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Notice } from '@/components/auth-layout'
import { Panel } from '@/components/page'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  api,
  type ConfigField,
  type Server,
  type ServerConfig,
  type UpdateConfigRequest,
} from '@/lib/api'
import { cn } from '@/lib/utils'

export function Labelled({
  label,
  htmlFor,
  tag,
  hint,
  error,
  children,
}: {
  label: string
  htmlFor?: string
  tag?: ReactNode
  hint?: string
  error?: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        {htmlFor ? (
          <label htmlFor={htmlFor} className="font-medium">
            {label}
          </label>
        ) : (
          <span className="font-medium">{label}</span>
        )}
        {tag !== undefined && <span className="text-xs text-muted-foreground">{tag}</span>}
      </div>
      {children}
      {hint !== undefined && <span className="text-xs text-muted-foreground">{hint}</span>}
      {error ? (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      ) : null}
    </div>
  )
}

/** A text input that saves when the administrator is done with it — on leaving
 *  the field or pressing Enter — not on every keystroke. */
export function TextControl({
  id,
  value,
  disabled,
  placeholder,
  onCommit,
}: {
  id: string
  value: string
  disabled: boolean
  placeholder?: string
  onCommit: (value: string) => Promise<boolean>
}) {
  const [draft, setDraft] = useState(value)
  // Follow the saved value when it changes underneath: a reset, a new template.
  const [seen, setSeen] = useState(value)
  if (seen !== value) {
    setSeen(value)
    setDraft(value)
  }

  const commit = () => {
    if (draft.trim() !== value) {
      void onCommit(draft)
    }
  }

  return (
    <Input
      id={id}
      className="font-mono"
      value={draft}
      disabled={disabled}
      placeholder={placeholder}
      autoComplete="off"
      spellCheck={false}
      autoCapitalize="off"
      onChange={(event) => setDraft(event.target.value)}
      onBlur={commit}
      onKeyDown={(event) => {
        if (event.key === 'Enter') {
          event.preventDefault()
          commit()
        }
      }}
    />
  )
}

/** One template field on a server: inherited, overridden, or locked. */
export function FieldRow({
  field,
  disabled,
  placeholder,
  error,
  onChange,
}: {
  field: ConfigField
  disabled: boolean
  placeholder?: string
  error?: string
  /** null goes back to the template's value. */
  onChange: (value: string | null) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const id = `config-${field.key}`
  const locked = field.fixed || !field.overridable

  const tag = field.fixed ? (
    t('config.source.fixed')
  ) : !field.overridable ? (
    t('config.source.locked')
  ) : field.source === 'override' ? (
    <>
      {t('config.source.override', {
        value: field.template_value || t('config.source.empty'),
      })}{' '}
      <button
        type="button"
        className="text-primary underline-offset-4 hover:underline disabled:opacity-50"
        disabled={disabled}
        onClick={() => void onChange(null)}
      >
        {t('config.source.revert')}
      </button>
    </>
  ) : (
    t('config.source.template')
  )

  return (
    <Labelled label={t(`fields.${field.key}`)} htmlFor={id} tag={tag} error={error}>
      {field.type === 'enum' ? (
        <div id={id} role="radiogroup" className="flex flex-wrap gap-1.5">
          {(field.options ?? []).map((option) => (
            <button
              key={option}
              type="button"
              role="radio"
              aria-checked={field.value === option}
              disabled={disabled || locked}
              onClick={() => void onChange(option)}
              className={cn(
                'rounded-lg border px-2.5 py-1 font-mono text-[13px] transition-colors outline-none focus-visible:ring-3 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-60',
                field.value === option
                  ? 'border-primary bg-primary/10 text-foreground'
                  : 'hover:bg-muted',
              )}
            >
              {option}
            </button>
          ))}
        </div>
      ) : (
        <TextControl
          id={id}
          value={field.value}
          disabled={disabled || locked}
          placeholder={placeholder}
          onCommit={(value) => onChange(value)}
        />
      )}
    </Labelled>
  )
}

export function TemplatePanel({
  server,
  config,
  overridden,
  disabled,
  error,
  onUpdate,
}: {
  server: Server
  config: ServerConfig
  overridden: ConfigField[]
  disabled: boolean
  error?: string
  onUpdate: (body: UpdateConfigRequest) => Promise<boolean>
}) {
  const { t } = useTranslation()
  const templates = useQuery({
    queryKey: ['templates'],
    queryFn: () => api.listTemplates(),
  })
  const choices = templates.data?.templates ?? []
  const behind =
    config.applied_template_version !== null &&
    config.applied_template_version < config.template.version

  return (
    <Panel title={t('config.template.title')}>
      <p className="font-heading text-base font-semibold" translate="no">
        {config.template.name} v{config.template.version}
      </p>
      <p className="text-muted-foreground">
        {t('config.template.summary', {
          total: config.fields.length,
          inherited: config.fields.length - overridden.length,
          overridden: overridden.length,
        })}
      </p>
      {behind && (
        <p className="text-warning">
          {t('config.template.behind', {
            applied: config.applied_template_version,
            latest: config.template.version,
          })}
        </p>
      )}
      {choices.length > 1 && (
        <Labelled label={t('config.template.change')} htmlFor={`template-${server.id}`}>
          <Select
            value={String(config.template.id)}
            disabled={disabled}
            onValueChange={(value) => void onUpdate({ template_id: Number(value) })}
          >
            <SelectTrigger id={`template-${server.id}`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {choices.map((template) => (
                <SelectItem key={template.id} value={String(template.id)}>
                  {template.name} v{template.version}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Labelled>
      )}
      {error ? <Notice title={error} /> : null}
      <div className="flex flex-wrap gap-2">
        {overridden.length > 0 && (
          <Button
            variant="outline"
            size="sm"
            disabled={disabled}
            onClick={() =>
              void onUpdate({
                overrides: Object.fromEntries(overridden.map((item) => [item.key, null])),
              })
            }
          >
            {t('config.template.revertAll')}
          </Button>
        )}
        <Button asChild variant="outline" size="sm">
          <Link to="/templates" search={{ id: config.template.id }}>
            {t('config.template.open')}
          </Link>
        </Button>
      </div>
    </Panel>
  )
}

import { useQuery } from '@tanstack/react-query'
import { Check, Copy } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Field } from '@/components/auth-layout'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { api } from '@/lib/api'
import type { InstallFormState } from '@/lib/install-form'
import { cn } from '@/lib/utils'

interface FieldsProps {
  value: InstallFormState
  onChange: (next: InstallFormState) => void
}

/** How the panel signs in: the method, the password, the hardening switch. */
export function AccessFields({
  value,
  onChange,
  user,
}: FieldsProps & { user: string }) {
  const { t } = useTranslation()
  const { data: panelKey } = useQuery({
    queryKey: ['panel-key'],
    queryFn: () => api.getPanelKey(),
    staleTime: Infinity,
  })
  const keyName = panelKey?.name ?? 'panel-ed25519'

  return (
    <>
      <fieldset className="flex flex-col gap-2">
        <legend className="mb-2 font-medium">{t('installForm.how')}</legend>
        <RadioCard
          name="install-auth"
          checked={value.auth === 'password'}
          onSelect={() => onChange({ ...value, auth: 'password' })}
          title={t('installForm.byPassword')}
          hint={t('installForm.byPasswordHint', { key: keyName })}
        />
        <RadioCard
          name="install-auth"
          checked={value.auth === 'panel_key'}
          onSelect={() => onChange({ ...value, auth: 'panel_key', password: '' })}
          title={t('installForm.byKey')}
          hint={t('installForm.byKeyHint')}
        />
      </fieldset>

      {value.auth === 'password' ? (
        <Field>
          <Label htmlFor="install-password">
            {t('installForm.password', { user: user || 'root' })}
          </Label>
          <Input
            id="install-password"
            type="password"
            // Not the panel's own password: keep the browser from offering it.
            autoComplete="new-password"
            placeholder={t('installForm.passwordPlaceholder')}
            required
            value={value.password}
            onChange={(event) => onChange({ ...value, password: event.target.value })}
          />
        </Field>
      ) : (
        <Field hint={t('installForm.publicKeyHint')}>
          <span className="font-medium">{t('installForm.publicKey')}</span>
          <CopyBlock text={panelKey?.public_key ?? ''} />
        </Field>
      )}

      <div className="flex items-center justify-between gap-3">
        <span className="flex flex-col">
          <span id="install-disable-password">{t('installForm.disablePassword')}</span>
          <span className="text-xs text-muted-foreground">
            {t('installForm.disablePasswordHint')}
          </span>
        </span>
        <Switch
          aria-labelledby="install-disable-password"
          checked={value.disablePasswordLogin}
          onCheckedChange={(checked) =>
            onChange({ ...value, disablePasswordLogin: checked })
          }
        />
      </div>
    </>
  )
}

/** Which template the server is set up from. Hidden while there is only one
 *  to choose from. */
export function TemplateField({ value, onChange }: FieldsProps) {
  const { t } = useTranslation()
  const { data } = useQuery({
    queryKey: ['templates'],
    queryFn: () => api.listTemplates(),
  })
  const templates = data?.templates ?? []
  if (templates.length < 2) {
    return null
  }
  const selected = value.templateId || templates[0].id

  return (
    <Field hint={t('installForm.templateHint')}>
      <Label htmlFor="install-template">{t('installForm.template')}</Label>
      <Select
        value={String(selected)}
        onValueChange={(next) => onChange({ ...value, templateId: Number(next) })}
      >
        <SelectTrigger id="install-template" className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {templates.map((template) => (
            <SelectItem key={template.id} value={String(template.id)}>
              {template.name}
              {template.description !== '' ? ` — ${template.description}` : ''}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

/** The site REALITY passes the server off as. */
export function MaskField({ value, onChange }: FieldsProps) {
  const { t } = useTranslation()
  return (
    <Field hint={t('installForm.maskHostHint')}>
      <Label htmlFor="install-mask-host">{t('installForm.maskHost')}</Label>
      <Input
        id="install-mask-host"
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
  )
}

function RadioCard({
  name,
  checked,
  onSelect,
  title,
  hint,
}: {
  name: string
  checked: boolean
  onSelect: () => void
  title: string
  hint: ReactNode
}) {
  return (
    <label
      className={cn(
        'flex cursor-pointer items-start gap-3 rounded-lg border px-3 py-2.5 transition-colors',
        checked ? 'border-primary bg-primary/5' : 'hover:bg-muted/60',
      )}
    >
      <input
        type="radio"
        name={name}
        className="mt-1 size-4 accent-primary"
        checked={checked}
        onChange={onSelect}
      />
      <span className="flex min-w-0 flex-col">
        <span className="font-medium">{title}</span>
        <span className="text-xs text-muted-foreground">{hint}</span>
      </span>
    </label>
  )
}

/** Monospace text with a copy button — keys, commands, logs. */
export function CopyBlock({ text, className }: { text: string; className?: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)

  const copy = () => {
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  return (
    <div className={cn('flex items-start gap-2 rounded-lg bg-muted p-2.5', className)}>
      <code
        className="min-w-0 flex-1 font-mono text-[13px] break-all whitespace-pre-wrap"
        translate="no"
      >
        {text}
      </code>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        onClick={copy}
        disabled={text === ''}
        aria-label={copied ? t('common.copied') : t('common.copy')}
      >
        {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
      </Button>
    </div>
  )
}

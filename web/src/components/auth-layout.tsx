import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { BrandMark } from '@/components/brand-mark'
import { LanguageMenu, ThemeMenu } from '@/components/preference-menus'

interface AuthLayoutProps {
  title: string
  description: string
  children: ReactNode
}

/** The card the sign-in and setup screens share: brand, heading, then a form. */
export function AuthLayout({ title, description, children }: AuthLayoutProps) {
  const { t } = useTranslation()

  return (
    <div className="relative flex min-h-svh flex-col items-center justify-center bg-muted/40 p-4">
      {/* Both preferences matter most here: this is where someone gets a white
          page at 2am with no shell to fix it from. */}
      <div className="absolute top-4 right-4 flex items-center gap-1">
        <LanguageMenu />
        <ThemeMenu />
      </div>
      <main className="flex w-full max-w-110 flex-col gap-5 rounded-xl bg-card p-6 text-sm shadow-panel ring-1 ring-foreground/10 sm:p-8">
        <div className="flex items-center gap-2.5">
          <BrandMark />
          <span className="font-heading text-[15px] font-semibold" translate="no">
            {t('app.name')}
          </span>
        </div>
        <div className="flex flex-col gap-1">
          <h1 className="font-heading text-xl font-semibold">{title}</h1>
          <p className="text-muted-foreground">{description}</p>
        </div>
        {children}
      </main>
    </div>
  )
}

/** Label + control (+ hint), spaced as on the design's auth screens. */
export function Field({
  children,
  hint,
}: {
  children: ReactNode
  hint?: ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      {children}
      {hint !== undefined && (
        <span className="text-xs text-muted-foreground">{hint}</span>
      )}
    </div>
  )
}

/** A tinted notice with a bold first line — errors and lockouts on auth forms. */
export function Notice({
  title,
  children,
  tone = 'destructive',
}: {
  title: string
  children?: ReactNode
  tone?: 'destructive' | 'warning'
}) {
  const tones = {
    destructive: 'border-destructive/40 bg-destructive/8 [&>strong]:text-destructive',
    warning: 'border-warning/40 bg-warning/8 [&>strong]:text-warning',
  }
  return (
    <div
      role="alert"
      className={`flex flex-col gap-0.5 rounded-xl border px-4 py-3 ${tones[tone]}`}
    >
      <strong className="font-semibold">{title}</strong>
      {children !== undefined && <span>{children}</span>}
    </div>
  )
}

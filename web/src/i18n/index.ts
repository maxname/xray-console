import i18n from 'i18next'
import { initReactI18next } from 'react-i18next'

export const LANGUAGE_STORAGE_KEY = 'xray-console-lang'

/**
 * Russian only for now. Adding a language means: a catalogue file next to
 * `ru.ts` with the same keys, an entry here and in LANGUAGE_NAMES, and a loader
 * in `catalogues` below. The language menu appears on its own once there are
 * two; nothing else in the UI enumerates them.
 */
export const SUPPORTED_LANGUAGES = ['ru'] as const
export type Language = (typeof SUPPORTED_LANGUAGES)[number]

const DEFAULT_LANGUAGE: Language = 'ru'

/**
 * Each language named in itself — a menu of languages is the one place you
 * cannot translate the labels, since someone who lands in the wrong language
 * has to find their way out.
 */
export const LANGUAGE_NAMES: Record<Language, string> = {
  ru: 'Русский',
}

function isSupported(value: string | null | undefined): value is Language {
  return !!value && (SUPPORTED_LANGUAGES as readonly string[]).includes(value)
}

/**
 * Resolve the language up front, to one we definitely have: a stored choice
 * wins, then the browser's preference by base tag (`ru-RU` → `ru`), the
 * default otherwise.
 */
export function pickLanguage(
  stored: string | null,
  preferred: readonly string[],
): Language {
  if (isSupported(stored)) return stored
  for (const tag of preferred) {
    const base = tag.split('-')[0]
    if (isSupported(base)) return base
  }
  return DEFAULT_LANGUAGE
}

const catalogues: Record<Language, () => Promise<Record<string, unknown>>> = {
  ru: () => import('./ru').then((m) => m.ru),
}

function storedLanguage(): string | null {
  try {
    return localStorage.getItem(LANGUAGE_STORAGE_KEY)
  } catch {
    return null // Private mode / storage disabled: fall through to the browser.
  }
}

const initial = pickLanguage(
  storedLanguage(),
  navigator.languages ?? [navigator.language],
)

// Top-level await: the module graph settles before main.tsx renders, so the
// first paint already has its strings — no Suspense boundary, no flash of raw
// keys.
await i18n.use(initReactI18next).init({
  lng: initial,
  resources: { [initial]: { translation: await catalogues[initial]() } },
  supportedLngs: SUPPORTED_LANGUAGES,
  // Every catalogue carries the full key set, so there is nothing to fall
  // back to.
  fallbackLng: false,
  interpolation: {
    // React already escapes rendered strings.
    escapeValue: false,
  },
})

/**
 * Switch language, fetching the catalogue first: it is not in the bundle until
 * someone asks for it. Use this rather than `i18n.changeLanguage`, which would
 * switch to a language whose strings have not arrived.
 */
export async function changeLanguage(next: Language): Promise<void> {
  if (!i18n.hasResourceBundle(next, 'translation')) {
    i18n.addResourceBundle(next, 'translation', await catalogues[next]())
  }
  await i18n.changeLanguage(next)
  try {
    localStorage.setItem(LANGUAGE_STORAGE_KEY, next)
  } catch {
    // Storage disabled — the choice just won't outlive the tab.
  }
}

i18n.on('languageChanged', (lng) => {
  document.documentElement.lang = lng
})
document.documentElement.lang = i18n.language

export default i18n

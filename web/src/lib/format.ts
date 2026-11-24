import i18n from '@/i18n'

/**
 * A length of time in the one unit that fits it, in the accusative the
 * sentences here need: "14 минут", "2 часа", "3 дня".
 */
export function formatDuration(seconds: number): string {
  const units: [key: string, size: number][] = [
    ['days', 86400],
    ['hours', 3600],
    ['minutes', 60],
  ]
  for (const [key, size] of units) {
    if (seconds >= size) {
      return i18n.t(`duration.${key}`, { count: Math.floor(seconds / size) })
    }
  }
  return i18n.t('duration.seconds', { count: Math.max(1, Math.floor(seconds)) })
}

/** "23.11" from "2026-11-23". */
export function formatDay(day: string): string {
  const [, month, date] = day.split('-')
  return `${date}.${month}`
}

/** "31.12.2026" — the day a moment falls on, year included. */
export function formatDate(unixSeconds: number): string {
  return new Intl.DateTimeFormat(i18n.language, {
    day: '2-digit',
    month: '2-digit',
    year: 'numeric',
  }).format(new Date(unixSeconds * 1000))
}

/** "19.11 в 21:07" — a moment recent enough that the year adds nothing. */
export function formatDayTime(unixSeconds: number): string {
  const date = new Date(unixSeconds * 1000)
  const locale = i18n.language
  const day = new Intl.DateTimeFormat(locale, {
    day: '2-digit',
    month: '2-digit',
  }).format(date)
  const time = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
  return locale === 'ru' ? `${day} в ${time}` : `${day} ${time}`
}

/** "99,9%" — the decimal mark of the language; whole numbers stay whole. */
export function formatPercent(percent: number): string {
  return `${new Intl.NumberFormat(i18n.language, { maximumFractionDigits: 2 }).format(percent)}%`
}

const BYTE_UNITS = ['b', 'kb', 'mb', 'gb', 'tb'] as const

/**
 * A number of bytes in the unit that fits it, to three significant digits:
 * "412 ГБ", "2,84 ТБ", "37,5 МБ".
 */
export function formatBytes(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < BYTE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  const digits = unit === 0 || value >= 100 ? 0 : value >= 10 ? 1 : 2
  const number = new Intl.NumberFormat(i18n.language, { maximumFractionDigits: digits }).format(value)
  return `${number}\u00a0${i18n.t(`units.${BYTE_UNITS[unit]}`)}`
}

/**
 * The panel's clock as a person reads it: "Europe/Moscow, UTC+3", or only the
 * offset when the panel goes by its machine's own zone, which has no name
 * worth showing.
 */
export function formatPanelClock(timezone: string, offsetMinutes: number): string {
  const sign = offsetMinutes < 0 ? '−' : '+'
  const hours = Math.floor(Math.abs(offsetMinutes) / 60)
  const minutes = Math.abs(offsetMinutes) % 60
  const offset = `UTC${sign}${hours}${minutes === 0 ? '' : `:${String(minutes).padStart(2, '0')}`}`
  return timezone === '' || timezone === 'Local' ? offset : `${timezone}, ${offset}`
}

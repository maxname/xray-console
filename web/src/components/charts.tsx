import { useTranslation } from 'react-i18next'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { TrafficDay, UptimeDay } from '@/lib/api'
import { formatBytes, formatDay } from '@/lib/format'
import { cn } from '@/lib/utils'

/** A headline number with its label above and one line of context below. */
export function StatTile({ label, value, note }: { label: string; value: string; note: string }) {
  return (
    <div className="flex flex-col gap-1 rounded-xl bg-card p-5 text-sm shadow-panel ring-1 ring-foreground/10">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-heading text-3xl font-semibold tabular-nums">{value}</span>
      <span className="text-xs text-muted-foreground">{note}</span>
    </div>
  )
}

/**
 * Thirty days as thirty ticks. A day with a failed check is red and short —
 * shape as well as colour, so the bad days read without telling red from
 * green. Each tick says its day and its numbers on hover or focus.
 */
export function UptimeBar({ days, label }: { days: UptimeDay[]; label: string }) {
  const { t } = useTranslation()
  return (
    <div
      role="img"
      aria-label={label}
      className="col-span-2 flex h-5 items-end gap-0.5 md:col-span-1"
    >
      {days.map((day) => {
        const failed = day.total - day.ok
        const title =
          day.total === 0
            ? t('overview.availability.dayEmpty', { day: formatDay(day.day) })
            : failed === 0
              ? t('overview.availability.dayOk', { day: formatDay(day.day), total: day.total })
              : t('overview.availability.dayFailed', {
                  day: formatDay(day.day),
                  count: failed,
                  total: day.total,
                })
        return (
          <Tooltip key={day.day}>
            <TooltipTrigger asChild>
              {/* The hit area is the full height of the row; the mark inside
                  is what varies. */}
              <span className="flex h-full min-w-0 flex-1 items-end" tabIndex={-1}>
                <span
                  className={cn(
                    'w-full rounded-sm',
                    day.total === 0 && 'h-1 bg-border',
                    day.total > 0 && failed === 0 && 'h-full bg-chart-3',
                    failed > 0 && 'h-2 bg-destructive',
                  )}
                />
              </span>
            </TooltipTrigger>
            <TooltipContent>{title}</TooltipContent>
          </Tooltip>
        )
      })}
    </div>
  )
}

export function UptimeLegend() {
  const { t } = useTranslation()
  const item = (mark: string, text: string) => (
    <span className="inline-flex items-center gap-1.5">
      <span className="flex h-3 w-1.5 items-end">
        <span className={cn('w-full rounded-sm', mark)} />
      </span>
      {text}
    </span>
  )
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      {item('h-full bg-chart-3', t('overview.availability.legendOk'))}
      {item('h-1.5 bg-destructive', t('overview.availability.legendFailed'))}
      {item('h-0.5 bg-border', t('overview.availability.legendEmpty'))}
    </div>
  )
}


/** The two directions traffic is counted in, in the colours they are drawn in. */
export function TrafficLegend() {
  const { t } = useTranslation()
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
      <span className="inline-flex items-center gap-1.5">
        <span className="size-2.5 rounded-sm bg-traffic-down" />
        {t('traffic.down')}
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="size-2.5 rounded-sm bg-traffic-up" />
        {t('traffic.up')}
      </span>
    </div>
  )
}

/**
 * Traffic by day as columns: to clients below, from clients on top, a gap of
 * the surface between them. The tallest day fills the height; each column
 * says its day and its numbers on hover or focus.
 */
export function TrafficDays({ days, label }: { days: TrafficDay[]; label: string }) {
  const { t } = useTranslation()
  const peak = Math.max(1, ...days.map((day) => day.up + day.down))
  // Few columns have room for a date under each; a month does not.
  const dated = days.length <= 10
  return (
    <div role="img" aria-label={label} className="flex h-32 items-stretch gap-1">
      {days.map((day) => {
        const total = day.up + day.down
        const share = (value: number) => `${(value / peak) * 100}%`
        return (
          <Tooltip key={day.day}>
            <TooltipTrigger asChild>
              <span className="flex min-w-0 flex-1 flex-col justify-end gap-1" tabIndex={-1}>
                <span className="flex min-h-0 flex-1 flex-col justify-end gap-0.5">
                  {total === 0 ? (
                    <span className="h-0.5 w-full rounded-sm bg-border" />
                  ) : (
                    <>
                      {day.up > 0 && (
                        <span
                          className="w-full rounded-t-sm bg-traffic-up"
                          style={{ height: share(day.up), minHeight: 2 }}
                        />
                      )}
                      {day.down > 0 && (
                        <span
                          className={cn('w-full bg-traffic-down', day.up === 0 && 'rounded-t-sm')}
                          style={{ height: share(day.down), minHeight: 2 }}
                        />
                      )}
                    </>
                  )}
                </span>
                {dated && (
                  <span className="text-center text-[11px] leading-none text-muted-foreground tabular-nums">
                    {formatDay(day.day)}
                  </span>
                )}
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {total === 0
                ? t('traffic.dayEmpty', { day: formatDay(day.day) })
                : t('traffic.day', {
                    day: formatDay(day.day),
                    down: formatBytes(day.down),
                    up: formatBytes(day.up),
                  })}
            </TooltipContent>
          </Tooltip>
        )
      })}
    </div>
  )
}

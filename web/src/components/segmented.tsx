import { useRef, type KeyboardEvent } from 'react'

import { cn } from '@/lib/utils'

/**
 * A row of options of which one is chosen: a period, a rule set, a kind of
 * link. To the keyboard and to a screen reader it is a group of radio
 * buttons — one stop in the tab order, arrows move the choice — which is what
 * it is; it switches a view, it does not own panels the way tabs do.
 */
export function Segmented<Value extends string | number>({
  label,
  options,
  value,
  onChange,
  className,
}: {
  /** What is being chosen, for those who cannot see the row. */
  label: string
  options: readonly { value: Value; label: string }[]
  value: Value
  onChange: (value: Value) => void
  className?: string
}) {
  const buttons = useRef<(HTMLButtonElement | null)[]>([])

  const move = (event: KeyboardEvent<HTMLButtonElement>, from: number) => {
    const step =
      event.key === 'ArrowRight' || event.key === 'ArrowDown'
        ? 1
        : event.key === 'ArrowLeft' || event.key === 'ArrowUp'
          ? -1
          : 0
    const to =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? options.length - 1
          : step === 0
            ? -1
            : (from + step + options.length) % options.length
    if (to === -1) {
      return
    }
    event.preventDefault()
    onChange(options[to].value)
    buttons.current[to]?.focus()
  }

  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn('flex flex-wrap gap-1 rounded-lg bg-muted p-1', className)}
    >
      {options.map((option, index) => {
        const chosen = option.value === value
        return (
          <button
            key={option.value}
            ref={(node) => {
              buttons.current[index] = node
            }}
            type="button"
            role="radio"
            aria-checked={chosen}
            tabIndex={chosen ? 0 : -1}
            className={cn(
              'rounded-md px-3 py-1.5 text-sm font-medium outline-none focus-visible:ring-3 focus-visible:ring-ring/50',
              chosen ? 'bg-card shadow-sm' : 'text-muted-foreground hover:text-foreground',
            )}
            onClick={() => onChange(option.value)}
            onKeyDown={(event) => move(event, index)}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

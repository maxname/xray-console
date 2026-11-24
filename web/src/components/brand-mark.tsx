import { cn } from '@/lib/utils'

/**
 * The "X" tile. Drawn with text rather than an image so it takes --primary in
 * both themes without a second asset.
 *
 * aria-hidden — decorative: the product name is always next to it as text.
 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        'flex size-7 shrink-0 items-center justify-center rounded-lg bg-primary font-heading font-bold text-primary-foreground select-none',
        className,
      )}
    >
      X
    </span>
  )
}

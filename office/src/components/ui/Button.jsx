/**
 * Button — Vulos Office design system
 * ----------------------------------------------------------------------------
 * Variants:
 *   - primary      Accent (deep teal) — the one "yes" affordance per surface
 *   - secondary    Quiet, paper background + line border — default for chrome
 *   - ghost        No background; for toolbars, tertiary actions
 *   - destructive  Persimmon — soft danger; rarely used
 *   - link         Underlined text-only button (use sparingly)
 *
 * Sizes: sm | md | lg
 *
 * The component is a thin styling layer over <button> — it keeps every API
 * (onClick, disabled, type, …) and forwards refs. No icons baked in: the
 * caller composes its own children, which is the Linear pattern.
 */

import { forwardRef } from 'react'

const sizeClasses = {
  sm: 'h-7 px-2.5 text-xs gap-1.5',
  md: 'h-8 px-3 text-sm gap-2',
  lg: 'h-10 px-4 text-md gap-2',
}

// Base — shared rules for every variant. `tracking-tightish` is the Vulos
// signature; soft shadow only on primary (so it reads slightly raised).
const base =
  'inline-flex items-center justify-center font-medium tracking-tightish ' +
  'rounded-md whitespace-nowrap select-none ' +
  'transition-[background,color,box-shadow,transform] duration-fast ease-out ' +
  'disabled:opacity-50 disabled:cursor-not-allowed ' +
  'focus-visible:outline-none focus-visible:shadow-focus ' +
  'active:translate-y-px'

const variantClasses = {
  primary:
    'bg-accent text-white shadow-e1 ' +
    'hover:bg-accent-hover ' +
    'active:bg-accent-press',
  secondary:
    'bg-paper text-ink border border-line ' +
    'hover:bg-bg-elev2 hover:border-line-strong',
  ghost:
    'bg-transparent text-ink-muted ' +
    'hover:bg-accent-tint hover:text-ink',
  destructive:
    'bg-danger-bg text-danger border border-transparent ' +
    'hover:bg-danger hover:text-white',
  link:
    'bg-transparent text-accent px-0 h-auto underline underline-offset-2 ' +
    'hover:text-accent-hover',
}

const Button = forwardRef(function Button(
  {
    variant = 'secondary',
    size = 'md',
    className = '',
    type = 'button',
    fullWidth = false,
    children,
    ...rest
  },
  ref,
) {
  const cn = [
    base,
    variantClasses[variant] || variantClasses.secondary,
    variant !== 'link' ? sizeClasses[size] || sizeClasses.md : '',
    fullWidth ? 'w-full' : '',
    className,
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <button ref={ref} type={type} className={cn} {...rest}>
      {children}
    </button>
  )
})

export default Button

/**
 * IconButton — square button for icon-only affordances (toolbars, headers).
 *
 * Sizes are matched to the icon scale you'd pass in (lucide-react size= prop):
 *   - sm  → 14–16px icon, 28px button (h-7 w-7)
 *   - md  → 16–18px icon, 32px button (h-8 w-8)  — default
 *   - lg  → 20px icon,    40px button (h-10 w-10)
 *
 * Variants:
 *   - default  ghost-style; for toolbars
 *   - solid    accent tint when active (`active` prop)
 */

import { forwardRef } from 'react'

const sizeClasses = {
  sm: 'h-7 w-7',
  md: 'h-8 w-8',
  lg: 'h-10 w-10',
}

const IconButton = forwardRef(function IconButton(
  {
    size = 'md',
    active = false,
    className = '',
    title,
    children,
    ...rest
  },
  ref,
) {
  const cn = [
    'inline-flex items-center justify-center shrink-0',
    'rounded-md text-ink-muted',
    'transition-[background,color] duration-fast ease-out',
    'hover:bg-accent-tint hover:text-ink',
    'disabled:opacity-40 disabled:cursor-not-allowed',
    'focus-visible:outline-none focus-visible:shadow-focus',
    active ? 'bg-accent-tint-2 text-accent-press' : '',
    sizeClasses[size] || sizeClasses.md,
    className,
  ]
    .filter(Boolean)
    .join(' ')

  return (
    <button ref={ref} type="button" title={title} aria-label={title} className={cn} {...rest}>
      {children}
    </button>
  )
})

export default IconButton

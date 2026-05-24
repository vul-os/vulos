/**
 * Tooltip — minimal, CSS-only tooltip wrapper.  Hovers after a short delay so
 * the chrome doesn't flicker when sweeping the cursor across a toolbar.
 *
 * Usage:
 *   <Tooltip label="Bold (⌘B)"><IconButton>…</IconButton></Tooltip>
 *
 * We deliberately avoid floating-ui / portals for the budget. A wrapper span
 * lays the bubble out absolutely; positioning is `top` only — sufficient for
 * our toolbar / topbar usage. For richer positioning, the surrounding scroll
 * container will clip and we accept that for now.
 */

export default function Tooltip({ label, children, side = 'bottom', className = '' }) {
  if (!label) return children
  const positions = {
    bottom: 'top-full mt-1.5 left-1/2 -translate-x-1/2',
    top:    'bottom-full mb-1.5 left-1/2 -translate-x-1/2',
    right:  'left-full ml-1.5 top-1/2 -translate-y-1/2',
    left:   'right-full mr-1.5 top-1/2 -translate-y-1/2',
  }
  return (
    <span className={`group relative inline-flex ${className}`}>
      {children}
      <span
        role="tooltip"
        className={[
          'pointer-events-none absolute z-40 whitespace-nowrap',
          positions[side] || positions.bottom,
          'px-2 py-1 rounded-sm text-2xs font-medium tracking-tightish',
          'bg-ink text-paper shadow-e2',
          'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100',
          'translate-y-0.5 group-hover:translate-y-0 group-focus-within:translate-y-0',
          'transition-[opacity,transform] duration-base ease-out',
          'delay-300 group-hover:delay-300',
        ].join(' ')}
      >
        {label}
      </span>
    </span>
  )
}

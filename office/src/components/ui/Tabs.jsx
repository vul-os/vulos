/**
 * Tabs — restrained, underline-only.  No chunky pill backgrounds.
 *
 * Usage:
 *   <Tabs value={state} onChange={setState} items={[
 *     { value: 'all', label: 'All' },
 *     { value: 'open', label: 'Open', count: 3 },
 *   ]} />
 */

export default function Tabs({ value, onChange, items, className = '' }) {
  return (
    <div
      role="tablist"
      className={`flex items-stretch border-b border-line ${className}`}
    >
      {items.map(({ value: v, label, count }) => {
        const selected = v === value
        return (
          <button
            key={v}
            role="tab"
            aria-selected={selected}
            onClick={() => onChange?.(v)}
            className={[
              'group relative inline-flex items-center gap-1.5 px-3 py-2 text-xs',
              'font-medium tracking-tightish transition-colors duration-fast ease-out',
              selected
                ? 'text-ink'
                : 'text-ink-faint hover:text-ink-muted',
            ].join(' ')}
          >
            {label}
            {typeof count === 'number' && (
              <span
                className={`text-2xs px-1 rounded-xs ${
                  selected
                    ? 'bg-accent-tint-2 text-accent-press'
                    : 'bg-bg-elev2 text-ink-faint'
                }`}
              >
                {count}
              </span>
            )}
            {/* underline indicator */}
            <span
              className={[
                'absolute left-2 right-2 -bottom-px h-px transition-colors duration-base ease-out',
                selected ? 'bg-accent' : 'bg-transparent',
              ].join(' ')}
            />
          </button>
        )
      })}
    </div>
  )
}

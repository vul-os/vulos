import { useState, useEffect } from 'react'
import { getAppsByCategory } from '../core/AppRegistry.js'

// ─── Clock ────────────────────────────────────────────────────────────────────

function useClock() {
  const [time, setTime] = useState(() => new Date())
  useEffect(() => {
    const id = setInterval(() => setTime(new Date()), 1000)
    return () => clearInterval(id)
  }, [])
  return time
}

// ─── Status bar ───────────────────────────────────────────────────────────────

function TVStatusBar() {
  const time = useClock()
  const hh = time.getHours().toString().padStart(2, '0')
  const mm = time.getMinutes().toString().padStart(2, '0')
  const dateStr = time.toLocaleDateString(undefined, {
    weekday: 'long',
    month: 'long',
    day: 'numeric',
  })

  return (
    <div
      className="shrink-0 flex items-center justify-between px-12 py-4"
      style={{ background: 'rgba(0,0,0,0.6)' }}
    >
      {/* Brand */}
      <div className="flex items-center gap-3">
        <img src="/vulos.png" alt="" className="w-8 h-8 opacity-80" />
        <span
          className="font-semibold tracking-widest uppercase"
          style={{ fontSize: '1.1rem', color: '#e5e5e5', letterSpacing: '0.25em' }}
        >
          VulOS
        </span>
      </div>

      {/* Clock + date */}
      <div className="text-right">
        <div
          className="font-light tabular-nums"
          style={{ fontSize: '2.5rem', color: '#ffffff', lineHeight: 1 }}
        >
          {hh}<span style={{ opacity: 0.6 }}>:</span>{mm}
        </div>
        <div style={{ fontSize: '0.95rem', color: '#a3a3a3', marginTop: '0.15rem' }}>
          {dateStr}
        </div>
      </div>
    </div>
  )
}

// ─── App card ────────────────────────────────────────────────────────────────

function TVAppCard({ app }) {
  return (
    <button
      className="tv-card group flex flex-col items-center justify-center gap-4 rounded-2xl transition-all duration-150"
      style={{
        width: '160px',
        minWidth: '160px',
        height: '140px',
        background: 'rgba(255,255,255,0.07)',
        border: '2px solid rgba(255,255,255,0.10)',
        cursor: 'pointer',
        outline: 'none',
      }}
      onFocus={(e) => {
        e.currentTarget.style.background = 'rgba(255,255,255,0.18)'
        e.currentTarget.style.border = '2px solid rgba(255,255,255,0.55)'
        e.currentTarget.style.transform = 'scale(1.08)'
        e.currentTarget.style.boxShadow = '0 0 0 4px rgba(255,255,255,0.12), 0 8px 32px rgba(0,0,0,0.5)'
      }}
      onBlur={(e) => {
        e.currentTarget.style.background = 'rgba(255,255,255,0.07)'
        e.currentTarget.style.border = '2px solid rgba(255,255,255,0.10)'
        e.currentTarget.style.transform = 'scale(1)'
        e.currentTarget.style.boxShadow = 'none'
      }}
      aria-label={app.name}
    >
      {/* Icon */}
      <span
        style={{
          fontSize: typeof app.icon === 'string' && app.icon.length <= 2 ? '2.5rem' : '1.8rem',
          lineHeight: 1,
          color: '#ffffff',
          display: 'block',
        }}
      >
        {app.icon}
      </span>

      {/* Name */}
      <span
        style={{
          fontSize: '1rem',
          fontWeight: 600,
          color: '#e5e5e5',
          letterSpacing: '0.02em',
          textAlign: 'center',
          paddingInline: '0.5rem',
          lineHeight: 1.2,
        }}
      >
        {app.name}
      </span>
    </button>
  )
}

// ─── Category row ─────────────────────────────────────────────────────────────

const CATEGORY_LABELS = {
  system: 'System',
  internet: 'Internet',
  productivity: 'Productivity',
  media: 'Media',
  other: 'Other',
}

function TVCategoryRow({ category, apps }) {
  const label = CATEGORY_LABELS[category] || category.charAt(0).toUpperCase() + category.slice(1)

  return (
    <div className="shrink-0" style={{ paddingBottom: '2rem' }}>
      {/* Row heading */}
      <div
        style={{
          fontSize: '0.85rem',
          fontWeight: 700,
          letterSpacing: '0.18em',
          textTransform: 'uppercase',
          color: '#737373',
          marginBottom: '1.2rem',
          paddingInline: '3rem',
        }}
      >
        {label}
      </div>

      {/* Horizontal scroll of cards */}
      <div
        className="flex gap-5 overflow-x-auto"
        style={{
          paddingInline: '3rem',
          paddingBottom: '0.5rem',
          scrollbarWidth: 'none',
        }}
      >
        {apps.map((app) => (
          <TVAppCard key={app.id} app={app} />
        ))}
      </div>
    </div>
  )
}

// ─── TVHome ───────────────────────────────────────────────────────────────────

const CATEGORY_ORDER = ['media', 'internet', 'productivity', 'system', 'other']

export default function TVHome() {
  const byCategory = getAppsByCategory()

  // Sort categories: known order first, then any remaining alphabetically
  const orderedCategories = [
    ...CATEGORY_ORDER.filter((c) => byCategory[c]?.length > 0),
    ...Object.keys(byCategory)
      .filter((c) => !CATEGORY_ORDER.includes(c) && byCategory[c]?.length > 0)
      .sort(),
  ]

  return (
    <div
      className="fixed inset-0 flex flex-col overflow-hidden"
      style={{
        background: '#0a0a0a',
        color: '#ffffff',
        fontFamily: 'system-ui, -apple-system, sans-serif',
      }}
    >
      {/* Status bar */}
      <TVStatusBar />

      {/* Divider */}
      <div style={{ height: '1px', background: 'rgba(255,255,255,0.06)', flexShrink: 0 }} />

      {/* Category rows — vertically scrollable */}
      <div
        className="flex-1 overflow-y-auto"
        style={{ paddingTop: '2.5rem', scrollbarWidth: 'none' }}
      >
        {orderedCategories.map((cat) => (
          <TVCategoryRow key={cat} category={cat} apps={byCategory[cat]} />
        ))}

        {/* Bottom breathing room */}
        <div style={{ height: '3rem' }} />
      </div>
    </div>
  )
}

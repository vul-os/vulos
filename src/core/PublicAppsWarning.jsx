import { useState, useEffect } from 'react'
import { useNarrow } from '../shell/useNarrow'
import '../shell/shell-chrome.css'

// PublicAppsWarning — the shell's NETWORK-EXPOSURE indicator (the "public ports"
// widget). It lives in the top-bar system tray and answers, at a glance, the one
// question that matters for a sovereign box: "is anything on this machine
// reachable beyond me?".
//
// Redesigned to fit the shell chrome (soft, token-driven chip instead of a solid
// alarm pill) and to convey state clearly:
//   · nothing exposed        → the chip doesn't render at all (calm by default)
//   · local-network only      → amber chip, LAN glyph, static dot
//   · anything public         → danger chip, globe glyph, gently pulsing dot
// Click opens the Public App Manager to toggle any app on/off.

const VIS_POLL_INTERVAL = 10_000 // 10 seconds

function useVisPublicApps() {
  const [visData, setVisData] = useState({ count: 0, hasPublic: false })

  useEffect(() => {
    let cancelled = false

    async function visFetch() {
      try {
        const res = await fetch('/api/apps/visibility')
        if (!res.ok) return
        const list = await res.json()
        if (cancelled) return
        const nonPrivate = list.filter((a) => a.visibility !== 'private')
        const hasPublic = nonPrivate.some((a) => a.visibility === 'public')
        setVisData({ count: nonPrivate.length, hasPublic })
      } catch {
        // network error — keep previous state
      }
    }

    visFetch()
    const id = setInterval(visFetch, VIS_POLL_INTERVAL)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [])

  return visData
}

// Anything reachable from the open internet — a globe.
function GlobeGlyph() {
  return (
    <svg viewBox="0 0 16 16" className="w-3 h-3 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true">
      <circle cx="8" cy="8" r="6.2" />
      <path d="M2 8h12M8 1.8c1.9 1.7 1.9 10.7 0 12.4M8 1.8c-1.9 1.7-1.9 10.7 0 12.4" />
    </svg>
  )
}

// Shared only on the local network — a broadcast / signal glyph.
function LanGlyph() {
  return (
    <svg viewBox="0 0 16 16" className="w-3 h-3 shrink-0" fill="none" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" aria-hidden="true">
      <path d="M3.5 6.5a6 6 0 019 0" opacity="0.55" />
      <path d="M5.3 8.6a3.4 3.4 0 015.4 0" />
      <circle cx="8" cy="11.5" r="1.1" fill="currentColor" stroke="none" />
    </svg>
  )
}

export default function PublicAppsWarning() {
  const narrow = useNarrow(760)
  const { count, hasPublic } = useVisPublicApps()

  // Calm by default: with nothing exposed the chip stays out of the bar entirely.
  if (count === 0) return null

  const fg = hasPublic ? 'var(--status-danger)' : 'var(--status-warning)'
  const bg = hasPublic ? 'var(--status-danger-soft)' : 'var(--status-warning-soft)'
  const bd = `color-mix(in srgb, ${hasPublic ? 'var(--status-danger)' : 'var(--status-warning)'} 38%, transparent)`

  const label = hasPublic ? 'Public' : 'Local'
  const title = hasPublic
    ? `${count} app${count !== 1 ? 's' : ''} reachable on the public web — click to manage exposure`
    : `${count} app${count !== 1 ? 's' : ''} shared on your local network — click to manage exposure`

  function visHandleClick() {
    window.dispatchEvent(new CustomEvent('vulos:open-public-apps'))
  }

  return (
    <button
      onClick={visHandleClick}
      title={title}
      aria-label={title}
      style={{ '--chip-fg': fg, '--chip-bg': bg, '--chip-bd': bd }}
      className="vshell-chip group inline-flex items-center gap-1.5 h-6 pl-1.5 pr-1 rounded-full
        text-[10px] font-semibold leading-none tracking-wide select-none cursor-pointer"
    >
      <span
        className="vshell-chip-dot w-1.5 h-1.5 rounded-full shrink-0"
        data-live={hasPublic ? 'true' : 'false'}
      />
      {hasPublic ? <GlobeGlyph /> : <LanGlyph />}
      {!narrow && <span>{label}</span>}
      <span
        className="inline-flex items-center justify-center min-w-[15px] h-[15px] px-1 rounded-full text-[9px] tabular-nums"
        style={{ background: 'color-mix(in srgb, var(--chip-fg) 24%, transparent)' }}
      >
        {count}
      </span>
    </button>
  )
}

/**
 * apps/spaces/src/SpacesApp.jsx — Vulos OS spaces app wrapper
 *
 * Bridges the @vulos/office-client spaces library into the OS shell:
 *   - Reads the OS theme from ThemeProvider (useTheme hook)
 *   - Routes sign-out to the OS logout handler
 *   - Routes in-app notifications to the OS notification bus
 *
 * MEET-OS-01: also injects the LiveKit runtime flag (`window.__VULOS_MEET_LIVEKIT`)
 * based on the OS session's tier hint (Free → false, Pro+ → true). The office
 * Spaces room route reads this flag via `readLiveKitFlag()` and selects between
 * the LiveKit large-room surface (raise-hand, breakout, recording, speaker grid)
 * and the existing fabric.js mesh path. The mesh fallback is preserved for
 * ≤5 participants / non-Pro / when the flag is false.
 *
 * Tier hint sources, in order:
 *   1. Pre-set `window.__VULOS_TIER` (OS bootstrap / Setup state).
 *   2. GET /api/auth/status response (`tier` field, when the cloud has wired it).
 *   3. Default: 'free' (mesh path, no large-room controls).
 *
 * The hint resolution is async; while it resolves the flag defaults to `false`
 * (mesh path), so the worst-case is that the first paint of a Pro user briefly
 * sees the mesh fallback before the flag flips. The Room route re-reads the
 * flag on each render so the flip takes effect on next navigation.
 */

import { useCallback, useEffect, useState } from 'react'
import { SpacesLib } from '@vulos/office-client/spaces'
import { useTheme } from '../../../src/core/ThemeProvider.jsx'

function osNotifier(title, body, priority = 'normal') {
  window.dispatchEvent(new CustomEvent('vulos:notification', {
    detail: {
      source: 'spaces',
      title,
      body: body || '',
      priority,
      level: priority === 'high' ? 'urgent' : 'info',
    },
  }))
}

function osLogout() {
  window.location.href = '/auth/logout'
}

// Tiers that unlock the LiveKit large-room path. Free stays on mesh.
const PRO_TIERS = new Set(['pro', 'team', 'business', 'enterprise'])

/**
 * Resolve the current OS tier hint.
 *  - Returns 'pro' / 'free' (lowercase string).
 *  - Synchronously reads `window.__VULOS_TIER` first (OS bootstrap / Setup state).
 *  - Falls back to GET /api/auth/status when the synchronous hint is missing.
 *  - Never throws; on any error returns 'free' (mesh path, safe default).
 */
export async function resolveTier({ fetchImpl = fetch } = {}) {
  if (typeof window !== 'undefined' && typeof window.__VULOS_TIER === 'string') {
    return window.__VULOS_TIER.toLowerCase()
  }
  try {
    const res = await fetchImpl('/api/auth/status', { credentials: 'same-origin' })
    if (!res || !res.ok) return 'free'
    const body = await res.json()
    if (body && typeof body.tier === 'string') return body.tier.toLowerCase()
    return 'free'
  } catch {
    return 'free'
  }
}

/**
 * applyLiveKitFlag: write `window.__VULOS_MEET_LIVEKIT` from a tier string.
 * Exported for tests; the SpacesApp calls it on mount + when the resolved tier
 * changes. The office-client's readLiveKitFlag() consults this exact window
 * property at room-route selection time.
 */
export function applyLiveKitFlag(tier) {
  if (typeof window === 'undefined') return false
  const enabled = PRO_TIERS.has((tier || '').toLowerCase())
  window.__VULOS_MEET_LIVEKIT = enabled
  return enabled
}

export default function SpacesApp() {
  const { resolved: theme } = useTheme()
  const [tier, setTier] = useState(() => {
    if (typeof window !== 'undefined' && typeof window.__VULOS_TIER === 'string') {
      return window.__VULOS_TIER.toLowerCase()
    }
    return 'free'
  })

  // Set the LiveKit flag synchronously from whatever tier we have right now so
  // the office Spaces room route picks the correct surface on first render.
  applyLiveKitFlag(tier)

  useEffect(() => {
    let cancelled = false
    resolveTier().then((t) => {
      if (cancelled) return
      setTier(t)
      applyLiveKitFlag(t)
    })
    return () => { cancelled = true }
  }, [])

  const handleNotification = useCallback(osNotifier, [])
  const handleSignOut = useCallback(osLogout, [])

  return (
    <SpacesLib
      apiBase="/api"
      theme={theme}
      onSignOut={handleSignOut}
      onNotification={handleNotification}
    />
  )
}

// DEVPROF-06: Driving mode hook.
//
// When the active user profile has layout="car" or layout="driving",
// this hook:
//   1. Returns isDriving=true so the shell can apply .driving-mode CSS
//   2. Automatically POSTs /api/notifications/dnd {mode:"total"} to silence
//      all notifications while driving
//   3. Clears DND when driving mode is exited
//
// The hook is self-contained — it reads the profile from AuthContext and
// manages its own DND call lifecycle. No changes to notify.go or AuthProvider.
import { useEffect, useRef, useState } from 'react'
import { useAuth, type AuthProfile } from '../auth/AuthProvider'
import { getPrefs, setMuted } from './notificationStore'

const DRIVING_LAYOUTS = new Set(['car', 'driving'])

// Marks that DRIVING MODE — not the user — set the local mute, so leaving
// driving mode restores the user's own setting rather than un-muting someone
// who had muted themselves before they started driving.
const MUTED_BY_DRIVING_KEY = 'vulos.driving.muted'

function readMutedByDriving(): boolean {
  try { return localStorage.getItem(MUTED_BY_DRIVING_KEY) === '1' } catch { return false }
}

function writeMutedByDriving(on: boolean): void {
  try {
    if (on) localStorage.setItem(MUTED_BY_DRIVING_KEY, '1')
    else localStorage.removeItem(MUTED_BY_DRIVING_KEY)
  } catch { /* no localStorage — the mute still applies for this session */ }
}

/**
 * useDrivingMode — returns { isDriving, toggle }
 *
 * isDriving: true when the user's profile layout is car/driving OR when
 *            manually toggled on.
 * toggle():  lets the user manually override the mode (e.g. from a shell
 *            quick-action button) without changing their profile.
 */
function profileLayout(profile: AuthProfile | null): string | undefined {
  const layout = profile?.layout
  return typeof layout === 'string' ? layout : undefined
}

export function useDrivingMode(): { isDriving: boolean; toggle: () => void } {
  const { profile } = useAuth()
  const [manualOverride, setManualOverride] = useState<boolean | null>(null)

  // Resolve effective state: manual override > profile layout
  const layout = profileLayout(profile)
  const isDriving =
    manualOverride !== null
      ? manualOverride
      : DRIVING_LAYOUTS.has(layout ?? '')

  const prevDriving = useRef<boolean | null>(null)

  useEffect(() => {
    if (prevDriving.current === isDriving) return
    prevDriving.current = isDriving

    // Silence THIS browser first, unconditionally.
    //
    // The box-wide call below is admin-only (DND-SCOPE-01 in
    // backend/cmd/server/routes_notify.go: POST /api/notifications/dnd returns
    // 403 for any non-admin profile, because one file backs DND for the whole
    // box and per-user DND is not implemented). This hook used to fire only
    // that call, as `fetch(...).catch(() => {})` — and fetch does NOT reject on
    // a 403, it resolves, so the catch never even ran. A non-admin driver got
    // the driving-mode styling with every notification still arriving, and
    // nothing anywhere said so. Driving is the worst place to ship a control
    // that reports success while doing nothing.
    //
    // The local mute is real for every profile, needs no permission, and is
    // what the driver actually asked for. The box-wide call is now an
    // ADDITIONAL best-effort escalation for admins, not the only mechanism.
    // "Did WE mute this?" is PERSISTED, not held in a ref. The mute itself
    // lives in localStorage, so a ref would be lost on reload while the mute
    // survived — and driving mode is exactly the case where the app gets
    // reloaded between engaging and leaving. The driver would then be silenced
    // permanently, with no indication and nothing to point at. Persisting the
    // marker alongside the mute keeps the two facts together.
    if (isDriving) {
      if (!getPrefs().muted) {
        setMuted(true)
        writeMutedByDriving(true)
      }
    } else if (readMutedByDriving()) {
      writeMutedByDriving(false)
      setMuted(false)
    }

    // Box-wide DND: silences delivery for the whole box, including Web Push to
    // the driver's phone, which the local mute cannot do. Admin-only, so a
    // non-admin simply does not get this part — by design, and no longer
    // silently mistaken for success.
    fetch('/api/notifications/dnd', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: isDriving ? 'total' : 'off' }),
    }).catch(() => {})
  }, [isDriving])

  function toggle(): void {
    setManualOverride((prev) => {
      // If no manual override, base it on the profile state (invert it)
      if (prev === null) return !DRIVING_LAYOUTS.has(profileLayout(profile) ?? '')
      return !prev
    })
  }

  return { isDriving, toggle }
}

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
import { useAuth } from '../auth/AuthProvider'

const DRIVING_LAYOUTS = new Set(['car', 'driving'])

/**
 * useDrivingMode — returns { isDriving, toggle }
 *
 * isDriving: true when the user's profile layout is car/driving OR when
 *            manually toggled on.
 * toggle():  lets the user manually override the mode (e.g. from a shell
 *            quick-action button) without changing their profile.
 */
export function useDrivingMode() {
  const { profile } = useAuth()
  const [manualOverride, setManualOverride] = useState(null) // true|false|null

  // Resolve effective state: manual override > profile layout
  const isDriving =
    manualOverride !== null
      ? manualOverride
      : DRIVING_LAYOUTS.has(profile?.layout)

  const prevDriving = useRef(null)

  useEffect(() => {
    if (prevDriving.current === isDriving) return
    prevDriving.current = isDriving

    if (isDriving) {
      // Engage total DND while driving
      fetch('/api/notifications/dnd', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'total' }),
      }).catch(() => {})
    } else {
      // Restore DND to off when leaving driving mode
      fetch('/api/notifications/dnd', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode: 'off' }),
      }).catch(() => {})
    }
  }, [isDriving])

  function toggle() {
    setManualOverride((prev) => {
      // If no manual override, base it on the profile state (invert it)
      if (prev === null) return !DRIVING_LAYOUTS.has(profile?.layout)
      return !prev
    })
  }

  return { isDriving, toggle }
}

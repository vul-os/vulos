/**
 * MOBILE-06 — useDeviceProfile
 *
 * Derives a device profile from viewport width and an optional stored override.
 * Breakpoints:
 *   < 768  → 'mobile'
 *   768–1023 → 'tablet'
 *   >= 1024  → 'desktop'
 *
 * The override (written by setOverride or by DEVPROF-03 callers) is persisted to
 * localStorage under the key 'vulos.deviceProfile'. When an override is present it
 * always wins over the auto-detected value.
 *
 * The hook debounces resize events via matchMedia so there is no polling timer.
 */

import { useState, useEffect, useCallback } from 'react'

// ─── breakpoints ────────────────────────────────────────────────────────────
const MOBILE6_BP_MOBILE  = 768   // < 768  → mobile
const MOBILE6_BP_TABLET  = 1024  // 768–1023 → tablet; >= 1024 → desktop

// ─── localStorage key (shared with DEVPROF-03 if it writes the same key) ────
const MOBILE6_STORAGE_KEY = 'vulos.deviceProfile'

// ─── valid profile values ────────────────────────────────────────────────────
const MOBILE6_PROFILES = ['mobile', 'tablet', 'desktop']

// ─── notification-behavior policy map ───────────────────────────────────────
/**
 * notificationBehavior(profile) → policy object
 *
 * Returns per-profile notification presentation policy.
 * Callers (e.g. Toasts) may use this to decide placement / style.
 */
export function notificationBehavior(profile) {
  const policies = {
    mobile: {
      placement: 'bottom-sheet',    // sheet that slides up from bottom
      position:  'bottom-center',
      maxVisible: 2,
      autoDismissMs: 4000,
    },
    tablet: {
      placement: 'bottom-right',    // compact corner toast
      position:  'bottom-right',
      maxVisible: 3,
      autoDismissMs: 5000,
    },
    desktop: {
      placement: 'top-right',       // classic macOS / Windows corner
      position:  'top-right',
      maxVisible: 5,
      autoDismissMs: 6000,
    },
  }
  return policies[profile] ?? policies.desktop
}

// ─── internal: derive profile from window width ───────────────────────────
function MOBILE6_deriveProfile(width) {
  if (width < MOBILE6_BP_MOBILE) return 'mobile'
  if (width < MOBILE6_BP_TABLET) return 'tablet'
  return 'desktop'
}

// ─── internal: read stored override (returns null if absent / invalid) ────
function MOBILE6_readStoredOverride() {
  try {
    const raw = localStorage.getItem(MOBILE6_STORAGE_KEY)
    if (raw && MOBILE6_PROFILES.includes(raw)) return raw
  } catch {}
  return null
}

// ─── internal: write stored override ─────────────────────────────────────
function MOBILE6_writeStoredOverride(value) {
  try {
    if (value === null) {
      localStorage.removeItem(MOBILE6_STORAGE_KEY)
    } else {
      localStorage.setItem(MOBILE6_STORAGE_KEY, value)
    }
  } catch {}
}

// ─── hook ─────────────────────────────────────────────────────────────────
/**
 * useDeviceProfile() → { profile, autoProfile, override, setOverride }
 *
 * profile      — effective profile ('mobile' | 'tablet' | 'desktop')
 * autoProfile  — viewport-derived profile (ignoring override)
 * override     — current stored override (null = none)
 * setOverride  — persist an override or clear it (pass null to clear)
 */
export function useDeviceProfile() {
  const [autoProfile, setAutoProfile] = useState(
    () => MOBILE6_deriveProfile(window.innerWidth)
  )
  const [override, setOverrideState] = useState(
    () => MOBILE6_readStoredOverride()
  )

  // Track auto profile via matchMedia — no polling, no debounce needed
  useEffect(() => {
    const mqMobile  = window.matchMedia(`(max-width: ${MOBILE6_BP_MOBILE - 1}px)`)
    const mqTablet  = window.matchMedia(
      `(min-width: ${MOBILE6_BP_MOBILE}px) and (max-width: ${MOBILE6_BP_TABLET - 1}px)`
    )

    function handleChange() {
      setAutoProfile(MOBILE6_deriveProfile(window.innerWidth))
    }

    mqMobile.addEventListener('change', handleChange)
    mqTablet.addEventListener('change', handleChange)
    return () => {
      mqMobile.removeEventListener('change', handleChange)
      mqTablet.removeEventListener('change', handleChange)
    }
  }, [])

  // Listen for cross-tab/DEVPROF-03 changes to the storage key
  useEffect(() => {
    function handleStorage(e) {
      if (e.key !== MOBILE6_STORAGE_KEY) return
      const next = e.newValue && MOBILE6_PROFILES.includes(e.newValue)
        ? e.newValue
        : null
      setOverrideState(next)
    }
    window.addEventListener('storage', handleStorage)
    return () => window.removeEventListener('storage', handleStorage)
  }, [])

  const setOverride = useCallback((value) => {
    const validated = (value === null || MOBILE6_PROFILES.includes(value)) ? value : null
    MOBILE6_writeStoredOverride(validated)
    setOverrideState(validated)
  }, [])

  const profile = override ?? autoProfile

  return { profile, autoProfile, override, setOverride }
}

/**
 * prefGroups.ts — every preference that follows the user, in one list.
 *
 * The one place that knows both the engine (core/syncedPrefs.ts, which imports
 * nothing from the app) and the modules that own state. Importing this file
 * registers the groups; nothing else has to remember to.
 *
 * The list is the readable answer to "what actually syncs", and
 * `prefGroups.test.ts` asserts it against roadmap/USER-STATE-INVENTORY.md's
 * SYNC table so a future feature cannot add a `localStorage` key and leave the
 * document describing an OS that no longer exists.
 *
 * # What is NOT here, and why
 *
 * Absence from this list is an EXCEPTION and each one is argued in
 * roadmap/USER-STATE-INVENTORY.md §3 — window geometry (a statement about a
 * particular screen), biometric enrolment (a property of this device's sensor),
 * location consent (granted to a device, not to a person), the endpoint cache
 * (this client's network position), the notification log (a log, whose home is
 * the box's own store). Nothing is absent because nobody got to it.
 */

import { registerPrefGroup, prefRead, prefWriteLocal, type PrefGroup } from './syncedPrefs'
import {
  AI_FIRSTRUN_LS_KEY, AI_FIRSTRUN_PREF_KEY,
  DENSITY_LS_KEY, DENSITY_PREF_KEY,
  DESKTOP_PREF_KEYS,
  DOCK_PINS_LS_KEY, DOCK_PINS_PREF_KEY,
  NOTIFY_PREF_KEY,
  PREF_GROUP_AI, PREF_GROUP_DENSITY, PREF_GROUP_DESKTOP, PREF_GROUP_DOCK,
  PREF_GROUP_NOTIFICATIONS, PREF_GROUP_THEME, PREF_GROUP_WALLPAPER, PREF_GROUP_WIDGETS,
  THEME_LS_KEYS, THEME_PREF_KEYS,
  WALLPAPER_LS_KEY, WALLPAPER_PREF_KEY,
  widgetsOwnPrefKey,
} from './prefKeys'
import { exportLayoutFields, importLayoutFields } from '../desktop/store'
import { exportRailFields, importRailFields } from '../widgets/layout'
import { exportNotificationPrefs, importNotificationPrefs } from './notificationStore'

/* ── localStorage-key ↔ bag-key groups ────────────────────────────────────── */

/**
 * The straightforward case: a bag key that IS a localStorage key.
 *
 * `onWrite` exists for the two entries with a DOM consequence the cache alone
 * does not carry — density is stamped on <html> before React mounts, so
 * applying a value from the box has to stamp it too or the arriving preference
 * only takes effect on the reload after next.
 */
function lsGroup(
  name: string,
  map: Readonly<Record<string, string>>,
  onWrite?: (values: Record<string, string>) => void,
): PrefGroup {
  const keys = Object.keys(map)
  return {
    name,
    owns: (k) => Object.prototype.hasOwnProperty.call(map, k),
    read: () => {
      const out: Record<string, string> = {}
      for (const bag of keys) {
        const v = prefRead(map[bag])
        if (v) out[bag] = v
      }
      return out
    },
    write: (values) => {
      for (const bag of keys) prefWriteLocal(map[bag], values[bag] ?? '')
      onWrite?.(values)
    },
  }
}

/* ── the list ─────────────────────────────────────────────────────────────── */

let registered = false

/**
 * Register every group. Idempotent — safe under StrictMode's double-invoke and
 * under Vite HMR, both of which re-run module bodies.
 */
export function registerAllPrefGroups(): void {
  if (registered) return
  registered = true

  registerPrefGroup(lsGroup(PREF_GROUP_THEME, {
    [THEME_PREF_KEYS.theme]: THEME_LS_KEYS.theme,
    [THEME_PREF_KEYS.accent]: THEME_LS_KEYS.accent,
    [THEME_PREF_KEYS.nightShift]: THEME_LS_KEYS.nightShift,
    [THEME_PREF_KEYS.nightShiftFrom]: THEME_LS_KEYS.nightShiftFrom,
    [THEME_PREF_KEYS.nightShiftTo]: THEME_LS_KEYS.nightShiftTo,
    [THEME_PREF_KEYS.nightShiftWarmth]: THEME_LS_KEYS.nightShiftWarmth,
    [THEME_PREF_KEYS.scheduleDark]: THEME_LS_KEYS.scheduleDark,
    [THEME_PREF_KEYS.scheduleLight]: THEME_LS_KEYS.scheduleLight,
  }))

  registerPrefGroup(lsGroup(PREF_GROUP_WALLPAPER, { [WALLPAPER_PREF_KEY]: WALLPAPER_LS_KEY }))

  registerPrefGroup(lsGroup(PREF_GROUP_DOCK, { [DOCK_PINS_PREF_KEY]: DOCK_PINS_LS_KEY }))

  registerPrefGroup(lsGroup(PREF_GROUP_DENSITY, { [DENSITY_PREF_KEY]: DENSITY_LS_KEY }, (values) => {
    if (typeof document === 'undefined') return
    document.documentElement.dataset.density = values[DENSITY_PREF_KEY] || 'comfortable'
  }))

  registerPrefGroup(lsGroup(PREF_GROUP_AI, { [AI_FIRSTRUN_PREF_KEY]: AI_FIRSTRUN_LS_KEY }))

  registerPrefGroup({
    name: PREF_GROUP_DESKTOP,
    owns: (k) => DESKTOP_PREF_KEYS.includes(k),
    read: exportLayoutFields,
    write: importLayoutFields,
  })

  registerPrefGroup({
    name: PREF_GROUP_WIDGETS,
    owns: widgetsOwnPrefKey,
    read: exportRailFields,
    write: importRailFields,
  })

  registerPrefGroup({
    name: PREF_GROUP_NOTIFICATIONS,
    owns: (k) => k === NOTIFY_PREF_KEY,
    read: exportNotificationPrefs,
    write: importNotificationPrefs,
  })
}

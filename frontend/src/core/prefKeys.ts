/**
 * prefKeys.ts — the names, and nothing else.
 *
 * Every synced preference has two names: the key in the profile's settings bag
 * (what the box stores) and the `localStorage` key (the pre-paint cache). Both
 * live here, in a module with NO imports.
 *
 * That is the whole point of the file. The owners of this state —
 * ThemeProvider, useWallpaper, Dock, Settings, desktop/store, widgets/layout,
 * notificationStore — all need the names, and core/prefGroups.ts needs to
 * import all of those owners to register them. If the names lived in
 * prefGroups.ts, every owner would import it and it would import them back: a
 * cycle whose symptom is an `undefined` constant at module-init time, in a
 * boot path, on some machines only.
 *
 * The localStorage names are FROZEN. They are the values on every box shipping
 * today, and hydration's one-time adoption step reads them to migrate a user's
 * existing wallpaper, theme, pins and layout onto their profile. Renaming one
 * does not lose a preference loudly — it loses it silently, by adopting nothing
 * and looking like a fresh box.
 */

/** A replicated COLUMN (`profiles.theme`), not a settings entry. See syncedPrefs.ts. */
export const THEME_BAG_KEY = 'profile.theme'

export const THEME_PREF_KEYS = {
  theme: THEME_BAG_KEY,
  accent: 'shell.accent',
  nightShift: 'shell.nightshift',
  nightShiftFrom: 'shell.nightshift.from',
  nightShiftTo: 'shell.nightshift.to',
  nightShiftWarmth: 'shell.nightshift.warmth',
  scheduleDark: 'shell.schedule.dark',
  scheduleLight: 'shell.schedule.light',
} as const

export const THEME_LS_KEYS = {
  theme: 'vulos-theme',
  accent: 'vulos-accent',
  nightShift: 'vulos-nightshift',
  nightShiftFrom: 'vulos-nightshift-from',
  nightShiftTo: 'vulos-nightshift-to',
  nightShiftWarmth: 'vulos-nightshift-warmth',
  scheduleDark: 'vulos-schedule-dark',
  scheduleLight: 'vulos-schedule-light',
} as const

export const WALLPAPER_PREF_KEY = 'shell.wallpaper'
export const WALLPAPER_LS_KEY = 'vulos-wallpaper'

export const DOCK_PINS_PREF_KEY = 'shell.dock.pins'
export const DOCK_PINS_LS_KEY = 'vulos-dock-pins'

export const DENSITY_PREF_KEY = 'shell.density'
export const DENSITY_LS_KEY = 'vulos.density'

export const AI_FIRSTRUN_PREF_KEY = 'shell.ai.firstrun'
export const AI_FIRSTRUN_LS_KEY = 'vulos-ai-firstrun-done'

export const NOTIFY_PREF_KEY = 'shell.notifications.prefs'
export const NOTIFY_LS_KEY = 'vulos.notifications.prefs.v1'

/**
 * The two composite stores keep their own persistence, but their cache keys are
 * named HERE with everything else.
 *
 * Not tidiness. These are the keys hydration's one-time adoption reads to
 * migrate a user's existing layout and rail onto their profile, and the guard
 * in backend/internal/sqlcrdt/osstate_test.go checks that every synced shell
 * state has both a cache key and a bag key by looking in this one file. A cache
 * key hidden in its owning module is one that guard cannot see.
 */
export const DESKTOP_LAYOUT_LS_KEY = 'vulos.desktop.layout'
export const DESKTOP_PACKS_LS_KEY = 'vulos.desktop.packs'
export const WIDGETS_LAYOUT_LS_KEY = 'vulos.widgets.layout.v1'

export const DESKTOP_PREF_KEY_PRESET = 'shell.desktop.preset'
export const DESKTOP_PREF_KEY_CONTROLS = 'shell.desktop.controls'
export const DESKTOP_PREF_KEY_DOCK_DESKTOP = 'shell.desktop.dock.desktop'
export const DESKTOP_PREF_KEY_DOCK_MOBILE = 'shell.desktop.dock.mobile'
export const DESKTOP_PREF_KEY_TOKENS = 'shell.desktop.tokens'

export const DESKTOP_PREF_KEYS: readonly string[] = [
  DESKTOP_PREF_KEY_PRESET,
  DESKTOP_PREF_KEY_CONTROLS,
  DESKTOP_PREF_KEY_DOCK_DESKTOP,
  DESKTOP_PREF_KEY_DOCK_MOBILE,
  DESKTOP_PREF_KEY_TOKENS,
]

export const WIDGETS_PREF_KEY_COUNT = 'shell.widgets.count'
export const WIDGETS_PREF_KEY_PREFIX = 'shell.widgets.'

/** Whether `bagKey` belongs to the widget rail (`count`, or an ordinal slot). */
export function widgetsOwnPrefKey(bagKey: string): boolean {
  return bagKey === WIDGETS_PREF_KEY_COUNT || /^shell\.widgets\.\d+$/.test(bagKey)
}

/** Group names, so a `pushPrefGroup` caller cannot typo one into a silent no-op. */
export const PREF_GROUP_THEME = 'theme'
export const PREF_GROUP_WALLPAPER = 'wallpaper'
export const PREF_GROUP_DOCK = 'dock'
export const PREF_GROUP_DENSITY = 'density'
export const PREF_GROUP_AI = 'ai'
export const PREF_GROUP_DESKTOP = 'desktop'
export const PREF_GROUP_WIDGETS = 'widgets'
export const PREF_GROUP_NOTIFICATIONS = 'notifications'

import {
  createContext, useContext, useState, useEffect, useCallback, useMemo, useSyncExternalStore,
  type ReactNode,
} from 'react'
import {
  accentText, accentSolid, worstSurfaceFor, compositeOver,
  ACCENT_TINT_RANGE, AA_TARGET,
} from './accentContrast'
import { getLayout, subscribeLayout } from '../desktop'
import { prefRead, setPref, subscribePrefs } from './syncedPrefs'
import { THEME_PREF_KEYS } from './prefKeys'

type ResolvedTheme = 'light' | 'dark'

interface ThemeContextValue {
  theme: string
  resolved: ResolvedTheme
  isDark: boolean
  setTheme: (t: string) => void
  toggle: () => void
  cycleTheme: () => void
  systemTheme: ResolvedTheme
  scheduleDark: string
  scheduleLight: string
  setScheduleDark: (t: string) => void
  setScheduleLight: (t: string) => void
  nightShiftMode: string
  nightShiftActive: boolean
  nightShiftWarmth: number
  nightShiftFrom: string
  nightShiftTo: string
  setNightShiftMode: (m: string) => void
  setNightShiftFrom: (t: string) => void
  setNightShiftTo: (t: string) => void
  setNightShiftWarmth: (v: number) => void
  accent: string
  setAccent: (c: string) => void
}

const ThemeContext = createContext<ThemeContextValue | null>(null)

const STORAGE_KEYS = {
  theme: 'vulos-theme',
  scheduleDark: 'vulos-schedule-dark',   // HH:MM
  scheduleLight: 'vulos-schedule-light', // HH:MM
  nightShift: 'vulos-nightshift',        // 'off' | 'auto' | 'custom'
  nightShiftFrom: 'vulos-nightshift-from',
  nightShiftTo: 'vulos-nightshift-to',
  nightShiftWarmth: 'vulos-nightshift-warmth', // 0-100
  accent: 'vulos-accent', // hex colour string
}

export const DEFAULT_ACCENT = '#3b82f6' // blue-500

/**
 * # There were two themes, and the one that synced was not the one you saw
 *
 * `profiles.Theme` is a real column on the replicated profile row, documented
 * `"dark" | "light" | "auto"`, decoded by handleUpdateProfile — and nothing in
 * frontend/src ever read or wrote it. What a user actually saw came from
 * `vulos-theme` in localStorage, written here. So the row that replicated was
 * not the setting that governed, and the setting that governed was per-BROWSER:
 * set your theme on one box and the other opened in the old one, and so did the
 * same box in a different browser.
 *
 * Resolved in favour of the COLUMN (roadmap/USER-STATE-INVENTORY.md §9). It
 * already exists, already replicates, already has a wire field.
 *
 * `vulos-theme` is not deleted — it is DEMOTED to the pre-paint cache. main.tsx
 * stamps `data-theme` onto <html> before React mounts, long before a profile
 * can be fetched, and removing the local copy would reintroduce the
 * flash-of-wrong-theme that key was added to prevent. Its two readers are this
 * file and main.tsx's getInitialResolvedTheme(); both keep working, neither is
 * authoritative any more.
 *
 * One widening, stated rather than left to be discovered: this shell has a
 * FOURTH theme value, 'schedule', that the column's comment does not list. The
 * column is a free string with no server-side validation, so it carries it; the
 * comment on Profile.Theme now says so.
 *
 * Accent, night shift and the schedule times have no column and ride the
 * preference bag.
 */

/**
 * Read a theme preference out of the local cache.
 *
 * Still localStorage, but no longer the source of truth — see
 * core/syncedPrefs.ts. `''` and "unset" are the same thing here, so an empty
 * stored value falls back exactly as a missing one does.
 */
function ls(key: string, fallback: string): string {
  return prefRead(key) || fallback
}

/**
 * The stores a theme value can change underneath React: a user's own write
 * (prefs), and the desktop layout, whose preset can carry an accent.
 */
function subscribeThemeSources(fn: () => void): () => void {
  const offPrefs = subscribePrefs(fn)
  const offLayout = subscribeLayout(fn)
  return () => { offPrefs(); offLayout() }
}

/**
 * The accent a layout preset or an installed pack asks for, if any.
 *
 * `--vd-accent` is one of the four allowlisted pack tokens. It deliberately does
 * NOT go straight to the DOM as a colour: it comes through here so it lands on
 * the same legibility derivation every user-picked accent gets (--accent-text /
 * --accent-solid below). A pack can choose a hue; it cannot choose to be
 * unreadable, because it never touches the value that is drawn as text.
 *
 * An accent the USER has explicitly chosen always wins — a layout preset is a
 * starting point, not an override of a decision already made.
 */
function layoutAccent(): string | null {
  try { return getLayout().tokens['--vd-accent'] ?? null } catch { return null }
}

function hasExplicitAccent(): boolean {
  return !!prefRead(STORAGE_KEYS.accent)
}

function initialAccent(): string {
  if (hasExplicitAccent()) return ls(STORAGE_KEYS.accent, DEFAULT_ACCENT)
  return layoutAccent() ?? DEFAULT_ACCENT
}

function getSystemTheme(): ResolvedTheme {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

interface ClockTime {
  h: number
  m: number
}

// Approximate sunrise/sunset from timezone offset (good enough default)
function getSunTimes(): { sunrise: ClockTime; sunset: ClockTime } {
  const now = new Date()
  const month = now.getMonth() // 0-11
  // Rough approximation: sunrise 5:30-7:00, sunset 17:30-20:00 depending on season
  const summerBias = Math.sin(((month - 2) / 12) * 2 * Math.PI)
  const sunrise = { h: 6, m: Math.round(30 - summerBias * 30) } // ~6:00-7:00
  const sunset = { h: 18, m: Math.round(30 + summerBias * 60) }  // ~17:30-19:30
  return { sunrise, sunset }
}

function parseTime(str: string): ClockTime {
  const [h, m] = (str || '').split(':').map(Number)
  return { h: h || 0, m: m || 0 }
}

function currentMinutes(): number {
  const now = new Date()
  return now.getHours() * 60 + now.getMinutes()
}

function timeToMinutes({ h, m }: ClockTime): number {
  return h * 60 + m
}

function isInTimeRange(fromMin: number, toMin: number): boolean {
  const now = currentMinutes()
  if (fromMin <= toMin) {
    return now >= fromMin && now < toMin
  }
  // Wraps midnight
  return now >= fromMin || now < toMin
}

function resolveTheme(theme: string, scheduleDark: string, scheduleLight: string, systemTheme: ResolvedTheme): ResolvedTheme {
  if (theme === 'auto') return systemTheme || getSystemTheme()
  if (theme === 'schedule') {
    const darkStart = parseTime(scheduleDark)
    const lightStart = parseTime(scheduleLight)
    return isInTimeRange(timeToMinutes(darkStart), timeToMinutes(lightStart)) ? 'dark' : 'light'
  }
  if (theme === 'light' || theme === 'dark') return theme
  // Unknown / legacy value → treat as System so we never render an invalid attr.
  return systemTheme || getSystemTheme()
}

// Resolve the effective theme from persisted settings alone — used by main.jsx
// to stamp data-theme on <html> BEFORE first paint, so a Light/System-light user
// never sees a flash of the dark default on reload.
// eslint-disable-next-line react-refresh/only-export-components
export function getInitialResolvedTheme(): ResolvedTheme {
  const theme = ls(STORAGE_KEYS.theme, 'auto')
  const scheduleDark = ls(STORAGE_KEYS.scheduleDark, '20:00')
  const scheduleLight = ls(STORAGE_KEYS.scheduleLight, '07:00')
  return resolveTheme(theme, scheduleDark, scheduleLight, getSystemTheme())
}

function resolveNightShift(mode: string, customFrom: string, customTo: string): boolean {
  if (mode === 'off') return false
  if (mode === 'auto') {
    const { sunrise, sunset } = getSunTimes()
    return isInTimeRange(timeToMinutes(sunset), timeToMinutes(sunrise))
  }
  if (mode === 'custom') {
    const from = parseTime(customFrom)
    const to = parseTime(customTo)
    return isInTimeRange(timeToMinutes(from), timeToMinutes(to))
  }
  return false
}

interface ThemeProviderProps {
  children?: ReactNode
}

/**
 * Read one theme preference, re-reading whenever the cache changes underneath.
 *
 * These used to be `useState` seeded once from localStorage, which was correct
 * while localStorage was the only writer. It no longer is: a value arriving
 * from the box lands in the cache from outside React, and a component holding
 * a copy it seeded at mount would keep rendering the old theme until the next
 * reload — the setting would sync and the screen would not.
 */
function useThemePref(key: string, fallback: string): string {
  return useSyncExternalStore(
    subscribeThemeSources,
    () => ls(key, fallback),
    () => fallback, // server render: no storage, no matchMedia
  )
}

export function ThemeProvider({ children }: ThemeProviderProps) {
  // Default mode is System (follow the OS/browser prefers-color-scheme).
  const theme = useThemePref(STORAGE_KEYS.theme, 'auto')
  // Live mirror of the OS/browser colour-scheme. Kept in React state (not just a
  // one-off read) so that when the system flips while in System/Auto mode the
  // whole context recomputes and every consumer re-renders — not merely the
  // <html data-theme> attribute.
  const [systemTheme, setSystemTheme] = useState<ResolvedTheme>(getSystemTheme)
  const scheduleDark = useThemePref(STORAGE_KEYS.scheduleDark, '20:00')
  const scheduleLight = useThemePref(STORAGE_KEYS.scheduleLight, '07:00')
  const nightShiftMode = useThemePref(STORAGE_KEYS.nightShift, 'off')
  const nightShiftFrom = useThemePref(STORAGE_KEYS.nightShiftFrom, '20:00')
  const nightShiftTo = useThemePref(STORAGE_KEYS.nightShiftTo, '07:00')
  const nightShiftWarmth = parseInt(useThemePref(STORAGE_KEYS.nightShiftWarmth, '40'), 10)
  // The accent is the one value with a second source: a layout preset or an
  // installed pack can ask for one, and it applies only while the user has not
  // chosen their own. Both sources are in subscribeThemeSources, so a preset
  // change and a synced accent both retint live.
  const accent = useSyncExternalStore(subscribeThemeSources, initialAccent, () => DEFAULT_ACCENT)

  // Re-evaluate time-based modes every minute
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (theme !== 'schedule' && nightShiftMode === 'off') return
    const id = setInterval(() => setTick(t => t + 1), 60_000)
    return () => clearInterval(id)
  }, [theme, nightShiftMode])

  // Track the OS/browser colour-scheme live, in every mode. In System/Auto mode
  // this drives the effective theme; in explicit Light/Dark it's simply ignored.
  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-color-scheme: light)')
    if (!mq) return
    const handler = () => setSystemTheme(getSystemTheme())
    if (mq.addEventListener) mq.addEventListener('change', handler)
    else if (mq.addListener) mq.addListener(handler) // Safari < 14
    return () => {
      if (mq.removeEventListener) mq.removeEventListener('change', handler)
      else if (mq.removeListener) mq.removeListener(handler)
    }
  }, [])

  // `tick` is in both lists on purpose, and the rule calling it "unnecessary"
  // is reading the argument lists rather than the functions.
  //
  // Both resolvers read the WALL CLOCK, which is not a value React can see.
  // resolveTheme('schedule', …) ends in isInTimeRange(), which compares against
  // currentMinutes(); resolveNightShift does the same, and in 'auto' mode also
  // calls getSunTimes(). Their answers therefore change with no change to any
  // argument — that is the entire point of a schedule.
  //
  // `tick` is the clock's stand-in: the interval above increments it once a
  // minute, but only while a time-based mode is actually selected. Remove it
  // and these memos recompute only when a SETTING changes, so a box left alone
  // in Schedule mode would sit in light theme straight through 20:00 and flip
  // hours later, whenever the user happened to touch an unrelated preference.
  // That is the bug the minute interval exists to prevent.
  const resolved = useMemo(
    () => resolveTheme(theme, scheduleDark, scheduleLight, systemTheme),
    [theme, scheduleDark, scheduleLight, systemTheme, tick]
  )

  const nightShiftActive = useMemo(
    () => resolveNightShift(nightShiftMode, nightShiftFrom, nightShiftTo),
    [nightShiftMode, nightShiftFrom, nightShiftTo, tick]
  )

  // Apply data-theme to root
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', resolved)
  }, [resolved])

  // Apply Night Shift filter
  useEffect(() => {
    const el = document.documentElement
    if (nightShiftActive) {
      el.setAttribute('data-nightshift', '')
      el.style.setProperty('--nightshift-warmth', String(nightShiftWarmth))
    } else {
      el.removeAttribute('data-nightshift')
      el.style.removeProperty('--nightshift-warmth')
    }
  }, [nightShiftActive, nightShiftWarmth])

  // Apply the accent, plus the two legibility-corrected variants derived from
  // it.
  //
  // The accent is the USER's choice, so no stylesheet value can promise that
  // accent-coloured TEXT is readable. Deriving here is the only place that can:
  // it runs whenever the accent or the resolved theme changes, and it reads the
  // surface actually in effect rather than assuming one.
  //
  //   --accent        exactly what they picked. Fills, borders, dots, focus.
  //   --accent-text   moved until it clears 4.5:1 on this theme's base surface.
  //   --accent-solid  moved until WHITE clears 4.5:1 on top of it.
  //
  // The last two move in OPPOSITE directions on a dark theme, which is why they
  // cannot be one token: accent text has to lighten to be read, and an accent
  // button has to darken for white to be read on it.
  useEffect(() => {
    const el = document.documentElement
    el.style.setProperty('--accent', accent)
    // Read the surface AFTER --accent is set, so the computed value reflects the
    // theme that is actually applied rather than the one being replaced.
    // Derive against the WORST of the surfaces accent text actually lands on,
    // not just the root canvas. Targeting --bg-base alone under-corrected:
    // panels and popovers are lighter than the desktop on the light theme, so a
    // value that measured 4.5 on the canvas measured 4.18 on a panel.
    const cs = getComputedStyle(el)
    // --accent-soft is included because accent text is often drawn ON a tint of
    // its own hue — a chip, a selected row, a section heading in a highlighted
    // card. That is the hardest surface it meets, and leaving it out left
    // "AI Assistant" at 4.26:1 on light and 4.40:1 on dark after everything else
    // had been corrected. It is a color-mix against transparent, so it is
    // resolved and composited over each opaque surface rather than used raw.
    const opaque = ['--bg-base', '--bg-surface', '--bg-elevated']
      .map((v) => cs.getPropertyValue(v).trim())
      .filter(Boolean)
    // Sample the whole RANGE of accent tints, not just --accent-soft.
    //
    // The previous version composited one value (--accent-soft, 15%) and
    // derived to exactly 4.5. That is correct for every surface it enumerated
    // and silently wrong for every surface it did not: App Hub's install chip
    // tints its own background at 13% and measured 4.21:1 dark / 4.28:1 light
    // while --accent-text said it was compliant. The failure is not the value,
    // it is that a control which paints its own accent-tinted background is a
    // DIFFERENT pair from the one that was measured — and there are several of
    // them across the OS.
    //
    // Contrast against a tint of the text's own hue degrades monotonically as
    // the tint deepens, so sampling the deepest tint the OS uses (22%) bounds
    // every lighter one, and AA_TARGET's headroom covers the gaps between the
    // sampled points. Built from a literal color-mix() rather than the token so
    // this does not depend on --accent-soft happening to be the deepest tint.
    const tinted = ACCENT_TINT_RANGE.flatMap((pct) =>
      opaque.map((b) => compositeOver(`color-mix(in srgb, ${accent} ${pct}%, transparent)`, b) ?? b))
    const surfaces = [...opaque, ...tinted]
    const worst = worstSurfaceFor(accent, surfaces.length ? surfaces : ['#08090c'])
    el.style.setProperty('--accent-text', accentText(accent, worst, AA_TARGET) ?? accent)
    el.style.setProperty('--accent-solid', accentSolid(accent, '#ffffff', AA_TARGET) ?? accent)
  }, [accent, resolved])

  // Persisted setters. setPref writes the local cache (so the UI is correct
  // immediately, and correct again before first paint on the next reload) and
  // queues the patch for the box. Neither half waits on the other.
  const setTheme = useCallback((t: string) => { setPref(THEME_PREF_KEYS.theme, STORAGE_KEYS.theme, t) }, [])
  const setScheduleDark = useCallback((t: string) => { setPref(THEME_PREF_KEYS.scheduleDark, STORAGE_KEYS.scheduleDark, t) }, [])
  const setScheduleLight = useCallback((t: string) => { setPref(THEME_PREF_KEYS.scheduleLight, STORAGE_KEYS.scheduleLight, t) }, [])
  const setNightShiftMode = useCallback((m: string) => { setPref(THEME_PREF_KEYS.nightShift, STORAGE_KEYS.nightShift, m) }, [])
  const setNightShiftFrom = useCallback((t: string) => { setPref(THEME_PREF_KEYS.nightShiftFrom, STORAGE_KEYS.nightShiftFrom, t) }, [])
  const setNightShiftTo = useCallback((t: string) => { setPref(THEME_PREF_KEYS.nightShiftTo, STORAGE_KEYS.nightShiftTo, t) }, [])
  const setNightShiftWarmth = useCallback((v: number) => { setPref(THEME_PREF_KEYS.nightShiftWarmth, STORAGE_KEYS.nightShiftWarmth, String(v)) }, [])
  const setAccent = useCallback((c: string) => { setPref(THEME_PREF_KEYS.accent, STORAGE_KEYS.accent, c) }, [])

  const toggle = useCallback(() => {
    setTheme(resolved === 'dark' ? 'light' : 'dark')
  }, [resolved, setTheme])

  // Three-way quick cycle for the top-bar toggle: System → Light → Dark → System.
  // Any non-primary mode (e.g. schedule) folds back into the cycle at System.
  const cycleTheme = useCallback(() => {
    setTheme(theme === 'auto' ? 'light' : theme === 'light' ? 'dark' : 'auto')
  }, [theme, setTheme])

  const value = useMemo<ThemeContextValue>(() => ({
    theme, resolved, isDark: resolved === 'dark', setTheme, toggle, cycleTheme, systemTheme,
    scheduleDark, scheduleLight, setScheduleDark, setScheduleLight,
    nightShiftMode, nightShiftActive, nightShiftWarmth,
    nightShiftFrom, nightShiftTo,
    setNightShiftMode, setNightShiftFrom, setNightShiftTo, setNightShiftWarmth,
    accent, setAccent,
  }), [
    theme, resolved, setTheme, toggle, cycleTheme, systemTheme,
    scheduleDark, scheduleLight, setScheduleDark, setScheduleLight,
    nightShiftMode, nightShiftActive, nightShiftWarmth,
    nightShiftFrom, nightShiftTo,
    setNightShiftMode, setNightShiftFrom, setNightShiftTo, setNightShiftWarmth,
    accent, setAccent,
  ])

  return (
    <ThemeContext.Provider value={value}>
      {children}
    </ThemeContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext)
  if (!ctx) return {
    theme: 'auto', resolved: getSystemTheme(), isDark: getSystemTheme() === 'dark',
    systemTheme: getSystemTheme(), setTheme: () => {}, toggle: () => {}, cycleTheme: () => {},
    scheduleDark: '20:00', scheduleLight: '07:00', setScheduleDark: () => {}, setScheduleLight: () => {},
    nightShiftMode: 'off', nightShiftActive: false, nightShiftWarmth: 40,
    nightShiftFrom: '20:00', nightShiftTo: '07:00',
    setNightShiftMode: () => {}, setNightShiftFrom: () => {}, setNightShiftTo: () => {}, setNightShiftWarmth: () => {},
    accent: DEFAULT_ACCENT, setAccent: () => {},
  }
  return ctx
}

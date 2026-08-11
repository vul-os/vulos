import { createContext, useContext, useState, useEffect, useCallback, useMemo, type ReactNode } from 'react'
import { accentText, accentSolid, worstSurfaceFor } from './accentContrast'

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

function ls(key: string, fallback: string): string {
  try { return localStorage.getItem(key) ?? fallback } catch { return fallback }
}

function lsSet(key: string, val: string): void {
  try { localStorage.setItem(key, val) } catch { /* noop */ }
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

export function ThemeProvider({ children }: ThemeProviderProps) {
  // Default mode is System (follow the OS/browser prefers-color-scheme).
  const [theme, setThemeState] = useState(() => ls(STORAGE_KEYS.theme, 'auto'))
  // Live mirror of the OS/browser colour-scheme. Kept in React state (not just a
  // one-off read) so that when the system flips while in System/Auto mode the
  // whole context recomputes and every consumer re-renders — not merely the
  // <html data-theme> attribute.
  const [systemTheme, setSystemTheme] = useState<ResolvedTheme>(getSystemTheme)
  const [scheduleDark, setScheduleDarkState] = useState(() => ls(STORAGE_KEYS.scheduleDark, '20:00'))
  const [scheduleLight, setScheduleLightState] = useState(() => ls(STORAGE_KEYS.scheduleLight, '07:00'))
  const [nightShiftMode, setNightShiftModeState] = useState(() => ls(STORAGE_KEYS.nightShift, 'off'))
  const [nightShiftFrom, setNightShiftFromState] = useState(() => ls(STORAGE_KEYS.nightShiftFrom, '20:00'))
  const [nightShiftTo, setNightShiftToState] = useState(() => ls(STORAGE_KEYS.nightShiftTo, '07:00'))
  const [nightShiftWarmth, setNightShiftWarmthState] = useState(() => parseInt(ls(STORAGE_KEYS.nightShiftWarmth, '40'), 10))
  const [accent, setAccentState] = useState(() => ls(STORAGE_KEYS.accent, DEFAULT_ACCENT))

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
    const surfaces = ['--bg-base', '--bg-surface', '--bg-elevated']
      .map((v) => cs.getPropertyValue(v).trim())
      .filter(Boolean)
    const worst = worstSurfaceFor(accent, surfaces.length ? surfaces : ['#08090c'])
    el.style.setProperty('--accent-text', accentText(accent, worst) ?? accent)
    el.style.setProperty('--accent-solid', accentSolid(accent) ?? accent)
  }, [accent, resolved])

  // Persisted setters
  const setTheme = useCallback((t: string) => { setThemeState(t); lsSet(STORAGE_KEYS.theme, t) }, [])
  const setScheduleDark = useCallback((t: string) => { setScheduleDarkState(t); lsSet(STORAGE_KEYS.scheduleDark, t) }, [])
  const setScheduleLight = useCallback((t: string) => { setScheduleLightState(t); lsSet(STORAGE_KEYS.scheduleLight, t) }, [])
  const setNightShiftMode = useCallback((m: string) => { setNightShiftModeState(m); lsSet(STORAGE_KEYS.nightShift, m) }, [])
  const setNightShiftFrom = useCallback((t: string) => { setNightShiftFromState(t); lsSet(STORAGE_KEYS.nightShiftFrom, t) }, [])
  const setNightShiftTo = useCallback((t: string) => { setNightShiftToState(t); lsSet(STORAGE_KEYS.nightShiftTo, t) }, [])
  const setNightShiftWarmth = useCallback((v: number) => { setNightShiftWarmthState(v); lsSet(STORAGE_KEYS.nightShiftWarmth, String(v)) }, [])
  const setAccent = useCallback((c: string) => { setAccentState(c); lsSet(STORAGE_KEYS.accent, c) }, [])

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

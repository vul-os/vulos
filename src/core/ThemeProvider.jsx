import { createContext, useContext, useState, useEffect, useCallback, useMemo } from 'react'

const ThemeContext = createContext(null)

const STORAGE_KEYS = {
  theme: 'vulos-theme',
  scheduleDark: 'vulos-schedule-dark',   // HH:MM
  scheduleLight: 'vulos-schedule-light', // HH:MM
  nightShift: 'vulos-nightshift',        // 'off' | 'auto' | 'custom'
  nightShiftFrom: 'vulos-nightshift-from',
  nightShiftTo: 'vulos-nightshift-to',
  nightShiftWarmth: 'vulos-nightshift-warmth', // 0-100
}

function ls(key, fallback) {
  try { return localStorage.getItem(key) ?? fallback } catch { return fallback }
}

function lsSet(key, val) {
  try { localStorage.setItem(key, val) } catch {}
}

function getSystemTheme() {
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

// Approximate sunrise/sunset from timezone offset (good enough default)
function getSunTimes() {
  const now = new Date()
  const month = now.getMonth() // 0-11
  // Rough approximation: sunrise 5:30-7:00, sunset 17:30-20:00 depending on season
  const summerBias = Math.sin(((month - 2) / 12) * 2 * Math.PI)
  const sunrise = { h: 6, m: Math.round(30 - summerBias * 30) } // ~6:00-7:00
  const sunset = { h: 18, m: Math.round(30 + summerBias * 60) }  // ~17:30-19:30
  return { sunrise, sunset }
}

function parseTime(str) {
  const [h, m] = (str || '').split(':').map(Number)
  return { h: h || 0, m: m || 0 }
}

function currentMinutes() {
  const now = new Date()
  return now.getHours() * 60 + now.getMinutes()
}

function timeToMinutes({ h, m }) {
  return h * 60 + m
}

function isInTimeRange(fromMin, toMin) {
  const now = currentMinutes()
  if (fromMin <= toMin) {
    return now >= fromMin && now < toMin
  }
  // Wraps midnight
  return now >= fromMin || now < toMin
}

function resolveTheme(theme, scheduleDark, scheduleLight) {
  if (theme === 'auto') return getSystemTheme()
  if (theme === 'schedule') {
    const darkStart = parseTime(scheduleDark)
    const lightStart = parseTime(scheduleLight)
    return isInTimeRange(timeToMinutes(darkStart), timeToMinutes(lightStart)) ? 'dark' : 'light'
  }
  return theme
}

function resolveNightShift(mode, customFrom, customTo) {
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

export function ThemeProvider({ children }) {
  const [theme, setThemeState] = useState(() => ls(STORAGE_KEYS.theme, 'dark'))
  const [scheduleDark, setScheduleDarkState] = useState(() => ls(STORAGE_KEYS.scheduleDark, '20:00'))
  const [scheduleLight, setScheduleLightState] = useState(() => ls(STORAGE_KEYS.scheduleLight, '07:00'))
  const [nightShiftMode, setNightShiftModeState] = useState(() => ls(STORAGE_KEYS.nightShift, 'off'))
  const [nightShiftFrom, setNightShiftFromState] = useState(() => ls(STORAGE_KEYS.nightShiftFrom, '20:00'))
  const [nightShiftTo, setNightShiftToState] = useState(() => ls(STORAGE_KEYS.nightShiftTo, '07:00'))
  const [nightShiftWarmth, setNightShiftWarmthState] = useState(() => parseInt(ls(STORAGE_KEYS.nightShiftWarmth, '40'), 10))

  // Re-evaluate time-based modes every minute
  const [tick, setTick] = useState(0)
  useEffect(() => {
    if (theme !== 'schedule' && nightShiftMode === 'off') return
    const id = setInterval(() => setTick(t => t + 1), 60_000)
    return () => clearInterval(id)
  }, [theme, nightShiftMode])

  const resolved = useMemo(
    () => resolveTheme(theme, scheduleDark, scheduleLight),
    [theme, scheduleDark, scheduleLight, tick]
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
      el.style.setProperty('--nightshift-warmth', nightShiftWarmth)
    } else {
      el.removeAttribute('data-nightshift')
      el.style.removeProperty('--nightshift-warmth')
    }
  }, [nightShiftActive, nightShiftWarmth])

  // Listen for system theme changes when in auto mode
  useEffect(() => {
    if (theme !== 'auto') return
    const mq = window.matchMedia('(prefers-color-scheme: light)')
    const handler = () => document.documentElement.setAttribute('data-theme', getSystemTheme())
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [theme])

  // Persisted setters
  const setTheme = useCallback((t) => { setThemeState(t); lsSet(STORAGE_KEYS.theme, t) }, [])
  const setScheduleDark = useCallback((t) => { setScheduleDarkState(t); lsSet(STORAGE_KEYS.scheduleDark, t) }, [])
  const setScheduleLight = useCallback((t) => { setScheduleLightState(t); lsSet(STORAGE_KEYS.scheduleLight, t) }, [])
  const setNightShiftMode = useCallback((m) => { setNightShiftModeState(m); lsSet(STORAGE_KEYS.nightShift, m) }, [])
  const setNightShiftFrom = useCallback((t) => { setNightShiftFromState(t); lsSet(STORAGE_KEYS.nightShiftFrom, t) }, [])
  const setNightShiftTo = useCallback((t) => { setNightShiftToState(t); lsSet(STORAGE_KEYS.nightShiftTo, t) }, [])
  const setNightShiftWarmth = useCallback((v) => { setNightShiftWarmthState(v); lsSet(STORAGE_KEYS.nightShiftWarmth, String(v)) }, [])

  const toggle = useCallback(() => {
    setTheme(resolved === 'dark' ? 'light' : 'dark')
  }, [resolved, setTheme])

  const value = useMemo(() => ({
    theme, resolved, isDark: resolved === 'dark', setTheme, toggle,
    scheduleDark, scheduleLight, setScheduleDark, setScheduleLight,
    nightShiftMode, nightShiftActive, nightShiftWarmth,
    nightShiftFrom, nightShiftTo,
    setNightShiftMode, setNightShiftFrom, setNightShiftTo, setNightShiftWarmth,
  }), [
    theme, resolved, setTheme, toggle,
    scheduleDark, scheduleLight, setScheduleDark, setScheduleLight,
    nightShiftMode, nightShiftActive, nightShiftWarmth,
    nightShiftFrom, nightShiftTo,
    setNightShiftMode, setNightShiftFrom, setNightShiftTo, setNightShiftWarmth,
  ])

  return (
    <ThemeContext.Provider value={value}>
      {children}
    </ThemeContext.Provider>
  )
}

export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) return {
    theme: 'dark', resolved: 'dark', isDark: true, setTheme: () => {}, toggle: () => {},
    scheduleDark: '20:00', scheduleLight: '07:00', setScheduleDark: () => {}, setScheduleLight: () => {},
    nightShiftMode: 'off', nightShiftActive: false, nightShiftWarmth: 40,
    nightShiftFrom: '20:00', nightShiftTo: '07:00',
    setNightShiftMode: () => {}, setNightShiftFrom: () => {}, setNightShiftTo: () => {}, setNightShiftWarmth: () => {},
  }
  return ctx
}

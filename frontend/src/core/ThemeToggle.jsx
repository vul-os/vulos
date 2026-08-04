import { useTheme } from './ThemeProvider'

// Three-way quick theme control. One click cycles System → Light → Dark → System.
// The glyph reflects the chosen MODE (not just the resolved colour), so System
// reads distinctly from an explicit Light/Dark choice.
//
// variant:
//   'pill' (default) — amber pill, used on the login / setup screens.
//   'bar'            — neutral icon button that matches the desktop menu-bar tray.
export default function ThemeToggle({ size = 'sm', variant = 'pill' }) {
  const { theme, systemTheme, cycleTheme } = useTheme()

  // Mode metadata: label + which glyph to show.
  const mode =
    theme === 'light' ? 'light' :
    theme === 'dark' ? 'dark' :
    theme === 'schedule' ? 'schedule' :
    'system'

  const label =
    mode === 'light' ? 'Light' :
    mode === 'dark' ? 'Dark' :
    mode === 'schedule' ? 'Scheduled' :
    `System (${systemTheme === 'dark' ? 'dark' : 'light'})`

  const nextLabel =
    mode === 'system' ? 'Light' :
    mode === 'light' ? 'Dark' :
    'System'

  const Icon = mode === 'light' ? SunIcon
    : mode === 'dark' ? MoonIcon
    : mode === 'schedule' ? ClockIcon
    : SystemIcon

  const box = size === 'sm' ? 'w-7 h-7' : 'w-9 h-9'
  const glyph = size === 'sm' ? 'w-3.5 h-3.5' : 'w-4 h-4'

  const skin = variant === 'bar'
    ? 'focus-primary text-neutral-400 hover:text-neutral-200 hover:bg-neutral-700/50 rounded'
    : `${box} rounded-full bg-amber-500/10 text-amber-500/80 hover:text-amber-400 hover:bg-amber-500/20 transition-colors`

  const dims = variant === 'bar' ? 'w-6 h-6' : box

  return (
    <button
      onClick={cycleTheme}
      title={`Theme: ${label} — click for ${nextLabel}`}
      aria-label={`Theme mode: ${label}. Activate to switch to ${nextLabel}.`}
      className={`${dims} flex items-center justify-center transition-colors ${skin}`}
    >
      <Icon className={glyph} />
    </button>
  )
}

function SunIcon({ className }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="currentColor" aria-hidden="true">
      <path d="M8 1a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 1zm0 11a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 12zm7-4a.5.5 0 010 1h-1a.5.5 0 010-1h1zM3 8a.5.5 0 010 1H2a.5.5 0 010-1h1zm9.354-3.646a.5.5 0 010 .708l-.708.707a.5.5 0 11-.707-.708l.707-.707a.5.5 0 01.708 0zM5.06 10.232a.5.5 0 010 .707l-.707.708a.5.5 0 11-.708-.708l.708-.707a.5.5 0 01.707 0zm7.678.708a.5.5 0 01-.708 0l-.707-.708a.5.5 0 01.707-.707l.708.707a.5.5 0 010 .708zM5.06 5.768a.5.5 0 01-.707 0l-.708-.707a.5.5 0 11.708-.708l.707.708a.5.5 0 010 .707zM8 4.5a3.5 3.5 0 100 7 3.5 3.5 0 000-7z" />
    </svg>
  )
}

function MoonIcon({ className }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="currentColor" aria-hidden="true">
      <path d="M6 .278a.768.768 0 01.08.858 7.2 7.2 0 00-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.787.787 0 01.81.316.733.733 0 01-.031.893A8.349 8.349 0 018.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.752.752 0 016 .278z" />
    </svg>
  )
}

// System = a small display/monitor, signalling "follow the device".
function SystemIcon({ className }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true">
      <rect x="1.5" y="2.5" width="13" height="9" rx="1.2" />
      <path d="M5.5 14h5M8 11.5V14" strokeLinecap="round" />
    </svg>
  )
}

function ClockIcon({ className }) {
  return (
    <svg viewBox="0 0 16 16" className={className} fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true">
      <circle cx="8" cy="8" r="6" />
      <path d="M8 5v3l2 1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

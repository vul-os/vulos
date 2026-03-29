import { useTheme } from './ThemeProvider'

export default function ThemeToggle({ size = 'sm' }) {
  const { isDark, toggle } = useTheme()

  const sizeClass = size === 'sm'
    ? 'w-7 h-7 text-[11px]'
    : 'w-9 h-9 text-sm'

  return (
    <button
      onClick={toggle}
      title={isDark ? 'Switch to light mode' : 'Switch to dark mode'}
      className={`${sizeClass} flex items-center justify-center rounded-full
        bg-amber-500/10 text-amber-500/80 hover:text-amber-400 hover:bg-amber-500/20
        transition-colors`}
    >
      {isDark ? (
        <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
          <path d="M8 1a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 1zm0 11a.5.5 0 01.5.5v1a.5.5 0 01-1 0v-1A.5.5 0 018 12zm7-4a.5.5 0 010 1h-1a.5.5 0 010-1h1zM3 8a.5.5 0 010 1H2a.5.5 0 010-1h1zm9.354-3.646a.5.5 0 010 .708l-.708.707a.5.5 0 11-.707-.708l.707-.707a.5.5 0 01.708 0zM5.06 10.232a.5.5 0 010 .707l-.707.708a.5.5 0 11-.708-.708l.708-.707a.5.5 0 01.707 0zm7.678.708a.5.5 0 01-.708 0l-.707-.708a.5.5 0 01.707-.707l.708.707a.5.5 0 010 .708zM5.06 5.768a.5.5 0 01-.707 0l-.708-.707a.5.5 0 11.708-.708l.707.708a.5.5 0 010 .707zM8 4.5a3.5 3.5 0 100 7 3.5 3.5 0 000-7z" />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
          <path d="M6 .278a.768.768 0 01.08.858 7.2 7.2 0 00-.878 3.46c0 4.021 3.278 7.277 7.318 7.277.527 0 1.04-.055 1.533-.16a.787.787 0 01.81.316.733.733 0 01-.031.893A8.349 8.349 0 018.344 16C3.734 16 0 12.286 0 7.71 0 4.266 2.114 1.312 5.124.06A.752.752 0 016 .278z" />
        </svg>
      )}
    </button>
  )
}

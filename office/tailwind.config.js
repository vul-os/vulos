/** @type {import('tailwindcss').Config} */
// -----------------------------------------------------------------------------
// Tailwind reads from src/design/tokens.css via CSS variables — the source of
// truth is the tokens file, not this config.  We expose token-backed utilities
// (`bg-paper`, `text-ink`, `border-line`, `bg-accent`, `font-serif`, …) and a
// few signal classes so app code never reaches for raw hex.
//
// `darkMode: ['class', '[data-theme="dark"]']` lets us flip dark on a parent
// element (e.g. the SignView's paper-look pages stay light even when the rest
// of the app is dark).
// -----------------------------------------------------------------------------
export default {
  darkMode: ['class', '[data-theme="dark"]'],
  content: [
    './index.html',
    './src/**/*.{js,ts,jsx,tsx}',
  ],
  theme: {
    extend: {
      colors: {
        // Brand kept as an alias for any legacy refs; new code uses `accent`.
        brand: {
          50:  'var(--teal-50)',
          100: 'var(--teal-100)',
          200: 'var(--teal-200)',
          300: 'var(--teal-300)',
          400: 'var(--teal-400)',
          500: 'var(--teal-500)',
          600: 'var(--teal-600)',
          700: 'var(--teal-700)',
          800: 'var(--teal-800)',
          900: 'var(--teal-900)',
        },
        oat: {
          50:  'var(--oat-50)',
          100: 'var(--oat-100)',
          200: 'var(--oat-200)',
          300: 'var(--oat-300)',
          400: 'var(--oat-400)',
          500: 'var(--oat-500)',
          600: 'var(--oat-600)',
          700: 'var(--oat-700)',
          800: 'var(--oat-800)',
          900: 'var(--oat-900)',
        },
        teal: {
          50:  'var(--teal-50)',
          100: 'var(--teal-100)',
          200: 'var(--teal-200)',
          300: 'var(--teal-300)',
          400: 'var(--teal-400)',
          500: 'var(--teal-500)',
          600: 'var(--teal-600)',
          700: 'var(--teal-700)',
          800: 'var(--teal-800)',
          900: 'var(--teal-900)',
        },
        // Semantic — these are what new code should use.
        bg:         'var(--bg)',
        'bg-elev':  'var(--bg-elev-1)',
        'bg-elev2': 'var(--bg-elev-2)',
        'bg-sunk':  'var(--bg-sunk)',
        paper:      'var(--bg-elev-1)',
        clay:       'var(--bg-elev-2)',
        ink:        'var(--ink)',
        'ink-muted':'var(--ink-muted)',
        'ink-faint':'var(--ink-faint)',
        line:       'var(--line)',
        'line-strong': 'var(--line-strong)',
        accent:        'var(--accent)',
        'accent-hover':'var(--accent-hover)',
        'accent-press':'var(--accent-press)',
        'accent-tint': 'var(--accent-tint)',
        'accent-tint-2':'var(--accent-tint-2)',
        warning: 'var(--signal-warning)',
        'warning-bg': 'var(--signal-warning-bg)',
        danger:  'var(--signal-error)',
        'danger-bg':  'var(--signal-error-bg)',
        success: 'var(--signal-success)',
        'success-bg': 'var(--signal-success-bg)',
        info:    'var(--signal-info)',
        'info-bg': 'var(--signal-info-bg)',
      },
      fontFamily: {
        sans:  ['var(--font-sans)'],
        serif: ['var(--font-serif)'],
        mono:  ['var(--font-mono)'],
      },
      fontSize: {
        '2xs': ['var(--text-2xs)',  { lineHeight: 'var(--leading-snug)' }],
        xs:    ['var(--text-xs)',   { lineHeight: 'var(--leading-snug)' }],
        sm:    ['var(--text-sm)',   { lineHeight: 'var(--leading-snug)' }],
        md:    ['var(--text-md)',   { lineHeight: 'var(--leading-body)' }],
        base:  ['var(--text-base)', { lineHeight: 'var(--leading-body)' }],
        lg:    ['var(--text-lg)',   { lineHeight: 'var(--leading-snug)' }],
        xl:    ['var(--text-xl)',   { lineHeight: 'var(--leading-snug)' }],
        '2xl': ['var(--text-2xl)',  { lineHeight: 'var(--leading-tight)' }],
        '3xl': ['var(--text-3xl)',  { lineHeight: 'var(--leading-tight)' }],
      },
      letterSpacing: {
        tightish: 'var(--tracking-tight)',
        wideish:  'var(--tracking-wide)',
        eyebrow:  'var(--tracking-wider)',
      },
      lineHeight: {
        doc: 'var(--leading-doc)',
      },
      borderRadius: {
        xs:   'var(--radius-xs)',
        sm:   'var(--radius-sm)',
        md:   'var(--radius-md)',
        lg:   'var(--radius-lg)',
        xl:   'var(--radius-xl)',
        pill: 'var(--radius-pill)',
      },
      boxShadow: {
        e1: 'var(--elev-1)',
        e2: 'var(--elev-2)',
        e3: 'var(--elev-3)',
        focus: 'var(--elev-focus)',
      },
      transitionTimingFunction: {
        out:    'var(--ease-out)',
        spring: 'var(--ease-spring)',
        in:     'var(--ease-in)',
      },
      transitionDuration: {
        fast: 'var(--duration-fast)',
        base: 'var(--duration-base)',
        slow: 'var(--duration-slow)',
      },
      maxWidth: {
        measure: '68ch',   // doc body — line length for readability
        measure2:'80ch',   // wider doc
      },
      keyframes: {
        'fade-in':  { '0%': { opacity: 0 }, '100%': { opacity: 1 } },
        'rise-in':  {
          '0%':   { opacity: 0, transform: 'translateY(6px)' },
          '100%': { opacity: 1, transform: 'translateY(0)' },
        },
        'scale-in': {
          '0%':   { opacity: 0, transform: 'scale(0.97)' },
          '100%': { opacity: 1, transform: 'scale(1)' },
        },
        'slide-in-right': {
          '0%':   { opacity: 0, transform: 'translateX(8px)' },
          '100%': { opacity: 1, transform: 'translateX(0)' },
        },
      },
      animation: {
        'fade-in':        'fade-in var(--duration-base) var(--ease-out) both',
        'rise-in':        'rise-in var(--duration-slow) var(--ease-spring) both',
        'scale-in':       'scale-in var(--duration-base) var(--ease-spring) both',
        'slide-in-right': 'slide-in-right var(--duration-base) var(--ease-out) both',
      },
    },
  },
  plugins: [],
}

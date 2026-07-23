/**
 * Logo.jsx — the Vulos brand mark for the management console.
 *
 * The art is the CANONICAL OSS Vulos logo — the exact identity vulos.org and
 * the Vulos OS itself use (a stylised "V" with a teardrop counter). It is
 * vendored verbatim from the OSS brand kit at vulos-cloud/public/brand/vulos.png
 * into web/public/brand/vulos.png, so the console shows the REAL mark rather
 * than a hand-drawn approximation. ("Vulos" is isiZulu for "open".)
 *
 * The console is served under /console with a strict, self-scoped CSP
 * (img-src 'self' data:); the PNG ships as a same-origin asset at
 * /console/brand/vulos.png, so the <img> is CSP-clean. The src is resolved
 * through Vite's BASE_URL ('/console/') so it works no matter where the SPA is
 * mounted.
 *
 * Mirrors vulos-cloud/src/components/Logo.jsx (same tone treatment, same
 * eager/decoding, same wordmark voice) so the console reads as the same product
 * as the marketing site.
 *
 * FROZEN EXPORTS (kept stable so the ported auth pages import them unchanged):
 *   <LogoMark size? tone? />                       — just the glyph (real PNG)
 *   <Wordmark />                                   — the "vulos" wordmark text
 *   default <Logo size? showWordmark? tone? />     — glyph + wordmark lockup
 */

// Base-aware asset URL — Vite substitutes import.meta.env.BASE_URL ('/console/').
const MARK_SRC = `${import.meta.env.BASE_URL}brand/vulos.png`

export function LogoMark({
  size = 28,
  tone = 'slate',
  style,
  className,
  alt = 'Vulos',
  ...props
}) {
  // The canonical art is a single dark-slate glyph on transparent. It reads
  // beautifully on the light theme (dark ink on warm paper), but on the
  // near-black dark theme a dark glyph would vanish. So the TONE treatment is
  // theme-aware and lives in CSS (.vk-logo-mark rules in theme.css), keyed on
  // the data-theme stamped pre-paint by index.html — deterministic and
  // FOUC-free: dark surfaces render the SAME glyph tonally lifted to a soft
  // near-white (a light logo on a dark canvas, as brands do), light surfaces
  // keep the slate ink. The `tone` prop only strengthens the ambient glow for
  // very-dark placements. This keeps the exact vulos.org identity while
  // guaranteeing the mark actually SHOWS in both themes.
  const cls = `vk-logo-mark vk-logo-mark--${tone === 'on-dark' ? 'on-dark' : 'slate'}${className ? ' ' + className : ''}`

  return (
    <img
      src={MARK_SRC}
      // 2× on retina — the browser down-samples the single native PNG cleanly.
      srcSet={`${MARK_SRC} 2x`}
      width={size}
      height={size}
      alt={alt}
      role="img"
      aria-label={alt}
      loading="eager"
      decoding="async"
      draggable={false}
      style={{
        display: 'block',
        flexShrink: 0,
        width: size,
        height: size,
        objectFit: 'contain',
        ...style,
      }}
      className={cls}
      {...props}
    />
  )
}

export function Wordmark({ style, className, ...props }) {
  return (
    <span
      role="text"
      style={{
        fontFamily:
          'var(--font-mono, ui-monospace, "SF Mono", "Cascadia Code", "Fira Code", monospace)',
        fontWeight: 600,
        fontSize: '1rem',
        // Tighter tracking than the default — matches the precision of the OSS UI.
        letterSpacing: '-0.02em',
        lineHeight: 1,
        color: 'var(--text-primary, #e5e5e5)',
        userSelect: 'none',
        ...style,
      }}
      className={className}
      {...props}
    >
      vulos
      <span
        style={{
          color: 'var(--text-faint, #666)',
          fontWeight: 400,
          letterSpacing: '-0.01em',
        }}
      >
        .org
      </span>
    </span>
  )
}

export default function Logo({
  size = 28,
  showWordmark = true,
  tone = 'slate',
  style,
  className,
  ...props
}) {
  return (
    <span
      role="img"
      aria-label="vulos.org"
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: size > 40 ? 14 : 10,
        ...style,
      }}
      className={className}
      {...props}
    >
      <LogoMark size={size} tone={tone} />
      {showWordmark && <Wordmark />}
    </span>
  )
}

/**
 * Logo.jsx — the Vulos brand mark for the management console.
 *
 * Ported from vulos-cloud's Logo.jsx but rendered as a SELF-CONTAINED inline SVG
 * instead of an external PNG: the console is served under /console with a strict,
 * self-scoped CSP (img-src 'self' data:) and ships no /brand asset, so an inline
 * vector keeps the mark air-gapped, crisp at any size, and CSP-clean.
 *
 * FROZEN EXPORTS (kept stable so the ported auth pages import them unchanged):
 *   <LogoMark size? tone? />                       — just the glyph
 *   <Wordmark />                                   — the "vulos" wordmark text
 *   default <Logo size? showWordmark? tone? />     — glyph + wordmark lockup
 */

export function LogoMark({
  size = 28,
  tone = 'slate',
  style,
  className,
  alt = 'Vulos',
  ...props
}) {
  const glow =
    tone === 'on-dark'
      ? 'drop-shadow(0 0 4px rgba(255,255,255,0.12))'
      : 'drop-shadow(0 1px 0 rgba(255,255,255,0.04))'
  // Canonical Vulos glyph — the stylised "V" with the teardrop counter, the
  // exact path shipped as assets/vulos-logo-dark.svg, inlined here so the mark
  // stays CSP-clean (img-src 'self' data:) and crisp at any size. Filled with
  // the theme-aware brand teal (--good: #2dd4bf on dark, #0d9488 on light — the
  // same teal the shipped dark asset hardcodes) so it reads on either canvas.
  // The native 48×46 viewBox is preserved; height is derived so the glyph never
  // distorts.
  const h = Math.round((size * 46) / 48)
  return (
    <svg
      width={size}
      height={h}
      viewBox="0 0 48 46"
      role="img"
      aria-label={alt}
      className={className}
      style={{ display: 'block', flexShrink: 0, filter: glow, ...style }}
      {...props}
    >
      <path
        d="M25.946 44.938c-.664.845-2.021.375-2.021-.698V33.937a2.26 2.26 0 0 0-2.262-2.262H10.287c-.92 0-1.456-1.04-.92-1.788l7.48-10.471c1.07-1.497 0-3.578-1.842-3.578H1.237c-.92 0-1.456-1.04-.92-1.788L10.013.474c.214-.297.556-.474.92-.474h28.894c.92 0 1.456 1.04.92 1.788l-7.48 10.471c-1.07 1.498 0 3.579 1.842 3.579h11.377c.943 0 1.473 1.088.89 1.83L25.947 44.94z"
        fill="var(--good, #2dd4bf)"
      />
    </svg>
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
        .console
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
      aria-label="Vulos console"
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

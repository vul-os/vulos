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
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label={alt}
      className={className}
      style={{ display: 'block', flexShrink: 0, filter: glow, ...style }}
      {...props}
    >
      {/* Diamond token — mirrors the ◇ mark used in the console sidebar. */}
      <rect
        x="16"
        y="2.2"
        width="19.5"
        height="19.5"
        rx="4"
        transform="rotate(45 16 16)"
        fill="none"
        stroke="var(--accent, #6366f1)"
        strokeWidth="2.2"
      />
      {/* Inner "V" counter — the teardrop suggestion from the OSS mark. */}
      <path
        d="M11 12.5 L16 21 L21 12.5"
        fill="none"
        stroke="var(--accent, #6366f1)"
        strokeWidth="2.2"
        strokeLinecap="round"
        strokeLinejoin="round"
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

/**
 * ui/index.jsx — design-system shim for the ported auth + console pages.
 *
 * The vulos-cloud auth/console pages import primitives from the marketing site's
 * UI kit. The management console doesn't ship the full kit, so this shim provides
 * exactly the pieces the ported surfaces use — Section, Button, Card, Pill, Stat
 * (+ NAV_HEIGHT) — with the styles copied verbatim from vulos-cloud so the console
 * looks identical to vulos.org. (The console SHELL itself is styled by
 * console/console.css; these are the in-page primitives the pages compose with.)
 */

import { useState } from 'react'

export const NAV_HEIGHT = 56 // px — retained for call-site parity

export function Section({ children, as: Tag = 'section', slim = false, style, className, ...rest }) {
  return (
    <>
      <style>{`
        .vk-section-inner { max-width: var(--maxw, 1180px); margin: 0 auto; width: 100%; }
        .vk-section {
          width: 100%;
          padding: 96px var(--sp-4, 32px);
        }
        .vk-section.slim { padding: 64px var(--sp-4, 32px); }
        @media (max-width: 1024px) {
          .vk-section      { padding: 80px var(--sp-4, 32px); }
          .vk-section.slim { padding: 56px var(--sp-4, 32px); }
        }
        @media (max-width: 768px) {
          .vk-section      { padding: 64px var(--sp-3, 24px); }
          .vk-section.slim { padding: 48px var(--sp-3, 24px); }
        }
        @media (max-width: 480px) {
          .vk-section      { padding: 48px var(--sp-2, 16px); }
          .vk-section.slim { padding: 40px var(--sp-2, 16px); }
        }
        @media (max-width: 360px) {
          .vk-section      { padding: 40px 12px; }
          .vk-section.slim { padding: 32px 12px; }
        }
      `}</style>
      <Tag
        className={`vk-section${slim ? ' slim' : ''}${className ? ' ' + className : ''}`}
        style={style}
        {...rest}
      >
        <div className="vk-section-inner">{children}</div>
      </Tag>
    </>
  )
}

/* ════════════════════════════════════════════════════════════
   BUTTON — solid accent (primary) / outline (ghost). Copied from
   the marketing kit so console CTAs read identically to vulos.org.
════════════════════════════════════════════════════════════ */
export function Button({ children, variant = 'primary', size = 'md', href, style, className, ...rest }) {
  const cls = `vk-btn vk-btn--${variant} vk-btn--${size}${className ? ' ' + className : ''}`
  const content = (
    <style>{`
      .vk-btn {
        display: inline-flex; align-items: center; justify-content: center; gap: 8px;
        font-family: var(--mono, ui-monospace, 'SF Mono', monospace);
        font-size: 0.875rem; font-weight: 500; line-height: 1; cursor: pointer;
        border: 1px solid transparent; text-decoration: none; white-space: nowrap;
        position: relative; letter-spacing: 0.01em;
        transition: background 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          border-color 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          color 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          transform 160ms var(--ease, cubic-bezier(.22,1,.36,1)),
          box-shadow 160ms var(--ease, cubic-bezier(.22,1,.36,1));
      }
      .vk-btn:disabled { opacity: 0.5; cursor: default; }
      .vk-btn:focus-visible { outline: none; box-shadow: var(--focus-ring, 0 0 0 3px rgba(15,106,108,.5)); }
      .vk-btn--sm { padding: 8px 16px;  border-radius: var(--radius, 12px); min-height: 36px; }
      .vk-btn--md { padding: 11px 22px; border-radius: var(--radius, 12px); min-height: 44px; }
      .vk-btn--lg { padding: 13px 28px; border-radius: var(--radius-lg, 20px); min-height: 52px; }
      .vk-btn--primary { background: var(--accent, #0f6a6c); color: #fff; border-color: var(--accent, #0f6a6c); }
      .vk-btn--primary:hover:not(:disabled), .vk-btn--primary:focus-visible {
        background: var(--accent-hover, color-mix(in srgb, var(--accent, #0f6a6c) 80%, black));
        border-color: var(--accent-hover, color-mix(in srgb, var(--accent, #0f6a6c) 80%, black));
        transform: translateY(-1px); box-shadow: 0 4px 16px rgba(15,106,108,.28), 0 1px 4px rgba(0,0,0,.3);
      }
      .vk-btn--primary:active { transform: translateY(0); box-shadow: none; }
      .vk-btn--ghost { background: transparent; color: var(--text-dim, #8e95b0); border-color: var(--border-strong, #2e3348); }
      .vk-btn--ghost:hover:not(:disabled), .vk-btn--ghost:focus-visible {
        color: var(--text, #eceef5); border-color: var(--border-emphasis, #333);
        background: var(--hover-overlay); transform: translateY(-1px);
      }
      .vk-btn--ghost:active { transform: translateY(0); }
    `}</style>
  )
  if (href) {
    return (<>{content}<a href={href} className={cls} style={style} {...rest}>{children}</a></>)
  }
  return (<>{content}<button className={cls} style={style} {...rest}>{children}</button></>)
}

/* ════════════════════════════════════════════════════════════
   CARD — surface with hairline border; optional lift-on-hover.
════════════════════════════════════════════════════════════ */
export function Card({ children, elevated = false, hover = true, style, className, ...rest }) {
  const [hovered, setHovered] = useState(false)
  return (
    <>
      <style>{`
        .vk-card {
          background: var(--bg-elev, #0e1018);
          border: 1px solid var(--border-strong, #2e3348);
          border-radius: var(--radius-lg, 20px);
          padding: var(--sp-4, 32px);
          transition: border-color var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1)),
            transform var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1)),
            box-shadow var(--dur-fast, 120ms) var(--ease, cubic-bezier(.22,1,.36,1));
        }
        .vk-card.elevated { background: var(--surface, #151720); }
        .vk-card.lift-hover:hover { border-color: var(--border-emphasis, #333); transform: translateY(-1px); box-shadow: var(--shadow); }
      `}</style>
      <div
        className={`vk-card${elevated ? ' elevated' : ''}${hover ? ' lift-hover' : ''}${className ? ' ' + className : ''}`}
        style={style}
        onMouseEnter={hover ? () => setHovered(true) : undefined}
        onMouseLeave={hover ? () => setHovered(false) : undefined}
        data-hovered={hovered ? '' : undefined}
        {...rest}
      >
        {children}
      </div>
    </>
  )
}

/* ════════════════════════════════════════════════════════════
   PILL — subtle outline badge, optional leading dot.
   color: "accent" | "good" | "warn" | "danger" | "faint"
════════════════════════════════════════════════════════════ */
export function Pill({ children, color, dot = false, style, ...rest }) {
  const schemes = {
    accent: { bg: 'color-mix(in srgb, var(--accent) 10%, transparent)',  color: 'var(--accent)',  border: 'color-mix(in srgb, var(--accent) 24%, transparent)' },
    good:   { bg: 'color-mix(in srgb, var(--good) 9%, transparent)',     color: 'var(--good)',    border: 'color-mix(in srgb, var(--good) 24%, transparent)' },
    warn:   { bg: 'color-mix(in srgb, var(--warn) 9%, transparent)',     color: 'var(--warn)',    border: 'color-mix(in srgb, var(--warn) 26%, transparent)' },
    danger: { bg: 'color-mix(in srgb, var(--danger) 9%, transparent)',   color: 'var(--danger)',  border: 'color-mix(in srgb, var(--danger) 28%, transparent)' },
    faint:  { bg: 'transparent',                                         color: 'var(--text-faint)', border: 'var(--border-strong)' },
  }
  const scheme = schemes[color] ?? schemes.faint
  return (
    <span
      style={{
        display: 'inline-flex', alignItems: 'center', gap: '5px',
        padding: '4px 10px', borderRadius: '99px',
        fontSize: '0.75rem', fontFamily: 'var(--mono, ui-monospace, monospace)',
        fontWeight: 500, letterSpacing: '0.04em',
        background: scheme.bg, color: scheme.color, border: `1px solid ${scheme.border}`,
        lineHeight: 1.4, ...style,
      }}
      {...rest}
    >
      {dot && (
        <span aria-hidden="true" style={{ display: 'inline-block', width: '5px', height: '5px', borderRadius: '50%', background: 'currentColor', flexShrink: 0, opacity: 0.85 }} />
      )}
      {children}
    </span>
  )
}

/* ════════════════════════════════════════════════════════════
   STAT — large mono numeric with a label + optional sublabel.
════════════════════════════════════════════════════════════ */
export function Stat({ label, value, sublabel, style, ...rest }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '6px', ...style }} {...rest}>
      <div style={{ fontSize: 'clamp(1.5rem, 3vw, 2rem)', fontFamily: 'var(--mono, ui-monospace, monospace)', fontWeight: 700, letterSpacing: '-0.03em', lineHeight: 1, color: 'var(--text-primary)', fontVariantNumeric: 'tabular-nums' }}>
        {value}
      </div>
      <div style={{ fontSize: '0.8125rem', fontFamily: 'var(--mono, ui-monospace, monospace)', color: 'var(--text-dim)', fontWeight: 400, letterSpacing: '0.01em' }}>
        {label}
      </div>
      {sublabel && (
        <div style={{ fontSize: '0.75rem', color: 'var(--text-faint)', lineHeight: 1.5 }}>{sublabel}</div>
      )}
    </div>
  )
}

import type { CSSProperties, ReactNode } from 'react'

// ─────────────────────────────────────────────────────────────────────────
// VULOS APP ART — the colourful, animated icon set.
//
// The reference is public/icons/terminal.svg: a full-bleed rounded plate in a
// deep brand colour, ONE bold near-white glyph, and ONE small bright accent
// chip (terminal's green cursor block). Every icon here follows that grammar,
// so the launcher reads as one family instead of twelve unrelated drawings:
//
//   · plate   — a rounded square filled edge-to-edge with a two-stop gradient
//               (`a` → `b`) plus a shared top gloss. Radius, gloss and inner
//               ring are ONE rule in CSS, so every plate matches exactly.
//   · glyph   — drawn on a 48×48 grid, near-white (#FFF / #F8FAFC), major
//               strokes at 3.2u so the shape survives down to 26–32px. Never
//               more than ~3 legible masses; detail that turns to mush small
//               is a bug, not a flourish.
//   · accent  — exactly one bright chip per icon in a hue that contrasts the
//               plate (the "green cursor" rule). It is usually also the part
//               that animates, so the motion has a focal point.
//
// MOTION BUDGET. Animations are declared ONLY inside a `:hover` rule, so an
// idle desktop full of icons has literally no `animation` property in play and
// costs zero frames — not even a paused one. Three icons (assistant, relay,
// activity) additionally carry a slow idle beat, four animated elements total
// across the whole set. Everything is transform/opacity/stroke-dashoffset, and
// the entire stylesheet sits inside `prefers-reduced-motion: no-preference`.
//
// Colours are literals rather than tokens on purpose: an app icon is a brand
// mark and must look identical in light and dark, exactly like the Terminal
// tile does today. Only the plate's ring/shadow is theme-aware.
// ─────────────────────────────────────────────────────────────────────────

/** CSSProperties plus the custom properties this module sets inline. */
export type ArtStyle = CSSProperties & {
  '--art-a'?: string
  '--art-b'?: string
  /** animation-duration / delay overrides for a single animated part */
  '--vd'?: string
  '--vdl'?: string
  /** stroke-dashoffset a `draw` part starts from — its own path length */
  '--vlen'?: string
}

export interface ArtSpec {
  /** Plate gradient: top-left stop. */
  a: string
  /** Plate gradient: bottom-right stop. */
  b: string
  /** Glyph drawn on a 0 0 48 48 grid, on top of the plate. */
  glyph: ReactNode
  /** Light plates need a dark hairline ring instead of the white one. */
  light?: boolean
}

const WHITE = '#FFFFFF'

// ── The art ───────────────────────────────────────────────────────────────

const ART: Record<string, ArtSpec> = {
  // Activity Monitor — a live trace with a bright pulse at the leading edge.
  activity: {
    a: '#22D3EE', b: '#0E7490',
    glyph: (
      <>
        <path
          d="M8 27h5.5l4-9.5 5 16 4-11.5 3 5H40"
          fill="none" stroke={WHITE} strokeWidth="3.4" strokeLinecap="round" strokeLinejoin="round"
          strokeDasharray="66" data-a="draw"
        />
        <circle cx="38" cy="27" r="4.4" fill="#A3E635" data-idle="beat" />
      </>
    ),
  },

  // Files — a folder whose front panel swings open on hover, revealing a doc.
  files: {
    a: '#FCD34D', b: '#D97706',
    glyph: (
      <>
        <path d="M7 14.5A3.5 3.5 0 0 1 10.5 11h8.1l3.6 4.4h15.3A3.5 3.5 0 0 1 41 18.9v15.6a3.5 3.5 0 0 1-3.5 3.5h-27A3.5 3.5 0 0 1 7 34.5z" fill="#FDE9C0" />
        <rect x="16.5" y="15.5" width="15" height="9" rx="2.4" fill="#38BDF8" />
        <path d="M7 22.5h34v12a3.5 3.5 0 0 1-3.5 3.5h-27A3.5 3.5 0 0 1 7 34.5z" fill={WHITE} className="vi-o-b" data-a="flap" />
      </>
    ),
  },

  // Drive — cloud storage; the upload arrow lifts away on hover.
  drive: {
    a: '#7DD3FC', b: '#0C4A6E',
    glyph: (
      <>
        <path d="M16.5 37a8 8 0 0 1-1.3-15.9A10.6 10.6 0 0 1 34.4 22.6 6.9 6.9 0 0 1 33.6 37z" fill={WHITE} />
        <path d="M24 32.5V21.5M19.4 26.1L24 21.5l4.6 4.6" fill="none" stroke="#16A34A" strokeWidth="3.4" strokeLinecap="round" strokeLinejoin="round" data-a="rise" />
      </>
    ),
  },

  // Assistant — a four-point spark with two satellites that twinkle at idle.
  assistant: {
    a: '#A78BFA', b: '#6D28D9',
    glyph: (
      <>
        <path d="M23 9.5c1.7 8.6 3.9 10.8 12.5 12.5C26.9 23.7 24.7 25.9 23 34.5c-1.7-8.6-3.9-10.8-12.5-12.5C19.1 20.3 21.3 18.1 23 9.5z" fill={WHITE} data-a="pulse" />
        <path d="M36.5 27c.7 3.5 1.6 4.4 5.1 5.1-3.5.7-4.4 1.6-5.1 5.1-.7-3.5-1.6-4.4-5.1-5.1 3.5-.7 4.4-1.6 5.1-5.1z" fill="#FDE047" data-idle="twinkle" />
        <path d="M36 6.5c.5 2.6 1.2 3.3 3.8 3.8-2.6.5-3.3 1.2-3.8 3.8-.5-2.6-1.2-3.3-3.8-3.8 2.6-.5 3.3-1.2 3.8-3.8z" fill="#67E8F9" data-idle="twinkle" style={{ '--vdl': '1.6s' } as ArtStyle} />
      </>
    ),
  },

  // Settings — three sliders with coloured knobs that slide on hover.
  persona: {
    a: '#818CF8', b: '#312E81',
    glyph: (
      <>
        <rect x="8" y="12.4" width="32" height="3.2" rx="1.6" fill={WHITE} opacity=".45" />
        <rect x="8" y="22.4" width="32" height="3.2" rx="1.6" fill={WHITE} opacity=".45" />
        <rect x="8" y="32.4" width="32" height="3.2" rx="1.6" fill={WHITE} opacity=".45" />
        <circle cx="30" cy="14" r="5" fill="#FBBF24" stroke={WHITE} strokeWidth="2.2" data-a="slidex" />
        <circle cx="17" cy="24" r="5" fill="#22D3EE" stroke={WHITE} strokeWidth="2.2" data-a="slidex" style={{ '--vdl': '.12s' } as ArtStyle} />
        <circle cx="26" cy="34" r="5" fill="#A3E635" stroke={WHITE} strokeWidth="2.2" data-a="slidex" style={{ '--vdl': '.24s' } as ArtStyle} />
      </>
    ),
  },

  // App Hub — a 2×2 grid; the fourth cell is the bright "add" chip.
  apphub: {
    a: '#F472B6', b: '#9D174D',
    glyph: (
      <>
        <rect x="9" y="9" width="13.5" height="13.5" rx="4.2" fill={WHITE} data-a="pop" />
        <rect x="25.5" y="9" width="13.5" height="13.5" rx="4.2" fill={WHITE} opacity=".72" data-a="pop" style={{ '--vdl': '.08s' } as ArtStyle} />
        <rect x="9" y="25.5" width="13.5" height="13.5" rx="4.2" fill={WHITE} opacity=".72" data-a="pop" style={{ '--vdl': '.16s' } as ArtStyle} />
        <rect x="25.5" y="25.5" width="13.5" height="13.5" rx="4.2" fill="#FDE047" data-a="pop" style={{ '--vdl': '.24s' } as ArtStyle} />
        <path d="M32.25 28.6v7.3M28.6 32.25h7.3" stroke="#9D174D" strokeWidth="3" strokeLinecap="round" />
      </>
    ),
  },

  // Disk Usage — a capacity ring that fills on hover.
  disks: {
    a: '#FDA4AF', b: '#881337',
    glyph: (
      <>
        <circle cx="24" cy="24" r="11.5" fill="none" stroke="#FFF1F2" strokeWidth="7" opacity=".85" />
        {/* The rotation centre comes from vi-o-hub's transform-origin, NOT from
            `rotate(-90 24 24)`: a centre in the attribute is applied ON TOP of
            the CSS origin, which pivots the arc twice and throws it clean off
            the plate (it rendered at y121-167 of a 96px tile before this). */}
        <circle
          cx="24" cy="24" r="11.5" fill="none" stroke="#FBBF24" strokeWidth="7" strokeLinecap="round"
          strokeDasharray="72.3" strokeDashoffset="26" transform="rotate(-90)"
          className="vi-o-hub" data-a="meter"
        />
        <circle cx="24" cy="24" r="4.6" fill={WHITE} />
      </>
    ),
  },

  // Packages — an isometric crate with a bright tape band; bobs on hover.
  packages: {
    a: '#34D399', b: '#065F46',
    glyph: (
      <g data-a="bob">
        <path d="M24 8l14.5 7.3L24 22.6 9.5 15.3z" fill={WHITE} />
        <path d="M9.5 15.3L24 22.6V40L9.5 32.7z" fill={WHITE} opacity=".7" />
        <path d="M38.5 15.3L24 22.6V40l14.5-7.3z" fill={WHITE} opacity=".46" />
        <path d="M16.8 11.6l14.5 7.3" stroke="#FBBF24" strokeWidth="3.2" strokeLinecap="round" />
      </g>
    ),
  },

  // Drivers — a silicon die with pins; the core blinks on hover.
  drivers: {
    a: '#A5B4FC', b: '#4338CA',
    glyph: (
      <>
        <path
          d="M18 7.5v5M24 7.5v5M30 7.5v5M18 35.5v5M24 35.5v5M30 35.5v5M7.5 18h5M7.5 24h5M7.5 30h5M35.5 18h5M35.5 24h5M35.5 30h5"
          stroke={WHITE} strokeWidth="2.8" strokeLinecap="round" opacity=".8"
        />
        <rect x="12.5" y="12.5" width="23" height="23" rx="5.5" fill={WHITE} />
        <rect x="18.5" y="18.5" width="11" height="11" rx="3.4" fill="#4338CA" />
        <rect x="21.2" y="21.2" width="5.6" height="5.6" rx="1.8" fill="#34E37A" data-a="blink" />
      </>
    ),
  },

  // Dashboard — three panels of live readouts; the gauge grows on hover.
  dashboard: {
    a: '#94A3B8', b: '#334155',
    glyph: (
      <>
        <rect x="7.5" y="8.5" width="19" height="14" rx="3.6" fill={WHITE} />
        <path d="M11 18.5l4.2-4.6 3.2 2.8 4.6-5.4" fill="none" stroke="#0EA5E9" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" strokeDasharray="22" data-a="draw" style={{ '--vlen': '22' } as ArtStyle} />
        <rect x="7.5" y="25.5" width="19" height="14" rx="3.6" fill={WHITE} />
        <rect x="11.5" y="31" width="3.6" height="5.4" rx="1.8" fill="#F472B6" className="vi-o-b" data-a="grow" />
        <rect x="17.2" y="28.6" width="3.6" height="7.8" rx="1.8" fill="#FBBF24" className="vi-o-b" data-a="grow" style={{ '--vdl': '.1s' } as ArtStyle} />
        <rect x="22.9" y="32.6" width="3.6" height="3.8" rx="1.8" fill="#34D399" className="vi-o-b" data-a="grow" style={{ '--vdl': '.2s' } as ArtStyle} />
        <rect x="29.5" y="8.5" width="11" height="31" rx="3.6" fill={WHITE} />
        <rect x="32.6" y="22" width="4.8" height="14" rx="2.4" fill="#6366F1" className="vi-o-b" data-a="grow" style={{ '--vdl': '.28s' } as ArtStyle} />
      </>
    ),
  },

  // Authenticator — a shield whose check draws itself on hover.
  authenticator: {
    a: '#4ADE80', b: '#14532D',
    glyph: (
      <>
        <path d="M24 6l14.5 5.2v11.3C38.5 33 31.8 39.9 24 43c-7.8-3.1-14.5-10-14.5-20.5V11.2z" fill={WHITE} />
        <path d="M17 23.8l5.2 5.2 9.4-10.2" fill="none" stroke="#16A34A" strokeWidth="4.2" strokeLinecap="round" strokeLinejoin="round" strokeDasharray="22" data-a="draw" style={{ '--vlen': '22' } as ArtStyle} />
      </>
    ),
  },

  // Vault — a gold padlock. Deliberately a DIFFERENT silhouette from the
  // Authenticator shield: the two would otherwise be one green/grey blur at
  // dock size. The shackle springs open on hover.
  vault: {
    a: '#64748B', b: '#0B1220',
    glyph: (
      <>
        <path d="M15.5 23.5v-5.8a8.5 8.5 0 0 1 17 0v5.8" fill="none" stroke="#FBBF24" strokeWidth="4.6" strokeLinecap="round" className="vi-o-b" data-a="unlock" />
        <rect x="9.5" y="21.5" width="29" height="20.5" rx="5.4" fill="#F8FAFC" />
        <circle cx="24" cy="29.6" r="3.5" fill="#334155" />
        <path d="M24 32.2v4.8" stroke="#334155" strokeWidth="3.4" strokeLinecap="round" />
      </>
    ),
  },

  // Messages — a bubble whose typing dots bounce on hover.
  messages: {
    a: '#38BDF8', b: '#075985',
    glyph: (
      <>
        <path d="M7.5 16A6.5 6.5 0 0 1 14 9.5h20a6.5 6.5 0 0 1 6.5 6.5v10a6.5 6.5 0 0 1-6.5 6.5H21.5L13 39v-6.5A6.5 6.5 0 0 1 7.5 26z" fill={WHITE} />
        <circle cx="17" cy="21" r="2.7" fill="#075985" data-a="bob" />
        <circle cx="24" cy="21" r="2.7" fill="#075985" data-a="bob" style={{ '--vdl': '.12s' } as ArtStyle} />
        <circle cx="31" cy="21" r="2.7" fill="#F59E0B" data-a="bob" style={{ '--vdl': '.24s' } as ArtStyle} />
      </>
    ),
  },

  // Relay / peering — a broadcast node; the ring pings slowly at idle.
  relay: {
    a: '#2DD4BF', b: '#115E59',
    glyph: (
      <>
        <path d="M11 11.5a18 18 0 0 0 0 25M37 11.5a18 18 0 0 1 0 25" fill="none" stroke="#CCFBF1" strokeWidth="2.8" strokeLinecap="round" opacity=".75" />
        <path d="M16.6 17a10 10 0 0 0 0 14M31.4 17a10 10 0 0 1 0 14" fill="none" stroke={WHITE} strokeWidth="3.2" strokeLinecap="round" />
        <circle cx="24" cy="24" r="9.5" fill="none" stroke="#5EEAD4" strokeWidth="2.2" data-idle="ping" />
        <circle cx="24" cy="24" r="4.6" fill="#FDE047" />
      </>
    ),
  },

  // Browser — a globe; the meridians drift on hover.
  browser: {
    a: '#60A5FA', b: '#1E3A8A',
    glyph: (
      <>
        <circle cx="24" cy="24" r="14.5" fill={WHITE} />
        <g data-a="drift">
          <path d="M9.5 24h29M24 9.5c4.7 4.8 4.7 24.2 0 29M24 9.5c-4.7 4.8-4.7 24.2 0 29" fill="none" stroke="#1E40AF" strokeWidth="2.5" strokeLinecap="round" />
        </g>
        <circle cx="30.5" cy="16.5" r="3.2" fill="#F97316" data-a="pulse" />
      </>
    ),
  },

  // Library — a book with a bookmark that slides on hover.
  library: {
    a: '#FDE68A', b: '#A16207',
    glyph: (
      <>
        <path d="M10 13a5 5 0 0 1 5-5h21a2 2 0 0 1 2 2v25H15a5 5 0 0 0-5 5z" fill={WHITE} />
        <path d="M10 35a5 5 0 0 1 5-5h23v8H15a5 5 0 0 0-5 5z" fill={WHITE} opacity=".62" />
        <path d="M31 8v14.5l-4.4-3.4-4.4 3.4V8z" fill="#EF4444" data-a="bob" />
      </>
    ),
  },

  // Gallery — a framed photo with a sun that pulses on hover.
  gallery: {
    a: '#F0ABFC', b: '#86198F',
    glyph: (
      <>
        <rect x="7.5" y="10" width="33" height="28" rx="5.5" fill={WHITE} />
        <circle cx="17" cy="19.5" r="3.8" fill="#FBBF24" data-a="pulse" />
        <path d="M10.5 36.5l7.6-8.4 5 4.8 6.2-7.3 8.2 10.9z" fill="#A21CAF" />
      </>
    ),
  },

  // Calendar — a light plate, red header, and a day chip that pops on hover.
  calendar: {
    a: '#F8FAFC', b: '#CBD5E1', light: true,
    glyph: (
      <>
        <rect x="0" y="0" width="48" height="14.5" fill="#EF4444" />
        <rect x="13" y="2.5" width="3.4" height="8" rx="1.7" fill="#FEE2E2" />
        <rect x="31.6" y="2.5" width="3.4" height="8" rx="1.7" fill="#FEE2E2" />
        <rect x="8" y="20" width="7" height="6" rx="2" fill="#94A3B8" />
        <rect x="18.5" y="20" width="7" height="6" rx="2" fill="#94A3B8" />
        <rect x="29" y="20" width="7" height="6" rx="2" fill="#94A3B8" />
        <rect x="8" y="30" width="7" height="6" rx="2" fill="#94A3B8" />
        <rect x="29" y="30" width="7" height="6" rx="2" fill="#94A3B8" />
        <rect x="18.5" y="30" width="7" height="6" rx="2" fill="#EF4444" data-a="pop" />
      </>
    ),
  },

  // Contacts — an address-card bust; the head nods on hover.
  contacts: {
    a: '#FDBA74', b: '#B45309',
    glyph: (
      <>
        <rect x="7.5" y="8.5" width="33" height="31" rx="6" fill={WHITE} />
        <circle cx="20.5" cy="20" r="5.4" fill="#B45309" data-a="bob" />
        <path d="M11.6 34.5a8.9 8.9 0 0 1 17.8 0z" fill="#B45309" />
        <path d="M32 18.5h5M32 24h5M32 29.5h5" stroke="#0EA5E9" strokeWidth="2.8" strokeLinecap="round" />
      </>
    ),
  },

  // Phone — a handset that rings (shakes) on hover.
  phone: {
    a: '#4ADE80', b: '#166534',
    glyph: (
      <>
        <path d="M12.5 7.5h5.6l2.7 7.6-3.9 2.9a20.5 20.5 0 0 0 9.6 9.6l2.9-3.9 7.6 2.7v5.6a3.8 3.8 0 0 1-4.2 3.8C19.5 34.4 10 24.9 8.7 11.7a3.8 3.8 0 0 1 3.8-4.2z" fill={WHITE} data-a="shake" />
        <path d="M28.5 12.5a8 8 0 0 1 7 7" fill="none" stroke="#FDE047" strokeWidth="3" strokeLinecap="round" data-a="pulse" />
      </>
    ),
  },

  // Calculator — a graphite body with a bright keypad.
  calculator: {
    a: '#A3A3A3', b: '#171717',
    glyph: (
      <>
        <rect x="8" y="7.5" width="32" height="9.5" rx="3.2" fill="#E2E8F0" />
        <rect x="27" y="10.6" width="9.6" height="3.3" rx="1.65" fill="#475569" />
        <circle cx="13.5" cy="23" r="2.9" fill={WHITE} />
        <circle cx="24" cy="23" r="2.9" fill={WHITE} />
        <circle cx="34.5" cy="23" r="2.9" fill="#FBBF24" data-a="pop" />
        <circle cx="13.5" cy="31" r="2.9" fill={WHITE} />
        <circle cx="24" cy="31" r="2.9" fill={WHITE} />
        <circle cx="34.5" cy="31" r="2.9" fill="#FBBF24" data-a="pop" style={{ '--vdl': '.1s' } as ArtStyle} />
        <circle cx="13.5" cy="39" r="2.9" fill={WHITE} />
        <circle cx="24" cy="39" r="2.9" fill={WHITE} />
        <circle cx="34.5" cy="39" r="2.9" fill="#34D399" data-a="pop" style={{ '--vdl': '.2s' } as ArtStyle} />
      </>
    ),
  },

  // Clock — a real face; the red second hand sweeps on hover.
  clock: {
    a: '#CBD5E1', b: '#1E293B',
    glyph: (
      <>
        <circle cx="24" cy="24" r="15.5" fill="#F8FAFC" />
        <circle cx="24" cy="24" r="15.5" fill="none" stroke="#0F172A" strokeWidth="2" opacity=".25" />
        <path d="M24 24V14.5" stroke="#0F172A" strokeWidth="3.2" strokeLinecap="round" />
        <path d="M24 24l6.8 4" stroke="#0F172A" strokeWidth="3.2" strokeLinecap="round" />
        <path d="M24 24V12.8" stroke="#EF4444" strokeWidth="1.8" strokeLinecap="round" className="vi-o-hub" data-a="tick" />
        <circle cx="24" cy="24" r="2" fill="#EF4444" />
      </>
    ),
  },

  // Text editor — a light "paper" plate with a blinking caret.
  editor: {
    a: '#FFFFFF', b: '#E2E8F0', light: true,
    glyph: (
      <>
        <rect x="9" y="9.5" width="20" height="5" rx="2.5" fill="#2563EB" />
        <rect x="9" y="19" width="30" height="3.4" rx="1.7" fill="#94A3B8" />
        <rect x="9" y="27" width="30" height="3.4" rx="1.7" fill="#94A3B8" />
        <rect x="9" y="35" width="14" height="3.4" rx="1.7" fill="#94A3B8" />
        <rect x="25.5" y="32.4" width="3" height="9" rx="1.5" fill="#2563EB" data-a="blink" />
      </>
    ),
  },

  // Weather — a sun that turns slowly behind a drifting cloud.
  weather: {
    a: '#7DD3FC', b: '#0284C7',
    glyph: (
      <>
        <g data-a="spin" style={{ '--vd': '9s' } as ArtStyle}>
          <path d="M19 5.5v4.2M19 28.3v4.2M5.6 19h4.2M28.2 19h4.2M9.5 9.5l3 3M25.5 25.5l3 3M28.5 9.5l-3 3M12.5 25.5l-3 3" stroke="#FDE047" strokeWidth="2.8" strokeLinecap="round" />
        </g>
        <circle cx="19" cy="19" r="7.4" fill="#FBBF24" />
        <path d="M17.5 40a7.6 7.6 0 0 1-1.1-15.1A10.1 10.1 0 0 1 34.8 27.6 6.6 6.6 0 0 1 34.1 40z" fill={WHITE} data-a="drift" />
      </>
    ),
  },

  // Camera — a body with a glass lens that racks focus on hover.
  camera: {
    a: '#A1A1AA', b: '#18181B',
    glyph: (
      <>
        <path d="M6 17.5a4.5 4.5 0 0 1 4.5-4.5h4.2l2.2-3.6a2.5 2.5 0 0 1 2.1-1.2h9.9a2.5 2.5 0 0 1 2.1 1.2l2.2 3.6h4.3A4.5 4.5 0 0 1 42 17.5v18a4.5 4.5 0 0 1-4.5 4.5h-27A4.5 4.5 0 0 1 6 35.5z" fill={WHITE} />
        <circle cx="24" cy="26" r="8.6" fill="#0EA5E9" data-a="pulse" />
        <circle cx="24" cy="26" r="4.2" fill="#0C4A6E" />
        <circle cx="21" cy="23" r="1.7" fill={WHITE} opacity=".85" />
        <circle cx="35.6" cy="18.8" r="2.4" fill="#F87171" data-a="blink" />
      </>
    ),
  },

  // Image editor — a paint palette; the brush wags on hover.
  imageEditor: {
    a: '#8B5CF6', b: '#4C1D95',
    glyph: (
      <>
        <path d="M24 7c9.4 0 17 6.6 17 14.8 0 5-4 7.2-7.6 7.2h-3.2c-2.4 0-4.2 1.9-4.2 4.1 0 1 .4 1.8.9 2.6.5.8.9 1.6.9 2.5 0 2.2-1.8 3.8-4 3.8C13.9 42 7 34.6 7 24.5 7 14.8 14.6 7 24 7z" fill={WHITE} />
        <circle cx="16" cy="18.5" r="3" fill="#EF4444" />
        <circle cx="24" cy="14.5" r="3" fill="#FBBF24" />
        <circle cx="32" cy="18.5" r="3" fill="#0EA5E9" />
        <circle cx="14.5" cy="28.5" r="3" fill="#22C55E" data-a="pulse" />
      </>
    ),
  },

  // Music — a beamed note over a bright equaliser.
  music: {
    a: '#C084FC', b: '#5B21B6',
    glyph: (
      <>
        <g data-a="bob">
          <path d="M19.5 31V13.6l16-3.4V27" fill="none" stroke={WHITE} strokeWidth="3.4" strokeLinecap="round" strokeLinejoin="round" />
          <path d="M19.5 13.6l16-3.4" stroke="#FDE047" strokeWidth="3.6" strokeLinecap="round" />
          <circle cx="15" cy="31.5" r="4.8" fill={WHITE} />
          <circle cx="31" cy="27.8" r="4.8" fill={WHITE} />
        </g>
      </>
    ),
  },

  // Screenshot — crop brackets closing on a bright capture area.
  screenshot: {
    a: '#94A3B8', b: '#1E293B',
    glyph: (
      <>
        <path d="M9 17.5V13a4 4 0 0 1 4-4h4.5M30.5 9H35a4 4 0 0 1 4 4v4.5M39 30.5V35a4 4 0 0 1-4 4h-4.5M17.5 39H13a4 4 0 0 1-4-4v-4.5" fill="none" stroke={WHITE} strokeWidth="3.4" strokeLinecap="round" />
        <rect x="16.5" y="16.5" width="15" height="15" rx="3.4" fill="#FBBF24" data-a="pulse" />
      </>
    ),
  },

  // System info — a display with three readout bars.
  system: {
    a: '#CBD5E1', b: '#334155',
    glyph: (
      <>
        <rect x="6.5" y="9" width="35" height="24" rx="4.5" fill={WHITE} />
        <path d="M24 33v5.5M17 38.5h14" stroke={WHITE} strokeWidth="3.2" strokeLinecap="round" />
        <rect x="11.5" y="21" width="4.6" height="8" rx="2.3" fill="#0EA5E9" className="vi-o-b" data-a="grow" />
        <rect x="19.4" y="16" width="4.6" height="13" rx="2.3" fill="#F59E0B" className="vi-o-b" data-a="grow" style={{ '--vdl': '.1s' } as ArtStyle} />
        <rect x="27.3" y="19" width="4.6" height="10" rx="2.3" fill="#34D399" className="vi-o-b" data-a="grow" style={{ '--vdl': '.2s' } as ArtStyle} />
      </>
    ),
  },

  // Video — a player; the scrubber runs on hover.
  video: {
    a: '#52525B', b: '#09090B',
    glyph: (
      <>
        <rect x="6.5" y="9.5" width="35" height="24" rx="5" fill={WHITE} />
        <path d="M20.5 15.8l11 5.7-11 5.7z" fill="#EF4444" data-a="pulse" />
        <rect x="11" y="37" width="26" height="3.4" rx="1.7" fill={WHITE} opacity=".35" />
        <rect x="11" y="37" width="26" height="3.4" rx="1.7" fill="#F59E0B" className="vi-o-l" style={{ transform: 'scaleX(.42)' }} data-a="scrub" />
      </>
    ),
  },

  // Voice recorder — a mic with a blinking record light.
  microphone: {
    a: '#52525B', b: '#18181B',
    glyph: (
      <>
        <rect x="18.5" y="7" width="11" height="20" rx="5.5" fill={WHITE} />
        <path d="M13 23a11 11 0 0 0 22 0" fill="none" stroke={WHITE} strokeWidth="3.2" strokeLinecap="round" />
        <path d="M24 34v5.5M18 39.5h12" stroke={WHITE} strokeWidth="3.2" strokeLinecap="round" />
        <circle cx="24" cy="13.5" r="2.9" fill="#EF4444" data-a="blink" />
      </>
    ),
  },

  // Office — a document with a colourful chart.
  office: {
    a: '#F87171', b: '#991B1B',
    glyph: (
      <>
        <path d="M11 10a4 4 0 0 1 4-4h11.5L38 17.5V38a4 4 0 0 1-4 4H15a4 4 0 0 1-4-4z" fill={WHITE} />
        <path d="M26.5 6L38 17.5H26.5z" fill="#FCA5A5" />
        <rect x="16.5" y="29" width="4.4" height="8" rx="2.2" fill="#EF4444" className="vi-o-b" data-a="grow" />
        <rect x="23.2" y="25" width="4.4" height="12" rx="2.2" fill="#F59E0B" className="vi-o-b" data-a="grow" style={{ '--vdl': '.1s' } as ArtStyle} />
        <rect x="29.9" y="21.5" width="4.4" height="15.5" rx="2.2" fill="#22C55E" className="vi-o-b" data-a="grow" style={{ '--vdl': '.2s' } as ArtStyle} />
      </>
    ),
  },
}

// Aliases — several app ids share one drawing (documented at each entry so a
// new id is a one-line addition, not a copy of the artwork).
ART.settings = ART.persona
ART.chat = ART.messages
ART.peering = ART.relay
ART['vulos-calendar'] = ART.calendar
ART['vulos-contacts'] = ART.contacts
ART['vulos-phone'] = ART.phone
ART['text-editor'] = ART.editor
ART['image-editor'] = ART.imageEditor
ART['system-info'] = ART.system
ART['voice-recorder'] = ART.microphone

export { ART }

export function hasArt(id: string | undefined): boolean {
  return !!id && Object.prototype.hasOwnProperty.call(ART, id)
}

// ── Stylesheet ────────────────────────────────────────────────────────────
// One injected sheet, shared by every plate. Injected the same way the tile
// chrome is (see AppIcons.tsx) so nothing depends on bundler CSS ordering.
export const APP_ART_CSS = `
.vi-plate{
  position:relative; display:block; flex-shrink:0; overflow:hidden;
  background:
    radial-gradient(118% 82% at 26% 6%, rgba(255,255,255,.30), rgba(255,255,255,0) 58%),
    linear-gradient(152deg, var(--art-a, #64748B), var(--art-b, #1E293B));
  box-shadow:
    inset 0 0 0 1px rgba(255,255,255,.16),
    inset 0 1.4px 0 rgba(255,255,255,.22),
    0 1px 2px rgba(0,0,0,.35);
}
.vi-plate[data-light]{
  box-shadow:
    inset 0 0 0 1px rgba(15,23,42,.14),
    inset 0 1.4px 0 rgba(255,255,255,.7),
    0 1px 2px rgba(15,23,42,.22);
}
.vi-art{ position:absolute; inset:0; display:block; width:100%; height:100%; }
/* Every animated part rotates/scales about ITSELF unless it opts into a
   different origin — without fill-box an SVG child would pivot about the
   viewBox origin and fly off the plate. */
.vi-art [data-a], .vi-art [data-idle]{ transform-box: fill-box; transform-origin: 50% 50%; }
.vi-art .vi-o-b{ transform-origin: 50% 100%; }
.vi-art .vi-o-l{ transform-origin: 0% 50%; }
.vi-art .vi-o-hub{ transform-box: view-box; transform-origin: 24px 24px; }

@keyframes vi-spin   { from{ transform: rotate(0deg) }   to{ transform: rotate(360deg) } }
@keyframes vi-tick   { from{ transform: rotate(0deg) }   to{ transform: rotate(360deg) } }
@keyframes vi-pulse  { 0%,100%{ transform: scale(1) } 50%{ transform: scale(1.13) } }
@keyframes vi-bob    { 0%,100%{ transform: translateY(0) } 45%{ transform: translateY(-2.4px) } }
@keyframes vi-blink  { 0%,100%{ opacity:1 } 50%{ opacity:.18 } }
/* --vlen must equal the path's own stroke-dasharray, otherwise the offset
   lands on a multiple of the dash and the "redraw" is invisible. */
@keyframes vi-draw   { from{ stroke-dashoffset: var(--vlen, 66) } to{ stroke-dashoffset: 0 } }
@keyframes vi-slidex { 0%,100%{ transform: translateX(0) } 50%{ transform: translateX(-7px) } }
@keyframes vi-rise   { 0%{ transform: translateY(3px); opacity:0 } 25%{ opacity:1 } 100%{ transform: translateY(-5px); opacity:0 } }
@keyframes vi-shake  { 0%,100%{ transform: rotate(0deg) } 25%{ transform: rotate(-11deg) } 75%{ transform: rotate(11deg) } }
@keyframes vi-flap   { 0%,100%{ transform: rotateX(0deg) } 50%{ transform: rotateX(38deg) } }
@keyframes vi-pop    { 0%,100%{ transform: scale(1) } 40%{ transform: scale(.78) } 70%{ transform: scale(1.08) } }
@keyframes vi-grow   { 0%{ transform: scaleY(.35) } 55%{ transform: scaleY(1) } 100%{ transform: scaleY(.35) } }
@keyframes vi-meter  { from{ stroke-dashoffset: 72.3 } to{ stroke-dashoffset: 26 } }
@keyframes vi-scrub  { from{ transform: scaleX(.08) } to{ transform: scaleX(1) } }
@keyframes vi-drift  { 0%,100%{ transform: translateX(-1.8px) } 50%{ transform: translateX(1.8px) } }
@keyframes vi-unlock { 0%,100%{ transform: none } 40%,70%{ transform: translateY(-2.6px) rotate(-9deg) } }
@keyframes vi-twinkle{ 0%,100%{ transform: scale(.68); opacity:.45 } 50%{ transform: scale(1); opacity:1 } }
@keyframes vi-ping   { 0%{ transform: scale(.72); opacity:.85 } 70%,100%{ transform: scale(1.5); opacity:0 } }
@keyframes vi-beat   { 0%,100%{ transform: scale(.82); opacity:.75 } 45%{ transform: scale(1.1); opacity:1 } }

@media (prefers-reduced-motion: no-preference){
  /* HOVER-ONLY BUDGET: the animation-name below is inert because no duration
     is set at rest. The duration (and therefore the animation) only exists
     inside the :hover rule, so an idle grid of icons runs zero animations. */
  .vi-art [data-a="spin"]  { animation-name: vi-spin;   animation-timing-function: linear }
  .vi-art [data-a="tick"]  { animation-name: vi-tick;   animation-timing-function: linear }
  .vi-art [data-a="pulse"] { animation-name: vi-pulse }
  .vi-art [data-a="bob"]   { animation-name: vi-bob }
  .vi-art [data-a="blink"] { animation-name: vi-blink }
  .vi-art [data-a="draw"]  { animation-name: vi-draw;   animation-timing-function: cubic-bezier(.65,0,.35,1) }
  .vi-art [data-a="slidex"]{ animation-name: vi-slidex }
  .vi-art [data-a="rise"]  { animation-name: vi-rise;   animation-timing-function: cubic-bezier(.4,0,.2,1) }
  .vi-art [data-a="shake"] { animation-name: vi-shake }
  .vi-art [data-a="flap"]  { animation-name: vi-flap }
  .vi-art [data-a="pop"]   { animation-name: vi-pop }
  .vi-art [data-a="grow"]  { animation-name: vi-grow }
  .vi-art [data-a="meter"] { animation-name: vi-meter;  animation-timing-function: cubic-bezier(.2,.8,.3,1) }
  .vi-art [data-a="scrub"] { animation-name: vi-scrub;  animation-timing-function: linear }
  .vi-art [data-a="drift"] { animation-name: vi-drift }
  .vi-art [data-a="unlock"]{ animation-name: vi-unlock }

  :is(.vula-itile, .vi-plate, button, a, [role="button"]):hover .vi-art [data-a],
  .vula-itile.is-hover .vi-art [data-a]{
    animation-duration: var(--vd, 1.15s);
    animation-delay: var(--vdl, 0s);
    animation-iteration-count: infinite;
    animation-fill-mode: none;
  }

  /* Idle beat — deliberately only three icons (four elements) across the whole
     set, all slow and transform/opacity only. Adding more is a CPU regression. */
  .vi-art [data-idle]{
    animation-duration: var(--vd, 3.6s);
    animation-delay: var(--vdl, 0s);
    animation-iteration-count: infinite;
    animation-timing-function: cubic-bezier(.4,0,.2,1);
  }
  .vi-art [data-idle="twinkle"]{ animation-name: vi-twinkle; animation-duration: var(--vd, 3.2s) }
  .vi-art [data-idle="ping"]   { animation-name: vi-ping;    animation-duration: var(--vd, 3.4s) }
  .vi-art [data-idle="beat"]   { animation-name: vi-beat;    animation-duration: var(--vd, 2.6s) }
}
`

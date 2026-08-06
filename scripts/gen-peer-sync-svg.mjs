#!/usr/bin/env node
// Generates docs/assets/peer-sync-{light,dark}.svg from ONE geometry source.
//
// The two variants previously existed as independently hand-maintained files.
// That is the divergent-duplicate shape this project keeps getting bitten by:
// nothing stopped a fix landing in one and not the other, and nothing checked.
// Geometry now lives here once and only the palette differs, so the pair cannot
// drift.
//
//   node scripts/gen-peer-sync-svg.mjs            # write both files
//   node scripts/gen-peer-sync-svg.mjs --check    # fail if either is stale
//
// Layout notes worth keeping:
//   * All three devices are centred on the SAME axis (AXIS below). They were
//     previously at 76 / 75 / 75.2, which reads as a wobble rather than a row.
//   * Every connector starts and ends on that axis for the same reason.
//   * Card chrome (title bars) is CLIPPED to its card. Drawing a square-cornered
//     bar over a rounded card left the bar's corners poking out past the card —
//     the most visible defect in the original.
//   * The container is centred on the content it contains, not eyeballed.

import { writeFileSync, readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const OUT = join(ROOT, 'docs', 'assets')

// ── Palette ──────────────────────────────────────────────────────────────────
const PALETTES = {
  light: {
    secondary: '#3a352e', // device outlines, neutral
    faint: '#7c7264', // labels
    ghost: '#a49a89', // dashed rules, container
    accent: '#0d9488', // the serving instance + peer link
    surface: '#efebe3', // card fill — matches the README page tone
  },
  dark: {
    secondary: '#c9c9c9',
    faint: '#7a7a7a',
    ghost: '#5a5a5a',
    accent: '#2dd4bf',
    surface: '#161616',
  },
}

// ── Geometry ─────────────────────────────────────────────────────────────────
const AXIS = 75 // the single horizontal axis every device is centred on
const MONO = "ui-monospace, 'SF Mono', 'JetBrains Mono', Menlo, monospace"

const phone = { x: 15, w: 26, h: 46 }
phone.y = AXIS - phone.h / 2 // 52
phone.cx = phone.x + phone.w / 2 // 28

const box = { x: 118, w: 52, h: 34 }
box.y = AXIS - box.h / 2 // 58
box.cx = box.x + box.w / 2 // 144

// Laptop height is screen + base wedge, so the wedge is included when centring.
const lap = { x: 204, w: 40, screenH: 26, baseH: 4 }
lap.y = AXIS - (lap.screenH + lap.baseH) / 2 // 60
lap.screenBottom = lap.y + lap.screenH // 86
lap.baseBottom = lap.screenBottom + lap.baseH // 90
lap.cx = lap.x + lap.w / 2 // 224
lap.baseX = lap.x - 4 // base overhangs the screen
lap.baseW = lap.w + 8

// Container hugs the content it holds, centred on it rather than by eye.
const PAD = 14
const contentL = box.x
const contentR = lap.baseX + lap.baseW // 248
const cont = { x: contentL - PAD, y: 30, h: 94, rx: 10 }
cont.w = contentR - contentL + PAD * 2 // 158

const LABEL_Y = 108 // one baseline for all three device labels
const SUB_Y = 117

const t = (x, y, s, size, opts = {}) => {
  const { weight, ls, op, anchor = 'middle' } = opts
  return (
    `<text fill="currentColor" x="${x}" y="${y}" text-anchor="${anchor}" ` +
    `font-family="${MONO}" font-size="${size}"` +
    (weight ? ` font-weight="${weight}"` : '') +
    (ls ? ` letter-spacing="${ls}"` : '') +
    (op ? ` opacity="${op}"` : '') +
    `>${s}</text>`
  )
}

function svg(tone) {
  const c = PALETTES[tone]
  return `<svg xmlns="http://www.w3.org/2000/svg"
     viewBox="0 0 280 143"
     role="img"
     aria-labelledby="hi-psync-title hi-psync-desc"
     preserveAspectRatio="xMidYMid meet"
     style="display:block;width:100%;height:auto"
     fill="none"
   >
  <title id="hi-psync-title">One person, many Vulos instances that sync as peers</title>
  <desc id="hi-psync-desc">
    A phone reaches, from anywhere, a group of instances you own — an always-on
    home box and a laptop — that stay in sync with each other as equal peers.
    The home box is highlighted as the instance currently serving traffic.
  </desc>

  <defs>
    <!-- Chrome is clipped to its card so a square-cornered title bar cannot
         poke out past the card's rounded corners. -->
    <clipPath id="clip-box"><rect x="${box.x}" y="${box.y}" width="${box.w}" height="${box.h}" rx="6" /></clipPath>
    <clipPath id="clip-lap"><rect x="${lap.x}" y="${lap.y}" width="${lap.w}" height="${lap.screenH}" rx="4" /></clipPath>
    <clipPath id="clip-phone"><rect x="${phone.x + 3}" y="${phone.y + 6}" width="${phone.w - 6}" height="${phone.h - 14}" rx="1.6" /></clipPath>
  </defs>

  <!-- ── Phone ── -->
  <g data-tone="secondary" color="${c.secondary}">
    <rect x="${phone.x}" y="${phone.y}" width="${phone.w}" height="${phone.h}" rx="5.5"
          fill="${c.surface}" stroke="currentColor" stroke-width="1" stroke-opacity="0.55" />
    <rect x="${phone.x + 3}" y="${phone.y + 6}" width="${phone.w - 6}" height="${phone.h - 14}" rx="1.6"
          fill="none" stroke="currentColor" stroke-width="0.7" stroke-opacity="0.32" />
    <rect x="${phone.cx - 4}" y="${phone.y + 2.5}" width="8" height="1.3" rx="0.65" fill="currentColor" opacity="0.5" />
    <rect x="${phone.cx - 4}" y="${phone.y + phone.h - 3.6}" width="8" height="1.4" rx="0.7" fill="currentColor" opacity="0.45" />
    <g clip-path="url(#clip-phone)">
      <rect x="${phone.x + 5}" y="${phone.y + 10}" width="16" height="3.2" rx="1" fill="currentColor" opacity="0.16" />
      <circle cx="${phone.x + 7}" cy="${phone.y + 11.6}" r="0.9" fill="currentColor" opacity="0.42" />
      <rect x="${phone.x + 5}" y="${phone.y + 16.5}" width="16" height="1.9" rx="0.95" fill="currentColor" opacity="0.28" />
      <rect x="${phone.x + 5}" y="${phone.y + 21}" width="10" height="1.9" rx="0.95" fill="currentColor" opacity="0.2" />
      <rect x="${phone.x + 5}" y="${phone.y + 25.5}" width="13" height="1.9" rx="0.95" fill="currentColor" opacity="0.2" />
    </g>
  </g>
  <g data-tone="faint" color="${c.faint}">
    ${t(phone.cx, LABEL_Y, 'Phone', 5.5, { weight: 700 })}
    ${t(phone.cx, SUB_Y, 'any browser', 4.6, { op: 0.8 })}
  </g>

  <!-- ── Reach: phone → the instances you own ── -->
  <g data-tone="ghost" color="${c.ghost}">
    <line x1="${phone.x + phone.w + 3}" y1="${AXIS}" x2="${cont.x - 3}" y2="${AXIS}"
          stroke="currentColor" stroke-width="1" stroke-dasharray="3 3" stroke-opacity="0.5" />
    <path d="M${cont.x - 8} ${AXIS - 3} L${cont.x - 3} ${AXIS} L${cont.x - 8} ${AXIS + 3} Z" fill="currentColor" opacity="0.6" />
  </g>
  <g data-tone="faint" color="${c.faint}">
    ${t((phone.x + phone.w + cont.x) / 2, AXIS - 7, 'reach from anywhere', 4.6, { ls: '0.04em' })}
  </g>

  <!-- ── Container: instances you own ── -->
  <g data-tone="ghost" color="${c.ghost}">
    <rect x="${cont.x}" y="${cont.y}" width="${cont.w}" height="${cont.h}" rx="${cont.rx}"
          stroke="currentColor" stroke-width="1" stroke-opacity="0.35" stroke-dasharray="5 4" fill="none" />
  </g>
  <g data-tone="faint" color="${c.faint}">
    ${t(cont.x + 8, cont.y - 6, 'INSTANCES YOU OWN', 5.5, { ls: '0.14em', anchor: 'start' })}
  </g>

  <!-- ── Home box (the instance currently serving) ── -->
  <g data-tone="good" color="${c.accent}">
    <rect x="${box.x}" y="${box.y}" width="${box.w}" height="${box.h}" rx="6"
          fill="${c.surface}" stroke="currentColor" stroke-width="1" stroke-opacity="0.6" />
    <g clip-path="url(#clip-box)">
      <rect x="${box.x}" y="${box.y}" width="${box.w}" height="3" fill="currentColor" opacity="0.55" />
    </g>
    <circle cx="${box.x + box.w - 8}" cy="${box.y + 8}" r="2" fill="currentColor" />
    <rect x="${box.x + 8}" y="${box.y + 12}" width="20" height="2.4" rx="1.2" fill="currentColor" opacity="0.4" />
    <rect x="${box.x + 8}" y="${box.y + 18}" width="14" height="2.4" rx="1.2" fill="currentColor" opacity="0.4" />
  </g>
  <g data-tone="faint" color="${c.faint}">
    ${t(box.cx, LABEL_Y, 'home box', 5.5, { weight: 700 })}
    ${t(box.cx, SUB_Y, 'always-on', 4.6, { op: 0.8 })}
  </g>

  <!-- ── Laptop ── -->
  <g data-tone="secondary" color="${c.secondary}">
    <rect x="${lap.x}" y="${lap.y}" width="${lap.w}" height="${lap.screenH}" rx="4"
          fill="${c.surface}" stroke="currentColor" stroke-width="1" stroke-opacity="0.5" />
    <g clip-path="url(#clip-lap)">
      <rect x="${lap.x}" y="${lap.y}" width="${lap.w}" height="2.6" fill="currentColor" opacity="0.45" />
    </g>
    <rect x="${lap.x + 6}" y="${lap.y + 9}" width="16" height="2.1" rx="1.05" fill="currentColor" opacity="0.3" />
    <rect x="${lap.x + 6}" y="${lap.y + 14}" width="11" height="2.1" rx="1.05" fill="currentColor" opacity="0.22" />
    <path d="M${lap.x} ${lap.screenBottom} L${lap.x + lap.w} ${lap.screenBottom} L${lap.baseX + lap.baseW - 1.2} ${lap.baseBottom - 1.2} A1.2 1.2 0 0 1 ${lap.baseX + lap.baseW - 2.4} ${lap.baseBottom} L${lap.baseX + 2.4} ${lap.baseBottom} A1.2 1.2 0 0 1 ${lap.baseX + 1.2} ${lap.baseBottom - 1.2} Z"
          fill="${c.surface}" stroke="currentColor" stroke-width="1" stroke-opacity="0.5" stroke-linejoin="round" />
    <rect x="${lap.cx - 5}" y="${lap.screenBottom + 1.4}" width="10" height="1.1" rx="0.55" fill="currentColor" opacity="0.3" />
  </g>
  <g data-tone="faint" color="${c.faint}">
    ${t(lap.cx, LABEL_Y, 'laptop', 5.5, { weight: 700 })}
    ${t(lap.cx, SUB_Y, 'travels with you', 4.6, { op: 0.8 })}
  </g>

  <!-- ── Peer sync: equal, bidirectional ── -->
  <g data-tone="accent" color="${c.accent}">
    <line x1="${box.x + box.w + 2}" y1="${AXIS}" x2="${lap.baseX - 2}" y2="${AXIS}"
          stroke="currentColor" stroke-width="1.4" stroke-opacity="0.9" />
    <path d="M${box.x + box.w + 7} ${AXIS - 3} L${box.x + box.w + 2} ${AXIS} L${box.x + box.w + 7} ${AXIS + 3} Z" fill="currentColor" />
    <path d="M${lap.baseX - 7} ${AXIS - 3} L${lap.baseX - 2} ${AXIS} L${lap.baseX - 7} ${AXIS + 3} Z" fill="currentColor" />
    <!-- The gap between the two instances is only ${lap.baseX - (box.x + box.w)}px wide, which is
         narrower than this label. Sitting it in the gap crowds both devices, so
         it rides above them where it has clear air. -->
    ${t((box.x + box.w + lap.baseX) / 2, box.y - 6, 'peer sync', 5, { weight: 700, ls: '0.04em' })}
  </g>
</svg>
`
}

const targets = [
  ['peer-sync-light.svg', svg('light')],
  ['peer-sync-dark.svg', svg('dark')],
]

if (process.argv.includes('--check')) {
  let stale = false
  for (const [name, want] of targets) {
    const p = join(OUT, name)
    if (!existsSync(p) || readFileSync(p, 'utf8') !== want) {
      console.error(`${p} is stale — run: node scripts/gen-peer-sync-svg.mjs`)
      stale = true
    }
  }
  if (stale) process.exit(1)
  console.log('peer-sync SVGs are up to date.')
} else {
  for (const [name, out] of targets) {
    writeFileSync(join(OUT, name), out)
    console.log(`wrote ${join(OUT, name)}`)
  }
}

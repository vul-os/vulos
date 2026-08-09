// ICON LAB — a real reviewing tool for the app icon set, not shipped code.
//
// Why it exists: every genuine defect in this icon set has been found by
// LOOKING at a render, never by reading the source. One agent shipped an arc
// that drew entirely off-canvas and TypeScript, tests and lint were all green.
// So the set gets an offline harness that renders every plate at the real sizes
// it appears at (dock 26, home 38, launchpad 60, review 96) in both themes.
//
// Run:   npx vite --port 5377        then open /icon-lab.html
// Shoot: see the `data-shot` sections — a Playwright script can clip each one.
//
// The id list is DERIVED from ART, not hand-written: a hand-written list is how
// a new icon gets added and never reviewed. Extra non-art ids are appended so
// the art plates can be compared against the tiles they sit next to.
import { StrictMode, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import '../index.css'
import AppIcon, { AppIconTile } from '../core/AppIcons'
import { ART } from '../core/appArt'

// Every art id, minus the aliases that point at an already-listed drawing —
// reviewing the same artwork twice wastes screen and hides a real gap.
const seen = new Set<unknown>()
const ART_IDS = Object.keys(ART).filter((id) => {
  if (seen.has(ART[id])) return false
  seen.add(ART[id])
  return true
})

// Non-art ids that share the launcher with the plates. `terminal` is the
// quality bar the whole set is measured against, so it leads.
const OTHER_IDS = ['terminal', 'lilmail', 'firefox', 'gitea', 'unknown-app']

export function Grid({ ids, size, tile }: { ids: string[]; size: number; tile: boolean }) {
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 14 }}>
      {ids.map((id) => (
        <div key={id} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 5, width: Math.max(size, 68) }}>
          {tile ? <AppIconTile id={id} size={size} /> : <AppIcon id={id} size={size} />}
          <span style={{ font: '400 9px system-ui', color: 'var(--text-tertiary, #888)', textAlign: 'center', lineHeight: 1.25 }}>{id}</span>
        </div>
      ))}
    </div>
  )
}

export function Section({ name, label, children }: { name: string; label: string; children: ReactNode }) {
  return (
    <section data-shot={name} style={{ padding: 16, marginBottom: 8, background: 'var(--bg-base, #0a0a0f)' }}>
      <div style={{ font: '600 12px system-ui', color: 'var(--text-secondary)', marginBottom: 12 }}>{label}</div>
      {children}
    </section>
  )
}

export function Lab() {
  const ids = [...OTHER_IDS.slice(0, 1), ...ART_IDS, ...OTHER_IDS.slice(1)]
  return (
    <div style={{ padding: 8, background: 'var(--bg-base, #0a0a0f)', minHeight: '100vh' }}>
      <Section name="t96" label="Tile @ 96 — detail review">
        <Grid ids={ids} size={96} tile />
      </Section>
      <Section name="t60" label="Tile @ 60 — Launchpad">
        <Grid ids={ids} size={60} tile />
      </Section>
      <Section name="t38" label="Tile @ 38 — Home">
        <Grid ids={ids} size={38} tile />
      </Section>
      <Section name="t32" label="Tile @ 32 — DOCK-SIZE LEGIBILITY TEST (mush here is a bug)">
        <Grid ids={ids} size={32} tile />
      </Section>
      <Section name="i26" label="Inline @ 26 — Dock">
        <Grid ids={ids} size={26} tile={false} />
      </Section>
      <Section name="i16" label="Inline @ 16 — titlebar (hairline glyph fallback)">
        <Grid ids={ids} size={16} tile={false} />
      </Section>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(<StrictMode><Lab /></StrictMode>)

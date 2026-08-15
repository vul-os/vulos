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
//
// A handful of drawings are keyed in appArt.tsx under an internal helper name
// (`imageEditor`, `system`, `microphone`, `editor`) with the real,
// hyphenated app id (`image-editor`, `system-info`, `voice-recorder`,
// `text-editor`) added afterwards as `ART['image-editor'] = ART.imageEditor`.
// `Object.keys()` visits insertion order, so a plain first-seen dedup always
// keeps the internal name and drops the real one — the lab then renders and
// labels the tile under an id no app ever actually passes to AppIconTile.
// `calendar`/`contacts` have the opposite shape: the bare word was never a
// real app id (retired — see AppRegistry.ts), only `vulos-calendar` /
// `vulos-contacts` are. PREFERRED_ID corrects both directions so the label
// always matches what a real app resolves, and the tile is rendered by that
// same real id (not just the internal spec) — exercising the actual lookup.
const PREFERRED_ID: Record<string, string> = {
  imageEditor: 'image-editor',
  system: 'system-info',
  microphone: 'voice-recorder',
  editor: 'text-editor',
  calendar: 'vulos-calendar',
  contacts: 'vulos-contacts',
}
const seen = new Set<unknown>()
const ART_IDS = Object.keys(ART)
  .filter((id) => {
    if (seen.has(ART[id])) return false
    seen.add(ART[id])
    return true
  })
  .map((id) => PREFERRED_ID[id] || id)

// Non-art ids that share the launcher with the plates. `terminal` is the
// quality bar the whole set is measured against, so it leads.
const OTHER_IDS = ['terminal', 'lilmail', 'firefox', 'gitea', 'unknown-app']

// FULL_CATALOG_IDS — every id that can actually reach AppIconTile at runtime:
// every AppRegistry.ts builtin/default-web-app id, every registry.json catalog
// id, and every APP_LOGOS key (product marks not in either list above). This
// is what a "does every real app resolve a real icon, with no silent fallback
// and no two apps sharing a tile" review has to look at — the ART-only list
// above misses the ~70 brand-logo and plain-glyph apps entirely. Re-derive
// with the one-liner in AppIcons.test.ts's coverage assertion if the roster
// drifts; this literal snapshot keeps the lab importable without reaching
// across the Vite root to registry.json.
const FULL_CATALOG_IDS = [
  'activity', 'android', 'apphub', 'aql', 'ardour', 'assistant', 'audacity', 'audiomass',
  'authenticator', 'blender', 'browser', 'browser-stream', 'calculator', 'camera', 'chrome',
  'cinny', 'clock', 'cockpit', 'code-server', 'conduit', 'darktable', 'dashboard', 'diagrams-net',
  'disks', 'diwan', 'drawio', 'drive', 'drivers', 'element', 'element-call', 'envoir', 'excalidraw',
  'files', 'filezilla', 'firefox', 'gallery', 'geany', 'gimp', 'gitea', 'gitstate', 'gnucash',
  'grafana', 'home', 'hoppscotch', 'httpbin', 'image-editor', 'immich', 'inkscape', 'jellyfin',
  'jitsi-meet', 'jupyter', 'kdenlive', 'keepassxc', 'kerf', 'kicad', 'kilio', 'kotva', 'library',
  'libreoffice', 'libretranslate', 'lilmail', 'llmux', 'lmms', 'lutris', 'magnetite', 'mail',
  'memos', 'messages', 'minio', 'minipaint', 'music', 'navidrome', 'nginx', 'obs-studio', 'octave',
  'packages', 'persona', 'phone', 'qbittorrent', 'qgis', 'screenshot', 'shotcut', 'soko', 'steam',
  'svg-edit', 'syncthing', 'system-info', 'terminal', 'text-editor', 'transmission', 'uptime-kuma',
  'vault', 'vaultwarden', 'video', 'vlc', 'voice-recorder', 'vulos-calendar', 'vulos-contacts',
  'vulos-phone', 'vuna', 'weather', 'wede', 'wine',
]

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
      <Section name="full" label={`Full catalog @ 56 — every reachable app id (${FULL_CATALOG_IDS.length}) — a letter tile here is a real fallback, not a review artifact`}>
        <Grid ids={FULL_CATALOG_IDS} size={56} tile />
      </Section>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(<StrictMode><Lab /></StrictMode>)

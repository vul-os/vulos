/**
 * The familiar-layout presets, as DATA.
 *
 * # Ergonomics, not imitation
 *
 * People arriving from Windows, macOS or Ubuntu carry muscle memory, and the
 * part of it that transfers is CONVENTION: which edge the launcher lives on,
 * whether the strip is a full-width bar or a floating island, which side the
 * window controls sit on, whether the clock is top-right or bottom-right.
 *
 * What does NOT transfer, and is deliberately absent from this file, is anyone's
 * trade dress. No cloned chrome, no copied icons, no lifted branding, no
 * "looks exactly like macOS" skin. Every preset draws Vulos' own tokens, its own
 * icons and its own type; a screenshot of any of them is unmistakably Vulos.
 * That is also why the presets are named for what they DO — "Taskbar", "Menu bar
 * and dock", "Side dock" — with the platform habit stated as a plain note rather
 * than as the name.
 *
 * # One of these is built through the public format
 *
 * `side-dock` is not written in TypeScript here. It is a JSON manifest in
 * ./packs/side-dock.pack.json, parsed by the same validatePack() a third party's
 * pack goes through. If the public pack format ever stops being able to express
 * a shipping preset, this module fails to load and the unit test goes red —
 * which is the only honest proof that the format is usable.
 */

import { validatePack } from './validate'
import type { DesktopLayout, LayoutPreset } from './types'
import sideDockPack from './packs/side-dock.pack.json'

/** The Vulos look. Always present, always reachable, and what revert returns to. */
export const DEFAULT_PRESET_ID = 'vulos'

const BUILTIN: LayoutPreset[] = [
  {
    id: 'vulos',
    name: 'Vulos',
    familiar: 'The default — a floating dock and a global menu bar',
    summary: 'A rounded dock floating at the bottom of the screen and a slim menu bar across the top. This is what a fresh box looks like.',
    source: 'builtin',
    layout: {
      dock: {
        desktop: {
          edge: 'bottom', size: 'medium', style: 'floating', align: 'center',
          autohide: false, launcher: true, assistant: true, drawer: false,
          items: ['drive', 'lilmail', 'vulos-calendar', 'messages', 'terminal', 'persona'],
        },
        mobile: {
          edge: 'bottom', size: 'large', style: 'bar', align: 'center',
          autohide: false, launcher: false, assistant: true, drawer: true,
          items: ['home', 'lilmail', 'messages'],
        },
      },
      windowControls: 'left',
      tokens: {},
    },
  },
  {
    id: 'taskbar',
    name: 'Taskbar',
    familiar: 'Matches Windows habits',
    summary: 'A full-width bar flush with the bottom edge, apps starting from the left, and window controls on the right of each titlebar.',
    source: 'builtin',
    layout: {
      dock: {
        desktop: {
          edge: 'bottom', size: 'small', style: 'bar', align: 'start',
          autohide: false, launcher: true, assistant: true, drawer: false,
          items: ['home', 'files', 'lilmail', 'vulos-calendar', 'messages', 'terminal', 'persona'],
        },
        mobile: {
          edge: 'bottom', size: 'large', style: 'bar', align: 'center',
          autohide: false, launcher: false, assistant: true, drawer: true,
          items: ['home', 'files', 'lilmail', 'messages'],
        },
      },
      windowControls: 'right',
      tokens: { '--vd-dock-radius': '0px', '--vd-window-radius': '6px' },
    },
  },
  {
    id: 'menubar-dock',
    name: 'Menu bar and dock',
    familiar: 'Matches macOS habits',
    summary: 'Large icons in a centred floating dock at the bottom, window controls on the left of each titlebar, and everything else in the top bar.',
    source: 'builtin',
    layout: {
      dock: {
        desktop: {
          edge: 'bottom', size: 'large', style: 'floating', align: 'center',
          autohide: false, launcher: true, assistant: true, drawer: false,
          items: ['home', 'lilmail', 'vulos-calendar', 'vulos-contacts', 'messages', 'files', 'terminal', 'persona'],
        },
        mobile: {
          edge: 'bottom', size: 'large', style: 'bar', align: 'center',
          autohide: false, launcher: false, assistant: true, drawer: true,
          items: ['home', 'lilmail', 'vulos-calendar', 'messages'],
        },
      },
      windowControls: 'left',
      tokens: { '--vd-dock-radius': '22px' },
    },
  },
]

/**
 * The pack-authored preset. Parsed at module load through the PUBLIC validator.
 *
 * A throw here is a build/test failure, never a runtime surprise for a user:
 * the manifest ships in the bundle, so if it is invalid, every test that
 * imports this module fails immediately and loudly.
 */
function loadPackPreset(): LayoutPreset {
  const result = validatePack(sideDockPack, 'packs/side-dock.pack.json')
  if (!result.ok) {
    throw new Error(`built-in pack failed its own public validator:\n  ${result.errors.join('\n  ')}`)
  }
  return result.value
}

export const LAYOUT_PRESETS: readonly LayoutPreset[] = Object.freeze([
  ...BUILTIN,
  loadPackPreset(),
])

export function getPreset(id: string): LayoutPreset | null {
  return LAYOUT_PRESETS.find((p) => p.id === id) ?? null
}

/** The stock layout. Structurally cloned so no caller can mutate the preset table. */
export function stockLayout(): DesktopLayout {
  return presetLayout(DEFAULT_PRESET_ID)
}

export function presetLayout(id: string): DesktopLayout {
  const preset = getPreset(id) ?? getPreset(DEFAULT_PRESET_ID)!
  return {
    presetId: preset.id,
    dock: {
      desktop: { ...preset.layout.dock.desktop, items: [...preset.layout.dock.desktop.items] },
      mobile: { ...preset.layout.dock.mobile, items: [...preset.layout.dock.mobile.items] },
    },
    windowControls: preset.layout.windowControls,
    tokens: { ...preset.layout.tokens },
  }
}

/**
 * A preview-safe, one-line description for a picker.
 *
 * "Preview-safe" means: authored strings only, no user or pack HTML, nothing
 * that needs escaping, short enough to render in a card at 390px. Onboarding
 * can print this verbatim.
 */
export function describePreset(preset: LayoutPreset): string {
  return `${preset.summary} ${preset.familiar}.`
}

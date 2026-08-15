/**
 * The public API of the Vulos desktop customization model.
 *
 * Everything else in the shell — the Dock, Settings, the first-boot appearance
 * step in the setup wizard, and the mobile shell — imports from HERE. The
 * internals (store.ts, validate.ts) are free to change; this surface is the
 * contract, and roadmap/CUSTOMIZATION.md documents it.
 *
 * Onboarding wiring, in three calls:
 *
 *   import { LAYOUT_PRESETS, describePreset, applyPreset } from '../desktop'
 *
 *   LAYOUT_PRESETS.map(p => <PresetCard key={p.id} … />)   // the choices
 *   describePreset(p)                                       // preview-safe copy
 *   applyPreset(p.id)                                       // commit the choice
 */

export type {
  DesktopLayout, DockAlign, DockEdge, DockProfile, DockSize, DockStyle,
  FormFactor, LayoutPack, LayoutPreset, TokenRule, ValidationResult, WindowControlsSide,
} from './types'

export {
  ALLOWED_TOKENS, DESKTOP_MAX_ITEMS, DOCK_ALIGNS, DOCK_EDGES, DOCK_SIZES, DOCK_STYLES,
  FORM_FACTORS, MOBILE_EDGES, MOBILE_MAX_ITEMS, MOBILE_SIZES, PACK_FORMAT, PACK_VERSION,
  TOKEN_ALLOWLIST,
  WINDOW_CONTROL_SIDES,
} from './types'

export { validateDockProfile, validateLayout, validatePack, validateTokenValue, validateTokens } from './validate'

export { DEFAULT_PRESET_ID, LAYOUT_PRESETS, describePreset, getPreset, presetLayout, stockLayout } from './presets'

export {
  RESET_QUERY_PARAM, RESET_QUERY_VALUE,
  activeFormFactor, allPresets, applyPreset, findPreset, getDockProfile, getLayout,
  initDesktopLayout, installPack, installedPackList, isStock, resetToStock, setTokens,
  setWindowControls, subscribeLayout, uninstallPack, updateDock,
} from './store'

export { useDesktopLayout, useDockProfile } from './useDesktopLayout'

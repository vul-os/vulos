/**
 * The desktop-layout store: persistence, DOM application, and revert.
 *
 * A module-level external store rather than a React context, deliberately. The
 * shell's provider tree lives in App.tsx, which this pass does not own, and a
 * customization system that can only work if someone remembers to mount a
 * provider is a customization system that silently does nothing. Consumers
 * subscribe with useSyncExternalStore (see useDesktopLayout.ts).
 *
 * # Validation is a boundary, not a step
 *
 * Every read from localStorage goes through validateLayout()/validateTokens()
 * before anything touches the DOM. Editing the storage key by hand gets you a
 * fallback to stock, not a bypass. That is the property the e2e hostile-pack
 * test asserts, and it is why the check lives here rather than in the Settings
 * UI that writes it.
 *
 * # Per-form-factor persistence
 *
 * A phone dock is a different dock, so the two profiles persist under two keys.
 * Rearranging the desktop dock writes only KEY_DOCK.desktop; a user's phone
 * arrangement is never collateral damage.
 *
 * `activeFormFactor()` CALLS shell/useViewport.ts's resolver rather than
 * reproducing it. It used to reproduce it, under a comment claiming the two
 * mirrored each other exactly — and they did not: that file also treats a
 * coarse-pointer, hover-less viewport up to 1024px as mobile, so between 768
 * and 1024 the touch shell was up while this store answered `desktop`. An iPad
 * in portrait ran MobileStack and was handed the desktop dock's twelve-item,
 * small-tile geometry.
 *
 * The comment was the defect, not the width: two rules that agree by assertion
 * drift the first time either is edited, and one of them had already been.
 *
 * # Revert
 *
 * resetToStock() clears every key and re-applies the Vulos preset. It is reachable
 * three ways, because the failure mode to design against is "the layout I chose
 * made the revert control hard to find":
 *   1. Settings → Appearance → Desktop layout → "Reset to stock layout".
 *   2. The Ctrl+Alt+Shift+Backspace hotkey, installed here at module load so it
 *      works from anywhere in the shell, including with an autohidden dock on an
 *      edge the user cannot locate.
 *   3. Loading the shell with ?desktop-layout=stock in the URL, for the case
 *      where the keyboard is not available (a kiosk, a touch-only box).
 * None of the three depend on a `--vd-*` token, so nothing a preset or pack can
 * set can affect how they are drawn.
 */

import {
  ALLOWED_TOKENS, type DesktopLayout, type DockProfile, type FormFactor, type LayoutPreset,
} from './types'
import { validateLayout, validatePack, validateTokens } from './validate'
import { DEFAULT_PRESET_ID, LAYOUT_PRESETS, getPreset, presetLayout, stockLayout } from './presets'
import { resolveViewportLayout } from '../shell/viewportRule'

const KEY_LAYOUT = 'vulos.desktop.layout'
const KEY_PACKS = 'vulos.desktop.packs'
/** Mirrors shell/useViewport.ts MOBILE_BREAKPOINT. Kept in lockstep on purpose. */
export const MOBILE_BREAKPOINT = 768

export const RESET_QUERY_PARAM = 'desktop-layout'
export const RESET_QUERY_VALUE = 'stock'

function readRaw(key: string): unknown {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return null
    return JSON.parse(raw)
  } catch {
    return null
  }
}

function writeRaw(key: string, value: unknown): void {
  try { localStorage.setItem(key, JSON.stringify(value)) } catch { /* private mode */ }
}

function removeRaw(key: string): void {
  try { localStorage.removeItem(key) } catch { /* private mode */ }
}

/* ── State ────────────────────────────────────────────────────────────────── */

let current: DesktopLayout = stockLayout()
let installedPacks: LayoutPreset[] = []
const listeners = new Set<() => void>()
let version = 0

function emit(): void {
  version++
  for (const fn of listeners) fn()
}

export function subscribeLayout(fn: () => void): () => void {
  listeners.add(fn)
  return () => { listeners.delete(fn) }
}

/** Monotonic snapshot token for useSyncExternalStore. */
export function getLayoutVersion(): number {
  return version
}

export function getLayout(): DesktopLayout {
  return current
}

/** Every preset a picker may offer: the built-ins plus anything installed. */
export function allPresets(): readonly LayoutPreset[] {
  return [...LAYOUT_PRESETS, ...installedPacks]
}

export function findPreset(id: string): LayoutPreset | null {
  return allPresets().find((p) => p.id === id) ?? null
}

/* ── Form factor ──────────────────────────────────────────────────────────── */

export function activeFormFactor(): FormFactor {
  // Delegated, not duplicated. This used to be `innerWidth < MOBILE_BREAKPOINT`
  // under a comment saying it mirrored useViewport.ts exactly — but that file
  // also treats a coarse-pointer tablet up to 1024px as mobile, so an iPad in
  // portrait ran the touch shell while this said `desktop` and handed it the
  // desktop dock's twelve-item, small-tile geometry.
  return resolveViewportLayout()
}

/**
 * The dock profile for a form factor — the API the mobile shell consumes.
 *
 * Defaults to whichever form factor the viewport currently is, so a caller that
 * does not care simply gets the right one.
 */
export function getDockProfile(formFactor: FormFactor = activeFormFactor()): DockProfile {
  return current.dock[formFactor]
}

/* ── DOM application ──────────────────────────────────────────────────────── */

/**
 * Stamp the layout onto <html>.
 *
 * Attributes (not classes) so CSS can select on them and a test can read them
 * back without guessing at a class-name convention. Only ALLOWED_TOKENS are ever
 * written as custom properties, and any allowlisted property NOT in the new set
 * is removed — so switching presets cannot leave a stale value behind. There is
 * no code path here that writes cssText, injects a <style> element, or accepts a
 * property name from data.
 */
function applyToDom(layout: DesktopLayout): void {
  if (typeof document === 'undefined') return
  const el = document.documentElement
  const dock = layout.dock[activeFormFactor()]
  el.setAttribute('data-dock-edge', dock.edge)
  el.setAttribute('data-dock-size', dock.size)
  el.setAttribute('data-dock-style', dock.style)
  el.setAttribute('data-dock-align', dock.align)
  el.setAttribute('data-dock-autohide', dock.autohide ? 'on' : 'off')
  el.setAttribute('data-window-controls', layout.windowControls)
  el.setAttribute('data-desktop-preset', layout.presetId)
  for (const name of ALLOWED_TOKENS) {
    const value = layout.tokens[name]
    if (value) el.style.setProperty(name, value)
    else el.style.removeProperty(name)
  }
}

/* ── Load / save ──────────────────────────────────────────────────────────── */

function loadPacks(): LayoutPreset[] {
  const raw = readRaw(KEY_PACKS)
  if (!Array.isArray(raw)) return []
  const out: LayoutPreset[] = []
  for (const entry of raw) {
    const r = validatePack(entry, 'installed pack')
    // A pack that no longer validates (tampered, or written by an older/newer
    // format) is DROPPED, not repaired. Silently "fixing" attacker-controlled
    // data is how a validator becomes a parser for a format nobody documented.
    if (r.ok && !LAYOUT_PRESETS.some((p) => p.id === r.value.id)) out.push(r.value)
  }
  return out
}

/**
 * Read persisted state, validate it, and apply it.
 *
 * Idempotent and safe to call more than once — the shell calls it from the
 * store's own module init, and tests call it directly.
 */
export function initDesktopLayout(): DesktopLayout {
  if (typeof window !== 'undefined' && wantsStockFromUrl()) {
    return resetToStock()
  }
  installedPacks = loadPacks()
  const raw = readRaw(KEY_LAYOUT)
  const result = validateLayout(raw)
  current = result.ok ? result.value : stockLayout()
  applyToDom(current)
  emit()
  return current
}

function persist(layout: DesktopLayout): void {
  writeRaw(KEY_LAYOUT, layout)
}

/** Validate, persist, apply, notify. The only mutation path in this module. */
function commit(next: DesktopLayout): DesktopLayout {
  const result = validateLayout(next)
  if (!result.ok) {
    // Reaching here means a caller inside the app built an invalid layout. Do
    // not write it and do not apply it — keep whatever was already valid.
    if (typeof console !== 'undefined') console.error('[desktop] refusing an invalid layout:', result.errors)
    return current
  }
  current = result.value
  persist(current)
  applyToDom(current)
  emit()
  return current
}

/* ── Public mutations ─────────────────────────────────────────────────────── */

/** Apply a preset wholesale. This is what the first-boot picker calls. */
export function applyPreset(id: string): DesktopLayout {
  const preset = findPreset(id)
  if (!preset) return current
  return commit({
    presetId: preset.id,
    dock: {
      desktop: { ...preset.layout.dock.desktop, items: [...preset.layout.dock.desktop.items] },
      mobile: { ...preset.layout.dock.mobile, items: [...preset.layout.dock.mobile.items] },
    },
    windowControls: preset.layout.windowControls,
    tokens: { ...preset.layout.tokens },
  })
}

/**
 * Change part of ONE form factor's dock. The other form factor is copied
 * through untouched — this is the "your phone arrangement is not collateral
 * damage" guarantee, and it is a single object spread rather than a merge
 * strategy so it cannot drift.
 */
export function updateDock(formFactor: FormFactor, patch: Partial<DockProfile>): DesktopLayout {
  return commit({
    ...current,
    dock: {
      ...current.dock,
      [formFactor]: { ...current.dock[formFactor], ...patch },
    } as Record<FormFactor, DockProfile>,
  })
}

export function setWindowControls(side: DesktopLayout['windowControls']): DesktopLayout {
  return commit({ ...current, windowControls: side })
}

/** Set allowlisted custom properties. Rejects the whole patch if any value fails. */
export function setTokens(patch: Record<string, string>): DesktopLayout {
  const result = validateTokens(patch)
  if (!result.ok) {
    if (typeof console !== 'undefined') console.error('[desktop] rejected tokens:', result.errors)
    return current
  }
  return commit({ ...current, tokens: { ...current.tokens, ...result.value } })
}

/**
 * Install a third-party pack. Returns the validation errors on rejection so the
 * installer UI can show the developer exactly what is wrong.
 */
export function installPack(manifest: unknown): { ok: boolean; errors: string[] } {
  const result = validatePack(manifest)
  if (!result.ok) return { ok: false, errors: result.errors }
  if (LAYOUT_PRESETS.some((p) => p.id === result.value.id)) {
    return { ok: false, errors: [`pack.id: "${result.value.id}" collides with a built-in preset`] }
  }
  installedPacks = [...installedPacks.filter((p) => p.id !== result.value.id), result.value]
  writeRaw(KEY_PACKS, installedPacks)
  emit()
  return { ok: true, errors: [] }
}

/** Remove an installed pack. If it is the active layout, fall back to stock. */
export function uninstallPack(id: string): void {
  installedPacks = installedPacks.filter((p) => p.id !== id)
  writeRaw(KEY_PACKS, installedPacks)
  if (current.presetId === id) resetToStock()
  else emit()
}

export function installedPackList(): readonly LayoutPreset[] {
  return installedPacks
}

/**
 * The one-action revert. Always works: it does not read current state, cannot
 * fail validation (stockLayout() is a built-in), and removes the persisted keys
 * rather than overwriting them, so a corrupt value cannot survive it.
 */
export function resetToStock(): DesktopLayout {
  removeRaw(KEY_LAYOUT)
  current = stockLayout()
  applyToDom(current)
  emit()
  return current
}

export function isStock(): boolean {
  return current.presetId === DEFAULT_PRESET_ID
    && Object.keys(current.tokens).length === 0
    && JSON.stringify(current) === JSON.stringify(stockLayout())
}

/* ── Escape hatches ───────────────────────────────────────────────────────── */

function wantsStockFromUrl(): boolean {
  try {
    return new URLSearchParams(window.location.search).get(RESET_QUERY_PARAM) === RESET_QUERY_VALUE
  } catch {
    return false
  }
}

/**
 * Ctrl+Alt+Shift+Backspace → back to stock, from anywhere.
 *
 * Bound at module load rather than from a component, because the case it exists
 * for is "the user cannot find, see or click the control that would undo this".
 * A component-mounted listener is exactly the thing a bad layout could make
 * unreachable.
 *
 * The chord is four keys deep and includes Backspace precisely so it cannot be
 * hit while typing; it is documented in roadmap/CUSTOMIZATION.md.
 */
function onKeydown(e: KeyboardEvent): void {
  if (e.ctrlKey && e.altKey && e.shiftKey && e.key === 'Backspace') {
    e.preventDefault()
    resetToStock()
  }
}

let hatchInstalled = false
export function installRevertHatch(): void {
  if (hatchInstalled || typeof window === 'undefined') return
  hatchInstalled = true
  window.addEventListener('keydown', onKeydown, true)
}

/**
 * Re-stamp the DOM when the viewport crosses the mobile breakpoint, so the
 * ACTIVE form factor's dock profile is the one in effect. Without this a window
 * dragged from 1200px to 600px would keep the desktop dock geometry while the
 * mobile shell renders.
 */
let breakpointWatched = false
function watchBreakpoint(): void {
  if (breakpointWatched || typeof window === 'undefined' || !window.matchMedia) return
  breakpointWatched = true
  const mq = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`)
  const handler = () => { applyToDom(current); emit() }
  mq.addEventListener('change', handler)
}

if (typeof window !== 'undefined') {
  initDesktopLayout()
  installRevertHatch()
  watchBreakpoint()
}

/** Test seam: forget everything and re-read from storage. */
export function __resetStoreForTests(): void {
  installedPacks = []
  current = stockLayout()
  initDesktopLayout()
}

export { DEFAULT_PRESET_ID, getPreset, presetLayout, stockLayout }

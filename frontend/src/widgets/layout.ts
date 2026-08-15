// layout.ts — which widgets are in the rail, in what order, at what size, with
// which permissions granted.
//
// Persisted to localStorage under one key, following the shell's established
// preference convention (try/catch around every access; a private-mode browser
// that throws on localStorage must still boot a working desktop).
//
// Everything read back out is re-validated against the live registry. A layout
// is long-lived user state and the registry is build-time code, so they drift:
// a widget gets removed from the OS, a widget drops a size it used to offer, a
// widget's manifest stops requesting a permission the user once granted. Each of
// those must degrade to "that entry is gone/clamped", never to a crash and never
// to a stale permission grant surviving the permission's removal.

import { getWidget } from './registry'
import { normalizeSettings } from './manifest'
import { widgetClear } from './storage'
import {
  WIDGET_PERMISSIONS,
  type WidgetInstance, type WidgetLayout, type WidgetPermission,
  type WidgetSettingValue, type WidgetSize,
} from './types'

const LS_KEY = 'vulos.widgets.layout.v1'
const MAX_INSTANCES = 24

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x)
}

/**
 * Mint an instance id. Lowercase hex so it satisfies storage.ts's id shape —
 * the two modules agree on the alphabet, and storage.ts re-checks rather than
 * assuming, because ids also arrive there from the sandbox bridge.
 */
export function newInstanceId(): string {
  try {
    const b = new Uint8Array(8)
    globalThis.crypto.getRandomValues(b)
    return 'w' + [...b].map((x) => x.toString(16).padStart(2, '0')).join('')
  } catch {
    return 'w' + Math.random().toString(16).slice(2, 18).padEnd(16, '0')
  }
}

/**
 * Reconcile one stored instance against the registry. Returns null when the
 * entry can no longer be rendered (unknown widget id, malformed record).
 */
export function reconcileInstance(raw: unknown): WidgetInstance | null {
  if (!isRecord(raw)) return null
  const widgetId = raw.widgetId
  const instanceId = raw.instanceId
  if (typeof widgetId !== 'string' || typeof instanceId !== 'string') return null
  const def = getWidget(widgetId)
  if (!def) return null // widget no longer ships — drop the placement
  const m = def.manifest

  // Size: clamp to something the manifest still offers. A widget that dropped
  // 'large' between releases must not render at a size it no longer lays out for.
  const size: WidgetSize = (typeof raw.size === 'string' && m.sizes.includes(raw.size as WidgetSize))
    ? raw.size as WidgetSize
    : m.sizes[0]

  // Grants: intersect the stored grants with what the manifest CURRENTLY
  // requests. A permission the widget no longer asks for is revoked by that
  // fact alone — a stored grant must never outlive the request that justified
  // it, or a widget could shed a scary permission to get installed and get it
  // back in the next update without ever re-prompting.
  const requested = new Set(m.permissions ?? [])
  const granted = Array.isArray(raw.granted)
    ? raw.granted.filter((p): p is WidgetPermission =>
        typeof p === 'string'
        && WIDGET_PERMISSIONS.includes(p as WidgetPermission)
        && requested.has(p as WidgetPermission))
    : []

  return {
    instanceId,
    widgetId,
    size,
    settings: normalizeSettings(m.settings, raw.settings),
    granted: [...new Set(granted)],
  }
}

/** Parse a persisted layout. Never throws; a corrupt blob yields an empty layout. */
export function parseLayout(raw: unknown): WidgetLayout | null {
  if (!isRecord(raw)) return null
  if (raw.version !== 1) return null
  if (!Array.isArray(raw.instances)) return null
  const instances: WidgetInstance[] = []
  const seen = new Set<string>()
  for (const r of raw.instances.slice(0, MAX_INSTANCES)) {
    const inst = reconcileInstance(r)
    if (!inst) continue
    if (seen.has(inst.instanceId)) continue // duplicate id would break React keys
    seen.add(inst.instanceId)
    instances.push(inst)
  }
  return { version: 1, instances }
}

/**
 * The rail a box shows before the user has arranged anything.
 *
 * Chosen so a fresh desktop is USEFUL and TRUTHFUL on a box with no accounts
 * configured and no network: a clock, the world clocks the founder asked for,
 * box health, the agenda, and notifications. Nothing here makes an outbound
 * request, and nothing here shows an empty frame — every one of these has real
 * local state or an honest empty.
 */
// A NOTE ON THE GRANTS BELOW. Widgets the OS ships in its own default rail come
// with the permissions their manifest requests already granted; widgets the USER
// adds from the gallery start with every permission denied (see WidgetRail's
// 'add' case). The line is deliberate: the box's own widgets are covered by the
// decision to install the OS, and a fresh desktop whose shipped widgets all read
// "Allow …" looks broken rather than careful. Anything from outside that
// decision asks. Every grant here is still intersected with what the manifest
// requests, and every one of them is revocable per placement.
export function defaultLayout(): WidgetLayout {
  const want: { id: string; size: WidgetSize; granted?: WidgetPermission[] }[] = [
    { id: 'vulos.clock', size: 'medium' },
    { id: 'vulos.worldclock', size: 'large' },
    // MEDIUM, not large. Collapsed, the agenda is a date header and one event —
    // at 'large' that sat in the top third of the tile with a void beneath it,
    // and the widget expands on click anyway.
    { id: 'vulos.agenda', size: 'medium', granted: ['calendar', 'launch'] },
    { id: 'vulos.pulse', size: 'medium', granted: ['telemetry'] },
    { id: 'vulos.notifications', size: 'medium', granted: ['notifications'] },
  ]
  const instances: WidgetInstance[] = []
  for (const w of want) {
    const def = getWidget(w.id)
    if (!def) continue
    const requested = new Set(def.manifest.permissions ?? [])
    instances.push({
      instanceId: newInstanceId(),
      widgetId: w.id,
      size: def.manifest.sizes.includes(w.size) ? w.size : def.manifest.sizes[0],
      settings: normalizeSettings(def.manifest.settings, {}),
      // Even the defaults are intersected with what the manifest requests, so
      // this list cannot grant something the widget didn't ask for.
      granted: (w.granted ?? []).filter((p) => requested.has(p)),
    })
  }
  return { version: 1, instances }
}

export function loadLayout(): WidgetLayout {
  let raw: string | null = null
  try { raw = localStorage.getItem(LS_KEY) } catch { /* private mode */ }
  if (!raw) return defaultLayout()
  let parsed: unknown = null
  try { parsed = JSON.parse(raw) } catch { return defaultLayout() }
  const layout = parseLayout(parsed)
  // A layout that parsed to nothing is NOT silently replaced by the defaults:
  // the user may have deliberately emptied the rail, and re-adding five widgets
  // every boot would be the OS overruling them. Only an unparseable blob falls
  // back.
  return layout ?? defaultLayout()
}

export function saveLayout(layout: WidgetLayout): void {
  try { localStorage.setItem(LS_KEY, JSON.stringify(layout)) } catch { /* private mode */ }
}

// ── Pure layout operations ───────────────────────────────────────────────────
// Each returns a NEW layout. They are pure so the reducer in the host is trivial
// and so every one of them is directly testable without a DOM.

export function addWidget(
  layout: WidgetLayout,
  widgetId: string,
  opts: { size?: WidgetSize; granted?: WidgetPermission[] } = {},
): WidgetLayout {
  const def = getWidget(widgetId)
  if (!def) return layout
  if (layout.instances.length >= MAX_INSTANCES) return layout
  const m = def.manifest
  const requested = new Set(m.permissions ?? [])
  const inst: WidgetInstance = {
    instanceId: newInstanceId(),
    widgetId,
    size: opts.size && m.sizes.includes(opts.size) ? opts.size : m.sizes[0],
    settings: normalizeSettings(m.settings, {}),
    granted: (opts.granted ?? []).filter((p) => requested.has(p)),
  }
  return { ...layout, instances: [...layout.instances, inst] }
}

/**
 * Remove a placement AND its stored data. Leaving the keys behind would mean a
 * user who removed a widget to get rid of it still has its data on the box, and
 * a re-add would silently resurrect it under a new instance id it can't reach —
 * i.e. garbage that accumulates forever.
 */
export function removeWidget(layout: WidgetLayout, instanceId: string): WidgetLayout {
  if (!layout.instances.some((i) => i.instanceId === instanceId)) return layout
  widgetClear(instanceId)
  return { ...layout, instances: layout.instances.filter((i) => i.instanceId !== instanceId) }
}

export function moveWidget(layout: WidgetLayout, instanceId: string, delta: number): WidgetLayout {
  const idx = layout.instances.findIndex((i) => i.instanceId === instanceId)
  if (idx < 0) return layout
  const to = idx + delta
  if (to < 0 || to >= layout.instances.length) return layout
  const next = [...layout.instances]
  const [moved] = next.splice(idx, 1)
  next.splice(to, 0, moved)
  return { ...layout, instances: next }
}

export function resizeWidget(layout: WidgetLayout, instanceId: string, size: WidgetSize): WidgetLayout {
  return mapInstance(layout, instanceId, (inst) => {
    const def = getWidget(inst.widgetId)
    if (!def || !def.manifest.sizes.includes(size)) return inst
    return { ...inst, size }
  })
}

export function setInstanceSetting(
  layout: WidgetLayout,
  instanceId: string,
  key: string,
  value: WidgetSettingValue,
): WidgetLayout {
  return mapInstance(layout, instanceId, (inst) => {
    const def = getWidget(inst.widgetId)
    if (!def) return inst
    // Re-normalise the WHOLE settings object rather than assigning the one key:
    // that runs the value through the manifest's declared type and range, so a
    // widget (or a bad caller) cannot write a string into a number setting or
    // an undeclared key into the context.
    const merged = normalizeSettings(def.manifest.settings, { ...inst.settings, [key]: value })
    return { ...inst, settings: merged }
  })
}

export function setGrants(
  layout: WidgetLayout,
  instanceId: string,
  granted: WidgetPermission[],
): WidgetLayout {
  return mapInstance(layout, instanceId, (inst) => {
    const def = getWidget(inst.widgetId)
    if (!def) return inst
    const requested = new Set(def.manifest.permissions ?? [])
    return { ...inst, granted: [...new Set(granted.filter((p) => requested.has(p)))] }
  })
}

function mapInstance(
  layout: WidgetLayout,
  instanceId: string,
  fn: (inst: WidgetInstance) => WidgetInstance,
): WidgetLayout {
  let changed = false
  const instances = layout.instances.map((i) => {
    if (i.instanceId !== instanceId) return i
    const next = fn(i)
    if (next !== i) changed = true
    return next
  })
  return changed ? { ...layout, instances } : layout
}

export const LAYOUT_STORAGE_KEY = LS_KEY
export const LAYOUT_MAX_INSTANCES = MAX_INSTANCES

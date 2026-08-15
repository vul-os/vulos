// registry.ts — the set of widgets this box knows about.
//
// Two ways in, and the difference between them is the whole security story:
//
//   defineWidget()           in-process React. Runs on the shell's origin with
//                            the shell's DOM. Only for code that ships inside
//                            the OS build and is reviewed with it.
//
//   defineSandboxedWidget()  HTML+JS run inside `<iframe sandbox="allow-scripts">`
//                            — an opaque origin. This is the lane for anything
//                            that did not ship with the OS.
//
// registerWidget() takes either. It is the same call a third-party integration
// makes, and the builtins go through it too, so the public path is the path that
// is actually exercised on every boot rather than a parallel one that rots.

import { assertManifest } from './manifest'
import type {
  AnyWidgetDefinition, SandboxedWidgetDefinition, WidgetContext,
  WidgetDefinition, WidgetManifest,
} from './types'
import type { ReactNode } from 'react'

const registry = new Map<string, AnyWidgetDefinition>()

/**
 * Declare an in-process widget. The manifest is validated HERE, at module
 * evaluation, so a malformed builtin manifest is a boot-time crash in
 * development rather than a widget that silently misbehaves in the rail.
 */
export function defineWidget(def: {
  manifest: WidgetManifest
  render: (ctx: WidgetContext) => ReactNode
}): WidgetDefinition {
  assertManifest(def.manifest)
  if (typeof def.render !== 'function') throw new Error(`widget ${def.manifest.id}: render must be a function`)
  return { manifest: def.manifest, render: def.render }
}

/**
 * Declare a sandboxed widget from a complete HTML document.
 *
 * `source` is delivered via `srcdoc`, never fetched from a URL. That is not a
 * convenience: a URL would be fetched by the browser at the shell's request,
 * which reintroduces exactly the outbound call net.ts exists to prevent, and it
 * would make the widget's code change under the user without the OS noticing.
 * A widget's code is data the box already holds.
 */
export function defineSandboxedWidget(def: {
  manifest: WidgetManifest
  source: string
}): SandboxedWidgetDefinition {
  assertManifest(def.manifest)
  if (typeof def.source !== 'string' || def.source.trim() === '') {
    throw new Error(`widget ${def.manifest.id}: source must be a non-empty HTML document`)
  }
  return { manifest: def.manifest, source: def.source, sandboxed: true }
}

/**
 * Add a widget to the box's catalogue.
 *
 * Re-registering the same id REPLACES it, and does so quietly on purpose: the
 * dev server hot-reloads these modules, and a throwing duplicate check turns
 * every save into a white screen. Registration order is preserved for the
 * gallery so the catalogue does not reshuffle between boots.
 */
export function registerWidget(def: AnyWidgetDefinition): AnyWidgetDefinition {
  assertManifest(def.manifest)
  registry.set(def.manifest.id, def)
  return def
}

export function getWidget(id: string): AnyWidgetDefinition | null {
  return registry.get(id) ?? null
}

export function listWidgets(): AnyWidgetDefinition[] {
  return [...registry.values()]
}

export function widgetCount(): number {
  return registry.size
}

/** Test seam only. Never called by the shell. */
export function clearRegistry(): void {
  registry.clear()
}

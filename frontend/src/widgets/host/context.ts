// context.ts — building the object a widget is handed, and the single place
// where a permission grant becomes a real thing.
//
// WHY THIS IS A FUNCTION AND NOT A LITERAL INSIDE THE RAIL'S JSX
//
// A permission model is only worth the runtime behaviour behind it. Vulos already
// has a cautionary example: the app manifest's `permissions` array is validated
// against a list of valid strings and then, for almost all of them, does nothing
// at all — an app that declares `camera` is not thereby granted or denied
// anything, because no code reads the declaration. The string is documentation
// wearing the costume of a control.
//
// So every grant this widget API makes has to be enforced HERE, in code, and has
// to be assertable without mounting a desktop. That is the whole reason this is a
// pure function: `buildContext` is the one place a `granted` array turns into a
// capability or a null, and `__tests__/permissions.test.ts` drives it directly.
// Buried in the rail's JSX the same logic would be reachable only through a
// rendered component, which in practice means untested, which in practice means
// a `granted.includes(…)` could be deleted and nothing would notice.
//
// THE RULE: a permission that is not granted yields `null` — not an object that
// throws, not an empty object, not a no-op stub. A widget must be able to see
// that it does not have something.

import { storageFor } from '../storage'
import { widgetNet } from '../net'
import type {
  WidgetCalendar, WidgetContext, WidgetInstance, WidgetManifest,
  WidgetNotifications, WidgetSettingValue, WidgetTelemetry,
} from '../types'

export interface BuildContextOptions {
  manifest: WidgetManifest
  instance: WidgetInstance
  /** The host clock value for this widget's declared cadence. */
  now: Date
  reducedMotion: boolean
  /** Shared seam data. Passed in whether or not it is granted; gating happens here. */
  telemetry: WidgetTelemetry
  calendar: WidgetCalendar
  notifications: WidgetNotifications
  setSetting: (key: string, value: WidgetSettingValue) => void
  notify: (title: string, body?: string) => void
  openApp: (appId: string) => void
}

export function buildContext(o: BuildContextOptions): WidgetContext {
  const granted = o.instance.granted
  const has = (p: string) => granted.includes(p as never)

  return {
    size: o.instance.size,
    now: o.now,
    settings: o.instance.settings,
    setSetting: o.setSetting,
    reducedMotion: o.reducedMotion,

    // Each of these is the enforcement. There is no other check downstream for
    // the in-process lane — a widget that gets a non-null value here can use it.
    storage: has('storage') ? storageFor(o.instance.instanceId) : null,

    // widgetNet ALSO returns null without a host allowlist or a box broker, so
    // the grant is necessary but not sufficient. That ordering matters: a user
    // who grants `network` on a box with no broker has still granted nothing
    // reachable, and the widget sees exactly the same `null` it would see if
    // they had refused.
    net: widgetNet(o.manifest.hosts ?? [], { granted: has('network') }),

    telemetry: has('telemetry') ? o.telemetry : null,
    calendar: has('calendar') ? o.calendar : null,
    notifications: has('notifications') ? o.notifications : null,

    // `notify` (post an alert) and `notifications` (read the recent list) are
    // deliberately separate permissions and separate fields. A widget that only
    // wants to display a count has no business being able to fabricate one.
    notify: has('notify') ? o.notify : null,
    openApp: has('launch') ? o.openApp : null,
  }
}

/**
 * Which shared seams the rail must actually OPEN for a set of mounted widgets.
 *
 * The second half of enforcement, and the half a per-widget null cannot give
 * you: if no mounted widget was granted `telemetry`, the rail must not open the
 * telemetry socket at all. Handing a widget `null` while still holding the
 * socket open would satisfy the type and miss the point — the user denied the
 * permission partly so the box would stop doing the work.
 */
export function seamsNeeded(instances: WidgetInstance[]): {
  telemetry: boolean
  calendar: boolean
  notifications: boolean
} {
  return {
    telemetry: instances.some((i) => i.granted.includes('telemetry')),
    calendar: instances.some((i) => i.granted.includes('calendar')),
    notifications: instances.some((i) => i.granted.includes('notifications')),
  }
}

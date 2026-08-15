// types.ts — the public contract between a widget and the OS.
//
// This file is the API. Everything a widget author writes is typed by something
// declared here, and everything the host is allowed to hand a widget appears
// here too — so the surface a widget can reach is exactly as large as this file
// and no larger. That is deliberate: on a sovereignty box the desktop is trusted
// screen real estate, and a widget is the smallest, most numerous, most casually
// installed thing that gets to sit on it.
//
// Read WIDGETS.md (docs/) for the developer-facing version of all of this.

import type { ReactNode } from 'react'

// ── Geometry ─────────────────────────────────────────────────────────────────
//
// Three sizes, not free-form pixels. A widget declares which of them it can
// render at and the USER picks among those; a widget can never demand a size, and
// it is never told a pixel dimension, because the rail's column width changes
// with the viewport and a widget that laid itself out in pixels would be the one
// thing that could break the grid for every other widget.
//
//   small   1 column × 1 row   — one number, one glyph, one word
//   medium  2 columns × 1 row  — a line of detail, a sparkline, 2-3 rows of text
//   large   2 columns × 2 rows — a list, a chart, a set of tiles
export type WidgetSize = 'small' | 'medium' | 'large'

export const WIDGET_SIZES: readonly WidgetSize[] = ['small', 'medium', 'large']

/** Grid footprint of each size, in rail columns/rows. Host-owned, not negotiable. */
export const SIZE_SPAN: Record<WidgetSize, { cols: number; rows: number }> = {
  small: { cols: 1, rows: 1 },
  medium: { cols: 2, rows: 1 },
  large: { cols: 2, rows: 2 },
}

// ── Permissions ──────────────────────────────────────────────────────────────
//
// DEFAULT DENY, EVERY ONE. A manifest REQUESTS permissions; the host GRANTS
// them, and until a grant exists the corresponding field of WidgetContext is
// null. There is no ambient authority anywhere in the context object: a widget
// that forgot to request `storage` does not get a degraded storage object, it
// gets `null` and has to cope.
//
// The list is closed. A widget cannot invent a permission string, and an
// unrecognised one fails manifest validation rather than being ignored — an
// ignored permission is a permission that gets silently granted the day someone
// adds it to the enum.
export type WidgetPermission =
  /** Namespaced key/value storage, quota'd. Cannot see another widget's keys. */
  | 'storage'
  /** Outbound HTTP, proxied by the box. See WidgetNet — this is the big one. */
  | 'network'
  /** Post a notification into the shell's notification centre. Rate-limited. */
  | 'notify'
  /** Read the most recent notifications. Separate from `notify` — reading what
   *  the box has told the user and being able to tell them something are
   *  different powers, and a widget that only wants to display a count has no
   *  business being able to fabricate an alert. */
  | 'notifications'
  /** Ask the shell to open an app. Cannot navigate, cannot open arbitrary URLs. */
  | 'launch'
  /** Read the box's own live system telemetry (CPU/memory/uptime). Local only. */
  | 'telemetry'
  /** Read the user's calendar agenda through the box's PIM seam. */
  | 'calendar'

export const WIDGET_PERMISSIONS: readonly WidgetPermission[] = [
  'storage', 'network', 'notify', 'notifications', 'launch', 'telemetry', 'calendar',
]

/**
 * What each permission means in one sentence, and whether granting it can leak
 * anything off the box. Rendered verbatim in the permission prompt — the user
 * sees this text, so it is part of the API, not a comment.
 */
export const PERMISSION_INFO: Record<WidgetPermission, { title: string; detail: string; leaves: boolean }> = {
  storage: {
    title: 'Store its own settings',
    detail: 'Keeps a small amount of data on this box, in its own namespace. It cannot read any other widget’s data.',
    leaves: false,
  },
  network: {
    title: 'Reach the internet',
    detail: 'Requests are made by your box, only to hosts you approve, and every request is listed here. Whatever this widget asks for, that host learns about you.',
    leaves: true,
  },
  notify: {
    title: 'Send you notifications',
    detail: 'Posts into your notification centre. Rate-limited by the OS.',
    leaves: false,
  },
  notifications: {
    title: 'Read your notifications',
    detail: 'Sees the most recent notifications and the unread count. Cannot dismiss them or change your preferences.',
    leaves: false,
  },
  launch: {
    title: 'Open apps',
    detail: 'Can ask the shell to open an app you already have. It cannot open arbitrary links or navigate the desktop.',
    leaves: false,
  },
  telemetry: {
    title: 'Read box health',
    detail: 'CPU, memory and uptime of this box. Never leaves the box unless the widget also has network access.',
    leaves: false,
  },
  calendar: {
    title: 'Read your agenda',
    detail: 'Upcoming events from your calendar. Never leaves the box unless the widget also has network access.',
    leaves: false,
  },
}

// ── Settings ─────────────────────────────────────────────────────────────────
//
// A widget describes its settings declaratively and the HOST renders the form.
// The widget never draws its own settings UI, which is what stops a widget from
// painting a convincing "Vulos wants your password" panel inside the rail. The
// user is always editing an OS-drawn form.
export type WidgetSettingSpec =
  | { key: string; type: 'string'; label: string; help?: string; default?: string; maxLength?: number; placeholder?: string }
  | { key: string; type: 'number'; label: string; help?: string; default?: number; min?: number; max?: number }
  | { key: string; type: 'boolean'; label: string; help?: string; default?: boolean }
  | { key: string; type: 'select'; label: string; help?: string; default?: string; options: { value: string; label: string }[] }
  /** A repeated free-text list (world-clock zones, stock tickers, …). */
  | { key: string; type: 'list'; label: string; help?: string; default?: string[]; maxItems?: number; placeholder?: string }

export type WidgetSettingValue = string | number | boolean | string[]
export type WidgetSettings = Record<string, WidgetSettingValue>

// ── Refresh ──────────────────────────────────────────────────────────────────
//
// The HOST owns every timer. A widget declares the cadence it wants and is
// re-rendered with a fresh `now`; it never calls setInterval itself.
//
// This is not a style preference. Ten widgets each running their own 1 Hz
// interval is ten timers the OS cannot see, cannot throttle when the rail is
// hidden, and cannot stop when the screen sleeps — and on a box that is meant to
// idle quietly, that is a real power cost. One shared scheduler can align every
// widget to the same tick and stop entirely when nothing is visible.
export type WidgetTick = 'none' | 'second' | 'minute'

// ── Manifest ─────────────────────────────────────────────────────────────────

export interface WidgetManifest {
  /** Stable identity. `[a-z0-9]` with `-` or `.` separators — e.g. `com.acme.tides`. */
  id: string
  /** Shown in the gallery and as the tile's accessible name. */
  name: string
  /** One line, shown under the name in the gallery. */
  description: string
  /** Semver-ish; compared as an opaque string, only surfaced to the user. */
  version: string
  author?: string
  /** Sizes this widget can render. First entry is the default. Non-empty. */
  sizes: WidgetSize[]
  /** Requested permissions. Absent/empty means the widget gets nothing. */
  permissions?: WidgetPermission[]
  /** Re-render cadence the host should drive. Default 'none'. */
  tick?: WidgetTick
  /** Declarative settings; the host renders the form and validates the values. */
  settings?: WidgetSettingSpec[]
  /**
   * Hosts this widget may reach when `network` is granted. REQUIRED if it
   * requests `network`; an empty or missing list with `network` requested is a
   * manifest error rather than "any host". Entries are bare hostnames — no
   * scheme, no path, no wildcards, no `*`.
   */
  hosts?: string[]
}

// ── The context a widget renders against ─────────────────────────────────────

/** Per-widget storage. Synchronous, small, namespaced, quota'd. */
export interface WidgetStorage {
  get(key: string): string | null
  set(key: string, value: string): boolean
  remove(key: string): void
  keys(): string[]
}

/** The result of a proxied request. Deliberately not a `Response`. */
export interface WidgetFetchResult {
  ok: boolean
  status: number
  /** Parsed JSON body, or null when the body was absent/unparseable. */
  data: unknown
  /** Machine-readable failure cause when `ok` is false. */
  error?: 'no-proxy' | 'blocked-host' | 'http' | 'offline' | 'bad-body'
}

/**
 * Outbound HTTP — and note what this is NOT.
 *
 * It is not `fetch`. There is no way to send credentials, set arbitrary headers,
 * read a redirect chain, open a WebSocket, or reach a host outside the
 * manifest's `hosts` list. The widget hands over a URL; the BOX makes the
 * request (see net.ts for why the browser must not) and hands back parsed JSON.
 *
 * Present only when `network` is granted AND the box exposes the proxy. Absent
 * otherwise — a widget must have a working no-network state to be installable.
 */
export interface WidgetNet {
  getJSON(url: string): Promise<WidgetFetchResult>
}

/** Live box telemetry, when `telemetry` is granted. All fields optional — a box may report none. */
export interface WidgetTelemetry {
  connected: boolean
  cpu?: number
  memPercent?: number
  memUsedBytes?: number
  memTotalBytes?: number
  battery?: number
  charging?: boolean
  tempC?: number
  uptime?: string
  hostname?: string
}

/** One agenda entry, when `calendar` is granted. */
export interface WidgetEvent {
  id: string
  title: string
  start: Date | null
  end: Date | null
  allDay: boolean
  location: string
}

export interface WidgetCalendar {
  /** null = still loading; [] = loaded and genuinely empty. */
  events: WidgetEvent[] | null
  error: boolean
}

/** One notification, when `notifications` is granted. Read-only view. */
export interface WidgetNotification {
  id: string
  title: string
  body: string
  read: boolean
}

export interface WidgetNotifications {
  recent: WidgetNotification[]
  unread: number
}

/**
 * Everything a widget is handed. There is no second channel: a widget's render
 * function receives this object and nothing else, and every capability on it is
 * either a plain value or null.
 */
export interface WidgetContext {
  /** The size the user chose, from the manifest's `sizes`. */
  size: WidgetSize
  /** Host-driven clock, at the manifest's `tick`. Stable identity between ticks. */
  now: Date
  /** Validated, defaulted settings. Never contains a key the manifest didn't declare. */
  settings: WidgetSettings
  /** Persist one setting. Rejected silently if the key isn't in the manifest. */
  setSetting(key: string, value: WidgetSettingValue): void
  /** True when the viewer asked for reduced motion — honour it, don't animate. */
  reducedMotion: boolean
  /** Granted capabilities. `null` means NOT GRANTED, not "broken". */
  storage: WidgetStorage | null
  net: WidgetNet | null
  telemetry: WidgetTelemetry | null
  calendar: WidgetCalendar | null
  notifications: WidgetNotifications | null
  notify: ((title: string, body?: string) => void) | null
  openApp: ((appId: string) => void) | null
}

// ── The widget itself ────────────────────────────────────────────────────────

/**
 * An in-process widget: a React render function plus a manifest.
 *
 * TRUST: in-process widgets run ON THE SHELL'S ORIGIN with the shell's DOM. The
 * permission model above is a CLARITY mechanism for them, not a containment one
 * — code in this lane could reach around the context object if it chose to. That
 * is acceptable for widgets that ship inside the OS build and are reviewed with
 * it, and it is NOT acceptable for anything else. Third-party widgets go in the
 * sandboxed lane below, where the boundary is the browser's, not a convention's.
 */
export interface WidgetDefinition {
  manifest: WidgetManifest
  render: (ctx: WidgetContext) => ReactNode
}

/**
 * A sandboxed widget: HTML+JS that the host runs inside
 * `<iframe sandbox="allow-scripts">` — an OPAQUE origin with no access to the
 * shell's DOM, cookies, localStorage, service worker or session. It reaches the
 * capabilities above only by asking over a MessagePort, and the host answers
 * only for permissions the user granted, scoping every answer to the widget
 * identity IT holds rather than any identity the frame claims.
 *
 * This is the lane every widget that did not ship with the OS runs in.
 */
export interface SandboxedWidgetDefinition {
  manifest: WidgetManifest
  /** A complete HTML document. Loaded via srcdoc, so it is never fetched from a URL. */
  source: string
  /** Marks the definition as sandboxed for the host's dispatch. */
  sandboxed: true
}

export type AnyWidgetDefinition = WidgetDefinition | SandboxedWidgetDefinition

export function isSandboxed(def: AnyWidgetDefinition): def is SandboxedWidgetDefinition {
  return (def as SandboxedWidgetDefinition).sandboxed === true
}

// ── Rail layout ──────────────────────────────────────────────────────────────

/** One placed widget. A widget may appear in the rail more than once. */
export interface WidgetInstance {
  /** Unique per placement, so two clocks can hold different settings. */
  instanceId: string
  widgetId: string
  size: WidgetSize
  settings: WidgetSettings
  /** Permissions the user actually granted this placement. */
  granted: WidgetPermission[]
}

export interface WidgetLayout {
  version: 1
  instances: WidgetInstance[]
}

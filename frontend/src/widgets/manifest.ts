// manifest.ts — validation of a widget manifest.
//
// A manifest crosses a trust boundary. Even a first-party one is authored by
// hand and a third-party one is authored by a stranger, so nothing here trusts
// the TypeScript types: every field is re-checked at runtime, because the type
// annotation on a value that came out of JSON is a comment.
//
// The rule this file exists to enforce is that a manifest is either VALID or
// REJECTED — never "mostly valid, with the parts we didn't understand ignored".
// An ignored field is a field that gets silently honoured the day the enum grows,
// and for `permissions` and `hosts` that is a security hole with a delayed fuse.

import {
  WIDGET_SIZES, WIDGET_PERMISSIONS,
  type WidgetManifest, type WidgetPermission, type WidgetSize,
  type WidgetSettingSpec, type WidgetTick, type WidgetSettings, type WidgetSettingValue,
} from './types'

export interface ManifestCheck {
  ok: boolean
  errors: string[]
}

// Widget ids: lowercase alphanumeric segments joined by `-` or `.`. Same shape
// as an app id plus dots for reverse-DNS. No leading/trailing separator, no `..`
// (which would let an id read as a path traversal wherever ids get concatenated
// into a storage key), and a length cap so an id cannot be used to blow a quota.
const ID_RE = /^[a-z0-9]+(?:[-.][a-z0-9]+)*$/
const MAX_ID_LEN = 64

// Hostnames only: labels joined by dots. Explicitly NOT a URL, NOT a pattern.
//
// No wildcard is supported, and that is the point. `*.example.com` reads as "the
// vendor's own subdomains" and actually means "anything the vendor's DNS can be
// made to point at", which includes a compromised or user-controlled subdomain.
// A widget that needs two hosts lists two hosts.
const HOST_RE = /^(?=.{1,253}$)[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$/
const TICKS: WidgetTick[] = ['none', 'second', 'minute']

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x)
}
function str(x: unknown): x is string {
  return typeof x === 'string'
}

/** Full runtime validation. Returns every problem, not just the first. */
export function checkManifest(m: unknown): ManifestCheck {
  const e: string[] = []
  if (!isRecord(m)) return { ok: false, errors: ['manifest is not an object'] }

  if (!str(m.id) || !ID_RE.test(m.id) || m.id.length > MAX_ID_LEN) {
    e.push('id must be lowercase alphanumeric segments joined by "-" or "." (max 64 chars)')
  }
  if (!str(m.name) || m.name.trim() === '' || m.name.length > 48) e.push('name must be a non-empty string of at most 48 chars')
  if (!str(m.description) || m.description.length > 160) e.push('description must be a string of at most 160 chars')
  if (!str(m.version) || m.version.trim() === '' || m.version.length > 32) e.push('version must be a non-empty string of at most 32 chars')
  if (m.author !== undefined && (!str(m.author) || m.author.length > 64)) e.push('author must be a string of at most 64 chars')

  // sizes
  if (!Array.isArray(m.sizes) || m.sizes.length === 0) {
    e.push('sizes must be a non-empty array')
  } else {
    for (const s of m.sizes) {
      if (!str(s) || !WIDGET_SIZES.includes(s as WidgetSize)) e.push(`unknown size "${String(s)}"`)
    }
    if (new Set(m.sizes).size !== m.sizes.length) e.push('sizes contains duplicates')
  }

  // permissions
  const perms: WidgetPermission[] = []
  if (m.permissions !== undefined) {
    if (!Array.isArray(m.permissions)) {
      e.push('permissions must be an array')
    } else {
      for (const p of m.permissions) {
        if (!str(p) || !WIDGET_PERMISSIONS.includes(p as WidgetPermission)) {
          e.push(`unknown permission "${String(p)}"`)
        } else {
          perms.push(p as WidgetPermission)
        }
      }
      if (new Set(m.permissions).size !== m.permissions.length) e.push('permissions contains duplicates')
    }
  }

  // hosts — required with `network`, forbidden without it.
  //
  // Forbidden-without is not pedantry: a `hosts` list on a widget that never got
  // `network` is a lie in the UI. The gallery shows a widget's hosts so a user
  // can see who it talks to, and a widget that lists hosts it cannot reach is
  // either a mistake or an attempt to look more (or less) capable than it is.
  const wantsNet = perms.includes('network')
  if (m.hosts !== undefined) {
    if (!Array.isArray(m.hosts)) {
      e.push('hosts must be an array of bare hostnames')
    } else {
      for (const h of m.hosts) {
        if (!str(h) || !HOST_RE.test(h)) e.push(`invalid host "${String(h)}" — bare hostname only, no scheme, path or wildcard`)
      }
      if (m.hosts.length > 8) e.push('hosts may list at most 8 hostnames')
    }
  }
  if (wantsNet && (!Array.isArray(m.hosts) || m.hosts.length === 0)) {
    e.push('a widget requesting "network" must declare the hosts it will reach')
  }
  if (!wantsNet && Array.isArray(m.hosts) && m.hosts.length > 0) {
    e.push('hosts declared without requesting the "network" permission')
  }

  if (m.tick !== undefined && (!str(m.tick) || !TICKS.includes(m.tick as WidgetTick))) {
    e.push('tick must be "none", "second" or "minute"')
  }

  if (m.settings !== undefined) {
    if (!Array.isArray(m.settings)) {
      e.push('settings must be an array')
    } else {
      const seen = new Set<string>()
      for (const s of m.settings) e.push(...checkSetting(s, seen))
      if (m.settings.length > 16) e.push('settings may declare at most 16 keys')
    }
  }

  return { ok: e.length === 0, errors: e }
}

const SETTING_KEY_RE = /^[a-zA-Z][a-zA-Z0-9_]{0,31}$/

function checkSetting(s: unknown, seen: Set<string>): string[] {
  if (!isRecord(s)) return ['a settings entry is not an object']
  const e: string[] = []
  const key = s.key
  if (!str(key) || !SETTING_KEY_RE.test(key)) {
    e.push(`invalid setting key "${String(key)}"`)
  } else if (seen.has(key)) {
    e.push(`duplicate setting key "${key}"`)
  } else {
    seen.add(key)
  }
  if (!str(s.label) || s.label.trim() === '') e.push(`setting "${String(key)}" needs a label`)
  switch (s.type) {
    case 'string':
    case 'boolean':
      break
    case 'number':
      if (s.min !== undefined && typeof s.min !== 'number') e.push(`setting "${String(key)}": min must be a number`)
      if (s.max !== undefined && typeof s.max !== 'number') e.push(`setting "${String(key)}": max must be a number`)
      break
    case 'select':
      if (!Array.isArray(s.options) || s.options.length === 0) {
        e.push(`setting "${String(key)}": select needs options`)
      } else {
        for (const o of s.options) {
          if (!isRecord(o) || !str(o.value) || !str(o.label)) e.push(`setting "${String(key)}": bad option`)
        }
      }
      break
    case 'list':
      if (s.default !== undefined && !Array.isArray(s.default)) e.push(`setting "${String(key)}": list default must be an array`)
      break
    default:
      e.push(`setting "${String(key)}": unknown type "${String(s.type)}"`)
  }
  return e
}

/** Throwing form, for the build-time `defineWidget` path. */
export function assertManifest(m: unknown): WidgetManifest {
  const r = checkManifest(m)
  if (!r.ok) throw new Error(`invalid widget manifest: ${r.errors.join('; ')}`)
  return m as WidgetManifest
}

// ── Settings values ──────────────────────────────────────────────────────────

/**
 * Coerce stored settings against the manifest's declarations.
 *
 * Two rules, both load-bearing:
 *
 *  1. UNDECLARED KEYS ARE DROPPED. Persisted settings are just localStorage — a
 *     user, another widget's bug, or anything that can write that key could put
 *     arbitrary keys there, and a widget that reads `ctx.settings.somethingElse`
 *     would then be reading attacker-chosen data. The context only ever carries
 *     keys the manifest declared.
 *  2. WRONG TYPES FALL BACK TO THE DEFAULT, never through. A number setting that
 *     holds the string "12" arrives as the default, not as "12" — otherwise every
 *     widget has to re-validate what the host already promised to type.
 */
export function normalizeSettings(specs: WidgetSettingSpec[] | undefined, raw: unknown): WidgetSettings {
  const out: WidgetSettings = {}
  if (!specs) return out
  const src = isRecord(raw) ? raw : {}
  for (const spec of specs) {
    const v = src[spec.key]
    out[spec.key] = coerceSetting(spec, v)
  }
  return out
}

function coerceSetting(spec: WidgetSettingSpec, v: unknown): WidgetSettingValue {
  switch (spec.type) {
    case 'string': {
      if (typeof v !== 'string') return spec.default ?? ''
      const max = spec.maxLength ?? 256
      return v.length > max ? v.slice(0, max) : v
    }
    case 'number': {
      if (typeof v !== 'number' || !Number.isFinite(v)) return spec.default ?? 0
      let n = v
      if (spec.min !== undefined) n = Math.max(spec.min, n)
      if (spec.max !== undefined) n = Math.min(spec.max, n)
      return n
    }
    case 'boolean':
      return typeof v === 'boolean' ? v : (spec.default ?? false)
    case 'select': {
      const allowed = spec.options.map((o) => o.value)
      if (typeof v === 'string' && allowed.includes(v)) return v
      return spec.default ?? allowed[0]
    }
    case 'list': {
      const max = spec.maxItems ?? 16
      if (!Array.isArray(v)) return (spec.default ?? []).slice(0, max)
      return v.filter((x): x is string => typeof x === 'string').slice(0, max)
    }
  }
}

/** The permissions a manifest requests, deduped and in declaration order. */
export function requestedPermissions(m: WidgetManifest): WidgetPermission[] {
  return [...new Set(m.permissions ?? [])]
}

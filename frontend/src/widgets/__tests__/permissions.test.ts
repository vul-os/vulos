// permissions.test.ts — every grant this API makes is REAL, in this code.
//
// THE REASON THIS FILE EXISTS
//
// Vulos already has a permission model that is documentation wearing the costume
// of a control: an app manifest's `permissions` array is validated against a list
// of valid strings, and then almost none of those strings has any runtime effect
// at all. An app declaring `camera` is neither granted nor denied anything,
// because nothing reads the declaration.
//
// So "the platform will contain it" is not available as an assumption here, and a
// widget permission that only appeared in a manifest and a settings switch would
// be exactly the same lie. Every permission in WIDGET_PERMISSIONS is therefore
// asserted twice:
//
//   1. DENIED ⇒ the capability is `null` on the context. Not a throwing object,
//      not an empty stub — null, so a widget can see it does not have the thing.
//   2. DENIED ⇒ the underlying seam is not opened at all. A widget handed `null`
//      while the rail still holds the telemetry socket open would satisfy the
//      type and miss the point: the user denied it partly so the box would stop
//      doing the work.
//
// The sandbox half of the same question — that the bridge REFUSES each verb
// without its grant — lives in widgetSecurity.test.ts.

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { buildContext, seamsNeeded } from '../host/context'
import { setProxyAvailable, resetProxyProbe } from '../net'
import { WIDGET_PERMISSIONS, PERMISSION_INFO, type WidgetInstance, type WidgetManifest, type WidgetPermission } from '../types'
import { installMemoryStorage, uninstallMemoryStorage } from './memoryStorage'

const MANIFEST: WidgetManifest = {
  id: 'test.everything',
  name: 'Everything',
  description: 'Requests every permission there is',
  version: '1.0.0',
  sizes: ['medium'],
  permissions: [...WIDGET_PERMISSIONS],
  hosts: ['api.example.com'],
}

function instance(granted: WidgetPermission[]): WidgetInstance {
  return { instanceId: 'w1', widgetId: MANIFEST.id, size: 'medium', settings: {}, granted }
}

function ctxWith(granted: WidgetPermission[]) {
  return buildContext({
    manifest: MANIFEST,
    instance: instance(granted),
    now: new Date('2026-08-15T12:00:00Z'),
    reducedMotion: false,
    telemetry: { connected: true, cpu: 12 },
    calendar: { events: [], error: false },
    notifications: { recent: [{ id: 'n1', title: 'hi', body: '', read: false }], unread: 1 },
    setSetting: () => {},
    notify: () => {},
    openApp: () => {},
  })
}

/** The context field each permission controls. */
const FIELD: Record<WidgetPermission, keyof ReturnType<typeof ctxWith>> = {
  storage: 'storage',
  network: 'net',
  notify: 'notify',
  notifications: 'notifications',
  launch: 'openApp',
  telemetry: 'telemetry',
  calendar: 'calendar',
}

beforeEach(() => {
  installMemoryStorage()
  // `network` additionally needs a box broker; pretend one exists so this file
  // measures the GRANT and not the broker probe (net.ts's own behaviour without
  // a broker is asserted in widgetSecurity.test.ts).
  setProxyAvailable(true)
})
afterEach(() => { uninstallMemoryStorage(); resetProxyProbe(); vi.restoreAllMocks() })

describe('every permission has a runtime effect', () => {
  it('covers every permission in the enum — no string is untested', () => {
    // If someone adds a permission and forgets to gate it, this list is where
    // the omission shows up rather than in production.
    expect(Object.keys(FIELD).sort()).toEqual([...WIDGET_PERMISSIONS].sort())
    for (const p of WIDGET_PERMISSIONS) {
      expect(PERMISSION_INFO[p], `${p} has no user-facing description`).toBeTruthy()
      expect(PERMISSION_INFO[p].title.length).toBeGreaterThan(3)
      expect(PERMISSION_INFO[p].detail.length).toBeGreaterThan(20)
    }
  })

  it.each([...WIDGET_PERMISSIONS])('%s: DENIED yields null on the context', (perm) => {
    const ctx = ctxWith([])
    expect(ctx[FIELD[perm]], `ctx.${FIELD[perm]} must be null without "${perm}"`).toBeNull()
  })

  it.each([...WIDGET_PERMISSIONS])('%s: GRANTED yields a usable capability', (perm) => {
    const ctx = ctxWith([perm])
    expect(ctx[FIELD[perm]], `ctx.${FIELD[perm]} must exist with "${perm}"`).not.toBeNull()
  })

  it.each([...WIDGET_PERMISSIONS])('%s: granting it grants NOTHING ELSE', (perm) => {
    const ctx = ctxWith([perm])
    for (const other of WIDGET_PERMISSIONS) {
      if (other === perm) continue
      expect(ctx[FIELD[other]], `granting "${perm}" also granted "${other}"`).toBeNull()
    }
  })
})

describe('the capabilities actually work when granted', () => {
  it('storage is real, and scoped to the instance', () => {
    const ctx = ctxWith(['storage'])
    expect(ctx.storage!.set('k', 'v')).toBe(true)
    expect(ctx.storage!.get('k')).toBe('v')
    expect(ctx.storage!.keys()).toEqual(['k'])
    // A second placement of the same widget sees nothing of the first.
    const other = buildContext({
      manifest: MANIFEST,
      instance: { ...instance(['storage']), instanceId: 'w2' },
      now: new Date(), reducedMotion: false,
      telemetry: { connected: false }, calendar: { events: null, error: false },
      notifications: { recent: [], unread: 0 },
      setSetting: () => {}, notify: () => {}, openApp: () => {},
    })
    expect(other.storage!.get('k')).toBeNull()
  })

  it('notify and openApp are the callbacks the host passed, not stubs', () => {
    const notify = vi.fn()
    const openApp = vi.fn()
    const ctx = buildContext({
      manifest: MANIFEST,
      instance: instance(['notify', 'launch']),
      now: new Date(), reducedMotion: false,
      telemetry: { connected: false }, calendar: { events: null, error: false },
      notifications: { recent: [], unread: 0 },
      setSetting: () => {}, notify, openApp,
    })
    ctx.notify!('hello')
    ctx.openApp!('lilmail')
    expect(notify).toHaveBeenCalledWith('hello')
    expect(openApp).toHaveBeenCalledWith('lilmail')
  })

  it('the data seams carry the real values through', () => {
    const ctx = ctxWith(['telemetry', 'calendar', 'notifications'])
    expect(ctx.telemetry!.cpu).toBe(12)
    expect(ctx.calendar!.events).toEqual([])
    expect(ctx.notifications!.unread).toBe(1)
  })
})

describe('network needs the grant AND a reachable broker', () => {
  it('is null without the grant even when the broker exists', () => {
    setProxyAvailable(true)
    expect(ctxWith([]).net).toBeNull()
  })

  it('is null WITH the grant when the box has no broker', () => {
    // The user granting `network` on a box that brokers nothing has granted
    // nothing reachable, and the widget sees exactly the null it would see if
    // they had refused. There is no silent direct-from-browser fallback.
    setProxyAvailable(false)
    expect(ctxWith(['network']).net).toBeNull()
  })

  it('is null with the grant and a broker when the manifest declares no hosts', () => {
    setProxyAvailable(true)
    const ctx = buildContext({
      manifest: { ...MANIFEST, hosts: [] },
      instance: instance(['network']),
      now: new Date(), reducedMotion: false,
      telemetry: { connected: false }, calendar: { events: null, error: false },
      notifications: { recent: [], unread: 0 },
      setSetting: () => {}, notify: () => {}, openApp: () => {},
    })
    expect(ctx.net).toBeNull()
  })
})

describe('a denied permission stops the BOX doing the work', () => {
  it('opens no seam when nothing was granted', () => {
    // The half a per-widget null cannot give you: if no mounted widget holds
    // `telemetry`, the rail must not open the telemetry socket at all.
    expect(seamsNeeded([instance([])])).toEqual({
      telemetry: false, calendar: false, notifications: false,
    })
  })

  it('opens only the seams some mounted widget holds', () => {
    expect(seamsNeeded([instance(['telemetry'])])).toMatchObject({ telemetry: true, calendar: false })
    expect(seamsNeeded([instance(['calendar'])])).toMatchObject({ telemetry: false, calendar: true })
    expect(seamsNeeded([])).toEqual({ telemetry: false, calendar: false, notifications: false })
  })

  it('opens a seam ONCE for many widgets, not once per widget', () => {
    // seamsNeeded returns a boolean, not a count, precisely so the rail cannot
    // mount two sources: five widgets granted `telemetry` must still be one
    // WebSocket to the box.
    const many = [instance(['telemetry']), instance(['telemetry']), instance(['telemetry'])]
    expect(seamsNeeded(many).telemetry).toBe(true)
    expect(typeof seamsNeeded(many).telemetry).toBe('boolean')
  })

  it('the rail mounts each source behind its seam flag', async () => {
    // Source-level assertion, in the spirit of zLayers.test.ts: the wiring is a
    // one-line conditional in JSX and deleting the condition would open every
    // socket for every user regardless of what they granted.
    const { readFileSync } = await import('node:fs')
    const src = readFileSync('src/widgets/host/WidgetRail.tsx', 'utf8')
    expect(src).toContain('seams.telemetry && <TelemetrySource')
    expect(src).toContain('seams.calendar && <CalendarSource')
    expect(src).toContain('seams.notifications && <NotificationSource')
    // …and that the rail does not rebuild the context inline, bypassing the gate.
    expect(src).toContain('buildContext({')
    expect(src).not.toMatch(/storage:\s*granted\.includes/)
  })
})

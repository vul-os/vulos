// widgetApi.test.ts — manifest validation, the registry, storage and layout.
//
// These are the parts of the widget system a THIRD PARTY can reach, so they are
// tested the way a hostile input would arrive: with the wrong types, with extra
// keys, with values that used to be valid and no longer are.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { checkManifest, normalizeSettings } from '../manifest'
import { clearRegistry, defineWidget, getWidget, listWidgets, registerWidget } from '../registry'
import {
  widgetGet, widgetSet, widgetRemove, widgetClear, widgetKeys, STORAGE_LIMITS,
} from '../storage'
import {
  addWidget, defaultLayout, LAYOUT_STORAGE_KEY, loadLayout, moveWidget, parseLayout,
  reconcileInstance, removeWidget, resizeWidget, saveLayout, setGrants, setInstanceSetting,
} from '../layout'
import { installMemoryStorage, uninstallMemoryStorage } from './memoryStorage'
import type { WidgetManifest } from '../types'

const BASE: WidgetManifest = {
  id: 'test.basic',
  name: 'Basic',
  description: 'A test widget',
  version: '1.0.0',
  sizes: ['small', 'medium'],
}

function register(m: Partial<WidgetManifest> & { id: string }) {
  return registerWidget(defineWidget({ manifest: { ...BASE, ...m }, render: () => null }))
}

beforeEach(() => {
  clearRegistry()
  installMemoryStorage()
})
afterEach(() => { vi.restoreAllMocks(); uninstallMemoryStorage() })

describe('the store probe', () => {
  it('treats a localStorage that lacks the Storage methods as no store', () => {
    // This suite's own environment ships exactly such an object, which is how
    // the bug was found: `globalThis.localStorage` existed, was truthy, and had
    // no getItem — so a truthiness check handed it back and the first call threw
    // a TypeError from a render path.
    uninstallMemoryStorage()
    Object.defineProperty(globalThis, 'localStorage', { value: {}, configurable: true, writable: true })
    expect(() => widgetSet('w1', 'k', 'v')).not.toThrow()
    expect(widgetSet('w1', 'k', 'v')).toBe(false)
    expect(widgetGet('w1', 'k')).toBeNull()
    expect(widgetKeys('w1')).toEqual([])
    expect(() => widgetClear('w1')).not.toThrow()
    installMemoryStorage()
  })
})

describe('checkManifest', () => {
  it('accepts a minimal valid manifest', () => {
    expect(checkManifest(BASE)).toEqual({ ok: true, errors: [] })
  })

  it('rejects a non-object', () => {
    for (const bad of [null, undefined, 'x', 42, []]) {
      expect(checkManifest(bad).ok, String(bad)).toBe(false)
    }
  })

  it('rejects malformed ids, including path-traversal shapes', () => {
    // Ids get concatenated into storage keys and read back out of persisted
    // JSON, so ".." and "/" must never survive validation.
    for (const id of ['', 'Has Caps', 'a..b', '-lead', 'trail-', 'a/b', '../etc', 'a'.repeat(65)]) {
      expect(checkManifest({ ...BASE, id }).ok, id).toBe(false)
    }
    for (const id of ['a', 'com.acme.tides', 'my-widget', 'a1.b2-c3']) {
      expect(checkManifest({ ...BASE, id }).ok, id).toBe(true)
    }
  })

  it('rejects an unknown size or permission rather than ignoring it', () => {
    // Ignoring an unrecognised permission is how it gets silently GRANTED the
    // day someone adds it to the enum.
    expect(checkManifest({ ...BASE, sizes: ['huge'] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, sizes: [] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, permissions: ['root'] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, permissions: ['storage', 'storage'] }).ok).toBe(false)
  })

  it('requires hosts when network is requested, and forbids them otherwise', () => {
    expect(checkManifest({ ...BASE, permissions: ['network'] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, permissions: ['network'], hosts: [] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, permissions: ['network'], hosts: ['api.example.com'] }).ok).toBe(true)
    // Hosts without the permission is a lie in the gallery UI.
    expect(checkManifest({ ...BASE, hosts: ['api.example.com'] }).ok).toBe(false)
  })

  it('rejects wildcards, schemes and paths in hosts', () => {
    // `*.example.com` reads as "the vendor's subdomains" and means "anything the
    // vendor's DNS can be pointed at".
    for (const h of ['*.example.com', 'https://api.example.com', 'api.example.com/v1', 'localhost', '..', '']) {
      expect(checkManifest({ ...BASE, permissions: ['network'], hosts: [h] }).ok, h).toBe(false)
    }
  })

  it('validates the settings declarations', () => {
    expect(checkManifest({ ...BASE, settings: [{ key: 'a', type: 'string', label: 'A' }] }).ok).toBe(true)
    expect(checkManifest({ ...BASE, settings: [{ key: '1bad', type: 'string', label: 'A' }] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, settings: [{ key: 'a', type: 'nope', label: 'A' }] }).ok).toBe(false)
    expect(checkManifest({ ...BASE, settings: [{ key: 'a', type: 'select', label: 'A', options: [] }] }).ok).toBe(false)
    expect(checkManifest({
      ...BASE,
      settings: [{ key: 'a', type: 'string', label: 'A' }, { key: 'a', type: 'string', label: 'B' }],
    }).ok).toBe(false)
  })

  it('reports every problem at once, not just the first', () => {
    const r = checkManifest({ ...BASE, id: 'BAD', sizes: [], version: '' })
    expect(r.errors.length).toBeGreaterThanOrEqual(3)
  })
})

describe('normalizeSettings', () => {
  const specs = [
    { key: 's', type: 'string' as const, label: 'S', default: 'hi', maxLength: 5 },
    { key: 'n', type: 'number' as const, label: 'N', default: 3, min: 1, max: 10 },
    { key: 'b', type: 'boolean' as const, label: 'B', default: true },
    { key: 'sel', type: 'select' as const, label: 'Sel', default: 'a', options: [{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }] },
    { key: 'l', type: 'list' as const, label: 'L', default: ['x'], maxItems: 2 },
  ]

  it('drops keys the manifest never declared', () => {
    // Persisted settings are just localStorage — anything that can write that
    // key could put arbitrary keys there, and a widget reading them would be
    // reading attacker-chosen data.
    const out = normalizeSettings(specs, { s: 'ok', evil: 'payload', __proto__: 'x' })
    expect(Object.keys(out).sort()).toEqual(['b', 'l', 'n', 's', 'sel'])
    expect((out as Record<string, unknown>).evil).toBeUndefined()
  })

  it('falls back to the default on a type mismatch rather than passing it through', () => {
    const out = normalizeSettings(specs, { s: 42, n: '7', b: 'yes', sel: 'zzz', l: 'nope' })
    expect(out.s).toBe('hi')
    expect(out.n).toBe(3)
    expect(out.b).toBe(true)
    expect(out.sel).toBe('a')
    expect(out.l).toEqual(['x'])
  })

  it('clamps ranges, lengths and list sizes', () => {
    const out = normalizeSettings(specs, { s: 'abcdefghij', n: 999, l: ['a', 'b', 'c', 'd'] })
    expect(out.s).toBe('abcde')
    expect(out.n).toBe(10)
    expect(out.l).toEqual(['a', 'b'])
    expect(normalizeSettings(specs, { n: -5 }).n).toBe(1)
  })

  it('drops non-string entries from a list', () => {
    expect(normalizeSettings(specs, { l: ['a', 3, null, 'b'] }).l).toEqual(['a', 'b'])
  })
})

describe('registry', () => {
  it('registers and retrieves through the public entry points', () => {
    register({ id: 'test.one' })
    register({ id: 'test.two' })
    expect(getWidget('test.one')?.manifest.name).toBe('Basic')
    expect(listWidgets()).toHaveLength(2)
    expect(getWidget('nope')).toBeNull()
  })

  it('refuses to define a widget with an invalid manifest', () => {
    expect(() => defineWidget({ manifest: { ...BASE, id: 'NOPE' }, render: () => null })).toThrow(/invalid widget manifest/)
  })

  it('replaces on re-registration rather than duplicating', () => {
    register({ id: 'test.one', name: 'First' })
    register({ id: 'test.one', name: 'Second' })
    expect(listWidgets()).toHaveLength(1)
    expect(getWidget('test.one')?.manifest.name).toBe('Second')
  })
})

describe('storage', () => {
  it('round-trips within a namespace', () => {
    expect(widgetSet('w1', 'k', 'v')).toBe(true)
    expect(widgetGet('w1', 'k')).toBe('v')
    expect(widgetKeys('w1')).toEqual(['k'])
    widgetRemove('w1', 'k')
    expect(widgetGet('w1', 'k')).toBeNull()
  })

  it('cannot see another instance\'s keys', () => {
    widgetSet('w1', 'secret', 'mine')
    widgetSet('w2', 'other', 'theirs')
    expect(widgetGet('w2', 'secret')).toBeNull()
    expect(widgetKeys('w2')).toEqual(['other'])
  })

  it('cannot reach the shell\'s own keys through a crafted id or key', () => {
    globalThis.localStorage.setItem('vulos-theme', 'dark')
    // A malformed instance id is refused outright, so there is no prefix to
    // escape from.
    expect(widgetSet('../..', 'x', 'y')).toBe(false)
    expect(widgetSet('', 'x', 'y')).toBe(false)
    expect(widgetSet('UPPER', 'x', 'y')).toBe(false)
    // And a key is only ever appended to the namespace prefix.
    widgetSet('w1', '::vulos-theme', 'light')
    expect(globalThis.localStorage.getItem('vulos-theme')).toBe('dark')
  })

  it('enforces the quota and refuses rather than throwing', () => {
    expect(widgetSet('w1', 'k', 'x'.repeat(STORAGE_LIMITS.MAX_VALUE_LEN + 1))).toBe(false)
    expect(widgetSet('w1', 'k'.repeat(STORAGE_LIMITS.MAX_KEY_LEN + 1), 'v')).toBe(false)
    // Fill the instance budget, then prove the next write is refused.
    const chunk = 'x'.repeat(STORAGE_LIMITS.MAX_VALUE_LEN)
    let n = 0
    while (widgetSet('w1', `k${n}`, chunk) && n < 20) n++
    expect(n).toBeGreaterThan(0)
    expect(widgetSet('w1', `k${n}`, chunk)).toBe(false)
  })

  it('rejects non-string values', () => {
    expect(widgetSet('w1', 'k', 42 as unknown as string)).toBe(false)
    expect(widgetSet('w1', 'k', null as unknown as string)).toBe(false)
  })

  it('clears only its own namespace', () => {
    widgetSet('w1', 'a', '1')
    widgetSet('w2', 'b', '2')
    globalThis.localStorage.setItem('vulos-theme', 'dark')
    widgetClear('w1')
    expect(widgetKeys('w1')).toEqual([])
    expect(widgetGet('w2', 'b')).toBe('2')
    expect(globalThis.localStorage.getItem('vulos-theme')).toBe('dark')
  })
})

describe('layout reconciliation', () => {
  it('drops a placement whose widget no longer ships', () => {
    expect(reconcileInstance({ instanceId: 'w1', widgetId: 'gone' })).toBeNull()
  })

  it('clamps a size the manifest no longer offers', () => {
    register({ id: 'test.basic', sizes: ['small'] })
    const inst = reconcileInstance({ instanceId: 'w1', widgetId: 'test.basic', size: 'large' })
    expect(inst?.size).toBe('small')
  })

  it('REVOKES a grant the manifest no longer requests', () => {
    // A stored grant must never outlive the request that justified it, or a
    // widget could shed a scary permission to get installed and get it back in
    // the next update without ever re-prompting.
    register({ id: 'test.basic', permissions: ['storage'] })
    const inst = reconcileInstance({
      instanceId: 'w1', widgetId: 'test.basic',
      granted: ['storage', 'network', 'telemetry', 'bogus'],
    })
    expect(inst?.granted).toEqual(['storage'])
  })

  it('drops duplicate instance ids, which would break React keys', () => {
    register({ id: 'test.basic' })
    const l = parseLayout({
      version: 1,
      instances: [
        { instanceId: 'w1', widgetId: 'test.basic' },
        { instanceId: 'w1', widgetId: 'test.basic' },
      ],
    })
    expect(l?.instances).toHaveLength(1)
  })

  it('refuses a layout of the wrong version or shape', () => {
    expect(parseLayout(null)).toBeNull()
    expect(parseLayout({ version: 2, instances: [] })).toBeNull()
    expect(parseLayout({ version: 1, instances: 'nope' })).toBeNull()
  })
})

describe('layout operations', () => {
  beforeEach(() => {
    register({ id: 'test.basic', sizes: ['small', 'medium'], permissions: ['storage'] })
    register({ id: 'test.other', sizes: ['large'] })
  })

  it('adds with every permission DENIED unless explicitly granted', () => {
    const l = addWidget({ version: 1, instances: [] }, 'test.basic')
    expect(l.instances[0].granted).toEqual([])
    expect(l.instances[0].size).toBe('small') // first declared size
  })

  it('cannot grant a permission the manifest never requested', () => {
    const l = addWidget({ version: 1, instances: [] }, 'test.basic', { granted: ['storage', 'network'] })
    expect(l.instances[0].granted).toEqual(['storage'])
  })

  it('ignores an unknown widget id', () => {
    const before = { version: 1 as const, instances: [] }
    expect(addWidget(before, 'nope')).toBe(before)
  })

  it('removes the placement AND wipes its stored data', () => {
    let l = addWidget({ version: 1, instances: [] }, 'test.basic')
    const id = l.instances[0].instanceId
    widgetSet(id, 'k', 'v')
    expect(widgetGet(id, 'k')).toBe('v')
    l = removeWidget(l, id)
    expect(l.instances).toHaveLength(0)
    // A user who removed a widget to get rid of it should not still have its
    // data on the box.
    expect(widgetGet(id, 'k')).toBeNull()
  })

  it('reorders, and refuses to move off either end', () => {
    let l = addWidget({ version: 1, instances: [] }, 'test.basic')
    l = addWidget(l, 'test.other')
    const [a, b] = l.instances.map((i) => i.instanceId)
    expect(moveWidget(l, a, 1).instances.map((i) => i.instanceId)).toEqual([b, a])
    expect(moveWidget(l, a, -1)).toBe(l)
    expect(moveWidget(l, b, 1)).toBe(l)
    expect(moveWidget(l, 'nope', 1)).toBe(l)
  })

  it('refuses a resize the manifest does not offer', () => {
    let l = addWidget({ version: 1, instances: [] }, 'test.basic')
    const id = l.instances[0].instanceId
    l = resizeWidget(l, id, 'medium')
    expect(l.instances[0].size).toBe('medium')
    l = resizeWidget(l, id, 'large') // not declared
    expect(l.instances[0].size).toBe('medium')
  })

  it('re-normalises a setting write instead of trusting it', () => {
    register({
      id: 'test.set', sizes: ['small'],
      settings: [{ key: 'n', type: 'number', label: 'N', default: 5, max: 10 }],
    })
    let l = addWidget({ version: 1, instances: [] }, 'test.set')
    const id = l.instances[0].instanceId
    l = setInstanceSetting(l, id, 'n', 99)
    expect(l.instances[0].settings.n).toBe(10)
    l = setInstanceSetting(l, id, 'undeclared', 'x')
    expect(l.instances[0].settings.undeclared).toBeUndefined()
  })

  it('intersects grants with what the manifest requests', () => {
    let l = addWidget({ version: 1, instances: [] }, 'test.basic')
    const id = l.instances[0].instanceId
    l = setGrants(l, id, ['storage', 'network', 'telemetry'])
    expect(l.instances[0].granted).toEqual(['storage'])
  })
})

describe('layout persistence', () => {
  it('falls back to the defaults only when the blob is unreadable', () => {
    register({ id: 'vulos.clock', sizes: ['medium'] })
    globalThis.localStorage.setItem(LAYOUT_STORAGE_KEY, 'not json{')
    expect(loadLayout().instances.length).toBeGreaterThan(0)
  })

  it('respects a deliberately EMPTY rail across a reload', () => {
    // Re-adding five widgets every boot would be the OS overruling the user.
    register({ id: 'vulos.clock', sizes: ['medium'] })
    saveLayout({ version: 1, instances: [] })
    expect(loadLayout().instances).toEqual([])
  })

  it('round-trips a real layout', () => {
    register({ id: 'test.basic', sizes: ['small', 'medium'], permissions: ['storage'] })
    const l = addWidget({ version: 1, instances: [] }, 'test.basic', { granted: ['storage'] })
    saveLayout(l)
    const back = loadLayout()
    expect(back.instances).toHaveLength(1)
    expect(back.instances[0].widgetId).toBe('test.basic')
    expect(back.instances[0].granted).toEqual(['storage'])
  })

  it('survives localStorage being unavailable', () => {
    const spy = vi.spyOn(globalThis.localStorage, 'getItem').mockImplementation(() => { throw new Error('private mode') })
    expect(() => loadLayout()).not.toThrow()
    spy.mockRestore()
  })

  it('builds defaults only from widgets that actually exist', () => {
    // Nothing registered → the default rail is empty rather than referencing
    // widgets that would resolve to null in the host.
    expect(defaultLayout().instances).toEqual([])
  })
})

/**
 * prefGroups.test.ts — what syncs, pinned as a COUNT and as an exact set.
 *
 * A registry that quietly loses an entry still passes every test written about
 * the entries it kept. So this file asserts the whole set and its size, not a
 * sample: dropping `wallpaper` from prefGroups.ts is a failure here, and so is
 * adding a preference without deciding whether it syncs.
 *
 * It also checks the decomposition claims against the real numbers rather than
 * against the comment that states them, because the 512-byte cap is the reason
 * the desktop layout and the widget rail are split at all, and a change that
 * pushed one value over it would otherwise be found by a user whose layout
 * silently stopped syncing.
 */

import { beforeEach, describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { registerAllPrefGroups } from './prefGroups'
import { MAX_SYNCED_KEYS, MAX_SYNCED_VALUE, registeredPrefGroups } from './syncedPrefs'
import {
  AI_FIRSTRUN_PREF_KEY, DENSITY_PREF_KEY, DESKTOP_PREF_KEYS, DOCK_PINS_PREF_KEY,
  NOTIFY_PREF_KEY, THEME_PREF_KEYS, WALLPAPER_PREF_KEY, WIDGETS_PREF_KEY_COUNT,
} from './prefKeys'
import { applyPreset, exportLayoutFields, importLayoutFields, getLayout, resetToStock } from '../desktop/store'
import {
  addWidget, defaultLayout, exportRailFields, foreignPlacementCount,
  importRailFields, loadLayout, saveLayout,
} from '../widgets/layout'
// SIDE-EFFECT IMPORT, and it is load-bearing. The widget registry starts EMPTY;
// the builtins register themselves at module evaluation. Without this line
// getWidget() answers null for every id, defaultLayout() returns zero
// placements, addWidget() is a no-op — and every rail assertion below compares
// an empty rail to an empty rail and passes while checking nothing. That is not
// hypothetical: this file was written without it, and a mutation that made
// importRailFields() empty the rail on an absent count SURVIVED.
import '../widgets/builtin'
import { widgetCount } from '../widgets/registry'

beforeEach(() => {
  localStorage.clear()
  registerAllPrefGroups()
})

describe('the registry', () => {
  it('registers exactly the eight groups the inventory calls SYNC', () => {
    const names = registeredPrefGroups().map((g) => g.name).sort()
    expect(names).toEqual([
      'ai', 'density', 'desktop', 'dock', 'notifications', 'theme', 'wallpaper', 'widgets',
    ])
    // Stated separately from the list above on purpose: a count is what catches
    // an entry being dropped in a merge that also updated the expected array.
    expect(names).toHaveLength(8)
  })

  it('claims every bag key exactly once — no two groups own the same key', () => {
    const groups = registeredPrefGroups()
    const keys = [
      ...Object.values(THEME_PREF_KEYS),
      WALLPAPER_PREF_KEY, DOCK_PINS_PREF_KEY, DENSITY_PREF_KEY,
      AI_FIRSTRUN_PREF_KEY, NOTIFY_PREF_KEY, WIDGETS_PREF_KEY_COUNT,
      ...DESKTOP_PREF_KEYS,
    ]
    for (const key of keys) {
      const owners = groups.filter((g) => g.owns(key)).map((g) => g.name)
      expect(owners, `owners of ${key}`).toHaveLength(1)
    }
  })

  it('leaves the argued exceptions unclaimed', () => {
    // Absence from the registry is what makes these exceptions rather than
    // gaps. If a future change starts syncing one of them it must go through
    // roadmap/USER-STATE-INVENTORY.md §3, not through this file being wrong.
    const groups = registeredPrefGroups()
    for (const key of [
      'vulos-shell-state',          // window geometry — a statement about a screen
      'vulos.biometric.unlock',     // an enrolment on THIS device's sensor
      'vulos.location.share',       // consent granted to a device
      'vulos.os.endpoints.v1',      // this client's network position
      'vulos.notifications.log.v1', // a log; its home is the box's own store
      'vulos.os.offlineQueue.v1',   // in-flight writes; replicating double-applies
    ]) {
      expect(groups.filter((g) => g.owns(key)), `${key} must not be synced here`).toHaveLength(0)
    }
  })

  it('fits a fully loaded shell inside the bag\'s 64-key budget', () => {
    // The widget rail is the only group whose key count grows with use, so the
    // worst case is a full rail plus one key from everything else.
    const fixedKeys = Object.values(THEME_PREF_KEYS).length
      + DESKTOP_PREF_KEYS.length
      + 5 // wallpaper, dock pins, density, AI first-run, notification prefs
    const railWorstCase = 1 + 24 // count + MAX_INSTANCES placements
    expect(fixedKeys + railWorstCase).toBeLessThanOrEqual(MAX_SYNCED_KEYS)
  })
})

describe('the desktop layout, decomposed', () => {
  it('round-trips a customised layout through five values that each fit', () => {
    applyPreset('taskbar')
    const fields = exportLayoutFields()

    expect(Object.keys(fields).sort()).toEqual([...DESKTOP_PREF_KEYS].sort())
    for (const [k, v] of Object.entries(fields)) {
      // The whole reason for the split: a single DesktopLayout blob measures
      // 611 bytes. Each field must clear the cap on its own.
      expect(v.length, `${k} is ${v.length} bytes`).toBeLessThanOrEqual(MAX_SYNCED_VALUE)
    }

    const before = JSON.stringify(getLayout())
    resetToStock()
    expect(JSON.stringify(getLayout())).not.toBe(before)

    importLayoutFields(fields)
    expect(JSON.stringify(getLayout())).toBe(before)
  })

  it('exports nothing at all for a stock layout', () => {
    // A box that has never been customised must not claim five keys to say
    // "default", and must not overwrite the layout its user chose on another
    // instance simply by having been opened.
    resetToStock()
    expect(exportLayoutFields()).toEqual({})
  })

  it('falls back to stock when a field arrives corrupt', () => {
    // Syncing must not create a new trust boundary: a value off the wire gets
    // the same validateLayout() a tampered localStorage value already got.
    applyPreset('taskbar')
    const fields = exportLayoutFields()
    importLayoutFields({ ...fields, 'shell.desktop.dock.desktop': '{"edge":"sideways"}' })
    expect(getLayout().presetId).toBe('vulos') // the stock preset
  })

  it('treats an empty set as stock, not as "leave it alone"', () => {
    applyPreset('taskbar')
    importLayoutFields({})
    expect(getLayout().presetId).toBe('vulos')
  })
})

describe('the widget rail, one key per placement', () => {
  it('has a populated registry — without which every test below is vacuous', () => {
    // Asserted rather than assumed. A rail test against an empty registry
    // compares [] to [] and reports success.
    expect(widgetCount()).toBeGreaterThan(0)
    expect(defaultLayout().instances.length).toBeGreaterThan(0)
  })

  it('round-trips a rail through values that each fit', () => {
    saveLayout(defaultLayout())
    const fields = exportRailFields()

    expect(Number(fields[WIDGETS_PREF_KEY_COUNT])).toBe(loadLayout().instances.length)
    for (const [k, v] of Object.entries(fields)) {
      expect(v.length, `${k} is ${v.length} bytes`).toBeLessThanOrEqual(MAX_SYNCED_VALUE)
    }

    const before = JSON.stringify(loadLayout())
    saveLayout({ version: 1, instances: [] })
    expect(loadLayout().instances).toHaveLength(0)

    importRailFields(fields)
    expect(JSON.stringify(loadLayout())).toBe(before)
  })

  it('carries a widget added here to the other box', () => {
    saveLayout(defaultLayout())
    const grown = addWidget(loadLayout(), 'vulos.clock')
    saveLayout(grown)

    const fields = exportRailFields()
    saveLayout({ version: 1, instances: [] })
    importRailFields(fields)

    expect(loadLayout().instances).toHaveLength(grown.instances.length)
  })

  it('leaves the rail alone when the box holds no count', () => {
    // Absent is not the same as "an empty rail". Hydration asserts only what it
    // was told, so a box that has never stored a rail does not empty this one.
    saveLayout(defaultLayout())
    const before = JSON.stringify(loadLayout())
    importRailFields({})
    expect(JSON.stringify(loadLayout())).toBe(before)
  })

  it('does not delete a widget this build cannot render', () => {
    // The worst defect in this pass, found by mutation testing rather than by
    // reading. The rail reconciles on read, so a placement whose widget this
    // build does not ship is DROPPED — correct for rendering. If the same box
    // then re-exported, it would push a shorter rail and the widget would
    // vanish from the box that CAN render it. A box that cannot understand a
    // placement must not become the authority on whether it exists.
    const real = defaultLayout().instances[0]
    const alien = { instanceId: 'wdeadbeefdeadbeef', widgetId: 'com.example.notinstalled', size: 'medium', settings: {}, granted: [] }

    importRailFields({
      'shell.widgets.count': '2',
      'shell.widgets.0': JSON.stringify(real),
      'shell.widgets.1': JSON.stringify(alien),
    })

    // Not rendered here…
    expect(loadLayout().instances.map((i) => i.widgetId)).not.toContain('com.example.notinstalled')
    expect(foreignPlacementCount()).toBe(1)

    // …but not destroyed either.
    const reExported = exportRailFields()
    expect(Number(reExported['shell.widgets.count'])).toBe(2)
    expect(Object.values(reExported).join(' ')).toContain('com.example.notinstalled')
  })

  it('stops carrying a placement once the other box removes it', () => {
    const real = defaultLayout().instances[0]
    const alien = { instanceId: 'wdeadbeefdeadbeef', widgetId: 'com.example.notinstalled', size: 'medium', settings: {}, granted: [] }
    importRailFields({
      'shell.widgets.count': '2',
      'shell.widgets.0': JSON.stringify(real),
      'shell.widgets.1': JSON.stringify(alien),
    })
    expect(foreignPlacementCount()).toBe(1)

    importRailFields({ 'shell.widgets.count': '1', 'shell.widgets.0': JSON.stringify(real) })

    expect(foreignPlacementCount()).toBe(0)
    expect(Object.values(exportRailFields()).join(' ')).not.toContain('com.example.notinstalled')
  })

  it('applies an explicitly empty rail', () => {
    // …but a count of 0 IS a statement: the user emptied their rail on the
    // other box, and re-adding five widgets here would be the OS overruling
    // them, which is the same rule loadLayout already follows locally.
    saveLayout(defaultLayout())
    importRailFields({ [WIDGETS_PREF_KEY_COUNT]: '0' })
    expect(loadLayout().instances).toHaveLength(0)
  })
})

describe('the inventory document and the code agree', () => {
  const doc = readFileSync(
    resolve(import.meta.dirname, '../../../roadmap/USER-STATE-INVENTORY.md'),
    'utf8',
  )

  it('names every bag key the shell actually writes', () => {
    // A document cannot fail a build, so this makes one that can. The failure
    // mode it prevents is the ordinary one: a preference is added, the code is
    // right, and the inventory keeps describing an OS that no longer exists.
    for (const key of [
      ...Object.values(THEME_PREF_KEYS),
      WALLPAPER_PREF_KEY, DOCK_PINS_PREF_KEY, DENSITY_PREF_KEY,
      AI_FIRSTRUN_PREF_KEY, NOTIFY_PREF_KEY, WIDGETS_PREF_KEY_COUNT,
      ...DESKTOP_PREF_KEYS,
    ]) {
      expect(doc, `${key} is written by the shell but absent from the inventory`).toContain(key)
    }
  })

  it('still records the counts it was written with', () => {
    expect(doc).toContain('15 SYNC, 5 PER-BOX, 4 EPHEMERAL')
  })
})

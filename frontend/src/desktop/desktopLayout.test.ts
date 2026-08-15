/**
 * Guards for the desktop customization model.
 *
 * These are the tests that make the security boundary real rather than
 * documented. Each hostile-pack case names the attack it closes; if
 * validate.ts's corresponding rule is deleted, the matching case goes red.
 * (Verified by mutation — see the commit message.)
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

import {
  DESKTOP_MAX_ITEMS, MOBILE_MAX_ITEMS, TOKEN_ALLOWLIST,
} from './types'
import { validateDockProfile, validateLayout, validatePack, validateTokenValue } from './validate'
import { DEFAULT_PRESET_ID, LAYOUT_PRESETS, describePreset, presetLayout, stockLayout } from './presets'
import {
  __resetStoreForTests, activeFormFactor, applyPreset, getDockProfile, getLayout,
  installPack, installedPackList, isStock, resetToStock, setTokens, uninstallPack, updateDock,
} from './store'

/** A minimal pack that validates, used as the base for every hostile mutation. */
function goodPack(): Record<string, unknown> {
  return {
    format: 'vulos.desktop.pack',
    version: 1,
    id: 'test-pack',
    name: 'Test pack',
    familiar: 'Matches nothing in particular',
    summary: 'A pack used by the test suite.',
    layout: {
      dock: {
        desktop: {
          edge: 'bottom', size: 'medium', style: 'floating', align: 'center',
          autohide: false, launcher: true, assistant: true, drawer: false, items: ['home'],
        },
        mobile: {
          edge: 'bottom', size: 'large', style: 'bar', align: 'center',
          autohide: false, launcher: false, assistant: true, drawer: true, items: ['home'],
        },
      },
      windowControls: 'left',
      tokens: {},
    },
  }
}

/** Reach into a nested plain object without fighting the type system. */
function at(pack: Record<string, unknown>, path: string[]): Record<string, unknown> {
  let node = pack as Record<string, unknown>
  for (const key of path) node = node[key] as Record<string, unknown>
  return node
}

/**
 * A real Storage, because this environment does not have one.
 *
 * Under Node 25 + vitest 4 the `localStorage` global is Node's own web-storage
 * object, which shadows jsdom's and — without --localstorage-file — is an empty
 * object with no getItem/setItem/clear at all. The store's try/catch means it
 * degrades to "no persistence" rather than throwing, so a persistence test
 * written against it would pass by measuring nothing. Installing a working
 * Storage is what makes these assertions real.
 */
function installStorage(): void {
  const map = new Map<string, string>()
  const storage = {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => { map.set(k, String(v)) },
    removeItem: (k: string) => { map.delete(k) },
    clear: () => { map.clear() },
    key: (i: number) => [...map.keys()][i] ?? null,
    get length() { return map.size },
  }
  Object.defineProperty(globalThis, 'localStorage', { value: storage, configurable: true, writable: true })
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true, writable: true })
}

beforeEach(() => {
  installStorage()
  localStorage.clear()
  document.documentElement.removeAttribute('style')
  __resetStoreForTests()
})

describe('the shipped presets', () => {
  it('all validate against the same rules a third-party pack must meet', () => {
    for (const preset of LAYOUT_PRESETS) {
      const result = validateLayout({ presetId: preset.id, ...preset.layout }, preset.id)
      expect(result.errors, `preset "${preset.id}" is not a valid layout`).toEqual([])
      expect(result.ok).toBe(true)
    }
  })

  it('includes the Vulos default, and it is the stock layout', () => {
    expect(LAYOUT_PRESETS.some((p) => p.id === DEFAULT_PRESET_ID)).toBe(true)
    expect(stockLayout().presetId).toBe(DEFAULT_PRESET_ID)
  })

  it('ships at least one preset authored THROUGH the public pack format', () => {
    // presets.ts parses packs/side-dock.pack.json with the public validatePack()
    // at module load. If the format could not express a shipping preset, that
    // import would have thrown before this test ran.
    const fromPack = LAYOUT_PRESETS.filter((p) => p.source === 'pack')
    expect(fromPack.length).toBeGreaterThanOrEqual(1)
    expect(fromPack.map((p) => p.id)).toContain('side-dock')
  })

  it('covers the three arriving-from platforms plus the Vulos default', () => {
    const ids = LAYOUT_PRESETS.map((p) => p.id)
    expect(ids).toEqual(expect.arrayContaining(['vulos', 'taskbar', 'menubar-dock', 'side-dock']))
  })

  it('keeps every mobile dock on a horizontal edge with a reachable item count', () => {
    for (const preset of LAYOUT_PRESETS) {
      const m = preset.layout.dock.mobile
      expect(['bottom', 'top'], `${preset.id} mobile edge`).toContain(m.edge)
      expect(m.items.length, `${preset.id} mobile items`).toBeLessThanOrEqual(MOBILE_MAX_ITEMS)
      expect(m.drawer, `${preset.id} mobile drawer`).toBe(true)
    }
  })

  it('gives every preset a preview-safe description with no markup', () => {
    for (const preset of LAYOUT_PRESETS) {
      const text = describePreset(preset)
      expect(text.length).toBeGreaterThan(20)
      expect(text).not.toMatch(/[<>]/)
    }
  })
})

describe('hostile packs are rejected', () => {
  it('rejects a remote url() smuggled into a token value — the CSS exfiltration channel', () => {
    const pack = goodPack()
    // Short enough to clear the length cap, so this case fails for the reason
    // it claims — the url( substring rule — and not incidentally.
    at(pack, ['layout']).tokens = { '--vd-accent': 'url(//a.co/x)' }
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/forbidden sequence "url\("/)
  })

  it('rejects an over-long token value outright', () => {
    const r = validateTokenValue('--vd-accent', '#'.padEnd(40, 'a'))
    expect(r.ok).toBe(false)
    expect(r.errors.join(' ')).toMatch(/1–32 characters/)
  })

  it('rejects a token that is not on the allowlist — no reaching the trust chrome', () => {
    const pack = goodPack()
    // The shape of the attack: repaint or reposition the shell's security
    // surfaces (TrustBadge, PublicAppBanner, SharedDesktopNotice) by setting a
    // property they read. There is no allowlist entry that they read, so the
    // only way in would be an unknown property name — which is an error.
    at(pack, ['layout']).tokens = { '--bg-elevated': '#000000' }
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/not an allowlisted custom property/)
  })

  it('rejects a raw CSS declaration smuggled as an extra key', () => {
    const pack = goodPack()
    // A validator that ignored unknown keys would accept this, and the next
    // person to add a passthrough would ship the injection.
    ;(pack as Record<string, unknown>).css = '[aria-label="Sovereignty and privacy status"]{display:none}'
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/unknown key "css"/)
  })

  it('rejects an extra key inside a dock profile', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'desktop']).style_override = 'position:fixed;inset:0'
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/unknown key "style_override"/)
  })

  it('rejects an opacity that would make the dock invisible', () => {
    const pack = goodPack()
    at(pack, ['layout']).tokens = { '--vd-dock-opacity': '0' }
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/below the minimum of 0\.6/)
  })

  it('rejects a radius beyond the range the dock chrome is drawn for', () => {
    const pack = goodPack()
    at(pack, ['layout']).tokens = { '--vd-dock-radius': '400px' }
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/above the maximum of 28px/)
  })

  it('rejects the 36px small tile on a phone dock — every target is a thumb', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'mobile']).size = 'small'
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/dock\.mobile\.size: expected one of medium \| large/)
  })

  it('still allows the small tile on a desktop dock, where a pointer is precise', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'desktop']).size = 'small'
    expect(validatePack(pack).ok).toBe(true)
  })

  it('rejects a second declaration hidden behind a semicolon', () => {
    const r = validateTokenValue('--vd-dock-radius', '8px; position: fixed')
    expect(r.ok).toBe(false)
    expect(r.errors.join(' ')).toMatch(/forbidden sequence ";"/)
  })

  it('rejects a data: URI', () => {
    const r = validateTokenValue('--vd-accent', 'data:image/svg+xml,x')
    expect(r.ok).toBe(false)
  })

  it('rejects a var() indirection that could re-read a protected token', () => {
    const r = validateTokenValue('--vd-accent', 'var(--x)')
    expect(r.ok).toBe(false)
    expect(r.errors.join(' ')).toMatch(/forbidden sequence "var\("/)
  })

  it('rejects a vertical dock on a phone — beautiful at 1440px, broken at 390px', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'mobile']).edge = 'left'
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/expected one of bottom \| top/)
  })

  it('rejects a phone dock that drops the app drawer', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'mobile']).drawer = false
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(/must keep the app-drawer affordance/)
  })

  it('rejects more phone dock items than fit a 390px thumb reach', () => {
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'mobile']).items = ['a', 'b', 'c', 'd', 'e', 'f']
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(new RegExp(`exceeds the mobile maximum of ${MOBILE_MAX_ITEMS}`))
  })

  it('rejects more desktop dock items than fit a 768px viewport', () => {
    const items = Array.from({ length: DESKTOP_MAX_ITEMS + 1 }, (_, i) => `app-${i}`)
    const pack = goodPack()
    at(pack, ['layout', 'dock', 'desktop']).items = items
    const result = validatePack(pack)
    expect(result.ok).toBe(false)
    expect(result.errors.join(' ')).toMatch(new RegExp(`exceeds the desktop maximum of ${DESKTOP_MAX_ITEMS}`))
  })

  it('rejects an app id that is a URL or a path', () => {
    for (const bad of ['https://evil.example/x', '../../etc/passwd', 'Home', 'a b']) {
      const pack = goodPack()
      at(pack, ['layout', 'dock', 'desktop']).items = [bad]
      expect(validatePack(pack).ok, `"${bad}" should be rejected`).toBe(false)
    }
  })

  it('rejects the wrong format discriminator and the wrong version', () => {
    const wrongFormat = goodPack(); wrongFormat.format = 'vulos.theme'
    expect(validatePack(wrongFormat).ok).toBe(false)
    const wrongVersion = goodPack(); wrongVersion.version = 2
    expect(validatePack(wrongVersion).ok).toBe(false)
  })

  it('accepts the base pack, so the cases above fail for the reason claimed', () => {
    // Without this, every assertion above could be passing because goodPack()
    // itself is invalid — the "hollow gate" shape this repo keeps finding.
    const result = validatePack(goodPack())
    expect(result.errors).toEqual([])
    expect(result.ok).toBe(true)
  })
})

describe('the store treats validation as a boundary', () => {
  it('falls back to stock when localStorage holds a hand-edited layout', () => {
    localStorage.setItem('vulos.desktop.layout', JSON.stringify({
      presetId: 'evil',
      dock: { desktop: { edge: 'bottom' }, mobile: {} },
      windowControls: 'left',
      tokens: { '--bg-elevated': 'url(https://attacker.example)' },
      css: 'body{display:none}',
    }))
    __resetStoreForTests()
    expect(getLayout().presetId).toBe(DEFAULT_PRESET_ID)
    expect(document.documentElement.style.getPropertyValue('--bg-elevated')).toBe('')
  })

  it('drops an installed pack that no longer validates instead of repairing it', () => {
    const bad = goodPack()
    at(bad, ['layout']).tokens = { '--vd-dock-opacity': '0' }
    localStorage.setItem('vulos.desktop.packs', JSON.stringify([bad]))
    __resetStoreForTests()
    expect(installedPackList()).toEqual([])
  })

  it('refuses an out-of-range token from inside the app too', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    applyPreset('taskbar')
    const before = getLayout().tokens['--vd-dock-radius']
    setTokens({ '--vd-dock-opacity': '0.1' })
    expect(getLayout().tokens['--vd-dock-opacity']).toBeUndefined()
    expect(getLayout().tokens['--vd-dock-radius']).toBe(before)
    spy.mockRestore()
  })

  it('only ever writes allowlisted custom properties to the root element', () => {
    applyPreset('taskbar')
    const inline = document.documentElement.getAttribute('style') || ''
    for (const decl of inline.split(';')) {
      const name = decl.split(':')[0].trim()
      if (!name) continue
      expect(Object.keys(TOKEN_ALLOWLIST), `unexpected inline property ${name}`).toContain(name)
    }
  })
})

describe('applying and reverting', () => {
  it('applies a preset to the root element as data attributes', () => {
    applyPreset('taskbar')
    const el = document.documentElement
    expect(el.getAttribute('data-desktop-preset')).toBe('taskbar')
    expect(el.getAttribute('data-dock-edge')).toBe('bottom')
    expect(el.getAttribute('data-dock-style')).toBe('bar')
    expect(el.getAttribute('data-dock-align')).toBe('start')
    expect(el.getAttribute('data-window-controls')).toBe('right')
  })

  it('reverts to stock in one action, clearing attributes and tokens', () => {
    applyPreset('side-dock')
    expect(document.documentElement.getAttribute('data-dock-edge')).toBe('left')
    expect(document.documentElement.style.getPropertyValue('--vd-window-radius')).toBe('8px')
    expect(isStock()).toBe(false)

    resetToStock()

    expect(isStock()).toBe(true)
    expect(getLayout().presetId).toBe(DEFAULT_PRESET_ID)
    expect(document.documentElement.getAttribute('data-dock-edge')).toBe('bottom')
    expect(document.documentElement.getAttribute('data-window-controls')).toBe('left')
    expect(document.documentElement.style.getPropertyValue('--vd-window-radius')).toBe('')
    expect(localStorage.getItem('vulos.desktop.layout')).toBeNull()
  })

  it('reverts even from a layout that was persisted before the reset', () => {
    applyPreset('menubar-dock')
    expect(localStorage.getItem('vulos.desktop.layout')).toContain('menubar-dock')
    resetToStock()
    __resetStoreForTests() // simulate a reload
    expect(getLayout().presetId).toBe(DEFAULT_PRESET_ID)
  })

  it('reverts on the Ctrl+Alt+Shift+Backspace hotkey, from anywhere', () => {
    applyPreset('side-dock')
    expect(getLayout().presetId).toBe('side-dock')
    window.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Backspace', ctrlKey: true, altKey: true, shiftKey: true, bubbles: true, cancelable: true,
    }))
    expect(getLayout().presetId).toBe(DEFAULT_PRESET_ID)
  })

  it('does not revert on a near-miss chord', () => {
    applyPreset('side-dock')
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace', ctrlKey: true, shiftKey: true, bubbles: true }))
    expect(getLayout().presetId).toBe('side-dock')
  })
})

describe('the two form factors are independent', () => {
  it('editing the desktop dock leaves the phone dock untouched', () => {
    applyPreset('vulos')
    const phoneBefore = JSON.stringify(getDockProfile('mobile'))
    updateDock('desktop', { edge: 'left', style: 'bar', autohide: true })
    expect(getDockProfile('desktop').edge).toBe('left')
    expect(getDockProfile('desktop').autohide).toBe(true)
    expect(JSON.stringify(getDockProfile('mobile'))).toBe(phoneBefore)
  })

  it('editing the phone dock leaves the desktop dock untouched', () => {
    applyPreset('vulos')
    const deskBefore = JSON.stringify(getDockProfile('desktop'))
    updateDock('mobile', { size: 'medium', items: ['home', 'files'] })
    expect(getDockProfile('mobile').items).toEqual(['home', 'files'])
    expect(JSON.stringify(getDockProfile('desktop'))).toBe(deskBefore)
  })

  it('refuses a phone-illegal edit to the phone dock', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {})
    applyPreset('vulos')
    updateDock('mobile', { edge: 'left' })
    expect(getDockProfile('mobile').edge).toBe('bottom')
    spy.mockRestore()
  })

  it('allows a vertical dock on the desktop profile', () => {
    const r = validateDockProfile(
      { ...presetLayout('vulos').dock.desktop, edge: 'right' }, 'desktop', 'x',
    )
    expect(r.ok).toBe(true)
  })

  it('reports a form factor that mirrors the shell breakpoint', () => {
    // jsdom's default window is 1024px wide.
    expect(activeFormFactor()).toBe('desktop')
  })
})

describe('third-party packs install and uninstall', () => {
  it('installs a valid pack, makes it selectable, and applies it', () => {
    const res = installPack(goodPack())
    expect(res.errors).toEqual([])
    expect(res.ok).toBe(true)
    applyPreset('test-pack')
    expect(getLayout().presetId).toBe('test-pack')
  })

  it('reports the errors a developer needs, rather than a bare false', () => {
    const bad = goodPack()
    at(bad, ['layout', 'dock', 'mobile']).edge = 'right'
    const res = installPack(bad)
    expect(res.ok).toBe(false)
    expect(res.errors.length).toBeGreaterThan(0)
    expect(res.errors[0]).toContain('dock.mobile.edge')
  })

  it('refuses to shadow a built-in preset id', () => {
    const shadow = goodPack()
    shadow.id = DEFAULT_PRESET_ID
    const res = installPack(shadow)
    expect(res.ok).toBe(false)
    expect(res.errors.join(' ')).toMatch(/collides with a built-in preset/)
  })

  it('falls back to stock when the active pack is uninstalled', () => {
    installPack(goodPack())
    applyPreset('test-pack')
    uninstallPack('test-pack')
    expect(getLayout().presetId).toBe(DEFAULT_PRESET_ID)
  })
})

import { describe, expect, it } from 'vitest'
import {
  MOBILE_MAX_SLOTS, MOBILE_TILE, NARROWEST_PHONE, SYSTEM_DESTINATION_APP_IDS,
  mobileDockAutohide, mobileDockSlots, mobileSlotWidth, type MobileSlot,
} from '../mobileDock'
import { MOBILE_MAX_ITEMS, stockLayout, type DockProfile } from '../../desktop'

/**
 * The phone dock's contents, as arithmetic over a validated profile.
 *
 * These assert the CONTRACT, not the rendering: which slots exist, in what
 * order, and which profile fields move them. The rendering is proved by
 * e2e/mobile-dock.e2e.ts on a real touch device profile, because a jsdom
 * assertion cannot see a 48px column or a dock 350px off the bottom of a screen.
 */

function profile(patch: Partial<DockProfile> = {}): DockProfile {
  return {
    edge: 'bottom', size: 'large', style: 'bar', align: 'center',
    autohide: false, launcher: false, assistant: true, drawer: true,
    items: [],
    ...patch,
  }
}

const REGISTRY = ['home', 'lilmail', 'messages', 'files', 'notes', 'terminal', 'drive', 'persona']

function kinds(slots: MobileSlot[]): string[] {
  return slots.map((s) => (s.kind === 'app' ? `app:${s.appId}` : s.kind))
}

describe('mobileDockSlots', () => {
  it('always emits Home and Apps first, even for an empty, drawerless profile', () => {
    // Home is the only way out of a fullscreen app on a device with no window
    // chrome; Apps is the only way to close a running app without opening it.
    // Neither is expressible in `items`, so neither can be removed.
    expect(kinds(mobileDockSlots(profile({ items: [], drawer: false }), REGISTRY)))
      .toEqual(['home', 'switcher'])
  })

  it('puts the profile items on the dock, in the profile order', () => {
    // THE founder request: different items on the mobile dock.
    expect(kinds(mobileDockSlots(profile({ items: ['terminal', 'lilmail'] }), REGISTRY)))
      .toEqual(['home', 'switcher', 'app:terminal', 'app:lilmail', 'library'])
  })

  it('skips an item naming an app that is not installed', () => {
    // The registry repopulates after boot, so an unknown id is "not yet" as
    // often as it is "never" — it must not render as a broken tile either way.
    expect(kinds(mobileDockSlots(profile({ items: ['lilmail', 'nope-not-installed'] }), REGISTRY)))
      .toEqual(['home', 'switcher', 'app:lilmail', 'library'])
  })

  it('folds a docked `home` item into the system Home slot', () => {
    // The STOCK mobile profile ships items: ['home', 'lilmail', 'messages'].
    // Rendering it would put two adjacent dock targets both called "Home" that
    // go to different places — and two buttons with the same accessible name in
    // one toolbar.
    expect(SYSTEM_DESTINATION_APP_IDS).toContain('home')
    const slots = kinds(mobileDockSlots(profile({ items: ['home', 'lilmail'] }), REGISTRY))
    expect(slots).toEqual(['home', 'switcher', 'app:lilmail', 'library'])
    expect(slots.filter((s) => s === 'home' || s === 'app:home')).toHaveLength(1)
  })

  it('is what the stock preset actually produces', () => {
    // Not a hand-built profile: the real shipped default, through the real store.
    const stock = stockLayout().dock.mobile
    expect(stock.items).toEqual(['home', 'lilmail', 'messages'])
    expect(kinds(mobileDockSlots(stock, REGISTRY)))
      .toEqual(['home', 'switcher', 'app:lilmail', 'app:messages', 'library'])
  })

  it('de-duplicates a repeated item', () => {
    expect(kinds(mobileDockSlots(profile({ items: ['files', 'files', 'notes'] }), REGISTRY)))
      .toEqual(['home', 'switcher', 'app:files', 'app:notes', 'library'])
  })

  it('caps at MOBILE_MAX_ITEMS even if a profile arrives with more', () => {
    // validate.ts rejects a sixth item, so this is defensive — but a hand-built
    // profile overflowing here would be a 40px touch target, not a caught error.
    const six = ['lilmail', 'messages', 'files', 'notes', 'terminal', 'drive']
    expect(six.length).toBeGreaterThan(MOBILE_MAX_ITEMS)
    const slots = mobileDockSlots(profile({ items: six }), REGISTRY)
    expect(slots.filter((s) => s.kind === 'app')).toHaveLength(MOBILE_MAX_ITEMS)
  })

  it('emits the Library slot for `drawer`', () => {
    expect(kinds(mobileDockSlots(profile({ drawer: true }), REGISTRY))).toContain('library')
    expect(kinds(mobileDockSlots(profile({ drawer: false, launcher: false }), REGISTRY))).not.toContain('library')
  })

  it('subsumes `launcher` into the Library slot rather than dropping it', () => {
    // On a phone the launcher and the drawer are ONE surface: the Library slot
    // opens the Launchpad, which is itself the searchable app list. A second
    // control for it would be a duplicate, so `launcher` reaches the same slot.
    expect(kinds(mobileDockSlots(profile({ drawer: false, launcher: true }), REGISTRY))).toContain('library')
  })
})

describe('mobileDockAutohide', () => {
  it('refuses autohide for every profile', () => {
    // There is no hover on touch to bring a hidden dock back, and this is the
    // phone's only navigation surface — an autohidden one is a UI you cannot
    // navigate out of. Same class of defect the --vd-dock-opacity floor exists
    // to prevent, so it is refused here rather than silently obeyed.
    expect(mobileDockAutohide(profile({ autohide: true }))).toBe(false)
    expect(mobileDockAutohide(profile({ autohide: false }))).toBe(false)
  })
})

describe('the 44px arithmetic', () => {
  it('MOBILE_MAX_SLOTS is Home + Apps + the item cap + Library', () => {
    expect(MOBILE_MAX_SLOTS).toBe(2 + MOBILE_MAX_ITEMS + 1)
    const full = mobileDockSlots(
      profile({ items: ['lilmail', 'messages', 'files', 'notes', 'terminal'], drawer: true }),
      REGISTRY,
    )
    expect(full).toHaveLength(MOBILE_MAX_SLOTS)
  })

  it('every slot clears the 44px platform floor at the narrowest supported phone', () => {
    // 390px, the width desktop/types.ts derives MOBILE_MAX_ITEMS from, with the
    // 8px safe-area gutter the dock actually applies.
    const width = mobileSlotWidth(MOBILE_MAX_SLOTS, NARROWEST_PHONE, 8)
    expect(width).toBeGreaterThanOrEqual(44)
    // And WCAG 2.5.8's floor, which is the one that is normative.
    expect(width).toBeGreaterThanOrEqual(24)
  })

  it('a ninth slot would NOT clear it — which is why the cap exists', () => {
    expect(mobileSlotWidth(MOBILE_MAX_SLOTS + 1, NARROWEST_PHONE, 8)).toBeLessThan(44)
  })
})

describe('MOBILE_TILE', () => {
  it('maps the two sizes a phone may use to their plates', () => {
    expect(MOBILE_TILE.medium.plate).toBe(44)
    expect(MOBILE_TILE.large.plate).toBe(56)
  })

  it('is total over DockSize so no lookup can be undefined', () => {
    // A partial record would make the lookup `| undefined` and invite a `?? 44`
    // fallback — a second source of truth for the one value tile size exists to
    // be. `small` is unreachable on a phone (validate.ts rejects it) but present.
    expect(Object.keys(MOBILE_TILE).sort()).toEqual(['large', 'medium', 'small'])
  })
})

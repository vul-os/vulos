import { MOBILE_MAX_ITEMS, type DockProfile, type DockSize } from '../desktop'

/**
 * The mobile dock, derived from the customization model's `mobile` dock profile.
 *
 * # Why this is a separate, pure module
 *
 * `layouts/MobileStack.tsx` renders the dock; this file DECIDES what is in it.
 * The decision is arithmetic over a validated profile, so it is worth being able
 * to test it without a DOM, a viewport or a registry — and worth having exactly
 * one place where "what does a phone dock contain" is answered, rather than a
 * chain of `&&` inside JSX that no test can address.
 *
 * # The contract this consumes
 *
 * `src/desktop`'s `DockProfile` is keyed by form factor and the two profiles
 * persist separately (see desktop/store.ts). `validate.ts` has already rejected
 * everything unusable on a phone before this module ever sees a profile:
 *   · a vertical dock (MOBILE_EDGES = bottom | top)
 *   · the `small` 36px tile (MOBILE_SIZES = medium | large)
 *   · a sixth item (MOBILE_MAX_ITEMS = 5)
 *   · `drawer: false` (a phone strip cannot reach the library any other way)
 * So this module does not re-clamp for correctness — it clamps DEFENSIVELY only,
 * because it is also called with hand-built profiles from unit tests and a
 * silent overflow would be a 40px touch target rather than a caught error.
 *
 * # Which fields the phone shell honours, and which it refuses
 *
 * Honoured, and each one is observable in the rendered dock:
 *   `items`     — the app tiles. THIS is "different items on the mobile dock".
 *   `edge`      — bottom (default) or top; the strip moves and the safe-area
 *                 padding moves with it.
 *   `size`      — medium/large → a 44px or 56px plate behind the mark.
 *   `style`     — `bar` spans the edge flush; `floating` is an inset island.
 *   `align`     — where a FLOATING island sits along its edge. A full-span bar
 *                 has no alignment to have, on any platform.
 *   `drawer`    — the Library slot. Mandatory per the validator; honoured
 *                 anyway, so the flag is not a lie in a hand-built profile.
 *   `assistant` — the resting ask bar on the phone home screen. On desktop this
 *                 flag shows a dock button because the assistant is a side
 *                 PANEL there; on a phone the assistant is a full-screen surface
 *                 reached from Home, so that is where the flag lands.
 *
 * REFUSED, deliberately, with the reason stated rather than silently dropped:
 *
 *   `autohide` — a phone dock is the ONLY navigation surface (there is no menu
 *                bar, no window chrome, no keyboard) and there is no hover on
 *                touch to bring a hidden one back. An autohidden phone dock is a
 *                UI you cannot navigate out of, which is the same failure the
 *                `--vd-dock-opacity` floor exists to prevent. The phone shell
 *                pins the dock and `mobileDockAutohide()` returns false for
 *                every profile, so the refusal is testable rather than implied.
 *
 *   `launcher` — on desktop the launcher (⌘Space) and the drawer are two
 *                surfaces; on the phone shell they are ONE. The Library slot
 *                opens the Launchpad, which is itself the searchable app list,
 *                and the validator makes `drawer` mandatory — so a launcher slot
 *                would be a second control for a surface that is already always
 *                present. It is subsumed, not ignored: `mobileDockSlots()` emits
 *                the library slot for `launcher || drawer`.
 *
 * # Home and Apps are not removable
 *
 * Home is the only way back out of a fullscreen app on a device with no window
 * chrome, and Apps is the only way to close a running app without opening it
 * first. Removing either strands the user, so neither is expressible in `items`
 * and both are emitted unconditionally. See roadmap/MOBILE-SHELL.md §4.2.
 */

/** One thing in the phone dock. */
export type MobileSlot =
  | { kind: 'home' }
  | { kind: 'switcher' }
  | { kind: 'library' }
  | { kind: 'app'; appId: string }

/**
 * App ids that name a system destination this shell already has a slot for.
 *
 * `home` is in the STOCK mobile profile's items (desktop/presets.ts), and the
 * Home *app* ("your day at a glance") is a genuinely different thing from the
 * phone home *screen*. But two adjacent dock targets both labelled "Home" that
 * go to different places is a navigation defect, not a customization — and the
 * accessible name collides inside a single toolbar. So a docked `home` item
 * folds into the system slot; the Home app stays launchable from the home grid
 * and the Library.
 */
export const SYSTEM_DESTINATION_APP_IDS: readonly string[] = ['home']

/**
 * Plate and glyph geometry per tile size — ONE source of truth, exactly as
 * shell/Dock.tsx's TILE is for the desktop, and the reason tile size is an enum
 * rather than a length token.
 *
 * `small` is unreachable on a phone (the validator rejects it, see
 * MOBILE_SIZES) and is present only so this is a total map over DockSize: a
 * partial record would make the lookup `| undefined` and invite a `?? 44`
 * fallback that would quietly become a second source of truth.
 */
export const MOBILE_TILE: Readonly<Record<DockSize, { plate: number; glyph: number }>> = Object.freeze({
  small: { plate: 36, glyph: 20 },
  medium: { plate: 44, glyph: 22 },
  large: { plate: 56, glyph: 26 },
})

/**
 * The gap that has to survive between two adjacent marks, in px.
 *
 * Not decoration. Screenshotted at 390×844 with the maximum eight slots and the
 * `large` tile: each column is 48.7px wide, the plate was a fixed 56px, and the
 * five app marks rendered edge-to-edge as one continuous strip of colour with no
 * separation at all — five apps that read as one object. The touch targets were
 * fine (the BUTTON is the target, and it measured 48.7px); it was only visible
 * by looking at the picture.
 */
export const DOCK_PLATE_GAP = 8

/**
 * The plate floor. Below this the mark stops being identifiable, and at that
 * point a dock is better off with fewer items than with unrecognisable ones —
 * which is what MOBILE_MAX_ITEMS is for.
 */
export const DOCK_PLATE_MIN = 28

/**
 * How big the plate may actually be drawn, given the column it has to sit in.
 *
 * `size` is the profile's REQUEST; this is what fits. AppIconTile takes a
 * numeric px size and writes it as an inline width/height, so a mark cannot be
 * shrunk by CSS after the fact — the number has to be right before it renders,
 * which is why this is arithmetic over a measured column rather than a
 * max-width.
 */
export function mobileDockPlate(size: DockSize, slotWidth: number): number {
  const requested = MOBILE_TILE[size].plate
  if (!Number.isFinite(slotWidth) || slotWidth <= 0) return requested
  return Math.max(DOCK_PLATE_MIN, Math.min(requested, Math.floor(slotWidth) - DOCK_PLATE_GAP))
}

/** The glyph inside a system slot's plate. Half the plate, as on the desktop. */
export function mobileDockGlyph(plate: number): number {
  return Math.round(plate * 0.5)
}

/**
 * Build the dock's slots for a validated mobile profile.
 *
 * `knownAppIds` is the live app registry. An item naming an app that is not
 * installed is SKIPPED rather than rendered as a broken tile — the same rule
 * shell/Dock.tsx applies, and it matters here because the registry repopulates
 * after boot, so the dock legitimately gains tiles a moment after first paint.
 */
export function mobileDockSlots(profile: DockProfile, knownAppIds: readonly string[]): MobileSlot[] {
  const known = new Set(knownAppIds)
  const slots: MobileSlot[] = [{ kind: 'home' }, { kind: 'switcher' }]

  const seen = new Set<string>()
  for (const id of profile.items) {
    if (seen.size >= MOBILE_MAX_ITEMS) break
    if (seen.has(id)) continue
    if (SYSTEM_DESTINATION_APP_IDS.includes(id)) continue
    if (!known.has(id)) continue
    seen.add(id)
    slots.push({ kind: 'app', appId: id })
  }

  if (profile.drawer || profile.launcher) slots.push({ kind: 'library' })
  return slots
}

/**
 * Always false. See the header — an autohidden dock on a hoverless device is
 * unreachable, and this shell refuses it for every profile. A function rather
 * than a constant so the refusal has a call site a test can point at, and so a
 * future device with a hovering pointer has somewhere to become an exception.
 */
export function mobileDockAutohide(_profile: DockProfile): boolean {
  void _profile
  return false
}

/**
 * How wide one slot gets, in px, at a given viewport width.
 *
 * The arithmetic the 44px floor rests on, written down so a test can assert it
 * rather than a screenshot having to notice it went wrong. `--safe-left/right`
 * are unknown here, so the caller passes the gutter it actually applies.
 */
export function mobileSlotWidth(slotCount: number, viewportWidth: number, gutter = 0): number {
  if (slotCount <= 0) return 0
  return (viewportWidth - gutter * 2) / slotCount
}

/**
 * The narrowest viewport this OS claims to support (desktop/types.ts derives
 * MOBILE_MAX_ITEMS from it).
 */
export const NARROWEST_PHONE = 390

/**
 * The most slots a phone dock can hold: Home + Apps + MOBILE_MAX_ITEMS + Library.
 *
 * At 390px that is 8 slots of 48.75px, which clears the 44px platform floor and
 * WCAG 2.5.8's 24px one. It is also why app tiles carry no visible caption at
 * this density — a 48px column cannot hold a legible app name, and a truncated
 * dock label is worse than no label (roadmap/MOBILE-SHELL.md §4.1). The mark
 * plus the running dot is the identity, exactly as on the desktop dock, and the
 * name is on the tile's accessible name where it is never truncated.
 */
export const MOBILE_MAX_SLOTS = 2 + MOBILE_MAX_ITEMS + 1

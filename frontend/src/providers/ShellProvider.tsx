import { createContext, useContext, useReducer, useCallback, useEffect, useMemo, useRef, type ReactNode } from 'react'
import { useViewport } from '../shell/useViewport'
import { canSpawnNativeWindow, getNativeMode } from '../core/useNativeMode'
import { tileGeometry, openWindowGeometry, DEFAULT_WINDOW_SIZE, MENU_BAR_H, DOCK_H, WINDOW_EDGE_MARGIN } from '../shell/windowTiling'
import { builtinComponent, isBuiltinComponent } from '../shell/builtinApps'
import { useShellSession, type ShellSession } from './useShellSession'
import { useViewportFocus } from './viewportFocus'
import {
  readViewportSize, viewportPx,
  toCanonicalPoint, fromCanonicalPoint,
  toCanonicalSize, fromCanonicalSize,
  isPlausibleCanonicalUnit,
  type ViewportSize, type CanonicalUnit,
} from './screenScale'

// ─── Types ───────────────────────────────────────────────────────────────────
// The window shape + reducer actions are the heart of the window manager
// (BMINIT-18's thin-WM sync, tiling, and desktop/session persistence all pivot
// on them), so they're typed precisely rather than loosely. Everything here is
// derived from how the existing consumers (shell/Window.jsx, shell/Dock.jsx,
// shell/MissionControl.jsx, layouts/*, __tests__/shellStore.test.js) actually
// read/write these objects — not aspirational, just what's really there.

export interface WindowPosition { x: number; y: number }
export interface WindowSize { width: number; height: number }

/** Geometry a tile zone resolves to. Derived from windowTiling's own
 *  (JSDoc-typed) tileGeometry return type so the two never drift apart. */
type TileGeometry = NonNullable<ReturnType<typeof tileGeometry>>

/** Payload for the "save this AI-generated app" affordance on a window's
 *  title bar (see shell/Window.jsx's _saveable button). */
export interface SaveableAIApp {
  title: string
  html: string
  python?: string
}

/**
 * A single shell window. Only `id` / `appId` / `position` / `size` /
 * `minimized` are guaranteed on every window — everything else depends on how
 * it was opened (component vs. html vs. url) or is bookkeeping the reducer /
 * thin-WM sync / persistence layer attaches.
 */
export interface ShellWindow {
  id: number
  appId: string
  title?: string
  url?: string
  icon?: string
  component?: ReactNode | null
  html?: string | null
  _saveable?: SaveableAIApp | null
  position: WindowPosition
  size: WindowSize
  minimized: boolean
  // Tiling bookkeeping — written by TILE_WINDOW / MAXIMIZE_WINDOW, read by
  // MOVE_WINDOW / RESIZE_WINDOW (to free the tile) and useWindowShortcuts.
  _tile?: string | null
  _maximized?: boolean
  _prevPosition?: WindowPosition | null
  _prevSize?: WindowSize | null
  // BMINIT-18 thin WM: opaque wlr-foreign-toplevel-management handle used by
  // the /api/shell/windows/{focus,minimize} calls. Never validated beyond
  // `!= null`, so it's opaque rather than assumed to be a number or string.
  _wtHandle?: unknown
  // Persisted-shape marker (see saveShellState / loadShellState /
  // RESTORE_STATE) — only meaningful on a just-loaded/just-restored window.
  _builtin?: boolean
  // allWindows-only derived flags (see the allWindows useMemo below); never
  // present on the plain per-desktop `windows` array.
  _visible?: boolean
  _active?: boolean
}

export interface Desktop {
  id: string
  label: string
  windows: ShellWindow[]
  activeWindow: number | null
}

/** A spawned native (v2/labwc) window — BMINIT-04/06, gated behind
 *  canSpawnNativeWindow(). `pid` is whatever the backend's spawn response
 *  hands back (untyped JSON), used only as an opaque identity key. */
export interface NativeWindow {
  pid: unknown
  title?: string
  appId?: string
}

/** popoutApp/closePopout/`popout` are exported context surface with no live
 *  caller today (see shell/Popout.jsx for the sole reader) — shape inferred
 *  from what Popout.jsx reads off it. */
export interface PopoutApp {
  appId?: string
  url: string
  title?: string
  icon?: string
}

export interface ShellMessage {
  id: number
  role: string
  text: string
  meta?: unknown
  timestamp: Date
}

/** One entry from GET /api/shell/windows (wlr-foreign-toplevel state, polled
 *  in v2 native mode). `state` is treated as an array of active state enums
 *  (mirroring the wlr-foreign-toplevel-management protocol) because the
 *  reducer calls `.includes('minimized')` on it rather than `===` — the
 *  backend contract itself isn't visible from the frontend, so this is the
 *  shape implied by that usage, not a verified wire type. */
interface WlToplevel {
  handle: unknown
  app_id?: string
  title?: string
  state?: string[]
}

interface ShellState {
  desktops: Record<string, Desktop>
  activeDesktop: string
  popout: PopoutApp | null
  nativeWindows: NativeWindow[]
  conversation: ShellMessage[]
  thinking: boolean
  launchpadOpen: boolean
  chatOpen: boolean
  missionControlOpen: boolean
}

/** The subset of ShellState that actually survives a reload — see
 *  saveShellState, which only ever writes `desktops` + `activeDesktop`. */
interface SavedShellState {
  desktops: Record<string, Desktop>
  activeDesktop: string
}

export type ShellAction =
  | { type: 'OPEN_WINDOW'; appId: string; title?: string; url?: string; icon?: string; component?: ReactNode | null; html?: string | null; _saveable?: SaveableAIApp | null
      // `singleton` is accepted here (and passed by every caller that wants
      // dedup — shell/launchApp.js, shell/home/Home.jsx) but openWindow()
      // below does NOT forward it to this action today, so it is currently
      // inert. Preserved exactly as-is; not a bug this pass fixes.
      ; singleton?: boolean
      // Optional initial size, opt-in per caller. Every window has opened at
      // a flat 720x500 regardless of app; every caller but Settings' still
      // omits this and gets that unchanged default. Settings passes a larger
      // size because its nav rail + content (Cards up to max-w-2xl) made
      // nearly every panel start pre-scrolled at 720x500 — see Portal.tsx.
      ; size?: WindowSize }
  | { type: 'CLOSE_WINDOW'; id: number }
  | { type: 'FOCUS_WINDOW'; id: number }
  | { type: 'MOVE_WINDOW'; id: number; position: WindowPosition; keepTile?: boolean }
  | { type: 'RESIZE_WINDOW'; id: number; size: WindowSize; keepTile?: boolean }
  | { type: 'TILE_WINDOW'; id: number; zone: string; geometry: TileGeometry | null }
  | { type: 'MAXIMIZE_WINDOW'; id: number }
  | { type: 'MINIMIZE_WINDOW'; id: number }
  | { type: 'SWITCH_DESKTOP'; id: string }
  | { type: 'ADD_DESKTOP'; id?: string; label?: string }
  | { type: 'REMOVE_DESKTOP'; id: string }
  | { type: 'MOVE_WINDOW_TO_DESKTOP'; windowId: number; desktopId: string }
  | { type: 'ADD_NATIVE_WINDOW'; nwin: NativeWindow }
  | { type: 'REMOVE_NATIVE_WINDOW'; pid: unknown }
  | { type: 'POPOUT_APP'; app: PopoutApp }
  | { type: 'CLOSE_POPOUT' }
  | { type: 'ADD_MESSAGE'; message: ShellMessage }
  | { type: 'SET_THINKING'; value: boolean }
  | { type: 'TOGGLE_LAUNCHPAD' }
  | { type: 'SET_LAUNCHPAD'; open: boolean }
  | { type: 'TOGGLE_CHAT' }
  | { type: 'SET_CHAT'; open: boolean }
  | { type: 'TOGGLE_MISSION_CONTROL' }
  | { type: 'SET_MISSION_CONTROL'; open: boolean }
  // `fromMirror` distinguishes "this is a peer's mirrored state" from "this is
  // MY OWN saved session" — see the RESTORE_STATE reducer case for why that
  // distinction is load-bearing rather than cosmetic (roadmap/SCREENS.md,
  // "Input focus across instances").
  | { type: 'RESTORE_STATE'; saved: SavedShellState; fromMirror?: boolean }
  | { type: 'SYNC_WLTOPLEVELS'; toplevels: WlToplevel[] }

const ShellContext = createContext<ShellContextValue | null>(null)

let nextId = 1

// Exported for unit tests (the store is the heart of the window manager).
// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function shellReducer(state: ShellState, action: ShellAction): ShellState {
  switch (action.type) {
    case 'OPEN_WINDOW': {
      const desktopId = state.activeDesktop
      const desktops = { ...state.desktops }
      const desk = { ...desktops[desktopId] }
      // Allow multiple windows unless singleton flag is set
      if (action.singleton) {
        const existing = desk.windows.find(w => w.appId === action.appId)
        if (existing) {
          desk.activeWindow = existing.id
          desktops[desktopId] = desk
          return { ...state, desktops }
        }
      }
      const id = nextId++
      // Opening geometry is the ONE geometry the shell invents rather than the
      // user producing it, so it is the one geometry that may be fitted to the
      // screen it is opening on. The cascade + the 720x500 default used to be
      // written inline here and consulted no viewport at all: at 768px — the
      // narrowest width the desktop canvas is ever mounted at — the first
      // window opened 12px off the right edge and the sixth 172px off it. See
      // openWindowGeometry in shell/windowTiling.ts for the measurements and
      // for why the clamp belongs here and not at render or at hydrate.
      //
      // readViewportSize() returns null wherever there is no `window` (SSR,
      // unit tests), and openWindowGeometry then reproduces the pre-clamp
      // cascade exactly rather than clamping against a fabricated extent.
      const geom = openWindowGeometry(desk.windows.length, action.size || DEFAULT_WINDOW_SIZE, readViewportSize())
      desk.windows = [...desk.windows, {
        id,
        appId: action.appId,
        title: action.title,
        url: action.url,
        component: action.component || null,
        html: action.html || null,
        _saveable: action._saveable || null,
        icon: action.icon || '',
        position: geom.position,
        // Every caller but Settings omits `size` and gets DEFAULT_WINDOW_SIZE —
        // see the OPEN_WINDOW action type's comment — shrunk only if it does
        // not fit this viewport.
        size: geom.size,
        minimized: false,
      }]
      desk.activeWindow = id
      desktops[desktopId] = desk
      return { ...state, desktops, launchpadOpen: false }
    }
    case 'CLOSE_WINDOW': {
      const desktopId = state.activeDesktop
      const desktops = { ...state.desktops }
      const desk = { ...desktops[desktopId] }
      desk.windows = desk.windows.filter(w => w.id !== action.id)
      desk.activeWindow = desk.activeWindow === action.id
        ? (desk.windows.at(-1)?.id ?? null)
        : desk.activeWindow
      desktops[desktopId] = desk
      return { ...state, desktops }
    }
    case 'FOCUS_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.activeWindow = action.id
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'MOVE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w =>
        // Dragging/moving a tiled window frees it — clear the tile flags so the
        // next keyboard-tiling gesture starts fresh from a floating window.
        w.id === action.id
          ? { ...w, position: action.position, _tile: action.keepTile ? w._tile : null, _maximized: action.keepTile ? w._maximized : false }
          : w
      )
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'RESIZE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w =>
        w.id === action.id
          ? { ...w, size: action.size, _tile: action.keepTile ? w._tile : null, _maximized: action.keepTile ? w._maximized : false }
          : w
      )
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'TILE_WINDOW': {
      const { id, zone, geometry } = action
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w => {
        if (w.id !== id) return w
        if (zone === 'restore') {
          if (!w._prevPosition) return { ...w, _tile: null, _maximized: false }
          // _prevPosition and _prevSize are always written together (see the
          // non-restore branch below) — the !w._prevPosition guard above
          // covers both in practice; _prevSize is asserted rather than
          // re-guarded to keep this restore path unchanged.
          return { ...w, position: w._prevPosition, size: w._prevSize!, _tile: null, _maximized: false, _prevPosition: null, _prevSize: null }
        }
        if (!geometry) return w
        // Preserve the floating geometry the first time a window is tiled so
        // 'restore' (and Down-arrow / double-click) can return it home.
        const alreadyTiled = !!(w._tile || w._maximized)
        return {
          ...w,
          _prevPosition: alreadyTiled ? w._prevPosition : w.position,
          _prevSize: alreadyTiled ? w._prevSize : w.size,
          position: geometry.position,
          size: geometry.size,
          _tile: zone,
          _maximized: zone === 'maximize',
        }
      })
      desk.activeWindow = id
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'MAXIMIZE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w => {
        if (w.id !== action.id) return w
        if (w._maximized) {
          // Invariant: _maximized is only ever set true alongside a freshly
          // (re)written _prevPosition/_prevSize (see the non-restore branch
          // below and TILE_WINDOW's identical invariant) — never null here in
          // practice. Asserted rather than guarded to keep this restore path
          // byte-for-byte identical to the original untyped behaviour.
          return { ...w, position: w._prevPosition!, size: w._prevSize!, _maximized: false, _tile: null }
        }
        // Preserve floating geometry only if the window isn't already tiled.
        const alreadyTiled = !!w._tile
        // Ask tileGeometry rather than recomputing the insets here. This used to
        // be its own `{ x: 0, y: 32 }` / `innerHeight - 32`, a second copy of
        // the same arithmetic — so when tiling learned to reserve the dock at
        // the bottom, maximize did not, and a maximized window kept running
        // underneath it. TILE_WINDOW already goes through tileGeometry; this is
        // the same geometry and now comes from the same place.
        const geom = tileGeometry('maximize', window.innerWidth, window.innerHeight)
        if (!geom) return w
        return {
          ...w,
          _prevPosition: alreadyTiled ? w._prevPosition : w.position,
          _prevSize: alreadyTiled ? w._prevSize : w.size,
          _maximized: true, _tile: 'maximize',
          position: geom.position,
          size: geom.size,
        }
      })
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'MINIMIZE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w =>
        w.id === action.id ? { ...w, minimized: !w.minimized } : w
      )
      if (desk.activeWindow === action.id) desk.activeWindow = null
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'SWITCH_DESKTOP':
      return { ...state, activeDesktop: action.id }
    case 'ADD_DESKTOP': {
      const id = action.id || `desktop-${Object.keys(state.desktops).length + 1}`
      return {
        ...state,
        desktops: {
          ...state.desktops,
          [id]: { id, label: action.label || `Desktop ${Object.keys(state.desktops).length + 1}`, windows: [], activeWindow: null },
        },
        activeDesktop: id,
      }
    }
    case 'REMOVE_DESKTOP': {
      if (Object.keys(state.desktops).length <= 1) return state
      const desktops = { ...state.desktops }
      const closing = desktops[action.id]
      delete desktops[action.id]
      const newActive = state.activeDesktop === action.id
        ? Object.keys(desktops)[0]
        : state.activeDesktop
      // Move orphaned windows to the target desktop (like macOS)
      if (closing?.windows?.length > 0) {
        const target = { ...desktops[newActive] }
        target.windows = [...target.windows, ...closing.windows]
        if (!target.activeWindow && closing.windows.length > 0) {
          target.activeWindow = closing.windows[0].id
        }
        desktops[newActive] = target
      }
      return { ...state, desktops, activeDesktop: newActive }
    }
    case 'MOVE_WINDOW_TO_DESKTOP': {
      const fromId = state.activeDesktop
      const toId = action.desktopId
      if (fromId === toId) return state
      const desktops = { ...state.desktops }
      const from = { ...desktops[fromId] }
      const to = { ...desktops[toId] }
      const win = from.windows.find(w => w.id === action.windowId)
      if (!win) return state
      from.windows = from.windows.filter(w => w.id !== action.windowId)
      from.activeWindow = from.activeWindow === action.windowId
        ? (from.windows.at(-1)?.id ?? null)
        : from.activeWindow
      to.windows = [...to.windows, win]
      to.activeWindow = win.id
      desktops[fromId] = from
      desktops[toId] = to
      return { ...state, desktops }
    }
    case 'ADD_NATIVE_WINDOW':
      return { ...state, nativeWindows: [...(state.nativeWindows || []), action.nwin] }
    case 'REMOVE_NATIVE_WINDOW':
      return { ...state, nativeWindows: (state.nativeWindows || []).filter(n => n.pid !== action.pid) }
    case 'POPOUT_APP':
      return { ...state, popout: action.app }
    case 'CLOSE_POPOUT':
      return { ...state, popout: null }
    case 'ADD_MESSAGE':
      return { ...state, conversation: [...state.conversation, action.message] }
    case 'SET_THINKING':
      return { ...state, thinking: action.value }
    case 'TOGGLE_LAUNCHPAD':
      return { ...state, launchpadOpen: !state.launchpadOpen }
    case 'SET_LAUNCHPAD':
      return { ...state, launchpadOpen: action.open }
    case 'TOGGLE_CHAT':
      return { ...state, chatOpen: !state.chatOpen }
    case 'SET_CHAT':
      return { ...state, chatOpen: action.open }
    case 'TOGGLE_MISSION_CONTROL':
      return { ...state, missionControlOpen: !state.missionControlOpen }
    case 'SET_MISSION_CONTROL':
      return { ...state, missionControlOpen: action.open }
    case 'RESTORE_STATE': {
      const saved = action.saved
      const desktops: Record<string, Desktop> = {}
      let maxId = 0
      for (const [deskId, desk] of Object.entries(saved.desktops || {})) {
        const windows = (desk.windows || []).map(w => {
          if (typeof w.id === 'number') maxId = Math.max(maxId, w.id)
          // Re-hydrate builtin apps: their React component can't be serialized,
          // so we persist only the appId and rebuild the live element on load.
          if (w._builtin && isBuiltinComponent(w.appId)) {
            return { ...w, component: builtinComponent(w.appId) }
          }
          return w
        })
        // activeWindow is the field keyboard shortcuts key off (see
        // shell/useWindowShortcuts.ts's `windows.find(w => w.id === activeWindow)`)
        // — it is this instance's notion of which window a keystroke should hit.
        // A LOCAL restore (page load, no `fromMirror`) is this tab's own prior
        // session, so adopting `saved`'s value verbatim is correct — it's what
        // this tab itself last had focused.
        //
        // A MIRRORED restore is a different thing entirely: it is the WRITER's
        // activeWindow, republished on a ~500ms debounce every time ANYTHING on
        // the writer's desktop changes. The writer/follower split tracks who
        // owns *persistence*, not who the user is looking at or typing into —
        // a follower can be, and normally is, the actually-focused viewport
        // (roadmap/SCREENS.md, "Input focus across instances": the leader has
        // nothing to do with which screen has the compositor's attention).
        // Adopting the writer's activeWindow unconditionally here would yank
        // THIS viewport's keyboard target to whatever the writer's screen last
        // clicked, mid-keystroke, on every unrelated edit — the exact
        // "instances compete for focus" failure mode this file exists to
        // avoid. So a mirrored restore keeps this instance's OWN active window
        // whenever it still exists in the incoming set, and only falls back to
        // the writer's pick when there is no local pick yet (first sync) or
        // the local pick was closed out from under it.
        let activeWindow = desk.activeWindow
        if (action.fromMirror) {
          const localActive = state.desktops[deskId]?.activeWindow ?? null
          if (localActive != null && windows.some(w => w.id === localActive)) {
            activeWindow = localActive
          }
        }
        desktops[deskId] = { ...desk, windows, activeWindow }
      }
      // Advance the id counter past every restored window so freshly-opened
      // windows never collide with a persisted id.
      if (maxId >= nextId) nextId = maxId + 1
      return { ...state, ...saved, desktops, conversation: state.conversation }
    }
    // BMINIT-18: thin WM — sync wlr-foreign-toplevel state into JSX windows.
    // Matches each toplevel (by app_id) to the corresponding JSX window and
    // stores its handle as _wtHandle so focusWindow/minimizeWindow can call the
    // wltoplevel API.  Toplevels that have no matching JSX window are ignored
    // (they are native app windows whose position/z labwc owns entirely).
    case 'SYNC_WLTOPLEVELS': {
      const toplevels = action.toplevels // [{handle, title, app_id, state}]
      const desktops = { ...state.desktops }
      for (const [deskId, desk] of Object.entries(desktops)) {
        const updated = desk.windows.map(w => {
          const match = toplevels.find(t => t.app_id === w.appId || t.title === w.title)
          if (!match) return w
          const minimized = !!(match.state && match.state.includes('minimized'))
          return { ...w, _wtHandle: match.handle, minimized: minimized || w.minimized }
        })
        desktops[deskId] = { ...desk, windows: updated }
      }
      return { ...state, desktops }
    }
    default:
      return state
  }
}

// ─── Cross-viewport geometry wire format ────────────────────────────────────
//
// position/size cross TWO boundaries that both leave this process: localStorage
// (saveShellState/loadShellState, read back by the SAME tab, maybe after a
// resize) and BroadcastChannel (useShellSession.publish/session.mirrored, read
// by a DIFFERENT tab that can be a different physical screen — see
// roadmap/SCREENS.md and screenScale.ts's module header for why raw CSS px is
// wrong the moment two instances don't share a viewport extent).
//
// So the wire shape carries a canonical fraction-of-viewport (screenScale.ts)
// instead of raw px whenever this tab can read its own viewport, tagged with
// `geomUnit` so a reader can tell it apart from:
//   - legacy data written before this conversion existed (no tag), and
//   - a same-shape fallback THIS tab writes when it can't read its own
//     viewport either (no `window`, e.g. SSR/tests) — also untagged, because
//     there is no extent to divide by and a fabricated fraction would be
//     worse than being honest that the numbers are px.
// isPlausibleCanonicalUnit's magnitude heuristic alone can't always tell a
// small window (0.3 x 0.4 of the viewport) from a raw px value, per
// screenScale.ts, so the explicit tag is the primary signal; the heuristic is
// only a backstop against a canonical-tagged payload whose numbers are
// obviously wrong (e.g. a future bug that tags without converting).
//
// ─── What an UNTAGGED payload is, and why it is fitted ──────────────────────
//
// Untagged geometry is raw px measured against a viewport this process cannot
// see and the payload does not record. Two producers make one:
//
//   1. Vulos v0.1.0 (tagged 2026-08-06, the first release the project ever
//      published). Its saveShellState wrote `position: w.position, size:
//      w.size` verbatim under the SAME localStorage key this build reads,
//      `vulos-shell-state` — unchanged from 6f019564 through today, so a
//      v0.1.0 user's blob is not orphaned by a key rename, it is read. The
//      canonical tag landed 2b3ee4bd (2026-08-13) and first shipped in v0.2.0
//      (2026-08-14). The legacy population is therefore every box that ran
//      v0.1.0 and has not yet re-saved under v0.2.0; it is small, but it is
//      provably not empty, which rules out "leave it".
//   2. serializeGeometry's own viewport-less fallback below, and a
//      mid-upgrade cross-tab writer still running pre-2b3ee4bd code while a
//      reader has already reloaded into this one.
//
// NOTHING in either shape records the writer's extent — v0.1.0's persisted
// object is desktops/activeDesktop and per-window id/appId/title/icon/
// position/size/minimized/_tile/_maximized/_builtin/url, and that is all. So
// there is no honest rescale available: converting these numbers would mean
// GUESSING a writer viewport, and guessing wrong shrinks or scatters geometry
// that was perfectly correct. Magnitude cannot rescue that guess either —
// that is the whole reason the tag exists.
//
// Left alone, though, they restore off-screen: a window at x=1800 written on
// a 1920-wide screen, read at 768, lands 1032px past the right edge, taking
// its title bar AND its bottom-right resize grip with it. The user has no
// gesture that reaches it.
//
// So untagged geometry is FITTED to the reading viewport (fitLegacyGeometry),
// not rescaled and not discarded. This is the same rule the open clamp
// states, applied to the one hydration case it does not cover: a canonical
// payload is user intent expressed proportionally, so it is restored
// proportionally and never clamped; an untagged payload is user intent about
// SOME OTHER screen, and where it should sit on THIS one is a question only
// the shell can answer — which makes it invented geometry, and invented
// geometry is fitted. Discarding it would answer that question by throwing
// the arrangement away even in the overwhelmingly common case where the
// upgrade happens on the same monitor and every window already fits.
//
// The two properties that fall out, both pinned in
// __tests__/shellStateGeometry.test.ts:
//   - a window that already fits is returned byte-identical (no clamp, no
//     rescale, no rounding), so a same-screen v0.1.0 upgrade changes nothing;
//   - a window that does not fit ends up fully inside the usable band, so it
//     is always reachable.
// The fit is one-way and one-time: the next serialize writes canonical, and
// from then on the window is proportional forever. The cost is that a legacy
// window the user had deliberately parked half off-screen is pulled back in
// once — untagged data cannot distinguish that from a bigger screen's
// leftovers, and "reachable" is the safer of the two failures. Post-v0.2.0
// data keeps deliberate off-screen parking exactly, because the canonical
// path is not clamped.

type GeometryUnitTag = 'canonical-v1'

/** A window's geometry exactly as it crosses localStorage / BroadcastChannel.
 *  Reuses WindowPosition/WindowSize's field names (x/y, width/height) for both
 *  the canonical and legacy cases, so `geomUnit` is the only thing that says
 *  which one it is. Never assign this straight into a live ShellWindow's
 *  position/size — always through hydrateGeometry first. */
interface SerializedGeometry {
  geomUnit?: GeometryUnitTag
  position: WindowPosition
  size: WindowSize
}

interface PersistedWindow {
  id: number
  appId: string
  title?: string
  icon?: string
  position: WindowPosition
  size: WindowSize
  geomUnit?: GeometryUnitTag
  minimized: boolean
  _tile?: string | null
  _maximized?: boolean
  _builtin?: boolean
  url?: string
}

interface PersistedDesktop {
  id: string
  label: string
  windows: PersistedWindow[]
  activeWindow: number | null
}

/** The wire/on-disk shape produced by serializableShellState and consumed by
 *  loadShellState / a follower's session.mirrored — geometry NOT YET resolved
 *  to any particular viewport. Distinct from SavedShellState (below), which is
 *  always raw px ready to hand a RESTORE_STATE dispatch; hydratePersistedState
 *  is what turns one into the other. */
interface PersistedShellState {
  desktops: Record<string, PersistedDesktop>
  activeDesktop: string
}

/** A window with no explicit/resolvable geometry falls back to this — literally
 *  what OPEN_WINDOW would give a freshly opened first window with no explicit
 *  size, so it lands visible and sane rather than off-screen, NaN, or a
 *  canonical fraction misread as a pixel count.
 *
 *  This is the ONLY geometry hydration ever clamps, and it is clamped for the
 *  same reason opening geometry is: it is invented by the shell, not produced
 *  by the user. The persisted numbers it replaces have already been judged
 *  unusable by the time this is called. Every OTHER hydration path — the
 *  canonical conversion below — is left alone deliberately: it is a
 *  proportional re-scaling of the user's own arrangement, and clamping it
 *  would write a small screen's limits back into the persisted state the next
 *  time this tab is the writer. */
function fallbackWindowGeometry(viewport: ViewportSize | null): { position: WindowPosition; size: WindowSize } {
  return openWindowGeometry(0, DEFAULT_WINDOW_SIZE, viewport)
}

/** Same shape as windowTiling's own private clamp, including its `Math.max(lo,
 *  hi)` guard for a viewport too short to hold even the insets — without it a
 *  hi below lo would push the window ABOVE the menu bar instead of below it. */
function clampToRange(v: number, lo: number, hi: number): number {
  return Math.min(Math.max(v, lo), Math.max(lo, hi))
}

/**
 * Fits UNTAGGED (raw-px-from-an-unknown-viewport) geometry into the reading
 * viewport's usable band. See the "What an UNTAGGED payload is, and why it is
 * fitted" section above for why fitting rather than rescaling or discarding.
 *
 * Deliberately identical in method and insets to openWindowGeometry — size
 * first, then position against the size that survived; WINDOW_EDGE_MARGIN
 * left/right, MENU_BAR_H/DOCK_H top/bottom, and fit beating every other
 * consideration — because a legacy restore and a fresh open are answering the
 * same question ("where does the shell put a window on THIS screen"), and two
 * answers that differ would be two behaviours to reason about. A test pins
 * that agreement so the two cannot drift while they live in separate files.
 *
 * What it deliberately does NOT do: re-position (a fitting window keeps its
 * exact x/y, it is not cascaded) and NOT round (an already-fitting fractional
 * geometry must survive byte-identical, which rounding would break).
 *
 * With a null viewport there is nothing to fit against and the numbers are
 * returned untouched — the same honesty as serializeGeometry refusing to
 * fabricate a fraction without an extent.
 *
 * A payload whose numbers are missing or non-finite gets the invented default
 * instead, for the same reason a corrupted canonical payload does: clamping
 * NaN produces NaN, and this function is the first thing that dereferences
 * numbers parsed out of foreign JSON (localStorage, or another tab's
 * postMessage) — the untagged branch used to hand them straight through.
 */
function fitLegacyGeometry(
  position: WindowPosition,
  size: WindowSize,
  viewport: ViewportSize | null,
): { position: WindowPosition; size: WindowSize } {
  const finite = Number.isFinite(position?.x) && Number.isFinite(position?.y) &&
    Number.isFinite(size?.width) && Number.isFinite(size?.height)
  if (!finite) return fallbackWindowGeometry(viewport)
  if (!viewport) return { position, size }
  const vw = viewport.widthPx as number
  const vh = viewport.heightPx as number
  const width = Math.max(1, Math.min(size.width, vw - 2 * WINDOW_EDGE_MARGIN))
  const height = Math.max(1, Math.min(size.height, vh - MENU_BAR_H - DOCK_H))
  const x = clampToRange(position.x, WINDOW_EDGE_MARGIN, vw - WINDOW_EDGE_MARGIN - width)
  const y = clampToRange(position.y, MENU_BAR_H, vh - DOCK_H - height)
  return { position: { x, y }, size: { width, height } }
}

/** Converts one window's live px position/size into the wire form. Canonical
 *  (fraction of THIS tab's own viewport) whenever the viewport is readable, so
 *  a reader on a different-sized viewport lands the window in the same
 *  RELATIVE place instead of the same raw px. Falls back to untagged raw px
 *  only when this tab cannot read its own viewport at all. */
function serializeGeometry(position: WindowPosition, size: WindowSize, viewport: ViewportSize | null): SerializedGeometry {
  if (!viewport) return { position, size }
  const p = toCanonicalPoint({ x: viewportPx(position.x), y: viewportPx(position.y) }, viewport)
  const s = toCanonicalSize({ width: viewportPx(size.width), height: viewportPx(size.height) }, viewport)
  return { geomUnit: 'canonical-v1', position: { x: p.nx, y: p.ny }, size: { width: s.nw, height: s.nh } }
}

/** The inverse: resolves a wire-format window's geometry to THIS tab's own
 *  viewport px, ready to render.
 *   - `geomUnit === 'canonical-v1'` and the numbers are plausible: convert
 *     through THIS tab's own current viewport — never the writer's.
 *   - no tag: legacy pre-canonical-unit data (v0.1.0), or a viewport-less
 *     writer's fallback — already raw px, and NEVER rescaled, because no
 *     writer extent was recorded to rescale against. Fitted to this viewport
 *     so it cannot restore somewhere unreachable; a window that already fits
 *     comes back untouched.
 *   - tagged canonical but this tab has no viewport, or the numbers fail the
 *     plausibility backstop (a corrupted payload): neither raw-as-is nor a
 *     fabricated conversion is safe, so fall back to
 *     fallbackWindowGeometry(). */
function hydrateGeometry(w: SerializedGeometry, viewport: ViewportSize | null): { position: WindowPosition; size: WindowSize } {
  if (w.geomUnit !== 'canonical-v1') return fitLegacyGeometry(w.position, w.size, viewport)
  const plausible = isPlausibleCanonicalUnit(w.position.x) && isPlausibleCanonicalUnit(w.position.y) &&
    isPlausibleCanonicalUnit(w.size.width) && isPlausibleCanonicalUnit(w.size.height)
  if (!viewport || !plausible) {
    return fallbackWindowGeometry(viewport)
  }
  const p = fromCanonicalPoint({ nx: w.position.x as CanonicalUnit, ny: w.position.y as CanonicalUnit }, viewport)
  const s = fromCanonicalSize({ nw: w.size.width as CanonicalUnit, nh: w.size.height as CanonicalUnit }, viewport)
  return { position: { x: p.x, y: p.y }, size: { width: s.width, height: s.height } }
}

/** Resolves an entire wire-format PersistedShellState (from loadShellState's
 *  localStorage read, or a follower's session.mirrored) to THIS tab's own
 *  viewport — the ONLY thing that ever feeds a RESTORE_STATE dispatch.
 *  Exported so both call sites (loadShellState below, and ShellProvider's
 *  follower effect) share one implementation, and so it's unit-testable
 *  against an explicit viewport without needing two live browser tabs. */
// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function hydratePersistedState(persisted: PersistedShellState, viewport: ViewportSize | null): SavedShellState {
  const desktops: Record<string, Desktop> = {}
  for (const [id, desk] of Object.entries(persisted.desktops)) {
    desktops[id] = {
      id: desk.id,
      label: desk.label,
      activeWindow: desk.activeWindow,
      windows: desk.windows.map((w): ShellWindow => {
        const { position, size } = hydrateGeometry(w, viewport)
        return {
          id: w.id, appId: w.appId, title: w.title, icon: w.icon,
          position, size, minimized: w.minimized,
          _tile: w._tile ?? null, _maximized: !!w._maximized,
          ...(w._builtin ? { _builtin: true } : {}),
          ...(w.url ? { url: w.url } : {}),
        }
      }),
    }
  }
  return { desktops, activeDesktop: persisted.activeDesktop }
}

// Persist shell state to localStorage (survives refresh)
const STORAGE_KEY = 'vulos-shell-state'
/**
 * The structured-cloneable projection of shell state.
 *
 * Extracted from saveShellState because TWO paths need it and only one had it.
 * `component` on a window is a React element, which localStorage never saw
 * (JSON.stringify would have dropped it) but BroadcastChannel does: postMessage
 * structured-clones, and a React element throws
 *   "Symbol(react.transitional.element) could not be cloned".
 *
 * Publishing raw state therefore threw an uncaught error the moment any window
 * with a React component was open — which is every builtin app. Three e2e specs
 * were failing on it, and cross-tab mirroring never delivered a single message.
 *
 * Also the single place position/size are converted to the canonical
 * fraction-of-viewport wire format (see the "Cross-viewport geometry wire
 * format" section above) — BOTH saveShellState (localStorage, read back by
 * this same tab) and useShellSession.publish (BroadcastChannel, read by a
 * DIFFERENT tab/screen) go through this, so both get the conversion. A
 * single-viewport reload isn't harmed by that: loadShellState below converts
 * back through THIS tab's own current viewport, which for an unresized window
 * is the same extent it was saved with, so the round trip is a no-op modulo
 * float precision (proven in ShellProvider's tests).
 */
// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function serializableShellState(state: ShellState): PersistedShellState {
  const viewport = readViewportSize()
  const toSave: PersistedShellState = {
    desktops: {},
    activeDesktop: state.activeDesktop,
  }
  for (const [id, desk] of Object.entries(state.desktops)) {
    toSave.desktops[id] = {
      ...desk,
      windows: desk.windows
        // Keep windows we can faithfully rebuild on reload: URL/web-view apps,
        // and builtin React apps (rebuilt from their appId). Stream / browser /
        // raw-html windows need a live backend session, so they're dropped.
        .filter(w => (w.url && !w.component && !w.html) || isBuiltinComponent(w.appId))
        .map(w => {
          const geom = serializeGeometry(w.position, w.size, viewport)
          const base = {
            id: w.id, appId: w.appId, title: w.title, icon: w.icon,
            ...geom, minimized: w.minimized,
            _tile: w._tile || null, _maximized: !!w._maximized,
          }
          return isBuiltinComponent(w.appId)
            ? { ...base, _builtin: true }
            : { ...base, url: w.url }
        }),
    }
  }
  return toSave
}

// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function saveShellState(state: ShellState): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(serializableShellState(state)))
  } catch { /* noop */ }
}
// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function loadShellState(): SavedShellState | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const data: PersistedShellState = JSON.parse(raw)
    // Validate schema before restoring
    if (!data || typeof data !== 'object') return null
    if (!data.desktops || typeof data.desktops !== 'object') return null
    if (typeof data.activeDesktop !== 'string') return null
    for (const desk of Object.values(data.desktops)) {
      if (!desk || !Array.isArray(desk.windows)) return null
      // Strip any non-serializable or stale data. Keep URL windows and builtin
      // apps (which restore from appId); drop anything else.
      desk.windows = desk.windows.filter(w =>
        w && typeof w.appId === 'string' &&
        (typeof w.url === 'string' || (w._builtin && isBuiltinComponent(w.appId)))
      )
    }
    if (!data.desktops[data.activeDesktop]) {
      data.activeDesktop = Object.keys(data.desktops)[0]
    }
    // Resolve canonical (or legacy raw-px) geometry to THIS tab's own current
    // viewport — see hydratePersistedState. Same-viewport reload round-trips;
    // a reload after resizing the window rescales proportionally instead of
    // leaving a window's saved position meaningless for the new extent.
    return hydratePersistedState(data, readViewportSize())
  } catch { return null }
}

const initialDesktop: Desktop = {
  id: 'desktop-1',
  label: 'Desktop 1',
  windows: [],
  activeWindow: null,
}

const initialState: ShellState = {
  desktops: { 'desktop-1': initialDesktop },
  activeDesktop: 'desktop-1',
  popout: null,
  nativeWindows: [],
  conversation: [],
  thinking: false,
  launchpadOpen: false,
  chatOpen: false,
  missionControlOpen: false,
}

/** A window as exposed via `allWindows` — every ShellWindow plus the
 *  desktop-relative visibility/focus flags computed in the useMemo below. */
export type VisibleShellWindow = ShellWindow & { _visible: boolean; _active: boolean }

export interface OpenWindowOptions {
  appId: string
  title?: string
  url?: string
  icon?: string
  component?: ReactNode | null
  html?: string | null
  _saveable?: SaveableAIApp | null
  /** Accepted by every caller that wants single-instance dedup, but NOT
   *  currently forwarded by openWindow() below — see the ShellAction
   *  'OPEN_WINDOW' comment. Preserved as-is. */
  singleton?: boolean
  /** Optional initial window size. Omit to get the shared 720x500 default —
   *  see the 'OPEN_WINDOW' ShellAction comment. */
  size?: WindowSize
}

/** Loose input accepted by openNativeWindow — DesktopContextMenu.jsx passes a
 *  minimal literal ({title, url, appId}), Window.jsx passes a full
 *  ShellWindow. Every field is read defensively (optional chaining / `||`
 *  fallbacks) so this stays a subset rather than requiring the full shape. */
export interface OpenNativeWindowInput {
  id?: number
  appId?: string
  title?: string
  url?: string
  size?: WindowSize
}

export interface ShellContextValue {
  windows: ShellWindow[]
  activeWindow: number | null
  allWindows: VisibleShellWindow[]
  desktops: Record<string, Desktop>
  activeDesktop: string
  popout: PopoutApp | null
  nativeWindows: NativeWindow[]
  conversation: ShellMessage[]
  thinking: boolean
  launchpadOpen: boolean
  chatOpen: boolean
  missionControlOpen: boolean
  layout: ReturnType<typeof useViewport>
  openWindow: (opts: OpenWindowOptions) => void
  closeWindow: (id: number) => void
  focusWindow: (id: number) => void
  moveWindow: (id: number, position: WindowPosition) => void
  resizeWindow: (id: number, size: WindowSize) => void
  minimizeWindow: (id: number) => void
  maximizeWindow: (id: number) => void
  tileWindow: (id: number, zone: string) => void
  switchDesktop: (id: string) => void
  addDesktop: (label?: string) => void
  /** Cross-tab session: who is on this desktop and whether we drive it.
   *  Null where BroadcastChannel is unavailable (single-tab behaviour).
   *  Deliberately NOTHING to do with keyboard focus — see `hasFocus` below
   *  and roadmap/SCREENS.md, "Input focus across instances": which instance
   *  owns persistence and which instance the user is looking at are
   *  unrelated facts, and conflating them was explicitly ruled out. */
  session: ShellSession | null
  /** Whether THIS instance currently holds the compositor's input focus —
   *  see providers/viewportFocus.ts. Read-only signal for callers that need
   *  to defer to it (e.g. a global keyboard-shortcut listener should not act
   *  on behalf of a viewport the user isn't looking at). ShellProvider itself
   *  never gates persistence or session role on this field, and must not
   *  start to — see providers/shellSession.ts, which keeps writer election
   *  entirely independent of focus for the same reason. */
  hasFocus: boolean
  removeDesktop: (id: string) => void
  moveWindowToDesktop: (windowId: number, desktopId: string) => void
  openNativeWindow: (win: OpenNativeWindowInput) => Promise<void>
  closeNativeWindow: (pid: unknown) => Promise<void>
  popoutApp: (app: PopoutApp) => void
  closePopout: () => void
  toggleLaunchpad: () => void
  setLaunchpad: (open: boolean) => void
  toggleChat: () => void
  setChat: (open: boolean) => void
  toggleMissionControl: () => void
  setMissionControl: (open: boolean) => void
  addMessage: (role: string, text: string, meta?: unknown) => void
  setThinking: (value: boolean) => void
}

export function ShellProvider({ children }: { children: ReactNode }) {
  const layout = useViewport()
  const [state, dispatch] = useReducer(shellReducer, initialState)
  const mounted = useRef(false)

  // Restore state from localStorage on mount
  useEffect(() => {
    const saved = loadShellState()
    if (saved && Object.keys(saved.desktops || {}).length > 0) {
      dispatch({ type: 'RESTORE_STATE', saved })
    }
    mounted.current = true
  }, [])

  // Cross-tab session: exactly one WRITER owns the persisted state; other tabs
  // on the same desktop follow it. See providers/shellSession.ts for why this
  // is an election rather than a merge.
  const session = useShellSession(state.activeDesktop)

  // Whether THIS instance currently has the compositor's input focus — see
  // providers/viewportFocus.ts. Independent of `session.role` on purpose:
  // roadmap/SCREENS.md, "Input focus across instances" — the writer is
  // whoever owns persistence, not whoever the user is looking at.
  const hasFocus = useViewportFocus()

  // Auto-save state on changes (debounced).
  //
  // ONLY THE WRITER SAVES. Every tab used to write this one key on its own
  // debounce with its own in-memory copy, so two tabs silently overwrote each
  // other — open a window in one, move a window in the other, and the second
  // tab's save dropped the first tab's window with nothing thrown and nothing
  // shown. A follower persisting here would restore exactly that.
  useEffect(() => {
    if (!mounted.current) return
    if (session.role !== 'writer') return
    const timer = setTimeout(() => {
      saveShellState(state)
      session.publish(serializableShellState(state))
    }, 500)
    return () => clearTimeout(timer)
    // `state` itself is not a dependency, and the two fields named here are not
    // an abbreviation of it — they are the whole of what this effect writes.
    // saveShellState persists exactly { desktops, activeDesktop }, and
    // serializableShellState reads exactly those two off the state it is given.
    // The other seven fields on ShellState (conversation, thinking, popout,
    // nativeWindows, launchpadOpen, chatOpen, missionControlOpen) are
    // deliberately not persisted — a reload is not supposed to restore a
    // half-finished chat or a menu someone left open.
    //
    // So depending on the whole object would re-arm this 500ms debounce on
    // every assistant message and every panel toggle without changing one
    // persisted byte, and each re-arm postpones the write. The effect saves the
    // live `state` it closed over, so what lands on disk is always current;
    // this list decides WHEN a save happens, not WHAT is in it.
  }, [state.desktops, state.activeDesktop, session.role, session.publish, session])

  // A follower mirrors whatever the writer publishes, so both tabs show the
  // same desktop instead of drifting apart. session.mirrored carries geometry
  // in the writer's canonical fraction-of-ITS-viewport (or, for a legacy/
  // viewport-less writer, raw px) — see serializableShellState. Resolve it to
  // THIS tab's own current viewport (which can be a different physical screen
  // entirely, per roadmap/SCREENS.md) before it ever reaches shell state.
  useEffect(() => {
    if (session.role !== 'follower' || !session.mirrored) return
    const hydrated = hydratePersistedState(session.mirrored as PersistedShellState, readViewportSize())
    // fromMirror: true — this is a peer's state, not our own saved session, so
    // RESTORE_STATE must not let it overwrite which window THIS viewport has
    // focused. See the reducer case's comment and roadmap/SCREENS.md, "Input
    // focus across instances".
    dispatch({ type: 'RESTORE_STATE', saved: hydrated, fromMirror: true })
  }, [session.role, session.mirrored])

  // Current desktop's windows (for dock, etc.)
  const currentDesktop = state.desktops[state.activeDesktop] || initialDesktop
  const windows = currentDesktop.windows
  const activeWindow = currentDesktop.activeWindow

  // ALL windows across all desktops — for persistent rendering
  // Each window gets a `_visible` flag (true only if on active desktop and not minimized)
  // Memoized so callbacks that depend on it (closeWindow, focusWindow,
  // minimizeWindow) only get a new identity when the underlying window data
  // actually changes, not on every unrelated render.
  const allWindows = useMemo(() => {
    const out: VisibleShellWindow[] = []
    for (const [deskId, desk] of Object.entries(state.desktops)) {
      for (const win of desk.windows) {
        out.push({
          ...win,
          _visible: deskId === state.activeDesktop,
          _active: deskId === state.activeDesktop && desk.activeWindow === win.id,
        })
      }
    }
    return out
  }, [state.desktops, state.activeDesktop])

  const openWindow = useCallback(({ appId, title, url, icon, component, html, _saveable, size }: OpenWindowOptions) => {
    dispatch({ type: 'OPEN_WINDOW', appId, title, url, icon, component, html, _saveable, size })
  }, [])
  const closeWindow = useCallback((id: number) => {
    // Find the window to check if it's a running app that needs stopping
    const win = allWindows.find(w => w.id === id)
    if (win?.url && win.appId) {
      // Non-builtin app with a URL — stop it on the backend
      fetch('/api/apps/stop', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: win.appId }),
      }).catch(() => {})
    }
    dispatch({ type: 'CLOSE_WINDOW', id })
  }, [allWindows])
  // BMINIT-18: in v2 labwc native mode the React WM is "thin" — it mirrors
  // labwc's window state via wlr-foreign-toplevel-management-v1 instead of
  // owning positioning/stacking.  focusWindow additionally calls
  // POST /api/shell/windows/focus to activate the compositor-side window.
  // minimizeWindow calls POST /api/shell/windows/minimize similarly.
  // closeWindow already stops the app via /api/apps/stop; the compositor
  // handles the actual window destruction via its foreign-toplevel close.
  //
  // The window handle used by the wltoplevel API is derived from the lswt
  // enumeration index.  We store it on the JSX window as win._wtHandle when
  // a foreign-toplevel sync refreshes the list.
  const isNativeV2 = getNativeMode() === 'native'

  // Poll /api/shell/windows every 2 s in v2 mode to keep JSX state in sync
  // with labwc (e.g. the user dragged/minimized/closed via the compositor SSD).
  useEffect(() => {
    if (!isNativeV2) return
    const refresh = () => {
      fetch('/api/shell/windows')
        .then(r => r.json())
        .then(wins => {
          if (!Array.isArray(wins)) return
          // Update _wtHandle on each JSX window by matching appId to lswt app_id.
          dispatch({ type: 'SYNC_WLTOPLEVELS', toplevels: wins })
        })
        .catch(() => {})
    }
    refresh()
    const t = setInterval(refresh, 2000)
    return () => clearInterval(t)
  }, [isNativeV2])

  const focusWindow = useCallback((id: number) => {
    dispatch({ type: 'FOCUS_WINDOW', id })
    if (isNativeV2) {
      // Find the corresponding wltoplevel handle and activate it in labwc.
      const win = allWindows.find(w => w.id === id)
      if (win?._wtHandle != null) {
        fetch('/api/shell/windows/focus', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ handle: win._wtHandle }),
        }).catch(() => {})
      }
    }
  }, [allWindows, isNativeV2])

  const moveWindow = useCallback((id: number, position: WindowPosition) => dispatch({ type: 'MOVE_WINDOW', id, position }), [])
  const resizeWindow = useCallback((id: number, size: WindowSize) => dispatch({ type: 'RESIZE_WINDOW', id, size }), [])

  const minimizeWindow = useCallback((id: number) => {
    dispatch({ type: 'MINIMIZE_WINDOW', id })
    if (isNativeV2) {
      const win = allWindows.find(w => w.id === id)
      if (win?._wtHandle != null) {
        fetch('/api/shell/windows/minimize', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ handle: win._wtHandle }),
        }).catch(() => {})
      }
    }
  }, [allWindows, isNativeV2])

  const maximizeWindow = useCallback((id: number) => dispatch({ type: 'MAXIMIZE_WINDOW', id }), [])

  // Snap/tile a window into a zone (or 'restore' to floating). Geometry is
  // computed here from the live viewport; the reducer records the tile so the
  // keyboard-tiling state machine can build quarters from halves, etc.
  const tileWindow = useCallback((id: number, zone: string) => {
    const geometry = zone === 'restore'
      ? null
      : tileGeometry(zone, window.innerWidth, window.innerHeight, MENU_BAR_H)
    dispatch({ type: 'TILE_WINDOW', id, zone, geometry })
  }, [])

  const switchDesktop = useCallback((id: string) => dispatch({ type: 'SWITCH_DESKTOP', id }), [])
  const addDesktop = useCallback((label?: string) => dispatch({ type: 'ADD_DESKTOP', label }), [])
  const removeDesktop = useCallback((id: string) => dispatch({ type: 'REMOVE_DESKTOP', id }), [])
  const moveWindowToDesktop = useCallback((windowId: number, desktopId: string) => dispatch({ type: 'MOVE_WINDOW_TO_DESKTOP', windowId, desktopId }), [])

  // v2 only (D93): openNativeWindow is a no-op in v1; canSpawnNativeWindow()
  // returns false unless VULOS_NATIVE_MODE_V2 is enabled server-side.
  const openNativeWindow = useCallback(async (win: OpenNativeWindowInput) => {
    if (!canSpawnNativeWindow()) return
    try {
      const res = await fetch('/api/shell/native-window', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          url: win.url || window.location.origin,
          title: win.title || 'Vulos',
          width: win.size?.width || 720,
          height: win.size?.height || 500,
          always_on_top: true,
        }),
      })
      if (!res.ok) return
      const data = await res.json()
      dispatch({ type: 'ADD_NATIVE_WINDOW', nwin: { pid: data.pid, title: win.title, appId: win.appId } })
      // If this was an embedded window, close it from the shell
      if (win.id) closeWindow(win.id)
    } catch { /* noop */ }
  }, [closeWindow])

  const closeNativeWindow = useCallback(async (pid: unknown) => {
    try {
      await fetch('/api/shell/native-window', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pid }),
      })
    } catch { /* noop */ }
    dispatch({ type: 'REMOVE_NATIVE_WINDOW', pid })
  }, [])

  const popoutApp = useCallback((app: PopoutApp) => dispatch({ type: 'POPOUT_APP', app }), [])
  const closePopout = useCallback(() => dispatch({ type: 'CLOSE_POPOUT' }), [])

  const toggleLaunchpad = useCallback(() => dispatch({ type: 'TOGGLE_LAUNCHPAD' }), [])
  const setLaunchpad = useCallback((open: boolean) => dispatch({ type: 'SET_LAUNCHPAD', open }), [])
  const toggleChat = useCallback(() => dispatch({ type: 'TOGGLE_CHAT' }), [])
  const setChat = useCallback((open: boolean) => dispatch({ type: 'SET_CHAT', open }), [])
  const toggleMissionControl = useCallback(() => dispatch({ type: 'TOGGLE_MISSION_CONTROL' }), [])
  const setMissionControl = useCallback((open: boolean) => dispatch({ type: 'SET_MISSION_CONTROL', open }), [])
  const addMessage = useCallback((role: string, text: string, meta?: unknown) => {
    dispatch({ type: 'ADD_MESSAGE', message: { id: Date.now() + Math.random(), role, text, meta, timestamp: new Date() } })
  }, [])
  const setThinking = useCallback((value: boolean) => dispatch({ type: 'SET_THINKING', value }), [])

  return (
    <ShellContext.Provider value={{
      windows, activeWindow, allWindows,
      desktops: state.desktops, activeDesktop: state.activeDesktop,
      popout: state.popout, nativeWindows: state.nativeWindows || [],
      conversation: state.conversation, thinking: state.thinking,
      launchpadOpen: state.launchpadOpen, chatOpen: state.chatOpen, missionControlOpen: state.missionControlOpen,
      layout,
      openWindow, closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow, tileWindow,
      switchDesktop, addDesktop, removeDesktop, moveWindowToDesktop, session, hasFocus,
      openNativeWindow, closeNativeWindow,
      popoutApp, closePopout,
      toggleLaunchpad, setLaunchpad, toggleChat, setChat,
      toggleMissionControl, setMissionControl,
      addMessage, setThinking,
    }}>
      {children}
    </ShellContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function useShell(): ShellContextValue {
  const ctx = useContext(ShellContext)
  if (!ctx) throw new Error('useShell must be used within ShellProvider')
  return ctx
}

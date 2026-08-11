import { createContext, useContext, useReducer, useCallback, useEffect, useMemo, useRef, type ReactNode } from 'react'
import { useViewport } from '../shell/useViewport'
import { canSpawnNativeWindow, getNativeMode } from '../core/useNativeMode'
import { tileGeometry, MENU_BAR_H } from '../shell/windowTiling'
import { builtinComponent, isBuiltinComponent } from '../shell/builtinApps'
import { useShellSession, type ShellSession } from './useShellSession'

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
  | { type: 'RESTORE_STATE'; saved: SavedShellState }
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
      desk.windows = [...desk.windows, {
        id,
        appId: action.appId,
        title: action.title,
        url: action.url,
        component: action.component || null,
        html: action.html || null,
        _saveable: action._saveable || null,
        icon: action.icon || '',
        position: { x: 60 + (desk.windows.length % 6) * 32, y: 50 + (desk.windows.length % 6) * 32 },
        // Every caller but Settings omits `size` and gets this unchanged
        // default — see the OPEN_WINDOW action type's comment.
        size: action.size || { width: 720, height: 500 },
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
        desktops[deskId] = { ...desk, windows }
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
 */
// eslint-disable-next-line react-refresh/only-export-components -- this module intentionally exports the provider alongside its reducer/persistence/hook; splitting them would fragment the shell's state machine across files for an HMR nicety.
export function serializableShellState(state: ShellState): SavedShellState {
  const toSave: SavedShellState = {
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
          const base = {
            id: w.id, appId: w.appId, title: w.title, icon: w.icon,
            position: w.position, size: w.size, minimized: w.minimized,
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
    const data: SavedShellState = JSON.parse(raw)
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
    return data
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
   *  Null where BroadcastChannel is unavailable (single-tab behaviour). */
  session: ShellSession | null
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
  }, [state.desktops, state.activeDesktop, session.role, session.publish, session])

  // A follower mirrors whatever the writer publishes, so both tabs show the
  // same desktop instead of drifting apart.
  useEffect(() => {
    if (session.role !== 'follower' || !session.mirrored) return
    dispatch({ type: 'RESTORE_STATE', saved: session.mirrored as SavedShellState })
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
          title: win.title || 'Vula',
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
      switchDesktop, addDesktop, removeDesktop, moveWindowToDesktop, session,
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

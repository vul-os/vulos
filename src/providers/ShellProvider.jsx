import { createContext, useContext, useReducer, useCallback, useEffect, useRef } from 'react'
import { useViewport } from '../shell/useViewport'
import { canSpawnNativeWindow, getNativeMode } from '../core/useNativeMode'

const ShellContext = createContext(null)

let nextId = 1

function shellReducer(state, action) {
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
        size: { width: 720, height: 500 },
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
        w.id === action.id ? { ...w, position: action.position } : w
      )
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'RESIZE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w =>
        w.id === action.id ? { ...w, size: action.size } : w
      )
      desktops[state.activeDesktop] = desk
      return { ...state, desktops }
    }
    case 'MAXIMIZE_WINDOW': {
      const desktops = { ...state.desktops }
      const desk = { ...desktops[state.activeDesktop] }
      desk.windows = desk.windows.map(w => {
        if (w.id !== action.id) return w
        if (w._maximized) {
          return { ...w, position: w._prevPosition, size: w._prevSize, _maximized: false }
        }
        // Position below menu bar (32px), fullscreen to bottom
        return {
          ...w,
          _prevPosition: w.position, _prevSize: w.size, _maximized: true,
          position: { x: 0, y: 32 },
          size: { width: window.innerWidth, height: window.innerHeight - 32 },
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
    case 'RESTORE_STATE':
      return { ...state, ...action.saved, conversation: state.conversation }
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
          const minimized = match.state && match.state.includes('minimized')
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
function saveShellState(state) {
  try {
    // Only persist serializable window data (strip component/html which can be large)
    const toSave = {
      desktops: {},
      activeDesktop: state.activeDesktop,
    }
    for (const [id, desk] of Object.entries(state.desktops)) {
      toSave.desktops[id] = {
        ...desk,
        windows: desk.windows.filter(w => w.url && !w.component && !w.html).map(w => ({
          id: w.id, appId: w.appId, title: w.title, url: w.url, icon: w.icon,
          position: w.position, size: w.size, minimized: w.minimized,
        })),
      }
    }
    localStorage.setItem(STORAGE_KEY, JSON.stringify(toSave))
  } catch { /* noop */ }
}
function loadShellState() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (!raw) return null
    const data = JSON.parse(raw)
    // Validate schema before restoring
    if (!data || typeof data !== 'object') return null
    if (!data.desktops || typeof data.desktops !== 'object') return null
    if (typeof data.activeDesktop !== 'string') return null
    for (const desk of Object.values(data.desktops)) {
      if (!desk || !Array.isArray(desk.windows)) return null
      // Strip any non-serializable or stale data
      desk.windows = desk.windows.filter(w => w && typeof w.appId === 'string' && typeof w.url === 'string')
    }
    if (!data.desktops[data.activeDesktop]) {
      data.activeDesktop = Object.keys(data.desktops)[0]
    }
    return data
  } catch { return null }
}

const initialDesktop = {
  id: 'desktop-1',
  label: 'Desktop 1',
  windows: [],
  activeWindow: null,
}

const initialState = {
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

export function ShellProvider({ children }) {
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

  // Auto-save state on changes (debounced)
  useEffect(() => {
    if (!mounted.current) return
    const timer = setTimeout(() => saveShellState(state), 500)
    return () => clearTimeout(timer)
  }, [state.desktops, state.activeDesktop])

  // Current desktop's windows (for dock, etc.)
  const currentDesktop = state.desktops[state.activeDesktop] || initialDesktop
  const windows = currentDesktop.windows
  const activeWindow = currentDesktop.activeWindow

  // ALL windows across all desktops — for persistent rendering
  // Each window gets a `_visible` flag (true only if on active desktop and not minimized)
  const allWindows = []
  for (const [deskId, desk] of Object.entries(state.desktops)) {
    for (const win of desk.windows) {
      allWindows.push({
        ...win,
        _visible: deskId === state.activeDesktop,
        _active: deskId === state.activeDesktop && desk.activeWindow === win.id,
      })
    }
  }

  const openWindow = useCallback(({ appId, title, url, icon, component, html, _saveable }) => {
    dispatch({ type: 'OPEN_WINDOW', appId, title, url, icon, component, html, _saveable })
  }, [])
  const closeWindow = useCallback((id) => {
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

  const focusWindow = useCallback((id) => {
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

  const moveWindow = useCallback((id, position) => dispatch({ type: 'MOVE_WINDOW', id, position }), [])
  const resizeWindow = useCallback((id, size) => dispatch({ type: 'RESIZE_WINDOW', id, size }), [])

  const minimizeWindow = useCallback((id) => {
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

  const maximizeWindow = useCallback((id) => dispatch({ type: 'MAXIMIZE_WINDOW', id }), [])

  const switchDesktop = useCallback((id) => dispatch({ type: 'SWITCH_DESKTOP', id }), [])
  const addDesktop = useCallback((label) => dispatch({ type: 'ADD_DESKTOP', label }), [])
  const removeDesktop = useCallback((id) => dispatch({ type: 'REMOVE_DESKTOP', id }), [])
  const moveWindowToDesktop = useCallback((windowId, desktopId) => dispatch({ type: 'MOVE_WINDOW_TO_DESKTOP', windowId, desktopId }), [])

  // v2 only (D93): openNativeWindow is a no-op in v1; canSpawnNativeWindow()
  // returns false unless VULOS_NATIVE_MODE_V2 is enabled server-side.
  const openNativeWindow = useCallback(async (win) => {
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

  const closeNativeWindow = useCallback(async (pid) => {
    try {
      await fetch('/api/shell/native-window', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ pid }),
      })
    } catch { /* noop */ }
    dispatch({ type: 'REMOVE_NATIVE_WINDOW', pid })
  }, [])

  const popoutApp = useCallback((app) => dispatch({ type: 'POPOUT_APP', app }), [])
  const closePopout = useCallback(() => dispatch({ type: 'CLOSE_POPOUT' }), [])

  const toggleLaunchpad = useCallback(() => dispatch({ type: 'TOGGLE_LAUNCHPAD' }), [])
  const setLaunchpad = useCallback((open) => dispatch({ type: 'SET_LAUNCHPAD', open }), [])
  const toggleChat = useCallback(() => dispatch({ type: 'TOGGLE_CHAT' }), [])
  const setChat = useCallback((open) => dispatch({ type: 'SET_CHAT', open }), [])
  const toggleMissionControl = useCallback(() => dispatch({ type: 'TOGGLE_MISSION_CONTROL' }), [])
  const setMissionControl = useCallback((open) => dispatch({ type: 'SET_MISSION_CONTROL', open }), [])
  const addMessage = useCallback((role, text, meta) => {
    dispatch({ type: 'ADD_MESSAGE', message: { id: Date.now() + Math.random(), role, text, meta, timestamp: new Date() } })
  }, [])
  const setThinking = useCallback((value) => dispatch({ type: 'SET_THINKING', value }), [])

  return (
    <ShellContext.Provider value={{
      windows, activeWindow, allWindows,
      desktops: state.desktops, activeDesktop: state.activeDesktop,
      popout: state.popout, nativeWindows: state.nativeWindows || [],
      conversation: state.conversation, thinking: state.thinking,
      launchpadOpen: state.launchpadOpen, chatOpen: state.chatOpen, missionControlOpen: state.missionControlOpen,
      layout,
      openWindow, closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow,
      switchDesktop, addDesktop, removeDesktop, moveWindowToDesktop,
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

// eslint-disable-next-line react-refresh/only-export-components
export function useShell() {
  const ctx = useContext(ShellContext)
  if (!ctx) throw new Error('useShell must be used within ShellProvider')
  return ctx
}

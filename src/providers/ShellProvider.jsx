import { createContext, useContext, useReducer, useCallback, useEffect, useRef } from 'react'
import { useViewport } from '../shell/useViewport'

const ShellContext = createContext(null)

let nextId = 1

function shellReducer(state, action) {
  switch (action.type) {
    case 'OPEN_WINDOW': {
      const desktopId = state.activeDesktop
      const desktops = { ...state.desktops }
      const desk = { ...desktops[desktopId] }
      const existing = desk.windows.find(w => w.appId === action.appId)
      if (existing) {
        desk.activeWindow = existing.id
        desktops[desktopId] = desk
        return { ...state, desktops }
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
        // Position below menu bar (32px), above dock (64px)
        // Note: windows are absolutely positioned in inset-0 container,
        // so y must account for menu bar height
        return {
          ...w,
          _prevPosition: w.position, _prevSize: w.size, _maximized: true,
          position: { x: 0, y: 32 },
          size: { width: window.innerWidth, height: window.innerHeight - 32 - 64 },
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
    case 'RESTORE_STATE':
      return { ...state, ...action.saved, conversation: state.conversation }
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
  } catch {}
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
  conversation: [],
  thinking: false,
  launchpadOpen: false,
  chatOpen: false,
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
  const closeWindow = useCallback((id) => dispatch({ type: 'CLOSE_WINDOW', id }), [])
  const focusWindow = useCallback((id) => dispatch({ type: 'FOCUS_WINDOW', id }), [])
  const moveWindow = useCallback((id, position) => dispatch({ type: 'MOVE_WINDOW', id, position }), [])
  const resizeWindow = useCallback((id, size) => dispatch({ type: 'RESIZE_WINDOW', id, size }), [])
  const minimizeWindow = useCallback((id) => dispatch({ type: 'MINIMIZE_WINDOW', id }), [])
  const maximizeWindow = useCallback((id) => dispatch({ type: 'MAXIMIZE_WINDOW', id }), [])

  const switchDesktop = useCallback((id) => dispatch({ type: 'SWITCH_DESKTOP', id }), [])
  const addDesktop = useCallback((label) => dispatch({ type: 'ADD_DESKTOP', label }), [])
  const removeDesktop = useCallback((id) => dispatch({ type: 'REMOVE_DESKTOP', id }), [])
  const moveWindowToDesktop = useCallback((windowId, desktopId) => dispatch({ type: 'MOVE_WINDOW_TO_DESKTOP', windowId, desktopId }), [])

  const popoutApp = useCallback((app) => dispatch({ type: 'POPOUT_APP', app }), [])
  const closePopout = useCallback(() => dispatch({ type: 'CLOSE_POPOUT' }), [])

  const toggleLaunchpad = useCallback(() => dispatch({ type: 'TOGGLE_LAUNCHPAD' }), [])
  const setLaunchpad = useCallback((open) => dispatch({ type: 'SET_LAUNCHPAD', open }), [])
  const toggleChat = useCallback(() => dispatch({ type: 'TOGGLE_CHAT' }), [])
  const setChat = useCallback((open) => dispatch({ type: 'SET_CHAT', open }), [])
  const addMessage = useCallback((role, text, meta) => {
    dispatch({ type: 'ADD_MESSAGE', message: { id: Date.now() + Math.random(), role, text, meta, timestamp: new Date() } })
  }, [])
  const setThinking = useCallback((value) => dispatch({ type: 'SET_THINKING', value }), [])

  return (
    <ShellContext.Provider value={{
      windows, activeWindow, allWindows,
      desktops: state.desktops, activeDesktop: state.activeDesktop,
      popout: state.popout,
      conversation: state.conversation, thinking: state.thinking,
      launchpadOpen: state.launchpadOpen, chatOpen: state.chatOpen,
      layout,
      openWindow, closeWindow, focusWindow, moveWindow, resizeWindow, minimizeWindow, maximizeWindow,
      switchDesktop, addDesktop, removeDesktop, moveWindowToDesktop,
      popoutApp, closePopout,
      toggleLaunchpad, setLaunchpad, toggleChat, setChat,
      addMessage, setThinking,
    }}>
      {children}
    </ShellContext.Provider>
  )
}

export function useShell() {
  const ctx = useContext(ShellContext)
  if (!ctx) throw new Error('useShell must be used within ShellProvider')
  return ctx
}

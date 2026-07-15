import { useState, useEffect, useRef } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getApps, searchApps } from '../core/AppRegistry'
import { launchApp } from './launchApp'
import { AppIconTile } from '../core/AppIcons'
import { useFocusTrap } from './useFocusTrap'

// The full lane-dispatch launch logic lives in the shared ./launchApp module so
// the Launchpad and the ⌘K command palette open apps identically.
// Mail is the gateway-proxied `lilmail` connector; Office is the standalone
// `vulos-office` web app; Calendar and Contacts are standalone builtin React
// apps over lilmail's /v1 (via /api/pim/*). Real-time comms (Talk/Meet) are
// third-party now and are not registered as OS apps.

const categoryLabels = {
  internet: 'Internet',
  productivity: 'Productivity',
  utilities: 'Utilities',
  media: 'Media',
  developer: 'Developer',
  system: 'System',
}

export default function Launchpad() {
  const { launchpadOpen, setLaunchpad, openWindow, setChat } = useShell()
  const [search, setSearch] = useState('')
  const [chatInput, setChatInput] = useState('')
  const [desktopEntries, setDesktopEntries] = useState([])
  const searchRef = useRef(null)
  const chatRef = useRef(null)
  // A11Y: trap focus inside the launchpad while open + restore on close.
  const trapRef = useFocusTrap(launchpadOpen)

  // Load desktop entries (apt-installed GUI apps)
  useEffect(() => {
    if (!launchpadOpen) return
    fetch('/api/desktop/entries')
      .then(r => r.json())
      .then(entries => setDesktopEntries(entries || []))
      .catch(() => {})
  }, [launchpadOpen])

  // ESC to close + focus search on open
  useEffect(() => {
    if (!launchpadOpen) return
    const handler = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        setLaunchpad(false)
        setSearch('')
        setChatInput('')
      }
    }
    window.addEventListener('keydown', handler, true)
    // Auto-focus search
    setTimeout(() => searchRef.current?.focus(), 50)
    return () => window.removeEventListener('keydown', handler, true)
  }, [launchpadOpen, setLaunchpad])

  if (!launchpadOpen) return null

  const close = () => { setLaunchpad(false); setSearch(''); setChatInput('') }

  // Merge builtin/registry apps with desktop entries (apt-installed GUI apps)
  const baseApps = search.trim() ? searchApps(search) : getApps()
  const baseIds = new Set(baseApps.map(a => a.id))

  // IDs of desktop entries that duplicate built-in apps (e.g. Chromium browser)
  const hiddenDesktopIds = new Set(['chromium-browser', 'chromium', 'org.chromium.Chromium'])

  // Convert desktop entries to app format, filter out duplicates and hidden entries
  const desktopApps = desktopEntries
    .filter(e => !e.no_display && e.exec && !baseIds.has(e.id) && !hiddenDesktopIds.has(e.id))
    .map(e => ({
      id: e.id,
      name: e.name,
      icon: e.icon || e.id,
      category: mapDesktopCategory(e.categories),
      _desktop: true, // Flag for stream-based launch
      _exec: e.exec,
    }))
    .filter(a => {
      if (!search.trim()) return true
      const q = search.toLowerCase()
      return a.name.toLowerCase().includes(q) || a.id.toLowerCase().includes(q)
    })

  const apps = [...baseApps, ...desktopApps]
  const grouped = search.trim() ? null : groupByCategory(apps)

  const launch = async (app) => {
    // All lane dispatch (builtin · web · stream · native · fallback) lives in
    // the shared launchApp helper, which the ⌘K palette also uses.
    await launchApp(app, { openWindow })
    close()
  }

  const handleChatSubmit = (e) => {
    e.preventDefault()
    if (!chatInput.trim()) return
    close()
    // Open chat panel and send the message
    setChat(true)
    // Dispatch a custom event so Portal can pick it up
    window.dispatchEvent(new CustomEvent('vulos:chat', { detail: chatInput.trim() }))
    setChatInput('')
  }

  return (
    <div
      ref={trapRef}
      role="dialog"
      aria-modal="true"
      aria-label="Application launcher"
      className="fixed inset-0 z-50 flex flex-col bg-neutral-950/80 backdrop-blur-2xl"
      onClick={(e) => { if (e.target === e.currentTarget) close() }}
    >
      {/* Search bar */}
      <div className="flex justify-center pt-10 pb-4 px-6">
        <div className="w-full max-w-lg relative">
          <div className="absolute left-3.5 top-1/2 -translate-y-1/2 text-neutral-500">
            <svg viewBox="0 0 16 16" className="w-4 h-4" fill="none" stroke="currentColor" strokeWidth="1.5">
              <circle cx="7" cy="7" r="5" />
              <path d="M11 11l3.5 3.5" strokeLinecap="round" />
            </svg>
          </div>
          <input
            ref={searchRef}
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search applications..."
            className="w-full bg-neutral-800/70 border border-neutral-700/50 rounded-xl pl-10 pr-4 py-3 text-sm text-white outline-none placeholder:text-neutral-500 focus:border-neutral-500/70 transition-colors"
          />
          {search && (
            <button
              onClick={() => setSearch('')}
              aria-label="Clear search"
              className="focus-primary rounded absolute right-3 top-1/2 -translate-y-1/2 text-neutral-500 hover:text-neutral-300 text-lg"
            >
              {'\u00D7'}
            </button>
          )}
        </div>
      </div>

      {/* App grid */}
      <div className="flex-1 overflow-y-auto px-6 pb-4">
        <div className="max-w-3xl mx-auto">
          {grouped ? (
            Object.entries(grouped).map(([cat, catApps]) => (
              <div key={cat} className="mb-6">
                <h3 className="text-[11px] uppercase tracking-wider text-neutral-500 mb-2.5 px-1 font-medium">
                  {categoryLabels[cat] || cat}
                </h3>
                <div className="grid grid-cols-4 sm:grid-cols-5 md:grid-cols-6 lg:grid-cols-7 gap-2">
                  {catApps.map(app => (
                    <AppTile key={app.id} app={app} onLaunch={launch} />
                  ))}
                </div>
              </div>
            ))
          ) : (
            <div className="grid grid-cols-4 sm:grid-cols-5 md:grid-cols-6 lg:grid-cols-7 gap-2">
              {apps.map(app => (
                <AppTile key={app.id} app={app} onLaunch={launch} />
              ))}
            </div>
          )}

          {apps.length === 0 && (
            <div className="text-center text-neutral-600 py-16 text-sm">
              No applications found
            </div>
          )}
        </div>
      </div>

      {/* Bottom bar — chat input + ESC hint */}
      <div className="flex-shrink-0 border-t border-neutral-800/40 bg-neutral-900/50 backdrop-blur-xl">
        <div className="max-w-lg mx-auto px-6 py-3">
          <form onSubmit={handleChatSubmit} className="flex items-center gap-2">
            <div className="flex-1 relative">
              <div className="absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600">
                <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor" opacity="0.6">
                  <path d="M2 3a2 2 0 012-2h8a2 2 0 012 2v6a2 2 0 01-2 2H6l-3 3V11H4a2 2 0 01-2-2V3z" />
                </svg>
              </div>
              <input
                ref={chatRef}
                type="text"
                value={chatInput}
                onChange={(e) => setChatInput(e.target.value)}
                placeholder="Ask anything..."
                className="w-full bg-neutral-800/50 border border-neutral-700/40 rounded-lg pl-9 pr-3 py-2 text-sm text-white outline-none placeholder:text-neutral-600 focus:border-neutral-600/60 transition-colors"
              />
            </div>
            <button
              type="submit"
              disabled={!chatInput.trim()}
              className="focus-primary px-3 py-2 rounded-lg text-xs font-medium bg-neutral-700/50 text-neutral-400 hover:bg-neutral-600/50 hover:text-neutral-200 disabled:opacity-30 disabled:cursor-default transition-colors"
            >
              Send
            </button>
            <kbd className="text-[10px] text-neutral-600 border border-neutral-800 rounded px-1.5 py-1 ml-1 select-none">esc</kbd>
          </form>
        </div>
      </div>
    </div>
  )
}

// Map freedesktop.org categories to our launchpad categories
function mapDesktopCategory(cats) {
  if (!cats || cats.length === 0) return 'utilities'
  const c = cats.map(s => s.toLowerCase())
  if (c.some(s => ['game', 'amusement', 'blocksgame', 'boardgame', 'cardgame'].includes(s))) return 'media'
  if (c.some(s => ['development', 'ide', 'debugger', 'building', 'database'].includes(s))) return 'developer'
  if (c.some(s => ['graphics', 'audio', 'video', 'audiovisual', 'music', 'player', 'recorder'].includes(s))) return 'media'
  if (c.some(s => ['network', 'webbrowser', 'email', 'chat', 'instantmessaging', 'remoteaccess'].includes(s))) return 'internet'
  if (c.some(s => ['office', 'wordprocessor', 'spreadsheet', 'presentation', 'calendar', 'contactmanagement'].includes(s))) return 'productivity'
  if (c.some(s => ['system', 'monitor', 'settings', 'terminalemulator', 'filesystem'].includes(s))) return 'system'
  if (c.some(s => ['utility', 'texteditor', 'archiving', 'calculator', 'clock'].includes(s))) return 'utilities'
  return 'utilities'
}

// Group apps by category (replacement for getAppsByCategory that works with merged list)
function groupByCategory(apps) {
  const groups = {}
  for (const app of apps) {
    const cat = app.category || 'utilities'
    if (!groups[cat]) groups[cat] = []
    groups[cat].push(app)
  }
  return groups
}

function AppTile({ app, onLaunch }) {
  return (
    <button
      onClick={() => onLaunch(app)}
      aria-label={`Open ${app.name}`}
      className="focus-primary flex flex-col items-center gap-1.5 p-2.5 rounded-xl hover:bg-white/5 active:bg-white/10 transition-colors group"
    >
      <AppIconTile id={app.id} size={48} unicode={app.icon} />
      <span className="text-[11px] text-neutral-400 group-hover:text-neutral-200 text-center truncate w-full transition-colors">{app.name}</span>
    </button>
  )
}

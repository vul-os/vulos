import { useState, useEffect, useRef, createElement, lazy, Suspense } from 'react'
import { useShell } from '../providers/ShellProvider'
import { getApps, searchApps, getAppsByCategory } from '../core/AppRegistry'
import Settings from '../core/Settings'
import { AppIconTile } from '../core/AppIcons'

const Terminal = lazy(() => import('../builtin/terminal/Terminal'))
const ActivityMonitor = lazy(() => import('../builtin/activity/ActivityMonitor'))
const FileManager = lazy(() => import('../builtin/files/FileManager'))
const RemoteBrowser = lazy(() => import('../builtin/webbrowser/RemoteBrowser'))
const AppHub = lazy(() => import('../builtin/apphub/AppHub'))
const Drivers = lazy(() => import('../builtin/drivers/Drivers'))
const Packages = lazy(() => import('../builtin/packages/Packages'))
const DiskUsage = lazy(() => import('../builtin/disks/DiskUsage'))

const categoryLabels = {
  core: 'Core',
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
  const searchRef = useRef(null)
  const chatRef = useRef(null)

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

  const apps = search.trim() ? searchApps(search) : getApps()
  const grouped = search.trim() ? null : getAppsByCategory()

  const launch = async (app) => {
    const loading = createElement('div', { className: 'p-4 text-neutral-500' }, 'Loading...')
    const builtins = {
      persona: () => createElement(Settings),
      terminal: () => createElement(Suspense, { fallback: loading }, createElement(Terminal)),
      activity: () => createElement(Suspense, { fallback: loading }, createElement(ActivityMonitor)),
      files: () => createElement(Suspense, { fallback: loading }, createElement(FileManager)),
      browser: () => createElement(Suspense, { fallback: loading }, createElement(RemoteBrowser)),
      apphub: () => createElement(Suspense, { fallback: loading }, createElement(AppHub)),
      drivers: () => createElement(Suspense, { fallback: loading }, createElement(Drivers)),
      packages: () => createElement(Suspense, { fallback: loading }, createElement(Packages)),
      disks: () => createElement(Suspense, { fallback: loading }, createElement(DiskUsage)),
    }
    if (builtins[app.id]) {
      openWindow({ appId: app.id, title: app.name, icon: app.icon, component: builtins[app.id]() })
      close()
      return
    }

    try {
      const res = await fetch('/api/apps/launch', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: app.id, app_port: app.port || 80, command: app.command || '' }),
      })
      if (res.ok) {
        const data = await res.json()
        const url = data.url || `/app/${app.id}/`
        openWindow({ appId: app.id, title: app.name, url, icon: app.icon })
      } else {
        openWindow({ appId: app.id, title: app.name, url: `/app/${app.id}/`, icon: app.icon })
      }
    } catch {
      openWindow({ appId: app.id, title: app.name, url: `/app/${app.id}/`, icon: app.icon })
    }
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
              className="absolute right-3 top-1/2 -translate-y-1/2 text-neutral-500 hover:text-neutral-300 text-lg"
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
              className="px-3 py-2 rounded-lg text-xs font-medium bg-neutral-700/50 text-neutral-400 hover:bg-neutral-600/50 hover:text-neutral-200 disabled:opacity-30 disabled:cursor-default transition-colors"
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

function AppTile({ app, onLaunch }) {
  return (
    <button
      onClick={() => onLaunch(app)}
      className="flex flex-col items-center gap-1.5 p-2.5 rounded-xl hover:bg-white/5 transition-colors group"
    >
      <AppIconTile id={app.id} size={48} unicode={app.icon} />
      <span className="text-[11px] text-neutral-400 group-hover:text-neutral-200 text-center truncate w-full transition-colors">{app.name}</span>
    </button>
  )
}

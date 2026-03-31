import { useState, useEffect } from 'react'
import { refreshInstalled } from '../../core/AppRegistry'
import { APP_LOGOS, APP_COLORS } from '../../core/AppIcons'

const CATEGORY_LABELS = {
  all: 'All',
  internet: 'Internet',
  media: 'Media',
  developer: 'Developer',
  productivity: 'Productivity',
  network: 'Network',
  database: 'Database',
  system: 'System',
}

function AppIcon({ appId, size = 44 }) {
  const [failed, setFailed] = useState(false)
  const logo = APP_LOGOS[appId]
  const color = APP_COLORS[appId] || '#555'
  const radius = Math.round(size * 0.22)

  if (logo && !failed) {
    return (
      <div
        className="flex-shrink-0 flex items-center justify-center bg-white/5 overflow-hidden"
        style={{ width: size, height: size, borderRadius: radius }}
      >
        <img
          src={logo}
          alt=""
          className="w-3/4 h-3/4 object-contain"
          onError={() => setFailed(true)}
          loading="lazy"
        />
      </div>
    )
  }

  return (
    <div
      className="flex-shrink-0 flex items-center justify-center font-bold text-white/80"
      style={{
        width: size, height: size, borderRadius: radius,
        background: `linear-gradient(135deg, ${color}40, ${color}20)`,
        border: `1px solid ${color}30`,
        fontSize: size * 0.36,
      }}
    >
      {appId?.[0]?.toUpperCase() || '?'}
    </div>
  )
}

export default function AppHub() {
  const [apps, setApps] = useState([])
  const [installed, setInstalled] = useState([])
  const [loading, setLoading] = useState(true)
  const [installing, setInstalling] = useState(null)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [selectedApp, setSelectedApp] = useState(null)
  const [selectedVersion, setSelectedVersion] = useState('')
  const [tab, setTab] = useState('browse')
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(null)

  const [cacheReady, setCacheReady] = useState(null)
  const [systemArch, setSystemArch] = useState(null)
  const [updatingCache, setUpdatingCache] = useState(false)
  const [updateProgress, setUpdateProgress] = useState('')

  const fetchData = async () => {
    setLoading(true)
    try {
      const [regRes, instRes, cacheRes] = await Promise.all([
        fetch('/api/store/registry'),
        fetch('/api/store/installed'),
        fetch('/api/packages/cache'),
      ])
      const regData = await regRes.json()
      const instData = await instRes.json()
      const cacheData = await cacheRes.json()
      setApps(regData || [])
      setInstalled(instData || [])
      setCacheReady(cacheData.ready)
      setSystemArch(cacheData.arch || null)
    } catch {
      setApps([])
      setInstalled([])
    }
    setLoading(false)
  }

  const updateAptCache = async () => {
    setUpdatingCache(true)
    setUpdateProgress('Updating package index...')
    setError(null)
    try {
      const res = await fetch('/api/packages/update', { method: 'POST' })
      if (!res.ok) throw new Error('Update failed')
      setUpdateProgress('Package index updated')
      setCacheReady(true)
      setTimeout(() => setUpdateProgress(''), 2000)
    } catch (e) {
      setError('Failed to update package index: ' + e.message)
    }
    setUpdatingCache(false)
  }

  useEffect(() => { fetchData() }, [])

  const installApp = async (appId, version) => {
    setInstalling(appId)
    setError(null)
    setSuccess(null)
    try {
      const res = await fetch('/api/store/registry/install', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId, version: version || '' }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        const msg = data.error || 'Install failed'
        // Parse structured error from backend
        const detail = data.detail || ''
        throw new Error(detail ? `${msg}\n${detail}` : msg)
      }
      setSuccess(`${apps.find(a => a.id === appId)?.name || appId} installed`)
      setTimeout(() => setSuccess(null), 4000)
      await refreshInstalled()
      await fetchData()
    } catch (e) {
      setError(e.message)
    }
    setInstalling(null)
  }

  const uninstallApp = async (appId) => {
    setInstalling(appId)
    setError(null)
    try {
      const res = await fetch('/api/store/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId }),
      })
      if (!res.ok) throw new Error('Uninstall failed')
      setSuccess(`${apps.find(a => a.id === appId)?.name || appId} removed`)
      setTimeout(() => setSuccess(null), 4000)
      await refreshInstalled()
      await fetchData()
      if (selectedApp?.id === appId) setSelectedApp(null)
    } catch (e) {
      setError(e.message)
    }
    setInstalling(null)
  }

  // Check if an app is compatible with the current system architecture
  const isArchCompatible = (app) => {
    if (!systemArch || !app.arch || app.arch.length === 0) return true
    return app.arch.includes(systemArch)
  }

  const filtered = apps.filter(app => {
    if (category !== 'all' && app.category !== category) return false
    if (search.trim()) {
      const q = search.toLowerCase()
      return app.name.toLowerCase().includes(q) ||
        app.description.toLowerCase().includes(q) ||
        app.id.toLowerCase().includes(q)
    }
    return true
  })

  const categories = ['all', ...new Set(apps.map(a => a.category).filter(Boolean).sort())]
  const installedIds = new Set((installed || []).map(a => a.id))
  const browseList = tab === 'installed' ? filtered.filter(a => a.installed || installedIds.has(a.id)) : filtered

  return (
    <div className="flex h-full bg-neutral-950 text-neutral-300 overflow-hidden">
      {/* Sidebar */}
      <div className="w-52 flex-shrink-0 border-r border-neutral-800/40 flex flex-col bg-neutral-950/80">
        <div className="px-4 pt-5 pb-3">
          <h1 className="text-[15px] font-semibold text-neutral-100">App Store</h1>
          <p className="text-[11px] text-neutral-600 mt-0.5">{apps.length} apps</p>
        </div>

        {/* Tabs */}
        <div className="px-3 pb-3 flex flex-col gap-0.5">
          <button
            className={`flex items-center gap-2.5 px-3 py-2 rounded-lg text-[12px] font-medium transition-colors text-left ${
              tab === 'browse' ? 'bg-neutral-800/70 text-neutral-100' : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
            }`}
            onClick={() => setTab('browse')}
          >
            <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor" opacity={0.7}>
              <path d="M2 3.5A1.5 1.5 0 013.5 2h2A1.5 1.5 0 017 3.5v2A1.5 1.5 0 015.5 7h-2A1.5 1.5 0 012 5.5v-2zM2 10.5A1.5 1.5 0 013.5 9h2A1.5 1.5 0 017 10.5v2A1.5 1.5 0 015.5 14h-2A1.5 1.5 0 012 12.5v-2zM9 3.5A1.5 1.5 0 0110.5 2h2A1.5 1.5 0 0114 3.5v2A1.5 1.5 0 0112.5 7h-2A1.5 1.5 0 019 5.5v-2zM9 10.5A1.5 1.5 0 0110.5 9h2a1.5 1.5 0 011.5 1.5v2a1.5 1.5 0 01-1.5 1.5h-2A1.5 1.5 0 019 12.5v-2z" />
            </svg>
            Browse
          </button>
          <button
            className={`flex items-center gap-2.5 px-3 py-2 rounded-lg text-[12px] font-medium transition-colors text-left ${
              tab === 'installed' ? 'bg-neutral-800/70 text-neutral-100' : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
            }`}
            onClick={() => setTab('installed')}
          >
            <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor" opacity={0.7}>
              <path d="M8.175.004a.75.75 0 01.625.35l2.2 3.3a.75.75 0 01-.625 1.15H9v4.25a.75.75 0 01-1.5 0V4.804H6.625a.75.75 0 01-.625-1.15l2.2-3.3a.75.75 0 01.975-.35zM3.5 10a.75.75 0 01.75.75v2.5h7.5v-2.5a.75.75 0 011.5 0v2.5A1.75 1.75 0 0111.5 15h-7A1.75 1.75 0 012.75 13.25v-2.5A.75.75 0 013.5 10z" />
            </svg>
            Installed
            {installed?.length > 0 && (
              <span className="ml-auto text-[10px] bg-neutral-700/80 text-neutral-400 px-1.5 py-0.5 rounded-full min-w-[18px] text-center">
                {installed.length}
              </span>
            )}
          </button>
        </div>

        {/* Categories */}
        <div className="px-3 pt-2 border-t border-neutral-800/30">
          <div className="text-[10px] uppercase tracking-wider text-neutral-600 font-medium px-3 py-2">Categories</div>
          <div className="flex flex-col gap-0.5">
            {categories.map(cat => (
              <button
                key={cat}
                className={`px-3 py-1.5 rounded-lg text-[12px] transition-colors text-left ${
                  category === cat
                    ? 'bg-neutral-800/70 text-neutral-100 font-medium'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
                }`}
                onClick={() => setCategory(cat)}
              >
                {CATEGORY_LABELS[cat] || cat}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {/* Apt cache update banner */}
        {cacheReady === false && !updatingCache && (
          <div className="mx-5 mt-3 px-4 py-3 rounded-xl bg-blue-950/40 border border-blue-800/40 flex items-center gap-3">
            <div className="w-9 h-9 rounded-lg bg-blue-600/20 flex items-center justify-center flex-shrink-0">
              <svg viewBox="0 0 20 20" className="w-5 h-5 text-blue-400" fill="currentColor">
                <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
              </svg>
            </div>
            <div className="flex-1 min-w-0">
              <div className="text-[13px] font-medium text-blue-300">Update Package Index</div>
              <div className="text-[11px] text-blue-400/70 mt-0.5">Required before installing apps. Downloads the latest package list from Debian repositories.</div>
            </div>
            <button
              onClick={updateAptCache}
              className="px-4 py-2 rounded-lg text-[12px] font-semibold text-white bg-blue-600 hover:bg-blue-500 transition-all flex-shrink-0 shadow-lg shadow-blue-600/20"
            >
              Update Now
            </button>
          </div>
        )}
        {updatingCache && (
          <div className="mx-5 mt-3 px-4 py-3 rounded-xl bg-blue-950/40 border border-blue-800/40 flex items-center gap-3">
            <span className="w-5 h-5 border-2 border-blue-400/30 border-t-blue-400 rounded-full animate-spin flex-shrink-0" />
            <div className="text-[12px] text-blue-300">{updateProgress || 'Updating...'}</div>
          </div>
        )}

        {/* Search bar */}
        <div className="px-5 pt-4 pb-3 flex-shrink-0">
          <div className="relative max-w-md">
            <svg viewBox="0 0 16 16" className="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600" fill="none" stroke="currentColor" strokeWidth="1.5">
              <circle cx="7" cy="7" r="5" />
              <path d="M11 11l3.5 3.5" strokeLinecap="round" />
            </svg>
            <input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search apps..."
              className="w-full bg-neutral-900/50 border border-neutral-800/50 rounded-xl pl-10 pr-4 py-2.5 text-[13px] text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-600 focus:ring-1 focus:ring-neutral-700/50 transition-all"
            />
          </div>
        </div>

        {/* Toasts */}
        {error && (
          <div className="mx-5 mb-3 rounded-xl bg-red-950/50 border border-red-900/40 overflow-hidden">
            <div className="px-4 py-3 flex items-start gap-3">
              <div className="w-5 h-5 rounded-full bg-red-500/20 flex items-center justify-center flex-shrink-0 mt-0.5">
                <svg viewBox="0 0 16 16" className="w-3 h-3 text-red-400" fill="currentColor">
                  <path d="M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z" />
                </svg>
              </div>
              <div className="flex-1 min-w-0">
                <div className="text-[12px] font-medium text-red-300">Installation failed</div>
                <pre className="text-[11px] text-red-400/80 mt-1 whitespace-pre-wrap break-words font-mono leading-relaxed max-h-32 overflow-y-auto">{error}</pre>
              </div>
              <button onClick={() => setError(null)} className="text-red-500/60 hover:text-red-400 transition-colors flex-shrink-0">
                <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor">
                  <path d="M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z" />
                </svg>
              </button>
            </div>
          </div>
        )}
        {success && (
          <div className="mx-5 mb-3 px-4 py-3 rounded-xl bg-emerald-950/50 border border-emerald-900/40 text-[12px] text-emerald-400 flex items-center gap-2.5">
            <svg viewBox="0 0 16 16" className="w-4 h-4 flex-shrink-0" fill="currentColor">
              <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
            </svg>
            {success}
          </div>
        )}

        {/* App grid */}
        <div className="flex-1 overflow-y-auto px-5 pb-6">
          {loading ? (
            <div className="flex items-center justify-center h-48 text-neutral-600">
              <span className="w-5 h-5 border-2 border-neutral-700 border-t-neutral-400 rounded-full animate-spin" />
            </div>
          ) : browseList.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-48 text-neutral-600 gap-2">
              <svg viewBox="0 0 24 24" className="w-10 h-10 text-neutral-800" fill="currentColor">
                <path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z" />
              </svg>
              <span className="text-[13px]">{tab === 'installed' ? 'No apps installed yet' : 'No apps found'}</span>
            </div>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {browseList.map(app => {
                const isInstalled = app.installed || installedIds.has(app.id)
                const isActive = installing === app.id
                return (
                  <div
                    key={app.id}
                    className={`group relative flex items-start gap-3.5 p-4 rounded-2xl cursor-pointer transition-all duration-150 ${
                      selectedApp?.id === app.id
                        ? 'bg-neutral-800/60 ring-1 ring-neutral-600/50'
                        : 'bg-neutral-900/30 hover:bg-neutral-800/40 border border-neutral-800/30 hover:border-neutral-700/50'
                    }`}
                    onClick={() => { setSelectedApp(app); setSelectedVersion(app.latest || '') }}
                  >
                    <AppIcon appId={app.icon || app.id} size={44} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-[13px] font-medium text-neutral-100 truncate">{app.name}</span>
                        {app.type === 'desktop' && (
                          <span className="text-[9px] font-medium uppercase tracking-wider px-1.5 py-0.5 rounded bg-purple-500/10 text-purple-400 border border-purple-500/15 flex-shrink-0">
                            Desktop
                          </span>
                        )}
                      </div>
                      <p className="text-[11px] text-neutral-500 mt-0.5 line-clamp-2 leading-relaxed">{app.description}</p>
                      <div className="flex items-center gap-2 mt-2">
                        {isInstalled ? (
                          <span className="text-[10px] font-medium text-emerald-400/80 flex items-center gap-1">
                            <svg viewBox="0 0 16 16" className="w-3 h-3" fill="currentColor">
                              <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                            </svg>
                            Installed
                          </span>
                        ) : !isArchCompatible(app) ? (
                          <span className="text-[9px] font-medium text-red-400/70 bg-red-500/10 px-2 py-1 rounded border border-red-500/15" title={`Requires ${app.arch?.join(', ')} — this system is ${systemArch}`}>
                            {systemArch === 'arm64' ? 'x86 only' : 'Incompatible'}
                          </span>
                        ) : (
                          <button
                            className="text-[11px] font-medium text-blue-400 hover:text-blue-300 bg-blue-500/10 hover:bg-blue-500/20 px-3 py-1 rounded-lg transition-colors border border-blue-500/15 hover:border-blue-500/25 disabled:opacity-40"
                            onClick={e => { e.stopPropagation(); installApp(app.id, app.latest) }}
                            disabled={isActive}
                          >
                            {isActive ? (
                              <span className="flex items-center gap-1.5">
                                <span className="w-3 h-3 border-2 border-blue-400/30 border-t-blue-400 rounded-full animate-spin" />
                                Installing...
                              </span>
                            ) : 'Get'}
                          </button>
                        )}
                        {app.author && (
                          <span className="text-[10px] text-neutral-600 truncate">{app.author}</span>
                        )}
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>

      {/* Detail panel */}
      {selectedApp && (
        <div className="w-80 flex-shrink-0 border-l border-neutral-800/40 flex flex-col bg-neutral-950/90 overflow-hidden">
          {/* Header */}
          <div className="p-5 border-b border-neutral-800/30">
            <div className="flex items-start gap-3.5">
              <AppIcon appId={selectedApp.icon || selectedApp.id} size={56} />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <h2 className="text-[16px] font-semibold text-neutral-100 truncate">{selectedApp.name}</h2>
                  {selectedApp.vetted && (
                    <svg viewBox="0 0 16 16" className="w-4 h-4 text-blue-400 flex-shrink-0" fill="currentColor">
                      <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                    </svg>
                  )}
                </div>
                <div className="text-[12px] text-neutral-500 mt-0.5">{selectedApp.author || 'Unknown'}</div>
              </div>
              <button
                className="text-neutral-600 hover:text-neutral-400 transition-colors p-1 -mt-1 -mr-1"
                onClick={() => setSelectedApp(null)}
              >
                <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor">
                  <path d="M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z" />
                </svg>
              </button>
            </div>
          </div>

          <div className="flex-1 overflow-y-auto p-5 flex flex-col gap-5">
            <p className="text-[13px] text-neutral-400 leading-relaxed">{selectedApp.description}</p>

            {selectedApp.type === 'desktop' && (
              <div className="flex items-start gap-2.5 px-3.5 py-3 rounded-xl bg-purple-500/5 border border-purple-500/10">
                <svg viewBox="0 0 16 16" className="w-4 h-4 text-purple-400 mt-0.5 flex-shrink-0" fill="currentColor">
                  <path d="M0 1.75C0 .784.784 0 1.75 0h12.5C15.216 0 16 .784 16 1.75v9.5A1.75 1.75 0 0114.25 13H8.06l-2.573 2.573A1.458 1.458 0 013 14.543V13H1.75A1.75 1.75 0 010 11.25v-9.5zm1.75-.25a.25.25 0 00-.25.25v9.5c0 .138.112.25.25.25h2a.75.75 0 01.75.75v2.19l2.72-2.72a.75.75 0 01.53-.22h6.5a.25.25 0 00.25-.25v-9.5a.25.25 0 00-.25-.25H1.75z" />
                </svg>
                <p className="text-[11px] text-purple-300/70 leading-relaxed">
                  Native desktop app streamed via remote display. Full GPU acceleration when available.
                </p>
              </div>
            )}

            {/* Actions */}
            <div className="flex gap-2">
              {selectedApp.installed || installedIds.has(selectedApp.id) ? (
                <>
                  <div className="flex-1 text-center text-[12px] font-medium text-emerald-400 bg-emerald-500/10 py-3 rounded-xl border border-emerald-500/15 flex items-center justify-center gap-1.5">
                    <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
                      <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                    </svg>
                    Installed
                  </div>
                  <button
                    className="px-4 py-3 rounded-xl text-[12px] font-medium text-red-400/80 border border-red-900/40 hover:bg-red-950/40 transition-colors disabled:opacity-40"
                    onClick={() => uninstallApp(selectedApp.id)}
                    disabled={installing === selectedApp.id}
                  >
                    {installing === selectedApp.id ? 'Removing...' : 'Remove'}
                  </button>
                </>
              ) : !isArchCompatible(selectedApp) ? (
                <div className="flex-1 py-3 rounded-xl text-[12px] font-medium text-red-400/80 bg-red-500/8 border border-red-500/15 text-center">
                  Not available for {systemArch === 'arm64' ? 'ARM64' : systemArch} — requires {selectedApp.arch?.join(', ')}
                </div>
              ) : (
                <button
                  className="flex-1 py-3 rounded-xl text-[13px] font-semibold text-white bg-blue-600 hover:bg-blue-500 transition-all disabled:opacity-40 shadow-lg shadow-blue-600/15"
                  onClick={() => installApp(selectedApp.id, selectedVersion)}
                  disabled={installing === selectedApp.id}
                >
                  {installing === selectedApp.id ? (
                    <span className="flex items-center justify-center gap-2">
                      <span className="w-4 h-4 border-2 border-blue-300/30 border-t-white rounded-full animate-spin" />
                      Installing...
                    </span>
                  ) : `Install${selectedVersion ? ` ${selectedVersion}` : ''}`}
                </button>
              )}
            </div>

            {/* Version picker */}
            {(selectedApp.versions || []).length > 1 && (
              <div>
                <div className="text-[11px] uppercase tracking-wider text-neutral-600 mb-2 font-medium">Version</div>
                <div className="flex gap-1.5 flex-wrap">
                  {(selectedApp.versions || []).map(v => (
                    <button
                      key={v}
                      className={`px-3 py-1.5 rounded-lg text-[12px] transition-all ${
                        selectedVersion === v
                          ? 'bg-blue-600/15 text-blue-400 border border-blue-500/25'
                          : 'bg-neutral-900/60 text-neutral-500 border border-neutral-800/50 hover:border-neutral-700'
                      }`}
                      onClick={() => setSelectedVersion(v)}
                    >
                      {v}
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Details */}
            <div>
              <div className="text-[11px] uppercase tracking-wider text-neutral-600 mb-2 font-medium">About</div>
              <div className="rounded-xl border border-neutral-800/40 divide-y divide-neutral-800/30 overflow-hidden">
                <DetailRow label="Type" value={selectedApp.type === 'desktop' ? 'Desktop App' : 'Web App'} />
                <DetailRow label="Category" value={CATEGORY_LABELS[selectedApp.category] || selectedApp.category} />
                <DetailRow label="License" value={selectedApp.license || '\u2014'} />
                <DetailRow label="Architecture" value={selectedApp.arch?.length ? selectedApp.arch.join(', ') : 'All'} />
                <DetailRow label="System" value={systemArch || 'Unknown'} />
                {selectedApp.homepage && <DetailRow label="Website" value={selectedApp.homepage} link />}
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function DetailRow({ label, value, link }) {
  return (
    <div className="flex justify-between items-center px-3.5 py-2.5 bg-neutral-900/20">
      <span className="text-[11px] text-neutral-600">{label}</span>
      {link ? (
        <a
          href={value}
          target="_blank"
          rel="noopener noreferrer"
          className="text-[11px] text-blue-400/70 hover:text-blue-400 truncate max-w-[160px] transition-colors"
        >
          {value.replace(/^https?:\/\/(www\.)?/, '')}
        </a>
      ) : (
        <span className="text-[11px] text-neutral-400 truncate max-w-[160px]">{value}</span>
      )}
    </div>
  )
}

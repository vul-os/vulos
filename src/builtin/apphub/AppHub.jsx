import { useState, useEffect } from 'react'
import { refreshInstalled } from '../../core/AppRegistry'

// SVG icon paths for known apps — each is a 24x24 viewBox path
const APP_ICONS = {
  adminer: { color: '#43853d', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 3c1.66 0 3 1.34 3 3s-1.34 3-3 3-3-1.34-3-3 1.34-3 3-3zm0 14.2c-2.5 0-4.71-1.28-6-3.22.03-1.99 4-3.08 6-3.08 1.99 0 5.97 1.09 6 3.08-1.29 1.94-3.5 3.22-6 3.22z' },
  'sqlite-web': { color: '#003B57', path: 'M12 2L3 7v10l9 5 9-5V7l-9-5zm0 2.18L18.36 7.5 12 10.82 5.64 7.5 12 4.18zM5 9.06l6 3.32v6.56l-6-3.32V9.06zm8 9.88V12.38l6-3.32v6.56l-6 3.32z' },
  minio: { color: '#C72C48', path: 'M20 6H4c-1.1 0-2 .9-2 2v8c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2zm-8 10H4V8h8v8zm4-4c-.55 0-1-.45-1-1s.45-1 1-1 1 .45 1 1-.45 1-1 1zm4 0c-.55 0-1-.45-1-1s.45-1 1-1 1 .45 1 1-.45 1-1 1z' },
  gitea: { color: '#609926', path: 'M4.21 9.79L12 2l7.79 7.79-1.42 1.42L12 4.83 5.63 11.21 4.21 9.79zM12 8a4 4 0 100 8 4 4 0 000-8zm0 6a2 2 0 110-4 2 2 0 010 4zm-7.79 2.21L12 22l7.79-7.79-1.42-1.42L12 19.17l-6.37-6.38-1.42 1.42z' },
  grafana: { color: '#F46800', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z' },
  prometheus: { color: '#E6522C', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm0 18c-4.41 0-8-3.59-8-8s3.59-8 8-8 8 3.59 8 8-3.59 8-8 8zm-1-6h2v2h-2v-2zm0-8h2v6h-2V6z' },
  ttyd: { color: '#4EC9B0', path: 'M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zm0 14H4V8h16v10zM7 10l4 3-4 3v-6zm5 5h5v2h-5v-2z' },
  httpbin: { color: '#6C8EBF', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm4.65 14.65l-1.41 1.41L12 14.83l-3.24 3.23-1.41-1.41L10.59 13.4 7.35 10.17l1.41-1.41L12 12l3.24-3.24 1.41 1.41L13.41 13.4l3.24 3.25z' },
  jupyter: { color: '#F37626', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zM7.07 18.28c.43-.9 3.05-1.28 4.93-1.28s4.51.39 4.93 1.28C15.57 19.36 13.86 20 12 20s-3.57-.64-4.93-1.72zM12 13a3.5 3.5 0 110-7 3.5 3.5 0 010 7z' },
  nginx: { color: '#009639', path: 'M12 2L3 7v10l9 5 9-5V7l-9-5zm-1 15.5L5 14V10l6 3.5v4zm1-5.5L6 8.5l6-3.5 6 3.5-6 3.5zm7 4l-6 3.5v-4L19 10v4z' },
  caddy: { color: '#1F88C0', path: 'M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm0 10.99h7c-.53 4.12-3.28 7.79-7 8.94V12H5V6.3l7-3.11V12z' },
  syncthing: { color: '#0891B2', path: 'M12 4V1L8 5l4 4V6c3.31 0 6 2.69 6 6 0 1.01-.25 1.97-.7 2.8l1.46 1.46C19.54 15.03 20 13.57 20 12c0-4.42-3.58-8-8-8zm0 14c-3.31 0-6-2.69-6-6 0-1.01.25-1.97.7-2.8L5.24 7.74C4.46 8.97 4 10.43 4 12c0 4.42 3.58 8 8 8v3l4-4-4-4v3z' },
  miniflux: { color: '#F59E0B', path: 'M20 4H4c-1.1 0-2 .9-2 2v12c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V6c0-1.1-.9-2-2-2zM4 12h4v2H4v-2zm10 6H4v-2h10v2zm6 0h-4v-2h4v2zm0-4H10v-2h10v2zm0-4H4V8h16v2z' },
  navidrome: { color: '#8B5CF6', path: 'M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6zm-2 16c-1.1 0-2-.9-2-2s.9-2 2-2 2 .9 2 2-.9 2-2 2z' },
  transmission: { color: '#B91C1C', path: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 14H9V8h2v8zm4 0h-2V8h2v8z' },
  headscale: { color: '#6366F1', path: 'M12 1L3 5v6c0 5.55 3.84 10.74 9 12 5.16-1.26 9-6.45 9-12V5l-9-4zm-2 16l-4-4 1.41-1.41L10 14.17l6.59-6.59L18 9l-8 8z' },
}

const CATEGORY_LABELS = {
  all: 'All',
  database: 'Database',
  developer: 'Developer',
  network: 'Network',
  productivity: 'Productivity',
  media: 'Media',
  system: 'System',
  other: 'Other',
}

const CATEGORY_ICONS = {
  all: 'M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z',
  database: 'M12 3C7.58 3 4 4.79 4 7v10c0 2.21 3.58 4 8 4s8-1.79 8-4V7c0-2.21-3.58-4-8-4zm0 2c3.87 0 6 1.5 6 2s-2.13 2-6 2-6-1.5-6-2 2.13-2 6-2zM6 17V14.87c1.47.82 3.63 1.13 6 1.13s4.53-.31 6-1.13V17c0 .5-2.13 2-6 2s-6-1.5-6-2z',
  developer: 'M9.4 16.6L4.8 12l4.6-4.6L8 6l-6 6 6 6 1.4-1.4zm5.2 0l4.6-4.6-4.6-4.6L16 6l6 6-6 6-1.4-1.4z',
  network: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z',
  productivity: 'M19 3h-4.18C14.4 1.84 13.3 1 12 1c-1.3 0-2.4.84-2.82 2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-7 0c.55 0 1 .45 1 1s-.45 1-1 1-1-.45-1-1 .45-1 1-1zm2 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z',
  media: 'M21 3H3c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h18c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H3V5h18v14zM9.5 15l5-3.5-5-3.5v7z',
}

function AppIcon({ appId, size = 36 }) {
  const icon = APP_ICONS[appId]
  if (!icon) {
    return (
      <div style={{
        width: size, height: size, borderRadius: size * 0.22,
        background: '#262626', display: 'flex', alignItems: 'center',
        justifyContent: 'center', fontSize: size * 0.4, color: '#737373',
        flexShrink: 0, border: '1px solid #333',
      }}>
        {appId?.[0]?.toUpperCase() || '?'}
      </div>
    )
  }
  return (
    <div style={{
      width: size, height: size, borderRadius: size * 0.22,
      background: icon.color + '18', display: 'flex', alignItems: 'center',
      justifyContent: 'center', flexShrink: 0, border: `1px solid ${icon.color}30`,
    }}>
      <svg viewBox="0 0 24 24" width={size * 0.55} height={size * 0.55} fill={icon.color}>
        <path d={icon.path} />
      </svg>
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

  const fetchData = async () => {
    setLoading(true)
    try {
      const [regRes, instRes] = await Promise.all([
        fetch('/api/store/registry'),
        fetch('/api/store/installed'),
      ])
      const regData = await regRes.json()
      const instData = await instRes.json()
      setApps(regData || [])
      setInstalled(instData || [])
    } catch {
      setApps([])
      setInstalled([])
    }
    setLoading(false)
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
      if (!res.ok) {
        const data = await res.json()
        throw new Error(data.error || 'Install failed')
      }
      setSuccess(`${appId} installed successfully`)
      setTimeout(() => setSuccess(null), 3000)
      await refreshInstalled()
      await fetchData()
    } catch (e) {
      setError(e.message)
      setTimeout(() => setError(null), 5000)
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
      setSuccess(`${appId} removed`)
      setTimeout(() => setSuccess(null), 3000)
      await refreshInstalled()
      await fetchData()
      if (selectedApp?.id === appId) setSelectedApp(null)
    } catch (e) {
      setError(e.message)
      setTimeout(() => setError(null), 5000)
    }
    setInstalling(null)
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

  const categories = ['all', ...new Set(apps.map(a => a.category).filter(Boolean))]
  const installedIds = new Set((installed || []).map(a => a.id))
  const browseList = tab === 'installed' ? filtered.filter(a => a.installed || installedIds.has(a.id)) : filtered

  return (
    <div className="flex flex-col h-full bg-neutral-950 text-neutral-300 text-xs overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800/60 flex-shrink-0">
        <div className="flex items-center gap-2.5">
          <svg viewBox="0 0 24 24" className="w-5 h-5 text-neutral-400" fill="currentColor">
            <path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z" />
          </svg>
          <span className="text-sm font-semibold text-neutral-100">App Hub</span>
        </div>
        <div className="flex bg-neutral-900 rounded-lg p-0.5 gap-0.5">
          <button
            className={`px-3 py-1.5 rounded-md text-[11px] font-medium transition-colors ${tab === 'browse' ? 'bg-neutral-800 text-neutral-200' : 'text-neutral-500 hover:text-neutral-400'}`}
            onClick={() => setTab('browse')}
          >Browse</button>
          <button
            className={`px-3 py-1.5 rounded-md text-[11px] font-medium transition-colors ${tab === 'installed' ? 'bg-neutral-800 text-neutral-200' : 'text-neutral-500 hover:text-neutral-400'}`}
            onClick={() => setTab('installed')}
          >Installed ({installed?.length || 0})</button>
        </div>
      </div>

      {/* Toasts */}
      {error && (
        <div className="mx-4 mt-2 px-3 py-2 rounded-lg bg-red-950/60 border border-red-900/50 text-red-400 text-[11px] flex items-center gap-2">
          <svg viewBox="0 0 20 20" className="w-3.5 h-3.5 flex-shrink-0" fill="currentColor"><path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.28 7.22a.75.75 0 00-1.06 1.06L8.94 10l-1.72 1.72a.75.75 0 101.06 1.06L10 11.06l1.72 1.72a.75.75 0 101.06-1.06L11.06 10l1.72-1.72a.75.75 0 00-1.06-1.06L10 8.94 8.28 7.22z" clipRule="evenodd" /></svg>
          {error}
        </div>
      )}
      {success && (
        <div className="mx-4 mt-2 px-3 py-2 rounded-lg bg-emerald-950/60 border border-emerald-900/50 text-emerald-400 text-[11px] flex items-center gap-2">
          <svg viewBox="0 0 20 20" className="w-3.5 h-3.5 flex-shrink-0" fill="currentColor"><path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.857-9.809a.75.75 0 00-1.214-.882l-3.483 4.79-1.88-1.88a.75.75 0 10-1.06 1.061l2.5 2.5a.75.75 0 001.137-.089l4-5.5z" clipRule="evenodd" /></svg>
          {success}
        </div>
      )}

      {/* Search + Category filters */}
      <div className="px-4 pt-3 pb-2 flex flex-col gap-2 flex-shrink-0">
        <div className="relative">
          <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600" fill="none" stroke="currentColor" strokeWidth="1.5">
            <circle cx="7" cy="7" r="5" />
            <path d="M11 11l3.5 3.5" strokeLinecap="round" />
          </svg>
          <input
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search apps..."
            className="w-full bg-neutral-900/80 border border-neutral-800/60 rounded-lg pl-9 pr-3 py-2 text-xs text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-700 transition-colors"
          />
        </div>
        <div className="flex gap-1.5 overflow-x-auto pb-0.5">
          {categories.map(cat => (
            <button
              key={cat}
              className={`flex items-center gap-1.5 px-2.5 py-1.5 rounded-md text-[10px] font-medium whitespace-nowrap flex-shrink-0 transition-all ${
                category === cat
                  ? 'bg-neutral-800 text-neutral-200 border border-neutral-700'
                  : 'text-neutral-500 border border-transparent hover:text-neutral-400 hover:bg-neutral-900'
              }`}
              onClick={() => setCategory(cat)}
            >
              {CATEGORY_ICONS[cat] && (
                <svg viewBox="0 0 24 24" className="w-3 h-3" fill="currentColor"><path d={CATEGORY_ICONS[cat]} /></svg>
              )}
              {CATEGORY_LABELS[cat] || cat}
            </button>
          ))}
        </div>
      </div>

      <div className="flex flex-1 min-h-0 overflow-hidden">
        {/* App list */}
        <div className="flex-1 overflow-y-auto px-4 pb-4">
          {loading ? (
            <div className="flex items-center justify-center h-full text-neutral-600">
              <span className="flex items-center gap-2">
                <span className="w-4 h-4 border-2 border-neutral-700 border-t-neutral-400 rounded-full animate-spin" />
                Loading registry...
              </span>
            </div>
          ) : browseList.length === 0 ? (
            <div className="flex flex-col items-center justify-center h-full text-neutral-600 gap-2">
              <svg viewBox="0 0 24 24" className="w-8 h-8 text-neutral-800" fill="currentColor">
                <path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z" />
              </svg>
              <span className="text-xs">{tab === 'installed' ? 'No apps installed yet' : 'No apps found'}</span>
            </div>
          ) : (
            <div className="flex flex-col gap-1.5 pt-1">
              {browseList.map(app => {
                const isInstalled = app.installed || installedIds.has(app.id)
                const isInstalling = installing === app.id
                const isSelected = selectedApp?.id === app.id

                return (
                  <div
                    key={app.id}
                    className={`flex items-center gap-3 px-3 py-2.5 rounded-xl cursor-pointer transition-all ${
                      isSelected
                        ? 'bg-neutral-800/80 border border-neutral-700/80'
                        : 'hover:bg-neutral-900/60 border border-transparent'
                    }`}
                    onClick={() => { setSelectedApp(app); setSelectedVersion(app.latest || '') }}
                  >
                    <AppIcon appId={app.icon || app.id} size={40} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5">
                        <span className="text-[13px] font-medium text-neutral-100">{app.name}</span>
                        {app.vetted && (
                          <svg viewBox="0 0 20 20" className="w-3.5 h-3.5 text-emerald-500" fill="currentColor">
                            <path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.857-9.809a.75.75 0 00-1.214-.882l-3.483 4.79-1.88-1.88a.75.75 0 10-1.06 1.061l2.5 2.5a.75.75 0 001.137-.089l4-5.5z" clipRule="evenodd" />
                          </svg>
                        )}
                      </div>
                      <div className="text-[11px] text-neutral-500 truncate mt-0.5">{app.description}</div>
                    </div>
                    <div className="flex items-center gap-2 flex-shrink-0">
                      {isInstalled ? (
                        <span className="text-[10px] font-medium text-blue-400 bg-blue-500/10 px-2.5 py-1 rounded-full border border-blue-500/20">Installed</span>
                      ) : (
                        <button
                          className="text-[11px] font-medium text-neutral-200 bg-neutral-800 hover:bg-neutral-700 px-3 py-1.5 rounded-lg transition-colors disabled:opacity-40"
                          onClick={(e) => { e.stopPropagation(); installApp(app.id, app.latest) }}
                          disabled={isInstalling}
                        >
                          {isInstalling ? (
                            <span className="flex items-center gap-1.5">
                              <span className="w-3 h-3 border-2 border-neutral-600 border-t-neutral-300 rounded-full animate-spin" />
                              Installing
                            </span>
                          ) : 'Install'}
                        </button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>

        {/* Detail panel */}
        {selectedApp && (
          <div className="w-[300px] border-l border-neutral-800/60 flex flex-col flex-shrink-0 overflow-hidden bg-neutral-950">
            {/* Detail header */}
            <div className="flex items-start gap-3 p-4 border-b border-neutral-800/40">
              <AppIcon appId={selectedApp.icon || selectedApp.id} size={52} />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-1.5">
                  <span className="text-[15px] font-semibold text-neutral-100">{selectedApp.name}</span>
                  {selectedApp.vetted && (
                    <svg viewBox="0 0 20 20" className="w-4 h-4 text-emerald-500" fill="currentColor">
                      <path fillRule="evenodd" d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.857-9.809a.75.75 0 00-1.214-.882l-3.483 4.79-1.88-1.88a.75.75 0 10-1.06 1.061l2.5 2.5a.75.75 0 001.137-.089l4-5.5z" clipRule="evenodd" />
                    </svg>
                  )}
                </div>
                <div className="text-[11px] text-neutral-500 mt-0.5">{selectedApp.author || 'Unknown'}</div>
              </div>
              <button
                className="text-neutral-600 hover:text-neutral-400 transition-colors p-1 -mt-1"
                onClick={() => setSelectedApp(null)}
              >
                <svg viewBox="0 0 20 20" className="w-4 h-4" fill="currentColor">
                  <path d="M6.28 5.22a.75.75 0 00-1.06 1.06L8.94 10l-3.72 3.72a.75.75 0 101.06 1.06L10 11.06l3.72 3.72a.75.75 0 101.06-1.06L11.06 10l3.72-3.72a.75.75 0 00-1.06-1.06L10 8.94 6.28 5.22z" />
                </svg>
              </button>
            </div>

            <div className="flex-1 overflow-y-auto p-4 flex flex-col gap-4">
              <p className="text-[12px] text-neutral-400 leading-relaxed">{selectedApp.description}</p>

              {/* Install/Remove action */}
              <div className="flex gap-2">
                {selectedApp.installed || installedIds.has(selectedApp.id) ? (
                  <>
                    <span className="flex-1 text-center text-[11px] font-medium text-blue-400 bg-blue-500/10 py-2 rounded-lg border border-blue-500/20">Installed</span>
                    <button
                      className="px-4 py-2 rounded-lg text-[11px] font-medium text-red-400 border border-red-900/50 hover:bg-red-950/40 transition-colors disabled:opacity-40"
                      onClick={() => uninstallApp(selectedApp.id)}
                      disabled={installing === selectedApp.id}
                    >
                      {installing === selectedApp.id ? 'Removing...' : 'Remove'}
                    </button>
                  </>
                ) : (
                  <button
                    className="flex-1 py-2.5 rounded-lg text-[12px] font-semibold text-white bg-blue-600 hover:bg-blue-500 transition-colors disabled:opacity-40"
                    onClick={() => installApp(selectedApp.id, selectedVersion)}
                    disabled={installing === selectedApp.id}
                  >
                    {installing === selectedApp.id ? (
                      <span className="flex items-center justify-center gap-2">
                        <span className="w-3.5 h-3.5 border-2 border-blue-300/40 border-t-white rounded-full animate-spin" />
                        Installing...
                      </span>
                    ) : `Install ${selectedVersion || ''}`}
                  </button>
                )}
              </div>

              {/* Version picker */}
              {(selectedApp.versions || []).length > 0 && (
                <div>
                  <div className="text-[10px] uppercase tracking-wider text-neutral-600 mb-2 font-medium">Version</div>
                  <div className="flex gap-1.5 flex-wrap">
                    {(selectedApp.versions || []).map(v => (
                      <button
                        key={v}
                        className={`px-3 py-1.5 rounded-md text-[11px] transition-all ${
                          selectedVersion === v
                            ? 'bg-blue-600/20 text-blue-400 border border-blue-500/30'
                            : 'bg-neutral-900 text-neutral-500 border border-neutral-800 hover:border-neutral-700'
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
                <div className="text-[10px] uppercase tracking-wider text-neutral-600 mb-2 font-medium">Details</div>
                <div className="bg-neutral-900/60 rounded-lg border border-neutral-800/50 divide-y divide-neutral-800/40">
                  <DetailRow label="Category" value={CATEGORY_LABELS[selectedApp.category] || selectedApp.category} />
                  <DetailRow label="License" value={selectedApp.license || '\u2014'} />
                  <DetailRow label="ID" value={selectedApp.id} />
                  {selectedApp.homepage && <DetailRow label="Homepage" value={selectedApp.homepage} link />}
                </div>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function DetailRow({ label, value, link }) {
  return (
    <div className="flex justify-between items-center px-3 py-2">
      <span className="text-[11px] text-neutral-600">{label}</span>
      {link ? (
        <span className="text-[11px] text-blue-400/70 truncate max-w-[160px]">{value}</span>
      ) : (
        <span className="text-[11px] text-neutral-400 truncate max-w-[160px]">{value}</span>
      )}
    </div>
  )
}

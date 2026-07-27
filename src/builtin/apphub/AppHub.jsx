import { useState, useEffect, useRef, useCallback } from 'react'
import { refreshInstalled } from '../../core/AppRegistry'
import { APP_LOGOS, APP_COLORS, APP_LETTERS } from '../../core/AppIcons'
import { useTheme } from '../../core/ThemeProvider'

const CATEGORY_LABELS = {
  all: 'All Apps',
  internet: 'Internet',
  media: 'Media',
  developer: 'Developer',
  productivity: 'Productivity',
  network: 'Network',
  database: 'Database',
  system: 'System',
}

const CATEGORY_ICONS = {
  all: 'M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z',
  internet: 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-1 17.93c-3.95-.49-7-3.85-7-7.93 0-.62.08-1.21.21-1.79L9 15v1c0 1.1.9 2 2 2v1.93zm6.9-2.54c-.26-.81-1-1.39-1.9-1.39h-1v-3c0-.55-.45-1-1-1H8v-2h2c.55 0 1-.45 1-1V7h2c1.1 0 2-.9 2-2v-.41c2.93 1.19 5 4.06 5 7.41 0 2.08-.8 3.97-2.1 5.39z',
  media: 'M12 3v10.55c-.59-.34-1.27-.55-2-.55-2.21 0-4 1.79-4 4s1.79 4 4 4 4-1.79 4-4V7h4V3h-6z',
  developer: 'M9.4 16.6L4.8 12l4.6-4.6L8 6l-6 6 6 6 1.4-1.4zm5.2 0l4.6-4.6-4.6-4.6L16 6l6 6-6 6-1.4-1.4z',
  productivity: 'M19 3h-4.18C14.4 1.84 13.3 1 12 1c-1.3 0-2.4.84-2.82 2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-7 0c.55 0 1 .45 1 1s-.45 1-1 1-1-.45-1-1 .45-1 1-1zm2 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z',
  network: 'M1 9l2 2c4.97-4.97 13.03-4.97 18 0l2-2C16.93 2.93 7.08 2.93 1 9zm8 8l3 3 3-3c-1.65-1.66-4.34-1.66-6 0zm-4-4l2 2c2.76-2.76 7.24-2.76 10 0l2-2C15.14 9.14 8.87 9.14 5 13z',
  database: 'M12 3C7.58 3 4 4.79 4 7v10c0 2.21 3.58 4 8 4s8-1.79 8-4V7c0-2.21-3.58-4-8-4zm0 2c3.87 0 6 1.5 6 2s-2.13 2-6 2-6-1.5-6-2 2.13-2 6-2zM6 17v-1.29c1.58.74 3.74 1.29 6 1.29s4.42-.55 6-1.29V17c0 .5-2.13 2-6 2s-6-1.5-6-2z',
  system: 'M19.14 12.94c.04-.3.06-.61.06-.94 0-.32-.02-.64-.07-.94l2.03-1.58c.18-.14.23-.41.12-.61l-1.92-3.32c-.12-.22-.37-.29-.59-.22l-2.39.96c-.5-.38-1.03-.7-1.62-.94l-.36-2.54c-.04-.24-.24-.41-.48-.41h-3.84c-.24 0-.43.17-.47.41l-.36 2.54c-.59.24-1.13.57-1.62.94l-2.39-.96c-.22-.08-.47 0-.59.22L2.74 8.87c-.12.21-.08.47.12.61l2.03 1.58c-.05.3-.07.62-.07.94s.02.64.07.94l-2.03 1.58c-.18.14-.23.41-.12.61l1.92 3.32c.12.22.37.29.59.22l2.39-.96c.5.38 1.03.7 1.62.94l.36 2.54c.05.24.24.41.48.41h3.84c.24 0 .44-.17.47-.41l.36-2.54c.59-.24 1.13-.56 1.62-.94l2.39.96c.22.08.47 0 .59-.22l1.92-3.32c.12-.22.07-.47-.12-.61l-2.01-1.58zM12 15.6c-1.98 0-3.6-1.62-3.6-3.6s1.62-3.6 3.6-3.6 3.6 1.62 3.6 3.6-1.62 3.6-3.6 3.6z',
}

const SOURCE_BADGE = {
  flatpak: { label: 'Flatpak', bg: 'bg-sky-500/10', text: 'text-sky-400', border: 'border-sky-500/20' },
  apt: { label: 'Apt', bg: 'bg-amber-500/10', text: 'text-amber-400', border: 'border-amber-500/20' },
  web: { label: 'Web', bg: 'bg-emerald-500/10', text: 'text-emerald-400', border: 'border-emerald-500/20' },
}

// APPSTORE-08: type badge config — maps the `type` field values to display labels + styles
const APPTYPE_BADGE = {
  web:     { label: 'Web',      bg: 'bg-violet-500/10', text: 'text-violet-400', border: 'border-violet-500/20' },
  desktop: { label: 'Streamed', bg: 'bg-orange-500/10', text: 'text-orange-400', border: 'border-orange-500/20' },
  service: { label: 'Service',  bg: 'bg-slate-500/10',  text: 'text-slate-400',  border: 'border-slate-500/20'  },
}

// APPSTORE-08: derive a normalised app-type key for filtering and badge rendering
function appTypeKey(app) {
  if (!app.type) return null
  if (app.type === 'web') return 'web'
  if (app.type === 'desktop') return 'desktop'
  if (app.type === 'service') return 'service'
  return null
}

// APPSTORE-08: small per-card badge derived from the `type` field
function AppTypeBadge({ app, className = '' }) {
  const key = appTypeKey(app)
  if (!key) return null
  const s = APPTYPE_BADGE[key]
  return (
    <span className={`inline-flex items-center text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded ${s.bg} ${s.text} border ${s.border} ${className}`}>
      {s.label}
    </span>
  )
}

function getSourceType(app) {
  if (app.flatpak_id) return 'flatpak'
  if (app.type === 'web') return 'web'
  return 'apt'
}

function AppIcon({ appId, letter, size = 44 }) {
  const [failed, setFailed] = useState(false)
  // Resolve the bundled brand mark / hue by the real app id. `letter` (the
  // registry `icon` field — often a label or emoji) is only a fallback glyph;
  // it must NOT shadow the id, or ids whose icon field differs (jellyfin,
  // navidrome, vaultwarden, …) would never find their coloured tile.
  const logo = APP_LOGOS[appId]
  const color = APP_COLORS[appId] || '#555'
  const radius = Math.round(size * 0.22)

  if (logo && !failed) {
    return (
      <div
        className="flex-shrink-0 flex items-center justify-center bg-[var(--bg-elevated)] overflow-hidden"
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
      className="flex-shrink-0 flex items-center justify-center font-semibold text-[var(--text-secondary)]"
      style={{
        width: size, height: size, borderRadius: radius,
        background: `linear-gradient(135deg, ${color}35, ${color}15)`,
        border: `1px solid ${color}25`,
        fontSize: size * 0.36,
      }}
    >
      {APP_LETTERS[appId] || (letter || appId || '?')[0]?.toUpperCase() || '?'}
    </div>
  )
}

function SourceBadge({ app, className = '' }) {
  const src = getSourceType(app)
  const s = SOURCE_BADGE[src]
  return (
    <span className={`inline-flex items-center text-[9px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded ${s.bg} ${s.text} border ${s.border} ${className}`}>
      {s.label}
    </span>
  )
}

// Animated install progress bar
function InstallProgress({ label }) {
  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex items-center gap-2.5">
        <span className="w-4 h-4 spinner flex-shrink-0" />
        <span className="text-[13px] font-medium text-[var(--accent)]">{label || 'Installing...'}</span>
      </div>
      <div className="h-1.5 rounded-full bg-[var(--bg-elevated)] overflow-hidden">
        <div className="h-full rounded-full bg-gradient-to-r from-[var(--accent)] to-[var(--accent)] animate-progress" />
      </div>
      <style>{`
        @keyframes progress {
          0% { width: 0%; }
          20% { width: 15%; }
          50% { width: 45%; }
          80% { width: 75%; }
          95% { width: 90%; }
          100% { width: 90%; }
        }
        .animate-progress { animation: progress 30s ease-out forwards; }
        @media (prefers-reduced-motion: reduce) {
          .animate-progress { animation: none; width: 45%; }
        }
      `}</style>
    </div>
  )
}

// Loading skeleton card — mirrors the real card footprint for a calm, intentional load state
function SkeletonCard() {
  return (
    <div className="flex items-center gap-3.5 p-3.5 rounded-xl bg-[var(--bg-surface)] border border-[var(--border-subtle)]">
      <div className="w-[42px] h-[42px] rounded-[10px] bg-[var(--bg-elevated)] animate-pulse flex-shrink-0 motion-reduce:animate-none" />
      <div className="flex-1 min-w-0 flex flex-col gap-2">
        <div className="h-3 w-1/2 rounded bg-[var(--bg-elevated)] animate-pulse motion-reduce:animate-none" />
        <div className="h-2.5 w-4/5 rounded bg-[var(--bg-elevated)] animate-pulse motion-reduce:animate-none" />
      </div>
    </div>
  )
}

export default function AppHub() {
  const { isDark } = useTheme()
  const [apps, setApps] = useState([])
  const [installed, setInstalled] = useState([])
  const [loading, setLoading] = useState(true)
  const [installing, setInstalling] = useState(null) // appID being installed
  const [installPhase, setInstallPhase] = useState('') // phase label
  const [uninstalling, setUninstalling] = useState(null)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('all')
  const [appTypeFilter, setAppTypeFilter] = useState('all') // APPSTORE-08
  const [selectedApp, setSelectedApp] = useState(null)
  const [selectedVersion, setSelectedVersion] = useState('')
  const [tab, setTab] = useState('browse')
  const [error, setError] = useState(null)
  const [success, setSuccess] = useState(null)
  const [cacheReady, setCacheReady] = useState(null)
  const [systemArch, setSystemArch] = useState(null)
  const [updatingCache, setUpdatingCache] = useState(false)
  const scrollRef = useRef(null)

  const fetchData = useCallback(async () => {
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
  }, [])

  const updateAptCache = async () => {
    setUpdatingCache(true)
    setError(null)
    try {
      const res = await fetch('/api/packages/update', { method: 'POST' })
      if (!res.ok) throw new Error('Update failed')
      setCacheReady(true)
    } catch (e) {
      setError('Failed to update package index: ' + e.message)
    }
    setUpdatingCache(false)
  }

  useEffect(() => { fetchData() }, [fetchData])

  const installApp = async (appId, version) => {
    if (installing) return
    setInstalling(appId)
    const app = apps.find(a => a.id === appId)
    setInstallPhase(app?.flatpak_id ? 'Downloading from Flathub...' : 'Installing packages...')
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
        const detail = data.detail || ''
        throw new Error(detail ? `${msg}\n${detail}` : msg)
      }
      setSuccess(`${app?.name || appId} installed successfully`)
      setTimeout(() => setSuccess(null), 5000)
      await refreshInstalled()
      await fetchData()
    } catch (e) {
      setError(e.message)
    }
    setInstalling(null)
    setInstallPhase('')
  }

  const uninstallApp = async (appId) => {
    if (uninstalling) return
    setUninstalling(appId)
    setError(null)
    try {
      const res = await fetch('/api/store/uninstall', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ app_id: appId }),
      })
      if (!res.ok) throw new Error('Uninstall failed')
      setSuccess(`${apps.find(a => a.id === appId)?.name || appId} removed`)
      setTimeout(() => setSuccess(null), 5000)
      await refreshInstalled()
      await fetchData()
      if (selectedApp?.id === appId) setSelectedApp(null)
    } catch (e) {
      setError(e.message)
    }
    setUninstalling(null)
  }

  const isArchCompatible = (app) => {
    if (!systemArch || !app.arch || app.arch.length === 0) return true
    return app.arch.includes(systemArch)
  }

  const filtered = apps.filter(app => {
    if (category !== 'all' && app.category !== category) return false
    // APPSTORE-08: type filter composes with category + search
    if (appTypeFilter !== 'all' && appTypeKey(app) !== appTypeFilter) return false
    if (search.trim()) {
      const q = search.toLowerCase()
      return app.name.toLowerCase().includes(q) ||
        app.description.toLowerCase().includes(q) ||
        app.id.toLowerCase().includes(q) ||
        (app.keywords || []).some(k => k.toLowerCase().includes(q))
    }
    return true
  })

  const categories = ['all', ...new Set(apps.map(a => a.category).filter(Boolean).sort())]
  const installedIds = new Set((installed || []).map(a => a.id))
  const browseList = tab === 'installed' ? filtered.filter(a => a.installed || installedIds.has(a.id)) : filtered

  const selectApp = (app) => {
    setSelectedApp(app)
    setSelectedVersion(app.latest || '')
    setError(null)
  }

  return (
    <div className={`relative flex h-full overflow-hidden bg-[var(--bg-base)] ${isDark ? 'text-neutral-300' : 'text-neutral-700'}`}>
      {/* Sidebar */}
      <div className="w-44 sm:w-52 lg:w-56 flex-shrink-0 border-r border-[var(--border-default)] flex flex-col bg-[var(--bg-base)]">
        <div className="px-4 sm:px-5 pt-5 pb-4">
          <h1 className="text-[17px] font-bold text-[var(--text-primary)] tracking-tight">App Store</h1>
          <p className="text-[11px] text-neutral-600 mt-1">{apps.length} apps available</p>
        </div>

        {/* Search */}
        <div className="px-3 pb-3">
          <div className="relative">
            <svg viewBox="0 0 16 16" className="w-3.5 h-3.5 absolute left-3 top-1/2 -translate-y-1/2 text-neutral-600 pointer-events-none" fill="none" stroke="currentColor" strokeWidth="1.5">
              <circle cx="6.5" cy="6.5" r="4.5" /><path d="M10 10l4 4" strokeLinecap="round" />
            </svg>
            <input
              value={search}
              onChange={e => setSearch(e.target.value)}
              placeholder="Search..."
              className="w-full bg-[var(--bg-elevated)] border border-[var(--border-default)] rounded-lg pl-9 pr-3 py-2 text-[12px] text-[var(--text-primary)] outline-none placeholder:text-neutral-600 focus:border-[var(--border-strong)] focus:bg-[var(--bg-hover)] transition-colors [transition-duration:var(--motion-fast)]"
            />
          </div>
        </div>

        {/* Tabs */}
        <div className="px-3 pb-2 flex flex-col gap-0.5">
          {[
            { id: 'browse', label: 'Browse', icon: 'M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z' },
            { id: 'installed', label: 'Installed', icon: 'M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41L9 16.17z', count: installed?.length },
          ].map(t => (
            <button
              key={t.id}
              className={`flex items-center gap-2.5 px-3 py-2 rounded-lg text-[12px] font-medium transition-colors [transition-duration:var(--motion-fast)] text-left ${
                tab === t.id ? 'bg-[var(--bg-selected)] text-[var(--text-primary)]' : 'text-neutral-500 hover:text-neutral-300 hover:bg-[var(--bg-hover)]'
              }`}
              onClick={() => setTab(t.id)}
            >
              <svg viewBox="0 0 24 24" className="w-[15px] h-[15px] flex-shrink-0" fill="currentColor" opacity={0.7}><path d={t.icon} /></svg>
              {t.label}
              {t.count > 0 && (
                <span className="ml-auto text-[10px] bg-[var(--bg-elevated)] text-neutral-400 px-1.5 py-0.5 rounded-full min-w-[20px] text-center font-medium">
                  {t.count}
                </span>
              )}
            </button>
          ))}
        </div>

        {/* APPSTORE-08: Type filter */}
        <div className="px-3 pt-3 border-t border-[var(--border-subtle)]">
          <div className="text-[10px] uppercase tracking-widest text-neutral-600 font-semibold px-3 py-2">App Type</div>
          <div className="flex flex-col gap-0.5">
            {[
              { id: 'all',     label: 'All Types' },
              { id: 'web',     label: 'Web' },
              { id: 'desktop', label: 'Streamed' },
              { id: 'service', label: 'Service' },
            ].map(t => (
              <button
                key={t.id}
                className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-[12px] transition-colors [transition-duration:var(--motion-fast)] text-left ${
                  appTypeFilter === t.id
                    ? 'bg-[var(--bg-selected)] text-[var(--text-primary)] font-medium'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-[var(--bg-hover)]'
                }`}
                onClick={() => setAppTypeFilter(t.id)}
              >
                {t.id !== 'all' && APPTYPE_BADGE[t.id] && (
                  <span className={`w-2 h-2 rounded-full flex-shrink-0 ${APPTYPE_BADGE[t.id].bg} border ${APPTYPE_BADGE[t.id].border}`} />
                )}
                {t.label}
              </button>
            ))}
          </div>
        </div>

        {/* Categories */}
        <div className="px-3 pt-3 border-t border-[var(--border-subtle)] flex-1 overflow-y-auto">
          <div className="text-[10px] uppercase tracking-widest text-neutral-600 font-semibold px-3 py-2">Categories</div>
          <div className="flex flex-col gap-0.5">
            {categories.map(cat => (
              <button
                key={cat}
                className={`flex items-center gap-2.5 px-3 py-1.5 rounded-lg text-[12px] transition-colors [transition-duration:var(--motion-fast)] text-left ${
                  category === cat
                    ? 'bg-[var(--bg-selected)] text-[var(--text-primary)] font-medium'
                    : 'text-neutral-500 hover:text-neutral-300 hover:bg-[var(--bg-hover)]'
                }`}
                onClick={() => setCategory(cat)}
              >
                {CATEGORY_ICONS[cat] && (
                  <svg viewBox="0 0 24 24" className="w-3.5 h-3.5 flex-shrink-0 opacity-50" fill="currentColor"><path d={CATEGORY_ICONS[cat]} /></svg>
                )}
                {CATEGORY_LABELS[cat] || cat}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        {/* Apt cache banner */}
        {cacheReady === false && (
          <div className="mx-4 sm:mx-6 mt-4 px-4 py-3.5 rounded-xl bg-[var(--accent-soft)] border accent-border-soft flex items-center gap-3">
            <div className="w-8 h-8 rounded-lg bg-[var(--accent-soft)] flex items-center justify-center flex-shrink-0">
              <svg viewBox="0 0 20 20" className="w-4.5 h-4.5 text-[var(--accent)]" fill="currentColor">
                <path fillRule="evenodd" d="M18 10a8 8 0 11-16 0 8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a.75.75 0 000 1.5h.253a.25.25 0 01.244.304l-.459 2.066A1.75 1.75 0 0010.747 15H11a.75.75 0 000-1.5h-.253a.25.25 0 01-.244-.304l.459-2.066A1.75 1.75 0 009.253 9H9z" clipRule="evenodd" />
              </svg>
            </div>
            <div className="flex-1 min-w-0">
              <div className="text-[12px] font-semibold text-[var(--accent)]">Package index required</div>
              <div className="text-[11px] text-[var(--text-tertiary)] mt-0.5">Update to install apps from Debian repositories</div>
            </div>
            <button
              onClick={updateAptCache}
              disabled={updatingCache}
              className="px-4 py-2 rounded-lg text-[11px] font-semibold text-white bg-[var(--accent)] hover:bg-[var(--accent-hover)] transition-colors [transition-duration:var(--motion-fast)] flex-shrink-0 disabled:opacity-50"
            >
              {updatingCache ? (
                <span className="flex items-center gap-1.5">
                  <span className="w-3 h-3 border-2 border-white/30 border-t-white rounded-full animate-spin" />
                  Updating...
                </span>
              ) : 'Update'}
            </button>
          </div>
        )}

        {/* Toast notifications */}
        <div className="absolute top-3 right-3 z-50 flex flex-col gap-2 max-w-[calc(100%-1.5rem)] sm:max-w-sm" style={{ right: selectedApp ? '340px' : '16px' }}>
          {success && (
            <div className="px-4 py-3 rounded-xl bg-[var(--status-success-soft)] border border-success-soft backdrop-blur-sm flex items-center gap-2.5 animate-[slideIn_0.2s_ease-out]">
              <svg viewBox="0 0 16 16" className="w-4 h-4 text-[var(--status-success)] flex-shrink-0" fill="currentColor">
                <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
              </svg>
              <span className="text-[12px] text-[var(--status-success)] font-medium">{success}</span>
            </div>
          )}
        </div>

        {/* App grid */}
        <div className="flex-1 overflow-y-auto px-4 sm:px-6 pt-4 pb-8" ref={scrollRef}>
          {/* Section header */}
          <div className="flex items-center justify-between gap-3 mb-4">
            <h2 className="text-[14px] font-semibold text-[var(--text-primary)] min-w-0 truncate">
              {tab === 'installed' ? 'Installed Apps' : category !== 'all' ? CATEGORY_LABELS[category] || category : 'All Apps'}
            </h2>
            <span className="text-[11px] text-neutral-600 flex-shrink-0">{browseList.length} {browseList.length === 1 ? 'app' : 'apps'}</span>
          </div>

          {loading ? (
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-2.5">
              {Array.from({ length: 6 }).map((_, i) => <SkeletonCard key={i} />)}
            </div>
          ) : browseList.length === 0 ? (
            <div className="flex flex-col items-center justify-center py-16 sm:py-24 text-neutral-600 gap-4 text-center px-6">
              <div className="w-16 h-16 rounded-2xl bg-[var(--bg-elevated)] border border-[var(--border-subtle)] flex items-center justify-center">
                <svg viewBox="0 0 24 24" className="w-8 h-8 text-neutral-700" fill="currentColor">
                  <path d="M4 8h4V4H4v4zm6 12h4v-4h-4v4zm-6 0h4v-4H4v4zm0-6h4v-4H4v4zm6 0h4v-4h-4v4zm6-10v4h4V4h-4zm-6 4h4V4h-4v4zm6 6h4v-4h-4v4zm0 6h4v-4h-4v4z" />
                </svg>
              </div>
              <div className="flex flex-col gap-1">
                <span className="text-[13px] font-medium text-[var(--text-secondary)]">{tab === 'installed' ? 'No apps installed yet' : 'No apps match your search'}</span>
                <span className="text-[11px] text-neutral-600">{tab === 'installed' ? 'Browse the store to add your first app' : 'Try a different search or category'}</span>
              </div>
            </div>
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-2.5">
              {browseList.map(app => {
                const isInstalled = app.installed || installedIds.has(app.id)
                const isBeingInstalled = installing === app.id
                const isBeingRemoved = uninstalling === app.id
                const isSelected = selectedApp?.id === app.id
                return (
                  <div
                    key={app.id}
                    className={`group relative flex items-center gap-3.5 p-3.5 rounded-xl cursor-pointer transition-[background-color,border-color,box-shadow,transform] [transition-duration:var(--motion-base)] [transition-timing-function:var(--ease-out)] motion-reduce:transition-none ${
                      isSelected
                        ? 'bg-[var(--bg-selected)] ring-1 ring-[var(--bg-selected-border)] border border-transparent'
                        : 'bg-[var(--bg-surface)] hover:bg-[var(--bg-hover)] border border-[var(--border-subtle)] hover:border-[var(--border-default)] hover:shadow-lg hover:shadow-black/20 hover:-translate-y-0.5 motion-reduce:hover:translate-y-0 motion-reduce:hover:shadow-none'
                    }`}
                    onClick={() => selectApp(app)}
                  >
                    <AppIcon appId={app.id} letter={app.icon} size={42} />
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-1.5 flex-wrap">
                        <span className="text-[13px] font-medium text-[var(--text-primary)] truncate">{app.name}</span>
                        <SourceBadge app={app} />
                        <AppTypeBadge app={app} />{/* APPSTORE-08 */}
                      </div>
                      <p className="text-[11px] text-neutral-500 mt-0.5 truncate">{app.description}</p>
                    </div>
                    <div className="flex-shrink-0 ml-1">
                      {isBeingInstalled ? (
                        <span className="w-7 h-7 flex items-center justify-center">
                          <span className="w-4 h-4 spinner" />
                        </span>
                      ) : isBeingRemoved ? (
                        <span className="w-7 h-7 flex items-center justify-center">
                          <span className="w-4 h-4 rounded-full animate-spin border-2 border-[var(--status-danger-soft)] border-t-[var(--status-danger)]" />
                        </span>
                      ) : isInstalled ? (
                        <svg viewBox="0 0 16 16" className="w-4.5 h-4.5 text-[var(--status-success)]" fill="currentColor">
                          <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                        </svg>
                      ) : !isArchCompatible(app) ? (
                        <svg viewBox="0 0 16 16" className="w-4 h-4 text-neutral-700" fill="currentColor">
                          <path d="M8 16A8 8 0 108 0a8 8 0 000 16zM4.22 4.22a.75.75 0 011.06 0L8 6.94l2.72-2.72a.75.75 0 111.06 1.06L9.06 8l2.72 2.72a.75.75 0 11-1.06 1.06L8 9.06l-2.72 2.72a.75.75 0 01-1.06-1.06L6.94 8 4.22 5.28a.75.75 0 010-1.06z" />
                        </svg>
                      ) : (
                        <button
                          className="px-3.5 py-2 min-h-[36px] rounded-lg text-[11px] font-semibold text-[var(--accent)] accent-bg-soft accent-bg-hover border accent-border-soft transition-colors [transition-duration:var(--motion-fast)]"
                          onClick={e => { e.stopPropagation(); installApp(app.id, app.latest) }}
                        >
                          Get
                        </button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      </div>

      {/* Detail panel — slide in from right */}
      {selectedApp && (
        <div className="absolute sm:relative inset-y-0 right-0 z-30 w-full max-w-sm sm:w-[340px] sm:max-w-none flex-shrink-0 border-l border-[var(--border-default)] flex flex-col bg-[var(--bg-surface)] overflow-hidden shadow-2xl sm:shadow-none animate-[slideIn_0.2s_ease-out] sm:animate-none">
          {/* Close */}
          <div className="flex justify-end p-3 pb-0">
            <button
              className="text-neutral-600 hover:text-neutral-400 transition-colors p-1.5 rounded-lg hover:bg-[var(--bg-hover)]"
              onClick={() => setSelectedApp(null)}
            >
              <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor">
                <path d="M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z" />
              </svg>
            </button>
          </div>

          <div className="flex-1 overflow-y-auto">
            {/* Hero section */}
            <div className="px-6 pb-5 flex flex-col items-center text-center">
              <AppIcon appId={selectedApp.id} letter={selectedApp.icon} size={80} />
              <h2 className="text-[18px] font-bold text-[var(--text-primary)] mt-4 flex items-center gap-2">
                {selectedApp.name}
                {selectedApp.vetted && (
                  <svg viewBox="0 0 16 16" className="w-4.5 h-4.5 text-[var(--accent)]" fill="currentColor">
                    <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                  </svg>
                )}
              </h2>
              <div className="text-[12px] text-neutral-500 mt-1">{selectedApp.author || 'Unknown'}</div>
              <div className="mt-2.5">
                <SourceBadge app={selectedApp} />
              </div>
            </div>

            {/* Install / Uninstall action */}
            <div className="px-6 pb-5">
              {installing === selectedApp.id ? (
                <div className="p-4 rounded-xl bg-[var(--accent-soft)] border accent-border-soft">
                  <InstallProgress label={installPhase} />
                </div>
              ) : uninstalling === selectedApp.id ? (
                <div className="p-4 rounded-xl bg-[var(--status-danger-soft)] border border-danger-soft">
                  <div className="flex items-center gap-2.5">
                    <span className="w-4 h-4 rounded-full animate-spin flex-shrink-0 border-2 border-[var(--status-danger-soft)] border-t-[var(--status-danger)]" />
                    <span className="text-[13px] font-medium text-[var(--status-danger)]">Removing...</span>
                  </div>
                </div>
              ) : (selectedApp.installed || installedIds.has(selectedApp.id)) ? (
                <div className="flex gap-2.5">
                  <div className="flex-1 py-3 rounded-xl text-[13px] font-semibold text-[var(--status-success)] bg-[var(--status-success-soft)] border border-success-soft flex items-center justify-center gap-2">
                    <svg viewBox="0 0 16 16" className="w-4 h-4" fill="currentColor">
                      <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zm3.78-9.72a.75.75 0 00-1.06-1.06L7 8.94 5.28 7.22a.75.75 0 00-1.06 1.06l2.5 2.5a.75.75 0 001.06 0l4-4z" clipRule="evenodd" />
                    </svg>
                    Installed
                  </div>
                  <button
                    className="px-5 py-3 rounded-xl text-[12px] font-semibold text-[var(--status-danger)] border border-danger-soft hover:bg-[var(--status-danger-soft)] transition-colors [transition-duration:var(--motion-fast)]"
                    onClick={() => uninstallApp(selectedApp.id)}
                  >
                    Remove
                  </button>
                </div>
              ) : !isArchCompatible(selectedApp) ? (
                <div className="py-3.5 rounded-xl text-[12px] font-medium text-[var(--status-danger)] bg-[var(--status-danger-soft)] border border-danger-soft text-center">
                  Not available for {systemArch === 'arm64' ? 'ARM64' : systemArch}
                </div>
              ) : (
                <button
                  className="w-full py-3.5 rounded-xl text-[13px] font-bold text-white bg-[var(--accent)] hover:bg-[var(--accent-hover)] transition-transform [transition-duration:var(--motion-fast)] shadow-lg shadow-[var(--accent)]/10 active:scale-[0.98] motion-reduce:active:scale-100"
                  onClick={() => installApp(selectedApp.id, selectedVersion)}
                >
                  Install{selectedVersion && selectedVersion !== 'latest' ? ` ${selectedVersion}` : ''}
                </button>
              )}

              {/* Error shown in panel */}
              {error && installing !== selectedApp.id && (
                <div className="mt-3 p-3.5 rounded-xl bg-[var(--status-danger-soft)] border border-danger-soft">
                  <div className="flex items-start gap-2.5">
                    <svg viewBox="0 0 16 16" className="w-4 h-4 text-[var(--status-danger)] mt-0.5 flex-shrink-0" fill="currentColor">
                      <path fillRule="evenodd" d="M8 16A8 8 0 108 0a8 8 0 000 16zM6.25 5.5a.75.75 0 00-1.5 0v4a.75.75 0 001.5 0v-4zm4.25-.75a.75.75 0 01.75.75v4a.75.75 0 01-1.5 0v-4a.75.75 0 01.75-.75zM8 13a1 1 0 100-2 1 1 0 000 2z" clipRule="evenodd" />
                    </svg>
                    <div className="flex-1 min-w-0">
                      <div className="text-[11px] font-semibold text-[var(--status-danger)] mb-1">Installation failed</div>
                      <pre className="text-[10px] text-[var(--text-tertiary)] whitespace-pre-wrap break-words font-mono leading-relaxed max-h-24 overflow-y-auto">{error}</pre>
                    </div>
                    <button onClick={() => setError(null)} className="text-neutral-600 hover:text-[var(--status-danger)] transition-colors flex-shrink-0">
                      <svg viewBox="0 0 16 16" className="w-3.5 h-3.5" fill="currentColor">
                        <path d="M5.28 4.22a.75.75 0 00-1.06 1.06L6.94 8l-2.72 2.72a.75.75 0 101.06 1.06L8 9.06l2.72 2.72a.75.75 0 101.06-1.06L9.06 8l2.72-2.72a.75.75 0 00-1.06-1.06L8 6.94 5.28 4.22z" />
                      </svg>
                    </button>
                  </div>
                </div>
              )}
            </div>

            {/* Description */}
            <div className="px-6 pb-5">
              <p className="text-[12px] text-neutral-400 leading-[1.7]">{selectedApp.description}</p>
            </div>

            {/* Source info */}
            {selectedApp.type === 'desktop' && (
              <div className="px-6 pb-5">
                <div className={`flex items-start gap-2.5 px-4 py-3 rounded-xl ${
                  selectedApp.flatpak_id
                    ? 'bg-sky-500/[0.04] border border-sky-500/[0.08]'
                    : 'bg-amber-500/[0.04] border border-amber-500/[0.08]'
                }`}>
                  <svg viewBox="0 0 24 24" className={`w-4 h-4 mt-0.5 flex-shrink-0 ${selectedApp.flatpak_id ? 'text-sky-400/60' : 'text-amber-400/60'}`} fill="currentColor">
                    <path d="M20 6h-8l-2-2H4c-1.1 0-1.99.9-1.99 2L2 18c0 1.1.9 2 2 2h16c1.1 0 2-.9 2-2V8c0-1.1-.9-2-2-2zm0 12H4V8h16v10z"/>
                  </svg>
                  <p className={`text-[11px] leading-relaxed ${selectedApp.flatpak_id ? 'text-sky-300/60' : 'text-amber-300/60'}`}>
                    {selectedApp.flatpak_id
                      ? 'Via Flatpak — latest version, sandboxed, independent of system packages.'
                      : 'Via Apt — Debian system package. Version depends on repository.'}
                  </p>
                </div>
              </div>
            )}

            {/* Version picker */}
            {(selectedApp.versions || []).length > 1 && (
              <div className="px-6 pb-5">
                <div className="text-[10px] uppercase tracking-widest text-neutral-600 mb-2 font-semibold">Version</div>
                <div className="flex gap-1.5 flex-wrap">
                  {(selectedApp.versions || []).map(v => (
                    <button
                      key={v}
                      className={`px-3 py-1.5 rounded-lg text-[11px] font-medium transition-colors [transition-duration:var(--motion-fast)] ${
                        selectedVersion === v
                          ? 'bg-[var(--bg-selected)] text-[var(--text-primary)] border border-[var(--bg-selected-border)]'
                          : 'bg-[var(--bg-elevated)] text-neutral-500 border border-[var(--border-subtle)] hover:border-[var(--border-strong)] hover:text-neutral-300'
                      }`}
                      onClick={() => setSelectedVersion(v)}
                    >
                      {v}
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Details table */}
            <div className="px-6 pb-6">
              <div className="text-[10px] uppercase tracking-widest text-neutral-600 mb-2 font-semibold">Details</div>
              <div className="rounded-xl border border-[var(--border-subtle)] overflow-hidden divide-y divide-[var(--border-subtle)]">
                <DetailRow label="Source" value={selectedApp.flatpak_id ? 'Flathub' : selectedApp.type === 'web' ? 'Web Service' : 'Debian'} />
                <DetailRow label="Category" value={CATEGORY_LABELS[selectedApp.category] || selectedApp.category} />
                <DetailRow label="License" value={selectedApp.license || '—'} />
                <DetailRow label="Arch" value={selectedApp.arch?.length ? selectedApp.arch.join(', ') : 'All'} />
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
    <div className="flex justify-between items-center gap-3 px-4 py-2.5 bg-[var(--bg-base)]">
      <span className="text-[11px] text-neutral-600 font-medium flex-shrink-0">{label}</span>
      {link ? (
        <a
          href={value}
          target="_blank"
          rel="noopener noreferrer"
          className="text-[11px] text-[var(--accent)] hover:text-[var(--accent-hover)] truncate max-w-[170px] transition-colors"
        >
          {value.replace(/^https?:\/\/(www\.)?/, '')}
        </a>
      ) : (
        <span className="text-[11px] text-neutral-400 truncate max-w-[170px]">{value}</span>
      )}
    </div>
  )
}

import { useState, useEffect, useCallback, useRef } from 'react'

const ICON_COLORS = [
  'bg-blue-500', 'bg-emerald-500', 'bg-violet-500', 'bg-amber-500',
  'bg-rose-500', 'bg-cyan-500', 'bg-indigo-500', 'bg-teal-500',
  'bg-pink-500', 'bg-orange-500', 'bg-lime-500', 'bg-fuchsia-500',
]

function colorForName(name) {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) | 0
  return ICON_COLORS[Math.abs(h) % ICON_COLORS.length]
}

function PkgIcon({ name, size = 'md' }) {
  const sz = size === 'lg' ? 'w-10 h-10 text-base' : 'w-8 h-8 text-sm'
  return (
    <div className={`${sz} ${colorForName(name)} rounded-xl flex items-center justify-center font-bold text-white shrink-0 shadow-sm`}>
      {name[0]?.toUpperCase()}
    </div>
  )
}

function Toast({ msg, onDismiss }) {
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    requestAnimationFrame(() => setVisible(true))
    if (msg.type !== 'info') {
      const t = setTimeout(() => { setVisible(false); setTimeout(onDismiss, 300) }, 3500)
      return () => clearTimeout(t)
    }
  }, [msg, onDismiss])

  const colors = msg.type === 'ok'
    ? 'bg-emerald-500/15 border-emerald-500/30 text-emerald-400'
    : msg.type === 'err'
    ? 'bg-red-500/15 border-red-500/30 text-red-400'
    : 'bg-blue-500/15 border-blue-500/30 text-blue-400'

  const icon = msg.type === 'ok' ? (
    <svg className="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
    </svg>
  ) : msg.type === 'err' ? (
    <svg className="w-4 h-4 shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2.5}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
    </svg>
  ) : (
    <svg className="w-4 h-4 shrink-0 animate-spin" fill="none" viewBox="0 0 24 24">
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
    </svg>
  )

  return (
    <div className={`fixed bottom-5 right-5 z-50 flex items-center gap-2.5 px-4 py-3 rounded-xl border backdrop-blur-sm shadow-xl text-sm font-medium transition-all duration-300 ${colors} ${visible ? 'translate-y-0 opacity-100' : 'translate-y-4 opacity-0'}`}>
      {icon}
      {msg.text}
    </div>
  )
}

const SIDEBAR_ITEMS = [
  { id: 'installed', label: 'Installed', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
    </svg>
  )},
  { id: 'search', label: 'Find Packages', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
    </svg>
  )},
  { id: 'updates', label: 'Updates', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
    </svg>
  )},
  { id: 'repos', label: 'Repositories', icon: (
    <svg className="w-[18px] h-[18px]" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
      <path strokeLinecap="round" strokeLinejoin="round" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01" />
    </svg>
  )},
]

export default function Packages() {
  const [tab, setTab] = useState('installed')
  const [status, setStatus] = useState(null)
  const [installed, setInstalled] = useState(null)
  const [search, setSearch] = useState('')
  const [searchResults, setSearchResults] = useState(null)
  const [searching, setSearching] = useState(false)
  const [actionMsg, setActionMsg] = useState(null)
  const [updating, setUpdating] = useState(false)
  const [upgrading, setUpgrading] = useState(false)
  const [filter, setFilter] = useState('')
  const searchRef = useRef(null)

  const refreshStatus = () => fetch('/api/packages/status').then(r => r.json()).then(setStatus).catch(() => {})
  const refreshInstalled = () => fetch('/api/packages/installed').then(r => r.json()).then(setInstalled).catch(() => {})

  useEffect(() => { refreshStatus(); refreshInstalled() }, [])

  const doSearch = useCallback(async () => {
    if (!search.trim()) { setSearchResults(null); return }
    setSearching(true)
    try {
      const res = await fetch('/api/packages/search?q=' + encodeURIComponent(search.trim()))
      setSearchResults(await res.json())
    } catch { setSearchResults([]) }
    setSearching(false)
  }, [search])

  useEffect(() => {
    if (tab !== 'search') return
    const t = setTimeout(doSearch, 400)
    return () => clearTimeout(t)
  }, [search, tab, doSearch])

  useEffect(() => {
    if (tab === 'search') searchRef.current?.focus()
  }, [tab])

  const action = async (endpoint, body, msg) => {
    setActionMsg({ text: msg + '...', type: 'info' })
    try {
      const res = await fetch(endpoint, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: msg + ' complete', type: 'ok' })
      refreshStatus(); refreshInstalled()
      if (tab === 'search') doSearch()
    } catch (e) {
      setActionMsg({ text: `Failed: ${e.message}`, type: 'err' })
    }
  }

  const doUpdate = async () => {
    setUpdating(true)
    setActionMsg({ text: 'Updating package index...', type: 'info' })
    try {
      const res = await fetch('/api/packages/update', { method: 'POST' })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: 'Package index updated', type: 'ok' })
      refreshStatus()
    } catch (e) {
      setActionMsg({ text: `Update failed: ${e.message}`, type: 'err' })
    }
    setUpdating(false)
  }

  const doUpgrade = async () => {
    setUpgrading(true)
    setActionMsg({ text: 'Upgrading all packages...', type: 'info' })
    try {
      const res = await fetch('/api/packages/upgrade', { method: 'POST' })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: 'System packages upgraded', type: 'ok' })
      refreshStatus(); refreshInstalled()
    } catch (e) {
      setActionMsg({ text: `Upgrade failed: ${e.message}`, type: 'err' })
    }
    setUpgrading(false)
  }

  const filteredInstalled = installed?.filter(
    p => !filter || p.name.toLowerCase().includes(filter.toLowerCase()) ||
         (p.description || '').toLowerCase().includes(filter.toLowerCase())
  ) || []

  return (
    <div className="h-full flex bg-neutral-950 text-neutral-200 overflow-hidden">
      {/* Sidebar */}
      <div className="w-52 shrink-0 flex flex-col border-r border-neutral-800/60 bg-neutral-950/80">
        <div className="px-4 pt-5 pb-4">
          <h1 className="text-[15px] font-semibold tracking-tight">Software</h1>
          <p className="text-[11px] text-neutral-500 mt-0.5">Package Manager</p>
        </div>

        {/* Installed count card */}
        {status && (
          <div className="mx-3 mb-4 px-3 py-3 rounded-xl bg-neutral-900/60 border border-neutral-800/40">
            <div className="text-2xl font-bold text-white leading-none">{status.installed_count?.toLocaleString()}</div>
            <div className="text-[11px] text-neutral-500 mt-1">packages installed</div>
          </div>
        )}

        {/* Nav items */}
        <nav className="flex-1 px-2 space-y-0.5">
          {SIDEBAR_ITEMS.map(item => (
            <button key={item.id} onClick={() => setTab(item.id)}
              className={`w-full flex items-center gap-2.5 px-3 py-2 rounded-lg text-[13px] font-medium transition-colors ${
                tab === item.id
                  ? 'bg-neutral-800/80 text-white'
                  : 'text-neutral-500 hover:text-neutral-300 hover:bg-neutral-900/60'
              }`}>
              <span className={tab === item.id ? 'text-blue-400' : 'text-neutral-600'}>{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>

        {/* Sidebar bottom actions */}
        <div className="p-3 space-y-2 border-t border-neutral-800/40">
          <button onClick={doUpdate} disabled={updating}
            className="w-full flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium bg-neutral-800/70 hover:bg-neutral-700/70 rounded-lg transition-colors disabled:opacity-40">
            {updating ? (
              <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
              </svg>
            ) : (
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
              </svg>
            )}
            {updating ? 'Refreshing...' : 'Refresh Index'}
          </button>
          <button onClick={doUpgrade} disabled={upgrading}
            className="w-full flex items-center justify-center gap-2 px-3 py-2 text-xs font-medium bg-blue-600 hover:bg-blue-500 text-white rounded-lg transition-colors disabled:opacity-40 shadow-sm shadow-blue-600/20">
            {upgrading ? (
              <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
              </svg>
            ) : (
              <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M7 11l5-5m0 0l5 5m-5-5v12" />
              </svg>
            )}
            {upgrading ? 'Upgrading...' : 'Upgrade All'}
          </button>
        </div>
      </div>

      {/* Main content */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">

        {/* ===== INSTALLED TAB ===== */}
        {tab === 'installed' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-3">Installed Packages</h2>
              <div className="relative">
                <svg className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
                <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
                  placeholder="Filter installed packages..."
                  className="w-full pl-10 pr-4 py-2.5 bg-neutral-900/60 border border-neutral-800/50 rounded-xl text-sm text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-600 focus:bg-neutral-900/80 transition-colors" />
                {filter && (
                  <button onClick={() => setFilter('')} className="absolute right-3 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400">
                    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                )}
              </div>
              {installed && (
                <p className="text-[11px] text-neutral-600 mt-2">
                  {filter ? `${filteredInstalled.length} matches` : `${installed.length} packages`}
                  {filteredInstalled.length > 300 && ' — showing first 300, narrow your filter'}
                </p>
              )}
            </div>

            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {!installed && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-8 h-8 animate-spin mb-3 text-neutral-600" fill="none" viewBox="0 0 24 24">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                  </svg>
                  <span className="text-sm">Loading packages...</span>
                </div>
              )}

              {installed && filteredInstalled.length === 0 && filter && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                  </svg>
                  <span className="text-sm">No matches for "{filter}"</span>
                </div>
              )}

              {installed && filteredInstalled.length > 0 && (
                <div className="space-y-1">
                  {filteredInstalled.slice(0, 300).map(p => (
                    <div key={p.name}
                      className="group flex items-center gap-3 px-3.5 py-2.5 rounded-xl hover:bg-neutral-900/70 transition-colors">
                      <PkgIcon name={p.name} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-neutral-100 truncate">{p.name}</span>
                          <span className="shrink-0 text-[10px] px-1.5 py-0.5 rounded-md bg-neutral-800/80 text-neutral-500 font-mono">{p.version}</span>
                        </div>
                        {p.description && (
                          <p className="text-[12px] text-neutral-500 mt-0.5 truncate leading-snug">{p.description}</p>
                        )}
                      </div>
                      <button
                        onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                        className="shrink-0 opacity-0 group-hover:opacity-100 px-2.5 py-1 text-[11px] font-medium text-red-400 bg-red-500/10 hover:bg-red-500/20 rounded-lg transition-all">
                        Remove
                      </button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        {/* ===== SEARCH TAB ===== */}
        {tab === 'search' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-3">Find Packages</h2>
              <div className="relative">
                <svg className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-600" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                </svg>
                <input ref={searchRef} type="text" value={search} onChange={e => setSearch(e.target.value)}
                  placeholder="Search for packages to install..."
                  className="w-full pl-10 pr-4 py-2.5 bg-neutral-900/60 border border-neutral-800/50 rounded-xl text-sm text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-600 focus:bg-neutral-900/80 transition-colors" />
                {search && (
                  <button onClick={() => { setSearch(''); setSearchResults(null) }} className="absolute right-3 top-1/2 -translate-y-1/2 text-neutral-600 hover:text-neutral-400">
                    <svg className="w-4 h-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                      <path strokeLinecap="round" strokeLinejoin="round" d="M6 18L18 6M6 6l12 12" />
                    </svg>
                  </button>
                )}
              </div>
            </div>

            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {searching && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-8 h-8 animate-spin mb-3 text-neutral-600" fill="none" viewBox="0 0 24 24">
                    <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                    <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                  </svg>
                  <span className="text-sm">Searching...</span>
                </div>
              )}

              {!searching && !searchResults && !search && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-12 h-12 mb-3 text-neutral-800" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
                  </svg>
                  <span className="text-sm text-neutral-500">Search for packages to install</span>
                  <span className="text-xs text-neutral-700 mt-1">Results appear as you type</span>
                </div>
              )}

              {!searching && searchResults?.length === 0 && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M20 7l-8-4-8 4m16 0l-8 4m8-4v10l-8 4m0-10L4 7m8 4v10M4 7v10l8 4" />
                  </svg>
                  <span className="text-sm">No packages found</span>
                </div>
              )}

              {!searching && searchResults && searchResults.length > 0 && (
                <div className="space-y-1">
                  {searchResults.slice(0, 100).map(p => (
                    <div key={p.name + p.version}
                      className="group flex items-center gap-3 px-3.5 py-2.5 rounded-xl hover:bg-neutral-900/70 transition-colors">
                      <PkgIcon name={p.name} />
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium text-neutral-100 truncate">{p.name}</span>
                          <span className="shrink-0 text-[10px] px-1.5 py-0.5 rounded-md bg-neutral-800/80 text-neutral-500 font-mono">{p.version}</span>
                          {p.installed && (
                            <span className="shrink-0 text-[10px] px-1.5 py-0.5 rounded-md bg-emerald-500/15 text-emerald-400 font-medium">installed</span>
                          )}
                        </div>
                        {p.description && (
                          <p className="text-[12px] text-neutral-500 mt-0.5 truncate leading-snug">{p.description}</p>
                        )}
                      </div>
                      {p.installed ? (
                        <button
                          onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                          className="shrink-0 px-3 py-1.5 text-[11px] font-medium text-red-400 bg-red-500/10 hover:bg-red-500/20 rounded-lg transition-colors">
                          Remove
                        </button>
                      ) : (
                        <button
                          onClick={() => action('/api/packages/install', { name: p.name }, `Installing ${p.name}`)}
                          className="shrink-0 px-3 py-1.5 text-[11px] font-medium text-blue-400 bg-blue-500/10 hover:bg-blue-500/20 rounded-lg transition-colors">
                          Install
                        </button>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}

        {/* ===== UPDATES TAB ===== */}
        {tab === 'updates' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-1">Updates</h2>
              <p className="text-xs text-neutral-500">Keep your system up to date</p>
            </div>
            <div className="flex-1 overflow-y-auto px-5 pb-5">
              <div className="rounded-xl border border-neutral-800/50 bg-neutral-900/30 p-6 flex flex-col items-center text-center">
                <div className="w-14 h-14 rounded-2xl bg-blue-500/10 flex items-center justify-center mb-4">
                  <svg className="w-7 h-7 text-blue-400" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                  </svg>
                </div>
                <h3 className="text-sm font-medium text-neutral-200 mb-1">System Updates</h3>
                <p className="text-xs text-neutral-500 mb-5 max-w-xs">
                  Refresh the package index and upgrade all packages to their latest versions.
                </p>
                <div className="flex gap-3">
                  <button onClick={doUpdate} disabled={updating}
                    className="flex items-center gap-2 px-4 py-2 text-xs font-medium bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors disabled:opacity-40">
                    {updating ? (
                      <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                      </svg>
                    ) : (
                      <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
                      </svg>
                    )}
                    {updating ? 'Refreshing...' : 'Refresh Index'}
                  </button>
                  <button onClick={doUpgrade} disabled={upgrading}
                    className="flex items-center gap-2 px-4 py-2 text-xs font-medium bg-blue-600 hover:bg-blue-500 text-white rounded-lg transition-colors disabled:opacity-40 shadow-sm shadow-blue-600/20">
                    {upgrading ? (
                      <svg className="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24">
                        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
                        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.4 0 0 5.4 0 12h4z" />
                      </svg>
                    ) : (
                      <svg className="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                        <path strokeLinecap="round" strokeLinejoin="round" d="M7 11l5-5m0 0l5 5m-5-5v12" />
                      </svg>
                    )}
                    {upgrading ? 'Upgrading...' : 'Upgrade All'}
                  </button>
                </div>
              </div>
            </div>
          </>
        )}

        {/* ===== REPOS TAB ===== */}
        {tab === 'repos' && (
          <>
            <div className="shrink-0 px-5 pt-5 pb-4">
              <h2 className="text-lg font-semibold mb-1">Repositories</h2>
              <p className="text-xs text-neutral-500">Configured package sources</p>
            </div>
            <div className="flex-1 overflow-y-auto px-5 pb-5">
              {!status?.repos?.length && (
                <div className="flex flex-col items-center justify-center py-16 text-neutral-500">
                  <svg className="w-10 h-10 mb-3 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.5}>
                    <path strokeLinecap="round" strokeLinejoin="round" d="M5 12h14M5 12a2 2 0 01-2-2V6a2 2 0 012-2h14a2 2 0 012 2v4a2 2 0 01-2 2M5 12a2 2 0 00-2 2v4a2 2 0 002 2h14a2 2 0 002-2v-4a2 2 0 00-2-2m-2-4h.01M17 16h.01" />
                  </svg>
                  <span className="text-sm">No repositories configured</span>
                </div>
              )}
              {status?.repos && (
                <div className="space-y-2">
                  {status.repos.map((r, i) => (
                    <div key={i}
                      className="flex items-center gap-3 px-4 py-3.5 rounded-xl bg-neutral-900/40 border border-neutral-800/40 hover:border-neutral-700/40 transition-colors">
                      <div className={`w-2.5 h-2.5 rounded-full shrink-0 ${r.enabled ? 'bg-emerald-400 shadow-sm shadow-emerald-400/30' : 'bg-neutral-600'}`} />
                      <div className="min-w-0 flex-1">
                        <span className="text-sm font-mono text-neutral-300 truncate block">{r.url}</span>
                      </div>
                      <span className={`shrink-0 text-[10px] px-2 py-1 rounded-lg font-medium ${
                        r.enabled
                          ? 'bg-emerald-500/10 text-emerald-400'
                          : 'bg-neutral-800 text-neutral-500'
                      }`}>
                        {r.enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </>
        )}
      </div>

      {/* Toast notification */}
      {actionMsg && <Toast msg={actionMsg} onDismiss={() => setActionMsg(null)} />}
    </div>
  )
}

import { useState, useEffect, useCallback } from 'react'

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

  const action = async (endpoint, body, msg) => {
    setActionMsg({ text: msg + '...', type: 'info' })
    try {
      const res = await fetch(endpoint, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: msg + ' — done', type: 'ok' })
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
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-200">
      {/* Header */}
      <div className="shrink-0 border-b border-neutral-800/50 px-5 pt-4 pb-3">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h1 className="text-base font-semibold">Packages</h1>
            <p className="text-xs text-neutral-500 mt-0.5">
              Alpine Linux package manager (apk)
              {status && <span className="ml-2 text-neutral-600">
                — {status.installed_count} installed
              </span>}
            </p>
          </div>
          <div className="flex gap-2">
            <button onClick={doUpdate} disabled={updating}
              className="px-3 py-1.5 text-xs bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors disabled:opacity-40">
              {updating ? 'Updating...' : 'Update Index'}
            </button>
            <button onClick={doUpgrade} disabled={upgrading}
              className="px-3 py-1.5 text-xs bg-blue-600/20 text-blue-400 hover:bg-blue-600/30 border border-blue-500/30 rounded-lg transition-colors disabled:opacity-40">
              {upgrading ? 'Upgrading...' : 'Upgrade All'}
            </button>
          </div>
        </div>

        {/* Tabs */}
        <div className="flex gap-1">
          {[
            { id: 'installed', label: 'Installed' },
            { id: 'search', label: 'Find Packages' },
            { id: 'repos', label: 'Repositories' },
          ].map(t => (
            <button key={t.id} onClick={() => setTab(t.id)}
              className={`px-3 py-1.5 text-xs rounded-lg transition-colors ${
                tab === t.id ? 'bg-neutral-800 text-white' : 'text-neutral-500 hover:text-neutral-300'}`}>
              {t.label}
            </button>
          ))}
        </div>
      </div>

      {/* Action message */}
      {actionMsg && (
        <div className={`mx-5 mt-3 px-3 py-2 rounded-lg text-xs ${
          actionMsg.type === 'ok' ? 'bg-green-900/30 text-green-400' :
          actionMsg.type === 'err' ? 'bg-red-900/30 text-red-400' :
          'bg-blue-900/30 text-blue-400'}`}>
          {actionMsg.text}
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-5">

        {/* Installed */}
        {tab === 'installed' && (
          <div>
            <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
              placeholder="Filter installed packages..."
              className="w-full mb-4 px-3 py-2 bg-neutral-900/60 border border-neutral-800/50 rounded-lg text-sm text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-700" />

            {!installed && <div className="text-sm text-neutral-500 py-8 text-center">Loading...</div>}

            {installed && (
              <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                {filteredInstalled.slice(0, 300).map(p => (
                  <div key={p.name} className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
                    <div className="min-w-0 flex-1">
                      <span className="text-sm font-mono">{p.name}</span>
                      <span className="text-[11px] text-neutral-600 ml-2">{p.version}</span>
                      {p.description && <p className="text-[11px] text-neutral-600 mt-0.5 truncate">{p.description}</p>}
                    </div>
                    <button onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                      className="shrink-0 text-[11px] text-red-400/60 hover:text-red-400 ml-3">
                      Remove
                    </button>
                  </div>
                ))}
              </div>
            )}

            {installed && filteredInstalled.length > 300 && (
              <p className="text-xs text-neutral-600 mt-2 text-center">
                Showing 300 of {filteredInstalled.length} — use filter to narrow
              </p>
            )}
            {installed && filteredInstalled.length === 0 && filter && (
              <p className="text-sm text-neutral-500 py-8 text-center">No matches</p>
            )}
          </div>
        )}

        {/* Search */}
        {tab === 'search' && (
          <div>
            <input type="text" value={search} onChange={e => setSearch(e.target.value)}
              placeholder="Search Alpine packages..."
              autoFocus
              className="w-full mb-4 px-3 py-2 bg-neutral-900/60 border border-neutral-800/50 rounded-lg text-sm text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-700" />

            {searching && <div className="text-sm text-neutral-500 py-8 text-center">Searching...</div>}

            {!searching && searchResults && (
              <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                {searchResults.slice(0, 100).map(p => (
                  <div key={p.name + p.version} className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-mono">{p.name}</span>
                        <span className="text-[11px] text-neutral-600">{p.version}</span>
                        {p.installed && (
                          <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-green-900/30 text-green-400">installed</span>
                        )}
                      </div>
                      {p.description && <p className="text-[11px] text-neutral-600 mt-0.5 truncate">{p.description}</p>}
                    </div>
                    {p.installed ? (
                      <button onClick={() => action('/api/packages/remove', { name: p.name }, `Removing ${p.name}`)}
                        className="shrink-0 text-[11px] text-red-400/60 hover:text-red-400 ml-3">
                        Remove
                      </button>
                    ) : (
                      <button onClick={() => action('/api/packages/install', { name: p.name }, `Installing ${p.name}`)}
                        className="shrink-0 text-[11px] text-blue-400 hover:text-blue-300 ml-3">
                        Install
                      </button>
                    )}
                  </div>
                ))}
              </div>
            )}

            {!searching && searchResults?.length === 0 && (
              <p className="text-sm text-neutral-500 py-8 text-center">No packages found</p>
            )}
            {!searching && !searchResults && !search && (
              <p className="text-sm text-neutral-500 py-8 text-center">Type to search Alpine repositories</p>
            )}
          </div>
        )}

        {/* Repos */}
        {tab === 'repos' && (
          <div>
            {!status?.repos?.length && (
              <p className="text-sm text-neutral-500 py-8 text-center">No repositories configured</p>
            )}
            {status?.repos && (
              <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                {status.repos.map((r, i) => (
                  <div key={i} className="flex items-center justify-between px-4 py-3 bg-neutral-900/40">
                    <span className="text-sm font-mono truncate">{r.url}</span>
                    <span className={`text-[10px] px-2 py-0.5 rounded-full ${
                      r.enabled ? 'bg-green-900/30 text-green-400' : 'bg-neutral-800 text-neutral-500'}`}>
                      {r.enabled ? 'enabled' : 'disabled'}
                    </span>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
